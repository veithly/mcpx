// Derived from OpenAI Codex, codex-rs/core/src/unified_exec/process_manager.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var nextSession atomic.Int64
var nextChunk atomic.Uint64

func (c *Client) Exec(ctx context.Context, request ExecRequest) (ExecResult, error) {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	if strings.TrimSpace(request.Cmd) == "" || strings.ContainsRune(request.Cmd, 0) {
		return ExecResult{}, errors.New("unifiedexec: cmd must be nonempty and contain no NUL")
	}
	if err := validateLimits(request.YieldTimeMS, request.MaxOutputTokens); err != nil {
		return ExecResult{}, err
	}
	dir := c.workDir
	if request.WorkDir != "" {
		dir = request.WorkDir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(c.workDir, dir)
		}
	}
	shell := c.shell
	if request.Shell != "" {
		var err error
		shell, err = resolveShell(request.Shell, c.env, dir)
		if err != nil {
			return ExecResult{}, err
		}
	}
	args, err := shellArgs(shell, request.Cmd, request.Login == nil || *request.Login)
	if err != nil {
		return ExecResult{}, err
	}
	cmd := exec.Command(shell, args...)
	cmd.Dir, cmd.Env = dir, c.env
	// Hold admission only through Start: Close cannot miss an in-flight child.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ExecResult{}, ErrClosed
	}
	if len(c.sessions) >= maxSessions {
		c.mu.Unlock()
		return ExecResult{}, errors.New("unifiedexec: session limit reached; collect completed sessions or close this client")
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return ExecResult{}, err
	}
	s, err := startSession(cmd, request.TTY, int(nextSession.Add(1)))
	if err == nil {
		c.sessions[s.id] = s
	}
	c.mu.Unlock()
	if err != nil {
		return ExecResult{}, fmt.Errorf("unifiedexec: start: %w", err)
	}
	yield := request.YieldTimeMS
	if yield == 0 {
		yield = 10000
	}
	return c.collect(ctx, s, start, time.Duration(clamp(yield, 250, 30000))*time.Millisecond, request.MaxOutputTokens)
}

func (c *Client) Write(ctx context.Context, request WriteRequest) (ExecResult, error) {
	start := time.Now()
	if err := validateLimits(request.YieldTimeMS, request.MaxOutputTokens); err != nil {
		return ExecResult{}, err
	}
	c.mu.Lock()
	s, closed := c.sessions[request.SessionID], c.closed
	c.mu.Unlock()
	if closed {
		return ExecResult{}, ErrClosed
	}
	if s == nil {
		return ExecResult{}, fmt.Errorf("unifiedexec: unknown session_id %d for this client", request.SessionID)
	}
	yield := request.YieldTimeMS
	if request.Chars == "" {
		yield = clamp(yield, 5000, 300000)
	} else {
		yield = clamp(yield, 250, 30000)
	}
	wait := time.Duration(yield) * time.Millisecond
	if request.Chars != "" {
		// Never put stdin behind the read gate: input may be what wakes a poll.
		if err := s.write(ctx, []byte(request.Chars), start.Add(wait)); err != nil {
			return ExecResult{SessionID: &s.id, WallTimeSeconds: time.Since(start).Seconds()}, err
		}
	}
	return c.collect(ctx, s, start, wait, request.MaxOutputTokens)
}

func (c *Client) collect(ctx context.Context, s *session, start time.Time, wait time.Duration, tokens int) (ExecResult, error) {
	result := ExecResult{ChunkID: fmt.Sprintf("%x", nextChunk.Add(1)), SessionID: &s.id}
	timer := time.NewTimer(max(time.Until(start.Add(wait)), 0))
	defer timer.Stop()
	select {
	case s.readGate <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	case <-c.done:
		return result, ErrClosed
	case <-timer.C:
		result.WallTimeSeconds = time.Since(start).Seconds()
		return result, nil
	}
	defer func() { <-s.readGate }()
	if s.retired {
		return result, errors.New("unifiedexec: session output already collected")
	}
	output := newOutputBuffer(tokens)
	finish := func(err error) (ExecResult, error) {
		s.mu.Lock()
		output.merge(s.pending)
		s.pending = newOutputBuffer(outputBytesCap / 4)
		finished, code, processErr := s.finished, s.exitCode, s.err
		s.mu.Unlock()
		result.Output, result.OriginalTokenCount = output.result()
		result.WallTimeSeconds = time.Since(start).Seconds()
		if finished {
			result.SessionID, result.ExitCode = nil, &code
			s.retired = true
			c.mu.Lock()
			delete(c.sessions, s.id)
			c.mu.Unlock()
		}
		return result, errors.Join(err, processErr)
	}
	for {
		s.mu.Lock()
		output.merge(s.pending)
		s.pending = newOutputBuffer(outputBytesCap / 4)
		finished := s.finished
		s.mu.Unlock()
		if finished {
			return finish(nil)
		}
		select {
		case <-s.changed:
		case <-s.done:
			return finish(nil)
		case <-ctx.Done():
			return finish(ctx.Err())
		case <-c.done:
			return finish(ErrClosed)
		case <-timer.C:
			return finish(nil)
		}
	}
}

func validateLimits(yield, tokens int) error {
	if yield < 0 || tokens < 0 {
		return errors.New("unifiedexec: yield_time_ms and max_output_tokens must not be negative")
	}
	return nil
}
func clamp(value, low, high int) int { return min(max(value, low), high) }
