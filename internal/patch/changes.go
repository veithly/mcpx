package patch

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const maxObservedFileBytes = 128 << 10
const maxObservedDiffBytes = 64 << 10
const maxObservedLines = 5000

// Change is an actual filesystem effect. Candidate paths in Result.Paths are
// insufficient because Codex patches apply sequentially and can fail midway.
type Change struct {
	Path          string
	Operation     string
	Diff          string
	DiffTruncated bool
}

type observedMutation struct {
	before, after   []byte
	existed, exists bool
}

type changeTracker struct {
	order   []string
	entries map[string]observedMutation
}

func newChangeTracker() *changeTracker {
	return &changeTracker{entries: make(map[string]observedMutation)}
}

func (t *changeTracker) record(path string, before []byte, existed bool, after []byte, exists bool) {
	path = filepath.ToSlash(path)
	item, ok := t.entries[path]
	if !ok {
		item = observedMutation{before: before, existed: existed}
		t.order = append(t.order, path)
	}
	item.after, item.exists = after, exists
	t.entries[path] = item
}

func (t *changeTracker) list() []Change {
	changes := make([]Change, 0, len(t.order))
	for _, path := range t.order {
		item := t.entries[path]
		if item.existed == item.exists && bytes.Equal(item.before, item.after) {
			continue
		}
		change := Change{Path: path, Operation: "update"}
		if !item.existed {
			change.Operation = "create"
		} else if !item.exists {
			change.Operation = "delete"
		}
		change.Diff, change.DiffTruncated = observedDiff(path, item)
		changes = append(changes, change)
	}
	return changes
}

func observedDiff(path string, item observedMutation) (string, bool) {
	if len(item.before) > maxObservedFileBytes || len(item.after) > maxObservedFileBytes ||
		bytes.Count(item.before, []byte{'\n'})+bytes.Count(item.after, []byte{'\n'}) > maxObservedLines ||
		!utf8.Valid(item.before) || !utf8.Valid(item.after) ||
		bytes.IndexByte(item.before, 0) >= 0 || bytes.IndexByte(item.after, 0) >= 0 {
		return "", true
	}
	from, to := "a/"+path, "b/"+path
	if !item.existed {
		from = "/dev/null"
	}
	if !item.exists {
		to = "/dev/null"
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: observedLines(item.before), B: observedLines(item.after),
		FromFile: from, ToFile: to, Context: 3,
	})
	if err != nil || len(diff) > maxObservedDiffBytes {
		return "", true
	}
	return diff, false
}

func observedLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
