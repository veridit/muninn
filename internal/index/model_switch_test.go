package index_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

// Switching the embedding model under a hook-sized budget must never leave
// search without vectors: the old pipeline stays active until every vector of
// the new one exists, and only then are the neighbour lists rebuilt.
func TestModelSwitchIsGradualAndSearchStaysSemantic(t *testing.T) {
	dir := t.TempDir()
	native := filepath.Join(dir, "native.json")
	now := time.Now().UTC()
	var mems []ingest.NativeMemory
	for i := 0; i < 100; i++ {
		mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("m%03d", i), Content: fmt.Sprintf("note %d about topic%d", i, i%7),
			Active: true, Scope: "global", Created: now, Updated: now})
	}
	mems = append(mems, ingest.NativeMemory{ID: "cake", Content: "Bake banana cake at 175 degrees", Active: true, Scope: "global", Created: now, Updated: now})
	ingest.SaveNative(native, mems)
	db, _ := store.Open(filepath.Join(dir, "m.db"))
	defer db.Close()
	ctx := context.Background()
	o := index.Options{NativePath: native}
	old := &testutil.FakeEmbedder{Name: "model-a"}
	if _, err := (&index.Indexer{DB: db, Embedder: old}).Run(ctx, o); err != nil {
		t.Fatal(err)
	}
	pipeA := db.GetMeta("pipeline_hash")

	neu := &testutil.FakeEmbedder{Name: "model-b"}
	ix := &index.Indexer{DB: db, Embedder: neu, MaxEmbed: 40}
	st, err := ix.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Embedded != 40 || st.EmbedPending != 61 || db.GetMeta("pipeline_hash") != pipeA {
		t.Fatalf("budgeted pass: %v pipeline switched early=%v", st, db.GetMeta("pipeline_hash") != pipeA)
	}
	// Search still runs semantically on model A's vectors mid-migration.
	res, err := (&search.Searcher{DB: db, Embedder: old}).Search(ctx, "pastry dessert", search.DefaultOptions())
	if err != nil || len(res) == 0 || res[0].Unit.Key != "cake" || res[0].Semantic <= 0 {
		t.Fatalf("mid-migration search: %+v %v", res, err)
	}
	ix.MaxEmbed = 0
	if st, _ = ix.Run(ctx, o); st.Embedded != 61 || st.EmbedPending != 0 || db.GetMeta("pipeline_hash") == pipeA {
		t.Fatalf("completing pass: %v", st)
	}
	var n int
	db.QueryRow(`SELECT COUNT(DISTINCT unit_id) FROM neighbors`).Scan(&n)
	if n != 101 {
		t.Fatalf("neighbour lists after switch: %d of 101", n)
	}
	checkNeighbors(t, db, 99)
}
