package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

func replayTestArgs(command string) map[string]any {
	return map[string]any{"remote_session_id": "sess-1", "command": command}
}

func TestReplayDeliversInterruptedAttemptResult(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("npm run check")
	key := r.toolReplayKey("execute", args)
	entry, owned := r.toolReplays.begin(key)
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	if got := r.toolReplays.get(key); got != entry {
		t.Fatal("in-flight entry must be returned to identical lookups")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("exit 0"), true)

	_, delivered, replayed := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false)
	if !delivered || replayed == nil {
		t.Fatal("interrupted attempt must be replayed")
	}
	if replayed.Content[0].(*mcp.TextContent) == nil {
		t.Fatal("replayed content missing")
	}
	if value, _ := replayed.Meta["replayed"].(bool); !value {
		t.Fatal("replayed meta flag missing")
	}
}

func TestReplayExcludesNarrativeArgumentsFromIdentity(t *testing.T) {
	r := &Runtime{}
	base := replayTestArgs("npm run check")
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", base))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("exit 0"), true)

	retried := map[string]any{"remote_session_id": "sess-1", "command": "npm run check", "purpose": "复核构建是否修复"}
	if _, ok, _ := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(retried), false); !ok {
		t.Fatal("purpose-only differences must still resolve to the recorded attempt")
	}
}

func TestReplayKeyExcludesPerAttemptFields(t *testing.T) {
	r := &Runtime{}
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", replayTestArgs("npm run check")))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("exit 0"), true)

	for _, field := range []string{"call_id", "callId", "confirmation_token"} {
		retried := replayTestArgs("npm run check")
		retried[field] = "attempt-42"
		if _, ok, _ := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(retried), false); !ok {
			t.Fatalf("retry differing only in %q must hit the recorded attempt", field)
		}
	}
}

func TestReplayDeliversCompletedAttemptAndHonorsRerun(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("git status")
	entry, owned := r.toolReplays.begin(r.toolReplayKey("git", args))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("clean"), false)

	_, delivered, replayed := r.replayDeliver(context.Background(), context.Background(), "git", mcpresult.Request(args), false)
	if !delivered || replayed == nil {
		t.Fatal("a completed entry must replay even when its client stayed attached")
	}
	if reason, _ := replayed.Meta["replay_reason"].(string); reason != "prior attempt already completed" {
		t.Fatalf("replay_reason = %q, want prior attempt already completed", reason)
	}

	rerun := replayTestArgs("git status")
	rerun["rerun"] = true
	fresh, replaying, _ := r.replayDeliver(context.Background(), context.Background(), "git", mcpresult.Request(rerun), false)
	if replaying || fresh == nil {
		t.Fatal("rerun=true must opt out of replay and register a fresh owned entry")
	}
}

func TestReplaySkipsIdempotencyKeyedCalls(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("npm run check")
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", args))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("exit 0"), true)

	keyed := replayTestArgs("npm run check")
	keyed["idempotency_key"] = "retry-key"
	replayEntry, delivered, _ := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(keyed), false)
	if delivered || replayEntry != nil {
		t.Fatal("idempotency-keyed calls must bypass the replay cache and stay with their persisted idempotency store")
	}
}

func TestReplaySeparatesSessionsAndExpiry(t *testing.T) {
	r := &Runtime{}
	other := map[string]any{"remote_session_id": "sess-2", "command": "npm run check"}
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", other))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("ok"), true)

	sameCommandOtherSession := map[string]any{"remote_session_id": "sess-9", "command": "npm run check"}
	if _, ok, _ := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(sameCommandOtherSession), false); ok {
		t.Fatal("another session's recorded attempt must not be replayed")
	}

	entry.completedAt = time.Now().Add(-toolReplayTTL - time.Minute)
	if got := r.toolReplays.get(r.toolReplayKey("execute", other)); got != nil {
		t.Fatal("expired replay entry must be dropped")
	}
}

func TestReplayWaitsForInFlightAttempt(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("slow command")
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", args))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("late result"), true)
	}()
	_, delivered, replayed := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false)
	if !delivered || replayed == nil {
		t.Fatal("identical retry must wait for the in-flight attempt and replay it")
	}
}

func TestReplayClientDisconnectDuringWaitDoesNotReexecute(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("still running")
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", args))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	clientCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, delivered, inFlight := r.replayDeliver(context.Background(), clientCtx, "execute", mcpresult.Request(args), false)
	cancel()
	if !delivered || inFlight == nil || !inFlight.IsError {
		t.Fatalf("a disconnected waiter must record an in-flight failure, not re-execute: delivered=%v result=%+v", delivered, inFlight)
	}
	if entry.isDone() {
		t.Fatal("the owned in-flight entry must stay untouched for its owner")
	}
	// The owner still finishes its entry later; the recorded attempt wins.
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("owner outcome"), true)
	if _, ok, _ := r.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false); !ok {
		t.Fatal("a later identical retry must replay the owner's recorded outcome")
	}
}

