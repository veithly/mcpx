package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

func TestToolFailureReachesMCPClient(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "failure-test", Version: "1"}, nil)
	cases := []struct {
		name string
		call mcp.ToolHandler
	}{
		{"error_only", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("fixture execution failed")
		}},
		{"partial_error", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcpresult.NewStructured(map[string]any{"status": "succeeded", "data": map[string]any{"stdout": "partial output"}}, "partial output"), errors.New("fixture execution failed")
		}},
		{"nil_result", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil }},
		{"panic_result", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { panic("fixture panic") }},
		{"empty_error", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{IsError: true}, nil
		}},
		{"invalid_result", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcpresult.NewStructured(map[string]any{"data": make(chan int)}, "cannot encode"), nil
		}},
	}
	for _, tc := range cases {
		rt.addTool(protocol, mcp.Tool{Name: tc.name, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, tc.call)
	}
	rt.addTool(protocol, mcp.Tool{Name: "healthy", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("still alive"), nil
	})
	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ss, err := protocol.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "failure-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			result, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: tc.name, Arguments: map[string]any{}})
			if err != nil {
				t.Fatalf("error escaped as protocol/transport failure instead of a tool result: %v", err)
			}
			if result == nil || !result.IsError || strings.TrimSpace(mcpresult.FirstText(result)) == "" {
				t.Fatalf("failure has no visible content: %+v", result)
			}
			data, ok := result.StructuredContent.(map[string]any)
			if !ok || data["status"] != "failed" || data["error"] == nil {
				t.Fatalf("missing structured failure: %#v", result.StructuredContent)
			}
			text := mcpresult.FirstText(result)
			if !strings.Contains(strings.ToLower(text), "retry") && !strings.Contains(text, "下一步") {
				t.Fatalf("no recovery hint in model-visible text: %q", text)
			}
			if tc.name == "partial_error" && !strings.Contains(text, "partial output") {
				t.Fatalf("partial diagnostic lost: %q", text)
			}
		})
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "healthy", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("one failure broke subsequent calls: %+v %v", result, err)
	}
}

func TestToolArgumentsFailBeforeHandler(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "validation-test", Version: "1"}, nil)
	called := false
	rt.addTool(protocol, mcp.Tool{Name: "argument_probe", InputSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":0}},"required":["count"],"additionalProperties":false}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return mcpresult.NewText("not expected"), nil
	})
	for _, args := range []string{`{"count":"bad"}`, `{"count":-1}`, `{"count":1.5}`, `{}`, `null`, `[]`, `{"count":1,"surprise":true}`, `{"count":`} {
		called = false
		result, err := rt.toolHandlers["argument_probe"](context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "argument_probe", Arguments: json.RawMessage(args)}})
		if called || err != nil || result == nil || !result.IsError || mcpresult.FirstText(result) == "" {
			t.Fatalf("invalid arguments %s: called=%v result=%+v err=%v", args, called, result, err)
		}
	}
}

func TestEnvelopeFailureSetsMCPErrorFlag(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	result, err := rt.resultJSON(envelope.Fail(envelope.StatusError, "req_failure", "demo", map[string]any{"exit_code": 2}, "PROCESS_EXIT", "command exited with code 2"))
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("failure is not marked isError: %+v %v", result, err)
	}
	pending, err := rt.resultJSON(envelope.Fail(envelope.StatusNeedConfirmation, "req_pending", "demo", nil, "USER_CONFIRMATION_REQUIRED", "approval required"))
	if err != nil || pending == nil || pending.IsError {
		t.Fatalf("pending approval must not masquerade as execution failure: %+v %v", pending, err)
	}
}

// A handler error that hides the sentinel behind a non-%w format verb must
// still be classified from the execution context state instead of degrading
// to TOOL_EXECUTION_FAILED.
func TestNormalizeToolOutcomeClassifiesByContextState(t *testing.T) {
	req := mcpresult.Request(map[string]any{"remote_session_id": "sess-failure"})
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), -time.Second)
	defer timeoutCancel()
	result := ensureToolResponse(timeoutCtx, "classify_fixture", req, nil, fmt.Errorf("worker gave up: %v", context.DeadlineExceeded))
	assertToolFailureWire(t, result, "TOOL_TIMEOUT")

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	result = ensureToolResponse(canceledCtx, "classify_fixture", req, nil, fmt.Errorf("worker aborted: %v", context.Canceled))
	assertToolFailureWire(t, result, "TOOL_CANCELLED")

	wrapped := ensureToolResponse(context.Background(), "classify_fixture", req, nil, fmt.Errorf("worker gave up: %w", context.DeadlineExceeded))
	assertToolFailureWire(t, wrapped, "TOOL_TIMEOUT")
}
