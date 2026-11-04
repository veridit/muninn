package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseZshInstances(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(home, ".zshrc")
	os.WriteFile(rc, []byte(`export FOO=1
# Separate jcode instances
typeset -gA JCODE_INSTANCES=(
  brovs   /work/Brovs
  games   "$HOME/Projects/Games"   # trailing comment
  seid    ~/src/seid
)
_jcode_run() { echo "(not a registry)"; }
`), 0o600)
	got, err := ParseZshInstances(rc, home)
	if err != nil {
		t.Fatal(err)
	}
	want := []JcodeInstance{
		{"brovs", "/work/Brovs", "/work/Brovs/.jcode"},
		{"games", home + "/Projects/Games", home + "/Projects/Games/.jcode"},
		{"seid", home + "/src/seid", home + "/src/seid/.jcode"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("instance %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	// One-line form.
	os.WriteFile(rc, []byte(`typeset -A JCODE_INSTANCES=( a /x b /y )`+"\n"), 0o600)
	got, _ = ParseZshInstances(rc, home)
	if len(got) != 2 || got[1].Home != "/y/.jcode" {
		t.Fatalf("one-line: %+v", got)
	}
}

func TestDiscoverJcodeSkipsMissingAndDedupes(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".jcode"), 0o700)
	os.MkdirAll(filepath.Join(home, "p1", ".jcode"), 0o700)
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte("typeset -gA JCODE_INSTANCES=(\n p1 $HOME/p1\n gone $HOME/missing\n)\n"), 0o600)
	got := DiscoverJcode(home, []string{filepath.Join(home, "p1", ".jcode")})
	if len(got) != 2 || got[0].Name != "global" || got[1].Name != "p1" {
		t.Fatalf("got %+v", got)
	}
	if InstanceForHome(got, filepath.Join(home, "p1", ".jcode")).Name != "p1" {
		t.Fatal("InstanceForHome")
	}
}
