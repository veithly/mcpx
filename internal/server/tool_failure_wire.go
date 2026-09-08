package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/arc"
)

func wrapFailureResult(ctx context.Context, name string, result *mcp.CallToolResult) *mcp.CallToolResult {
	runtime, _ := runtimeContextFrom(ctx)
	now := time.Now().UnixMilli()
	started := runtime.StartedAtMs
	if started <= 0 {
		started = now
	}
	received := runtime.ReceivedAtMs
	if received <= 0 {
		received = started
	}
	return arc.WrapToolResult(name, arc.ResultContext{RequestID: runtime.RequestID, TraceID: runtime.TraceID, SpanID: runtime.SpanID, Context: arc.Context{OperationID: runtime.OperationID}, Timing: arc.Timing{StartedAtMs: started, ReceivedAtMs: received, CompletedAtMs: now, ProcessingMs: max(0, now-received), ServerElapsedMs: max(0, now-started)}}, result)
}

// Contains primitives only; independent of ARC rendering, custom marshalers
// and the malformed result that triggered this final safety net.
func emergencyToolFailure() *mcp.CallToolResult {
	now := time.Now().UnixMilli()
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "RESULT_ENCODING_FAILED: MCPX could not format this call's result. 下一步：Inspect the call history or affected state before retrying or choosing another command; execution effects are unknown."}}, StructuredContent: map[string]any{
		"status": "failed", "type": "error", "context": map[string]any{}, "timing": map[string]any{"started_at_ms": now, "server_received_at_ms": now, "server_timestamp_ms": now, "network_latency_ms": 0, "tool_duration_ms": 0}, "data": map[string]any{}, "error": map[string]any{"code": "RESULT_ENCODING_FAILED", "category": "internal", "message": "tool result formatting failed", "retryable": false, "details": map[string]any{"execution_state": "unknown", "safe_to_retry_unchanged": false, "retry_hint": "Inspect recorded state before a safe retry or alternative."}},
	}}
}
