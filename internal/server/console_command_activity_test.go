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
		{"inside window", consoleSession{LastCommandAt: now - 179999}, true},
		{"at boundary", consoleSession{LastCommandAt: now - 180000}, false},
		{"future", consoleSession{LastCommandAt: now + 1}, false},
		{"long task", consoleSession{RunningTasks: 1, LastCommandAt: now - 900000}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recentCommand(tc.s, now); got != tc.want {
				t.Fatalf("recent=%v want=%v", got, tc.want)
			}
		})
	}
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
	requireSidebarStatus(t, mutateSidebar(t, c, "workspace", "beta", "beta", "pin", map[string]any{"pinned": true}), 200)
	state := getSidebar(t, c, "?limit=1")
	if state.Workspaces[0].Name != "alpha" || !state.Workspaces[0].IsActive || state.Workspaces[0].ActiveSessions != 2 || state.Workspaces[0].SessionCount != 2 || state.Workspaces[0].PreferredSessionID != b {
		t.Fatalf("workspace=%+v", state.Workspaces)
	}
	if _, err := rt.state.DB().Exec(`UPDATE terminal_tasks SET status='exited',finished_at=? WHERE id='fixture-running'`, now-240000); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.state.DB().Exec(`UPDATE terminal_tasks SET started_at=?,finished_at=? WHERE id='fixture-recent'`, now-250000, now-240000); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.state.DB().Exec(`UPDATE remote_sessions SET last_active_at=? WHERE id IN(?,?)`, now, a, other); err != nil {
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
	if response := c.request(t, "GET", "detail?workspace=beta&session_id="+b, nil); response.Code == 200 {
		t.Fatal("cross-workspace session accepted")
	}
}
