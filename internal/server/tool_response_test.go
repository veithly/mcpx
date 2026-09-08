package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/arc"
	"mcpx/internal/mcpresult"
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
