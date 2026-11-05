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
	u, err := a.DB.Resolve(ref)
	if err != nil {
		return ForgetResult{}, err
	}
	var kind, path string
	if err := a.DB.QueryRow(`SELECT kind,path FROM sources WHERE id=?`, u.SourceID).Scan(&kind, &path); err != nil {
		return ForgetResult{}, err
	}
	out := ForgetResult{ID: u.SID}
	if kind == "native" {
		ok, err := ingest.Forget(path, u.Key)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, fmt.Errorf("no active memory %q", ref)
		}
		// Update immediately as well as the incremental index, so other callers
		// never see an active forgotten row if the source scan is deferred.
		if _, err := a.DB.Exec(`UPDATE units SET active=0 WHERE id=?`, u.ID); err != nil {
			return out, err
		}
		if _, err := a.Reindex(ctx); err != nil {
			return out, err
		}
		out.Message = "forgot " + u.SID
	} else {
		if err := a.DB.Suppress(u.SID); err != nil {
			return out, err
		}
		out.Suppressed = true
		out.Message = "suppressed " + u.SID + " in muninn (upstream files unchanged)"
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
