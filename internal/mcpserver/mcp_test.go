package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

// Two client instances share one index. Each MCP session must see only its
// own instance plus global, exactly like the recall hook.
func testApp(t *testing.T) (*app.App, map[string]string) {
	t.Helper()
	t.Setenv("MUNINN_HOME", t.TempDir())
	db, err := store.Open(config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ids := map[string]string{}
	for scope, c := range map[string]string{
		"jcode@alpha": "Alpha client deploy password rotation happens on Mondays.",
		"jcode@beta":  "Beta client deploy password rotation happens on Fridays.",
		"global":      "Deploy password rotation is logged in the runbook.",
	} {
		m, _, err := ingest.Remember(app.NativePath(), c, "fact", scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[scope] = m.ID
	}
	fe := &testutil.FakeEmbedder{}
	cfg := config.Defaults()
	cfg.JcodeMemoryDir = ""
	a := &app.App{Cfg: cfg, DB: db, Embedder: fe, Indexer: &index.Indexer{DB: db, Embedder: fe}}
	a.Searcher = &search.Searcher{DB: db, Embedder: fe}
	a.Recall = &recall.Engine{DB: db, Searcher: a.Searcher, Options: a.RecallOptions()}
	if _, err := a.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, ids
}

func connect(t *testing.T, a *app.App, opt Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(a, "test", opt).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func scopesOf(t *testing.T, out string) []string {
	t.Helper()
	var h hitsOut
	if err := json.Unmarshal([]byte(out), &h); err != nil {
		t.Fatalf("bad search output %q: %v", out, err)
	}
	var s []string
	for _, r := range h.Results {
		s = append(s, r.Scope)
	}
	return s
}

func TestMCPScopedToInstance(t *testing.T) {
	a, ids := testApp(t)
	alpha := Options{Scopes: []string{"jcode@alpha", "jcode@alpha:*", "global"}, DefaultScope: "jcode@alpha"}
	cs := connect(t, a, alpha)

	out, isErr := call(t, cs, "memory_search", map[string]any{"query": "deploy password rotation", "limit": 10})
	if isErr {
		t.Fatal(out)
	}
	got := scopesOf(t, out)
	if len(got) == 0 {
		t.Fatal("alpha search found nothing")
	}
	for _, s := range got {
		if s == "jcode@beta" {
			t.Fatalf("alpha session saw beta memory: %v", got)
		}
	}

	// beta's id must not be forgettable from alpha, and must stay active.
	if out, isErr := call(t, cs, "memory_forget", map[string]any{"id": ids["jcode@beta"]}); !isErr {
		t.Fatalf("alpha forgot beta memory: %s", out)
	}
	mems, _ := ingest.LoadNative(app.NativePath())
	for _, m := range mems {
		if m.ID == ids["jcode@beta"] && !m.Active {
			t.Fatal("beta memory was deactivated")
		}
	}

	// remember defaults to the instance scope; another instance's scope is refused.
	if out, isErr := call(t, cs, "memory_remember", map[string]any{"content": "Alpha uses tabs."}); isErr {
		t.Fatal(out)
	}
	if out, isErr := call(t, cs, "memory_remember", map[string]any{"content": "x", "scope": "jcode@beta"}); !isErr {
		t.Fatalf("alpha wrote into beta: %s", out)
	}
	mems, _ = ingest.LoadNative(app.NativePath())
	for _, m := range mems {
		if m.Content == "Alpha uses tabs." && m.Scope != "jcode@alpha" {
			t.Fatalf("default scope = %q, want jcode@alpha", m.Scope)
		}
	}

	// related on a beta unit id reveals nothing.
	var betaUnit int64
	for _, r := range mustSearch(t, a, "Beta client Fridays") {
		if r.Unit.Scope == "jcode@beta" {
			betaUnit = r.Unit.ID
		}
	}
	if betaUnit == 0 {
		t.Fatal("setup: beta unit not indexed")
	}
	out, isErr = call(t, cs, "memory_related", map[string]any{"unit_id": betaUnit})
	if !isErr || out != "no visible memory" {
		t.Fatalf("related leaked: %q error=%v", out, isErr)
	}
	beta, err := a.DB.GetUnit(betaUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{beta.SID, beta.SID[:4], beta.Key} {
		out, isErr = call(t, cs, "memory_related", map[string]any{"id": ref})
		if !isErr || out != "no visible memory" {
			t.Fatalf("related leaked: %q error=%v", out, isErr)
		}
		if out, isErr := call(t, cs, "memory_forget", map[string]any{"id": ref}); !isErr {
			t.Fatalf("alpha forgot beta via %q: %s", ref, out)
		}
	}
}

func TestMCPForgetsByShortPrefix(t *testing.T) {
	a, ids := testApp(t)
	cs := connect(t, a, Options{Scopes: []string{"jcode@alpha", "global"}, DefaultScope: "jcode@alpha"})
	u, err := a.DB.Resolve(ids["jcode@alpha"])
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, cs, "memory_forget", map[string]any{"id": u.SID[:4]}); isErr {
		t.Fatal(out)
	}
	u, err = a.DB.Resolve(u.Key)
	if err != nil || u.Active {
		t.Fatalf("not forgotten: %+v %v", u, err)
	}
}

func TestMCPGlobalWithoutInstance(t *testing.T) {
	t.Setenv("JCODE_HOME", "")
	a, _ := testApp(t)
	cs := connect(t, a, Options{})
	out, _ := call(t, cs, "memory_search", map[string]any{"query": "deploy password rotation", "limit": 10})
	seen := map[string]bool{}
	for _, s := range scopesOf(t, out) {
		seen[s] = true
	}
	if seen["jcode@alpha"] || seen["jcode@beta"] || !seen["global"] {
		t.Fatalf("global session leaked client or lost global: %v", seen)
	}
}

func mustSearch(t *testing.T, a *app.App, q string) []search.Result {
	t.Helper()
	o := a.RecallOptions()
	o.MinScore, o.Limit = 0, 20
	res, err := a.Searcher.Search(context.Background(), q, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// 2026-11-06: secrets are judged on write, never hidden on display. The cheap
// check asks; the writer answers with safe_reason; search shows the text as is.
func TestMCPRememberAsksBeforeStoringLookalikeSecret(t *testing.T) {
	a, _ := testApp(t)
	cs := connect(t, a, Options{Scopes: []string{"jcode@alpha", "global"}, DefaultScope: "jcode@alpha"})
	content := "Quartz map uses browser key AIza" + strings.Repeat("Q", 35) + ", referrer-locked and public in the bundle"
	out, isErr := call(t, cs, "memory_remember", map[string]any{"content": content})
	if !isErr || !strings.Contains(out, "Are you sure there are no secrets here?") {
		t.Fatalf("lookalike stored without asking: %s", out)
	}
	mems, _ := ingest.LoadNative(app.NativePath())
	for _, m := range mems {
		if m.Content == content {
			t.Fatal("stored before the writer answered")
		}
	}
	out, isErr = call(t, cs, "memory_remember", map[string]any{"content": content, "safe_reason": "browser key, ships in public JS, referrer-locked"})
	if isErr {
		t.Fatal(out)
	}
	mems, _ = ingest.LoadNative(app.NativePath())
	var reason string
	for _, m := range mems {
		if m.Content == content {
			reason = m.SafeReason
		}
	}
	if reason != "browser key, ships in public JS, referrer-locked" {
		t.Fatalf("safe reason not recorded: %q", reason)
	}
	if _, err := a.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	found, _ := call(t, cs, "memory_search", map[string]any{"query": "quartz map browser key referrer", "limit": 5})
	if !strings.Contains(found, "AIza"+strings.Repeat("Q", 35)) {
		t.Fatalf("display hid a judged-safe memory: %s", found)
	}
	// Ordinary text never triggers the question.
	if out, isErr := call(t, cs, "memory_remember", map[string]any{"content": "See [[t" + "ask-636-deferred-recursion-anonymous-self]] for quartz detail"}); isErr {
		t.Fatalf("task link tripped the secret check: %s", out)
	}
}
