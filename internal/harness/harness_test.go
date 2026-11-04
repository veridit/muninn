package harness

import (
	"strings"
	"testing"
)

func TestJcodeTurnStartEdit(t *testing.T) {
	cmd := "/usr/local/bin/muninn hook jcode"
	base := "[display]\nemoji = true\n\n[hooks]\npre_tool = \"~/bin/gate\"\nturn_start = \"~/bin/probe.sh\"\n\n[ambient]\nx = 1\n"

	out, changed, err := editJcodeTurnStart(base, cmd, true)
	if err != nil || !changed {
		t.Fatalf("install: %v %v", changed, err)
	}
	if !strings.Contains(out, `turn_start = ["~/bin/probe.sh","/usr/local/bin/muninn hook jcode"]`) {
		t.Fatalf("existing hook not kept:\n%s", out)
	}
	if !strings.Contains(out, "pre_tool = \"~/bin/gate\"") || !strings.Contains(out, "[ambient]\nx = 1") {
		t.Fatalf("unrelated config disturbed:\n%s", out)
	}
	if _, changed, _ := editJcodeTurnStart(out, cmd, true); changed {
		t.Fatal("install not idempotent")
	}
	back, changed, err := editJcodeTurnStart(out, cmd, false)
	if err != nil || !changed || !strings.Contains(back, `turn_start = "~/bin/probe.sh"`) {
		t.Fatalf("uninstall: %v %v\n%s", changed, err, back)
	}

	// No [hooks] section yet.
	out, _, _ = editJcodeTurnStart("[display]\nemoji = true\n", cmd, true)
	if !strings.Contains(out, "[hooks]\n"+jcodeMarker+"\nturn_start = \"/usr/local/bin/muninn hook jcode\"") {
		t.Fatalf("fresh install:\n%s", out)
	}
	back, _, _ = editJcodeTurnStart(out, cmd, false)
	if strings.Contains(back, "muninn") {
		t.Fatalf("uninstall left traces:\n%s", back)
	}
}

func TestPromptHookOutputShape(t *testing.T) {
	if PromptHookOutput("") != nil {
		t.Fatal("empty block must print nothing")
	}
	got := string(PromptHookOutput("[muninn recall] x"))
	want := `{"hookSpecificOutput":{"additionalContext":"[muninn recall] x","hookEventName":"UserPromptSubmit"}}` + "\n"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestUserTextSkipsNoise(t *testing.T) {
	m := jcodeMessage{Role: "user"}
	m.Content = append(m.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"tool_result", "ignored"})
	if userText(m) != "" {
		t.Fatal("tool result treated as user text")
	}
}
