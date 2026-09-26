package server

import (
	"context"
	"testing"
	"time"

	"mcpx/internal/observation"
)

func TestParityprogressLifecycleAndErrorRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ctx := context.Background()
	opened := callEnvelope(t, rt.toolSession, ctx, map[string]any{"action": "open", "workspace": "demo"})
	sid := opened["remote_session_id"].(string)
	wrapped := rt.toolHandlers["progress"]
	call := func(status string) map[string]any {
		args := map[string]any{"remote_session_id": sid, "current": "isolated progress check", "result": []any{"fixture verified"}}
		if status != "" {
			args["status"] = status
		}
		return callEnvelope(t, wrapped, ctx, args)
	}
	ongoing := call("")
	if !statusOK(ongoing) || ongoing["data"].(map[string]any)["status"] != "in_progress" {
		t.Fatalf("ongoing=%+v", ongoing)
	}
	rejected := call("invalid")
	if statusOK(rejected) || errorCode(rejected) != "invalid_arguments" {
		t.Fatalf("invalid status not rejected: %+v", rejected)
	}
	t.Logf("default=in_progress invalid_status=%+v", rejected["error"])
	completed := call("completed")
	if !statusOK(completed) || completed["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("recovery=%+v", completed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := rt.observation.store.QueryMemory(ctx, observation.MemoryQuery{Workspace: "demo", SessionID: sid, Type: "progress", Latest: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 && page.Items[0].Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed not persisted: %+v", page)
		}
		time.Sleep(10 * time.Millisecond)
	}
	attached := callEnvelope(t, rt.toolSessionOpen, ctx, map[string]any{"remote_session_id": sid})
	latest, ok := attached["data"].(map[string]any)["latest_model_state"].(map[string]any)
	if !ok || latest["status"] != "completed" || latest["summary"] != "isolated progress check" {
		t.Fatalf("restored=%+v", attached)
	}
	t.Logf("recovery=completed persisted=completed reattach_status=%s summary=%s", latest["status"], latest["summary"])
}
