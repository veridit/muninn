// Package search ranks units for a query. The algorithm is seid search's:
//
//  1. concepts matching query words (exact for <=5 chars, substring otherwise) and bigrams
//  2. communities of those concepts
//  4. direct concept hits score 2 per concept; other concepts of the same
//     communities score 1 (community expansion)
//  5. FTS5: title match +2, content match +1 per query word;
//     5b conjunction bonus +5*n when all n>=2 words match; bigram phrase +3
//     5c semantic cosine (only sims > floor count), weight 10
//  6. total = keyword + log(1+community) + semantic*weight; reserve the first
//     slots for the strongest semantic matches, then fill by total.
//
// muninn adds: a 1-bit Hamming prefilter before exact cosine when the corpus
// is large, a minimum total score for automatic recall, and --explain output.
package search

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/store"
)

// Options tunes a search.
type Options struct {
	Limit          int
	MinScore       float64 // drop results below this total (0 = keep all)
	SemanticWeight float64
	SemanticFloor  float64
	SemanticSlots  int
	Scopes         []string // empty = all
	Kinds          []string // empty = all
}

// DefaultOptions mirrors seid search.
func DefaultOptions() Options {
	return Options{Limit: 10, SemanticWeight: 10, SemanticFloor: 0.3, SemanticSlots: 3}
}

// Result is one ranked unit with its per-signal scores.
type Result struct {
	Unit      store.Unit
	Keyword   int
	Community int
	Semantic  float64 // raw cosine
	Total     float64
}

// Explain renders the score breakdown.
func (r Result) Explain() string {
	return fmt.Sprintf("total=%.2f keyword=%d community=%d(+%.2f) semantic=%.3f", r.Total, r.Keyword,
		r.Community, math.Log(1+float64(r.Community)), r.Semantic)
}

// Searcher runs queries against the store.
type Searcher struct {
	DB       *store.DB
	Embedder embed.Embedder // nil = keyword-only
}

type acc struct{ kw, comm, words int }

// QueryWords tokenises a query, dropping stopwords and 1-char tokens.
func QueryWords(q string) []string {
	var out []string
	for _, w := range index.Words(q) {
		if len(w) > 1 && !index.IsStopword(w) {
			out = append(out, w)
		}
	}
	return out
}

func bigrams(ws []string) []string {
	var out []string
	for i := 0; i+1 < len(ws); i++ {
		out = append(out, ws[i]+" "+ws[i+1])
	}
	return out
}

// fts5Quote makes a phrase literal so operator characters cannot break MATCH.
func fts5Quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// PrefilterAbove is the corpus size above which sketches prefilter cosine.
const PrefilterAbove = 4096

