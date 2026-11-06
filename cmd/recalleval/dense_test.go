// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/irbench"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestDenseScopeBeforeHammingBudgetAndOfflineCache(t *testing.T) {
	var units []store.Unit
	var vectors []irbench.Vector
	canonical := map[int64]bool{}
	byID := map[int64]irbench.Vector{}
	for i := 1; i <= 1101; i++ {
		id := int64(i)
		scope := "excluded"
		v := []float32{1, 0}
		if i >= 1100 {
			scope = "remap"
		}
		if i == 1101 {
			scope = "remap"
			v = []float32{.8, .6}
		}
		u := store.Unit{ID: id, SID: fmt.Sprint(i), ContentHash: fmt.Sprint(i), Body: "quartz", Scope: scope, Kind: "memory", Active: true}
		units = append(units, u)
		canonical[id] = i != 1100
		vector := irbench.Vector{ID: id, F32: v, Sketch: embed.Sketch(v)}
		vectors = append(vectors, vector)
		byID[id] = vector
	}
	corpus := irbench.NewCorpus(units, 1.2, .75)
	cache := queryCache{Queries: map[string]queryVectors{queryKey("quartz"): {Vectors: [][]float32{{1, 0}}}}}
	for _, variant := range []string{"maxsim", "rrf"} {
		fn := denseSearch(corpus, canonical, vectors, byID, variant, cache)
		o := search.Options{Limit: 1, Scopes: []string{"bound"}, ScopeFor: func(u store.Unit) (string, bool) { return "bound", u.Scope == "remap" }}
		rs, err := fn(context.Background(), "quartz", o)
		if err != nil || len(rs) != 1 || rs[0].Unit.ID != 1101 || rs[0].Unit.Scope != "bound" || !rs[0].SemanticOn || rs[0].Semantic < .79 {
			t.Fatalf("%s scope-starved or bad semantic: %+v %v", variant, rs, err)
		}
		if _, err := fn(context.Background(), "not cached", o); err == nil {
			t.Fatal("missing query cache did not fail closed")
		}
	}
}

type countingEmbedder struct {
	calls int
	texts []string
}

func (e *countingEmbedder) Model() string { return "fake" }
func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.texts = append(e.texts, texts...)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func TestPrepareCacheSkipsOwnOutputDeduplicatesAndResumes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JCODE_SCRATCH_DIR", root)
	dir := filepath.Join(root, "irbench")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('pipeline_hash','expected'),('pipeline_query_prefix','query: ')"); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "input.json")
	raw, _ := json.Marshal([]message{{ID: 1, Split: "development", Message: "quartz"}, {ID: 2, Split: "development", Message: " quartz "}, {ID: 3, Split: "development", Message: recall.Marker + " quartz"}, {ID: 4, Split: "heldout", Message: "different"}})
	if err = os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "query-cache.json")
	em := &countingEmbedder{}
	if err = prepareQueryCache(context.Background(), db, em, input, "development", path); err != nil {
		t.Fatal(err)
	}
	if em.calls != 1 || len(em.texts) != 1 || em.texts[0] != "query: quartz" {
		t.Fatalf("unexpected embedding workload: %+v", em)
	}
	if err = prepareQueryCache(context.Background(), db, em, input, "development", path); err != nil {
		t.Fatal(err)
	}
	if em.calls != 1 {
		t.Fatal("cache restart repeated embedding")
	}
	cache, err := loadQueryCache(db, em.Model(), path)
	if err != nil || len(cache.Queries) != 1 {
		t.Fatalf("bad cache: %+v %v", cache, err)
	}
	if _, err = os.Stat(path + ".partial"); !os.IsNotExist(err) {
		t.Fatal("atomic write left partial file")
	}
}
func TestQueryCachePipelineMismatchFailsClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JCODE_SCRATCH_DIR", root)
	dir := filepath.Join(root, "irbench")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('pipeline_hash','expected'),('pipeline_query_prefix','prefix')"); err != nil {
		t.Fatal(err)
	}
	cache := queryCache{Pipeline: "wrong", Prefix: "prefix", Model: "fake", Queries: map[string]queryVectors{}}
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cache.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadQueryCache(db, "fake", path); err == nil {
		t.Fatal("accepted wrong pipeline")
	}
	cache.Pipeline = "expected"
	raw, _ = json.Marshal(cache)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadQueryCache(db, "wrongmodel", path); err == nil {
		t.Fatal("accepted wrong model")
	}
	if _, err = loadQueryCache(db, "fake", path); err != nil {
		t.Fatal(err)
	}
	if queryKey("  quartz  ") != queryKey("quartz") {
		t.Fatal("query normalization differs from engine")
	}
}
