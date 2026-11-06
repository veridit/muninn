package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureLegacyCollisionPrivateCopy(t *testing.T) {
	path := os.Getenv("MUNINN_REPAIR_COPY")
	if path == "" {
		t.Skip("explicit private backup copy only")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Memories []ingest.NativeMemory `json:"memories"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	if err := os.WriteFile(app.NativePath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ingest.LoadNative(app.NativePath())
	if err != nil || len(got) != len(original.Memories) {
		t.Fatalf("row preservation failed rows=%d err=%v", len(got), err)
	}
	ids := map[string]bool{}
	payloads := map[string]int{}
	key := func(m ingest.NativeMemory) string { m.ID, m.SID = "", ""; v, _ := json.Marshal(m); return string(v) }
	for _, m := range original.Memories {
		payloads[key(m)]++
	}
	for _, m := range got {
		if ids[m.ID] {
			t.Fatal("duplicate repaired ID")
		}
		ids[m.ID] = true
		payloads[key(m)]--
	}
	for _, count := range payloads {
		if count != 0 {
			t.Fatal("payload fields changed")
		}
	}
	fakeCapture(t, f, line("fact", "high", "Synthetic dry-run repair verification.", ""))
	opts := captureOpts("legacy-copy-dry-run")
	opts.DryRun = true
	before, _ := os.ReadFile(app.NativePath())
	r, err := b.Capture(context.Background(), f.App, opts)
	if err != nil || len(r.Actions) == 0 {
		t.Fatalf("dry-run failed: %v", err)
	}
	after, _ := os.ReadFile(app.NativePath())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry-run changed repaired memories")
	}
	t.Logf("PRIVATE COPY: rows_before=%d rows_after=%d duplicate_ids=0 payloads_preserved=true dry_run=ok", len(original.Memories), len(got))
}

func TestCaptureLegacyCollisionIndexedIdentities(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	rows := []ingest.NativeMemory{
		{ID: "cap_synthetic", Origin: "capture:jcode:legacy:batch:0", Content: "Synthetic quartz first collision.", Scope: "jcode@alpha", Active: true, Created: time.Unix(100, 0).UTC()},
		{ID: "cap_synthetic", Origin: "capture:jcode:legacy:batch:0", Content: "Synthetic quartz second collision.", Scope: "jcode@alpha", Active: true, Created: time.Unix(200, 0).UTC()},
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "memories": rows})
	if err := os.WriteFile(app.NativePath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		resolved, err := f.App.DB.ResolveAll(m.SID)
		if err != nil || len(resolved) != 1 || resolved[0].Body != m.Content {
			t.Fatal("repaired identity merged with legacy identity")
		}
	}
	old, err := b.Resolve(f.App, "cap_synthetic")
	if err != nil || old.Body != rows[0].Content {
		t.Fatal("old ID no longer resolves to first row")
	}
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

func TestCaptureRetryPreservesCommittedSupersessionTarget(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	old := f.Units["Alpha"]
	other, _, err := ingest.Remember(app.NativePath(), "Synthetic alternative quartz target.", "fact", "jcode@alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	model := fakeCapture(t, f, line("correction", "high", "Original synthetic correction remains authoritative.", old.SID))
	// Fail the SQL half after native memory plus retirement intent were saved.
	if _, err := f.App.DB.Exec(`CREATE TRIGGER interrupt_retirement BEFORE UPDATE ON units BEGIN SELECT RAISE(FAIL,'synthetic interruption after saved correction'); END;`); err != nil {
		t.Fatal(err)
	}
	o := captureOpts("retirement-recovery")
	if _, err := b.Capture(context.Background(), f.App, o); err == nil {
		t.Fatal("retirement was not interrupted")
	}
	if _, err := f.App.DB.Exec(`DROP TRIGGER interrupt_retirement`); err != nil {
		t.Fatal(err)
	}
	model.reply = line("correction", "high", "Regenerated correction must not retire the alternative.", other.SID)
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 1 || len(r.Actions) != 1 || r.Actions[0].Action != "skip" || r.Actions[0].Target != old.SID {
		t.Fatalf("retry=%+v err=%v", r, err)
	}
	oldView, err := f.App.DB.Resolve(old.SID)
	if err != nil || oldView.Active {
		t.Fatalf("original target not retired: %v", err)
	}
	otherView, err := f.App.DB.Resolve(other.SID)
	if err != nil || !otherView.Active {
		t.Fatalf("regenerated target was retired: %v", err)
	}
	t.Log("CAPTURE correction retry: persisted original retirement intent recovered; regenerated target untouched")
}
