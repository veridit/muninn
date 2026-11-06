package tidy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/store"
)

func TestStrictVerdictRequiredDuplicateAndNullFields(t *testing.T) {
	for _, raw := range []string{`[{"decision":"stay","reason":"x"}]`, `[{"decision":"stay","target":null,"reason":"x"}]`, `[{"decision":"stay","target":"","reason":"x","decision":"move"}]`} {
		if ParseVerdicts(raw, []Entry{{Source: "global"}}, nil)[0].Reason != "invalid judge answer" {
			t.Fatalf("accepted non-strict verdict %s", raw)
		}
	}
}
func TestReadGraphRemapsMirrorEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "muninn.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	upstream, _, _, _ := db.Source("jcode", "upstream")
	native, _, _, _ := db.Source("native", "native")
	from, err := db.UpsertUnit(store.Unit{SourceID: upstream, Key: "from", Scope: "global", Body: "first", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := db.UpsertUnit(store.Unit{SourceID: upstream, Key: "target", Origin: "target-identity", Scope: "jcode@old", Body: "other", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := db.UpsertUnit(store.Unit{SourceID: native, Key: "import", Origin: "target-identity", Scope: "client:new", Body: "other", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO neighbors VALUES(?,?,.9)`, from, mirror); err != nil {
		t.Fatal(err)
	}
	g, err := ReadGraph(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Units) != 2 || len(g.Edges) != 1 || g.Edges[0].To != canonical {
		t.Fatalf("did not resolve mirror endpoint: %+v", g)
	}
}
func TestDiscoverRootsDoesNotCreateMarkers(t *testing.T) {
	// Discovery of a nonexistent location must not initialize project identity.
	missing := filepath.Join(t.TempDir(), "not-created")
	if roots := DiscoverRoots([]string{missing}); len(roots) != 0 {
		t.Fatalf("roots=%v", roots)
	}
}
