package project

import (
	"strings"
	"testing"
)

// 2026-11-06: a recalled engram note exposed a private PWA's capability URL
// (the random path is the key). Recall must not show such URLs; ordinary
// URLs stay readable.
func TestRedactCapabilityURLs(t *testing.T) {
	slug := "colosseum-Xq7Tm2Lk9Vb4Rw8Np3Zd6Hs1Jf5Gc0Ya"
	for _, tc := range []struct {
		in      string
		redacts bool
	}{
		{"Public URL is https://heneld.no/" + slug + "/ after upload", true},
		{"PROPFIND https://myfiles.fastmail.com/" + slug + "/ returned 207", true},
		{"key in query https://x.example/share?id=AbCdEfGhIjKlMnOpQrStUv12345", false},
		{"see https://github.com/veridit/muninn/pull/42 for details", false},
		{"docs https://pkg.go.dev/golang.org/x/sys/unix#Setpriority", false},
		{"commit https://github.com/x/y/commit/8759b7b3c1d2e4f5a6b7c8d9e0f1a2b3c4d5e6f7", false},
		{"long word https://example.com/internationalization-guidelines-handbook", false},
		{"token " + "sk-" + strings.Repeat("z", 30) + " leaked", true},
	} {
		got, changed := Redact(tc.in)
		if changed != tc.redacts {
			t.Errorf("Redact(%q) changed=%v, want %v (got %q)", tc.in, changed, tc.redacts, got)
		}
		if tc.redacts && strings.Contains(got, slug) {
			t.Errorf("slug survived: %q", got)
		}
		if LooksSecret(tc.in) != tc.redacts {
			t.Errorf("LooksSecret(%q) = %v, want %v", tc.in, !tc.redacts, tc.redacts)
		}
	}
}
