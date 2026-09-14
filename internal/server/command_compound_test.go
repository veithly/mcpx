package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/audit"
	"mcpx/internal/config"
	"mcpx/internal/mcpresult"
	"mcpx/internal/remotesession"
)

func openCompoundCommandSession(t *testing.T, rt *Runtime) (string, string) {
	t.Helper()
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("demo workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Session.ID, registered.Path
}

func runCompoundCommand(t *testing.T, rt *Runtime, remoteID, command string) map[string]any {
	t.Helper()
	request := mcpresult.Request(map[string]any{
		"intent":            "verify compound command preflight",
		"remote_session_id": remoteID,
		"command":           command,
		"purpose":           "verify compound command preflight",
		"scope":             "workspace",
	})
	result, err := rt.toolCommandExecute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return decodeToolResult(t, result)
}

func TestCompoundCommandsPreflightEverySegmentBeforeShellStart(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands = config.CommandRules{
		Allow:   []string{`^mkdir\b`, `^echo\b`, `^cat\b`, `^mcpx_missing_command_for_or$`},
		Deny:    []string{`^rm\b`},
		Default: "deny",
	}
	auditLogger, err := audit.New(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	rt.audit = auditLogger
	remoteID, workspace := openCompoundCommandSession(t, rt)

	for _, testCase := range []struct {
		name    string
		command string
		path    string
	}{
		{name: "and", command: "mkdir denied-and && rm -rf blocked", path: "denied-and"},
		{name: "or", command: "mkdir denied-or || rm -rf blocked", path: "denied-or"},
		{name: "newline", command: "mkdir denied-newline\nrm -rf blocked", path: "denied-newline"},
		{name: "heredoc", command: "mkdir denied-heredoc <<'TEXT'\nliteral input\nTEXT\nrm -rf blocked", path: "denied-heredoc"},
	} {
		t.Run("deny_"+testCase.name, func(t *testing.T) {
			response := runCompoundCommand(t, rt, remoteID, testCase.command)
			if response["status"] != "failed" {
				t.Fatalf("denied compound command was accepted: %+v", response)
			}
			errorBody, _ := response["error"].(map[string]any)
			if code, _ := errorBody["code"].(string); !strings.EqualFold(code, "denied") {
				t.Fatalf("unexpected deny error: %+v", response)
			}
			if _, err := os.Stat(filepath.Join(workspace, testCase.path)); !os.IsNotExist(err) {
				t.Fatalf("first segment executed before later deny, stat err=%v", err)
			}
		})
	}

	andResponse := runCompoundCommand(t, rt, remoteID, "echo left && echo right")
	if andResponse["status"] != "ok" {
		t.Fatalf("allowed && command failed: %+v", andResponse)
	}
	andData, _ := andResponse["data"].(map[string]any)
	stdout, _ := andData["stdout"].(string)
	if !strings.Contains(stdout, "left") || !strings.Contains(stdout, "right") {
		t.Fatalf("&& shell semantics were not preserved: %+v", andData)
	}
	policy, _ := andData["command_policy"].(map[string]any)
	segments, _ := policy["segments"].([]any)
	if policy["decision"] != "allow" || policy["all_segments_preflighted"] != true || policy["atomic_policy_gate"] != true || len(segments) != 2 {
		t.Fatalf("missing compound policy result: %+v", policy)
	}
	first, _ := segments[0].(map[string]any)
	if first["operator_after"] != "&&" || first["decision"] != "allow" {
		t.Fatalf("unexpected first segment audit: %+v", first)
	}

	for _, testCase := range []struct {
		name    string
		command string
		output  string
	}{
		{name: "newline", command: "echo first\necho second", output: "first\nsecond\n"},
		{name: "heredoc", command: "cat << 'TEXT'\nliteral input\nTEXT\necho after", output: "literal input\nafter\n"},
	} {
		t.Run("allow_"+testCase.name, func(t *testing.T) {
			response := runCompoundCommand(t, rt, remoteID, testCase.command)
			if response["status"] != "ok" {
				t.Fatalf("allowed command failed: %+v", response)
			}
			data, _ := response["data"].(map[string]any)
			if data["stdout"] != testCase.output {
				t.Fatalf("shell semantics were not preserved: %+v", data)
			}
			policy, _ := data["command_policy"].(map[string]any)
			segments, _ := policy["segments"].([]any)
			if policy["all_segments_preflighted"] != true || policy["atomic_policy_gate"] != true || len(segments) != 2 {
				t.Fatalf("missing multiline policy result: %+v", policy)
			}
			first, _ := segments[0].(map[string]any)
			if first["operator_after"] != "\n" || first["decision"] != "allow" {
				t.Fatalf("unexpected newline segment audit: %+v", first)
			}
		})
	}

	orResponse := runCompoundCommand(t, rt, remoteID, "mcpx_missing_command_for_or || echo fallback")
	if orResponse["status"] != "ok" {
		t.Fatalf("allowed || command failed: %+v", orResponse)
	}
	orData, _ := orResponse["data"].(map[string]any)
	if stdout, _ := orData["stdout"].(string); !strings.Contains(stdout, "fallback") {
		t.Fatalf("|| fallback did not execute: %+v", orData)
	}

	auditBytes, err := os.ReadFile(auditLogger.Path())
	if err != nil {
		t.Fatal(err)
	}
	sawPreflight, sawAnd, sawOr, sawAtomicGate := false, false, false, false
	for _, line := range strings.Split(strings.TrimSpace(string(auditBytes)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode audit line: %v: %s", err, line)
		}
		if event["status"] != "preflight_approved" {
			continue
		}
		sawPreflight = true
		detail, _ := event["detail"].(map[string]any)
		policy, _ := detail["command_policy"].(map[string]any)
		if policy["atomic_policy_gate"] == true {
			sawAtomicGate = true
		}
		segments, _ := policy["segments"].([]any)
		for _, raw := range segments {
			segment, _ := raw.(map[string]any)
			switch segment["operator_after"] {
			case "&&":
				sawAnd = true
			case "||":
				sawOr = true
			}
		}
	}
	if !sawPreflight || !sawAnd || !sawOr || !sawAtomicGate {
		t.Fatalf("preflight audit incomplete: preflight=%v and=%v or=%v atomic=%v\n%s", sawPreflight, sawAnd, sawOr, sawAtomicGate, auditBytes)
	}
}

func TestCompoundCommandAuditFailureRejectsBeforeShellStart(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Security.Commands = config.CommandRules{Allow: []string{`^mkdir\b`}, Default: "deny"}
	remoteID, workspace := openCompoundCommandSession(t, rt)

	auditLogger, err := audit.New(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(auditLogger.Path())); err != nil {
		t.Fatal(err)
	}
	rt.audit = auditLogger

	response := runCompoundCommand(t, rt, remoteID, "mkdir audit-must-not-run")
	if response["status"] != "failed" {
		t.Fatalf("audit failure must reject execution: %+v", response)
	}
	errorBody, _ := response["error"].(map[string]any)
	if code, _ := errorBody["code"].(string); !strings.EqualFold(code, "audit_write_failed") {
		t.Fatalf("unexpected audit failure error: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(workspace, "audit-must-not-run")); !os.IsNotExist(err) {
		t.Fatalf("command executed despite preflight audit failure: %v", err)
	}
}
