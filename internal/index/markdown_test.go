package index_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

func TestMarkdownNotesIncremental(t *testing.T) {
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes")
	os.MkdirAll(filepath.Join(notes, "sub"), 0o700)
	os.MkdirAll(filepath.Join(notes, ".hidden"), 0o700)
	os.MkdirAll(filepath.Join(notes, "node_modules"), 0o700)
	write := func(p, s string) {
		time.Sleep(5 * time.Millisecond)
		if err := os.WriteFile(filepath.Join(notes, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("deploy-guide.md", "# Staging\nAlways deploy to staging first.\n# Production\nProduction deploys need a green CI run.\n")
	write("sub/editor.md", "# Editor\nUse 4-space indentation.\n")
	write(".hidden/secret.md", "# Hidden\nshould not be indexed\n")
	write("node_modules/x.md", "# Vendor\nshould not be indexed\n")
	write("readme.txt", "not markdown\n")

	db, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fe := &testutil.FakeEmbedder{}
	ix := &index.Indexer{DB: db, Embedder: fe}
	ctx := context.Background()
	o := index.Options{NotesRoots: []string{notes}}

	st, err := ix.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 3 || st.SourcesScanned != 2 {
		t.Fatalf("cold: want 3 sections from 2 files (hidden/vendor/txt skipped), got %v", st)
	}

	// Edit one section only: one unit changes, one embedding.
	before := fe.Calls.Load()
	write("deploy-guide.md", "# Staging\nAlways deploy to staging first.\n# Production\nProduction deploys need two approvals.\n")
	st, err = ix.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Changed != 1 || st.Unchanged != 1 || st.SourcesSkipped != 1 || fe.Calls.Load()-before != 1 {
		t.Fatalf("one-section edit: %v (embedded %d)", st, fe.Calls.Load()-before)
	}

	s := &search.Searcher{DB: db, Embedder: fe}
	so := search.DefaultOptions()
	res, err := s.Search(ctx, "production approvals", so)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || !strings.Contains(res[0].Unit.Body, "two approvals") || res[0].Unit.Title != "Production" || res[0].Unit.Kind != "section" {
		t.Fatalf("search after edit: %+v", res)
	}
	// The file label participates: "deploy guide" matches by file name.
	res, _ = s.Search(ctx, "deploy guide", so)
	if len(res) == 0 || !strings.Contains(res[0].Unit.Body, "deploy guide\n") {
		t.Fatalf("file label not searchable: %+v", res)
	}

	// Inserting a line at the top shifts every section's line number (and
	// so its key). Text is unchanged, so the content-addressed vectors are
	// reused: only the new preamble section may be embedded.
	before = fe.Calls.Load()
	write("deploy-guide.md", "intro\n# Staging\nAlways deploy to staging first.\n# Production\nProduction deploys need two approvals.\n")
	st, err = ix.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fe.Calls.Load() - before; got != 1 {
		t.Fatalf("line shift re-embedded %d texts, want 1 (only the new preamble): %v", got, st)
	}

	// Deleting a file removes its sections.
	os.Remove(filepath.Join(notes, "sub", "editor.md"))
	st, err = ix.Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed != 1 {
		t.Fatalf("delete file: %v", st)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM units`).Scan(&n)
	if n != 3 {
		t.Fatalf("units after delete = %d, want 3", n)
	}
}
