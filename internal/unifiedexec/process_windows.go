//go:build windows

// Derived from OpenAI Codex, codex-rs/utils/pty/src/pipe.rs and win/job.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
// Suspended-start/job assignment follows MCPX internal/terminal/group_windows.go.
package unifiedexec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func spawn(cmd *exec.Cmd, tty bool) (reader, input *os.File, kill func() error, err error) {
	if tty {
		return nil, nil, nil, fmt.Errorf("%w: native Windows PTY", ErrUnsupported)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return nil, nil, nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	if err = cmd.Start(); err != nil {
		_ = reader.Close()
		return nil, nil, nil, err
	}
	fail := func(cause error) (*os.File, *os.File, func() error, error) {
		_ = cmd.Process.Kill()
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		_ = reader.Close()
		return nil, nil, nil, cause
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		return fail(err)
	}
	if err = resumeThread(uint32(cmd.Process.Pid)); err != nil {
		return fail(err)
	}
	kill = func() error {
		err := windows.TerminateJobObject(job, 1)
		closeErr := windows.CloseHandle(job)
		if err != nil {
			return err
		}
		return closeErr
	}
	return reader, nil, kill, nil
}

func resumeThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("unifiedexec: suspended thread not found: %w", err)
}

func terminalEOF(error) bool { return false }
func exitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}
