package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/config"
	"mcpx/internal/mcpresult"
)

func TestCleanCorePlanEvidenceAndArtifactWorkflow(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	path := filepath.Join(workspace.Path, "plan.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	created := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "create", "remote_session_id": remoteID, "purpose": "track the workflow",
		"summary": "complete the clean core workflow", "tasks": []any{map[string]any{"local_id": "main", "title": "apply and verify"}},
		"idempotency_key": "plan-create-1",
	})
	if !statusOK(created) {
		t.Fatalf("plan create=%+v", created)
	}
	createdData := created["data"].(map[string]any)
	if _, exists := createdData["goal"]; exists {
		t.Fatalf("clean-core plan output must not expose internal goal: %+v", createdData)
	}
	planID := createdData["plan_id"].(string)
	replay := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "create", "remote_session_id": remoteID, "purpose": "same plan, rephrased",
		"summary": "complete the clean core workflow", "tasks": []any{map[string]any{"local_id": "main", "title": "apply and verify"}},
		"idempotency_key": "plan-create-1",
	})
	if replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("plan create replay=%+v", replay)
	}
	tasks := asMapSlice(createdData["tasks"])
	taskID := tasks[0]["plan_task_id"].(string)
	advanced := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "advance", "remote_session_id": remoteID, "purpose": "start the tracked task", "plan_id": planID, "plan_task_id": taskID,
	})
	if !statusOK(advanced) {
		t.Fatalf("plan advance=%+v", advanced)
	}

	artifact := callEnvelope(t, rt.toolArtifactClean, context.Background(), map[string]any{
		"action": "register", "remote_session_id": remoteID, "purpose": "record the verification artifact", "path": "plan.txt", "kind": "other", "idempotency_key": "artifact-register-1",
	})
	if !statusOK(artifact) {
		t.Fatalf("artifact register=%+v", artifact)
	}
	artifactID := artifact["data"].(map[string]any)["artifact_id"].(string)

	completed := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "complete", "remote_session_id": remoteID, "purpose": "record verifiable completion", "plan_id": planID, "plan_task_id": taskID,
		"evidence": []any{
			map[string]any{"kind": "artifact", "reference_id": artifactID},
		},
	})
	if !statusOK(completed) || completed["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("plan complete=%+v", completed)
	}
	delivered := callEnvelope(t, rt.toolPlanClean, context.Background(), map[string]any{
		"action": "deliver", "remote_session_id": remoteID, "purpose": "deliver the completed workflow", "plan_id": planID,
	})
	deliveryData := delivered["data"].(map[string]any)
	if deliveredPlan, ok := deliveryData["plan"].(map[string]any); !ok || deliveredPlan["goal"] != nil {
		t.Fatalf("delivery plan must not expose internal goal: %+v", deliveryData["plan"])
	}
	if !statusOK(delivered) || deliveryData["ready"] != true {
		t.Fatalf("plan deliver=%+v", delivered)
	}
}

func TestCleanCoreSkillToolDoesNotRequirePublicRevisionTokens(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	rt.cfg.Discovery.Skills.Enabled = true
	rt.cfg.Discovery.Skills.Dirs = []string{".skills"}
	skillDir := filepath.Join(workspace.Path, ".skills", "docs")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: docs\ndescription: return documentation\nruntime: markdown\n---\n\n# Docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	called := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "call the documentation skill", "name": "docs", "arguments": map[string]any{},
	})
	calledData := called["data"].(map[string]any)
	if !statusOK(called) || !strings.Contains(calledData["content"].(string), "# Docs") {
		t.Fatalf("direct skill_tool call=%+v", called)
	}

	described := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "name": "docs",
	})
	if !statusOK(described) {
		t.Fatalf("skill_tool describe=%+v", described)
	}
	describedData := described["data"].(map[string]any)
	if describedData["discovery_id"] != nil || describedData["discovery_revision"] != nil {
		t.Fatalf("skill_tool describe leaked public revision tokens: %+v", describedData)
	}
	if !strings.Contains(describedData["instructions"].(string), "# Docs") {
		t.Fatalf("skill_tool describe missing instructions: %+v", describedData)
	}

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: docs\ndescription: changed documentation\nruntime: markdown\n---\n\n# Docs v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "call the changed documentation skill", "name": "docs", "arguments": map[string]any{},
	})
	if statusOK(changed) || errorCode(changed) != "skill_revision_changed" {
		t.Fatalf("changed skill revision=%+v", changed)
	}
}