// Search ranks units for query.
func (s *Searcher) Search(ctx context.Context, query string, o Options) ([]Result, error) {
	if o.Limit <= 0 {
		o.Limit = 10
	}
	words := QueryWords(query)
	scores := map[int64]*acc{}
	get := func(id int64) *acc {
		a := scores[id]
		if a == nil {
			a = &acc{}
			scores[id] = a
		}
		return a
	}

	// 1. matching concepts.
	concepts := map[int]bool{}
	addConcepts := func(q string, args ...any) error {
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			rows.Scan(&id)
			concepts[id] = true
		}
		return rows.Err()
	}
	for _, w := range words {
		var err error
		if len(w) <= 5 {
			err = addConcepts(`SELECT id FROM concepts WHERE term=?`, w)
		} else {
			err = addConcepts(`SELECT id FROM concepts WHERE term LIKE ?`, "%"+w+"%")
		}
		if err != nil {
			return nil, err
		}
	}
	for _, b := range bigrams(words) {
		if err := addConcepts(`SELECT id FROM concepts WHERE term=?`, b); err != nil {
			return nil, err
		}
	}
	if len(concepts) > 0 {
		ids := intList(concepts)
		// 4. direct hits: 2 points per matching concept in a unit.
		rows, err := s.DB.QueryContext(ctx, `SELECT unit_id, COUNT(*) FROM unit_concepts WHERE concept_id IN (`+ids+`) GROUP BY unit_id`)
		if err != nil {
			return nil, err
		}
		direct := map[int64]int{}
		for rows.Next() {
			var u int64
			var n int
			rows.Scan(&u, &n)
			get(u).kw += 2 * n
			direct[u] = n
		}
		rows.Close()
		// 2+4. community expansion: 1 point per other concept of the query communities.
		rows, err = s.DB.QueryContext(ctx, `SELECT uc.unit_id, COUNT(*) FROM unit_concepts uc
		  JOIN communities c ON c.concept_id = uc.concept_id
		  WHERE c.community_id IN (SELECT DISTINCT community_id FROM communities WHERE concept_id IN (`+ids+`))
		  GROUP BY uc.unit_id`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u int64
			var n int
			rows.Scan(&u, &n)
			if c := n - direct[u]; c > 0 {
				get(u).comm += c
			}
		}
		rows.Close()
	}

	// 5. FTS5 per word: title +2, content (body or tags) +1.
	match := func(col, term string, f func(int64)) error {
		rows, err := s.DB.QueryContext(ctx, `SELECT rowid FROM units_fts WHERE `+col+` MATCH ?`, fts5Quote(term))
		if err != nil {
			return nil // a malformed term never fails the whole search
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			f(id)
		}
		return rows.Err()
	}
	for _, w := range words {
		hit := map[int64]bool{}
		if err := match("title", w, func(id int64) { get(id).kw += 2; hit[id] = true }); err != nil {
			return nil, err
		}
		if err := match("units_fts", w, func(id int64) {
			if !hit[id] {
				get(id).kw++
			}
			hit[id] = true
		}); err != nil {
			return nil, err
		}
		for id := range hit {
			get(id).words++
		}
	}
	// 5b. conjunction bonus and bigram phrases.
	if n := len(words); n >= 2 {
		for _, a := range scores {
			if a.words >= n {
				a.kw += 5 * n
			}
		}
		for _, b := range bigrams(words) {
			if err := match("units_fts", b, func(id int64) { get(id).kw += 3 }); err != nil {
				return nil, err
			}
		}
	}

	// 5c. semantic similarity.
	sem := map[int64]float64{}
	if s.Embedder != nil && strings.TrimSpace(query) != "" {
		vecs, err := s.Embedder.Embed(ctx, []string{query})
		if err == nil && len(vecs) == 1 {
			if err := s.semantic(ctx, vecs[0], o, sem); err != nil {
				return nil, err
			}
			for id := range sem {
				get(id)
			}
		}
	}

	// Score candidates without touching rows, keep the best few (by total and
	// by semantic, for the reserved slots), and only then load those units.
	// Loading every candidate one query at a time dominated search time.
	type cand struct {
		id    int64
		a     *acc
		total float64
	}
	cands := make([]cand, 0, len(scores))
	for id, a := range scores {
		t := float64(a.kw) + math.Log(1+float64(a.comm)) + sem[id]*o.SemanticWeight
		if t > 0.001 && t >= o.MinScore {
			cands = append(cands, cand{id, a, t})
		}
	}
	keep := map[int64]bool{}
	pool := 4*o.Limit + 4*o.SemanticSlots + 16 // headroom for scope/kind/inactive filtering
	sort.Slice(cands, func(i, j int) bool { return cands[i].total > cands[j].total })
	for i := 0; i < len(cands) && i < pool; i++ {
		keep[cands[i].id] = true
	}
	bySem := append([]cand(nil), cands...)
	sort.Slice(bySem, func(i, j int) bool { return sem[bySem[i].id] > sem[bySem[j].id] })
	for i := 0; i < len(bySem) && i < pool; i++ {
		keep[bySem[i].id] = true
	}

	// Load the kept units and apply filters.
	results := make([]Result, 0, len(keep))
	for _, c := range cands {
		if !keep[c.id] {
			continue
		}
		id, a := c.id, c.a
		u, err := s.DB.GetUnit(id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !u.Active || !allowed(u.Scope, o.Scopes) || !allowed(u.Kind, o.Kinds) {
			continue
		}
		results = append(results, Result{Unit: u, Keyword: a.kw, Community: a.comm, Semantic: sem[id], Total: c.total})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Total != results[j].Total {
			return results[i].Total > results[j].Total
		}
		return results[i].Unit.ID < results[j].Unit.ID
	})

	// 6. reserve semantic slots, then fill by total; never pad.
	byS := append([]Result(nil), results...)
	sort.SliceStable(byS, func(i, j int) bool { return byS[i].Semantic > byS[j].Semantic })
	chosen := map[int64]bool{}
	var top []Result
	for _, r := range byS {
		if len(top) >= o.SemanticSlots || r.Semantic <= 0 {
			break
		}
		if r.Total >= o.MinScore {
			chosen[r.Unit.ID] = true
			top = append(top, r)
		}
	}
	for _, r := range results {
		if len(top) >= o.Limit {
			break
		}
		if !chosen[r.Unit.ID] && r.Total >= o.MinScore {
			chosen[r.Unit.ID] = true
			top = append(top, r)
		}
	}
	sort.SliceStable(top, func(i, j int) bool { return top[i].Total > top[j].Total })
	if len(top) > o.Limit {
		top = top[:o.Limit]
	}
	return top, nil
}

