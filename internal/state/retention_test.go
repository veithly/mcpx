package state

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpx/internal/config"
)

func newRetentionTestService(t *testing.T, logDir string) (*sql.DB, *RetentionService, time.Time) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	policy := config.RetentionConfig{
		Enabled:             true,
		Interval:            "1h",
		ProcessEventTTL:     "1h",
		ProcessEventMaxRows: 1,
		MemoryEventTTL:      "1h",
		MemoryEventMaxRows:  1,
		TerminalTaskTTL:     "1h",
		SnapshotTTL:         "1h",
		VacuumThresholdRows: 100000,
	}
	service, err := NewRetentionService(store.DB(), logDir, policy)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return store.DB(), service, now
}

func insertRetentionPrincipal(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO principals(id, kind, subject_hash, created_at, last_seen_at) VALUES (?, 'test', ?, 1, 1)`, id, id)
	if err != nil {
		t.Fatal(err)
	}
}

func insertRetentionSession(t *testing.T, db *sql.DB, id, workspace, status, owner string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO remote_sessions
        (id, workspace_name, workspace_path, label, status, owner_principal_id, created_at, last_active_at)
        VALUES (?, ?, '/tmp', ?, ?, ?, 1, 1)`, id, workspace, id, status, owner)
	if err != nil {
		t.Fatal(err)
	}
}

