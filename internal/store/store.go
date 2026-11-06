// Package store is muninn's SQLite persistence layer (pure Go, WAL).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Unit is one recallable piece of text: a memory, or a markdown section.
type Unit struct {
	ID          int64
	SID         string
	Origin      string // stable identity, never the display id
	SourceID    int64
	Key         string // stable key within the source (memory id, or file#line)
	ContentHash string
	Ident       string // hash over text and metadata; unchanged ident = skip entirely
	Kind        string // "memory" | "section"
	Title       string
	Body        string
	Category    string
	Tags        []string
	Scope       string // "global" or a project identifier
	Active      bool
	Updated     time.Time
}

// DB wraps the database handle.
type DB struct {
	*sql.DB
	Path string
}

const schema = `
CREATE TABLE IF NOT EXISTS capture_backlog (session TEXT NOT NULL, message TEXT NOT NULL, PRIMARY KEY(session,message));
CREATE TABLE IF NOT EXISTS capture_bindings (session TEXT PRIMARY KEY, scope TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS capture_seen (
 session TEXT NOT NULL, message TEXT NOT NULL, PRIMARY KEY(session,message)
);
CREATE TABLE IF NOT EXISTS capture_state (
 session TEXT PRIMARY KEY, high_water INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS capture_lock (id INTEGER PRIMARY KEY, token TEXT NOT NULL, expires_ns INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS capture_reinforcements (
 event TEXT PRIMARY KEY, sid TEXT NOT NULL, at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,          -- jcode | markdown | native
  path TEXT NOT NULL UNIQUE,
  mtime_ns INTEGER NOT NULL DEFAULT 0,
  size INTEGER NOT NULL DEFAULT 0,
  file_hash TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS units (
  id INTEGER PRIMARY KEY,
  source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  ident TEXT NOT NULL DEFAULT '', -- hash of text + metadata: change detection
  kind TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT '',
  tags TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT 'global',
  active INTEGER NOT NULL DEFAULT 1,
  updated_ns INTEGER NOT NULL DEFAULT 0,
  UNIQUE(source_id, key)
);
CREATE INDEX IF NOT EXISTS units_hash ON units(content_hash);
-- Covering index for scoped candidate scans (search never reads unit bodies
-- until the final few results).
CREATE INDEX IF NOT EXISTS units_scope ON units(scope, active, kind, content_hash);
CREATE VIRTUAL TABLE IF NOT EXISTS units_fts USING fts5(title, body, tags, content='units', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS units_ai AFTER INSERT ON units BEGIN
  INSERT INTO units_fts(rowid, title, body, tags) VALUES (new.id, new.title, new.body, new.tags);
END;
CREATE TRIGGER IF NOT EXISTS units_ad AFTER DELETE ON units BEGIN
  INSERT INTO units_fts(units_fts, rowid, title, body, tags) VALUES ('delete', old.id, old.title, old.body, old.tags);
END;
CREATE TRIGGER IF NOT EXISTS units_au AFTER UPDATE ON units BEGIN
  INSERT INTO units_fts(units_fts, rowid, title, body, tags) VALUES ('delete', old.id, old.title, old.body, old.tags);
  INSERT INTO units_fts(rowid, title, body, tags) VALUES (new.id, new.title, new.body, new.tags);
END;
-- Content-addressed vectors: identical text shares one row; a recipe change
-- (pipeline_hash) invalidates exactly the affected rows.
CREATE TABLE IF NOT EXISTS embeddings (
  content_hash TEXT NOT NULL,
  pipeline_hash TEXT NOT NULL,
  dim INTEGER NOT NULL,
  vec BLOB NOT NULL,      -- normalised float32, little-endian
  qvec BLOB NOT NULL,     -- int8 quantised copy (4x smaller)
  sketch BLOB NOT NULL,   -- 1-bit sign sketch (32x smaller) for Hamming prefilter
  PRIMARY KEY (content_hash, pipeline_hash)
);
-- Narrow copy of the sketches so the Hamming prefilter reads ~100 bytes per
-- unit instead of every 4 KB embeddings row.
CREATE TABLE IF NOT EXISTS sketches (
  content_hash TEXT NOT NULL,
  pipeline_hash TEXT NOT NULL,
  sketch BLOB NOT NULL,
  PRIMARY KEY (content_hash, pipeline_hash)
) WITHOUT ROWID;
-- Incremental neighbour graph (k nearest by cosine), maintained per dirty unit.
CREATE TABLE IF NOT EXISTS neighbors (
  unit_id INTEGER NOT NULL,
  nbr_id INTEGER NOT NULL,
  sim REAL NOT NULL,
  PRIMARY KEY (unit_id, nbr_id)
);
CREATE INDEX IF NOT EXISTS neighbors_nbr ON neighbors(nbr_id);
-- Concept graph (seid search): unigram concepts, unit membership, communities.
CREATE TABLE IF NOT EXISTS concepts (id INTEGER PRIMARY KEY, term TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS unit_concepts (
  unit_id INTEGER NOT NULL, concept_id INTEGER NOT NULL,
  PRIMARY KEY (unit_id, concept_id)
);
CREATE INDEX IF NOT EXISTS unit_concepts_c ON unit_concepts(concept_id);
CREATE TABLE IF NOT EXISTS communities (concept_id INTEGER PRIMARY KEY, community_id INTEGER NOT NULL);
-- Per-unit concept counts by community (as in seid search's
-- section_communities): community expansion reads this instead of joining
-- every unit_concepts row of the query communities.
CREATE TABLE IF NOT EXISTS unit_communities (
  community_id INTEGER NOT NULL, unit_id INTEGER NOT NULL, n INTEGER NOT NULL,
  PRIMARY KEY (community_id, unit_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS unit_communities_u ON unit_communities(unit_id);
-- Persisted DF-Louvain state (gob), so incremental updates resume exactly.
CREATE TABLE IF NOT EXISTS graph_state (id INTEGER PRIMARY KEY CHECK (id = 1), state BLOB NOT NULL);
-- Recall injections, for per-session dedupe and the web/TUI log.
CREATE TABLE IF NOT EXISTS injections (
  session TEXT NOT NULL, unit_id INTEGER NOT NULL, content_hash TEXT NOT NULL,
  harness TEXT NOT NULL, at_ns INTEGER NOT NULL,
  PRIMARY KEY (session, content_hash)
);
`

