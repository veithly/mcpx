package observation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxToolResultBytes bounds the durable replay copy independently from the
// human timeline snapshot. A larger result remains discoverable as truncated
// output through the ordinary tool response and task/artifact recovery paths.
const MaxToolResultBytes = 4 << 20

// PersistedToolResult is the durable, request-addressable copy of a completed
// MCP tool result. Result is the exact JSON wire value returned to the client.
type PersistedToolResult struct {
	RequestID       string
	Workspace       string
	RemoteSessionID string
	CallID          string
	OperationID     string
	StepID          string
	Tool            string
	Status          string
	Result          json.RawMessage
	Summary         string
	ExecutionTaskID string
	ExitCode        *int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveToolResult atomically replaces the result for one request ID. It is
// intentionally independent from the asynchronous observation queue: a lost
// client response must never race a best-effort timeline write.
func (s *Store) SaveToolResult(ctx context.Context, result PersistedToolResult) error {
	if s == nil || s.db == nil {
		return errors.New("observation store database is required")
	}
	result.RequestID = strings.TrimSpace(result.RequestID)
	result.Workspace = strings.TrimSpace(result.Workspace)
	result.Tool = strings.TrimSpace(result.Tool)
	result.Status = strings.TrimSpace(result.Status)
	if result.RequestID == "" || result.Workspace == "" || result.Tool == "" || result.Status == "" {
		return errors.New("request, workspace, tool and status are required")
	}
	if len(result.Result) == 0 || !json.Valid(result.Result) {
		return errors.New("tool result must be valid JSON")
	}
	if len(result.Result) > MaxToolResultBytes {
		return fmt.Errorf("tool result exceeds %d bytes", MaxToolResultBytes)
	}
	now := s.now().UTC()
	if result.CreatedAt.IsZero() {
		result.CreatedAt = now
	} else {
		result.CreatedAt = result.CreatedAt.UTC()
	}
	result.UpdatedAt = now
	var exitCode any
	if result.ExitCode != nil {
		exitCode = *result.ExitCode
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tool_results
		(request_id, workspace_name, remote_session_id, call_id, operation_id, step_id, tool_name, status, result_json, summary, execution_task_id, exit_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(request_id) DO UPDATE SET
		workspace_name=excluded.workspace_name, remote_session_id=excluded.remote_session_id,
		call_id=excluded.call_id, operation_id=excluded.operation_id, step_id=excluded.step_id,
		tool_name=excluded.tool_name, status=excluded.status, result_json=excluded.result_json,
		summary=excluded.summary, execution_task_id=excluded.execution_task_id, exit_code=excluded.exit_code,
		updated_at=excluded.updated_at`,
		result.RequestID, result.Workspace, result.RemoteSessionID, result.CallID, result.OperationID, result.StepID,
		result.Tool, result.Status, string(result.Result), result.Summary, result.ExecutionTaskID, exitCode,
		result.CreatedAt.UnixMilli(), result.UpdatedAt.UnixMilli())
	return err
}

// GetToolResult returns a completed result only within the authenticated
// workspace/session scope. It survives Runtime reconstruction and process
// restart because it is backed by the shared state database.
func (s *Store) GetToolResult(ctx context.Context, workspace, remoteSessionID, requestID string) (PersistedToolResult, error) {
	if s == nil || s.db == nil {
		return PersistedToolResult{}, errors.New("observation store database is required")
	}
	var result PersistedToolResult
	var raw string
	var exitCode sql.NullInt64
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT request_id, workspace_name, remote_session_id, call_id, operation_id, step_id,
		tool_name, status, result_json, summary, execution_task_id, exit_code, created_at, updated_at
		FROM tool_results WHERE request_id=? AND workspace_name=? AND (?='' OR remote_session_id=?)`,
		strings.TrimSpace(requestID), strings.TrimSpace(workspace), strings.TrimSpace(remoteSessionID), strings.TrimSpace(remoteSessionID)).Scan(
		&result.RequestID, &result.Workspace, &result.RemoteSessionID, &result.CallID, &result.OperationID, &result.StepID,
		&result.Tool, &result.Status, &raw, &result.Summary, &result.ExecutionTaskID, &exitCode, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PersistedToolResult{}, sql.ErrNoRows
	}
	if err != nil {
		return PersistedToolResult{}, err
	}
	result.Result = json.RawMessage(raw)
	if exitCode.Valid {
		value := int(exitCode.Int64)
		result.ExitCode = &value
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	result.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return result, nil
}
