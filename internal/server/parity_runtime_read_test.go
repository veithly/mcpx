package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParityruntime_readNormal(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.Instructions.GlobalAgentsPath = ""
	ws, _ := rt.reg.Get("demo")
	if err := os.MkdirAll(filepath.Join(ws.Path, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", "child/AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(ws.Path, path), []byte("Temporary fixture instruction\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []map[string]any{
		{},
		{"view": "project", "workspace": "demo"},
		{"workspace": "demo", "anchor_path": "child"},
	} {
		result := callRawToolResult(t, rt.toolHandlers["runtime_read"], args)
		if result.IsError {
			t.Fatalf("args=%v: %s", args, firstToolText(result))
		}
		data, _ := decodeToolResult(t, result)["data"].(map[string]any)
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if args["anchor_path"] != nil && !strings.Contains(string(encoded), "dir:child") {
			t.Fatalf("missing nested rule: %s", encoded)
		}
		if len(args) == 0 && data["runtime"] == nil {
			t.Fatalf("missing capabilities: %s", encoded)
		}
		t.Logf("args=%v isError=%v", args, result.IsError)
	}
}

func TestParityruntime_readRejectOutsideInstructions(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.Instructions.GlobalAgentsPath = ""
	ws, _ := rt.reg.Get("demo")
	outside := filepath.Join(filepath.Dir(ws.Path), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "AGENTS.md"), []byte("OUTSIDE_FIXTURE_RULE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Path, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		args     map[string]any
		leakedID string
	}{
		{"parent_anchor", map[string]any{"workspace": "demo", "anchor_path": "../outside"}, "dir:../outside"},
		{"symlink_paths", map[string]any{"workspace": "demo", "paths": []any{"linked"}}, "dir:linked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := callRawToolResult(t, rt.toolHandlers["runtime_read"], tt.args)
			data := decodeToolResult(t, result)["data"]
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("isError=%v data=%s", result.IsError, encoded)
			if !result.IsError && strings.Contains(string(encoded), tt.leakedID) {
				t.Errorf("outside instruction exposed as active: %s", tt.leakedID)
			}
		})
	}
}
