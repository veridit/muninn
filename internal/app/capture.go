package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
)

type CaptureOptions struct {
	Harness, Session, Home     string
	Trigger                    string
	Messages                   []harness.CaptureMessage
	DryRun, Periodic, Backfill bool
}
type CaptureAction struct {
	extract.Candidate
	Action string  `json:"action"`
	Reason string  `json:"reason"`
	Score  float64 `json:"cosine,omitempty"`
	Target string  `json:"target,omitempty"`
	Origin string  `json:"origin"`
}
type CaptureReport struct {
	Actions        []CaptureAction `json:"actions"`
	Usage          extract.Usage   `json:"usage"`
	LatencyMS      int64           `json:"latency_ms"`
	Processed      int             `json:"processed"`
	BacklogSkipped int             `json:"backlog_skipped"`
	Rejected       int             `json:"rejected"`
	Skipped        string          `json:"skipped,omitempty"`
}

func captureHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Capture reuses the surface Binding for all reads and writes. The caller must
// resolve a session's persisted cwd, not the daemon's working directory.
func (b Binding) Capture(ctx context.Context, a *App, o CaptureOptions) (report CaptureReport, runErr error) {
	audit := AuditRun{Time: time.Now().UTC(), Harness: o.Harness, Session: o.Session, Home: o.Home, Trigger: o.Trigger, DryRun: o.DryRun, Backfill: o.Backfill, Model: a.Cfg.Extract.Model}
	if audit.Trigger == "" {
		audit.Trigger = "manual"
	}
	defer func() {
		audit.Report = report
		if runErr != nil {
			audit.Error = auditSafe(runErr.Error())
			audit.ErrorClass = extract.FailureClass(runErr)
		}
		if err := a.SaveAudit(audit); err != nil && runErr == nil {
			runErr = fmt.Errorf("capture audit: %w", err)
		}
	}()
	if a.Cfg.Extract.Provider == "off" {
		report.Skipped = "extraction off"
		return report, nil
	}
	// Automatic capture never inherits an owner's temporarily widened read view.
	if c := b.context; c != nil {
		b = a.Bound(c.home, c.cwd)
	} else {
		b = b.Refresh(a)
	}
	scope, err := b.WriteScope("")
	audit.Scope = scope
	if err != nil {
		return report, err
	}
	if o.Session == "" || o.Harness == "" {
		return report, errors.New("capture requires a harness and session identity")
	}
	seconds := a.Cfg.Extract.TimeoutSeconds
	if seconds <= 0 {
		seconds = 45
	}
	if seconds > 120 {
		seconds = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	if !o.DryRun {
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			return report, err
		}
		token := hex.EncodeToString(tokenBytes)
		for {
			now := time.Now()
			res, err := a.DB.ExecContext(ctx, `INSERT INTO capture_lock(id,token,expires_ns) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET token=excluded.token,expires_ns=excluded.expires_ns WHERE capture_lock.expires_ns<?`, token, now.Add(time.Duration(seconds+10)*time.Second).UnixNano(), now.UnixNano())
			if err != nil {
				return report, err
			}
			n, _ := res.RowsAffected()
			if n > 0 {
				break
			}
			if o.Periodic {
				report.Skipped = "capture already running, retry on next hook"
				return report, nil
			}
			// Final observers wait within their detached deadline rather than dropping
			// the last delta just because a periodic observer is still extracting.
			select {
			case <-ctx.Done():
				return report, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		defer a.DB.Exec(`DELETE FROM capture_lock WHERE id=1 AND token=?`, token)
	}
	identity := captureHash(o.Harness + "\x00" + o.Home + "\x00" + o.Session)
	if !o.DryRun {
		if _, err := a.DB.ExecContext(ctx, `INSERT OR IGNORE INTO capture_bindings(session,scope) VALUES(?,?)`, identity, scope); err != nil {
			return report, err
		}
	}
	var originalScope string
	err = a.DB.QueryRowContext(ctx, `SELECT scope FROM capture_bindings WHERE session=?`, identity).Scan(&originalScope)
	if err != nil && err != sql.ErrNoRows {
		return report, err
	}
	if err == nil && originalScope != scope {
		return report, errors.New("capture session binding changed, start a new session")
	}
	session := captureHash(identity + "\x00" + scope)
	seen := map[string]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT message FROM capture_seen WHERE session=?`, session)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return report, err
		}
		seen[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	if o.Backfill {
		rows, err := a.DB.QueryContext(ctx, `SELECT message FROM capture_backlog WHERE session=?`, session)
		if err != nil {
			return report, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return report, err
			}
			delete(seen, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, err
		}
	}
	var pending []harness.CaptureMessage
	turns := 0
	for _, m := range o.Messages {
		if !seen[m.ID] {
			pending = append(pending, m)
			if m.Role == "user" && m.Text != "" {
				turns++
			}
		}
	}
	every := a.Cfg.Extract.EveryTurns
	if every <= 0 {
		every = 12
	}
	if len(seen) == 0 && !o.Backfill && (o.Trigger == "turn_end" || o.Trigger == "session_end") && turns > every*2 {
		remaining := every * 2
		cut := len(pending)
		for cut > 0 {
			cut--
			if pending[cut].Role == "user" && pending[cut].Text != "" {
				remaining--
				if remaining == 0 {
					break
				}
			}
		}
		older := pending[:cut]
		report.BacklogSkipped = len(older)
		if !o.DryRun {
			tx, err := a.DB.BeginTx(ctx, nil)
			if err != nil {
				return report, err
			}
			for _, m := range older {
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO capture_seen(session,message) VALUES(?,?)`, session, m.ID); err != nil {
					break
				}
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO capture_backlog(session,message) VALUES(?,?)`, session, m.ID); err != nil {
					break
				}
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO capture_state(session,high_water) VALUES(?,?) ON CONFLICT(session) DO UPDATE SET high_water=(SELECT count(*) FROM capture_seen WHERE session=excluded.session)`, session, len(older))
			}
			if err != nil {
				tx.Rollback()
				return report, err
			}
			if err = tx.Commit(); err != nil {
				return report, err
			}
		}
		for _, m := range older {
			seen[m.ID] = true
		}
		pending = pending[cut:]
		turns = every * 2
	}
	if o.Periodic && turns < every {
		report.Skipped = "waiting for extraction interval"
		return report, nil
	}
	if len(pending) == 0 {
		report.Skipped = "no new messages"
		return report, nil
	}
	// Chunk by messages, retaining their stable ids. No tail truncation: messages
	// beyond this invocation's deadline stay pending for the next observer.
	for start := 0; start < len(pending); {
		var chunk strings.Builder
		end := start
		for end < len(pending) {
			m := pending[end]
			t := m.Text
			if project.LooksSecret(t) {
				t = ""
			}
			line := m.Role + ": " + t + "\n"
			if t != "" && chunk.Len()+len(line) > 48<<10 && end > start {
				break
			}
			if len(line) > 48<<10 {
				return report, errors.New("capture message exceeds 48 KiB, refusing silent loss")
			}
			audit.Messages = append(audit.Messages, AuditMessage{ID: m.ID, Role: m.Role, Hash: captureHash(m.Text), Text: t})
			if t != "" {
				chunk.WriteString(line)
			}
			end++
		}
		text := chunk.String()
		var result extract.Result
		if strings.TrimSpace(text) != "" {
			opts := a.RecallOptions()
			opts.MinScore = 0
			opts.Limit = a.Cfg.Extract.TopK
			if opts.Limit <= 0 {
				opts.Limit = 12
			}
			opts.Scopes = b.Scopes
			opts.ScopeFor = b.ScopeFor
			opts.Kinds = []string{"memory"}
			// Limit query words so transcript-sized FTS expressions remain bounded.
			query := text
			if len(query) > 4000 {
				query = query[len(query)-4000:]
			}
			hits, err := a.Searcher.Search(ctx, query, opts)
			if err != nil {
				return report, err
			}
			var known []extract.Known
			for _, r := range hits {
				if !project.LooksSecret(r.Unit.Body) {
					known = append(known, extract.Known{SID: r.Unit.SID, Content: r.Unit.Body})
				}
			}
			// Leave time to persist this chunk after the provider returns.
			providerCtx := ctx
			providerCancel := func() {}
			if deadline, ok := ctx.Deadline(); ok {
				reserve := 2 * time.Second
				if left := time.Until(deadline); left < 4*time.Second {
					reserve = left / 4
				}
				providerCtx, providerCancel = context.WithDeadline(ctx, deadline.Add(-reserve))
			}
			result, err = extract.Run(providerCtx, a.Cfg.Extract, text, known)
			providerCancel()
			audit.Calls = append(audit.Calls, result)
			if err != nil {
				report.Usage.PromptTokens += result.Usage.PromptTokens
				report.Usage.CompletionTokens += result.Usage.CompletionTokens
				report.Usage.TotalTokens += result.Usage.TotalTokens
				report.LatencyMS += result.LatencyMS
				return report, err
			}
		}
		report.Usage.PromptTokens += result.Usage.PromptTokens
		report.Usage.CompletionTokens += result.Usage.CompletionTokens
		report.Usage.TotalTokens += result.Usage.TotalTokens
		report.LatencyMS += result.LatencyMS
		report.Rejected += result.Rejected
		// Stable batch fingerprint makes retries idempotent after native-file save
		// but before the SQLite high-water checkpoint commits.
		ids := make([]string, 0, end-start)
		for _, m := range pending[start:end] {
			ids = append(ids, m.ID)
		}
		batch := captureHash(session + "\x00" + strings.Join(ids, "\x00"))
		local := map[string]bool{}
		for n, c := range result.Candidates {
			if local[c.Content] {
				report.Actions = append(report.Actions, CaptureAction{Candidate: c, Action: "skip", Reason: "duplicate proposal in batch"})
				continue
			}
			local[c.Content] = true
			action, err := b.capturePlan(ctx, a, c)
			if err != nil {
				return report, err
			}
			action.Origin = fmt.Sprintf("capture:%s:%s:%s:%d", o.Harness, o.Session, batch[:12], n)
			// Include the binding/home in receipts even when harness ids collide.
			receipt := captureHash(session + "\x00" + batch + "\x00" + c.Content)
			if !o.DryRun {
				if err := b.captureApply(ctx, a, scope, action, receipt); err != nil {
					return report, err
				}
			}
			report.Actions = append(report.Actions, action)
		}
		if !o.DryRun {
			tx, err := a.DB.BeginTx(ctx, nil)
			if err != nil {
				return report, err
			}
			for _, m := range pending[start:end] {
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO capture_seen(session,message) VALUES(?,?)`, session, m.ID); err != nil {
					break
				}
				if _, err = tx.ExecContext(ctx, `DELETE FROM capture_backlog WHERE session=? AND message=?`, session, m.ID); err != nil {
					break
				}
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO capture_state(session,high_water) VALUES(?,?) ON CONFLICT(session) DO UPDATE SET high_water=(SELECT count(*) FROM capture_seen WHERE session=excluded.session)`, session, len(seen)+end)
			}
			if err != nil {
				tx.Rollback()
				return report, err
			}
			if err = tx.Commit(); err != nil {
				return report, err
			}
		}
		report.Processed += end - start
		start = end
	}
	return report, nil
}

