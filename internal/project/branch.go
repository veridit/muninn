package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/veridit/huginn/gitbranch"
	"github.com/veridit/muninn/internal/ingest"
)

type BranchOptions struct{ DryRun, Migrate bool }
type BranchStats struct {
	SyncStats
	Branch, Mount string
	Migrated      bool
}

func (s BranchStats) String() string {
	location := "local folder"
	if s.Branch != "" {
		location = "branch " + s.Branch + " at " + s.Mount
	}
	return fmt.Sprintf("%s | %s | migrated=%v", s.SyncStats, location, s.Migrated)
}

// SyncBranch uses huginn's shared orphan-worktree and push-as-lock protocol.
// Only project memories reach the branch, never the local capture audit.
func SyncBranch(p Project, nativePath string, o BranchOptions) (BranchStats, error) {
	var st BranchStats
	root, gitErr := git(p.Root, "rev-parse", "--show-toplevel")
	isGit := gitErr == nil && filepath.Clean(root) == filepath.Clean(p.Root)
	if isGit {
		st.Branch, st.Mount = NotesBranch, NotesDir
	}
	legacy, err := ReadFile(p.LegacyFile())
	hasLegacy := err == nil
	if err != nil && !os.IsNotExist(err) {
		return st, err
	}
	tracked := false
	if isGit {
		_, err = git(p.Root, "ls-files", "--error-unmatch", "--", ".muninn/memories.jsonl")
		tracked = err == nil
	}
	if (hasLegacy || tracked) && !o.Migrate {
		return st, fmt.Errorf("legacy .muninn/memories.jsonl exists, run muninn sync --migrate (or --migrate --dry-run)")
	}
	if tracked {
		status, err := git(p.Root, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return st, err
		}
		if status != "" {
			return st, fmt.Errorf("migration requires a clean code worktree and index, commit or stash tracked changes first")
		}
	}
	st.Migrated = o.Migrate && (hasLegacy || tracked)
	if o.DryRun {
		st.SyncStats, err = syncFile(p, nativePath, true, false)
		if err != nil {
			return st, err
		}
		if hasLegacy {
			items := legacyItems(p, legacy)
			st.Imported, err = ingest.MergeImport(nativePath, items, true)
		}
		return st, err
	}
	mergeLegacy := func() error {
		if !hasLegacy {
			return nil
		}
		_, err := ingest.MergeImport(nativePath, legacyItems(p, legacy), false)
		return err
	}
	if !isGit {
		if err = mergeLegacy(); err != nil {
			return st, err
		}
		st.SyncStats, err = Sync(p, nativePath, false)
	} else {
		repository, e := gitbranch.Open(p.Root, NotesBranch, NotesDir, gitbranch.WithIdentity("muninn", "muninn@users.noreply.github.com"))
		if e != nil {
			return st, e
		}
		err = repository.Update(func(_ *gitbranch.Repository) error {
			if err := mergeLegacy(); err != nil {
				return err
			}
			var err error
			st.SyncStats, err = syncFile(p, nativePath, false, false)
			return err
		})
		if err == nil {
			err = ingest.MarkSynced(nativePath, p.Scope(), func(id string) string { return originFor(p, id) })
		}
	}
	if err != nil {
		return st, err
	}
	// Never remove the code-branch copy until its replacement was committed
	// and pushed successfully. Only this one file is staged by the migration.
	if tracked {
		if _, err = migrationGit(p.Root, "rm", "--", ".muninn/memories.jsonl"); err != nil {
			return st, err
		}
		if _, err = migrationGit(p.Root, "commit", "--only", "-m", "Move project memories to muninn-notes branch", "--", ".muninn/memories.jsonl"); err != nil {
			_, restoreErr := migrationGit(p.Root, "restore", "--staged", "--worktree", "--", ".muninn/memories.jsonl")
			if restoreErr != nil {
				return st, fmt.Errorf("migration commit failed: %w (restore also failed: %v)", err, restoreErr)
			}
			return st, err
		}
	} else if hasLegacy {
		if err = os.Remove(p.LegacyFile()); err != nil {
			return st, err
		}
	}
	return st, nil
}
func legacyItems(p Project, lines []Line) []ingest.Imported {
	out := make([]ingest.Imported, 0, len(lines))
	for _, l := range lines {
		out = append(out, ingest.Imported{Origin: originFor(p, l.ID), Title: l.Title, Content: l.Content, Category: l.Category, Tags: l.Tags, Scope: p.Scope(), Active: l.Active, Created: l.Created, Updated: l.Updated})
	}
	return out
}
func migrationGit(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_AUTHOR_NAME=muninn", "GIT_COMMITTER_NAME=muninn", "GIT_AUTHOR_EMAIL=muninn@users.noreply.github.com", "GIT_COMMITTER_EMAIL=muninn@users.noreply.github.com")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("migration git %s: %s: %w", args[0], strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}
