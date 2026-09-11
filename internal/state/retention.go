package state

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/logging"
)

const retentionBatchSize = 500

// retentionMaxBatches bounds catch-up work per category per pass: at most
// retentionMaxBatches*retentionBatchSize rows are removed per category, so a
// backlogged database shrinks every pass instead of only 500 rows per day.
// It is a variable so tests can exercise the quota with tiny datasets.
var retentionMaxBatches = 200

// retentionBatchPause yields the write lock between full-progress batches so
// concurrent observation/tool writes are not starved by cleanup.
const retentionBatchPause = 50 * time.Millisecond

// RetentionService removes only bounded, reconstructable state. It never
// deletes Remote Sessions, Plans, or their source evidence.
type RetentionService struct {
	db     *sql.DB
	logDir string
	policy config.RetentionConfig
	now    func() time.Time
	remove func(string) error
}

// RetentionReport describes one housekeeping pass.
type RetentionReport struct {
	Disabled                 bool
	DeletedObservationEvents int
	DeletedToolResults       int
	DeletedTerminalTasks     int
	DeletedFileSnapshots     int
	DeletedEnvironmentSnaps  int
	DeletedEphemeralRecords  int
	DeletedOperations        int
	Vacuumed                 bool
	Errors                   []string
}

// NewRetentionService creates a state cleanup service after validating the
// effective global configuration.
func NewRetentionService(db *sql.DB, logDir string, policy config.RetentionConfig) (*RetentionService, error) {
	if db == nil {
		return nil, fmt.Errorf("retention database is required")
	}
	if _, _, _, _, _, err := policy.RetentionDurations(); err != nil {
		return nil, err
	}
	return &RetentionService{
		db:     db,
		logDir: strings.TrimSpace(logDir),
		policy: policy,
		now:    time.Now,
		remove: os.Remove,
	}, nil
}

// RunOnce executes one bounded cleanup pass. Every category runs and fails
// independently: category errors are collected in the report so one broken
// table cannot stall all other cleanup for the day. Only initialization and
// configuration failures are returned as the error.
func (s *RetentionService) RunOnce(ctx context.Context) (RetentionReport, error) {
	var report RetentionReport
	if s == nil || s.db == nil {
		return report, fmt.Errorf("retention service is not initialized")
	}
	if !s.policy.Enabled {
		report.Disabled = true
		return report, nil
	}
	_, processTTL, memoryTTL, terminalTaskTTL, snapshotTTL, err := s.policy.RetentionDurations()
	if err != nil {
		return report, err
	}
	now := s.now().UTC()

	// agent.activity and operation.* events were historically covered by no
	// category and grew without bound; they now expire on the process TTL.
	for _, category := range []struct {
		name   string
		cutoff time.Time
		limit  int
	}{
		{name: "process", cutoff: now.Add(-processTTL), limit: s.policy.ProcessEventMaxRows},
		{name: "memory", cutoff: now.Add(-memoryTTL), limit: s.policy.MemoryEventMaxRows},
		{name: "activity", cutoff: now.Add(-processTTL), limit: s.policy.ProcessEventMaxRows},
	} {
		deleted, err := s.deleteObservationBatch(ctx, category.name, category.cutoff, category.limit)
		report.DeletedObservationEvents += deleted
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete %s observation events: %v", category.name, err))
		}
	}

	deleted, err := s.deleteToolResults(ctx, now.Add(-processTTL))
	report.DeletedToolResults = deleted
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("delete tool results: %v", err))
	}

	deleted, err = s.deleteExpiredEphemeral(ctx, now.UnixMilli())
	report.DeletedEphemeralRecords += deleted
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}

	deleted, cleanupErrors, err := s.deleteTerminalTasks(ctx, now.Add(-terminalTaskTTL))
	report.DeletedTerminalTasks += deleted
	report.Errors = append(report.Errors, cleanupErrors...)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("delete terminal tasks: %v", err))
	}

	deleted, err = s.deleteExpiredOperations(ctx, now.UnixMilli())
	report.DeletedOperations += deleted
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("delete expired operations: %v", err))
	}

	counts, err := s.deleteClosedSessionSnapshots(ctx, now.Add(-snapshotTTL))
	report.DeletedFileSnapshots += counts.file
	report.DeletedEnvironmentSnaps += counts.environment
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}

	if err := s.checkpoint(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("wal checkpoint: %v", err))
	}
	vacuumed, vacuumNotices := s.vacuumIfNeeded(ctx)
	report.Vacuumed = vacuumed
	report.Errors = append(report.Errors, vacuumNotices...)
	return report, nil
}

