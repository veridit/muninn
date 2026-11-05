package recall

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	source, _, _, err := db.Source("native", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		body := fmt.Sprintf("common routine documentation sample number %d", i)
		scope := "global"
		if i == 0 {
			body = "A violet axolotl requires the quartz protocol for deployment."
		}
		if i == 1 {
			body = "Hiddenword private documentation"
			scope = "client:hidden"
		}
		u := store.Unit{SourceID: source, Key: fmt.Sprintf("fixture-%d", i), Body: body, ContentHash: index.ContentHash(body), Kind: "memory", Scope: scope, Active: true}
		id, err := db.UpsertUnit(u)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range index.Concepts(body) {
			db.Exec(`INSERT OR IGNORE INTO concepts(term) VALUES(?)`, w)
			db.Exec(`INSERT OR IGNORE INTO unit_concepts(unit_id,concept_id) SELECT ?,id FROM concepts WHERE term=?`, id, w)
		}
	}
	o := search.DefaultOptions()
	o.Limit = 5
	o.MinScore = 1
	o.SemanticSlots = 0
	o.Scopes = []string{"global"}
	return &Engine{DB: db, Searcher: &search.Searcher{DB: db}, Options: o, RelativeFloor: .62, MinSemantic: .64, MinSemanticFull: .58}
}

func TestPassagesBoundedAndShortExact(t *testing.T) {
	short := "banana mode\n\nplease"
	if p := Passages(short); len(p) != 1 || p[0] != short {
		t.Fatalf("short=%q", p)
	}
	text := "# Heading\n\n" + strings.Repeat("ø quartz ", 160) + "\n\n- item one\n- item two\n\nend"
	ps := Passages(text)
	if len(ps) < 3 {
		t.Fatalf("passages=%q", ps)
	}
	for _, p := range ps {
		if len(p) > PassageBytes || !utf8.ValidString(p) || p == "" {
			t.Fatalf("invalid passage %q", p)
		}
	}
	if !strings.Contains(strings.Join(ps, " "), "- item one\n- item two") {
		t.Fatal("list split")
	}
}

func TestRareTermsAndBinding(t *testing.T) {
	e := testEngine(t)
	ps, err := e.rarePassages(context.Background(), []string{"common routine violet Hiddenword TASK-123 CamelCase internal/recall/recall.go abc123"}, e.Options)
	if err != nil {
		t.Fatal(err)
	}
	terms := " " + strings.Join(ps[0].terms, " ") + " "
	for _, want := range []string{"violet", "task-123", "camelcase", "internal/recall/recall.go", "abc123"} {
		if !strings.Contains(terms, " "+want+" ") {
			t.Errorf("missing %s in %s", want, terms)
		}
	}
	for _, drop := range []string{"common", "routine", "hiddenword"} {
		if strings.Contains(terms, " "+drop+" ") {
			t.Errorf("kept %s in %s", drop, terms)
		}
	}
}

func TestBoilerplateCountsSessionsAndSeparatesBindings(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	r := Request{LearningScope: "client:1", Session: "one", Scopes: []string{"global"}}
	for i := 0; i < 12; i++ {
		if len(e.boilerplate(ctx, r, []string{"You have 7 incomplete todos"})) != 1 {
			t.Fatal("same session counted twice")
		}
	}
	for i := 1; i < BoilerplateSessions; i++ {
		r.Session = fmt.Sprint(i)
		e.boilerplate(ctx, r, []string{"You have 9 incomplete todos"})
	}
	r.Session = "another"
	if len(e.boilerplate(ctx, r, []string{"You have 23 incomplete todos"})) != 0 {
		t.Fatal("not learned")
	}
	r.LearningScope = "client:2"
	if len(e.boilerplate(ctx, r, []string{"You have 23 incomplete todos"})) != 1 {
		t.Fatal("cross binding learning leak")
	}
	if normalFingerprint("TASK-123 fix") == normalFingerprint("TASK-456 fix") {
		t.Fatal("identifiers collapsed")
	}
	var n int
	e.DB.QueryRow(`SELECT COUNT(*) FROM recall_passages WHERE length(binding)<>64 OR length(fingerprint)<>64 OR length(session)<>64`).Scan(&n)
	if n != 0 {
		t.Fatal("plaintext stored")
	}
}

func TestRedundancy(t *testing.T) {
	body := "The violet axolotl requires quartz staging before any production deployment."
	if !redundant(body, "Please follow this:\n"+body+"\nThen proceed.") {
		t.Fatal("quoted memory not suppressed")
	}
	if redundant(body, "The violet axolotl lives in the forest.") {
		t.Fatal("topic overlap suppressed")
	}
}

type batchEmbedder struct {
	calls int
	sizes []int
	wait  bool
}

