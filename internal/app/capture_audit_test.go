package app_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureAuditPrivateDryRunAndRetention(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	secret := "sk-" + strings.Repeat("z", 30)
	fakeCapture(t, f, line("decision", "high", "Use quartz for durable deployment policy.", "")+"\n"+line("fact", "high", secret, "")+"\ngarbage")
	before, err := os.ReadFile(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	o := captureOpts("audit-dry")
	o.Home = "/synthetic/instance"
	o.Trigger = "session_end"
	o.DryRun = true
	o.Messages = append(o.Messages, harness.CaptureMessage{ID: "secret-message", Role: "user", Text: secret})
	report, err := b.Capture(context.Background(), f.App, o)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run mutated native memories")
	}
	for _, table := range []string{"capture_seen", "capture_bindings", "capture_state"} {
		var n int
		if err := f.App.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("dry-run mutated %s count=%d err=%v", table, n, err)
		}
	}
	runs, err := f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	r := runs[0]
	if !r.DryRun || r.Trigger != "session_end" || r.Home != o.Home || r.Scope == "" || r.Model == "" || len(r.Messages) != 2 || r.Messages[1].Text != "" || r.Messages[1].Hash == "" || len(r.Calls) != 1 || len(r.Calls[0].Request) == 0 || len(r.Calls[0].Rejections) != 2 || r.Calls[0].Rejections[0].Reason != "secret withheld" || len(r.Report.Actions) != 1 || r.Report.Actions[0].Reason == "" || report.Usage.TotalTokens != 15 {
		t.Fatalf("incomplete audit: %+v", r)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), secret) {
		t.Fatal("secret text leaked to audit")
	}
	var stored string
	if err := f.App.DB.QueryRow(`SELECT record FROM capture_runs`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) {
		t.Fatal("secret text leaked into stored record")
	}
	var count int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_actions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("action rows=%d err=%v", count, err)
	}
	old := r
	old.Time = time.Now().Add(-48 * time.Hour)
	f.App.Cfg.Extract.AuditDays = 1
	if err := f.App.SaveAudit(old); err != nil {
		t.Fatal(err)
	}
	runs, err = f.App.AuditRuns("", time.Now().Add(-72*time.Hour))
	if err != nil || len(runs) != 1 {
		t.Fatalf("retention runs=%d err=%v", len(runs), err)
	}
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_actions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphaned actions=%d err=%v", count, err)
	}
	t.Log("CAPTURE audit: sanitized request/output, message hashes, rejection reasons, dry-run immutability, atomic retention: ok")
}

func TestCaptureAuditFailureAndSkipped(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	fakeCapture(t, f, "")
	f.App.Cfg.Extract.Provider = "openai-compatible"
	f.App.Cfg.Extract.APIKeyEnv = "MUNINN_TEST_MISSING_KEY"
	t.Setenv("MUNINN_TEST_MISSING_KEY", "")
	o := captureOpts("audit-failed")
	_, err := b.Capture(context.Background(), f.App, o)
	if err == nil {
		t.Fatal("missing key accepted")
	}
	runs, err := f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil || len(runs) != 1 || runs[0].Error == "" || len(runs[0].Calls) != 1 {
		t.Fatalf("failure not audited: %+v %v", runs, err)
	}
	f.App.Cfg.Extract.Provider = "off"
	_, err = b.Capture(context.Background(), f.App, o)
	if err != nil {
		t.Fatal(err)
	}
	runs, err = f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil || len(runs) != 2 || runs[0].Report.Skipped != "extraction off" {
		t.Fatalf("skip not audited: %+v %v", runs, err)
	}
}
