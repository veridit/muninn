package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/store"
)

// captureBatch is a chunk-local native snapshot. Receipts and deterministic IDs
// live in the same atomic file write as the memory, before the SQL checkpoint.
type captureBatch struct {
	mems        []ingest.NativeMemory
	byID, bySID map[string]int
	receipts    map[string]bool
	retire      map[string]bool   // sid -> has upstream mirrors
	reinforce   map[string]string // receipt -> upstream sid
	dirty       bool
}

func newCaptureBatch() (*captureBatch, error) {
	mems, err := ingest.LoadNative(NativePath())
	if err != nil {
		return nil, err
	}
	c := &captureBatch{mems: mems, byID: map[string]int{}, bySID: map[string]int{}, receipts: map[string]bool{}, retire: map[string]bool{}, reinforce: map[string]string{}}
	for i, m := range mems {
		c.byID[m.ID] = i
		c.bySID[m.SID] = i
		for _, r := range m.CaptureEvents {
			c.receipts[r] = true
		}
	}
	return c, nil
}
func (c *captureBatch) exact(b Binding, a *App, candidate extract.Candidate) (CaptureAction, bool) {
	for _, m := range c.mems {
		if !m.Active || m.Content != candidate.Content || a.DB.IsSuppressed(m.SID) {
			continue
		}
		if _, err := b.WriteScope(m.Scope); err != nil {
			continue
		}
		candidate.Supersedes = ""
		return CaptureAction{Candidate: candidate, Action: "reinforce", Reason: "exact duplicate in native chunk snapshot", Target: m.SID}, true
	}
	return CaptureAction{}, false
}
func (c *captureBatch) apply(ctx context.Context, b Binding, a *App, scope string, action *CaptureAction, receipt string) error {
	b = b.Refresh(a)
	// A retried model may pick a different place. The persisted receipt owns the
	// original destination and supersede intent, just as it owns the content.
	for _, m := range c.mems {
		if m.Origin == action.Origin && slices.Contains(m.CaptureEvents, receipt) {
			scope = m.Scope
			action.Scope = scope
			action.Place = b.PlaceForScope(scope).Name
			action.PlaceHint = b.PlacementHint(action.Content, scope)
			break
		}
	}
	if _, err := b.WriteScope(scope); err != nil {
		return err
	}
	if project.LooksSecret(action.Content) && action.SafeReason == "" {
		return fmt.Errorf("secret candidate refused")
	}
	now := time.Now().UTC()
	h := captureHash(action.Origin + "\x00" + scope)
	id := "cap_" + h[:16]
	existing, hasExisting := c.byID[id]
	// A legacy position collision may have been re-keyed on load. Its receipt
	// remains authoritative and must not be attached to the first row instead.
	if c.receipts[receipt] {
		for i, m := range c.mems {
			if m.Origin == action.Origin && m.Scope == scope && slices.Contains(m.CaptureEvents, receipt) {
				existing, hasExisting = i, true
				break
			}
		}
	}
	retireTarget := ""
	if hasExisting {
		m := c.mems[existing]
		if m.Origin != action.Origin || m.Scope != scope {
			return fmt.Errorf("capture native id collision")
		}
		// Recovery must replay the stored intent, not a regenerated model's
		// potentially different target at the same deterministic position.
		retireTarget = m.CaptureSupersedes
	} else if !c.receipts[receipt] && action.Action == "supersede" && action.Trust == "high" {
		retireTarget = action.Target
	}
	if retireTarget != "" {
		// Revalidate all owned mirrors before any file mutation. Upstream stores are
		// never written. Retirement is part of this chunk's single native save.
		targetPresent := true
		if hasExisting {
			if err := a.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM units WHERE sid=?)`, retireTarget).Scan(&targetPresent); err != nil {
				return err
			}
		}
		var units []store.Unit
		if targetPresent {
			var err error
			units, err = a.DB.ResolveAll(retireTarget)
			if err != nil {
				return err
			}
		}
		mirrors := false
		for n, u := range units {
			var kind string
			if err := a.DB.QueryRowContext(ctx, `SELECT kind FROM sources WHERE id=?`, u.SourceID).Scan(&kind); err != nil {
				return err
			}
			if kind != "native" {
				mirrors = true
				if n > 0 {
					continue
				}
			}
			shown, ok := b.Present(u)
			if !ok {
				return fmt.Errorf("no visible memory")
			}
			if _, err := b.WriteScope(shown.Scope); err != nil {
				return err
			}
		}
		if i, ok := c.bySID[retireTarget]; ok {
			if _, err := b.WriteScope(c.mems[i].Scope); err != nil {
				return err
			}
			c.mems[i].Active = false
			c.mems[i].Updated = now
			c.dirty = true
		}
		c.retire[retireTarget] = mirrors
	}
	if hasExisting {
		m := &c.mems[existing]
		if !slices.Contains(m.CaptureEvents, receipt) {
			m.CaptureEvents = append(m.CaptureEvents, receipt)
			c.dirty = true
		}
		c.receipts[receipt] = true
		action.Action = "skip"
		action.Reason = "capture id already persisted before checkpoint"
		action.Target = retireTarget
		return nil
	}
	if c.receipts[receipt] {
		action.Action = "skip"
		action.Reason = "receipt already persisted before checkpoint"
		return nil
	}
	if action.Action == "reinforce" {
		if i, ok := c.bySID[action.Target]; ok {
			m := &c.mems[i]
			if _, err := b.WriteScope(m.Scope); err != nil {
				action.Action = "skip"
				action.Reason = "duplicate outside writable scope"
				return nil
			}
			if !m.Active {
				action.Action = "skip"
				action.Reason = "target retired in current chunk"
				return nil
			}
			m.Strength++
			m.Updated = now
			m.CaptureEvents = append(m.CaptureEvents, receipt)
			c.receipts[receipt] = true
			c.dirty = true
			return nil
		}
		u, err := b.Resolve(a, action.Target)
		if err != nil {
			return err
		}
		if _, err := b.WriteScope(u.Scope); err != nil {
			action.Action = "skip"
			action.Reason = "duplicate outside writable scope"
			return nil
		}
		c.reinforce[receipt] = u.SID
		return nil
	}
	if action.Action == "skip" {
		return nil
	}
	c.byID[id] = len(c.mems)
	c.mems = append(c.mems, ingest.NativeMemory{ID: id, Origin: action.Origin, Content: action.Content, Category: action.Category, Trust: action.Trust, Scope: scope, Active: true, Strength: 1, Created: now, Updated: now, CaptureEvents: []string{receipt}, CaptureSupersedes: retireTarget, SafeReason: action.SafeReason})
	c.receipts[receipt] = true
	c.dirty = true
	return nil
}
func (c *captureBatch) save(ctx context.Context, a *App) error {
	if c.dirty {
		if err := ingest.SaveNative(NativePath(), c.mems); err != nil {
			return err
		}
	}
	// No reindex here: graph/community work belongs to the next recall/index pass.
	// Suppress retired units immediately, so a stale index cannot recall them.
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for sid, mirrors := range c.retire {
		if _, err = tx.ExecContext(ctx, `UPDATE units SET active=0 WHERE sid=?`, sid); err != nil {
			return err
		}
		if mirrors {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO suppressed(sid) VALUES(?)`, sid); err != nil {
				return err
			}
		}
	}
	for receipt, sid := range c.reinforce {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO capture_reinforcements(event,sid,at_ns) VALUES(?,?,?)`, receipt, sid, time.Now().UnixNano()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
