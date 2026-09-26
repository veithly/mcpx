package server

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/remotesession"
)

func TestParityenvironmentSnapshotRoundTripAndRecovery(t *testing.T) {
	rt, cs, ctx := parityEnvironmentReadClient(t)
	// The shared fixture confines persistence to t.TempDir and removes host probes from PATH.
	principal, err := rt.principalFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := rt.reg.Get("demo")
	created, err := rt.remote.Create(ctx, principal, remotesession.CreateInput{WorkspaceName: "demo", WorkspacePath: ws.Path})
	if err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) (*mcp.CallToolResult, map[string]any) {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "environment", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		payload, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("structured content type=%T", result.StructuredContent)
		}
		return result, payload
	}
	result, payload := call(map[string]any{"remote_session_id": "rs_missing", "sections": []string{"runtime"}})
	failure, _ := payload["error"].(map[string]any)
	t.Logf("unknown session: status=%v isError=%v code=%v message=%v", payload["status"], result.IsError, failure["code"], failure["message"])
	if !result.IsError || payload["status"] != "failed" {
		t.Fatalf("unknown session accepted: %+v", payload)
	}
	result, payload = call(map[string]any{"remote_session_id": created.Session.ID, "sections": []string{"runtime"}})
	data, _ := payload["data"].(map[string]any)
	id, _ := data["snapshot_id"].(string)
	if result.IsError || id == "" || data["runtime"] == nil || data["toolchains"] != nil {
		t.Fatalf("snapshot save: %+v", payload)
	}
	saved, err := rt.environment.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	session, err := rt.remote.Get(ctx, principal, created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RemoteSessionID != created.Session.ID || saved.Report.Toolchains == nil || session.EnvironmentSnapshotID != id {
		t.Fatal("saved snapshot or session binding mismatch")
	}
	t.Logf("save after error: status=%v snapshot_present=true runtime_present=true filtered_toolchains=true persisted_full_report=true session_binding=true", payload["status"])
	result, payload = parityEnvironmentReadCall(t, ctx, cs, map[string]any{"remote_session_id": created.Session.ID, "snapshot_id": id, "sections": []string{"runtime"}})
	data, _ = payload["data"].(map[string]any)
	comparison, _ := data["comparison"].(map[string]any)
	changes, ok := comparison["changes"].([]any)
	if result.IsError || comparison["base_snapshot_id"] != id || !ok || len(changes) != 0 {
		t.Fatalf("snapshot compare: %+v", payload)
	}
	t.Logf("read compare: status=%v base_matches=true changes=%d", payload["status"], len(changes))
}
