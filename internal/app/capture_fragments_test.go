package app

import (
	"strings"
	"testing"

	"github.com/veridit/muninn/internal/harness"
)

func TestCaptureFragmentSecretWithholdingPrecedesSplit(t *testing.T) {
	text := strings.Repeat("safe ", 202) + "sk-" + strings.Repeat("z", 30)
	messages, originals, last := captureFragments([]harness.CaptureMessage{{ID: "secret-boundary", Role: "user", Text: text}}, nil, 1024)
	if len(messages) != 1 || messages[0].ID != "secret-boundary" || len(originals) != 0 || len(last) != 0 {
		t.Fatal("secret-bearing message split before withholding")
	}
}
