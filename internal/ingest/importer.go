package ingest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // engram.db reader
)

// Imported is one memory read from another memory system, before merging.
type Imported struct {
	Origin    string // stable upstream identity; the idempotency key
	Title     string
	Content   string
	Category  string
	Tags      []string
	Scope     string
	Active    bool
	Created   time.Time
	Updated   time.Time
	keepScope bool // new default only; existing Engram identities keep their scope
	// Placed: the scope came from the directory the memory was written in.
	// That physical provenance, not the importing binding, decides where it
	// lives, and an existing copy an earlier explicit --map put in a jcode
	// instance keeps that place.
	Placed bool
}

// ImportStats counts what one import did.
type ImportStats struct {
	Added, Updated, Unchanged, LocalKept, Skipped int
}

func (s ImportStats) String() string {
	return fmt.Sprintf("added=%d updated=%d unchanged=%d kept-local-edit=%d skipped=%d",
		s.Added, s.Updated, s.Unchanged, s.LocalKept, s.Skipped)
}

func originHash(title, content, scope string, active bool) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%t", title, content, scope, active)))
	return hex.EncodeToString(h[:12])
}

// importID derives the native id from the origin, so the same upstream memory
// always maps to the same muninn memory.
func importID(origin string) string {
	h := sha256.Sum256([]byte(origin))
	return "imp_" + hex.EncodeToString(h[:8])
}

// MergeImport merges items into the native store at path. It is idempotent:
// an item is keyed by Origin; unchanged upstream text is left alone; changed
// upstream text replaces the copy unless the copy was edited in muninn since
// the last import (then the local edit wins and is counted). Upstream
// deletions are not propagated: an import is a copy, not a mirror.
func MergeImport(path string, items []Imported, dry bool) (ImportStats, error) {
	var st ImportStats
	mems, err := LoadNative(path)
	if err != nil {
		return st, err
	}
	byOrigin := map[string]int{}
	for i, m := range mems {
		if m.Origin != "" {
			byOrigin[m.Origin] = i
		}
	}
	now := time.Now().UTC()
	for _, it := range items {
		it.Content = strings.TrimSpace(it.Content)
		if it.Origin == "" || it.Content == "" {
			st.Skipped++
			continue
		}
		if it.Scope == "" {
			it.Scope = "global"
		}
		i, ok := byOrigin[it.Origin]
		if ok && (it.keepScope || it.Placed && strings.HasPrefix(mems[i].Scope, "jcode@")) {
			it.Scope = mems[i].Scope
		}
		h := originHash(it.Title, it.Content, it.Scope, it.Active)
		if !ok {
			created, updated := it.Created, it.Updated
			if created.IsZero() {
				created = now
			}
			if updated.IsZero() {
				updated = created
			}
			mems = append(mems, NativeMemory{ID: importID(it.Origin), Title: it.Title, Content: it.Content,
				Category: it.Category, Tags: it.Tags, Scope: it.Scope, Active: it.Active, Strength: 1,
				Created: created, Updated: updated, Origin: it.Origin, OriginHash: h})
			byOrigin[it.Origin] = len(mems) - 1
			st.Added++
			continue
		}
		m := &mems[i]
		if m.OriginHash == h {
			st.Unchanged++
			continue
		}
		if originHash(m.Title, m.Content, m.Scope, m.Active) != m.OriginHash {
			st.LocalKept++ // edited in muninn since the last import
			continue
		}
		if clientBoundary(m.Scope) != clientBoundary(it.Scope) {
			return st, fmt.Errorf("import cannot cross a client boundary; use muninn rescope --cross-client")
		}
		m.Title, m.Content, m.Category, m.Tags, m.Active = it.Title, it.Content, it.Category, it.Tags, it.Active
		m.Scope, m.OriginHash, m.Updated = it.Scope, h, now
		if !it.Updated.IsZero() {
			m.Updated = it.Updated // a copy, not a new edit: keep upstream time
		}
		st.Updated++
	}
	if dry || st.Added+st.Updated == 0 {
		return st, nil
	}
	return st, SaveNative(path, mems)
}

func clientBoundary(scope string) string {
	if !strings.HasPrefix(scope, "client:") {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(scope, "client:"), ":")
	return name
}

// MarkSynced records that every memory in scope now equals its copy in an
// external file: memories without an origin get origin(id), and every
// OriginHash is set to the current text. The next merge of that file is then
// a no-op, and later edits on either side are detected again.
func MarkSynced(path, scope string, origin func(id string) string) error {
	mems, err := LoadNative(path)
	if err != nil {
		return err
	}
	changed := false
	for i := range mems {
		m := &mems[i]
		if m.Scope != scope {
			continue
		}
		if m.Origin == "" {
			m.Origin, changed = origin(m.ID), true
		}
		if h := originHash(m.Title, m.Content, m.Scope, m.Active); m.OriginHash != h {
			m.OriginHash, changed = h, true
		}
	}
	if !changed {
		return nil
	}
	return SaveNative(path, mems)
}