// Open opens (creating if needed) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Memories are private: create the database owner-only before SQLite does
	// (SQLite would apply the umask, typically 0644), and tighten existing files.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
			os.Chmod(p, 0o600)
		}
	}
	// mmap: search scans the narrow tables; through the default pager every
	// page was a pread syscall (profiled at ~65% of a warm search).
	// _txlock=immediate: every BeginTx here writes. A deferred transaction
	// that reads first and then writes cannot upgrade after another process
	// commits, and SQLite returns SQLITE_BUSY at once without consulting
	// busy_timeout (2026-11-06: 4 concurrent capture hooks failed this way).
	// BEGIN IMMEDIATE takes the write lock up front and waits instead.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)" +
		"&_pragma=mmap_size(1073741824)&_pragma=cache_size(-32768)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; WAL lets other processes read
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// One-time backfill for indexes written before the sketches table existed.
	var have, want int
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sketches), EXISTS(SELECT 1 FROM embeddings)`).Scan(&have, &want)
	if have == 0 && want == 1 {
		if _, err := db.Exec(`INSERT OR IGNORE INTO sketches(content_hash, pipeline_hash, sketch)
		  SELECT content_hash, pipeline_hash, sketch FROM embeddings`); err != nil {
			db.Close()
			return nil, fmt.Errorf("backfill sketches: %w", err)
		}
	}
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM unit_communities), EXISTS(SELECT 1 FROM communities)`).Scan(&have, &want)
	if have == 0 && want == 1 {
		if _, err := db.Exec(`INSERT INTO unit_communities(community_id, unit_id, n)
		  SELECT c.community_id, uc.unit_id, COUNT(*) FROM unit_concepts uc
		  JOIN communities c ON c.concept_id=uc.concept_id GROUP BY 1, 2`); err != nil {
			db.Close()
			return nil, fmt.Errorf("backfill unit_communities: %w", err)
		}
	}
	returnDB := &DB{DB: db, Path: path}
	if err := returnDB.migrateShortIDs(); err != nil {
		db.Close()
		return nil, fmt.Errorf("short ids: %w", err)
	}
	return returnDB, nil
}

// Source looks up or creates a source row and returns its id and stat.
func (d *DB) Source(kind, path string) (id int64, mtime, size int64, err error) {
	err = d.QueryRow(`SELECT id, mtime_ns, size FROM sources WHERE path=?`, path).Scan(&id, &mtime, &size)
	if err == sql.ErrNoRows {
		res, e := d.Exec(`INSERT INTO sources(kind, path) VALUES(?, ?)`, kind, path)
		if e != nil {
			return 0, 0, 0, e
		}
		id, err = res.LastInsertId()
		return id, -1, -1, err
	}
	return id, mtime, size, err
}

// SetSourceStat records the stat seen at the last successful scan.
func (d *DB) SetSourceStat(id, mtime, size int64) error {
	_, err := d.Exec(`UPDATE sources SET mtime_ns=?, size=? WHERE id=?`, mtime, size, id)
	return err
}

