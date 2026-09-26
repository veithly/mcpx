package server

import (
	"strings"
	"unicode/utf8"
)

const cleanDiffTotalPreviewMaxBytes = 64 << 10

type diffPreviewResult struct {
	Text      string
	Truncated bool
}

func boundedDiffPreview(diff string, maxBytes int) diffPreviewResult {
	if diff == "" || maxBytes <= 0 {
		return diffPreviewResult{}
	}
	if len(diff) <= maxBytes {
		return diffPreviewResult{Text: diff}
	}
	limit := maxBytes
	if limit > len(diff) {
		limit = len(diff)
	}
	// Keep the preview at a complete UTF-8 and diff-line boundary. A very
	// long single line falls back to the last valid UTF-8 boundary.
	cut := limit
	for cut > 0 && !utf8.ValidString(diff[:cut]) {
		cut--
	}
	if newline := strings.LastIndexByte(diff[:cut], '\n'); newline >= 0 {
		cut = newline + 1
	}
	if cut == 0 {
		return diffPreviewResult{Text: "[diff preview unavailable]\n", Truncated: true}
	}
	text := diff[:cut]
	marker := "... [diff truncated]\n"
	if len(text)+len(marker) <= maxBytes {
		text += marker
	}
	return diffPreviewResult{Text: text, Truncated: true}
}
