package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureIgnoresOwnerReadView(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha").View(true, nil)
	m := fakeCapture(t, f, line("fact", "high", "Quartz owner view remains bound.", ""))
	if _, err := b.Capture(context.Background(), f.App, captureOpts("owner-view")); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{"Matrix(Beta)", "Matrix(Client)", "Matrix(OtherClient)"} {
		if strings.Contains(m.input, foreign) {
			t.Fatal("owner view widened capture context")
		}
	}
}

func TestCaptureSessionCannotChangeClientBinding(t *testing.T) {
	f := scopetest.New(t)
	m := fakeCapture(t, f, line("fact", "high", "Quartz client policy.", ""))
	o := captureOpts("fixed-binding")
	b := f.Bind(t, "client")
	if _, err := b.Capture(context.Background(), f.App, o); err != nil {
		t.Fatal(err)
	}
	b = f.Bind(t, "alpha")
	_, err := b.Capture(context.Background(), f.App, o)
	if err == nil || !strings.Contains(err.Error(), "binding changed") || m.calls.Load() != 1 {
		t.Fatal("session crossed client binding")
	}
	t.Log("CAPTURE binding drift: previously captured client session refused under non-client binding before LLM request")
}

func TestCaptureFinalObserverWaitsForLeaseWithinBudget(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	m := fakeCapture(t, f, line("fact", "high", "Quartz final preference.", ""))
	_, err := f.App.DB.Exec(`INSERT INTO capture_lock(id,token,expires_ns) VALUES(1,'fixture',?)`, time.Now().Add(time.Minute).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	o := captureOpts("final-lease")
	o.Periodic = true
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || !strings.Contains(r.Skipped, "already running") || m.calls.Load() != 0 {
		t.Fatal("periodic lease skip failed")
	}
	o.Periodic = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = b.Capture(ctx, f.App, o)
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("final lease wait unbounded")
	}
	f.App.DB.Exec(`DELETE FROM capture_lock`)
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 1 || m.calls.Load() != 1 {
		t.Fatal("final flush after lease failed")
	}
	t.Log("CAPTURE final lease: periodic observer skips busy lease; final observer waits only within deadline and flushes after lease release")
}
