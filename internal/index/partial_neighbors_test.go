package index_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

func TestNeighborsRepeatedDirtyOffersAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	native := filepath.Join(dir, "native.json")
	now := time.Now().UTC()
	var mems []ingest.NativeMemory
	for i := 0; i < 6; i++ {
		mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("m%d", i), Content: fmt.Sprintf("synthetic quartz policy %d", i), Active: true, Scope: "global", Created: now, Updated: now})
	}
	ix := &index.Indexer{DB: db, Embedder: &testutil.FakeEmbedder{Dim: 16}}
	run := func() {
		t.Helper()
		if err := ingest.SaveNative(native, mems); err != nil {
			t.Fatal(err)
		}
		if _, err := ix.Run(context.Background(), index.Options{NativePath: native}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	// A repeated source identity, or a replayed frontier, must not produce two
	// offers of the same neighbor in a single batched write.
	m := ingest.NativeMemory{ID: "new", Content: "synthetic quartz new policy", Active: true, Scope: "global", Created: now, Updated: now}
	mems = append(mems, m, m)
	run()
	checkNeighbors(t, db, 1)
	var id int64
	if err := db.QueryRow(`SELECT id FROM units WHERE key='new'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpdateNeighbors([]int64{id, id, id}, nil); err != nil {
		t.Fatal(err)
	}
	checkNeighbors(t, db, 2)
	t.Log("INDEX neighbors: duplicate source/frontier offers replay idempotently and match full recompute")
}

func TestNeighborsRandomPartialEmbeddingPassesMatchRecompute(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "m.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			native := filepath.Join(dir, "native.json")
			now := time.Now().UTC()
			r := rand.New(rand.NewSource(seed))
			var mems []ingest.NativeMemory
			for i := 0; i < 22; i++ {
				mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("m%d", i), Content: synthetic(r, i), Active: true, Scope: "global", Created: now, Updated: now})
			}
			flaky := &interruptedEmbedder{FakeEmbedder: testutil.FakeEmbedder{Dim: 32}}
			ix := &index.Indexer{DB: db, Embedder: flaky}
			o := index.Options{NativePath: native}
			if err := ingest.SaveNative(native, mems); err != nil {
				t.Fatal(err)
			}
			if _, err := ix.Run(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			for round := 1; round <= 5; round++ {
				for i := 0; i < 5; i++ {
					mems[r.Intn(len(mems))].Content = synthetic(r, round*100+i)
				}
				mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("new%d", round), Content: synthetic(r, 1000+round), Active: true, Scope: "global", Created: now, Updated: now})
				if err := ingest.SaveNative(native, mems); err != nil {
					t.Fatal(err)
				}
				for pass := 0; pass < 20; pass++ {
					ix.MaxEmbed = 1 + r.Intn(3)
					flaky.fail = pass%4 == 0
					st, err := ix.Run(context.Background(), o)
					if err != nil {
						t.Fatalf("round=%d pass=%d limit=%d: %v", round, pass, ix.MaxEmbed, err)
					}
					checkNeighbors(t, db, round*100+pass)
					checkNeighborEdgeIdentities(t, db)
					if st.EmbedPending == 0 && st.EmbedErr == nil {
						break
					}
					if pass == 19 {
						t.Fatal("partial indexing never completed")
					}
				}
				ix.MaxEmbed = 0
				flaky.fail = false
				if _, err := ix.Run(context.Background(), o); err != nil {
					t.Fatal(err)
				}
				checkNeighbors(t, db, round*100+99)
				checkNeighborEdgeIdentities(t, db)
			}
		})
	}
	t.Log("INDEX neighbors: random embedding limits 1..3, edits/additions, partial-pass edges equal full recompute")
}

type interruptedEmbedder struct {
	testutil.FakeEmbedder
	fail bool
}

func (e *interruptedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.fail {
		return nil, errors.New("synthetic interrupted embedding batch")
	}
	return e.FakeEmbedder.Embed(ctx, texts)
}

func checkNeighborEdgeIdentities(t *testing.T, db *store.DB) {
	t.Helper()
	type vector struct {
		id int64
		q  []byte
	}
	rows, err := db.Query(`SELECT u.id,e.qvec FROM units u JOIN embeddings e ON e.content_hash=u.content_hash AND e.pipeline_hash=(SELECT value FROM meta WHERE key='pipeline_hash') WHERE u.active=1`)
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	for rows.Next() {
		var v vector
		if err := rows.Scan(&v.id, &v.q); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		vectors = append(vectors, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, edges := storedNeighbors(t, db)
	for _, v := range vectors {
		type neighbor struct {
			id  int64
			sim float32
		}
		var want []neighbor
		for _, other := range vectors {
			if other.id != v.id {
				want = append(want, neighbor{other.id, embed.DotQ(v.q, other.q)})
			}
		}
		sort.Slice(want, func(i, j int) bool {
			if want[i].sim == want[j].sim {
				return want[i].id < want[j].id
			}
			return want[i].sim > want[j].sim
		})
		if len(want) > index.NeighborK {
			want = want[:index.NeighborK]
		}
		if len(edges[v.id]) != len(want) {
			t.Fatalf("unit %d edge count mismatch", v.id)
		}
		for _, n := range want {
			if _, ok := edges[v.id][n.id]; !ok {
				t.Fatalf("unit %d missing recomputed neighbor %d", v.id, n.id)
			}
		}
	}
}
