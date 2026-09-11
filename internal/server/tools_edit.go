package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/audit"
	"mcpx/internal/edit"
	"mcpx/internal/envelope"
	"mcpx/internal/idempotency"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

const cleanEditIdempotencyOperation = "edit"

type storedEditError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Path         string `json:"path,omitempty"`
	Index        int    `json:"index"`
	Current      string `json:"current_sha256,omitempty"`
	ChangedLines int    `json:"changed_lines,omitempty"`
}

type storedEditResult struct {
	EditID string           `json:"edit_id"`
	Result edit.BatchResult `json:"result"`
	Error  *storedEditError `json:"error,omitempty"`
}

func (s storedEditError) applyError() *edit.ApplyError {
	return &edit.ApplyError{Code: s.Code, Message: s.Message, Path: s.Path, Index: s.Index, Current: s.Current, ChangedLines: s.ChangedLines}
}

// editHeartbeatInterval refreshes the pending idempotency lease well inside
// the store's PendingLease window.
var editHeartbeatInterval = 10 * time.Second

// startEditHeartbeat keeps a pending idempotency record's lease fresh while a
// large batch is written, so a live owner is never treated as expired and
// taken over by a concurrent claim. It stops when stop is closed.
func (r *Runtime) startEditHeartbeat(ctx context.Context, key idempotency.Key, fingerprint string, stop <-chan struct{}) {
	if r == nil || r.idempotency == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(editHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = r.idempotency.Touch(ctx, key, fingerprint)
			case <-stop:
				return
			}
		}
	}()
}

// editInDoubtMetadata encodes the original commit failure and the
// written/unwritten boundary for the in-doubt response details.
func editInDoubtMetadata(err error, boundary *edit.BatchWriteError) []byte {
	details := map[string]any{
		"recovery":       "read current file SHA values before retrying",
		"original_error": err.Error(),
	}
	if boundary != nil {
		details["failed_path"] = boundary.FailedPath
		details["failed_index"] = boundary.FailedIndex
		details["applied_paths"] = boundary.AppliedPaths
		details["pending_paths"] = boundary.PendingPaths
	}
	encoded, _ := json.Marshal(details)
	return encoded
}