// semantic scores every embedded active unit by cosine; above PrefilterAbove
// vectors it first keeps the closest candidates by sketch Hamming distance.
func (s *Searcher) semantic(ctx context.Context, q []float32, o Options, out map[int64]float64) error {
	pipeline := s.DB.GetMeta("pipeline_hash")
	if pipeline == "" {
		pipeline = index.PipelineHash(s.Embedder.Model())
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings WHERE pipeline_hash=?`, pipeline).Scan(&n)
	allow := map[int64]bool(nil)
	if n > PrefilterAbove {
		allow = map[int64]bool{}
		qs := embed.Sketch(q)
		type hd struct {
			id int64
			d  int
		}
		var all []hd
		rows, err := s.DB.QueryContext(ctx, `SELECT u.id, e.sketch FROM units u JOIN embeddings e
		  ON e.content_hash=u.content_hash AND e.pipeline_hash=? WHERE u.active=1`, pipeline)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var sk []byte
			rows.Scan(&id, &sk)
			all = append(all, hd{id, embed.Hamming(qs, sk)})
		}
		rows.Close()
		sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
		// Measured on 5000 random 768-d vectors: keep 256 -> recall@5 0.83,
		// 512 -> 0.91, 1024 -> 0.98. 1024 rescored dot products are cheap.
		keep := max(1024, 32*o.Limit)
		for i := 0; i < len(all) && i < keep; i++ {
			allow[all[i].id] = true
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, e.vec FROM units u JOIN embeddings e
	  ON e.content_hash=u.content_hash AND e.pipeline_hash=? WHERE u.active=1`, pipeline)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return err
		}
		if allow != nil && !allow[id] {
			continue
		}
		if sim := float64(embed.Dot(q, embed.DecodeF32(blob))); sim > o.SemanticFloor {
			out[id] = sim
		}
	}
	return rows.Err()
}

// Related returns a unit's stored nearest neighbours.
func (s *Searcher) Related(ctx context.Context, unitID int64, limit int) ([]Result, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT nbr_id, sim FROM neighbors WHERE unit_id=? ORDER BY sim DESC LIMIT ?`, unitID, limit)
	if err != nil {
		return nil, err
	}
	type ns struct {
		id  int64
		sim float64
	}
	var list []ns
	for rows.Next() {
		var x ns
		rows.Scan(&x.id, &x.sim)
		list = append(list, x)
	}
	rows.Close()
	var out []Result
	for _, x := range list {
		u, err := s.DB.GetUnit(x.id)
		if err != nil || !u.Active {
			continue
		}
		out = append(out, Result{Unit: u, Semantic: x.sim, Total: x.sim})
	}
	return out, nil
}

func allowed(v string, set []string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func intList(m map[int]bool) string {
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}
