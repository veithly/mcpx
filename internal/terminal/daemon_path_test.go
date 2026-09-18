package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDirectProcessFallbackPreservesResolutionSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	primary, fallback := t.TempDir(), t.TempDir()
	for _, dir := range []string{primary, fallback} {
		if err := os.WriteFile(filepath.Join(dir, "probe"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	t.Setenv("PATH", primary)
	cmd := commandProcessWithFallback(ctx, "probe", []string{"literal; argument"}, []string{fallback})
	if cmd.Path != filepath.Join(primary, "probe") || cmd.Args[1] != "literal; argument" {
		t.Fatal("fallback overrode PATH or argv")
	}
	t.Setenv("PATH", t.TempDir())
	cmd = commandProcessWithFallback(ctx, "probe", nil, []string{fallback})
	if cmd.Err != nil || cmd.Path != filepath.Join(fallback, "probe") {
		t.Fatalf("fallback failed: %v", cmd.Err)
	}
	cmd = commandProcessWithFallback(ctx, "./probe", nil, []string{fallback})
	if cmd.Path != "./probe" {
		t.Fatal("explicit path was replaced")
	}
	t.Chdir(primary)
	t.Setenv("PATH", ".")
	cmd = commandProcessWithFallback(ctx, "probe", nil, []string{fallback})
	if !errors.Is(cmd.Err, exec.ErrDot) {
		t.Fatalf("ErrDot was bypassed: %v", cmd.Err)
	}
}

func TestDirectProcessFindsInstalledNodeWithDaemonPATH(t *testing.T) {
	if runtime.GOOS != "darwin" || (!executableFile("/opt/homebrew/bin/node") && !executableFile("/usr/local/bin/node")) {
		t.Skip("requires a macOS package-manager Node installation")
	}
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	manager := NewTaskManager()
	task, err := manager.StartRemoteProcessWithObservationContext("req", "call", "execute", "session", "demo", t.TempDir(), "node -",
		ProcessSpec{Executable: "node", Args: []string{"-"}, Stdin: "process.stdout.write('daemon-path-ok')", WallLimit: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !task.Wait(ctx) {
		t.Fatal("Node did not finish")
	}
	out, _ := task.LogsFor("stdout", 0)
	if out != "daemon-path-ok" || task.StatusView()["status"] != TaskExited {
		t.Fatalf("output=%q status=%v", out, task.StatusView())
	}
}
