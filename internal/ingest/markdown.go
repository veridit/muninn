package ingest

import (
	"path/filepath"
	"strings"
)

// Section is one heading-delimited chunk of a markdown file.
type Section struct {
	File    string
	Heading string
	Line    int
	Content string // file label + "\n" + body text
}

// SplitMarkdown splits markdown into sections at '#' and '##'+ headings.
// Ported from seid search's split_into_sections: each section keeps its
// heading and starting line; its content is prefixed with a file label
// (basename without .md, dashes as spaces) so the file name participates in
// matching. Text before the first heading becomes a heading-less section.
func SplitMarkdown(path, content string) []Section {
	label := strings.TrimSuffix(filepath.Base(path), ".md")
	label = strings.ReplaceAll(label, "-", " ")

	var (
		out     []Section
		heading string
		start   = 1
		buf     strings.Builder
	)
	flush := func() {
		text := buf.String()
		if heading != "" || text != "" {
			out = append(out, Section{File: path, Heading: heading, Line: start, Content: label + "\n" + text})
		}
		buf.Reset()
	}
	for i, line := range strings.Split(content, "\n") {
		n := i + 1
		if strings.HasPrefix(line, "#") {
			flush()
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			start = n
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return out
}
