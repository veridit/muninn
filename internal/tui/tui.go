// Package tui is an interactive terminal browser for muninn.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/veridit/muninn/internal/app"
	"github.com/veridit/muninn/internal/ingest"
	"github.com/veridit/muninn/internal/recall"
	"github.com/veridit/muninn/internal/search"
)

type mode int

const (
	modeSearch mode = iota
	modeRecall
)

type model struct {
	a       *app.App
	input   textinput.Model
	mode    mode
	results []search.Result
	cursor  int
	explain bool
	status  string
	width   int
	height  int
	block   string
}

type resultsMsg struct {
	res     []search.Result
	block   string
	elapsed time.Duration
	err     error
}

// Run starts the TUI.
func Run(a *app.App) error {
	in := textinput.New()
	in.Placeholder = "search memories…"
	in.Focus()
	in.Prompt = "› "
	m := model{a: a, input: in, status: "tab: search/recall · ctrl+e: explain · ↑↓ move · ctrl+r: related · ctrl+d: forget · esc: quit"}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) run() tea.Cmd {
	q, md := m.input.Value(), m.mode
	return func() tea.Msg {
		t0 := time.Now()
		ctx := context.Background()
		if strings.TrimSpace(q) == "" {
			return resultsMsg{}
		}
		if md == modeRecall {
			r, err := m.a.Recall.Recall(ctx, recall.Request{Message: q, Harness: "tui"})
			return resultsMsg{block: r.Block, elapsed: time.Since(t0), err: err}
		}
		o := m.a.RecallOptions()
		o.MinScore, o.Limit = 0, 30
		res, err := m.a.Searcher.Search(ctx, q, o)
		return resultsMsg{res: res, elapsed: time.Since(t0), err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 4
	case resultsMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			break
		}
		m.results, m.block, m.cursor = msg.res, msg.block, 0
		m.status = fmt.Sprintf("%d results in %s", len(msg.res), msg.elapsed.Round(time.Millisecond))
		if m.mode == modeRecall {
			m.status = "recall preview in " + msg.elapsed.Round(time.Millisecond).String()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.mode = 1 - m.mode
			m.input.Placeholder = map[mode]string{modeSearch: "search memories…", modeRecall: "type a message to preview automatic recall…"}[m.mode]
			return m, m.run()
		case "ctrl+e":
			m.explain = !m.explain
			return m, nil
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.results)-1 {
				m.cursor++
			}
			return m, nil
		case "ctrl+r":
			if m.cursor < len(m.results) {
				id := m.results[m.cursor].Unit.ID
				res, err := m.a.Searcher.Related(context.Background(), id, 15)
				if err == nil {
					m.results, m.cursor = res, 0
					m.status = fmt.Sprintf("neighbours of #%d", id)
				}
			}
			return m, nil
		case "ctrl+d":
			if m.cursor < len(m.results) {
				u := m.results[m.cursor].Unit
				if ok, err := ingest.Forget(app.NativePath(), u.Key); err == nil && ok {
					m.a.Reindex(context.Background())
					m.status = "forgot " + u.Key
					return m, m.run()
				}
				m.status = "only muninn memories can be forgotten here"
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		return m, tea.Batch(cmd, m.run())
	}
	return m, cmd
}

func (m model) View() string {
	var b strings.Builder
	title := "muninn · search"
	if m.mode == modeRecall {
		title = "muninn · recall preview"
	}
	b.WriteString("\x1b[1m" + title + "\x1b[0m\n\n" + m.input.View() + "\n\n")
	if m.mode == modeRecall {
		if m.block == "" {
			b.WriteString("  (nothing passes the recall threshold)\n")
		} else {
			b.WriteString(m.block + "\n")
		}
	} else {
		rows := max(3, m.height-8)
		start := 0
		if m.cursor >= rows {
			start = m.cursor - rows + 1
		}
		for i := start; i < len(m.results) && i < start+rows; i++ {
			r := m.results[i]
			line := fmt.Sprintf("%6.2f  %s", r.Total, strings.Join(strings.Fields(r.Unit.Body), " "))
			if m.explain {
				line += "  \x1b[2m" + r.Explain() + "\x1b[0m"
			}
			if w := m.width - 4; w > 10 && len(line) > w {
				line = recall.Truncate(line, w)
			}
			if i == m.cursor {
				b.WriteString("\x1b[7m› " + line + "\x1b[0m\n")
			} else {
				b.WriteString("  " + line + "\n")
			}
		}
	}
	b.WriteString("\n\x1b[2m" + m.status + "\x1b[0m")
	return b.String()
}
