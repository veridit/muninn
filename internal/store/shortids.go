package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/veridit/muninn/internal/shortid"
)

type sidQueries interface {
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

// allocateSID reserves one id per identity, not per source row. The full hash
// parameter lets tests force collisions without weakening the production hash.
func allocateSID(q sidQueries, identity, full string, preferred ...string) (string, error) {
	var existing string
	err := q.QueryRow(`SELECT sid FROM memory_ids WHERE identity=?`, identity).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	start := 6
	if len(preferred) > 0 && len(preferred[0]) >= 6 && strings.HasPrefix(full, preferred[0]) {
		start = len(preferred[0])
	}
	for n := start; n <= len(full); n++ {
		sid := full[:n]
		res, err := q.Exec(`INSERT INTO memory_ids(sid,identity) VALUES(?,?) ON CONFLICT DO NOTHING`, sid, identity)
		if err != nil {
			return "", err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return "", err
		}
		if count == 1 {
			return sid, nil
		}
		// Another connection may have reserved this identity in the meantime.
		err = q.QueryRow(`SELECT sid FROM memory_ids WHERE identity=?`, identity).Scan(&existing)
		if err == nil {
			return existing, nil
		}
		if err != sql.ErrNoRows {
			return "", err
		}
	}
	return "", fmt.Errorf("short id hash exhausted for %q", identity)
}

func (d *DB) prepareSID(u *Unit) error {
	if u.Origin == "" {
		var kind, path string
		if err := d.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&kind, &path); err != nil {
			return err
		}
		u.Origin = unitIdentity(kind, path, u.Scope, u.Key, "")
	}
	var existing, origin string
	err := d.QueryRow(`SELECT sid,origin FROM units WHERE source_id=? AND key=?`, u.SourceID, u.Key).Scan(&existing, &origin)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && origin == u.Origin {
		u.SID = existing
		return nil
	}
	if existing != "" {
		if _, err := d.Exec(`INSERT OR IGNORE INTO unit_aliases(alias,source_id,key) VALUES(?,?,?)`, existing, u.SourceID, u.Key); err != nil {
			return err
		}
	}
	u.SID, err = allocateSID(d.DB, u.Origin, shortid.Full(u.Origin), u.SID)
	return err
}

func unitIdentity(kind, path, scope, key, origin string) string {
	if kind == "native" {
		return shortid.Identity(key, origin)
	}
	// Section identity needs a file too: notes/L1:Title alone is not unique.
	if kind == "markdown" {
		return scope + "/" + path + "/" + key
	}
	return scope + "/" + key
}

