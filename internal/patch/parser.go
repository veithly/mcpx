// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/{parser,streaming_parser}.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; translated and adapted for MCPX.
package patch

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const beginPatch = "*** Begin Patch"
const endPatch = "*** End Patch"
const eofMarker = "*** End of File"

type hunk struct {
	kind       byte
	path, move string
	hasMove    bool
	contents   strings.Builder
	chunks     []chunk
}
type chunk struct {
	context        *string
	old, new       []string
	contextIndices [][2]int
	eof            bool
}

func (c *chunk) empty() bool { return len(c.old) == 0 && len(c.new) == 0 }
func (c *chunk) addContext(s string) {
	c.contextIndices = append(c.contextIndices, [2]int{len(c.old), len(c.new)})
	c.old = append(c.old, s)
	c.new = append(c.new, s)
}

type parseError struct {
	message string
	line    int
}

func (e *parseError) Error() string {
	if e.line == 0 {
		return "Invalid patch: " + e.message + "\n"
	}
	return fmt.Sprintf("Invalid patch hunk on line %d: %s\n", e.line, e.message)
}
func invalidPatch(s string) error { return &parseError{message: s} }
func boundaries(lines []string) error {
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != beginPatch {
		return invalidPatch("The first line of the patch must be '*** Begin Patch'")
	}
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != endPatch {
		return invalidPatch("The last line of the patch must be '*** End Patch'")
	}
	return nil
}

type parsedPatch struct {
	hunks         []*hunk
	environmentID string
}

