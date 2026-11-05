package recall

import (
	"regexp"
	"strings"
)

// SaveCue returns a one-line nudge when the user's message reads like a
// durable rule, preference, decision or correction, and "" otherwise. It
// never saves anything itself: the agent decides, in the user's context.
//
// Detection is by phrasing, not elapsed time, and deliberately narrow: a
// nudge on every turn would be noise the agent learns to ignore. Questions
// are skipped ("should we always ...?" is not a decision).
func SaveCue(msg string) string {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" || IsOwnOutput(msg) || len(m) > 2000 {
		return ""
	}
	if strings.HasSuffix(m, "?") {
		return ""
	}
	for _, c := range cues {
		if c.re.MatchString(m) {
			return "Looks like a lasting " + c.kind + ": if it matters beyond this session, memory_remember it (narrowest scope; forget any memory it contradicts)."
		}
	}
	return ""
}

type cue struct {
	kind string
	re   *regexp.Regexp
}

// The phrases are English plus Norwegian (the author's language); each is
// anchored on words, so "always" inside "hallways" never matches.
var cues = []cue{
	{"correction", regexp.MustCompile(`\b(no|nope),? (that'?s|it'?s) (wrong|not right|incorrect)\b|\bthat'?s (wrong|incorrect|outdated)\b|\b(actually|correction):? \b|\bnot true anymore\b|\bno longer (true|the case|used)\b|\b(det|dette) er (feil|ikke riktig)\b|\bstemmer ikke\b`)},
	{"rule", regexp.MustCompile(`\bfrom now on\b|\bgoing forward\b|\b(always|never) (use|do|run|put|write|commit|push|ask|call|start|add|keep|prefer|reply|answer|test)\b|\bdon'?t ever\b|\bmake sure (to|you) always\b|\bheretter\b|\bfra nå av\b|\b(alltid|aldri) (bruk|gjør|kjør|skriv|svar)\b`)},
	{"preference", regexp.MustCompile(`\bi (prefer|like|want|hate|dislike) (it )?(when|to|that|you)\b|\bmy preference is\b|\bi'?d rather\b|\bjeg foretrekker\b|\bjeg liker (ikke )?(at|når)\b`)},
	{"decision", regexp.MustCompile(`\b(we|i) (have )?decided\b|\b(the )?decision is\b|\blet'?s go with\b|\bwe'?ll use\b|\bvi (har )?bestemt\b|\bvi går for\b`)},
}
