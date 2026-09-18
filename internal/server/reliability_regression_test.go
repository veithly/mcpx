package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/config"
	"mcpx/internal/mcpresult"
)

// A real stdio peer tests the complete proxy path, including protocol errors
// after an effect. No network, desktop access, or external interpreter needed.
func TestReliabilityMCPPeer(t *testing.T) {
	mode := os.Getenv("MCPX_RELIABILITY_PEER")
	if mode == "" {
		t.Skip("subprocess fixture")
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			reply["result"] = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "reliability", "version": "1"}}
		case "tools/list":
			reply["result"] = map[string]any{"tools": []any{map[string]any{"name": "probe", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true}}}}
		case "tools/call":
			f, err := os.OpenFile(os.Getenv("MCPX_RELIABILITY_CALL_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(2)
			}
			_, _ = f.WriteString("effect\n")
			_ = f.Close()
			switch mode {
			case "protocol_error":
				reply["error"] = map[string]any{"code": -32603, "message": "response failed after effect"}
			case "image", "image_error":
				reply["result"] = &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "desktop state"}, &mcp.ImageContent{MIMEType: "image/png", Data: []byte(strings.Repeat("x", 512<<10))}}, IsError: mode == "image_error"}
			case "oversize":
				reply["result"] = &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 5<<20)}}}
			}
		default:
			reply["result"] = map[string]any{}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(2)
		}
	}
}

func reliabilityMCPRequest(t *testing.T, mode string) (*Runtime, map[string]any, string) {
	t.Helper()
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.MCP.Enabled = true
	ws, _ := rt.reg.Get("demo")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	if err := config.WriteMCPFile(config.ProjectMCPPath(ws.Path), config.MCPFile{MCPServers: map[string]config.MCPServer{
		"probe": {Description: "reliability fixture", Command: exe, Args: []string{"-test.run=^TestReliabilityMCPPeer$"}, Env: map[string]string{"MCPX_RELIABILITY_PEER": mode, "MCPX_RELIABILITY_CALL_LOG": log}},
	}}); err != nil {
		t.Fatal(err)
	}
	session := operationTestSession(t, rt, "demo")
	args := map[string]any{"action": "call", "remote_session_id": session.ID, "server": "probe", "tool": "probe", "purpose": "verify proxy reliability", "arguments": map[string]any{}, "idempotency_key": "probe-once"}
	return rt, args, log
}

func TestMCPProtocolErrorDoesNotRepeatEffects(t *testing.T) {
	rt, args, log := reliabilityMCPRequest(t, "protocol_error")
	for range 2 {
		result := callRawToolResult(t, rt.toolHandlers["mcp_tool"], args)
		if !result.IsError {
			t.Fatal("protocol failure must remain an error")
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(calls), "effect\n"); n != 1 {
		t.Fatalf("upstream executed %d times; want one even after same-key replay", n)
	}
}

func TestMCPImageResultHasSeparateBoundedBudget(t *testing.T) {
	for _, mode := range []string{"image", "image_error", "oversize", "configured_small_budget"} {
		t.Run(mode, func(t *testing.T) {
			peerMode := mode
			if mode == "configured_small_budget" {
				peerMode = "image"
			}
			rt, args, log := reliabilityMCPRequest(t, peerMode)
			if mode == "configured_small_budget" {
				rt.cfg.Limits.MaxMCPResultBytes = 256 << 10
			}
			for range 2 {
				result := callRawToolResult(t, rt.toolHandlers["mcp_tool"], args)
				if mode == "oversize" || mode == "configured_small_budget" {
					if !result.IsError || !strings.Contains(firstToolText(result), "MCP_RESULT_TOO_LARGE") {
						t.Fatal("oversized results must still be bounded")
					}
					continue
				}
				if result.IsError != (mode == "image_error") {
					t.Fatalf("upstream error status changed: %s", firstToolText(result))
				}
				// Failed upstream results may append one MCPX recovery hint.
				if len(result.Content) < 2 {
					t.Fatalf("lost upstream content: %s", firstToolText(result))
				}
				img, ok := result.Content[1].(*mcp.ImageContent)
				if !ok || len(img.Data) != 512<<10 {
					t.Fatal("image was lost or truncated")
				}
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "effect\n") != 1 {
				t.Fatal("result replay repeated the upstream effect")
			}
		})
	}
}

func TestRuntimeReadWorkspaceViewsDoNotRequireSession(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	for _, view := range []string{"project", "instructions"} {
		t.Run(view, func(t *testing.T) {
			result := callRawToolResult(t, rt.toolHandlers["runtime_read"], map[string]any{"view": view, "workspace": "alpha"})
			if result.IsError {
				t.Fatalf("explicit workspace read failed: %s", firstToolText(result))
			}
			missing := callRawToolResult(t, rt.toolHandlers["runtime_read"], map[string]any{"view": view})
			if !missing.IsError {
				t.Fatal("must not guess a workspace")
			}
			session := operationTestSession(t, rt, "alpha")
			mismatch, err := rt.toolRuntimeRead(context.Background(), mcpresult.Request(map[string]any{"view": view, "workspace": "beta", "remote_session_id": session.ID}))
			if err != nil || mismatch == nil || !mismatch.IsError {
				t.Fatal("must reject a conflicting workspace and session")
			}
		})
	}
}