// JcodeImportItems reads every memory of one jcode instance's memory dir.
func JcodeImportItems(dir, prefix string) ([]Imported, error) {
	files, err := JcodeFiles(dir, prefix)
	if err != nil {
		return nil, err
	}
	var out []Imported
	for path, scope := range files {
		mems, err := ReadJcodeFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, m := range mems {
			out = append(out, Imported{Origin: scope + "/" + m.Key, Content: m.Content, Category: m.Category,
				Tags: m.Tags, Scope: scope, Active: m.Active, Created: m.Updated, Updated: m.Updated})
		}
	}
	return out, nil
}

// EngramObservation is the subset of an engram observation muninn imports,
// shared by `engram export` JSON and the engram SQLite database.
type EngramObservation struct {
	SyncID    string  `json:"sync_id"`
	ID        int64   `json:"id"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	Project   *string `json:"project"`
	Scope     string  `json:"scope"`
	TopicKey  *string `json:"topic_key"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
	SessionID string  `json:"session_id"`
	// Directory is the working directory of the engram session that wrote
	// the observation (sessions.directory). It places the memory.
	Directory string `json:"-"`
}

// EngramOptions maps engram projects to muninn scopes.
type EngramOptions struct {
	// ProjectScopes overrides the scope for an engram project, e.g.
	// {"statbus": "jcode@statbus"}. It wins over directory resolution.
	ProjectScopes map[string]string
	// ScopeForDir resolves an observation's session directory to the scope
	// muninn itself would use there ("project:<id>"), as if the memory had
	// been written in that project. Unresolved observations get
	// "engram:<project>".
	ScopeForDir func(dir string) (string, bool)
}

// EngramItems converts observations. New personal or empty-project observations
// use personal, preserving existing identities' scopes without a migration.
func EngramItems(obs []EngramObservation, o EngramOptions) []Imported {
	out := make([]Imported, 0, len(obs))
	for _, ob := range obs {
		key := ob.SyncID
		if key == "" {
			key = fmt.Sprintf("id-%d", ob.ID) // very old rows have no sync_id
		}
		project := ""
		if ob.Project != nil {
			project = strings.TrimSpace(*ob.Project)
		}
		scope := "personal"
		resolved := false
		if ob.Scope != "personal" && project != "" {
			scope = "engram:" + project
			if s, ok := o.ProjectScopes[project]; ok {
				scope = s
			} else if o.ScopeForDir != nil && ob.Directory != "" {
				if s, ok := o.ScopeForDir(ob.Directory); ok {
					scope, resolved = s, true
				}
			}
		}
		var tags []string
		if ob.TopicKey != nil && *ob.TopicKey != "" {
			tags = []string{*ob.TopicKey}
		}
		out = append(out, Imported{Origin: "engram:" + key, Title: ob.Title, Content: ob.Content,
			keepScope: ob.Scope == "personal" || project == "", Placed: resolved,
			Category:  ob.Type, Tags: tags, Scope: scope, Active: ob.DeletedAt == nil || *ob.DeletedAt == "",
			Created: engramTime(ob.CreatedAt), Updated: engramTime(ob.UpdatedAt)})
	}
	return out
}

func engramTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ReadEngramExport reads observations from an `engram export` JSON file.
func ReadEngramExport(path string) ([]EngramObservation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Sessions []struct {
			ID        string `json:"id"`
			Directory string `json:"directory"`
		} `json:"sessions"`
		Observations []EngramObservation `json:"observations"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	dirs := map[string]string{}
	for _, s := range f.Sessions {
		dirs[s.ID] = s.Directory
	}
	for i := range f.Observations {
		f.Observations[i].Directory = dirs[f.Observations[i].SessionID]
	}
	return f.Observations, nil
}

// ReadEngramDB reads observations from an engram SQLite database, read-only.
func ReadEngramDB(path string) ([]EngramObservation, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT o.id, COALESCE(o.sync_id,''), o.type, o.title, o.content, o.project, o.scope, o.topic_key,
	  o.created_at, o.updated_at, o.deleted_at, o.session_id, COALESCE(s.directory,'')
	  FROM observations o LEFT JOIN sessions s ON s.id = o.session_id ORDER BY o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EngramObservation
	for rows.Next() {
		var o EngramObservation
		if err := rows.Scan(&o.ID, &o.SyncID, &o.Type, &o.Title, &o.Content, &o.Project, &o.Scope, &o.TopicKey,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt, &o.SessionID, &o.Directory); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
