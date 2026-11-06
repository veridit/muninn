// SPDX-License-Identifier: MPL-2.0
// Command irlme consumes tiger's prepared LongMemEval-S round-value JSON. It does
// not download, resample, or split the benchmark, and never embeds evidence labels.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"github.com/veridit/muninn/internal/irbench"
	"github.com/veridit/muninn/internal/store"
	"os"
	"sort"
	"time"
)

type round struct {
	Key, Session, Date, Body string
	Evidence                 bool
}
type question struct {
	ID, Question, Date string
	Sessions           []string
	Rounds             []round
}

func main() {
	input := flag.String("input", "", "shared prepared longmem-subset.json")
	python := flag.String("python", "", "optional scratch Python for local neural rerank")
	worker := flag.String("worker", "scripts/irbench-reranker.py", "worker")
	device := flag.String("device", "mps", "rerank device")
	flag.Parse()
	raw, err := os.ReadFile(*input)
	must(err)
	var qs []question
	must(json.Unmarshal(raw, &qs))
	ctx := context.Background()
	var rr *irbench.Reranker
	if *python != "" {
		rr, err = irbench.StartReranker(ctx, *python, *worker, "--device", *device)
		must(err)
		defer rr.Close()
		_, err = rr.Score(ctx, "warmup", []string{"warmup"})
		must(err)
	}
	enc := json.NewEncoder(os.Stdout)
	for _, q := range qs {
		var units []store.Unit
		for i, r := range q.Rounds {
			units = append(units, store.Unit{ID: int64(i + 1), Body: r.Body, Active: true})
		}
		t := time.Now()
		c := irbench.NewCorpus(units, 1.2, .75)
		build := float64(time.Since(t).Microseconds()) / 1000
		variants := []struct {
			name string
			o    irbench.LexicalOptions
		}{{"bm25", irbench.LexicalOptions{K: 10, Exhaustive: true}}, {"maxscore", irbench.LexicalOptions{K: 10}}, {"idf24", irbench.LexicalOptions{K: 10, Terms: 24, Exhaustive: true}}, {"adaptive", irbench.LexicalOptions{K: 10, StopRatio: .2, Exhaustive: true}}, {"idf24-adaptive", irbench.LexicalOptions{K: 10, Terms: 24, StopRatio: .2, Exhaustive: true}}}
		var baseline []irbench.Hit
		emit := func(name string, hits []irbench.Hit, ms float64) {
			correct, total := 0, 0
			found := map[string]bool{}
			for _, r := range q.Rounds {
				if r.Evidence {
					total++
				}
			}
			for _, h := range hits {
				r := q.Rounds[h.ID-1]
				if r.Evidence {
					correct++
				}
				found[r.Session] = true
			}
			sessions := 0
			for _, s := range q.Sessions {
				if found[s] {
					sessions++
				}
			}
			must(enc.Encode(map[string]any{"question_id": q.ID, "variant": name, "latency_ms": ms, "build_ms": build, "rounds": len(units), "evidence_found": correct, "evidence_total": total, "sessions_found": sessions, "sessions_total": len(q.Sessions), "hits": hits}))
		}
		for _, v := range variants {
			t := time.Now()
			hits, _, err := c.Search(ctx, q.Question, v.o, nil)
			must(err)
			ms := float64(time.Since(t).Nanoseconds()) / 1e6
			emit(v.name, hits, ms)
			if v.name == "bm25" {
				baseline = hits
			}
		}
		if rr != nil {
			var docs []string
			for _, h := range baseline {
				docs = append(docs, c.Docs[h.ID].Unit.Body)
			}
			t := time.Now()
			scores, err := rr.Score(ctx, q.Question, docs)
			must(err)
			hits := append([]irbench.Hit(nil), baseline...)
			for i := range hits {
				hits[i].Score = scores[i]
			}
			sort.Slice(hits, func(i, j int) bool {
				if hits[i].Score != hits[j].Score {
					return hits[i].Score > hits[j].Score
				}
				return hits[i].ID < hits[j].ID
			})
			ms := float64(time.Since(t).Nanoseconds()) / 1e6
			emit("minilm-rerank", hits, ms)
			for _, threshold := range []float64{0, 2, 4} {
				var gated []irbench.Hit
				for _, h := range hits {
					if h.Score >= threshold {
						gated = append(gated, h)
					}
				}
				name := map[float64]string{0: "minilm-gate0", 2: "minilm-gate2", 4: "minilm-gate4"}[threshold]
				emit(name, gated, ms)
			}
		}
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
