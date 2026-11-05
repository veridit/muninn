// Package scopetest provides the shared cross-surface isolation matrix fixture.
package scopetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
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
	for _, d := range []string{f.ClientCwd, filepath.Join(root, "client", ".muninn")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "client", ".muninn", "client"), []byte("acme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "client", ".muninn", "project"), []byte("client-project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mems []ingest.NativeMemory
	for _, row := range []struct{ name, scope string }{
		{"Alpha", "jcode@alpha"}, {"AlphaProject", "jcode@alpha:project"},
		{"Beta", "jcode@beta"}, {"BetaProject", "jcode@beta:project"},
		{"Shared", "global"}, {"Personal", "personal"}, {"JcodeGlobal", "jcode@global"}, {"Engram", "engram:frogs"}, {"Named", "sql_saga"}, {"Project", "project:matrix-project"}, {"Client", "client:acme"}, {"ClientProject", "client:acme:client-project"}, {"OtherClient", "client:other"},
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
	hidden := []string{"Beta", "BetaProject", "OtherClient"}
	if binding == "client" {
		hidden = append(hidden, "Shared", "Project", "Alpha", "AlphaProject", "Personal", "JcodeGlobal", "Engram", "Named")
	} else {
		hidden = append(hidden, "Client", "ClientProject")
		if binding == "alpha" {
			hidden = append(hidden, "Personal", "JcodeGlobal", "Engram", "Named")
		} else {
			hidden = append(hidden, "Alpha", "AlphaProject")
		}
	}
	for _, name := range hidden {
		u := f.Units[name]
		for _, secret := range []string{u.Body, u.SID, u.Key} {
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
		expected = []string{"Client", "ClientProject"}
	} else if binding == "alpha" {
		expected = append(expected, "Alpha", "AlphaProject")
	} else {
		expected = append(expected, "Personal", "JcodeGlobal", "Engram", "Named")
	}
	for _, name := range expected {
		if !strings.Contains(out, f.Units[name].Body) {
			t.Fatalf("%s missing %s:\n%s", binding, name, out)
		}
	}
}
func Pass(t *testing.T, surface, binding string) {
	t.Helper()
	t.Log(fmt.Sprintf("SCOPE MATRIX PASS surface=%s binding=%s", surface, binding))
}

func (f *Fixture) Invalidate(t *testing.T) {
	t.Helper()
	f.Bind(t, "client")
	if err := os.WriteFile(filepath.Join(f.Root, "client", ".muninn", "client"), []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(f.ClientCwd)
}
