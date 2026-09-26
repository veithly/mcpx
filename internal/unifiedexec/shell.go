// Derived from OpenAI Codex, codex-rs/core/src/shell.rs and
// codex-rs/shell-command/src/shell_detect.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && (k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			return v
		}
	}
	return ""
}

func defaultShell(ctx context.Context, env []string, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if shell := envValue(env, "SHELL"); shell != "" {
		return resolveShell(shell, env, dir)
	}
	candidates := []string{"bash", "/bin/bash", "zsh", "/bin/zsh", "/bin/sh"}
	if runtime.GOOS == "darwin" {
		candidates = []string{"zsh", "/bin/zsh", "bash", "/bin/bash", "/bin/sh"}
	}
	if runtime.GOOS == "windows" {
		candidates = []string{"pwsh.exe", "powershell.exe", envValue(env, "COMSPEC"), "cmd.exe"}
	}
	for _, candidate := range candidates {
		if path, err := resolveShell(candidate, env, dir); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("unifiedexec: no supported shell; set SHELL in Options.Env")
}

func resolveShell(shell string, env []string, dir string) (string, error) {
	if _, err := shellArgs(shell, "", false); err != nil {
		return "", err
	}
	if filepath.IsAbs(shell) || strings.ContainsAny(shell, `/\`) {
		if !filepath.IsAbs(shell) {
			shell = filepath.Join(dir, shell)
		}
		if executable(shell) {
			return shell, nil
		}
	} else {
		names := []string{shell}
		if runtime.GOOS == "windows" && filepath.Ext(shell) == "" {
			names = append(names, shell+".exe")
		}
		for _, path := range filepath.SplitList(envValue(env, "PATH")) {
			// Match Go's ErrDot protection; never find a shell through cwd.
			if !filepath.IsAbs(path) {
				continue
			}
			for _, name := range names {
				if full := filepath.Join(path, name); executable(full) {
					return full, nil
				}
			}
		}
	}
	return "", fmt.Errorf("unifiedexec: shell %q is not executable in the supplied environment", shell)
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0)
}

func shellArgs(shell, command string, login bool) ([]string, error) {
	if strings.ContainsRune(shell, 0) {
		return nil, fmt.Errorf("unifiedexec: invalid shell")
	}
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe") {
	case "sh", "bash", "zsh":
		flag := "-c"
		if login {
			flag = "-lc"
		}
		return []string{flag, command}, nil
	case "powershell", "pwsh":
		args := []string{}
		if !login {
			args = append(args, "-NoProfile")
		}
		return append(args, "-Command", command), nil
	case "cmd":
		return []string{"/c", command}, nil
	default:
		return nil, fmt.Errorf("unifiedexec: unsupported shell %q", shell)
	}
}
