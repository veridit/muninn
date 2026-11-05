package harness

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CaptureMessage preserves stable ids across journal checkpointing. Text contains
// only user/assistant text blocks, never tool calls/results or background DMs.
type CaptureMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}
type CaptureTranscript struct {
	Cwd      string
	Messages []CaptureMessage
}

type captureStored struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	DisplayRole string `json:"display_role"`
	Content     []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func captureText(m captureStored) string {
	if (m.Role != "user" && m.Role != "assistant") || m.DisplayRole != "" {
		return ""
	}
	var parts []string
	for _, b := range m.Content {
		if b.Type != "text" {
			continue
		}
		t := strings.TrimSpace(b.Text)
		if strings.HasPrefix(t, "DM from") || strings.HasPrefix(t, "Cross-swarm DM") || strings.HasPrefix(t, "<system-reminder>") || strings.HasPrefix(t, "[tool ") {
			continue
		}
		// A secret-bearing block is withheld in its entirety, before remote egress.
		// The caller applies the shared secret detector without rewriting facts.
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// ReadCaptureSession is read-only. Stable message ids prevent duplicated replay
// if a snapshot and its old journal temporarily coexist during checkpointing.
func ReadCaptureSession(home, id string) (CaptureTranscript, error) {
	var out CaptureTranscript
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return out, os.ErrInvalid
	}
	base := filepath.Join(home, "sessions", id)
	f, err := os.Open(base + ".json")
	if err != nil {
		return out, err
	}
	var snap struct {
		WorkingDir string          `json:"working_dir"`
		Messages   []captureStored `json:"messages"`
	}
	err = json.NewDecoder(io.LimitReader(f, 256<<20)).Decode(&snap)
	f.Close()
	if err != nil {
		return out, errors.New("invalid jcode session snapshot")
	}
	out.Cwd = snap.WorkingDir
	seen := map[string]bool{}
	add := func(ms []captureStored) {
		for _, m := range ms {
			id := m.ID
			if id == "" {
				b, _ := json.Marshal(m)
				h := sha256.Sum256(b)
				id = hex.EncodeToString(h[:])
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			out.Messages = append(out.Messages, CaptureMessage{ID: id, Role: m.Role, Text: captureText(m)})
		}
	}
	add(snap.Messages)
	j, err := os.Open(base + ".journal.jsonl")
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	sc := bufio.NewScanner(io.LimitReader(j, 256<<20))
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		var entry struct {
			Meta struct {
				WorkingDir string `json:"working_dir"`
			} `json:"meta"`
			Append []captureStored `json:"append_messages"`
		}
		// A torn trailing write is normal. Other malformed lines are left for a
		// later snapshot rather than marking any of their messages processed.
		if json.Unmarshal(sc.Bytes(), &entry) != nil {
			continue
		}
		if entry.Meta.WorkingDir != "" {
			out.Cwd = entry.Meta.WorkingDir
		}
		add(entry.Append)
	}
	if sc.Err() != nil {
		return out, errors.New("jcode journal exceeds read limit")
	}
	if out.Cwd == "" {
		return out, errors.New("session has no working directory, refusing ambiguous binding")
	}
	return out, nil
}

// CaptureLines adapts generic stdin chunks. Repeating an identical chunk with
// the same session id is idempotent, while repeated lines retain their ordinal.
func CaptureLines(text string) []CaptureMessage {
	var out []CaptureMessage
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 4096), 256<<10)
	counts := map[string]int{}
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if t == "" {
			continue
		}
		h := sha256.Sum256([]byte(t))
		key := hex.EncodeToString(h[:])
		counts[key]++
		// Include occurrence in the stable identity to distinguish identical lines.
		b, _ := json.Marshal([]any{key, counts[key]})
		id := sha256.Sum256(b)
		if strings.HasPrefix(t, "DM from") || strings.HasPrefix(t, "Cross-swarm DM") || strings.HasPrefix(t, "[tool ") {
			t = ""
		}
		out = append(out, CaptureMessage{ID: hex.EncodeToString(id[:]), Role: "transcript", Text: t})
	}
	return out
}
