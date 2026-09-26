package server

import (
	"mcpx/internal/mcpresult"

	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/auth"
	"mcpx/internal/config"
)

func TestProjectTaskDiscoveryAndArtifactRemoteSessionFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":       "module example.invalid/project\n\ngo 1.24\n",
		"main.go":      "package project\n\nfunc Value() int { return 1 }\n",
		"main_test.go": "package project\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 1 { t.Fatal() } }\n",
		"report.txt":   "all checks passed\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "development-token"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Logging.Enabled = false
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx := auth.ContextWithAuthorization(context.Background(), "Bearer development-token")
	created := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"workspace": "project", "label": "development flow"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if remoteSessionID == "" {
		t.Fatalf("create=%+v", created)
	}

	listed := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"remote_session_id": remoteSessionID, "include_project_tasks": true})
	if listed["status"] != "ok" {
		t.Fatalf("project task discovery=%+v", listed)
	}

	data, _ := listed["data"].(map[string]any)
	tasks, _ := data["project_tasks"].([]any)
	foundTest := false
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		if task["name"] == "test" && task["command"] == "go test ./..." {
			foundTest = true
		}
	}
	if !foundTest {
		t.Fatalf("project test task not discovered: %+v", data)
	}

	registerRequest := mcpresult.Request(map[string]any{
		"intent":            "register the test report artifact",
		"remote_session_id": remoteSessionID, "path": "report.txt", "kind": "test_report", "name": "Go test report",
	})

	registeredResult, err := runtime.toolArtifactRegister(ctx, registerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(registeredResult.Content) < 2 {
		t.Fatalf("artifact result content=%+v", registeredResult.Content)
	}
	link, ok := registeredResult.Content[1].(*mcp.ResourceLink)
	if !ok || link.URI == "" {
		t.Fatalf("resource link=%+v", registeredResult.Content[1])
	}
	resources, err := runtime.resourceArtifact(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: link.URI}})
	if err != nil {
		t.Fatal(err)
	}
	rc := resources.Contents[0]
	if rc == nil || rc.Text != "all checks passed\n" {
		t.Fatalf("resource=%+v", resources)
	}
}

func TestSessionOpenReportsDegradedBootstrapSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Logging.Enabled = false
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	ctx := context.Background()

	// Baseline: a healthy bootstrap must not invent a degraded field.
	created := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"workspace": "project"})
	remoteSessionID, _ := created["remote_session_id"].(string)
	if remoteSessionID == "" {
		t.Fatalf("create=%+v", created)
	}
	if _, exists := created["data"].(map[string]any)["degraded"]; exists {
		t.Fatalf("healthy bootstrap must omit degraded: %+v", created["data"])
	}

	// Inject a failure source: an unparseable workspace .mcp.json breaks the
	// MCP inventory loader, which the session payload must surface.
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{"mcpServers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	degraded := callEnvelope(t, runtime.toolSessionOpen, ctx, map[string]any{"remote_session_id": remoteSessionID})
	data, _ := degraded["data"].(map[string]any)
	items, ok := data["degraded"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("degraded bootstrap must list failing sources: %+v", data)
	}
	found := false
	for _, item := range items {
		text, _ := item.(string)
		if strings.HasPrefix(text, "mcp_servers: load failed:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded must name mcp_servers as a failed source: %v", items)
	}
}
