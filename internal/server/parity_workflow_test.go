package server

import (
	"context"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestParityInterruptedReplaySeparatesTransportBindings(t *testing.T) {
	rt := newWorkspaceRuntime(t, "first", "second")
	first := operationTestSession(t, rt, "first")
	second := operationTestSession(t, rt, "second")
	ctxA := withRuntimeContext(context.Background(), RuntimeContext{RequestID: "req-a", TransportSessionID: "transport-a"})
	ctxB := withRuntimeContext(context.Background(), RuntimeContext{RequestID: "req-b", TransportSessionID: "transport-b"})
	principal, err := rt.principalFromContext(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	rt.bindRemoteSession(ctxA, principal, first.ID)
	rt.bindRemoteSession(ctxB, principal, second.ID)
	req := mcpresult.Request(map[string]any{"action": "run", "command": "printf same", "purpose": "binding probe"})
	owner, delivered, _ := rt.replayDeliver(ctxA, ctxA, "execute", req, false)
	if owner == nil || delivered {
		t.Fatal("first call did not start")
	}
	rt.toolReplayFinish(ctxA, owner, mcpresult.NewText("first workspace outcome"), true)
	next, delivered, result := rt.replayDeliver(ctxB, ctxB, "execute", req, false)
	if delivered || next == nil {
		t.Fatalf("another bound workspace replayed first outcome: %+v", result)
	}
	rt.toolReplayFinish(ctxB, next, mcpresult.NewText("second workspace outcome"), false)
}
