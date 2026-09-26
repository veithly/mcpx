package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/approval"
	"mcpx/internal/audit"
	"mcpx/internal/auth"
	"mcpx/internal/envelope"
	workspacefile "mcpx/internal/file"
	"mcpx/internal/mcpresult"
	"mcpx/internal/patch"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
	"mcpx/internal/unifiedexec"
)

func isProgrammingTool(name string) bool {
	return name == "exec_command" || name == "write_stdin" || name == "apply_patch"
}

// Programming results deliberately cross MCP unchanged: no ARC envelope,
// synthetic Operation, automatic effect replay, or duplicated text rendering.
func (r *Runtime) addProgrammingTool(s *mcp.Server, tool mcp.Tool, handler mcp.ToolHandler) {
	tool.OutputSchema = nil
	validate := toolValidator(tool)
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		r.processLifecycle.RLock()
		defer r.processLifecycle.RUnlock()
		started := time.Now()
		ctx, runtime := ensureRuntimeContext(ctx, mcpresult.Header(req), started)
		ctx = withToolInvocationName(ctx, tool.Name)
		defer func() {
			if recovered := recover(); recovered != nil {
				result = programmingFailure("EXECUTION_RUNTIME_ERROR", "The programming tool failed internally; inspect any partial effects before retrying.")
			}
			if err != nil {
				result = programmingFailure("EXECUTION_ERROR", err.Error())
				err = nil
			}
			if result == nil {
				result = programmingFailure("EXECUTION_ERROR", "Execution returned no result")
			}
			mcpresult.MetaSet(result, "mcpx/request_id", runtime.RequestID)
			if r.observation != nil {
				envReq, parseErr := r.parseEnv(ctx, req)
				if parseErr == nil {
					if envReq.RemoteSessionID == "" {
						if p, e := r.principalFromContext(ctx); e == nil {
							envReq.RemoteSessionID = r.boundRemoteSessionID(ctx, p)
						}
					}
					recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
					_ = r.observation.RecordToolCompleted(recordCtx, tool.Name, envReq, observationArguments(tool.Name, mcpresult.Arguments(req)), result, nil, makeInteractionTiming(runtime.StartedAtMs, started, time.Now()))
					_ = r.observation.RecordToolResult(recordCtx, tool.Name, envReq, observationArguments(tool.Name, mcpresult.Arguments(req)), result, makeInteractionTiming(runtime.StartedAtMs, started, time.Now()))
					cancel()
				}
			}
		}()
		if validationErr := validate(req); validationErr != nil {
			return programmingFailure("INVALID_ARGUMENTS", validationErr.Error()), nil
		}
		return handler(ctx, req)
	}
	if r.toolHandlers == nil {
		r.toolHandlers = map[string]mcp.ToolHandler{}
	}
	if r.toolMeta == nil {
		r.toolMeta = map[string]toolAnnotation{}
	}
	r.toolHandlers[tool.Name] = wrapped
	r.toolMeta[tool.Name] = mutatingToolAnnotation
	r.toolIndexMu.Lock()
	if r.toolIndex == nil {
		r.toolIndex = map[string]mcp.Tool{}
	}
	r.toolIndex[tool.Name] = tool
	r.toolIndexMu.Unlock()
	s.AddTool(&tool, wrapped)
}

func programmingFailure(code, message string) *mcp.CallToolResult {
	result := mcpresult.NewStructured(map[string]any{"error": map[string]any{"code": code, "message": message}, "output": message}, message)
	result.IsError = true
	return result
}

func programmingExecResult(result unifiedexec.ExecResult) *mcp.CallToolResult {
	raw, _ := json.Marshal(result)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	return mcpresult.NewStructured(data, result.Output)
}

func programmingExecOutcome(result unifiedexec.ExecResult, err error) *mcp.CallToolResult {
	if err == nil {
		return programmingExecResult(result)
	}
	// A cancelled poll may already have consumed output. Preserve its process
	// handle and partial bytes so request history can recover the same process.
	out := programmingExecResult(result)
	data := out.StructuredContent.(map[string]any)
	data["error"] = map[string]any{"code": "EXECUTION_ERROR", "message": err.Error()}
	out.Content = append(out.Content, &mcp.TextContent{Text: err.Error()})
	out.IsError = true
	return out
}

