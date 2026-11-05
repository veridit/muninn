package ingest

import (
	"path/filepath"
	"testing"
)

func TestEngramPersonalDefaultDoesNotRescopeExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.json")
	old := Imported{Origin: "engram:old-personal", Content: "original fact", Scope: "global", Active: true}
	if _, err := MergeImport(path, []Imported{old}, false); err != nil {
		t.Fatal(err)
	}
	obs := []EngramObservation{{SyncID: "old-personal", Content: "upstream changed fact", Scope: "personal"}, {SyncID: "new-personal", Content: "new fact", Scope: "personal"}, {SyncID: "empty-project", Content: "empty project fact", Scope: "project"}}
	items := EngramItems(obs, EngramOptions{})
	if _, err := MergeImport(path, items, false); err != nil {
		t.Fatal(err)
	}
	mems, err := LoadNative(path)
	if err != nil {
		t.Fatal(err)
	}
	if mems[0].Scope != "global" || mems[0].Content != "upstream changed fact" {
		t.Fatalf("existing row was rescoped: %+v", mems[0])
	}
	for _, m := range mems[1:] {
		if m.Scope != "personal" {
			t.Fatalf("new row: %+v", m)
		}
	}
	st, err := MergeImport(path, items, false)
	if err != nil || st.Added != 0 || st.Updated != 0 {
		t.Fatalf("rerun: %+v %v", st, err)
	}
	t.Log("SCOPE MATRIX PASS surface=engram.personal-default-no-migration binding=global")
}

func TestImportCannotCrossClientAfterBaselineReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.json")
	item := Imported{Origin: "file:stable", Content: "original", Scope: "personal", Active: true}
	if _, err := MergeImport(path, []Imported{item}, false); err != nil {
		t.Fatal(err)
	}
	mems, err := LoadNative(path)
	if err != nil {
		t.Fatal(err)
	}
	mems[0].Scope = "client:acme"
	// A sync may legitimately reset its baseline after an explicit admin move.
	mems[0].OriginHash = originHash(mems[0].Title, mems[0].Content, mems[0].Scope, mems[0].Active)
	if err := SaveNative(path, mems); err != nil {
		t.Fatal(err)
	}
	item.Content = "upstream change"
	if _, err := MergeImport(path, []Imported{item}, false); err == nil {
		t.Fatal("import crossed client boundary")
	}
	got, _ := LoadNative(path)
	if got[0].Scope != "client:acme" || got[0].Content != "original" {
		t.Fatalf("changed despite refusal: %+v", got)
	}
	t.Log("SCOPE MATRIX PASS surface=import.baseline-boundary binding=client")
}
