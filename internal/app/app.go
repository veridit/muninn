// Package app wires configuration, storage, embedding, indexing and recall.
package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// App is an opened muninn instance.
type App struct {
	Cfg      config.Config
	DB       *store.DB
	Embedder embed.Embedder // nil when Ollama is unreachable
	Indexer  *index.Indexer
	Searcher *search.Searcher
	Recall   *recall.Engine
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
	if os.Getenv("MUNINN_NO_EMBED") == "" {
		o := embed.NewOllama(cfg.OllamaURL, cfg.EmbedModel, time.Duration(cfg.EmbedDelayMS)*time.Millisecond)
		if o.Available(ctx) {
			a.Embedder = o
		}
	}
	a.Indexer = &index.Indexer{DB: db, Embedder: a.Embedder}
	a.Searcher = &search.Searcher{DB: db, Embedder: a.Embedder}
	a.Recall = &recall.Engine{DB: db, Searcher: a.Searcher, Options: a.RecallOptions(),
		RelativeFloor: cfg.RelativeFloor, MinSemantic: cfg.RecallMinSemantic, MinSemanticFull: cfg.RecallMinSemanticFull}
	return a, nil
}

// Close releases the database.
func (a *App) Close() error { return a.DB.Close() }

// IndexOptions returns the configured sources.
func (a *App) IndexOptions() index.Options {
	return index.Options{JcodeDir: a.Cfg.JcodeMemoryDir, NativePath: NativePath(), NotesRoots: a.Cfg.NotesRoots}
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
