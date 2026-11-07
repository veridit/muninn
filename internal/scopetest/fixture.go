// Package scopetest provides the shared cross-surface isolation matrix fixture.
package scopetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
)

const Query = "quartz deployment policy"

type Fixture struct {
	App                  *app.App
	Root, Cwd, ClientCwd string
	Units                map[string]store.Unit
}

func New(t *testing.T) *Fixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MUNINN_HOME", filepath.Join(root, "muninn"))
	t.Setenv("MUNINN_NO_EMBED", "1")
	t.Setenv("JCODE_HOME", "")
	cfg := config.Defaults()
	cfg.Embedder = "none"
	cfg.MaxResults = 30
	cfg.RelativeFloor = 0
	for _, name := range []string{"alpha", "beta", "global"} {
		h := filepath.Join(root, name, ".jcode")
		if err := os.MkdirAll(h, 0700); err != nil {
			t.Fatal(err)
		}
		cfg.JcodeHomes = append(cfg.JcodeHomes, h)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	f := &Fixture{Root: root, Cwd: filepath.Join(root, "work"), ClientCwd: filepath.Join(root, "client", "subdir"), Units: map[string]store.Unit{}}
	if err := os.MkdirAll(filepath.Join(f.Cwd, ".muninn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Cwd, ".muninn", "project"), []byte("matrix-project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(f.ClientCwd, ".muninn"), filepath.Join(root, "client", ".muninn")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "client", ".muninn", "client"), []byte("acme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ClientCwd, ".muninn", "project"), []byte("client-project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mems []ingest.NativeMemory
	for _, row := range []struct{ name, scope string }{
		{"Alpha", "jcode@alpha"}, {"AlphaProject", "jcode@alpha:project"},
		{"Beta", "jcode@beta"}, {"BetaProject", "jcode@beta:project"},
		{"Shared", "global"}, {"Personal", "personal"}, {"JcodeGlobal", "jcode@global"}, {"Engram", "engram:frogs"}, {"Named", "sql_saga"}, {"Project", "project:matrix-project"}, {"OtherProject", "project:other-project"}, {"Client", "client:acme"}, {"ClientProject", "client:acme:client-project"}, {"OtherClient", "client:other"},
	} {
		mems = append(mems, ingest.NativeMemory{ID: "mem_matrix_" + row.name, Content: "Matrix(" + row.name + ") quartz deployment policy marker", Category: "fact", Scope: row.scope, Active: true, Created: time.Now(), Updated: time.Now()})
	}
	if err := ingest.SaveNative(app.NativePath(), mems); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.App = a
	t.Cleanup(func() { a.Close() })
	if _, err := a.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, m := range mems {
		u, err := a.DB.Resolve(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(m.ID, "mem_matrix_")
		f.Units[name] = u
	}
	// Real non-native source rows deliberately retain their old stored scope.
	// All frontends must apply the physical boundary without an id migration.
	for _, row := range []struct{ name, dir, kind, scope string }{
		{"ClientNote", "client", "markdown", "notes"},
		{"OutsideNote", "outside", "markdown", "notes"},
		{"OtherClientNote", "other", "markdown", "notes"},
		{"ClientHarness", "client", "jcode", "jcode@alpha"},
		{"ClientFile", "client", "project", "global"},
	} {
		dir := filepath.Join(root, row.dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if row.dir == "other" {
			if err := os.MkdirAll(filepath.Join(dir, ".muninn"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".muninn", "client"), []byte("other\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(dir, row.name+".md")
		body := "Matrix(" + row.name + ") quartz deployment policy marker"
		if err := os.WriteFile(path, []byte("# "+row.name+"\n"+body), 0600); err != nil {
			t.Fatal(err)
		}
		ix := index.Indexer{DB: a.DB}
		st := index.Stats{}
		var ids []int64
		if row.kind == "markdown" {
			ids, _, err = ix.IndexMarkdown(context.Background(), []string{path}, &st)
		} else {
			var src int64
			src, _, _, err = a.DB.Source(row.kind, path)
			if err == nil {
				ids, _, err = ix.SyncSource(src, []index.Candidate{{Key: "matrix_" + row.name, Body: body, Scope: row.scope, Kind: "memory", Active: true}}, &st)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 {
			t.Fatalf("source %s: ids=%v", row.name, ids)
		}
		u, err := a.DB.GetUnit(ids[0])
		if err != nil {
			t.Fatal(err)
		}
		f.Units[row.name] = u
	}
	for _, u := range f.Units {
		if _, err := a.DB.Exec(`INSERT INTO injections(session,unit_id,content_hash,harness,at_ns) VALUES(?,?,?,?,?)`, "matrix-"+u.Key, u.ID, u.ContentHash, "test", time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		for _, v := range f.Units {
			if u.ID != v.ID {
				if _, err := a.DB.Exec(`INSERT OR REPLACE INTO neighbors(unit_id,nbr_id,sim) VALUES(?,?,?)`, u.ID, v.ID, 0.9); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return f
}
func (f *Fixture) Bind(t *testing.T, name string) app.Binding {
	t.Helper()
	h := ""
	if name == "alpha" {
		h = filepath.Join(f.Root, "alpha", ".jcode")
	}
	cwd := f.Cwd
	if name == "client" {
		cwd = f.ClientCwd
		h = filepath.Join(f.Root, "alpha", ".jcode")
	}
	t.Setenv("JCODE_HOME", h)
	return f.App.Bound("", cwd)
}

// Clean checks real bodies, aliases and sids, not merely the scope label.
func (f *Fixture) Clean(t *testing.T, binding, out string) {
	t.Helper()
	hidden := []string{"Beta", "BetaProject", "OtherClient", "OtherProject"}
	hidden = append(hidden, "OtherClientNote")
	if binding == "client" {
		hidden = append(hidden, "OutsideNote")
		hidden = append(hidden, "Shared", "Project", "Alpha", "AlphaProject", "Personal", "JcodeGlobal", "Engram", "Named")
	} else {
		hidden = append(hidden, "ClientNote", "ClientHarness", "ClientFile")
		hidden = append(hidden, "Client", "ClientProject")
		if binding == "alpha" {
			hidden = append(hidden, "OutsideNote")
			hidden = append(hidden, "Personal", "JcodeGlobal", "Engram", "Named")
		} else {
			hidden = append(hidden, "Alpha", "AlphaProject")
		}
	}
	for _, name := range hidden {
		u := f.Units[name]
		for _, secret := range []string{"Matrix(" + name + ") quartz deployment policy marker", u.SID, u.Key} {
			if strings.Contains(out, secret) {
				t.Fatalf("%s leaked %s (%q):\n%s", binding, name, secret, out)
			}
		}
	}
}
func (f *Fixture) Read(t *testing.T, binding, out string) {
	t.Helper()
	f.Clean(t, binding, out)
	expected := []string{"Shared", "Project"}
	if binding == "client" {
		expected = []string{"Client", "ClientProject", "ClientNote", "ClientHarness", "ClientFile"}
	} else if binding == "alpha" {
		expected = append(expected, "Alpha", "AlphaProject")
	} else {
		expected = append(expected, "Personal", "JcodeGlobal", "Engram", "Named", "OutsideNote")
	}
	for _, name := range expected {
		if !strings.Contains(out, "Matrix("+name+") quartz deployment policy marker") {
			t.Fatalf("%s missing %s:\n%s", binding, name, out)
		}
	}
}
func Pass(t *testing.T, surface, binding string) {
	t.Helper()
	t.Log(fmt.Sprintf("SCOPE MATRIX PASS surface=%s binding=%s", surface, binding))
}

func (f *Fixture) CheckStats(t *testing.T, binding, out string) {
	t.Helper()
	var s app.IndexStats
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"alpha": 4, "global": 7, "client": 5}[binding]
	if s.Units != want || s.Active != want {
		t.Fatalf("%s aggregate leaked or hid rows: units=%d active=%d want=%d", binding, s.Units, s.Active, want)
	}
	if binding == "client" && (s.ByScope["client:acme"] != 4 || s.ByScope["client:acme:client-project"] != 1 || len(s.ByScope) != 2) {
		t.Fatalf("physical source scope not overlaid: %+v", s.ByScope)
	}
}

// HiddenRefs includes independent expected boundary cases and stable aliases.
func (f *Fixture) HiddenRefs(binding string) []string {
	refs := []string{f.Units["Beta"].SID, f.Units["Beta"].SID[:4], f.Units["Beta"].Key, f.Units["OtherClient"].SID}
	names := []string{"OtherClientNote"}
	if binding == "client" {
		names = append(names, "OutsideNote")
	} else {
		names = append(names, "ClientNote", "ClientHarness", "ClientFile")
	}
	for _, name := range names {
		u := f.Units[name]
		refs = append(refs, u.SID, u.SID[:4], fmt.Sprintf("#%d", u.ID))
	}
	return refs
}

func (f *Fixture) Invalidate(t *testing.T) {
	t.Helper()
	f.Bind(t, "client")
	if err := os.WriteFile(filepath.Join(f.Root, "client", ".muninn", "client"), []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.ClientCwd)
}
