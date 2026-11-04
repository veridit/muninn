package ingest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSplitMarkdownMatchesSeidReference pins muninn's splitter to the exact
// output of seid search's split_into_sections on the same input (reference
// produced by compiling compiler/lib/search_pipeline.ml lines 1-73 and
// printing line|heading|%S content). seid behaviour is kept on purpose,
// including two quirks: '#' lines inside code fences count as headings, and
// trailing '##' on a heading is kept.
func TestSplitMarkdownMatchesSeidReference(t *testing.T) {
	in := "preamble line\n\n# Top Title\nbody one\n```sh\n# not a heading inside code\necho hi\n```\n## Sub-section ##\nbody two\n#nospace heading\n###### deep\nlast\n"
	want := []string{
		`1||"t\npreamble line\n\n"`,
		`3|Top Title|"t\nbody one\n` + "```" + `sh\n"`,
		`6|not a heading inside code|"t\necho hi\n` + "```" + `\n"`,
		`9|Sub-section ##|"t\nbody two\n"`,
		`11|nospace heading|"t\n"`,
		`12|deep|"t\nlast\n\n"`,
	}
	got := SplitMarkdown("/tmp/mdcmp/t.md", in)
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
	}
	for i, s := range got {
		line := fmt.Sprintf("%d|%s|%s", s.Line, s.Heading, ocamlS(s.Content))
		if line != want[i] {
			t.Errorf("section %d\n got %s\nwant %s", i, line, want[i])
		}
	}
	_ = os.Getpid
}

// ocamlS mimics OCaml's %S for the characters used in the fixture.
func ocamlS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}
