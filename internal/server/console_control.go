package server

import (
	"context"
	"net/http"
	"time"

	"mcpx/internal/audit"
	"mcpx/internal/operation"
)

func (c *consoleHandler) sendRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Workspace string `json:"workspace"`
		Session   string `json:"session_id"`
		Task      string `json:"execution_task_id"`
		Kind      string `json:"kind"`
		Body      string `json:"body"`
		Key       string `json:"client_key"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	if input.Kind != "steer" && input.Kind != "interrupt" {
		consoleError(w, 400, "kind must be steer or interrupt")
		return
	}
	if err := c.target(r.Context(), input.Workspace, input.Session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	if input.Kind == "interrupt" && input.Session == "" {
		consoleError(w, 400, "中断必须指定一个会话，避免停止其他项目的任务")
		return
	}
	if input.Task != "" {
		if input.Kind != "interrupt" {
			consoleError(w, 400, "execution task requires interrupt")
			return
		}
		if _, err := c.runtime.tasks.Get(input.Session, input.Task); err != nil {
			consoleError(w, 404, "execution task not found in session")
			return
		}
	}
	if err := c.runtime.writeAudit(audit.Event{Tool: "console.request", Workspace: input.Workspace, RemoteSessionID: input.Session, Status: "requested", Detail: map[string]any{"kind": input.Kind, "execution_task_id": input.Task}}); err != nil {
		consoleError(w, 500, "audit unavailable")
		return
	}
	// Once accepted, a disconnected browser must not cancel the local stop midway.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	item, err := c.runtime.control.Enqueue(ctx, input.Workspace, input.Session, input.Kind, input.Body, input.Key)
	if err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	response := map[string]any{"request": item, "stopped_tasks": []string{}, "cancelled_operations": []string{}, "errors": []string{}}
	if input.Kind == "interrupt" && !item.Replayed {
		stopped, cancelled, failures := c.stopSession(ctx, input.Workspace, input.Session, input.Task)
		response["stopped_tasks"] = stopped
		response["cancelled_operations"] = cancelled
		response["errors"] = failures
	}
	c.notice(ctx, input.Workspace, input.Session, "operator.request", input.Kind+" request queued: "+item.ID)
	consoleJSON(w, 202, response)
}

// Stop only identifiers selected from this workspace/session. Query results are
// closed before accessing the managers, which may use the same SQLite pool.
func (c *consoleHandler) stopSession(ctx context.Context, workspace, session, taskID string) ([]string, []string, []string) {
	stopped, cancelled, failures := []string{}, []string{}, []string{}
	if taskID == "" && c.runtime.operations != nil {
		rows, err := c.runtime.state.DB().QueryContext(ctx, `SELECT id FROM operations WHERE workspace_name=? AND remote_session_id=? AND state IN ('queued','running','waiting_confirmation')`, workspace, session)
		ids := []string{}
		if err == nil {
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil {
			failures = append(failures, "cannot enumerate operations")
		} else {
			for _, id := range ids {
				if record, cancelErr := c.runtime.operations.Cancel(ctx, id); cancelErr == nil {
					if record.State == operation.StateCancelled {
						cancelled = append(cancelled, id)
					}
				} else {
					failures = append(failures, "cannot cancel operation "+id)
				}
			}
		}
	}
	ids := []string{}
	if taskID != "" {
		ids = append(ids, taskID)
	} else {
		rows, err := c.runtime.state.DB().QueryContext(ctx, `SELECT id FROM terminal_tasks WHERE workspace_name=? AND remote_session_id=? AND status='running'`, workspace, session)
		if err == nil {
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		if err != nil {
			failures = append(failures, "cannot enumerate execution tasks")
		}
	}
	for _, id := range ids {
		task, err := c.runtime.tasks.Get(session, id)
		if err != nil {
			failures = append(failures, "cannot find task "+id)
			continue
		}
		if err = task.Kill(); err != nil {
			failures = append(failures, "cannot stop task "+id)
		} else {
			stopped = append(stopped, id)
		}
	}
	return stopped, cancelled, failures
}

func (c *consoleHandler) decideApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Workspace string `json:"workspace"`
		Session   string `json:"session_id"`
		ID        string `json:"id"`
		Decision  string `json:"decision"`
		Key       string `json:"client_key"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	if err := c.target(r.Context(), input.Workspace, input.Session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	pending, ok := c.runtime.approvals.Get(input.ID)
	if !ok || pending.RemoteSessionID != input.Session || pending.Workspace != input.Workspace || pending.Tool != "command_execute" || pending.CommandDigest == "" {
		consoleError(w, 404, "审批已过期、已处理或不属于当前会话")
		return
	}
	if input.Decision != "approved" && input.Decision != "denied" {
		consoleError(w, 400, "invalid decision")
		return
	}
	if err := c.runtime.writeAudit(audit.Event{Tool: "console.approval", Workspace: input.Workspace, RemoteSessionID: input.Session, Status: input.Decision, Detail: map[string]any{"pending_id": pending.ID, "command_digest": pending.CommandDigest}}); err != nil {
		consoleError(w, 500, "audit unavailable")
		return
	}
	if err := c.runtime.control.Decide(r.Context(), pending.ID, pending.CommandDigest, input.Decision); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	item, err := c.runtime.control.Enqueue(r.Context(), input.Workspace, input.Session, "approval", operatorApprovalMessage(pending.ID, input.Decision, pending.Summary), input.Key)
	if err != nil {
		consoleError(w, 500, "审批已保存，但通知入队失败；请重试以投递通知")
		return
	}
	c.notice(r.Context(), input.Workspace, input.Session, "operator.approval", input.Decision+": "+pending.ID)
	consoleJSON(w, 200, map[string]any{"decision": input.Decision, "request": item, "execution": "awaiting_agent_retry"})
}
