package recall

import (
	"strings"
	"testing"
)

func TestSaveCue(t *testing.T) {
	fire := []string{
		"From now on, run make lint before every commit.",
		"No, that's wrong, the staging port is 8443.",
		"Never use Chrome for browser automation here.",
		"I prefer when you keep answers short.",
		"We decided to drop the legacy importer.",
		"Fra nå av skal du alltid svare på norsk.",
		"Dette er feil, vi bruker ikke Docker lenger.",
		"That's outdated, the build moved to GitHub Actions.",
	}
	quiet := []string{
		"thanks",
		"ok, continue",
		"can you check the CI",
		"should we always run the tests first?",
		"walk down the hallways and never look back", // "never look" is not a listed verb
		"refactor this function",
		"[muninn recall] From now on use tabs",
		"",
	}
	for _, m := range fire {
		if SaveCue(m) == "" {
			t.Errorf("should cue: %q", m)
		}
	}
	for _, m := range quiet {
		if c := SaveCue(m); c != "" {
			t.Errorf("should stay quiet: %q -> %q", m, c)
		}
	}
}

func TestRenderCarriesIDsAndFixRule(t *testing.T) {
	b := Render([]Hit{{Key: "mem_1a2b", Source: "native", Text: "Uses tabs"}, {Key: "abc-123", Source: "jcode", Text: "Port 8443"}})
	for _, want := range []string{"- [mem_1a2b] Uses tabs", "- [jcode:abc-123] Port 8443", "muninn forget <id>"} {
		if !contains(b, want) {
			t.Errorf("block missing %q:\n%s", want, b)
		}
	}
	if !IsOwnOutput(b) {
		t.Error("block must still be recognised as muninn's own output")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
