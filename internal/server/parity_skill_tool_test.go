package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func paritySkillToolFixture(t *testing.T, manifestFile, body string) (*Runtime, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	rt := newWorkspaceRuntime(t, "demo")
	ws, _ := rt.reg.Get("demo")
	rt.cfg.Discovery.Skills.Enabled = true
	rt.cfg.Discovery.Skills.Dirs = []string{".skills"}
	dir := filepath.Join(ws.Path, ".skills", "parity-docs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, manifestFile)
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	return rt, opened["remote_session_id"].(string), file
}

func TestParityskill_toolListDescribeCallAndRecovery(t *testing.T) {
	body := "---\nname: parity-docs\ndescription: fictitious documentation\nruntime: markdown\n---\n# Parity v1\n"
	rt, id, file := paritySkillToolFixture(t, "SKILL.md", body)
	invoke := func(action, name string) map[string]any {
		return callEnvelope(t, rt.toolHandlers["skill_tool"], context.Background(), map[string]any{"action": action, "name": name, "remote_session_id": id, "purpose": "read fictitious documentation"})
	}
	listed := invoke("list", "")
	if !statusOK(listed) {
		t.Fatalf("list=%+v", listed)
	}
	items := asMapSlice(listed["data"].(map[string]any)["skills"])
	if len(items) != 1 || items[0]["name"] != "parity-docs" {
		t.Fatalf("list=%+v", listed)
	}
	described := invoke("describe", "parity-docs")
	if !statusOK(described) || !strings.Contains(described["data"].(map[string]any)["instructions"].(string), "# Parity v1") {
		t.Fatalf("describe=%+v", described)
	}
	called := invoke("call", "parity-docs")
	if !statusOK(called) || !strings.Contains(called["data"].(map[string]any)["content"].(string), "# Parity v1") {
		t.Fatalf("call=%+v", called)
	}
	t.Log("list=1 skill; describe.instructions=# Parity v1; call.content=# Parity v1")
	missing := invoke("describe", "missing")
	if errorCode(missing) != "skill_not_found" {
		t.Fatalf("missing=%+v", missing)
	}
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(body, "v1", "v2")), 0600); err != nil {
		t.Fatal(err)
	}
	changed := invoke("call", "parity-docs")
	if errorCode(changed) != "skill_revision_changed" {
		t.Fatalf("changed=%+v", changed)
	}
	refreshed := invoke("describe", "parity-docs")
	if !statusOK(refreshed) {
		t.Fatalf("refresh=%+v", refreshed)
	}
	recovered := invoke("call", "parity-docs")
	if !statusOK(recovered) || !strings.Contains(recovered["data"].(map[string]any)["content"].(string), "# Parity v2") {
		t.Fatalf("recovered=%+v", recovered)
	}
	t.Logf("missing=%s; changed=%s; describe+call recovered=# Parity v2", errorCode(missing), errorCode(changed))
}

func TestParityskill_toolDescribeMissingEntryMustFail(t *testing.T) {
	rt, id, _ := paritySkillToolFixture(t, "skill.yaml", "name: parity-docs\ndescription: fictitious broken doc\nruntime: markdown\nentry: absent.md\n")
	describe := callEnvelope(t, rt.toolHandlers["skill_tool"], context.Background(), map[string]any{"action": "describe", "name": "parity-docs", "remote_session_id": id})
	call := callEnvelope(t, rt.toolHandlers["skill_tool"], context.Background(), map[string]any{"action": "call", "name": "parity-docs", "remote_session_id": id, "purpose": "read fictitious broken documentation"})
	t.Logf("describe.status=%v instructions=%v; call.status=%v error=%s", describe["status"], describe["error"], call["status"], errorCode(call))
	if statusOK(call) || errorCode(call) != "skill_error" {
		t.Fatalf("call=%+v", call)
	}
	if statusOK(describe) {
		t.Fatal("describe reported success despite unreadable markdown entry; expected an actionable read error")
	}
}
