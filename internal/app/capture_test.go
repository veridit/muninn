package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
)

type captureLLM struct {
	server      *httptest.Server
	calls       atomic.Int32
	reply       string
	input       string
	beforeReply atomic.Value
}

func fakeCapture(t *testing.T, f *scopetest.Fixture, reply string) *captureLLM {
	t.Helper()
	m := &captureLLM{reply: reply}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		if gate := m.beforeReply.Load(); gate != nil {
			gate.(func())()
		}
		var v struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&v)
		if len(v.Messages) > 1 {
			m.input = v.Messages[1].Content
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": m.reply}, "finish_reason": "stop"}}, "usage": extract.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}})
	}))
	t.Cleanup(m.server.Close)
	f.App.Cfg.Extract = extract.Defaults()
	f.App.Cfg.Extract.Provider = "ollama"
	f.App.Cfg.Extract.BaseURL = m.server.URL
	return m
}
func line(category, trust, content, supersedes string) string {
	b, _ := json.Marshal(extract.Candidate{Category: category, Trust: trust, Content: content, Supersedes: supersedes})
	return string(b)
}
func captureOpts(session string) app.CaptureOptions {
	return app.CaptureOptions{Harness: "stdin", Session: session, Messages: []harness.CaptureMessage{{ID: "one", Role: "user", Text: "I prefer quartz deployment policy for future work."}}}
}

func TestCaptureExactReinforceAndHighWater(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	u := f.Units["Alpha"]
	m := fakeCapture(t, f, line("fact", "high", u.Body, ""))
	o := captureOpts("reinforce")
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 1 || r.Actions[0].Action != "reinforce" {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(m.input, u.Body) {
		t.Fatal("relevant existing memory missing from LLM context")
	}
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range mems {
		if v.SID == u.SID && v.Strength != 1 {
			t.Fatalf("strength %d", v.Strength)
		}
	} // fixture starts at zero
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 0 || r.Processed != 0 || m.calls.Load() != 1 {
		t.Fatalf("second capture %+v err=%v calls=%d", r, err, m.calls.Load())
	}
	var hwm int
	if err := f.App.DB.QueryRow(`SELECT high_water FROM capture_state`).Scan(&hwm); err != nil || hwm != 1 {
		t.Fatalf("high water %d %v", hwm, err)
	}
	t.Log("CAPTURE dedupe/HWM: exact match reinforced; second run adds=0 processed=0 requests=1")
}

func TestCaptureSupersedeTrustVisibility(t *testing.T) {
	for _, tc := range []struct{ name, trust, target, want string }{{"visible-high", "high", "Alpha", "supersede"}, {"visible-medium", "medium", "Alpha", "add"}, {"invisible-high", "high", "Beta", "add"}, {"client-high", "high", "Client", "add"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, "alpha")
			old := f.Units[tc.target]
			fakeCapture(t, f, line("correction", tc.trust, "Use the revised quartz protocol.", old.SID))
			r, err := b.Capture(context.Background(), f.App, captureOpts(tc.name))
			if err != nil || len(r.Actions) != 1 || r.Actions[0].Action != tc.want {
				t.Fatalf("%+v %v", r, err)
			}
			got, err := f.App.DB.GetUnit(old.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Active == (tc.want == "supersede") {
				t.Fatalf("old active=%v action=%s", got.Active, tc.want)
			}
			mems, _ := ingest.LoadNative(app.NativePath())
			found := false
			for _, v := range mems {
				if v.Content == "Use the revised quartz protocol." {
					found = true
					if v.Scope != "jcode@alpha" || v.Trust != tc.trust || !strings.HasPrefix(v.Origin, "capture:stdin:"+tc.name+":") {
						t.Fatal("metadata lost")
					}
				}
			}
			if !found {
				t.Fatal("new memory missing")
			}
		})
	}
	t.Log("CAPTURE supersede: only visible + high-trust target retired; medium/invisible/client targets unchanged")
}

func TestCaptureClientRootBoundAndSecretDropped(t *testing.T) {
	f := scopetest.New(t)
	b := f.App.Bound(filepath.Join(f.Root, "alpha", ".jcode"), f.ClientCwd)
	secret := "sk-" + strings.Repeat("z", 30)
	m := fakeCapture(t, f, line("fact", "high", "Client quartz policy remains isolated.", "")+"\n"+line("fact", "high", secret, ""))
	o := captureOpts("client")
	o.Messages = append(o.Messages, harness.CaptureMessage{ID: "secret", Role: "user", Text: secret})
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 1 || r.Rejected != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(m.input, "Matrix(Client)") {
		t.Fatal("visible client memory missing from model context")
	}
	if strings.Contains(m.input, secret) || strings.Contains(m.input, "Matrix(Alpha)") || strings.Contains(m.input, "Matrix(OtherClient)") {
		t.Fatal("remote egress crossed client/secret boundary")
	}
	mems, _ := ingest.LoadNative(app.NativePath())
	found := false
	for _, v := range mems {
		if strings.Contains(v.Content, secret) {
			t.Fatal("secret stored")
		}
		if v.Content == "Client quartz policy remains isolated." {
			found = true
			if v.Scope != "client:acme" {
				t.Fatal(v.Scope)
			}
		}
	}
	if !found {
		t.Fatal("client memory not stored")
	}
	for _, name := range []string{"alpha", "beta", "global"} {
		global := f.Bind(t, name)
		rows, err := f.App.DB.ResolveAll(r.Actions[0].Origin)
		if err == nil {
			for _, u := range rows {
				if _, ok := global.Present(u); ok {
					t.Fatal("client memory leaked")
				}
			}
		}
	}
	t.Log("CAPTURE client/secret: stored client:acme only; foreign known memories and credential block withheld; secret candidate dropped")
}

