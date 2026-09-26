package patch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeleteAndMoveUnlinkSymlinkOnly(t *testing.T) {
	for _, move := range []bool{false, true} {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"source": "old\n"})
		if err := os.Symlink("source", filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		body := "*** Delete File: alias\n"
		if move {
			body = "*** Update File: alias\n*** Move to: dest\n@@\n-old\n+new\n"
		}
		got := apply(t, root, wrap(body))
		want := map[string]string{"source": "old\n"}
		if move {
			want["dest"] = "new\n"
		}
		if got.ExitCode != 0 || !reflect.DeepEqual(snapshot(t, root), want) {
			t.Fatalf("symlink operation removed referent: %+v %q", got, snapshot(t, root))
		}
	}
}

func TestPathRejectionDoesNotWrite(t *testing.T) {
	for _, mode := range []string{"relative-escape", "absolute-escape", "absolute-dotdot", "sibling-prefix", "symlink-file", "symlink-directory", "dangling-symlink", "dangling-parent", "policy-add", "policy-update", "policy-delete", "policy-move-source", "policy-move-destination", "policy-move-after-eof", "policy-physical-target", "policy-lexical-alias", "workdir-escape"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			root, outside := filepath.Join(parent, "workspace"), filepath.Join(parent, "outside")
			writeFiles(t, root, map[string]string{"source": "old\n", "denied": "old\n"})
			writeFiles(t, outside, map[string]string{"victim": "untouched\n"})
			target := "denied"
			operation := "*** Add File: %s\n+changed\n"
			work := root
			link := func(name, to string) {
				if err := os.Symlink(to, filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "relative-escape":
				target = "../outside/victim"
			case "absolute-escape":
				target = filepath.Join(outside, "victim")
			case "absolute-dotdot":
				target = root + "/../outside/victim"
			case "sibling-prefix":
				target = root + "-other/victim"
			case "symlink-file":
				link("link", filepath.Join(outside, "victim"))
				target = "link"
			case "symlink-directory":
				link("link", outside)
				target = "link/new/nested"
			case "dangling-symlink":
				link("link", filepath.Join(outside, "missing"))
				target = "link"
			case "dangling-parent":
				link("link", filepath.Join(outside, "missing"))
				target = "link/child"
			case "policy-update":
				operation = "*** Update File: %s\n@@\n-old\n+changed\n"
			case "policy-delete":
				operation = "*** Delete File: %s\n"
			case "policy-move-source":
				operation = "*** Update File: %s\n*** Move to: destination\n@@\n-old\n+changed\n"
			case "policy-move-destination":
				operation = "*** Update File: source\n*** Move to: %s\n@@\n-old\n+changed\n"
			case "policy-move-after-eof":
				operation = "*** Update File: source\n*** End of File\n*** Move to: %s\n@@\n-old\n+changed\n"
			case "policy-physical-target":
				link("alias", "denied")
				target = "alias"
			case "policy-lexical-alias":
				if err := os.Remove(filepath.Join(root, "denied")); err != nil {
					t.Fatal(err)
				}
				link("denied", "source")
			case "workdir-escape":
				work = outside
			}
			patch := "*** Begin Patch\n*** Add File: must-not-exist\n+no\n" + strings.Replace(operation, "%s", target, 1) + "*** End Patch\n"
			before := snapshot(t, parent)
			denied := errors.New("denied by file policy")
			result, err := Apply(context.Background(), Request{WorkspaceRoot: root, WorkDir: work, Input: patch, ValidatePath: func(path string) error {
				if filepath.Base(path) == "denied" {
					return denied
				}
				return nil
			}})
			if err == nil || result.ExitCode != -1 || result.Output != "" {
				t.Fatalf("wanted preflight rejection, got %+v, %v", result, err)
			}
			if strings.HasPrefix(mode, "policy-") && !errors.Is(err, denied) {
				t.Fatalf("policy error lost: %v", err)
			}
			if !reflect.DeepEqual(before, snapshot(t, parent)) {
				t.Fatal("rejected patch changed files")
			}
		})
	}
}

func TestRechecksSymlinksAfterPolicy(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"allowed": "allowed\n", "denied": "denied\n"})
	link := filepath.Join(root, "link")
	if err := os.Symlink("allowed", link); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: "*** Begin Patch\n*** Add File: link\n+new\n*** End Patch\n", ValidatePath: func(path string) error {
		if filepath.Base(path) == "allowed" {
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("denied", link); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "changed during validation") || result.Output != "" {
		t.Fatalf("symlink swap was not rejected: %+v %v", result, err)
	}
	files := snapshot(t, root)
	if files["allowed"] != "allowed\n" || files["denied"] != "denied\n" {
		t.Fatal("symlink swap allowed a write")
	}
}