// toolEdit is the clean-core unified file edit entry.
func (r *Runtime) toolEdit(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, session, fail := r.changeRequest(ctx, req, true)
	if fail != nil {
		return fail, nil
	}

	edits, err := parseCleanEdits(envReq.Payload)
	if err != nil {
		return r.editToolError(envReq, session, err)
	}
	for index, item := range edits {
		if item.Operation == edit.OpDelete {
			return r.editToolError(envReq, session, &edit.ApplyError{
				Code:    "MOVE_OUT_REQUIRED",
				Message: "file removal requires move_out(action=prepare), user confirmation, then move_out(action=submit)",
				Path:    item.Path,
				Index:   index,
				Err:     edit.ErrUnsupportedOp,
			})
		}
	}
	effective := r.effectiveConfig(session.WorkspacePath)
	for _, item := range edits {
		for _, path := range []string{item.Path, item.NewPath} {
			if path != "" && security.MatchFile(effective.Security.Files, path) == security.Deny {
				response := envelope.Fail(envelope.StatusDenied, envReq.RequestID, session.WorkspaceName,
					nil, "FILE_DENIED", "file denied by policy")
				response.Error.Details["path"] = path
				response.RemoteSessionID = session.ID
				return r.resultJSON(response)
			}
		}
	}

	idempotencyKey := strings.TrimSpace(stringPayload(envReq.Payload, "idempotency_key"))
	fingerprint := cleanEditFingerprint(envReq, edits)
	apply := true
	if value, exists := envReq.Payload["apply"].(bool); exists {
		apply = value
	}
	if !apply {
		result, dryRunErr := edit.ApplyBatch(edit.BatchRequest{WorkspaceRoot: session.WorkspacePath, Edits: edits, DryRun: true})
		if dryRunErr != nil {
			return r.editToolError(envReq, session, dryRunErr)
		}
		data := editResponseData(session.ID, "", result, false)
		data["applied"] = false
		data["apply"] = false
		data["preview_only"] = true
		data["remote_session_id"] = session.ID
		return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
	}
	var idemKey idempotency.Key
	var claim idempotency.Claim
	claimed := idempotencyKey != "" && r.idempotency != nil
	if claimed {
		idemKey = idempotency.Key{RemoteSessionID: session.ID, PrincipalID: principal.ID, Operation: cleanEditIdempotencyOperation, Value: idempotencyKey}
		var claimErr error
		claim, claimErr = r.idempotency.Claim(ctx, idemKey, fingerprint, cleanEditRecordTTL)
		if claimErr != nil {
			return r.editToolError(envReq, session, claimErr)
		}
		switch claim.Kind {
		case idempotency.ClaimConflict:
			return r.editIdempotencyConflict(envReq, session, fingerprint, claim.Record.Fingerprint)
		case idempotency.ClaimReplay:
			return r.replayStoredEdit(envReq, session, claim.Record)
		case idempotency.ClaimInDoubt:
			if reconciled, _, handled := r.reconcilePendingEdit(ctx, envReq, session, principal.ID, claim, idemKey, fingerprint); handled {
				return reconciled, nil
			}
			// Reconcile proved the workspace still matches the original state
			// (the batch failed before any write landed): retake the key and
			// re-execute in place instead of dead-ending on in-doubt.
			retake, retakeErr := r.idempotency.ReclaimInDoubt(ctx, idemKey, fingerprint)
			if retakeErr != nil || retake.Kind != idempotency.ClaimOwner {
				return r.editIdempotencyInDoubt(envReq, session, claim.Record)
			}
			claim = retake
		case idempotency.ClaimWait:
			record, waitErr := r.idempotency.Wait(ctx, claim, idemKey)
			if waitErr != nil {
				return r.editIdempotencyPending(envReq, session, waitErr.Error())
			}
			return r.replayStoredEdit(envReq, session, record)
		case idempotency.ClaimPending:
			return r.editIdempotencyPending(envReq, session, "the same idempotency request is still running")
		}
		if claim.Kind == idempotency.ClaimOwner {
			if reconciled, _, handled := r.reconcilePendingEdit(ctx, envReq, session, principal.ID, claim, idemKey, fingerprint); handled {
				return reconciled, nil
			}
			// Keep the pending lease fresh while a large batch is written so a
			// live owner is never taken over as expired; Release is the safety
			// net that frees waiters even when the terminal store calls fail.
			defer claim.Release()
			stopHeartbeat := make(chan struct{})
			defer close(stopHeartbeat)
			r.startEditHeartbeat(ctx, idemKey, fingerprint, stopHeartbeat)
		}
	}

	editID := newRuntimeID("edit", 12)
	preparedPersisted := false
	var preparedResult edit.BatchResult
	result, err := edit.ApplyBatchWithHook(edit.BatchRequest{
		WorkspaceRoot: session.WorkspacePath,
		Edits:         edits,
	}, func(prepared edit.BatchResult) error {
		preparedResult = prepared
		stored := storedEditResult{EditID: editID, Result: prepared}
		if err := r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "pending", prepared); err != nil {
			return err
		}
		if claimed {
			encoded, encodeErr := json.Marshal(stored)
			if encodeErr != nil {
				return encodeErr
			}
			metadata, _ := json.Marshal(map[string]any{"edit_id": editID, "paths": editPaths(prepared)})
			if err := r.idempotency.UpdatePending(ctx, idemKey, fingerprint, encoded, metadata); err != nil {
				return err
			}
		}
		preparedPersisted = true
		return nil
	})
	if err != nil {
		if preparedPersisted {
			// The commit phase started, so the workspace may be partially
			// written. Preserve the original error and the written/unwritten
			// boundary in the in-doubt response instead of swallowing them.
			var boundary *edit.BatchWriteError
			_ = errors.As(err, &boundary)
			metadata := editInDoubtMetadata(err, boundary)
			_ = r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "in_doubt", preparedResult)
			if claimed {
				_ = r.idempotency.MarkInDoubt(ctx, idemKey, fingerprint, metadata)
			}
			r.logAudit(audit.Event{
				RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName,
				Tool: "edit", Status: "in_doubt",
				Detail: map[string]any{"edit_id": editID, "original_error": err.Error()},
			})
			return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{
				Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt, Metadata: metadata,
			})
		}
		if claimed {
			var validationErr *edit.ApplyError
			if errors.As(err, &validationErr) {
				// Deterministic validation failure before any filesystem
				// effect: persist the exact error so the same key replays it.
				stored := storedEditResult{Error: storedApplyError(err)}
				encoded, _ := json.Marshal(stored)
				_ = r.idempotency.Complete(ctx, idemKey, fingerprint, idempotency.StateFailed, encoded, nil)
			} else {
				// Transient hook/store failure before any effect: roll the
				// pending record back so the same key can retry from scratch.
				_ = r.idempotency.Abandon(ctx, idemKey, fingerprint)
			}
		}
		return r.editToolError(envReq, session, err)
	}
	if err := r.saveCleanEditRecord(ctx, session.ID, principal.ID, editID, "succeeded", result); err != nil {
		metadata := []byte(`{"recovery":"edit record persistence failed; inspect file SHA values"}`)
		if claimed {
			_ = r.idempotency.MarkInDoubt(ctx, idemKey, fingerprint, metadata)
		}
		return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{
			Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt, Metadata: metadata,
		})
	}
	if claimed {
		stored := storedEditResult{EditID: editID, Result: result}
		encoded, _ := json.Marshal(stored)
		metadata, _ := json.Marshal(map[string]any{"edit_id": editID, "paths": editPaths(result)})
		if err := r.idempotency.Complete(ctx, idemKey, fingerprint, idempotency.StateSucceeded, encoded, metadata); err != nil {
			return r.editIdempotencyInDoubt(envReq, session, idempotency.Record{Key: idemKey, Fingerprint: fingerprint, State: idempotency.StateInDoubt})
		}
	}
	files, lineNoun := "files", "lines"
	if len(result.Results) == 1 {
		files = "file"
	}
	if result.TotalChangedLines == 1 {
		lineNoun = "line"
	}
	_ = r.remote.AddEvent(ctx, principal, remotesession.Event{
		RemoteSessionID: session.ID,
		Type:            "edit.applied",
		Summary:         fmt.Sprintf("edit %d %s, %d changed %s", len(result.Results), files, result.TotalChangedLines, lineNoun),
	})
	r.observeCleanEdit(ctx, envReq, session, editID, result)
	r.logAudit(audit.Event{
		RequestID: envReq.RequestID, RemoteSessionID: session.ID, Workspace: session.WorkspaceName,
		Tool: "edit", Status: "ok",
		Detail: map[string]any{"files": len(result.Results), "deleted_files": 0, "total_changed_lines": result.TotalChangedLines},
	})
	return r.editToolSuccess(envReq, session, editID, result, false)
}

