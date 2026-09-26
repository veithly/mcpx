package server

import (
	"context"
	"testing"
	"time"

	"mcpx/internal/remotesession"
)

// Shared remote-session fixture used by secret-provider isolation tests. It
// creates no process and does not expose a retired execution entry point.
func openCompoundCommandSession(t *testing.T, rt *Runtime) (string, string) {
	t.Helper()
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Session.ID, registered.Path
}

func TestObserveHistoricalTaskOwnershipAndLogContinuation(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	remoteID, workspace := openCompoundCommandSession(t, rt)
	otherID, _ := openCompoundCommandSession(t, rt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := rt.tasks.StartRemoteWithObservationContext(ctx, "history-fixture", "history-fixture", "management", remoteID, "demo", workspace, testPrintCommand("history-output"))
	if err != nil {
		t.Fatal(err)
	}
	if !task.Wait(ctx) {
		t.Fatal("history fixture did not finish")
	}
	status := callEnvelope(t, rt.toolObserve, ctx, map[string]any{"remote_session_id": remoteID, "view": "task", "execution_task_id": task.ID})
	data, _ := status["data"].(map[string]any)
	if status["status"] != "ok" || data["outcome"] != "succeeded" {
		t.Fatalf("history status: %+v", status)
	}
	foreign := callEnvelope(t, rt.toolObserve, ctx, map[string]any{"remote_session_id": otherID, "view": "task", "execution_task_id": task.ID})
	if errorCode(foreign) != "execution_task_not_found" {
		t.Fatalf("history crossed sessions: %+v", foreign)
	}
	rt.cfg.Limits.MaxResultBytes = 4
	logs := callEnvelope(t, rt.toolObserve, ctx, map[string]any{"remote_session_id": remoteID, "view": "logs", "execution_task_id": task.ID})
	data, _ = logs["data"].(map[string]any)
	if data["stdout"] != "hist" || data["stdout_next_offset"] != float64(4) {
		t.Fatalf("bounded history: %+v", logs)
	}
	next, _ := data["next_action"].(map[string]any)
	args, _ := next["arguments"].(map[string]any)
	if next["tool"] != "observe" || args["view"] != "logs" || args["stdout_offset"] != float64(4) {
		t.Fatalf("history continuation: %+v", next)
	}
}
