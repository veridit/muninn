// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"fmt"
	"github.com/veridit/muninn/internal/irbench"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// lexicalCallback substitutes ranking only. The approved replay loop and recall
// engine retain binding resolution, own-output guards and injection chronology.
func lexicalCallback(ctx context.Context, db *store.DB, variant string) func(context.Context, string, search.Options) ([]search.Result, error) {
	lo := irbench.LexicalOptions{Exhaustive: true}
	switch variant {
	case "bm25":
	case "idf24":
		lo.Terms = 24
	case "adaptive":
		lo.StopRatio = .2
	case "idf24-adaptive":
		lo.Terms = 24
		lo.StopRatio = .2
	default:
		panic(fmt.Sprintf("unknown CPU arm %q", variant))
	}
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
	return func(ctx context.Context, q string, o search.Options) ([]search.Result, error) {
		visible := irbench.Visible(o)
		// All visible scored candidates remain available until canonical text/SID
		// dedup is done. No global top-k can starve a bound scope.
		opts := lo
		opts.K = len(c.Docs)
		hits, _, err := c.Search(ctx, q, opts, func(u store.Unit) bool { return canonical[u.ID] && visible(u) })
		if err != nil {
			return nil, err
		}
		limit := o.Limit
		if limit <= 0 {
			limit = 10
		}
		seenSID, seenText := map[string]bool{}, map[string]bool{}
		out := make([]search.Result, 0, limit)
		for _, h := range hits {
			if h.Score < o.MinScore {
				continue
			}
			u := c.Docs[h.ID].Unit
			if o.ScopeFor != nil {
				scope, ok := o.ScopeFor(u)
				if !ok {
					continue
				}
				u.Scope = scope
			}
			if (u.SID != "" && seenSID[u.SID]) || (u.ContentHash != "" && seenText[u.ContentHash]) {
				continue
			}
			seenSID[u.SID] = true
			seenText[u.ContentHash] = true
			out = append(out, search.Result{Unit: u, Total: h.Score})
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}
}
