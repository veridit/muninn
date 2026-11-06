package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/tidy"
)

func reviewedPlan(units ...store.Unit) tidy.Plan {
	p := tidy.Plan{Version: 1, Database: config.DBPath()}
	for _, u := range units {
		p.Entries = append(p.Entries, tidy.Entry{SID: u.SID, Origin: u.Origin, Fingerprint: tidy.Fingerprint(u), Source: u.Scope, Target: "client:other", Decision: "move"})
	}
	return p
}
func TestTidyApplyNativeUpstreamAndUndo(t *testing.T) {
	f := scopetest.New(t)
	ctx := context.Background()
	native := f.Units["Shared"]
	up := f.Units["OutsideNote"]
	// Whitespace and dated text must survive import exactly.
	up.Body = "  2025-01-02: historical note\nkeep trailing whitespace  \n"
	id, err := f.App.DB.UpsertUnit(up)
	if err != nil {
		t.Fatal(err)
	}
	up, _ = f.App.DB.GetUnit(id)
	raw, err := os.ReadFile(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	p := reviewedPlan(native, up)
	before, err := os.ReadDir(config.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	var changes int
	f.App.DB.QueryRow(`SELECT total_changes()`).Scan(&changes)
	result, err := f.App.ApplyTidy(ctx, p, "", true)
	if err != nil || len(result.Moves) != 2 || result.Backup != "" {
		t.Fatalf("dry=%+v err=%v", result, err)
	}
	var afterChanges int
	f.App.DB.QueryRow(`SELECT total_changes()`).Scan(&afterChanges)
	if changes != afterChanges {
		t.Fatal("dry run wrote database")
	}
	after, _ := os.ReadDir(config.DataDir())
	now, _ := os.ReadFile(app.NativePath())
	if len(before) != len(after) || !bytes.Equal(raw, now) {
		t.Fatal("dry run wrote files")
	}
	result, err = f.App.ApplyTidy(ctx, p, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moves) != 2 {
		t.Fatalf("moves=%+v", result)
	}
	backup, err := os.ReadFile(filepath.Join(result.Backup, "memories.json"))
	if err != nil || !bytes.Equal(backup, raw) {
		t.Fatal("backup not byte exact")
	}
	if fi, err := os.Stat(filepath.Join(result.Backup, "receipt.json")); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("missing private receipt")
	}
	n, err := f.App.DB.Resolve(native.SID)
	if err != nil || n.Scope != "client:other" || n.Body != native.Body || n.SID != native.SID {
		t.Fatalf("native=%+v err=%v", n, err)
	}
	copy, err := f.App.DB.Resolve(result.Moves[1].Ref)
	if err != nil || copy.Scope != "client:other" || copy.Body != up.Body || copy.SID == up.SID || !copy.Active {
		t.Fatalf("copy=%+v err=%v", copy, err)
	}
	if !f.App.DB.IsSuppressed(up.SID) || f.App.DB.IsSuppressed(copy.SID) {
		t.Fatal("wrong identity suppressed")
	}
	b := f.App.Bound("", f.Cwd)
	for i, u := range []store.Unit{native, up} {
		if _, err = b.Rescope(ctx, f.App, result.Moves[i].Ref, u.Scope, true); err != nil {
			t.Fatal(err)
		}
		restored, err := f.App.DB.Resolve(result.Moves[i].Ref)
		if err != nil || restored.Scope != u.Scope || restored.Body != u.Body {
			t.Fatal("rescope undo failed")
		}
	}
	if !f.App.DB.IsSuppressed(up.SID) {
		t.Fatal("undo unexpectedly revived upstream")
	}
}
func TestTidyApplyOnlyStaleAndNoMoves(t *testing.T) {
	f := scopetest.New(t)
	ctx := context.Background()
	p := reviewedPlan(f.Units["Shared"], f.Units["Personal"])
	result, err := f.App.ApplyTidy(ctx, p, "global->client:other", false)
	if err != nil || len(result.Moves) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	personal, _ := f.App.DB.Resolve(f.Units["Personal"].SID)
	if personal.Scope != "personal" {
		t.Fatal("--only moved wrong source")
	}
	if _, err = f.App.ApplyTidy(ctx, p, "", false); err == nil {
		t.Fatal("stale plan accepted")
	}
	p.Entries[0].Decision = "stay"
	p.Entries[1].Decision = "unsure"
	result, err = f.App.ApplyTidy(ctx, p, "", false)
	if err != nil || len(result.Moves) != 0 || result.Backup != "" {
		t.Fatal("non-moves applied")
	}
}
func TestTidyPreflightDetectsUnindexedNativeChanges(t *testing.T) {
	f := scopetest.New(t)
	p := reviewedPlan(f.Units["Shared"])
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	for i := range mems {
		if mems[i].ID == f.Units["Shared"].Key {
			mems[i].Content = "changed since indexing"
		}
	}
	if err = ingest.SaveNative(app.NativePath(), mems); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(app.NativePath())
	if _, err = f.App.ApplyTidy(context.Background(), p, "", false); err == nil {
		t.Fatal("unindexed edit accepted")
	}
	after, _ := os.ReadFile(app.NativePath())
	if !bytes.Equal(before, after) {
		t.Fatal("preflight modified edited memory")
	}
}
