package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTidyNeverExposedThroughMCP(t *testing.T) {
	a, _ := testApp(t)
	cs := connect(t, a, Options{Scopes: []string{"*"}, DefaultScope: "global"})
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		n := strings.ToLower(tool.Name)
		if strings.Contains(n, "tidy") || strings.Contains(n, "rescope") {
			t.Fatalf("owner-only action exposed as %s", tool.Name)
		}
	}
	for _, name := range []string{"tidy", "tidy_scan", "tidy_apply", "memory_tidy", "memory_rescope"} {
		result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("owner action %s reachable through MCP", name)
		}
	}
}
