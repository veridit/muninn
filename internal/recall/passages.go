package recall

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

const PassageBytes = 600
const MaxPassages = 8
const RecallBudget = 1500 * time.Millisecond
const BoilerplateSessions = 5

// Passages preserves short queries exactly, groups list blocks and paragraphs,
// and cuts oversized blocks at whitespace without splitting UTF-8.
func Passages(message string) []string {
	message = strings.TrimSpace(message)
	if len(message) <= PassageBytes {
		return []string{message}
	}
	var blocks []string
	var block strings.Builder
	flush := func() {
		if s := strings.TrimSpace(block.String()); s != "" {
			blocks = append(blocks, s)
		}
		block.Reset()
	}
	for _, line := range strings.Split(message, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			flush()
			continue
		}
		if strings.HasPrefix(s, "#") {
			flush()
		}
		if block.Len() > 0 {
			block.WriteByte('\n')
		}
		block.WriteString(line)
	}
	flush()
	var out []string
	for _, b := range blocks {
		for len(b) > PassageBytes {
			cut := len(Truncate(b, PassageBytes))
			if i := strings.LastIndexAny(b[:cut], " \n\t"); i >= PassageBytes/2 {
				cut = i
			}
			out = append(out, strings.TrimSpace(b[:cut]))
			b = strings.TrimSpace(b[cut:])
		}
		if b != "" {
			if len(out) > 0 && len(b) < 100 && len(out[len(out)-1])+len(b)+1 <= PassageBytes {
				out[len(out)-1] += "\n" + b
			} else {
				out = append(out, b)
			}
		}
	}
	return out
}

// identifierTerms recognises structure before lowercasing. Index tokenisation
// retains TASK-123 and sids, and splits paths into searchable components.
func identifierTerms(text string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range strings.Fields(text) {
		raw = strings.Trim(raw, "`\"'()[]{},:;")
		special := strings.ContainsAny(raw, "/_")
		upper, lower, digit, camel := false, false, false, false
		previousLower := false
		for _, r := range raw {
			camel = camel || (previousLower && unicode.IsUpper(r))
			previousLower = unicode.IsLower(r)
			upper = upper || unicode.IsUpper(r)
			lower = lower || unicode.IsLower(r)
			digit = digit || unicode.IsDigit(r)
		}
		if special || camel || (upper && !lower && len(raw) > 1) || digit {
			if strings.Contains(raw, "/") {
				out[strings.ToLower(strings.TrimRight(raw, "."))] = true
				continue
			}
			for _, w := range index.Words(raw) {
				if len(w) > 1 {
					out[w] = true
				}
			}
		}
	}
	return out
}

type passage struct {
	text    string
	ordinal int
	terms   []string
	mass    float64
}

