//go:build !darwin && !windows

// Derived from OpenAI Codex, codex-rs/utils/pty/src/process_group.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import "syscall"

func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }
