package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/tidy"
)

func TestTidyCLIDryRunNoWrites(t *testing.T) {
	f := scopetest.New(t)
	u := f.Units["Shared"]
	p := tidy.Plan{Version: 1, Database: config.DBPath(), Entries: []tidy.Entry{{SID: u.SID, Origin: u.Origin, Fingerprint: tidy.Fingerprint(u), Source: u.Scope, Target: "client:other", Decision: "move"}}}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := saveTidyPlan(path, p); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	dbBefore, err := os.ReadFile(config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	cmd := tidyCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"apply", path, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(app.NativePath())
	dbAfter, _ := os.ReadFile(config.DBPath())
	if !bytes.Equal(before, after) || !bytes.Equal(dbBefore, dbAfter) {
		t.Fatal("dry CLI modified store")
	}
	if _, err := os.Stat(filepath.Join(config.DataDir(), "tidy")); !os.IsNotExist(err) {
		t.Fatal("dry CLI created backup dir")
	}
	if !strings.Contains(out.String(), "Dry run: 1 moves, no writes.") {
		t.Fatal(out.String())
	}
}
func TestTidyCLIPlanFilesPrivateAndNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	p := tidy.Plan{Version: 1}
	if err := saveTidyPlan(path, p); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".md"} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Fatal("plan not private")
		}
	}
	if err := saveTidyPlan(path, p); err == nil {
		t.Fatal("overwrote existing plan")
	}
	if !backgroundCommands["tidy"] {
		t.Fatal("tidy not throttled")
	}
}
