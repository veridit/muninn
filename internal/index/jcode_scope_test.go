package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

func writeJcodeStore(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, "memory", "projects")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(home, "memory", "global.json"),
		[]byte(`{"graph_version":1,"memories":{"g1":{"id":"g1","content":"`+content+` (instance global)","category":"fact","active":true}}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "abc.json"),
		[]byte(`{"graph_version":1,"memories":{"p1":{"id":"p1","content":"`+content+` (project)","category":"fact","active":true}}}`), 0o600)
}

// Two isolated jcode instances with the same project hash must not leak
// into each other's recall scope.
func TestJcodeInstancesAreScopedAndIsolated(t *testing.T) {
	dir := t.TempDir()
	clientA := filepath.Join(dir, "clientA", ".jcode")
	clientB := filepath.Join(dir, "clientB", ".jcode")
	writeJcodeStore(t, clientA, "Client A deploys with blue green rollout")
	writeJcodeStore(t, clientB, "Client B deploys with canary rollout")
	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fe := &testutil.FakeEmbedder{}
	ix := &index.Indexer{DB: db, Embedder: fe}
	o := index.Options{Jcode: []index.JcodeSource{
		{Dir: filepath.Join(clientA, "memory"), Prefix: "jcode@clientA"},
		{Dir: filepath.Join(clientB, "memory"), Prefix: "jcode@clientB"},
	}}
	st, err := ix.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 4 {
		t.Fatalf("want 4 units across 2 instances, got %v", st)
	}
	s := &search.Searcher{DB: db, Embedder: fe}
	so := search.DefaultOptions()
	so.Scopes = []string{"jcode@clientA", "jcode@clientA:*", "global"}
	res, err := s.Search(context.Background(), "deploys rollout", so)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("clientA scope: want 2 hits, got %d", len(res))
	}
	for _, r := range res {
		if r.Unit.Scope != "jcode@clientA" && r.Unit.Scope != "jcode@clientA:abc" {
			t.Fatalf("leak from another instance: %q %q", r.Unit.Scope, r.Unit.Body)
		}
	}
}

// Changing the source recipe relabels existing units without re-embedding.
func TestSourceRecipeBumpRelabelsWithoutReembedding(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "x", ".jcode")
	writeJcodeStore(t, home, "Something worth remembering")
	db, _ := store.Open(filepath.Join(dir, "m.db"))
	defer db.Close()
	fe := &testutil.FakeEmbedder{}
	ix := &index.Indexer{DB: db, Embedder: fe}
	ctx := context.Background()
	if _, err := ix.Run(ctx, index.Options{Jcode: []index.JcodeSource{{Dir: filepath.Join(home, "memory"), Prefix: "old"}}}); err != nil {
		t.Fatal(err)
	}
	// Simulate an index written by an older recipe.
	db.SetMeta("source_recipe", "1")
	before := fe.Calls.Load()
	st, err := ix.Run(ctx, index.Options{Jcode: []index.JcodeSource{{Dir: filepath.Join(home, "memory"), Prefix: "jcode@x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 2 || fe.Calls.Load() != before {
		t.Fatalf("relabel: %v, embedded %d", st, fe.Calls.Load()-before)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM units WHERE scope LIKE 'jcode@x%'`).Scan(&n)
	if n != 2 {
		t.Fatalf("scopes not relabelled: %d", n)
	}
}
