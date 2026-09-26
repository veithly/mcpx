package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestParitymcp_toolListDescribeCallAndInvalidArgumentRecovery(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.MCP.Enabled = true
	ws, _ := rt.reg.Get("demo")
	starts := writeFakeMCPTestServer(t, ws.Path)
	session := operationTestSession(t, rt, "demo")
	handler := rt.toolHandlers["mcp_tool"]
	for _, action := range []string{"list", "describe"} {
		args := map[string]any{"action": action, "remote_session_id": session.ID, "server": "fake", "tool": "echo"}
		result := callEnvelope(t, handler, context.Background(), args)
		if !statusOK(result) {
			t.Fatalf("%s failed: %+v", action, result)
		}
		data := result["data"].(map[string]any)
		if action == "list" {
			items := asMapSlice(data["tools"])
			if len(items) != 1 || items[0]["name"] != "echo" {
				t.Fatalf("inventory=%+v", data)
			}
		} else if data["input_schema"] == nil {
			t.Fatalf("schema missing: %+v", data)
		}
		t.Logf("%s data=%+v", action, data)
	}
	args := map[string]any{"action": "call", "remote_session_id": session.ID, "server": "fake", "tool": "echo", "purpose": "isolated parity", "arguments": map[string]any{"value": 7}, "idempotency_key": "parity-invalid"}
	invalid := callEnvelope(t, handler, context.Background(), args)
	if statusOK(invalid) || errorCode(invalid) != "mcp_argument_invalid" {
		t.Fatalf("invalid arguments=%+v", invalid)
	}
	t.Logf("invalid arguments: code=%s", errorCode(invalid))
	// Corrected arguments are a new operation, not a replay of failed arguments.
	args["arguments"] = map[string]any{"value": "recovered"}
	args["idempotency_key"] = "parity-valid"
	result := callRawToolResult(t, handler, args)
	if result.IsError || mcpresult.FirstText(result) != "echo:recovered" {
		t.Fatalf("recovery=%+v", result)
	}
	before := fakeMCPStartCount(t, starts)
	replay := callRawToolResult(t, handler, args)
	if replay.IsError || mcpresult.FirstText(replay) != "echo:recovered" || replay.Meta[mcpMetaReplay] != true {
		t.Fatalf("replay=%+v", replay)
	}
	if after := fakeMCPStartCount(t, starts); after != before {
		t.Fatalf("replay restarted peer: %d -> %d", before, after)
	}
	t.Logf("corrected call=%q; replay=%v; starts unchanged=%d", mcpresult.FirstText(result), replay.Meta[mcpMetaReplay], before)
}

func TestParitymcp_toolProtocolFailureDoesNotRepeatEffect(t *testing.T) {
	rt, args, logPath := reliabilityMCPRequest(t, "protocol_error")
	for i := 0; i < 2; i++ {
		result := callRawToolResult(t, rt.toolHandlers["mcp_tool"], args)
		if !result.IsError {
			t.Fatal("protocol error must be visible")
		}
		t.Logf("attempt=%d error=%t text=%s", i+1, result.IsError, mcpresult.FirstText(result))
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(calls), "effect\n")
	if count != 1 {
		t.Fatalf("same-key retry duplicated effects: count=%d", count)
	}
	t.Logf("two calls, upstream effects=%d", count)
}
