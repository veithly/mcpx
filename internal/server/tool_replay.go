package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

// Network-interruption tolerance for tool calls: a dropped tunnel cancels the
// client's request context mid-call, which used to abort in-flight work and
// leave the outcome unknown. Tools now run detached and finish recording, and
// an identical retry within toolReplayTTL receives the recorded outcome with a
// replay marker instead of racing a duplicate execution. Re-executing on
// purpose requires the caller to set rerun=true.

const toolReplayTTL = 10 * time.Minute
const toolReplayMaxEntries = 512

// replayDigestExcluded arguments differ between a call and its reconnect retry
// without changing the executed effect, so they are excluded from the replay
// identity. rerun must always be excluded: it opts out of replay entirely.
var replayDigestExcluded = map[string]bool{
	"purpose": true, "activity": true, "intent": true,
	"acknowledge_requests": true, "rerun": true,
}

type replayEntry struct {
	done        chan struct{}
	result      *mcp.CallToolResult
	interrupted bool // the attempt's client connection dropped before completion
	completedAt time.Time
}

type toolReplayCache struct {
	mu      sync.Mutex
	entries map[string]*replayEntry
	order   []string
}

// begin registers a running entry, or returns the existing in-flight entry so
// concurrent identical calls can wait on one execution.
func (c *toolReplayCache) begin(key string) *replayEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*replayEntry{}
	}
	if existing, ok := c.entries[key]; ok {
		select {
		case <-existing.done:
		default:
			return existing
		}
	}
	entry := &replayEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.pruneLocked()
	return entry
}

func (c *toolReplayCache) finish(entry *replayEntry, result *mcp.CallToolResult, interrupted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry == nil {
		return
	}
	entry.result = result
	entry.interrupted = interrupted
	entry.completedAt = time.Now()
	close(entry.done)
}

func (c *toolReplayCache) get(key string) *replayEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	select {
	case <-entry.done:
		if time.Since(entry.completedAt) > toolReplayTTL {
			c.removeLocked(key)
			return nil
		}
	default:
	}
	return entry
}

func (c *toolReplayCache) removeLocked(key string) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// pruneLocked bounds memory: expired or oldest finished entries are dropped
// first; in-flight work is never evicted.
func (c *toolReplayCache) pruneLocked() {
	for len(c.entries) > toolReplayMaxEntries {
		oldest := ""
		for _, key := range c.order {
			entry, ok := c.entries[key]
			if !ok {
				continue
			}
			select {
			case <-entry.done:
				if time.Since(entry.completedAt) > toolReplayTTL {
					oldest = key
				} else if oldest == "" {
					oldest = key
				}
			default:
				continue
			}
			if time.Since(entry.completedAt) > toolReplayTTL {
				break
			}
		}
		if oldest == "" {
			return
		}
		c.removeLocked(oldest)
	}
}

func (r *Runtime) toolReplayKey(name string, arguments map[string]any) string {
	session, _ := arguments["remote_session_id"].(string)
	workspace, _ := arguments["workspace"].(string)
	filtered := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if !replayDigestExcluded[key] {
			filtered[key] = value
		}
	}
	canonical, err := json.Marshal(filtered)
	if err != nil {
		canonical = []byte("{}")
	}
	sum := sha256.Sum256([]byte(name + "\x00" + session + "\x00" + workspace + "\x00" + string(canonical)))
	return hex.EncodeToString(sum[:])
}

// replayDeliver resolves an identical retry of a previous attempt. ok=true
// means the returned result must be delivered without executing: the earlier
// attempt was interrupted by the client's connection dropping, ran to
// completion, and its recorded outcome is authoritative. ok=false means the
// call should execute (and its replay entry has been registered).
func (r *Runtime) replayDeliver(clientCtx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, bool) {
	if value, _ := arguments["rerun"].(bool); value {
		return nil, false
	}
	key := r.toolReplayKey(name, arguments)
	entry := r.toolReplays.get(key)
	if entry == nil {
		r.toolReplays.begin(key)
		return nil, false
	}
	select {
	case <-entry.done:
	case <-clientCtx.Done():
		// This retry's connection is gone too; nothing we return is delivered.
		return nil, false
	}
	if !entry.interrupted || entry.result == nil {
		// The earlier attempt finished with its client attached, so an
		// identical call is a deliberate re-run, not a reconnect recovery.
		return nil, false
	}
	replayed := *entry.result
	mcpresult.MetaSet(&replayed, "replayed", true)
	mcpresult.MetaSet(&replayed, "replay_reason", "network interruption recovery; the original attempt completed after its connection was lost. To re-execute on purpose, resend identical arguments with rerun=true.")
	replayed.Content = append(replayed.Content, &mcp.TextContent{Text: "【网络中断回放】该调用在上次连接中断期间已执行完成，以上为服务端记录结果（10 分钟内有效）。若确要重新执行，请原样重发并把参数 rerun 设为 true。"})
	return &replayed, true
}

func (r *Runtime) toolReplayFinish(entry *replayEntry, result *mcp.CallToolResult, interrupted bool) {
	r.toolReplays.finish(entry, result, interrupted)
}
