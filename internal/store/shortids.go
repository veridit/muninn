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
}

// allocateSID extends only the colliding new identity. Existing ids stay stable.
func allocateSID(q sidQueries, identity string, full string, source int64, key string) (string, error) {
	for n := 6; n <= len(full); n++ {
		sid := full[:n]
		var other string
		var src int64
		var k string
		err := q.QueryRow(`SELECT origin,source_id,key FROM units WHERE sid=?
 UNION SELECT u.origin,u.source_id,u.key FROM units u JOIN unit_aliases a ON a.source_id=u.source_id AND a.key=u.key WHERE a.alias=? LIMIT 1`, sid, sid).Scan(&other, &src, &k)
		if err == sql.ErrNoRows || (err == nil && src == source && k == key) {
			return sid, nil
		}
		if err != nil {
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
	if err == nil && existing != "" && origin == u.Origin {
		u.SID = existing
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if existing != "" {
		if _, err := d.Exec(`INSERT OR IGNORE INTO unit_aliases(alias,source_id,key) VALUES(?,?,?)`, existing, u.SourceID, u.Key); err != nil {
			return err
		}
	}
	full := shortid.Full(u.Origin)
	if len(u.SID) >= 6 && strings.HasPrefix(full, u.SID) {
		var found int
		err := d.QueryRow(`SELECT 1 FROM units WHERE sid=? AND NOT(source_id=? AND key=?)`, u.SID, u.SourceID, u.Key).Scan(&found)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
	}
	u.SID, err = allocateSID(d.DB, u.Origin, full, u.SourceID, u.Key)
	return err
}

func unitIdentity(kind, path, scope, key, origin string) string {
	if kind == "native" {
		return shortid.Identity(key, origin)
	}
	// Markdown sections also need a file identity: notes/L1:Title alone is not unique.
	if kind == "markdown" {
		return scope + "/" + path + "/" + key
	}
	return scope + "/" + key
}

func (d *DB) migrateShortIDs() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS units_sid ON units(sid);
 CREATE TABLE IF NOT EXISTS suppressed (sid TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS unit_aliases (alias TEXT NOT NULL, source_id INTEGER NOT NULL, key TEXT NOT NULL, PRIMARY KEY(alias,source_id,key));`); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT u.id, u.source_id, u.key, u.scope, s.kind, s.path FROM units u JOIN sources s ON s.id=u.source_id WHERE u.sid IS NULL OR u.sid='' ORDER BY u.id`)
	if err != nil {
		return err
	}
	type pending struct {
		id, source             int64
		key, scope, kind, path string
	}
	var all []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.source, &p.key, &p.scope, &p.kind, &p.path); err != nil {
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
		identity := unitIdentity(p.kind, p.path, p.scope, p.key, origin)
		sid, err := allocateSID(tx, identity, shortid.Full(identity), p.source, p.key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE units SET sid=?, origin=? WHERE id=?`, sid, identity, p.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Resolve accepts an exact short id, a unique prefix of at least four chars,
// an upstream key, a legacy source:key ref, or a legacy numeric row id.
// Ambiguity is an error, never an arbitrary match.
func (d *DB) Resolve(ref string) (Unit, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Unit{}, fmt.Errorf("empty memory id")
	}
	var exact int64
	err := d.QueryRow(`SELECT id FROM units WHERE sid=?`, ref).Scan(&exact)
	if err == nil {
		return d.GetUnit(exact)
	}
	if err != sql.ErrNoRows {
		return Unit{}, err
	}
	rows, err := d.Query(`SELECT u.id FROM units u JOIN sources s ON s.id=u.source_id
 WHERE u.key=? OR s.kind||':'||u.key=? OR CAST(u.id AS TEXT)=?
 UNION SELECT u.id FROM units u JOIN unit_aliases a ON a.source_id=u.source_id AND a.key=u.key WHERE a.alias=?`, ref, ref, ref, ref)
	if err != nil {
		return Unit{}, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Unit{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Unit{}, err
	}
	if len(ids) == 0 && len(ref) >= 4 && strings.Trim(ref, shortid.Alphabet) == "" {
		rows, err = d.Query(`SELECT id FROM units WHERE sid LIKE ?`, ref+"%")
		if err != nil {
			return Unit{}, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return Unit{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Unit{}, err
		}
	}
	if len(ids) == 0 {
		return Unit{}, fmt.Errorf("no memory %q", ref)
	}
	if len(ids) > 1 {
		return Unit{}, fmt.Errorf("ambiguous memory id %q (%d matches)", ref, len(ids))
	}
	return d.GetUnit(ids[0])
}

func (d *DB) IsSuppressed(sid string) bool {
	var one int
	return d.QueryRow(`SELECT 1 FROM suppressed WHERE sid=?`, sid).Scan(&one) == nil
}

// Suppress excludes a non-owned unit without touching its upstream files.
func (d *DB) Suppress(sid string) error {
	_, err := d.Exec(`INSERT OR IGNORE INTO suppressed(sid) VALUES(?)`, sid)
	return err
}
