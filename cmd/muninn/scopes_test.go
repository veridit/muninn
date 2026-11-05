package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/veridit/muninn/internal/scopetest"
)

func captureSurface(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prev := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = prev }()
	err = fn()
	os.Stdout = prev
	if _, e := f.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	if e != nil {
		t.Fatal(e)
	}
	out := string(data)
	if err != nil {
		out += "\n" + err.Error()
	}
	return out, err
}
func cliSurface(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	return captureSurface(t, c.Execute)
}
func TestScopeMatrixCLIAndHooks(t *testing.T) {
	for _, binding := range []string{"alpha", "global", "client"} {
		t.Run(binding, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, binding)
			cwd := f.Cwd
			if binding == "client" {
				cwd = f.ClientCwd
			}
			t.Chdir(cwd)
			for _, tc := range []struct {
				name string
				cmd  *cobra.Command
				args []string
			}{
				{"cli.search", searchCmd(), []string{scopetest.Query, "--limit", "30"}},
				{"cli.recall", recallCmd(), []string{scopetest.Query}},
				{"cli.list", listCmd(), nil},
			} {
				out, err := cliSurface(t, tc.cmd, tc.args...)
				if err != nil {
					t.Fatal(err)
				}
				f.Read(t, binding, out)
				scopetest.Pass(t, tc.name, binding)
			}
			visible := f.Units["Alpha"]
			if binding == "global" {
				visible = f.Units["Personal"]
			}
			if binding == "client" {
				visible = f.Units["Client"]
			}
			for _, ref := range []string{visible.SID, visible.SID[:4], visible.Key} {
				out, err := cliSurface(t, showCmd(), ref)
				if err != nil {
					t.Fatal(err)
				}
				f.Clean(t, binding, out)
				if !strings.Contains(out, visible.Body) {
					t.Fatal(out)
				}
				out, err = cliSurface(t, relatedCmd(), ref)
				if err != nil {
					t.Fatal(err)
				}
				f.Clean(t, binding, out)
				if !strings.Contains(out, "quartz deployment") {
					t.Fatal("empty related result")
				}
			}
			for _, name := range []string{"cli.show", "cli.related"} {
				scopetest.Pass(t, name, binding)
			}
			hidden := f.Units["Beta"]
			for _, tc := range []struct {
				name    string
				factory func() *cobra.Command
			}{{"cli.show", showCmd}, {"cli.related", relatedCmd}, {"cli.forget", forgetCmd}} {
				for _, ref := range []string{hidden.SID, hidden.SID[:4], hidden.Key, f.Units["OtherClient"].SID} {
					out, err := cliSurface(t, tc.factory(), ref)
					if err == nil || !strings.Contains(out, "no visible memory") {
						t.Fatalf("%s accepted hidden id: %s", tc.name, out)
					}
					f.Clean(t, binding, out)
				}
			}
			out, err := cliSurface(t, statsCmd())
			if err != nil {
				t.Fatal(err)
			}
			f.Clean(t, binding, out)
			if strings.Contains(out, "jcode@beta") || strings.Contains(out, "client:other") {
				t.Fatal(out)
			}
			if binding == "global" && (!strings.Contains(out, "engram:frogs") || !strings.Contains(out, "sql_saga") || !strings.Contains(out, "personal")) {
				t.Fatal(out)
			}
			scopetest.Pass(t, "cli.stats", binding)
			out, err = cliSurface(t, rememberCmd(), "matrix new durable fact")
			if err != nil {
				t.Fatal(err)
			}
			f.Clean(t, binding, out)
			res, err := f.App.DB.Query(`SELECT scope FROM units WHERE body='matrix new durable fact'`)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Next() {
				t.Fatal("remember missing")
			}
			var scope string
			res.Scan(&scope)
			res.Close()
			if scope != b.DefaultScope {
				t.Fatalf("default scope %q want %q", scope, b.DefaultScope)
			}
			for _, scope := range []string{"jcode@beta", "client:other"} {
				out, err = cliSurface(t, rememberCmd(), "must not be stored", "--scope", scope)
				if err == nil {
					t.Fatal("cross-scope write succeeded")
				}
				f.Clean(t, binding, out)
			}
			scopetest.Pass(t, "cli.remember", binding)
			for _, hook := range []string{"claude", "codex"} {
				payload, err := os.CreateTemp(t.TempDir(), "payload")
				if err != nil {
					t.Fatal(err)
				}
				// Distinct sessions avoid per-session deduplication between hooks.
				payload.WriteString(`{"session_id":"matrix-` + hook + `","prompt":"` + scopetest.Query + `","cwd":"` + cwd + `"}`)
				payload.Seek(0, 0)
				prev := os.Stdin
				os.Stdin = payload
				out, err := captureSurface(t, func() error { return runHook(hook, 5*time.Second) })
				os.Stdin = prev
				payload.Close()
				if err != nil {
					t.Fatal(err)
				}
				f.Read(t, binding, out)
				scopetest.Pass(t, "hook."+hook, binding)
			}
			// --all / --scope may widen instance views but never client boundaries.
			for _, args := range [][]string{{scopetest.Query, "--all", "--limit", "30"}, {scopetest.Query, "--scope", "client:*", "--limit", "30"}} {
				out, err := cliSurface(t, searchCmd(), args...)
				if err != nil {
					t.Fatal(err)
				}
				if binding == "client" {
					f.Read(t, binding, out)
				} else {
					for _, name := range []string{"Client", "ClientProject", "OtherClient"} {
						if strings.Contains(out, f.Units[name].Body) {
							t.Fatalf("owner override crossed client boundary: %s", out)
						}
					}
				}
			}
			out, err = cliSurface(t, forgetCmd(), visible.SID[:4])
			if err != nil {
				t.Fatal(err)
			}
			f.Clean(t, binding, out)
			if u, err := f.App.DB.Resolve(hidden.SID); err != nil || !u.Active {
				t.Fatal("hidden memory changed")
			}
			scopetest.Pass(t, "cli.forget", binding)
		})
	}
}

