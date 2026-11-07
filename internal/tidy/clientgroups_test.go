package tidy

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/store"
)

func selectionFixture(owner, other, scopeSize int, terms map[string]int) Graph {
	g := Graph{Topics: []TopicGroup{{ID: 7, Members: map[int64]int{}, Terms: terms}}}
	for i := 1; i <= scopeSize+other; i++ {
		scope := "client:quartz:child"
		if i > scopeSize {
			scope = "global"
		}
		g.Units = append(g.Units, store.Unit{ID: int64(i), SID: fmt.Sprint(i), Scope: scope, Active: true})
		if i <= owner || i > scopeSize {
			g.Topics[0].Members[int64(i)] = 2
		}
	}
	return g
}

func TestClientGroupSelectionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name               string
		owner, other, size int
		terms              map[string]int
		want               bool
	}{
		{"exact purity", 6, 3, 6, map[string]int{"quartz": 6}, true},
		{"below purity", 5, 3, 5, map[string]int{"quartz": 5}, false},
		{"minimum five", 5, 0, 5, map[string]int{"quartz": 5}, true},
		{"below minimum", 4, 0, 4, map[string]int{"quartz": 4}, false},
		{"one percent boundary", 5, 0, 500, map[string]int{"quartz": 5}, true},
		{"one percent rounds up", 5, 0, 501, map[string]int{"quartz": 5}, false},
		{"one percent met", 6, 0, 501, map[string]int{"quartz": 6}, true},
		{"dates and numbers", 5, 0, 5, map[string]int{"2025-01-02": 5, "123": 4}, false},
		{"mixed descriptive", 5, 0, 5, map[string]int{"2025-01-02": 5, "quartz": 4}, true},
		{"empty label", 5, 0, 5, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := selectionFixture(tc.owner, tc.other, tc.size, tc.terms)
			groups, scores := clientGroups(g, Defaults())
			if (len(groups) == 1) != tc.want {
				t.Fatalf("groups=%+v", groups)
			}
			if tc.want && (groups[0].Members != tc.owner || groups[0].Total != tc.owner+tc.other || scores[1]["client:quartz"] != 2) {
				t.Fatalf("groups=%+v scores=%v", groups, scores)
			}
		})
	}
}

func TestGroupScoreSumsStoredNAndThreeTimesOwn(t *testing.T) {
	g := voteFixture()
	// Both topic groups belong to acme, including the outsider's n.
	g.Topics = append(g.Topics, TopicGroup{ID: 2, Members: map[int64]int{1: 2, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1}, Terms: map[string]int{"garnet": 5}})
	_, scores := clientGroups(g, Defaults())
	if scores[1]["client:acme"] != 5 {
		t.Fatalf("scores=%v", scores[1])
	}
	// An own-scope group contributes 2, so target 5 fails the ratio.
	own := TopicGroup{ID: 3, Members: map[int64]int{1: 2}, Terms: map[string]int{"garden": 6}}
	for i := 14; i < 20; i++ {
		g.Units[i-1].Scope = "global"
		own.Members[int64(i)] = 1
	}
	g.Topics = append(g.Topics, own)
	p, err := Scan(g, Defaults(), nil)
	if err != nil || p.Entries[0].Strength != "vote-only" {
		t.Fatalf("entries=%+v err=%v", p.Entries, err)
	}
	g.Topics[0].Members[1] = 4
	p, err = Scan(g, Defaults(), nil)
	if err != nil || p.Entries[0].Strength != "strong" || p.Entries[0].GroupScores["global"] != 2 {
		t.Fatalf("entries=%+v err=%v", p.Entries, err)
	}
}

func TestClassificationClassesAndReport(t *testing.T) {
	for _, tc := range []struct {
		name          string
		groups, votes bool
		want          string
	}{
		{"both", true, true, "strong"}, {"groups", true, false, "group-only"}, {"votes", false, true, "vote-only"}, {"neither", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := voteFixture()
			if !tc.groups {
				g.Topics = nil
			}
			if !tc.votes {
				g.Edges = nil
			}
			p, err := Scan(g, Defaults(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(p.Entries) != 0 {
					t.Fatal(p.Entries)
				}
				return
			}
			if len(p.Entries) != 1 || p.Entries[0].Strength != tc.want || p.Classes[tc.want] != 1 {
				t.Fatalf("plan=%+v", p)
			}
			if len(p.Transitions) != 1 || p.Transitions[0].Class != tc.want {
				t.Fatal(p.Transitions)
			}
			for _, section := range []string{"Client-group signatures (heuristic evidence)", "Counts per class", "Source -> target counts per class"} {
				if !strings.Contains(p.Markdown(), section) {
					t.Fatal("missing " + section)
				}
			}
			if tc.groups && (!strings.Contains(p.Markdown(), "quartz") || !strings.Contains(p.Markdown(), "12 scope members / 13 total")) {
				t.Fatal(p.Markdown())
			}
		})
	}
}

func TestGroupOptionsValidation(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), -.1, 0, .5, 1.01} {
		o := Defaults()
		o.GroupPurity = value
		if o.Validate() == nil {
			t.Fatalf("accepted purity %v", value)
		}
	}
	for _, value := range []float64{.618, 2.0 / 3, .667, 1} {
		o := Defaults()
		o.GroupPurity = value
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, which := range []string{"min", "score"} {
		o := Defaults()
		if which == "min" {
			o.GroupMin = 0
		} else {
			o.GroupScore = 0
		}
		if o.Validate() == nil {
			t.Fatal("accepted zero " + which)
		}
	}
}

func TestGroupLabelFrequencyOrderAndLimit(t *testing.T) {
	g := TopicGroup{Terms: map[string]int{"zebra": 9, "quartz": 5, "garnet": 5, "a": 1, "b": 1, "c": 1}}
	if got := groupLabel(g); !reflect.DeepEqual(got, []string{"zebra", "garnet", "quartz", "a", "b"}) {
		t.Fatal(got)
	}
}

func TestReadTopicGroupsCanonicalMirrorsAndLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "muninn.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	upstream, _, _, _ := db.Source("jcode", "synthetic-upstream")
	native, _, _, _ := db.Source("native", "synthetic-native")
	mirror, err := db.UpsertUnit(store.Unit{SourceID: upstream, Key: "mirror", Origin: "shared", Scope: "global", Body: "synthetic quartz", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := db.UpsertUnit(store.Unit{SourceID: native, Key: "copy", Origin: "shared", Scope: "client:quartz", Body: "synthetic quartz", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO concepts VALUES(1,'quartz'),(2,'garnet')`,
		`INSERT INTO communities VALUES(1,7),(2,7)`,
		fmt.Sprintf(`INSERT INTO unit_communities VALUES(7,%d,2),(7,%d,2)`, mirror, canonical),
		fmt.Sprintf(`INSERT INTO unit_concepts VALUES(%d,1),(%d,2),(%d,1),(%d,2)`, mirror, mirror, canonical, canonical),
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	g, err := ReadGraph(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Topics) != 1 || !reflect.DeepEqual(g.Topics[0].Members, map[int64]int{canonical: 2}) || !reflect.DeepEqual(g.Topics[0].Terms, map[string]int{"quartz": 1, "garnet": 1}) {
		t.Fatalf("topics=%+v", g.Topics)
	}
	if got := groupLabel(g.Topics[0]); !reflect.DeepEqual(got, []string{"garnet", "quartz"}) {
		t.Fatal(got)
	}
}
