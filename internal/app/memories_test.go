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

func TestHarnessAndImportShareSIDAndNativeIsCanonical(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MUNINN_HOME", home)
	t.Setenv("MUNINN_NO_EMBED", "1")
	a, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	dir := filepath.Join(home, "harness-memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "global.json")
	original := []byte(`{"graph_version":1,"memories":{"mem_shared":{"id":"mem_shared","content":"quartz banana policy upstream","active":true}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := a.Indexer.IndexJcode(ctx, dir, "jcode@test", &index.Stats{}); err != nil {
		t.Fatal(err)
	}
	items, err := ingest.JcodeImportItems(dir, "jcode@test")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("import items: %+v", items)
	}
	if _, err := ingest.MergeImport(NativePath(), items, false); err != nil {
		t.Fatal(err)
	}
	mems, err := ingest.LoadNative(NativePath())
	if err != nil {
		t.Fatal(err)
	}
	mems[0].Content = "quartz banana policy corrected locally"
	if err := ingest.SaveNative(NativePath(), mems); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Indexer.IndexNative(ctx, NativePath(), &index.Stats{}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.DB.ResolveAll("jcode:mem_shared")
	if err != nil || len(rows) != 2 {
		t.Fatalf("resolved twins: %+v %v", rows, err)
	}
	if len(rows[0].SID) != 6 || rows[0].SID != rows[1].SID {
		t.Fatalf("same identity received different ids: %+v", rows)
	}
	if rows[0].Body != mems[0].Content || !a.DB.IsCanonical(rows[0]) || a.DB.IsCanonical(rows[1]) {
		t.Fatalf("native not canonical: %+v", rows)
	}
	for _, ref := range []string{rows[0].SID, rows[0].SID[:4], rows[0].Key, "jcode:mem_shared"} {
		got, err := a.DB.ResolveAll(ref)
		if err != nil || len(got) != 2 || got[0].ID != rows[0].ID {
			t.Fatalf("twins through %s: %+v %v", ref, got, err)
		}
	}
	a.Recall.Options = search.DefaultOptions()
	a.Recall.Options.MinScore = 0
	a.Recall.MinSemantic, a.Recall.RelativeFloor = 0, 0
	resp, err := a.Recall.Recall(ctx, recall.Request{Message: "quartz banana policy"})
	if err != nil || len(resp.Hits) != 1 || resp.Hits[0].Source != "native" || resp.Hits[0].Text != mems[0].Content {
		t.Fatalf("canonical recall: %+v %v", resp, err)
	}
	out, err := a.Forget(ctx, rows[0].SID)
	if err != nil || !out.Suppressed || !strings.Contains(out.Message, "forgot") {
		t.Fatalf("forget twins: %+v %v", out, err)
	}
	resp, err = a.Recall.Recall(ctx, recall.Request{Message: "quartz banana policy"})
	if err != nil || len(resp.Hits) != 0 {
		t.Fatalf("twins revived: %+v %v", resp, err)
	}
	mems, err = ingest.LoadNative(NativePath())
	if err != nil || len(mems) != 1 || mems[0].Active {
		t.Fatalf("native not retired: %+v %v", mems, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("harness file changed: %s %v", got, err)
	}
	t.Logf("shared identity sid=%s rows=%d recall_hits=1 canonical=native after_forget_hits=%d", rows[0].SID, len(rows), len(resp.Hits))
}
