package server

import (
	"context"
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
	entry := r.toolReplays.begin(key)
	if got := r.toolReplays.get(key); got != entry {
		t.Fatal("in-flight entry must be returned to identical lookups")
	}
	r.toolReplayFinish(entry, mcpresult.NewText("exit 0"), true)

	delivered, ok := r.replayDeliver(context.Background(), "execute", args)
	if !ok || delivered == nil {
		t.Fatal("interrupted attempt must be replayed")
	}
	if delivered.Content[0].(*mcp.TextContent) == nil {
		t.Fatal("replayed content missing")
	}
	if value, _ := delivered.Meta["replayed"].(bool); !value {
		t.Fatal("replayed meta flag missing")
	}
}

func TestReplayExcludesNarrativeArgumentsFromIdentity(t *testing.T) {
	r := &Runtime{}
	base := replayTestArgs("npm run check")
	entry := r.toolReplays.begin(r.toolReplayKey("execute", base))
	r.toolReplayFinish(entry, mcpresult.NewText("exit 0"), true)

	retried := map[string]any{"remote_session_id": "sess-1", "command": "npm run check", "purpose": "复核构建是否修复"}
	if _, ok := r.replayDeliver(context.Background(), "execute", retried); !ok {
		t.Fatal("purpose-only differences must still resolve to the recorded attempt")
	}
}

func TestReplaySkipsHealthyAttemptsAndRerun(t *testing.T) {
	r := &Runtime{}
	args := replayTestArgs("git status")
	entry := r.toolReplays.begin(r.toolReplayKey("git", args))
	r.toolReplayFinish(entry, mcpresult.NewText("clean"), false)
	if _, ok := r.replayDeliver(context.Background(), "git", args); ok {
		t.Fatal("an attempt that finished with its client attached must not replay")
	}

	rerun := replayTestArgs("git status")
	rerun["rerun"] = true
	if _, ok := r.replayDeliver(context.Background(), "git", rerun); ok {
		t.Fatal("rerun=true must opt out of replay")
	}
}

func TestReplaySeparatesSessionsAndExpiry(t *testing.T) {
	r := &Runtime{}
	other := map[string]any{"remote_session_id": "sess-2", "command": "npm run check"}
	entry := r.toolReplays.begin(r.toolReplayKey("execute", other))
	r.toolReplayFinish(entry, mcpresult.NewText("ok"), true)

	sameCommandOtherSession := map[string]any{"remote_session_id": "sess-9", "command": "npm run check"}
	if _, ok := r.replayDeliver(context.Background(), "execute", sameCommandOtherSession); ok {
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
	entry := r.toolReplays.begin(r.toolReplayKey("execute", args))
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.toolReplayFinish(entry, mcpresult.NewText("late result"), true)
	}()
	delivered, ok := r.replayDeliver(context.Background(), "execute", args)
	if !ok || delivered == nil {
		t.Fatal("identical retry must wait for the in-flight attempt and replay it")
	}
}
