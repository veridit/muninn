package app

import (
	"context"
	"fmt"

	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/store"
)

// ForgetResult distinguishes retirement of an owned memory from local suppression.
type ForgetResult struct {
	ID         string `json:"id"`
	Suppressed bool   `json:"suppressed,omitempty"`
	Message    string `json:"message"`
}

// Forget resolves every public identifier through the same store resolver.
// Non-owned source files are never modified.
func (a *App) Forget(ctx context.Context, ref string) (ForgetResult, error) {
	units, err := a.DB.ResolveAll(ref)
	if err != nil {
		return ForgetResult{}, err
	}
	out := ForgetResult{ID: units[0].SID}
	retired, mirrors := false, false
	for _, u := range units {
		var kind, path string
		if err := a.DB.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&kind, &path); err != nil {
			return out, err
		}
		if kind != "native" {
			mirrors = true
			continue
		}
		ok, err := ingest.Forget(path, u.Key)
		if err != nil {
			return out, err
		}
		retired = retired || ok
		if _, err := a.DB.Exec(`UPDATE units SET active=0 WHERE id=?`, u.ID); err != nil {
			return out, err
		}
	}
	if mirrors {
		if err := a.DB.Suppress(out.ID); err != nil {
			return out, err
		}
		out.Suppressed = true
	}
	if !retired && !mirrors {
		return out, fmt.Errorf("no active memory %q", ref)
	}
	if retired {
		if _, err := a.Reindex(ctx); err != nil {
			return out, err
		}
	}
	switch {
	case retired && mirrors:
		out.Message = "forgot " + out.ID + " and suppressed its mirrors in muninn (upstream files unchanged)"
	case mirrors:
		out.Message = "suppressed " + out.ID + " in muninn (upstream files unchanged)"
	default:
		out.Message = "forgot " + out.ID
	}
	return out, nil
}

// Origin describes the retained upstream mapping, not the public display id.
type Origin struct {
	SID    string `json:"sid"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Key    string `json:"key"`
	Origin string `json:"origin"`
}

func (a *App) Origin(u store.Unit) (Origin, error) {
	o := Origin{SID: u.SID, Key: u.Key, Origin: u.Origin}
	err := a.DB.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&o.Source, &o.Path)
	return o, err
}
