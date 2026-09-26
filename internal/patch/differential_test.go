package patch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The sole external baseline entrypoint. Default tests never discover or start
// Codex. Explicit selection fails (rather than skipping) if the oracle fails.
func TestOptionalDifferential(t *testing.T) {
	binary := os.Getenv("MCPX_PATCH_DIFFERENTIAL_BINARY")
	if binary == "" {
		t.Skip("external baseline requires MCPX_PATCH_DIFFERENTIAL_BINARY")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	compare := func(t *testing.T, input string, seed map[string]string, mode FileUpdateMode) {
		t.Helper()
		root := t.TempDir()
		writeFiles(t, root, seed)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "--codex-run-as-apply-patch", input)
		cmd.Dir = root
		preserve := "0"
		if mode == PreserveLineEndings {
			preserve = "1"
		}
		cmd.Env = append(os.Environ(), "CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS="+preserve)
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatalf("oracle failed: %v %s", err, output)
		}
		wantFiles := snapshot(t, root)
		resetFiles(t, root, seed)
		got, err := Apply(ctx, Request{WorkspaceRoot: root, Input: input, UpdateMode: mode})
		if err != nil || got.ExitCode != cmd.ProcessState.ExitCode() || got.Output != string(output) || !reflect.DeepEqual(snapshot(t, root), wantFiles) {
			t.Fatalf("native=%+v err=%v oracle exit=%d output=%q files native=%q oracle=%q", got, err, cmd.ProcessState.ExitCode(), output, snapshot(t, root), wantFiles)
		}
	}
	for _, tc := range semanticCases() {
		t.Run("semantics/"+tc.name, func(t *testing.T) {
			compare(t, wrap("*** Update File: f\n"+tc.body), map[string]string{"f": tc.seed}, NormalizeToLF)
		})
	}
	entries, err := os.ReadDir("testdata/scenarios")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, mode := range []FileUpdateMode{NormalizeToLF, PreserveLineEndings} {
			t.Run(fmt.Sprintf("scenario/%s/mode%d", entry.Name(), mode), func(t *testing.T) {
				dir := filepath.Join("testdata/scenarios", entry.Name())
				input, err := os.ReadFile(filepath.Join(dir, "patch.txt"))
				if err != nil {
					t.Fatal(err)
				}
				compare(t, string(input), snapshot(t, filepath.Join(dir, "input")), mode)
			})
		}
	}
	wrappers := [][2]string{{"", ""}, {"<<EOF\n", "\nEOF"}, {"<<'EOF'\n", "\nEOF"}, {"<<\"EOF\"\n", "\nprefixEOF"}}
	spaces := []string{"", " \t", "\v\f\r", "\u0085\u00a0", "\u1680\u2003", "\u2028\u2029", "\u202f\u205f\u3000"}
	for i, wrapper := range wrappers {
		for j, space := range spaces {
			for _, op := range []string{"add", "update", "delete", "move"} {
				t.Run(fmt.Sprintf("headers/%d/%d/%s", i, j, op), func(t *testing.T) {
					seed := map[string]string{"target": "old\n"}
					body := ""
					switch op {
					case "add":
						body = space + "*** Add File: target" + space + "\n+new\n"
					case "update":
						body = space + "*** Update File: target" + space + "\n@@" + space + "\n-old\n+new\n"
					case "delete":
						body = space + "*** Delete File: target" + space + "\n"
					case "move":
						seed["source"] = "old\n"
						body = space + "*** Update File: source" + space + "\n*** End of File" + space + "\n*** End of File\n*** Move to: target" + space + "\n@@\n-old\n+new\n"
					}
					compare(t, wrapper[0]+space+beginPatch+space+"\n"+body+space+endPatch+space+wrapper[1], seed, NormalizeToLF)
				})
			}
		}
	}
	for i, input := range []string{"", "bad", wrap("*** Create File: f\n+x\n"), wrap("*** Update File: f\n@@\n"), wrap("*** Environment ID: \n"), wrap("*** Add File: made\n+done\n*** Update File: f\n@@\n-missing\n+new\n"), wrap("*** Update File: f\n*** Move to: f\n@@\n-old\n+new\n"), wrap("*** Add File: large\n+" + strings.Repeat("xyz", 10000) + "\n")} {
		t.Run(fmt.Sprintf("additional/%d", i), func(t *testing.T) { compare(t, input, map[string]string{"f": "old\n"}, NormalizeToLF) })
	}
}
