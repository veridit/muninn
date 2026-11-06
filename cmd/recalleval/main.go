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
	cachePath := flag.String("query-cache", "", "scratch dense query cache")
	prepareCache := flag.Bool("prepare-cache", false, "embed selected split once, requires exclusive Ollama lease")
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
		panic("database must be under scratch irbench/replay")
	}
	db, err := store.Open(*dbpath)
	must(err)
	defer db.Close()
	cfg, err := config.Load()
	must(err)
	ctx := context.Background()
	em := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, 0)
	if *prepareCache {
		must(prepareQueryCache(ctx, db, em, *input, *split, *cachePath))
		return
	}
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
	var queryCosts map[string]queryVectors
	if *variant == "baseline" {
		_, err = em.Embed(ctx, []string{"warmup"})
		must(err)
	} else if *variant == "maxsim" || *variant == "rrf" {
		cache, err := loadQueryCache(db, em.Model(), *cachePath)
		must(err)
		queryCosts = cache.Queries
		// Raw RRF and cosine scores are not production keyword scores. These
		// predeclared diagnostic arms use semantic and relative gates only.
		e.Options.MinScore = 0
		e.SearchFunc = denseCallback(ctx, db, *variant, cache)
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
		cpuMS := float64(time.Since(t).Microseconds()) / 1000
		embedMS := 0.0
		if resp.Skipped == "" && queryCosts != nil {
			embedMS = queryCosts[queryKey(q)].EmbeddingMS
		}
		must(enc.Encode(map[string]any{"id": m.ID, "response": resp, "latency_ms": cpuMS + embedMS, "retrieval_cpu_ms": cpuMS, "embedding_ms": embedMS, "cached_embedding": queryCosts != nil, "error": errText, "scopes": b.Scopes, "query": q, "variant": *variant}))
		fmt.Fprintf(os.Stderr, "id=%d ms=%d hits=%d error=%s\n", m.ID, time.Since(t).Milliseconds(), len(resp.Hits), errText)
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
