package index

import (
	"sort"
	"strings"
	"unicode"
)

// Stopwords for concept extraction (seid search list, English).
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for are but not you all can had her was one our out has have been
	some them than its over such that this will each from they were which their what there when make like just
	also into more other could after two how these way about many would does used only new very most here both
	between being same another every example see via e.g. i.e. etc added using based note instead still should
	must need needs works work with your then`) {
		stopwords[w] = true
	}
}

// IsStopword reports whether w is filtered from concepts and queries.
func IsStopword(w string) bool { return stopwords[w] }

// Words lowercases and splits text into word tokens ([a-z0-9_-]).
func Words(text string) []string {
	f := func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
	}
	return strings.FieldsFunc(strings.ToLower(text), f)
}

// Concepts extracts sorted unique unigram concepts: longer than 3 chars and
// not a stopword (seid search's extract_concepts; bigrams are excluded to
// keep the co-occurrence graph tractable).
func Concepts(text string) []string {
	set := map[string]bool{}
	for _, w := range Words(text) {
		if len(w) > 3 && !IsStopword(w) {
			set[w] = true
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}
