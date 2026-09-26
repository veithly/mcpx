// Derived from OpenAI Codex, codex-rs/core/src/unified_exec/{mod,process,process_manager}.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const outputBytesCap = 1024 * 1024
const maxSessions = 64

var ErrClosed = errors.New("unifiedexec: client is closed")
var ErrUnsupported = errors.New("unifiedexec: unsupported on this platform")

// Client owns native children. New's context bounds initialization only; Close
// ends their lifetime. Request contexts cancel waits, never replay commands.
type Client struct {
	mu        sync.Mutex
	sessions  map[int]*session
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	workDir   string
	env       []string
	shell     string
}

func New(ctx context.Context, opts Options) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := opts.WorkDir
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("unifiedexec: workdir is not a directory")
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	env = slices.Clone(env)
	// A non-nil empty environment must not silently inherit the host's secrets.
	if env == nil {
		env = []string{}
	}
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return nil, errors.New("unifiedexec: invalid environment entry")
		}
	}
	shell, err := defaultShell(ctx, env, dir)
	if err != nil {
		return nil, err
	}
	return &Client{sessions: make(map[int]*session), done: make(chan struct{}), workDir: dir, env: env, shell: shell}, nil
}

// HasRunning and ActiveSessions are snapshots, not close-admission locks.
func (c *Client) HasRunning() bool { return len(c.ActiveSessions()) != 0 }

func (c *Client) ActiveSessions() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]int, 0, len(c.sessions))
	for id, s := range c.sessions {
		if s.running.Load() {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.done)
		sessions := make([]*session, 0, len(c.sessions))
		for _, s := range c.sessions {
			sessions = append(sessions, s)
		}
		c.mu.Unlock()
		// Signal all children first; each waiter drains output and reaps its child.
		for _, s := range sessions {
			c.closeErr = errors.Join(c.closeErr, s.terminate())
		}
		for _, s := range sessions {
			<-s.done
		}
		c.mu.Lock()
		clear(c.sessions)
		c.mu.Unlock()
	})
	return c.closeErr
}
