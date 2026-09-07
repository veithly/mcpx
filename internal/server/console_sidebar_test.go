package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

func sidebarToolResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}, StructuredContent: map[string]any{"status": "succeeded", "data": map[string]any{}}}
}

type sidebarTestState struct {
	Workspaces []consoleWorkspace `json:"workspaces"`
	Sessions   []consoleSession   `json:"sessions"`
	Revision   int64              `json:"sidebar_revision"`
	Removed    []string           `json:"removed_session_ids"`
}

func getSidebar(t *testing.T, c *consoleTestClient, path string) sidebarTestState {
	t.Helper()
	response := c.request(t, "GET", "state"+path, nil)
	if response.Code != 200 {
		t.Fatalf("state %d: %s", response.Code, response.Body.String())
	}
	var state sidebarTestState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func mutateSidebar(t *testing.T, c *consoleTestClient, kind, id, ws, action string, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	state := getSidebar(t, c, "")
	input := map[string]any{"kind": kind, "id": id, "workspace": ws, "action": action, "revision": state.Revision}
	for k, v := range extra {
		input[k] = v
	}
	return c.request(t, "POST", "sidebar", input)
}
func requireSidebarStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status=%d want=%d: %s", response.Code, want, response.Body.String())
	}
}

func TestSidebarPinMoveAndStaleRevision(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	a := consoleRemote(t, rt, "alpha")
	b := consoleRemote(t, rt, "alpha")
	other := consoleRemote(t, rt, "beta")
	c := consoleLogin(t, rt)
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "beta", "beta", "pin", map[string]any{"pinned": true}), 200)
	state := getSidebar(t, c, "")
	if state.Workspaces[0].Name != "beta" || !state.Workspaces[0].Pinned {
		t.Fatalf("workspace pin: %+v", state.Workspaces)
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", a, "alpha", "pin", map[string]any{"pinned": true}), 200)
	state = getSidebar(t, c, "")
	if state.Sessions[0].ID != a || !state.Sessions[0].Pinned {
		t.Fatalf("session pin: %+v", state.Sessions)
	}
	requireSidebarStatus(t, c.request(t, "POST", "sidebar", map[string]any{"kind": "session", "id": a, "workspace": "alpha", "action": "delete", "confirm": true, "revision": 0}), 409)
	requireSidebarStatus(t, mutateSidebar(t, c, "session", a, "alpha", "pin", map[string]any{"pinned": false}), 200)
	requireSidebarStatus(t, mutateSidebar(t, c, "session", a, "alpha", "move", map[string]any{"target_id": b, "placement": "before"}), 200)
	state = getSidebar(t, c, "")
	indices := map[string]int{}
	for i, s := range state.Sessions {
		indices[s.ID] = i
	}
	if indices[a] >= indices[b] {
		t.Fatal("manual order not saved")
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", a, "alpha", "move", map[string]any{"target_id": other, "placement": "before"}), 409)
	requireSidebarStatus(t, mutateSidebar(t, c, "session", a, "beta", "pin", map[string]any{"pinned": true}), 404)
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "beta", "beta", "pin", map[string]any{"pinned": false}), 200)
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "beta", "beta", "move", map[string]any{"target_id": "alpha", "placement": "before"}), 200)
	if state = getSidebar(t, c, ""); state.Workspaces[0].Name != "beta" {
		t.Fatal("workspace drag order missing")
	}
}

func TestSidebarDeleteDoesNotRemoveProjectOrResurrectSessions(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	id := consoleRemote(t, rt, "alpha")
	consoleRemote(t, rt, "beta")
	c := consoleLogin(t, rt)
	ws, _ := rt.reg.Get("alpha")
	sentinel := filepath.Join(ws.Path, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep my project"), 0600); err != nil {
		t.Fatal(err)
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", id, "alpha", "delete", nil), 400)
	requireSidebarStatus(t, mutateSidebar(t, c, "session", id, "alpha", "delete", map[string]any{"confirm": true}), 200)
	for i := 0; i < 3; i++ {
		state := getSidebar(t, c, "")
		for _, s := range state.Sessions {
			if s.ID == id {
				t.Fatal("deleted session resurrected")
			}
		}
	}
	result := callEnvelope(t, rt.toolSessionOpen, context.Background(), map[string]any{"remote_session_id": id})
	if errorCode(result) == "" {
		t.Fatal("deleted session still accepts resume")
	}
	newer := consoleRemote(t, rt, "alpha")
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "alpha", "alpha", "delete", map[string]any{"confirm": true}), 200)
	state := getSidebar(t, c, "")
	if len(state.Workspaces) != 1 || state.Workspaces[0].Name != "beta" {
		t.Fatalf("workspace removal=%+v", state)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep my project" {
		t.Fatalf("project file affected: %q %v", data, err)
	}
	p, _ := rt.principalFromContext(context.Background())
	if _, err := rt.createRemoteSession(context.Background(), p, envelope.Request{Payload: map[string]any{"workspace_path": ws.Path}}, ""); err == nil {
		t.Fatal("agent silently restored a deleted workspace")
	}
	requireSidebarStatus(t, c.request(t, "POST", "workspaces", map[string]any{"path": ws.Path}), 200)
	state = getSidebar(t, c, "")
	if len(state.Workspaces) != 2 {
		t.Fatal("workspace re-add failed")
	}
	for _, s := range state.Sessions {
		if s.ID == id || s.ID == newer {
			t.Fatal("workspace re-add restored deleted conversation")
		}
	}
}

