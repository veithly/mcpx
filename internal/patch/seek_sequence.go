// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/seek_sequence.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; translated to Go for MCPX.
package patch

import (
	"strings"
	"unicode"
)

func seekSequence(lines, pattern []string, start int, eof bool, mode FileUpdateMode) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}
	if eof {
		end := len(lines) - len(pattern)
		if mode == NormalizeToLF || end > start {
			start = end
		}
	}
	// A later exact match beats an earlier fuzzy match. Contrary to its prose,
	// this upstream revision restricts EOF searches to the final region.
	transforms := []func(string) string{func(s string) string { return s }, func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }, strings.TrimSpace, normalizePunctuation}
	for _, normalize := range transforms {
		for i := start; i <= len(lines)-len(pattern); i++ {
			matched := true
			for j, s := range pattern {
				if normalize(lines[i+j]) != normalize(s) {
					matched = false
					break
				}
			}
			if matched {
				return i
			}
		}
	}
	return -1
}
func normalizePunctuation(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case 0x2010, 0x2011, 0x2012, 0x2013, 0x2014, 0x2015, 0x2212:
			return '-'
		case 0x2018, 0x2019, 0x201a, 0x201b:
			return 39
		case 0x201c, 0x201d, 0x201e, 0x201f:
			return 34
		case 0x00a0, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a, 0x202f, 0x205f, 0x3000:
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
}
