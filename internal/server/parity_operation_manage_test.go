package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/operation"
	"mcpx/internal/remotesession"
)

// All persistence lives in newWorkspaceRuntime's t.TempDir; no resident endpoint.
func parityOperationManageHTTP(t *testing.T) (*Runtime, remotesession.Session, func(map[string]any) map[string]any) {
	t.Helper()
	rt := newWorkspaceRuntime(t, "demo")
	remote := operationTestSession(t, rt, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "operation-manage-parity", Version: "1"}, nil)
	rt.registerTools(protocol)
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "operation-manage-parity-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return rt, remote, func(args map[string]any) map[string]any {
		t.Helper()
		if _, exists := args["remote_session_id"]; !exists {
			args["remote_session_id"] = remote.ID
		}
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "operation_manage", Arguments: args})
		if err != nil {
			t.Fatalf("operation_manage protocol error: %v", err)
		}
		wire, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("missing structured content: %+v", result)
		}
		return wire
	}
}

func TestParityoperation_manageNormalLifecycle(t *testing.T) {
	rt, remote, call := parityOperationManageHTTP(t)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	record, err := rt.operations.Submit(context.Background(), operation.SubmitSpec{
		RemoteSessionID: remote.ID, WorkspaceName: remote.WorkspaceName,
		RequestID: "parity-normal", Purpose: "隔离验证 operation_manage 生命周期",
		Steps: []operation.StepSpec{{ID: "read", Tool: "read"}},
	}, func(ctx context.Context, _ operation.ExecuteInput) operation.ExecuteResult {
		select {
		case <-release:
			return operation.ExecuteResult{Result: json.RawMessage("{\"value\":\"normal-ok\"}")}
		case <-ctx.Done():
			return operation.ExecuteResult{Err: ctx.Err()}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := call(map[string]any{"operation_id": record.ID, "action": "wait", "timeout_ms": 10})
	if pending["status"] != "accepted" {
		t.Fatalf("pending wait=%+v", pending)
	}
	t.Logf("pending wait: status=%v state=%v", pending["status"], pending["data"].(map[string]any)["state"])
	close(release)
	finished := call(map[string]any{"operation_id": record.ID, "action": "wait", "timeout_ms": 5000})
	if finished["status"] != "succeeded" {
		t.Fatalf("finished wait=%+v", finished)
	}
	step := finished["data"].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if step["result"].(map[string]any)["value"] != "normal-ok" {
		t.Fatalf("wait lost step result: %+v", step)
	}
	for _, action := range []string{"status", "result", "cancel"} {
		wire := call(map[string]any{"operation_id": record.ID, "action": action})
		data := wire["data"].(map[string]any)
		if wire["status"] != "succeeded" || data["state"] != "succeeded" {
			t.Fatalf("%s changed completed state: %+v", action, wire)
		}
		if action == "result" && data["result"].(map[string]any)["value"] != "normal-ok" {
			t.Fatalf("result content=%+v", data)
		}
		t.Logf("%s: status=%v state=%v", action, wire["status"], data["state"])
	}
	other := operationTestSession(t, rt, "demo")
	denied := call(map[string]any{"remote_session_id": other.ID, "operation_id": record.ID, "action": "status"})
	if denied["status"] != "failed" || !strings.Contains(operationErrorMessage(denied), "another Remote Session") {
		t.Fatalf("cross-session access not rejected: %+v", denied)
	}
	t.Logf("cross-session status: %s", operationErrorMessage(denied))
	missing := call(map[string]any{"operation_id": "parity-missing", "action": "status"})
	if missing["status"] != "failed" || !strings.Contains(operationErrorMessage(missing), "operation not found") {
		t.Fatalf("unknown operation=%+v", missing)
	}
	t.Logf("unknown status: %s", operationErrorMessage(missing))
}

func TestParityoperation_managePaginationNextAction(t *testing.T) {
	rt, remote, call := parityOperationManageHTTP(t)
	record := submitOperationForTest(t, rt, remote, "page", "{\"value\":\"012345678901234567890123456789\"}")
	first := call(map[string]any{"operation_id": record.ID, "action": "result", "limit": 10})
	data := first["data"].(map[string]any)
	page := data["result"].(map[string]any)
	if first["status"] != "succeeded" || data["next_cursor"] != "10" {
		t.Fatalf("first page=%+v", first)
	}
	nextArgs := first["actions"].([]any)[0].(map[string]any)["arguments"].(map[string]any)
	continued := call(nextArgs)
	nextData := continued["data"].(map[string]any)
	nextPage := nextData["result"].(map[string]any)
	t.Logf("first chunk=%q next_cursor=%v advertised_limit=%v", page["chunk"], data["next_cursor"], nextArgs["limit"])
	raw, _ := json.Marshal(nextPage)
	t.Logf("following next_action: result=%s next_cursor=%q", raw, nextData["next_cursor"])
	if continued["status"] != "succeeded" || nextPage["offset"] != page["next_offset"] || nextPage["chunk"] == nil {
		t.Fatalf("continuation did not preserve cursor/chunk contract: want offset=%v; got %s", page["next_offset"], raw)
	}
}

func TestParityoperation_managePaginationUTF8(t *testing.T) {
	rt, remote, call := parityOperationManageHTTP(t)
	const original = "{\"value\":\"中文\"}"
	record := submitOperationForTest(t, rt, remote, "utf8", original)
	var rebuilt strings.Builder
	cursor := ""
	for i := 0; i < 8; i++ {
		wire := call(map[string]any{"operation_id": record.ID, "action": "result", "limit": 11, "cursor": cursor})
		if wire["status"] != "succeeded" {
			t.Fatalf("result=%+v", wire)
		}
		data := wire["data"].(map[string]any)
		page := data["result"].(map[string]any)
		chunk, ok := page["chunk"].(string)
		if !ok {
			t.Fatalf("missing page chunk: %+v", data)
		}
		rebuilt.WriteString(chunk)
		cursor, _ = data["next_cursor"].(string)
		t.Logf("offset=%v next_offset=%v chunk=%q next_cursor=%q", page["offset"], page["next_offset"], chunk, cursor)
		if cursor == "" {
			break
		}
	}
	if cursor != "" || rebuilt.String() != original {
		t.Fatalf("UTF-8 pagination changed payload: want=%q got=%q", original, rebuilt.String())
	}
}
