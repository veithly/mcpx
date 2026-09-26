// Derived from OpenAI Codex, codex-rs/core/src/unified_exec/head_tail_buffer.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Like Codex's head/tail presentation, preserve context on both sides while
// bounding memory independently of command output volume. Tokens are estimated
// as four bytes each, with a 1 MiB ceiling matching unified_exec's buffer.
type outputBuffer struct {
	limit int
	total int
	head  []byte
	tail  []byte
}

func newOutputBuffer(tokens int) *outputBuffer {
	if tokens == 0 {
		tokens = 10000
	}
	return &outputBuffer{limit: min(tokens, 1024*1024/4) * 4}
}

func (b *outputBuffer) append(chunk []byte) {
	b.total += len(chunk)
	headLimit := (b.limit + 1) / 2
	n := min(len(chunk), headLimit-len(b.head))
	b.head = append(b.head, chunk[:n]...)
	chunk = chunk[n:]
	tailLimit := b.limit - headLimit
	if len(chunk) >= tailLimit {
		b.tail = append(b.tail[:0], chunk[len(chunk)-tailLimit:]...)
		return
	}
	if overflow := len(b.tail) + len(chunk) - tailLimit; overflow > 0 {
		b.tail = append(b.tail[:0], b.tail[overflow:]...)
	}
	b.tail = append(b.tail, chunk...)
}

func (b *outputBuffer) result() (string, *int) {
	if b.total <= b.limit {
		return strings.ToValidUTF8(string(append(b.head, b.tail...)), "�"), nil
	}
	head, tail := b.head, b.tail
	// Trim only an incomplete final rune; arbitrary invalid bytes elsewhere
	// are replaced below instead of causing quadratic full-prefix scans.
	if len(head) > 0 {
		start := len(head) - 1
		for start > 0 && !utf8.RuneStart(head[start]) {
			start--
		}
		if !utf8.FullRune(head[start:]) {
			head = head[:start]
		}
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	estimate := (b.total + 3) / 4
	marker := fmt.Sprintf("\n... [%d bytes omitted; original token count is an estimate] ...\n", b.total-len(head)-len(tail))
	return strings.ToValidUTF8(string(head)+marker+string(tail), "�"), &estimate
}

func (b *outputBuffer) merge(other *outputBuffer) {
	b.append(other.head)
	b.append(other.tail)
	b.total += other.total - len(other.head) - len(other.tail)
}
