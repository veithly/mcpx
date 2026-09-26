package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/mcpresult"
)

func TestParityworkspaceHTTPListAndRecovery(t *testing.T) {
	for _, names := range [][]string{nil, {"alpha", "beta"}} {
		label := "empty"
		if len(names) > 0 {
			label = "registered"
		}
		t.Run(label, func(t *testing.T) {
			rt := newWorkspaceRuntime(t, names...)
			protocol := mcp.NewServer(&mcp.Implementation{Name: "workspace-parity", Version: "1"}, nil)
			rt.registerTools(protocol)
			server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "workspace-parity-client", Version: "1"}, nil)
			cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			for attempt := 0; attempt < 3; attempt++ {
				args := map[string]any{}
				if attempt == 1 {
					args["unexpected"] = true
				}
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workspace", Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(encoded, &body); err != nil {
					t.Fatal(err)
				}
				if attempt == 1 {
					if !result.IsError || errorCode(body) != "invalid_arguments" {
						t.Fatalf("invalid input: %s", encoded)
					}
					t.Logf("unexpected parameter: isError=%v code=%s", result.IsError, errorCode(body))
					continue
				}
				if result.IsError || !statusOK(body) {
					t.Fatalf("list: %s", encoded)
				}
				data := body["data"].(map[string]any)
				items, ok := data["workspaces"].([]any)
				if !ok || len(items) != len(names) {
					t.Fatalf("list shape: %s", encoded)
				}
				for i, name := range names {
					ws, _ := rt.reg.Get(name)
					item := items[i].(map[string]any)
					if item["name"] != name || item["path"] != ws.Path || item["description"] != ws.Description {
						t.Fatalf("item: %+v", item)
					}
				}
				if data["project_selection"] == "" || data["project_selection"] == nil || mcpresult.FirstText(result) == "" {
					t.Fatalf("missing output guidance: %s", encoded)
				}
				t.Logf("attempt=%d status=%v workspaces=%d guidance=true text=true", attempt, body["status"], len(items))
			}
		})
	}
}
