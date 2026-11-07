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
		if entryTargetAllowed(e, s, scopes) {
			out = append(out, s)
		}
	}
	return out
}

func entryTargetAllowed(e Entry, target string, scopes []string) bool {
	if !targetAllowed(effectiveSource(e), target) {
		return false
	}
	if target == "jcode@global" {
		winner, _ := concreteTarget(TopScope(target), scopes, e.ConcreteVotes)
		return e.ConcreteVotes[target] > 0 && winner == target
	}
	return true
}

func concreteTarget(top string, scopes []string, votes map[string]int) (string, bool) {
	best := ""
	candidates := 0
	for _, s := range scopes {
		if TopScope(s) == top {
			candidates++
			if best == "" || votes[s] > votes[best] || (votes[s] == votes[best] && s < best) {
				best = s
			}
		}
	}
	if best != "" && votes[best] == 0 && (candidates > 1 || best == "jcode@global") {
		return "", true
	}
	return best, false
}