func TestReplayConcurrentIdenticalCallsExecuteOnce(t *testing.T) {
	r := &Runtime{}
	var executions int32
	release := make(chan struct{})
	instrumented := r.instrumentTool("dup_check", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		atomic.AddInt32(&executions, 1)
		<-release
		return mcpresult.NewText("single execution"), nil
	})
	args := replayTestArgs("identical call")
	req := mcpresult.Request(args)

	first := make(chan *mcp.CallToolResult, 1)
	go func() {
		result, err := instrumented(context.Background(), req)
		if err != nil {
			t.Errorf("owner call returned error: %v", err)
		}
		first <- result
	}()
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&executions) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(&executions) != 1 {
		t.Fatal("owner execution never started")
	}
	second := make(chan *mcp.CallToolResult, 1)
	go func() {
		result, err := instrumented(context.Background(), req)
		if err != nil {
			t.Errorf("joining call returned error: %v", err)
		}
		second <- result
	}()
	time.Sleep(50 * time.Millisecond) // let the joiner reach its wait
	close(release)
	ownerResult := <-first
	joinerResult := <-second
	if atomic.LoadInt32(&executions) != 1 {
		t.Fatalf("concurrent identical calls executed %d times", executions)
	}
	if value, _ := joinerResult.Meta["replayed"].(bool); !value {
		t.Fatalf("joining call must replay the owner's outcome: %+v", joinerResult)
	}
	if value, _ := ownerResult.Meta["replayed"].(bool); value {
		t.Fatal("owner result must not be marked replayed")
	}
}

func TestReplayFinishIsIdempotent(t *testing.T) {
	r := &Runtime{}
	entry, owned := r.toolReplays.begin(r.toolReplayKey("execute", replayTestArgs("once")))
	if !owned {
		t.Fatal("fresh key must register an owned entry")
	}
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("first"), true)
	r.toolReplayFinish(context.Background(), entry, mcpresult.NewText("second"), false) // must not panic or overwrite
	if got := r.toolReplays.get(r.toolReplayKey("execute", replayTestArgs("once"))); got == nil {
		t.Fatal("finished entry must remain readable")
	} else if text := mcpresult.FirstText(got.result); text != "first" {
		t.Fatalf("late finish overwrote recorded result: %q", text)
	} else if !got.interrupted {
		t.Fatal("late finish overwrote interrupted marker")
	}
}

func (e *replayEntry) isDone() bool {
	if e == nil {
		return false
	}
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func newReplayPersistRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// waitForReplayRow waits for the detached persistence goroutine to land the
// finished entry in SQLite.
func waitForReplayRow(t *testing.T, rt *Runtime, digest string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var resultJSON string
		err := rt.state.DB().QueryRow(`SELECT result_json FROM tool_replays WHERE replay_digest = ?`, digest).Scan(&resultJSON)
		if err == nil {
			return resultJSON
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("finished replay entry was never persisted")
	return ""
}

func TestReplayEntriesSurviveRuntimeRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	rt := newReplayPersistRuntime(t)

	args := replayTestArgs("npm run build")
	key := rt.toolReplayKey("execute", args)
	entry, delivered, _ := rt.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false)
	if entry == nil || delivered {
		t.Fatal("fresh identity must register an owned entry")
	}
	rt.toolReplayFinish(context.Background(), entry, mcpresult.NewText("build ok"), true)
	stored := waitForReplayRow(t, rt, key)
	if !strings.Contains(stored, "build ok") {
		t.Fatalf("persisted result_json = %q, want the recorded outcome", stored)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	// Second process on the same state database: the identical retry must hit
	// the persisted entry and be marked as a restart recovery.
	restarted := newReplayPersistRuntime(t)
	defer func() { _ = restarted.Close() }()
	_, delivered, replayed := restarted.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false)
	if !delivered || replayed == nil {
		t.Fatal("identical retry after restart must replay the persisted outcome")
	}
	if value, _ := replayed.Meta["replayed"].(bool); !value {
		t.Fatal("post-restart replay marker missing")
	}
	reason, _ := replayed.Meta["replay_reason"].(string)
	if !strings.Contains(reason, "service restart recovery") {
		t.Fatalf("restored entry must present as restart recovery, reason=%q", reason)
	}
	// rerun must still force fresh execution on top of a restored entry.
	rerun := replayTestArgs("npm run build")
	rerun["rerun"] = true
	fresh, replaying, _ := restarted.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(rerun), false)
	if replaying || fresh == nil {
		t.Fatal("rerun=true must opt out of a restored replay and register a fresh owned entry")
	}
}

func TestReplayPersistPrunesExpiredRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	rt := newReplayPersistRuntime(t)
	defer func() { _ = rt.Close() }()

	// A row persisted by a previous run, already past the replay TTL.
	expiredDigest := rt.toolReplayKey("execute", replayTestArgs("old command"))
	if _, err := rt.state.DB().Exec(`INSERT INTO tool_replays
		(replay_digest, remote_session_id, workspace_name, tool_name, result_json, interrupted, created_at)
		VALUES (?, 'sess-1', '', 'execute', '{"content":[]}', 0, ?)`,
		expiredDigest, time.Now().Add(-2*toolReplayTTL).UTC().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	args := replayTestArgs("fresh command")
	freshDigest := rt.toolReplayKey("execute", args)
	entry, delivered, _ := rt.replayDeliver(context.Background(), context.Background(), "execute", mcpresult.Request(args), false)
	if entry == nil || delivered {
		t.Fatal("fresh identity must register an owned entry")
	}
	rt.toolReplayFinish(context.Background(), entry, mcpresult.NewText("fresh"), true)
	waitForReplayRow(t, rt, freshDigest)
	// A late duplicate finish must never overwrite the persisted outcome.
	rt.toolReplayFinish(context.Background(), entry, mcpresult.NewText("late overwrite"), false)
	stored := waitForReplayRow(t, rt, freshDigest)
	if strings.Contains(stored, "late overwrite") {
		t.Fatalf("rejected duplicate finish overwrote the persisted result: %s", stored)
	}

	// The write prunes lapsed rows in the same pass; wait for that pass.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var lapsed int
		if err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM tool_replays WHERE created_at <= ?`,
			time.Now().Add(-toolReplayTTL).UTC().UnixMilli()).Scan(&lapsed); err == nil && lapsed == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var count int
	if err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM tool_replays`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("tool_replays rows=%d, want only the fresh entry after TTL prune", count)
	}
}
