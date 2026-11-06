package app

import (
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/harness"
)

// Masking precedes splitting: a fragment boundary must never cut a token in
// half and pass both halves to the provider. The rest of the message survives.
func TestCaptureFragmentMaskPrecedesSplit(t *testing.T) {
	tok := "sk-" + strings.Repeat("z", 30)
	for pad := 990; pad < 1030; pad++ {
		text := strings.Repeat("s", pad) + " " + tok + " tail fact"
		messages, _, _ := captureFragments([]harness.CaptureMessage{{ID: "secret-boundary", Role: "user", Text: text}}, nil, 1024)
		var all strings.Builder
		for _, m := range messages {
			all.WriteString(m.Text)
			if strings.Contains(m.Text, "zzzzzzzzzz") {
				t.Fatalf("pad %d: token fragment reached provider: %q", pad, m.Text)
			}
		}
		if !strings.Contains(all.String(), "[possible secret] tail fact") {
			t.Fatalf("pad %d: surrounding text lost", pad)
		}
	}
}
