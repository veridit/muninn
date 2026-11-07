package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/extract"
	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/project"
	"github.com/veridit/muninn/internal/scopetest"
)

func makePlacementRepo(t *testing.T, root string) project.Project {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-q", "--allow-empty", "-m", "synthetic root " + filepath.Base(root)}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	p, err := project.Find(root, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlacesSessionInventory(t *testing.T) {
	f := scopetest.New(t)
	home, _ := os.UserHomeDir()
	globalHome := filepath.Join(home, ".jcode")
	plain := filepath.Join(f.Root, "plain-folder")
	os.MkdirAll(plain, 0700)
	workspace := filepath.Join(f.Root, "workspace")
	p := makePlacementRepo(t, filepath.Join(workspace, "synthetic-repo"))
	makePlacementRepo(t, filepath.Join(workspace, "another-repo"))
	cp := makePlacementRepo(t, filepath.Join(f.Root, "client", "synthetic-client-repo"))
	sub := filepath.Join(p.Root, "nested")
	os.MkdirAll(sub, 0700)
	tests := []struct {
		name, cwd, scope string
		count            int
	}{
		{"plain", plain, "folder:" + plain, 2},
		{"git", p.Root, p.Scope(), 2},
		{"git subfolder", sub, p.Scope(), 2},
		{"workspace parent", workspace, "folder:" + workspace, 2},
		{"home", home, "folder:" + home, 2},
		{"client root", filepath.Join(f.Root, "client"), "client:acme", 1},
		{"client project", cp.Root, cp.Scope(), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := f.App.Bound(globalHome, tc.cwd)
			places := b.Places()
			if len(places) != tc.count || places[0].Name != "here" || places[0].Scope != tc.scope {
				t.Fatalf("places=%+v want %s", places, tc.scope)
			}
			seen := map[string]bool{}
			for _, place := range places {
				if seen[place.Scope] {
					t.Fatal("duplicate destination")
				}
				seen[place.Scope] = true
				if b.Client != "" && !strings.HasPrefix(place.Scope, "client:acme") {
					t.Fatal("personal global leaked", places)
				}
			}
			for _, name := range []string{"", "here", "unsure", "bogus", "client:other", "jcode@global"} {
				got, err := b.Placement("", name)
				if err != nil || got.Scope != tc.scope {
					t.Fatalf("%s: %+v %v", name, got, err)
				}
			}
			got, err := b.Placement("", "global")
			wantGlobal := "jcode@global"
			if b.Client != "" {
				wantGlobal = "client:acme"
			}
			if err != nil || got.Scope != wantGlobal {
				t.Fatalf("global: %+v %v", got, err)
			}
		})
	}
	named := f.App.Bound(filepath.Join(f.Root, "alpha", ".jcode"), plain)
	if got := named.Places()[0].Scope; got != "jcode@alpha:"+harness.JcodeProjectHash(plain) {
		t.Fatal(got)
	}
	// No identity was created for a plain folder or workspace parent.
	for _, root := range []string{plain, workspace} {
		if _, err := os.Stat(filepath.Join(root, ".muninn", "project")); !os.IsNotExist(err) {
			t.Fatal("placement created a marker", err)
		}
	}
}

