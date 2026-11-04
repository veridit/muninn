package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InstallResult describes what an installer did (or would do).
type InstallResult struct {
	Harness string
	File    string
	Backup  string
	Changed bool
	Note    string
}

func (r InstallResult) String() string {
	s := fmt.Sprintf("%s: %s", r.Harness, r.File)
	switch {
	case !r.Changed:
		s += " (already up to date)"
	case r.Backup != "":
		s += " (backup " + r.Backup + ")"
	}
	if r.Note != "" {
		s += "\n  " + r.Note
	}
	return s
}

func backup(path string, dry bool) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	b := path + ".muninn-bak-" + time.Now().Format("20060102-150405")
	if dry {
		return b, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return b, os.WriteFile(b, data, 0o600)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".muninn-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hookEntry is one {"hooks":[{type,command,timeout}]} group in a
// Claude Code settings.json or a Codex hooks.json.
func isMuninnGroup(g any) bool {
	m, ok := g.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm["command"].(string); strings.Contains(c, "muninn") && strings.Contains(c, " hook ") {
				return true
			}
		}
	}
	return false
}

// editPromptHook adds (install) or removes (uninstall) muninn's
// UserPromptSubmit group in a JSON hooks file, preserving everything else.
func editPromptHook(path, command string, timeout int, install, dry bool) (InstallResult, error) {
	res := InstallResult{File: path}
	root := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &root); err != nil {
			return res, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	groups, _ := hooks["UserPromptSubmit"].([]any)
	var kept []any
	found := false
	for _, g := range groups {
		if isMuninnGroup(g) {
			found = true
			continue
		}
		kept = append(kept, g)
	}
	want := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": timeout}}}
	if install {
		kept = append(kept, want)
	}
	if install && found {
		// Replace only if the command changed.
		for _, g := range groups {
			if isMuninnGroup(g) {
				old, _ := json.Marshal(g)
				nw, _ := json.Marshal(want)
				if bytes.Equal(old, nw) {
					return res, nil
				}
			}
		}
	}
	if !install && !found {
		return res, nil
	}
	if len(kept) == 0 {
		delete(hooks, "UserPromptSubmit")
	} else {
		hooks["UserPromptSubmit"] = kept
	}
	root["hooks"] = hooks
	out, _ := json.MarshalIndent(root, "", "  ")
	out = append(out, '\n')
	res.Changed = true
	var err error
	if res.Backup, err = backup(path, dry); err != nil {
		return res, err
	}
	if dry {
		return res, nil
	}
	return res, writeAtomic(path, out)
}

// InstallClaude wires `muninn hook claude` into ~/.claude/settings.json.
func InstallClaude(bin string, install, dry bool) (InstallResult, error) {
	home, _ := os.UserHomeDir()
	r, err := editPromptHook(filepath.Join(home, ".claude", "settings.json"), shellQuote(bin)+" hook claude", 15, install, dry)
	r.Harness = "claude"
	return r, err
}

// InstallCodex wires `muninn hook codex` into ~/.codex/hooks.json. Codex
// requires the user to trust a new hook before it runs.
func InstallCodex(bin string, install, dry bool) (InstallResult, error) {
	home, _ := os.UserHomeDir()
	r, err := editPromptHook(filepath.Join(home, ".codex", "hooks.json"), shellQuote(bin)+" hook codex", 15, install, dry)
	r.Harness = "codex"
	if install && r.Changed {
		r.Note = "Codex runs new hooks only after you trust them: open Codex and approve it in /hooks."
	}
	return r, err
}

// InstallJcode wires `muninn hook jcode` into [hooks] turn_start of
// ~/.jcode/config.toml with a minimal line edit that keeps other hooks.
func InstallJcode(bin string, install, dry bool) (InstallResult, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".jcode", "config.toml")
	res := InstallResult{Harness: "jcode", File: path}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	cmd := bin + " hook jcode"
	text, changed, err := editJcodeTurnStart(string(b), cmd, install)
	if err != nil || !changed {
		return res, err
	}
	res.Changed = true
	if res.Backup, err = backup(path, dry); err != nil {
		return res, err
	}
	if dry {
		return res, nil
	}
	return res, writeAtomic(path, []byte(text))
}

const jcodeMarker = "# muninn: automatic local memory recall"

// editJcodeTurnStart edits the turn_start key inside [hooks]. jcode accepts a
// string or an array of strings, so an existing hook is kept alongside muninn.
func editJcodeTurnStart(text, cmd string, install bool) (string, bool, error) {
	lines := strings.Split(text, "\n")
	inHooks, hooksAt, keyAt := false, -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			inHooks = t == "[hooks]"
			if inHooks {
				hooksAt = i
			}
			continue
		}
		if inHooks && (strings.HasPrefix(t, "turn_start=") || strings.HasPrefix(t, "turn_start ")) {
			keyAt = i
		}
	}
	quoted := fmt.Sprintf("%q", cmd)
	var existing []string
	if keyAt >= 0 {
		v := strings.TrimSpace(lines[keyAt][strings.Index(lines[keyAt], "=")+1:])
		if strings.HasPrefix(v, "[") {
			var arr []string
			if err := json.Unmarshal([]byte(v), &arr); err != nil {
				return text, false, fmt.Errorf("cannot parse turn_start %s: edit it by hand", v)
			}
			existing = arr
		} else if v != "" && v != `""` {
			var s string
			if err := json.Unmarshal([]byte(v), &s); err != nil {
				return text, false, fmt.Errorf("cannot parse turn_start %s: edit it by hand", v)
			}
			existing = []string{s}
		}
	}
	var next []string
	has := false
	for _, e := range existing {
		if strings.Contains(e, "muninn") && strings.HasSuffix(e, " hook jcode") {
			has = true
			if !install {
				continue
			}
			e = cmd
		}
		next = append(next, e)
	}
	if install && !has {
		next = append(next, cmd)
	}
	if install && has && len(next) == len(existing) && strings.Join(next, "\x00") == strings.Join(existing, "\x00") {
		return text, false, nil
	}
	if !install && !has {
		return text, false, nil
	}
	var value string
	switch len(next) {
	case 0:
		value = ""
	case 1:
		value = fmt.Sprintf("%q", next[0])
	default:
		b, _ := json.Marshal(next)
		value = string(b)
	}
	_ = quoted
	switch {
	case keyAt >= 0 && value == "":
		lines = append(lines[:keyAt], lines[keyAt+1:]...)
		if keyAt > 0 && strings.TrimSpace(lines[keyAt-1]) == jcodeMarker {
			lines = append(lines[:keyAt-1], lines[keyAt:]...)
		}
	case keyAt >= 0:
		lines[keyAt] = "turn_start = " + value
	case hooksAt >= 0:
		ins := []string{jcodeMarker, "turn_start = " + value}
		lines = append(lines[:hooksAt+1], append(ins, lines[hooksAt+1:]...)...)
	default:
		lines = append(lines, "", "[hooks]", jcodeMarker, "turn_start = "+value)
	}
	return strings.Join(lines, "\n"), true, nil
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
