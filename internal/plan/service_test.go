package plan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/state"
)

func TestServiceCreateGetAndRestartLoadsTasksEvidenceAndEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mcpx.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	remoteSessionID := seedPlanSession(t, store.DB())
	service := NewService(store.DB())
	created, err := service.Create(ctx, remoteSessionID, "principal-test", CreateInput{
		Goal: "ship feature", Summary: "initial plan", Tasks: []TaskInput{
			{ID: "design", Title: "Design"},
			{ID: "implement", Title: "Implement", DependsOn: []string{"design"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != PlanReady || created.Progress.Total != 2 || created.Progress.Todo != 2 || len(created.Events) != 1 {
		t.Fatalf("created plan = %+v", created)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := NewService(store.DB()).Get(ctx, remoteSessionID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal != created.Goal || len(loaded.Tasks) != 2 || loaded.Tasks[1].DependsOn[0] != loaded.Tasks[0].ID {
		t.Fatalf("reloaded plan = %+v", loaded)
	}
}

func TestServiceCreateIssuesServerTaskIDsAndMapsDependencies(t *testing.T) {
	ctx := context.Background()
	store := openPlanStore(t)
	defer store.Close()
	remoteSessionID := seedPlanSession(t, store.DB())
	service := NewService(store.DB())
	input := CreateInput{Goal: "goal", Tasks: []TaskInput{
		{ID: "audit-users", Title: "Backend"},
		{ID: "frontend", Title: "Frontend", DependsOn: []string{"audit-users"}},
		{Title: "Verify", DependsOn: []string{"audit-users"}},
	}}
	first, err := service.Create(ctx, remoteSessionID, "principal-test", input)
	if err != nil {
		t.Fatal(err)
	}
	// The same local labels must be reusable by a later plan without hitting
	// the global plan_tasks primary key.
	second, err := service.Create(ctx, remoteSessionID, "principal-test", input)
	if err != nil {
		t.Fatalf("reuse of local task ids must not conflict: %v", err)
	}
	for _, plan := range []Plan{first, second} {
		if len(plan.Tasks) != 3 {
			t.Fatalf("plan tasks=%d", len(plan.Tasks))
		}
		for index, task := range plan.Tasks {
			if !strings.HasPrefix(task.ID, "pt_") {
				t.Fatalf("task id must be server-issued: %q", task.ID)
			}
			if index > 0 {
				other := plan.Tasks[0].ID
				if task.DependsOn[0] != other {
					t.Fatalf("dependency must map to final task id: %+v", task)
				}
			}
		}
	}
	ids := make(map[string]bool)
	for _, plan := range []Plan{first, second} {
		for _, task := range plan.Tasks {
			if ids[task.ID] {
				t.Fatalf("task id must be unique across plans: %s", task.ID)
			}
			ids[task.ID] = true
		}
	}
	if _, err := service.StartTask(ctx, remoteSessionID, first.ID, first.Tasks[0].ID, "principal-test"); err != nil {
		t.Fatalf("start with server-issued task id: %v", err)
	}
}

func TestServiceEnforcesDependenciesAndCompletionEvidence(t *testing.T) {
	ctx := context.Background()
	store := openPlanStore(t)
	defer store.Close()
	remoteSessionID := seedPlanSession(t, store.DB())
	service := NewService(store.DB())
	created, err := service.Create(ctx, remoteSessionID, "principal-test", CreateInput{Goal: "goal", Tasks: []TaskInput{
		{ID: "first", Title: "First"}, {ID: "second", Title: "Second", DependsOn: []string{"first"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := created.Tasks[0].ID
	second := created.Tasks[1].ID
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, second, "principal-test"); !errors.Is(err, ErrDependency) {
		t.Fatalf("start dependent error = %v, want dependency error", err)
	}
	if _, err := service.BlockTask(ctx, remoteSessionID, created.ID, first, "principal-test", "", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty block reason error = %v, want invalid input", err)
	}
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, first, "principal-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BlockTask(ctx, remoteSessionID, created.ID, first, "principal-test", "dependency unavailable", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, first, "principal-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteTask(ctx, remoteSessionID, created.ID, first, "principal-test", nil); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatalf("complete without evidence error = %v", err)
	}
	completed, err := service.CompleteTask(ctx, remoteSessionID, created.ID, first, "principal-test", []EvidenceInput{{Kind: "source", ReferenceID: "internal/main.go"}})
	if err != nil || completed.Status != TaskCompleted || len(completed.Evidence) != 1 {
		t.Fatalf("completed task = %+v, err=%v", completed, err)
	}
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, second, "principal-test"); err != nil {
		t.Fatal(err)
	}
	verificationID := seedToolEvidence(t, store.DB(), remoteSessionID, "exec_command", "succeeded", 0)
	if _, err := service.CompleteTask(ctx, remoteSessionID, created.ID, second, "principal-test", []EvidenceInput{{Kind: EvidenceVerification, ReferenceID: verificationID, Metadata: map[string]any{"status": "passed"}}}); err != nil {
		t.Fatal(err)
	}
	delivery, err := service.Deliver(ctx, remoteSessionID, created.ID, "principal-test")
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Ready || delivery.Status != "completed" || delivery.Plan.Status != PlanCompleted {
		t.Fatalf("delivery = %+v", delivery)
	}
}

func TestServiceRejectsCyclesAndPreservesCompletedTasksDuringReplan(t *testing.T) {
	ctx := context.Background()
	store := openPlanStore(t)
	defer store.Close()
	remoteSessionID := seedPlanSession(t, store.DB())
	service := NewService(store.DB())
	if _, err := service.Create(ctx, remoteSessionID, "principal-test", CreateInput{Goal: "cycle", Tasks: []TaskInput{
		{ID: "a", Title: "A", DependsOn: []string{"b"}}, {ID: "b", Title: "B", DependsOn: []string{"a"}},
	}}); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle error = %v", err)
	}
	created, err := service.Create(ctx, remoteSessionID, "principal-test", CreateInput{Goal: "replan", Tasks: []TaskInput{{ID: "done", Title: "Done"}, {ID: "todo", Title: "Todo"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := created.Tasks[0].ID
	todo := created.Tasks[1].ID
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, done, "principal-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteTask(ctx, remoteSessionID, created.ID, done, "principal-test", []EvidenceInput{{Kind: "source", ReferenceID: "done.go"}}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Replan(ctx, remoteSessionID, created.ID, "principal-test", ReplanInput{Reason: "split remaining work", Operations: []TaskOperation{
		{Action: "add", TaskID: "verify", Title: "Verify", DependsOn: []string{todo}},
		{Action: "update", TaskID: todo, Title: "Updated todo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != PlanInProgress || taskIndex(updated.Tasks, done) < 0 || updated.Tasks[taskIndex(updated.Tasks, done)].Status != TaskCompleted || taskIndex(updated.Tasks, "verify") < 0 {
		t.Fatalf("replanned plan = %+v", updated)
	}
	if _, err := service.Replan(ctx, remoteSessionID, created.ID, "principal-test", ReplanInput{Reason: "mutate completed", Operations: []TaskOperation{{Action: "remove", TaskID: done}}}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("completed removal error = %v", err)
	}
	if _, err := service.Replan(ctx, remoteSessionID, created.ID, "principal-test", ReplanInput{Reason: "replace verify", Operations: []TaskOperation{
		{Action: "remove", TaskID: "verify"}, {Action: "add", TaskID: "verify", Title: "Reused"},
	}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reused task id error = %v", err)
	}
}

func TestServiceDeliveryBlocksInvalidEvidenceUntilVerificationPasses(t *testing.T) {
	ctx := context.Background()
	store := openPlanStore(t)
	defer store.Close()
	remoteSessionID := seedPlanSession(t, store.DB())
	service := NewService(store.DB())
	created, err := service.Create(ctx, remoteSessionID, "principal-test", CreateInput{Goal: "change", Tasks: []TaskInput{{ID: "apply", Title: "Apply"}}})
	if err != nil {
		t.Fatal(err)
	}
	apply := created.Tasks[0].ID
	if _, err := service.StartTask(ctx, remoteSessionID, created.ID, apply, "principal-test"); err != nil {
		t.Fatal(err)
	}
	verificationID := seedToolEvidence(t, store.DB(), remoteSessionID, "exec_command", "succeeded", 0)
	if _, err := service.CompleteTask(ctx, remoteSessionID, created.ID, apply, "principal-test", []EvidenceInput{{Kind: EvidenceVerification, ReferenceID: verificationID, Metadata: map[string]any{"status": "failed"}}}); err != nil {
		t.Fatal(err)
	}
	blocked, err := service.Deliver(ctx, remoteSessionID, created.ID, "principal-test")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Ready || blocked.Status != "blocked" || len(blocked.Blockers) == 0 {
		t.Fatalf("blocked delivery = %+v", blocked)
	}
	if _, err := store.DB().Exec(`UPDATE plan_task_evidence SET metadata_json = '{"status":"passed"}' WHERE kind = 'verification'`); err != nil {
		t.Fatal(err)
	}
	// The blocked plan is deliverable after its verification evidence passes.
	ready, err := service.Deliver(ctx, remoteSessionID, created.ID, "principal-test")
	if err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.Plan.Status != PlanCompleted {
		t.Fatalf("ready delivery = %+v", ready)
	}
}

func openPlanStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func seedToolEvidence(t *testing.T, db *sql.DB, remoteSessionID, tool, status string, exitCode any) string {
	t.Helper()
	id := newID("req_")
	raw, err := json.Marshal(map[string]any{
		"_meta":             map[string]any{"mcpx/request_id": id},
		"structuredContent": map[string]any{"output": "test result", "exit_code": exitCode},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO tool_results (request_id, workspace_name, remote_session_id, tool_name, status, result_json, exit_code, created_at, updated_at)
		VALUES (?, 'test', ?, ?, ?, ?, ?, 1, 1)`, id, remoteSessionID, tool, status, string(raw), exitCode)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedPlanSession(t *testing.T, db *sql.DB) string {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "internal", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "done.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO principals (id, kind, subject_hash, created_at, last_seen_at) VALUES ('principal-test', 'test', 'test', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO remote_sessions (id, workspace_name, workspace_path, label, description, status, owner_principal_id, version, created_at, last_active_at) VALUES ('rs_plan_test', 'test', ?, 'test', '', 'active', 'principal-test', 1, 1, 1)`, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO remote_session_members (remote_session_id, principal_id, role, joined_at, last_active_at) VALUES ('rs_plan_test', 'principal-test', 'owner', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	return "rs_plan_test"
}

func TestCodexEvidenceValidatesPersistedRequest(t *testing.T) {
	cases := []struct {
		name, kind, tool, status         string
		exitCode                         any
		otherSession, missing, wantValid bool
	}{
		{name: "read", kind: EvidenceRead, tool: "exec_command", status: "succeeded", exitCode: 0, wantValid: true},
		{name: "edit", kind: EvidenceEdit, tool: "apply_patch", status: "succeeded", exitCode: 0, wantValid: true},
		{name: "execute", kind: EvidenceExecute, tool: "exec_command", status: "succeeded", exitCode: 0, wantValid: true},
		{name: "stdin", kind: EvidenceExecute, tool: "write_stdin", status: "succeeded", exitCode: 0, wantValid: true},
		{name: "verification", kind: EvidenceVerification, tool: "write_stdin", status: "succeeded", exitCode: 0, wantValid: true},
		{name: "nonzero", kind: EvidenceExecute, tool: "exec_command", status: "failed", exitCode: 7},
		{name: "nonzero-status-cannot-override", kind: EvidenceRead, tool: "exec_command", status: "succeeded", exitCode: 7},
		{name: "failed-patch", kind: EvidenceEdit, tool: "apply_patch", status: "failed", exitCode: 1},
		{name: "verification-nonzero", kind: EvidenceVerification, tool: "exec_command", status: "failed", exitCode: 2},
		{name: "running", kind: EvidenceExecute, tool: "exec_command", status: "accepted"},
		{name: "missing-exit-code", kind: EvidenceExecute, tool: "write_stdin", status: "succeeded"},
		{name: "failed-status", kind: EvidenceExecute, tool: "exec_command", status: "failed", exitCode: 0},
		{name: "read-wrong-tool", kind: EvidenceRead, tool: "apply_patch", status: "succeeded", exitCode: 0},
		{name: "edit-wrong-tool", kind: EvidenceEdit, tool: "exec_command", status: "succeeded", exitCode: 0},
		{name: "execute-wrong-tool", kind: EvidenceExecute, tool: "apply_patch", status: "succeeded", exitCode: 0},
		{name: "verification-wrong-tool", kind: EvidenceVerification, tool: "observe", status: "succeeded", exitCode: 0},
		{name: "cross-session", kind: EvidenceEdit, tool: "apply_patch", status: "succeeded", exitCode: 0, otherSession: true},
		{name: "missing", kind: EvidenceExecute, tool: "exec_command", status: "succeeded", exitCode: 0, missing: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openPlanStore(t)
			defer store.Close()
			sessionID := seedPlanSession(t, store.DB())
			service := NewService(store.DB())
			item, err := service.Create(ctx, sessionID, "principal-test", CreateInput{Goal: "proof", Tasks: []TaskInput{{Title: "work"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.StartTask(ctx, sessionID, item.ID, item.Tasks[0].ID, "principal-test"); err != nil {
				t.Fatal(err)
			}
			resultSession := sessionID
			if test.otherSession {
				resultSession = "rs_other"
			}
			id := "req_missing"
			if !test.missing {
				id = seedToolEvidence(t, store.DB(), resultSession, test.tool, test.status, test.exitCode)
			}
			_, err = service.CompleteTask(ctx, sessionID, item.ID, item.Tasks[0].ID, "principal-test", []EvidenceInput{{
				Kind: test.kind, ReferenceID: id, Metadata: map[string]any{"status": "succeeded", "exit_code": 0, "passed": true},
			}})
			if !test.wantValid {
				if !errors.Is(err, ErrEvidence) {
					t.Fatalf("accepted invalid evidence: %v", err)
				}
				loaded, err := service.Get(ctx, sessionID, item.ID)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Tasks[0].Status != TaskInProgress || len(loaded.Tasks[0].Evidence) != 0 {
					t.Fatalf("rejected evidence changed task: %+v", loaded.Tasks[0])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			delivery, err := service.Deliver(ctx, sessionID, item.ID, "principal-test")
			if err != nil || !delivery.Ready {
				t.Fatalf("delivery=%+v err=%v", delivery, err)
			}
		})
	}
}

func TestCodexDeliveryRechecksToolResults(t *testing.T) {
	for _, kind := range []string{EvidenceRead, EvidenceEdit, EvidenceExecute, EvidenceVerification} {
		for _, mutation := range []string{
			"status = 'failed', exit_code = 9", "exit_code = NULL", "status = 'accepted'",
			"remote_session_id = 'rs_other'", "tool_name = 'observe'", "delete",
		} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				ctx := context.Background()
				store := openPlanStore(t)
				defer store.Close()
				sessionID := seedPlanSession(t, store.DB())
				service := NewService(store.DB())
				item, err := service.Create(ctx, sessionID, "principal-test", CreateInput{Goal: "recheck", Tasks: []TaskInput{{Title: "work"}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.StartTask(ctx, sessionID, item.ID, item.Tasks[0].ID, "principal-test"); err != nil {
					t.Fatal(err)
				}
				tool := "exec_command"
				if kind == EvidenceEdit {
					tool = "apply_patch"
				}
				id := seedToolEvidence(t, store.DB(), sessionID, tool, "succeeded", 0)
				if _, err := service.CompleteTask(ctx, sessionID, item.ID, item.Tasks[0].ID, "principal-test", []EvidenceInput{{Kind: kind, ReferenceID: id, Metadata: map[string]any{"passed": true}}}); err != nil {
					t.Fatal(err)
				}
				query := "UPDATE tool_results SET " + mutation + " WHERE request_id = ?"
				if mutation == "delete" {
					query = "DELETE FROM tool_results WHERE request_id = ?"
				}
				if _, err := store.DB().Exec(query, id); err != nil {
					t.Fatal(err)
				}
				delivery, err := service.Deliver(ctx, sessionID, item.ID, "principal-test")
				if err != nil {
					t.Fatal(err)
				}
				if delivery.Ready || !strings.Contains(strings.Join(delivery.Blockers, ","), "tool_result_invalid:"+id) {
					t.Fatalf("stale validation flag bypassed durable result: %+v", delivery)
				}
			})
		}
	}
}
