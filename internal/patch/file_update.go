// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/file_update.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; translated to Go for MCPX.
package patch

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type replacement struct {
	start, count int
	lines        []string
}

func updatedContents(contents, path string, chunks []chunk, mode FileUpdateMode) (string, error) {
	if mode == PreserveLineEndings {
		source := parseSource(contents)
		lines := make([]string, len(source.lines))
		for i, l := range source.lines {
			lines[i] = l.text
		}
		replacements, err := computeReplacements(lines, path, chunks, mode)
		if err != nil {
			return "", err
		}
		return source.apply(replacements), nil
	}
	lines := strings.Split(contents, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	replacements, err := computeReplacements(lines, path, chunks, mode)
	if err != nil {
		return "", err
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		lines = slices.Replace(lines, r.start, min(r.start+r.count, len(lines)), r.lines...)
	}
	if len(lines) == 0 || lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n"), nil
}
func computeReplacements(lines []string, path string, chunks []chunk, mode FileUpdateMode) ([]replacement, error) {
	var result []replacement
	index := 0
	for _, c := range chunks {
		if c.context != nil {
			found := seekSequence(lines, []string{*c.context}, index, false, mode)
			if found < 0 {
				return nil, fmt.Errorf("Failed to find context '%s' in %s", *c.context, path)
			}
			index = found + 1
		}
		if len(c.old) == 0 {
			insertion := len(lines)
			if mode == NormalizeToLF && insertion > 0 && lines[insertion-1] == "" {
				insertion--
			}
			result = append(result, replacement{insertion, 0, c.new})
			continue
		}
		pattern, segment := c.old, c.new
		found := seekSequence(lines, pattern, index, c.eof, mode)
		if found < 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(segment) > 0 && segment[len(segment)-1] == "" {
				segment = segment[:len(segment)-1]
			}
			found = seekSequence(lines, pattern, index, c.eof, mode)
		}
		if found < 0 {
			return nil, fmt.Errorf("Failed to find expected lines in %s:\n%s", path, strings.Join(c.old, "\n"))
		}
		if mode == NormalizeToLF {
			result = append(result, replacement{found, len(pattern), segment})
		} else {
			oldStart, newStart := 0, 0
			for _, pair := range c.contextIndices {
				oldContext, newContext := pair[0], pair[1]
				if oldContext >= len(pattern) || newContext >= len(segment) {
					break
				}
				if oldStart != oldContext || newStart != newContext {
					result = append(result, replacement{found + oldStart, oldContext - oldStart, segment[newStart:newContext]})
				}
				oldStart = oldContext + 1
				newStart = newContext + 1
			}
			if oldStart != len(pattern) || newStart != len(segment) {
				result = append(result, replacement{found + oldStart, len(pattern) - oldStart, segment[newStart:]})
			}
		}
		index = found + len(pattern)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].start < result[j].start })
	return result, nil
}
