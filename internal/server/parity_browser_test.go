package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParitybrowserStatusUnsupportedHTTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("must not discover real Windows browsers")
	}
	rt := newWorkspaceRuntime(t, "demo")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opened := callEnvelope(t, rt.toolSession, ctx, map[string]any{"action": "open", "workspace": "demo"})
	remoteID, ok := opened["remote_session_id"].(string)
	if !ok {
		t.Fatalf("session open: %+v", opened)
	}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-browser", Version: "1"}, nil)
	rt.registerTools(protocol)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer srv.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity-client", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "browser", Arguments: map[string]any{"action": "status", "remote_session_id": remoteID}})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeToolResult(t, result)
	failure, _ := payload["error"].(map[string]any)
	t.Logf("status: isError=%v code=%v message=%v", result.IsError, failure["code"], failure["message"])
	if !result.IsError || failure["code"] != "BROWSER_UNAVAILABLE" || failure["message"] != "OpenAI browser extension integration is only supported on Windows" {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

// Reject unsupported combinations rather than silently changing the gesture.
func TestParitybrowserNodeClickPreservesGesture(t *testing.T) {
	for _, action := range []string{"click", "double_click"} {
		t.Run(action, func(t *testing.T) {
			payload := map[string]any{"tab_id": "7", "node_id": "42", "keys": []any{"Shift"}}
			if action == "click" {
				payload["button"] = 3
			}
			command, err := browserServiceCommand(action, payload)
			t.Logf("input=%v mapped=%v err=%v", payload, command, err)
			if err != nil {
				return
			}
			if command["keys"] == nil || (action == "click" && command["button"] == nil) {
				t.Error("accepted gesture lost keys/button; reject unsupported combination or preserve semantics")
			}
		})
	}
}
