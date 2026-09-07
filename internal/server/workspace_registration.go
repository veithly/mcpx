package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcpx/internal/audit"
	"mcpx/internal/config"
	"mcpx/internal/remotesession"
	"mcpx/internal/workspace"
)

func sameProjectPath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	x, e1 := os.Stat(a)
	y, e2 := os.Stat(b)
	return e1 == nil && e2 == nil && os.SameFile(x, y)
}

// registerProject uses an explicit existing absolute path, never the Runtime
// working directory. Reuse this for UI selection and session(workspace_path).
// Caller serializes lifecycle changes with consoleMu.
func (r *Runtime) registerProject(ctx context.Context, raw, expectedName string) (workspace.Workspace, bool, error) {
	path := config.ExpandHome(strings.TrimSpace(raw))
	if !filepath.IsAbs(path) {
		return workspace.Workspace{}, false, fmt.Errorf("%w: project path must be absolute", remotesession.ErrInvalidInput)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || filepath.Dir(path) == path {
		return workspace.Workspace{}, false, fmt.Errorf("%w: select an existing project folder, not a disk root", remotesession.ErrInvalidInput)
	}
	r.workspaceConfigMu.Lock()
	defer r.workspaceConfigMu.Unlock()
	cfg, err := config.LoadGlobal(r.globalCfgPath)
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	for _, entry := range cfg.Workspaces {
		if sameProjectPath(entry.Path, path) {
			if expectedName != "" && expectedName != entry.Name {
				return workspace.Workspace{}, false, fmt.Errorf("%w: this path belongs to workspace %q", remotesession.ErrInvalidInput, entry.Name)
			}
			if err = r.reg.Register(entry); err != nil {
				return workspace.Workspace{}, false, err
			}
			ws, _ := r.reg.Get(entry.Name)
			return ws, false, nil
		}
	}
	name := filepath.Base(path)
	if expectedName != "" && expectedName != name {
		return workspace.Workspace{}, false, fmt.Errorf("%w: workspace name must match project directory %q", remotesession.ErrInvalidInput, name)
	}
	for _, entry := range cfg.Workspaces {
		if entry.Name == name {
			return workspace.Workspace{}, false, fmt.Errorf("%w: workspace name %q already belongs to a different path", remotesession.ErrInvalidInput, name)
		}
	}
	if existing, ok := r.reg.Get(name); ok && !sameProjectPath(existing.Path, path) {
		return workspace.Workspace{}, false, fmt.Errorf("%w: registered name already in use", remotesession.ErrInvalidInput)
	}
	if err = r.writeAudit(audit.Event{Tool: "workspace.register", Workspace: name, Status: "requested", Detail: map[string]any{"path": path}}); err != nil {
		return workspace.Workspace{}, false, err
	}
	entry := config.WorkspaceEntry{Name: name, Path: path}
	cfg.Workspaces = append(cfg.Workspaces, entry)
	if err = config.WriteGlobal(r.globalCfgPath, cfg); err != nil {
		return workspace.Workspace{}, false, err
	}
	if err = r.reg.Register(entry); err != nil {
		return workspace.Workspace{}, false, err
	}
	ws, _ := r.reg.Get(name)
	return ws, true, nil
}
