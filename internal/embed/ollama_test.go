package embed

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeOllama struct {
	batchCalls, legacyCalls atomic.Int64
	sizes                   []int
	keepAlive               string
	noBatch                 bool
	wrongCount              bool
}

func (f *fakeOllama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		if f.noBatch {
			http.NotFound(w, r) // "404 page not found", like an old Ollama
			return
		}
		var in struct {
			Model     string   `json:"model"`
			Input     []string `json:"input"`
			KeepAlive string   `json:"keep_alive"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Model == "missing" {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"model 'missing' not found"}`))
			return
		}
		f.batchCalls.Add(1)
		f.sizes = append(f.sizes, len(in.Input))
		f.keepAlive = in.KeepAlive
		n := len(in.Input)
		if f.wrongCount {
			n--
		}
		out := make([][]float32, n)
		for i := range out {
			out[i] = []float32{3, 4} // unnormalised on purpose
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	})
	mux.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		f.legacyCalls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0, 5}})
	})
	return mux
}

func texts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "t"
	}
	return out
}

func TestOllamaBatchingNormalisationKeepAlive(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	o := NewOllama(srv.URL, "m", 0)
	if !o.Available(context.Background()) {
		t.Fatal("Available = false")
	}
	vecs, err := o.Embed(context.Background(), texts(70))
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 70 || f.batchCalls.Load() != 3 || f.sizes[0] != 32 || f.sizes[2] != 6 {
		t.Fatalf("batching: %d vecs, %d calls, sizes %v", len(vecs), f.batchCalls.Load(), f.sizes)
	}
	if math.Abs(float64(vecs[0][0])-0.6) > 1e-6 || math.Abs(float64(vecs[0][1])-0.8) > 1e-6 {
		t.Fatalf("not normalised: %v", vecs[0])
	}
	if f.keepAlive != "30m" {
		t.Fatalf("keep_alive = %q", f.keepAlive)
	}
}

func TestOllamaLegacyFallbackAndErrors(t *testing.T) {
	f := &fakeOllama{noBatch: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	vecs, err := NewOllama(srv.URL, "m", 0).Embed(context.Background(), texts(3))
	if err != nil || len(vecs) != 3 || f.legacyCalls.Load() != 3 || vecs[0][1] != 1 {
		t.Fatalf("legacy fallback: %v %v calls=%d", vecs, err, f.legacyCalls.Load())
	}

	// A missing model is an error, not a silent legacy fallback.
	f2 := &fakeOllama{}
	srv2 := httptest.NewServer(f2.handler())
	defer srv2.Close()
	if _, err := NewOllama(srv2.URL, "missing", 0).Embed(context.Background(), texts(1)); err == nil || !strings.Contains(err.Error(), "not found") || f2.legacyCalls.Load() != 0 {
		t.Fatalf("missing model: err=%v legacy=%d", err, f2.legacyCalls.Load())
	}

	// A response with the wrong number of vectors is rejected.
	f3 := &fakeOllama{wrongCount: true}
	srv3 := httptest.NewServer(f3.handler())
	defer srv3.Close()
	if _, err := NewOllama(srv3.URL, "m", 0).Embed(context.Background(), texts(2)); err == nil {
		t.Fatal("wrong vector count accepted")
	}
}

func TestOllamaUnavailableIsFast(t *testing.T) {
	// Nothing listens here: Available must say no quickly so hooks degrade.
	o := NewOllama("http://127.0.0.1:1", "m", 0)
	t0 := time.Now()
	if o.Available(context.Background()) {
		t.Fatal("Available = true for a closed port")
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Available took %v", d)
	}
	// A hung server is bounded by Available's 1.5 s timeout.
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		io.WriteString(w, "{}")
	}))
	defer hung.Close()
	t0 = time.Now()
	if NewOllama(hung.URL, "m", 0).Available(context.Background()) {
		t.Fatal("hung server reported available")
	}
	if d := time.Since(t0); d > 2500*time.Millisecond {
		t.Fatalf("hung Available took %v", d)
	}
}
