package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/config"
)

type authRoundTripper struct {
	base http.RoundTripper
	auth string
}

func (t authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", t.auth)
	return t.base.RoundTrip(clone)
}

func roundTripperWithAuth(base http.RoundTripper, authHeader string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return authRoundTripper{base: base, auth: authHeader}
}

// normalizeAcceptanceResult maps machine result shapes onto the acceptance
// harness envelope used by older assertions (ok/status/data).
func normalizeAcceptanceResult(payload map[string]any, res *mcp.CallToolResult, text string) (map[string]any, bool) {
	if payload == nil {
		return nil, false
	}

	// Full ARC envelope in _meta (or accidentally inlined).
	if mcpx, ok := payload["mcpx"].(map[string]any); ok {
		if _, hasLegacyOK := payload["ok"]; hasLegacyOK {
			return nil, false
		}
		result, _ := mcpx["result"].(map[string]any)
		return acceptanceNormalized(result, payload, res, text), true
	}

	// Model structuredContent after WrapToolResult: {status, type, data, error?}
	if payload["type"] != nil && payload["data"] != nil {
		return acceptanceNormalized(payload, nil, res, text), true
	}

	// Handler public wire: {status, data, error?}
	if status, ok := payload["status"].(string); ok && status != "" {
		if _, hasData := payload["data"]; hasData {
			return acceptanceNormalized(payload, nil, res, text), true
		}
		if _, hasError := payload["error"]; hasError {
			return acceptanceNormalized(payload, nil, res, text), true
		}
	}
	return nil, false
}

func acceptanceNormalized(result map[string]any, arcEnvelope map[string]any, res *mcp.CallToolResult, text string) map[string]any {
	publicStatus, _ := result["status"].(string)
	if publicStatus == "" {
		publicStatus = "succeeded"
	}
	status := publicStatus
	if status == "succeeded" {
		status = "ok"
	} else if status == "waiting_confirmation" {
		status = "need_confirmation"
	}
	okValue := publicStatus == "succeeded"
	hints, _ := result["hints"].(map[string]any)
	if result["type"] == "error" {
		okValue = false
	}
	if hints["preferred_behavior"] == "ask_confirm" && status == "succeeded" {
		status, okValue = "waiting_confirmation", false
	}
	data := result["data"]
	// Wire envelope already has data nested; model SC also uses data.
	// When result is the ARC result body, data is business payload.
	normalized := map[string]any{
		"ok":            okValue,
		"status":        status,
		"public_status": publicStatus,
		"data":          data,
		"_result":       res,
		"_text":         text,
	}
	if arcEnvelope == nil {
		if value := resultARCValue(res); value != nil {
			raw, err := json.Marshal(value)
			if err == nil {
				var decoded map[string]any
				if json.Unmarshal(raw, &decoded) == nil {
					arcEnvelope = decoded
				}
			}
		}
	}
	if arcEnvelope != nil {
		normalized["_arc"] = arcEnvelope
	}
	if actions, ok := result["actions"]; ok && actions != nil {
		normalized["actions"] = actions
	}
	if errBody, ok := result["error"]; ok && errBody != nil {
		normalized["error"] = errBody
	} else if resultData, ok := data.(map[string]any); ok {
		if errData, exists := resultData["error"]; exists {
			normalized["error"] = errData
		}
	}
	return normalized
}

