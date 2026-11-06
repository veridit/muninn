// SPDX-License-Identifier: MPL-2.0
// Command irdense measures dense primitives, not chronological recall replay.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/irbench"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dbp := flag.String("db", "", "read-only scratch snapshot")
	input := flag.String("input", "", "reused replay JSON")
	limit := flag.Int("limit", 150, "max development queries")
	flag.Parse()
	root := filepath.Join(os.Getenv("JCODE_SCRATCH_DIR"), "irbench")
	abs, err := filepath.Abs(*dbp)
	must(err)
	rel, err := filepath.Rel(root, abs)
	must(err)
	if os.Getenv("JCODE_SCRATCH_DIR") == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		panic("DB must be in scratch/irbench")
	}
	db, err := sql.Open("sqlite", "file:"+abs+"?mode=ro&immutable=1")
	must(err)
	defer db.Close()
	ctx := context.Background()
	vectors, err := irbench.LoadVectors(ctx, db)
	must(err)
	if len(vectors) == 0 {
		panic("no vectors")
	}
	fmt.Fprintf(os.Stderr, "vectors=%d\n", len(vectors))
	var prefix string
	must(db.QueryRow("SELECT value FROM meta WHERE key='pipeline_query_prefix'").Scan(&prefix))
	cfg, err := config.Load()
	must(err)
	em := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, 0)
	_, err = em.Embed(ctx, []string{"warmup"})
	must(err)
	raw, err := os.ReadFile(*input)
	must(err)
	var qs []struct {
		ID      int    `json:"id"`
		Message string `json:"message"`
		Split   string `json:"split"`
	}
	must(json.Unmarshal(raw, &qs))
	enc := json.NewEncoder(os.Stdout)
	n := 0
	for _, q := range qs {
		if q.Split != "development" {
			continue
		}
		n++
		if n > *limit {
			break
		}
		chunks := irbench.Chunks(q.Message, 600, 8)
		texts := append([]string{prefix + q.Message}, chunks...)
		for i := 1; i < len(texts); i++ {
			texts[i] = prefix + texts[i]
		}
		t := time.Now()
		embs, err := em.Embed(ctx, texts)
		must(err)
		embedms := float64(time.Since(t).Nanoseconds()) / 1e6
		must(enc.Encode(map[string]any{"id": q.ID, "variant": "embedding", "latency_ms": embedms, "chunks": len(chunks), "query_bytes": len(q.Message)}))
		for _, v := range []struct {
			name string
			qs   [][]float32
			keep int
		}{{"single-exact", embs[:1], 0}, {"single-sketch", embs[:1], 1024}, {"maxsim-exact", embs[1:], 0}, {"maxsim-sketch", embs[1:], 1024}} {
			t := time.Now()
			hits, err := irbench.MaxSim(ctx, v.qs, vectors, 10, v.keep, nil)
			must(err)
			must(enc.Encode(map[string]any{"id": q.ID, "variant": v.name, "latency_ms": float64(time.Since(t).Nanoseconds()) / 1e6, "hits": hits, "chunks": len(v.qs), "query_bytes": len(q.Message)}))
		}
		fmt.Fprintf(os.Stderr, "query=%d chunks=%d embed_ms=%.1f\n", q.ID, len(chunks), embedms)
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
