package server

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/unifiedexec"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func programmingFixture(t *testing.T) (*Runtime, string, string, func(string, map[string]any) *mcp.CallToolResult) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell HTTP fixtures; Windows native backend has separate tests")
	}
	// The public workflow must not depend on Codex being installed. Put traps
	// first on PATH and remove package-manager locations from all child paths.
	trap := t.TempDir()
	marker := filepath.Join(trap, "external-runtime-invoked")
	t.Setenv("MCPX_TEST_RUNTIME_TRAP", marker)
	t.Setenv("MCPX_CODEX_BINARY", filepath.Join(trap, "codex"))
	for _, name := range []string{"codex", "codex-exec-server", "apply_patch"} {
		body := "#!/bin/sh\nprintf invoked > \"$MCPX_TEST_RUNTIME_TRAP\"\nexit 99\n"
		if err := os.WriteFile(filepath.Join(trap, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", trap+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Cleanup(func() {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("programming workflow invoked an external Codex/apply_patch runtime")
		}
	})
	rt := newWorkspaceRuntime(t, "project")
	rt.cfg.Security.Commands.Default = "allow"
	rt.cfg.Security.Commands.Allow = nil
	rt.cfg.Security.Commands.Deny = nil
	remote := operationTestSession(t, rt, "project")
	protocol := mcp.NewServer(&mcp.Implementation{Name: "programming-test", Version: "1"}, nil)
	rt.registerTools(protocol)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client, err := mcp.NewClient(&mcp.Implementation{Name: "programming-client", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	listed, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"exec_command", "write_stdin", "apply_patch"} {
		if !names[name] {
			t.Fatalf("missing %s", name)
		}
	}
	for _, name := range []string{"read", "edit", "execute"} {
		if names[name] {
			t.Fatalf("retired programming interface still public: %s", name)
		}
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		args["remote_session_id"] = remote.ID
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s RPC: %v", name, err)
		}
		return result
	}
	return rt, remote.ID, remote.WorkspacePath, call
}

func TestProgrammingPartialFailureKeepsHandle(t *testing.T) {
	id := 42
	result := programmingExecOutcome(unifiedexec.ExecResult{SessionID: &id, Output: "partial", ChunkID: "chunk"}, errors.New("poll cancelled"))
	data := programmingWire(t, result)
	if !result.IsError || data["session_id"] != float64(42) || data["output"] != "partial" || data["error"] == nil {
		t.Fatalf("recovery state lost: %+v", data)
	}
}

func programmingWire(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("not structured: %+v", result)
	}
	if _, wrapped := data["data"]; wrapped {
		t.Fatalf("Programming result gained a second envelope: %+v", data)
	}
	return data
}

func TestProgrammingRealWorkflow(t *testing.T) {
	_, _, root, call := programmingFixture(t)
	patch := "*** Begin Patch\n*** Add File: example.txt\n+before\n*** End Patch"
	created := call("apply_patch", map[string]any{"input": patch})
	if created.IsError {
		t.Fatalf("patch: %+v", created)
	}
	run := func(cmd string) *mcp.CallToolResult {
		return call("exec_command", map[string]any{"cmd": cmd, "shell": "/bin/sh", "login": false, "yield_time_ms": 250})
	}
	first := run("cat example.txt")
	if first.IsError || !strings.Contains(programmingWire(t, first)["output"].(string), "before") {
		t.Fatal("read did not use process output")
	}
	failed := run("printf failed-check >&2; exit 7")
	data := programmingWire(t, failed)
	if failed.IsError || data["exit_code"] != float64(7) || !strings.Contains(data["output"].(string), "failed-check") {
		t.Fatalf("nonzero must stay a command outcome: %+v", data)
	}
	updated := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: example.txt\n@@\n-before\n+after\n*** End Patch"})
	if updated.IsError {
		t.Fatal(updated)
	}
	second := run("cat example.txt")
	if !strings.Contains(programmingWire(t, second)["output"].(string), "after") {
		t.Fatal("new call replayed old file output")
	}
	moved := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: example.txt\n*** Move to: moved.txt\n@@\n-after\n+done\n*** End Patch"})
	if moved.IsError {
		t.Fatal(moved)
	}
	removed := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Delete File: moved.txt\n*** End Patch"})
	if removed.IsError {
		t.Fatal(removed)
	}
	if _, err := os.Stat(filepath.Join(root, "moved.txt")); !os.IsNotExist(err) {
		t.Fatal("upstream delete did not apply")
	}
	bad := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: absent.txt\n@@\n-no\n+yes\n*** End Patch"})
	if !bad.IsError {
		t.Fatal("patch failure hidden")
	}
}

