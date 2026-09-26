package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
	"mcpx/internal/observation"
	"mcpx/internal/state"
)

func TestVerificationLiveQueriesBypassEvenRestoredReplays(t *testing.T) {
	for _, test := range []struct{ tool, action string }{
		{"observe", ""},
		{"operation_manage", "wait"}, {"operation_manage", "status"}, {"operation_manage", "result"},
		{"workspace", ""}, {"session", "open"}, {"session", "list"},
		{"artifact", "read"}, {"artifact", "list"}, {"plan", "read"},
		{"skill_tool", "describe"}, {"mcp_tool", "list"},
		{"runtime_read", ""}, {"environment_read", ""}, {"screenshot_capture", ""},
	} {
		t.Run(test.tool+"/"+test.action, func(t *testing.T) {
			rt := &Runtime{}
			args := map[string]any{"remote_session_id": "session", "action": test.action}
			key := rt.toolReplayKey(test.tool, args)
			rt.toolReplays.restore(key, mcpresult.NewText("stale result"), time.Now())
			entry, delivered, result := rt.replayDeliver(context.Background(), context.Background(), test.tool, mcpresult.Request(args), false)
			if entry != nil || delivered || result != nil {
				t.Fatal("live query reused or registered an effect-replay entry")
			}
		})
	}
}

func TestVerificationEmptyLogsExplainTaskOutcome(t *testing.T) {
	data := map[string]any{"execution_task_id": "task_empty", "status": "exited", "outcome": "succeeded", "exit_code": 0, "stdout": "", "stderr": ""}
	text := commandOutputText(context.Background(), data, "Task log chunk returned.")
	for _, want := range []string{"task_empty", "status=exited", "outcome=succeeded", "exit_code=0", "No stdout/stderr", "separate file"} {
		if !strings.Contains(text, want) {
			t.Errorf("empty log reply omitted %q: %s", want, text)
		}
	}
}

func TestVerificationLogContinuationUsesReturnedByteOffsets(t *testing.T) {
	data := map[string]any{"execution_task_id": "task_large", "stdout": "中文abcdef", "stderr": "error-text", "stdout_offset": 5, "stderr_offset": 3}
	capTaskExecutionOutput(data, 7)
	setTaskLogContinuation(data, "session")
	if data["stdout_next_offset"] != 12 || data["stderr_next_offset"] != 10 {
		t.Fatalf("incorrect byte offsets: %+v", data)
	}
	action, _ := data["next_action"].(map[string]any)
	args, _ := action["arguments"].(map[string]any)
	if action["tool"] != "observe" || args["view"] != "logs" || args["execution_task_id"] != "task_large" || args["stdout_offset"] != 12 || args["stderr_offset"] != 10 {
		t.Fatalf("truncated output lacks an exact continuation: %+v", action)
	}
}

func TestVerificationLargeTaskLogCanBeReadToEnd(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ws, _ := rt.reg.Get("demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	want := strings.Repeat("log-line\n", 40000)
	if err := os.WriteFile(filepath.Join(ws.Path, "large-log.txt"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := rt.tasks.StartRemoteWithObservationContext(context.Background(), "req_large", "req_large", "execute", remoteID, "demo", ws.Path, "cat large-log.txt")
	if err != nil {
		t.Fatal(err)
	}
	// The output also passes through the durable observation sink; race
	// instrumentation must not turn that I/O overhead into a false failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !task.Wait(ctx) {
		t.Fatal("log fixture did not finish")
	}
	first := rt.taskResultData(task, 0, 0)
	if first["output_truncated"] != true {
		t.Fatal("partial disk log chunk must advertise remaining output")
	}
	next := first["stdout_next_offset"].(int)
	second := rt.taskResultData(task, next, 0)
	if first["stdout"].(string)+second["stdout"].(string) != want || second["output_truncated"] == true {
		t.Fatal("following returned byte offsets lost log output")
	}
}

func TestVerificationShellStreamsAndFailureCodes(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ws, _ := rt.reg.Get("demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	for _, shell := range []string{"sh", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not installed: %v", shell, err)
			}
			task, err := rt.tasks.StartRemoteWithObservationContext(context.Background(), "req_shell_"+shell, "req_shell_"+shell, "execute", remoteID, "demo", ws.Path, shell+" -c 'printf shell-out; printf shell-err >&2; exit 7'")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if !task.Wait(ctx) {
				t.Fatal("shell did not reach a terminal state")
			}
			data := rt.taskResultData(task, 0, 0)
			if data["stdout"] != "shell-out" || data["stderr"] != "shell-err" || data["exit_code"] != 7 || data["outcome"] != "error" {
				t.Fatalf("shell result was lost: %+v", data)
			}
		})
	}
}

