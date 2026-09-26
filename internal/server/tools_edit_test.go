package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"mcpx/internal/observation"
)

func TestCleanCoreSessionDefaultsToOpenAndResumesSuppliedID(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"workspace": "demo"})
	if !statusOK(opened) {
		t.Fatalf("default session open failed: %+v", opened)
	}
	remoteID := opened["remote_session_id"].(string)
	resumed := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"remote_session_id": remoteID})
	if resumed["remote_session_id"] != remoteID {
		t.Fatalf("resume changed remote id: %+v", resumed)
	}
}

func TestCleanCoreObserveRejectsChangesView(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID := opened["remote_session_id"].(string)
	payload, err := json.Marshal(map[string]any{
		"diff_summary":        "--- a/a.txt\n+++ b/a.txt\n-old\n+new\n",
		"total_changed_lines": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.observation.Record(context.Background(), observation.Event{
		Workspace: "demo", RemoteSessionID: remoteID, Tool: "edit", Type: observation.TypeFileChanged,
		Status: "succeeded", Path: "a.txt", Output: payload,
	}); err != nil {
		t.Fatal(err)
	}
	response := callEnvelope(t, rt.toolObserve, context.Background(), map[string]any{
		"remote_session_id": remoteID, "view": "changes",
	})
	if statusOK(response) || errorCode(response) != "invalid_action" {
		t.Fatalf("observe changes must be rejected: %+v", response)
	}
	// Runtime may still record file_change events internally for audit/history;
	// removing observe(changes) only removes the ambiguous public view.
	events, _, err := rt.observation.store.Query(context.Background(), observation.HistoryQuery{
		Workspace: "demo", SessionID: remoteID, Kinds: []string{observation.TypeFileChanged}, Limit: 20,
	})
	if err != nil || len(events) != 1 || !strings.Contains(string(events[0].Output), "+new") {
		t.Fatalf("internal file_change observation missing: events=%+v err=%v", events, err)
	}
}

func digestForTest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
