// Derived from OpenAI Codex, codex-rs/core/src/unified_exec/process.rs and
// codex-rs/utils/pty/src/{pipe,pty,process}.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type session struct {
	id        int
	cmd       *exec.Cmd
	input     *os.File
	reader    *os.File
	kill      func() error
	killOnce  sync.Once
	killErr   error
	running   atomic.Bool
	readGate  chan struct{}
	writeGate chan struct{}
	changed   chan struct{}
	done      chan struct{}
	retired   bool // readGate
	mu        sync.Mutex
	pending   *outputBuffer
	finished  bool
	exitCode  int
	err       error
}

func startSession(cmd *exec.Cmd, tty bool, id int) (*session, error) {
	reader, input, kill, err := spawn(cmd, tty)
	if err != nil {
		return nil, err
	}
	s := &session{id: id, cmd: cmd, input: input, reader: reader, kill: kill,
		readGate: make(chan struct{}, 1), writeGate: make(chan struct{}, 1),
		changed: make(chan struct{}, 1), done: make(chan struct{}), pending: newOutputBuffer(outputBytesCap / 4)}
	s.running.Store(true)
	readDone := make(chan struct{})
	go func() { defer close(readDone); s.readOutput() }()
	go s.reap(readDone)
	return s, nil
}

func (s *session) terminate() error {
	s.killOnce.Do(func() { s.killErr = s.kill() })
	return s.killErr
}

func (s *session) reap(readDone <-chan struct{}) {
	err := s.cmd.Wait()
	code := exitCode(s.cmd.ProcessState)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = nil
	} // Nonzero exit is a result, not a transport failure.
	// Kill descendants even when the shell exits before them. They must not
	// retain output descriptors or keep running after their handle is retired.
	err = errors.Join(err, s.terminate())
	timer := time.NewTimer(250 * time.Millisecond)
	select {
	case <-readDone:
	case <-timer.C:
		_ = s.reader.Close()
		<-readDone
	}
	timer.Stop()
	_ = s.reader.Close()
	if s.input != nil && s.input != s.reader {
		_ = s.input.Close()
	}
	s.mu.Lock()
	s.exitCode, s.finished = code, true
	s.err = errors.Join(s.err, err)
	s.mu.Unlock()
	s.running.Store(false)
	close(s.done)
}

func (s *session) readOutput() {
	buf := make([]byte, 32*1024)
	var carry []byte
	for {
		n, err := s.reader.Read(buf)
		data := append(carry, buf[:n]...)
		end := len(data)
		if err == nil {
			// Hold at most three bytes so a rune split across reads/polls is
			// delivered once, intact. Invalid complete bytes are replaced later.
			for i := max(0, end-utf8.UTFMax+1); i < end; i++ {
				if utf8.RuneStart(data[i]) && !utf8.FullRune(data[i:]) {
					end = i
					break
				}
			}
		}
		if end > 0 {
			s.mu.Lock()
			s.pending.append(data[:end])
			s.mu.Unlock()
			select {
			case s.changed <- struct{}{}:
			default:
			}
		}
		carry = append([]byte(nil), data[end:]...)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) && !terminalEOF(err) {
				s.mu.Lock()
				s.err = fmt.Errorf("unifiedexec: read output: %w", err)
				s.mu.Unlock()
			}
			return
		}
	}
}

func (s *session) write(ctx context.Context, chars []byte, deadline time.Time) error {
	if s.input == nil {
		return errors.New("unifiedexec: stdin is closed; start with tty=true to send input")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	select {
	case s.writeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("unifiedexec: process exited")
	case <-timer.C:
		return errors.New("unifiedexec: stdin write deadline exceeded")
	}
	defer func() { <-s.writeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.input.SetWriteDeadline(deadline); err != nil {
		return err
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = s.input.SetWriteDeadline(time.Now())
		close(cancelDone)
	})
	n, err := s.input.Write(chars)
	if !stop() {
		<-cancelDone
	}
	_ = s.input.SetWriteDeadline(time.Time{})
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("unifiedexec: stdin accepted %d of %d bytes; not replayed: %w", n, len(chars), err)
	}
	return nil
}