func (b Binding) capturePlan(ctx context.Context, a *App, c extract.Candidate) (CaptureAction, error) {
	b = b.Refresh(a)
	out := CaptureAction{Candidate: c, Action: "add", Reason: "new memory in bound scope"}
	if c.Supersedes != "" && c.Trust == "high" {
		old, err := b.Resolve(a, c.Supersedes)
		if err == nil && old.Active {
			if _, err := b.WriteScope(old.Scope); err == nil {
				out.Action = "supersede"
				out.Reason = "high-trust correction in writable scope"
				out.Target = old.SID
				return out, nil
			}
		}
	}
	// Ignore invalid/low-trust supersession requests entirely, including display.
	out.Supersedes = ""
	if c.Supersedes != "" {
		out.Reason = "new memory, supersede target unavailable, outside writable scope, or not high trust"
	}
	// Exact matching searches every visible memory, not merely the model's top-K.
	where, args := b.SQL("u.scope", "u.source_id")
	args = append(args, c.Content)
	var id int64
	rows, err := a.DB.QueryContext(ctx, `SELECT u.id FROM units u WHERE u.active=1 AND u.kind='memory' AND (`+where+`) AND u.body=? AND NOT EXISTS (SELECT 1 FROM suppressed s WHERE s.sid=u.sid) AND u.id=(SELECT c.id FROM units c JOIN sources s ON s.id=c.source_id WHERE c.sid=u.sid ORDER BY (s.kind='native') DESC,c.updated_ns DESC,c.id LIMIT 1) ORDER BY u.id LIMIT 1`, args...)
	if err != nil {
		return out, err
	}
	if rows.Next() {
		err = rows.Scan(&id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if rowErr != nil {
		return out, rowErr
	}
	if id != 0 {
		u, err := a.DB.GetUnit(id)
		if err != nil {
			return out, err
		}
		out.Action = "reinforce"
		out.Reason = "exact duplicate"
		out.Target = u.SID
		return out, nil
	}
	// Cosine alone, not total keyword score, decides near-duplicate reinforcement.
	if a.Searcher.Embedder != nil {
		opts := a.RecallOptions()
		opts.Limit = 8
		opts.MinScore = 0
		opts.SemanticSlots = 8
		opts.SemanticFloor = 0
		opts.Scopes = b.Scopes
		opts.ScopeFor = b.ScopeFor
		opts.Kinds = []string{"memory"}
		hits, err := a.Searcher.Search(ctx, c.Content, opts)
		if err != nil {
			return out, err
		}
		threshold := a.Cfg.Extract.DedupeCosine
		if threshold <= 0 {
			threshold = extract.Defaults().DedupeCosine
		}
		for _, hit := range hits {
			if hit.SemanticOn && hit.Semantic >= threshold {
				out.Action = "reinforce"
				out.Reason = "cosine exceeds dedupe threshold"
				out.Score = hit.Semantic
				out.Target = hit.Unit.SID
				return out, nil
			}
		}
	}
	return out, nil
}

func (b Binding) captureApply(ctx context.Context, a *App, scope string, action CaptureAction, receipt string) error {
	b = b.Refresh(a)
	// Validate again immediately before mutation. Never trust a model-provided sid.
	if project.LooksSecret(action.Content) {
		return errors.New("secret candidate refused")
	}
	mems, err := ingest.LoadNative(NativePath())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if action.Action == "reinforce" {
		u, err := b.Resolve(a, action.Target)
		if err != nil {
			return err
		}
		if _, err = b.WriteScope(u.Scope); err != nil {
			return err
		}
		for i := range mems {
			m := &mems[i]
			if m.Active && m.SID == u.SID {
				if slices.Contains(m.CaptureEvents, receipt) {
					return nil
				}
				m.Strength++
				m.Updated = now
				m.CaptureEvents = append(m.CaptureEvents, receipt)
				if err := ingest.SaveNative(NativePath(), mems); err != nil {
					return err
				}
				_, err = a.Reindex(ctx)
				return err
			}
		}
		// Upstream stores are read-only. Persist reinforcement evidence in muninn,
		// instead of modifying jcode's graph or creating a duplicate native copy.
		_, err = a.DB.ExecContext(ctx, `INSERT OR IGNORE INTO capture_reinforcements(event,sid,at_ns) VALUES(?,?,?)`, receipt, u.SID, now.UnixNano())
		return err
	}
	for _, m := range mems {
		if slices.Contains(m.CaptureEvents, receipt) {
			if action.Action == "supersede" && action.Trust == "high" {
				_, err = b.Forget(ctx, a, action.Target)
				return err
			}
			return nil
		}
	}
	h := captureHash(action.Origin + "\x00" + scope)
	mems = append(mems, ingest.NativeMemory{ID: "cap_" + h[:16], Origin: action.Origin, Content: action.Content, Category: action.Category, Trust: action.Trust, Scope: scope, Active: true, Strength: 1, Created: now, Updated: now, CaptureEvents: []string{receipt}})
	if err := ingest.SaveNative(NativePath(), mems); err != nil {
		return err
	}
	if _, err := a.Reindex(ctx); err != nil {
		return err
	}
	if action.Action == "supersede" && action.Trust == "high" {
		_, err = b.Forget(ctx, a, action.Target)
		return err
	}
	return nil
}
