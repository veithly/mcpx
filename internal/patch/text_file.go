// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/text_file.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; translated to Go for MCPX.
package patch

import "strings"

type sourceLine struct{ text, ending string }
type sourceFile struct {
	lines     []sourceLine
	preferred string
}

func parseSource(contents string) sourceFile {
	s := sourceFile{}
	start := 0
	for cursor := 0; cursor < len(contents); {
		n := 0
		switch contents[cursor] {
		case '\n':
			n = 1
		case '\r':
			n = 1
			if cursor+1 < len(contents) && contents[cursor+1] == '\n' {
				n = 2
			}
		}
		if n == 0 {
			cursor++
			continue
		}
		ending := contents[cursor : cursor+n]
		if s.preferred == "" {
			s.preferred = ending
		}
		s.lines = append(s.lines, sourceLine{contents[start:cursor], ending})
		cursor += n
		start = cursor
	}
	if start < len(contents) {
		s.lines = append(s.lines, sourceLine{text: contents[start:]})
	}
	if s.preferred == "" {
		s.preferred = "\n"
	}
	return s
}
func (s sourceFile) apply(replacements []replacement) string {
	var out strings.Builder
	appendLine := func(l sourceLine) {
		out.WriteString(l.text)
		if l.ending == "" {
			out.WriteString(s.preferred)
		} else {
			out.WriteString(l.ending)
		}
	}
	index := 0
	for _, r := range replacements {
		for _, l := range s.lines[index:r.start] {
			appendLine(l)
		}
		for _, l := range r.lines {
			appendLine(sourceLine{l, s.preferred})
		}
		index = r.start + r.count
	}
	for _, l := range s.lines[index:] {
		appendLine(l)
	}
	return out.String()
}
