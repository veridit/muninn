package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/veridit/muninn/internal/project"
)

// ImportScopeForDir places a memory imported from another system by the
// directory it was written in, as if muninn itself had written it there:
//
//   - inside a jcode instance root (e.g. ~/Projects/Brovs): that instance's
//     scope, "jcode@<name>", so only that instance recalls it;
//   - inside a git project (or a folder with .muninn/project): the project's
//     scope, "project:<id>", recalled only by sessions in that project;
//   - the home directory itself, or a folder that no longer exists: not
//     resolved (ok=false), so the caller keeps its fallback.
//
// The innermost match wins: a git project inside an instance root still
// belongs to that instance, because the instance binding is what recalls.
func (a *App) ImportScopeForDir(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." {
		return "", false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	// Client roots are never resolved: external input carries no client
	// provenance (see MergeImport). Those rows keep the fallback scope and
	// move with the explicit `rescope --cross-client`.
	if client, _, err := project.ClientRoot(dir); err != nil || client != "" {
		return "", false
	}
	home, _ := os.UserHomeDir()
	best := ""
	bestLen := -1
	for _, inst := range a.JcodeInstances() {
		if inst.Name == "global" || inst.Root == "" {
			continue
		}
		root := filepath.Clean(inst.Root)
		if (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))) && len(root) > bestLen {
			best, bestLen = inst.Scope(), len(root)
		}
	}
	if best != "" {
		return best, true
	}
	if filepath.Clean(dir) == filepath.Clean(home) {
		return "", false
	}
	if p, err := project.Find(dir, false); err == nil {
		return p.Scope(), true
	}
	return "", false
}
