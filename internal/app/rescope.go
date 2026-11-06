package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
)

// Rescope is deliberately CLI-admin-only. No hook, MCP, TUI or HTTP route calls it.
// Cross-client migration preserves the stable identity and upstream mapping.
func (b Binding) Rescope(ctx context.Context, a *App, ref, target string, crossClient bool) (string, error) {
	b = b.Refresh(a)
	if b.Err != nil {
		return "", b.Err
	}
	if target == "" || strings.ContainsAny(target, "*!\r\n") {
		return "", fmt.Errorf("--to requires a literal scope")
	}
	u, err := a.DB.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("no visible memory")
	}
	clientOf := func(scope string) string {
		if !strings.HasPrefix(scope, "client:") {
			return ""
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(scope, "client:"), ":")
		return name
	}
	crossing := clientOf(u.Scope) != clientOf(target)
	if crossing && !crossClient {
		return "", fmt.Errorf("crossing a client boundary requires --cross-client")
	}
	if !crossClient {
		if _, err := b.Resolve(a, ref); err != nil {
			return "", err
		}
		if _, err := b.WriteScope(target); err != nil {
			return "", err
		}
	}
	var kind, path string
	if err := a.DB.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&kind, &path); err != nil {
		return "", err
	}
	if kind != "native" {
		return "", fmt.Errorf("rescope requires an owned native memory; import the upstream memory first")
	}
	mems, err := ingest.LoadNative(path)
	if err != nil {
		return "", err
	}
	found := false
	for i := range mems {
		if mems[i].ID == u.Key {
			mems[i].Scope = target
			mems[i].Updated = time.Now().UTC()
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("no visible memory")
	}
	if err := ingest.SaveNative(path, mems); err != nil {
		return "", err
	}
	if _, err := a.Reindex(ctx); err != nil {
		return "", err
	}
	return u.SID, nil
}

// MergeImport prevents imports from becoming a second boundary-crossing path.
func (b Binding) MergeImport(path string, items []ingest.Imported, dry bool, sourceDirs ...string) (ingest.ImportStats, error) {
	if b.Err != nil {
		return ingest.ImportStats{}, b.Err
	}
	client := ""
	if len(sourceDirs) > 0 {
		var err error
		client, _, err = project.ClientRoot(filepath.Clean(sourceDirs[0]))
		if err != nil {
			return ingest.ImportStats{}, err
		}
	}
	items = append([]ingest.Imported(nil), items...)
	for i := range items {
		if client != "" {
			items[i].Scope = "client:" + client
		} else if strings.HasPrefix(items[i].Scope, "client:") {
			return ingest.ImportStats{}, fmt.Errorf("external input has no client provenance; import outside the client then use muninn rescope --cross-client")
		}
	}
	mems, err := ingest.LoadNative(path)
	if err != nil {
		return ingest.ImportStats{}, err
	}
	existing := map[string]string{}
	for _, m := range mems {
		existing[m.Origin] = m.Scope
	}
	for _, it := range items {
		scope := it.Scope
		if scope == "" {
			scope = "global"
		}
		// A placed item's scope is where it was physically written (never a
		// client, see ImportScopeForDir), so the importing binding's write
		// policy does not apply. Client boundaries still hold in MergeImport.
		if it.Placed && !strings.HasPrefix(scope, "client:") {
			if old, ok := existing[it.Origin]; !ok || !strings.HasPrefix(old, "client:") {
				continue
			}
		}
		if _, err := b.WriteScope(scope); err != nil {
			return ingest.ImportStats{}, err
		}
		if old, ok := existing[it.Origin]; ok {
			if _, err := b.WriteScope(old); err != nil {
				return ingest.ImportStats{}, err
			}
		}
	}
	return ingest.MergeImport(path, items, dry)
}
