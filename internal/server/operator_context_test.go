package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/config"
	"mcpx/internal/control"
	"mcpx/internal/mcpresult"
)

func TestOperatorRequestArrivingDuringToolIsReturnedAndAcknowledged(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	sid := consoleRemote(t, rt, "alpha")
	ctx := context.Background()
	var queued control.Request
	handler := rt.instrumentTool("read", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var err error
		queued, err = rt.control.Enqueue(ctx, "alpha", sid, "steer", "Stop and review the failing tests", "during-call")
		if err != nil {
			t.Fatal(err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "operation finished"}}, StructuredContent: map[string]any{"status": "succeeded", "data": map[string]any{"value": "done"}}}, nil
	})
	out, err := handler(ctx, mcpresult.Request(map[string]any{"remote_session_id": sid, "path": "."}))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), queued.ID) || !strings.Contains(string(wire), "operator_control") {
		t.Fatalf("in-flight update absent: %s", wire)
	}
	if items, _ := rt.control.List(ctx, "alpha", sid); len(items) != 1 || items[0].Status != "delivered" {
		t.Fatalf("wrong delivery state: %+v", items)
	}
	next := rt.instrumentTool("read", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := mcpresult.Arguments(req)["acknowledge_requests"]; ok {
			t.Fatal("transport receipt leaked into executable arguments")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "next"}}, StructuredContent: map[string]any{"status": "succeeded", "data": map[string]any{}}}, nil
	})
	if _, err = next(ctx, mcpresult.Request(map[string]any{"remote_session_id": sid, "path": ".", "acknowledge_requests": []string{queued.ID}})); err != nil {
		t.Fatal(err)
	}
	items, _ := rt.control.List(ctx, "alpha", sid)
	if items[0].Status != "acknowledged" {
		t.Fatalf("ack failed: %+v", items)
	}
}

func TestConsoleWorkspaceRegistrationPreservesExistingName(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	c := consoleLogin(t, rt)
	path := filepath.Join(t.TempDir(), "physical-project")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGlobal(rt.globalCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := config.WorkspaceEntry{Name: "custom-project-name", Path: path}
	cfg.Workspaces = append(cfg.Workspaces, entry)
	if err = config.WriteGlobal(rt.globalCfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err = rt.reg.Register(entry); err != nil {
		t.Fatal(err)
	}
	res := c.request(t, "POST", "workspaces", map[string]string{"path": path})
	if res.Code != 200 || !strings.Contains(res.Body.String(), "custom-project-name") {
		t.Fatalf("existing workspace renamed: %d %s", res.Code, res.Body.String())
	}
	if _, ok := rt.reg.Get("physical-project"); ok {
		t.Fatal("duplicate physical workspace created")
	}
	newPath := filepath.Join(t.TempDir(), "new-project")
	if err = os.Mkdir(newPath, 0700); err != nil {
		t.Fatal(err)
	}
	res = c.request(t, "POST", "workspaces", map[string]string{"path": newPath})
	if res.Code != 201 {
		t.Fatalf("add workspace=%d %s", res.Code, res.Body.String())
	}
	if _, ok := rt.reg.Get("new-project"); !ok {
		t.Fatal("workspace not immediately available to MCP tools")
	}
	res = c.request(t, "POST", "workspaces", map[string]string{"path": "relative"})
	if res.Code != 400 {
		t.Fatal("relative workspace path accepted")
	}
}

func TestFullAccessExecutesWithoutConfirmationButPreservesDeny(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	ctx := context.Background()
	sid := consoleRemote(t, rt, "alpha")
	rt.cfg.Security.Commands.Confirm = []string{"echo *"}
	rt.cfg.Security.Commands.Deny = []string{"echo explicitly-denied"}
	if err := rt.control.SetMode(ctx, "alpha", control.FullAccess); err != nil {
		t.Fatal(err)
	}
	result := callEnvelope(t, rt.toolExecCommand, ctx, map[string]any{"remote_session_id": sid, "cmd": "echo console-full-access", "yield_time_ms": 1000})
	if result["status"] == "waiting_confirmation" || errorCode(result) != "" {
		t.Fatalf("permitted full access failed: %+v", result)
	}
	if len(rt.approvals.ListRemoteSession(sid)) != 0 {
		t.Fatal("full access generated unnecessary approval")
	}
	denied := callEnvelope(t, rt.toolExecCommand, ctx, map[string]any{"remote_session_id": sid, "cmd": "echo explicitly-denied"})
	if !strings.Contains(fmt.Sprint(denied), "COMMAND_DENIED") {
		t.Fatalf("explicit deny bypassed: %+v", denied)
	}
}

func TestFullAccessDoesNotUndoOperatorRejection(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	ctx := context.Background()
	sid := consoleRemote(t, rt, "alpha")
	rt.cfg.Security.Commands.Confirm = []string{"echo *"}
	args := map[string]any{"remote_session_id": sid, "cmd": "echo rejected-command"}
	first := callEnvelope(t, rt.toolExecCommand, ctx, args)
	if !strings.Contains(fmt.Sprint(first), "APPROVAL_REQUIRED") {
		t.Fatalf("expected pending: %+v", first)
	}
	pending := rt.approvals.ListRemoteSession(sid)
	if len(pending) != 1 {
		t.Fatalf("pending=%+v", pending)
	}
	if err := rt.control.Decide(ctx, pending[0].ID, pending[0].CommandDigest, "denied"); err != nil {
		t.Fatal(err)
	}
	if err := rt.control.SetMode(ctx, "alpha", control.FullAccess); err != nil {
		t.Fatal(err)
	}
	result := callEnvelope(t, rt.toolExecCommand, ctx, args)
	if !strings.Contains(fmt.Sprint(result), "COMMAND_DENIED") {
		t.Fatalf("operator rejection overridden: %+v", result)
	}
}
