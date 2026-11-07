package tidy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/store"
)

func TestReadGraphEffectiveSourceScopeAndNativeException(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".muninn"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".muninn", "client")
	if err := os.WriteFile(marker, []byte("quartz\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "muninn.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	imported, _, _, _ := db.Source("jcode", filepath.Join(root, "store.json"))
	// A source file symlink outside the root still has the root's provenance.
	physical := filepath.Join(root, "physical.json")
	if err = os.WriteFile(physical, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err = os.Symlink(physical, link); err != nil {
		t.Fatal(err)
	}
	linked, _, _, _ := db.Source("jcode", link)
	linkedID, err := db.UpsertUnit(store.Unit{SourceID: linked, Key: "linked", Scope: "jcode@elsewhere", Body: "synthetic linked note", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	native, _, _, _ := db.Source("native", filepath.Join(root, "native.json"))
	importedID, err := db.UpsertUnit(store.Unit{SourceID: imported, Key: "import", Scope: "jcode@quartz:folder", Body: "synthetic quartz import", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	nativeID, err := db.UpsertUnit(store.Unit{SourceID: native, Key: "native", Scope: "global", Body: "synthetic native note", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	g, err := ReadGraph(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if g.EffectiveScopes[importedID] != "client:quartz" || g.EffectiveScopes[linkedID] != "client:quartz" || g.EffectiveScopes[nativeID] != "global" {
		t.Fatal(g.EffectiveScopes)
	}
	for _, u := range g.Units {
		if u.ID == importedID && u.Scope != "jcode@quartz:folder" {
			t.Fatal("stored scope overwritten")
		}
	}
	// Invalid source-client provenance must not silently revert to stored scope.
	if err = os.WriteFile(marker, []byte("invalid client name"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadGraph(context.Background(), path); err == nil {
		t.Fatal("invalid marker accepted")
	}
}

func TestEffectiveScopesRemovePhantomVotesAndGroupSignals(t *testing.T) {
	g := voteFixture()
	for i := 1; i <= 12; i++ {
		g.Units[i].Scope = "client:acme:child"
	}
	// The apparent outsider is already recalled within acme's client boundary.
	g.EffectiveScopes = map[int64]string{1: "client:acme"}
	p, err := Scan(g, Defaults(), nil)
	if err != nil || len(p.Entries) != 0 {
		t.Fatalf("phantom entries=%+v err=%v", p.Entries, err)
	}
	groups, _ := clientGroups(g, Defaults())
	if len(groups) != 1 || groups[0].Members != 13 || groups[0].Total != 13 {
		t.Fatalf("membership uses stored scope: %+v", groups)
	}
	// Conversely, provenance changes the neighbors' votes and group owner.
	g = voteFixture()
	g.EffectiveScopes = map[int64]string{}
	for _, u := range g.Units {
		if u.ID >= 2 && u.ID <= 13 {
			g.EffectiveScopes[u.ID] = "jcode@garnet:folder"
		}
	}
	p, err = Scan(g, Defaults(), nil)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Target != "jcode@garnet:folder" || p.Entries[0].Strength != "strong" || p.Entries[0].Votes["jcode@garnet"] != 12 || p.Entries[0].Neighbors[0].Scope != "jcode@garnet:folder" {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	if p.Entries[0].Source != "global" || p.Entries[0].Fingerprint != Fingerprint(g.Units[0]) {
		t.Fatal("stored apply identity changed")
	}
}

func TestJudgeRejectsEffectiveSelfTarget(t *testing.T) {
	e := Entry{Source: "jcode@quartz:folder", EffectiveSource: "client:quartz"}
	v := ParseVerdicts(`[{"decision":"move","target":"client:quartz","reason":"same client"}]`, []Entry{e}, []string{"client:quartz"})
	if v[0].Decision != "unsure" {
		t.Fatal(v)
	}
}