func insertRetentionEvent(t *testing.T, db *sql.DB, workspace, remoteID, eventType, tool string, createdAt int64) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO observation_events
        (workspace_name, remote_session_id, event_type, tool_name, summary, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`, workspace, remoteID, eventType, tool, eventType, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return sequence
}

func insertRetentionTask(t *testing.T, db *sql.DB, id, remoteID, status, logPath string, startedAt int64, finishedAt *int64) {
	t.Helper()
	var finished any
	if finishedAt != nil {
		finished = *finishedAt
	}
	_, err := db.Exec(`INSERT INTO terminal_tasks
        (id, remote_session_id, workspace_name, workspace_path, command, status, log_path, started_at, finished_at, updated_at)
        VALUES (?, ?, 'demo', '/tmp', 'echo output', ?, ?, ?, ?, ?)`, id, remoteID, status, logPath, startedAt, finished, startedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func writeRetentionTaskLogs(t *testing.T, logDir, id string) []string {
	t.Helper()
	base := filepath.Join(logDir, id)
	paths := []string{base + ".log", base + ".stdout.log", base + ".stderr.log"}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("output"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// insertRetentionEventsBulk seeds count rows of one event type in a single
// transaction so catch-up tests can create thousands of candidates cheaply.
func insertRetentionEventsBulk(t *testing.T, db *sql.DB, workspace, eventType string, count int, createdAt int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO observation_events
        (workspace_name, remote_session_id, event_type, tool_name, summary, created_at)
        VALUES (?, '', ?, 'file_read', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < count; i++ {
		if _, err := stmt.Exec(workspace, eventType, strings.Repeat("x", 512), createdAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countRetentionEvents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func sqliteFreelistCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var freelist int
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&freelist); err != nil {
		t.Fatal(err)
	}
	return freelist
}

func TestRetentionCatchUpDeletesBeyondOneBatchPerPass(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	service.policy.ProcessEventMaxRows = 5
	service.policy.MemoryEventMaxRows = 5

	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	insertRetentionEventsBulk(t, db, "demo", "tool.started", 1100, old)
	for i := 0; i < 5; i++ {
		insertRetentionEvent(t, db, "demo", "", "tool.started", "file_read", recent+int64(i))
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors=%v", report.Errors)
	}
	if report.DeletedObservationEvents != 1100 {
		t.Fatalf("deleted=%d, want 1100 (catch-up must exceed one 500-row batch); report=%+v", report.DeletedObservationEvents, report)
	}
	if remaining := countRetentionEvents(t, db); remaining != 5 {
		t.Fatalf("remaining=%d, want 5 (newest-window events)", remaining)
	}
}

func TestRetentionStopsAtPerPassBatchQuota(t *testing.T) {
	originalBatches := retentionMaxBatches
	retentionMaxBatches = 2
	defer func() { retentionMaxBatches = originalBatches }()

	db, service, now := newRetentionTestService(t, "")
	service.policy.ProcessEventMaxRows = 5
	service.policy.MemoryEventMaxRows = 5

	old := now.Add(-2 * time.Hour).UnixMilli()
	insertRetentionEventsBulk(t, db, "demo", "tool.started", 1200, old)

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The newest-window keeps 5 of the 1200 rows, so 1195 are candidates and
	// two batches delete at most 1000 in this pass.
	if report.DeletedObservationEvents != 1000 {
		t.Fatalf("deleted=%d, want 1000 (2 batches x 500); report=%+v", report.DeletedObservationEvents, report)
	}
	if remaining := countRetentionEvents(t, db); remaining != 200 {
		t.Fatalf("remaining=%d, want 200 (quota stop)", remaining)
	}
}

func TestObservationRetentionActivityPredicate(t *testing.T) {
	predicate, err := observationPredicate("activity", "e")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{
		"agent.activity", "operation.started", "operation.step.started", "operation.step.completed", "operation.completed",
	} {
		if !strings.Contains(predicate, "'"+eventType+"'") {
			t.Fatalf("activity predicate misses %q: %s", eventType, predicate)
		}
	}
	if _, err := observationPredicate("bogus", "e"); err == nil {
		t.Fatal("unknown category must be rejected")
	}
}

func TestRetentionExpiresAgentActivityAndOperationEvents(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	insertRetentionPrincipal(t, db, "principal")
	service.policy.ProcessEventMaxRows = 100

	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	var oldSequences, recentSequences []int64
	for _, eventType := range []string{
		"agent.activity", "operation.started", "operation.step.started", "operation.step.completed", "operation.completed",
	} {
		oldSequences = append(oldSequences, insertRetentionEvent(t, db, "demo", "session", eventType, "", old))
		recentSequences = append(recentSequences, insertRetentionEvent(t, db, "demo", "session", eventType, "", recent))
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors=%v", report.Errors)
	}
	if report.DeletedObservationEvents != len(oldSequences) {
		t.Fatalf("deleted=%d, want %d; report=%+v", report.DeletedObservationEvents, len(oldSequences), report)
	}
	for _, sequence := range oldSequences {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expired activity event %d remains", sequence)
		}
	}
	for _, sequence := range recentSequences {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("recent activity event %d was deleted", sequence)
		}
	}
}

func TestRetentionIsolatesCategoryErrors(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	old := now.Add(-2 * time.Hour).UnixMilli()
	if _, err := db.Exec(`DROP TABLE observation_events`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO tool_results
		(request_id, workspace_name, remote_session_id, tool_name, status, result_json, created_at, updated_at)
		VALUES ('old-result', 'demo', 'session', 'execute', 'succeeded', '{}', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("one broken category must not fail the pass: %v", err)
	}
	if report.DeletedToolResults != 1 {
		t.Fatalf("deleted tool results=%d, want 1; report=%+v", report.DeletedToolResults, report)
	}
	// process, memory and activity categories each report their own failure.
	if len(report.Errors) < 3 {
		t.Fatalf("errors=%v, want one per observation category", report.Errors)
	}
}

func TestObservationRetentionTreatsProgressAsMemory(t *testing.T) {
	process, err := observationPredicate("process", "e")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := observationPredicate("memory", "e")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(process, "tool_name <> 'progress'") {
		t.Fatalf("process predicate does not exclude progress: %s", process)
	}
	if !strings.Contains(memory, "tool_name = 'progress'") {
		t.Fatalf("memory predicate does not retain progress: %s", memory)
	}
}

func TestObservationRetentionNewestWindowIsNotCorrelated(t *testing.T) {
	db, _, now := newRetentionTestService(t, "")
	query, err := observationDeletionQuery("process")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query,
		"demo", now.UnixMilli(), "demo", 10000, retentionBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToUpper(detail), "CORRELATED LIST SUBQUERY") {
			t.Fatalf("retention query repeats newest window for every event: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionBoundsSessionEventsAndReferencedSnapshots(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	insertRetentionPrincipal(t, db, "principal")
	insertRetentionSession(t, db, "active", "demo", "active", "principal")
	insertRetentionSession(t, db, "closed", "demo", "closed", "principal")

	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	processOld := insertRetentionEvent(t, db, "demo", "", "tool.started", "file_read", old)
	activeProcess := insertRetentionEvent(t, db, "demo", "active", "command.output", "command_execute", old)
	closedMemory := insertRetentionEvent(t, db, "demo", "closed", "file.changed", "", old)
	activeMemory := insertRetentionEvent(t, db, "demo", "active", "session.lifecycle", "", old)
	firstRecent := insertRetentionEvent(t, db, "demo", "", "tool.started", "file_read", recent)
	secondRecent := insertRetentionEvent(t, db, "demo", "", "tool.started", "file_read", recent+1)
	if processOld == 0 || activeProcess == 0 || closedMemory == 0 || activeMemory == 0 || firstRecent == 0 || secondRecent == 0 {
		t.Fatal("failed to create retention fixtures")
	}
	_, err := db.Exec(`INSERT INTO runtime_instances(id, started_at, last_seen_at) VALUES ('runtime', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO environment_snapshots
        (id, remote_session_id, runtime_instance_id, static_digest, snapshot_json, created_at)
        VALUES ('env-active', 'active', 'runtime', 'digest', '{}', ?),
               ('env-closed', 'closed', 'runtime', 'digest', '{}', ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE remote_sessions SET environment_snapshot_id = 'env-active' WHERE id = 'active'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO file_snapshots(id, remote_session_id, snapshot_json, created_at)
        VALUES ('file-active', 'active', '{}', ?), ('file-closed', 'closed', '{}', ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedObservationEvents != 5 {
		t.Fatalf("deleted observation events=%d, want 5; report=%+v", report.DeletedObservationEvents, report)
	}
	for _, sequence := range []int64{secondRecent} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("protected/recent event %d was deleted", sequence)
		}
	}
	for _, sequence := range []int64{processOld, activeProcess, closedMemory, activeMemory, firstRecent} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("eligible event %d remains", sequence)
		}
	}

	var envCount, fileCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM environment_snapshots WHERE id = 'env-active'`).Scan(&envCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM environment_snapshots WHERE id = 'env-closed'`).Scan(&envCount); err != nil {
		t.Fatal(err)
	}
	if envCount != 0 {
		t.Fatal("closed environment snapshot remains")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM environment_snapshots WHERE id = 'env-active'`).Scan(&envCount); err != nil {
		t.Fatal(err)
	}
	if envCount != 1 {
		t.Fatal("referenced environment snapshot was deleted")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_snapshots WHERE id = 'file-closed'`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 0 {
		t.Fatal("closed file snapshot remains")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_snapshots WHERE id = 'file-active'`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 1 {
		t.Fatal("active file snapshot was deleted")
	}
}

func TestRetentionDeletesExpiredEventsFromOpenSessionStatuses(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	insertRetentionPrincipal(t, db, "principal")
	service.policy.ProcessEventMaxRows = 100

	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	var oldSequences, recentSequences []int64
	for _, status := range []string{"active", "idle", "blocked"} {
		insertRetentionSession(t, db, status, "demo", status, "principal")
		oldSequences = append(oldSequences, insertRetentionEvent(t, db, "demo", status, "tool.started", "file_read", old))
		recentSequences = append(recentSequences, insertRetentionEvent(t, db, "demo", status, "tool.started", "file_read", recent))
	}

	if _, err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, sequence := range oldSequences {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expired event %d remains for an open session", sequence)
		}
	}
	for _, sequence := range recentSequences {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM observation_events WHERE sequence = ?`, sequence).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("recent event %d was deleted", sequence)
		}
	}
}

