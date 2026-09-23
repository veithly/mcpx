package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/arc"
	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

func assertToolFailureWire(t *testing.T, result *mcp.CallToolResult, code string) {
	t.Helper()
	if result == nil || !result.IsError || strings.TrimSpace(mcpresult.FirstText(result)) == "" {
		t.Fatalf("missing error content: %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(arc.OutputSchema())
	var schema jsonschema.Schema
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(wire); err != nil {
		t.Fatalf("error does not meet published output schema: %v\n%s", err, encoded)
	}
	if errorCode(wire) != strings.ToLower(code) {
		t.Fatalf("code=%s want=%s: %s", errorCode(wire), code, encoded)
	}
	body, _ := wire["error"].(map[string]any)
	details, _ := body["details"].(map[string]any)
	if details["safe_to_retry_unchanged"] != false && code != "TOOL_BUSY" {
		t.Fatalf("unsafe retry advice: %+v", details)
	}
}

func TestToolResponseTimeoutDoesNotClaimWorkerStopped(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	sid := consoleRemote(t, rt, "demo")
	finish := make(chan struct{})
	exited := make(chan struct{})
	defer func() {
		close(finish)
		<-exited
		deadline := time.Now().Add(3 * time.Second)
		for len(rt.toolResponseSlots) > 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(rt.toolResponseSlots) > 0 {
			t.Error("worker did not finish bookkeeping")
		}
	}()
	handler := rt.boundedTool("slow_fixture", rt.instrumentTool("slow_fixture", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		defer close(exited)
		<-finish
		return mcpresult.NewText("late completion"), nil
	}), 40*time.Millisecond)
	started := time.Now()
	result, err := handler(context.Background(), mcpresult.Request(map[string]any{"remote_session_id": sid}))
	if err != nil {
		t.Fatal(err)
	}
	assertToolFailureWire(t, result, "TOOL_TIMEOUT")
	if time.Since(started) > time.Second {
		t.Fatal("response waited on non-cooperative tool")
	}
	if len(rt.toolResponseSlots) != 1 {
		t.Fatal("capacity released before worker actually finished")
	}
	rt.consoleMu.Lock()
	running := rt.consoleCalls[sid].Count
	rt.consoleMu.Unlock()
	if running != 1 {
		t.Fatal("timeout falsely marked active tool stopped")
	}
	wire := result.StructuredContent.(map[string]any)
	details := wire["error"].(map[string]any)["details"].(map[string]any)
	if details["request_id"] == "" || details["execution_state"] != "unknown" {
		t.Fatalf("no useful timeout identity: %+v", details)
	}
}

func TestToolCapacityLeavesRecoveryChannelAvailable(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	called := false
	action := rt.boundedTool("edit", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return mcpresult.NewText("effect"), nil
	}, time.Second)
	for i := 0; i < cap(rt.toolResponseSlots); i++ {
		rt.toolResponseSlots <- struct{}{}
	}
	defer func() {
		for len(rt.toolResponseSlots) > 0 {
			<-rt.toolResponseSlots
		}
	}()
	result, err := action(context.Background(), mcpresult.Request(map[string]any{}))
	if err != nil || called {
		t.Fatalf("busy call started: %v", err)
	}
	assertToolFailureWire(t, result, "TOOL_BUSY")
	recover := rt.boundedTool("observe", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("status available"), nil
	}, time.Second)
	result, err = recover(context.Background(), mcpresult.Request(map[string]any{}))
	if err != nil || result.IsError || mcpresult.FirstText(result) != "status available" {
		t.Fatalf("recovery channel blocked: %+v %v", result, err)
	}
}

func TestToolCancelledBeforeAdmissionDoesNotRun(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	called := false
	h := rt.boundedTool("cancel_fixture", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return nil, nil
	}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := h(ctx, mcpresult.Request(map[string]any{}))
	if err != nil || called {
		t.Fatalf("cancelled call executed: %v", err)
	}
	assertToolFailureWire(t, result, "TOOL_CANCELLED")
}

func TestEarlyToolValidationAndEmergencyMeetOutputSchema(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	result, err := rt.toolHandlers["execute"](context.Background(), mcpresult.Request(map[string]any{"action": "run", "command": false}))
	if err != nil {
		t.Fatal(err)
	}
	assertToolFailureWire(t, result, "INVALID_ARGUMENTS")
	assertToolFailureWire(t, emergencyToolFailure(), "RESULT_ENCODING_FAILED")
}

func TestUpstreamErrorHintIsNotDuplicated(t *testing.T) {
	req := mcpresult.Request(map[string]any{"action": "call"})
	result := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "original upstream failure"}}, StructuredContent: map[string]any{"partial": true}, Meta: mcp.Meta{mcpMetaServer: "fixture", mcpMetaTool: "reject"}}
	for i := 0; i < 3; i++ {
		result = ensureToolResponse(context.Background(), "mcp_tool", req, result, nil)
	}
	if len(result.Content) != 2 || mcpresult.FirstText(result) != "original upstream failure" {
		t.Fatalf("upstream contract rewritten or hint repeated: %+v", result)
	}
	if result.StructuredContent.(map[string]any)["partial"] != true {
		t.Fatal("upstream data lost")
	}
}

