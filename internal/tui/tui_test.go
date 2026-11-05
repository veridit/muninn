package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/config"
	"github.com/veridit/muninn/internal/index"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
	"github.com/veridit/muninn/internal/store"
	"github.com/veridit/muninn/internal/testutil"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	t.Setenv("MUNINN_HOME", t.TempDir())
	db, err := store.Open(config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, c := range []string{"Bake banana cake at 175 degrees.", "Deploys go through staging first.", "Use ripgrep for searches."} {
		if _, _, err := ingest.Remember(app.NativePath(), c, "fact", "global", nil); err != nil {
			t.Fatal(err)
		}
	}
	fe := &testutil.FakeEmbedder{}
	cfg := config.Defaults()
	cfg.JcodeMemoryDir = ""
	a := &app.App{Cfg: cfg, DB: db, Embedder: fe, Indexer: &index.Indexer{DB: db, Embedder: fe}}
	a.Searcher = &search.Searcher{DB: db, Embedder: fe}
	a.Recall = &recall.Engine{DB: db, Searcher: a.Searcher, Options: a.RecallOptions()}
	if _, err := a.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}

// drive feeds a message and synchronously resolves the commands it returns
// (search runs in a tea.Cmd), the way the bubbletea runtime would.
func drive(t *testing.T, m tea.Model, msg tea.Msg) tea.Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for i := 0; len(queue) > 0 && i < 50; i++ {
		next := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(next)
		queue = append(queue, resolve(cmd)...)
	}
	return m
}

func resolve(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, resolve(c)...)
		}
		return out
	case resultsMsg:
		return []tea.Msg{msg}
	default:
		return nil // blink ticks etc.
	}
}

func typeText(t *testing.T, m tea.Model, s string) tea.Model {
	for _, r := range s {
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestTUISearchRecallExplainAndForget(t *testing.T) {
	a := testApp(t)
	m := newModel(a)
	var tm tea.Model = m
	tm = drive(t, tm, tea.WindowSizeMsg{Width: 120, Height: 30})
	tm = typeText(t, tm, "banana")
	v := tm.View()
	if !strings.Contains(v, "Bake banana cake") || !strings.Contains(v, "results in") {
		t.Fatalf("search view:\n%s", v)
	}
	tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyCtrlE})
	if !strings.Contains(tm.View(), "keyword=") {
		t.Fatalf("explain not shown:\n%s", tm.View())
	}
	// Recall preview mode.
	tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(tm.View(), "recall preview") {
		t.Fatalf("recall mode:\n%s", tm.View())
	}
	// Back to search, forget the selected muninn memory.
	tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyTab})
	tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyCtrlD})
	if !strings.Contains(tm.View(), "forgot ") {
		t.Fatalf("forget:\n%s", tm.View())
	}
	if strings.Contains(tm.View(), "Bake banana cake") {
		t.Fatalf("forgotten memory still listed:\n%s", tm.View())
	}
	// Esc quits.
	_, cmd := tm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("esc did not quit")
	}
}
