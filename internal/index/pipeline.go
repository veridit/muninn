package index

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func (ix *Indexer) saveGraph(g *Graph, touched map[int]bool) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	if _, err := ix.DB.Exec(`INSERT INTO graph_state(id, state) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET state=excluded.state`, buf.Bytes()); err != nil {
		return err
	}
	// Persist community assignments for concepts whose community may have moved.
	for c := range touched {
		if _, err := ix.DB.Exec(`INSERT INTO communities(concept_id, community_id) VALUES(?,?)
		  ON CONFLICT(concept_id) DO UPDATE SET community_id=excluded.community_id`, c, g.Community[c]); err != nil {
			return err
		}
	}
	return nil
}

// Options controls one indexing pass.
type Options struct {
	JcodeDir   string
	NativePath string
	NotesRoots []string
}

// Run performs one incremental pass over every configured source. The whole
// pass runs in one transaction: per-statement fsyncs dominated cold indexing
// (profiled at 92% in syscalls), and a pass is all-or-nothing anyway.
// store.Open caps the pool at one connection, so BEGIN/COMMIT on the pool
// bracket every statement of the pass.
func (ix *Indexer) Run(ctx context.Context, o Options) (Stats, error) {
	if _, err := ix.DB.Exec(`BEGIN IMMEDIATE`); err != nil {
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

func (ix *Indexer) run(ctx context.Context, o Options) (Stats, error) {
	t0 := time.Now()
	var st Stats
	ix.deltas = nil
	var dirty, removed []int64
	collect := func(d, r []int64, err error) error {
		dirty, removed = append(dirty, d...), append(removed, r...)
		return err
	}
	if o.JcodeDir != "" {
		if _, err := os.Stat(o.JcodeDir); err == nil {
			if err := collect(ix.IndexJcode(ctx, o.JcodeDir, &st)); err != nil {
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
		if err := ix.saveGraph(g, touched); err != nil {
			return st, err
		}
	}
	if err := ix.EmbedDirty(ctx, &st); err != nil {
		return st, err
	}
	if err := ix.UpdateNeighbors(dirty, removed); err != nil {
		return st, err
	}
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
	// Units that referenced a removed neighbour need refilling.
	refill := map[int64]bool{}
	for _, r := range removed {
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
		best := make([]nbr, 0, len(vecs))
		for _, v := range vecs {
			if v.id != id {
				best = append(best, nbr{v.id, embed.DotQ(q, v.q)})
			}
		}
		sort.Slice(best, func(i, j int) bool { return best[i].sim > best[j].sim })
		if len(best) > NeighborK {
			best = best[:NeighborK]
		}
		if _, err := ix.DB.Exec(`DELETE FROM neighbors WHERE unit_id=?`, id); err != nil {
			return err
		}
		if len(best) == 0 {
			return nil
		}
		var sb strings.Builder
		args := make([]any, 0, 3*len(best))
		sb.WriteString(`INSERT INTO neighbors(unit_id, nbr_id, sim) VALUES `)
		for i, b := range best {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?)")
			args = append(args, id, b.id, b.sim)
		}
		_, err := ix.DB.Exec(sb.String(), args...)
		return err
	}
	dirtySet := map[int64]bool{}
	for _, d := range dirty {
		dirtySet[d] = true
		if err := recompute(d); err != nil {
			return err
		}
	}
	for u := range refill {
		if !dirtySet[u] {
			if err := recompute(u); err != nil {
				return err
			}
		}
	}
	// Offer each dirty unit to every other unit's list (cheap: compare with k-th).
	for _, d := range dirty {
		q, ok := byID[d]
		if !ok {
			continue
		}
		for _, v := range vecs {
			if v.id == d || dirtySet[v.id] {
				continue
			}
			sim := embed.DotQ(q, v.q)
			var cnt int
			var kth float32 = -2
			ix.DB.QueryRow(`SELECT COUNT(*), COALESCE(MIN(sim), -2) FROM neighbors WHERE unit_id=?`, v.id).Scan(&cnt, &kth)
			if cnt < NeighborK || sim > kth {
				if _, err := ix.DB.Exec(`INSERT OR REPLACE INTO neighbors(unit_id, nbr_id, sim) VALUES(?,?,?)`, v.id, d, sim); err != nil {
					return err
				}
				if cnt >= NeighborK {
					ix.DB.Exec(`DELETE FROM neighbors WHERE unit_id=? AND nbr_id=(SELECT nbr_id FROM neighbors WHERE unit_id=? ORDER BY sim ASC LIMIT 1)`, v.id, v.id)
				}
			}
		}
	}
	return nil
}
