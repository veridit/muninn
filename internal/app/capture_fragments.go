package app

import (
	"fmt"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/harness"
	"github.com/veridit/muninn/internal/project"
)

// captureFragments bounds hook requests without dropping the tail of a large
// assistant message. Stable fragment receipts let a later invocation resume it.
// The original message is seen only when its final fragment checkpoints.
func captureFragments(messages []harness.CaptureMessage, seen map[string]bool, limit int) ([]harness.CaptureMessage, map[string]string, map[string]string) {
	var out []harness.CaptureMessage
	originals, last := map[string]string{}, map[string]string{}
	for _, m := range messages {
		size := max(4, limit-len(m.Role)-2)
		// Withhold whole secret-bearing messages before splitting, never split a
		// credential pattern across fragments and accidentally bypass the detector.
		if project.LooksSecret(m.Text) || len(m.Text) <= size {
			out = append(out, m)
			continue
		}
		for offset := 0; offset < len(m.Text); {
			end := min(offset+size, len(m.Text))
			for end < len(m.Text) && end > offset && !utf8.RuneStart(m.Text[end]) {
				end--
			}
			if end == offset {
				end = min(offset+size, len(m.Text))
			}
			text := m.Text[offset:end]
			id := "fragment:" + captureHash(fmt.Sprintf("%s\x00%d\x00%s", m.ID, offset, text))
			originals[id] = m.ID
			if end == len(m.Text) {
				last[id] = m.ID
			}
			if !seen[id] {
				out = append(out, harness.CaptureMessage{ID: id, Role: m.Role, Text: text})
			}
			offset = end
		}
	}
	return out, originals, last
}