// TestManagementAcceptanceViaMCPProtocol covers the authenticated public catalog,
// session bootstrap, plans, instructions and resume over real Streamable HTTP.
// Programming effects are covered by codex_programming_test.go.
func TestManagementAcceptanceViaMCPProtocol(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MCPX_HOME", home)
	workspace := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(workspace, "frontend", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"demo.go":                     "package demo\n\nconst Value = 1\n",
		"a.go":                        "package demo\n\nfunc Alpha() int { return 1 }\n",
		"b.go":                        "package demo\n\nfunc Beta() int { return 2 }\n",
		"delete_me.txt":               "remove through confirmation\n",
		"AGENTS.md":                   "# Project: chinese comments\n",
		"frontend/AGENTS.md":          "# frontend: use pnpm\n",
		"frontend/src/AGENTS.md":      "# src: no generated\n",
		"go.mod":                      "module demo\n\ngo 1.22\n",
		"frontend/src/views/Home.vue": "<template><div/></template>\n",
	}
	for rel, content := range files {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	globalAgents := filepath.Join(home, "GLOBAL_AGENTS.md")
	if err := os.WriteFile(globalAgents, []byte("# Global: run all tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	token := "acceptance-token"
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = token
	cfg.Logging.Enabled = false
	cfg.Security.Commands.Allow = append(cfg.Security.Commands.Allow, `^printf\b`, `^sleep\b`, `^go test\b`)
	cfg.Workspaces = []config.WorkspaceEntry{{Name: "project", Path: workspace}}
	cfg.Discovery.Instructions.GlobalAgentsPath = globalAgents
	// Keep this protocol fixture independent of the developer machine's global
	// ~/.agents/skills and ~/.codex/skills directories.
	cfg.Discovery.Skills.Dirs = []string{filepath.Join(home, "skills")}
	if err := config.WriteGlobal(filepath.Join(home, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Options{Version: "0.9.0-test", Commit: "deadbeef", Date: "2026-07-31"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx", Version: "0.9.0-test"}, &mcp.ServerOptions{
		Instructions: agentGuidanceInstructions(),
	})
	runtime.registerTools(protocol)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return protocol
	}, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Stateless: true})
	gw := NewGateway(cfg, nil, streamable)
	ts := httptest.NewServer(gw.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpClient := &http.Client{Transport: roundTripperWithAuth(http.DefaultTransport, "Bearer "+token)}
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// --- A01: tools/list schema ---
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	byName := map[string]mcp.Tool{}
	for _, tool := range listed.Tools {
		if tool != nil {
			byName[tool.Name] = *tool
		}
	}
	expectedTools := []string{
		"workspace", "session", "exec_command", "write_stdin", "apply_patch", "move_out", "observe", "progress",
		"operation_batch", "operation_manage",
		"plan", "artifact", "skill_tool", "mcp_tool", "browser",
		"runtime_read", "environment_read", "environment", "screenshot_capture", "secret_provide",
	}
	if len(byName) != len(expectedTools) {
		t.Fatalf("tools/list count=%d, want %d: %v", len(byName), len(expectedTools), byName)
	}
	for _, required := range expectedTools {
		if _, ok := byName[required]; !ok {
			t.Fatalf("tools/list missing %s", required)
		}
	}
	for name, tool := range byName {
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var listedTool map[string]any
		if err := json.Unmarshal(encoded, &listedTool); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		rawOutputSchema, hasOutputSchema := listedTool["outputSchema"]
		if name == "mcp_tool" || isProgrammingTool(name) {
			if hasOutputSchema && rawOutputSchema != nil {
				t.Fatalf("%s must omit outputSchema: %+v", name, rawOutputSchema)
			}
		} else {
			outputSchema, ok := rawOutputSchema.(map[string]any)
			if !ok {
				t.Fatalf("%s must expose an OutputSchema: %+v", name, rawOutputSchema)
			}
			if outputSchema["$id"] != "urn:mcpx:structured-content:v2.0" {
				t.Fatalf("%s must expose the ARC structuredContent OutputSchema: %+v", name, rawOutputSchema)
			}

		}
		inputSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema %s: %v", name, err)
		}
		if strings.Contains(string(inputSchema), `"started_at_ms"`) {
			t.Fatalf("%s must not expose started_at_ms: %s", name, inputSchema)
		}
		if strings.Contains(string(inputSchema), `"goal"`) {
			t.Fatalf("%s must not expose deprecated goal: %s", name, inputSchema)
		}
		for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
			if strings.Contains(string(inputSchema), `"`+forbidden+`"`) {
				t.Fatalf("%s exposes host presentation argument %q: %s", name, forbidden, inputSchema)
			}
		}
	}
	moveSchema, _ := json.Marshal(byName["move_out"].InputSchema)
	var moveSchemaMap map[string]any
	if err := json.Unmarshal(moveSchema, &moveSchemaMap); err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"prepare", "submit", "targets", "expected_sha256", "symlink", "confirmation_uuid"} {
		if !strings.Contains(string(moveSchema), needle) {
			t.Fatalf("move_out schema missing %q: %s", needle, moveSchema)
		}
	}
	if _, hasOneOf := moveSchemaMap["oneOf"]; hasOneOf {
		t.Fatalf("move_out must not rely on oneOf: %s", moveSchema)
	}
	moveProperties, _ := moveSchemaMap["properties"].(map[string]any)
	for _, field := range []string{"action", "purpose", "targets", "confirmation_uuid", "remote_session_id", "idempotency_key"} {
		if moveProperties[field] == nil {
			t.Fatalf("move_out schema missing property %q: %s", field, moveSchema)
		}
	}
	if moveProperties["workspace"] != nil || strings.Contains(string(moveSchema), `"kind"`) {
		t.Fatalf("move_out must let Runtime infer workspace/kind: %s", moveSchema)
	}
	actionProp, _ := moveProperties["action"].(map[string]any)
	actionDesc, _ := actionProp["description"].(string)
	for _, needle := range []string{"prepare", "submit", "purpose", "targets", "confirmation_uuid"} {
		if !strings.Contains(actionDesc, needle) {
			t.Fatalf("move_out action.description missing %q: %s", needle, actionDesc)
		}
	}
	moveRequired, _ := moveSchemaMap["required"].([]any)
	if !reflect.DeepEqual(moveRequired, []any{"action"}) {
		t.Fatalf("move_out required must be exactly [action], got %v", moveRequired)
	}
	for _, forbidden := range []string{"workspace", "move_request_id", "manifest_sha256"} {
		if moveProperties[forbidden] != nil {
			t.Fatalf("move_out must bind %q server-side: %s", forbidden, moveSchema)
		}
	}
	for _, extensionName := range []string{"skill_tool", "mcp_tool"} {
		extensionSchema, _ := json.Marshal(byName[extensionName].InputSchema)
		text := string(extensionSchema)
		for _, action := range []string{"list", "describe", "call"} {
			if !strings.Contains(text, `"`+action+`"`) {
				t.Fatalf("%s schema missing action %q: %s", extensionName, action, extensionSchema)
			}
		}
		for _, legacy := range []string{"discovery_id", "discovery_revision"} {
			if strings.Contains(text, legacy) {
				t.Fatalf("%s schema exposes legacy field %q: %s", extensionName, legacy, extensionSchema)
			}
		}
	}
	planSchema, _ := json.Marshal(byName["plan"].InputSchema)
	for _, forbidden := range []string{"presentation", "renderer", "show_source", "density"} {
		if strings.Contains(string(planSchema), forbidden) {
			t.Fatalf("plan_manage exposes host presentation field %q: %s", forbidden, planSchema)
		}
	}

	// Catalog names must match tools/list (A01).
	declared := capabilityToolNames()
	if len(declared) != len(listed.Tools) {
		t.Fatalf("catalog count %d != tools/list %d", len(declared), len(listed.Tools))
	}

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return decodeToolResult(t, res)
	}

	// --- A03: session_open single call ---
	opened := call("session", map[string]any{
		"action":                       "open",
		"workspace":                    "project",
		"label":                        "acceptance",
		"include_instructions_content": true,
		"include_project_tasks":        true,
	})
	if opened["status"] != "ok" && opened["ok"] != true {
		t.Fatalf("session_open: %+v", opened)
	}
	openData, _ := opened["data"].(map[string]any)
	if openData == nil {
		t.Fatalf("session_open data missing: %+v", opened)
	}
	guidance, _ := openData["agent_guidance"].(map[string]any)
	routing, _ := guidance["tool_routing"].(map[string]any)
	if guidance["version"] != agentGuidanceVersion || !containsAnyString(routing["modify_files"], "apply_patch") || guidance["response_contract"] != nil || guidance["edit_payload"] != nil {
		t.Fatalf("session_open compact guidance missing or incomplete: %+v", guidance)
	}
	remoteSession, _ := openData["remote_session"].(map[string]any)
	remoteID, _ := remoteSession["id"].(string)
	if remoteID == "" {
		t.Fatalf("session_open missing remote_session.id: %+v", openData)
	}
	if openData["remote_session_id"] != remoteID {
		t.Fatalf("session_open missing top-level remote_session_id: %+v", openData)
	}
	revs, _ := openData["revisions"].(map[string]any)
	for _, key := range []string{
		"tool_schema_revision", "capability_manifest_revision", "guidance_revision",
		"instruction_revision", "session_capability_revision", "client_protocol_revision",
	} {
		if revs[key] == nil || revs[key] == "" {
			t.Fatalf("missing revision %s: %+v", key, revs)
		}
	}
	for _, removed := range []string{"skill_revision", "mcp_revision"} {
		if revs[removed] != nil {
			t.Fatalf("session_open must not expose extension revision %s: %+v", removed, revs)
		}
	}
	extensions, _ := openData["extension_inventory"].(map[string]any)
	if extensions == nil || extensions["skills"] == nil || extensions["mcp_servers"] == nil || openData["skills"] != nil || openData["upstream_mcp"] != nil {
		t.Fatalf("session_open extension inventory contract: %+v", openData)
	}
	mcpxMeta, _ := openData["mcpx"].(map[string]any)
	if mcpxMeta["version"] != "0.9.0-test" {
		t.Fatalf("mcpx version: %+v", mcpxMeta)
	}
	instr, _ := openData["instructions"].(map[string]any)
	docs, _ := instr["documents"].([]any)
	if len(docs) < 2 {
		t.Fatalf("expected global+project instructions, got %+v", instr)
	}
	// Inline content present for at least one doc.
	hasContent := false
	for _, raw := range docs {
		doc, _ := raw.(map[string]any)
		if c, ok := doc["content"].(string); ok && c != "" {
			hasContent = true
		}
	}
	if !hasContent {
		t.Fatalf("session_open should inline AGENTS content: %+v", instr)
	}
	schemaRev1 := fmt.Sprint(revs["tool_schema_revision"])
	sessionRev1 := fmt.Sprint(revs["session_capability_revision"])
	if openData["client_refresh"] != nil || openData["omitted_sections"] != nil {
		t.Fatalf("session bootstrap must not require client revision bookkeeping: %+v", openData)
	}

	// --- P0 plan: create, advance, and deliver ---
	statusOK := func(resp map[string]any) bool {
		return resp["status"] == "ok" || resp["status"] == "succeeded" || resp["ok"] == true
	}
	planCreated := call("plan", map[string]any{
		"action": "create", "remote_session_id": remoteID, "summary": "acceptance plan", "purpose": "acceptance plan create",
		"tasks": []any{map[string]any{"local_id": "verify", "title": "Verify protocol"}},
	})
	planData, _ := planCreated["data"].(map[string]any)
	if !statusOK(planCreated) || planData["plan_id"] == nil {
		t.Fatalf("plan create = %+v", planCreated)
	}
	planID, _ := planData["plan_id"].(string)
	planTasks, _ := planData["tasks"].([]any)
	taskID := ""
	if len(planTasks) > 0 {
		taskID, _ = planTasks[0].(map[string]any)["plan_task_id"].(string)
	}
	if !strings.HasPrefix(taskID, "pt_") {
		t.Fatalf("plan_create must issue server task id: %+v", planData)
	}
	started := call("plan", map[string]any{"action": "advance", "remote_session_id": remoteID, "plan_id": planID, "plan_task_id": taskID, "purpose": "start task"})
	if !statusOK(started) || started["data"].(map[string]any)["plan_task_id"] != taskID {
		t.Fatalf("plan start = %+v", started)
	}
	completed := call("plan", map[string]any{
		"action": "complete", "remote_session_id": remoteID, "plan_id": planID, "plan_task_id": taskID, "purpose": "complete task",
		"evidence": []any{map[string]any{"kind": "source", "reference_id": "demo.go"}},
	})
	if !statusOK(completed) || completed["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("plan complete = %+v", completed)
	}
	delivered := call("plan", map[string]any{"action": "deliver", "remote_session_id": remoteID, "plan_id": planID, "purpose": "deliver plan"})
	if !statusOK(delivered) {
		t.Fatalf("plan deliver = %+v", delivered)
	}
	if ready, _ := delivered["data"].(map[string]any)["ready"].(bool); !ready {
		// delivery payload may live under nested fields depending on presentation path
		if delivered["data"].(map[string]any)["status"] != "delivered" && delivered["data"].(map[string]any)["status"] != "ready" {
			t.Fatalf("plan deliver data = %+v", delivered["data"])
		}
	}

	// --- A02: runtime_read capabilities revisions; role-independent tool_schema_revision ---
	caps := call("runtime_read", map[string]any{"view": "capabilities", "remote_session_id": remoteID})
	capData, _ := caps["data"].(map[string]any)
	capRevs, _ := capData["revisions"].(map[string]any)
	if fmt.Sprint(capRevs["tool_schema_revision"]) != schemaRev1 {
		t.Fatalf("tool_schema_revision drifted between session_open and capability_list: %v vs %v", schemaRev1, capRevs["tool_schema_revision"])
	}
	if fmt.Sprint(capRevs["session_capability_revision"]) != sessionRev1 {
		// Same session/role — should match.
		t.Fatalf("session_capability_revision mismatch: %v vs %v", sessionRev1, capRevs["session_capability_revision"])
	}
	resumed := call("session", map[string]any{"action": "open", "remote_session_id": remoteID})
	resumedData, _ := resumed["data"].(map[string]any)
	if resumedData["client_refresh"] != nil || resumedData["omitted_sections"] != nil {
		t.Fatalf("session re-open must not add legacy revision bookkeeping: %+v", resumedData)
	}
	if resumedData["revisions"] == nil || resumedData["remote_session"] == nil || resumedData["workspace"] == nil {
		t.Fatalf("session re-open must preserve dynamic identity and revision facts: %+v", resumedData)
	}
	for _, field := range []string{"agent_guidance", "client_protocol", "tools", "instructions", "schema_source", "capability_version", "capability_groups", "recommended_workflows"} {
		if resumedData[field] != nil {
			t.Fatalf("session re-open must not repeat static bootstrap field %s: %+v", field, resumedData[field])
		}
	}

	// --- A04 nested AGENTS ---
	listedInstr := call("runtime_read", map[string]any{
		"view":              "instructions",
		"remote_session_id": remoteID,
		"anchor_path":       "frontend/src/views/Home.vue",
	})
	listedData, _ := listedInstr["data"].(map[string]any)
	chain, _ := listedData["instructions"].([]any)
	if len(chain) < 4 {
		t.Fatalf("nested AGENTS chain too short: %+v", listedData)
	}
	scopes := []string{}
	for _, raw := range chain {
		doc, _ := raw.(map[string]any)
		scopes = append(scopes, fmt.Sprint(doc["scope"]))
	}
	joined := strings.Join(scopes, ",")
	if !strings.Contains(joined, "global") || !strings.Contains(joined, "project") || !strings.Contains(joined, "directory") {
		t.Fatalf("expected global/project/directory scopes, got %v", scopes)
	}

}
