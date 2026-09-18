package terminal

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
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
