package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/logging"
	"mcpx/internal/mcpresult"
)

// Network-interruption tolerance for tool calls: a dropped tunnel cancels the
// client's request context mid-call, which used to abort in-flight work and
// leave the outcome unknown. Tools now run detached and finish recording, and
// an identical retry of an interrupted call within toolReplayTTL receives the
// recorded outcome. Normal completed calls do not memoize future work: a test
// rerun after changing its inputs is a new execution.
//
// Finished entries are also persisted to SQLite so the same retry after a
// process restart replays the recorded outcome instead of re-executing the
// side effects. Only interrupted outcomes are restored; a restart does not
// retroactively interrupt a call that already returned.

const toolReplayTTL = 10 * time.Minute
const toolReplayMaxEntries = 512

// toolReplayPersistTimeout bounds each durable replay write; the write runs
// detached from the response so a slow disk can never delay a tool reply.
const toolReplayPersistTimeout = 3 * time.Second

// replayDigestExcluded arguments differ between a call and its reconnect retry
// without changing the executed effect, so they are excluded from the replay
// identity.
// call_id/callId and confirmation_token identify a single transport attempt or
// confirmation round, so they never participate in the business identity.
var replayDigestExcluded = map[string]bool{
	"purpose": true, "activity": true, "intent": true,
	"acknowledge_requests": true,
	"call_id":              true, "callId": true, "confirmation_token": true,
}

type replayEntry struct {
	done        chan struct{}
	closeOnce   sync.Once
	result      *mcp.CallToolResult
	interrupted bool // the attempt's client connection dropped before completion
	completedAt time.Time

	// Identity for durable persistence. Written by the owning attempt right
	// after begin and read by the same goroutine's finish path, so no extra
	// synchronization is required.
	key       string
	tool      string
	session   string
	workspace string

	// restored marks an entry re-armed from SQLite after a process restart.
	restored bool
}

type toolReplayCache struct {
	mu      sync.Mutex
	entries map[string]*replayEntry
	order   []string
}

// begin registers a running entry, or joins the existing one. owned=false
// means another attempt owns the identity: the caller must never finish that
// entry, it may only wait for it and replay the recorded outcome. A finished
// replayable entry is returned unowned so the caller replays it.
func (c *toolReplayCache) begin(key string) (entry *replayEntry, owned bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*replayEntry{}
	}
	if existing, ok := c.entries[key]; ok {
		select {
		case <-existing.done:
			if existing.interrupted && existing.result != nil && time.Since(existing.completedAt) <= toolReplayTTL {
				return existing, false
			}
		default:
			return existing, false
		}
	}
	c.removeLocked(key)
	entry = &replayEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.pruneLocked()
	return entry, true
}

// beginFresh registers a new running entry, replacing a finished one so an
// operator-acknowledging call re-records the identity for later retries. An
// in-flight entry is never replaced: the caller must join it.
func (c *toolReplayCache) beginFresh(key string) (*replayEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*replayEntry{}
	}
	if existing, ok := c.entries[key]; ok {
		select {
		case <-existing.done:
		default:
			return existing, false
		}
	}
	c.removeLocked(key)
	entry := &replayEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.pruneLocked()
	return entry, true
}

// beginTracked registers an owned running entry and stamps the identity its
// recorded outcome needs for durable persistence after the process restarts.
func (c *toolReplayCache) beginTracked(key, tool string, arguments map[string]any) (*replayEntry, bool) {
	entry, owned := c.begin(key)
	if owned {
		entry.setIdentity(key, tool, arguments)
	}
	return entry, owned
}

// beginFreshTracked is beginTracked for an explicit re-execution identity.
func (c *toolReplayCache) beginFreshTracked(key, tool string, arguments map[string]any) (*replayEntry, bool) {
	entry, owned := c.beginFresh(key)
	if owned {
		entry.setIdentity(key, tool, arguments)
	}
	return entry, owned
}

func (e *replayEntry) setIdentity(key, tool string, arguments map[string]any) {
	e.key = key
	e.tool = tool
	e.session, _ = arguments["remote_session_id"].(string)
	e.workspace, _ = arguments["workspace"].(string)
}

