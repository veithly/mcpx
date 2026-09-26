package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"
	"mcpx/internal/plan"
)

func programmingEvidenceRequest(t *testing.T, rt *Runtime, sessionID, tool, status string, code *int, result *mcp.CallToolResult) string {
	t.Helper()
	id, _ := mcpresult.MetaGet(result, "mcpx/request_id").(string)
	if id == "" {
		t.Fatal("programming result has no request receipt")
	}
	stored, err := rt.observation.store.GetToolResult(context.Background(), "project", sessionID, id)
	if err != nil {
		t.Fatalf("request %s was not persisted: %v", id, err)
	}
	if stored.Tool != tool || stored.Status != status || (code == nil) != (stored.ExitCode == nil) ||
		(code != nil && *stored.ExitCode != *code) {
		t.Fatalf("wrong tool outcome: tool=%s status=%s exit=%v", stored.Tool, stored.Status, stored.ExitCode)
	}
	var wire struct {
		Content map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal(stored.Result, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire.Content["output"]; !ok {
		t.Fatalf("durable result lost raw programming output: %+v", wire.Content)
	}
	return id
}

func TestProgrammingEvidenceRealPlanAndDelivery(t *testing.T) {
	rt, sessionID, _, call := programmingFixture(t)
	ctx := context.Background()
	zero, seven := 0, 7
	patch := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Add File: proof.txt\n+native programming evidence\n*** End Patch"})
	if patch.IsError {
		t.Fatal(patch)
	}
	editID := programmingEvidenceRequest(t, rt, sessionID, "apply_patch", "succeeded", &zero, patch)
	run := func(command string) *mcp.CallToolResult {
		return call("exec_command", map[string]any{"cmd": command, "shell": "/bin/sh", "login": false, "yield_time_ms": 250})
	}
	read := run("cat proof.txt")
	readID := programmingEvidenceRequest(t, rt, sessionID, "exec_command", "succeeded", &zero, read)
	if !strings.Contains(programmingWire(t, read)["output"].(string), "native programming evidence") {
		t.Fatal(read)
	}
	failed := run("printf failed-check; exit 7")
	if failed.IsError {
		t.Fatal("nonzero exit changed programming MCP semantics")
	}
	failedID := programmingEvidenceRequest(t, rt, sessionID, "exec_command", "failed", &seven, failed)
	started := run("sleep 0.7; printf verification-done")
	runningID := programmingEvidenceRequest(t, rt, sessionID, "exec_command", "accepted", nil, started)
	processID := programmingWire(t, started)["session_id"]

	planCall := func(args map[string]any) map[string]any {
		t.Helper()
		args["purpose"] = "verify persisted programming evidence"
		result := call("plan", args)
		if result.IsError {
			t.Fatalf("plan rejected: %+v", result.StructuredContent)
		}
		return result.StructuredContent.(map[string]any)["data"].(map[string]any)
	}
	created := planCall(map[string]any{"action": "create", "tasks": []any{map[string]any{"title": "change and verify"}}})
	planID := created["plan_id"].(string)
	taskID := asMapSlice(created["tasks"])[0]["plan_task_id"].(string)
	planCall(map[string]any{"action": "advance", "plan_id": planID, "plan_task_id": taskID})
	for _, test := range []struct{ name, kind, id string }{
		{"nonzero", plan.EvidenceExecute, failedID},
		{"verification-nonzero", plan.EvidenceVerification, failedID},
		{"running", plan.EvidenceExecute, runningID},
		{"wrong-tool", plan.EvidenceEdit, readID},
	} {
		rejected := call("plan", map[string]any{"action": "complete", "purpose": "reject invalid evidence", "plan_id": planID, "plan_task_id": taskID,
			"idempotency_key": "invalid-" + test.name,
			"evidence":        []plan.EvidenceInput{{Kind: test.kind, ReferenceID: test.id, Metadata: map[string]any{"passed": true, "exit_code": 0}}},
		})
		if !rejected.IsError {
			t.Fatalf("%s evidence was accepted: %+v", test.name, rejected.StructuredContent)
		}
	}
	finished := call("write_stdin", map[string]any{"session_id": processID, "chars": "", "yield_time_ms": 1000})
	finishedID := programmingEvidenceRequest(t, rt, sessionID, "write_stdin", "succeeded", &zero, finished)
	if !strings.Contains(programmingWire(t, finished)["output"].(string), "verification-done") {
		t.Fatal(finished)
	}
	planCall(map[string]any{"action": "complete", "plan_id": planID, "plan_task_id": taskID,
		"evidence": []plan.EvidenceInput{
			{Kind: plan.EvidenceRead, ReferenceID: readID},
			{Kind: plan.EvidenceEdit, ReferenceID: editID},
			{Kind: plan.EvidenceExecute, ReferenceID: finishedID},
			{Kind: plan.EvidenceVerification, ReferenceID: finishedID},
		},
	})
	delivered := planCall(map[string]any{"action": "deliver", "plan_id": planID})
	if delivered["ready"] != true {
		t.Fatalf("delivery blocked: %+v", delivered)
	}

	other := operationTestSession(t, rt, "project")
	principal, err := rt.principalFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherPlan, err := rt.plans.Create(ctx, other.ID, principal.ID, plan.CreateInput{Goal: "scope check", Tasks: []plan.TaskInput{{Title: "work"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.plans.StartTask(ctx, other.ID, otherPlan.ID, otherPlan.Tasks[0].ID, principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.plans.CompleteTask(ctx, other.ID, otherPlan.ID, otherPlan.Tasks[0].ID, principal.ID,
		[]plan.EvidenceInput{{Kind: plan.EvidenceEdit, ReferenceID: editID}}); !errors.Is(err, plan.ErrEvidence) {
		t.Fatalf("cross-session evidence accepted: %v", err)
	}
}

func TestProgrammingEvidenceRealWorkspaceReceipts(t *testing.T) {
	rt, sessionID, root, call := programmingFixture(t)
	ctx := context.Background()
	if output, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "preexisting.txt"), []byte("user work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rt.workspaceDiff.CaptureBaseline(ctx, sessionID, root); err != nil {
		t.Fatal(err)
	}
	patch := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Add File: attributed.txt\n+Codex patch\n*** End Patch"})
	if patch.IsError {
		t.Fatal(patch)
	}
	zero := 0
	programmingEvidenceRequest(t, rt, sessionID, "apply_patch", "succeeded", &zero, patch)
	unreceipted := call("exec_command", map[string]any{"cmd": "printf command-write > unknown.txt", "shell": "/bin/sh", "login": false, "yield_time_ms": 250})
	programmingEvidenceRequest(t, rt, sessionID, "exec_command", "succeeded", &zero, unreceipted)

	// Upstream applies the first file, then fails to open the deletion target.
	// A failed request cannot claim all of its preflight paths as completed.
	partial := call("apply_patch", map[string]any{"input": "*** Begin Patch\n*** Add File: partial.txt\n+partial effect\n*** Delete File: absent.txt\n*** End Patch"})
	if !partial.IsError {
		t.Fatal("expected upstream partial failure")
	}
	if mcpresult.MetaGet(partial, "mcpx/patch_paths") != nil {
		t.Fatal("failed patch published a success receipt")
	}
	if _, err := os.Stat(filepath.Join(root, "partial.txt")); err != nil {
		t.Fatalf("upstream partial effect missing: %v", err)
	}
	report, err := rt.workspaceDiff.Inspect(ctx, sessionID, "project", root, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range report.Entries {
		got[entry.Path] = entry.Attribution
	}
	for path, want := range map[string]string{"attributed.txt": "mcpx", "preexisting.txt": "preexisting", "unknown.txt": "unknown", "partial.txt": "unknown"} {
		if got[path] != want {
			t.Fatalf("%s attribution=%q, want %q; %+v", path, got[path], want, report.Entries)
		}
	}
}