func TestRetentionDeletesExpiredEphemeralRecords(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	insertRetentionPrincipal(t, db, "principal")
	insertRetentionSession(t, db, "closed", "demo", "closed", "principal")
	old := now.Add(-time.Hour).UnixMilli()
	_, err := db.Exec(`INSERT INTO approvals(id, remote_session_id, principal_id, tool, summary, payload_json, status, created_at, expires_at)
        VALUES ('approval', 'closed', 'principal', 'execute', 'old', '{}', 'expired', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO secret_requests(id, remote_session_id, principal_id, payload_json, status, created_at, expires_at)
        VALUES ('secret', 'closed', 'principal', '{}', 'expired', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO idempotency_records(remote_session_id, principal_id, client_request_id, operation, response_json, created_at, expires_at)
        VALUES ('closed', 'principal', 'request', 'test', '{}', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM approvals`,
		`SELECT COUNT(*) FROM secret_requests`,
		`SELECT COUNT(*) FROM idempotency_records`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expired record remains for %s", query)
		}
	}
}

func TestRetentionDeletesExpiredToolResults(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	_, err := db.Exec(`INSERT INTO tool_results
		(request_id, workspace_name, remote_session_id, tool_name, status, result_json, created_at, updated_at)
		VALUES ('old-result', 'demo', 'session', 'execute', 'succeeded', '{}', ?, ?),
		       ('recent-result', 'demo', 'session', 'execute', 'succeeded', '{}', ?, ?)`, old, old, recent, recent)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedToolResults != 1 {
		t.Fatalf("deleted tool results=%d, want 1; report=%+v", report.DeletedToolResults, report)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_results WHERE request_id = 'old-result'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired tool result remains")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_results WHERE request_id = 'recent-result'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("recent tool result was deleted")
	}
}

