// Package irbench contains opt-in retrieval experiments, not production defaults.
// SPDX-License-Identifier: MPL-2.0
package irbench

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

type Posting struct {
	ID    int64
	Score float64
}
type Hit struct {
	ID    int64   `json:"id"`
	Score float64 `json:"score"`
}
type Document struct {
	Unit   store.Unit
	Length int
	TF     map[string]float64
}
type Corpus struct {
	Docs   map[int64]Document
	DF     map[string]int
	Lists  map[string][]Posting
	Bounds map[string]float64
	AvgLen float64
	K1, B  float64
}
type LexicalOptions struct {
	Terms      int
	StopRatio  float64
	K          int
	Exhaustive bool
}
type Stats struct {
	InputTerms int `json:"input_terms"`
	KeptTerms  int `json:"kept_terms"`
	Candidates int `json:"candidates"`
	Scored     int `json:"scored"`
	Probes     int `json:"probes"`
}

// Tokens deliberately reuses muninn's current lexical normalization.
func Tokens(text string) []string {
	var out []string
	for _, w := range index.Words(text) {
		if len(w) > 1 && !index.IsStopword(w) {
			out = append(out, w)
		}
	}
	return out
}

// Load reads a snapshot. It never opens or modifies a live store. The caller
// supplies a read-only SQL handle. Field weighting is BM25 weighted TF, not BM25F.
func Load(ctx context.Context, db *sql.DB, k1, b float64) (*Corpus, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,source_id,scope,kind,title,body,tags,content_hash,sid,active FROM units WHERE active=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var units []store.Unit
	for rows.Next() {
		var u store.Unit
		var tags string
		if err := rows.Scan(&u.ID, &u.SourceID, &u.Scope, &u.Kind, &u.Title, &u.Body, &tags, &u.ContentHash, &u.SID, &u.Active); err != nil {
			return nil, err
		}
		u.Tags = []string{tags}
		units = append(units, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return NewCorpus(units, k1, b), nil
}
func NewCorpus(units []store.Unit, k1, b float64) *Corpus {
	c := &Corpus{Docs: map[int64]Document{}, DF: map[string]int{}, Lists: map[string][]Posting{}, Bounds: map[string]float64{}, K1: k1, B: b}
	for _, u := range units {
		tf := map[string]float64{}
		length := 0
		for i, text := range []string{u.Title, u.Body, strings.Join(u.Tags, " ")} {
			w := 1.
			if i == 0 {
				w = 2
			}
			ts := Tokens(text)
			length += len(ts)
			for _, t := range ts {
				tf[t] += w
			}
		}
		c.Docs[u.ID] = Document{u, length, tf}
		c.AvgLen += float64(length)
		for t := range tf {
			c.DF[t]++
		}
	}
	if len(c.Docs) > 0 {
		c.AvgLen /= float64(len(c.Docs))
	}
	if c.AvgLen == 0 {
		c.AvgLen = 1
	}
	for id, d := range c.Docs {
		for t, tf := range d.TF {
			s := c.IDF(t) * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(d.Length)/c.AvgLen))
			c.Lists[t] = append(c.Lists[t], Posting{id, s})
			c.Bounds[t] = math.Max(c.Bounds[t], s)
		}
	}
	for t := range c.Lists {
		sort.Slice(c.Lists[t], func(i, j int) bool { return c.Lists[t][i].ID < c.Lists[t][j].ID })
	}
	return c
}
func (c *Corpus) IDF(t string) float64 {
	return math.Log(1 + (float64(len(c.Docs)-c.DF[t])+.5)/(float64(c.DF[t])+.5))
}

// Terms deduplicates query terms, discards OOVs, optionally removes corpus-common
// terms, then keeps highest IDF terms. Pruning is approximate, unlike MaxScore.
func (c *Corpus) Terms(query string, limit int, stopRatio float64) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range Tokens(query) {
		if seen[t] || c.DF[t] == 0 {
			continue
		}
		seen[t] = true
		if stopRatio > 0 && float64(c.DF[t])/float64(len(c.Docs)) >= stopRatio {
			continue
		}
		terms = append(terms, t)
	}
	sort.Slice(terms, func(i, j int) bool {
		a, b := c.IDF(terms[i]), c.IDF(terms[j])
		if a != b {
			return a > b
		}
		return terms[i] < terms[j]
	})
	if limit > 0 && len(terms) > limit {
		terms = terms[:limit]
	}
	return terms
}
func better(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ID < b.ID
}
func topAdd(top []Hit, h Hit, k int) []Hit {
	if k <= 0 {
		return nil
	}
	if len(top) == k && !better(h, top[len(top)-1]) {
		return top
	}
	top = append(top, h)
	sort.Slice(top, func(i, j int) bool { return better(top[i], top[j]) })
	if len(top) > k {
		top = top[:k]
	}
	return top
}
func threshold(top []Hit, k int) float64 {
	if len(top) < k {
		return 0
	}
	return top[len(top)-1].Score
}