// restore re-arms a persisted finished entry after a process restart. Restored
// entries were selected by loadToolReplays as interrupted outcomes.
func (c *toolReplayCache) restore(key string, result *mcp.CallToolResult, completedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*replayEntry{}
	}
	if _, ok := c.entries[key]; ok {
		return
	}
	entry := &replayEntry{
		done: make(chan struct{}), result: result, interrupted: true,
		completedAt: completedAt, restored: true,
	}
	close(entry.done)
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.pruneLocked()
}

// finish records the outcome exactly once and reports whether this call was
// the one that recorded it; a joining caller racing the owner must never be
// able to double-close the entry or overwrite its result.
func (c *toolReplayCache) finish(entry *replayEntry, result *mcp.CallToolResult, interrupted bool) bool {
	if entry == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	recorded := false
	entry.closeOnce.Do(func() {
		entry.result = result
		entry.interrupted = interrupted
		entry.completedAt = time.Now()
		close(entry.done)
		recorded = true
	})
	return recorded
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

// isTransparentProxyCall reports whether this call proxies an upstream MCP
// tool. Those calls carry their own persisted idempotency contract (including
// upstream revalidation and confirmation consumption), so the reconnect replay
// cache must neither short-circuit nor shadow them.
func isTransparentProxyCall(name string, req *mcp.CallToolRequest) bool {
	return name == "mcp_tool" && toolAction(req) == "call"
}

// isLiveToolQuery keeps current-state reads out of the effect-replay cache.
// A repeated poll or read is a new observation, including after a restart;
// exact historical results remain available through observe(request_ids).
func isLiveToolQuery(name string, req *mcp.CallToolRequest) bool {
	action := toolAction(req)
	switch name {
	case "read", "observe", "workspace", "environment_read", "runtime_read", "screenshot_capture":
		return true
	case "execute":
		return action == "attach"
	case "operation_manage":
		return action == "status" || action == "wait" || action == "result"
	case "artifact":
		return action == "list" || action == "read"
	case "plan":
		return action == "read"
	case "skill_tool", "mcp_tool":
		return action == "list" || action == "describe"
	case "session":
		// An explicit open without an ID creates a fresh Remote Session;
		// replaying a prior result can attach a different client to that session.
		return action == "list" || action == "" || action == "open"
	default:
		return false
	}
}

// replayDeliver resolves an identical retry of a previous attempt. It returns
// the entry this caller must finish (only when it registered a fresh one) plus
// delivered=true with a result that must be returned without executing:
// interrupted outcomes and concurrent identical calls join the single
// execution instead of racing duplicates. acknowledges marks operator
// requests: it is a new conversational step, so it always executes and
// re-records the identity.
func (r *Runtime) replayDeliver(ctx, clientCtx context.Context, name string, req *mcp.CallToolRequest, acknowledges bool) (entry *replayEntry, delivered bool, result *mcp.CallToolResult) {
	if isLiveToolQuery(name, req) {
		return nil, false, nil
	}
	arguments := mcpresult.Arguments(req)
	// The model may omit the session after transport binding. Resolve that
	// identity before deduplication, otherwise identical commands from distinct
	// workspaces share a replay key and can receive each other's result.
	if runtime, ok := runtimeContextFrom(ctx); ok && runtime.TransportSessionID != "" && strings.TrimSpace(stringPayload(arguments, "remote_session_id")) == "" {
		principal, err := r.principalFromContext(ctx)
		if err != nil {
			return nil, false, nil
		}
		if sessionID := r.boundRemoteSessionID(ctx, principal); sessionID != "" {
			arguments = cloneArguments(arguments)
			arguments["remote_session_id"] = sessionID
		}
	}
	if name == "operation_batch" && stringPayload(arguments, "operation_id") != "" {
		// The operation store owns this stable submission identity and returns
		// its current durable state, including across process restarts.
		return nil, false, nil
	}
	key := r.toolReplayKey(name, arguments)
	if strings.TrimSpace(stringPayload(arguments, "idempotency_key")) != "" {
		// Keyed calls own a persisted idempotency contract that survives
		// restarts and revalidates preflight; the in-memory replay cache must
		// not shadow it.
		return nil, false, nil
	}
	var owned bool
	if acknowledges {
		entry, owned = r.toolReplays.beginFreshTracked(key, name, arguments)
	} else {
		entry, owned = r.toolReplays.beginTracked(key, name, arguments)
	}
	if owned {
		return entry, false, nil
	}
	// Another attempt owns this identity: wait for its outcome instead of
	// re-executing. The wait is bounded by the worker hard limit; a real
	// client disconnect ends this retry's participation early.
	select {
	case <-entry.done:
	case <-clientCtx.Done():
		return nil, true, inFlightReplayFailure(clientCtx, name, req)
	case <-ctx.Done():
		return nil, true, inFlightReplayFailure(ctx, name, req)
	}
	if entry.result == nil {
		// The finished entry carries no replayable outcome; execute fresh.
		if fresh, ok := r.toolReplays.beginTracked(key, name, arguments); ok {
			return fresh, false, nil
		}
		return nil, false, nil
	}
	return nil, true, r.replayedResult(entry)
}

// replayedResult copies a finished entry's recorded outcome with replay
// markers attached in both _meta and content, because not every host forwards
// metadata to its model. Meta and content are copied so the markers never leak
// into the entry's authoritative recorded outcome.
func (r *Runtime) replayedResult(entry *replayEntry) *mcp.CallToolResult {
	replayed := *entry.result
	if entry.result.Meta != nil {
		meta := make(mcp.Meta, len(entry.result.Meta))
		for key, value := range entry.result.Meta {
			meta[key] = value
		}
		replayed.Meta = meta
	}
	content := make([]mcp.Content, len(entry.result.Content))
	copy(content, entry.result.Content)
	replayed.Content = content
	var reason string
	switch {
	case entry.restored:
		reason = "service restart recovery; the interrupted attempt completed before the restart."
	case entry.interrupted:
		reason = "network interruption recovery; the original attempt completed after its response was interrupted."
	default:
		reason = "prior attempt already completed"
	}
	mcpresult.MetaSet(&replayed, "replayed", true)
	mcpresult.MetaSet(&replayed, "replay_reason", reason)
	replayed.Content = append(replayed.Content, &mcp.TextContent{Text: "【回放先前尝试】以上为同一在途或响应中断尝试的服务端记录结果，未重复执行。先核对记录的副作用；若明确需要新的执行，使用工具支持的新 idempotency_key。"})
	return &replayed
}

// inFlightReplayFailure is delivered (and recorded) when a retry waits on an
// identical in-flight attempt and gives up without it finishing. It must never
// fall back to execution: the still-running attempt owns the side effects.
func inFlightReplayFailure(ctx context.Context, name string, req *mcp.CallToolRequest) *mcp.CallToolResult {
	result := toolFailure(ctx, name, req, "TOOL_ATTEMPT_IN_FLIGHT",
		"An identical attempt is still running; this retry was not started and did not execute. Its outcome will be recorded, and an identical retry after it finishes replays the recorded result.", nil)
	if wire, ok := result.StructuredContent.(map[string]any); ok {
		if body, ok := wire["error"].(map[string]any); ok {
			if details, ok := body["details"].(map[string]any); ok {
				details["execution_state"] = "not_started"
				details["retry_hint"] = "Wait for the running attempt to finish and inspect its recorded outcome before submitting new work; do not duplicate in-flight side effects."
			}
		}
	}
	return result
}

// toolReplayFinish records the outcome in the in-memory cache and persists it
// off the response path so an identical retry after a process restart replays
// instead of re-executing. ctx is only mined for values via WithoutCancel; the
// write deliberately outlives the request.
func (r *Runtime) toolReplayFinish(ctx context.Context, entry *replayEntry, result *mcp.CallToolResult, interrupted bool) {
	if r == nil || !r.toolReplays.finish(entry, result, interrupted) {
		// A joining caller lost the race with the owner; the recorded outcome
		// (and its persisted row) stays authoritative.
		return
	}
	if entry == nil || result == nil || entry.key == "" || r.state == nil {
		return
	}
	// Marshal in the finishing goroutine: outer guards may still finalize the
	// result object in place after this returns, so the detached writer only
	// ever touches the already-extracted bytes.
	encoded, err := json.Marshal(result)
	if err != nil {
		logging.With("component", "tool_replay").Error("encode replay result failed", "tool", entry.tool, "error", err)
		return
	}
	completedAt := entry.completedAt
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	interruptedFlag := 0
	if interrupted {
		interruptedFlag = 1
	}
	// Close waits briefly for these writers before the database shuts down; a
	// write that misses the window only costs the post-restart replay.
	r.replayPersistWG.Add(1)
	go func() {
		defer r.replayPersistWG.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolReplayPersistTimeout)
		defer cancel()
		if err := r.saveToolReplay(ctx, entry.key, entry.tool, entry.session, entry.workspace, encoded, interruptedFlag, completedAt); err != nil {
			logging.With("component", "tool_replay").Error("persist replay entry failed",
				"tool", entry.tool, "error", err)
		}
	}()
}

