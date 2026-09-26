package server

import (
	"context"
	"testing"
)

func TestParityplanCreateAdvanceRead(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ctx := context.Background()
	opened := callEnvelope(t, rt.toolSession, ctx, map[string]any{"action": "open", "workspace": "demo"})
	sid := opened["remote_session_id"].(string)
	created := callEnvelope(t, rt.toolHandlers["plan"], ctx, map[string]any{
		"action": "create", "remote_session_id": sid, "purpose": "parity plan", "summary": "verify plan lifecycle",
		"tasks": []any{map[string]any{"title": "work"}}, "idempotency_key": "create",
	})
	if !statusOK(created) {
		t.Fatalf("create=%+v", created)
	}
	data := created["data"].(map[string]any)
	pid := data["plan_id"].(string)
	tid := asMapSlice(data["tasks"])[0]["plan_task_id"].(string)
	advanced := callEnvelope(t, rt.toolHandlers["plan"], ctx, map[string]any{
		"action": "advance", "remote_session_id": sid, "purpose": "start", "plan_id": pid, "plan_task_id": tid,
	})
	if !statusOK(advanced) {
		t.Fatalf("advance=%+v", advanced)
	}
	read := callEnvelope(t, rt.toolHandlers["plan"], ctx, map[string]any{"action": "read", "remote_session_id": sid, "plan_id": pid})
	if !statusOK(read) {
		t.Fatalf("read=%+v", read)
	}
	task := asMapSlice(read["data"].(map[string]any)["tasks"])[0]
	if task["plan_task_id"] != tid || task["status"] != "in_progress" {
		t.Fatalf("read task=%+v", task)
	}
	t.Logf("create=%s advance=%s read=%s task_status=%s", created["status"], advanced["status"], read["status"], task["status"])
}

func TestParityplanDependencyRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ctx := context.Background()
	opened := callEnvelope(t, rt.toolSession, ctx, map[string]any{"action": "open", "workspace": "demo"})
	sid := opened["remote_session_id"].(string)
	call := func(args map[string]any) map[string]any {
		args["remote_session_id"] = sid
		if args["action"] != "read" {
			args["purpose"] = "parity dependency recovery"
		}
		return callEnvelope(t, rt.toolHandlers["plan"], ctx, args)
	}
	created := call(map[string]any{"action": "create", "tasks": []any{
		map[string]any{"local_id": "first", "title": "first"},
		map[string]any{"title": "second", "depends_on": []string{"first"}},
	}})
	if !statusOK(created) {
		t.Fatalf("create=%+v", created)
	}
	data := created["data"].(map[string]any)
	pid := data["plan_id"].(string)
	tid := asMapSlice(data["tasks"])[1]["plan_task_id"].(string)
	request := map[string]any{"action": "advance", "plan_id": pid, "plan_task_id": tid, "idempotency_key": "advance-second"}
	rejected := call(request)
	if errorCode(rejected) != "plan_invalid_request" {
		t.Fatalf("expected dependency rejection: %+v", rejected)
	}
	t.Logf("blocked advance: error=%+v actions=%+v", rejected["error"], rejected["actions"])
	repaired := call(map[string]any{"action": "replan", "plan_id": pid, "reason": "second no longer needs first", "operations": []any{
		map[string]any{"action": "update", "plan_task_id": tid, "depends_on": []string{}},
	}})
	if !statusOK(repaired) {
		t.Fatalf("replan=%+v", repaired)
	}
	replay := call(request)
	t.Logf("same-key retry after successful replan: status=%v error=%+v data=%+v", replay["status"], replay["error"], replay["data"])
	read := call(map[string]any{"action": "read", "plan_id": pid})
	if !statusOK(read) {
		t.Fatalf("read=%+v", read)
	}
	task := asMapSlice(read["data"].(map[string]any)["tasks"])[1]
	t.Logf("read after retry: status=%v depends_on=%v", task["status"], task["depends_on"])
	if task["depends_on"] != nil || task["status"] != "todo" {
		t.Fatalf("dependency repair was not persisted: %+v", task)
	}
	request["idempotency_key"] = "advance-second-after-replan"
	recovered := call(request)
	if !statusOK(recovered) || recovered["data"].(map[string]any)["status"] != "in_progress" {
		t.Fatalf("new-key recovery=%+v", recovered)
	}
	t.Logf("new-key retry: status=%v task_status=%v", recovered["status"], recovered["data"].(map[string]any)["status"])
	originalError := rejected["error"].(map[string]any)
	replayError := replay["error"].(map[string]any)
	if statusOK(replay) || replay["status"] != rejected["status"] || errorCode(replay) != errorCode(rejected) || replayError["message"] != originalError["message"] || replayError["category"] != originalError["category"] || replayError["retryable"] != originalError["retryable"] {
		t.Fatalf("same-key terminal failure must replay unchanged: original=%+v replay=%+v", rejected["error"], replay["error"])
	}
}
