//go:build !windows

// Derived from OpenAI Codex, codex-rs/utils/pty/src/{pipe,pty,process_group}.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func spawn(cmd *exec.Cmd, tty bool) (reader, input *os.File, kill func() error, err error) {
	var writer *os.File
	if tty {
		reader, writer, err = pty.Open()
		if err != nil {
			return nil, nil, nil, err
		}
		// Register a nonblocking master with Go's poller. Deadlines and Close
		// must interrupt both reads and writes, including a full stdin queue.
		fd, dupErr := unix.Dup(int(reader.Fd()))
		_ = reader.Close()
		if dupErr != nil {
			_ = writer.Close()
			return nil, nil, nil, dupErr
		}
		unix.CloseOnExec(fd)
		if err = unix.SetNonblock(fd, true); err != nil {
			_ = unix.Close(fd)
			_ = writer.Close()
			return nil, nil, nil, err
		}
		reader = os.NewFile(uintptr(fd), "unifiedexec-pty")
		if err = pty.Setsize(writer, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
			_ = reader.Close()
			_ = writer.Close()
			return nil, nil, nil, err
		}
		input, cmd.Stdin = reader, writer
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	} else {
		reader, writer, err = os.Pipe()
		if err != nil {
			return nil, nil, nil, err
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// nil stdin is /dev/null in os/exec, exactly the pipe-no-stdin contract.
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	err = cmd.Start()
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return nil, nil, nil, err
	}
	pid := cmd.Process.Pid
	kill = func() error {
		err := killGroup(pid)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("unifiedexec: kill process group %d: %w", pid, err)
		}
		return err
	}
	return reader, input, kill, nil
}

func terminalEOF(err error) bool { return errors.Is(err, syscall.EIO) }
func exitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
