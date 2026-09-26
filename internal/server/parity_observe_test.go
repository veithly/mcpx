package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/terminal"
)

// All effects stay in newWorkspaceRuntime's t.TempDir. Invoke only observe
// through MCP HTTP; fixture processes never load a login shell.
func parityobserveFixture(t *testing.T) (*Runtime, string, func(map[string]any) map[string]any) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires /bin/cat and /usr/bin/printf")
	}
	rt := newWorkspaceRuntime(t, "observe-fixture")
	session := operationTestSession(t, rt, "observe-fixture")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-observe", Version: "1"}, nil)
	rt.registerTools(protocol)
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "parity-observe-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return rt, session.ID, func(args map[string]any) map[string]any {
		t.Helper()
		if _, exists := args["remote_session_id"]; !exists {
			args["remote_session_id"] = session.ID
		}
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "observe", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		wire, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("observe has no structured result: %+v", result)
		}
		return wire
	}
}

func parityobserveTask(t *testing.T, rt *Runtime, sid string, spec terminal.ProcessSpec) *terminal.Task {
	t.Helper()
	ws, _ := rt.reg.Get("observe-fixture")
	task, err := rt.tasks.StartRemoteProcessWithObservationContext("parity-observe-fixture", "parity-observe-fixture", "execute", sid, ws.Name, ws.Path, "isolated observe fixture", spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !task.Wait(ctx) {
		_ = task.Kill()
		t.Fatal("isolated process did not finish")
	}
	return task
}

func TestParityobserveNormalTaskLogsAndErrors(t *testing.T) {
	rt, sid, call := parityobserveFixture(t)
	task := parityobserveTask(t, rt, sid, terminal.ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "printf 'hello中文'; printf 'fixture-error' >&2; exit 7"}})
	status := call(map[string]any{"execution_task_id": task.ID})
	data := status["data"].(map[string]any)
	t.Logf("task: status=%v task_status=%v outcome=%v exit_code=%v", status["status"], data["status"], data["outcome"], data["exit_code"])
	if status["status"] != "succeeded" || data["status"] != "exited" || data["outcome"] != "error" || data["exit_code"] != float64(7) {
		t.Fatalf("incorrect task outcome: %+v", status)
	}
	logs := call(map[string]any{"view": "logs", "execution_task_id": task.ID, "stdout_offset": 5, "stderr_offset": 8})
	data = logs["data"].(map[string]any)
	t.Logf("logs: status=%v stdout=%q stderr=%q offsets=%v/%v", logs["status"], data["stdout"], data["stderr"], data["stdout_next_offset"], data["stderr_next_offset"])
	if logs["status"] != "succeeded" || data["stdout"] != "中文" || data["stderr"] != "error" || data["stdout_next_offset"] != float64(11) || data["stderr_next_offset"] != float64(13) {
		t.Fatalf("incorrect logs: %+v", logs)
	}
	other := operationTestSession(t, rt, "observe-fixture")
	for _, args := range []map[string]any{
		{"view": "task"},
		{"view": "task", "execution_task_id": "task_missing"},
		{"view": "task", "execution_task_id": task.ID, "remote_session_id": other.ID},
	} {
		wire := call(args)
		want := "EXECUTION_TASK_NOT_FOUND"
		if args["execution_task_id"] == nil {
			want = "EXECUTION_TASK_ID_REQUIRED"
		}
		t.Logf("invalid target: status=%v code=%s", wire["status"], errorCode(wire))
		if wire["status"] != "failed" || errorCode(wire) != strings.ToLower(want) {
			t.Fatalf("incorrect target error: %+v", wire)
		}
	}
}

func TestParityobserveRestoredTaskLogs(t *testing.T) {
	rt, sid, call := parityobserveFixture(t)
	want := "persisted-observe-output"
	task := parityobserveTask(t, rt, sid, terminal.ProcessSpec{Executable: "/usr/bin/printf", Args: []string{"%s", want}})
	args := map[string]any{"view": "logs", "execution_task_id": task.ID}
	before := call(args)["data"].(map[string]any)
	if before["stdout"] != want {
		t.Fatalf("fixture output missing before restore: %+v", before)
	}
	var logPath string
	if err := rt.state.DB().QueryRow("SELECT log_path FROM terminal_tasks WHERE id = ?", task.ID).Scan(&logPath); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(strings.TrimSuffix(logPath, ".log") + ".stdout.log")
	if err != nil || string(disk) != want {
		t.Fatalf("persisted stdout missing: bytes=%d err=%v", len(disk), err)
	}
	// Exercise the real SQLite restore path without a service restart.
	rt.tasks, err = terminal.NewPersistentTaskManager(rt.state.DB(), filepath.Dir(logPath))
	if err != nil {
		t.Fatal(err)
	}
	after := call(args)
	data := after["data"].(map[string]any)
	t.Logf("restore: before_stdout=%q disk_bytes=%d status=%v task_status=%v stdout=%q next_offset=%v actions=%v", before["stdout"], len(disk), after["status"], data["status"], data["stdout"], data["stdout_next_offset"], after["actions"])
	if after["status"] != "succeeded" || data["stdout"] != want || data["stdout_next_offset"] != float64(len(want)) {
		t.Fatalf("restored task silently lost persisted stdout")
	}
}

func TestParityobserveUTF8LogContinuation(t *testing.T) {
	rt, sid, call := parityobserveFixture(t)
	want := strings.Repeat("a", (256<<10)-1) + "中文tail"
	task := parityobserveTask(t, rt, sid, terminal.ProcessSpec{Executable: "/bin/cat", Stdin: want})
	first := call(map[string]any{"view": "logs", "execution_task_id": task.ID})
	data := first["data"].(map[string]any)
	actions, ok := first["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("missing observe continuation: status=%v offset=%v", first["status"], data["stdout_next_offset"])
	}
	action := actions[0].(map[string]any)
	if action["id"] != "observe" {
		t.Fatalf("unexpected continuation: %+v", action)
	}
	second := call(action["arguments"].(map[string]any))
	secondData := second["data"].(map[string]any)
	part1 := data["stdout"].(string)
	part2 := secondData["stdout"].(string)
	tail := part1
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	t.Logf("UTF8: first_status=%v second_status=%v offsets=%v/%v first_tail=%q second=%q input_bytes=%d joined_bytes=%d", first["status"], second["status"], data["stdout_next_offset"], secondData["stdout_next_offset"], tail, part2, len(want), len(part1+part2))
	if part1+part2 != want {
		t.Fatal("following the server's exact next_action corrupts UTF-8 output")
	}
}
