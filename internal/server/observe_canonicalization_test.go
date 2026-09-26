package server

import (
	"encoding/json"
	"testing"

	"mcpx/internal/mcpresult"
)

func TestObserveCanonicalizesUniqueTargets(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "default session", args: map[string]any{"remote_session_id": "rs_1"}, want: "session"},
		{name: "execution task", args: map[string]any{"remote_session_id": "rs_1", "execution_task_id": "task_1"}, want: "task"},
		{name: "plan task", args: map[string]any{"remote_session_id": "rs_1", "plan_task_id": "pt_1"}, want: "plan"},
		{name: "history keyword", args: map[string]any{"remote_session_id": "rs_1", "keyword": "panic"}, want: "history"},
		{name: "explicit logs", args: map[string]any{"remote_session_id": "rs_1", "view": "logs", "execution_task_id": "task_1", "stdout_offset": 10, "stderr_offset": 20}, want: "logs"},
		{name: "explicit history pagination", args: map[string]any{"view": "history", "cursor": "next", "limit": 10}, want: "history"},
		{name: "history filter pagination", args: map[string]any{"keyword": "panic", "cursor": "next"}, want: "history"},
		{name: "unscoped pagination", args: map[string]any{"cursor": "next"}, want: ""},
		{name: "unscoped log offsets", args: map[string]any{"stdout_offset": 10}, want: ""},
		{name: "conflicting targets", args: map[string]any{"remote_session_id": "rs_1", "execution_task_id": "task_1", "keyword": "panic"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := canonicalObserveRequest(mcpresult.Request(tt.args))
			if got != tt.want {
				t.Fatalf("view=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestObservePublicSchemaMatchesCanonicalSemantics(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	tool := rt.toolIndex["observe"]
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			if raw == "view" || raw == "remote_session_id" {
				t.Fatalf("observe view/session should be inferable from request/transport, schema=%s", encoded)
			}
		}
	}
	properties := schema["properties"].(map[string]any)
	view := properties["view"].(map[string]any)
	values := map[string]bool{}
	for _, raw := range view["enum"].([]any) {
		values[raw.(string)] = true
	}
	for _, required := range []string{"session", "task", "plan", "history", "logs"} {
		if !values[required] {
			t.Fatalf("observe view enum missing %q: %+v", required, values)
		}
	}
	for _, removed := range []string{"status", "changes", "diff"} {
		if values[removed] {
			t.Fatalf("observe view enum exposes removed view %q: %+v", removed, values)
		}
	}
	for _, removedField := range []string{"edit_id", "offset", "include_diff", "path"} {
		if properties[removedField] != nil {
			t.Fatalf("observe schema exposes removed field %q: %s", removedField, encoded)
		}
	}
	if properties["stdout_offset"] == nil || properties["stderr_offset"] == nil {
		t.Fatalf("observe logs must accept server next_action offsets: %s", encoded)
	}
}
