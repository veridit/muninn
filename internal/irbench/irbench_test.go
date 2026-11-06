// SPDX-License-Identifier: MPL-2.0
package irbench

import (
	"context"
	"fmt"
	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/store"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaxScoreOracle(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for trial := 0; trial < 100; trial++ {
		var docs []store.Unit
		for id := 1; id <= 100; id++ {
			var words []string
			for j := 0; j < 1+r.Intn(80); j++ {
				words = append(words, fmt.Sprintf("word%d", r.Intn(30)))
			}
			docs = append(docs, store.Unit{ID: int64(id), Body: strings.Join(words, " ")})
		}
		c := NewCorpus(docs, 1.2, .75)
		if err := Validate(c); err != nil {
			t.Fatal(err)
		}
		var words []string
		for j := 0; j < 1+r.Intn(30); j++ {
			words = append(words, fmt.Sprintf("word%d", r.Intn(30)))
		}
		q := strings.Join(words, " ")
		allow := func(u store.Unit) bool { return u.ID%3 != 0 }
		o := LexicalOptions{K: 1 + r.Intn(20), Terms: r.Intn(15), StopRatio: .8}
		a, _, err := c.Search(context.Background(), q, o, allow)
		if err != nil {
			t.Fatal(err)
		}
		o.Exhaustive = true
		b, _, err := c.Search(context.Background(), q, o, allow)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("trial %d\nmaxscore=%v\noracle=%v", trial, a, b)
		}
	}
}
func TestAdaptivePrune(t *testing.T) {
	c := NewCorpus([]store.Unit{{ID: 1, Body: "common rare"}, {ID: 2, Body: "common"}, {ID: 3, Body: "common"}}, 1.2, .75)
	if got := c.Terms("common rare rare nonexistent", 24, .8); !reflect.DeepEqual(got, []string{"rare"}) {
		t.Fatal(got)
	}
}
func TestMaxScoreTies(t *testing.T) {
	var ds []store.Unit
	for i := 1; i <= 20; i++ {
		ds = append(ds, store.Unit{ID: int64(i), Body: "identical terms"})
	}
	c := NewCorpus(ds, 1.2, .75)
	got, _, _ := c.Search(context.Background(), "identical terms", LexicalOptions{K: 3}, nil)
	for i, h := range got {
		if h.ID != int64(i+1) {
			t.Fatal(got)
		}
	}
}
func TestCancel(t *testing.T) {
	c := NewCorpus(nil, 1.2, .75)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Search(ctx, "word", LexicalOptions{}, nil); err == nil {
		t.Fatal("cancellation lost")
	}
}
func TestChunks(t *testing.T) {
	q := strings.Repeat("norsk blåbær ", 1000)
	got := Chunks(q, 600, 8)
	if len(got) != 8 {
		t.Fatal(len(got))
	}
	for _, s := range got {
		if !utf8.ValidString(s) || len(s) > 600 {
			t.Fatal("bad chunk")
		}
	}
	if got := Chunks("short query", 600, 8); len(got) != 1 || got[0] != "short query" {
		t.Fatal(got)
	}
}
func TestRRF(t *testing.T) {
	got := RRF(60, []Hit{{1, 100}, {2, 10}, {1, 1}}, []Hit{{2, .9}, {3, .8}})
	if got[0].ID != 2 || math.Abs(got[0].Score-(1./61+1./62)) > 1e-12 {
		t.Fatal(got)
	}
	if got[1].ID != 1 || got[1].Score != 1./61 {
		t.Fatal(got)
	}
}
func TestMaxSim(t *testing.T) {
	vs := []Vector{{1, []float32{1, 0}, nil}, {2, []float32{0, 1}, nil}, {3, []float32{-1, 0}, nil}}
	for i := range vs {
		vs[i].Sketch = embed.Sketch(vs[i].F32)
	}
	q := [][]float32{{1, 0}, {0, 1}}
	a, err := MaxSim(context.Background(), q, vs, 3, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a[0].ID != 1 || a[1].ID != 2 || a[0].Score != 1 || a[1].Score != 1 {
		t.Fatal(a)
	}
	b, err := MaxSim(context.Background(), q, vs, 3, 10, nil)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal(b, err)
	}
	if _, err := MaxSim(context.Background(), [][]float32{{1}}, vs, 3, 0, nil); err == nil {
		t.Fatal("accepted mismatched dimensions")
	}
}
