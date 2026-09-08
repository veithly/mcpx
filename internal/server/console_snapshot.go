package server

import (
	"context"
	"sort"
	"time"

	"mcpx/internal/control"
)

type consoleWorkspace struct {
	Name               string `json:"name"`
	Path               string `json:"path"`
	Description        string `json:"description"`
	Mode               string `json:"access_mode"`
	Pinned             bool   `json:"pinned"`
	Position           int    `json:"position"`
	Working            int    `json:"working_sessions"`
	SessionCount       int    `json:"session_count"`
	ActiveSessions     int    `json:"active_sessions"`
	LastCommandAt      int64  `json:"last_command_at"`
	IsActive           bool   `json:"is_active"`
	PreferredSessionID string `json:"preferred_session_id"`
}
type consoleSession struct {
	ID                string `json:"id"`
	Workspace         string `json:"workspace"`
	Path              string `json:"workspace_path"`
	Label             string `json:"label"`
	Description       string `json:"description"`
	Status            string `json:"status"`
	LastActive        int64  `json:"last_active_at"`
	LastCommandAt     int64  `json:"last_command_at"`
	RecentCommand     bool   `json:"recent_command"`
	RunningTasks      int    `json:"running_tasks"`
	RunningCalls      int    `json:"running_calls"`
	RunningOperations int    `json:"running_operations"`
	IsWorking         bool   `json:"is_working"`
	Pinned            bool   `json:"pinned"`
	Position          int    `json:"position"`
}

func sidebarKey(kind, id string) string { return kind + ":" + id }
func sidebarPreference(items []control.SidebarItem) map[string]control.SidebarItem {
	out := make(map[string]control.SidebarItem, len(items))
	for _, item := range items {
		out[sidebarKey(item.Kind, item.ID)] = item
	}
	return out
}
func manualPosition(position int) int {
	if position == 0 {
		return int(^uint(0) >> 1)
	}
	return position
}
func sessionLess(a, b consoleSession) bool {
	if a.IsWorking != b.IsWorking {
		return a.IsWorking
	}
	if a.Pinned != b.Pinned {
		return a.Pinned
	}
	if a.Position != b.Position {
		return manualPosition(a.Position) < manualPosition(b.Position)
	}
	if a.LastActive != b.LastActive {
		return a.LastActive > b.LastActive
	}
	return a.ID < b.ID
}

// Caller holds consoleMu; preferences and in-flight state cannot change between
// producing the ordering and its mutation revision.
func (c *consoleHandler) snapshot(ctx context.Context) ([]consoleWorkspace, []consoleSession, control.SidebarState, error) {
	r := c.runtime
	state, err := r.control.Sidebar(ctx)
	if err != nil {
		return nil, nil, state, err
	}
	prefs := sidebarPreference(state.Items)
	workspaces := []consoleWorkspace{}
	visible := map[string]bool{}
	for _, ws := range r.reg.List() {
		pref := prefs[sidebarKey("workspace", ws.Name)]
		if pref.DeletedAt > 0 {
			continue
		}
		mode, err := r.control.Mode(ctx, ws.Name)
		if err != nil {
			return nil, nil, state, err
		}
		visible[ws.Name] = true
		workspaces = append(workspaces, consoleWorkspace{Name: ws.Name, Path: ws.Path, Description: ws.Description, Mode: mode, Pinned: pref.Pinned, Position: pref.Position})
	}
	rows, err := r.state.DB().QueryContext(ctx, `SELECT rs.id,rs.workspace_name,rs.workspace_path,rs.label,rs.description,rs.status,
 MAX(rs.last_active_at, COALESCE((SELECT MAX(e.created_at) FROM observation_events e WHERE e.remote_session_id=rs.id),0)),
 (SELECT COUNT(*) FROM terminal_tasks t WHERE t.remote_session_id=rs.id AND t.status='running'),
 (SELECT COUNT(*) FROM operations o WHERE o.remote_session_id=rs.id AND o.state IN ('queued','running')),
 COALESCE((SELECT MAX(MAX(t.started_at,COALESCE(t.finished_at,0))) FROM terminal_tasks t WHERE t.remote_session_id=rs.id),0)
 FROM remote_sessions rs`)
	if err != nil {
		return nil, nil, state, err
	}
	sessions := []consoleSession{}
	working := map[string]int{}
	byWorkspace := map[string][]consoleSession{}
	now := time.Now().UnixMilli()
	for rows.Next() {
		var s consoleSession
		if err = rows.Scan(&s.ID, &s.Workspace, &s.Path, &s.Label, &s.Description, &s.Status, &s.LastActive, &s.RunningTasks, &s.RunningOperations, &s.LastCommandAt); err != nil {
			rows.Close()
			return nil, nil, state, err
		}
		pref := prefs[sidebarKey("session", s.ID)]
		if pref.DeletedAt > 0 || !visible[s.Workspace] {
			continue
		}
		s.Pinned = pref.Pinned
		s.Position = pref.Position
		s.RunningCalls = r.consoleCalls[s.ID].Count
		s.IsWorking = s.RunningTasks+s.RunningCalls+s.RunningOperations > 0
		s.RecentCommand = recentCommand(s, now)
		if s.IsWorking {
			working[s.Workspace]++
		}
		sessions = append(sessions, s)
		byWorkspace[s.Workspace] = append(byWorkspace[s.Workspace], s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, state, err
	}
	for i := range workspaces {
		ws := &workspaces[i]
		ws.Working = working[ws.Name]
		members := byWorkspace[ws.Name]
		ws.SessionCount = len(members)
		for _, s := range members {
			ws.LastCommandAt = max(ws.LastCommandAt, s.LastCommandAt)
			if s.RecentCommand {
				ws.ActiveSessions++
			}
		}
		ws.IsActive = ws.ActiveSessions > 0
		if len(members) > 0 {
			sort.SliceStable(members, func(a, b int) bool { return preferredSessionLess(members[a], members[b]) })
			ws.PreferredSessionID = members[0].ID
		}
	}
	sort.SliceStable(workspaces, func(i, j int) bool {
		a, b := workspaces[i], workspaces[j]
		if a.IsActive != b.IsActive {
			return a.IsActive
		}
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Position != b.Position {
			return manualPosition(a.Position) < manualPosition(b.Position)
		}
		return false
	})
	sort.SliceStable(sessions, func(i, j int) bool { return sessionLess(sessions[i], sessions[j]) })
	return workspaces, sessions, state, nil
}

const consoleActiveWindowMS int64 = 3 * 60 * 1000

// Read/poll events never renew command activity. Long-running commands remain active.
func recentCommand(s consoleSession, now int64) bool {
	return s.RunningTasks > 0 || (s.LastCommandAt > 0 && s.LastCommandAt <= now && now-s.LastCommandAt < consoleActiveWindowMS)
}

// Resolve over all sessions before pagination; never switch an open view on poll.
func preferredSessionLess(a, b consoleSession) bool {
	if (a.RunningTasks > 0) != (b.RunningTasks > 0) {
		return a.RunningTasks > 0
	}
	if a.IsWorking != b.IsWorking {
		return a.IsWorking
	}
	if a.RecentCommand != b.RecentCommand {
		return a.RecentCommand
	}
	if a.LastCommandAt != b.LastCommandAt {
		return a.LastCommandAt > b.LastCommandAt
	}
	if a.LastActive != b.LastActive {
		return a.LastActive > b.LastActive
	}
	return a.ID < b.ID
}
