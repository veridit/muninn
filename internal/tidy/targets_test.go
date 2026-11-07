package tidy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/extract"
)

func TestProvenanceBoundaryDropsBeforeJudging(t *testing.T) {
	for _, tc := range []struct{ source, target string }{
		{"client:quartz", "client:garnet:child"},
		{"client:quartz:child", "client:garnet"},
		{"client:quartz", "global"},
		{"client:quartz", "jcode@quartz"},
		{"global", "client:quartz:child"},
		{"folder:/synthetic", "client:quartz"},
	} {
		t.Run(tc.source+"->"+tc.target, func(t *testing.T) {
			g := voteFixture()
			g.Topics = nil
			g.Units[0].Scope = "jcode@stored"
			g.EffectiveScopes = map[int64]string{1: tc.source}
			for i := 1; i <= 12; i++ {
				g.Units[i].Scope = tc.target
			}
			p, err := Scan(g, Defaults(), nil)
			if err != nil || len(p.Entries) != 0 || p.DroppedBoundary != 1 {
				t.Fatalf("entries=%v dropped=%d err=%v", p.Entries, p.DroppedBoundary, err)
			}
			raw := fmt.Sprintf(`[{"decision":"move","target":%q,"reason":"synthetic resemblance"}]`, tc.target)
			e := Entry{Source: "jcode@stored", EffectiveSource: tc.source}
			if v := ParseVerdicts(raw, []Entry{e}, []string{tc.target})[0]; v.Decision != "unsure" || v.Reason != "invalid judge answer" {
				t.Fatalf("boundary verdict accepted: %+v", v)
			}
		})
	}
}

func TestJudgeMayOnlyMoveWithinClientChildren(t *testing.T) {
	e := Entry{Source: "jcode@stored", EffectiveSource: "client:quartz"}
	for _, target := range []string{"client:quartz:one", "client:quartz", "client:garnet:one", "global"} {
		raw := fmt.Sprintf(`[{"decision":"move","target":%q,"reason":"synthetic ownership"}]`, target)
		v := ParseVerdicts(raw, []Entry{e}, []string{target})[0]
		if (v.Decision == "move") != (target == "client:quartz:one") {
			t.Fatalf("%s: %+v", target, v)
		}
	}
}

func TestEveryProposalTargetExistsAndIsConcrete(t *testing.T) {
	for _, prefix := range []string{"project:", "jcode@global:", "folder:"} {
		g := voteFixture()
		g.Topics = nil
		for i := 1; i <= 12; i++ {
			g.Units[i].Scope = prefix + "minority"
			if i > 4 {
				g.Units[i].Scope = prefix + "winner"
			}
		}
		o := Defaults()
		if prefix == "folder:" {
			o.Majority = 8
		}
		p, err := Scan(g, o, nil)
		if err != nil || len(p.Entries) != 1 {
			t.Fatalf("%s: entries=%+v err=%v", prefix, p.Entries, err)
		}
		if p.Entries[0].Target != prefix+"winner" {
			t.Fatalf("wrong concrete majority: %+v", p.Entries[0])
		}
		for _, e := range p.Entries {
			found := false
			for _, u := range g.Units {
				if u.Scope == e.Target {
					found = true
				}
			}
			if !found || !writableScope(e.Target) {
				t.Fatalf("fake target %q", e.Target)
			}
		}
	}
}

