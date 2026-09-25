package terminal

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func commandProcess(ctx context.Context, executable string, args ...string) *exec.Cmd {
	var fallbackDirs []string
	if runtime.GOOS == "darwin" {
		// launchd does not load interactive shell configuration. Search only
		// established absolute installation paths, after the caller's PATH.
		fallbackDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	}
	return commandProcessWithFallback(ctx, executable, args, fallbackDirs)
}

// commandEnvironment keeps executable lookup consistent for the process and
// its children, including /usr/bin/env shebangs under launchd. Apply it after
// cmd.Dir is set so Cmd.Environ also supplies the correct PWD.
func commandEnvironment(cmd *exec.Cmd) []string {
	env := cmd.Environ()
	if runtime.GOOS != "darwin" {
		return env
	}
	for i, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		paths := filepath.SplitList(strings.TrimPrefix(entry, "PATH="))
		for _, fallback := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			found := false
			for _, path := range paths {
				if path == fallback {
					found = true
					break
				}
			}
			if !found {
				paths = append(paths, fallback)
			}
		}
		env[i] = "PATH=" + strings.Join(paths, ":")
		return env
	}
	return append(env, "PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin")
}

func commandProcessWithFallback(ctx context.Context, executable string, args, fallbackDirs []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	if !errors.Is(cmd.Err, exec.ErrNotFound) || filepath.Base(executable) != executable {
		// Preserve explicit paths, PATH precedence, and exec.ErrDot protection.
		return cmd
	}
	for _, dir := range fallbackDirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, executable)
		if executableFile(candidate) {
			return exec.CommandContext(ctx, candidate, args...)
		}
	}
	return cmd
}
