// SPDX-License-Identifier: MPL-2.0
// Command irfusion measures RRF on existing primitive candidate lists, no model.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"github.com/veridit/muninn/internal/irbench"
	"os"
	"time"
)

type record struct {
	ID      int           `json:"id"`
	Variant string        `json:"variant"`
	Repeat  int           `json:"repeat"`
	Hits    []irbench.Hit `json:"hits"`
}

func load(path string) []record {
	f, e := os.Open(path)
	must(e)
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 8<<20)
	var out []record
	for s.Scan() {
		var r record
		must(json.Unmarshal(s.Bytes(), &r))
		out = append(out, r)
	}
	must(s.Err())
	return out
}
func main() {
	lexical := flag.String("lexical", "", "existing lexical-live JSONL")
	dense := flag.String("dense", "", "existing dense JSONL from same snapshot")
	flag.Parse()
	ls := map[int][]irbench.Hit{}
	for _, r := range load(*lexical) {
		if r.Variant == "idf24-exhaustive" && r.Repeat == 0 {
			ls[r.ID] = r.Hits
		}
	}
	enc := json.NewEncoder(os.Stdout)
	n := 0
	for _, r := range load(*dense) {
		if r.Variant != "maxsim-sketch" {
			continue
		}
		l, ok := ls[r.ID]
		if !ok {
			panic("missing same-query lexical candidates")
		}
		for repeat := 0; repeat < 10; repeat++ {
			t := time.Now()
			hits := irbench.RRF(60, l, r.Hits)
			ms := float64(time.Since(t).Nanoseconds()) / 1e6
			if len(hits) > 10 {
				hits = hits[:10]
			}
			must(enc.Encode(map[string]any{"id": r.ID, "variant": "rrf60", "repeat": repeat, "latency_ms": ms, "hits": hits, "lexical_pool": len(l), "dense_pool": len(r.Hits)}))
			n++
		}
	}
	if n == 0 {
		panic("no matched candidates")
	}
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
