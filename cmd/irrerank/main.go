// SPDX-License-Identifier: MPL-2.0
// Command irrerank times a persistent local reranker on explicit JSONL pairs.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"github.com/veridit/muninn/internal/irbench"
	"os"
	"time"
)

func main() {
	input := flag.String("input", "", "JSONL id/query/documents records")
	python := flag.String("python", "", "scratch venv Python path")
	worker := flag.String("worker", "scripts/irbench-reranker.py", "local inference worker")
	device := flag.String("device", "cpu", "cpu or mps")
	flag.Parse()
	if *input == "" || *python == "" {
		panic("explicit input and python required")
	}
	ctx := context.Background()
	r, err := irbench.StartReranker(ctx, *python, *worker, "--device", *device)
	must(err)
	defer r.Close()
	t := time.Now()
	_, err = r.Score(ctx, "warmup", []string{"warmup document"})
	must(err)
	enc := json.NewEncoder(os.Stdout)
	must(enc.Encode(map[string]any{"startup_and_warmup_ms": float64(time.Since(t).Microseconds()) / 1000, "device": *device}))
	f, err := os.Open(*input)
	must(err)
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		var q struct {
			ID        int      `json:"id"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		must(json.Unmarshal(scan.Bytes(), &q))
		t := time.Now()
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		scores, err := r.Score(c, q.Query, q.Documents)
		cancel()
		must(err)
		must(enc.Encode(map[string]any{"id": q.ID, "scores": scores, "documents": len(q.Documents), "query_bytes": len(q.Query), "latency_ms": float64(time.Since(t).Nanoseconds()) / 1e6, "device": *device}))
	}
	must(scan.Err())
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
