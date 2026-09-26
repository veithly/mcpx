package plan

import (
	"context"
	"database/sql"
	"fmt"
)

// These are server-persisted Codex outcomes. Caller metadata cannot turn a
// running, failed or unrelated tool invocation into completion evidence.
type toolOutcome struct {
	Tool     string
	Status   string
	ExitCode sql.NullInt64
}

func (outcome toolOutcome) validate(kind, requestID string) error {
	if outcome.Tool == "" {
		return evidenceLookupError(kind, requestID, sql.ErrNoRows)
	}
	matches := false
	switch kind {
	case EvidenceRead:
		matches = outcome.Tool == "exec_command"
	case EvidenceEdit:
		matches = outcome.Tool == "apply_patch"
	case EvidenceExecute, EvidenceVerification:
		matches = outcome.Tool == "exec_command" || outcome.Tool == "write_stdin"
	}
	if !matches {
		return fmt.Errorf("%w: %s request %s belongs to tool %s", ErrEvidence, kind, requestID, outcome.Tool)
	}
	if outcome.Status != "succeeded" || !outcome.ExitCode.Valid || outcome.ExitCode.Int64 != 0 {
		return fmt.Errorf("%w: %s request %s requires a terminal succeeded tool result with exit_code=0 (status=%s)", ErrEvidence, kind, requestID, outcome.Status)
	}
	return nil
}

func queryToolOutcomes(ctx context.Context, tx *sql.Tx, remoteSessionID string, ids []string) (map[string]toolOutcome, error) {
	result := make(map[string]toolOutcome)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, remoteSessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, `SELECT request_id, tool_name, status, exit_code FROM tool_results
		WHERE remote_session_id = ? AND request_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var outcome toolOutcome
		if err := rows.Scan(&id, &outcome.Tool, &outcome.Status, &outcome.ExitCode); err != nil {
			return nil, err
		}
		result[id] = outcome
	}
	return result, rows.Err()
}