func (r *Runtime) editToolSuccess(envReq envelope.Request, session remotesession.Session, editID string, result edit.BatchResult, replay bool) (*mcp.CallToolResult, error) {
	data := editResponseData(session.ID, editID, result, replay)
	data["remote_session_id"] = session.ID
	return r.remoteResult(envReq, session.ID, session.WorkspaceName, data)
}

func (r *Runtime) editToolError(envReq envelope.Request, session remotesession.Session, err error) (*mcp.CallToolResult, error) {
	var ae *edit.ApplyError
	if errors.As(err, &ae) {
		details := map[string]any{}
		if ae.Path != "" {
			details["path"] = ae.Path
		}
		if ae.Index >= 0 {
			details["replacement_index"] = ae.Index
		}
		if ae.Current != "" {
			details["current_sha256"] = ae.Current
		}
		if ae.ChangedLines > 0 {
			details["total_changed_lines"] = ae.ChangedLines
		}
		code := ae.Code
		if code == "" {
			code = "EDIT_FAILED"
		}
		msg := ae.Error()
		details["recovery"] = editRecovery(code, ae)
		suggestedNext := editSuggestedNext(code, session.ID, ae)
		if suggestedNext != nil {
			details["suggested_next"] = suggestedNext
		}
		response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, code, msg)
		for key, value := range details {
			response.Error.Details[key] = value
		}
		if suggestedNext != nil {
			tool, _ := suggestedNext["tool"].(string)
			arguments, _ := suggestedNext["arguments"].(map[string]any)
			response.Error.Recovery = &envelope.Recovery{Action: tool, Tool: tool, Arguments: argumentsOrEmpty(arguments)}
		}
		response.RemoteSessionID = session.ID
		return r.resultJSON(response)
	}
	response := envelope.Fail(envelope.StatusError, envReq.RequestID, session.WorkspaceName, nil, "EDIT_FAILED", err.Error())
	response.RemoteSessionID = session.ID
	return r.resultJSON(response)
}

func editRecovery(code string, ae *edit.ApplyError) any {
	switch code {
	case "STALE_REVISION":
		return "re-read the file to get the latest sha256, update base_sha256, and retry with a new idempotency_key; the old key remains bound to the stale request"
	case "MATCH_NOT_FOUND", "MATCH_AMBIGUOUS":
		return "re-read the file and use a longer unique match snippet, or use a revision-guarded line range when the target lines are known"
	case "RANGE_OUT_OF_BOUNDS":
		return "re-read the file to refresh its current line layout and sha256, then retry the range against that revision"
	case "TOO_MANY_CHANGES":
		return map[string]any{
			"action":            "split_edit",
			"max_changed_lines": edit.MaxChangedLines,
			"message":           fmt.Sprintf("split the edit into smaller batches; max total changed lines is %d", edit.MaxChangedLines),
		}
	case "SYMLINK_NOT_ALLOWED", "DELETE_FILE_ONLY":
		return "move out only an explicit regular file, directory or symlink entry; do not follow symlink paths"
	case "MOVE_OUT_REQUIRED":
		return "use move_out(action=prepare), ask the web user to confirm the frozen manifest, then move_out(action=submit) with confirmation_uuid; edit never removes files"
	case "FILE_DENIED", "POLICY_DENIED":
		return "adjust the path or obtain policy approval"
	default:
		if ae != nil && ae.Message != "" {
			return ae.Message
		}
		return "inspect error details and retry with corrected parameters"
	}
}

