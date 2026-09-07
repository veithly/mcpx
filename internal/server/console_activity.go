package server

import (
	"context"
	"fmt"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
)

type consoleLiveCall struct {
	Workspace string
	Count     int
}

// The same lock admits tools and removes sidebar objects. A session cannot be
// deleted between the running check and a new tool beginning its effect.
func (r *Runtime) beginConsoleCall(ctx context.Context, req envelope.Request) (func(), error) {
	if r.control == nil {
		return func() {}, nil
	}
	id := remoteSessionID(req)
	if id == "" {
		return func() {}, nil
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	r.consoleMu.Lock()
	defer r.consoleMu.Unlock()
	session, err := r.remote.Get(ctx, principal, id)
	if err != nil {
		return nil, err
	}
	if err = r.validateSessionWorkspace(ctx, req, session); err != nil {
		return nil, err
	}
	if r.consoleCalls == nil {
		r.consoleCalls = map[string]consoleLiveCall{}
	}
	current := r.consoleCalls[id]
	current.Workspace = session.WorkspaceName
	current.Count++
	r.consoleCalls[id] = current
	return func() {
		r.consoleMu.Lock()
		defer r.consoleMu.Unlock()
		current := r.consoleCalls[id]
		current.Count--
		if current.Count <= 0 {
			delete(r.consoleCalls, id)
		} else {
			r.consoleCalls[id] = current
		}
		touchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = r.state.DB().ExecContext(touchCtx, `UPDATE remote_sessions SET last_active_at=MAX(last_active_at,?) WHERE id=?`, time.Now().UnixMilli(), id)
	}, nil
}

func (r *Runtime) validateSessionWorkspace(ctx context.Context, req envelope.Request, session remotesession.Session) error {
	if path := stringPayload(req.Payload, "workspace_path"); path != "" && !sameProjectPath(config.ExpandHome(path), session.WorkspacePath) {
		return fmt.Errorf("%w: requested project path differs from this session; open a new session for the requested path", remotesession.ErrInvalidInput)
	}
	name := req.Workspace
	if name == "" {
		name = stringPayload(req.Payload, "workspace")
	}
	if name != "" && name != session.WorkspaceName {
		return fmt.Errorf("%w: requested workspace %q differs from session workspace %q; open a session for the intended project instead of reusing this ID", remotesession.ErrInvalidInput, name, session.WorkspaceName)
	}
	if r.control != nil {
		deleted, err := r.control.Deleted(ctx, session.WorkspaceName, session.ID)
		if err != nil {
			return err
		}
		if deleted {
			return fmt.Errorf("%w: workspace or conversation removed from workbench", remotesession.ErrNotFound)
		}
	}
	if ws, ok := r.reg.Get(session.WorkspaceName); ok && !sameProjectPath(ws.Path, session.WorkspacePath) {
		return fmt.Errorf("%w: registered project path differs from session binding; open a new session", remotesession.ErrInvalidInput)
	}
	return nil
}
