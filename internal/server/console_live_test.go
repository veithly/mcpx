package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcpx/internal/observation"
	"mcpx/internal/terminal"
)

func TestConsoleInterruptStopsOnlyTargetSessionAndDoesNotRepeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process lifecycle test")
	}
	rt := newWorkspaceRuntime(t, "alpha")
	first := consoleRemote(t, rt, "alpha")
	other := consoleRemote(t, rt, "alpha")
	if first == other {
		t.Fatal("test requires two independent sessions")
	}
	ws, _ := rt.reg.Get("alpha")
	start := func(session string) *terminal.Task {
		task, err := rt.tasks.StartRemote(context.Background(), session, "alpha", ws.Path, "sleep 20")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = task.Kill() })
		return task
	}
	target := start(first)
	untouched := start(other)
	if target.StatusView()["status"] != terminal.TaskRunning || untouched.StatusView()["status"] != terminal.TaskRunning {
		t.Fatalf("test tasks did not start: target=%+v other=%+v", target.StatusView(), untouched.StatusView())
	}
	c := consoleLogin(t, rt)
	request := map[string]any{"workspace": "alpha", "session_id": first, "kind": "interrupt", "body": "先停止并复盘", "client_key": "stop-once"}
	result := c.request(t, "POST", "requests", request)
	if result.Code != 202 {
		t.Fatalf("interrupt=%d %s", result.Code, result.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for target.StatusView()["status"] == terminal.TaskRunning && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if target.StatusView()["status"] == terminal.TaskRunning {
		t.Fatal("target process still running")
	}
	if untouched.StatusView()["status"] != terminal.TaskRunning {
		t.Fatalf("different session changed: %+v; stop response=%s", untouched.StatusView(), result.Body.String())
	}
	items, err := rt.control.List(context.Background(), "alpha", first)
	if err != nil || len(items) != 1 || items[0].Kind != "interrupt" {
		t.Fatalf("interrupt notice missing: %+v %v", items, err)
	}
	subsequent := start(first)
	result = c.request(t, "POST", "requests", request)
	if result.Code != 202 {
		t.Fatalf("replay=%d %s", result.Code, result.Body.String())
	}
	if subsequent.StatusView()["status"] != terminal.TaskRunning {
		t.Fatal("network retry stopped a new task")
	}
}

func TestConsoleBackgroundCommandDoesNotMasqueradeAsAgentWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process lifecycle test")
	}
	rt := newWorkspaceRuntime(t, "alpha")
	background := consoleRemote(t, rt, "alpha")
	live := consoleRemote(t, rt, "alpha")
	ws, _ := rt.reg.Get("alpha")
	task, err := rt.tasks.StartRemote(context.Background(), background, "alpha", ws.Path, "sleep 20")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = task.Kill() })
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	if _, err = rt.state.DB().Exec(`UPDATE terminal_tasks SET started_at=?,updated_at=? WHERE id=?`, old, old, task.ID); err != nil {
		t.Fatal(err)
	}
	// The task was started directly through the manager and the live call is
	// injected below, so no Agent instruction touched either session;
	// backdate the instruction anchor to model a background process left
	// running after its Agent turn ended.
	if _, err = rt.state.DB().Exec(`UPDATE remote_sessions SET last_active_at=? WHERE id IN(?,?)`, old, background, live); err != nil {
		t.Fatal(err)
	}

	rt.consoleMu.Lock()
	rt.consoleCalls = map[string]consoleLiveCall{live: {Workspace: "alpha", Count: 1}}
	rt.consoleMu.Unlock()
	c := consoleLogin(t, rt)
	state := getSidebar(t, c, "")
	if len(state.Workspaces) != 1 || !state.Workspaces[0].IsActive || state.Workspaces[0].Working != 1 || state.Workspaces[0].ActiveSessions != 1 || state.Workspaces[0].PreferredSessionID != live {
		t.Fatalf("live workspace state=%+v", state.Workspaces)
	}
	var backgroundState, liveState *consoleSession
	for i := range state.Sessions {
		session := &state.Sessions[i]
		switch session.ID {
		case background:
			backgroundState = session
		case live:
			liveState = session
		}
	}
	if backgroundState == nil || backgroundState.RunningTasks != 1 || backgroundState.IsWorking || backgroundState.RecentCommand {
		t.Fatalf("background command masqueraded as work: %+v", backgroundState)
	}
	if liveState == nil || !liveState.IsWorking || liveState.RunningCalls != 1 {
		t.Fatalf("real live call missing: %+v", liveState)
	}
	if len(state.Sessions) == 0 || state.Sessions[0].ID != live {
		t.Fatalf("live session not sorted first: %+v", state.Sessions)
	}

	rt.consoleMu.Lock()
	delete(rt.consoleCalls, live)
	rt.consoleMu.Unlock()
	state = getSidebar(t, c, "")
	if state.Workspaces[0].Working != 0 || state.Workspaces[0].IsActive {
		t.Fatalf("background command kept project active: %+v", state.Workspaces[0])
	}
	for _, session := range state.Sessions {
		if session.ID == background && session.RunningTasks != 1 {
			t.Fatalf("background command disappeared from terminal state: %+v", session)
		}
	}
	requireSidebarStatus(t, mutateSidebar(t, c, "session", background, "alpha", "delete", map[string]any{"confirm": true}), 409)
}

func TestConsoleSSEReplaysAfterLastEventID(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	sid := consoleRemote(t, rt, "alpha")
	other := consoleRemote(t, rt, "beta")
	ctx := context.Background()
	c := consoleLogin(t, rt)
	record := func(ws, session, summary string) {
		if err := rt.observation.Record(ctx, observation.Event{Workspace: ws, RemoteSessionID: session, Type: "operator.test", Summary: summary}); err != nil {
			t.Fatal(err)
		}
	}
	record("alpha", sid, "first")
	record("beta", other, "do-not-leak")
	record("alpha", sid, "second")
	history, _, err := rt.observation.store.Query(ctx, observation.HistoryQuery{Workspace: "alpha", SessionID: sid, Kinds: []string{"operator.test"}, Ascending: true, Limit: 10})
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v %v", history, err)
	}
	server := httptest.NewServer(c.handler)
	defer server.Close()
	streamCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, "GET", server.URL+consoleAPI+"stream?workspace=alpha&session_id="+sid+"&after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c.cookie)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(history[0].Sequence, 10))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream status=%d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	kind := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if kind == "activity" && strings.HasPrefix(line, "data: ") {
			var event observation.Event
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Sequence != history[1].Sequence || event.Summary != "second" || event.Workspace != "alpha" {
				t.Fatalf("wrong replay or cross-workspace leak: %+v", event)
			}
			cancel()
			return
		}
	}
	t.Fatalf("stream did not replay persisted event: %v", scanner.Err())
}
