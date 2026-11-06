// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/irbench"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type queryVectors struct {
	Vectors     [][]float32 `json:"vectors"`
	EmbeddingMS float64     `json:"embedding_ms"`
}
type queryCache struct {
	Pipeline, Prefix, Model string
	Queries                 map[string]queryVectors
}

func queryKey(q string) string {
	q = recall.Truncate(strings.TrimSpace(q), recall.MaxQueryBytes)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(q)))
}
func pipelineIdentity(db *store.DB) (string, string, error) {
	var pipeline, prefix string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='pipeline_hash'").Scan(&pipeline); err != nil {
		return "", "", err
	}
	if err := db.QueryRow("SELECT value FROM meta WHERE key='pipeline_query_prefix'").Scan(&prefix); err != nil {
		return "", "", err
	}
	return pipeline, prefix, nil
}
func cachePathAllowed(path string) error {
	root := filepath.Join(os.Getenv("JCODE_SCRATCH_DIR"), "irbench")
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, abs)
	if path == "" || os.Getenv("JCODE_SCRATCH_DIR") == "" || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("cache must be under scratch irbench")
	}
	return nil
}
func loadQueryCache(db *store.DB, model, path string) (queryCache, error) {
	var cache queryCache
	if err := cachePathAllowed(path); err != nil {
		return cache, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cache, err
	}
	if err = json.Unmarshal(raw, &cache); err != nil {
		return cache, err
	}
	pipeline, prefix, err := pipelineIdentity(db)
	if err != nil {
		return cache, err
	}
	if cache.Pipeline != pipeline || cache.Prefix != prefix || cache.Model != model {
		return cache, fmt.Errorf("query cache pipeline/model mismatch")
	}
	return cache, nil
}

// Called only with an explicitly coordinated exclusive Ollama lease. A completed
// query is atomically checkpointed, permitting bounded lease release/resumption.
func prepareQueryCache(ctx context.Context, db *store.DB, em embed.Embedder, input, split, path string) error {
	if err := cachePathAllowed(path); err != nil {
		return err
	}
	pipeline, prefix, err := pipelineIdentity(db)
	if err != nil {
		return err
	}
	cache := queryCache{Pipeline: pipeline, Prefix: prefix, Model: em.Model(), Queries: map[string]queryVectors{}}
	if _, err = os.Stat(path); err == nil {
		cache, err = loadQueryCache(db, em.Model(), path)
		if err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var messages []message
	if err = json.Unmarshal(raw, &messages); err != nil {
		return err
	}
	for _, m := range messages {
		if m.Split != split || recall.IsOwnOutput(m.Message) || strings.TrimSpace(m.Message) == "" {
			continue
		}
		q := recall.Truncate(strings.TrimSpace(m.Message), recall.MaxQueryBytes)
		key := queryKey(q)
		if _, ok := cache.Queries[key]; ok {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		t := time.Now()
		vectors, err := irbench.EmbedChunks(c, em, q, prefix, 600, 8)
		ms := float64(time.Since(t).Microseconds()) / 1000
		cancel()
		if err != nil {
			return fmt.Errorf("query %d: %w", m.ID, err)
		}
		if len(vectors) == 0 {
			return fmt.Errorf("query %d returned no vectors", m.ID)
		}
		cache.Queries[key] = queryVectors{Vectors: vectors, EmbeddingMS: ms}
		raw, err = json.Marshal(cache)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path+".partial", raw, 0600); err != nil {
			return err
		}
		if err = os.Rename(path+".partial", path); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "JCODE_PROGRESS {\"message\":\"cached query %d\",\"current\":%d,\"unit\":\"queries\"}\n", m.ID, len(cache.Queries))
	}
	return nil
}
