package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCommonSuggestedActionsFitPublicSchemas(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	cases := []struct {
		name   string
		action map[string]any
	}{
		{name: "workspace recovery", action: nextAction("workspace", map[string]any{})},
		{name: "session recovery", action: nextAction("session", map[string]any{"workspace": "demo"})},
		{name: "source search", action: nextAction("exec_command", map[string]any{"remote_session_id": "rs_1", "cmd": "rg -n needle ."})},
		{name: "source window", action: nextAction("exec_command", map[string]any{"cmd": "sed -n '100,200p' a.go"})},
		{name: "process continuation", action: nextAction("write_stdin", map[string]any{"remote_session_id": "rs_1", "session_id": 42, "yield_time_ms": 10000})},
		{name: "task logs", action: nextAction("observe", map[string]any{"remote_session_id": "rs_1", "view": "logs", "execution_task_id": "task_1", "stdout_offset": 10, "stderr_offset": 20})},
		{name: "environment snapshot", action: nextAction("environment", map[string]any{"remote_session_id": "rs_1"})},
		{name: "environment compare", action: nextAction("environment_read", map[string]any{"workspace": "demo", "view": "compare", "snapshot_id": "env_1"})},
		{name: "management task status", action: nextAction("observe", map[string]any{"remote_session_id": "rs_1", "view": "task", "execution_task_id": "task_1"})},
		{name: "artifact continuation", action: nextAction("artifact", map[string]any{"remote_session_id": "rs_1", "action": "read", "artifact_id": "art_1", "offset": 1024, "limit": 1024})},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assertSuggestedActionFitsPublicSchema(t, rt, tt.action)
		})
	}

}

func assertSuggestedActionFitsPublicSchema(t *testing.T, rt *Runtime, action map[string]any) {
	t.Helper()
	toolName, _ := action["tool"].(string)
	arguments, _ := action["arguments"].(map[string]any)
	tool, ok := rt.toolIndex[toolName]
	if !ok {
		t.Fatalf("suggested unknown tool %q: %+v", toolName, action)
	}
	var schema map[string]any
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for key := range arguments {
		if properties[key] == nil {
			t.Fatalf("suggested %s argument %q is not in public schema: args=%+v schema=%s", toolName, key, arguments, encoded)
		}
	}
	required := requiredFields(schema)
	for _, key := range required {
		if value, exists := arguments[key]; !exists || value == nil || fmt.Sprint(value) == "" {
			t.Fatalf("suggested %s missing required %q: args=%+v", toolName, key, arguments)
		}
	}
	if actionName, _ := arguments["action"].(string); actionName != "" {
		if actionProp, ok := properties["action"].(map[string]any); ok {
			actionDesc, _ := actionProp["description"].(string)
			if !strings.Contains(actionDesc, actionName) {
				t.Fatalf("suggested %s action %q is not described in action.description: %s", toolName, actionName, actionDesc)
			}
		}
	}
}

func requiredFields(schema map[string]any) []string {
	raw, _ := schema["required"].([]any)
	result := make([]string, 0, len(raw))
	for _, value := range raw {
		if key, ok := value.(string); ok {
			result = append(result, key)
		}
	}
	return result
}
