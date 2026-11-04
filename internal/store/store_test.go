package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDatabaseIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	p := filepath.Join(t.TempDir(), "sub", "m.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("db mode %v, want owner-only", fi.Mode().Perm())
	}
	// An existing world-readable db is tightened on open.
	os.Chmod(p, 0o644)
	db, _ = Open(p)
	db.Close()
	fi, _ = os.Stat(p)
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("existing db not tightened: %v", fi.Mode().Perm())
	}
}
