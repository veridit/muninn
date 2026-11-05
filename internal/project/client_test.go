package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientRootWalkNearestAndInvalid(t *testing.T) {
	root := t.TempDir()
	if physical, e := filepath.EvalSymlinks(root); e == nil {
		root = physical
	}
	child := filepath.Join(root, "work", "nested")
	for _, dir := range []string{filepath.Join(root, ".muninn"), filepath.Join(root, "work", ".muninn"), child} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, ".muninn", "client")
	if err := os.WriteFile(marker, []byte("  acme \n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An ordinary project directory must not shadow the parent's client marker.
	if err := os.WriteFile(filepath.Join(root, "work", ".muninn", "project"), []byte("nested-project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, got, err := ClientRoot(child)
	if err != nil || name != "acme" || got != root {
		t.Fatalf("ancestor: %q %q %v", name, got, err)
	}
	p, err := Find(child, false)
	if err != nil || p.Scope() != "client:acme:nested-project" {
		t.Fatalf("project scope: %+v %v", p, err)
	}
	nested := filepath.Join(root, "work", ".muninn", "client")
	if err := os.WriteFile(nested, []byte("other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name, got, err = ClientRoot(child)
	if err != nil || name != "other" || got != filepath.Join(root, "work") {
		t.Fatalf("nearest: %q %q %v", name, got, err)
	}
	for _, invalid := range []string{"", "  \n", "invalid/name", "two\nlines", "name = \"acme\""} {
		if err := os.WriteFile(nested, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ClientRoot(child); err == nil || !strings.Contains(err.Error(), "invalid client name") {
			t.Fatalf("invalid %q accepted: %v", invalid, err)
		}
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	name, _, err = ClientRoot(link)
	if err != nil || name != "acme" {
		t.Fatalf("symlink bypass: %q %v", name, err)
	}
	t.Log("SCOPE MATRIX PASS surface=client-marker.walk-nearest-invalid-symlink binding=client")
}
