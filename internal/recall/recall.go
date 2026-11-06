// Package recall turns a user message into a short, labelled memory block
// for injection into an agent session.
package recall

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// Marker starts every injected block. Adapters use it to recognise (and never
// react to) muninn's own output, which prevents injection loops.
const Marker = "-- muninn recall:"

// MaxQueryBytes bounds the focused query (UTF-8 safe).
const MaxQueryBytes = 8 << 10

// Request is one recall call.
type Request struct {
	Session  string // harness session id, for per-session dedupe
	Harness  string
	Message  string // the user's latest message
	Scopes   []string
	ScopeFor func(store.Unit) (string, bool) `json:"-"`
}

// Hit is one recalled memory.
type Hit struct {
	SID    string  `json:"sid"`
	UnitID int64   `json:"id"`
	Key    string  `json:"key"`    // id within its source: mem_/imp_ for muninn, the jcode memory id, ...
	Source string  `json:"source"` // native | jcode | markdown
	Text   string  `json:"text"`
	Kind   string  `json:"kind"`
	Scope  string  `json:"scope"`
	Score  float64 `json:"score"`
	Why    string  `json:"why"`
	// Updated is when the memory was last written. Recall shows it: a memory
	// is a dated note of what was believed then, not documentation.
	Updated time.Time `json:"updated"`
}

// Response is the recall result. Block is empty when nothing passed the bar.
type Response struct {
	Hits    []Hit  `json:"hits"`
	Block   string `json:"block"`
	Elapsed string `json:"elapsed"`
	Skipped string `json:"skipped,omitempty"`
}

// Engine runs recall.
type Engine struct {
	DB       *store.DB
	Searcher *search.Searcher
	Options  search.Options
	// SearchFunc optionally substitutes candidate retrieval for controlled
	// experiments. Bindings, guards, gates and chronological injection tracking
	// still run here. Nil preserves the production Searcher path.
	SearchFunc func(context.Context, string, search.Options) ([]search.Result, error)
	// RelativeFloor drops hits below this fraction of the best hit (0 = off).
	RelativeFloor float64
	// MinSemantic is the cosine a hit needs for automatic injection when an
	// embedder is available (0 = off).
	MinSemantic float64
	// MinSemanticFull is the lower cosine accepted when every query word is
	// present in the memory (short exact queries embed less precisely).
	MinSemanticFull float64
}

// IsOwnOutput reports whether text is (or quotes) a muninn injection.
func IsOwnOutput(text string) bool {
	return strings.Contains(text, Marker) || strings.Contains(text, "[muninn recall]")
}

// Truncate cuts s to at most n bytes on a rune boundary.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Recall searches, drops memories already injected this session, records the
// new injections, and renders the block.
func (e *Engine) Recall(ctx context.Context, r Request) (Response, error) {
	t0 := time.Now()
	msg := strings.TrimSpace(r.Message)
	if msg == "" {
		return Response{Skipped: "empty message"}, nil
	}
	if IsOwnOutput(msg) {
		return Response{Skipped: "own injection"}, nil
	}
	o := e.Options
	if len(r.Scopes) > 0 {
		o.Scopes = r.Scopes
	}
	o.ScopeFor = r.ScopeFor
	find := e.SearchFunc
	if find == nil {
		find = e.Searcher.Search
	}
	results, err := find(ctx, Truncate(msg, MaxQueryBytes), o)
	if err != nil {
		return Response{}, err
	}
	// Semantic gate first (only when the query embedding actually succeeded;
	// if Ollama fails mid-session recall degrades to keyword-only instead of
	// dropping everything), then the relative floor against the best hit.
	if e.MinSemantic > 0 && len(results) > 0 && results[0].SemanticOn {
		kept := results[:0]
		for _, r := range results {
			if r.Semantic >= e.MinSemantic || (e.MinSemanticFull > 0 && r.Coverage >= 1 && r.Semantic >= e.MinSemanticFull) {
				kept = append(kept, r)
			}
		}
		results = kept
	}
	var hits []Hit
	// Retain the first-block argument for compatibility with Render callers.
	first := r.Session == "" || !e.sessionSeen(r.Session)
	for i, res := range results {
		if i > 0 && e.RelativeFloor > 0 && res.Total < e.RelativeFloor*results[0].Total {
			break // results are sorted by total
		}
		if r.Session != "" && e.injected(r.Session, res.Unit.ContentHash) {
			continue
		}
		hits = append(hits, Hit{SID: res.Unit.SID, UnitID: res.Unit.ID, Key: res.Unit.Key, Source: e.sourceKind(res.Unit.SourceID), Text: res.Unit.Body, Kind: res.Unit.Kind,
			Scope: res.Unit.Scope, Score: res.Total, Why: res.Explain(), Updated: res.Unit.Updated})
		if r.Session != "" {
			e.record(r.Session, res.Unit.ID, res.Unit.ContentHash, r.Harness)
		}
	}
	resp := Response{Hits: hits, Elapsed: time.Since(t0).Round(time.Millisecond).String()}
	resp.Block = Render(hits, first)
	if resp.Block == "" {
		// Nothing relevant to recall, but the message may itself be worth
		// keeping: say so once, briefly.
		if cue := SaveCue(msg); cue != "" {
			resp.Block = Marker + " " + cue
		}
	} else if cue := SaveCue(msg); cue != "" {
		resp.Block += "\n" + cue
	}
	return resp, nil
}

func (e *Engine) sourceKind(id int64) string {
	var k string
	e.DB.QueryRow(`SELECT kind FROM sources WHERE id=?`, id).Scan(&k)
	return k
}

// sessionSeen reports whether this session has already been shown a block.
func (e *Engine) sessionSeen(session string) bool {
	var one int
	return e.DB.QueryRow(`SELECT 1 FROM injections WHERE session=? LIMIT 1`, session).Scan(&one) == nil
}

func (e *Engine) injected(session, hash string) bool {
	var one int
	return e.DB.QueryRow(`SELECT 1 FROM injections WHERE session=? AND content_hash=?`, session, hash).Scan(&one) == nil
}

func (e *Engine) record(session string, unitID int64, hash, harness string) {
	e.DB.Exec(`INSERT OR IGNORE INTO injections(session, unit_id, content_hash, harness, at_ns) VALUES(?,?,?,?,?)`,
		session, unitID, hash, harness, time.Now().UnixNano())
}

// Render formats hits as a labelled block, or "" when there are none.
//
// Every block carries identical provenance and management guidance. The full
// argument is retained for callers that previously distinguished first blocks.
func Render(hits []Hit, full bool) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s notes from memory, not the user; newer wins; verify. To manage: muninn MCP (memory_* tools) or 'muninn help'\n", Marker)
	b.WriteString(":.Memory .date@0:Day .id@1:Unique .note@2:Markdown\n")
	for _, h := range hits {
		text := Truncate(strings.Join(strings.Fields(h.Text), " "), 1200)
		text = strings.ReplaceAll(text, "\\", "\\\\")
		text = strings.ReplaceAll(text, "`", "\\`")
		fmt.Fprintf(&b, ".0:%s .1:%s .2:`%s`\n", date(h.Updated), ref(h), text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func date(t time.Time) string {
	if t.IsZero() || t.Year() < 2000 {
		return ""
	}
	return t.Format("2006-01-02")
}

func ref(h Hit) string {
	if h.SID != "" {
		return h.SID
	}
	switch h.Source {
	case "native":
		return h.Key
	case "jcode":
		return "jcode:" + h.Key
	case "":
		return fmt.Sprintf("#%d", h.UnitID)
	}
	return h.Source + ":" + h.Key
}
