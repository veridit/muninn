package index_test

import (
	"context"
	"database/sql"
	"fmt"
	"math"
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

// bruteNeighbors recomputes every active unit's top-K neighbour sims from scratch.
func bruteNeighbors(t *testing.T, db *store.DB) map[int64][]float32 {
	t.Helper()
	rows, err := db.Query(`SELECT u.id, e.qvec FROM units u JOIN embeddings e
	  ON e.content_hash=u.content_hash AND e.pipeline_hash=(SELECT value FROM meta WHERE key='pipeline_hash')
	  WHERE u.active=1`)
	if err != nil {
		t.Fatal(err)
	}
	type v struct {
		id int64
		q  []byte
	}
	var vs []v
	for rows.Next() {
		var x v
		rows.Scan(&x.id, &x.q)
		vs = append(vs, x)
	}
	rows.Close()
	out := map[int64][]float32{}
	for _, a := range vs {
		var sims []float32
		for _, b := range vs {
			if a.id != b.id {
				sims = append(sims, embed.DotQ(a.q, b.q))
			}
		}
		sort.Slice(sims, func(i, j int) bool { return sims[i] > sims[j] })
		if len(sims) > index.NeighborK {
			sims = sims[:index.NeighborK]
		}
		out[a.id] = sims
	}
	return out
}

func storedNeighbors(t *testing.T, db *store.DB) (map[int64][]float32, map[int64]map[int64]float32) {
	t.Helper()
	rows, err := db.Query(`SELECT unit_id, nbr_id, sim FROM neighbors`)
	if err != nil {
		t.Fatal(err)
	}
	sims := map[int64][]float32{}
	edges := map[int64]map[int64]float32{}
	for rows.Next() {
		var u, n int64
		var s float64
		rows.Scan(&u, &n, &s)
		sims[u] = append(sims[u], float32(s))
		if edges[u] == nil {
			edges[u] = map[int64]float32{}
		}
		edges[u][n] = float32(s)
	}
	rows.Close()
	for u := range sims {
		sort.Slice(sims[u], func(i, j int) bool { return sims[u][i] > sims[u][j] })
	}
	return sims, edges
}

// checkNeighbors asserts the incrementally maintained graph equals a
// from-scratch recomputation: same top-K similarity multiset per unit, every
// stored edge points at an active unit, and every stored sim is current.
func checkNeighbors(t *testing.T, db *store.DB, round int) {
	t.Helper()
	want := bruteNeighbors(t, db)
	got, edges := storedNeighbors(t, db)
	active := map[int64]bool{}
	for id := range want {
		active[id] = true
	}
	for u := range got {
		if !active[u] {
			t.Fatalf("round %d: stale list for inactive/removed unit %d", round, u)
		}
	}
	var qv = map[int64][]byte{}
	rows, _ := db.Query(`SELECT u.id, e.qvec FROM units u JOIN embeddings e ON e.content_hash=u.content_hash`)
	for rows.Next() {
		var id int64
		var q []byte
		rows.Scan(&id, &q)
		qv[id] = q
	}
	rows.Close()
	for u, ns := range edges {
		for n, s := range ns {
			if !active[n] {
				t.Fatalf("round %d: unit %d keeps neighbour %d which is not active", round, u, n)
			}
			if cur := embed.DotQ(qv[u], qv[n]); math.Abs(float64(cur-s)) > 1e-4 {
				t.Fatalf("round %d: stale sim %d->%d stored %.4f current %.4f", round, u, n, s, cur)
			}
		}
	}
	for u, w := range want {
		g := got[u]
		if len(g) != len(w) {
			t.Fatalf("round %d: unit %d has %d neighbours, want %d\n got %v\nwant %v\nedges %v", round, u, len(g), len(w), g, w, edges[u])
		}
		for i := range w {
			if math.Abs(float64(g[i]-w[i])) > 1e-4 {
				t.Fatalf("round %d: unit %d neighbour %d sim %.4f, want %.4f\n got %v\nwant %v", round, u, i, g[i], w[i], g, w)
			}
		}
	}
}

func TestNeighborsMatchBruteForceUnderChurn(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { neighborChurn(t, seed, 0) })
	}
}

