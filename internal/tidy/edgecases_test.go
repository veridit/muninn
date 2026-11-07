package tidy

import (
	"fmt"
	"testing"

	"github.com/veridit/muninn/internal/store"
)

func TestAmbiguousAnchorsAloneDoNotCreateSuspects(t *testing.T) {
	g := voteFixture()
	g.Topics = nil
	g.Edges = nil
	g.Units[0].Body = "ACME-999 ZETA-999"
	for i := 70; i < 82; i++ {
		g.Units = append(g.Units, store.Unit{ID: int64(i), SID: fmt.Sprint(i), Scope: "client:zeta", Body: fmt.Sprintf("ZETA-%d", i), Active: true})
	}
	p, err := Scan(g, Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *Entry
	for i := range p.Entries {
		if p.Entries[i].SID == "suspect" {
			found = &p.Entries[i]
		}
	}
	if found != nil {
		t.Fatalf("anchors alone created suspect=%+v", found)
	}
}
func TestLiteralNamedScopesMayContainSpaces(t *testing.T) {
	g := voteFixture()
	p, _ := Scan(g, Defaults(), nil)
	p.Entries[0].Decision = "move"
	p.Entries[0].Target = "engram:new project"
	if entries, err := Selected(p, "global->engram:new project"); err != nil || len(entries) != 1 {
		t.Fatalf("named scope rejected: %v", err)
	}
	for _, target := range []string{"*", "!client:other", "x\ny", " x", "x->y"} {
		p.Entries[0].Target = target
		if _, err := Selected(p, ""); err == nil {
			t.Fatalf("invalid target %q accepted", target)
		}
	}
}
