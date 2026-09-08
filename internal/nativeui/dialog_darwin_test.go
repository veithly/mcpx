package nativeui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: shows and cancels a real NSOpenPanel. No file is selected,
// no accessibility automation, protected-directory probing or TCC mutation.
func TestNativeMacDialogCancellation(t *testing.T) {
	if os.Getenv("MCPX_NATIVE_UI_QA") != "1" {
		t.Skip("set MCPX_NATIVE_UI_QA=1 in an interactive macOS login")
	}
	argv, err := DirectoryCommand("darwin")
	if err != nil {
		t.Fatal(err)
	}
	argv[len(argv)-1] = strings.Replace(argv[len(argv)-1], "var answer = panel.runModal;", `var timer = $.NSTimer.timerWithTimeIntervalRepeatsBlock(0.7, false, function() { panel.cancel(null); });
$.NSRunLoop.currentRunLoop.addTimerForMode(timer, $.NSModalPanelRunLoopMode);
var answer = panel.runModal;`, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("native dialog: %v\n%s", err, output)
	}
	var result struct{ Cancelled bool }
	// Cocoa may log diagnostics to stderr; the JSON protocol is the last line.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if err = json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil || !result.Cancelled {
		t.Fatalf("cancel response=%s err=%v", output, err)
	}
}
