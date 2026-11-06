package project

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/ingest"
)

func branchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=synthetic", "GIT_COMMITTER_NAME=synthetic", "GIT_AUTHOR_EMAIL=synthetic@example.invalid", "GIT_COMMITTER_EMAIL=synthetic@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func TestBranchSyncTwoClonesKeepCodeClean(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	if err := os.MkdirAll(remote, 0700); err != nil {
		t.Fatal(err)
	}
	branchGit(t, remote, "init", "--bare", "--initial-branch=main")
	a, b := filepath.Join(base, "alice"), filepath.Join(base, "bob")
	os.MkdirAll(a, 0700)
	gitInit(t, a)
	branchGit(t, a, "remote", "add", "origin", remote)
	branchGit(t, a, "push", "origin", "HEAD:main")
	branchGit(t, base, "clone", remote, b)
	pa, err := Find(a, false)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Find(b, false)
	if err != nil || pa.ID != pb.ID {
		t.Fatalf("identity mismatch %+v %+v %v", pa, pb, err)
	}
	alice, bob := filepath.Join(base, "alice-native.json"), filepath.Join(base, "bob-native.json")
	ingest.Remember(alice, "Quartz releases require a checklist.", "decision", pa.Scope(), nil)
	ingest.Remember(alice, "Private synthetic preference.", "preference", "global", nil)
	secret := "sk-" + strings.Repeat("z", 30)
	ingest.Remember(alice, secret, "fact", pa.Scope(), nil)
	if _, err := SyncBranch(pa, alice, BranchOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := branchGit(t, a, "status", "--porcelain"); got != "" {
		t.Fatalf("code worktree dirty: %s", got)
	}
	if got := branchGit(t, filepath.Dir(pa.File()), "symbolic-ref", "--short", "HEAD"); got != NotesBranch {
		t.Fatalf("wrong branch %s", got)
	}
	data, err := os.ReadFile(pa.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "Private") {
		t.Fatal("export leaked nonproject/secret text")
	}
	if _, err := SyncBranch(pb, bob, BranchOptions{}); err != nil {
		t.Fatal(err)
	}
	ingest.Remember(bob, "Quartz deploys use a signed artifact.", "fact", pb.Scope(), nil)
	if _, err := SyncBranch(pb, bob, BranchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncBranch(pa, alice, BranchOptions{}); err != nil {
		t.Fatal(err)
	}
	lines, err := ReadFile(pa.File())
	if err != nil || len(lines) != 2 {
		t.Fatalf("cross-clone lines=%d err=%v", len(lines), err)
	}
	for _, root := range []string{a, b} {
		if got := branchGit(t, root, "status", "--porcelain"); got != "" {
			t.Fatalf("code worktree dirty: %s", got)
		}
	}
	if got := branchGit(t, a, "ls-tree", "--name-only", NotesBranch); got != "memories.jsonl" {
		t.Fatalf("unexpected branch contents: %s", got)
	}
	t.Log("PROJECT branch: two clones exchange only project memories on muninn-notes, both code worktrees clean")
}

func TestBranchMigrationDryRunIdempotenceAndMarkers(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".muninn"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".muninn/project"), []byte("synthetic-project\n"), 0600)
	os.WriteFile(filepath.Join(root, ".muninn/client"), []byte("synthetic-client\n"), 0600)
	now := time.Now().UTC()
	data, _ := json.Marshal(Line{ID: "shared", Content: "Quartz migrations keep client markers.", Category: "decision", Active: true, Created: now, Updated: now})
	legacy := filepath.Join(root, ".muninn/memories.jsonl")
	os.WriteFile(legacy, append(data, '\n'), 0600)
	branchGit(t, root, "add", ".muninn")
	branchGit(t, root, "commit", "-m", "synthetic legacy")
	p, err := Find(root, false)
	if err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(t.TempDir(), "native.json")
	before := branchGit(t, root, "rev-parse", "HEAD")
	if _, err := SyncBranch(p, native, BranchOptions{}); err == nil {
		t.Fatal("legacy migration was implicit")
	}
	st, err := SyncBranch(p, native, BranchOptions{DryRun: true, Migrate: true})
	if err != nil || !st.Migrated {
		t.Fatalf("dry-run=%+v err=%v", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, NotesDir)); !os.IsNotExist(err) {
		t.Fatal("dry-run created branch mount")
	}
	if _, err := os.Stat(native); !os.IsNotExist(err) {
		t.Fatal("dry-run mutated native store")
	}
	if got := branchGit(t, root, "rev-parse", "HEAD"); got != before {
		t.Fatal("dry-run committed main")
	}
	if _, err := SyncBranch(p, native, BranchOptions{Migrate: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy file remains")
	}
	for name, want := range map[string]string{"project": "synthetic-project\n", "client": "synthetic-client\n"} {
		got, err := os.ReadFile(filepath.Join(root, ".muninn", name))
		if err != nil || string(got) != want {
			t.Fatalf("marker %s changed", name)
		}
	}
	if got := branchGit(t, root, "rev-list", "--count", before+"..HEAD"); got != "1" {
		t.Fatalf("migration made %s code commits", got)
	}
	if got := branchGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("migration dirty main: %s", got)
	}
	after := branchGit(t, root, "rev-parse", "HEAD")
	notesHead := branchGit(t, filepath.Dir(p.File()), "rev-parse", "HEAD")
	st, err = SyncBranch(p, native, BranchOptions{Migrate: true})
	if err != nil || st.Migrated {
		t.Fatalf("migration retry=%+v err=%v", st, err)
	}
	if branchGit(t, root, "rev-parse", "HEAD") != after || branchGit(t, filepath.Dir(p.File()), "rev-parse", "HEAD") != notesHead {
		t.Fatal("idempotent migration added commits")
	}
	t.Log("PROJECT migration: dry-run writes nothing, one code removal commit, branch copy retained, markers preserved, retry idempotent")
}
