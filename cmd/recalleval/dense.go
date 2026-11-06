// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"fmt"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/irbench"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"math"
)

// denseCallback never invokes a model. Query vectors were prepared under an
// exclusive lease and validated against the frozen snapshot pipeline.
func denseCallback(ctx context.Context, db *store.DB, variant string, cache queryCache) func(context.Context, string, search.Options) ([]search.Result, error) {
	c, err := irbench.Load(ctx, db.DB, 1.2, .75)
	must(err)
	canonical := map[int64]bool{}
	for id, d := range c.Docs {
		u, err := db.GetUnit(id)
		must(err)
		d.Unit = u
		c.Docs[id] = d
		canonical[id] = u.Active && db.IsCanonical(u)
	}
	vectors, err := irbench.LoadVectors(ctx, db.DB)
	must(err)
	byID := map[int64]irbench.Vector{}
	for _, v := range vectors {
		byID[v.ID] = v
	}
	return denseSearch(c, canonical, vectors, byID, variant, cache)
}

func denseSearch(c *irbench.Corpus, canonical map[int64]bool, vectors []irbench.Vector, byID map[int64]irbench.Vector, variant string, cache queryCache) func(context.Context, string, search.Options) ([]search.Result, error) {
	return func(ctx context.Context, q string, o search.Options) ([]search.Result, error) {
		cached, ok := cache.Queries[queryKey(q)]
		if !ok || len(cached.Vectors) == 0 {
			return nil, fmt.Errorf("missing dense query cache entry")
		}
		visible := irbench.Visible(o)
		allowed := func(id int64) bool { d, ok := c.Docs[id]; return ok && canonical[id] && visible(d.Unit) }
		// Scope and canonical ownership apply before each passage's Hamming budget.
		dense, err := irbench.MaxSim(ctx, cached.Vectors, vectors, len(vectors), 1024, allowed)
		if err != nil {
			return nil, err
		}
		dedup := func(hits []irbench.Hit, n int) []irbench.Hit {
			var out []irbench.Hit
			sid, text := map[string]bool{}, map[string]bool{}
			for _, h := range hits {
				u := c.Docs[h.ID].Unit
				if (u.SID != "" && sid[u.SID]) || (u.ContentHash != "" && text[u.ContentHash]) {
					continue
				}
				sid[u.SID] = true
				text[u.ContentHash] = true
				out = append(out, h)
				if len(out) == n {
					break
				}
			}
			return out
		}
		dense = dedup(dense, 20)
		ranked := dense
		if variant == "rrf" {
			lex, _, err := c.Search(ctx, q, irbench.LexicalOptions{K: len(c.Docs), Terms: 24, Exhaustive: true}, func(u store.Unit) bool { return allowed(u.ID) })
			if err != nil {
				return nil, err
			}
			ranked = dedup(irbench.RRF(60, dedup(lex, 20), dense), 40)
		}
		limit := o.Limit
		if limit <= 0 {
			limit = 10
		}
		words := map[string]bool{}
		for _, w := range irbench.Tokens(q) {
			words[w] = true
		}
		out := make([]search.Result, 0, limit)
		for _, h := range ranked {
			u := c.Docs[h.ID].Unit
			if o.ScopeFor != nil {
				scope, ok := o.ScopeFor(u)
				if !ok {
					continue
				}
				u.Scope = scope
			}
			sim := 0.0
			if v, ok := byID[h.ID]; ok {
				sim = math.Inf(-1)
				for _, qv := range cached.Vectors {
					if len(qv) != len(v.F32) {
						return nil, fmt.Errorf("dense dimension mismatch")
					}
					sim = math.Max(sim, float64(embed.Dot(qv, v.F32)))
				}
			}
			coverage := 0.0
			if len(words) > 0 {
				matched := 0
				for w := range words {
					if c.Docs[h.ID].TF[w] > 0 {
						matched++
					}
				}
				coverage = float64(matched) / float64(len(words))
			}
			score := h.Score
			if variant == "maxsim" {
				score = 10 * sim
			}
			out = append(out, search.Result{Unit: u, Total: score, Semantic: sim, SemanticOn: true, Coverage: coverage})
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}
}
