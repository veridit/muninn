package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Memory is one memory read from a harness store or created natively.
type Memory struct {
	Key      string
	Content  string
	Category string
	Tags     []string
	Active   bool
	Updated  time.Time
}

// jcodeFile is the subset of a jcode memory graph file muninn reads.
// Observed on disk: {"graph_version":…, "memories": {id: {...}}, ...}.
// category is usually a string but can be {"custom": "..."}.
type jcodeFile struct {
	Memories map[string]struct {
		ID        string          `json:"id"`
		Content   string          `json:"content"`
		Category  json.RawMessage `json:"category"`
		Tags      []string        `json:"tags"`
		Active    *bool           `json:"active"`
		UpdatedAt string          `json:"updated_at"`
	} `json:"memories"`
}

// jcodeCategory decodes a category that is either "fact" or {"custom":"..."}.
func jcodeCategory(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var c struct {
		Custom string `json:"custom"`
	}
	if json.Unmarshal(raw, &c) == nil && c.Custom != "" {
		return "custom"
	}
	return ""
}

// ReadJcodeFile parses one jcode memory graph JSON file (read-only).
func ReadJcodeFile(path string) ([]Memory, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f jcodeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(f.Memories))
	for id, m := range f.Memories {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		key := m.ID
		if key == "" {
			key = id
		}
		t, _ := time.Parse(time.RFC3339Nano, m.UpdatedAt)
		out = append(out, Memory{
			Key: key, Content: m.Content, Category: jcodeCategory(m.Category), Tags: m.Tags,
			Active: m.Active == nil || *m.Active, Updated: t,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// JcodeFiles lists jcode memory stores under dir: global.json and
// projects/*.json. Scope is "<prefix>" for global.json and
// "<prefix>:<project-hash>" for project stores.
func JcodeFiles(dir, prefix string) (map[string]string, error) {
	files := map[string]string{}
	if _, err := os.Stat(filepath.Join(dir, "global.json")); err == nil {
		files[filepath.Join(dir, "global.json")] = prefix
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "projects", "*.json"))
	for _, m := range matches {
		files[m] = prefix + ":" + strings.TrimSuffix(filepath.Base(m), ".json")
	}
	return files, nil
}
