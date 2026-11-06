package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/ingest"
)

// Line is one memory in .muninn-notes/memories.jsonl. The file is sorted by id
// and holds one object per line, so git diffs and merges stay line-based.
type Line struct {
	ID       string    `json:"id"`
	Title    string    `json:"title,omitempty"`
	Content  string    `json:"content"`
	Category string    `json:"category,omitempty"`
	Tags     []string  `json:"tags,omitempty"`
	Active   bool      `json:"active"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// secretRe mirrors scripts/check-secrets.sh: a memory that looks like it
// holds a credential is never written into a repository. The sk- prefix must
// start a token: "task-636-..." links in seid memories are not API keys.
var secretRe = regexp.MustCompile(`((?:^|[^A-Za-z0-9])sk-(proj-|ant-|or-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(API_KEY|SECRET|TOKEN|PASSWORD)[A-Z_]*=[^\s<$"'{]{8,})`)

// LooksSecret reports whether text matches a credential pattern or holds a
// capability URL. Used where text leaves the machine for a shared repository.
func LooksSecret(text string) bool { return SecretHint(text) != "" }

// SecretHint is the cheap check that prompts a judgment, it does not decide.
// It names what looked secret ("" when nothing did). The writer, a person or
// an agent, confirms the text is safe to store (browser keys that ship in
// public bundles, references like secrets.NAME, and placeholders are safe).
func SecretHint(text string) string {
	if secretRe.MatchString(text) {
		return "a credential-shaped token (API key, access token, password or private key)"
	}
	if capabilityURLs(text) != text {
		return "a URL whose random path may be its access key"
	}
	return ""
}

// HasCredential reports a credential-shaped token.
func HasCredential(text string) bool { return secretRe.MatchString(text) }

// MaskCredentials replaces credential-shaped tokens with a marker. Capture uses
// it only for text sent to the extraction provider, never for stored memories.
func MaskCredentials(text string) string {
	return secretRe.ReplaceAllStringFunc(text, func(m string) string {
		// The sk- alternative may include one leading boundary character.
		if i := strings.Index(m, "sk-"); i == 1 && !strings.HasPrefix(m, "sk-") {
			return m[:1] + "[possible secret]"
		}
		return "[possible secret]"
	})
}

// Capability URLs: an http(s) URL whose path holds a random-looking segment
// may be a bearer secret; anyone with the URL has access.

func capabilityURLs(text string) string {
	return urlRe.ReplaceAllStringFunc(text, func(u string) string {
		for _, seg := range strings.Split(u, "/")[3:] {
			for _, part := range strings.FieldsFunc(seg, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
				if isCapabilityToken(part) {
					return "[capability URL withheld]"
				}
			}
		}
		return u
	})
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `)\]]+`)

// isCapabilityToken: 24+ alphanumerics with both letters and digits and
// mixed character classes, the shape of a random bearer path, not a word.
func isCapabilityToken(s string) bool {
	if len(s) < 24 {
		return false
	}
	var letters, digits, upper, lower, nonhex int
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			letters++
			lower++
			if r > 'f' {
				nonhex++
			}
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
			nonhex++
		default:
			return false
		}
	}
	// Lowercase hex (commit and content hashes) is public, not a key.
	if nonhex == 0 {
		return false
	}
	return letters > 0 && digits > 0 && (upper > 0 && lower > 0 || digits >= 4)
}

// SyncStats reports one sync.
type SyncStats struct {
	Imported      ingest.ImportStats
	Written       int
	SkippedSecret int
	FileChanged   bool
}

func (s SyncStats) String() string {
	return fmt.Sprintf("in: %s | out: %d memories, %d withheld (look like secrets), file changed=%v",
		s.Imported, s.Written, s.SkippedSecret, s.FileChanged)
}

// Sync merges the repository file into the native store (idempotent, keyed
// by the line id), then writes every memory of the project scope back to
// the file, sorted. Personal and other scopes are never written. With dry,
// nothing is written anywhere.
func Sync(p Project, nativePath string, dry bool) (SyncStats, error) {
	return syncFile(p, nativePath, dry, true)
}

func syncFile(p Project, nativePath string, dry, mark bool) (SyncStats, error) {
	var st SyncStats
	lines, err := ReadFile(p.File())
	if err != nil && !os.IsNotExist(err) {
		return st, err
	}
	items := make([]ingest.Imported, 0, len(lines))
	for _, l := range lines {
		items = append(items, ingest.Imported{Origin: originFor(p, l.ID), Title: l.Title, Content: l.Content,
			Category: l.Category, Tags: l.Tags, Scope: p.Scope(), Active: l.Active, Created: l.Created, Updated: l.Updated})
	}
	if st.Imported, err = ingest.MergeImport(nativePath, items, dry); err != nil {
		return st, err
	}
	mems, err := ingest.LoadNative(nativePath)
	if err != nil {
		return st, err
	}
	var out []Line
	for _, m := range mems {
		if m.Scope != p.Scope() {
			continue
		}
		// A shared repository is wider than the store, and its secret gate would
		// refuse the line: text that looks secret stays local even when judged safe.
		if LooksSecret(m.Title) || LooksSecret(m.Content) {
			st.SkippedSecret++
			continue
		}
		id := m.ID
		if strings.HasPrefix(m.Origin, originFor(p, "")) {
			id = strings.TrimPrefix(m.Origin, originFor(p, "")) // keep the file's own id
		}
		out = append(out, Line{ID: id, Title: m.Title, Content: m.Content, Category: m.Category, Tags: m.Tags,
			Active: m.Active, Created: m.Created.UTC(), Updated: m.Updated.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	st.Written = len(out)
	var buf bytes.Buffer
	for _, l := range out {
		b, err := json.Marshal(l)
		if err != nil {
			return st, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	old, _ := os.ReadFile(p.File())
	st.FileChanged = !bytes.Equal(old, buf.Bytes())
	if dry {
		return st, nil
	}
	if st.FileChanged {
		if err := os.MkdirAll(filepath.Dir(p.File()), 0o755); err != nil {
			return st, err
		}
		tmp := p.File() + ".tmp"
		if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
			return st, err
		}
		if err := os.Rename(tmp, p.File()); err != nil {
			return st, err
		}
	}
	if !mark {
		return st, nil
	}
	return st, ingest.MarkSynced(nativePath, p.Scope(), func(id string) string { return originFor(p, id) })
}

// originFor keys a repository memory by project and line id, so the same
// line in every clone maps to the same native memory.
func originFor(p Project, id string) string {
	if p.Client != "" {
		return "repo:client:" + p.Client + ":" + p.ID + "/" + id
	}
	return "repo:" + p.ID + "/" + id
}

// ReadFile parses a memories.jsonl file. Blank lines are ignored; a broken
// line is an error that names its line number (merge conflicts show here).
func ReadFile(path string) ([]Line, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("project memory file must be a regular file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" {
			continue
		}
		var l Line
		if err := json.Unmarshal([]byte(t), &l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if l.ID == "" {
			return nil, fmt.Errorf("%s:%d: memory without id", path, n)
		}
		out = append(out, l)
	}
	return out, sc.Err()
}