func TestRetentionDeletesExpiredOperationsOnlyForClosedSessions(t *testing.T) {
	db, service, now := newRetentionTestService(t, "")
	insertRetentionPrincipal(t, db, "principal")
	insertRetentionSession(t, db, "closed", "demo", "closed", "principal")
	insertRetentionSession(t, db, "active", "demo", "active", "principal")
	old := now.Add(-2 * time.Hour).UnixMilli()
	recent := now.Add(-10 * time.Minute).UnixMilli()
	future := now.Add(time.Hour).UnixMilli()
	_, err := db.Exec(`INSERT INTO operations
		(id, remote_session_id, workspace_name, request_id, purpose, state, result_json, error_json, created_at, expires_at)
		VALUES
		('expired', 'closed', 'demo', 'request', 'old', 'succeeded', '{}', '{}', ?, ?),
		('active-session', 'active', 'demo', 'request', 'old', 'succeeded', '{}', '{}', ?, ?),
		('recent', 'closed', 'demo', 'request', 'recent', 'succeeded', '{}', '{}', ?, ?),
		('running', 'closed', 'demo', 'request', 'running', 'running', '{}', '{}', ?, ?)`,
		old, old, old, old, recent, future, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO operation_steps
		(operation_id, step_id, tool_name, state, request_id, created_at, result_json, error_json)
		VALUES ('expired', 'step', 'source_read', 'succeeded', 'request', ?, '{}', '{}')`, old)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedOperations != 1 {
		t.Fatalf("deleted operations=%d, want 1; report=%+v", report.DeletedOperations, report)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operations WHERE id = 'expired'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired closed-session operation remains")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_steps WHERE operation_id = 'expired'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("operation child step was not cascaded")
	}
	for _, id := range []string{"active-session", "recent", "running"} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM operations WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("operation %q was deleted unexpectedly", id)
		}
	}
}

func TestRetentionTaskLogFailureKeepsRow(t *testing.T) {
	logDir := t.TempDir()
	db, service, now := newRetentionTestService(t, logDir)
	insertRetentionPrincipal(t, db, "principal")
	insertRetentionSession(t, db, "closed", "demo", "closed", "principal")
	logPath := filepath.Join(logDir, "task.log")
	if err := os.WriteFile(logPath, []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO terminal_tasks
        (id, remote_session_id, workspace_name, workspace_path, command, status, log_path, started_at, finished_at, updated_at)
        VALUES ('task', 'closed', 'demo', '/tmp', 'echo output', 'exited', ?, ?, ?, ?)`, logPath, now.Add(-2*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	service.remove = func(string) error { return errors.New("permission denied") }
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("cleanup errors=%v, want one", report.Errors)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM terminal_tasks WHERE id = 'task'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("task row was deleted after log removal failed")
	}
}

func TestRetentionDeletesFinishedTasksFromOpenSessions(t *testing.T) {
	logDir := t.TempDir()
	db, service, now := newRetentionTestService(t, logDir)
	insertRetentionPrincipal(t, db, "principal")
	old := now.Add(-2 * time.Hour).UnixMilli()

	var removableIDs []string
	for _, status := range []string{"active", "idle", "blocked"} {
		insertRetentionSession(t, db, status, "demo", status, "principal")
		id := status + "-task"
		writeRetentionTaskLogs(t, logDir, status)
		finished := old
		insertRetentionTask(t, db, id, status, "exited", filepath.Join(logDir, status+".log"), old, &finished)
		removableIDs = append(removableIDs, id)
	}

	insertRetentionSession(t, db, "running", "demo", "active", "principal")
	runningPaths := writeRetentionTaskLogs(t, logDir, "running")
	insertRetentionTask(t, db, "running-task", "running", "running", runningPaths[0], old, nil)

	insertRetentionSession(t, db, "referenced", "demo", "active", "principal")
	referencedPaths := writeRetentionTaskLogs(t, logDir, "referenced")
	insertRetentionTask(t, db, "referenced-task", "referenced", "exited", referencedPaths[0], old, &old)
	_, err := db.Exec(`INSERT INTO plans
        (id, remote_session_id, goal, status, created_by, created_at, updated_at)
        VALUES ('plan', 'referenced', 'retain evidence', 'ready', 'principal', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO plan_tasks
        (id, plan_id, ordinal, title, status, created_at, updated_at)
        VALUES ('plan-task', 'plan', 1, 'retain task', 'todo', ?, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO plan_task_evidence
        (id, plan_id, task_id, kind, reference_id, validated, created_by, created_at)
        VALUES ('evidence', 'plan', 'plan-task', 'execute', 'referenced-task', 1, 'principal', ?)`, old)
	if err != nil {
		t.Fatal(err)
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedTerminalTasks != len(removableIDs) {
		t.Fatalf("deleted terminal tasks=%d, want %d; report=%+v", report.DeletedTerminalTasks, len(removableIDs), report)
	}
	for _, id := range removableIDs {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM terminal_tasks WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("finished task %q remains", id)
		}
	}
	for _, id := range []string{"running-task", "referenced-task"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM terminal_tasks WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("protected task %q was deleted", id)
		}
	}
	for _, path := range runningPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("running task log %q was removed: %v", path, err)
		}
	}
	for _, path := range referencedPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("referenced task log %q was removed: %v", path, err)
		}
	}
	for _, status := range []string{"active", "idle", "blocked"} {
		for _, path := range []string{
			filepath.Join(logDir, status+".log"),
			filepath.Join(logDir, status+".stdout.log"),
			filepath.Join(logDir, status+".stderr.log"),
		} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("removed task log %q still exists: %v", path, err)
			}
		}
	}
}

