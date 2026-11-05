package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

func TestPhysicalSourceBoundarySemantic(t *testing.T) {
	f := scopetest.New(t)
	e := &testutil.FakeEmbedder{}
	f.App.Searcher.Embedder = e
	pipeline := index.PipelineHash(e.Model())
	for _, u := range f.Units {
		vs, err := e.Embed(context.Background(), []string{u.Body})
		if err != nil {
			t.Fatal(err)
		}
		v := vs[0]
		if err := f.App.DB.PutEmbedding(u.ContentHash, pipeline, len(v), embed.EncodeF32(v), embed.Quantize(v), embed.Sketch(v)); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []string{"alpha", "global", "client"} {
		b := f.Bind(t, binding)
		o := f.App.RecallOptions()
		o.MinScore = 0
		o.Limit = 30
		o.Scopes = b.Scopes
		o.ScopeFor = b.ScopeFor
		rows, err := f.App.Searcher.Search(context.Background(), scopetest.Query, o)
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, r := range rows {
			if !r.SemanticOn || r.Semantic <= 0 {
				t.Fatalf("missing semantic score: %+v", r)
			}
			text.WriteString(r.Unit.Body)
			text.WriteString("\n")
		}
		f.Read(t, binding, text.String())
		scopetest.Pass(t, "source-path.semantic", binding)
	}
}

func TestBindingSourceSnapshotFailsClosed(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	src, _, _, err := f.App.DB.Source("markdown", filepath.Join(f.Root, "outside", "new.md"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.App.DB.UpsertUnit(store.Unit{SourceID: src, Key: "new-source", Scope: "global", Body: "new source", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.App.DB.GetUnit(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Present(u); ok {
		t.Fatal("unknown source provenance allowed")
	}
	where, args := b.SQL("scope", "source_id")
	var n int
	if err := f.App.DB.QueryRow("SELECT COUNT(*) FROM units WHERE id=? AND ("+where+")", append([]any{id}, args...)...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("SQL admitted unknown provenance")
	}
	if _, ok := b.Refresh(f.App).Present(u); !ok {
		t.Fatal("new unrooted source invisible after refresh")
	}
	scopetest.Pass(t, "source-path.snapshot-fail-closed", "global")
}
