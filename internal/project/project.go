// Package project gives a working directory a stable identity that survives
// renames, moves and fresh clones, and syncs a project's memories with a
// JSONL file committed in the repository.
package project

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Project is a repository (or plain folder) muninn keeps memories for.
type Project struct {
	Client string // nearest client-root boundary, if any
	ID     string // stable: git root commit, or the id in .muninn/project
	Label  string // human name (the folder name)
	Root   string // top-level directory
}

// Scope is the recall scope of the project's own memories.
func (p Project) Scope() string {
	if p.Client != "" {
		return "client:" + p.Client + ":" + p.ID
	}
	return "project:" + p.ID
}

// File is the shared memory file inside the repository.
func (p Project) File() string { return filepath.Join(p.Root, ".muninn", "memories.jsonl") }

// Find resolves the project containing dir. In a git repository the id is
// the first root commit (identical for every clone, unchanged by renames
// and moves; the "rc-" prefix keeps it recognisable). Outside git, or in a
// repo without commits, the id lives in <root>/.muninn/project, created on
// first use when create is true.
func Find(dir string, create bool) (Project, error) {
	client, root, err := ClientRoot(dir)
	if err != nil {
		return Project{}, err
	}
	if client == "" {
		return find(dir, create)
	}
	p, err := find(dir, false)
	if err == nil {
		// root is physical (ClientRoot resolves symlinks); compare physical
		// paths too, or /tmp -> /private/tmp style links hide the project.
		pr := p.Root
		if phys, e := filepath.EvalSymlinks(pr); e == nil {
			pr = phys
		}
		rel, e := filepath.Rel(root, pr)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			err = ErrNoProject
		}
	}
	if err != nil {
		p, err = withFileID(root, create)
	}
	p.Client = client
	return p, err
}

func find(dir string, create bool) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}
	if top, err := git(abs, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		top = filepath.Clean(top)
		if id, err := readID(top); err == nil {
			return Project{ID: id, Label: filepath.Base(top), Root: top}, nil
		}
		if roots, err := git(top, "rev-list", "--max-parents=0", "HEAD"); err == nil && roots != "" {
			// Several roots (merged histories): the lexically smallest is
			// deterministic for every clone.
			ids := strings.Fields(roots)
			best := ids[0]
			for _, r := range ids[1:] {
				if r < best {
					best = r
				}
			}
			return Project{ID: "rc-" + best[:16], Label: filepath.Base(top), Root: top}, nil
		}
		return withFileID(top, create)
	}
	// Not git: walk up to a folder that already has .muninn/project.
	for d := abs; ; d = filepath.Dir(d) {
		if id, err := readID(d); err == nil {
			return Project{ID: id, Label: filepath.Base(d), Root: d}, nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return withFileID(abs, create)
}

// ErrNoProject means dir has no identity yet and create was false.
var ErrNoProject = errors.New("not a muninn project (no git history and no .muninn/project)")

func withFileID(root string, create bool) (Project, error) {
	if !create {
		return Project{}, ErrNoProject
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := "id-" + hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Join(root, ".muninn"), 0o755); err != nil {
		return Project{}, err
	}
	if err := os.WriteFile(filepath.Join(root, ".muninn", "project"), []byte(id+"\n"), 0o644); err != nil {
		return Project{}, err
	}
	return Project{ID: id, Label: filepath.Base(root), Root: root}, nil
}

// readID reads an explicit id; it wins over the git root commit so a team
// can pin an identity (e.g. after rewriting history).
func readID(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, ".muninn", "project"))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("empty .muninn/project")
	}
	return id, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
