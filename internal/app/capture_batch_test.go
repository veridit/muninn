package app_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
)

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
	f.App.DB.QueryRow(`SELECT count(*) FROM units`).Scan(&unitsBefore)
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
	f.App.DB.QueryRow(`SELECT count(*) FROM units`).Scan(&unitsAfter)
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

func TestCaptureRetryAfterNativeSaveCheckpointInterruption(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("regenerated=%v", changed), func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, "alpha")
			model := fakeCapture(t, f, line("decision", "high", "Original synthetic quartz capture proposal.", ""))
			before, _ := ingest.LoadNative(app.NativePath())
			if _, err := f.App.DB.Exec(`CREATE TRIGGER interrupt_checkpoint BEFORE INSERT ON capture_seen BEGIN SELECT RAISE(FAIL,'synthetic interruption after native save'); END;`); err != nil {
				t.Fatal(err)
			}
			o := captureOpts("interrupted-after-save")
			r, err := b.Capture(context.Background(), f.App, o)
			if err == nil || r.Processed != 0 {
				t.Fatalf("checkpoint was not interrupted: %+v %v", r, err)
			}
			saved, err := ingest.LoadNative(app.NativePath())
			if err != nil || len(saved) != len(before)+1 {
				t.Fatalf("save did not precede checkpoint failure: %v", err)
			}
			if _, err := f.App.DB.Exec(`DROP TRIGGER interrupt_checkpoint`); err != nil {
				t.Fatal(err)
			}
			if changed {
				model.reply = line("decision", "high", "Regenerated synthetic quartz proposal must not append another ID.", "")
			}
			r, err = b.Capture(context.Background(), f.App, o)
			if err != nil || r.Processed != 1 || len(r.Actions) != 1 || r.Actions[0].Action != "skip" {
				t.Fatalf("retry=%+v %v", r, err)
			}
			after, err := ingest.LoadNative(app.NativePath())
			if err != nil || len(after) != len(saved) {
				t.Fatalf("retry duplicated native row: %v", err)
			}
			for _, m := range after {
				if strings.HasPrefix(m.Origin, "capture:stdin:interrupted-after-save:") && (m.Content != "Original synthetic quartz capture proposal." || m.Strength != 1) {
					t.Fatal("retry overwrote/reinforced committed proposal")
				}
			}
			r, err = b.Capture(context.Background(), f.App, o)
			if err != nil || r.Processed != 0 || model.calls.Load() != 2 {
				t.Fatalf("checkpoint replay=%+v %v", r, err)
			}
			t.Log("CAPTURE checkpoint interruption: native save survived, retry upserts deterministic ID, no double strength, replay checkpointed")
		})
	}
}

func TestCaptureKilledAfterSaveBeforeCheckpoint(t *testing.T) {
	f := scopetest.New(t)
	model := fakeCapture(t, f, line("decision", "high", "Synthetic killed-process quartz policy.", ""))
	requested, release := make(chan struct{}), make(chan struct{})
	model.beforeReply.Store(func() {
		if model.calls.Load() == 1 {
			close(requested)
			<-release
		}
	})
	if err := config.Save(f.App.Cfg); err != nil {
		t.Fatal(err)
	}
	before, _ := ingest.LoadNative(app.NativePath())
	cmd := exec.Command(os.Args[0], "-test.run=^TestCaptureKillChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MUNINN_CAPTURE_KILL_CHILD=1", "MUNINN_CAPTURE_KILL_CWD="+f.Cwd, "MUNINN_CAPTURE_KILL_HOME="+filepath.Join(f.Root, "alpha", ".jcode"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	select {
	case <-requested:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not reach provider")
	}
	// Hold the SQLite writer while the child is extracting. WAL permits its
	// reads and native save, but guarantees it cannot commit the checkpoint.
	tx, err := f.App.DB.Begin()
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('capture-kill-test','locked')`); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mems, err := ingest.LoadNative(app.NativePath())
		if err == nil && len(mems) == len(before)+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never persisted native batch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child was not interrupted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var seen int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&seen); err != nil || seen != 0 {
		t.Fatalf("checkpoint crossed writer lock: %d %v", seen, err)
	}
	// Simulate the dead worker lease expiring without slowing the test 55s.
	if _, err := f.App.DB.Exec(`DELETE FROM capture_lock`); err != nil {
		t.Fatal(err)
	}
	r, err := f.Bind(t, "alpha").Capture(context.Background(), f.App, captureOpts("killed-process"))
	if err != nil || r.Processed != 1 || len(r.Actions) != 1 || r.Actions[0].Action != "skip" {
		t.Fatalf("retry=%+v err=%v", r, err)
	}
	after, err := ingest.LoadNative(app.NativePath())
	if err != nil || len(after) != len(before)+1 {
		t.Fatalf("duplicate after killed process: %v", err)
	}
	t.Log("CAPTURE kill: child killed after native save while SQLite checkpoint blocked; lease expired; retry preserved one ID and committed checkpoint")
}

func TestCaptureKillChild(t *testing.T) {
	if os.Getenv("MUNINN_CAPTURE_KILL_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	a, err := app.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b := a.Bound(os.Getenv("MUNINN_CAPTURE_KILL_HOME"), os.Getenv("MUNINN_CAPTURE_KILL_CWD"))
	if _, err := b.Capture(context.Background(), a, captureOpts("killed-process")); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFreshNativeSnapshotDedupesBeforeLazyIndex(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	fakeCapture(t, f, line("decision", "high", "Synthetic quartz pending-index preference.", ""))
	before, _ := ingest.LoadNative(app.NativePath())
	first, err := b.Capture(context.Background(), f.App, captureOpts("snapshot-first"))
	if err != nil || first.Actions[0].Action != "add" {
		t.Fatalf("first=%+v %v", first, err)
	}
	second, err := b.Capture(context.Background(), f.App, captureOpts("snapshot-next-session"))
	if err != nil || second.Actions[0].Action != "reinforce" || second.Actions[0].Target == "" {
		t.Fatalf("second=%+v %v", second, err)
	}
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil || len(mems) != len(before)+1 {
		t.Fatalf("unindexed native was duplicated: %v", err)
	}
	for _, m := range mems {
		if m.Content == "Synthetic quartz pending-index preference." && m.Strength != 2 {
			t.Fatalf("strength=%d", m.Strength)
		}
	}
}
