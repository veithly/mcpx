package server

import (
	"net/http"
	"time"

	"mcpx/internal/audit"
	"mcpx/internal/control"
)

type sidebarMutation struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Action    string `json:"action"`
	Revision  *int64 `json:"revision"`
	Pinned    *bool  `json:"pinned,omitempty"`
	TargetID  string `json:"target_id,omitempty"`
	Placement string `json:"placement,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
}

func (c *consoleHandler) sidebar(w http.ResponseWriter, r *http.Request) {
	var input sidebarMutation
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	if input.Revision == nil || input.ID == "" || input.Workspace == "" || (input.Kind != "workspace" && input.Kind != "session") {
		consoleError(w, 400, "invalid sidebar target or missing revision")
		return
	}
	runtime := c.runtime
	runtime.consoleMu.Lock()
	defer runtime.consoleMu.Unlock()
	workspaces, sessions, state, err := c.snapshot(r.Context())
	if err != nil {
		consoleError(w, 500, "cannot read sidebar state")
		return
	}
	if *input.Revision != state.Revision {
		consoleError(w, 409, "侧栏已在其他页面发生变化，请刷新后重试")
		return
	}
	prefs := sidebarPreference(state.Items)
	current := control.SidebarItem{Kind: input.Kind, ID: input.ID, Workspace: input.Workspace}
	if pref, ok := prefs[sidebarKey(input.Kind, input.ID)]; ok {
		current = pref
	}
	found, working := false, false
	if input.Kind == "workspace" {
		for _, ws := range workspaces {
			if ws.Name == input.ID && ws.Name == input.Workspace {
				found = true
				working = ws.Working > 0
			}
		}
	} else {
		for _, session := range sessions {
			if session.ID == input.ID && session.Workspace == input.Workspace {
				found = true
				working = session.IsWorking
			}
		}
	}
	if !found {
		consoleError(w, 404, "Workspace 或会话已被移除，或不属于当前项目")
		return
	}
	changed := []control.SidebarItem{}
	switch input.Action {
	case "pin":
		if input.Pinned == nil {
			consoleError(w, 400, "pinned is required")
			return
		}
		current.Pinned = *input.Pinned
		changed = append(changed, current)
	case "move":
		if input.Placement != "before" && input.Placement != "after" {
			consoleError(w, 400, "invalid drop placement")
			return
		}
		// Ordering changes presentation only, never a conversation's execution root.
		group := []control.SidebarItem{}
		if input.Kind == "workspace" {
			for _, ws := range workspaces {
				if ws.Pinned == current.Pinned {
					group = append(group, control.SidebarItem{Kind: "workspace", ID: ws.Name, Workspace: ws.Name, Pinned: ws.Pinned})
				}
			}
		} else {
			for _, session := range sessions {
				if session.Workspace == input.Workspace && session.Pinned == current.Pinned && session.IsWorking == working {
					group = append(group, control.SidebarItem{Kind: "session", ID: session.ID, Workspace: session.Workspace, Pinned: session.Pinned})
				}
			}
		}
		target := -1
		ordered := []control.SidebarItem{}
		for _, item := range group {
			if item.ID == current.ID {
				continue
			}
			if item.ID == input.TargetID {
				target = len(ordered)
			}
			ordered = append(ordered, item)
		}
		if target < 0 {
			consoleError(w, 409, "只能在同一项目、相同运行与置顶分组内排序；拖动不会更改项目归属")
			return
		}
		if input.Placement == "after" {
			target++
		}
		ordered = append(ordered, control.SidebarItem{})
		copy(ordered[target+1:], ordered[target:])
		ordered[target] = current
		for index := range ordered {
			ordered[index].Position = index + 1
		}
		changed = ordered
	case "delete":
		if !input.Confirm {
			consoleError(w, 400, "请确认从工作台移除；项目文件不会删除")
			return
		}
		if working {
			consoleError(w, 409, "任务仍在工作，请先停止执行并等待工具调用结束后再删除")
			return
		}
		// Include queued/waiting operations, not only visible terminal processes.
		var active int
		err = runtime.state.DB().QueryRowContext(r.Context(), `SELECT COUNT(*) FROM operations WHERE workspace_name=? AND (?='' OR remote_session_id=?) AND state IN ('queued','running','waiting_confirmation')`, input.Workspace, sessionTarget(input), sessionTarget(input)).Scan(&active)
		if err != nil {
			consoleError(w, 500, "cannot check active operations")
			return
		}
		if active > 0 {
			consoleError(w, 409, "仍有未结束的操作，请先中断会话再删除")
			return
		}
		now := time.Now().UnixMilli()
		current.DeletedAt = now
		changed = append(changed, current)
		if input.Kind == "workspace" {
			for _, session := range sessions {
				if session.Workspace == input.Workspace {
					pref := prefs[sidebarKey("session", session.ID)]
					pref.Kind = "session"
					pref.ID = session.ID
					pref.Workspace = session.Workspace
					pref.DeletedAt = now
					changed = append(changed, pref)
				}
			}
		}
	default:
		consoleError(w, 400, "action must be pin, move or delete")
		return
	}
	if err = runtime.writeAudit(audit.Event{Tool: "console.sidebar", Workspace: input.Workspace, RemoteSessionID: sessionTarget(input), Status: "requested", Detail: map[string]any{"action": input.Action, "kind": input.Kind, "id": input.ID}}); err != nil {
		consoleError(w, 500, "audit unavailable")
		return
	}
	if err = runtime.control.SaveSidebar(r.Context(), state.Revision, changed); err != nil {
		consoleError(w, 409, err.Error())
		return
	}
	c.notice(r.Context(), input.Workspace, sessionTarget(input), "operator.sidebar", input.Action+": "+input.ID)
	consoleJSON(w, 200, map[string]any{"sidebar_revision": state.Revision + 1, "action": input.Action, "id": input.ID, "project_files_untouched": true})
}
func sessionTarget(input sidebarMutation) string {
	if input.Kind == "session" {
		return input.ID
	}
	return ""
}
