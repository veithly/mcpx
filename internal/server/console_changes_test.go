package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleChangesShowsCommandCreatedFile(t *testing.T) {
	rt, sessionID, root, call := programmingFixture(t)
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init")
	git("config", "user.name", "MCPX Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-m", "baseline")
	result := call("exec_command", map[string]any{"cmd": "printf 'after\\n' > tracked.txt; printf 'new\\n' > created.txt", "yield_time_ms": 1000})
	if result.IsError {
		t.Fatalf("file-writing command failed: %+v", result)
	}
	response := consoleLogin(t, rt).request(t, "GET", "changes?workspace=project&session_id="+sessionID, nil)
	if response.Code != 200 {
		t.Fatalf("changes endpoint %d: %s", response.Code, response.Body.String())
	}
	var report struct {
		Entries []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"entries"`
		UnifiedDiff string `json:"unified_diff"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, item := range report.Entries {
		states[item.Path] = item.Status
	}
	if states["tracked.txt"] != "modified" || states["created.txt"] != "untracked" || !strings.Contains(report.UnifiedDiff, "+after") {
		t.Fatalf("command file changes missing: states=%+v diff=%q", states, report.UnifiedDiff)
	}
}
