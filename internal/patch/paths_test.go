package patch

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// These 112 cases now assert fixed native expectations without an oracle.
// Each target must reach file policy, and a rejection prevents every write.
func TestHeaderGrammarAndPolicy(t *testing.T) {
	wrappers := []struct{ start, end string }{
		{}, {"<<EOF\n", "\nEOF"}, {"<<'EOF'\n", "\nEOF"}, {"<<\"EOF\"\n", "\nprefixEOF"},
	}
	spaces := []string{"", " \t", "\v\f\r", "\u0085\u00a0", "\u1680\u2003", "\u2028\u2029", "\u202f\u205f\u3000"}
	for w, wrapper := range wrappers {
		for s, space := range spaces {
			for _, op := range []string{"add", "update", "delete", "move"} {
				t.Run(fmt.Sprintf("wrapper%d/space%d/%s", w, s, op), func(t *testing.T) {
					root := t.TempDir()
					seed := map[string]string{"target": "old\n"}
					paths := []string{"target"}
					var body string
					switch op {
					case "add":
						body = space + "*** Add File: target" + space + "\n+new\n"
					case "update":
						body = space + "*** Update File: target" + space + "\n@@" + space + "\n-old\n+new\n"
					case "delete":
						body = space + "*** Delete File: target" + space + "\n"
					case "move":
						body = space + "*** Update File: source" + space + "\n*** End of File" + space + "\n*** End of File\n*** Move to: target" + space + "\n@@\n-old\n+new\n"
						seed["source"] = "old\n"
						paths = []string{"source", "target"}
					}
					patch := wrapper.start + space + "*** Begin Patch" + space + "\n" + body + space + "*** End Patch" + space + wrapper.end
					assertParserPreflight(t, root, patch, seed, paths, "target")
				})
			}
		}
	}
}

func TestEmbeddedMarkersStayContent(t *testing.T) {
	// All marker-looking lines below are file contents, including a complete
	// embedded heredoc. No trimming may turn context/add/remove lines into paths.
	content := "<<'EOF'\n*** Begin Patch\n*** Add File: /outside-add\n*** Delete File: /outside-delete\n*** Update File: /outside-update\n*** Move to: /outside-move\n*** End Patch\nEOF\n"
	for _, prefix := range []string{" ", "+", "-"} {
		t.Run(fmt.Sprintf("prefix%q", prefix), func(t *testing.T) {
			root := t.TempDir()
			seed := map[string]string{"source": "old\n"}
			if prefix != "+" {
				seed["source"] = content + "old\n"
			}
			lines := prefix + strings.ReplaceAll(strings.TrimSuffix(content, "\n"), "\n", "\n"+prefix) + "\n"
			patch := "*** Begin Patch\n*** Update File: source\n@@\n" + lines + "-old\n+new\n*** Add File: final\n+done\n*** End Patch\n"
			assertParserPreflight(t, root, patch, seed, []string{"source", "final"}, "final")
		})
	}
}

func TestMalformedHeredocsAreErrors(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: target\n+new\n*** End Patch"
	for _, input := range []string{
		"apply_patch <<'EOF'\n" + patch + "\nEOF",
		"<<'EOF'\n<<'EOF'\n" + patch + "\nEOF\nEOF",
		patch + "\n" + patch,
		"<<OTHER\n" + patch + "\nOTHER",
	} {
		root := t.TempDir()
		got, err := Apply(context.Background(), Request{WorkspaceRoot: root, Input: input})
		if err != nil || got.ExitCode != 1 || !strings.HasPrefix(got.Output, "Invalid patch") || len(snapshot(t, root)) != 0 {
			t.Fatalf("syntax error changed or produced writes: %+v %v", got, err)
		}
	}
}
