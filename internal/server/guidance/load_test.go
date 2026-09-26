package guidance

import (
	"reflect"
	"testing"
)

func TestLoadAgent(t *testing.T) {
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != "3.0" || len(cfg.Rules) < 8 || len(cfg.Rules) > 16 {
		t.Fatalf("unexpected compact guidance: %+v", cfg)
	}
	for route, tools := range map[string][]string{
		"inspect_source":      {"exec_command"},
		"run_commands":        {"exec_command"},
		"continue_process":    {"write_stdin"},
		"modify_files":        {"apply_patch"},
		"inspect_changes":     {"exec_command"},
		"inspect_environment": {"environment_read"},
		"observe_gateway":     {"observe"},
	} {
		if !reflect.DeepEqual(cfg.ToolRouting[route], tools) {
			t.Fatalf("routing %s = %v, want %v", route, cfg.ToolRouting[route], tools)
		}
	}
	for route, tools := range cfg.ToolRouting {
		for _, tool := range tools {
			if tool == "read" || tool == "edit" || tool == "execute" {
				t.Fatalf("route %s exposes removed programming tool %s", route, tool)
			}
		}
	}
}
