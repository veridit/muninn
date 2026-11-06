package app

import (
	"encoding/json"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/store"
	"time"
)

// AuditRun lives only in the local index database, never in native sync files.
// SQLite provides atomic append/prune across concurrent detached observers.
// OpenAudit opens local persistence without loading or probing any model.
func OpenAudit() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(config.DBPath())
	if err != nil {
		return nil, err
	}
	return &App{Cfg: cfg, DB: db}, nil
}

type AuditRun struct {
	ID         int64            `json:"id"`
	Time       time.Time        `json:"time"`
	Harness    string           `json:"harness"`
	Session    string           `json:"session"`
	Home       string           `json:"home"`
	Scope      string           `json:"scope"`
	Trigger    string           `json:"trigger"`
	DryRun     bool             `json:"dry_run"`
	Model      string           `json:"model"`
	Messages   []AuditMessage   `json:"messages"`
	Calls      []extract.Result `json:"calls"`
	Report     CaptureReport    `json:"report"`
	ErrorClass string           `json:"error_class,omitempty"`
	Backfill   bool             `json:"backfill"`
	Error      string           `json:"error,omitempty"`
}
type AuditMessage struct {
	OriginalID string `json:"original_id,omitempty"`
	ID         string `json:"id"`
	Role       string `json:"role"`
	Hash       string `json:"hash"`
	Text       string `json:"text"`
}

func auditSafe(s string) string {
	if project.LooksSecret(s) {
		return "[withheld: secret]"
	}
	return s
}
func (a *App) auditSchema() error {
	_, err := a.DB.Exec(`CREATE TABLE IF NOT EXISTS capture_runs(id INTEGER PRIMARY KEY, at_ns INTEGER NOT NULL, session TEXT NOT NULL, record TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS capture_runs_time ON capture_runs(at_ns);
CREATE TABLE IF NOT EXISTS capture_actions(run_id INTEGER NOT NULL, position INTEGER NOT NULL, record TEXT NOT NULL, PRIMARY KEY(run_id,position));`)
	return err
}
func (a *App) SaveAudit(r AuditRun) error {
	if err := a.auditSchema(); err != nil {
		return err
	}
	// Defense in depth: provider output is untrusted and may echo credentials.
	for i := range r.Calls {
		r.Calls[i].Raw = auditSafe(r.Calls[i].Raw)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO capture_runs(at_ns,session,record) VALUES(?,?,?)`, r.Time.UnixNano(), r.Session, string(data))
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for i, action := range r.Report.Actions {
		data, err := json.Marshal(action)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO capture_actions(run_id,position,record) VALUES(?,?,?)`, id, i, string(data)); err != nil {
			return err
		}
	}
	days := a.Cfg.Extract.AuditDays
	if days <= 0 {
		days = 30
	}
	if _, err = tx.Exec(`DELETE FROM capture_runs WHERE at_ns<?`, time.Now().Add(-time.Duration(days)*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM capture_actions WHERE run_id NOT IN (SELECT id FROM capture_runs)`); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) AuditRuns(session string, since time.Time) ([]AuditRun, error) {
	if err := a.auditSchema(); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(`SELECT id,record FROM capture_runs WHERE at_ns>=? AND (?='' OR session=?) ORDER BY id DESC`, since.UnixNano(), session, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRun{}
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var r AuditRun
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		r.ID = id
		out = append(out, r)
	}
	return out, rows.Err()
}
