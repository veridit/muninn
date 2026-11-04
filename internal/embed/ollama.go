package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Embedder produces vectors for texts.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// Ollama talks to a local Ollama server.
type Ollama struct {
	URL   string
	Name  string
	Delay time.Duration // pause between batches (seid search: --ollama-delay)
	// KeepAlive keeps the small embedding model resident between prompts so
	// the first hook call of a pause does not pay a model load (Ollama's
	// default unload is 5 minutes). nomic-embed-text is ~300 MB.
	KeepAlive string
	HTTP      *http.Client
}

// NewOllama returns a client with sane timeouts.
func NewOllama(url, model string, delay time.Duration) *Ollama {
	return &Ollama{URL: url, Name: model, Delay: delay, KeepAlive: "30m", HTTP: &http.Client{Timeout: 120 * time.Second}}
}

func (o *Ollama) Model() string { return o.Name }

// Available reports whether the server answers quickly.
func (o *Ollama) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.URL+"/api/tags", nil)
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// BatchSize bounds texts per /api/embed call.
const BatchSize = 32

// Embed embeds texts in batches using /api/embed, returning unit vectors.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += BatchSize {
		end := min(start+BatchSize, len(texts))
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
		if o.Delay > 0 && end < len(texts) {
			time.Sleep(o.Delay)
		}
	}
	return out, nil
}

func (o *Ollama) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.Name, "input": texts, "keep_alive": o.KeepAlive})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: HTTP %d: %.200s", resp.StatusCode, raw)
	}
	var r struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("ollama: decode: %w", err)
	}
	if len(r.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: got %d vectors for %d texts", len(r.Embeddings), len(texts))
	}
	for i := range r.Embeddings {
		Normalize(r.Embeddings[i])
	}
	return r.Embeddings, nil
}
