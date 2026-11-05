package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/veridit/muninn/internal/scopetest"
	"github.com/veridit/muninn/internal/search"
)

func TestScopeMatrixTUI(t *testing.T) {
	for _, binding := range []string{"alpha", "global", "client"} {
		t.Run(binding, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, binding)
			cwd := f.Cwd
			if binding == "client" {
				cwd = f.ClientCwd
			}
			t.Chdir(cwd)
			m := newModel(f.App)
			m.width = 400
			m.height = 40
			m.input.SetValue(scopetest.Query)
			var tm tea.Model = drive(t, m, m.run()())
			f.Read(t, binding, tm.View())
			if !strings.Contains(tm.View(), b.Name) {
				t.Fatal("binding missing from status")
			}
			scopetest.Pass(t, "tui.search", binding)
			tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyTab})
			f.Read(t, binding, tm.View())
			scopetest.Pass(t, "tui.recall", binding)
			tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyTab})
			tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyCtrlR})
			f.Clean(t, binding, tm.View())
			if !strings.Contains(tm.View(), "quartz deployment") {
				t.Fatal("empty related")
			}
			scopetest.Pass(t, "tui.related", binding)
			// A stale selection or injected id cannot use either action to bypass binding.
			forged := tm.(model)
			forged.results = []search.Result{{Unit: f.Units["Beta"]}}
			forged.cursor = 0
			tm = drive(t, forged, tea.KeyMsg{Type: tea.KeyCtrlD})
			if u, err := f.App.DB.Resolve(f.Units["Beta"].SID); err != nil || !u.Active {
				t.Fatal("beta forgotten")
			}
			// Forget a legitimate selection through the real key handler.
			m = newModel(f.App)
			m.width = 400
			m.height = 40
			m.input.SetValue(scopetest.Query)
			tm = drive(t, m, m.run()())
			selected := tm.(model).results[0].Unit
			tm = drive(t, tm, tea.KeyMsg{Type: tea.KeyCtrlD})
			f.Clean(t, binding, tm.View())
			if !strings.Contains(tm.View(), "forgot ") {
				t.Fatal(tm.View())
			}
			if u, err := f.App.DB.Resolve(selected.SID); err != nil || u.Active {
				t.Fatal("visible row not forgotten")
			}
			scopetest.Pass(t, "tui.forget", binding)
		})
	}
}

func TestScopeMatrixInvalidClientTUI(t *testing.T) {
	f := scopetest.New(t)
	f.Invalidate(t)
	m := newModel(f.App)
	m.input.SetValue(scopetest.Query)
	var tm tea.Model = drive(t, m, m.run()())
	if !strings.Contains(tm.View(), "invalid client name") {
		t.Fatal(tm.View())
	}
	f.Clean(t, "client", tm.View())
	scopetest.Pass(t, "tui.fail-closed", "invalid-client")
}
