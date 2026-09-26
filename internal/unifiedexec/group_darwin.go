// Derived from OpenAI Codex, codex-rs/utils/pty/src/process_group.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func killGroup(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	// macOS may deny killpg during PTY leader teardown. Upstream retries
	// individual group members. x/sys sysctl supplies the same membership
	// information without cgo/libproc or an external ps executable.
	members, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return fmt.Errorf("enumerate denied process group: %w", err)
	}
	var failures error
	for _, member := range members {
		pid := int(member.Proc.P_pid)
		if int(member.Eproc.Pgid) != pgid || pid <= 0 || member.Proc.P_stat == 5 {
			continue
		} // SZOMB
		current, err := syscall.Getpgid(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if current != pgid {
			continue
		} // PID could have been reused since enumeration.
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			failures = errors.Join(failures, fmt.Errorf("kill member %d: %w", pid, err))
		}
	}
	return failures
}
