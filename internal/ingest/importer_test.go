package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func strp(s string) *string { return &s }

func TestMergeImportIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.json")
	items := []Imported{
		{Origin: "engram:obs-1", Title: "Tabs", Content: "The project uses tabs.", Scope: "engram:demo", Active: true},
		{Origin: "engram:obs-2", Content: "Deploy on Fridays is forbidden.", Scope: "global", Active: true},
		{Origin: "", Content: "no origin is skipped", Active: true},
	}
	st, err := MergeImport(path, items, false)
	if err != nil || st.Added != 2 || st.Skipped != 1 {
		t.Fatalf("first import: %v %v", st, err)
	}
	before, _ := os.ReadFile(path)
	st, err = MergeImport(path, items, false)
	after, _ := os.ReadFile(path)
	if err != nil || st.Unchanged != 2 || st.Added+st.Updated != 0 || string(before) != string(after) {
		t.Fatalf("second import must change nothing: %v %v", st, err)
	}

	// Upstream edit propagates.
	items[0].Content = "The project uses 4-space indentation."
	if st, _ = MergeImport(path, items, false); st.Updated != 1 {
		t.Fatalf("upstream change: %v", st)
	}
	// A different scope mapping on re-import moves the copy.
	items[0].Scope = "jcode@demo"
	if st, _ = MergeImport(path, items, false); st.Updated != 1 {
		t.Fatalf("scope remap: %v", st)
	}
	// A local edit in muninn wins over a later upstream change.
	mems, _ := LoadNative(path)
	for i := range mems {
		if mems[i].Origin == "engram:obs-2" {
			mems[i].Content = "Deploy on Fridays needs approval (corrected locally)."
		}
	}
	SaveNative(path, mems)
	items[1].Content = "Deploy on Fridays is fine now."
	if st, _ = MergeImport(path, items, false); st.LocalKept != 1 || st.Updated != 0 {
		t.Fatalf("local edit must be kept: %v", st)
	}
	mems, _ = LoadNative(path)
	if len(mems) != 2 || mems[0].ID != importID("engram:obs-1") {
		t.Fatalf("stable ids, no duplicates: %+v", mems)
	}
	// Dry run writes nothing.
	items = append(items, Imported{Origin: "engram:obs-3", Content: "new", Active: true})
	b1, _ := os.ReadFile(path)
	if st, _ = MergeImport(path, items, true); st.Added != 1 {
		t.Fatalf("dry-run counts: %v", st)
	}
	if b2, _ := os.ReadFile(path); string(b1) != string(b2) {
		t.Fatal("dry-run wrote the store")
	}
}

func TestEngramItemsScopes(t *testing.T) {
	obs := []EngramObservation{
		{SyncID: "obs-a", Type: "decision", Title: "t", Content: "c", Project: strp("statbus"), Scope: "project", CreatedAt: "2026-06-03 11:34:21"},
		{SyncID: "obs-b", Type: "preference", Title: "t", Content: "c", Project: strp("seid-lang"), Scope: "personal"},
		{ID: 7, Type: "bug", Title: "t", Content: "c", Project: strp("frogs"), Scope: "project", DeletedAt: strp("2026-07-01 00:00:00")},
		{SyncID: "obs-d", Type: "manual", Title: "t", Content: "c", Scope: "project"},
	}
	got := EngramItems(obs, EngramOptions{ProjectScopes: map[string]string{"statbus": "jcode@statbus"}})
	want := []struct {
		origin, scope string
		active        bool
	}{
		{"engram:obs-a", "jcode@statbus", true},
		{"engram:obs-b", "personal", true},
		{"engram:id-7", "engram:frogs", false},
		{"engram:obs-d", "personal", true},
	}
	for i, w := range want {
		if got[i].Origin != w.origin || got[i].Scope != w.scope || got[i].Active != w.active {
			t.Errorf("%d: got %s %s %v, want %+v", i, got[i].Origin, got[i].Scope, got[i].Active, w)
		}
	}
	if got[0].Created.IsZero() || got[0].Category != "decision" {
		t.Errorf("time/category not mapped: %+v", got[0])
	}
}

func TestJcodeImportItemsKeyByScopeAndID(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "projects"), 0o700)
	os.WriteFile(filepath.Join(dir, "projects", "p1.json"),
		[]byte(`{"memories":{"m1":{"id":"m1","content":"x","category":"fact"}}}`), 0o600)
	items, err := JcodeImportItems(dir, "jcode@acme")
	if err != nil || len(items) != 1 || items[0].Origin != "jcode@acme:p1/m1" || items[0].Scope != "jcode@acme:p1" {
		t.Fatalf("%+v %v", items, err)
	}
}