func TestClientPlacesDoNotLeakSiblingMemories(t *testing.T) {
	f := scopetest.New(t)
	root := filepath.Join(f.Root, "client")
	pA := makePlacementRepo(t, filepath.Join(root, "synthetic-project-a"))
	pB := makePlacementRepo(t, filepath.Join(root, "synthetic-project-b"))
	if pA.Scope() == pB.Scope() {
		t.Fatal("synthetic projects must have distinct identities")
	}
	folder := filepath.Join(root, "synthetic-folder")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(f.Root, "alpha", ".jcode")
	a := f.App.Bound(home, pA.Root)
	b := f.App.Bound(home, pB.Root)
	plain := f.App.Bound(home, folder)
	top := f.App.Bound(home, root)
	ctx := context.Background()
	var units []struct{ id, scope string }
	for i, binding := range []app.Binding{a, b, plain} {
		u, _, err := binding.RememberSafe(ctx, f.App, "Synthetic isolated client memory "+string(rune('a'+i)), "fact", "", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, struct{ id, scope string }{u.SID, u.Scope})
	}
	physicalFolder, _ := filepath.EvalSymlinks(folder)
	if units[2].scope != "client:acme:folder:"+physicalFolder {
		t.Fatal(units[2])
	}
	for i, binding := range []app.Binding{a, b, plain, top} {
		for j, u := range units {
			_, err := binding.Resolve(f.App, u.id)
			if (err == nil) != (i == j) {
				t.Fatalf("binding %d reading %d: %v", i, j, err)
			}
			_, err = binding.WriteScope(u.scope)
			if (err == nil) != (i == j) {
				t.Fatalf("binding %d writing %d: %v", i, j, err)
			}
		}
		if !binding.Allows("client:acme") || binding.Allows("jcode@global") {
			t.Fatal("top-level boundary", binding)
		}
		where, args := binding.SQL("u.scope", "u.source_id")
		query := `SELECT count(*) FROM units u WHERE (` + where + `) AND body LIKE 'Synthetic isolated client memory %'`
		var count int
		if err := f.App.DB.QueryRow(query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 3 {
			want = 0
		}
		if count != want {
			t.Fatalf("SQL binding %d count=%d want=%d", i, count, want)
		}
	}
	if got := top.Places(); len(got) != 1 || got[0].Scope != "client:acme" {
		t.Fatal(got)
	}
	child := filepath.Join(folder, "nested")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if !f.App.Bound(home, child).Allows(units[2].scope) {
		t.Fatal("owned ancestor folder hidden")
	}
	alias := filepath.Join(f.Root, "client-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	future := f.App.Bound(home, filepath.Join(alias, "future", "nested"))
	if future.Err != nil || future.Places()[0].Scope != "client:acme:folder:"+filepath.Join(root, "future", "nested") {
		t.Fatalf("future symlink: %+v %+v", future, future.Places())
	}
	if _, err := future.WriteScope("client:acme:folder:" + f.Root); err == nil {
		t.Fatal("future path escaped client")
	}
}

func TestRememberPlacementDefaultPrecedenceAndQuestion(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	ctx := context.Background()
	u, _, err := b.RememberSafe(ctx, f.App, "Synthetic durable local fact.", "fact", "", nil, "")
	if err != nil || u.Scope != "project:matrix-project" {
		t.Fatalf("default: %+v %v", u, err)
	}
	u, _, err = b.RememberPlaced(ctx, f.App, "Synthetic portable preference.", "preference", "jcode@global", nil, "", "here", "")
	if err != nil || u.Scope != "jcode@global" {
		t.Fatalf("literal precedence: %+v %v", u, err)
	}
	for _, text := range []string{"The work repository uses a synthetic release checklist.", "Synthetic instructions for " + filepath.Join(f.Cwd, "docs", "release.md")} {
		before, _ := os.ReadFile(app.NativePath())
		_, _, err = b.RememberPlaced(ctx, f.App, text, "fact", "", nil, "", "global", "")
		var question *app.PlaceCheckError
		if !errors.As(err, &question) || !strings.Contains(err.Error(), "Are you sure it applies beyond it?") {
			t.Fatalf("question: %v", err)
		}
		after, _ := os.ReadFile(app.NativePath())
		if string(before) != string(after) {
			t.Fatal("question stored text")
		}
		u, _, err = b.RememberPlaced(ctx, f.App, text, "fact", "", nil, "", "global", "Synthetic example applies to every repository.")
		if err != nil || u.Scope != "jcode@global" {
			t.Fatalf("recall: %+v %v", u, err)
		}
	}
	client := f.Bind(t, "client")
	_, _, err = client.RememberPlaced(ctx, f.App, "Synthetic client fact.", "fact", "jcode@global", nil, "", "global", "synthetic explanation")
	if err == nil {
		t.Fatal("literal precedence bypassed client boundary")
	}
	_, _, err = b.RememberPlaced(ctx, f.App, "sk-"+strings.Repeat("z", 30), "fact", "", nil, "", "global", "Placement reason is not a secret reason.")
	var secret *app.SecretCheckError
	if !errors.As(err, &secret) {
		t.Fatalf("secret gate bypassed: %v", err)
	}
}

func TestCaptureRetryKeepsPersistedPlace(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	candidate := extract.Candidate{Category: "decision", Trust: "high", Content: "Synthetic portable capture rule.", Place: "global", PlaceReason: "Synthetic cross-project preference."}
	raw, _ := json.Marshal(candidate)
	model := fakeCapture(t, f, string(raw))
	if _, err := f.App.DB.Exec(`CREATE TRIGGER interrupt_place_checkpoint BEFORE INSERT ON capture_seen BEGIN SELECT RAISE(FAIL,'synthetic interruption'); END;`); err != nil {
		t.Fatal(err)
	}
	opts := captureOpts("synthetic-placement-retry")
	if _, err := b.Capture(context.Background(), f.App, opts); err == nil {
		t.Fatal("checkpoint not interrupted")
	}
	if _, err := f.App.DB.Exec(`DROP TRIGGER interrupt_place_checkpoint`); err != nil {
		t.Fatal(err)
	}
	candidate.Place = "here"
	raw, _ = json.Marshal(candidate)
	model.reply = string(raw)
	report, err := b.Capture(context.Background(), f.App, opts)
	if err != nil || len(report.Actions) != 1 || report.Actions[0].Action != "skip" || report.Actions[0].Scope != "jcode@global" || report.Actions[0].Place != "global" {
		t.Fatalf("retry: %+v %v", report, err)
	}
	mems, err := ingest.LoadNative(app.NativePath())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range mems {
		if m.Content == candidate.Content {
			count++
			if m.Scope != "jcode@global" || m.Strength != 1 {
				t.Fatalf("changed committed memory: %+v", m)
			}
		}
	}
	if count != 1 {
		t.Fatalf("copies=%d", count)
	}
}

func TestCapturePlacementMappingAndAudit(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	var lines []string
	for i, name := range []string{"here", "global", "unsure", "invalid", "", "client"} {
		c := extract.Candidate{Content: "Synthetic work placement fact " + string(rune('a'+i)), Category: "fact", Trust: "high", Place: name, PlaceReason: "Synthetic explicit placement explanation."}
		raw, _ := json.Marshal(c)
		lines = append(lines, string(raw))
	}
	fakeCapture(t, f, strings.Join(lines, "\n"))
	r, err := b.Capture(context.Background(), f.App, captureOpts("synthetic-placement"))
	if err != nil || len(r.Actions) != 6 {
		t.Fatalf("%+v %v", r, err)
	}
	for i, a := range r.Actions {
		want := "project:matrix-project"
		name := "here"
		if i == 1 {
			want = "jcode@global"
			name = "global"
			if a.PlaceHint != "work" {
				t.Fatalf("hint missing: %+v", a)
			}
		}
		if a.Scope != want || a.Place != name || a.PlaceReason == "" {
			t.Fatalf("placement: %+v", a)
		}
	}
	// The model receives meanings, never concrete destination scope strings.
	mems, _ := ingest.LoadNative(app.NativePath())
	for _, a := range r.Actions {
		found := false
		for _, v := range mems {
			if v.Content == a.Content {
				found = true
				if v.Scope != a.Scope {
					t.Fatal("mapped scope lost")
				}
			}
		}
		if !found {
			t.Fatal("candidate not stored")
		}
	}
	runs, err := f.App.AuditRuns("synthetic-placement", time.Time{})
	if err != nil || len(runs) != 1 || runs[0].Report.Actions[1].PlaceHint != "work" {
		t.Fatalf("audit: %+v %v", runs, err)
	}
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(runs[0].Calls[0].Request, &request); err != nil {
		t.Fatal(err)
	}
	system := request.Messages[0].Content
	if !strings.Contains(system, "Would this still be true and useful in a different project?") || strings.Contains(system, "project:matrix-project") || strings.Contains(system, "jcode@global") {
		t.Fatal("model placement prompt missing or leaked scope")
	}
}
