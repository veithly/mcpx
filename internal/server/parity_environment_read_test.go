package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/environment"
)

func parityEnvironmentReadClient(t *testing.T) (*Runtime, *mcp.ClientSession, context.Context) {
	t.Helper()
	// No real toolchain executables are invoked by this isolated fixture.
	t.Setenv("PATH", t.TempDir())
	rt := newWorkspaceRuntime(t, "demo")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-environment-read", Version: "1"}, nil)
	rt.registerTools(protocol)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity-client", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return rt, cs, ctx
}

func parityEnvironmentReadCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "environment_read", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type=%T", result.StructuredContent)
	}
	return result, payload
}

func TestParityenvironment_readCurrentCompareAndRecovery(t *testing.T) {
	rt, cs, ctx := parityEnvironmentReadClient(t)
	result, payload := parityEnvironmentReadCall(t, ctx, cs, map[string]any{"workspace": "demo", "sections": []string{"runtime"}})
	data, _ := payload["data"].(map[string]any)
	if result.IsError || payload["status"] != "succeeded" || data["runtime"] == nil || data["toolchains"] != nil || data["snapshot_id"] != nil {
		t.Fatalf("current: %+v", payload)
	}
	t.Logf("current: status=%v runtime_present=%v toolchains_present=%v snapshot_present=%v", payload["status"], data["runtime"] != nil, data["toolchains"] != nil, data["snapshot_id"] != nil)
	base, err := rt.environment.Save(ctx, "", environment.Report{Runtime: &environment.RuntimeInfo{MCPXVersion: "fixture-before"}})
	if err != nil {
		t.Fatal(err)
	}
	result, payload = parityEnvironmentReadCall(t, ctx, cs, map[string]any{"workspace": "demo", "snapshot_id": base.ID, "sections": []string{"runtime"}})
	data, _ = payload["data"].(map[string]any)
	comparison, _ := data["comparison"].(map[string]any)
	if result.IsError || comparison["base_snapshot_id"] != base.ID {
		t.Fatalf("compare: %+v", payload)
	}
	t.Logf("inferred compare: status=%v base_matches=%v", payload["status"], comparison["base_snapshot_id"] == base.ID)
	for _, tc := range []struct {
		name string
		args map[string]any
		code string
	}{
		{"missing snapshot", map[string]any{"snapshot_id": "env_missing", "sections": []string{"runtime"}}, "ENVIRONMENT_SNAPSHOT_NOT_FOUND"},
		{"invalid section", map[string]any{"sections": []string{"invalid"}}, "INVALID_ARGUMENTS"},
	} {
		result, payload = parityEnvironmentReadCall(t, ctx, cs, tc.args)
		failure, _ := payload["error"].(map[string]any)
		t.Logf("%s: status=%v isError=%v error=%v", tc.name, payload["status"], result.IsError, failure["code"])
		if !result.IsError || failure["code"] != tc.code {
			t.Errorf("unexpected failure: %+v", payload)
		}
	}
	result, payload = parityEnvironmentReadCall(t, ctx, cs, map[string]any{"sections": []string{"runtime"}})
	if result.IsError || payload["status"] != "succeeded" {
		t.Fatalf("recovery: %+v", payload)
	}
	t.Logf("recovery: status=%v", payload["status"])
	t.Run("compare_requires_snapshot", func(t *testing.T) {
		result, payload := parityEnvironmentReadCall(t, ctx, cs, map[string]any{"view": "compare", "sections": []string{"runtime"}})
		data, _ := payload["data"].(map[string]any)
		t.Logf("compare without snapshot: status=%v isError=%v comparison_present=%v", payload["status"], result.IsError, data["comparison"] != nil)
		if !result.IsError {
			t.Error("compare without snapshot_id must fail rather than return current environment as success")
		}
	})
}

func TestParityenvironment_readRuntimeOnlySkipsToolchains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable fixture")
	}
	_, cs, ctx := parityEnvironmentReadClient(t)
	bin := t.TempDir()
	marker := filepath.Join(bin, "probe-ran")
	t.Setenv("PARITY_ENV_PROBE_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf probed > \"$PARITY_ENV_PROBE_MARKER\"\nprintf 'go version fixture\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	result, payload := parityEnvironmentReadCall(t, ctx, cs, map[string]any{"sections": []string{"runtime"}})
	if result.IsError {
		t.Fatalf("runtime read: %+v", payload)
	}
	_, err := os.Stat(marker)
	t.Logf("runtime-only: status=%v unrelated_go_probe_executed=%v", payload["status"], err == nil)
	if err == nil {
		t.Error("runtime-only read executed a toolchain version probe although snapshots are disabled")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
