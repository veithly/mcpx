package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/operation"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

// TestOperationResultPreservesEnvelopeErrorDetail pins the contract that a
// failed step keeps the envelope's machine-readable error code and message
// (e.g. PROCESS_EXIT + the exit reason) instead of collapsing into a generic
// "public tool execution failed" string.
func TestOperationResultPreservesEnvelopeErrorDetail(t *testing.T) {
	r := &Runtime{}
	resp := envelope.Fail(envelope.StatusError, "req_1", "ws",
		map[string]any{"exit_code": 1}, "PROCESS_EXIT", "command exited with code 1")
	result, resultErr := r.resultJSON(resp)
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	out := operationResult(result, nil)
	if out.Err == nil {
		t.Fatal("failed envelope must produce a step error")
	}
	if got := out.Err.Error(); got != "PROCESS_EXIT: command exited with code 1" {
		t.Fatalf("step error=%q, want PROCESS_EXIT detail", got)
	}
}

func TestPublicToolFailureFromTextEnvelopeAndFallback(t *testing.T) {
	nested := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: `{"status":"failed","mcpx":{"result":{"error":{"code":"COMMAND_NOT_FOUND","message":"command executable was not found (exit code 127)"}}}}`,
	}}}
	if got := publicToolFailure(nested); got.Error() != "COMMAND_NOT_FOUND: command executable was not found (exit code 127)" {
		t.Fatalf("nested envelope error=%v", got)
	}
	bare := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"failed"}`}}}
	if got := publicToolFailure(bare); got.Error() != "public tool execution failed" {
		t.Fatalf("fallback error=%v", got)
	}
}

// TestWaitForOperationTaskKillsTaskOnCancel proves that an operation step
// whose wait context is cancelled (operation cancel or step deadline) kills
// the underlying terminal Task instead of leaving the shell process running.
func TestWaitForOperationTaskKillsTaskOnCancel(t *testing.T) {
	manager := terminal.NewTaskManager()
	r := &Runtime{tasks: manager}
	task, err := manager.StartRemoteWithObservation(
		context.Background(), "req_kill", "execute", "rs_kill", "demo", t.TempDir(), "sleep 30",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf(`{"execution_task_id":%q}`, task.ID)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	input := operation.ExecuteInput{
		OperationID: "op_kill", StepID: "main", RequestID: "req_kill",
		RemoteSessionID: "rs_kill", WorkspaceName: "demo", Purpose: "kill on cancel", Tool: "execute",
	}
	_, waitErr := r.waitForOperationTask(ctx, input, result)
	cancel()
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("wait error=%v, want context.Canceled", waitErr)
	}
	doneCtx, doneCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer doneCancel()
	if !task.Wait(doneCtx) {
		t.Fatal("killed task did not reach a terminal state")
	}
	if status := task.StatusView()["status"]; status != terminal.TaskKilled {
		t.Fatalf("task status=%v, want killed", status)
	}
	// A second kill stays a no-op (idempotent), matching the operation layer's
	// timeout+cancel double path.
	if err := task.Kill(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteErrorConflictCarriesCurrentVersion(t *testing.T) {
	r := &Runtime{}
	envReq := envelope.Request{RequestID: "req_conflict", Workspace: "demo"}
	result, resultErr := r.remoteError(envReq, "rs-1", "demo", &remotesession.VersionConflictError{CurrentVersion: 7})
	if resultErr != nil {
		t.Fatal(resultErr)
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content=%+v", result.StructuredContent)
	}
	errBody, _ := sc["error"].(map[string]any)
	// envelope.Fail normalizes codes to upper case on the wire.
	if errBody == nil || errBody["code"] != "VERSION_CONFLICT" {
		t.Fatalf("error body=%+v", errBody)
	}
	if message, _ := errBody["message"].(string); !strings.Contains(message, "current version is 7") || !strings.Contains(message, "re-read") {
		t.Fatalf("conflict message lacks retry guidance: %q", message)
	}
	data, _ := sc["data"].(map[string]any)
	if data == nil || data["current_version"] != float64(7) {
		t.Fatalf("data=%+v, want current_version 7", data)
	}
}
