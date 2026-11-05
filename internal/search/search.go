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
	"strconv"
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
	// Coverage is the fraction of (non-stopword) query words found in the unit
	// by full-text search, 0..1.
	Coverage float64
	// SemanticOn is true when the query embedding succeeded, so Semantic is
	// meaningful (0 then means "below the floor", not "unknown").
	SemanticOn bool
}

// Explain renders the score breakdown.
func (r Result) Explain() string {
	return fmt.Sprintf("total=%.2f keyword=%d community=%d(+%.2f) semantic=%.3f coverage=%.2f", r.Total, r.Keyword,
		r.Community, math.Log(1+float64(r.Community)), r.Semantic, r.Coverage)
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
		rows, err = s.DB.QueryContext(ctx, `SELECT unit_id, SUM(n) FROM unit_communities
		  WHERE community_id IN (SELECT DISTINCT community_id FROM communities WHERE concept_id IN (`+ids+`))
		  GROUP BY unit_id`)
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
	semOn := false
	if s.Embedder != nil && strings.TrimSpace(query) != "" {
		// Embed the query the way the active pipeline's documents were
		// embedded (mid-migration that may be the previous prompt recipe).
		qtext := embed.Query(s.Embedder, query)
		if s.DB.GetMeta("pipeline_hash") != index.PipelineHash(s.Embedder.Model()) {
			qtext = s.DB.GetMeta("pipeline_query_prefix") + query
		}
		vecs, err := s.Embedder.Embed(ctx, []string{qtext})
		if err == nil && len(vecs) == 1 {
			semOn = true
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

	// Load the kept units and apply filters. Identical text from two sources
	// (a jcode memory and its imported copy) is one result.
	results := make([]Result, 0, len(keep))
	seenText := map[string]bool{}
	seenSID := map[string]bool{}
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
		if !u.Active || !s.DB.IsCanonical(u) || !allowed(u.Scope, o.Scopes) || !allowed(u.Kind, o.Kinds) || seenSID[u.SID] || seenText[u.ContentHash] {
			continue
		}
		seenText[u.ContentHash] = true
		seenSID[u.SID] = true
		r := Result{Unit: u, Keyword: a.kw, Community: a.comm, Semantic: sem[id], Total: c.total, SemanticOn: semOn}
		if len(words) > 0 {
			r.Coverage = math.Min(1, float64(a.words)/float64(len(words)))
		}
		results = append(results, r)
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
	// Candidates: active units in scope with a vector, read from the narrow
	// sketches table (scope and kind filters apply before any vector work).
	qs := embed.Sketch(q)
	type hd struct {
		id int64
		d  int
	}
	var all []hd
	where, args := scopeSQL("u.scope", o.Scopes)
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, u.scope, u.kind, s.sketch FROM units u JOIN sketches s
	  ON s.content_hash=u.content_hash AND s.pipeline_hash=? WHERE u.active=1 AND (`+where+`)`, append([]any{pipeline}, args...)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var scope, kind string
		var sk []byte
		if err := rows.Scan(&id, &scope, &kind, &sk); err != nil {
			rows.Close()
			return err
		}
		if !allowed(scope, o.Scopes) || !allowed(kind, o.Kinds) {
			continue
		}
		all = append(all, hd{id, embed.Hamming(qs, sk)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Measured on 5000 random 768-d vectors: keep 256 -> recall@5 0.83,
	// 512 -> 0.91, 1024 -> 0.98. 1024 rescored dot products are cheap.
	if keep := max(1024, 32*o.Limit); len(all) > PrefilterAbove && len(all) > keep {
		sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
		all = all[:keep]
	}
	// Exact rescore: fetch float32 vectors for the candidates only.
	const batch = 500
	for start := 0; start < len(all); start += batch {
		ids := make([]string, 0, batch)
		for _, c := range all[start:min(start+batch, len(all))] {
			ids = append(ids, strconv.FormatInt(c.id, 10))
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT u.id, e.vec FROM units u JOIN embeddings e
		  ON e.content_hash=u.content_hash AND e.pipeline_hash=? WHERE u.id IN (`+strings.Join(ids, ",")+`)`, pipeline)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return err
			}
			if sim := float64(embed.Dot(q, embed.DecodeF32(blob))); sim > o.SemanticFloor {
				out[id] = sim
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Related returns a unit's stored nearest neighbours.
func (s *Searcher) Related(ctx context.Context, unitID int64, limit int) ([]Result, error) {
	return s.RelatedIn(ctx, unitID, limit, nil)
}

// RelatedIn is Related restricted to scopes (nil = all). A unit outside the
// scopes yields nothing, so an id cannot be used to probe another client's
// memories.
func (s *Searcher) RelatedIn(ctx context.Context, unitID int64, limit int, scopes []string) ([]Result, error) {
	if len(scopes) > 0 {
		u, err := s.DB.GetUnit(unitID)
		if err != nil || !allowed(u.Scope, scopes) {
			return nil, nil
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT nbr_id, sim FROM neighbors WHERE unit_id=? ORDER BY sim DESC`, unitID)
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
	seenSID := map[string]bool{}
	for _, x := range list {
		u, err := s.DB.GetUnit(x.id)
		if err != nil || !u.Active || !s.DB.IsCanonical(u) || seenSID[u.SID] || !allowed(u.Scope, scopes) {
			continue
		}
		seenSID[u.SID] = true
		out = append(out, Result{Unit: u, Semantic: x.sim, Total: x.sim})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// scopeSQL turns a scope set into an index-friendly predicate: exact entries
// become equality, "prefix*" entries become a half-open range. allowed()
// remains the authority; this only narrows what SQLite reads.
func scopeSQL(col string, set []string) (string, []any) {
	if len(set) == 0 {
		return "1", nil
	}
	var parts []string
	var args []any
	for _, s := range set {
		if p, ok := strings.CutSuffix(s, "*"); ok {
			if p == "" {
				return "1", nil
			}
			parts = append(parts, "("+col+" >= ? AND "+col+" < ?)")
			args = append(args, p, p+"\U0010FFFF")
		} else {
			parts = append(parts, col+" = ?")
			args = append(args, s)
		}
	}
	return strings.Join(parts, " OR "), args
}

// allowed reports whether v is permitted by set. An entry ending in "*" is a
// prefix match ("jcode@brovs*" admits "jcode@brovs" and "jcode@brovs:abc").
func allowed(v string, set []string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v || (strings.HasSuffix(s, "*") && strings.HasPrefix(v, s[:len(s)-1])) {
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
