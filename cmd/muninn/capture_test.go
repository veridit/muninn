package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

func TestCaptureCLIHookUsesPersistedClientBinding(t *testing.T) {
	f := scopetest.New(t)
	t.Chdir(f.Cwd)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"category":"preference","trust":"high","content":"Client prefers the quartz protocol."}`}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	cfg := f.App.Cfg
	cfg.Extract.Provider = "ollama"
	cfg.Extract.BaseURL = server.URL
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(f.Root, "alpha", ".jcode")
	os.MkdirAll(filepath.Join(home, "sessions"), 0700)
	var messages []any
	for i := 0; i < 12; i++ {
		messages = append(messages, map[string]any{"id": string(rune('a' + i)), "role": "user", "content": []map[string]string{{"type": "text", "text": "Use quartz for future deployment work."}}})
	}
	data, _ := json.Marshal(map[string]any{"working_dir": f.ClientCwd, "messages": messages})
	os.WriteFile(filepath.Join(home, "sessions", "example.json"), data, 0600)
	t.Setenv("JCODE_HOME", home)
	t.Setenv("JCODE_HOOK_SESSION_ID", "example")
	t.Setenv("JCODE_HOOK_EVENT", "turn_end")
	c := captureCmd()
	var stdout bytes.Buffer
	c.SetOut(&stdout)
	c.SetArgs([]string{"--hook"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || stdout.Len() != 0 {
		t.Fatal("observer output/request contract")
	}
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mems {
		if m.Content == "Client prefers the quartz protocol." {
			found = true
			if m.Scope != "client:acme" {
				t.Fatalf("persisted client cwd ignored: %s", m.Scope)
			}
		}
	}
	if !found {
		t.Fatal("capture missing")
	}
	log, err := os.ReadFile(filepath.Join(config.DataDir(), "hook.log"))
	if err != nil || !strings.Contains(string(log), "processed=12") || strings.Contains(string(log), "Client prefers") {
		t.Fatalf("unsafe/missing log: %v", err)
	}
	t.Setenv("JCODE_HOOK_EVENT", "session_end")
	c = captureCmd()
	c.SetArgs([]string{"--hook"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("session end replayed existing messages")
	}
	// Generic dry-run exposes the requested table, never memory writes.
	c = captureCmd()
	c.SetIn(strings.NewReader("user: I prefer quartz."))
	stdout.Reset()
	c.SetOut(&stdout)
	c.SetArgs([]string{"--session", "generic", "--dry-run"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CATEGORY", "TRUST", "ACTION", "CONTENT", "Client prefers the quartz protocol."} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %s", want)
		}
	}
	t.Log("CAPTURE CLI hook: detached-observer entrypoint used session client cwd, logged counts only, session-end replay adds=0, dry-run table rendered")
}
