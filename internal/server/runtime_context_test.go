package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
)

func TestRuntimeContextComesFromTransportHeaders(t *testing.T) {
	now := time.UnixMilli(1_785_486_000_100)
	headers := http.Header{
		"X-Request-ID":         []string{"req_header"},
		"Traceparent":          []string{"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"},
		"X-MCPX-Started-At-Ms": []string{"1785486000000"},
	}
	ctx, runtime := ensureRuntimeContext(context.Background(), headers, now)
	if runtime.RequestID != "req_header" || runtime.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("runtime identity = %+v", runtime)
	}
	if runtime.ParentSpanID != "0123456789abcdef" || runtime.SpanID == runtime.ParentSpanID || runtime.SpanID == "" {
		t.Fatalf("span context = %+v", runtime)
	}
	if runtime.StartedAtMs != 1785486000000 || runtime.ReceivedAtMs != now.UnixMilli() {
		t.Fatalf("runtime timing = %+v", runtime)
	}
	if got, ok := runtimeContextFrom(ctx); !ok || got.RequestID != runtime.RequestID {
		t.Fatalf("context value = %+v, %v", got, ok)
	}
}

func TestRuntimeContextGeneratesValuesWithoutHeaders(t *testing.T) {
	now := time.UnixMilli(1_785_486_000_100)
	_, runtime := ensureRuntimeContext(context.Background(), nil, now)
	if runtime.RequestID == "" || runtime.TraceID == "" || runtime.SpanID == "" {
		t.Fatalf("runtime IDs were not generated: %+v", runtime)
	}
	if runtime.StartedAtMs != now.UnixMilli() || runtime.ReceivedAtMs != now.UnixMilli() {
		t.Fatalf("generated timing = %+v", runtime)
	}
}

func TestClientContextStashSurvivesDetachedWorker(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := withClientContext(context.WithoutCancel(parent), parent)
	if _, ok := clientContextFrom(worker); !ok {
		t.Fatal("detached worker lost the stashed client context")
	}
	stashed, ok := clientContextFrom(worker)
	if !ok || stashed != parent {
		t.Fatalf("stashed context = %v, %v", stashed, ok)
	}
	cancel()
	if _, ok := clientContextFrom(context.Background()); ok {
		t.Fatal("plain contexts must not report a stashed client context")
	}
}
func TestRegisteredToolSchemasExcludeClientTimestampAndServerRuntimeContext(t *testing.T) {
	runtime := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	for name, registered := range runtime.listedToolMap() {
		encoded, err := json.Marshal(registered)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var tool map[string]any
		if err := json.Unmarshal(encoded, &tool); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		inputSchema, _ := tool["inputSchema"].(map[string]any)
		if schemaContainsProperty(inputSchema, "started_at_ms") {
			t.Fatalf("%s input schema must not expose started_at_ms: %s", name, encoded)
		}
		for _, field := range []string{"request_id", "trace_id", "span_id", "received_at_ms", "completed_at_ms", "network_latency_ms", "processing_ms", "server_elapsed_ms"} {
			if schemaContainsProperty(inputSchema, field) {
				t.Fatalf("%s input schema contains server runtime field %q: %s", name, field, encoded)
			}
		}
	}
}

func TestInstrumentToolUsesServerReceiveTimeWithoutClientTimestamp(t *testing.T) {
	before := time.Now().UnixMilli()
	ctx := withRuntimeContext(context.Background(), RuntimeContext{
		RequestID: "req_context", TraceID: "trace_context", SpanID: "span_context", StartedAtMs: 1,
	})
	called := false
	var handlerStarted int64
	handler := func(handlerCtx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		runtime, ok := runtimeContextFrom(handlerCtx)
		if !ok || runtime.RequestID != "req_context" {
			t.Fatalf("handler runtime context = %+v, %v", runtime, ok)
		}
		handlerStarted = runtime.StartedAtMs
		return mcpresult.NewStructured(map[string]any{"value": "ok"}, "ok"), nil
	}
	request := mcpresult.Request(map[string]any{"action": "list"})

	result, err := (&Runtime{}).instrumentTool("runtime_context_test", handler)(ctx, request)
	if err != nil || !called {
		t.Fatalf("instrumented call err=%v called=%v", err, called)
	}
	after := time.Now().UnixMilli()
	if handlerStarted < before || handlerStarted > after {
		t.Fatalf("handler started_at_ms = %d, want server receive time in [%d,%d]", handlerStarted, before, after)
	}
	envelope := decodeARCEnvelope(t, result)
	mcpx, _ := envelope["mcpx"].(map[string]any)
	trace, _ := mcpx["trace"].(map[string]any)
	if trace["request_id"] != "req_context" || trace["trace_id"] != "trace_context" || trace["span_id"] != "span_context" {
		t.Fatalf("ARC did not project runtime identity: %+v", trace)
	}
	if trace["started_at_ms"] != trace["received_at_ms"] || trace["network_latency_ms"] != float64(0) {
		t.Fatalf("ARC did not use server receive time fallback: %+v", trace)
	}
	structured, _ := result.StructuredContent.(map[string]any)
	timing, _ := structured["timing"].(map[string]any)
	if timing["started_at_ms"] != timing["server_received_at_ms"] || timing["server_timestamp_ms"] == nil || timing["tool_duration_ms"] == nil {
		t.Fatalf("model timing = %+v", timing)
	}
}

func schemaRequiresProperty(value any, wanted string) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	required, _ := item["required"].([]any)
	for _, field := range required {
		if field == wanted {
			return true
		}
	}
	return false
}

func schemaContainsProperty(value any, wanted string) bool {
	switch item := value.(type) {
	case map[string]any:
		if properties, ok := item["properties"].(map[string]any); ok {
			if _, exists := properties[wanted]; exists {
				return true
			}
		}
		for _, nested := range item {
			if schemaContainsProperty(nested, wanted) {
				return true
			}
		}
	case []any:
		for _, nested := range item {
			if schemaContainsProperty(nested, wanted) {
				return true
			}
		}
	}
	return false
}
