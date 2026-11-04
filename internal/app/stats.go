package app

import (
	"context"
)

// IndexStats summarises the store for `muninn stats`, the web UI and MCP.
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
}

// Stats computes IndexStats.
func Stats(ctx context.Context, a *App) (IndexStats, error) {
	s := IndexStats{ByKind: map[string]int{}, ByScope: map[string]int{}}
	q := func(sql string, dst *int) { a.DB.QueryRowContext(ctx, sql).Scan(dst) }
	q(`SELECT COUNT(*) FROM units`, &s.Units)
	q(`SELECT COUNT(*) FROM units WHERE active=1`, &s.Active)
	q(`SELECT COUNT(*) FROM embeddings`, &s.Vectors)
	q(`SELECT COUNT(*) FROM concepts`, &s.Concepts)
	q(`SELECT COUNT(DISTINCT community_id) FROM communities`, &s.Communities)
	q(`SELECT COUNT(*) FROM neighbors`, &s.Neighbors)
	q(`SELECT COUNT(*) FROM injections`, &s.Injections)
	for _, g := range []struct {
		col string
		m   map[string]int
	}{{"kind", s.ByKind}, {"scope", s.ByScope}} {
		rows, err := a.DB.QueryContext(ctx, `SELECT `+g.col+`, COUNT(*) FROM units WHERE active=1 GROUP BY `+g.col)
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var k string
			var n int
			rows.Scan(&k, &n)
			g.m[k] = n
		}
		rows.Close()
	}
	s.Embedder = "none (keyword-only)"
	if a.Embedder != nil {
		s.Embedder = a.Embedder.Model() + " @ " + a.Cfg.OllamaURL
	}
	s.DBPath = a.DB.Path
	return s, nil
}
