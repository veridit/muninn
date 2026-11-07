package main

import (
	"github.com/veridit/muninn/internal/scopetest"
	"strings"
	"testing"
)

func TestCLIRememberPlaceAndQuestion(t *testing.T) {
	f := scopetest.New(t)
	f.Bind(t, "global")
	t.Chdir(f.Cwd)
	out, err := cliSurface(t, rememberCmd(), "Synthetic work repository rule.", "--place", "global")
	if err == nil || !strings.Contains(out, "Are you sure it applies beyond it?") {
		t.Fatal(out)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic work repository rule.", "--place", "global", "--place-reason", "Synthetic portable rule.")
	if err != nil || !strings.Contains(out, "place: global") || !strings.Contains(out, "available: here") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic literal precedence rule.", "--place", "global", "--scope", "project:matrix-project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = cliSurface(t, rememberCmd(), "Synthetic project flag compatibility rule.", "--project")
	if err != nil || !strings.Contains(out, "place: here") {
		t.Fatalf("%s %v", out, err)
	}
}
