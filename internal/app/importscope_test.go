package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "root of " + dir}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// 2026-11-06: a WebKit session in the global instance recalled Brovs DevOps
// notes imported from engram as engram:devops. Imported memories must land
// where they were written, as if muninn had written them there.
func TestEngramImportPlacesByDirectory(t *testing.T) {
	f := scopetest.New(t)
	alphaRoot := filepath.Join(f.Root, "alpha")
	devops := filepath.Join(alphaRoot, "DevOps") // a git repo inside the alpha instance root
	gitRepo(t, devops)
	frogs := filepath.Join(f.Root, "frogs") // a git project outside every instance
	gitRepo(t, frogs)
	home, _ := os.UserHomeDir()
	gone := filepath.Join(f.Root, "deleted-since")

	obs := []ingest.EngramObservation{
		{SyncID: "in-instance", Content: "DevOps restart needs nohup quartz", Project: ptr("devops"), Scope: "project", Directory: devops},
		{SyncID: "in-project", Content: "Frogs dev backend quartz restart", Project: ptr("frogs"), Scope: "project", Directory: frogs},
		{SyncID: "at-home", Content: "Home folder quartz note", Project: ptr("bin"), Scope: "project", Directory: home},
		{SyncID: "vanished", Content: "Vanished folder quartz note", Project: ptr("old"), Scope: "project", Directory: gone},
		{SyncID: "mapped", Content: "Mapped quartz note", Project: ptr("statbus"), Scope: "project", Directory: frogs},
	}
	items := ingest.EngramItems(obs, ingest.EngramOptions{ProjectScopes: map[string]string{"statbus": "sql_saga"}, ScopeForDir: f.App.ImportScopeForDir})
	frogsScope, ok := f.App.ImportScopeForDir(frogs)
	if !ok {
		t.Fatal("git project did not resolve")
	}
	want := map[string]string{
		"engram:in-instance": "jcode@alpha", "engram:in-project": frogsScope,
		"engram:at-home": "folder:" + filepath.Clean(home), "engram:vanished": "engram:old", "engram:mapped": "sql_saga",
	}
	for _, it := range items {
		if it.Scope != want[it.Origin] {
			t.Errorf("%s: scope %q, want %q", it.Origin, it.Scope, want[it.Origin])
		}
	}

	// A real import from the global binding writes instance and project
	// scopes the global binding itself could not write.
	global := f.App.Bound(filepath.Join(home, ".jcode"), f.Cwd)
	if _, err := global.MergeImport(app.NativePath(), items, false); err != nil {
		t.Fatalf("import from global: %v", err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	global = global.Refresh(f.App) // sessions refresh their binding per request
	for _, c := range []struct {
		name   string
		b      app.Binding
		scope  string
		allows bool
	}{
		{"global elsewhere", global, "jcode@alpha", false},
		{"global elsewhere", global, frogsScope, false},
		{"global elsewhere", global, "folder:" + filepath.Clean(home), false},
		{"global in frogs", f.App.Bound(filepath.Join(home, ".jcode"), frogs), frogsScope, true},
		{"alpha instance", f.App.Bound(filepath.Join(alphaRoot, ".jcode"), devops), "jcode@alpha", true},
		{"alpha instance", f.App.Bound(filepath.Join(alphaRoot, ".jcode"), devops), frogsScope, false},
	} {
		if got := c.b.Allows(c.scope); got != c.allows {
			t.Errorf("%s: Allows(%s)=%v, want %v", c.name, c.scope, got, c.allows)
		}
	}

	// Re-import is a no-op, and an earlier explicit --map into an instance
	// keeps that place instead of being moved by directory resolution.
	if st, err := global.MergeImport(app.NativePath(), items, false); err != nil || st.Added+st.Updated != 0 {
		t.Fatalf("re-import: %+v %v", st, err)
	}
	pinned := ingest.EngramItems([]ingest.EngramObservation{{SyncID: "pinned", Content: "Pinned quartz", Project: ptr("frogs"), Scope: "project", Directory: frogs}},
		ingest.EngramOptions{ProjectScopes: map[string]string{"frogs": "jcode@beta"}})
	beta := f.App.Bound(filepath.Join(f.Root, "beta", ".jcode"), f.Cwd)
	if _, err := beta.MergeImport(app.NativePath(), pinned, false); err != nil {
		t.Fatal(err)
	}
	placed := ingest.EngramItems([]ingest.EngramObservation{{SyncID: "pinned", Content: "Pinned quartz", Project: ptr("frogs"), Scope: "project", Directory: frogs}},
		ingest.EngramOptions{ScopeForDir: f.App.ImportScopeForDir})
	if st, err := global.MergeImport(app.NativePath(), placed, false); err != nil || st.Updated != 0 {
		t.Fatalf("placement moved an explicitly mapped copy: %+v %v", st, err)
	}
	mems, _ := ingest.LoadNative(app.NativePath())
	for _, m := range mems {
		if m.Origin == "engram:pinned" && m.Scope != "jcode@beta" {
			t.Fatalf("pinned copy rescoped to %s", m.Scope)
		}
	}
}

func ptr(s string) *string { return &s }

// 2026-11-07 (user): imports from folders that are not projects belong to that
// folder. A Sigrun note recalled in an unrelated session triggered this.
func TestFolderImportsStayInTheirFolder(t *testing.T) {
	f := scopetest.New(t)
	plain := filepath.Join(f.Root, "notes", "quartz-pwa") // no git
	sub := filepath.Join(plain, "assets")
	workspace := filepath.Join(f.Root, "workspace") // two repos inside: a parent folder
	gitRepo(t, filepath.Join(workspace, "one"))
	gitRepo(t, filepath.Join(workspace, "two"))
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, workspace} {
		if s, ok := f.App.ImportScopeForDir(dir); !ok || s != "folder:"+dir {
			t.Fatalf("%s placed at %q %v", dir, s, ok)
		}
	}
	obs := []ingest.EngramObservation{
		{SyncID: "plain", Content: "Quartz PWA folder note", Project: ptr("quartz"), Scope: "project", Directory: plain},
		{SyncID: "workspace", Content: "Workspace quartz note", Project: ptr("ws"), Scope: "project", Directory: workspace},
	}
	home, _ := os.UserHomeDir()
	global := f.App.Bound(filepath.Join(home, ".jcode"), f.Cwd)
	if _, err := global.MergeImport(app.NativePath(), ingest.EngramItems(obs, ingest.EngramOptions{ScopeForDir: f.App.ImportScopeForDir}), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cwd, scope string
		allows     bool
	}{
		{plain, "folder:" + plain, true},
		{sub, "folder:" + plain, true},
		{f.Cwd, "folder:" + plain, false},
		{workspace, "folder:" + workspace, true},
		{filepath.Join(workspace, "one"), "folder:" + workspace, false},
	} {
		if got := f.App.Bound(filepath.Join(home, ".jcode"), c.cwd).Allows(c.scope); got != c.allows {
			t.Errorf("session in %s: Allows(%s)=%v, want %v", c.cwd, c.scope, got, c.allows)
		}
	}
}
