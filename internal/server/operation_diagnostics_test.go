package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mcpx/internal/operation"
)

func TestOperationManageWaitTimeoutDoesNotCancel(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	session := operationTestSession(t, rt, "demo")
	record, err := rt.operations.Submit(context.Background(), operation.SubmitSpec{
		RemoteSessionID: session.ID, WorkspaceName: "demo", RequestID: "req_test", Purpose: "等待测试",
		Steps: []operation.StepSpec{{ID: "wait", Tool: "read"}},
	}, func(ctx context.Context, input operation.ExecuteInput) operation.ExecuteResult {
		select {
		case <-time.After(100 * time.Millisecond):
			return operation.ExecuteResult{Result: []byte(`{"ok":true}`)}
		case <-ctx.Done():
			return operation.ExecuteResult{Err: ctx.Err()}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	current, timedOut, err := rt.operations.Wait(context.Background(), record.ID, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut || (current.State != operation.StateQueued && current.State != operation.StateRunning) {
		t.Fatalf("current=%+v timedOut=%v", current, timedOut)
	}
	final, timedOut, err := rt.operations.Wait(context.Background(), record.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut || final.State != operation.StateSucceeded {
		t.Fatalf("final=%+v timedOut=%v", final, timedOut)
	}
}

func TestOperationViewFailedStepCarriesErrorSummary(t *testing.T) {
	record := operation.Record{
		ID: "op_1", RemoteSessionID: "sess-1", WorkspaceName: "demo",
		State: operation.StateFailed, Purpose: "摘要测试",
		Steps: []operation.StepRecord{
			{
				ID: "build", Tool: "execute", State: operation.StateFailed,
				Error: json.RawMessage(`{"message":"RUNTIME_STEP_TIMEOUT: step exceeded the 45s execution deadline"}`),
			},
			{
				ID: "deploy", Tool: "execute", State: operation.StateCancelled,
				Error: json.RawMessage(`{"message":"PROCESS_EXIT: command exited with code 1"}`),
			},
			{ID: "verify", Tool: "read", State: operation.StateSucceeded},
			{ID: "queued", Tool: "read", State: operation.StateQueued},
		},
	}
	view := operationView(record, false)
	steps := view["steps"].([]map[string]any)
	if got := steps[0]["error"]; got != "RUNTIME_STEP_TIMEOUT: step exceeded the 45s execution deadline" {
		t.Fatalf("failed step error = %v, want the durable step timeout code", got)
	}
	if got := steps[1]["error"]; got != "PROCESS_EXIT: command exited with code 1" {
		t.Fatalf("cancelled step error = %v, want the durable exit reason", got)
	}
	if _, exists := steps[2]["error"]; exists {
		t.Fatal("succeeded step must not carry an error field")
	}
	if _, exists := steps[3]["error"]; exists {
		t.Fatal("queued step must not carry an error field")
	}
	// Result views keep the full machine error object instead of the summary.
	detailed := operationView(record, true)
	detailedSteps := detailed["steps"].([]map[string]any)
	if _, ok := detailedSteps[0]["error"].(map[string]any); !ok {
		t.Fatalf("result view must keep the full error object, got %T", detailedSteps[0]["error"])
	}
	if _, ok := detailedSteps[0]["error"].(map[string]any)["message"]; !ok {
		t.Fatal("result view error object lost its message field")
	}
	// A malformed or empty error must never crash the view.
	broken := operation.Record{Steps: []operation.StepRecord{
		{ID: "x", Tool: "read", State: operation.StateInterrupted, Error: nil},
	}}
	brokenSteps := operationView(broken, false)["steps"].([]map[string]any)
	if _, exists := brokenSteps[0]["error"]; exists {
		t.Fatal("interrupted step without durable error must not invent one")
	}
}

func TestStepErrorSummaryFallbacks(t *testing.T) {
	cases := []struct {
		raw  json.RawMessage
		want string
	}{
		{json.RawMessage(`{"message":"RUNTIME_STEP_TIMEOUT: slow"}`), "RUNTIME_STEP_TIMEOUT: slow"},
		{json.RawMessage(`{"code":"PROCESS_EXIT"}`), "PROCESS_EXIT"},
		{json.RawMessage(`"bare string"`), "bare string"},
		{json.RawMessage(`{}`), ""},
		{nil, ""},
	}
	for _, test := range cases {
		if got := stepErrorSummary(test.raw); got != test.want {
			t.Fatalf("stepErrorSummary(%s) = %q, want %q", test.raw, got, test.want)
		}
	}
}