// UnitHashes returns key -> (unit id, ident) for a source.
func (d *DB) UnitHashes(sourceID int64) (map[string][2]string, error) {
	rows, err := d.Query(`SELECT key, id, ident FROM units WHERE source_id=?`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][2]string{}
	for rows.Next() {
		var k, h string
		var id int64
		if err := rows.Scan(&k, &id, &h); err != nil {
			return nil, err
		}
		m[k] = [2]string{fmt.Sprint(id), h}
	}
	return m, rows.Err()
}

// UpsertUnit inserts or replaces a unit identified by (source, key); returns its id.
func (d *DB) UpsertUnit(u Unit) (int64, error) {
	if err := d.prepareSID(&u); err != nil {
		return 0, err
	}
	active := 0
	if u.Active {
		active = 1
	}
	_, err := d.Exec(`INSERT INTO units(sid, origin, source_id, key, content_hash, ident, kind, title, body, category, tags, scope, active, updated_ns)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(source_id, key) DO UPDATE SET sid=excluded.sid, origin=excluded.origin, content_hash=excluded.content_hash, ident=excluded.ident, kind=excluded.kind,
	    title=excluded.title, body=excluded.body, category=excluded.category, tags=excluded.tags,
	    scope=excluded.scope, active=excluded.active, updated_ns=excluded.updated_ns`,
		u.SID, u.Origin, u.SourceID, u.Key, u.ContentHash, u.Ident, u.Kind, u.Title, u.Body, u.Category,
		strings.Join(u.Tags, " "), u.Scope, active, u.Updated.UnixNano())
	if err != nil {
		return 0, err
	}
	var id int64
	err = d.QueryRow(`SELECT id FROM units WHERE source_id=? AND key=?`, u.SourceID, u.Key).Scan(&id)
	return id, err
}

// DeleteUnit removes a unit and its concept membership. Neighbour edges are
// left for Indexer.UpdateNeighbors, which needs them to know which other
// lists referenced the unit and must be refilled.
func (d *DB) DeleteUnit(id int64) error {
	for _, q := range []string{
		`DELETE FROM unit_concepts WHERE unit_id=?`,
		`DELETE FROM unit_communities WHERE unit_id=?`,
		`DELETE FROM units WHERE id=?`,
	} {
		if _, err := d.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// GetUnit loads one unit.
func (d *DB) GetUnit(id int64) (Unit, error) {
	var u Unit
	var tags string
	var active int
	var upd int64
	err := d.QueryRow(`SELECT id, sid, origin, source_id, key, content_hash, kind, title, body, category, tags, scope, active, updated_ns
	  FROM units WHERE id=?`, id).Scan(&u.ID, &u.SID, &u.Origin, &u.SourceID, &u.Key, &u.ContentHash, &u.Kind, &u.Title, &u.Body,
		&u.Category, &tags, &u.Scope, &active, &upd)
	u.Tags = strings.Fields(tags)
	u.Active = active == 1 && !d.IsSuppressed(u.SID)
	u.Updated = time.Unix(0, upd)
	return u, err
}

// HasEmbedding reports whether a vector exists for (hash, pipeline).
func (d *DB) HasEmbedding(hash, pipeline string) bool {
	var one int
	return d.QueryRow(`SELECT 1 FROM embeddings WHERE content_hash=? AND pipeline_hash=?`, hash, pipeline).Scan(&one) == nil
}

// PutEmbedding stores a vector with its compressed forms.
func (d *DB) PutEmbedding(hash, pipeline string, dim int, vec, qvec, sketch []byte) error {
	_, err := d.Exec(`INSERT OR REPLACE INTO embeddings(content_hash, pipeline_hash, dim, vec, qvec, sketch) VALUES(?,?,?,?,?,?)`,
		hash, pipeline, dim, vec, qvec, sketch)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT OR REPLACE INTO sketches(content_hash, pipeline_hash, sketch) VALUES(?,?,?)`, hash, pipeline, sketch)
	return err
}

// GC removes vectors that belong to other pipelines or to no current unit.
func (d *DB) GC(pipeline string) (int64, error) {
	res, err := d.Exec(`DELETE FROM embeddings WHERE pipeline_hash<>? OR content_hash NOT IN (SELECT content_hash FROM units)`, pipeline)
	if err != nil {
		return 0, err
	}
	if _, err := d.Exec(`DELETE FROM sketches WHERE pipeline_hash<>? OR content_hash NOT IN (SELECT content_hash FROM units)`, pipeline); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Meta helpers.
func (d *DB) GetMeta(k string) string {
	var v string
	_ = d.QueryRow(`SELECT value FROM meta WHERE key=?`, k).Scan(&v)
	return v
}

func (d *DB) SetMeta(k, v string) error {
	_, err := d.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
	return err
}
