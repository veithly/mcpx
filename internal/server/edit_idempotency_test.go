package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpx/internal/edit"
	"mcpx/internal/envelope"
	"mcpx/internal/idempotency"
	"mcpx/internal/remotesession"
)

func editTestSession(t *testing.T, rt *Runtime) (remotesession.Session, string) {
	t.Helper()
	opened := callEnvelope(t, rt.toolSession, context.Background(), map[string]any{"action": "open", "workspace": "demo"})
	remoteID, _ := opened["remote_session_id"].(string)
	if remoteID == "" {
		t.Fatalf("session open failed: %+v", opened)
	}
	workspace, _ := rt.reg.Get("demo")
	return remotesession.Session{ID: remoteID, WorkspaceName: "demo", WorkspacePath: workspace.Path}, remoteID
}

func seedEditRecord(t *testing.T, rt *Runtime, session remotesession.Session, principalID, keyValue, fingerprint string, stored storedEditResult, metadata []byte) {
	t.Helper()
	key := idempotency.Key{RemoteSessionID: session.ID, PrincipalID: principalID, Operation: cleanEditIdempotencyOperation, Value: keyValue}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.idempotency.Claim(context.Background(), key, fingerprint, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := rt.idempotency.UpdatePending(context.Background(), key, fingerprint, encoded, metadata); err != nil {
		t.Fatal(err)
	}
	if err := rt.idempotency.MarkInDoubt(context.Background(), key, fingerprint, metadata); err != nil {
		t.Fatal(err)
	}
}

func TestEditReplayFollowsRecordState(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	session, _ := editTestSession(t, rt)
	envReq := envelope.Request{RequestID: "req_replay_state"}

	planned := edit.BatchResult{
		Results:           []edit.FileResult{{Path: "x.txt", Operation: edit.OpCreate, NewSHA256: "sha256:abc", ChangedLines: 1}},
		TotalChangedLines: 1,
	}
	success := storedEditResult{EditID: "edit-1", Result: planned}
	successJSON, err := json.Marshal(success)
	if err != nil {
		t.Fatal(err)
	}
	failed := storedEditResult{EditID: "edit-1", Error: &storedEditError{Code: "STALE_REVISION", Message: "base_sha256 does not match current file", Index: -1}}
	failedJSON, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		record idempotency.Record
		expect func(t *testing.T, response map[string]any)
	}{
		{
			name:   "succeeded replays success",
			record: idempotency.Record{State: idempotency.StateSucceeded, Response: successJSON},
			expect: func(t *testing.T, response map[string]any) {
				if !statusOK(response) {
					t.Fatalf("succeeded replay failed: %+v", response)
				}
				if response["data"].(map[string]any)["idempotent_replay"] != true {
					t.Fatalf("missing replay marker: %+v", response)
				}
			},
		},
		{
			name: "in_doubt never replays success",
			record: idempotency.Record{
				State: idempotency.StateInDoubt, Response: successJSON,
				Metadata: []byte(`{"original_error":"write boom"}`),
			},
			expect: func(t *testing.T, response map[string]any) {
				if statusOK(response) || errorCode(response) != "idempotency_in_doubt" {
					t.Fatalf("in-doubt record must not replay success: %+v", response)
				}
				details := response["error"].(map[string]any)["details"].(map[string]any)
				if details["original_error"] != "write boom" {
					t.Fatalf("in-doubt details lost the original error: %+v", details)
				}
			},
		},
		{
			name:   "pending reports in progress",
			record: idempotency.Record{State: idempotency.StatePending, Response: successJSON},
			expect: func(t *testing.T, response map[string]any) {
				if statusOK(response) || errorCode(response) != "idempotency_in_progress" {
					t.Fatalf("pending record must report in progress: %+v", response)
				}
			},
		},
		{
			name:   "failed replays the stored error",
			record: idempotency.Record{State: idempotency.StateFailed, Response: failedJSON},
			expect: func(t *testing.T, response map[string]any) {
				if statusOK(response) || errorCode(response) != "stale_revision" {
					t.Fatalf("failed record must replay the stored error: %+v", response)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := rt.replayStoredEdit(envReq, session, tc.record)
			if err != nil {
				t.Fatal(err)
			}
			tc.expect(t, decodeToolResult(t, result))
		})
	}
}

func TestEditInDoubtOriginalStateRerunsSameKey(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	session, remoteID := editTestSession(t, rt)
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session.WorkspacePath, "rerun.txt")
	original := []byte("line: one\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	request := map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "rerun an in-doubt batch whose original state is intact",
		"idempotency_key":   "rerun-key-1",
		"edits": []map[string]any{{
			"path": "rerun.txt", "operation": "update", "rev": compactFileRevision(digestForTest(original)),
			"replacements": []map[string]any{{"match": "line: one", "replacement": "line: two"}},
		}},
	}
	edits, err := parseCleanEdits(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := cleanEditFingerprint(envelope.Request{}, edits)

	// Seed an in-doubt record whose planned batch never touched the disk:
	// the workspace still matches the original state, so the same key is
	// safe to re-execute in place.
	plannedEdits := append([]edit.FileEdit(nil), edits...)
	if err := resolveEditRevisions(session.WorkspacePath, plannedEdits); err != nil {
		t.Fatal(err)
	}
	planned, err := edit.ApplyBatch(edit.BatchRequest{WorkspaceRoot: session.WorkspacePath, Edits: plannedEdits, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	seedEditRecord(t, rt, session, principal.ID, "rerun-key-1", fingerprint,
		storedEditResult{EditID: "edit-seed", Result: planned}, nil)

	first := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if !statusOK(first) {
		t.Fatalf("original-state in_doubt must rerun with the same key: %+v", first)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "line: two\n" {
		t.Fatalf("rerun must apply the batch: %q err=%v", content, err)
	}

	replay := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if !statusOK(replay) || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("finished rerun must replay success: %+v", replay)
	}
}

func TestEditInDoubtPartialWriteStaysInDoubt(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	session, remoteID := editTestSession(t, rt)
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	request := map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "verify a partial write stays in doubt",
		"idempotency_key":   "partial-key-1",
		"edits": []map[string]any{
			{"path": "p1.txt", "operation": "create", "content": "alpha\n"},
			{"path": "p2.txt", "operation": "create", "content": "beta\n"},
		},
	}
	edits, err := parseCleanEdits(request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := cleanEditFingerprint(envelope.Request{}, edits)

	// Seed an in-doubt record for a batch that failed after p1.txt landed.
	planned, err := edit.ApplyBatch(edit.BatchRequest{WorkspaceRoot: session.WorkspacePath, Edits: edits, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"original_error":"write p2.txt: injected","failed_path":"p2.txt","failed_index":1,"applied_paths":["p1.txt"],"pending_paths":["p2.txt"]}`)
	seedEditRecord(t, rt, session, principal.ID, "partial-key-1", fingerprint,
		storedEditResult{EditID: "edit-partial", Result: planned}, metadata)
	if err := os.WriteFile(filepath.Join(session.WorkspacePath, "p1.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if statusOK(response) || errorCode(response) != "idempotency_in_doubt" {
		t.Fatalf("partial write must never replay as success: %+v", response)
	}
	details := response["error"].(map[string]any)["details"].(map[string]any)
	if details["original_error"] != "write p2.txt: injected" || details["failed_path"] != "p2.txt" {
		t.Fatalf("in-doubt response lost the failure boundary: %+v", details)
	}
	if _, err := os.Stat(filepath.Join(session.WorkspacePath, "p2.txt")); err == nil {
		t.Fatal("pending file must remain untouched")
	}
}

func TestEditHeartbeatKeepsPendingLeaseFresh(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ctx := context.Background()
	newKey := func(value string) idempotency.Key {
		return idempotency.Key{RemoteSessionID: "session-hb", PrincipalID: "principal-hb", Operation: cleanEditIdempotencyOperation, Value: value}
	}
	backdate := func(key idempotency.Key) {
		t.Helper()
		stale := time.Now().Add(-35 * time.Second).UTC().UnixMilli()
		if _, err := rt.state.DB().ExecContext(ctx, `UPDATE clean_idempotency_records SET updated_at = ?
			WHERE remote_session_id = ? AND principal_id = ? AND operation = ? AND idempotency_key = ?`,
			stale, key.RemoteSessionID, key.PrincipalID, key.Operation, key.Value); err != nil {
			t.Fatal(err)
		}
	}

	key := newKey("hb-1")
	if _, err := rt.idempotency.Claim(ctx, key, "sha256:fp", time.Hour); err != nil {
		t.Fatal(err)
	}
	backdate(key)

	previous := editHeartbeatInterval
	editHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { editHeartbeatInterval = previous })
	stop := make(chan struct{})
	rt.startEditHeartbeat(ctx, key, "sha256:fp", stop)
	time.Sleep(80 * time.Millisecond)

	// A foreign claimant (fresh store, no local flight) sees a live lease and
	// must wait instead of taking the record over.
	foreign := idempotency.NewStore(rt.state.DB())
	if claim, err := foreign.Claim(ctx, key, "sha256:fp", time.Hour); err != nil || claim.Kind != idempotency.ClaimPending {
		t.Fatalf("heartbeated record must not be taken over: %+v err=%v", claim, err)
	}
	close(stop)

	// Without a heartbeat the expired lease is takeable.
	key2 := newKey("hb-2")
	if _, err := rt.idempotency.Claim(ctx, key2, "sha256:fp", time.Hour); err != nil {
		t.Fatal(err)
	}
	backdate(key2)
	time.Sleep(5 * time.Millisecond)
	takeover, err := foreign.Claim(ctx, key2, "sha256:fp", time.Hour)
	if err != nil || takeover.Kind != idempotency.ClaimOwner {
		t.Fatalf("expired lease must be takeable without heartbeat: %+v err=%v", takeover, err)
	}
}

func TestEditTransientHookErrorDoesNotPoisonKey(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	session, remoteID := editTestSession(t, rt)
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session.WorkspacePath, "retry.txt")
	original := []byte("v1\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	request := map[string]any{
		"remote_session_id": remoteID,
		"purpose":           "retry after a transient hook failure",
		"idempotency_key":   "retry-key-1",
		"edits": []map[string]any{{
			"path": "retry.txt", "operation": "update", "rev": compactFileRevision(digestForTest(original)),
			"replacements": []map[string]any{{"match": "v1", "replacement": "v2"}},
		}},
	}
	// Break the pre-write hook's edit-record table to simulate a transient
	// storage failure after the idempotency key was claimed.
	if _, err := rt.state.DB().Exec(`DROP TABLE clean_edit_records`); err != nil {
		t.Fatal(err)
	}
	first := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if statusOK(first) || errorCode(first) != "edit_failed" {
		t.Fatalf("transient hook failure must surface the original error: %+v", first)
	}
	// The pending record must be rolled back so the same key can retry.
	key := idempotency.Key{RemoteSessionID: remoteID, PrincipalID: principal.ID, Operation: cleanEditIdempotencyOperation, Value: "retry-key-1"}
	if _, ok, err := rt.idempotency.Lookup(context.Background(), key); err != nil || ok {
		t.Fatalf("rolled-back key must be free again: ok=%v err=%v", ok, err)
	}

	if _, err := rt.state.DB().Exec(`CREATE TABLE IF NOT EXISTS clean_edit_records (
		id TEXT PRIMARY KEY,
		remote_session_id TEXT NOT NULL,
		principal_id TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('pending','succeeded','in_doubt')),
		result_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	second := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if !statusOK(second) {
		t.Fatalf("same-key retry after rollback must succeed: %+v", second)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "v2\n" {
		t.Fatalf("retry must apply the batch: %q err=%v", content, err)
	}
	replay := callEnvelope(t, rt.toolEdit, context.Background(), request)
	if !statusOK(replay) || replay["data"].(map[string]any)["idempotent_replay"] != true {
		t.Fatalf("finished retry must replay success: %+v", replay)
	}
}
