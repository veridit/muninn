package mcpserver

import (
	"encoding/json"
	"github.com/veridit/muninn/internal/scopetest"
	"strings"
	"testing"
)

func TestMCPRememberPlacesAndQuestion(t *testing.T) {
	f := scopetest.New(t)
	b := f.Bind(t, "global")
	cs := connect(t, f.App, b)
	args := map[string]any{"content": "Synthetic work repo preference applies across projects.", "place": "global"}
	out, bad := call(t, cs, "memory_remember", args)
	if !bad || !strings.Contains(out, "Are you sure it applies beyond it?") {
		t.Fatalf("question: %s", out)
	}
	args["place_reason"] = "Synthetic cross-project preference."
	out, bad = call(t, cs, "memory_remember", args)
	if bad {
		t.Fatal(out)
	}
	var reply idOut
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Place != "global" || len(reply.Places) != 2 {
		t.Fatalf("reply: %+v", reply)
	}
	u, err := f.App.DB.Resolve(reply.ID)
	if err != nil || u.Scope != "jcode@global" {
		t.Fatalf("stored: %+v %v", u, err)
	}
	args = map[string]any{"content": "Synthetic literal-scope portable rule.", "place": "global", "scope": "project:matrix-project"}
	out, bad = call(t, cs, "memory_remember", args)
	if bad {
		t.Fatal(out)
	}
	json.Unmarshal([]byte(out), &reply)
	if reply.Place != "here" {
		t.Fatal(out)
	}
}