// The response deadline must not kill the worker: after TOOL_TIMEOUT the late
// worker still records its outcome, and an identical retry replays it instead
// of re-executing the side effects.
func TestBoundedToolLateWorkerOutcomeReplayable(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	sid := consoleRemote(t, rt, "demo")
	var executions int32
	release := make(chan struct{})
	workerExited := make(chan struct{})
	handler := rt.boundedTool("late_fixture", rt.instrumentTool("late_fixture", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		atomic.AddInt32(&executions, 1)
		defer close(workerExited)
		<-release
		return mcpresult.NewText("late completion"), nil
	}), 40*time.Millisecond)
	args := map[string]any{"remote_session_id": sid, "command": "make check"}
	first, err := handler(context.Background(), mcpresult.Request(args))
	if err != nil {
		t.Fatal(err)
	}
	assertToolFailureWire(t, first, "TOOL_TIMEOUT")
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&executions) == 1 && len(rt.toolResponseSlots) == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if atomic.LoadInt32(&executions) != 1 || len(rt.toolResponseSlots) != 0 {
		t.Fatalf("late worker did not finish bookkeeping: executions=%d slots=%d", executions, len(rt.toolResponseSlots))
	}
	replayed, err := handler(context.Background(), mcpresult.Request(args))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.IsError {
		t.Fatalf("recorded late completion must replay as success: %+v", replayed)
	}
	if value, _ := replayed.Meta["replayed"].(bool); !value {
		t.Fatalf("replayed marker missing: %+v", replayed.Meta)
	}
	if reason, _ := replayed.Meta["replay_reason"].(string); reason != "prior attempt already completed" {
		t.Fatalf("replay_reason = %q, want prior attempt already completed", reason)
	}
	if atomic.LoadInt32(&executions) != 1 {
		t.Fatalf("replay re-executed the tool body: %d executions", executions)
	}
}

// A restart-interrupted execution Task must surface as an explicit error
// envelope (with a re-run next_action), never as a success envelope.
func TestInterruptedTaskOutcomeIsErrorEnvelope(t *testing.T) {
	rt := &Runtime{}
	data := map[string]any{"status": string(terminal.TaskInterrupted), "command": "make check", "execution_task_id": "task-1"}
	code, message := annotateExecutionOutcome(data)
	if code != "EXECUTION_INTERRUPTED" || data["outcome"] != "interrupted" || data["error_code"] != "EXECUTION_INTERRUPTED" {
		t.Fatalf("interrupted annotation = %q data=%+v", code, data)
	}
	result, err := rt.executionOutcomeFailure(envelope.Request{RequestID: "req-interrupted"},
		remotesession.Session{ID: "sess-1", WorkspaceName: "demo"}, data, code, message)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("interrupted Task must produce an error envelope: %+v", result)
	}
	wire, _ := result.StructuredContent.(map[string]any)
	body, _ := wire["error"].(map[string]any)
	if body["code"] != "EXECUTION_INTERRUPTED" {
		t.Fatalf("error code = %v", body["code"])
	}
	envelopeData, _ := wire["data"].(map[string]any)
	next, _ := envelopeData["next_action"].(map[string]any)
	if next["tool"] != "execute" {
		t.Fatalf("interrupted next_action must guide a re-run: %+v", next)
	}
	arguments, _ := next["arguments"].(map[string]any)
	if arguments["action"] != "run" || arguments["command"] != "make check" {
		t.Fatalf("interrupted re-run arguments = %+v", arguments)
	}
}

func TestDetachedToolWorkerReportsIgnoredDeadline(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	stalled := make(chan string, 1)
	rt := &Runtime{onToolUnresponsive: func(name, requestID string) { stalled <- name }}
	handler := rt.boundedToolWithLimits("stuck_read", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-release // Deliberately ignores the worker deadline.
		return mcpresult.NewText("late"), nil
	}, 10*time.Millisecond, 25*time.Millisecond, 20*time.Millisecond)
	result, err := handler(context.Background(), mcpresult.Request(map[string]any{"view": "list"}))
	if err != nil || !result.IsError {
		t.Fatalf("expected bounded response timeout: result=%+v err=%v", result, err)
	}
	select {
	case name := <-stalled:
		if name != "stuck_read" {
			t.Fatalf("wrong stalled tool: %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("detached worker did not report ignored deadline")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(rt.toolResponseSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(rt.toolResponseSlots) != 0 {
		t.Fatal("detached worker failed to release its slot")
	}
}
