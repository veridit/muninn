package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJcodeTakeoverRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfg := "[features]\nmemory = true\nswarm = true\n\n[tools]\nprofile = \"\"\ndisabled = [\"browser\"]\n\n[hooks]\npost_tool_feedback = \"echo keep-me\"\n"
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600)
	other := `{"servers":{"vikunja":{"type":"http","url":"https://example.invalid/mcp"}}}`
	os.WriteFile(filepath.Join(home, "mcp.json"), []byte(other), 0o600)

	r, err := InstallJcodeHome(home, "jcode[t]", "/bin/muninn", true, false)
	if err != nil || !r.Changed {
		t.Fatalf("%v %v", r, err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	got := string(b)
	for _, want := range []string{"memory = false", `disabled = ["browser","memory"]`, `post_tool_feedback = "echo keep-me"`, `turn_start = "/bin/muninn hook jcode"`, "swarm = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
	var m struct {
		Servers map[string]map[string]any `json:"servers"`
	}
	raw, _ := os.ReadFile(filepath.Join(home, "mcp.json"))
	json.Unmarshal(raw, &m)
	if m.Servers["vikunja"]["url"] != "https://example.invalid/mcp" || m.Servers["muninn"]["command"] != "/bin/muninn" {
		t.Fatalf("mcp.json: %s", raw)
	}
	if fi, _ := os.Stat(filepath.Join(home, "mcp.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.json mode changed to %v", fi.Mode().Perm())
	}
	if r, _ := InstallJcodeHome(home, "jcode[t]", "/bin/muninn", true, false); r.Changed {
		t.Fatalf("second install must be a no-op: %v", r)
	}
	if _, err := InstallJcodeHome(home, "jcode[t]", "/bin/muninn", false, false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(home, "config.toml"))
	raw, _ = os.ReadFile(filepath.Join(home, "mcp.json"))
	if !strings.Contains(string(b), "memory = true") || !strings.Contains(string(b), `disabled = ["browser"]`) || strings.Contains(string(raw), "muninn") || !strings.Contains(string(raw), "vikunja") {
		t.Fatalf("uninstall did not restore:\n%s\n%s", b, raw)
	}
}
