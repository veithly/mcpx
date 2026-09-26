package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func parityMoveOutClient(t *testing.T) (*mcp.ClientSession, string, string, string) {
	t.Helper()
	trash := installMoveOutTrashMocks(t, "linux")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rt := newWorkspaceRuntime(t, "demo")
	sid := openMoveOutSession(t, rt)
	ws, _ := rt.reg.Get("demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-move-out", Version: "test"}, nil)
	rt.registerTools(protocol)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity-move-out-client", Version: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, sid, ws.Path, trash
}

func parityMoveOutCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("missing structuredContent: %+v", result)
	}
	return result, sc
}

func parityMoveOutAction(t *testing.T, sc map[string]any) (string, map[string]any) {
	t.Helper()
	actions := asMapSlice(sc["actions"])
	if len(actions) > 0 {
		action := actions[0]
		name, _ := action["id"].(string)
		if name == "" {
			name, _ = action["tool"].(string)
		}
		args, _ := action["arguments"].(map[string]any)
		return name, args
	}
	data, _ := sc["data"].(map[string]any)
	next, _ := data["next_action"].(map[string]any)
	name, _ := next["tool"].(string)
	args, _ := next["arguments"].(map[string]any)
	return name, args
}

func TestParitymove_outRoundTrip(t *testing.T) {
	cs, sid, root, trash := parityMoveOutClient(t)
	source := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(source, []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "prepare", "remote_session_id": sid, "purpose": "将测试文件安全移出", "targets": []any{map[string]any{"path": "sample.txt", "expected_sha256": digestForTest([]byte("fixture\n"))}}}
	result, prepared := parityMoveOutCall(t, cs, "move_out", args)
	if result.IsError {
		t.Fatalf("prepare: %+v", prepared)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("prepare mutated source: %v", err)
	}
	name, submit := parityMoveOutAction(t, prepared)
	if name != "move_out" || submit["action"] != "submit" || len(submit) != 3 {
		t.Fatalf("submit action: %+v", prepared)
	}
	// User confirmation is simulated only for this isolated fixture.
	result, committed := parityMoveOutCall(t, cs, name, submit)
	data := committed["data"].(map[string]any)
	if result.IsError || data["moved_count"] != float64(1) {
		t.Fatalf("submit: %+v", committed)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source remains: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(trash, "sample.txt")); err != nil || string(b) != "fixture\n" {
		t.Fatalf("trash=%q err=%v", b, err)
	}
	result, replay := parityMoveOutCall(t, cs, name, submit)
	if result.IsError || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("replay: %+v", replay)
	}
	t.Logf("prepare unchanged; returned submit executed unchanged; moved_count=%v; replay=true; temporary trash bytes intact", data["moved_count"])
}

func TestParitymove_outRecovery(t *testing.T) {
	cs, sid, root, _ := parityMoveOutClient(t)
	path := filepath.Join(root, "stale.txt")
	if err := os.WriteFile(path, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, stale := parityMoveOutCall(t, cs, "move_out", map[string]any{"action": "prepare", "remote_session_id": sid, "purpose": "移出测试文件", "targets": []any{map[string]any{"path": "stale.txt", "expected_sha256": digestForTest([]byte("old\n"))}}})
	if !result.IsError || errorCode(stale) != "stale_revision" {
		t.Fatalf("stale guard: %+v", stale)
	}
	encoded, _ := json.Marshal(stale)
	t.Logf("stale response=%s", encoded)
	name, args := parityMoveOutAction(t, stale)
	if name != "" {
		result, next := parityMoveOutCall(t, cs, name, args)
		encoded, _ = json.Marshal(next["error"])
		t.Logf("verbatim recovery tool=%s isError=%t error=%s", name, result.IsError, encoded)
		if result.IsError {
			t.Errorf("returned recovery action cannot execute unchanged")
		}
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new\n" {
		t.Fatalf("stale prepare changed file: %q %v", b, err)
	}
}