// growFreelist seeds fat rows and deletes most of them so the database holds
// free pages even though rows were removed.
func growFreelist(t *testing.T, db *sql.DB, seed, remove int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO observation_events
		(workspace_name, remote_session_id, event_type, tool_name, summary, created_at)
		VALUES ('demo', '', 'tool.started', 'file_read', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < seed; i++ {
		if _, err := stmt.Exec(strings.Repeat("x", 512), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM observation_events WHERE rowid IN
		(SELECT rowid FROM observation_events ORDER BY rowid LIMIT ?)`, remove); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionVacuumsIncrementalWhenFreelistExceedsThreshold(t *testing.T) {
	db, service, _ := newRetentionTestService(t, "")
	service.policy.VacuumThresholdRows = 8

	growFreelist(t, db, 500, 450)
	before := sqliteFreelistCount(t, db)
	if before <= 8 {
		t.Fatalf("fixture freelist=%d, want > 8", before)
	}

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors=%v", report.Errors)
	}
	if !report.Vacuumed {
		t.Fatalf("incremental vacuum did not run; freelist before=%d report=%+v", before, report)
	}
	if after := sqliteFreelistCount(t, db); after > 8 {
		t.Fatalf("freelist after=%d, want <= 8", after)
	}
}

func TestRetentionLegacyDatabaseOnlyTruncatesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=auto_vacuum(NONE)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := applyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var autoVacuum int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&autoVacuum); err != nil {
		t.Fatal(err)
	}
	if autoVacuum != 0 {
		t.Fatalf("fixture auto_vacuum=%d, want 0 (legacy)", autoVacuum)
	}

	growFreelist(t, db, 500, 450)
	before := sqliteFreelistCount(t, db)
	if before <= 0 {
		t.Fatalf("fixture freelist=%d, want > 0", before)
	}

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	service, err := NewRetentionService(db, "", config.RetentionConfig{
		Enabled:             true,
		Interval:            "1h",
		ProcessEventTTL:     "1h",
		ProcessEventMaxRows: 5,
		MemoryEventTTL:      "1h",
		MemoryEventMaxRows:  5,
		TerminalTaskTTL:     "1h",
		SnapshotTTL:         "1h",
		VacuumThresholdRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }

	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("errors=%v", report.Errors)
	}
	if report.Vacuumed {
		t.Fatal("legacy database must not report incremental vacuum")
	}
	if after := sqliteFreelistCount(t, db); after <= 0 {
		t.Fatal("legacy database freelist was reclaimed without a rebuild")
	}
}

func TestNewDatabasesEnableIncrementalAutoVacuum(t *testing.T) {
	db, _, _ := newRetentionTestService(t, "")
	var autoVacuum int
	if err := db.QueryRow(`PRAGMA auto_vacuum`).Scan(&autoVacuum); err != nil {
		t.Fatal(err)
	}
	if autoVacuum != 2 {
		t.Fatalf("auto_vacuum=%d, want 2 (INCREMENTAL)", autoVacuum)
	}
}