func (r *Runtime) programmingRequest(ctx context.Context, req *mcp.CallToolRequest) (envelope.Request, auth.Principal, remotesession.Session, *mcp.CallToolResult) {
	envReq, principal, remote, fail := r.changeRequest(ctx, req, false)
	if fail != nil {
		return envReq, principal, remote, fail
	}
	if remote.Role != "owner" && remote.Role != "editor" {
		return envReq, principal, remote, programmingFailure("FORBIDDEN", "programming tools require an owner or editor session")
	}
	if remote.Status != "active" {
		return envReq, principal, remote, programmingFailure("SESSION_CLOSED", "open or resume the Remote Session before using programming tools")
	}
	if !r.effectiveConfig(remote.WorkspacePath).Terminal.Enabled {
		return envReq, principal, remote, programmingFailure("DISABLED", "programming tools are disabled by workspace policy")
	}
	r.touchRemoteSessionActivity(ctx, envReq)
	return envReq, principal, remote, nil
}

func programmingWorkingDirectory(root, requested string) (string, error) {
	if requested == "" {
		requested = "."
	}
	if filepath.IsAbs(requested) {
		relative, err := filepath.Rel(root, requested)
		if err != nil {
			return "", err
		}
		requested = relative
	}
	resolved, err := workspacefile.Resolve(root, requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir must be an existing directory")
	}
	return resolved, nil
}

func (r *Runtime) processClient(ctx context.Context, remote remotesession.Session) (*unifiedexec.Client, error) {
	r.processMu.Lock()
	defer r.processMu.Unlock()
	if client := r.processClients[remote.ID]; client != nil {
		return client, nil
	}
	client, err := unifiedexec.New(ctx, unifiedexec.Options{WorkDir: remote.WorkspacePath, Env: os.Environ()})
	if err != nil {
		return nil, err
	}
	if r.processClients == nil {
		r.processClients = map[string]*unifiedexec.Client{}
	}
	r.processClients[remote.ID] = client
	return client, nil
}

func (r *Runtime) closeProcessClients() {
	r.processLifecycle.Lock()
	defer r.processLifecycle.Unlock()
	r.processMu.Lock()
	clients := r.processClients
	r.processClients = nil
	r.processMu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
}

// Approval is owned by the gateway/operator. The model cannot authorize itself
// by supplying an extra boolean; it retries the same Codex arguments after approval.
func (r *Runtime) programmingCommandApproval(ctx context.Context, envReq envelope.Request, principal auth.Principal, remote remotesession.Session, payload map[string]any, command, workdir string) *mcp.CallToolResult {
	analysis := security.AnalyzeCommand(r.effectiveConfig(remote.WorkspacePath).Security.Commands, command)
	if analysis.Decision == security.Deny {
		return programmingFailure("COMMAND_DENIED", "command denied by workspace policy")
	}
	if analysis.Decision != security.Confirm {
		return nil
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(append([]byte(remote.ID+"\x00"+workdir+"\x00"), raw...))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, pending := range r.approvals.ListRemoteSession(remote.ID) {
		if pending.Tool != "exec_command" || pending.PrincipalID != principal.ID || pending.CommandDigest != digest {
			continue
		}
		if r.control != nil {
			decision, err := r.control.Decision(ctx, pending.ID, digest)
			if err != nil {
				return programmingFailure("APPROVAL_UNAVAILABLE", err.Error())
			}
			if decision == "denied" {
				return programmingFailure("COMMAND_DENIED", "operator denied this exact command")
			}
			if decision == "approved" {
				if _, ok := r.approvals.Consume(pending.ID); !ok {
					return programmingFailure("APPROVAL_UNAVAILABLE", "approval was already consumed")
				}
				return nil
			}
		}
		if r.workspaceFullAccess(ctx, remote.WorkspaceName) {
			return nil
		}
		return programmingApprovalRequired(pending.ID)
	}
	if r.workspaceFullAccess(ctx, remote.WorkspaceName) {
		return nil
	}
	pending, err := r.approvals.PutPending(approval.Pending{Tool: "exec_command", Summary: command, Command: command, CommandDigest: digest, WorkDir: workdir, RequestID: envReq.RequestID, Workspace: remote.WorkspaceName, RemoteSessionID: remote.ID, PrincipalID: principal.ID, ContentKey: digest, Purpose: "Execute requested programming command"})
	if err != nil {
		return programmingFailure("APPROVAL_UNAVAILABLE", err.Error())
	}
	return programmingApprovalRequired(pending.ID)
}

func programmingApprovalRequired(id string) *mcp.CallToolResult {
	result := programmingFailure("APPROVAL_REQUIRED", "Approve this exact command in the MCPX operator console, then retry the same arguments. The process was not started.")
	result.StructuredContent.(map[string]any)["approval_id"] = id
	return result
}

func (r *Runtime) toolExecCommand(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, remote, fail := r.programmingRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	args := mcpresult.Arguments(req)
	command := stringPayload(args, "cmd")
	if strings.TrimSpace(command) == "" {
		return programmingFailure("INVALID_ARGUMENTS", "cmd must be non-empty"), nil
	}
	cwd, err := programmingWorkingDirectory(remote.WorkspacePath, stringPayload(args, "workdir"))
	if err != nil {
		return programmingFailure("INVALID_WORKDIR", err.Error()), nil
	}
	if fail := r.programmingCommandApproval(ctx, envReq, principal, remote, args, command, cwd); fail != nil {
		return fail, nil
	}
	if err := r.writeAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "exec_command", Command: command, Status: "preflight_approved"}); err != nil {
		return programmingFailure("AUDIT_WRITE_FAILED", err.Error()), nil
	}
	client, err := r.processClient(ctx, remote)
	if err != nil {
		return programmingFailure("EXECUTION_UNAVAILABLE", err.Error()), nil
	}
	var login *bool
	if v, ok := args["login"].(bool); ok {
		login = &v
	}
	result, err := client.Exec(ctx, unifiedexec.ExecRequest{Cmd: command, WorkDir: cwd, Shell: stringPayload(args, "shell"), Login: login, TTY: boolPayload(args, "tty"), YieldTimeMS: intPayload(args, "yield_time_ms"), MaxOutputTokens: intPayload(args, "max_output_tokens")})
	return programmingExecOutcome(result, err), nil
}

