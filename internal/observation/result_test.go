package observation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/state"
)

func TestToolResultSurvivesStateStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "mcpx.db")
	first, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(first.DB())
	exitCode := 7
	want := PersistedToolResult{
		RequestID: "req_restart", Workspace: "mcpx", RemoteSessionID: "sess_restart",
		CallID: "call_restart", OperationID: "op_restart", StepID: "step_restart",
		Tool: "execute", Status: "failed", Result: json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"command failed"}]}`),
		Summary: "command failed", ExecutionTaskID: "task_restart", ExitCode: &exitCode,
	}
	if err := store.SaveToolResult(context.Background(), want); err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := NewStore(second.DB()).GetToolResult(context.Background(), "mcpx", "sess_restart", "req_restart")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != want.Tool || got.Status != want.Status || string(got.Result) != string(want.Result) || got.ExecutionTaskID != want.ExecutionTaskID || got.ExitCode == nil || *got.ExitCode != exitCode {
		t.Fatalf("persisted result=%+v", got)
	}
}

func TestToolResultScopeRejectsOtherSession(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	if err := store.SaveToolResult(context.Background(), PersistedToolResult{
		RequestID: "req_scope", Workspace: "mcpx", RemoteSessionID: "sess_owner", Tool: "read", Status: "succeeded", Result: json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetToolResult(context.Background(), "mcpx", "sess_other", "req_scope"); err == nil {
		t.Fatal("result from another session was exposed")
	}
}

func TestSaveToolResultTruncatesOversizedResultInsteadOfRejecting(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	payload, err := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", MaxToolResultBytes)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= MaxToolResultBytes {
		t.Fatalf("fixture payload=%d bytes, want > %d", len(payload), MaxToolResultBytes)
	}
	err = store.SaveToolResult(context.Background(), PersistedToolResult{
		RequestID: "req_oversize", Workspace: "mcpx", Tool: "execute", Status: "succeeded",
		Result: json.RawMessage(payload), Summary: "big result",
	})
	if err != nil {
		t.Fatalf("oversized result must persist truncated, not be rejected: %v", err)
	}
	got, err := store.GetToolResult(context.Background(), "mcpx", "", "req_oversize")
	if err != nil {
		t.Fatalf("recovery row missing for oversized result: %v", err)
	}
	if !got.Truncated {
		t.Fatal("truncated flag was not persisted")
	}
	if len(got.Result) > MaxToolResultBytes {
		t.Fatalf("stored result len=%d, want <= %d", len(got.Result), MaxToolResultBytes)
	}
	if !json.Valid(got.Result) {
		t.Fatal("stored truncated result is not valid JSON")
	}
}

func TestSaveToolResultKeepsSmallResultsExactAndUntruncated(t *testing.T) {
	db := openObservationTestDB(t)
	store := NewStore(db.DB())
	if err := store.SaveToolResult(context.Background(), PersistedToolResult{
		RequestID: "req_small", Workspace: "mcpx", Tool: "read", Status: "succeeded",
		Result: json.RawMessage(`{"content":[{"type":"text","text":"exact"}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetToolResult(context.Background(), "mcpx", "", "req_small")
	if err != nil {
		t.Fatal(err)
	}
	if got.Truncated {
		t.Fatal("small result must not be flagged truncated")
	}
	if string(got.Result) != `{"content":[{"type":"text","text":"exact"}]}` {
		t.Fatalf("small result was rewritten: %s", got.Result)
	}
}
