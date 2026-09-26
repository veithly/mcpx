// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Expectations derived from codex-rs/apply-patch/tests/fixtures/scenarios and
// src/{parser,streaming_parser,seek_sequence,file_update,text_file}.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; adapted for native Go tests.
package patch

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return files
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			to, err := os.Readlink(path)
			files[rel] = "symlink:" + to
			return err
		}
		data, err := os.ReadFile(path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
func resetFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, root, files)
}
func apply(t *testing.T, root, input string) Result {
	t.Helper()
	got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func wrap(body string) string { return beginPatch + "\n" + body + endPatch + "\n" }

func TestUpstreamScenarios(t *testing.T) {
	entries, err := os.ReadDir("testdata/scenarios")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			dir := filepath.Join("testdata/scenarios", entry.Name())
			root := t.TempDir()
			input, err := os.ReadFile(filepath.Join(dir, "patch.txt"))
			if err != nil {
				t.Fatal(err)
			}
			// The upstream runner explicitly selects preserve; run default mode too
			// except for its two fixtures specifically demonstrating preserve mode.
			for _, mode := range []FileUpdateMode{NormalizeToLF, PreserveLineEndings} {
				if mode == NormalizeToLF && (strings.HasPrefix(entry.Name(), "023_") || strings.HasPrefix(entry.Name(), "024_")) {
					continue
				}
				resetFiles(t, root, snapshot(t, filepath.Join(dir, "input")))
				got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: string(input), UpdateMode: mode})
				if err != nil {
					t.Fatal(err)
				}
				want := snapshot(t, filepath.Join(dir, "expected"))
				if !reflect.DeepEqual(snapshot(t, root), want) {
					t.Fatalf("mode=%d result=%+v files=%q want=%q", mode, got, snapshot(t, root), want)
				}
			}
		})
	}
}

func assertParserPreflight(t *testing.T, root, input string, seed map[string]string, paths []string, deny string) {
	t.Helper()
	resetFiles(t, root, seed)
	// Fixed native expectations, including the original 112 header cases.
	want := map[string]string{}
	for name, data := range seed {
		want[name] = data
	}
	output := "Success. Updated the following files:\n"
	switch {
	case strings.Contains(input, "*** Add File: final"):
		content := "<<'EOF'\n*** Begin Patch\n*** Add File: /outside-add\n*** Delete File: /outside-delete\n*** Update File: /outside-update\n*** Move to: /outside-move\n*** End Patch\nEOF\n"
		want["source"] = content + "new\n"
		if strings.Contains(input, "\n-<<'EOF'") {
			want["source"] = "new\n"
		}
		want["final"] = "done\n"
		output += "A final\nM source\n"
	case len(paths) == 2:
		delete(want, "source")
		want["target"] = "new\n"
		output += "M target\n"
	case strings.Contains(input, "*** Add File: target"):
		want["target"] = "new\n"
		output += "A target\n"
	case strings.Contains(input, "*** Update File: target"):
		want["target"] = "new\n"
		output += "M target\n"
	default:
		delete(want, "target")
		output += "D target\n"
	}
	checked := map[string]bool{}
	got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: input, ValidatePath: func(path string) error { checked[filepath.Base(path)] = true; return nil }})
	if err != nil || got.ExitCode != 0 || got.Output != output || !reflect.DeepEqual(got.Paths, paths) || !reflect.DeepEqual(snapshot(t, root), want) {
		t.Fatalf("result=%+v err=%v files=%q expected=%q", got, err, snapshot(t, root), want)
	}
	for _, path := range paths {
		if !checked[filepath.Base(path)] {
			t.Fatalf("path %q bypassed policy", path)
		}
	}
	resetFiles(t, root, seed)
	denied := errors.New("policy denial")
	got, err = Apply(context.Background(), Request{WorkspaceRoot: root, Input: input, ValidatePath: func(path string) error {
		if filepath.Base(path) == deny {
			return denied
		}
		return nil
	}})
	if !errors.Is(err, denied) || got.ExitCode != -1 || got.Output != "" || !reflect.DeepEqual(snapshot(t, root), seed) {
		t.Fatalf("denial wrote files: %+v %v", got, err)
	}
}

func TestSequentialTargetsAndWorkDirs(t *testing.T) {
	for _, work := range []string{"", ".", "sub", "$ROOT/sub"} {
		t.Run(work, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			input := wrap("*** Add File: a\n+one\n*** Update File: a\n@@\n-one\n+two\n*** Add File: b\n+overwritten\n*** Update File: a\n*** Move to: b\n@@\n-two\n+three\n*** Update File: b\n@@\n-three\n+four\n*** Delete File: b\n*** Add File: b\n+five\n")
			got, err := Apply(context.Background(), Request{WorkspaceRoot: root, WorkDir: strings.ReplaceAll(work, "$ROOT", root), Input: input})
			if err != nil || got.ExitCode != 0 {
				t.Fatalf("%+v %v", got, err)
			}
			path := "b"
			if strings.Contains(work, "sub") {
				path = "sub/b"
			}
			if !reflect.DeepEqual(snapshot(t, root), map[string]string{path: "five\n"}) {
				t.Fatal(snapshot(t, root))
			}
		})
	}
}

func TestLargePatchAndNoExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MCPX_CODEX_BINARY", "/does-not-exist")
	root := t.TempDir()
	line := strings.Repeat("中文'$\\\"", 100000)
	got := apply(t, root, wrap("*** Add File: large\n+"+line+"\n"))
	if got.ExitCode != 0 || snapshot(t, root)["large"] != line+"\n" {
		t.Fatalf("large patch failed: %+v", got)
	}
}

func TestPartialFailureAndInvalidPreflight(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"f": "actual\n"})
	got := apply(t, root, wrap("*** Add File: made\n+done\n*** Update File: f\n@@\n-absent\n+new\n"))
	if got.ExitCode != 1 || strings.Contains(got.Output, "Success") || snapshot(t, root)["made"] != "done\n" || snapshot(t, root)["f"] != "actual\n" {
		t.Fatalf("%+v files=%q", got, snapshot(t, root))
	}
	resetFiles(t, root, map[string]string{})
	got = apply(t, root, wrap("*** Add File: made\n+done\n*** Update File: f\n"))
	if got.ExitCode != 1 || len(snapshot(t, root)) != 0 || len(got.Paths) != 0 {
		t.Fatalf("invalid full parse wrote: %+v", got)
	}
}