func (s *RetentionService) checkpoint(ctx context.Context, statement string) error {
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, statement).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	_ = busy
	return nil
}

// vacuumIfNeeded releases freed pages once the freelist crosses the configured
// threshold, so Vacuumed is backed by real reclamation instead of being a dead
// config knob. Databases created after auto_vacuum=INCREMENTAL was enabled
// release pages incrementally; legacy databases (auto_vacuum=none) only get a
// WAL truncate checkpoint, because SQLite cannot shrink their main file
// without a one-time VACUUM rebuild that would exclusive-lock the shared
// state DB for tens of seconds. Full VACUUM is never run from the pass.
func (s *RetentionService) vacuumIfNeeded(ctx context.Context) (bool, []string) {
	var freelist int
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelist); err != nil {
		return false, []string{fmt.Sprintf("read freelist_count: %v", err)}
	}
	if freelist <= s.policy.VacuumThresholdRows {
		return false, nil
	}
	var autoVacuum int
	if err := s.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		return false, []string{fmt.Sprintf("read auto_vacuum: %v", err)}
	}
	if autoVacuum == 2 { // 2 = SQLITE_AUTOVACUUM_INCREMENTAL
		// The pragma streams one result row per vacuum step, so the rows must
		// be drained to empty the whole freelist (Exec would step only once).
		// Success is judged by the freelist actually shrinking.
		rows, err := s.db.QueryContext(ctx, "PRAGMA incremental_vacuum")
		if err != nil {
			return false, []string{fmt.Sprintf("incremental vacuum: %v", err)}
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, []string{fmt.Sprintf("incremental vacuum: %v", err)}
		}
		rows.Close()
		var after int
		if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&after); err != nil {
			return false, []string{fmt.Sprintf("re-read freelist_count: %v", err)}
		}
		return after < freelist, nil
	}
	if err := s.checkpoint(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return false, []string{fmt.Sprintf("wal truncate checkpoint: %v", err)}
	}
	logging.With("component", "state_retention").Info(
		"state database freelist exceeds vacuum threshold; WAL truncated, legacy database needs a one-time offline VACUUM rebuild to reclaim main file space",
		"freelist_pages", freelist, "auto_vacuum", autoVacuum)
	return false, nil
}

// deleteBatches repeats a bounded batch delete until a batch comes back short
// (no candidates left), the per-pass batch quota is exhausted, or ctx is done.
// The short-batch exit keeps a pass from pausing after the final batch.
func (s *RetentionService) deleteBatches(ctx context.Context, exec func(ctx context.Context, limit int) (int, error)) (int, error) {
	deleted := 0
	for batch := 0; batch < retentionMaxBatches; batch++ {
		count, err := exec(ctx, retentionBatchSize)
		if err != nil {
			return deleted, err
		}
		deleted += count
		if count < retentionBatchSize {
			return deleted, nil
		}
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(retentionBatchPause):
		}
	}
	return deleted, nil
}

// TotalDeleted returns the number of rows removed in this pass.
func (r RetentionReport) TotalDeleted() int {
	return r.DeletedObservationEvents + r.DeletedToolResults + r.DeletedTerminalTasks + r.DeletedFileSnapshots + r.DeletedEnvironmentSnaps + r.DeletedEphemeralRecords + r.DeletedOperations
}

func (s *RetentionService) deleteToolResults(ctx context.Context, cutoff time.Time) (int, error) {
	return s.deleteBatches(ctx, func(ctx context.Context, limit int) (int, error) {
		result, err := s.db.ExecContext(ctx, `DELETE FROM tool_results
			WHERE rowid IN (
				SELECT rowid FROM tool_results
				WHERE updated_at < ?
				ORDER BY updated_at ASC, request_id ASC
				LIMIT ?
			)`, cutoff.UnixMilli(), limit)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(count), nil
	})
}

