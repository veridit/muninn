package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureHookBoundsInitialBacklogAndBackfills(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	provider := fakeCapture(t, f, "")
	f.App.Cfg.Extract.EveryTurns = 2
	o := captureOpts("old-session")
	o.Messages = nil
	o.Trigger = "session_end"
	for i := 0; i < 1000; i++ {
		o.Messages = append(o.Messages, harness.CaptureMessage{ID: fmt.Sprintf("u-%d", i), Role: "user", Text: fmt.Sprintf("Durable quartz policy turn %d.", i)}, harness.CaptureMessage{ID: fmt.Sprintf("a-%d", i), Role: "assistant", Text: "Acknowledged."})
	}
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.BacklogSkipped != 1992 || r.Processed != 8 || strings.Contains(provider.input, "policy turn 0.") || !strings.Contains(provider.input, "policy turn 996.") {
		t.Fatalf("unbounded backlog report=%+v input=%s", r, provider.input)
	}
	var n int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_backlog`).Scan(&n); err != nil || n != 1992 {
		t.Fatalf("backlog=%d err=%v", n, err)
	}
	runs, err := f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil || runs[0].Report.BacklogSkipped != 1992 {
		t.Fatalf("skip not audited: %+v %v", runs, err)
	}
	o.Trigger = ""
	o.Backfill = true
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 1992 || r.BacklogSkipped != 0 {
		t.Fatalf("backfill=%+v err=%v", r, err)
	}
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_backlog`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("backlog not cleared=%d err=%v", n, err)
	}
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 0 {
		t.Fatalf("replayed=%+v err=%v", r, err)
	}
	t.Log("CAPTURE backlog: initial hook skipped=1992 processed=8, manual backfill processed=1992, replay processed=0")
}

func TestCaptureSlowLaterChunkKeepsEarlierCheckpoint(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	var calls atomic.Int32
	var slow atomic.Bool
	slow.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n > 1 && slow.Load() {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": line("fact", "high", "Quartz deployments need a durable checklist.", "")}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	f.App.Cfg.Extract = extract.Defaults()
	f.App.Cfg.Extract.Provider = "ollama"
	f.App.Cfg.Extract.BaseURL = server.URL
	o := captureOpts("slow-chunks")
	o.Backfill = true
	o.Messages = []harness.CaptureMessage{{ID: "first", Role: "user", Text: "quartz " + strings.Repeat("x", 30<<10)}, {ID: "second", Role: "user", Text: "quartz " + strings.Repeat("y", 30<<10)}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := b.Capture(ctx, f.App, o)
	if !errors.Is(err, context.DeadlineExceeded) || r.Processed != 1 {
		t.Fatalf("partial=%+v err=%v", r, err)
	}
	var n int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("earlier checkpoint lost count=%d err=%v", n, err)
	}
	runs, err := f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil || runs[0].ErrorClass != "timeout" || runs[0].Report.Processed != 1 {
		t.Fatalf("partial timeout audit=%+v err=%v", runs, err)
	}
	slow.Store(false)
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 1 || calls.Load() != 3 {
		t.Fatalf("retry=%+v calls=%d err=%v", r, calls.Load(), err)
	}
	t.Log("CAPTURE slow provider: chunk 1 checkpoint survived chunk 2 timeout; retry processed only chunk 2")
}
