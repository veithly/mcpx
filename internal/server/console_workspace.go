package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"mcpx/internal/audit"
	"mcpx/internal/control"
	"mcpx/internal/observation"
)

func (c *consoleHandler) target(ctx context.Context, workspace, session string) error {
	if _, ok := c.runtime.reg.Get(workspace); !ok {
		return errors.New("workspace not found")
	}
	if c.runtime.control != nil {
		deleted, err := c.runtime.control.Deleted(ctx, workspace, session)
		if err != nil {
			return err
		}
		if deleted {
			return errors.New("workspace or conversation has been removed")
		}
	}
	if session != "" {
		var stored string
		if err := c.runtime.state.DB().QueryRowContext(ctx, `SELECT workspace_name FROM remote_sessions WHERE id=?`, session).Scan(&stored); err != nil || stored != workspace {
			return errors.New("session does not belong to workspace")
		}
	}
	return nil
}

func (c *consoleHandler) state(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 3000 {
			consoleError(w, 400, "invalid limit")
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			consoleError(w, 400, "invalid offset")
			return
		}
		offset = n
	}
	c.runtime.consoleMu.Lock()
	workspaces, sessions, state, err := c.snapshot(r.Context())
	c.runtime.consoleMu.Unlock()
	if err != nil {
		consoleError(w, 500, "cannot load sidebar")
		return
	}
	total := len(sessions)
	start := min(offset, total)
	end := min(start+limit, total)
	next := 0
	if end < total {
		next = end
	}
	removed := []string{}
	for _, item := range state.Items {
		if item.Kind == "session" && item.DeletedAt > 0 {
			removed = append(removed, item.ID)
		}
	}
	consoleJSON(w, 200, map[string]any{"removed_session_ids": removed, "workspaces": workspaces, "sessions": sessions[start:end], "next_offset": next, "total_sessions": total, "sidebar_revision": state.Revision, "version": c.runtime.build.Version, "server_time": time.Now().UnixMilli()})
}

func (c *consoleHandler) addWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	c.runtime.consoleMu.Lock()
	defer c.runtime.consoleMu.Unlock()
	ws, created, err := c.runtime.registerProject(r.Context(), input.Path, "")
	if err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	if err := c.runtime.control.RestoreWorkspace(r.Context(), ws.Name); err != nil {
		consoleError(w, 409, err.Error())
		return
	}
	mode, err := c.runtime.control.Mode(r.Context(), ws.Name)
	if err != nil {
		consoleError(w, 500, "cannot read access mode")
		return
	}
	status := 200
	if created {
		status = 201
	}
	consoleJSON(w, status, map[string]any{"name": ws.Name, "path": ws.Path, "description": ws.Description, "access_mode": mode, "pinned": false, "position": 0, "working_sessions": 0})
}

func (c *consoleHandler) setAccess(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Workspace string `json:"workspace"`
		Mode      string `json:"mode"`
		Confirm   bool   `json:"confirm_full_access"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	if err := c.target(r.Context(), input.Workspace, ""); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	if input.Mode == control.FullAccess && !input.Confirm {
		consoleError(w, 400, "需要操作员明确选择完全访问模式")
		return
	}
	if err := c.runtime.writeAudit(audit.Event{Tool: "console.access", Workspace: input.Workspace, Status: "requested", Detail: map[string]any{"mode": input.Mode}}); err != nil {
		consoleError(w, 500, "audit unavailable")
		return
	}
	if err := c.runtime.control.SetMode(r.Context(), input.Workspace, input.Mode); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	c.notice(r.Context(), input.Workspace, "", "operator.access_changed", "Workspace access mode: "+input.Mode)
	consoleJSON(w, 200, map[string]string{"workspace": input.Workspace, "access_mode": input.Mode})
}

func (c *consoleHandler) detail(w http.ResponseWriter, r *http.Request) {
	ws, session := r.URL.Query().Get("workspace"), r.URL.Query().Get("session_id")
	if err := c.target(r.Context(), ws, session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	tasks := []map[string]any{}
	if session != "" {
		var err error
		tasks, err = c.runtime.tasks.List(session, 100)
		if err != nil {
			consoleError(w, 500, "cannot read execution tasks")
			return
		}
	}
	for _, task := range tasks {
		if command, ok := task["command"].(string); ok {
			task["command"] = observation.SanitizeIntent(command)
		}
	}
	requests, err := c.runtime.control.List(r.Context(), ws, session)
	if err != nil {
		consoleError(w, 500, "cannot read user requests")
		return
	}
	approvals := []map[string]any{}
	if session != "" {
		for _, p := range c.runtime.approvals.ListRemoteSession(session) {
			decision, err := c.runtime.control.Decision(r.Context(), p.ID, p.CommandDigest)
			if err != nil {
				consoleError(w, 500, "cannot read approvals")
				return
			}
			approvals = append(approvals, map[string]any{"id": p.ID, "tool": p.Tool, "summary": observation.SanitizeIntent(p.Summary), "command": observation.SanitizeIntent(p.Command), "scope": p.Scope, "created_at": p.CreatedAt, "decision": decision, "can_decide": p.Tool == "command_execute" && p.CommandDigest != ""})
		}
	}
	mode, err := c.runtime.control.Mode(r.Context(), ws)
	if err != nil {
		consoleError(w, 500, "cannot read access mode")
		return
	}
	response := map[string]any{"tasks": tasks, "requests": requests, "approvals": approvals, "access_mode": mode}
	if session != "" {
		var current consoleSession
		err := c.runtime.state.DB().QueryRowContext(r.Context(), `SELECT rs.id,rs.workspace_name,rs.workspace_path,rs.label,rs.description,rs.status,rs.last_active_at,
 (SELECT COUNT(*) FROM terminal_tasks t WHERE t.remote_session_id=rs.id AND t.status='running'),
 (SELECT COUNT(*) FROM operations o WHERE o.remote_session_id=rs.id AND o.state IN ('queued','running')),
 COALESCE((SELECT MAX(MAX(t.started_at,COALESCE(t.finished_at,0))) FROM terminal_tasks t WHERE t.remote_session_id=rs.id),0)
 FROM remote_sessions rs WHERE rs.id=? AND rs.workspace_name=?`, session, ws).Scan(&current.ID, &current.Workspace, &current.Path, &current.Label, &current.Description, &current.Status, &current.LastActive, &current.RunningTasks, &current.RunningOperations, &current.LastCommandAt)
		if err != nil {
			consoleError(w, 404, "session no longer available")
			return
		}
		c.runtime.consoleMu.Lock()
		current.RunningCalls = c.runtime.consoleCalls[session].Count
		c.runtime.consoleMu.Unlock()
		current.IsWorking = current.RunningTasks+current.RunningCalls+current.RunningOperations > 0
		current.RecentCommand = recentCommand(current, time.Now().UnixMilli())
		response["session_info"] = current
	}
	consoleJSON(w, 200, response)
}

func (c *consoleHandler) notice(ctx context.Context, ws, session, kind, summary string) {
	if c.runtime.observation != nil {
		_ = c.runtime.observation.Record(ctx, observation.Event{Workspace: ws, RemoteSessionID: session, Type: kind, Summary: summary, Status: "succeeded"})
	}
}
