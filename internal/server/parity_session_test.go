package server

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A live task must remain discoverable and prevent close beyond a history page.
func TestParitysessionRunningTaskBeyondHistoryPage(t *testing.T) {
	for _, completed := range []int{99, 100} {
		t.Run(fmt.Sprintf("completed_%d", completed), func(t *testing.T) {
			rt := newWorkspaceRuntime(t, "demo")
			rt.cfg.Discovery.MCP.Enabled = false
			rt.cfg.Discovery.Skills.Enabled = false
			rt.cfg.Discovery.Instructions.GlobalAgentsPath = ""
			ctx := context.Background()
			remoteID := consoleRemote(t, rt, "demo")
			ws, _ := rt.reg.Get("demo")
			running, err := rt.tasks.StartRemote(ctx, remoteID, "demo", ws.Path, testSleepCommand(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			tx, err := rt.state.DB().Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for i := 0; i < completed; i++ {
				at := running.StartedAt.UnixMilli() + int64(i+1)
				_, err := tx.Exec(`INSERT INTO terminal_tasks
					(id, remote_session_id, workspace_name, workspace_path, command, status, log_path, started_at, finished_at, updated_at)
					VALUES (?, ?, 'demo', ?, 'completed fixture', 'exited', '', ?, ?, ?)`,
					fmt.Sprintf("completed-%03d", i), remoteID, ws.Path, at, at, at)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			handler := rt.toolHandlers["session"]
			resumed := callEnvelope(t, handler, ctx, map[string]any{"remote_session_id": remoteID})
			if !statusOK(resumed) {
				t.Fatalf("resume failed: %+v", resumed)
			}
			data := resumed["data"].(map[string]any)
			tasks, _ := data["tasks"].([]any)
			closed := callEnvelope(t, handler, ctx, map[string]any{"action": "close", "remote_session_id": remoteID})
			state := running.StatusView()["status"]
			t.Logf("completed=%d resume.tasks_scope=%v resume.tasks=%d close.status=%v close.error=%q live_task.status=%v",
				completed, data["tasks_scope"], len(tasks), closed["public_status"], errorCode(closed), state)
			if fmt.Sprint(state) != "running" {
				t.Fatal("fixture task must still be running")
			}
			if len(tasks) != 1 {
				t.Errorf("resume hid the running task: got %d, want 1", len(tasks))
			}
			if errorCode(closed) != "running_task" {
				t.Errorf("close must reject a live task; got status=%v code=%q", closed["status"], errorCode(closed))
			}
		})
	}
}
