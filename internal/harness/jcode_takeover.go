package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// jcode has its own memory system (extraction sidecar, recall, and a
// `memory` tool the model calls to save). With muninn installed that is a
// second, competing store: the model saves into jcode's files ("🧠 saved")
// while muninn recalls and corrects. InstallJcodeHome therefore hands memory
// to muninn per instance:
//
//   - [features] memory = false      jcode's extraction and recall stop
//   - [tools] disabled += "memory"   the model no longer sees jcode's tool
//   - mcp.json servers.muninn        it saves through muninn's MCP tools
//
// jcode's existing memory files stay where they are and muninn keeps
// indexing them read-only, so nothing already remembered is lost.

// setTOMLKey sets key = value inside [section] (adding the section or key as
// needed) and returns the new text and whether it changed. value is TOML.
func setTOMLKey(text, section, key, value string) (string, bool) {
	lines := strings.Split(text, "\n")
	in, secAt, keyAt := false, -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = t == "["+section+"]"
			if in {
				secAt = i
			}
			continue
		}
		if in && (strings.HasPrefix(t, key+"=") || strings.HasPrefix(t, key+" ")) {
			keyAt = i
		}
	}
	want := key + " = " + value
	switch {
	case keyAt >= 0:
		if strings.TrimSpace(lines[keyAt]) == want {
			return text, false
		}
		lines[keyAt] = want
	case secAt >= 0:
		lines = append(lines[:secAt+1], append([]string{want}, lines[secAt+1:]...)...)
	default:
		lines = append(lines, "", "["+section+"]", want)
	}
	return strings.Join(lines, "\n"), true
}

// getTOMLKey returns the raw TOML value of key in [section], or "".
func getTOMLKey(text, section, key string) string {
	in := false
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			in = t == "["+section+"]"
			continue
		}
		if in && (strings.HasPrefix(t, key+"=") || strings.HasPrefix(t, key+" ")) {
			return strings.TrimSpace(t[strings.Index(t, "=")+1:])
		}
	}
	return ""
}

// editJcodeMemoryTakeover turns jcode's own memory off (install) or back on
// (uninstall), keeping every other tool the user disabled.
func editJcodeMemoryTakeover(text string, install bool) (string, bool, error) {
	changed := false
	var c bool
	text, c = setTOMLKey(text, "features", "memory", fmt.Sprint(!install))
	changed = changed || c
	var disabled []string
	if v := getTOMLKey(text, "tools", "disabled"); v != "" {
		if err := json.Unmarshal([]byte(v), &disabled); err != nil {
			return text, false, fmt.Errorf("cannot parse [tools] disabled = %s: edit it by hand", v)
		}
	}
	has := false
	var next []string
	for _, d := range disabled {
		if d == "memory" {
			has = true
			if !install {
				continue
			}
		}
		next = append(next, d)
	}
	if install && !has {
		next = append(next, "memory")
	}
	if has != install {
		if next == nil {
			next = []string{}
		}
		b, _ := json.Marshal(next)
		text, c = setTOMLKey(text, "tools", "disabled", string(b))
		changed = changed || c
	}
	return text, changed, nil
}

// editJcodeMCP adds (or removes) the muninn MCP server in a jcode mcp.json,
// keeping every other server and field untouched.
func editJcodeMCP(raw []byte, bin, jcodeHome string, install bool) ([]byte, bool, error) {
	doc := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return raw, false, fmt.Errorf("cannot parse mcp.json: %w", err)
		}
	}
	servers := map[string]json.RawMessage{}
	if s, ok := doc["servers"]; ok {
		if err := json.Unmarshal(s, &servers); err != nil {
			return raw, false, fmt.Errorf("cannot parse mcp.json servers: %w", err)
		}
	}
	// --jcode-home pins the instance, so the MCP server is scoped like the
	// hook even if jcode does not pass JCODE_HOME to MCP children.
	args := []string{"mcp"}
	if jcodeHome != "" {
		args = append(args, "--jcode-home", jcodeHome)
	}
	want, _ := json.Marshal(map[string]any{"command": bin, "args": args, "env": map[string]string{}})
	cur, has := servers["muninn"]
	switch {
	case install && has && jsonEqual(cur, want):
		return raw, false, nil
	case install:
		servers["muninn"] = want
	case !has:
		return raw, false, nil
	default:
		delete(servers, "muninn")
	}
	s, _ := json.Marshal(servers)
	doc["servers"] = s
	out, err := json.MarshalIndent(doc, "", "  ")
	return append(out, '\n'), true, err
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && fmt.Sprint(x) == fmt.Sprint(y)
}

// takeOverJcodeMemory handles the MCP half (config.toml is edited together
// with the hook in InstallJcodeHome, under one backup). The mcp.json file
// may hold credentials of other servers: it is rewritten in place with the
// same permissions, never copied anywhere but its own timestamped backup.
func takeOverJcodeMemory(jcodeHome, bin string, install, dry bool) ([]string, error) {
	var notes []string
	mcpPath := filepath.Join(jcodeHome, "mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return notes, err
	}
	out, changed, err := editJcodeMCP(raw, bin, jcodeHome, install)
	if err != nil {
		return notes, err
	}
	if changed {
		if !dry {
			if len(raw) > 0 {
				if _, err := backup(mcpPath, false); err != nil {
					return notes, err
				}
			}
			if err := writeAtomic(mcpPath, out); err != nil { // keeps the file's mode
				return notes, err
			}
		}
		if install {
			notes = append(notes, "muninn MCP server added to mcp.json")
		} else {
			notes = append(notes, "muninn MCP server removed from mcp.json")
		}
	}
	return notes, nil
}
