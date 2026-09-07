package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mcpx/internal/audit"
	"mcpx/internal/config"
	"mcpx/internal/control"
	"mcpx/internal/observation"
)

func (c *consoleHandler) target(ctx context.Context, workspace, session string) error {
	if _, ok := c.runtime.reg.Get(workspace); !ok {
		return errors.New("workspace not found")
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
	workspaces := []map[string]any{}
	for _, ws := range c.runtime.reg.List() {
		mode, err := c.runtime.control.Mode(r.Context(), ws.Name)
		if err != nil {
			consoleError(w, 500, "cannot read access settings")
			return
		}
		workspaces = append(workspaces, map[string]any{"name": ws.Name, "path": ws.Path, "description": ws.Description, "access_mode": mode})
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 100000 {
		consoleError(w, 400, "invalid offset")
		return
	}
	rows, err := c.runtime.state.DB().QueryContext(r.Context(), `SELECT rs.id,rs.workspace_name,rs.label,rs.description,rs.status,rs.last_active_at,
 (SELECT COUNT(*) FROM terminal_tasks t WHERE t.remote_session_id=rs.id AND t.status='running')
 FROM remote_sessions rs ORDER BY rs.last_active_at DESC,rs.id DESC LIMIT 101 OFFSET ?`, offset)
	if err != nil {
		consoleError(w, 500, "cannot load sessions")
		return
	}
	sessions := []map[string]any{}
	for rows.Next() {
		var id, ws, label, description, status string
		var active int64
		var running int
		if err = rows.Scan(&id, &ws, &label, &description, &status, &active, &running); err != nil {
			rows.Close()
			consoleError(w, 500, "cannot decode sessions")
			return
		}
		sessions = append(sessions, map[string]any{"id": id, "workspace": ws, "label": label, "description": description, "status": status, "last_active_at": active, "running_tasks": running})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		consoleError(w, 500, "cannot load sessions")
		return
	}
	next := 0
	if len(sessions) > 100 {
		sessions = sessions[:100]
		next = offset + 100
	}
	consoleJSON(w, 200, map[string]any{"workspaces": workspaces, "sessions": sessions, "next_offset": next, "version": c.runtime.build.Version, "server_time": time.Now().UnixMilli()})
}

func (c *consoleHandler) addWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	path := config.ExpandHome(strings.TrimSpace(input.Path))
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Clean(path)) == filepath.Clean(path) {
		consoleError(w, 400, "请选择已有项目的绝对路径，而不是磁盘根目录")
		return
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		consoleError(w, 400, "项目目录不存在或无法访问")
		return
	}
	if filepath.Dir(path) == path {
		consoleError(w, 400, "项目路径不能指向磁盘根目录")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		consoleError(w, 400, "项目路径必须是已有目录")
		return
	}
	name := filepath.Base(path)
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	cfg, err := config.LoadGlobal(c.runtime.globalCfgPath)
	if err != nil {
		consoleError(w, 500, "cannot read workspace configuration")
		return
	}
	// Re-adding an existing physical directory must not rename a custom
	// workspace or redirect the sessions that are already bound to it.
	for _, entry := range cfg.Workspaces {
		registeredPath, resolveErr := filepath.EvalSymlinks(entry.Path)
		if resolveErr != nil {
			registeredPath = filepath.Clean(entry.Path)
		}
		if registeredPath == path {
			if err := c.runtime.reg.Register(entry); err != nil {
				consoleError(w, 409, err.Error())
				return
			}
			mode, err := c.runtime.control.Mode(r.Context(), entry.Name)
			if err != nil {
				consoleError(w, 500, "cannot read access settings")
				return
			}
			consoleJSON(w, 200, map[string]any{"name": entry.Name, "path": entry.Path, "description": entry.Description, "access_mode": mode})
			return
		}
	}
	for _, entry := range cfg.Workspaces {
		if entry.Name == name && filepath.Clean(entry.Path) != path {
			consoleError(w, 409, "已存在同名 Workspace；请先使用不同的项目目录名")
			return
		}
	}
	if existing, ok := c.runtime.reg.Get(name); ok && existing.Path != path {
		consoleError(w, 409, "workspace name already in use")
		return
	}
	if err = c.runtime.writeAudit(audit.Event{Tool: "console.workspace", Workspace: name, Status: "requested", Detail: map[string]any{"path": path}}); err != nil {
		consoleError(w, 500, "audit unavailable")
		return
	}
	if err = config.RegisterWorkspace(c.runtime.globalCfgPath, path); err != nil {
		consoleError(w, 500, "cannot persist workspace")
		return
	}
	if err = c.runtime.reg.Register(config.WorkspaceEntry{Name: name, Path: path}); err != nil {
		consoleError(w, 409, err.Error())
		return
	}
	consoleJSON(w, 201, map[string]any{"name": name, "path": path, "description": "", "access_mode": control.Approval})
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
	consoleJSON(w, 200, map[string]any{"tasks": tasks, "requests": requests, "approvals": approvals, "access_mode": mode})
}

func (c *consoleHandler) notice(ctx context.Context, ws, session, kind, summary string) {
	if c.runtime.observation != nil {
		_ = c.runtime.observation.Record(ctx, observation.Event{Workspace: ws, RemoteSessionID: session, Type: kind, Summary: summary, Status: "succeeded"})
	}
}