func (d *DB) migrateShortIDs() error {
	if d.GetMeta("shortids_schema") == "2" {
		return nil
	}
	// Open has not published this single-connection pool yet. Acquire the
	// write lock before reading schema, avoiding deferred-transaction upgrade
	// races when several hooks open an old database at the same time.
	tx := d.DB
	if _, err := tx.Exec(`BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer tx.Exec(`ROLLBACK`)
	var version string
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key='shortids_schema'`).Scan(&version); err == nil && version == "2" {
		_, err = tx.Exec(`COMMIT`)
		return err
	}
	rows, err := tx.Query(`PRAGMA table_info(units)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, col := range []string{"sid", "origin"} {
		if !columns[col] {
			if _, err := tx.Exec(`ALTER TABLE units ADD COLUMN ` + col + ` TEXT`); err != nil {
				return err
			}
		}
	}
	// A sid names a memory, not a row. Live harness and imported native rows
	// may share it. Uniqueness belongs in memory_ids (sid <-> identity).
	var unique int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('units') WHERE name='units_sid' AND "unique"=1`).Scan(&unique); err != nil {
		return err
	}
	if unique > 0 {
		if _, err := tx.Exec(`DROP INDEX units_sid`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS units_sid ON units(sid);
 CREATE INDEX IF NOT EXISTS units_origin ON units(origin);
 CREATE TABLE IF NOT EXISTS memory_ids (sid TEXT PRIMARY KEY, identity TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS suppressed (sid TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS unit_aliases (alias TEXT NOT NULL,source_id INTEGER NOT NULL,key TEXT NOT NULL,PRIMARY KEY(alias,source_id,key));`); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT u.id,u.key,u.scope,s.kind,s.path,COALESCE(u.sid,''),COALESCE(u.origin,'') FROM units u JOIN sources s ON s.id=u.source_id
 WHERE u.sid IS NULL OR u.sid='' OR u.origin IS NULL OR NOT EXISTS(SELECT 1 FROM memory_ids m WHERE m.identity=u.origin AND m.sid=u.sid) ORDER BY u.id`)
	if err != nil {
		return err
	}
	type pending struct {
		id                                  int64
		key, scope, kind, path, sid, origin string
	}
	var all []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.key, &p.scope, &p.kind, &p.path, &p.sid, &p.origin); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	origins := map[string]map[string]string{}
	for _, p := range all {
		identity := p.origin
		if identity == "" {
			origin := ""
			if p.kind == "native" {
				if _, ok := origins[p.path]; !ok {
					origins[p.path] = map[string]string{}
					b, err := os.ReadFile(p.path)
					if err == nil {
						var f struct{ Memories []struct{ ID, Origin string } }
						if err := json.Unmarshal(b, &f); err != nil {
							return fmt.Errorf("native origins %s: %w", p.path, err)
						}
						for _, m := range f.Memories {
							origins[p.path][m.ID] = m.Origin
						}
					} else if !os.IsNotExist(err) {
						return err
					}
				}
				origin = origins[p.path][p.key]
			}
			identity = unitIdentity(p.kind, p.path, p.scope, p.key, origin)
		}
		sid, err := allocateSID(tx, identity, shortid.Full(identity), p.sid)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE units SET sid=?,origin=? WHERE id=?`, sid, identity, p.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('shortids_schema','2') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	_, err = tx.Exec(`COMMIT`)
	return err
}

// ResolveAll accepts a sid, a unique prefix (4+ chars), an upstream key,
// a legacy source:key ref or numeric row id, and returns every row of that
// identity. Native is canonical, even if inactive, so mirrors never revive it.
func (d *DB) ResolveAll(ref string) ([]Unit, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty memory id")
	}
	var origin string
	err := d.QueryRow(`SELECT origin FROM units WHERE sid=? LIMIT 1`, ref).Scan(&origin)
	if err == nil {
		return d.identityUnits(origin)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	rows, err := d.Query(`SELECT DISTINCT u.origin FROM units u JOIN sources s ON s.id=u.source_id WHERE u.key=? OR s.kind||':'||u.key=? OR CAST(u.id AS TEXT)=?
 UNION SELECT DISTINCT u.origin FROM units u JOIN unit_aliases a ON a.source_id=u.source_id AND a.key=u.key WHERE a.alias=?`, ref, ref, ref, ref)
	if err != nil {
		return nil, err
	}
	var identities []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		identities = append(identities, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(identities) == 0 && len(ref) >= 4 && strings.Trim(ref, shortid.Alphabet) == "" {
		rows, err = d.Query(`SELECT DISTINCT origin FROM units WHERE sid LIKE ?`, ref+"%")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			identities = append(identities, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("no memory %q", ref)
	}
	if len(identities) > 1 {
		return nil, fmt.Errorf("ambiguous memory id %q (%d identities)", ref, len(identities))
	}
	return d.identityUnits(identities[0])
}

func (d *DB) identityUnits(origin string) ([]Unit, error) {
	rows, err := d.Query(`SELECT u.id FROM units u JOIN sources s ON s.id=u.source_id WHERE u.origin=? ORDER BY (s.kind='native') DESC,u.updated_ns DESC,u.id`, origin)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []Unit
	for _, id := range ids {
		u, err := d.GetUnit(id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// Resolve is the canonical-row view of ResolveAll, used by actions on one row.
func (d *DB) Resolve(ref string) (Unit, error) {
	units, err := d.ResolveAll(ref)
	if err != nil {
		return Unit{}, err
	}
	return units[0], nil
}

// IsCanonical checks ownership before recall. An inactive native twin still
// supersedes a live harness row, even when their text differs.
func (d *DB) IsCanonical(u Unit) bool {
	var id int64
	return d.QueryRow(`SELECT u.id FROM units u JOIN sources s ON s.id=u.source_id WHERE u.sid=? ORDER BY (s.kind='native') DESC,u.updated_ns DESC,u.id LIMIT 1`, u.SID).Scan(&id) == nil && id == u.ID
}

func (d *DB) IsSuppressed(sid string) bool {
	var one int
	return d.QueryRow(`SELECT 1 FROM suppressed WHERE sid=?`, sid).Scan(&one) == nil
}

// Suppress excludes every mirror of an identity without touching upstream files.
func (d *DB) Suppress(sid string) error {
	_, err := d.Exec(`INSERT OR IGNORE INTO suppressed(sid) VALUES(?)`, sid)
	return err
}
