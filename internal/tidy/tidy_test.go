package tidy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/store"
)

func voteFixture() Graph {
	g := Graph{Units: []store.Unit{{ID: 1, SID: "suspect", Origin: "native:suspect", Scope: "global", Body: "ACME-999 api.acme.invalid deployment", Active: true}}}
	for i := 2; i <= 13; i++ {
		g.Units = append(g.Units, store.Unit{ID: int64(i), SID: fmt.Sprint(i), Scope: "client:acme:project", Body: fmt.Sprintf("ACME-%d api.acme.invalid deployment", i), Active: true})
		g.Edges = append(g.Edges, Edge{1, int64(i), .8})
	}
	for i := 14; i < 64; i++ {
		g.Units = append(g.Units, store.Unit{ID: int64(i), SID: fmt.Sprint(i), Scope: "personal", Body: "garden flowers and tea", Active: true})
	}
	return g
}
func TestVotesAnchorsStrengthAndGrouping(t *testing.T) {
	g := voteFixture()
	p, err := Scan(g, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("entries=%+v", p.Entries)
	}
	e := p.Entries[0]
	if e.Strength != "strong" || e.Target != "client:acme" || e.Decision != "unsure" || e.Votes["client:acme"] != 12 || len(e.Neighbors) != 5 {
		t.Fatalf("entry=%+v", e)
	}
	if !strings.Contains(strings.Join(e.Anchors, ","), "acme-#") {
		t.Fatal("ticket prefix not derived")
	}
	groups := p.Groups()
	if len(groups) != 1 || groups[0].Count != 1 || !strings.Contains(p.Markdown(), "global -> client:acme (1)") {
		t.Fatalf("groups=%+v", groups)
	}
	// Below-threshold neighbors cannot manufacture a vote, anchors still flag weak.
	for i := range g.Edges {
		g.Edges[i].Sim = .69
	}
	p, _ = Scan(g, Defaults(), nil)
	if p.Entries[0].Strength != "weak" || p.Entries[0].Votes["client:acme"] != 0 {
		t.Fatal("similarity threshold ignored")
	}
}
func TestVoteOwnMaxAndGeneral(t *testing.T) {
	g := Graph{Units: []store.Unit{{ID: 1, SID: "one", Scope: "global", Body: "note"}}}
	for i := 2; i <= 16; i++ {
		s := "client:acme"
		if i > 12 {
			s = "global"
		}
		g.Units = append(g.Units, store.Unit{ID: int64(i), SID: fmt.Sprint(i), Scope: s, Body: fmt.Sprint(i)})
		g.Edges = append(g.Edges, Edge{1, int64(i), .9})
	}
	p, _ := Scan(g, Defaults(), nil)
	if len(p.Entries) != 0 {
		t.Fatal("own-max ignored")
	}
	for i := range g.Units {
		if i == 0 {
			continue
		}
		g.Units[i].Scope = []string{"client:a", "client:b", "client:c"}[(i-1)%3]
	}
	p, _ = Scan(g, Defaults(), nil)
	if len(p.Entries) != 0 || len(p.General) != 1 {
		t.Fatalf("general=%+v entries=%+v", p.General, p.Entries)
	}
}
func TestAnchorsDistinctiveRootsAndScopes(t *testing.T) {
	g := voteFixture()
	as := DeriveAnchors(g.Units, map[string][]string{"client:acme": {filepath.Join(t.TempDir(), "acme")}})
	matches := MatchAnchors("ACME-123456 api.acme.invalid", as)
	if len(matches["client:acme"]) < 2 {
		t.Fatalf("anchors=%v matches=%v", as, matches)
	}
	for in, want := range map[string]string{"jcode@global:hash": "jcode@global", "client:a:project": "client:a", "project:id": "project:id", "engram:demo": "engram:demo", "named:other": "named:other"} {
		if TopScope(in) != want {
			t.Fatalf("TopScope(%s)", in)
		}
	}
	for _, bad := range []Options{{-.1, 10, 3}, {.7, 0, 3}, {.7, 17, 3}, {.7, 10, -1}} {
		if bad.Validate() == nil {
			t.Fatal("accepted invalid thresholds")
		}
	}
}
func TestReadGraphCanonicalSuppressionAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "muninn.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, _, _, _ := db.Source("jcode", filepath.Join(t.TempDir(), "upstream"))
	native, _, _, _ := db.Source("native", filepath.Join(t.TempDir(), "memories.json"))
	a := store.Unit{SourceID: source, Key: "a", Origin: "shared-identity", Scope: "jcode@a", Body: "upstream", Active: true}
	id, err := db.UpsertUnit(a)
	if err != nil {
		t.Fatal(err)
	}
	a.SourceID = native
	a.Key = "copy"
	a.Active = false
	if _, err = db.UpsertUnit(a); err != nil {
		t.Fatal(err)
	}
	b := store.Unit{SourceID: source, Key: "b", Scope: "global", Body: "visible", Active: true}
	bid, err := db.UpsertUnit(b)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = db.GetUnit(bid)
	if _, err = db.Exec(`INSERT INTO neighbors VALUES(?,?,.9)`, bid, id); err != nil {
		t.Fatal(err)
	}
	g, err := ReadGraph(context.Background(), path)
	if err != nil || len(g.Units) != 1 || g.Units[0].SID != b.SID {
		t.Fatalf("graph=%+v err=%v", g, err)
	}
	if err = db.Suppress(b.SID); err != nil {
		t.Fatal(err)
	}
	g, err = ReadGraph(context.Background(), path)
	if err != nil || len(g.Units) != 0 {
		t.Fatalf("suppressed graph=%+v err=%v", g, err)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if _, err = ReadGraph(context.Background(), missing); err == nil {
		t.Fatal("missing DB accepted")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read created database")
	}
}
func TestJudgeBadAnswersBecomeUnsure(t *testing.T) {
	entries := []Entry{{Source: "global"}}
	for _, raw := range []string{"garbage", `[{"decision":"move","target":"invented","reason":"x"}]`, `[{"decision":"move","target":"client:a","reason":"x","extra":1}]`, `[{"decision":"stay","target":"global","reason":"two\nlines"}]`, `[]`, `[{"decision":"move","target":"client:a","reason":"x"}] trailing`} {
		if got := ParseVerdicts(raw, entries, []string{"client:a"}); len(got) != 1 || got[0].Decision != "unsure" {
			t.Fatalf("accepted %s: %+v", raw, got)
		}
	}
	good := `[{"decision":"move","target":"client:a","reason":"belongs to this client"}]`
	if ParseVerdicts(good, entries, []string{"client:a"})[0].Decision != "move" {
		t.Fatal("valid verdict rejected")
	}
}
func TestJudgeBatchesStrongFirstRedactsAndLimits(t *testing.T) {
	secret := "sk-" + strings.Repeat("z", 35)
	var requests int
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if strings.Contains(req.Messages[1].Content, secret) {
			t.Error("sent secret")
		}
		var input struct {
			Entries []Entry `json:"entries"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			t.Error(err)
		}
		vs := []Verdict{}
		for _, e := range input.Entries {
			seen = append(seen, e.SID)
			vs = append(vs, Verdict{"stay", "", "correctly placed"})
		}
		raw, _ := json.Marshal(vs)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(raw)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	c := extract.Defaults()
	c.Provider = "ollama"
	c.BaseURL = server.URL
	p := Plan{Entries: []Entry{{SID: "weak", Source: "global", Strength: "weak", Text: secret}, {SID: "strong", Source: "global", Strength: "strong", Text: secret}}}
	if err := Judge(context.Background(), &p, c, 1); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !reflect.DeepEqual(seen, []string{"strong"}) || p.Entries[0].Judged || p.Entries[1].Decision != "stay" {
		t.Fatalf("plan=%+v seen=%v", p, seen)
	}
	if err := Judge(context.Background(), &p, c, 0); err != nil || requests != 1 {
		t.Fatal("zero limit called provider")
	}
}
func TestSelectionsAndStaleness(t *testing.T) {
	g := voteFixture()
	p, _ := Scan(g, Defaults(), nil)
	p.Entries[0].Decision = "move"
	if es, err := CheckSnapshot(p, g, "global->client:acme"); err != nil || len(es) != 1 {
		t.Fatalf("selected=%v err=%v", es, err)
	}
	g.Units[0].Body = "changed"
	if _, err := CheckSnapshot(p, g, ""); err == nil {
		t.Fatal("stale plan accepted")
	}
	if _, err := Selected(p, "not a pair"); err == nil {
		t.Fatal("invalid filter accepted")
	}
	p.Entries = append(p.Entries, p.Entries[0])
	if _, err := Selected(p, ""); err == nil {
		t.Fatal("duplicate accepted")
	}
}