func (*batchEmbedder) Model() string { return "test-batch" }
func (b *batchEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	b.calls++
	b.sizes = append(b.sizes, len(texts))
	if b.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func TestPassageBatchJustificationAndMax(t *testing.T) {
	e := testEngine(t)
	b := &batchEmbedder{}
	e.Searcher.Embedder = b
	msg := "Please investigate violet axolotl and quartz protocol.\n\n" + strings.Repeat("ordinary generic filler unrelated text ", 30)
	out, err := e.Recall(context.Background(), Request{Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 || b.sizes[0] < 2 || b.sizes[0] > MaxPassages {
		t.Fatalf("batch calls=%d sizes=%v", b.calls, b.sizes)
	}
	if len(out.Hits) != 1 || !strings.Contains(out.Hits[0].Why, "rare_term=") || !strings.Contains(out.Hits[0].Why, "passage=") {
		t.Fatalf("justification=%+v", out.Hits)
	}
	// Community expansion without either rare-term overlap or strong cosine
	// must never qualify, even if its keyword score is large.
	res := search.Result{Unit: store.Unit{Body: "ordinary generic text"}, Keyword: 999, Semantic: .4, SemanticOn: true}
	if rareMatch(res, []string{"violet"}) != "" {
		t.Fatal("invented rare overlap")
	}
}

func TestDeadlineRetainsKeywordResults(t *testing.T) {
	e := testEngine(t)
	b := &batchEmbedder{wait: true}
	e.Searcher.Embedder = b
	msg := "Find violet axolotl quartz.\n\n" + strings.Repeat("generic unrelated filler ", 40)
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := e.Recall(ctx, Request{Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 600*time.Millisecond {
		t.Fatalf("deadline exceeded: %s", time.Since(start))
	}
	if len(out.Hits) != 1 {
		t.Fatalf("lost partial results: %+v", out)
	}
}

func TestShortMessageOldSearchEquivalence(t *testing.T) {
	e := testEngine(t)
	for _, msg := range []string{"violet axolotl", "quartz protocol", "ordinary nothing", "thanks"} {
		old, err := e.Searcher.Search(context.Background(), msg, e.Options)
		if err != nil {
			t.Fatal(err)
		}
		old = e.semanticGate(old)
		want := 0
		for i, r := range old {
			if i > 0 && r.Total < e.RelativeFloor*old[0].Total {
				break
			}
			want++
		}
		got, err := e.Recall(context.Background(), Request{Message: msg})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Hits) != want {
			t.Fatalf("%q: new=%d old=%d", msg, len(got.Hits), want)
		}
		for i, h := range got.Hits {
			if h.UnitID != old[i].Unit.ID || h.Score != old[i].Total {
				t.Fatalf("changed short %q", msg)
			}
		}
	}
}

// Keep the embed contract checked at compile time.
var _ embed.Embedder = (*batchEmbedder)(nil)

func TestJustificationRule(t *testing.T) {
	for _, tc := range []struct {
		body     string
		semantic float64
		on, want bool
	}{
		{"unrelated text", .8, true, true},
		{"violet axolotl", .1, true, true},
		{"unrelated text", .63, true, false},
		{"unrelated text", .9, false, false},
		{"internal/search/elsewhere.go", .1, false, false},
		{"see internal/recall/recall.go", .1, false, true},
	} {
		r := search.Result{Unit: store.Unit{Body: tc.body}, Semantic: tc.semantic, SemanticOn: tc.on, Keyword: 999, Community: 999}
		_, got := justified(r, []string{"violet", "internal/recall/recall.go"}, .64)
		if got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

type stubbornEmbedder struct{ release chan struct{} }

func (*stubbornEmbedder) Model() string { return "stubborn" }
func (b *stubbornEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	<-b.release
	return nil, nil
}

func TestDeadlineNonCooperativeEmbedder(t *testing.T) {
	e := testEngine(t)
	b := &stubbornEmbedder{release: make(chan struct{})}
	defer close(b.release)
	e.Searcher.Embedder = b
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
		start := time.Now()
		r, err := e.Recall(ctx, Request{Message: "violet axolotl"})
		cancel()
		if err != nil || time.Since(start) > 700*time.Millisecond {
			t.Fatalf("unbounded call: %s %v", time.Since(start), err)
		}
		if len(r.Hits) == 0 {
			t.Fatal("lost completed keyword result")
		}
	}
}

type contrastingEmbedder struct{}

func (contrastingEmbedder) Model() string { return "contrasting" }
func (contrastingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if strings.Contains(text, "distinctive") {
			out[i] = []float32{1, 0}
		} else {
			out[i] = []float32{0, 1}
		}
	}
	return out, nil
}

func TestStrongOnePassageIsNotAveraged(t *testing.T) {
	e := testEngine(t)
	e.Searcher.Embedder = contrastingEmbedder{}
	e.Options.SemanticWeight = 10
	e.MinSemantic = .9
	u, err := e.DB.GetUnit(1)
	if err != nil {
		t.Fatal(err)
	}
	v := []float32{1, 0}
	if err = e.DB.PutEmbedding(u.ContentHash, index.PipelineHash(e.Searcher.Embedder.Model()), 2, embed.EncodeF32(v), nil, embed.Sketch(v)); err != nil {
		t.Fatal(err)
	}
	message := "distinctive query with no lexical overlap\n\n" + strings.Repeat("generic unfamiliar prose ", 40)
	r, err := e.Recall(context.Background(), Request{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 1 || r.Hits[0].Score != 10 || !strings.Contains(r.Hits[0].Why, "semantic=1.000") {
		t.Fatalf("max-over-passages failed: %+v", r)
	}
}