func TestVerificationHistoryTimeParsing(t *testing.T) {
	want := time.Date(2026, 9, 11, 12, 19, 28, 123000000, time.UTC)
	for _, value := range []string{"2026-09-11T12:19:28.123Z", "2026-09-11T20:19:28.123+08:00", "1789129168123"} {
		t.Run(value, func(t *testing.T) {
			got, err := historyTimePayload(map[string]any{"created_after": value}, "created_after")
			if err != nil || !got.Equal(want) {
				t.Fatalf("parsed %q as %s, want %s: %v", value, got, want, err)
			}
		})
	}
	for _, value := range []string{"1789129168123junk", "2026-not-a-time", "0", "-1"} {
		if _, err := historyTimePayload(map[string]any{"created_after": value}, "created_after"); err == nil {
			t.Errorf("invalid timestamp %q was accepted", value)
		}
	}
}

func TestVerificationHistoryFindsExecuteByTimeAndKind(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	when := time.Date(2026, 9, 11, 12, 19, 28, 0, time.UTC)
	err := rt.observation.Record(context.Background(), observation.Event{
		Workspace: "demo", RemoteSessionID: remoteID, RequestID: "req_verification_history",
		Type: observation.TypeToolCompleted, Tool: "execute", Status: "succeeded",
		Command: "printf verification-history", CreatedAt: when,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"time", map[string]any{"created_after": "2026-09-11T12:18:00Z", "created_before": "2026-09-11T12:20:00Z"}},
		{"command", map[string]any{"kinds": []string{"command"}}},
		{"task", map[string]any{"kinds": []string{"task"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]any{"remote_session_id": remoteID, "view": "history", "keyword": "verification-history"}
			for key, value := range test.args {
				args[key] = value
			}
			result := callEnvelope(t, rt.toolObserve, context.Background(), args)
			data, _ := result["data"].(map[string]any)
			events, _ := data["events"].([]any)
			if !statusOK(result) || len(events) != 1 {
				t.Fatalf("history lost the executed command: %+v", result)
			}
		})
	}
}

func TestVerificationObservationRetainsResultTaskID(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bridge := &observationBridge{store: observation.NewStore(db.DB())}
	req := envelope.Request{RequestID: "req_verification_task", Workspace: "demo", RemoteSessionID: "session"}
	result := compactToolResult(map[string]any{
		"execution_task_id": "task_verification", "status": "running", "command": "build-native",
	}, "Command is running.")
	if err := bridge.RecordToolCompleted(context.Background(), "execute", req, map[string]any{}, result, nil, interactionTiming{}); err != nil {
		t.Fatal(err)
	}
	events, _, err := bridge.store.Query(context.Background(), observation.HistoryQuery{Workspace: "demo", SessionID: "session", ExecutionTaskIDs: []string{"task_verification"}})
	if err != nil || len(events) != 1 {
		t.Fatalf("result task cannot be found through history: events=%+v err=%v", events, err)
	}
	if !strings.Contains(string(events[0].Output), "task_verification") {
		t.Fatalf("human observation omitted the execution task: %s", events[0].Output)
	}
}