func editSuggestedNext(code, remoteSessionID string, ae *edit.ApplyError) map[string]any {
	if ae == nil || strings.TrimSpace(ae.Path) == "" {
		return nil
	}
	refreshWindow := func(reason string) map[string]any {
		return nextActionWithReason("read", reason, map[string]any{
			"remote_session_id": remoteSessionID,
			"view":              "file",
			"path":              ae.Path,
			"mode":              "window",
			"offset":            0,
			"limit":             1,
		})
	}
	switch code {
	case "STALE_REVISION":
		return refreshWindow("refresh the current file sha256 before regenerating the edit")
	case "MATCH_NOT_FOUND", "MATCH_AMBIGUOUS", "RANGE_OUT_OF_BOUNDS":
		return nextActionWithReason("read", "refresh the current file content before regenerating the update", map[string]any{
			"remote_session_id": remoteSessionID,
			"view":              "file",
			"path":              ae.Path,
		})
	case "MOVE_OUT_REQUIRED":
		return refreshWindow("read the current file sha256 before preparing move_out")
	default:
		return nil
	}
}

func (r *Runtime) observeCleanEdit(ctx context.Context, envReq envelope.Request, session remotesession.Session, editID string, result edit.BatchResult) {
	if r == nil || r.observation == nil {
		return
	}
	paths := make([]string, 0, len(result.Results))
	for _, item := range result.Results {
		paths = append(paths, item.Path)
	}
	observationResults := make([]map[string]any, 0, len(result.Results))
	for _, item := range result.Results {
		file := map[string]any{
			"path":            item.Path,
			"new_path":        item.NewPath,
			"operation":       item.Operation,
			"original_sha256": item.OriginalSHA256,
			"new_sha256":      item.NewSHA256,
			"changed_lines":   item.ChangedLines,
			"deleted":         item.Deleted,
			"diff_bytes":      len(item.Diff),
		}
		if item.Diff != "" {
			file["diff"] = item.Diff
		}
		observationResults = append(observationResults, file)
	}
	payload, _ := json.Marshal(map[string]any{
		"edit_id":             editID,
		"diff_summary":        boundedDiffPreview(result.DiffSummary, cleanDiffTotalPreviewMaxBytes).Text,
		"diff_bytes":          len(result.DiffSummary),
		"diff_truncated":      len(result.DiffSummary) > cleanDiffTotalPreviewMaxBytes,
		"total_changed_lines": result.TotalChangedLines,
		"paths":               paths,
		"results":             observationResults,
	})
	summary := fmt.Sprintf("edit %d file(s), %d changed lines", len(result.Results), result.TotalChangedLines)
	_ = r.observation.Record(ctx, observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		RequestID:       envReq.RequestID,
		CallID:          observationCallID(envReq),
		Tool:            "edit",
		Type:            observation.TypeFileChanged,
		Status:          "succeeded",
		Purpose:         envReq.Intent,
		Intent:          envReq.Intent,
		Output:          payload,
		Summary:         summary,
		Path:            strings.Join(paths, ","),
	})
}

func parseCleanEdits(payload map[string]any) ([]edit.FileEdit, error) {
	if payload == nil {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
	}
	raw, ok := payload["edits"]
	if !ok || raw == nil {
		// Single-file shorthand: top-level path + replacements/content.
		if path, _ := payload["path"].(string); strings.TrimSpace(path) != "" {
			raw = []any{payload}
		} else {
			return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var edits []edit.FileEdit
	if err := json.Unmarshal(encoded, &edits); err != nil {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "invalid edits payload", Index: -1, Err: edit.ErrInvalidInput}
	}
	if len(edits) == 0 {
		return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: "edits required", Index: -1, Err: edit.ErrInvalidInput}
	}
	for i := range edits {
		if strings.TrimSpace(edits[i].Operation) == "" {
			return nil, &edit.ApplyError{Code: "INVALID_INPUT", Message: fmt.Sprintf("edits[%d].operation required", i), Index: i, Err: edit.ErrInvalidInput}
		}
	}
	return edits, nil
}
