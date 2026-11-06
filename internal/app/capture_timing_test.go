//go:build !race

package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
)

// This is a production wall-clock acceptance, run explicitly on all CI OSes.
// Race-instrumented modernc SQLite is not a production performance measurement.
// Functional capture/crash/scope tests still run with -race in the normal suite.
func TestCaptureEightCandidatesOnTwentyThousandUnits(t *testing.T) {
	f := scopetest.New(t)
	source, _, _, err := f.App.DB.Source("jcode", f.Root+"/alpha/.jcode/memory/bulk.json")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 20000; n++ {
		_, err := f.App.DB.UpsertUnit(store.Unit{SourceID: source, Key: fmt.Sprintf("bulk-%d", n), ContentHash: fmt.Sprintf("bulk-hash-%d", n), Kind: "memory", Body: fmt.Sprintf("Synthetic quartz deployment catalog record %d.", n), Scope: "jcode@alpha", Active: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	var proposals []string
	for n := 0; n < 8; n++ {
		proposals = append(proposals, line("decision", "high", fmt.Sprintf("Quartz batch candidate %d requires its own synthetic checklist.", n), ""))
	}
	fakeCapture(t, f, strings.Join(proposals, "\n"))
	before, _ := ingest.LoadNative(app.NativePath())
	var unitsBefore int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM units`).Scan(&unitsBefore); err != nil {
		t.Fatal(err)
	}
	o := captureOpts("twenty-thousand")
	o.Trigger = "session_end"
	start := time.Now()
	r, err := f.Bind(t, "alpha").Capture(context.Background(), f.App, o)
	elapsed := time.Since(start)
	if err != nil || len(r.Actions) != 8 || r.Processed != 1 {
		t.Fatalf("report=%+v err=%v", r, err)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("8 candidates took %s, require <10s", elapsed)
	}
	after, err := ingest.LoadNative(app.NativePath())
	if err != nil || len(after) != len(before)+8 {
		t.Fatalf("native count=%d err=%v", len(after), err)
	}
	var unitsAfter int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM units`).Scan(&unitsAfter); err != nil {
		t.Fatal(err)
	}
	if unitsAfter != unitsBefore || r.ReindexMS != 0 {
		t.Fatal("capture performed an eager reindex")
	}
	runs, err := f.App.AuditRuns(o.Session, time.Time{})
	if err != nil || len(runs) != 1 {
		t.Fatal(err)
	}
	if runs[0].CaptureTimings != r.CaptureTimings {
		t.Fatal("audit timing breakdown differs from report")
	}
	t.Logf("CAPTURE 20k: units=%d proposals=8 processed=1 native_added=8 elapsed=%s extract_ms=%d plan_ms=%d apply_ms=%d reindex_ms=%d", unitsBefore, elapsed, r.ExtractMS, r.PlanMS, r.ApplyMS, r.ReindexMS)
}
