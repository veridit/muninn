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
const Marker = "[muninn recall]"

// MaxQueryBytes bounds the focused query (UTF-8 safe).
const MaxQueryBytes = 8 << 10

// Request is one recall call.
type Request struct {
	Session string // harness session id, for per-session dedupe
	Harness string
	Message string // the user's latest message
	Scopes  []string
}

// Hit is one recalled memory.
type Hit struct {
	UnitID int64   `json:"id"`
	Text   string  `json:"text"`
	Kind   string  `json:"kind"`
	Scope  string  `json:"scope"`
	Score  float64 `json:"score"`
	Why    string  `json:"why"`
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
	// RelativeFloor drops hits below this fraction of the best hit (0 = off).
	RelativeFloor float64
	// MinSemantic is the cosine a hit needs for automatic injection when an
	// embedder is available (0 = off).
	MinSemantic float64
}

// IsOwnOutput reports whether text is (or quotes) a muninn injection.
func IsOwnOutput(text string) bool {
	return strings.Contains(text, Marker)
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
	results, err := e.Searcher.Search(ctx, Truncate(msg, MaxQueryBytes), o)
	if err != nil {
		return Response{}, err
	}
	// Semantic gate first (when embeddings exist), then the relative floor
	// against the best surviving hit.
	if e.MinSemantic > 0 && e.Searcher.Embedder != nil {
		kept := results[:0]
		for _, r := range results {
			if r.Semantic >= e.MinSemantic {
				kept = append(kept, r)
			}
		}
		results = kept
	}
	var hits []Hit
	for i, res := range results {
		if i > 0 && e.RelativeFloor > 0 && res.Total < e.RelativeFloor*results[0].Total {
			break // results are sorted by total
		}
		if r.Session != "" && e.injected(r.Session, res.Unit.ContentHash) {
			continue
		}
		hits = append(hits, Hit{UnitID: res.Unit.ID, Text: res.Unit.Body, Kind: res.Unit.Kind,
			Scope: res.Unit.Scope, Score: res.Total, Why: res.Explain()})
		if r.Session != "" {
			e.record(r.Session, res.Unit.ID, res.Unit.ContentHash, r.Harness)
		}
	}
	resp := Response{Hits: hits, Elapsed: time.Since(t0).Round(time.Millisecond).String()}
	resp.Block = Render(hits)
	return resp, nil
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
func Render(hits []Hit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Local memories that may be relevant to this message (from muninn, not typed by the user). Use them only if they apply.\n", Marker)
	for _, h := range hits {
		text := strings.Join(strings.Fields(h.Text), " ")
		fmt.Fprintf(&b, "- %s\n", Truncate(text, 1200))
	}
	return strings.TrimRight(b.String(), "\n")
}
