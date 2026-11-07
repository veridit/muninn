package tidy

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/store"
)

// readEffectiveScopes shares Binding.ScopeFor's source-client provenance rule,
// but scans all clients as an owner. Stored scopes remain untouched for apply
// and fingerprints. Invalid physical boundaries fail closed, never fall back.
func readEffectiveScopes(ctx context.Context, tx *sql.Tx, units []store.Unit) (map[int64]string, error) {
	wanted := map[int64]bool{}
	for _, u := range units {
		wanted[u.SourceID] = true
	}
	clients := map[int64]string{}
	rows, err := tx.QueryContext(ctx, `SELECT id,path,kind FROM sources`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var path, kind string
		if err = rows.Scan(&id, &path, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		if !wanted[id] || kind == "native" {
			continue
		}
		client, e := project.SourceClient(path)
		if e != nil {
			rows.Close()
			return nil, fmt.Errorf("effective scope for source %d: %w", id, e)
		}
		clients[id] = client
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, u := range units {
		out[u.ID] = u.Scope
		if client := clients[u.SourceID]; client != "" {
			out[u.ID] = "client:" + client
		}
	}
	return out, nil
}

func (g Graph) scopeFor(u store.Unit) string {
	if scope, ok := g.EffectiveScopes[u.ID]; ok {
		return scope
	}
	return u.Scope
}

func effectiveSource(e Entry) string {
	if e.EffectiveSource != "" {
		return e.EffectiveSource
	}
	return e.Source
}
