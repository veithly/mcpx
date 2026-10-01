package server

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConsoleCommandWindowExcludesPollingAndHasExactBoundary(t *testing.T) {
	now := int64(1_800_000_000_000)
	for _, tc := range []struct {
		name string
		s    consoleSession
		want bool
	}{
		{"poll only", consoleSession{LastActive: now, IsWorking: true, RunningCalls: 1}, false},
		{"inside window", consoleSession{LastCommandAt: now - 299999}, true},
		{"instruction inside window", consoleSession{LastInstructionAt: now - 60000}, true},
		{"at boundary", consoleSession{LastCommandAt: now - 300000}, false},
		{"future", consoleSession{LastCommandAt: now + 1}, false},
		{"old background task", consoleSession{RunningTasks: 1, LastCommandAt: now - 900000}, false},
		{"old instruction and task", consoleSession{RunningTasks: 1, LastCommandAt: now - 900000, LastInstructionAt: now - 900000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recentCommand(tc.s, now); got != tc.want {
				t.Fatalf("recent=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestConsoleShowsNativeProcessAsActive(t *testing.T) {
	rt, sessionID, _, call := programmingFixture(t)
	result := call("exec_command", map[string]any{"cmd": "sleep 20", "yield_time_ms": 250})
	if result.IsError || programmingWire(t, result)["session_id"] == nil {
		t.Fatalf("native command did not remain active: %+v", result)
	}
	c := consoleLogin(t, rt)
	state := getSidebar(t, c, "")
	var workspace consoleWorkspace
	for _, item := range state.Workspaces {
		if item.Name == "project" {
			workspace = item
		}
	}
	if !workspace.IsActive || workspace.Working != 1 || workspace.PreferredSessionID != sessionID {
		t.Fatalf("native command missing from active project: %+v", workspace)
	}
	var session consoleSession
	for _, item := range state.Sessions {
		if item.ID == sessionID {
			session = item
		}
	}
	if session.RunningTasks != 1 || !session.IsWorking || !session.RecentCommand {
		t.Fatalf("native command missing from active session: %+v", session)
	}
	response := c.request(t, "GET", "detail?workspace=project&session_id="+sessionID, nil)
	if response.Code != 200 {
		t.Fatalf("detail %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Session         consoleSession `json:"session_info"`
		NativeProcesses []int          `json:"native_process_sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Session.RunningTasks != 1 || len(detail.NativeProcesses) != 1 {
		t.Fatalf("native command missing from detail: %+v", detail)
	}
}

func TestConsoleShowsRecentNativeCommand(t *testing.T) {
	rt, sessionID, _, call := programmingFixture(t)
	result := call("exec_command", map[string]any{"cmd": "printf done", "yield_time_ms": 1000})
	if result.IsError {
		t.Fatalf("native command failed: %+v", result)
	}
	state := getSidebar(t, consoleLogin(t, rt), "")
	for _, session := range state.Sessions {
		if session.ID != sessionID {
			continue
		}
		if !session.RecentCommand || session.LastCommandAt == 0 {
			t.Fatalf("recent native command missing: %+v", session)
		}
		return
	}
	t.Fatal("native command session missing from sidebar")
}

func TestConsoleActiveWorkspaceAndPreferredSessionSurvivePagination(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	c := consoleLogin(t, rt)
	a := consoleRemote(t, rt, "alpha")
	b := consoleRemote(t, rt, "alpha")
	other := consoleRemote(t, rt, "beta")
	now := time.Now().UnixMilli()
	ws, _ := rt.reg.Get("alpha")
	insert := func(id, sid, status string, start int64, end any) {
		t.Helper()
		_, err := rt.state.DB().Exec(`INSERT INTO terminal_tasks(id,remote_session_id,workspace_name,workspace_path,command,status,log_path,started_at,finished_at,updated_at) VALUES(?,?,'alpha',?,'fixture',?,'',?,?,?)`, id, sid, ws.Path, status, start, end, start)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("fixture-recent", a, "exited", now-8000, now-2000)
	insert("fixture-running", b, "running", now-900000, nil)
	// No recent Agent instruction anywhere: session creation stamps
	// last_active_at, so backdate every fixture session to keep the activity
	// verdict driven by the seeded command rows alone.
	if _, err := rt.state.DB().Exec(`UPDATE remote_sessions SET last_active_at=? WHERE id IN(?,?,?)`, now-900000, a, b, other); err != nil {
		t.Fatal(err)
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "beta", "beta", "pin", map[string]any{"pinned": true}), 200)
	state := getSidebar(t, c, "?limit=1")
	if state.Workspaces[0].Name != "alpha" || !state.Workspaces[0].IsActive || state.Workspaces[0].ActiveSessions != 1 || state.Workspaces[0].SessionCount != 2 || state.Workspaces[0].PreferredSessionID != a {
		t.Fatalf("workspace=%+v", state.Workspaces)
	}
	for _, session := range state.Sessions {
		if session.ID == b && (session.RunningTasks != 0 || session.IsWorking || session.RecentCommand) {
			t.Fatalf("durable running receipt leaked into live state: %+v", session)
		}
	}
	if _, err := rt.state.DB().Exec(`UPDATE terminal_tasks SET status='exited',finished_at=? WHERE id='fixture-running'`, now-320000); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.state.DB().Exec(`UPDATE terminal_tasks SET started_at=?,finished_at=? WHERE id='fixture-recent'`, now-310000, now-300000); err != nil {
		t.Fatal(err)
	}
	state = getSidebar(t, c, "")
	if state.Workspaces[0].Name != "beta" {
		t.Fatalf("expired activity displaced pin: %+v", state.Workspaces)
	}
	for _, w := range state.Workspaces {
		if w.IsActive {
			t.Fatalf("poll renewed command activity: %+v", w)
		}
	}
	// The selected session is resolvable independently of the sidebar's page.
	response := c.request(t, "GET", "detail?workspace=alpha&session_id="+b, nil)
	if response.Code != 200 {
		t.Fatalf("detail %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Session consoleSession `json:"session_info"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Session.ID != b || detail.Session.Workspace != "alpha" {
		t.Fatalf("wrong session detail %+v", detail)
	}
	if detail.Session.RunningTasks != 0 || detail.Session.IsWorking || detail.Session.RecentCommand {
		t.Fatalf("stale durable task leaked into detail liveness: %+v", detail.Session)
	}
	if response := c.request(t, "GET", "detail?workspace=beta&session_id="+b, nil); response.Code == 200 {
		t.Fatal("cross-workspace session accepted")
	}
}
