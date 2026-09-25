package edit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementRoundTripNormalizesLogicalNewlines(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		t.Run(newline, func(t *testing.T) {
			dir := t.TempDir()
			original := []byte("first" + newline + "old" + newline)
			path := filepath.Join(dir, "demo.txt")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ApplyBatch(BatchRequest{WorkspaceRoot: dir, Edits: []FileEdit{{Path: "demo.txt", Operation: OpUpdate, BaseSHA256: hashBytes(original), Replacements: []Replacement{{Match: "first\r\nold\r\n", Replacement: "first\r\nnew\r\n"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if want := "first" + newline + "new" + newline; string(got) != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
