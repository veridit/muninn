package embed

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func randUnit(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return Normalize(v)
}

func TestQuantizedCosineIsClose(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	maxErr := 0.0
	for i := 0; i < 200; i++ {
		a, b := randUnit(r, 768), randUnit(r, 768)
		exact := float64(Dot(a, b))
		q := float64(DotQ(Quantize(a), Quantize(b)))
		maxErr = math.Max(maxErr, math.Abs(exact-q))
	}
	if maxErr > 0.01 {
		t.Fatalf("int8 cosine max error %.4f > 0.01", maxErr)
	}
	t.Logf("int8 cosine max abs error over 200 pairs: %.5f", maxErr)
}

func TestRoundTripF32(t *testing.T) {
	v := []float32{0.1, -0.2, 0.3}
	got := DecodeF32(EncodeF32(v))
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("%v != %v", got, v)
		}
	}
}

// The sketch prefilter must keep the true top-5 neighbours inside its
// shortlist (recall@5 within the 1024-candidate window used by search).
func TestSketchPrefilterRecall(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	const n, dim, keep = 5000, 768, 1024
	corpus := make([][]float32, n)
	for i := range corpus {
		corpus[i] = randUnit(r, dim)
	}
	hits, total := 0, 0
	for trial := 0; trial < 20; trial++ {
		// A query near a random corpus point, so true neighbours are meaningful.
		base := corpus[r.Intn(n)]
		q := make([]float32, dim)
		for i := range q {
			q[i] = base[i] + 0.05*float32(r.NormFloat64())
		}
		Normalize(q)
		type s struct {
			i   int
			sim float32
			ham int
		}
		all := make([]s, n)
		qs := Sketch(q)
		for i, v := range corpus {
			all[i] = s{i, Dot(q, v), Hamming(qs, Sketch(v))}
		}
		bySim := append([]s(nil), all...)
		sort.Slice(bySim, func(a, b int) bool { return bySim[a].sim > bySim[b].sim })
		sort.Slice(all, func(a, b int) bool { return all[a].ham < all[b].ham })
		short := map[int]bool{}
		for _, x := range all[:keep] {
			short[x.i] = true
		}
		for _, x := range bySim[:5] {
			total++
			if short[x.i] {
				hits++
			}
		}
	}
	recall := float64(hits) / float64(total)
	t.Logf("sketch prefilter recall@5 (keep %d of %d): %.3f", keep, n, recall)
	if recall < 0.95 {
		t.Fatalf("recall@5 %.3f < 0.95", recall)
	}
}
