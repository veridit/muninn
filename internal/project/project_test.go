package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/ingest"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"}, {"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
}

func TestIdentitySurvivesMoveAndClone(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "alpha")
	os.MkdirAll(filepath.Join(a, "sub"), 0o755)
	gitInit(t, a)
	p1, err := Find(filepath.Join(a, "sub"), false)
	if err != nil || !strings.HasPrefix(p1.ID, "rc-") || p1.Label != "alpha" {
		t.Fatalf("%+v %v", p1, err)
	}
	moved := filepath.Join(base, "renamed")
	os.Rename(a, moved)
	p2, _ := Find(moved, false)
	clone := filepath.Join(base, "clone")
	if out, err := exec.Command("git", "clone", "-q", moved, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	p3, _ := Find(clone, false)
	if p2.ID != p1.ID || p3.ID != p1.ID {
		t.Fatalf("ids differ: %s %s %s", p1.ID, p2.ID, p3.ID)
	}
}

func TestPlainFolderGetsPinnedID(t *testing.T) {
	d := t.TempDir()
	if _, err := Find(d, false); err != ErrNoProject {
		t.Fatalf("want ErrNoProject, got %v", err)
	}
	p, err := Find(d, true)
	if err != nil || !strings.HasPrefix(p.ID, "id-") {
		t.Fatalf("%+v %v", p, err)
	}
	os.MkdirAll(filepath.Join(d, "deep", "er"), 0o755)
	q, _ := Find(filepath.Join(d, "deep", "er"), false)
	if q.ID != p.ID || q.Root != p.Root {
		t.Fatalf("subdir resolves to %+v, want %+v", q, p)
	}
}

// Two teammates share one repo file through git: each sync is idempotent,
// edits travel both ways, secrets and other scopes never reach the file.
func TestSyncRoundTripBetweenClones(t *testing.T) {
	repo := t.TempDir()
	p := Project{ID: "rc-demo", Label: "demo", Root: repo}
	alice := filepath.Join(t.TempDir(), "alice.json")
	bob := filepath.Join(t.TempDir(), "bob.json")

	ingest.Remember(alice, "Run make check before pushing", "fact", p.Scope(), nil)
	ingest.Remember(alice, "Personal: I prefer dark mode", "preference", "global", nil)
	// Assembled at runtime so the repository's own secret scan stays clean.
	fakeSecret := "API" + "_KEY=" + strings.Repeat("x", 16)
	ingest.Remember(alice, "deploy uses "+fakeSecret+" from the vault", "fact", p.Scope(), nil)
	st, err := Sync(p, alice, false)
	if err != nil || st.Written != 1 || st.SkippedSecret != 1 || !st.FileChanged {
		t.Fatalf("alice first sync: %v %v", st, err)
	}
	b, _ := os.ReadFile(p.File())
	if strings.Contains(string(b), "dark mode") || strings.Contains(string(b), fakeSecret) {
		t.Fatalf("file leaked: %s", b)
	}
	if st, _ = Sync(p, alice, false); st.FileChanged || st.Imported.Added+st.Imported.Updated != 0 {
		t.Fatalf("alice re-sync not idempotent: %v", st)
	}

	// Bob pulls and syncs: he gets the memory, and the file stays identical.
	if st, _ = Sync(p, bob, false); st.Imported.Added != 1 || st.FileChanged {
		t.Fatalf("bob first sync: %v", st)
	}
	// Bob corrects the memory locally and syncs: the file changes.
	mems, _ := ingest.LoadNative(bob)
	for i := range mems {
		if mems[i].Scope == p.Scope() {
			mems[i].Content = "Run make check and make lint before pushing"
		}
	}
	ingest.SaveNative(bob, mems)
	if st, _ = Sync(p, bob, false); !st.FileChanged {
		t.Fatalf("bob edit not written: %v", st)
	}
	// Alice pulls: her copy is updated, nothing duplicated.
	if st, _ = Sync(p, alice, false); st.Imported.Updated != 1 || st.FileChanged {
		t.Fatalf("alice pull: %v", st)
	}
	mems, _ = ingest.LoadNative(alice)
	n := 0
	for _, m := range mems {
		if m.Scope == p.Scope() && m.Active && !LooksSecret(m.Content) {
			n++
			if !strings.Contains(m.Content, "make lint") {
				t.Fatalf("alice has stale text: %q", m.Content)
			}
		}
	}
	if n != 1 {
		t.Fatalf("alice has %d shared memories, want 1", n)
	}
}

func TestReadFileReportsConflictLine(t *testing.T) {
	f := filepath.Join(t.TempDir(), "m.jsonl")
	os.WriteFile(f, []byte(`{"id":"a","content":"x","active":true}`+"\n<<<<<<< HEAD\n"), 0o644)
	if _, err := ReadFile(f); err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("want error at line 2, got %v", err)
	}
}
