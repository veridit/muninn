package index_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

// Synthetic memories only.
var fixture = []struct{ content, category string }{
	{`Trigger word "banana mode": when the user says banana mode, reply in Norwegian, at most 3 lines.`, "preference"},
	{"The project uses 4-space indentation in all source files.", "fact"},
	{"Bake banana cake at 175 degrees for 50 minutes.", "fact"},
	{"Use ripgrep instead of grep for repository searches.", "preference"},
	{"Deploys go through the staging environment before production.", "fact"},
}

func setup(t *testing.T) (*store.DB, *index.Indexer, *testutil.FakeEmbedder, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fe := &testutil.FakeEmbedder{}
	native := filepath.Join(dir, "native.json")
	for _, f := range fixture {
		if _, _, err := ingest.Remember(native, f.content, f.category, "global", nil); err != nil {
			t.Fatal(err)
		}
	}
	return db, &index.Indexer{DB: db, Embedder: fe}, fe, native
}

func TestIncrementalNeverReembedsUnchanged(t *testing.T) {
	db, ix, fe, native := setup(t)
	ctx := context.Background()
	st, err := ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != len(fixture) || st.Embedded != len(fixture) {
		t.Fatalf("cold: %v", st)
	}
	calls := fe.Calls.Load()

	// Second pass with nothing changed: the source is skipped on (mtime,size).
	st, err = ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	if st.SourcesSkipped != 1 || st.Embedded != 0 || fe.Calls.Load() != calls {
		t.Fatalf("no-op pass did work: %v (embed calls %d -> %d)", st, calls, fe.Calls.Load())
	}

	// Add one memory: exactly one unit added and exactly one text embedded.
	time.Sleep(10 * time.Millisecond)
	if _, _, err := ingest.Remember(native, "Lunch is at noon on Fridays.", "fact", "global", nil); err != nil {
		t.Fatal(err)
	}
	st, err = ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 1 || st.Unchanged != len(fixture) || st.Embedded != 1 || fe.Calls.Load() != calls+1 {
		t.Fatalf("one-change pass: %v (embed calls %d -> %d)", st, calls, fe.Calls.Load())
	}

	// Forget one: it leaves recall without re-embedding anything.
	mems, _ := ingest.LoadNative(native)
	if ok, err := ingest.Forget(native, mems[0].ID); !ok || err != nil {
		t.Fatalf("forget: %v %v", ok, err)
	}
	st, err = ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 1 || st.Embedded != 0 {
		t.Fatalf("forget pass: %v", st)
	}
	var active int
	db.QueryRow(`SELECT COUNT(*) FROM units WHERE active=1`).Scan(&active)
	if active != len(fixture) {
		t.Fatalf("active units = %d, want %d", active, len(fixture))
	}
}

func TestIdenticalTextSharesOneVector(t *testing.T) {
	db, ix, fe, native := setup(t)
	ctx := context.Background()
	if _, err := ix.Run(ctx, index.Options{NativePath: native}); err != nil {
		t.Fatal(err)
	}
	// The same text in another scope is a second unit but needs no new vector.
	time.Sleep(10 * time.Millisecond)
	if _, _, err := ingest.Remember(native, fixture[3].content, fixture[3].category, "project-x", nil); err != nil {
		t.Fatal(err)
	}
	before := fe.Calls.Load()
	st, err := ix.Run(ctx, index.Options{NativePath: native})
	if err != nil {
		t.Fatal(err)
	}
	var units, vecs int
	db.QueryRow(`SELECT COUNT(*) FROM units`).Scan(&units)
	db.QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&vecs)
	if st.Added != 1 || fe.Calls.Load() != before || units != len(fixture)+1 || vecs != len(fixture) {
		t.Fatalf("shared vector: %v units=%d vecs=%d embed calls %d -> %d", st, units, vecs, before, fe.Calls.Load())
	}
}

func TestRecallRanksRelevantAndDropsIrrelevant(t *testing.T) {
	db, ix, fe, native := setup(t)
	ctx := context.Background()
	if _, err := ix.Run(ctx, index.Options{NativePath: native}); err != nil {
		t.Fatal(err)
	}
	s := &search.Searcher{DB: db, Embedder: fe}
	o := search.DefaultOptions()
	o.Limit, o.MinScore = 5, 6

	cases := []struct {
		q    string
		want string // substring of the expected top result; "" = expect nothing
	}{
		{"banana mode", "banana mode"},
		{"what indentation does the project use", "4-space"},
		{"how long to bake banana pie", "banana cake"},
		{"write a bash loop", ""},
	}
	for _, c := range cases {
		res, err := s.Search(ctx, c.q, o)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if len(res) != 0 {
				t.Errorf("%q: expected no recall, got %q (%s)", c.q, res[0].Unit.Body, res[0].Explain())
			}
			continue
		}
		if len(res) == 0 {
			t.Errorf("%q: no results", c.q)
			continue
		}
		if !contains(res[0].Unit.Body, c.want) {
			t.Errorf("%q: top = %q (%s), want %q", c.q, res[0].Unit.Body, res[0].Explain(), c.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
