// Command recalleval runs scratch-only retrieval experiments. It never opens a
// default database: all inputs and outputs must be supplied explicitly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type message struct {
	ID        int    `json:"id"`
	Home      string `json:"home"`
	Session   string `json:"session"`
	Message   string `json:"message"`
	Cwd       string `json:"cwd"`
	Split     string `json:"split"`
	Timestamp string `json:"timestamp"`
}

var identifiers = regexp.MustCompile(`\b[[:alnum:]_./-]*[_./][[:alnum:]_./-]+\b`)

func focused(s string) string {
	if len(s) <= 600 {
		return s
	}
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	last := paras[len(paras)-1]
	ids := identifiers.FindAllString(s, -1)
	if len(ids) > 24 {
		ids = ids[:24]
	}
	return recall.Truncate(strings.Join(ids, " ")+"\n"+last, 2048)
}
func options(o search.Options, variant string) search.Options {
	return o
}
func main() {
	dbpath := flag.String("db", "", "explicit scratch DB")
	input := flag.String("input", "", "replay JSON")
	split := flag.String("split", "development", "split")
	variant := flag.String("variant", "baseline", "experiment")
	flag.Parse()
	if *dbpath == "" || *input == "" {
		panic("explicit -db and -input required")
	}
	base := filepath.Join(os.Getenv("JCODE_SCRATCH_DIR"), "irbench", "replay")
	abs, err := filepath.Abs(*dbpath)
	if err != nil {
		panic(err)
	}
	rel, err := filepath.Rel(base, abs)
	if os.Getenv("JCODE_SCRATCH_DIR") == "" || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		panic("database must be under scratch recall3")
	}
	db, err := store.Open(*dbpath)
	must(err)
	defer db.Close()
	cfg, err := config.Load()
	must(err)
	ctx := context.Background()
	em := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, 0)
	// CPU lexical experiments do not probe or warm Ollama.
	if *variant == "baseline" {
		if !em.Available(ctx) {
			panic("Ollama unavailable")
		}
	}
	s := &search.Searcher{DB: db, Embedder: em}
	a := &app.App{Cfg: cfg, DB: db, Searcher: s, Embedder: em}
	o := options(a.RecallOptions(), *variant)
	e := &recall.Engine{DB: db, Searcher: s, Options: o, RelativeFloor: cfg.RelativeFloor, MinSemantic: cfg.RecallMinSemantic, MinSemanticFull: cfg.RecallMinSemanticFull}
	if *variant == "no-gates" || *variant == "union" || *variant == "tool-search" {
		e.MinSemantic = 0
		e.MinSemanticFull = 0
		e.RelativeFloor = 0
	}
	if *variant == "no-semantic-gate" {
		e.MinSemantic = 0
		e.MinSemanticFull = 0
	}
	if *variant == "no-relative-gate" {
		e.RelativeFloor = 0
	}
	if *variant == "baseline" {
		_, err = em.Embed(ctx, []string{"warmup"})
		must(err)
	} else {
		e.SearchFunc = lexicalCallback(ctx, db, *variant)
	}
	data, err := os.ReadFile(*input)
	must(err)
	var messages []message
	must(json.Unmarshal(data, &messages))
	enc := json.NewEncoder(os.Stdout)
	for _, m := range messages {
		if m.Split != *split {
			continue
		}
		b := a.Bound(m.Home, m.Cwd)
		must(b.Err)
		q := m.Message
		if *variant == "heuristic" {
			q = focused(q)
		}
		if recall.IsOwnOutput(m.Message) {
			q = m.Message
		}
		r := recall.Request{Session: "jcode:" + m.Session, Harness: "jcode", Message: q, Scopes: b.Scopes, ScopeFor: b.ScopeFor}
		if *variant == "union" || *variant == "tool-search" {
			r.Session = ""
		}
		t := time.Now()
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		resp, err := e.Recall(c, r)
		cancel()
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		must(enc.Encode(map[string]any{"id": m.ID, "response": resp, "latency_ms": float64(time.Since(t).Microseconds()) / 1000, "error": errText, "scopes": b.Scopes, "query": q, "variant": *variant}))
		fmt.Fprintf(os.Stderr, "id=%d ms=%d hits=%d error=%s\n", m.ID, time.Since(t).Milliseconds(), len(resp.Hits), errText)
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
