// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/{parser,streaming_parser,seek_sequence,
// file_update,text_file}.rs at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2.
// Native assertions adapted for MCPX, including documented default-mode quirks.
package patch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type semanticCase struct {
	name, seed, body, want string
	failure                bool
}

func semanticCases() []semanticCase {
	return []semanticCase{
		{"single", "old", "@@\n-old\n+new\n", "new\n", false},
		{"exact-before-fuzzy", " old \nold\n", "@@\n-old\n+new\n", " old \nnew\n", false},
		{"rstrip-before-trim", " old \nold \n", "@@\n-old\n+new\n", " old \nnew\n", false},
		{"trim", " old \n", "@@\n-old\n+new\n", "new\n", false},
		{"unicode", "“hello”—it’s\u00a0fine\n", "@@\n-\"hello\"-it's fine\n+new\n", "new\n", false},
		{"crlf", "one\r\ntwo\r\nthree\r\n", "@@\n-two\n+new\n", "one\r\nnew\nthree\r\n", false},
		{"all-crlf", "one\r\ntwo\r\n", "@@\n one\n-two\n+new\n", "one\nnew\n", false},
		{"eof-repeated", "old\nkeep\nold\n", "@@\n-old\n+new\n*** End of File\n", "old\nkeep\nnew\n", false},
		{"eof-no-fallback", "old\nkeep\n", "@@\n-old\n+new\n*** End of File\n", "old\nkeep\n", true},
		{"insert-eof", "one\n", "@@\n+two\n", "one\ntwo\n", false},
		{"insert-before-final-blank", "one\n\n", "@@\n+two\n", "one\ntwo\n", false},
		{"blank-sentinel", "one\n", "@@\n-one\n+two\n \n", "two\n", false},
		{"delete-all", "one\n", "@@\n-one\n", "", false},
		{"multiple", "section\nold\nkeep\nfooter\nold", "@@ section\n-old\n+new\n@@ footer\n-old\n+end\n", "section\nnew\nkeep\nfooter\nend\n", false},
		{"overlap-eof", "a\nb\n", "@@\n-b\n+x\n@@\n-b\n+y\n*** End of File\n", "a\nx\n", false},
		{"empty-source", "", "@@\n+new\n", "new\n", false},
		{"multiple-inserts", "old\n", "@@\n+one\n@@\n+two\n", "old\none\ntwo\n", false},
		{"missing-context", "old\n", "@@ missing\n-old\n+new\n", "old\n", true},
		{"empty-final-context", "old\n", "@@\n old\n\n+new\n", "old\n\nnew\n", false},
		{"bare-cr-source", "a\rb\r", "@@\n-a\n+x\n", "a\rb\r", true},
	}
}
func TestUpdateSemantics(t *testing.T) {
	for _, tc := range semanticCases() {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"f": tc.seed})
			got := apply(t, root, wrap("*** Update File: f\n"+tc.body))
			if (got.ExitCode != 0) != tc.failure || snapshot(t, root)["f"] != tc.want {
				t.Fatalf("result=%+v file=%q want=%q", got, snapshot(t, root)["f"], tc.want)
			}
		})
	}
}
func TestInvalidParserDiagnostics(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "Invalid patch: The last line of the patch must be '*** End Patch'\n"},
		{"bad", "Invalid patch: The first line of the patch must be '*** Begin Patch'\n"},
		{"*** Begin Patch\nbad", "Invalid patch: The last line of the patch must be '*** End Patch'\n"},
		{wrap("*** Update File: f\n"), "Invalid patch hunk on line 2: Update file hunk for path 'f' is empty\n"},
		{wrap("*** Update File: f\n@@\n"), "Invalid patch hunk on line 4: Update hunk does not contain any lines\n"},
		{wrap("*** Update File: f\n@@\n*** End of File\n"), "Invalid patch hunk on line 4: Update hunk does not contain any lines\n"},
		{wrap("*** Environment ID: \n"), "Invalid patch: apply_patch environment_id cannot be empty\n"},
		{wrap("*** Environment ID: a\n*** Environment ID: b\n"), "Invalid patch: apply_patch environment_id cannot be specified more than once\n"},
	} {
		root := t.TempDir()
		got := apply(t, root, tc.input)
		if got.ExitCode != 1 || got.Output != tc.want || len(snapshot(t, root)) != 0 {
			t.Fatalf("input=%q result=%+v want=%q", tc.input, got, tc.want)
		}
	}
}
func TestStreamingParserAtEveryBoundary(t *testing.T) {
	input := wrap("*** Environment ID: local\n*** Add File: a\n+世界\n*** Update File: f\n*** Move to: b\n@@ context\n-old\n+new\n*** End of File\n")
	for cut := 0; cut <= len(input); cut++ {
		p := streamingParser{}
		if err := p.push(input[:cut]); err != nil {
			t.Fatal(err)
		}
		if err := p.push(input[cut:]); err != nil {
			t.Fatal(err)
		}
		hunks, err := p.finish()
		if err != nil {
			t.Fatal(err)
		}
		if len(hunks) != 2 || hunks[0].contents.String() != "世界\n" || !hunks[1].chunks[0].eof || p.environmentID != "local" {
			t.Fatalf("cut=%d hunks=%+v", cut, hunks)
		}
	}
}
func TestRawCRLFPatchAndExplicitPreserve(t *testing.T) {
	for _, mode := range []FileUpdateMode{NormalizeToLF, PreserveLineEndings} {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"f": "one\r\ntwo\r\n"})
		input := strings.ReplaceAll(wrap("*** Update File: f\n@@\n one\n-two\n+new\n"), "\n", "\r\n")
		got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: input, UpdateMode: mode})
		want := "one\nnew\n"
		if mode == PreserveLineEndings {
			want = "one\r\nnew\r\n"
		}
		if err != nil || got.ExitCode != 0 || snapshot(t, root)["f"] != want {
			t.Fatalf("mode=%d result=%+v err=%v bytes=%q", mode, got, err, snapshot(t, root)["f"])
		}
	}
}
func TestPermissionsAndOverwrite(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"f": "old\n"})
	path := filepath.Join(root, "f")
	if err := os.Chmod(path, 0751); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"*** Add File: f\n+overwrite\n", "*** Update File: f\n@@\n-overwrite\n+new\n"} {
		got := apply(t, root, wrap(body))
		info, err := os.Stat(path)
		if err != nil || got.ExitCode != 0 || info.Mode().Perm() != 0751 {
			t.Fatalf("result=%+v info=%v err=%v", got, info, err)
		}
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	got := apply(t, root, wrap("*** Add File: f\n+denied\n"))
	if got.ExitCode != 1 || snapshot(t, root)["f"] != "new\n" {
		t.Fatalf("read-only overwrite: %+v", got)
	}
}
func TestMutationDuringPolicyAndCancellation(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"f": "old\n"})
	got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: wrap("*** Add File: made\n+done\n*** Update File: f\n@@\n-old\n+new\n"), ValidatePath: func(p string) error {
		if filepath.Base(p) == "f" {
			return os.WriteFile(p, []byte("user edit\n"), 0600)
		}
		return nil
	}})
	if err == nil || got.ExitCode != -1 || !reflect.DeepEqual(snapshot(t, root), map[string]string{"f": "user edit\n"}) {
		t.Fatalf("conflict lost: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = Apply(ctx, Request{WorkspaceRoot: root, Input: wrap("*** Add File: made\n+x\n")})
	if err != context.Canceled || got.ExitCode != -1 {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestManyHunks(t *testing.T) {
	var old, body, want strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&old, "old%d\n", i)
		fmt.Fprintf(&body, "@@\n-old%d\n+new%d\n", i, i)
		fmt.Fprintf(&want, "new%d\n", i)
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"f": old.String()})
	got := apply(t, root, wrap("*** Update File: f\n"+body.String()))
	if got.ExitCode != 0 || snapshot(t, root)["f"] != want.String() {
		t.Fatalf("many hunks failed: %+v", got)
	}
}

func TestEnvironmentSelection(t *testing.T) {
	for _, id := range []string{"local", "remote", "production"} {
		t.Run(id, func(t *testing.T) {
			root := t.TempDir()
			got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: wrap("*** Environment ID: " + id + "\n*** Add File: f\n+new\n")})
			if id == "local" {
				if err != nil || got.ExitCode != 0 || snapshot(t, root)["f"] != "new\n" {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "environment_id") || got.ExitCode != -1 || len(snapshot(t, root)) != 0 {
				t.Fatalf("unsupported environment wrote: %+v %v", got, err)
			}
		})
	}
}
