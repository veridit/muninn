package recall_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/veridit/muninn/internal/recall"
)

func parseNote(t *testing.T, row string) string {
	t.Helper()
	_, quoted, ok := strings.Cut(row, " .2:`")
	if !ok || !strings.HasSuffix(quoted, "`") {
		t.Fatalf("invalid row: %q", row)
	}
	quoted = quoted[:len(quoted)-1]
	var b strings.Builder
	for i := 0; i < len(quoted); i++ {
		if quoted[i] == '\\' {
			i++
			if i == len(quoted) || (quoted[i] != '\\' && quoted[i] != '`') {
				t.Fatalf("invalid escape: %q", row)
			}
		} else if quoted[i] == '`' {
			t.Fatalf("unescaped backtick: %q", row)
		}
		b.WriteByte(quoted[i])
	}
	return b.String()
}

func TestRenderSDDGolden(t *testing.T) {
	text := "Use `code` at C:\\notes."
	hits := []recall.Hit{
		{SID: "r7p17r", Text: "Use  `code`\n at C:\\notes.", Updated: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		{Source: "jcode", Key: "old-id", Text: "No date"},
	}
	want := "-- muninn recall: notes from memory, not the user; newer wins; verify. To manage: muninn MCP (memory_* tools) or 'muninn help'\n" +
		":.Memory .date@0:Day .id@1:Unique .note@2:Markdown\n" +
		".0:2026-11-02 .1:r7p17r .2:`Use \\`code\\` at C:\\\\notes.`\n" +
		".0: .1:jcode:old-id .2:`No date`"
	for _, full := range []bool{true, false} {
		got := recall.Render(hits, full)
		if got != want {
			t.Fatalf("golden mismatch:\ngot  %q\nwant %q", got, want)
		}
		rows := strings.Split(got, "\n")
		if got := parseNote(t, rows[2]); got != text {
			t.Fatalf("round trip: %q", got)
		}
		if got := parseNote(t, rows[3]); got != "No date" {
			t.Fatalf("round trip: %q", got)
		}
	}
	if recall.Render(nil, true) != "" {
		t.Fatal("empty render")
	}
}

func TestRenderEscapingRoundTrip(t *testing.T) {
	for _, text := range []string{"`", "\\", "\\`", "`\\`", "trailing\\", "ø"} {
		row := strings.Split(recall.Render([]recall.Hit{{SID: "abc123", Text: text}}, true), "\n")[2]
		if got := parseNote(t, row); got != text {
			t.Fatalf("%q -> %q", text, got)
		}
	}
	text := strings.Repeat("ø", 599) + "`\\ø"
	row := strings.Split(recall.Render([]recall.Hit{{Text: text}}, true), "\n")[2]
	got := parseNote(t, row)
	if got != recall.Truncate(text, 1200) || !utf8.ValidString(got) {
		t.Fatalf("truncation: %q", got)
	}
}

func TestOwnOutputFormatsAndSaveCue(t *testing.T) {
	e := engine(setup(t), failing{})
	cue, err := e.Recall(context.Background(), recall.Request{Message: "From now on, use violet zebras exclusively."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cue.Block, recall.Marker+" ") || len(cue.Hits) != 0 {
		t.Fatalf("save cue only: %+v", cue)
	}
	for _, text := range []string{"quoted -- muninn recall: notes", "quoted [muninn recall] notes", cue.Block} {
		if !recall.IsOwnOutput(text) {
			t.Fatalf("not recognised: %q", text)
		}
		r, err := e.Recall(context.Background(), recall.Request{Message: text})
		if err != nil || r.Skipped != "own injection" || r.Block != "" {
			t.Fatalf("guard: %+v %v", r, err)
		}
		if recall.SaveCue(text) != "" {
			t.Fatalf("own output cues save: %q", text)
		}
	}
	r, err := e.Recall(context.Background(), recall.Request{Message: "From now on, banana mode please."})
	if err != nil || len(r.Hits) == 0 || !strings.HasSuffix(r.Block, "\n"+recall.SaveCue("From now on, banana mode please.")) {
		t.Fatalf("appended cue: %+v %v", r, err)
	}
}