// saveToolReplay upserts the finished entry and prunes rows older than the
// replay TTL so the table never grows past what a restart can restore.
func (r *Runtime) saveToolReplay(ctx context.Context, key, tool, session, workspace string, encoded []byte, interrupted int, completedAt time.Time) error {
	if _, err := r.state.DB().ExecContext(ctx, `INSERT INTO tool_replays
        (replay_digest, remote_session_id, workspace_name, tool_name, result_json, interrupted, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(replay_digest) DO UPDATE SET
          result_json = excluded.result_json,
          interrupted = excluded.interrupted,
          created_at = excluded.created_at
        WHERE excluded.created_at >= tool_replays.created_at`,
		key, session, workspace, tool, string(encoded), interrupted, completedAt.UTC().UnixMilli()); err != nil {
		return err
	}
	return r.pruneToolReplays(ctx)
}

// pruneToolReplays deletes durable replay rows whose TTL has lapsed.
func (r *Runtime) pruneToolReplays(ctx context.Context) error {
	cutoff := time.Now().Add(-toolReplayTTL).UTC().UnixMilli()
	_, err := r.state.DB().ExecContext(ctx, `DELETE FROM tool_replays WHERE created_at <= ?`, cutoff)
	return err
}

// loadToolReplays repopulates the in-memory cache from SQLite at construction
// so an identical retry after a restart replays the recorded outcome instead
// of re-executing side effects. Failures are logged, never fatal: the runtime
// degrades to the previous restart-loses-replays behavior.
func (r *Runtime) loadToolReplays(ctx context.Context) {
	if r == nil || r.state == nil {
		return
	}
	cutoff := time.Now().Add(-toolReplayTTL).UTC().UnixMilli()
	rows, err := r.state.DB().QueryContext(ctx, `SELECT replay_digest, result_json, created_at
        FROM tool_replays WHERE interrupted = 1 AND created_at > ? ORDER BY created_at DESC LIMIT ?`,
		cutoff, toolReplayMaxEntries)
	if err != nil {
		logging.With("component", "tool_replay").Error("restore replay cache failed", "error", err)
		return
	}
	defer rows.Close()
	restored := 0
	for rows.Next() {
		var key, resultJSON string
		var createdAt int64
		if err := rows.Scan(&key, &resultJSON, &createdAt); err != nil {
			logging.With("component", "tool_replay").Error("restore replay entry failed", "error", err)
			return
		}
		var result mcp.CallToolResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			continue
		}
		r.toolReplays.restore(key, &result, time.UnixMilli(createdAt).UTC())
		restored++
	}
	if err := rows.Err(); err != nil {
		logging.With("component", "tool_replay").Error("restore replay cache failed", "error", err)
		return
	}
	if restored > 0 {
		logging.With("component", "tool_replay").Info("restored replayable tool outcomes", "count", restored)
	}
	if err := r.pruneToolReplays(ctx); err != nil {
		logging.With("component", "tool_replay").Error("prune replay rows failed", "error", err)
	}
}