func (s *RetentionService) deleteExpiredOperations(ctx context.Context, now int64) (int, error) {
	return s.deleteBatches(ctx, func(ctx context.Context, limit int) (int, error) {
		result, err := s.db.ExecContext(ctx, `DELETE FROM operations
			WHERE rowid IN (
				SELECT operations.rowid
				FROM operations
				LEFT JOIN remote_sessions ON remote_sessions.id = operations.remote_session_id
				WHERE operations.state IN ('succeeded', 'failed', 'interrupted', 'cancelled')
				  AND operations.expires_at <= ?
				  AND (remote_sessions.id IS NULL OR remote_sessions.status IN ('closed', 'archived'))
				ORDER BY operations.expires_at ASC, operations.id ASC
				LIMIT ?
			)`, now, limit)
		if err != nil {
			return 0, fmt.Errorf("delete expired operations: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(count), nil
	})
}

func (s *RetentionService) deleteObservationBatch(ctx context.Context, category string, cutoff time.Time, maxRows int) (int, error) {
	query, err := observationDeletionQuery(category)
	if err != nil {
		return 0, err
	}
	workspaces, err := s.observationWorkspaces(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for batch := 0; batch < retentionMaxBatches; batch++ {
		roundDeleted, fullBatch := 0, false
		for _, workspace := range workspaces {
			count, err := s.deleteObservationWorkspaceBatch(ctx, query, workspace, cutoff, maxRows)
			if err != nil {
				return deleted, err
			}
			deleted += count
			roundDeleted += count
			if count >= retentionBatchSize {
				fullBatch = true
			}
		}
		if roundDeleted == 0 || !fullBatch {
			return deleted, nil
		}
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(retentionBatchPause):
		}
	}
	return deleted, nil
}

func (s *RetentionService) deleteObservationWorkspaceBatch(ctx context.Context, query, workspace string, cutoff time.Time, maxRows int) (int, error) {
	// Pass workspace twice so the newest window is independent of the outer
	// row. A correlated subquery turns this bounded cleanup into O(n^2).
	rows, err := s.db.QueryContext(ctx, query, workspace, cutoff.UnixMilli(), workspace, maxRows, retentionBatchSize)
	if err != nil {
		return 0, err
	}
	var sequences []int64
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			rows.Close()
			return 0, err
		}
		sequences = append(sequences, sequence)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(sequences) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sequences)), ",")
	args := make([]any, len(sequences))
	for i, sequence := range sequences {
		args[i] = sequence
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM observation_events WHERE sequence IN ("+placeholders+")", args...)
	if err != nil {
		return 0, err
	}
	if _, err := result.RowsAffected(); err != nil {
		return 0, err
	}
	return len(sequences), nil
}

func observationDeletionQuery(category string) (string, error) {
	predicate, err := observationPredicate(category, "e")
	if err != nil {
		return "", err
	}
	newestPredicate, err := observationPredicate(category, "newest")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`SELECT e.sequence
FROM observation_events e
WHERE e.workspace_name = ? AND %s
  AND (e.created_at < ? OR e.sequence NOT IN (
      SELECT newest.sequence
      FROM observation_events newest
      WHERE newest.workspace_name = ? AND %s
      ORDER BY newest.created_at DESC, newest.sequence DESC
      LIMIT ?
  ))
ORDER BY e.created_at ASC, e.sequence ASC
LIMIT ?`, predicate, newestPredicate), nil
}

func observationPredicate(category, alias string) (string, error) {
	switch category {
	case "process":
		return fmt.Sprintf(`(%s.event_type IN ('tool.started', 'command.output', 'observer.notice') OR
         (%s.event_type = 'tool.completed' AND %s.tool_name <> 'progress'))`, alias, alias, alias), nil
	case "memory":
		return fmt.Sprintf(`(%s.event_type IN ('file.changed', 'session.lifecycle') OR
         (%s.event_type = 'tool.completed' AND %s.tool_name = 'progress'))`, alias, alias, alias), nil
	case "activity":
		return fmt.Sprintf(`%s.event_type IN ('agent.activity', 'operation.started', 'operation.step.started', 'operation.step.completed', 'operation.completed')`, alias), nil
	default:
		return "", fmt.Errorf("unknown observation retention category %q", category)
	}
}

