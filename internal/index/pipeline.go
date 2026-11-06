package index

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veridit/muninn/internal/embed"
)

// unitConcepts returns a unit's concept ids.
func (ix *Indexer) unitConcepts(unitID int64) ([]int, error) {
	rows, err := ix.DB.Query(`SELECT concept_id FROM unit_concepts WHERE unit_id=? ORDER BY concept_id`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var c int
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// setUnitConcepts replaces a unit's concept membership, creating concepts.
func (ix *Indexer) setUnitConcepts(unitID int64, terms []string) ([]int, error) {
	if _, err := ix.DB.Exec(`DELETE FROM unit_concepts WHERE unit_id=?`, unitID); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(terms))
	for _, t := range terms {
		if _, err := ix.DB.Exec(`INSERT OR IGNORE INTO concepts(term) VALUES(?)`, t); err != nil {
			return nil, err
		}
		var id int
		if err := ix.DB.QueryRow(`SELECT id FROM concepts WHERE term=?`, t).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := ix.DB.Exec(`INSERT OR IGNORE INTO unit_concepts(unit_id, concept_id) VALUES(?,?)`, unitID, id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids, nil
}

func (ix *Indexer) loadGraph() (*Graph, error) {
	var blob []byte
	err := ix.DB.QueryRow(`SELECT state FROM graph_state WHERE id=1`).Scan(&blob)
	if err == sql.ErrNoRows {
		return NewGraph(), nil
	}
	if err != nil {
		return nil, err
	}
	g := NewGraph()
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(g); err != nil {
		return NewGraph(), nil // corrupt or old state: start over
	}
	return g, nil
}

func (ix *Indexer) saveGraph(g *Graph, touched map[int]bool, units map[int64]bool) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	if _, err := ix.DB.Exec(`INSERT INTO graph_state(id, state) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET state=excluded.state`, buf.Bytes()); err != nil {
		return err
	}
	// Persist community assignments for concepts whose community may have
	// moved, and collect the units whose per-community counts go stale.
	var moved []string
	for c := range touched {
		var old int
		err := ix.DB.QueryRow(`SELECT community_id FROM communities WHERE concept_id=?`, c).Scan(&old)
		if err == nil && old == g.Community[c] {
			continue
		}
		if _, err := ix.DB.Exec(`INSERT INTO communities(concept_id, community_id) VALUES(?,?)
		  ON CONFLICT(concept_id) DO UPDATE SET community_id=excluded.community_id`, c, g.Community[c]); err != nil {
			return err
		}
		moved = append(moved, strconv.Itoa(c))
	}
	for start := 0; start < len(moved); start += 500 {
		rows, err := ix.DB.Query(`SELECT DISTINCT unit_id FROM unit_concepts WHERE concept_id IN (` +
			strings.Join(moved[start:min(start+500, len(moved))], ",") + `)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u int64
			rows.Scan(&u)
			units[u] = true
		}
		rows.Close()
	}
	return ix.refreshUnitCommunities(units)
}

// refreshUnitCommunities recomputes per-community concept counts of units.
func (ix *Indexer) refreshUnitCommunities(units map[int64]bool) error {
	ids := make([]string, 0, len(units))
	for u := range units {
		ids = append(ids, strconv.FormatInt(u, 10))
	}
	for start := 0; start < len(ids); start += 500 {
		list := strings.Join(ids[start:min(start+500, len(ids))], ",")
		if _, err := ix.DB.Exec(`DELETE FROM unit_communities WHERE unit_id IN (` + list + `)`); err != nil {
			return err
		}
		if _, err := ix.DB.Exec(`INSERT INTO unit_communities(community_id, unit_id, n)
		  SELECT c.community_id, uc.unit_id, COUNT(*) FROM unit_concepts uc
		  JOIN communities c ON c.concept_id=uc.concept_id
		  WHERE uc.unit_id IN (` + list + `) GROUP BY 1, 2`); err != nil {
			return err
		}
	}
	return nil
}

func dirtyUnits(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// JcodeSource is one jcode instance's memory directory and its scope prefix.
type JcodeSource struct {
	Dir    string // <JCODE_HOME>/memory
	Prefix string // "jcode@<instance>"
}

// Options controls one indexing pass.
type Options struct {
	Jcode      []JcodeSource
	NativePath string
	NotesRoots []string
}

// Run performs one incremental pass over every configured source. The whole
// pass runs in one transaction: per-statement fsyncs dominated cold indexing
// (profiled at 92% in syscalls), and a pass is all-or-nothing anyway.
// store.Open caps the pool at one connection, so BEGIN/COMMIT on the pool
// bracket every statement of the pass.
// ErrBusy is returned with NoWait when another process holds the write lock.
var ErrBusy = errors.New("index busy: another muninn process is writing")

func (ix *Indexer) Run(ctx context.Context, o Options) (Stats, error) {
	if ix.NoWait {
		// busy_timeout applies per statement; drop it for the lock attempt.
		ix.DB.Exec(`PRAGMA busy_timeout=0`)
		_, err := ix.DB.Exec(`BEGIN IMMEDIATE`)
		ix.DB.Exec(`PRAGMA busy_timeout=5000`)
		if err != nil {
			if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "locked") {
				return Stats{}, ErrBusy
			}
			return Stats{}, err
		}
	} else if _, err := ix.DB.Exec(`BEGIN IMMEDIATE`); err != nil {
		return Stats{}, err
	}
	st, err := ix.run(ctx, o)
	if err != nil {
		ix.DB.Exec(`ROLLBACK`)
		return st, err
	}
	if _, err := ix.DB.Exec(`COMMIT`); err != nil {
		return st, err
	}
	return st, nil
}

// SourceRecipe versions how sources are turned into units (keys, scopes,
// metadata). Bumping it makes the next pass re-read every source once even
// if its (mtime, size) is unchanged. Text that did not change keeps its
// content hash, so nothing is re-embedded.
//
//	2: per-instance jcode scopes ("jcode@<instance>[:<project>]")
const SourceRecipe = "2"

func (ix *Indexer) run(ctx context.Context, o Options) (Stats, error) {
	if ix.DB.GetMeta("source_recipe") != SourceRecipe {
		if _, err := ix.DB.Exec(`UPDATE sources SET mtime_ns=-1, size=-1`); err != nil {
			return Stats{}, err
		}
		if err := ix.DB.SetMeta("source_recipe", SourceRecipe); err != nil {
			return Stats{}, err
		}
	}
	t0 := time.Now()
	var st Stats
	lap := t0
	phase := func(name string) {
		now := time.Now()
		st.Phases = append(st.Phases, Phase{name, now.Sub(lap)})
		lap = now
	}
	ix.deltas = nil
	var dirty, removed []int64
	collect := func(d, r []int64, err error) error {
		dirty, removed = append(dirty, d...), append(removed, r...)
		return err
	}
	for _, j := range o.Jcode {
		if _, err := os.Stat(j.Dir); err == nil {
			if err := collect(ix.IndexJcode(ctx, j.Dir, j.Prefix, &st)); err != nil {
				return st, err
			}
		}
	}
	if o.NativePath != "" {
		if _, err := os.Stat(o.NativePath); err == nil {
			if err := collect(ix.IndexNative(ctx, o.NativePath, &st)); err != nil {
				return st, err
			}
		}
	}
	if len(o.NotesRoots) > 0 {
		if err := collect(ix.IndexMarkdown(ctx, MarkdownFiles(o.NotesRoots), &st)); err != nil {
			return st, err
		}
	}
	if err := ix.prunedSources(&st, o); err != nil {
		return st, err
	}
	phase("sources")
	// Communities: DF-Louvain on the frontier of what changed.
	if len(ix.deltas) > 0 {
		g, err := ix.loadGraph()
		if err != nil {
			return st, err
		}
		g.Update(ix.deltas)
		touched := map[int]bool{}
		for _, d := range ix.deltas {
			for _, c := range d.New {
				touched[c] = true
			}
			for _, c := range d.Old {
				touched[c] = true
			}
			for _, c := range d.New {
				for j := range g.Adj[c] {
					touched[j] = true
				}
			}
		}
		if err := ix.saveGraph(g, touched, dirtyUnits(dirty)); err != nil {
			return st, err
		}
		phase("communities")
	}
	ix.pipelineSwitched = false
	if err := ix.EmbedDirty(ctx, &st); err != nil {
		return st, err
	}
	phase("embed")
	// A capped previous pass may already have checkpointed the source while
	// its vector was still missing. Materializing that vector is a graph delta
	// even if no source text changed in this pass. Do not mix staging models.
	if !ix.pipelineSwitched && ix.Embedder != nil && ix.DB.GetMeta("pipeline_hash") == PipelineHash(ix.Embedder.Model()) {
		for _, hash := range ix.embeddedHashes {
			rows, err := ix.DB.Query(`SELECT id FROM units WHERE active=1 AND content_hash=?`, hash)
			if err != nil {
				return st, err
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return st, err
				}
				dirty = append(dirty, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return st, err
			}
		}
	}
	if ix.pipelineSwitched {
		// New model or prompt recipe: rebuild every neighbour list once.
		if _, err := ix.DB.Exec(`DELETE FROM neighbors`); err != nil {
			return st, err
		}
		rows, err := ix.DB.Query(`SELECT id FROM units WHERE active=1`)
		if err != nil {
			return st, err
		}
		dirty = dirty[:0]
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			dirty = append(dirty, id)
		}
		rows.Close()
		removed = nil
	}
	if err := ix.UpdateNeighbors(dirty, removed); err != nil {
		return st, err
	}
	phase("neighbors")
	st.Elapsed = time.Since(t0)
	return st, nil
}

// prunedSources removes units of sources that no longer exist on disk.
func (ix *Indexer) prunedSources(st *Stats, o Options) error {
	rows, err := ix.DB.Query(`SELECT id, path FROM sources`)
	if err != nil {
		return err
	}
	type src struct {
		id   int64
		path string
	}
	var gone []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.id, &s.path); err != nil {
			rows.Close()
			return err
		}
		if _, err := os.Stat(s.path); os.IsNotExist(err) {
			gone = append(gone, s)
		}
	}
	rows.Close()
	for _, s := range gone {
		if _, _, err := ix.SyncSource(s.id, nil, st); err != nil {
			return err
		}
		if _, err := ix.DB.Exec(`DELETE FROM sources WHERE id=?`, s.id); err != nil {
			return err
		}
	}
	return nil
}

// MarkdownFiles expands roots (files or directories) into .md files,
// skipping hidden and vendor directories.
func MarkdownFiles(roots []string) []string {
	var out []string
	for _, r := range roots {
		fi, err := os.Stat(r)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			out = append(out, r)
			continue
		}
		filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() && p != r && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "_build") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(name, ".md") {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// NeighborK is the neighbour-list length.
const NeighborK = 16

type vecRow struct {
	id int64
	q  []byte
}

func (ix *Indexer) activeVectors() ([]vecRow, error) {
	pipeline := ix.DB.GetMeta("pipeline_hash")
	rows, err := ix.DB.Query(`SELECT u.id, e.qvec FROM units u JOIN embeddings e
	  ON e.content_hash=u.content_hash AND e.pipeline_hash=? WHERE u.active=1`, pipeline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vecRow
	for rows.Next() {
		var r vecRow
		if err := rows.Scan(&r.id, &r.q); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type nbr struct {
	id  int64
	sim float32
}

// UpdateNeighbors maintains the kNN graph incrementally: each dirty unit gets
// a fresh list (one pass over the vectors); every other unit only gains the
// dirty unit if it beats its current k-th neighbour; units that lost a removed
// neighbour are refilled. Nothing else is recomputed.
func (ix *Indexer) UpdateNeighbors(dirty, removed []int64) error {
	// Source reconciliation and newly embedded vectors can nominate the same
	// unit more than once. A frontier is a set, never a multiset of offers.
	unique := func(ids []int64) []int64 {
		seen := map[int64]bool{}
		out := make([]int64, 0, len(ids))
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		return out
	}
	dirty, removed = unique(dirty), unique(removed)
	if len(dirty) == 0 && len(removed) == 0 {
		return nil
	}
	vecs, err := ix.activeVectors()
	if err != nil {
		return err
	}
	byID := make(map[int64][]byte, len(vecs))
	for _, v := range vecs {
		byID[v.id] = v.q
	}
	// Every dirty unit (changed text, forgotten, revived) and every removed
	// unit is first detached: units that referenced it lose that edge and are
	// refilled from scratch, because the old similarity is stale or the target
	// is no longer active. Dirty units that are still active are then offered
	// back to every other list below.
	refill := map[int64]bool{}
	detach := append(append([]int64(nil), removed...), dirty...)
	for _, r := range detach {
		rows, err := ix.DB.Query(`SELECT unit_id FROM neighbors WHERE nbr_id=?`, r)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u int64
			rows.Scan(&u)
			refill[u] = true
		}
		rows.Close()
		if _, err := ix.DB.Exec(`DELETE FROM neighbors WHERE unit_id=? OR nbr_id=?`, r, r); err != nil {
			return err
		}
	}
	recompute := func(id int64) error {
		q, ok := byID[id]
		if !ok {
			_, err := ix.DB.Exec(`DELETE FROM neighbors WHERE unit_id=?`, id)
			return err
		}
		return ix.writeList(id, topK(q, id, vecs))
	}
	dirtySet := map[int64]bool{}
	for _, d := range dirty {
		dirtySet[d] = true
	}
	// Fresh lists for dirty and refill units, computed in parallel (pure CPU:
	// one int8 dot product per vector).
	var need []int64
	for _, d := range dirty {
		need = append(need, d)
	}
	for u := range refill {
		if !dirtySet[u] {
			need = append(need, u)
		}
	}
	fresh := make([][]nbr, len(need))
	parallel(len(need), func(i int) {
		if q, ok := byID[need[i]]; ok {
			fresh[i] = topK(q, need[i], vecs)
		}
	})
	for i, id := range need {
		if _, ok := byID[id]; !ok {
			if err := recompute(id); err != nil {
				return err
			}
			continue
		}
		if err := ix.writeList(id, fresh[i]); err != nil {
			return err
		}
	}
	// Offer each active dirty unit to every other list. Lists recomputed above
	// already considered every vector. The lists live in memory for the pass:
	// one SQL lookup per (dirty unit x list) made a 3,691-unit import run
	// ~74M queries (42 min); now each touched list is read once and written
	// once, and the dot products are split across cores by target list (each
	// worker owns a disjoint set of lists, so no locking).
	type list struct {
		nb      []nbr
		changed bool
	}
	lists := make(map[int64]*list, len(vecs))
	for _, v := range vecs {
		lists[v.id] = &list{}
	}
	if len(dirty) > 0 {
		rows, err := ix.DB.Query(`SELECT unit_id, nbr_id, sim FROM neighbors`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var u int64
			var n nbr
			if err := rows.Scan(&u, &n.id, &n.sim); err != nil {
				rows.Close()
				return err
			}
			if l := lists[u]; l != nil {
				l.nb = append(l.nb, n)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	var offers []vecRow
	for _, d := range dirty {
		if q, ok := byID[d]; ok {
			offers = append(offers, vecRow{d, q})
		}
	}
	parallel(len(vecs), func(i int) {
		v := vecs[i]
		if dirtySet[v.id] || refill[v.id] {
			return
		}
		l := lists[v.id]
		for _, d := range offers {
			sim := embed.DotQ(d.q, v.q)
			// An offered unit may already be in this list (it was re-embedded,
			// or a partial pass was interrupted): update in place, never add a
			// second edge to the same neighbour.
			if j := slices.IndexFunc(l.nb, func(n nbr) bool { return n.id == d.id }); j >= 0 {
				if l.nb[j].sim != sim {
					l.nb[j].sim = sim
					l.changed = true
				}
				continue
			}
			if len(l.nb) >= NeighborK {
				worst := 0
				for j := range l.nb {
					if l.nb[j].sim < l.nb[worst].sim || (l.nb[j].sim == l.nb[worst].sim && l.nb[j].id > l.nb[worst].id) {
						worst = j
					}
				}
				if sim < l.nb[worst].sim || (sim == l.nb[worst].sim && d.id >= l.nb[worst].id) {
					continue
				}
				l.nb[worst] = nbr{d.id, sim}
			} else {
				l.nb = append(l.nb, nbr{d.id, sim})
			}
			l.changed = true
		}
	})
	for id, l := range lists {
		if l.changed {
			if err := ix.writeList(id, l.nb); err != nil {
				return err
			}
		}
	}
	return nil
}

// topK returns id's NeighborK most similar vectors.
func topK(q []byte, id int64, vecs []vecRow) []nbr {
	best := make([]nbr, 0, len(vecs))
	for _, v := range vecs {
		if v.id != id {
			best = append(best, nbr{v.id, embed.DotQ(q, v.q)})
		}
	}
	sort.Slice(best, func(i, j int) bool {
		if best[i].sim == best[j].sim {
			return best[i].id < best[j].id
		}
		return best[i].sim > best[j].sim
	})
	if len(best) > NeighborK {
		best = best[:NeighborK]
	}
	return best
}

// writeList replaces one unit's stored neighbour list. Duplicate neighbour
// ids (from any caller) collapse to the highest similarity.
func (ix *Indexer) writeList(id int64, nb []nbr) error {
	// Keep the storage boundary replay-safe too, including duplicate IDs in
	// a batched caller. Sort and cap after deduplication, not before it.
	byID := map[int64]float32{}
	for _, n := range nb {
		if n.id == id {
			continue
		}
		if old, ok := byID[n.id]; !ok || n.sim > old {
			byID[n.id] = n.sim
		}
	}
	nb = make([]nbr, 0, len(byID))
	for nid, sim := range byID {
		nb = append(nb, nbr{nid, sim})
	}
	sort.Slice(nb, func(i, j int) bool {
		if nb[i].sim == nb[j].sim {
			return nb[i].id < nb[j].id
		}
		return nb[i].sim > nb[j].sim
	})
	if len(nb) > NeighborK {
		nb = nb[:NeighborK]
	}
	if _, err := ix.DB.Exec(`SAVEPOINT neighbor_list`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			ix.DB.Exec(`ROLLBACK TO neighbor_list`)
			ix.DB.Exec(`RELEASE neighbor_list`)
		}
	}()
	if _, err := ix.DB.Exec(`DELETE FROM neighbors WHERE unit_id=?`, id); err != nil {
		return err
	}
	seen := make(map[int64]int, len(nb))
	uniq := nb[:0:0]
	for _, b := range nb {
		if j, ok := seen[b.id]; ok {
			if b.sim > uniq[j].sim {
				uniq[j].sim = b.sim
			}
			continue
		}
		seen[b.id] = len(uniq)
		uniq = append(uniq, b)
	}
	nb = uniq
	if len(nb) == 0 {
		_, err := ix.DB.Exec(`RELEASE neighbor_list`)
		committed = err == nil
		return err
	}
	var sb strings.Builder
	args := make([]any, 0, 3*len(nb))
	sb.WriteString(`INSERT INTO neighbors(unit_id, nbr_id, sim) VALUES `)
	for i, b := range nb {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?)")
		args = append(args, id, b.id, b.sim)
	}
	sb.WriteString(` ON CONFLICT(unit_id,nbr_id) DO UPDATE SET sim=excluded.sim`)
	if _, err := ix.DB.Exec(sb.String(), args...); err != nil {
		return err
	}
	_, err := ix.DB.Exec(`RELEASE neighbor_list`)
	committed = err == nil
	return err
}

// parallel runs f(0..n-1) on all cores.
func parallel(n int, f func(i int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			f(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				f(i)
			}
		}()
	}
	wg.Wait()
}
