package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// Synthetic fixture with both category shapes seen in real jcode stores.
func TestReadJcodeFileCategoryShapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "global.json")
	os.WriteFile(p, []byte(`{"graph_version":1,"memories":{
	  "a":{"id":"a","content":"plain fact","category":"fact","tags":["x"],"active":true,"updated_at":"2026-01-01T00:00:00Z"},
	  "b":{"id":"b","content":"custom one","category":{"custom":"anything"},"active":true},
	  "c":{"id":"c","content":"inactive","category":"fact","active":false},
	  "d":{"id":"d","content":"   ","category":"fact"}}}`), 0o600)
	mems, err := ReadJcodeFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 3 {
		t.Fatalf("want 3 (empty content dropped), got %d: %+v", len(mems), mems)
	}
	got := map[string]Memory{}
	for _, m := range mems {
		got[m.Key] = m
	}
	if got["a"].Category != "fact" || got["b"].Category != "custom" || got["c"].Active {
		t.Fatalf("decoded wrong: %+v", got)
	}
}

func TestSplitMarkdownSeidRules(t *testing.T) {
	secs := SplitMarkdown("/x/design-notes.md", "intro line\n# Title\nbody a\n## Sub\nbody b\n")
	if len(secs) != 3 {
		t.Fatalf("got %d sections: %+v", len(secs), secs)
	}
	if secs[0].Heading != "" || secs[0].Content != "design notes\nintro line\n" {
		t.Fatalf("preamble: %+v", secs[0])
	}
	if secs[1].Heading != "Title" || secs[1].Line != 2 || secs[2].Heading != "Sub" || secs[2].Line != 4 {
		t.Fatalf("headings/lines: %+v", secs)
	}
}
