package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
)

func TestShowListsCanonicalTextAndAllOrigins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MUNINN_HOME", home)
	t.Setenv("MUNINN_NO_EMBED", "1")
	ctx := context.Background()
	a, err := app.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "harness.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	src, _, _, err := a.DB.Source("jcode", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.UpsertUnit(store.Unit{SourceID: src, Key: "mem_shared", Scope: "jcode@test", Body: "old upstream quartz policy", ContentHash: "old", Kind: "memory", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.MergeImport(app.NativePath(), []ingest.Imported{{Origin: "jcode@test/mem_shared", Content: "corrected quartz policy", Scope: "jcode@test", Active: true}}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Indexer.IndexNative(ctx, app.NativePath(), &index.Stats{}); err != nil {
		t.Fatal(err)
	}
	u, err := a.DB.Resolve("jcode:mem_shared")
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	// Run the actual CLI command and capture its regular stdout, not a renderer.
	out, err := os.CreateTemp(t.TempDir(), "show-output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	previous := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = previous }()
	command := showCmd()
	command.SetArgs([]string{u.SID[:4], "--all"})
	err = command.Execute()
	os.Stdout = previous
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"[" + u.SID + "] corrected quartz policy", "source: native", "source: jcode", path, "key: mem_shared", "origin: jcode@test/mem_shared", "mirror text: old upstream quartz policy"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show missing %q:\n%s", want, got)
		}
	}
}