// rarePassages computes document frequency only over visible active units.
// Concept membership is indexed, and no foreign binding affects rarity.
func (e *Engine) rarePassages(ctx context.Context, texts []string, o search.Options) ([]passage, error) {
	visible := map[int64]bool{}
	rows, err := e.DB.QueryContext(ctx, `SELECT id,source_id,scope FROM units WHERE active=1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u store.Unit
		if err = rows.Scan(&u.ID, &u.SourceID, &u.Scope); err != nil {
			break
		}
		if o.ScopeFor != nil {
			var ok bool
			u.Scope, ok = o.ScopeFor(u)
			if !ok {
				continue
			}
		}
		if search.Allowed(u.Scope, o.Scopes) {
			visible[u.ID] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	terms := map[string]bool{}
	for _, p := range texts {
		for _, w := range search.QueryWords(p) {
			terms[w] = true
			if len(terms) >= 1024 {
				break
			}
		}
		if len(terms) >= 1024 {
			break
		}
	}
	keys := make([]string, 0, len(terms))
	for w := range terms {
		keys = append(keys, w)
	}
	sort.Strings(keys)
	df := map[string]int{}
	for start := 0; start < len(keys); start += 400 {
		end := min(start+400, len(keys))
		args := make([]any, 0, end-start)
		marks := make([]string, 0, end-start)
		for _, w := range keys[start:end] {
			args = append(args, w)
			marks = append(marks, "?")
		}
		rows, err = e.DB.QueryContext(ctx, `SELECT c.term,uc.unit_id FROM concepts c JOIN unit_concepts uc ON uc.concept_id=c.id WHERE c.term IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var w string
			var id int64
			if err = rows.Scan(&w, &id); err != nil {
				break
			}
			if visible[id] {
				df[w]++
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	// Concepts deliberately omit <=3-character words. FTS supplies their
	// actual document frequency rather than treating them as absent.
	for _, w := range keys {
		if len(w) > 3 {
			continue
		}
		rows, err = e.DB.QueryContext(ctx, `SELECT rowid FROM units_fts WHERE units_fts MATCH ?`, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			if visible[id] {
				df[w]++
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var out []passage
	for i, text := range texts {
		p := passage{text: text, ordinal: i + 1, terms: []string{}}
		ids := identifierTerms(text)
		seen := map[string]bool{}
		candidates := search.QueryWords(text)
		var identifiers []string
		for w := range ids {
			identifiers = append(identifiers, w)
		}
		sort.Strings(identifiers)
		candidates = append(identifiers, candidates...)
		for _, w := range candidates {
			if seen[w] {
				continue
			}
			seen[w] = true
			if ids[w] || (df[w] > 0 && float64(df[w]) <= math.Max(1, float64(len(visible))*.005)) {
				p.terms = append(p.terms, w)
				p.mass += math.Log(1 + float64(len(visible))/float64(1+df[w]))
			}
		}
		// Bound keyword work even for identifier-heavy machine output.
		if len(p.terms) > 24 {
			p.terms = p.terms[:24]
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mass > out[j].mass })
	if len(out) > MaxPassages {
		out = out[:MaxPassages]
	}
	return out, nil
}

func normalFingerprint(text string) string {
	var b strings.Builder
	for _, w := range index.Words(text) {
		onlyDigits := true
		for _, r := range w {
			onlyDigits = onlyDigits && unicode.IsDigit(r)
		}
		if onlyDigits {
			b.WriteString("# ")
			continue
		}
		for _, r := range w {
			b.WriteRune(r)
		}
		b.WriteByte(' ')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

// Boilerplate stores only binding-salted hashes and hashed session identities.
// A session counts once per fingerprint. No anonymous calls train the model.
// The bounded table uses LRU eviction, not an unbounded query log.
func (e *Engine) boilerplate(ctx context.Context, r Request, texts []string) []string {
	if r.LearningScope == "" || r.Session == "" {
		return texts
	}
	scopes := append([]string(nil), r.Scopes...)
	sort.Strings(scopes)
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(r.LearningScope+"\x00"+strings.Join(scopes, "\x00"))))
	session := fmt.Sprintf("%x", sha256.Sum256([]byte(r.Session)))
	var out []string
	for _, text := range texts {
		if ctx.Err() != nil {
			break
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(scope+"\x00"+normalFingerprint(text))))
		var count int
		e.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM recall_passages WHERE binding=? AND fingerprint=?`, scope, hash).Scan(&count)
		if count < BoilerplateSessions {
			out = append(out, text)
		}
		e.DB.ExecContext(ctx, `INSERT INTO recall_passages(binding,fingerprint,session,at_ns) VALUES(?,?,?,?) ON CONFLICT(binding,fingerprint,session) DO UPDATE SET at_ns=excluded.at_ns`, scope, hash, session, time.Now().UnixNano())
	}
	e.DB.ExecContext(ctx, `DELETE FROM recall_passages WHERE rowid IN (SELECT rowid FROM recall_passages ORDER BY at_ns DESC LIMIT -1 OFFSET 4096)`)
	return out
}

// redundant uses containment of five-word shingles, so quoting most of a
// memory anywhere in the message suppresses it, even across passage cuts.
func redundant(body, message string) bool {
	a, b := index.Words(body), index.Words(message)
	if len(a) < 5 {
		return false
	}
	shingles := map[string]bool{}
	for i := 0; i+5 <= len(b); i++ {
		shingles[strings.Join(b[i:i+5], " ")] = true
	}
	hits, total := 0, 0
	for i := 0; i+5 <= len(a); i++ {
		total++
		if shingles[strings.Join(a[i:i+5], " ")] {
			hits++
		}
	}
	return total > 0 && float64(hits)/float64(total) >= .8
}

func rareMatch(r search.Result, terms []string) string {
	text := strings.ToLower(r.Unit.Title + " " + r.Unit.Body + " " + strings.Join(r.Unit.Tags, " "))
	words := map[string]bool{}
	for _, w := range index.Words(r.Unit.Title + " " + r.Unit.Body + " " + strings.Join(r.Unit.Tags, " ")) {
		words[w] = true
	}
	for _, w := range terms {
		if words[w] || (strings.Contains(w, "/") && strings.Contains(text, w)) {
			return w
		}
	}
	return ""
}

func justified(r search.Result, terms []string, gate float64) (string, bool) {
	term := rareMatch(r, terms)
	return term, term != "" || (r.SemanticOn && r.Semantic >= gate)
}

func (e *Engine) passageSearch(ctx context.Context, r Request, o search.Options, msg string, searcher *search.Searcher) ([]search.Result, map[int64]string, error) {
	texts := e.boilerplate(ctx, r, Passages(msg))
	why := map[int64]string{}
	if len(texts) == 0 {
		return nil, why, nil
	}
	// A single short message is the old query and gate, byte for byte.
	if len(texts) == 1 && len(msg) <= PassageBytes {
		var fallback []search.Result
		if searcher.Embedder != nil {
			q := o
			q.VectorProvided = true
			fallback, _ = searcher.Search(ctx, msg, q)
		}
		results, err := searcher.Search(ctx, msg, o)
		if err != nil && ctx.Err() != nil {
			results, err = fallback, nil
		}
		results = e.semanticGate(results)
		kept := results[:0]
		for _, res := range results {
			if redundant(res.Unit.Body, msg) {
				continue
			}
			why[res.Unit.ID] = "passage=1 legacy_short_query=true"
			kept = append(kept, res)
		}
		return kept, why, err
	}
	ps, err := e.rarePassages(ctx, texts, o)
	if err != nil {
		if ctx.Err() != nil {
			return nil, why, nil
		}
		return nil, why, err
	}
	best := map[int64]search.Result{}
	accept := func(results []search.Result, p passage) {
		for _, res := range results {
			term, ok := justified(res, p.terms, e.MinSemantic)
			if !ok {
				continue
			}
			if prev, ok := best[res.Unit.ID]; !ok || res.Total > prev.Total {
				best[res.Unit.ID] = res
				why[res.Unit.ID] = fmt.Sprintf("passage=%d semantic=%.3f rare_term=%q", p.ordinal, res.Semantic, term)
			}
		}
	}
	// Find cheap keyword results first. A slow embedding cannot erase them.
	for _, p := range ps {
		q := o
		q.KeywordTerms = p.terms
		q.VectorProvided = true
		results, err := searcher.Search(ctx, p.text, q)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return nil, why, err
		}
		accept(results, p)
	}
	if searcher.Embedder != nil && ctx.Err() == nil {
		input := make([]string, len(ps))
		pipeline := e.DB.GetMetaContext(ctx, "pipeline_hash")
		prefix := e.DB.GetMetaContext(ctx, "pipeline_query_prefix")
		for i, p := range ps {
			input[i] = embed.Query(searcher.Embedder, p.text)
			if pipeline != index.PipelineHash(searcher.Embedder.Model()) {
				input[i] = prefix + p.text
			}
		}
		// Reserve time to score the batch and render. The wrapper also bounds
		// providers which do not promptly honour cancellation.
		deadline, _ := ctx.Deadline()
		embedCtx, cancel := context.WithDeadline(ctx, deadline.Add(-200*time.Millisecond))
		vecs, err := searcher.Embedder.Embed(embedCtx, input)
		cancel()
		if err == nil && len(vecs) == len(ps) {
			for i, p := range ps {
				q := o
				q.KeywordTerms = p.terms
				q.VectorProvided = true
				q.QueryVector = vecs[i]
				results, err := searcher.Search(ctx, p.text, q)
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					return nil, why, err
				}
				accept(results, p)
			}
		}
	}
	out := make([]search.Result, 0, len(best))
	seen := map[string]bool{}
	for _, res := range best {
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Unit.ID < out[j].Unit.ID
	})
	kept := out[:0]
	for _, res := range out {
		if seen[res.Unit.ContentHash] || redundant(res.Unit.Body, msg) {
			continue
		}
		seen[res.Unit.ContentHash] = true
		kept = append(kept, res)
	}
	return kept, why, nil
}

func (e *Engine) semanticGate(results []search.Result) []search.Result {
	if e.MinSemantic > 0 && len(results) > 0 && results[0].SemanticOn {
		kept := results[:0]
		for _, r := range results {
			if r.Semantic >= e.MinSemantic || (e.MinSemanticFull > 0 && r.Coverage >= 1 && r.Semantic >= e.MinSemanticFull) {
				kept = append(kept, r)
			}
		}
		return kept
	}
	return results
}
