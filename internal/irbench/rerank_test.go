// SPDX-License-Identifier: MPL-2.0
package irbench

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"
)

func TestRerankerHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "irbench-helper" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req RerankRequest
		if err := dec.Decode(&req); err == io.EOF {
			os.Exit(0)
		} else if err != nil {
			os.Exit(2)
		}
		if req.Query == "slow" {
			time.Sleep(time.Minute)
		}
		scores := make([]float64, len(req.Documents))
		for i := range scores {
			scores[i] = float64(i)
		}
		_ = enc.Encode(map[string]any{"scores": scores})
	}
}
func TestRerankerIPCAndCancel(t *testing.T) {
	r, err := StartReranker(context.Background(), os.Args[0], "-test.run=TestRerankerHelper", "--", "irbench-helper")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	scores, err := r.Score(context.Background(), "query", []string{"one", "two"})
	if err != nil || len(scores) != 2 || scores[1] != 1 {
		t.Fatal(scores, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = r.Score(ctx, "slow", []string{"one"}); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if _, err = r.Score(context.Background(), "query", nil); err == nil {
		t.Fatal("cancelled worker reused")
	}
}
