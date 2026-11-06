package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/tidy"
)

type TidyMove struct{ Ref, Source, Target, Upstream string }
type TidyResult struct {
	Backup string
	Moves  []TidyMove
}

// ApplyTidy is deliberately owner CLI only. It is not registered with any agent
// or network surface. Dry runs neither acquire a write lock nor repair files.
func (a *App) ApplyTidy(ctx context.Context, p tidy.Plan, only string, dry bool) (result TidyResult, err error) {
	entries, err := tidy.Selected(p, only)
	if err != nil {
		return result, err
	}
	if p.Database != "" && filepath.Clean(p.Database) != filepath.Clean(a.DB.Path) {
		return result, fmt.Errorf("plan belongs to a different database")
	}
	if len(entries) == 0 {
		return result, nil
	}
	// Share the capture lease so hooks cannot rewrite memories during the batch.
	if !dry {
		token := fmt.Sprintf("tidy-%d", time.Now().UnixNano())
		now := time.Now()
		r, e := a.DB.ExecContext(ctx, `INSERT INTO capture_lock(id,token,expires_ns) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET token=excluded.token,expires_ns=excluded.expires_ns WHERE capture_lock.expires_ns<?`, token, now.Add(time.Hour).UnixNano(), now.UnixNano())
		if e != nil {
			return result, e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return result, e
		}
		if n != 1 {
			return result, fmt.Errorf("capture is busy, retry tidy when idle")
		}
		defer a.DB.Exec(`DELETE FROM capture_lock WHERE id=1 AND token=?`, token)
	}
	type pending struct {
		entry      tidy.Entry
		unit       store.Unit
		kind, path string
	}
	var moves []pending
	paths := map[string]bool{NativePath(): true}
	// Check the destination file even for upstream-only batches. MergeImport's
	// normal loader can repair duplicate IDs, and must not do that during tidy.
	destination, readErr := os.ReadFile(NativePath())
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	}
	var nativeStore struct{ Memories []ingest.NativeMemory }
	if readErr == nil {
		if err = json.Unmarshal(destination, &nativeStore); err != nil {
			return result, err
		}
	}
	ids := map[string]bool{}
	origins := map[string]bool{}
	for _, m := range nativeStore.Memories {
		if ids[m.ID] {
			return result, fmt.Errorf("duplicate native ids: repair before tidy")
		}
		ids[m.ID] = true
		origins[m.Origin] = true
	}
	for _, e := range entries {
		u, x := a.DB.Resolve(e.SID)
		if x != nil || !u.Active || u.Origin != e.Origin || u.Scope != e.Source || tidy.Fingerprint(u) != e.Fingerprint {
			return result, fmt.Errorf("stale plan for %s: rescan and review", e.SID)
		}
		var kind, path string
		if x = a.DB.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&kind, &path); x != nil {
			return result, x
		}
		if kind == "native" {
			paths[path] = true
			// Raw JSON reads are intentional: LoadNative can repair duplicates, which
			// would violate dry-run and preflight guarantees.
			raw, x := os.ReadFile(path)
			if x != nil {
				return result, x
			}
			var f struct{ Memories []ingest.NativeMemory }
			if x = json.Unmarshal(raw, &f); x != nil {
				return result, x
			}
			found := false
			ids := map[string]bool{}
			for _, m := range f.Memories {
				if ids[m.ID] {
					return result, fmt.Errorf("duplicate native ids: repair before tidy")
				}
				ids[m.ID] = true
				if m.ID == u.Key {
					found = m.Active && m.Content == u.Body && m.Title == u.Title && m.Scope == u.Scope && m.Category == u.Category
				}
			}
			if !found {
				return result, fmt.Errorf("native file changed for %s: reindex and rescan", u.SID)
			}
		} else {
			// A detached identity is required: Suppress(original SID) otherwise also
			// hides a normal imported native twin with that same identity.
			if origins["tidy:"+u.Origin] {
				return result, fmt.Errorf("tidy copy already exists on disk for %s; reindex and inspect it", u.SID)
			}
			var exists int
			if x = a.DB.QueryRow(`SELECT COUNT(*) FROM units WHERE origin=?`, "tidy:"+u.Origin).Scan(&exists); x != nil {
				return result, x
			}
			if exists > 0 {
				return result, fmt.Errorf("tidy copy already exists for %s; inspect it before retrying", u.SID)
			}
		}
		moves = append(moves, pending{e, u, kind, path})
	}
	if dry {
		for _, m := range moves {
			result.Moves = append(result.Moves, TidyMove{m.unit.SID, m.entry.Source, m.entry.Target, ""})
		}
		return result, nil
	}
	base := filepath.Join(config.DataDir(), "tidy")
	if err = os.MkdirAll(base, 0700); err != nil {
		return result, err
	}
	result.Backup, err = os.MkdirTemp(base, "apply-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return result, err
	}
	type saved struct {
		Path    string
		Data    []byte
		Existed bool
	}
	var originals []saved
	for path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
		originals = append(originals, saved{path, raw, e == nil})
	}
	// Both raw memories and the reviewed suppression identities are saved before mutation.
	raw, _ := json.MarshalIndent(originals, "", "  ")
	if err = os.WriteFile(filepath.Join(result.Backup, "files.json"), raw, 0600); err != nil {
		return result, err
	}
	for _, s := range originals {
		if s.Path == NativePath() {
			data := s.Data
			if !s.Existed {
				data = []byte("{\"version\":1,\"memories\":[]}")
			}
			if err = os.WriteFile(filepath.Join(result.Backup, "memories.json"), data, 0600); err != nil {
				return result, err
			}
		}
	}
	raw, _ = json.MarshalIndent(p, "", "  ")
	if err = os.WriteFile(filepath.Join(result.Backup, "plan.json"), raw, 0600); err != nil {
		return result, err
	}
	var suppressed []string
	// On a normal error restore the exact bytes and only the new suppressions.
	// The backup and per-move receipt survive a crash for manual recovery.
	defer func() {
		if err == nil {
			return
		}
		for _, s := range originals {
			var e error
			if s.Existed {
				e = os.WriteFile(s.Path, s.Data, 0600)
			} else {
				e = os.Remove(s.Path)
				if errors.Is(e, os.ErrNotExist) {
					e = nil
				}
			}
			err = errors.Join(err, e)
		}
		for _, sid := range suppressed {
			_, e := a.DB.Exec(`DELETE FROM suppressed WHERE sid=?`, sid)
			err = errors.Join(err, e)
		}
		_, e := a.Reindex(context.Background())
		err = errors.Join(err, e)
		result.Moves = nil
	}()
	cwd, _ := os.Getwd()
	binding := a.Bound("", cwd)
	for _, m := range moves {
		ref := m.unit.SID
		upstream := ""
		if m.kind == "native" {
			_, err = binding.rescope(ctx, a, m.unit.SID, m.entry.Target, true, false)
		} else {
			origin := "tidy:" + m.unit.Origin
			_, err = ingest.MergeImport(NativePath(), []ingest.Imported{{ExactText: true, Origin: origin, Title: m.unit.Title, Content: m.unit.Body, Category: m.unit.Category, Tags: m.unit.Tags, Scope: m.entry.Target, Active: true, Created: m.unit.Updated, Updated: m.unit.Updated}}, false)
			if err == nil {
				var mems []ingest.NativeMemory
				mems, err = ingest.LoadNative(NativePath())
				found := false
				for _, mem := range mems {
					if mem.Origin == origin {
						ref = mem.ID
						found = mem.Content == m.unit.Body && mem.Scope == m.entry.Target && mem.Active
					}
				}
				if err == nil && !found {
					err = fmt.Errorf("import did not preserve reviewed memory")
				}
			}
			if err == nil {
				err = a.DB.Suppress(m.unit.SID)
				if err == nil {
					suppressed = append(suppressed, m.unit.SID)
					upstream = m.unit.SID
				}
			}
		}
		if err != nil {
			return result, err
		}
		result.Moves = append(result.Moves, TidyMove{ref, m.entry.Source, m.entry.Target, upstream})
		raw, _ = json.MarshalIndent(result, "", "  ")
		if err = os.WriteFile(filepath.Join(result.Backup, "receipt.json"), raw, 0600); err != nil {
			return result, err
		}
	}
	_, err = a.Reindex(ctx)
	return result, err
}
