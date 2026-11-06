package app_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestTidyRollsBackFailedSuppression(t *testing.T) {
	f := scopetest.New(t)
	p := reviewedPlan(f.Units["Shared"], f.Units["OutsideNote"])
	before, err := os.ReadFile(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.App.DB.Exec(`CREATE TRIGGER tidy_test_failure BEFORE INSERT ON suppressed BEGIN SELECT RAISE(FAIL,'synthetic suppression failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := f.App.ApplyTidy(context.Background(), p, "", false)
	if err == nil || result.Backup == "" || len(result.Moves) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	after, _ := os.ReadFile(app.NativePath())
	if !bytes.Equal(before, after) {
		t.Fatal("failed apply did not restore original bytes")
	}
	u, err := f.App.DB.Resolve(f.Units["Shared"].SID)
	if err != nil || u.Scope != "global" {
		t.Fatal("failed apply left native moved")
	}
	if f.App.DB.IsSuppressed(f.Units["OutsideNote"].SID) {
		t.Fatal("failed apply left suppression")
	}
}
func TestTidyRefusesCaptureLeaseAndUnindexedCopy(t *testing.T) {
	f := scopetest.New(t)
	p := reviewedPlan(f.Units["OutsideNote"])
	if _, err := f.App.DB.Exec(`INSERT INTO capture_lock VALUES(1,'other',?)`, time.Now().Add(time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.ApplyTidy(context.Background(), p, "", false); err == nil {
		t.Fatal("applied while capture busy")
	}
	if _, err := f.App.DB.Exec(`DELETE FROM capture_lock`); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.MergeImport(app.NativePath(), []ingest.Imported{{Origin: "tidy:" + p.Entries[0].Origin, Content: "existing unindexed local copy", Active: true, Scope: "personal"}}, false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(app.NativePath())
	if _, err := f.App.ApplyTidy(context.Background(), p, "", false); err == nil {
		t.Fatal("overwrote unindexed tidy copy")
	}
	after, _ := os.ReadFile(app.NativePath())
	if !bytes.Equal(before, after) {
		t.Fatal("preflight rewrote local copy")
	}
}