func TestCaptureDryRunPeriodicAndTimeout(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	m := fakeCapture(t, f, line("fact", "medium", "Quartz uses the stable deployment protocol.", ""))
	before, _ := os.ReadFile(app.NativePath())
	o := captureOpts("dry")
	o.DryRun = true
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	after, _ := os.ReadFile(app.NativePath())
	if string(before) != string(after) {
		t.Fatal("dry run wrote memory")
	}
	var count int
	f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&count)
	if count != 0 {
		t.Fatal("dry run advanced HWM")
	}
	o.DryRun = false
	o.Periodic = true
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || r.Skipped != "waiting for extraction interval" || m.calls.Load() != 1 {
		t.Fatal("periodic interval ignored")
	}
	// Final session capture bypasses cadence.
	o.Periodic = false
	r, err = b.Capture(context.Background(), f.App, o)
	if err != nil || len(r.Actions) != 1 {
		t.Fatalf("session-end capture %+v %v", r, err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer slow.Close()
	f.App.Cfg.Extract.BaseURL = slow.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = b.Capture(ctx, f.App, captureOpts("timeout"))
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout did not return: %v", err)
	}
	f.App.DB.QueryRow(`SELECT count(*) FROM capture_seen`).Scan(&count)
	if count != 1 {
		t.Fatal("timeout advanced high water")
	}
	t.Log("CAPTURE dry-run/cadence/timeout: no dry-run writes; 12-turn gate; session-end flush; failed extraction keeps HWM unchanged")
}

func TestCaptureUpstreamReadOnlyReinforcement(t *testing.T) {
	f := scopetest.New(t)
	home := filepath.Join(f.Root, "alpha", ".jcode")
	p := filepath.Join(home, "memory", "global.json")
	os.MkdirAll(filepath.Dir(p), 0700)
	body := `{"memories":{"upstream":{"id":"upstream","content":"Quartz upstream policy.","category":"fact"}}}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := f.Bind(t, "alpha")
	fakeCapture(t, f, line("fact", "medium", "Quartz upstream policy.", ""))
	r, err := b.Capture(context.Background(), f.App, captureOpts("upstream"))
	if err != nil || len(r.Actions) != 1 || r.Actions[0].Action != "reinforce" {
		t.Fatalf("%+v %v", r, err)
	}
	after, _ := os.ReadFile(p)
	if string(after) != body {
		t.Fatal("modified upstream")
	}
	var n int
	f.App.DB.QueryRow(`SELECT count(*) FROM capture_reinforcements`).Scan(&n)
	if n != 1 {
		t.Fatal("reinforcement not recorded")
	}
	t.Log("CAPTURE upstream: jcode JSON byte-identical; reinforcement recorded only in muninn DB")
}

// Deterministic two-dimensional vectors test the strict cosine gate. Real-model
// calibration is separately reported, never inferred from this fake embedder.
type captureVectors struct{}

func (captureVectors) Model() string { return "capture-calibration" }
func (captureVectors) Embed(_ context.Context, texts []string) ([][]float32, error) {
	var out [][]float32
	for _, s := range texts {
		switch {
		case strings.Contains(s, "paraphrase"):
			out = append(out, []float32{0.99, 0.14106736})
		case strings.Contains(s, "distant"):
			out = append(out, []float32{0, 1})
		default:
			out = append(out, []float32{1, 0})
		}
	}
	return out, nil
}

func TestCaptureNearDuplicateCosine(t *testing.T) {
	f := scopetest.New(t)
	f.App.Embedder = captureVectors{}
	f.App.Indexer.Embedder = captureVectors{}
	f.App.Searcher.Embedder = captureVectors{}
	if _, err := f.App.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := f.Bind(t, "alpha")
	m := fakeCapture(t, f, line("fact", "medium", "Quartz paraphrase protocol.", ""))
	for i, tc := range []struct{ content, want string }{{"Quartz paraphrase protocol.", "reinforce"}, {"Quartz distant protocol.", "add"}} {
		m.reply = line("fact", "medium", tc.content, "")
		r, err := b.Capture(context.Background(), f.App, captureOpts(fmt.Sprintf("cosine-%d", i)))
		if err != nil || len(r.Actions) != 1 || r.Actions[0].Action != tc.want {
			t.Fatalf("%+v %v", r, err)
		}
	}
	t.Log("CAPTURE cosine: 0.99 reinforces at threshold 0.98; 0.00 adds")
}

// Capture has no one to ask: the model judges. A random-path URL it marks safe
// with a reason is stored as is, with the reason; an unjudged one is rejected.
func TestCaptureModelJudgesLookalikeSecrets(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "alpha")
	url := "https://example.org/tour-" + strings.Repeat("Xq7Tm2Lk9V", 3) + "/"
	judged := extract.Candidate{Category: "fact", Trust: "medium", Content: "Quartz tour is shared with the family at " + url, SafeReason: "shared on purpose with family, holds no account access"}
	jb, _ := json.Marshal(judged)
	fakeCapture(t, f, string(jb)+"\n"+line("fact", "medium", "Quartz admin panel at "+url, ""))
	o := captureOpts("judge")
	r, err := b.Capture(context.Background(), f.App, o)
	if err != nil || r.Rejected != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	mems, _ := ingest.LoadNative(app.NativePath())
	var stored, unjudged bool
	for _, m := range mems {
		if m.Content == judged.Content && m.SafeReason == judged.SafeReason {
			stored = true
		}
		if strings.HasPrefix(m.Content, "Quartz admin panel") {
			unjudged = true
		}
	}
	if !stored || unjudged {
		t.Fatalf("judged stored=%v unjudged stored=%v", stored, unjudged)
	}
}
