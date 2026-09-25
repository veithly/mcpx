package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise the published schema and transport as well as the handlers. Direct
// handler tests alone cannot catch a tools/list vs read/edit contract mismatch.
func TestToolReliabilityReadEditExecuteViaHTTP(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands.Default = "allow"
	rt.cfg.Security.Commands.Deny = nil
	remote := operationTestSession(t, rt, "demo")
	path := filepath.Join(remote.WorkspacePath, "中文 file.txt")
	if err := os.WriteFile(path, []byte("first\r\nold\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "reliability", Version: "1"}, nil)
	rt.registerTools(protocol)
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "reliability-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "edit" {
			raw, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(raw), `"rev"`) || strings.Contains(string(raw), `"base_sha256"`) {
				t.Fatalf("unexpected revision contract: %s", raw)
			}
		}
	}
	// A cached connector can publish a schema that differs from tools/list.
	// Reject it before execution and identify the actual contract to refresh.
	stale, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "edit", Arguments: map[string]any{
		"remote_session_id": remote.ID, "purpose": "probe stale connector schema without writes",
		"edits": []any{map[string]any{"path": "中文 file.txt", "operation": "update", "base_sha256": "not-the-current-contract", "content": "must not write"}},
	}})
	if err != nil || !stale.IsError {
		t.Fatalf("stale connector request accepted: %v", err)
	}
	failure := stale.StructuredContent.(map[string]any)["error"].(map[string]any)
	details := failure["details"].(map[string]any)
	if details["tool_schema_revision"] != rt.currentToolSchemaRevision() || details["schema_source"] != "tools/list" {
		t.Fatalf("schema recovery metadata missing: %+v", details)
	}
	if !strings.Contains(failure["message"].(string), "rev") {
		t.Fatalf("current edit field omitted: %+v", failure)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		args["remote_session_id"] = remote.ID
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s protocol error: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s tool error: %+v", name, result.StructuredContent)
		}
		wire := result.StructuredContent.(map[string]any)
		if wire["status"] != "succeeded" && wire["status"] != "accepted" {
			t.Fatalf("%s: %+v", name, wire)
		}
		return wire["data"].(map[string]any)
	}
	read := call("read", map[string]any{"path": "中文 file.txt", "mode": "full"})
	sha, _ := read["rev"].(string)
	if sha == "" {
		t.Fatalf("read omitted rev: %+v", read)
	}
	editArgs := map[string]any{"purpose": "verify revision and newline round trip", "idempotency_key": "reliability-edit", "edits": []any{map[string]any{"path": "中文 file.txt", "operation": "update", "rev": sha, "replacements": []any{map[string]any{"match": "first\r\nold\r\n", "replacement": "first\r\nnew\r\n"}}}}}
	call("edit", editArgs)
	call("edit", editArgs)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first\r\nnew\r\n" {
		t.Fatalf("readback=%q err=%v", got, err)
	}
	command := call("execute", map[string]any{"action": "run", "purpose": "verify literal argument execution", "argv": []any{"/usr/bin/printf", "%s", "中文 $(literal)"}, "shell": false})
	if command["stdout"] != "中文 $(literal)" || command["exit_code"] != float64(0) {
		t.Fatalf("literal argv failed: %+v", command)
	}
	async := call("execute", map[string]any{"action": "run", "purpose": "verify async completion", "command": "sleep 0.1; printf async-ok", "execution_mode": "async", "yield_time_ms": 1})
	id, _ := async["operation_id"].(string)
	if id == "" {
		t.Fatalf("missing operation handle: %+v", async)
	}
	finished := call("operation_manage", map[string]any{"action": "wait", "operation_id": id, "timeout_ms": 5000})
	if finished["state"] != "succeeded" {
		t.Fatalf("async did not finish: %+v", finished)
	}
	cancelled := call("operation_manage", map[string]any{"action": "cancel", "operation_id": id})
	if cancelled["state"] != "succeeded" {
		t.Fatalf("late cancel changed outcome: %+v", cancelled)
	}
}
