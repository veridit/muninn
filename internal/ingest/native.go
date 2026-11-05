package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/shortid"
)

// NativeMemory is a memory muninn owns (created with `muninn remember`).
type NativeMemory struct {
	Trust         string    `json:"trust,omitempty"`
	CaptureEvents []string  `json:"capture_events,omitempty"`
	ID            string    `json:"id"`
	SID           string    `json:"sid"`
	Title         string    `json:"title,omitempty"`
	Content       string    `json:"content"`
	Category      string    `json:"category,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	Active        bool      `json:"active"`
	Strength      int       `json:"strength"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	// Origin identifies an imported memory ("engram:<sync_id>",
	// "jcode@<instance>:<project>/<id>"); OriginHash is the hash of the
	// upstream text at the last import. Both are empty for native memories.
	Origin     string `json:"origin,omitempty"`
	OriginHash string `json:"origin_hash,omitempty"`
}

type nativeFile struct {
	Version  int            `json:"version"`
	Memories []NativeMemory `json:"memories"`
}

// LoadNative reads the native store; a missing file is empty.
func LoadNative(path string) ([]NativeMemory, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f nativeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	assignNativeSIDs(f.Memories)
	return f.Memories, nil
}

// SaveNative writes the store atomically (temp file + rename).
func SaveNative(path string, mems []NativeMemory) error {
	assignNativeSIDs(mems)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sort.Slice(mems, func(i, j int) bool { return mems[i].Created.Before(mems[j].Created) })
	b, err := json.MarshalIndent(nativeFile{Version: 1, Memories: mems}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func assignNativeSIDs(mems []NativeMemory) {
	used := map[string]string{}
	for i := range mems {
		m := &mems[i]
		full := shortid.Full(shortid.Identity(m.ID, m.Origin))
		if len(m.SID) < 6 || !strings.HasPrefix(full, m.SID) {
			m.SID = ""
		}
		if m.SID != "" {
			used[m.SID] = shortid.Identity(m.ID, m.Origin)
		}
	}
	for i := range mems {
		m := &mems[i]
		if m.SID != "" {
			continue
		}
		identity := shortid.Identity(m.ID, m.Origin)
		full := shortid.Full(identity)
		for n := 6; n <= len(full); n++ {
			sid := full[:n]
			if other, ok := used[sid]; !ok || other == identity {
				m.SID = sid
				used[sid] = identity
				break
			}
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "mem_" + hex.EncodeToString(b)
}

// Remember adds a memory. Exact-duplicate content in the same category and
// scope reinforces the existing memory instead of creating a copy.
func Remember(path, content, category, scope string, tags []string) (NativeMemory, bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return NativeMemory{}, false, errors.New("empty memory")
	}
	if scope == "" {
		scope = "global"
	}
	mems, err := LoadNative(path)
	if err != nil {
		return NativeMemory{}, false, err
	}
	now := time.Now().UTC()
	for i := range mems {
		m := &mems[i]
		if m.Active && m.Content == content && m.Category == category && m.Scope == scope {
			m.Strength++
			m.Updated = now
			return *m, true, SaveNative(path, mems)
		}
	}
	m := NativeMemory{ID: newID(), Content: content, Category: category, Tags: tags, Scope: scope,
		Active: true, Strength: 1, Created: now, Updated: now}
	mems = append(mems, m)
	assignNativeSIDs(mems)
	m = mems[len(mems)-1]
	return m, false, SaveNative(path, mems)
}

// Forget deactivates a memory by id (kept on disk, excluded from recall).
func Forget(path, id string) (bool, error) {
	mems, err := LoadNative(path)
	if err != nil {
		return false, err
	}
	for i := range mems {
		if (mems[i].ID == id || mems[i].SID == id) && mems[i].Active {
			mems[i].Active = false
			mems[i].Updated = time.Now().UTC()
			return true, SaveNative(path, mems)
		}
	}
	return false, nil
}
