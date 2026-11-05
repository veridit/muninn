// Package recall turns a user message into a short, labelled memory block
// for injection into an agent session.
package recall

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

// Marker starts every injected block. Adapters use it to recognise (and never
// react to) muninn's own output, which prevents injection loops.
const Marker = "-- muninn recall:"

// MaxQueryBytes bounds message preprocessing, not a single embedding input.
// Each selected embedding is separately capped by PassageBytes.
const MaxQueryBytes = 64 << 10

// Request is one recall call.
type Request struct {
	// LearningScope is an explicit binding identity. Empty disables learning.
	LearningScope string `json:"-"`
	Session       string // harness session id, for per-session dedupe
	Harness       string
	Message       string // the user's latest message
	Scopes        []string
	ScopeFor      func(store.Unit) (string, bool) `json:"-"`
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
	embedOnce  sync.Once
	embedSlots chan struct{}
	DB         *store.DB
	Searcher   *search.Searcher
	Options    search.Options
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
	ctx, cancel := context.WithTimeout(ctx, RecallBudget)
	defer cancel()
	msg := Truncate(strings.TrimSpace(r.Message), MaxQueryBytes)
	if msg == "" {
		return Response{Skipped: "empty message"}, nil
	}
	if IsOwnOutput(r.Message) {
		return Response{Skipped: "own injection"}, nil
	}
	o := e.Options
	if len(r.Scopes) > 0 {
		o.Scopes = r.Scopes
	}
	o.ScopeFor = r.ScopeFor
	// Isolate embedding work from the DB and caller. At most one lingering
	// provider call per engine is allowed after cancellation.
	e.embedOnce.Do(func() { e.embedSlots = make(chan struct{}, 1) })
	worker := *e.Searcher
	if worker.Embedder != nil {
		worker.Embedder = budgetEmbedder{worker.Embedder, e.embedSlots}
	}
	searchCtx, stopSearch := context.WithTimeout(ctx, RecallBudget-150*time.Millisecond)
	results, reasons, err := e.passageSearch(searchCtx, r, o, msg, &worker)
	stopSearch()
	if err != nil {
		return Response{}, err
	}
	var hits []Hit
	// Retain the first-block argument for compatibility with Render callers.
	first := r.Session == "" || !e.sessionSeen(ctx, r.Session)
	for i, res := range results {
		if i > 0 && e.RelativeFloor > 0 && res.Total < e.RelativeFloor*results[0].Total {
			break // results are sorted by total
		}
		limit := o.Limit
		if limit <= 0 {
			limit = 5
		}
		if len(msg) > PassageBytes {
			limit = min(limit, 5)
		}
		if len(hits) >= limit {
			break
		}
		if r.Session != "" && e.injected(ctx, r.Session, res.Unit.ContentHash) {
			continue
		}
		hits = append(hits, Hit{SID: res.Unit.SID, UnitID: res.Unit.ID, Key: res.Unit.Key, Source: e.sourceKind(ctx, res.Unit.SourceID), Text: res.Unit.Body, Kind: res.Unit.Kind,
			Scope: res.Unit.Scope, Score: res.Total, Why: strings.TrimSpace(res.Explain() + " " + reasons[res.Unit.ID]), Updated: res.Unit.Updated})
		if r.Session != "" {
			e.record(ctx, r.Session, res.Unit.ID, res.Unit.ContentHash, r.Harness)
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

type budgetEmbedder struct {
	embed.Embedder
	slots chan struct{}
}

func (b budgetEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type result struct {
		vectors [][]float32
		err     error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-b.slots }()
		vectors, err := b.Embedder.Embed(ctx, texts)
		done <- result{vectors, err}
	}()
	select {
	case r := <-done:
		return r.vectors, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) sourceKind(ctx context.Context, id int64) string {
	var k string
	e.DB.QueryRowContext(ctx, `SELECT kind FROM sources WHERE id=?`, id).Scan(&k)
	return k
}

// sessionSeen reports whether this session has already been shown a block.
func (e *Engine) sessionSeen(ctx context.Context, session string) bool {
	var one int
	return e.DB.QueryRowContext(ctx, `SELECT 1 FROM injections WHERE session=? LIMIT 1`, session).Scan(&one) == nil
}

func (e *Engine) injected(ctx context.Context, session, hash string) bool {
	var one int
	// Fail closed on timeout: never bypass dedupe because the budget expired.
	err := e.DB.QueryRowContext(ctx, `SELECT 1 FROM injections WHERE session=? AND content_hash=?`, session, hash).Scan(&one)
	return err == nil || ctx.Err() != nil
}

func (e *Engine) record(ctx context.Context, session string, unitID int64, hash, harness string) {
	e.DB.ExecContext(ctx, `INSERT OR IGNORE INTO injections(session, unit_id, content_hash, harness, at_ns) VALUES(?,?,?,?,?)`,
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
