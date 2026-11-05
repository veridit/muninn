package app

import (
	"context"
	"os"

	"github.com/veridit/muninn/internal/search"
)

// IndexStats reports only data reachable through the active binding.
type IndexStats struct {
	Units       int            `json:"units"`
	Active      int            `json:"active"`
	ByKind      map[string]int `json:"by_kind"`
	ByScope     map[string]int `json:"by_scope"`
	Vectors     int            `json:"vectors"`
	Concepts    int            `json:"concepts"`
	Communities int            `json:"communities"`
	Neighbors   int            `json:"neighbor_edges"`
	Injections  int            `json:"injections"`
	Embedder    string         `json:"embedder"`
	DBPath      string         `json:"db_path"`
	Binding     string         `json:"binding"`
}

// Stats defaults to the same environment binding as every other surface.
func Stats(ctx context.Context, a *App, bindings ...Binding) (IndexStats, error) {
	cwd, _ := os.Getwd()
	b := a.Bound("", cwd)
	if len(bindings) > 0 {
		b = bindings[0]
	}
	s := IndexStats{ByKind: map[string]int{}, ByScope: map[string]int{}, Binding: b.Name}
	where, args := search.ScopeSQL("u.scope", b.Scopes)
	cte := `WITH visible AS (SELECT u.* FROM units u WHERE (` + where + `)
	 AND u.id=(SELECT c.id FROM units c JOIN sources cs ON cs.id=c.source_id WHERE c.sid=u.sid ORDER BY (cs.kind='native') DESC,c.updated_ns DESC,c.id LIMIT 1)) `
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM visible`, &s.Units},
		{`SELECT COUNT(*) FROM visible WHERE active=1 AND sid NOT IN (SELECT sid FROM suppressed)`, &s.Active},
		{`SELECT COUNT(*) FROM embeddings e WHERE EXISTS (SELECT 1 FROM visible u WHERE u.content_hash=e.content_hash)`, &s.Vectors},
		{`SELECT COUNT(DISTINCT concept_id) FROM unit_concepts WHERE unit_id IN (SELECT id FROM visible)`, &s.Concepts},
		{`SELECT COUNT(DISTINCT community_id) FROM unit_communities WHERE unit_id IN (SELECT id FROM visible)`, &s.Communities},
		{`SELECT COUNT(*) FROM neighbors WHERE unit_id IN (SELECT id FROM visible) AND nbr_id IN (SELECT id FROM visible)`, &s.Neighbors},
		{`SELECT COUNT(*) FROM injections WHERE unit_id IN (SELECT id FROM visible)`, &s.Injections},
	} {
		if err := a.DB.QueryRowContext(ctx, cte+q.sql, args...).Scan(q.dst); err != nil {
			return s, err
		}
	}
	for _, g := range []struct {
		col string
		m   map[string]int
	}{{"kind", s.ByKind}, {"scope", s.ByScope}} {
		rows, err := a.DB.QueryContext(ctx, cte+`SELECT `+g.col+`,COUNT(*) FROM visible WHERE active=1 AND sid NOT IN (SELECT sid FROM suppressed) GROUP BY `+g.col, args...)
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return s, err
			}
			g.m[k] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s, err
		}
	}
	s.Embedder = "none (keyword-only)"
	if a.Embedder != nil {
		s.Embedder = a.Embedder.Model() + " @ " + a.Cfg.OllamaURL
	}
	s.DBPath = a.DB.Path
	return s, nil
}
