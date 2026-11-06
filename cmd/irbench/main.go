// Command irbench measures opt-in retrieval primitives against an explicit
// read-only scratch snapshot. It is not a replacement for recalleval's replay.
// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/veridit/muninn/internal/irbench"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type query struct {
	ID      int    `json:"id"`
	Message string `json:"message"`
	Split   string `json:"split"`
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	dbp := flag.String("db", "", "read-only scratch snapshot")
	input := flag.String("input", "", "existing replay JSON, reused without session replay")
	split := flag.String("split", "development", "split to measure")
	repeat := flag.Int("repeat", 3, "warm repetitions")
	flag.Parse()
	root := filepath.Join(os.Getenv("JCODE_SCRATCH_DIR"), "irbench")
	abs, err := filepath.Abs(*dbp)
	must(err)
	rel, err := filepath.Rel(root, abs)
	must(err)
	if os.Getenv("JCODE_SCRATCH_DIR") == "" || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		panic("DB must be in scratch/irbench")
	}
	db, err := sql.Open("sqlite", "file:"+abs+"?mode=ro&immutable=1")
	must(err)
	defer db.Close()
	ctx := context.Background()
	t := time.Now()
	c, err := irbench.Load(ctx, db, 1.2, .75)
	must(err)
	must(irbench.Validate(c))
	fmt.Fprintf(os.Stderr, "documents=%d terms=%d build_ms=%.3f\n", len(c.Docs), len(c.Lists), float64(time.Since(t).Microseconds())/1000)
	raw, err := os.ReadFile(*input)
	must(err)
	var qs []query
	must(json.Unmarshal(raw, &qs))
	enc := json.NewEncoder(os.Stdout)
	variants := []struct {
		name string
		o    irbench.LexicalOptions
	}{{"exhaustive", irbench.LexicalOptions{K: 10, Exhaustive: true}}, {"maxscore", irbench.LexicalOptions{K: 10}}, {"idf24", irbench.LexicalOptions{K: 10, Terms: 24}},
		{"idf24-exhaustive", irbench.LexicalOptions{K: 10, Terms: 24, Exhaustive: true}}, {"adaptive", irbench.LexicalOptions{K: 10, StopRatio: .2}}, {"idf24-adaptive", irbench.LexicalOptions{K: 10, Terms: 24, StopRatio: .2}}}
	latencies := map[string][]float64{}
	mismatch := 0
	queries := 0
	for _, q := range qs {
		if q.Split != *split {
			continue
		}
		queries++
		oracle, _, err := c.Search(ctx, q.Message, variants[0].o, nil)
		must(err)
		for _, v := range variants {
			for rep := 0; rep < *repeat; rep++ {
				t := time.Now()
				hits, st, err := c.Search(ctx, q.Message, v.o, nil)
				must(err)
				ms := float64(time.Since(t).Nanoseconds()) / 1e6
				latencies[v.name] = append(latencies[v.name], ms)
				overlap := 0
				for _, h := range hits {
					for _, a := range oracle {
						if h.ID == a.ID {
							overlap++
							break
						}
					}
				}
				if v.name == "maxscore" {
					if len(hits) != len(oracle) {
						mismatch++
					} else {
						for i := range hits {
							if hits[i] != oracle[i] {
								mismatch++
								break
							}
						}
					}
				}
				must(enc.Encode(map[string]any{"id": q.ID, "variant": v.name, "repeat": rep, "latency_ms": ms, "stats": st, "hits": hits, "oracle_overlap_at_10": overlap, "oracle_size": len(oracle), "query_bytes": len(q.Message)}))
			}
		}
	}
	if queries == 0 || *repeat <= 0 {
		panic("no benchmark samples: check split and repeat")
	}
	summary := map[string]any{"documents": len(c.Docs), "queries": queries, "maxscore_oracle_mismatches": mismatch}
	for v, ls := range latencies {
		sort.Float64s(ls)
		summary[v] = map[string]any{"n": len(ls), "p50_ms": ls[(len(ls)-1)/2], "p95_ms": ls[int(float64(len(ls)-1)*.95)]}
	}
	raw, err = json.Marshal(summary)
	must(err)
	fmt.Fprintln(os.Stderr, string(raw))
}
