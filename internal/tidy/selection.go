package tidy

import (
	"fmt"
	"strings"
)

func literalScope(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "*!\r\n\x00") && !strings.Contains(s, "->")
}

// Selected validates every selected move before any mutation, including duplicate
// identities. --only is an exact source/target pair, never a pattern.
func Selected(p Plan, only string) ([]Entry, error) {
	if p.Version != 1 {
		return nil, fmt.Errorf("unsupported tidy plan version")
	}
	if only != "" {
		a, b, ok := strings.Cut(only, "->")
		if !ok || !literalScope(a) || !literalScope(b) {
			return nil, fmt.Errorf("--only requires a literal SRC->DST pair")
		}
	}
	seen := map[string]bool{}
	var out []Entry
	for _, e := range p.Entries {
		if e.Decision != "move" {
			continue
		}
		if only != "" && e.Source+"->"+e.Target != only {
			continue
		}
		if e.SID == "" || e.Origin == "" || len(e.Fingerprint) != 64 || !literalScope(e.Target) || e.Target == e.Source {
			return nil, fmt.Errorf("invalid move entry %q", e.SID)
		}
		if seen[e.SID] || seen[e.Origin] {
			return nil, fmt.Errorf("duplicate move identity %q", e.SID)
		}
		seen[e.SID] = true
		seen[e.Origin] = true
		out = append(out, e)
	}
	return out, nil
}

// CheckSnapshot is also used by the CLI dry run, which never opens a writable App.
func CheckSnapshot(p Plan, g Graph, only string) ([]Entry, error) {
	entries, err := Selected(p, only)
	if err != nil {
		return nil, err
	}
	units := map[string]string{}
	for _, u := range g.Units {
		units[u.SID] = Fingerprint(u)
	}
	for _, e := range entries {
		if units[e.SID] != e.Fingerprint {
			return nil, fmt.Errorf("stale plan for %s: rescan and review", e.SID)
		}
	}
	return entries, nil
}
