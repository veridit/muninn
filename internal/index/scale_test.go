package index_test

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

var vocab = []string{"deploy", "staging", "build", "cache", "index", "query", "banana", "kernel", "editor",
	"review", "branch", "commit", "schema", "migration", "latency", "token", "budget", "browser", "socket",
	"thread", "memory", "pointer", "release", "config", "secret", "network", "proxy", "docker", "postgres", "search"}

func synthetic(r *rand.Rand, i int) string {
	n := 8 + r.Intn(14)
	s := fmt.Sprintf("Note %d:", i)
	for j := 0; j < n; j++ {
		s += " " + vocab[r.Intn(len(vocab))]
	}
	return s + "."
}

// TestScaleIncremental indexes N synthetic memories cold, then measures a
// one-memory change. Run with -run Scale -v; skipped with -short.
func TestScaleIncremental(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	const n = 2000
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	native := filepath.Join(dir, "native.json")
	r := rand.New(rand.NewSource(7))
	mems := make([]ingest.NativeMemory, n)
	now := time.Now().UTC()
	for i := range mems {
		mems[i] = ingest.NativeMemory{ID: fmt.Sprintf("mem_%06d", i), Content: synthetic(r, i), Active: true, Scope: "global", Created: now, Updated: now}
	}
	if err := ingest.SaveNative(native, mems); err != nil {
		t.Fatal(err)
	}
	fe := &testutil.FakeEmbedder{Dim: 256}
	ix := &index.Indexer{DB: db, Embedder: fe}
	ctx := context.Background()

	t0 := time.Now()
	st, err := ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(t0)

	time.Sleep(10 * time.Millisecond)
	mems[123].Content = "Note 123: banana deploy through staging with a fresh cache."
	mems[123].Updated = time.Now().UTC()
	if err := ingest.SaveNative(native, mems); err != nil {
		t.Fatal(err)
	}
	before := fe.Calls.Load()
	t1 := time.Now()
	st2, err := ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	warm := time.Since(t1)
	embeddedForChange := fe.Calls.Load() - before

	t2 := time.Now()
	st3, _ := ix.Run(ctx, index.Options{NativePath: native})
	noop := time.Since(t2)

	s := &search.Searcher{DB: db, Embedder: fe}
	o := search.DefaultOptions()
	o.Limit = 5
	t3 := time.Now()
	const q = 20
	for i := 0; i < q; i++ {
		if _, err := s.Search(ctx, "banana deploy staging cache", o); err != nil {
			t.Fatal(err)
		}
	}
	perQuery := time.Since(t3) / q

	t.Logf("cold   %d memories: %v (embedded %d)", n, cold.Round(time.Millisecond), st.Embedded)
	t.Logf("change 1 memory:    %v (changed %d, unchanged %d, embedded %d)", warm.Round(time.Millisecond), st2.Changed, st2.Unchanged, embeddedForChange)
	t.Logf("no-op pass:         %v (sources skipped %d)", noop.Round(time.Microsecond), st3.SourcesSkipped)
	t.Logf("search (warm):      %v per query over %d memories", perQuery.Round(time.Microsecond), n)
	if st2.Changed != 1 || st2.Unchanged != n-1 || embeddedForChange != 1 {
		t.Fatalf("incremental pass did extra work: %v", st2)
	}
}
