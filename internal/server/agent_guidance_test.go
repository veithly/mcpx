package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

func TestAgentGuidanceIsCompactPrincipledContract(t *testing.T) {
	guidance := agentGuidance()
	if guidance["version"] != agentGuidanceVersion || guidance["priority"] != "high" {
		t.Fatalf("guidance metadata=%+v", guidance)
	}
	if guidance["response_contract"] != nil || guidance["edit_payload"] != nil {
		t.Fatalf("protocol cheat-sheets must live in schemas/runtime, not guidance: %+v", guidance)
	}
	routingIface := guidance["tool_routing"]
	var routing map[string]any
	switch typed := routingIface.(type) {
	case map[string]any:
		routing = typed
	case map[string][]string:
		routing = make(map[string]any, len(typed))
		for key, value := range typed {
			routing[key] = value
		}
	default:
		t.Fatalf("tool routing type=%T", routingIface)
	}
	if !containsAnyString(routing["inspect_source"], "exec_command") || !containsAnyString(routing["inspect_environment"], "environment_read") || !containsAnyString(routing["modify_files"], "apply_patch") || !containsAnyString(routing["inspect_changes"], "exec_command") {
		t.Fatalf("canonical routing=%+v", routing)
	}
	rules, ok := guidance["rules"].([]string)
	if !ok || len(rules) < 8 || len(rules) > 16 {
		t.Fatalf("guidance should stay compact: %T %+v", guidance["rules"], guidance["rules"])
	}
	joined := strings.Join(rules, "\n")
	for _, required := range []string{"structuredContent", "不要猜测", "exec_command", "write_stdin", "apply_patch", "session_id", "remote_session_id", "output", "exit_code", "console", "git diff", "最小充分证据", "最终回复前"} {
		if !strings.Contains(joined, required) {
			t.Errorf("compact guidance missing principle %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"progress_summary", "reasoning_summary", "STALE_REVISION", "必须携带 activity.intent", "edit 参数速查", "用 read", "用 execute"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("guidance still contains protocol bookkeeping %q: %s", forbidden, joined)
		}
	}
	instructions := agentGuidanceInstructions()
	if strings.Contains(instructions, "用户可见响应契约") || strings.Contains(instructions, "edit 参数速查") {
		t.Fatalf("rendered instructions still duplicate tool protocol: %s", instructions)
	}
	if len(instructions) > 6000 {
		t.Fatalf("rendered guidance unexpectedly large: %d bytes", len(instructions))
	}
	for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
		if strings.Contains(strings.ToLower(mustJSON(t, guidance)), `"`+forbidden+`"`) {
			t.Fatalf("guidance contains host presentation argument %q", forbidden)
		}
	}
	revision := agentGuidanceRevision()
	if revision == "" || revision != agentGuidanceRevision() {
		t.Fatalf("guidance revision is not stable: %q", revision)
	}
}

func TestEveryPublicToolHasModelFacingDescriptionAndActionBranches(t *testing.T) {
	runtime := &Runtime{}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	runtime.registerTools(protocol)
	for name, registered := range runtime.listedToolMap() {
		if strings.TrimSpace(registered.Description) == "" {
			t.Fatalf("tool %s has no description", name)
		}
		if len(mcpresult.ToolSchemaJSON(registered)) == 0 {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(mcpresult.ToolSchemaJSON(registered), &schema); err != nil {
			t.Fatalf("tool %s schema: %v", name, err)
		}
		if _, hasOneOf := schema["oneOf"]; hasOneOf {
			t.Fatalf("tool %s must not rely on top-level oneOf: %s", name, mcpresult.ToolSchemaJSON(registered))
		}
		if _, hasAnyOf := schema["anyOf"]; hasAnyOf {
			t.Fatalf("tool %s must not rely on top-level anyOf: %s", name, mcpresult.ToolSchemaJSON(registered))
		}
		if _, hasAllOf := schema["allOf"]; hasAllOf {
			t.Fatalf("tool %s must not rely on top-level allOf: %s", name, mcpresult.ToolSchemaJSON(registered))
		}
		properties, _ := schema["properties"].(map[string]any)
		if actionProp, ok := properties["action"].(map[string]any); ok {
			if _, hasEnum := actionProp["enum"].([]any); hasEnum {
				desc, _ := actionProp["description"].(string)
				if strings.TrimSpace(desc) == "" {
					t.Fatalf("tool %s action has enum but missing action.description", name)
				}
			}
		}
	}
}

func TestRecoveryActionIsStructuredInErrorDetails(t *testing.T) {
	response := envelope.Fail(envelope.StatusError, "req_test", "demo", nil, "NOT_FOUND", "missing")
	addRecoveryAction(&response, "exec_command", "locate the missing file", map[string]any{"cmd": "rg --files"})
	if response.Error == nil {
		t.Fatal("missing error body")
	}
	next, ok := response.Error.Details["next_action"].(map[string]any)
	if !ok || next["tool"] != "exec_command" || next["reason"] != "locate the missing file" {
		t.Fatalf("next action=%+v", response.Error.Details["next_action"])
	}
	if response.Error.Recovery == nil || response.Error.Recovery.Tool != "exec_command" || response.Error.Recovery.Arguments["cmd"] != "rg --files" {
		t.Fatalf("structured recovery=%+v", response.Error.Recovery)
	}
}

func TestPublicActionPreservesProcessAndRemoteSessionIDs(t *testing.T) {
	tool, args := normalizePublicAction("write_stdin", map[string]any{
		"session_id": 42, "remote_session_id": "rs_1", "chars": "hello\n",
	})
	if tool != "write_stdin" || args["session_id"] != 42 || args["remote_session_id"] != "rs_1" || args["chars"] != "hello\n" {
		t.Fatalf("public action changed process or routing context: %s %+v", tool, args)
	}
}

func containsAnyString(value any, wanted string) bool {
	switch items := value.(type) {
	case []string:
		for _, item := range items {
			if item == wanted {
				return true
			}
		}
	case []any:
		for _, item := range items {
			if item, ok := item.(string); ok && item == wanted {
				return true
			}
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
