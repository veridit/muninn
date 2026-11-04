// Package harness adapts recall to each agent tool's hook protocol.
//
//   - Claude Code and Codex: a synchronous UserPromptSubmit command hook. The
//     hook reads {"prompt","session_id","cwd",...} on stdin and prints
//     {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":...}}.
//     Context lands before the model answers. (Verified against the Claude Code
//     hooks reference and codex-rs/hooks/src/engine/output_parser.rs.)
//   - jcode: turn_start is a fire-and-forget observer without the prompt, so
//     the adapter reads the last user message from the session journal and
//     delivers the block with `jcode transcript --mode send -S <session>`.
//     It arrives at the next safe point of the running turn.
package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PromptHookInput is the stdin payload of Claude Code / Codex UserPromptSubmit.
type PromptHookInput struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
}

// ReadPromptHook parses the hook payload from r.
func ReadPromptHook(r io.Reader) (PromptHookInput, error) {
	var in PromptHookInput
	b, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return in, err
	}
	err = json.Unmarshal(b, &in)
	return in, err
}

// PromptHookOutput renders the additionalContext response. An empty block
// yields no output at all (the prompt proceeds unchanged).
func PromptHookOutput(block string) []byte {
	if block == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": block,
		},
	})
	return append(b, '\n')
}

// jcodeMessage is the subset of a jcode message muninn reads.
type jcodeMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// userText returns the typed text of a user message, or "" for tool results
// and harness-generated notices.
func userText(m jcodeMessage) string {
	if m.Role != "user" {
		return ""
	}
	var parts []string
	for _, c := range m.Content {
		if (c.Type == "text" || c.Type == "") && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	t := strings.TrimSpace(strings.Join(parts, " "))
	if strings.HasPrefix(t, "<system-reminder>") || strings.HasPrefix(t, "You have ") && strings.Contains(t, "incomplete todos") {
		return ""
	}
	return t
}

// JcodeLastUserMessage returns the most recent user text of a jcode session.
// jcode keeps a snapshot (<id>.json, {"messages":[...]}) and an append journal
// (<id>.journal.jsonl, lines with {"append_messages":[...]}) that is compacted
// into the snapshot; the journal is newer, so it wins when it has user text.
func JcodeLastUserMessage(sessionsDir, sessionID string) (string, error) {
	if strings.ContainsAny(sessionID, `/\`) || sessionID == "" {
		return "", os.ErrInvalid
	}
	base := filepath.Join(sessionsDir, sessionID)
	if t, err := lastFromJournal(base + ".journal.jsonl"); err == nil && t != "" {
		return t, nil
	}
	b, err := os.ReadFile(base + ".json")
	if err != nil {
		return "", err
	}
	var snap struct {
		Messages []jcodeMessage `json:"messages"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return "", err
	}
	for i := len(snap.Messages) - 1; i >= 0; i-- {
		if t := userText(snap.Messages[i]); t != "" {
			return t, nil
		}
	}
	return "", nil
}

func lastFromJournal(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	const tail = 2 << 20
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		f.Seek(fi.Size()-tail, io.SeekStart)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	last := ""
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"append_messages"`)) || !bytes.Contains(line, []byte(`"user"`)) {
			continue
		}
		var entry struct {
			Append []jcodeMessage `json:"append_messages"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		for _, m := range entry.Append {
			if t := userText(m); t != "" {
				last = t
			}
		}
	}
	return last, sc.Err()
}
