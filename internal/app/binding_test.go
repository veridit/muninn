package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
)

func TestBindingHomeAndClientPrecedence(t *testing.T) {
	f := scopetest.New(t)
	alpha := f.Bind(t, "alpha")
	if !alpha.Allows("jcode@alpha:project") || alpha.Allows("personal") || alpha.Allows("engram:frogs") || alpha.Allows("jcode@beta") || alpha.Allows("client:acme") {
		t.Fatal(alpha)
	}
	beta := f.App.Bound(filepath.Join(f.Root, "beta", ".jcode"), f.Cwd)
	if !beta.Allows("jcode@beta") || beta.Allows("jcode@alpha") {
		t.Fatal(beta)
	}
	home, _ := os.UserHomeDir()
	global := f.App.Bound(filepath.Join(home, ".jcode"), f.Cwd)
	for _, scope := range []string{"personal", "engram:frogs", "sql_saga", "global", "jcode@global", "jcode@global:project"} {
		if !global.Allows(scope) {
			t.Fatalf("global lost %s", scope)
		}
	}
	for _, b := range []app.Binding{alpha, global} {
		for _, view := range []app.Binding{b.View(true, nil), b.View(false, []string{"client:*"})} {
			if view.Allows("client:acme") {
				t.Fatal("owner view crossed client boundary")
			}
			if _, err := view.WriteScope("jcode@beta"); err == nil {
				t.Fatal("owner view widened writes")
			}
		}
	}
	b := f.App.Bound(filepath.Join(f.Root, "beta", ".jcode"), f.ClientCwd)
	if b.Client != "acme" || b.DefaultScope != "client:acme" {
		t.Fatal(b)
	}
	for _, scope := range []string{"global", "personal", "jcode@beta", "engram:frogs", "client:other"} {
		if b.Allows(scope) {
			t.Fatal(scope)
		}
		if _, err := b.WriteScope(scope); err == nil {
			t.Fatal("client wrote " + scope)
		}
	}
	p, err := project.Find(f.ClientCwd, false)
	if err != nil || p.Scope() != "client:acme:client-project" {
		t.Fatalf("client project: %+v %v", p, err)
	}
	// No cross-process cache, and no fail-open behavior on an invalid marker.
	if err := os.WriteFile(filepath.Join(f.Root, "client", ".muninn", "client"), []byte("invalid/name\n"), 0600); err != nil {
		t.Fatal(err)
	}
	invalid := f.App.Bound("", f.ClientCwd)
	if invalid.Allows("global") || invalid.Allows("client:acme") || invalid.View(true, nil).Allows("global") {
		t.Fatal("invalid marker failed open")
	}
	if _, err := invalid.WriteScope(""); err == nil {
		t.Fatal("invalid marker allowed write")
	}
	scopetest.Pass(t, "binding.precedence-boundary", "alpha/global/client")
}

func TestImportCannotCrossClientBoundary(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "client")
	imported := []ingest.Imported{{Origin: "matrix-import-origin", Content: "external fixture fact", Scope: "personal", Active: true}}
	if _, err := ingest.MergeImport(app.NativePath(), imported, false); err != nil {
		t.Fatal(err)
	}
	imported[0].Scope = "client:acme"
	imported[0].Content = "attempted cross-boundary overwrite"
	if _, err := b.MergeImport(app.NativePath(), imported, false); err == nil {
		t.Fatal("import crossed boundary")
	}
	imported[0].Origin = "new-origin"
	imported[0].Scope = "personal"
	if _, err := b.MergeImport(app.NativePath(), imported, false); err == nil {
		t.Fatal("client imported personal memory")
	}
	scopetest.Pass(t, "import.boundary", "client")
}

func TestRescopedCanonicalHidesAndSuppressesOldMirror(t *testing.T) {
	f := scopetest.New(t)
	global := f.Bind(t, "global")
	u := f.Units["Personal"]
	if err := os.WriteFile(filepath.Join(f.Root, "mirror.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	src, _, _, err := f.App.DB.Source("jcode", filepath.Join(f.Root, "mirror.json"))
	if err != nil {
		t.Fatal(err)
	}
	mirrorID, err := f.App.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_mirror", Origin: u.Origin, Scope: "global", Body: u.Body, ContentHash: u.ContentHash, Kind: "memory", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	mirror, _ := f.App.DB.GetUnit(mirrorID)
	if mirror.SID != u.SID {
		t.Fatal("fixture mirror identity differs")
	}
	if _, err := global.Rescope(context.Background(), f.App, u.SID, "client:acme", true); err != nil {
		t.Fatal(err)
	}
	if _, err := global.ResolveAll(f.App, "jcode:mem_mirror"); err == nil {
		t.Fatal("old mirror escaped new boundary")
	}
	client := f.App.Bound("", f.ClientCwd)
	out, err := client.Forget(context.Background(), f.App, u.SID)
	if err != nil || !out.Suppressed {
		t.Fatalf("canonical forget: %+v %v", out, err)
	}
	if mirror, _ := f.App.DB.GetUnit(mirrorID); mirror.Active {
		t.Fatal("old mirror not suppressed")
	}
	scopetest.Pass(t, "rescope.canonical-mirror", "client")
}

// 2026-11-06 (user): jcode's per-folder stores belong to their folder. A store
// made in the home or a parent folder must not show in a child project, and a
// project subfolder still sees the project root's store.
func TestGlobalFolderStoresStayInTheirFolder(t *testing.T) {
	f := scopetest.New(t)
	parent := filepath.Dir(f.Cwd)
	sub := filepath.Join(f.Cwd, "deep", "er")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	stores := map[string]string{"Parent": parent, "Here": f.Cwd, "Sub": sub, "Elsewhere": filepath.Join(parent, "elsewhere")}
	for name, dir := range stores {
		if _, _, err := ingest.Remember(app.NativePath(), "Folder store "+name+" quartz note", "fact", "jcode@global:"+harness.JcodeProjectHash(dir), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	sees := func(cwd string) map[string]bool {
		b := f.App.Bound(filepath.Join(home, ".jcode"), cwd)
		out := map[string]bool{}
		for name, dir := range stores {
			out[name] = b.Allows("jcode@global:" + harness.JcodeProjectHash(dir))
		}
		if !b.Allows("jcode@global") || !b.Allows("global") {
			t.Fatalf("global session in %s lost the global store", cwd)
		}
		return out
	}
	for cwd, want := range map[string]map[string]bool{
		parent: {"Parent": true},
		f.Cwd:  {"Here": true},
		sub:    {"Here": true, "Sub": true},
	} {
		got := sees(cwd)
		for name := range stores {
			if got[name] != want[name] {
				t.Errorf("session in %s: store %s visible=%v, want %v", cwd, name, got[name], want[name])
			}
		}
	}
}
