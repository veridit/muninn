// SPDX-License-Identifier: MPL-2.0
package irbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Reranker is a persistent local JSON-lines subprocess, for example the supplied
// MiniLM runner. No shell expansion, remote endpoint or model download happens
// here. Startup is excluded from warm per-request timings and reported separately.
// One owner serializes calls. Cancellation kills the worker, never reuses a
// potentially desynchronized response stream. Close releases the process.
type Reranker struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *json.Decoder
	dead bool
}
type RerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

func StartReranker(ctx context.Context, program string, args ...string) (*Reranker, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		return nil, err
	}
	return &Reranker{cmd: cmd, in: in, out: json.NewDecoder(out)}, nil
}
func (r *Reranker) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return nil
	}
	r.dead = true
	r.in.Close()
	_ = r.cmd.Process.Kill()
	return r.cmd.Wait()
}
func (r *Reranker) Score(ctx context.Context, q string, docs []string) ([]float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return nil, fmt.Errorf("reranker closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		Scores []float64 `json:"scores"`
		Error  string    `json:"error"`
	}
	type response struct {
		r   result
		err error
	}
	ch := make(chan response, 1)
	go func() {
		var v result
		err := json.NewEncoder(r.in).Encode(RerankRequest{q, docs})
		if err == nil {
			err = r.out.Decode(&v)
		}
		ch <- response{v, err}
	}()
	select {
	case <-ctx.Done():
		r.dead = true
		_ = r.cmd.Process.Kill()
		_ = r.cmd.Wait()
		_ = r.in.Close()
		return nil, ctx.Err()
	case v := <-ch:
		if v.err != nil {
			return nil, v.err
		}
		if v.r.Error != "" {
			return nil, fmt.Errorf("reranker: %s", v.r.Error)
		}
		if len(v.r.Scores) != len(docs) {
			return nil, fmt.Errorf("reranker returned %d scores for %d documents", len(v.r.Scores), len(docs))
		}
		return v.r.Scores, nil
	}
}
