package index_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/store"
)

// A hook-mode pass must not queue behind another process's write
// transaction (a long `muninn index`): it returns ErrBusy at once.
func TestNoWaitReturnsBusyImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	writer, _ := store.Open(path)
	defer writer.Close()
	hook, _ := store.Open(path)
	defer hook.Close()
	if _, err := writer.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer writer.Exec(`ROLLBACK`)
	t0 := time.Now()
	_, err := (&index.Indexer{DB: hook, NoWait: true}).Run(context.Background(), index.Options{})
	if !errors.Is(err, index.ErrBusy) || time.Since(t0) > time.Second {
		t.Fatalf("got %v after %s, want ErrBusy at once", err, time.Since(t0))
	}
	// Reads still work while the writer holds the lock (WAL).
	var n int
	if err := hook.QueryRow(`SELECT COUNT(*) FROM units`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}