func (s *RetentionService) observationWorkspaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT workspace_name FROM observation_events ORDER BY workspace_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workspaces []string
	for rows.Next() {
		var workspace string
		if err := rows.Scan(&workspace); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

func (s *RetentionService) deleteExpiredEphemeral(ctx context.Context, now int64) (int, error) {
	total := 0
	var firstErr error
	for _, table := range []string{"approvals", "secret_requests", "idempotency_records", "clean_idempotency_records", "clean_edit_records"} {
		deleted, err := s.deleteBatches(ctx, func(ctx context.Context, limit int) (int, error) {
			result, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid IN (SELECT rowid FROM "+table+" WHERE expires_at <= ? ORDER BY expires_at LIMIT ?)", now, limit)
			if err != nil {
				return 0, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			return int(count), nil
		})
		total += deleted
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("delete expired %s: %w", table, err)
		}
	}
	return total, firstErr
}

func (s *RetentionService) deleteTerminalTasks(ctx context.Context, cutoff time.Time) (int, []string, error) {
	deleted := 0
	var cleanupErrors []string
	for batch := 0; batch < retentionMaxBatches; batch++ {
		count, roundErrors, err := s.deleteTerminalTaskBatch(ctx, cutoff)
		deleted += count
		cleanupErrors = append(cleanupErrors, roundErrors...)
		if err != nil {
			return deleted, cleanupErrors, err
		}
		// A zero-count round means only log-failed candidates remain; retrying
		// them within this pass would spin without progress.
		if count == 0 || count < retentionBatchSize {
			return deleted, cleanupErrors, nil
		}
		select {
		case <-ctx.Done():
			return deleted, cleanupErrors, ctx.Err()
		case <-time.After(retentionBatchPause):
		}
	}
	return deleted, cleanupErrors, nil
}

func (s *RetentionService) deleteTerminalTaskBatch(ctx context.Context, cutoff time.Time) (int, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.log_path
FROM terminal_tasks t
WHERE t.status <> 'running'
  AND COALESCE(t.finished_at, t.updated_at) < ?
  AND NOT EXISTS (
      SELECT 1 FROM plan_task_evidence e
      WHERE e.kind = 'execute' AND e.reference_id = t.id
  )
ORDER BY COALESCE(t.finished_at, t.updated_at), t.id
LIMIT ?`, cutoff.UnixMilli(), retentionBatchSize)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	type candidate struct{ id, logPath string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.logPath); err != nil {
			return 0, nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	deleted := 0
	var cleanupErrors []string
	for _, item := range candidates {
		if err := s.removeTaskLog(item.logPath); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("task %s log: %v", item.id, err))
			continue
		}
		result, err := s.db.ExecContext(ctx, "DELETE FROM terminal_tasks WHERE id = ?", item.id)
		if err != nil {
			return deleted, cleanupErrors, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return deleted, cleanupErrors, err
		}
		deleted += int(count)
	}
	return deleted, cleanupErrors, nil
}

func (s *RetentionService) removeTaskLog(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || s.logDir == "" {
		return nil
	}
	paths := []string{path}
	if strings.HasSuffix(path, ".log") && !strings.HasSuffix(path, ".stdout.log") && !strings.HasSuffix(path, ".stderr.log") {
		base := strings.TrimSuffix(path, ".log")
		paths = append(paths, base+".stdout.log", base+".stderr.log")
	}
	for _, candidate := range paths {
		if err := s.removeTaskLogFile(candidate); err != nil {
			return err
		}
	}
	return nil
}

func (s *RetentionService) removeTaskLogFile(path string) error {
	root, err := filepath.Abs(s.logDir)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("log path is outside task directory")
	}
	if err := s.remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type snapshotDeleteCounts struct {
	file        int
	environment int
}

func (s *RetentionService) deleteClosedSessionSnapshots(ctx context.Context, cutoff time.Time) (snapshotDeleteCounts, error) {
	var counts snapshotDeleteCounts
	fileDeleted, err := s.deleteBatches(ctx, func(ctx context.Context, limit int) (int, error) {
		result, err := s.db.ExecContext(ctx, `DELETE FROM file_snapshots
WHERE id IN (
    SELECT fs.id FROM file_snapshots fs
    LEFT JOIN remote_sessions rs ON rs.id = fs.remote_session_id
    WHERE fs.created_at < ?
      AND (rs.id IS NULL OR rs.status IN ('closed', 'archived'))
    ORDER BY fs.created_at ASC, fs.id
    LIMIT ?
)`, cutoff.UnixMilli(), limit)
		if err != nil {
			return 0, fmt.Errorf("delete file snapshots: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(count), nil
	})
	counts.file = fileDeleted
	if err != nil {
		return counts, err
	}

	environmentDeleted, err := s.deleteBatches(ctx, func(ctx context.Context, limit int) (int, error) {
		result, err := s.db.ExecContext(ctx, `DELETE FROM environment_snapshots
WHERE id IN (
    SELECT es.id FROM environment_snapshots es
    LEFT JOIN remote_sessions rs ON rs.id = es.remote_session_id
    WHERE es.created_at < ?
      AND (rs.id IS NULL OR rs.status IN ('closed', 'archived'))
      AND NOT EXISTS (
          SELECT 1 FROM remote_sessions current_session
          WHERE current_session.environment_snapshot_id = es.id
      )
    ORDER BY es.created_at ASC, es.id
    LIMIT ?
)`, cutoff.UnixMilli(), limit)
		if err != nil {
			return 0, fmt.Errorf("delete environment snapshots: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(count), nil
	})
	counts.environment = environmentDeleted
	if err != nil {
		return counts, err
	}
	return counts, nil
}
