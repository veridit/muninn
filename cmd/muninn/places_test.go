package main

import (
	"github.com/veridit/muninn/internal/scopetest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIProjectFlagAtClientRootAndPlainFolder(t *testing.T) {
	f := scopetest.New(t)
	f.Bind(t, "client")
	root := filepath.Join(f.Root, "client")
	t.Chdir(root)
	out, err := cliSurface(t, rememberCmd(), "Synthetic client-root project flag rule.", "--project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("root: %s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".muninn", "project")); !os.IsNotExist(err) {
		t.Fatal("client root acquired a phantom project identity", err)
	}
	f.Bind(t, "alpha")
	plain := filepath.Join(f.Root, "synthetic-project-flag-folder")
	if err := os.MkdirAll(plain, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(plain)
	out, err = cliSurface(t, rememberCmd(), "Synthetic new project flag rule.", "--project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("plain: %s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(plain, ".muninn", "project")); err != nil {
		t.Fatal("explicit project flag did not create identity", err)
	}
}

func TestCLIRememberPlaceAndQuestion(t *testing.T) {
	f := scopetest.New(t)
	f.Bind(t, "global")
	t.Chdir(f.Cwd)
	out, err := cliSurface(t, rememberCmd(), "Synthetic work repository rule.", "--place", "global")
	if err == nil || !strings.Contains(out, "Are you sure it applies beyond it?") {
		t.Fatal(out)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic work repository rule.", "--place", "global", "--place-reason", "Synthetic portable rule.")
	if err != nil || !strings.Contains(out, "place: global") || !strings.Contains(out, "available: here") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic literal precedence rule.", "--place", "global", "--scope", "project:matrix-project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic project flag compatibility rule.", "--project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("%s %v", out, err)
	}
}
