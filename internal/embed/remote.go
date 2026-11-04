package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Remote asks a running `muninn serve` to embed with the model it keeps
// loaded. Hooks are short-lived processes; loading the built-in model costs
// ~0.4 s and ~560 MB each time, while serve answers in tens of milliseconds.
type Remote struct {
	Addr  string // host:port of muninn serve (loopback)
	Name  string // model name serve reported
	HTTP  *http.Client
}

// EmbedRequest/EmbedResponse are the /api/embed wire format.
type EmbedRequest struct {
	Texts []string `json:"texts"`
}

type EmbedResponse struct {
	Model   string      `json:"model"`
	Vectors [][]float32 `json:"vectors"`
}

// DialRemote returns a Remote when serve answers within 300 ms and reports
// the wanted model, else nil.
func DialRemote(ctx context.Context, addr, wantModel string) *Remote {
	r := &Remote{Addr: addr, HTTP: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/embed", nil)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var info EmbedResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Model != wantModel {
		return nil
	}
	r.Name = info.Model
	return r
}

func (r *Remote) Model() string { return r.Name }

func (r *Remote) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	b, _ := json.Marshal(EmbedRequest{Texts: texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+r.Addr+"/api/embed", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("muninn serve /api/embed: %s", resp.Status)
	}
	var out EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Model != r.Name || len(out.Vectors) != len(texts) {
		return nil, fmt.Errorf("muninn serve returned %d vectors from %q, want %d from %q", len(out.Vectors), out.Model, len(texts), r.Name)
	}
	return out.Vectors, nil
}
