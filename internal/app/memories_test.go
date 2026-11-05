package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

func TestHarnessForgetSuppressesRecall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MUNINN_HOME", home)
	t.Setenv("MUNINN_NO_EMBED", "1")
	a, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	path := filepath.Join(home, "jcode.json")
	original := []byte(`{"memories":{"mem_legacy":{"id":"mem_legacy","content":"quartz banana policy","active":true}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	src, _, _, err := a.DB.Source("jcode", path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_legacy", Scope: "global", Body: "quartz banana policy", ContentHash: "hash", Kind: "memory", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	a.Recall.Options = search.DefaultOptions()
	a.Recall.Options.MinScore = 0
	a.Recall.MinSemantic = 0
	a.Recall.RelativeFloor = 0
	before, err := a.Recall.Recall(context.Background(), recall.Request{Message: "quartz banana"})
	if err != nil || len(before.Hits) != 1 {
		t.Fatalf("before: %+v %v", before, err)
	}
	u, _ := a.DB.GetUnit(id)
	out, err := a.Forget(context.Background(), "jcode:mem_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Suppressed || out.ID != u.SID || !strings.Contains(out.Message, "upstream files unchanged") {
		t.Fatalf("suppression must be explicit: %+v", out)
	}
	after, err := a.Recall.Recall(context.Background(), recall.Request{Message: "quartz banana"})
	if err != nil || len(after.Hits) != 0 {
		t.Fatalf("after: %+v %v", after, err)
	}
	// Upstream edits and reindexing must not revive a suppressed identity.
	if err := a.DB.DeleteUnit(id); err != nil {
		t.Fatal(err)
	}
	_, err = a.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_legacy", Scope: "global", Body: "quartz banana revised policy", ContentHash: "hash2", Kind: "memory", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	after, err = a.Recall.Recall(context.Background(), recall.Request{Message: "quartz banana"})
	if err != nil || len(after.Hits) != 0 {
		t.Fatalf("revived: %+v %v", after, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("upstream changed: %s %v", got, err)
	}
}

func TestImportRerunRetainsSIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MUNINN_HOME", home)
	t.Setenv("MUNINN_NO_EMBED", "1")
	a, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	items := []ingest.Imported{{Origin: "engram:one", Content: "one imported durable fact", Active: true}, {Origin: "engram:two", Content: "two imported facts", Active: true}}
	first, err := ingest.MergeImport(NativePath(), items, false)
	if err != nil || first.Added != 2 {
		t.Fatalf("first: %s %v", first, err)
	}
	if _, _, err := a.Indexer.IndexNative(context.Background(), NativePath(), &index.Stats{}); err != nil {
		t.Fatal(err)
	}
	mems, err := ingest.LoadNative(NativePath())
	if err != nil {
		t.Fatal(err)
	}
	sids := map[string]string{}
	for _, m := range mems {
		u, err := a.DB.Resolve(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if u.SID != m.SID {
			t.Fatalf("JSON sid differs: %s vs %s", m.SID, u.SID)
		}
		sids[m.Origin] = u.SID
	}
	second, err := ingest.MergeImport(NativePath(), items, false)
	if err != nil || second.Added != 0 || second.Updated != 0 {
		t.Fatalf("rerun: %s %v", second, err)
	}
	if _, _, err := a.Indexer.IndexNative(context.Background(), NativePath(), &index.Stats{}); err != nil {
		t.Fatal(err)
	}
	mems, err = ingest.LoadNative(NativePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mems {
		u, err := a.DB.Resolve(m.ID)
		if err != nil || m.SID != sids[m.Origin] || u.SID != m.SID {
			t.Fatalf("changed sid: %+v %+v %v", m, u, err)
		}
	}
	t.Logf("import rerun %s; sids unchanged=%d", second, len(sids))
}
