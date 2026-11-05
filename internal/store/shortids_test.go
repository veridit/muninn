package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/shortid"
)

func testShortDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSIDDeterminism(t *testing.T) {
	var want string
	for i := 0; i < 2; i++ {
		d := testShortDB(t)
		src, _, _, err := d.Source("native", filepath.Join(t.TempDir(), "memories.json"))
		if err != nil {
			t.Fatal(err)
		}
		id, err := d.UpsertUnit(Unit{SourceID: src, Key: "imp_old", Origin: "engram:stable-origin", Kind: "memory", Body: "portable", Scope: "global", Active: true})
		if err != nil {
			t.Fatal(err)
		}
		u, err := d.GetUnit(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(u.SID) != 6 || strings.Trim(u.SID, shortid.Alphabet) != "" {
			t.Fatalf("invalid sid %q", u.SID)
		}
		if i == 0 {
			want = u.SID
		} else if u.SID != want {
			t.Fatalf("same origin gave %s and %s", want, u.SID)
		}
	}
}

func TestSIDCollisionExtension(t *testing.T) {
	d := testShortDB(t)
	src, _, _, _ := d.Source("jcode", "fixture")
	for i, key := range []string{"one", "two", "three"} {
		// The full hash parameter is the tiny collision hook. No production
		// randomness or weakened production hash is involved.
		sid, err := allocateSID(d.DB, key, "abcdef0123456789", src, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(sid) != 6+i {
			t.Fatalf("collision %d: got %q", i, sid)
		}
		_, err = d.Exec(`INSERT INTO units(source_id,key,content_hash,kind,body,sid,origin) VALUES(?,?,'hash','memory','text',?,?)`, src, key, sid, key)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSIDOldSchemaMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// schema intentionally remains the pre-sid CREATE IF NOT EXISTS schema.
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(t.TempDir(), "memories.json")
	if err := os.WriteFile(native, []byte(`{"memories":[{"id":"imp_legacy","origin":"engram:upstream"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO sources(id,kind,path) VALUES(1,'native',?),(2,'jcode','harness');
 INSERT INTO units(source_id,key,content_hash,kind,body,scope) VALUES(1,'imp_legacy','h1','memory','a','global'),(2,'mem_old','h2','memory','b','jcode@test:proj')`, native); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var missing, total, distinct int
	if err := d.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT sid),SUM(sid IS NULL OR sid='') FROM units`).Scan(&total, &distinct, &missing); err != nil {
		t.Fatal(err)
	}
	if total != 2 || distinct != 2 || missing != 0 {
		t.Fatalf("migration total=%d distinct=%d missing=%d", total, distinct, missing)
	}
	u, err := d.Resolve("imp_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != "engram:upstream" || u.SID != shortid.Full(u.Origin)[:6] {
		t.Fatalf("origin lost: %+v", u)
	}
	sid := u.SID
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	u, err = d.Resolve("imp_legacy")
	if err != nil || u.SID != sid {
		t.Fatalf("reopen changed sid: %+v %v", u, err)
	}
}

func TestResolveSIDAndAliases(t *testing.T) {
	d := testShortDB(t)
	src, _, _, _ := d.Source("jcode", "fixture")
	id, err := d.UpsertUnit(Unit{SourceID: src, Key: "mem_legacy", Scope: "jcode@test:proj", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := d.GetUnit(id)
	for _, ref := range []string{u.SID, u.SID[:4], "mem_legacy", "jcode:mem_legacy"} {
		got, err := d.Resolve(ref)
		if err != nil || got.ID != id {
			t.Fatalf("Resolve(%q): %+v %v", ref, got, err)
		}
	}
	if _, err := d.Resolve(u.SID[:3]); err == nil {
		t.Fatal("accepted 3-char prefix")
	}
	src2, _, _, _ := d.Source("native", "fixture2")
	id2, err := d.UpsertUnit(Unit{SourceID: src2, Key: "imp_old", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	u2, _ := d.Resolve("imp_old")
	if u2.ID != id2 {
		t.Fatal("legacy import alias failed")
	}
	if _, err := d.Exec(`UPDATE units SET sid=? WHERE id=?`, u.SID[:4]+"zz", id2); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(u.SID[:4]); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if got, err := d.Resolve(u.SID); err != nil || got.ID != id {
		t.Fatalf("exact id must beat longer prefixes: %+v %v", got, err)
	}
}

func TestSIDOriginTransitionRetainsAlias(t *testing.T) {
	d := testShortDB(t)
	src, _, _, _ := d.Source("native", "fixture")
	u := Unit{SourceID: src, Key: "mem_native", Kind: "memory", Scope: "global", Active: true}
	id, err := d.UpsertUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := d.GetUnit(id)
	u.Origin = "repo:shared/mem_native"
	if _, err := d.UpsertUnit(u); err != nil {
		t.Fatal(err)
	}
	current, err := d.GetUnit(id)
	if err != nil || current.SID != shortid.Full(u.Origin)[:6] {
		t.Fatalf("transition: %+v %v", current, err)
	}
	for _, ref := range []string{old.SID, u.Key, current.SID} {
		got, err := d.Resolve(ref)
		if err != nil || got.ID != id {
			t.Fatalf("transition alias %s: %+v %v", ref, got, err)
		}
	}
}

// Optional acceptance probe. The caller must supply a disposable DB snapshot.
func TestSIDMigrationCopy(t *testing.T) {
	path := os.Getenv("MUNINN_MIGRATION_COPY")
	if path == "" {
		t.Skip("set MUNINN_MIGRATION_COPY to a disposable database snapshot")
	}
	start := time.Now()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	elapsed := time.Since(start)
	var units, distinct, maxLen, nulls int
	if err := d.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT sid),MAX(LENGTH(sid)),SUM(sid IS NULL OR sid='') FROM units`).Scan(&units, &distinct, &maxLen, &nulls); err != nil {
		t.Fatal(err)
	}
	t.Logf("migration elapsed=%s units=%d distinct_sids=%d max_sid_length=%d null_sids=%d", elapsed, units, distinct, maxLen, nulls)
	if units != distinct || nulls != 0 {
		t.Fatal("sid migration incomplete")
	}
}
