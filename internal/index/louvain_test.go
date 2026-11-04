package index

import (
	"math"
	"testing"
)

// Two dense cliques joined by one weak edge must split into two communities,
// and adding a unit to clique A afterwards must only touch the frontier.
func TestDFLouvainSplitsAndStaysIncremental(t *testing.T) {
	g := NewGraph()
	a := []int{1, 2, 3, 4}
	b := []int{10, 11, 12, 13}
	var d []UnitDelta
	for i := 0; i < 3; i++ {
		d = append(d, UnitDelta{New: a}, UnitDelta{New: b})
	}
	d = append(d, UnitDelta{New: []int{4, 10}}) // weak bridge
	g.Update(d)
	ca, cb := g.Community[1], g.Community[10]
	for _, v := range a {
		if g.Community[v] != ca {
			t.Fatalf("clique A split: %v", g.Community)
		}
	}
	for _, v := range b {
		if g.Community[v] != cb {
			t.Fatalf("clique B split: %v", g.Community)
		}
	}
	if ca == cb {
		t.Fatalf("cliques merged: %v", g.Community)
	}
	// Total weight equals the sum of degrees / 2 (state stays consistent).
	var k float64
	for _, x := range g.K {
		k += x
	}
	if math.Abs(k/2-g.M) > 1e-9 {
		t.Fatalf("degree/weight mismatch: sumK/2=%v M=%v", k/2, g.M)
	}
	// Incremental: a unit joining clique A with a new concept.
	waves, _ := g.Update([]UnitDelta{{New: []int{1, 2, 99}}})
	if g.Community[99] != g.Community[1] {
		t.Fatalf("new concept 99 not absorbed into A: %v", g.Community)
	}
	if waves == 0 {
		t.Fatalf("expected a frontier pass")
	}
	// Removing every unit empties the graph weight.
	var rm []UnitDelta
	for _, x := range d {
		rm = append(rm, UnitDelta{Old: x.New})
	}
	rm = append(rm, UnitDelta{Old: []int{1, 2, 99}})
	g.Update(rm)
	if math.Abs(g.M) > 1e-9 {
		t.Fatalf("M after removing all = %v", g.M)
	}
}

func TestConceptsMatchSeidRules(t *testing.T) {
	got := Concepts("The banana-mode trigger: use it when the user asks. Banana banana!")
	want := []string{"asks", "banana", "banana-mode", "trigger", "user"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestEmbeddingTextTruncatesOnRuneBoundary(t *testing.T) {
	body := ""
	for len(body) < MaxEmbedBytes+10 {
		body += "æøå"
	}
	s := EmbeddingText("t", body)
	if len(s) > MaxEmbedBytes {
		t.Fatalf("len %d", len(s))
	}
	for _, r := range s {
		if r == '\uFFFD' {
			t.Fatal("split a rune")
		}
	}
	if PipelineHash("a") == PipelineHash("b") {
		t.Fatal("pipeline hash ignores model")
	}
}
