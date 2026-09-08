package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/arc"
	"mcpx/internal/config"
	"mcpx/internal/mcpresult"
)

func TestToolFailuresReachStreamableHTTPClient(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	sid := consoleRemote(t, rt, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "http-failures", Version: "1"}, nil)
	input := json.RawMessage(`{"type":"object"}`)
	rt.addTool(protocol, mcp.Tool{Name: "broken", InputSchema: input}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("fixture dependency rejected the command")
	})
	rt.addTool(protocol, mcp.Tool{Name: "invalid_output", InputSchema: input}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewStructured(map[string]any{"payload": make(chan int)}, "unserializable"), nil
	})
	rt.addTool(protocol, mcp.Tool{Name: "valid", InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("healthy follow-up"), nil
	})
	finish := make(chan struct{})
	late := rt.boundedTool("late", rt.instrumentTool("late", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-finish
		return mcpresult.NewText("late result recorded"), nil
	}), 60*time.Millisecond)
	protocol.AddTool(&mcp.Tool{Name: "late", InputSchema: input, OutputSchema: arc.OutputSchema()}, late)
	defer func() {
		close(finish)
		until := time.Now().Add(3 * time.Second)
		for len(rt.toolResponseSlots) > 0 && time.Now().Before(until) {
			time.Sleep(time.Millisecond)
		}
	}()
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Stateless: true})
	ts := httptest.NewServer(NewGateway(cfg, nil, streamable).Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-recovery-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	cases := []struct {
		name, code string
		args       map[string]any
	}{
		{"broken", "TOOL_EXECUTION_FAILED", map[string]any{}},
		{"invalid_output", "RESULT_ENCODING_FAILED", map[string]any{}},
		{"valid", "INVALID_ARGUMENTS", map[string]any{"value": "wrong-type"}},
		{"late", "TOOL_TIMEOUT", map[string]any{"remote_session_id": sid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			result, err := cs.CallTool(bounded, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("tool failure became an HTTP/protocol failure: %v", err)
			}
			assertToolFailureWire(t, result, tc.code)
			if !strings.Contains(mcpresult.FirstText(result), "下一步：") {
				t.Fatal("host-visible recovery missing")
			}
		})
	}
	healthy, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "valid", Arguments: map[string]any{"value": 7}})
	if err != nil || healthy == nil || healthy.IsError || !strings.Contains(mcpresult.FirstText(healthy), "healthy follow-up") {
		t.Fatalf("subsequent call failed: %+v %v", healthy, err)
	}
}

func TestAllPublishedOutputSchemasResolve(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	for _, tool := range rt.registeredTools() {
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if _, err = schema.Resolve(nil); err != nil {
			t.Fatalf("%s output schema invalid: %v", tool.Name, err)
		}
	}
}

func TestCancelledCallStillRecordsEventualOutcome(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	sid := consoleRemote(t, rt, "demo")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := rt.instrumentTool("late_record_fixture", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cancel()
		return mcpresult.NewText("late result recorded"), nil
	})
	result, err := handler(ctx, mcpresult.Request(map[string]any{"remote_session_id": sid}))
	if err != nil || result == nil {
		t.Fatalf("completion lost: %+v %v", result, err)
	}
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		var count int
		err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM observation_events WHERE remote_session_id=? AND event_type='tool.completed' AND tool_name='late_record_fixture'`, sid).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cancelled response erased the actual completed tool event")
}
