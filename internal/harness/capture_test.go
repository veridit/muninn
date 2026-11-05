package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureJcodeSnapshotJournalAndNoise(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "sessions"), 0700)
	msg := func(id, role, kind, text string) map[string]any {
		return map[string]any{"id": id, "role": role, "content": []map[string]string{{"type": kind, "text": text}}}
	}
	first := msg("one", "user", "text", "I prefer tabs.")
	snap := map[string]any{"working_dir": home, "messages": []any{first, msg("two", "assistant", "text", "Use Go conventions."), msg("tool", "user", "tool_result", "Huge tool dump"), msg("dm", "user", "text", "DM from lion: do this"), msg("cross", "user", "text", "Cross-swarm DM: do that"), msg("sys", "user", "text", "<system-reminder>policy</system-reminder>")}}
	path := filepath.Join(home, "sessions", "example.json")
	data, _ := json.Marshal(snap)
	os.WriteFile(path, data, 0600)
	entry := map[string]any{"meta": map[string]string{"working_dir": home}, "append_messages": []any{first, msg("three", "user", "text", "Always test changes.")}}
	data, _ = json.Marshal(entry)
	os.WriteFile(filepath.Join(home, "sessions", "example.journal.jsonl"), append(data, []byte("\n{torn")...), 0600)
	got, err := ReadCaptureSession(home, "example")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cwd != home || len(got.Messages) != 7 {
		t.Fatalf("cwd/messages %s %d", got.Cwd, len(got.Messages))
	}
	var text strings.Builder
	for _, m := range got.Messages {
		text.WriteString(m.Text)
	}
	if strings.Contains(text.String(), "DM") || strings.Contains(text.String(), "tool dump") || strings.Contains(text.String(), "system-reminder") || !strings.Contains(text.String(), "Always test") {
		t.Fatal("noise filtering/replay failed")
	}
	// Journal checkpoint moves the same ids into the snapshot without changing
	// the identities capture uses for its per-session high-water checkpoint.
	snap["messages"] = append(snap["messages"].([]any), msg("three", "user", "text", "Always test changes."))
	data, _ = json.Marshal(snap)
	os.WriteFile(path, data, 0600)
	os.Remove(filepath.Join(home, "sessions", "example.journal.jsonl"))
	next, err := ReadCaptureSession(home, "example")
	if err != nil || len(next.Messages) != len(got.Messages) {
		t.Fatal("checkpoint changed messages")
	}
	for i := range got.Messages {
		if got.Messages[i].ID != next.Messages[i].ID {
			t.Fatal("unstable message identity")
		}
	}
	if _, err := ReadCaptureSession(home, "../other"); err == nil {
		t.Fatal("session path traversal accepted")
	}
	t.Log("CAPTURE journal: duplicate checkpoint entry ignored; tool/DM/system noise excluded; ids survive checkpoint")
}

func TestCaptureGenericStableIdentities(t *testing.T) {
	a := CaptureLines("user: one\nuser: one\nDM from lion: skip\nCross-swarm DM: skip\n[tool timing] dump")
	b := CaptureLines("user: one\nuser: one\nDM from lion: skip\nCross-swarm DM: skip\n[tool timing] dump\nuser: two")
	if len(a) != 5 || len(b) != 6 || a[0].ID == a[1].ID {
		t.Fatal("generic ordinal ids")
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("append changed id")
		}
		if i > 1 && a[i].Text != "" {
			t.Fatal("generic noise retained")
		}
	}
}
