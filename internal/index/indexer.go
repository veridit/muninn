package index

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/embed"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
)

// Stats summarises one indexing pass.
type Stats struct {
	SourcesScanned, SourcesSkipped     int
	Added, Changed, Removed, Unchanged int
	Embedded, EmbedReused              int
	Elapsed                            time.Duration
	EmbedErr                           error
}

func (s Stats) String() string {
	return fmt.Sprintf("sources scanned=%d skipped=%d | units added=%d changed=%d removed=%d unchanged=%d | embedded=%d reused=%d | %s",
		s.SourcesScanned, s.SourcesSkipped, s.Added, s.Changed, s.Removed, s.Unchanged, s.Embedded, s.EmbedReused, s.Elapsed.Round(time.Millisecond))
}

// Indexer applies sources to the store incrementally.
type Indexer struct {
	DB       *store.DB
	Embedder embed.Embedder // nil = keyword-only

	deltas []UnitDelta // concept-set changes collected during a pass
}

// Candidate is a unit as produced by a source, before storage.
type Candidate struct {
	Key, Kind, Title, Body, Category, Scope string
	Tags                                    []string
	Active                                  bool
	Updated                                 time.Time
}

// SyncSource reconciles one source with its current candidates. Units whose
// content hash is unchanged are not touched at all: no write, no re-embed, no
// graph work. Only dirty units flow onward (seid search's incremental rule).
func (ix *Indexer) SyncSource(sourceID int64, cands []Candidate, st *Stats) (dirty []int64, removed []int64, err error) {
	existing, err := ix.DB.UnitHashes(sourceID)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, c := range cands {
		seen[c.Key] = true
		text := EmbeddingText(c.Title, c.Body)
		ident := ContentHash(text + "\x00" + c.Category + "\x00" + c.Scope + "\x00" +
			strconv.FormatBool(c.Active) + "\x00" + strings.Join(c.Tags, " "))
		if old, ok := existing[c.Key]; ok && old[1] == ident {
			st.Unchanged++
			continue
		}
		var oldConcepts []int
		if old, ok := existing[c.Key]; ok {
			oldID, _ := strconv.ParseInt(old[0], 10, 64)
			if oldConcepts, err = ix.unitConcepts(oldID); err != nil {
				return nil, nil, err
			}
		}
		id, err := ix.DB.UpsertUnit(store.Unit{
			SourceID: sourceID, Key: c.Key, ContentHash: ContentHash(text), Ident: ident, Kind: c.Kind,
			Title: c.Title, Body: c.Body, Category: c.Category, Tags: c.Tags, Scope: c.Scope,
			Active: c.Active, Updated: c.Updated,
		})
		if err != nil {
			return nil, nil, err
		}
		var newConcepts []int
		if c.Active {
			if newConcepts, err = ix.setUnitConcepts(id, Concepts(c.Title+" "+c.Body+" "+strings.Join(c.Tags, " "))); err != nil {
				return nil, nil, err
			}
		} else if _, err = ix.setUnitConcepts(id, nil); err != nil {
			return nil, nil, err
		}
		ix.deltas = append(ix.deltas, UnitDelta{Old: oldConcepts, New: newConcepts})
		if _, ok := existing[c.Key]; ok {
			st.Changed++
		} else {
			st.Added++
		}
		dirty = append(dirty, id)
	}
	for key, old := range existing {
		if seen[key] {
			continue
		}
		id, _ := strconv.ParseInt(old[0], 10, 64)
		oldConcepts, err := ix.unitConcepts(id)
		if err != nil {
			return nil, nil, err
		}
		ix.deltas = append(ix.deltas, UnitDelta{Old: oldConcepts})
		if err := ix.DB.DeleteUnit(id); err != nil {
			return nil, nil, err
		}
		removed = append(removed, id)
		st.Removed++
	}
	return dirty, removed, nil
}

// statOf returns a file's (mtime ns, size).
func statOf(path string) (int64, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	return fi.ModTime().UnixNano(), fi.Size(), nil
}