func TestRetiredAndBareScopesCannotBecomeTargets(t *testing.T) {
	for _, s := range []string{"project", "engram", "engram:old", "client:", "folder:", "jcode@"} {
		g := voteFixture()
		g.Topics = nil
		for i := 1; i <= 12; i++ {
			g.Units[i].Scope = s
		}
		p, err := Scan(g, Defaults(), nil)
		if err != nil || len(p.Entries) != 0 || p.DroppedInvalidTarget+p.DroppedBoundary != 1 {
			t.Fatalf("%s: entries=%v dropped=%d err=%v", s, p.Entries, p.DroppedInvalidTarget, err)
		}
		raw := fmt.Sprintf(`[{"decision":"move","target":%q,"reason":"synthetic"}]`, s)
		if ParseVerdicts(raw, []Entry{{Source: "global"}}, []string{s})[0].Decision == "move" {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestHarmlessNonMoveTargetsNormalizeToEmpty(t *testing.T) {
	e := Entry{Source: "jcode@old", EffectiveSource: "folder:/synthetic", Target: "project:synthetic"}
	for _, decision := range []string{"stay", "unsure", " STAY "} {
		for _, target := range []string{"", e.Source, e.EffectiveSource, e.Target} {
			raw := fmt.Sprintf(`[{"decision":%q,"target":%q,"reason":"synthetic reason"}]`, decision, target)
			v := ParseVerdicts(raw, []Entry{e}, []string{e.Target})[0]
			if v.Decision != strings.ToLower(strings.TrimSpace(decision)) || v.Target != "" || v.Reason != "synthetic reason" {
				t.Fatalf("rejected safe non-move: %+v", v)
			}
		}
	}
}

func TestInvalidJudgeDiagnosticsKeepExactRawObjects(t *testing.T) {
	object := `{"decision":"move","target":"client:garnet","reason":"synthetic"}`
	entries := []Entry{{Source: "client:quartz"}}
	vs, anomalies := parseVerdicts("["+object+"]", entries, []string{"client:garnet"})
	if vs[0].Reason != "invalid judge answer" || len(anomalies) != 1 || anomalies[0].Index != 0 || anomalies[0].Raw != object {
		t.Fatalf("verdicts=%v anomalies=%v", vs, anomalies)
	}
	vs, anomalies = parseVerdicts("[]", entries, nil)
	if vs[0].Reason != "invalid judge answer" || len(anomalies) != 1 || anomalies[0].Index != -1 || anomalies[0].Raw != "[]" {
		t.Fatalf("verdicts=%v anomalies=%v", vs, anomalies)
	}
}

func TestPrivateJudgeDebugAndPerEntryAllowlist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var input struct {
			Entries []struct {
				Allowed []string `json:"allowed_targets"`
			} `json:"entries"`
		}
		json.Unmarshal([]byte(req.Messages[1].Content), &input)
		if len(input.Entries) != 1 || len(input.Entries[0].Allowed) != 1 || input.Entries[0].Allowed[0] != "project:synthetic" {
			t.Errorf("unsafe allowlist: %+v", input)
		}
		if strings.Contains(req.Messages[1].Content, `"scopes":`) || !strings.Contains(req.Messages[0].Content, "never evidence that the current placement is wrong") {
			t.Error("move allowlist could be mistaken for current scope existence")
		}
		raw := `[{"decision":"stay","target":"project:synthetic","reason":"synthetic reason"}]`
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": raw}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	c := extract.Defaults()
	c.Provider = "ollama"
	c.BaseURL = server.URL
	p := Plan{Scopes: []string{"global", "project:synthetic", "client:quartz"}, Entries: []Entry{{SID: "synthetic", Source: "global", Target: "project:synthetic", Strength: "vote-only"}}}
	path := filepath.Join(t.TempDir(), "private", "debug.jsonl")
	if err := JudgeWithDebug(context.Background(), &p, c, 1, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "normalized decision or non-move target") {
		t.Fatalf("debug=%s err=%v", data, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("debug permissions %v", info.Mode())
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "normalized decision or non-move target") {
		t.Fatal("raw debug leaked to plan")
	}
	if p.Entries[0].Decision != "stay" {
		t.Fatal(p.Entries)
	}
	if err := JudgeWithDebug(context.Background(), &p, c, 1, path); err == nil {
		t.Fatal("debug overwrite accepted")
	}
}

func TestGroupFloorFractionAndMinimum(t *testing.T) {
	g := selectionFixture(5, 0, 1001, map[string]int{"quartz": 5})
	for _, tc := range []struct {
		floor float64
		want  int
	}{{.01, 0}, {.005, 0}, {0, 1}} {
		o := Defaults()
		o.GroupFloor = tc.floor
		groups, _ := clientGroups(g, o)
		if len(groups) != tc.want {
			t.Fatalf("floor=%v groups=%v", tc.floor, groups)
		}
	}
	g = selectionFixture(6, 0, 1001, map[string]int{"quartz": 6})
	o := Defaults()
	o.GroupFloor = .005
	if groups, _ := clientGroups(g, o); len(groups) != 1 {
		t.Fatal(groups)
	}
	g = selectionFixture(4, 0, 1001, map[string]int{"quartz": 4})
	o.GroupFloor = 0
	if groups, _ := clientGroups(g, o); len(groups) != 0 {
		t.Fatal("minimum five bypassed")
	}
	for _, floor := range []float64{-1, 1.1} {
		o.GroupFloor = floor
		if o.Validate() == nil {
			t.Fatal("invalid floor accepted")
		}
	}
}

func TestZeroVoteGroupTargetAmbiguityIsSuppressed(t *testing.T) {
	g := voteFixture()
	g.Edges = nil
	for i := 1; i <= 12; i++ {
		g.Units[i].Scope = "project:one"
		if i > 6 {
			g.Units[i].Scope = "project:two"
		}
	}
	p, err := Scan(g, Defaults(), nil)
	if err != nil || len(p.Entries) != 0 || p.DroppedAmbiguousTarget != 1 {
		t.Fatalf("entries=%v ambiguous=%d err=%v", p.Entries, p.DroppedAmbiguousTarget, err)
	}
	// A single concrete group owner is not ambiguous even without vote evidence.
	for i := 1; i <= 12; i++ {
		g.Units[i].Scope = "project:one"
	}
	p, err = Scan(g, Defaults(), nil)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Target != "project:one" {
		t.Fatalf("entries=%v err=%v", p.Entries, err)
	}
}

func TestRealGlobalRootRequiresWinningConcreteVotes(t *testing.T) {
	scopes := []string{"jcode@global:hash", "jcode@global"}
	for _, tc := range []struct {
		root, folder int
		want         bool
	}{{0, 0, false}, {0, 10, false}, {1, 10, false}, {10, 1, true}, {10, 10, true}} {
		e := Entry{Source: "global", ConcreteVotes: map[string]int{"jcode@global": tc.root, "jcode@global:hash": tc.folder}}
		v := ParseVerdicts(`[{"decision":"move","target":"jcode@global","reason":"synthetic"}]`, []Entry{e}, scopes)[0]
		if (v.Decision == "move") != tc.want {
			t.Fatalf("votes=%v verdict=%+v", e.ConcreteVotes, v)
		}
	}
	if Defaults().GroupFloor != .0024 {
		t.Fatal("approved floor default changed")
	}
}