func TestSidebarWorkingCallIsPinnedAndCannotBeDeleted(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	id := consoleRemote(t, rt, "alpha")
	other := consoleRemote(t, rt, "alpha")
	c := consoleLogin(t, rt)
	requireSidebarStatus(t, mutateSidebar(t, c, "session", other, "alpha", "pin", map[string]any{"pinned": true}), 200)
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := rt.instrumentTool("read", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		<-finish
		return sidebarToolResult(), nil
	})
	go func() {
		defer close(done)
		_, _ = handler(context.Background(), mcpresult.Request(map[string]any{"remote_session_id": id, "path": "."}))
	}()
	defer func() { close(finish); <-done }()
	select {
	case <-started:
	case <-done:
		t.Fatal("handler did not start")
	case <-time.After(5 * time.Second):
		t.Fatal("tool admission timed out")
	}
	state := getSidebar(t, c, "")
	if state.Sessions[0].ID != id || !state.Sessions[0].IsWorking || state.Workspaces[0].Working != 1 {
		t.Fatalf("working call not visible first: %+v", state)
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", id, "alpha", "delete", map[string]any{"confirm": true}), 409)
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "alpha", "alpha", "delete", map[string]any{"confirm": true}), 409)
}

func TestSidebarRunningOldSessionSurvivesFirstPage(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	old := consoleRemote(t, rt, "alpha")
	for i := 0; i < 105; i++ {
		consoleRemote(t, rt, "alpha")
	}
	if _, err := rt.state.DB().Exec(`UPDATE remote_sessions SET last_active_at=1 WHERE id=?`, old); err != nil {
		t.Fatal(err)
	}
	rt.consoleMu.Lock()
	rt.consoleCalls = map[string]consoleLiveCall{old: {Workspace: "alpha", Count: 1}}
	rt.consoleMu.Unlock()
	c := consoleLogin(t, rt)
	state := getSidebar(t, c, "?limit=100")
	if len(state.Sessions) != 100 || state.Sessions[0].ID != old {
		t.Fatal("running session hidden by pagination")
	}
}

func TestSidebarAdmissionUpdatesActivityAndBlocksDeletedEffect(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	id := consoleRemote(t, rt, "alpha")
	c := consoleLogin(t, rt)
	if _, err := rt.state.DB().Exec(`UPDATE remote_sessions SET last_active_at=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	effect := false
	handler := rt.instrumentTool("read", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		effect = true
		return sidebarToolResult(), nil
	})
	_, err := handler(context.Background(), mcpresult.Request(map[string]any{"remote_session_id": id}))
	if err != nil || !effect {
		t.Fatal("valid tool blocked")
	}
	var active int64
	if err = rt.state.DB().QueryRow(`SELECT last_active_at FROM remote_sessions WHERE id=?`, id).Scan(&active); err != nil || active < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("activity not touched: %d %v", active, err)
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", id, "alpha", "delete", map[string]any{"confirm": true}), 200)
	effect = false
	_, _ = handler(context.Background(), mcpresult.Request(map[string]any{"remote_session_id": id}))
	if effect {
		t.Fatal("effect admitted after deletion")
	}
}

func TestSidebarRequiresOperatorAndCSRF(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	c := &consoleTestClient{handler: rt.consoleHandler()}
	input := map[string]any{"kind": "workspace", "id": "alpha", "workspace": "alpha", "action": "pin", "pinned": true, "revision": 0}
	requireSidebarStatus(t, c.request(t, "POST", "sidebar", input), 401)
	c = consoleLogin(t, rt)
	c.csrf = "wrong"
	requireSidebarStatus(t, c.request(t, "POST", "sidebar", input), 403)
}
