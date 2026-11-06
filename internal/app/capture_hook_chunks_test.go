package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureHookByteBoundFragmentsResume(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	var calls atomic.Int32
	var slow atomic.Bool
	slow.Store(true)
	var mu sync.Mutex
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var input struct {
			Transcript string `json:"transcript"`
		}
		json.Unmarshal([]byte(request.Messages[1].Content), &input)
		if len(input.Transcript) > 1024 || !utf8.ValidString(input.Transcript) || request.MaxTokens > 1024 {
			t.Errorf("unbounded/invalid hook request bytes=%d max_tokens=%d", len(input.Transcript), request.MaxTokens)
		}
		n := calls.Add(1)
		if n == 2 && slow.Load() {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		mu.Lock()
		texts = append(texts, strings.TrimSuffix(strings.TrimPrefix(input.Transcript, "assistant: "), "\n"))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": ""}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	f.App.Cfg.Extract = extract.Defaults()
	f.App.Cfg.Extract.Provider = "ollama"
	f.App.Cfg.Extract.BaseURL = server.URL
	f.App.Cfg.Extract.HookChunkBytes = 1024
	o := captureOpts("hook-fragments")
	o.Trigger = "session_end"
	original := strings.Repeat("é", 1500) + "quartz-tail"
	o.Messages = []harness.CaptureMessage{{ID: "oversized", Role: "assistant", Text: original}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r, err := b.Capture(ctx, f.App, o)
	if !errors.Is(err, context.DeadlineExceeded) || r.Processed != 1 {
		t.Fatalf("first run=%+v err=%v", r, err)
	}
	var originalSeen int
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen WHERE message='oversized'`).Scan(&originalSeen); err != nil || originalSeen != 0 {
		t.Fatalf("original prematurely checkpointed=%d %v", originalSeen, err)
	}
	slow.Store(false)
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 2 {
		t.Fatalf("resume=%+v err=%v", r, err)
	}
	mu.Lock()
	got := strings.Join(texts, "")
	mu.Unlock()
	if got != original {
		t.Fatal("fragment tail dropped or completed prefix replayed")
	}
	if err := f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen WHERE message='oversized'`).Scan(&originalSeen); err != nil || originalSeen != 1 {
		t.Fatalf("original checkpoint missing=%d %v", originalSeen, err)
	}
	before := calls.Load()
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Processed != 0 || calls.Load() != before {
		t.Fatalf("replayed completed fragments=%+v %v", r, err)
	}
	runs, err := f.App.AuditRuns(o.Session, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if runs[1].Messages[0].OriginalID != "oversized" {
		t.Fatal("audit fragment missing original identity")
	}
	t.Log("CAPTURE hook chunks: <=1024 bytes, <=1024 output tokens, UTF-8 intact, timeout resumes remaining fragments without tail loss")
}
