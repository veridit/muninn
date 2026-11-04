// Package testutil provides synthetic fixtures. Never put real memories here.
package testutil

import (
	"context"
	"hash/fnv"
	"strings"
	"sync/atomic"

	"github.com/veridit/muninn/internal/embed"
)

// FakeEmbedder maps words to fixed pseudo-random directions and sums them,
// with a small synonym table so "pie" and "cake" land near each other. It
// counts every text it embeds, so tests can prove what was (not) re-embedded.
type FakeEmbedder struct {
	Dim   int
	Name  string       // model name; default "fake-embed"
	Calls atomic.Int64 // number of texts embedded
}

var synonyms = map[string]string{"pie": "cake", "pastry": "cake", "dessert": "cake", "bananas": "banana"}

func (f *FakeEmbedder) Model() string {
	if f.Name != "" {
		return f.Name
	}
	return "fake-embed"
}

func (f *FakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dim := f.Dim
	if dim == 0 {
		dim = 64
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		f.Calls.Add(1)
		v := make([]float32, dim)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			w = strings.Trim(w, ".,:;!?\"'()")
			if s, ok := synonyms[w]; ok {
				w = s
			}
			h := fnv.New64a()
			h.Write([]byte(w))
			seed := h.Sum64()
			for d := 0; d < dim; d++ {
				seed ^= seed << 13
				seed ^= seed >> 7
				seed ^= seed << 17
				v[d] += float32(int64(seed%2001)-1000) / 1000
			}
		}
		out[i] = embed.Normalize(v)
	}
	return out, nil
}
