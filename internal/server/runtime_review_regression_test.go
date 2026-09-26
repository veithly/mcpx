package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewGitOutputRequiresConfirmation(t *testing.T) {
	rt, remote, root, call := programmingFixture(t)
	rt.cfg.Security.Commands.Default = "confirm"
	for name, body := range map[string]string{"left.txt": "a\n", "right.txt": "b\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"cmd": "git diff --no-index --output=probe.diff left.txt right.txt", "shell": "/bin/sh", "login": false, "yield_time_ms": 250}
	if result := call("exec_command", args); !result.IsError {
		t.Fatal("confirmation bypassed")
	}
	if _, err := os.Stat(filepath.Join(root, "probe.diff")); !os.IsNotExist(err) {
		t.Fatal("effect before approval")
	}
	pending := rt.approvals.ListRemoteSession(remote)
	if len(pending) != 1 {
		t.Fatal("approval absent")
	}
	if err := rt.control.Decide(context.Background(), pending[0].ID, pending[0].CommandDigest, "approved"); err != nil {
		t.Fatal(err)
	}
	call("exec_command", args)
	content, err := os.ReadFile(filepath.Join(root, "probe.diff"))
	if err != nil || len(content) == 0 {
		t.Fatal("approved effect missing")
	}
}

func TestReviewDelayedChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--review-delayed-child" {
			continue
		}
		if err := os.WriteFile(os.Args[i+1], []byte("started"), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(1500 * time.Millisecond)
		if err := os.WriteFile(os.Args[i+2], []byte("late"), 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
}

func TestReviewCancelStopsActualTaskAndFreezesTerminal(t *testing.T) {
	rt, remote, root, call := programmingFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	started, late := filepath.Join(root, "started"), filepath.Join(root, "late")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	command := quote(executable) + " -test.run=^TestReviewDelayedChild$ -- --review-delayed-child " + quote(started) + " " + quote(late)
	result := call("exec_command", map[string]any{"cmd": command, "shell": "/bin/sh", "login": false, "yield_time_ms": 250})
	if result.IsError {
		t.Fatal(result)
	}
	stopped, _, failures := (&consoleHandler{runtime: rt}).stopSession(context.Background(), "project", remote, "")
	if len(stopped) == 0 || len(failures) > 0 {
		t.Fatalf("stop=%v failures=%v", stopped, failures)
	}
	time.Sleep(1600 * time.Millisecond)
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatal("late process effect after console stop")
	}
}