func TestCrossClientRequiresExplicitAdminFlag(t *testing.T) {
	f := scopetest.New(t)
	f.Bind(t, "global")
	t.Chdir(f.Cwd)
	id := f.Units["Personal"].SID
	for _, args := range [][]string{{id, "--to", "client:acme"}, {f.Units["Client"].SID, "--to", "personal"}} {
		out, err := cliSurface(t, rescopeCmd(), args...)
		if err == nil || !strings.Contains(out, "requires --cross-client") {
			t.Fatalf("unapproved crossing: %s", out)
		}
	}
	out, err := cliSurface(t, rescopeCmd(), id, "--to", "client:acme", "--cross-client")
	if err != nil {
		t.Fatalf("admin rescope: %s", out)
	}
	u, err := f.App.DB.Resolve(id)
	if err != nil || u.Scope != "client:acme" || u.SID != id {
		t.Fatalf("rescope lost identity: %+v %v", u, err)
	}
	if _, err := f.App.Bound("", f.Cwd).Resolve(f.App, id); err == nil {
		t.Fatal("global sees moved client memory")
	}
	t.Chdir(f.ClientCwd)
	b := f.App.Bound("", f.ClientCwd)
	if _, err := b.Resolve(f.App, id); err != nil {
		t.Fatal(err)
	}
	// No other writer can move or overwrite that identity across the boundary.
	if _, err := f.App.Bound("", f.Cwd).MergeImport("unused", nil, true); err != nil {
		t.Fatal(err)
	}
	_, err = b.Forget(context.Background(), f.App, f.Units["Shared"].SID)
	if err == nil {
		t.Fatal("client forgot global")
	}
	scopetest.Pass(t, "cli.rescope-cross-client", "admin-explicit")
}
