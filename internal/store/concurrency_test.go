package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 2026-11-06: concurrent capture hooks failed with "database is locked (5)
// (SQLITE_BUSY)". A deferred transaction that reads, then writes, cannot
// upgrade once another process has committed, and SQLite fails it at once
// without honoring busy_timeout. Each child here reads then writes inside one
// BeginTx, the exact shape that failed; with IMMEDIATE every one succeeds.
func TestConcurrentProcessesReadThenWrite(t *testing.T) {
	if p := os.Getenv("STORE_CHILD_DB"); p != "" {
		db, err := Open(p)
		if err != nil {
			fmt.Println("open:", err)
			os.Exit(1)
		}
		defer db.Close()
		ctx := context.Background()
		for i := 0; i < 40; i++ {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				fmt.Println("begin:", err)
				os.Exit(1)
			}
			var n int
			tx.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&n)
			if _, err := tx.Exec(`INSERT INTO capture_seen(session,message) VALUES(?,?)`, os.Getenv("STORE_CHILD_ID"), strconv.Itoa(i)+"-"+strconv.Itoa(n)); err != nil {
				fmt.Println("write:", err)
				os.Exit(1)
			}
			if err := tx.Commit(); err != nil {
				fmt.Println("commit:", err)
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	const children = 6
	cmds := make([]*exec.Cmd, children)
	outs := make([]*strings.Builder, children)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestConcurrentProcessesReadThenWrite$")
		cmds[i].Env = append(os.Environ(), "STORE_CHILD_DB="+path, "STORE_CHILD_ID=c"+strconv.Itoa(i))
		outs[i] = &strings.Builder{}
		cmds[i].Stdout, cmds[i].Stderr = outs[i], outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outs[i])
		}
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&n)
	if n != children*40 {
		t.Fatalf("rows %d, want %d", n, children*40)
	}
}