// Search runs exact MaxScore over the selected terms. Bounds are maxima of
// actual nonnegative posting scores. Strict inequalities preserve equal-score
// ID tie-breaking. allow is applied before the top-k threshold is updated.
func (c *Corpus) Search(ctx context.Context, query string, o LexicalOptions, allow func(store.Unit) bool) ([]Hit, Stats, error) {
	if o.K <= 0 {
		o.K = 10
	}
	terms := c.Terms(query, o.Terms, o.StopRatio)
	st := Stats{InputTerms: len(Tokens(query)), KeptTerms: len(terms)}
	if o.Exhaustive {
		scores := map[int64]float64{}
		sort.Slice(terms, func(i, j int) bool {
			if c.Bounds[terms[i]] != c.Bounds[terms[j]] {
				return c.Bounds[terms[i]] < c.Bounds[terms[j]]
			}
			return terms[i] < terms[j]
		})
		for _, t := range terms {
			for _, p := range c.Lists[t] {
				if ctx.Err() != nil {
					return nil, st, ctx.Err()
				}
				scores[p.ID] += p.Score
				st.Probes++
			}
		}
		var top []Hit
		for id, s := range scores {
			st.Candidates++
			if allow == nil || allow(c.Docs[id].Unit) {
				st.Scored++
				top = topAdd(top, Hit{id, s}, o.K)
			}
		}
		return top, st, nil
	}
	sort.Slice(terms, func(i, j int) bool {
		if c.Bounds[terms[i]] != c.Bounds[terms[j]] {
			return c.Bounds[terms[i]] < c.Bounds[terms[j]]
		}
		return terms[i] < terms[j]
	})
	n := len(terms)
	pos := make([]int, n)
	prefix := make([]float64, n+1)
	for i, t := range terms {
		prefix[i+1] = prefix[i] + c.Bounds[t]
	}
	var top []Hit
	for {
		if err := ctx.Err(); err != nil {
			return nil, st, err
		}
		cut := 0
		th := threshold(top, o.K)
		for cut < n && prefix[cut+1] < th {
			cut++
		}
		if cut == n {
			break
		}
		doc := int64(math.MaxInt64)
		for i := cut; i < n; i++ {
			ps := c.Lists[terms[i]]
			if pos[i] < len(ps) && ps[pos[i]].ID < doc {
				doc = ps[pos[i]].ID
			}
		}
		if doc == math.MaxInt64 {
			break
		}
		st.Candidates++
		score := 0.
		// Sum in ascending list order, matching the exhaustive oracle's FP order.
		vals := make([]float64, n)
		for i := cut; i < n; i++ {
			ps := c.Lists[terms[i]]
			if pos[i] < len(ps) && ps[pos[i]].ID == doc {
				vals[i] = ps[pos[i]].Score
				score += vals[i]
				pos[i]++
				st.Probes++
			}
		}
		possible := true
		for i := cut - 1; i >= 0; i-- {
			if score+prefix[i+1]+1e-12 < th {
				possible = false
				break
			}
			ps := c.Lists[terms[i]]
			p := pos[i]
			if p < len(ps) && ps[p].ID < doc {
				p += sort.Search(len(ps)-p, func(j int) bool { return ps[p+j].ID >= doc })
				pos[i] = p
			}
			st.Probes++
			if p < len(ps) && ps[p].ID == doc {
				vals[i] = ps[p].Score
				score += vals[i]
			}
		}
		if possible && (allow == nil || allow(c.Docs[doc].Unit)) {
			score = 0
			for _, v := range vals {
				score += v
			}
			st.Scored++
			top = topAdd(top, Hit{doc, score}, o.K)
		}
	}
	return top, st, nil
}

// Visible supplies the same scope/kind rules as production search, including
// source-binding remapping. Callers still apply canonical and session dedup.
func Visible(o search.Options) func(store.Unit) bool {
	return func(u store.Unit) bool {
		if o.ScopeFor != nil {
			var ok bool
			u.Scope, ok = o.ScopeFor(u)
			if !ok {
				return false
			}
		}
		return search.Allowed(u.Scope, o.Scopes) && search.Allowed(u.Kind, o.Kinds)
	}
}

func Validate(c *Corpus) error {
	for t, ps := range c.Lists {
		prev := int64(-1)
		for _, p := range ps {
			if p.ID <= prev || p.Score < 0 || math.IsNaN(p.Score) || p.Score > c.Bounds[t] {
				return fmt.Errorf("invalid posting list %q", t)
			}
			prev = p.ID
		}
	}
	return nil
}
