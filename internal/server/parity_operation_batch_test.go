package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/operation"
)

// Exercise the registered public handlers and real child tools. All runtime
// state and file reads are confined to the existing t.TempDir fixture.
func TestParityoperation_batchDAGSuccess(t *testing.T) {
	rt := newWorkspaceRuntime(t, "batch")
	session := operationTestSession(t, rt, "batch")
	if err := os.WriteFile(filepath.Join(session.WorkspacePath, "ok.txt"), []byte("project"), 0o600); err != nil {
		t.Fatal(err)
	}
	accepted := callOperationTool(t, rt, "operation_batch", map[string]any{
		"remote_session_id": session.ID, "purpose": "隔离验证 operation_batch 正常依赖链",
		"operations": []any{
			map[string]any{"id": "a", "tool": "runtime_read", "arguments": map[string]any{"view": "project"}},
			map[string]any{"id": "b", "tool": "runtime_read", "arguments": map[string]any{"view": "project"}, "depends_on": []any{"a"}},
			map[string]any{"id": "c", "tool": "runtime_read", "arguments": map[string]any{"view": "project"}, "depends_on": []any{"b"}},
		},
	})
	if accepted["status"] != "accepted" || acceptedOperationID(accepted) == "" {
		t.Fatalf("submission=%+v", accepted)
	}
	completed := callOperationTool(t, rt, "operation_manage", map[string]any{
		"remote_session_id": session.ID, "operation_id": acceptedOperationID(accepted), "action": "wait", "timeout_ms": 5000,
	})
	parityOperationBatchLog(t, accepted["status"], completed)
	if completed["status"] != "succeeded" {
		t.Fatalf("completion=%+v", completed)
	}
	record, err := rt.operations.Get(context.Background(), acceptedOperationID(accepted))
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Steps) != 3 {
		t.Fatalf("steps=%d", len(record.Steps))
	}
	for i, step := range record.Steps {
		if step.State != operation.StateSucceeded || step.StartedAt == nil || step.CompletedAt == nil {
			t.Fatalf("step %s state=%s timestamps incomplete", step.ID, step.State)
		}
		if i > 0 && step.StartedAt.Before(*record.Steps[i-1].CompletedAt) {
			t.Fatalf("step %s started before its dependency completed", step.ID)
		}
	}
	encoded, err := json.Marshal(completed["data"])
	if err != nil || !strings.Contains(string(encoded), "manifests") {
		t.Fatalf("project facts missing from public result: %s err=%v", encoded, err)
	}
}

// Desired-behavior regression: must fail until failure propagation reaches
// every descendant instead of leaving an un-runnable queued step.
func TestParityoperation_batchFailurePropagatesTransitively(t *testing.T) {
	rt := newWorkspaceRuntime(t, "batch")
	session := operationTestSession(t, rt, "batch")
	accepted := callOperationTool(t, rt, "operation_batch", map[string]any{
		"remote_session_id": session.ID, "purpose": "隔离验证 operation_batch 失败依赖传播",
		"operations": []any{
			map[string]any{"id": "a", "tool": "runtime_read", "arguments": map[string]any{"view": "project", "workspace": "missing"}},
			map[string]any{"id": "b", "tool": "runtime_read", "arguments": map[string]any{"view": "project"}, "depends_on": []any{"a"}},
			map[string]any{"id": "c", "tool": "runtime_read", "arguments": map[string]any{"view": "project"}, "depends_on": []any{"b"}},
		},
	})
	if accepted["status"] != "accepted" || acceptedOperationID(accepted) == "" {
		t.Fatalf("submission=%+v", accepted)
	}
	completed := callOperationTool(t, rt, "operation_manage", map[string]any{
		"remote_session_id": session.ID, "operation_id": acceptedOperationID(accepted), "action": "wait", "timeout_ms": 500,
	})
	parityOperationBatchLog(t, accepted["status"], completed)
	record, err := rt.operations.Get(context.Background(), acceptedOperationID(accepted))
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Steps) != 3 || record.Steps[0].State != operation.StateFailed || record.Steps[1].State != operation.StateSkipped {
		t.Fatalf("failure fixture did not reach failed root and skipped child: %+v", record.Steps)
	}
	if completed["status"] != "failed" || record.State != operation.StateFailed || record.Steps[2].State != operation.StateSkipped {
		t.Fatalf("transitive failure did not terminate: status=%v operation=%s a=%s b=%s c=%s; want failed/failed/failed/skipped/skipped",
			completed["status"], record.State, record.Steps[0].State, record.Steps[1].State, record.Steps[2].State)
	}
}

func parityOperationBatchLog(t *testing.T, acceptedStatus any, completed map[string]any) {
	t.Helper()
	data, _ := completed["data"].(map[string]any)
	steps, _ := data["steps"].([]any)
	summary := make([]map[string]any, 0, len(steps))
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		summary = append(summary, map[string]any{"id": step["id"], "state": step["state"], "error": step["error"]})
	}
	encoded, err := json.Marshal(map[string]any{"submit_status": acceptedStatus, "wait_status": completed["status"], "state": data["state"], "steps": summary, "next_action": data["next_action"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("public_response=%s", encoded)
}
