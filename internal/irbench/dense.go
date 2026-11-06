// SPDX-License-Identifier: MPL-2.0
package irbench

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/store"
)

// RRF uses one-based ranks. Repeated IDs in a channel vote only once. It is a
// ranking operator, not a calibrated relevance probability or abstention gate.
func RRF(k float64, lists ...[]Hit) []Hit {
	if k < 0 {
		k = 0
	}
	scores := map[int64]float64{}
	for _, list := range lists {
		seen := map[int64]bool{}
		for i, h := range list {
			if !seen[h.ID] {
				scores[h.ID] += 1 / (k + float64(i+1))
				seen[h.ID] = true
			}
		}
	}
	out := make([]Hit, 0, len(scores))
	for id, s := range scores {
		out = append(out, Hit{id, s})
	}
	sort.Slice(out, func(i, j int) bool { return better(out[i], out[j]) })
	return out
}

// Chunks is a bounded UTF-8-safe paragraph/window splitter. One short query is
// unchanged. This is passage max pooling, not token-level trained ColBERT.
func Chunks(query string, bytes, maxChunks int) []string {
	if bytes <= 0 {
		bytes = 600
	}
	if maxChunks <= 0 {
		maxChunks = 8
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(query, "\n\n") {
		p = strings.TrimSpace(p)
		for len(p) > 0 && len(out) < maxChunks {
			n := min(bytes, len(p))
			for n > 0 && !utf8.ValidString(p[:n]) {
				n--
			}
			if n == 0 {
				_, n = utf8.DecodeRuneInString(p)
			}
			if n < len(p) {
				if split := strings.LastIndexAny(p[:n], " \n\t"); split > n/2 {
					n = split
				}
			}
			out = append(out, strings.TrimSpace(p[:n]))
			p = strings.TrimSpace(p[n:])
		}
		if len(out) == maxChunks {
			break
		}
	}
	return out
}

type Vector struct {
	ID     int64
	F32    []float32
	Sketch []byte
}

func LoadVectors(ctx context.Context, db *sql.DB) ([]Vector, error) {
	rows, err := db.QueryContext(ctx, `SELECT u.id,e.vec,s.sketch FROM units u JOIN embeddings e ON e.content_hash=u.content_hash JOIN sketches s ON s.content_hash=u.content_hash AND s.pipeline_hash=e.pipeline_hash WHERE u.active=1 AND e.pipeline_hash=(SELECT value FROM meta WHERE key='pipeline_hash') ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vector
	for rows.Next() {
		var v Vector
		var raw []byte
		if err := rows.Scan(&v.ID, &raw, &v.Sketch); err != nil {
			return nil, err
		}
		v.F32 = embed.DecodeF32(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

// MaxSim scores max over query passage vectors against each single document
// vector. Sketch pruning is per passage and unioned, not on an averaged query.
// keep=0 computes the exact oracle. Input vectors must already be unit length.
func MaxSim(ctx context.Context, queries [][]float32, vectors []Vector, k, keep int, allow func(int64) bool) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	scores := map[int64]float64{}
	for _, q := range queries {
		if len(q) == 0 {
			return nil, fmt.Errorf("empty query vector")
		}
		type hd struct{ i, d int }
		var candidates []hd
		qs := embed.Sketch(q)
		for i, v := range vectors {
			if i%256 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if allow != nil && !allow(v.ID) {
				continue
			}
			if len(v.F32) != len(q) {
				return nil, fmt.Errorf("vector dimension mismatch: %d != %d", len(v.F32), len(q))
			}
			d := 0
			if keep > 0 {
				d = embed.Hamming(qs, v.Sketch)
			}
			candidates = append(candidates, hd{i, d})
		}
		if keep > 0 && len(candidates) > keep {
			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].d != candidates[j].d {
					return candidates[i].d < candidates[j].d
				}
				return vectors[candidates[i].i].ID < vectors[candidates[j].i].ID
			})
			candidates = candidates[:keep]
		}
		for _, c := range candidates {
			v := vectors[c.i]
			sim := float64(embed.Dot(q, v.F32))
			old, ok := scores[v.ID]
			if !ok || sim > old {
				scores[v.ID] = sim
			}
		}
	}
	var top []Hit
	for id, s := range scores {
		if !math.IsNaN(s) {
			if k >= len(vectors) {
				top = append(top, Hit{id, s})
			} else {
				top = topAdd(top, Hit{id, s}, k)
			}
		}
	}
	if k >= len(vectors) {
		sort.Slice(top, func(i, j int) bool { return better(top[i], top[j]) })
	}
	return top, nil
}

// EmbedChunks batches passages once, preserving the active pipeline prefix.
func EmbedChunks(ctx context.Context, e embed.Embedder, query, prefix string, bytes, n int) ([][]float32, error) {
	texts := Chunks(query, bytes, n)
	for i := range texts {
		texts[i] = prefix + texts[i]
	}
	if len(texts) == 0 {
		return nil, nil
	}
	return e.Embed(ctx, texts)
}

// Units converts lexical output for consumers retaining production gates.
func (c *Corpus) Units(hits []Hit) []store.Unit {
	out := make([]store.Unit, 0, len(hits))
	for _, h := range hits {
		if d, ok := c.Docs[h.ID]; ok {
			out = append(out, d.Unit)
		}
	}
	return out
}
