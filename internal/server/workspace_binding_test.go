package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mcpx/internal/envelope"
	"mcpx/internal/remotesession"
)

func TestProjectPathCreatesCorrectWorkspaceWithoutBootstrapSession(t *testing.T) {
	rt := newWorkspaceRuntime(t, "mcpx")
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "target-project")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	result := callEnvelope(t, rt.toolSessionOpen, ctx, map[string]any{"workspace_path": path, "label": "actual project"})
	if errorCode(result) != "" {
		t.Fatalf("session path failed: %+v", result)
	}
	var wrong, total int
	if err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM remote_sessions WHERE workspace_name='mcpx'`).Scan(&wrong); err != nil {
		t.Fatal(err)
	}
	if err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM remote_sessions`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if wrong != 0 || total != 1 {
		t.Fatalf("unexpected helper sessions: mcpx=%d total=%d", wrong, total)
	}
	var actualName, actualPath string
	if err := rt.state.DB().QueryRow(`SELECT workspace_name,workspace_path FROM remote_sessions`).Scan(&actualName, &actualPath); err != nil {
		t.Fatal(err)
	}
	if actualName != "target-project" || !sameProjectPath(actualPath, path) {
		t.Fatalf("wrong binding %s %s", actualName, actualPath)
	}
	if ws, ok := rt.reg.Get(actualName); !ok || !sameProjectPath(ws.Path, path) {
		t.Fatal("new workspace not registered")
	}
}
func TestProjectSelectionNeverFallsBackToMCPX(t *testing.T) {
	rt := newWorkspaceRuntime(t, "mcpx")
	ctx := context.Background()
	p, _ := rt.principalFromContext(ctx)
	if _, err := rt.createRemoteSession(ctx, p, envelope.Request{Payload: map[string]any{}}, ""); !errors.Is(err, errWorkspaceNotFound) {
		t.Fatalf("missing project fell back: %v", err)
	}
	var count int
	if err := rt.state.DB().QueryRow(`SELECT COUNT(*) FROM remote_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unwanted session created: %d %v", count, err)
	}
}
func TestProjectResumeRejectsConflictingNameAndPath(t *testing.T) {
	rt := newWorkspaceRuntime(t, "mcpx", "other")
	sid := consoleRemote(t, rt, "mcpx")
	other, _ := rt.reg.Get("other")
	for _, args := range []map[string]any{{"remote_session_id": sid, "workspace": "other"}, {"remote_session_id": sid, "workspace_path": other.Path}} {
		result := callEnvelope(t, rt.toolSessionOpen, context.Background(), args)
		if errorCode(result) == "" {
			t.Fatalf("conflict silently ignored: %+v", args)
		}
	}
	ws, _ := rt.reg.Get("mcpx")
	p, _ := rt.principalFromContext(context.Background())
	stored, err := rt.remote.Get(context.Background(), p, sid)
	if err != nil || stored.WorkspaceName != "mcpx" || !sameProjectPath(stored.WorkspacePath, ws.Path) {
		t.Fatalf("original session was rebound: %+v %v", stored, err)
	}
	req := envelope.Request{RemoteSessionID: sid, Workspace: "other", Payload: map[string]any{}}
	if err := rt.validateSessionWorkspace(context.Background(), req, stored); err == nil {
		t.Fatal("command boundary ignored conflicting project")
	}
}
func TestProjectSameBasenameCannotRedirectRegisteredWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	other := filepath.Join(t.TempDir(), "alpha")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := rt.reg.Get("alpha")
	rt.consoleMu.Lock()
	_, _, err := rt.registerProject(context.Background(), other, "")
	rt.consoleMu.Unlock()
	if err == nil {
		t.Fatal("same basename hijacked workspace")
	}
	after, _ := rt.reg.Get("alpha")
	if before.Path != after.Path {
		t.Fatal("workspace path changed")
	}
}
func TestProjectCreateIdempotencyCannotCrossWorkspaces(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	ctx := context.Background()
	p, _ := rt.principalFromContext(ctx)
	a, _ := rt.reg.Get("alpha")
	b, _ := rt.reg.Get("beta")
	first, err := rt.remote.Create(ctx, p, remotesession.CreateInput{WorkspaceName: "alpha", WorkspacePath: a.Path, ClientRequestID: "project-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.remote.Create(ctx, p, remotesession.CreateInput{WorkspaceName: "beta", WorkspacePath: b.Path, ClientRequestID: "project-key"})
	if !errors.Is(err, remotesession.ErrConflict) {
		t.Fatalf("cross-project key=%v", err)
	}
	replay, err := rt.createRemoteSession(ctx, p, envelope.Request{Payload: map[string]any{"client_request_id": "project-key"}}, "alpha")
	if err != nil || replay.Session.ID != first.Session.ID || !sameProjectPath(replay.Session.WorkspacePath, a.Path) {
		t.Fatalf("same-project replay lost binding: %+v %v", replay, err)
	}
}