// IndexJcode indexes the jcode memory store under dir with scope prefix
// (e.g. "jcode@brovs").
func (ix *Indexer) IndexJcode(ctx context.Context, dir, prefix string, st *Stats) ([]int64, []int64, error) {
	files, err := ingest.JcodeFiles(dir, prefix)
	if err != nil {
		return nil, nil, err
	}
	var dirtyAll, removedAll []int64
	for path, scope := range files {
		d, r, err := ix.indexFile(path, "jcode", st, func() ([]Candidate, error) {
			mems, err := ingest.ReadJcodeFile(path)
			if err != nil {
				return nil, err
			}
			out := make([]Candidate, 0, len(mems))
			for _, m := range mems {
				out = append(out, Candidate{Key: m.Key, Kind: "memory", Body: m.Content, Category: m.Category,
					Tags: m.Tags, Scope: scope, Active: m.Active, Updated: m.Updated})
			}
			return out, nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		dirtyAll, removedAll = append(dirtyAll, d...), append(removedAll, r...)
	}
	return dirtyAll, removedAll, nil
}

// IndexNative indexes muninn's own memory store.
func (ix *Indexer) IndexNative(ctx context.Context, path string, st *Stats) ([]int64, []int64, error) {
	return ix.indexFile(path, "native", st, func() ([]Candidate, error) {
		mems, err := ingest.LoadNative(path)
		if err != nil {
			return nil, err
		}
		out := make([]Candidate, 0, len(mems))
		for _, m := range mems {
			out = append(out, Candidate{Key: m.ID, Kind: "memory", Body: m.Content, Category: m.Category,
				Tags: m.Tags, Scope: m.Scope, Active: m.Active, Updated: m.Updated})
		}
		return out, nil
	})
}

// IndexMarkdown indexes markdown files under roots, split by heading.
func (ix *Indexer) IndexMarkdown(ctx context.Context, files []string, st *Stats) ([]int64, []int64, error) {
	var dirtyAll, removedAll []int64
	for _, path := range files {
		d, r, err := ix.indexFile(path, "markdown", st, func() ([]Candidate, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			secs := ingest.SplitMarkdown(path, string(b))
			out := make([]Candidate, 0, len(secs))
			for _, s := range secs {
				out = append(out, Candidate{Key: "L" + strconv.Itoa(s.Line) + ":" + s.Heading, Kind: "section",
					Title: s.Heading, Body: s.Content, Scope: "notes", Active: true})
			}
			return out, nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		dirtyAll, removedAll = append(dirtyAll, d...), append(removedAll, r...)
	}
	return dirtyAll, removedAll, nil
}

// indexFile skips a file whose (mtime, size) is unchanged; otherwise it parses
// and reconciles it.
func (ix *Indexer) indexFile(path, kind string, st *Stats, parse func() ([]Candidate, error)) ([]int64, []int64, error) {
	mtime, size, err := statOf(path)
	if err != nil {
		return nil, nil, err
	}
	id, oldM, oldS, err := ix.DB.Source(kind, path)
	if err != nil {
		return nil, nil, err
	}
	if oldM == mtime && oldS == size {
		st.SourcesSkipped++
		return nil, nil, nil
	}
	st.SourcesScanned++
	cands, err := parse()
	if err != nil {
		return nil, nil, err
	}
	dirty, removed, err := ix.SyncSource(id, cands, st)
	if err != nil {
		return nil, nil, err
	}
	return dirty, removed, ix.DB.SetSourceStat(id, mtime, size)
}

// EmbedDirty embeds units that lack a vector under the current pipeline.
// Identical text across units shares one vector (content-addressed).
func (ix *Indexer) EmbedDirty(ctx context.Context, st *Stats) error {
	if ix.Embedder == nil {
		return nil
	}
	pipeline := PipelineHash(ix.Embedder.Model())
	rows, err := ix.DB.Query(`SELECT DISTINCT u.content_hash, u.title, u.body FROM units u
	  LEFT JOIN embeddings e ON e.content_hash=u.content_hash AND e.pipeline_hash=?
	  WHERE e.content_hash IS NULL`, pipeline)
	if err != nil {
		return err
	}
	type job struct{ hash, text string }
	var jobs []job
	for rows.Next() {
		var h, t, b string
		if err := rows.Scan(&h, &t, &b); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job{h, EmbeddingText(t, b)})
	}
	rows.Close()
	var total int
	ix.DB.QueryRow(`SELECT COUNT(DISTINCT content_hash) FROM units`).Scan(&total)
	st.EmbedReused = total - len(jobs)
	for start := 0; start < len(jobs); start += embed.BatchSize {
		end := min(start+embed.BatchSize, len(jobs))
		texts := make([]string, 0, end-start)
		for _, j := range jobs[start:end] {
			texts = append(texts, j.text)
		}
		vecs, err := ix.Embedder.Embed(ctx, texts)
		if err != nil {
			st.EmbedErr = err
			return nil // degrade to keyword-only; never fail indexing
		}
		for i, v := range vecs {
			if err := ix.DB.PutEmbedding(jobs[start+i].hash, pipeline, len(v),
				embed.EncodeF32(v), embed.Quantize(v), embed.Sketch(v)); err != nil {
				return err
			}
			st.Embedded++
		}
	}
	return ix.DB.SetMeta("pipeline_hash", pipeline)
}