func TestProgrammingProcessContinuation(t *testing.T) {
	_, _, _, call := programmingFixture(t)
	initial := call("exec_command", map[string]any{"cmd": "sleep 0.6; printf finished", "shell": "/bin/sh", "login": false, "yield_time_ms": 250})
	data := programmingWire(t, initial)
	id, ok := data["session_id"].(float64)
	if initial.IsError || !ok {
		t.Fatalf("missing running handle: %+v", data)
	}
	var output strings.Builder
	output.WriteString(data["output"].(string))
	for i := 0; i < 8; i++ {
		next := call("write_stdin", map[string]any{"session_id": id, "chars": "", "yield_time_ms": 1000})
		data = programmingWire(t, next)
		if next.IsError {
			t.Fatal(data)
		}
		output.WriteString(data["output"].(string))
		if code, ok := data["exit_code"].(float64); ok {
			if code != 0 {
				t.Fatal(data)
			}
			break
		}
	}
	if output.String() != "finished" {
		t.Fatalf("continuation duplicate/lost output: %q", output.String())
	}
}

func TestProgrammingPolicyAndWorkspaceBoundaries(t *testing.T) {
	rt, _, root, call := programmingFixture(t)
	outside := t.TempDir()
	bad := call("exec_command", map[string]any{"cmd": "printf nope", "workdir": outside})
	if !bad.IsError {
		t.Fatal("outside workdir accepted")
	}
	target := filepath.Join(outside, "escaped.txt")
	patch := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Add File: " + target + "\n+no\n*** End Patch"})
	if !patch.IsError {
		t.Fatal("outside patch accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("outside target written")
	}
	rt.cfg.Security.Commands.Default = "deny"
	denied := call("exec_command", map[string]any{"cmd": "touch forbidden"})
	if !denied.IsError {
		t.Fatal("policy ignored")
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("denied command ran")
	}
	invalid := call("exec_command", map[string]any{"command": "old contract"})
	if !invalid.IsError {
		t.Fatal("old argument silently accepted")
	}
}

func TestProgrammingApprovalAndInteractiveSession(t *testing.T) {
	rt, remote, root, call := programmingFixture(t)
	rt.cfg.Security.Commands.Default = "confirm"
	args := map[string]any{"cmd": "printf approved > approval.txt", "shell": "/bin/sh", "login": false, "yield_time_ms": 250}
	first := call("exec_command", args)
	if !first.IsError {
		t.Fatal("unapproved effect ran")
	}
	if _, err := os.Stat(filepath.Join(root, "approval.txt")); !os.IsNotExist(err) {
		t.Fatal("confirmation already wrote file")
	}
	pending := rt.approvals.ListRemoteSession(remote)
	if len(pending) != 1 {
		t.Fatalf("approval=%+v", pending)
	}
	if err := rt.control.Decide(context.Background(), pending[0].ID, pending[0].CommandDigest, "approved"); err != nil {
		t.Fatal(err)
	}
	approved := call("exec_command", args)
	if approved.IsError {
		t.Fatalf("approved command=%+v", approved)
	}
	content, err := os.ReadFile(filepath.Join(root, "approval.txt"))
	if err != nil || string(content) != "approved" {
		t.Fatal("approved effect missing")
	}
	rt.cfg.Security.Commands.Default = "allow"
	started := call("exec_command", map[string]any{"cmd": "read value; printf 'received:%s' \"$value\"", "shell": "/bin/sh", "login": false, "tty": true, "yield_time_ms": 250})
	data := programmingWire(t, started)
	id, ok := data["session_id"].(float64)
	if !ok {
		t.Fatalf("PTY handle missing: %+v", data)
	}
	closed := call("session", map[string]any{"mode": "closed"})
	if !closed.IsError {
		t.Fatal("closed a live process")
	}
	replied := call("write_stdin", map[string]any{"session_id": id, "chars": "hello\n", "yield_time_ms": 1000})
	data = programmingWire(t, replied)
	if replied.IsError || !strings.Contains(data["output"].(string), "received:hello") {
		t.Fatalf("PTY interaction=%+v", data)
	}
}
