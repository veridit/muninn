// Package app wires configuration, storage, embedding, indexing and recall.
package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// App is an opened muninn instance.
type App struct {
	sourceMu    sync.Mutex
	sourceRoots map[string]sourceBoundary
	Cfg         config.Config
	DB          *store.DB
	Embedder    embed.Embedder // nil = keyword-only
	EmbedNote   string         // which embedder was chosen and why
	Indexer     *index.Indexer
	Searcher    *search.Searcher
	Recall      *recall.Engine
}

// NativePath is muninn's own memory file.
func NativePath() string { return filepath.Join(config.DataDir(), "memories.json") }

// Open loads config, opens the database and probes Ollama.
func Open(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(config.DBPath())
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, DB: db}
	a.Embedder, a.EmbedNote = pickEmbedder(ctx, cfg)
	a.Indexer = &index.Indexer{DB: db, Embedder: a.Embedder}
	a.Searcher = &search.Searcher{DB: db, Embedder: a.Embedder}
	minSem, minFull := cfg.RecallMinSemantic, cfg.RecallMinSemanticFull
	if a.Embedder != nil && a.Embedder.Model() == embed.Builtin.Name {
		minSem, minFull = cfg.BuiltinMinSemantic, cfg.BuiltinMinSemanticFull
	}
	a.Recall = &recall.Engine{DB: db, Searcher: a.Searcher, Options: a.RecallOptions(),
		RelativeFloor: cfg.RelativeFloor, MinSemantic: minSem, MinSemanticFull: minFull}
	return a, nil
}

// pickEmbedder resolves cfg.Embedder. For the built-in model it prefers a
// running `muninn serve` (model already loaded) over loading it in-process.
func pickEmbedder(ctx context.Context, cfg config.Config) (embed.Embedder, string) {
	if os.Getenv("MUNINN_NO_EMBED") != "" || cfg.Embedder == "none" {
		return nil, "keyword-only (embedder disabled)"
	}
	ollama := func() embed.Embedder {
		o := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, time.Duration(cfg.EmbedDelayMS)*time.Millisecond)
		if o.Available(ctx) {
			return o
		}
		return nil
	}
	builtin := func() (embed.Embedder, string) {
		if os.Getenv("MUNINN_SERVE") == "" {
			if r := embed.DialRemote(ctx, cfg.ListenAddr, embed.Builtin.Name); r != nil {
				return r, "built-in model via muninn serve"
			}
		}
		l, err := embed.NewLocal(config.DataDir())
		if err != nil {
			return nil, err.Error()
		}
		return l, "built-in model (in-process)"
	}
	switch cfg.Embedder {
	case "ollama":
		if o := ollama(); o != nil {
			return o, "ollama " + cfg.EmbedModel
		}
		return nil, "keyword-only (Ollama not reachable at " + cfg.OllamaURL + ")"
	case "builtin":
		return builtin()
	default: // auto
		if o := ollama(); o != nil {
			return o, "ollama " + cfg.EmbedModel
		}
		if e, note := builtin(); e != nil {
			return e, note
		}
		return nil, "keyword-only (no Ollama, built-in model not downloaded: run `muninn model download`)"
	}
}

// Close releases the database.
func (a *App) Close() error { return a.DB.Close() }

// JcodeInstances lists the jcode installations muninn indexes and hooks.
func (a *App) JcodeInstances() []harness.JcodeInstance {
	home, _ := os.UserHomeDir()
	extra := append([]string(nil), a.Cfg.JcodeHomes...)
	if d := a.Cfg.JcodeMemoryDir; d != "" { // legacy single-dir setting
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			extra = append(extra, filepath.Dir(d))
		}
	}
	if (a.Cfg.JcodeDiscover != nil && !*a.Cfg.JcodeDiscover) || config.Home() != "" {
		// Explicit opt-out, or an isolated MUNINN_HOME (tests, throwaway
		// homes): never pick up the user's real jcode installations.
		var out []harness.JcodeInstance
		for _, h := range extra {
			if fi, err := os.Stat(h); err == nil && fi.IsDir() {
				out = append(out, harness.InstanceForHome(nil, h))
			}
		}
		return out
	}
	return harness.DiscoverJcode(home, extra)
}

// IndexOptions returns the configured sources.
func (a *App) IndexOptions() index.Options {
	o := index.Options{NativePath: NativePath(), NotesRoots: a.Cfg.NotesRoots}
	for _, i := range a.JcodeInstances() {
		o.Jcode = append(o.Jcode, index.JcodeSource{Dir: filepath.Join(i.Home, "memory"), Prefix: i.Scope()})
	}
	return o
}

// Reindex runs one incremental pass.
func (a *App) Reindex(ctx context.Context) (index.Stats, error) {
	return a.Indexer.Run(ctx, a.IndexOptions())
}

// RecallOptions are the automatic-recall search settings.
func (a *App) RecallOptions() search.Options {
	o := search.DefaultOptions()
	o.Limit = a.Cfg.MaxResults
	o.MinScore = a.Cfg.MinScore
	o.SemanticWeight = a.Cfg.SemanticWeight
	o.SemanticFloor = a.Cfg.SemanticFloor
	o.SemanticSlots = a.Cfg.SemanticSlots
	return o
}
