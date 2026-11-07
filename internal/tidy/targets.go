package tidy

import "strings"

// writableScope excludes counting buckets and retired import aliases. The
// scanner additionally requires targets to exist in its effective snapshot.
func writableScope(s string) bool {
	if !literalScope(s) {
		return false
	}
	if s == "global" || s == "personal" {
		return true
	}
	for _, prefix := range []string{"project:", "folder:", "client:"} {
		if strings.HasPrefix(s, prefix) {
			return len(s) > len(prefix) && !strings.HasSuffix(s, ":")
		}
	}
	if strings.HasPrefix(s, "jcode@") {
		return len(s) > len("jcode@") && !strings.HasSuffix(s, ":")
	}
	return false
}

// Client ownership is provenance, never a similarity inference. Non-client
// memories cannot enter a client, and client memories cannot leave their owner.
func targetAllowed(source, target string) bool {
	if !writableScope(target) || source == target {
		return false
	}
	if strings.HasPrefix(source, "client:") {
		return strings.HasPrefix(target, TopScope(source)+":")
	}
	return !strings.HasPrefix(target, "client:")
}

func allowedTargets(e Entry, scopes []string) []string {
	out := []string{}
	for _, s := range scopes {
		if targetAllowed(effectiveSource(e), s) {
			out = append(out, s)
		}
	}
	return out
}

func concreteTarget(top string, scopes []string, votes map[string]int) string {
	best := ""
	for _, s := range scopes {
		if TopScope(s) == top && (best == "" || votes[s] > votes[best]) {
			best = s
		}
	}
	return best
}