func (r *Runtime) toolWriteStdin(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_, _, remote, fail := r.programmingRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	r.processMu.Lock()
	client := r.processClients[remote.ID]
	r.processMu.Unlock()
	if client == nil {
		return programmingFailure("SESSION_NOT_FOUND", "no process session belongs to this Remote Session"), nil
	}
	args := mcpresult.Arguments(req)
	result, err := client.Write(ctx, unifiedexec.WriteRequest{SessionID: intPayload(args, "session_id"), Chars: stringPayload(args, "chars"), YieldTimeMS: intPayload(args, "yield_time_ms"), MaxOutputTokens: intPayload(args, "max_output_tokens")})
	return programmingExecOutcome(result, err), nil
}

func (r *Runtime) toolApplyPatch(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, _, remote, fail := r.programmingRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	input := stringPayload(mcpresult.Arguments(req), "input")
	effective := r.effectiveConfig(remote.WorkspacePath)
	result, err := patch.Apply(ctx, patch.Request{WorkDir: remote.WorkspacePath, WorkspaceRoot: remote.WorkspacePath, Input: input, ValidatePath: func(path string) error {
		rel, err := filepath.Rel(remote.WorkspacePath, path)
		if err != nil {
			return err
		}
		if security.MatchFile(effective.Security.Files, filepath.ToSlash(rel)) == security.Deny || security.MatchFile(effective.Security.Files, filepath.ToSlash(path)) == security.Deny {
			return fmt.Errorf("file denied by workspace policy")
		}
		return nil
	}})
	if err != nil {
		return programmingFailure("PATCH_REJECTED", err.Error()), nil
	}
	out := mcpresult.NewStructured(map[string]any{"output": result.Output, "exit_code": result.ExitCode}, result.Output)
	out.IsError = result.ExitCode != 0
	if result.ExitCode == 0 {
		mcpresult.MetaSet(out, "mcpx/patch_paths", result.Paths)
	}
	r.logAudit(audit.Event{RequestID: envReq.RequestID, RemoteSessionID: remote.ID, Workspace: remote.WorkspaceName, Tool: "apply_patch", Status: fmt.Sprintf("exit_%d", result.ExitCode), Detail: map[string]any{"paths": result.Paths}})
	return out, nil
}
