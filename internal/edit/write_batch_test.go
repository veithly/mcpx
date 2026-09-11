package edit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestBatchWriteFailureCarriesBoundary drives a commit-phase failure through
// the pre-write hook: after the first file is committed, the second file's
// parent directory is replaced by a regular file so its write fails. The
// returned error must preserve the failing entry and the exact
// written/unwritten boundary.
func TestBatchWriteFailureCarriesBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyBatchWithHook(BatchRequest{
		WorkspaceRoot: dir,
		Edits: []FileEdit{
			{Path: "a.txt", Operation: OpCreate, Content: "alpha\n"},
			{Path: "sub/b.txt", Operation: OpCreate, Content: "beta\n"},
		},
	}, func(BatchResult) error {
		// Sabotage the second write between hook and commit.
		if err := os.Remove(filepath.Join(dir, "sub")); err != nil {
			t.Errorf("remove sub: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub"), []byte("not a dir"), 0o644); err != nil {
			t.Errorf("write sub file: %v", err)
		}
		return nil
	})
	var boundary *BatchWriteError
	if !errors.As(err, &boundary) {
		t.Fatalf("expected BatchWriteError, got %v (%T)", err, err)
	}
	if boundary.FailedIndex != 1 || boundary.FailedPath != "sub/b.txt" {
		t.Fatalf("failed entry=%d/%q want 1/sub-b.txt", boundary.FailedIndex, boundary.FailedPath)
	}
	if len(boundary.AppliedPaths) != 1 || boundary.AppliedPaths[0] != "a.txt" {
		t.Fatalf("applied=%v want [a.txt]", boundary.AppliedPaths)
	}
	if len(boundary.PendingPaths) != 0 {
		t.Fatalf("pending=%v want empty", boundary.PendingPaths)
	}
	if boundary.Err == nil {
		t.Fatal("original write error must be preserved")
	}

	committed, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(committed) != "alpha\n" {
		t.Fatalf("first file must stay committed: %q err=%v", committed, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "sub", "b.txt")); statErr == nil {
		t.Fatal("pending file must not exist")
	}
}

func TestBatchWriteErrorUnwrapsOriginalCause(t *testing.T) {
	cause := errors.New("disk on fire")
	err := &BatchWriteError{FailedIndex: 2, FailedPath: "x.txt", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("BatchWriteError must unwrap to the original cause")
	}
	if err.Error() != "write x.txt: disk on fire" {
		t.Fatalf("message=%q", err.Error())
	}
}