func TestSkillToolListValidationRiskConfirmationAndManifestRevision(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	workspace, _ := rt.reg.Get("demo")
	rt.cfg.Discovery.Skills.Enabled = true
	rt.cfg.Discovery.Skills.Dirs = []string{".skills"}
	skillDir := filepath.Join(workspace.Path, ".skills", "publish")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `name: publish
description: publish a checked value
runtime: python
entry: run.py
permissions:
  - workspace-write
arguments_schema:
  type: object
  additionalProperties: false
  properties:
    value:
      type: string
  required: [value]
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "run.py"), []byte("print('published')\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	listed := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID, "query": "checked",
	})
	if !statusOK(listed) {
		t.Fatalf("skill list=%+v", listed)
	}
	skills := asMapSlice(listed["data"].(map[string]any)["skills"])
	if len(skills) != 1 || skills[0]["name"] != "publish" {
		t.Fatalf("skill query inventory=%+v", listed)
	}
	missing := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "name": "missing",
	})
	if statusOK(missing) || errorCode(missing) != "skill_not_found" {
		t.Fatalf("missing skill=%+v", missing)
	}
	described := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "name": "publish",
	})
	if !statusOK(described) {
		t.Fatalf("skill describe=%+v", described)
	}
	describedData := described["data"].(map[string]any)
	risk, _ := describedData["risk"].(map[string]any)
	if risk["destructive"] != false || risk["open_world"] != true || risk["confirmation_required"] != nil {
		t.Fatalf("executable skill risk must be descriptive only=%+v", risk)
	}
	invalid := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "publish checked value", "name": "publish", "arguments": map[string]any{"extra": "x"},
	})
	if statusOK(invalid) || errorCode(invalid) != "skill_argument_invalid" {
		t.Fatalf("skill invalid arguments=%+v", invalid)
	}
	request := map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "publish checked value", "name": "publish", "arguments": map[string]any{"value": "secret-not-in-recovery"},
	}
	completed := callEnvelope(t, rt.toolSkillTool, context.Background(), request)
	if !statusOK(completed) || !strings.Contains(completed["data"].(map[string]any)["stdout"].(string), "published") {
		t.Fatalf("executable skill must run without server confirmation=%+v", completed)
	}

	changedManifest := strings.Replace(manifest, "required: [value]", "required: [replacement]", 1)
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(changedManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := callEnvelope(t, rt.toolSkillTool, context.Background(), map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "publish checked value", "name": "publish", "arguments": map[string]any{"replacement": "v2"},
	})
	if statusOK(changed) || errorCode(changed) != "skill_revision_changed" {
		t.Fatalf("manifest-only skill revision change=%+v", changed)
	}
}

func TestCleanCoreMCPToolDoesNotRequirePublicRevisionTokens(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.MCP.Enabled = true
	workspace, _ := rt.reg.Get("demo")
	script := filepath.Join(t.TempDir(), "fake_mcp.py")
	serverCode := `#!/usr/bin/env python3
import json
import os
import sys

start_log = os.environ.get("MCPX_TEST_START_LOG")
if start_log:
    with open(start_log, "a", encoding="utf-8") as f:
        f.write("started\n")

def send(message):
    sys.stdout.write(json.dumps(message, separators=(',', ':')) + "\n")
    sys.stdout.flush()

tools = [{"name": "echo", "description": "echo a value", "inputSchema": {"type": "object", "properties": {"value": {"type": "string"}}, "required": ["value"]}}]
for line in sys.stdin:
    try:
        request = json.loads(line)
    except Exception:
        continue
    request_id = request.get("id")
    if request_id is None:
        continue
    method = request.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": request_id, "result": {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "fake", "version": "1"}}})
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": request_id, "result": {"tools": tools}})
    elif method == "tools/call":
        value = request.get("params", {}).get("arguments", {}).get("value", "")
        send({"jsonrpc": "2.0", "id": request_id, "result": {"content": [{"type": "text", "text": "echo:" + value}], "isError": False}})
    else:
        send({"jsonrpc": "2.0", "id": request_id, "error": {"code": -32601, "message": "method not found"}})
`
	if err := os.WriteFile(script, []byte(serverCode), 0o700); err != nil {
		t.Fatal(err)
	}
	startLog := filepath.Join(t.TempDir(), "fake-mcp-starts.log")
	if err := config.WriteMCPFile(config.ProjectMCPPath(workspace.Path), config.MCPFile{MCPServers: map[string]config.MCPServer{
		"fake": testMCPServer(t, "Echo values for contract tests", script, map[string]string{"MCPX_TEST_START_LOG": startLog}),
	}}); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	described := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "server": "fake", "tool": "echo",
	})
	if !statusOK(described) {
		t.Fatalf("mcp_tool describe=%+v", described)
	}
	describedData := described["data"].(map[string]any)
	if describedData["discovery_id"] != nil || describedData["discovery_revision"] != nil || describedData["input_schema"] == nil {
		t.Fatalf("mcp_tool describe contract=%+v", describedData)
	}
	startsBeforeCall := fakeMCPStartCount(t, startLog)
	callRequest := map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "call the fake MCP", "server": "fake", "tool": "echo", "arguments": map[string]any{"value": "one"},
	}
	called, err := rt.toolMCPTool(context.Background(), mcpresult.Request(callRequest))
	if err != nil {
		t.Fatal(err)
	}
	if called == nil || called.IsError || mcpresult.FirstText(called) != "echo:one" {
		t.Fatalf("unannotated mcp_tool must execute without server confirmation=%+v", called)
	}
	if startsAfterCall := fakeMCPStartCount(t, startLog); startsAfterCall != startsBeforeCall+1 {
		t.Fatalf("schema check and call must share one upstream instance: before=%d after=%d", startsBeforeCall, startsAfterCall)
	}
}

func TestMCPToolContractCoversInventoryErrorsSchemaAndUpstreamFailure(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	rt.cfg.Discovery.MCP.Enabled = true
	workspace, _ := rt.reg.Get("demo")
	schemaPath := filepath.Join(t.TempDir(), "echo-schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "contract_mcp.py")
	serverCode := `#!/usr/bin/env python3
import json
import os
import sys

def send(message):
    sys.stdout.write(json.dumps(message, separators=(',', ':')) + "\n")
    sys.stdout.flush()

def current_tools():
    with open(os.environ["MCPX_TEST_SCHEMA"], "r", encoding="utf-8") as f:
        schema = json.load(f)
    closed_read = {"readOnlyHint": True, "destructiveHint": False, "openWorldHint": False}
    return [
        {"name": "echo", "description": "echo schema-bound value", "inputSchema": schema, "annotations": closed_read},
        {"name": "fail", "description": "return a structured upstream failure", "inputSchema": {"type":"object"}, "annotations": closed_read},
    ]

for line in sys.stdin:
    try:
        request = json.loads(line)
    except Exception:
        continue
    request_id = request.get("id")
    if request_id is None:
        continue
    method = request.get("method")
    if method == "initialize":
        send({"jsonrpc":"2.0", "id":request_id, "result":{"protocolVersion":"2025-11-25", "capabilities":{"tools":{}}, "serverInfo":{"name":"contract", "version":"1"}}})
    elif method == "tools/list":
        send({"jsonrpc":"2.0", "id":request_id, "result":{"tools":current_tools()}})
    elif method == "tools/call":
        if request.get("params", {}).get("name") == "fail":
            send({"jsonrpc":"2.0", "id":request_id, "result":{"content":[{"type":"text", "text":"upstream rejected request"}], "isError":True}})
        else:
            value = request.get("params", {}).get("arguments", {}).get("value", "")
            send({"jsonrpc":"2.0", "id":request_id, "result":{"content":[{"type":"text", "text":"echo:" + value}], "isError":False}})
    else:
        send({"jsonrpc":"2.0", "id":request_id, "error":{"code":-32601, "message":"method not found"}})
`
	if err := os.WriteFile(script, []byte(serverCode), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteMCPFile(config.ProjectMCPPath(workspace.Path), config.MCPFile{MCPServers: map[string]config.MCPServer{
		"contract": testMCPServer(t, "Contract-test MCP server", script, map[string]string{"MCPX_TEST_SCHEMA": schemaPath}),
		"offline":  {Description: "Unavailable server", Command: "mcpx-command-that-does-not-exist"},
	}}); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)

	servers := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID, "query": "contract",
	})
	if !statusOK(servers) {
		t.Fatalf("mcp server list=%+v", servers)
	}
	serverItems := asMapSlice(servers["data"].(map[string]any)["servers"])
	if len(serverItems) != 1 || serverItems[0]["name"] != "contract" || serverItems[0]["description"] != "Contract-test MCP server" {
		t.Fatalf("compact MCP inventory must support relevance routing: %+v", servers)
	}
	tools := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID, "server": "contract",
	})
	if !statusOK(tools) || len(asMapSlice(tools["data"].(map[string]any)["tools"])) != 2 {
		t.Fatalf("mcp tool list=%+v", tools)
	}
	unknownServer := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID, "server": "missing",
	})
	if statusOK(unknownServer) || errorCode(unknownServer) != "mcp_server_not_found" {
		t.Fatalf("unknown MCP server=%+v", unknownServer)
	}
	unknownTool := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "server": "contract", "tool": "missing",
	})
	if statusOK(unknownTool) || errorCode(unknownTool) != "mcp_tool_not_found" {
		t.Fatalf("unknown MCP tool=%+v", unknownTool)
	}
	unavailable := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "list", "remote_session_id": remoteID, "server": "offline",
	})
	if statusOK(unavailable) || errorCode(unavailable) != "mcp_server_unavailable" {
		t.Fatalf("unavailable MCP server=%+v", unavailable)
	}

	described := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "server": "contract", "tool": "echo",
	})
	if !statusOK(described) {
		t.Fatalf("echo describe=%+v", described)
	}
	describedRisk, _ := described["data"].(map[string]any)["risk"].(map[string]any)
	if describedRisk["read_only"] != true || describedRisk["open_world"] != false || describedRisk["confirmation_required"] != nil {
		t.Fatalf("upstream annotations must remain descriptive without server confirmation policy: %+v", describedRisk)
	}
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","properties":{"replacement":{"type":"string"}},"required":["replacement"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schemaChanged := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "call schema-changed echo", "server": "contract", "tool": "echo", "arguments": map[string]any{"replacement": "v2"},
	})
	if statusOK(schemaChanged) || errorCode(schemaChanged) != "mcp_tool_schema_changed" {
		t.Fatalf("MCP schema change=%+v", schemaChanged)
	}

	failDescription := callEnvelope(t, rt.toolMCPTool, context.Background(), map[string]any{
		"action": "describe", "remote_session_id": remoteID, "server": "contract", "tool": "fail",
	})
	if !statusOK(failDescription) {
		t.Fatalf("fail describe=%+v", failDescription)
	}
	failed, err := rt.toolMCPTool(context.Background(), mcpresult.Request(map[string]any{
		"action": "call", "remote_session_id": remoteID, "purpose": "observe an upstream failure", "intent": "test operation", "server": "contract", "tool": "fail", "arguments": map[string]any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if failed == nil || !failed.IsError || mcpresult.FirstText(failed) != "upstream rejected request" {
		t.Fatalf("upstream MCP business failure must pass through=%+v", failed)
	}
}

func fakeMCPStartCount(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	return strings.Count(normalized, "started\n")
}

func mcpConfirmationKey(t *testing.T, response map[string]any) string {
	t.Helper()
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("confirmation response missing data: %+v", response)
	}
	key, _ := data["confirmation_key"].(string)
	if strings.TrimSpace(key) == "" {
		t.Fatalf("confirmation response missing confirmation_key: %+v", response)
	}
	return key
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