// Hooks index with a small MaxEmbed, so a unit can be re-offered to a list
// that already holds it. 2026-11-06 this produced duplicate (unit,nbr) rows
// and a UNIQUE failure that broke recall on the live DB.
func TestNeighborsPartialPassesNoDuplicateEdges(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { neighborChurn(t, seed, 1+int(seed)%3) })
	}
}

func neighborChurn(t *testing.T, seed int64, maxEmbed int) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	native := filepath.Join(dir, "native.json")
	r := rand.New(rand.NewSource(seed))
	now := time.Now().UTC()
	var mems []ingest.NativeMemory
	for i := 0; i < 40; i++ {
		mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("mem_%03d", i), Content: synthetic(r, i), Active: true, Scope: "global", Created: now, Updated: now})
	}
	ix := &index.Indexer{DB: db, Embedder: &testutil.FakeEmbedder{Dim: 64}}
	ctx := context.Background()
	save := func() {
		time.Sleep(5 * time.Millisecond) // distinct mtime
		if err := ingest.SaveNative(native, mems); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if _, err := ix.Run(ctx, index.Options{NativePath: native}); err != nil {
		t.Fatal(err)
	}
	checkNeighbors(t, db, 0)
	next := len(mems)
	for round := 1; round <= 25; round++ {
		for k := 0; k < 1+r.Intn(4); k++ {
			switch op := r.Intn(4); {
			case op == 0 && len(mems) > 20: // remove
				i := r.Intn(len(mems))
				mems = append(mems[:i], mems[i+1:]...)
			case op == 1: // change text
				i := r.Intn(len(mems))
				mems[i].Content = synthetic(r, 1000+round*10+k)
			case op == 2: // forget / revive
				i := r.Intn(len(mems))
				mems[i].Active = !mems[i].Active
			default: // add
				mems = append(mems, ingest.NativeMemory{ID: fmt.Sprintf("mem_%03d", next), Content: synthetic(r, next), Active: true, Scope: "global", Created: now, Updated: now})
				next++
			}
		}
		save()
		if maxEmbed > 0 {
			// Several capped passes, as consecutive hook runs would do.
			ix.MaxEmbed = maxEmbed
			for p := 0; p < 1+r.Intn(3); p++ {
				if _, err := ix.Run(ctx, index.Options{NativePath: native}); err != nil {
					t.Fatalf("round %d partial pass %d: %v", round, p, err)
				}
				var dups int
				if err := db.QueryRow(`SELECT count(*) FROM (SELECT unit_id, nbr_id FROM neighbors GROUP BY 1,2 HAVING count(*) > 1)`).Scan(&dups); err != nil || dups != 0 {
					t.Fatalf("round %d partial pass %d: %d duplicate edges (%v)", round, p, dups, err)
				}
			}
			// TODO(knn-converge): after capped passes, a full pass must equal
			// brute force too; it does not yet (worker lane). Until then this
			// test asserts the crash invariant only.
			continue
		}
		if _, err := ix.Run(ctx, index.Options{NativePath: native}); err != nil {
			t.Fatal(err)
		}
		checkNeighbors(t, db, round)
		checkUnitCommunities(t, db, round)
	}
	_ = sql.ErrNoRows
}

// checkUnitCommunities asserts the incrementally maintained per-unit community
// counts equal a from-scratch recomputation.
func checkUnitCommunities(t *testing.T, db *store.DB, round int) {
	t.Helper()
	dump := func(q string) string {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var s string
		for rows.Next() {
			var c, u, n int64
			rows.Scan(&c, &u, &n)
			s += fmt.Sprintf("%d/%d=%d ", c, u, n)
		}
		return s
	}
	want := dump(`SELECT c.community_id, uc.unit_id, COUNT(*) FROM unit_concepts uc
	  JOIN communities c ON c.concept_id=uc.concept_id GROUP BY 1, 2 ORDER BY 1, 2`)
	got := dump(`SELECT community_id, unit_id, n FROM unit_communities ORDER BY 1, 2`)
	if got != want {
		t.Fatalf("round %d: unit_communities drifted\n got %s\nwant %s", round, got, want)
	}
}
