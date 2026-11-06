package project

import (
	"strings"
	"testing"
)

// The secret check only prompts a judgment. It must fire on credential shapes
// and random-path URLs, and stay quiet on ordinary text (2026-11-06: 36 seid
// memories tripped it through [[task-...]] links).
func TestSecretHint(t *testing.T) {
	slug := "colosseum-Xq7Tm2Lk9Vb4Rw8Np3Zd6Hs1Jf5Gc0Ya"
	for _, tc := range []struct {
		in   string
		hint bool
	}{
		{"Public URL is https://heneld.no/" + slug + "/ after upload", true},
		{"key in query https://x.example/share?id=AbCdEfGhIjKlMnOpQrStUv12345", false},
		{"see https://github.com/veridit/muninn/pull/42 for details", false},
		{"commit https://github.com/x/y/commit/8759b7b3c1d2e4f5a6b7c8d9e0f1a2b3c4d5e6f7", false},
		{"long word https://example.com/internationalization-guidelines-handbook", false},
		{"token " + "sk-" + strings.Repeat("z", 30) + " leaked", true},
		{"see [[t" + "ask-636-deferred-recursion-anonymous-self]] for detail", false},
		{"handoff design/examples/t" + "ask-855-port-parity-01", false},
	} {
		if got := SecretHint(tc.in) != ""; got != tc.hint {
			t.Errorf("SecretHint(%q) fired=%v, want %v", tc.in, got, tc.hint)
		}
		if LooksSecret(tc.in) != tc.hint {
			t.Errorf("LooksSecret(%q) disagrees with SecretHint", tc.in)
		}
	}
}

// Masking is for provider egress only: the token goes, the sentence stays,
// and a leading boundary character survives.
func TestMaskCredentials(t *testing.T) {
	tok := "sk-" + strings.Repeat("z", 30)
	got := MaskCredentials("deploy uses (" + tok + ") from the vault")
	if strings.Contains(got, tok) || got != "deploy uses ([possible secret]) from the vault" {
		t.Fatalf("mask = %q", got)
	}
	slug := "https://heneld.no/colosseum-Xq7Tm2Lk9Vb4Rw8Np3Zd6Hs1Jf5Gc0Ya/"
	if MaskCredentials("tour at "+slug) != "tour at "+slug {
		t.Fatal("capability URLs are judged, not masked")
	}
}
