package index

// Graph is the concept co-occurrence graph with Louvain communities, updated
// incrementally with DF-Louvain (arXiv:2404.19634, Algorithm 1) exactly as in
// seid search: apply the batch edge delta, seed a frontier from structural
// changes, and run a frontier-restricted move loop. Only the neighbourhood of
// what changed is reconsidered; nothing is recomputed from scratch.
type Graph struct {
	Adj       map[int]map[int]float64 // symmetric weights
	K         map[int]float64         // weighted degree
	Community map[int]int
	SumTot    map[int]float64
	SumIn     map[int]float64
	M         float64 // total edge weight
}

// NewGraph returns an empty graph.
func NewGraph() *Graph {
	return &Graph{Adj: map[int]map[int]float64{}, K: map[int]float64{}, Community: map[int]int{},
		SumTot: map[int]float64{}, SumIn: map[int]float64{}}
}

// UnitDelta is one unit's concept set before and after a change.
// A new unit is (nil, new); a removed unit is (old, nil).
type UnitDelta struct{ Old, New []int }

type edge struct{ a, b int }

func pair(a, b int) edge {
	if a > b {
		a, b = b, a
	}
	return edge{a, b}
}

// cliqueEdges returns every pair within a concept set (a unit is a clique).
func cliqueEdges(cs []int) map[edge]bool {
	out := map[edge]bool{}
	for i := 0; i < len(cs); i++ {
		for j := i + 1; j < len(cs); j++ {
			if cs[i] != cs[j] {
				out[pair(cs[i], cs[j])] = true
			}
		}
	}
	return out
}

// MaxConceptsPerUnit caps clique size so one huge unit cannot add O(n^2)
// edges (seid search skips Louvain past a size threshold for the same reason).
const MaxConceptsPerUnit = 48

func capped(cs []int) []int {
	if len(cs) > MaxConceptsPerUnit {
		return cs[:MaxConceptsPerUnit]
	}
	return cs
}

func (g *Graph) ensure(v int) (isNew bool) {
	if _, ok := g.Community[v]; ok {
		return false
	}
	g.Community[v] = v
	if g.Adj[v] == nil {
		g.Adj[v] = map[int]float64{}
	}
	g.SumTot[v] += g.K[v]
	return true
}

// Update applies unit deltas and re-optimises communities on the frontier.
// Returns (waves, moves).
func (g *Graph) Update(deltas []UnitDelta) (int, int) {
	// 7. Batch edge delta: each unit contributes +-1 to every pair in its clique.
	ed := map[edge]float64{}
	newVerts := map[int]bool{}
	for _, d := range deltas {
		oldE, newE := cliqueEdges(capped(d.Old)), cliqueEdges(capped(d.New))
		for e := range newE {
			if !oldE[e] {
				ed[e]++
			}
		}
		for e := range oldE {
			if !newE[e] {
				ed[e]--
			}
		}
		for _, v := range d.New {
			if g.ensure(v) {
				newVerts[v] = true
			}
		}
	}
	// 8. Apply delta; track structural insertions and deletions.
	var added, removed []edge
	for e, dw := range ed {
		if dw > -1e-12 && dw < 1e-12 {
			continue
		}
		g.ensure(e.a)
		g.ensure(e.b)
		oldW := g.Adj[e.a][e.b]
		newW := oldW + dw
		g.K[e.a] += dw
		g.K[e.b] += dw
		g.M += dw
		ca, cb := g.Community[e.a], g.Community[e.b]
		g.SumTot[ca] += dw
		g.SumTot[cb] += dw
		if ca == cb {
			g.SumIn[ca] += dw
		}
		if newW <= 1e-9 {
			delete(g.Adj[e.a], e.b)
			delete(g.Adj[e.b], e.a)
			if oldW > 1e-9 {
				removed = append(removed, e)
			}
		} else {
			g.Adj[e.a][e.b] = newW
			g.Adj[e.b][e.a] = newW
			if oldW <= 1e-9 {
				added = append(added, e)
			}
		}
	}
	// 9. Seed frontier: within-community deletion or cross-community insertion
	// marks the source; brand-new vertices are always visited.
	frontier := map[int]bool{}
	for _, e := range removed {
		if g.Community[e.a] == g.Community[e.b] {
			frontier[e.a] = true
		}
	}
	for _, e := range added {
		if g.Community[e.a] != g.Community[e.b] {
			frontier[e.a] = true
		}
	}
	for v := range newVerts {
		frontier[v] = true
	}
	return g.moveLoop(frontier)
}

// moveLoop is the frontier-restricted Louvain local-moving phase: at most 20
// waves, stop on a zero-move wave or when a wave's total gain drops below
// tolerance. Note: seid search compares the gain against 1e-2*m, but gains
// are modularity deltas (already normalised by 2m), so that floor grows with
// total weight and ends the loop after one wave on real graphs. muninn
// compares against tau directly, as in the paper.
func (g *Graph) moveLoop(frontier map[int]bool) (int, int) {
	const maxWaves, tolerance = 20, 1e-2
	if g.M < 1e-9 || len(frontier) == 0 {
		return 0, 0
	}
	m2 := 2 * g.M
	floor := tolerance
	waves, moves := 0, 0
	for len(frontier) > 0 && waves < maxWaves {
		waves++
		current := make([]int, 0, len(frontier))
		for v := range frontier {
			current = append(current, v)
		}
		frontier = map[int]bool{}
		waveGain, waveMoves := 0.0, 0
		for _, i := range current {
			ci := g.Community[i]
			ki := g.K[i]
			// Weight from i to each neighbouring community (ci included).
			ncomms := map[int]float64{ci: 0}
			for j, w := range g.Adj[i] {
				if j != i {
					ncomms[g.Community[j]] += w
				}
			}
			// Take i out of ci, then evaluate every candidate with the standard
			// Blondel gain dQ(c) = k_i,c/m - SumTot_c*k_i/(2m^2). seid search
			// used k_i,c/(2m) for the first term, which halves the pull of a
			// community and leaves new concepts stranded alone.
			g.SumIn[ci] -= ncomms[ci]
			g.SumTot[ci] -= ki
			gainOf := func(c int) float64 { return ncomms[c]/g.M - g.SumTot[c]*ki/(m2*g.M) }
			stay := gainOf(ci)
			bestC, bestGain := ci, 0.0
			for c := range ncomms {
				if c == ci {
					continue
				}
				if d := gainOf(c) - stay; d > bestGain+1e-15 || (d > 0 && d >= bestGain-1e-15 && c < bestC && bestC != ci) {
					bestGain, bestC = d, c
				}
			}
			g.SumIn[bestC] += ncomms[bestC]
			g.SumTot[bestC] += ki
			g.Community[i] = bestC
			if bestC != ci {
				moves++
				waveMoves++
				waveGain += bestGain
				for j := range g.Adj[i] { // onChange(i): expand to neighbours
					if j != i {
						frontier[j] = true
					}
				}
			}
		}
		if waveMoves == 0 || waveGain < floor {
			break
		}
	}
	return waves, moves
}

// CommunitySize returns the number of concepts in each community.
func (g *Graph) CommunitySize() map[int]int {
	out := map[int]int{}
	for _, c := range g.Community {
		out[c]++
	}
	return out
}
