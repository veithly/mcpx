package plan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func deliveryChecks(ctx context.Context, tx *sql.Tx, remoteSessionID string, item Plan, now time.Time) ([]DeliveryCheck, []string, error) {
	blockers := make([]string, 0)
	incomplete := make([]string, 0)
	blocked := make([]string, 0)
	for _, task := range item.Tasks {
		if task.Status != TaskCompleted && task.Status != TaskSkipped {
			incomplete = append(incomplete, task.ID+":"+task.Status)
		}
		if task.Status == TaskBlocked {
			blocked = append(blocked, task.ID)
		}
	}
	checks := []DeliveryCheck{{Code: "tasks_complete", Passed: len(incomplete) == 0, Message: "all plan tasks are completed or skipped"}}
	if len(incomplete) != 0 {
		checks[0].Details = map[string]any{"tasks": incomplete}
		blockers = append(blockers, "tasks_incomplete")
	}
	checks = append(checks, DeliveryCheck{Code: "no_blocked_tasks", Passed: len(blocked) == 0, Message: "no plan tasks are blocked"})
	if len(blocked) != 0 {
		checks[len(checks)-1].Details = map[string]any{"tasks": blocked}
		blockers = append(blockers, "tasks_blocked")
	}

	failedVerification := make([]string, 0)
	toolEvidence := make([]Evidence, 0)
	requestIDs := make([]string, 0)
	artifactIDs := make([]string, 0)
	for _, task := range item.Tasks {
		for _, evidence := range task.Evidence {
			if evidenceFailed(evidence) {
				failedVerification = append(failedVerification, evidence.ID)
			}
			switch evidence.Kind {
			case EvidenceRead, EvidenceEdit, EvidenceExecute, EvidenceVerification:
				toolEvidence = append(toolEvidence, evidence)
				requestIDs = append(requestIDs, evidence.ReferenceID)
			case EvidenceArtifact:
				artifactIDs = append(artifactIDs, evidence.ReferenceID)
			}
		}
	}
	checks = append(checks, DeliveryCheck{Code: "verification_passed", Passed: len(failedVerification) == 0, Message: "recorded verification evidence has no failure"})
	if len(failedVerification) != 0 {
		checks[len(checks)-1].Details = map[string]any{"evidence": failedVerification}
		blockers = append(blockers, "verification_failed")
	}

	outcomes, err := queryToolOutcomes(ctx, tx, remoteSessionID, requestIDs)
	if err != nil {
		return nil, nil, err
	}
	artifacts, err := queryExistingIDs(ctx, tx, "artifacts", remoteSessionID, artifactIDs)
	if err != nil {
		return nil, nil, err
	}
	evidenceBlockers := make([]string, 0)
	for _, evidence := range toolEvidence {
		if err := outcomes[evidence.ReferenceID].validate(evidence.Kind, evidence.ReferenceID); err != nil {
			evidenceBlockers = append(evidenceBlockers, "tool_result_invalid:"+evidence.ReferenceID)
		}
	}
	for _, id := range uniqueStrings(artifactIDs) {
		if !artifacts[id] {
			evidenceBlockers = append(evidenceBlockers, "artifact_missing:"+id)
		}
	}
	blockers = append(blockers, evidenceBlockers...)

	var pendingApprovals int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE remote_session_id = ? AND status = 'pending' AND expires_at > ?`, remoteSessionID, now.UnixMilli()).Scan(&pendingApprovals); err != nil {
		return nil, nil, err
	}
	checks = append(checks, DeliveryCheck{Code: "approvals_clear", Passed: pendingApprovals == 0, Message: "no pending approvals remain"})
	if pendingApprovals != 0 {
		checks[len(checks)-1].Details = map[string]any{"pending": pendingApprovals}
		blockers = append(blockers, "pending_approvals")
	}
	check := DeliveryCheck{Code: "execution_evidence_valid", Passed: len(evidenceBlockers) == 0, Message: "read, edit, execution and verification requests completed successfully; artifacts are available"}
	if !check.Passed {
		check.Message = "tool result or artifact evidence is missing, unsuccessful or belongs to another session"
		check.Details = map[string]any{"items": uniqueStrings(evidenceBlockers)}
	}
	checks = append(checks, check)
	return checks, uniqueStrings(blockers), nil
}

func evidenceFailed(evidence Evidence) bool {
	if strings.ToLower(evidence.Kind) != EvidenceVerification {
		return false
	}
	for _, key := range []string{"status", "result", "outcome"} {
		if value, ok := evidence.Metadata[key].(string); ok {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "failed" || value == "failure" || value == "error" {
				return true
			}
		}
	}
	if value, ok := evidence.Metadata["passed"].(bool); ok && !value {
		return true
	}
	if value, ok := evidence.Metadata["success"].(bool); ok && !value {
		return true
	}
	return false
}

func queryExistingIDs(ctx context.Context, tx *sql.Tx, table, remoteSessionID string, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	if table != "artifacts" {
		return nil, fmt.Errorf("unsupported existence table %s", table)
	}
	query := `SELECT id FROM artifacts WHERE remote_session_id = ? AND id IN (` + placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, remoteSessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