func parse(input string) (*parsedPatch, error) {
	if !utf8.ValidString(input) {
		return nil, invalidPatch("PATCH must be UTF-8")
	}
	// Rust str::lines splits LF/CRLF only, not bare CR or Unicode separators.
	input = strings.TrimSpace(input)
	var lines []string
	if input != "" {
		lines = strings.Split(input, "\n")
		for i := 0; i < len(lines)-1; i++ {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
	}
	if err := boundaries(lines); err != nil {
		if len(lines) < 4 || (lines[0] != "<<EOF" && lines[0] != "<<'EOF'" && lines[0] != "<<\"EOF\"") || !strings.HasSuffix(lines[len(lines)-1], "EOF") {
			return nil, err
		}
		lines = lines[1 : len(lines)-1]
		if err := boundaries(lines); err != nil {
			return nil, err
		}
	}
	p := streamingParser{}
	if err := p.push(strings.Join(lines, "\n")); err != nil {
		return nil, err
	}
	hunks, err := p.finish()
	if err != nil {
		return nil, err
	}
	return &parsedPatch{hunks: hunks, environmentID: p.environmentID}, nil
}

// Complete and incremental input use one state machine. Policy validation
// consumes its hunks; there is no separate path scanner or second grammar.
type streamingParser struct {
	buffer         strings.Builder
	mode           byte // zero, started, A, D, M, ended
	line, hunkLine int
	hunks          []*hunk
	environmentID  string
}

func (p *streamingParser) bad(s string) error { return &parseError{message: s, line: p.line} }
func (p *streamingParser) last() *hunk        { return p.hunks[len(p.hunks)-1] }
func (p *streamingParser) nonempty(line string) error {
	if len(p.hunks) == 0 || p.last().kind != 'M' {
		return nil
	}
	h := p.last()
	if len(h.chunks) == 0 && p.mode == 'M' {
		return &parseError{message: fmt.Sprintf("Update file hunk for path '%s' is empty", h.path), line: p.hunkLine}
	}
	if len(h.chunks) > 0 && h.chunks[len(h.chunks)-1].empty() {
		if line == endPatch {
			return p.bad("Update hunk does not contain any lines")
		}
		return p.unexpected(line)
	}
	return nil
}
func (p *streamingParser) unexpected(line string) error {
	return p.bad(fmt.Sprintf("Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)", line))
}
func (p *streamingParser) headers(line string) (bool, error) {
	if p.mode == 'S' && strings.HasPrefix(line, "*** Environment ID:") {
		if p.environmentID != "" {
			return true, invalidPatch("apply_patch environment_id cannot be specified more than once")
		}
		p.environmentID = strings.TrimSpace(strings.TrimPrefix(line, "*** Environment ID:"))
		if p.environmentID == "" {
			return true, invalidPatch("apply_patch environment_id cannot be empty")
		}
		return true, nil
	}
	if line == endPatch {
		if err := p.nonempty(line); err != nil {
			return true, err
		}
		p.mode = 'E'
		return true, nil
	}
	for _, header := range []struct {
		prefix string
		kind   byte
	}{{"*** Add File: ", 'A'}, {"*** Delete File: ", 'D'}, {"*** Update File: ", 'M'}} {
		if path, ok := strings.CutPrefix(line, header.prefix); ok {
			if err := p.nonempty(line); err != nil {
				return true, err
			}
			p.hunks = append(p.hunks, &hunk{kind: header.kind, path: path})
			p.mode = header.kind
			p.hunkLine = p.line
			return true, nil
		}
	}
	return false, nil
}
func (p *streamingParser) push(delta string) error {
	for {
		part, rest, ok := strings.Cut(delta, "\n")
		p.buffer.WriteString(part)
		if !ok {
			return nil
		}
		line := strings.TrimSuffix(p.buffer.String(), "\r")
		p.buffer.Reset()
		p.line++
		if err := p.process(line); err != nil {
			return err
		}
		delta = rest
	}
}
func (p *streamingParser) finish() ([]*hunk, error) {
	if p.buffer.Len() > 0 {
		line := p.buffer.String()
		p.buffer.Reset()
		p.line++
		// Upstream accepts padded final markers even in update mode.
		if strings.TrimSpace(line) == endPatch {
			if err := p.nonempty(endPatch); err != nil {
				return nil, err
			}
			p.mode = 'E'
		} else if err := p.process(line); err != nil {
			return nil, err
		}
	}
	if p.mode != 'E' {
		return nil, invalidPatch("The last line of the patch must be '*** End Patch'")
	}
	return p.hunks, nil
}
func (p *streamingParser) process(line string) error {
	trimmed := strings.TrimSpace(line)
	switch p.mode {
	case 0:
		if trimmed != beginPatch {
			return invalidPatch("The first line of the patch must be '*** Begin Patch'")
		}
		p.mode = 'S'
		return nil
	case 'E':
		if trimmed != "" {
			return invalidPatch("The last line of the patch must be '*** End Patch'")
		}
		return nil
	case 'S', 'A', 'D':
		if ok, err := p.headers(trimmed); ok || err != nil {
			return err
		}
		if p.mode == 'A' && strings.HasPrefix(line, "+") {
			p.last().contents.WriteString(line[1:])
			p.last().contents.WriteByte('\n')
			return nil
		}
		return p.bad(fmt.Sprintf("'%s' is not a valid hunk header. Valid hunk headers: '*** Add File: {path}', '*** Delete File: {path}', '*** Update File: {path}'", trimmed))
	}
	updateLine := strings.TrimRightFunc(line, unicode.IsSpace)
	if ok, err := p.headers(updateLine); ok || err != nil {
		return err
	}
	h := p.last()
	var last *chunk
	if len(h.chunks) > 0 {
		last = &h.chunks[len(h.chunks)-1]
	}
	marker := updateLine == "@@" || strings.HasPrefix(updateLine, "@@ ")
	if last != nil && last.eof {
		if updateLine == "" {
			return nil
		}
		if !marker {
			return p.bad(fmt.Sprintf("Expected update hunk to start with a @@ context marker, got: '%s'", line))
		}
	}
	if len(h.chunks) == 0 && !h.hasMove && strings.HasPrefix(updateLine, "*** Move to: ") {
		h.move = strings.TrimPrefix(updateLine, "*** Move to: ")
		h.hasMove = true
		return nil
	}
	if marker {
		if last != nil && last.empty() {
			return p.unexpected(line)
		}
		c := chunk{}
		if updateLine != "@@" {
			s := strings.TrimPrefix(updateLine, "@@ ")
			c.context = &s
		}
		h.chunks = append(h.chunks, c)
		return nil
	}
	if updateLine == eofMarker {
		if last != nil {
			if last.empty() {
				return p.bad("Update hunk does not contain any lines")
			}
			last.eof = true
		}
		return nil
	}
	if line == "" || line[0] == ' ' || line[0] == '+' || line[0] == '-' {
		if last == nil {
			h.chunks = append(h.chunks, chunk{})
			last = &h.chunks[0]
		}
		switch {
		case line == "":
			last.addContext("")
		case line[0] == ' ':
			last.addContext(line[1:])
		case line[0] == '+':
			last.new = append(last.new, line[1:])
		case line[0] == '-':
			last.old = append(last.old, line[1:])
		}
		return nil
	}
	if last != nil && !last.empty() {
		return p.bad(fmt.Sprintf("Expected update hunk to start with a @@ context marker, got: '%s'", line))
	}
	return p.unexpected(line)
}
