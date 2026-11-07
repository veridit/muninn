package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/scopetest"
)

func TestScopeMatrixMCP(t *testing.T) {
	for _, binding := range []string{"alpha", "global", "client"} {
		t.Run(binding, func(t *testing.T) {
			f := scopetest.New(t)
			b := f.Bind(t, binding)
			cwd := f.Cwd
			if binding == "client" {
				cwd = f.ClientCwd
			}
			t.Chdir(cwd)
			// Zero options must derive the binding too, not silently become unrestricted.
			cs := connect(t, f.App, Options{})
			for _, tc := range []struct {
				tool string
				args map[string]any
			}{
				{"memory_search", map[string]any{"query": scopetest.Query, "limit": 30}},
				{"memory_recall", map[string]any{"message": scopetest.Query}},
			} {
				out, isErr := call(t, cs, tc.tool, tc.args)
				if isErr {
					t.Fatal(out)
				}
				f.Read(t, binding, out)
				scopetest.Pass(t, "mcp."+tc.tool, binding)
			}
			visible := f.Units["Alpha"]
			if binding == "global" {
				visible = f.Units["Personal"]
			}
			if binding == "client" {
				visible = f.Units["Client"]
			}
			for _, ref := range []string{visible.SID, visible.SID[:4], visible.Key} {
				out, isErr := call(t, cs, "memory_related", map[string]any{"id": ref, "limit": 30})
				if isErr {
					t.Fatal(out)
				}
				f.Clean(t, binding, out)
				if !strings.Contains(out, "quartz deployment") {
					t.Fatal("empty related")
				}
			}
			for _, ref := range f.HiddenRefs(binding) {
				for _, tool := range []string{"memory_related", "memory_forget"} {
					out, isErr := call(t, cs, tool, map[string]any{"id": ref})
					if !isErr || out != "no visible memory" {
						t.Fatalf("%s leaked: %s", tool, out)
					}
					f.Clean(t, binding, out)
				}
			}
			scopetest.Pass(t, "mcp.memory_related", binding)
			out, isErr := call(t, cs, "memory_stats", map[string]any{})
			if isErr {
				t.Fatal(out)
			}
			f.CheckStats(t, binding, out)
			f.Clean(t, binding, out)
			if strings.Contains(out, "jcode@beta") || strings.Contains(out, "client:other") {
				t.Fatal(out)
			}
			if binding == "global" && (!strings.Contains(out, "engram:frogs") || !strings.Contains(out, "sql_saga") || !strings.Contains(out, "personal")) {
				t.Fatal(out)
			}
			scopetest.Pass(t, "mcp.memory_stats", binding)
			out, isErr = call(t, cs, "memory_remember", map[string]any{"content": "matrix MCP durable preference"})
			if isErr {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
			var remembered idOut
			if err := json.Unmarshal([]byte(out), &remembered); err != nil {
				t.Fatal(err)
			}
			u, err := f.App.DB.Resolve(remembered.ID)
			if err != nil || u.Scope != b.Places()[0].Scope {
				t.Fatalf("default: %+v %v", u, err)
			}
			for _, scope := range []string{"jcode@beta", "client:other"} {
				out, isErr = call(t, cs, "memory_remember", map[string]any{"content": "must not be stored", "scope": scope})
				if !isErr {
					t.Fatal("cross-binding write")
				}
				f.Clean(t, binding, out)
			}
			scopetest.Pass(t, "mcp.memory_remember", binding)
			out, isErr = call(t, cs, "memory_forget", map[string]any{"id": visible.SID[:4]})
			if isErr {
				t.Fatal(out)
			}
			f.Clean(t, binding, out)
			if u, err := f.App.DB.Resolve(f.Units["Beta"].SID); err != nil || !u.Active {
				t.Fatal("beta changed")
			}
			scopetest.Pass(t, "mcp.memory_forget", binding)
		})
	}
}

func TestScopeMatrixInvalidClientMCP(t *testing.T) {
	f := scopetest.New(t)
	f.Invalidate(t)
	cs := connect(t, f.App, Options{})
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"memory_search", map[string]any{"query": scopetest.Query}},
		{"memory_recall", map[string]any{"message": scopetest.Query}},
		{"memory_stats", map[string]any{}},
		{"memory_related", map[string]any{"id": f.Units["Shared"].SID}},
		{"memory_forget", map[string]any{"id": f.Units["Shared"].SID}},
		{"memory_remember", map[string]any{"content": "not stored"}},
	} {
		out, isErr := call(t, cs, tc.tool, tc.args)
		if !isErr || !strings.Contains(out, "invalid client name") {
			t.Fatalf("%s failed open: %s", tc.tool, out)
		}
		f.Clean(t, "client", out)
	}
	scopetest.Pass(t, "mcp.fail-closed", "invalid-client")
}
