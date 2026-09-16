package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/logging"
	"mcpx/internal/observation"
	"mcpx/internal/operation"
	"mcpx/internal/remotesession"
	"mcpx/internal/terminal"
)

const observationSummaryMaxBytes = 8 << 10

// observationWriteTimeout bounds SQLite append only. It must stay above the
// store busy_timeout(1500ms) so the failure mode under lock contention is
// "waited, then failed" rather than an immediate deadline while SQLite was
// still willing to grant the lock. Observation is best-effort: fail soft.
const observationWriteTimeout = 5 * time.Second

type observationTaskStreamKey struct {
	taskID          string
	remoteSessionID string
	stream          string
}

type observationTaskOutputKey struct {
	taskID          string
	remoteSessionID string
}

type observationTaskOutputPending struct {
	text   strings.Builder
	bytes  int
	offset int64
}

type observationTaskOutputState struct {
	usedBytes    int64
	truncated    bool
	events       int
	lastEmit     time.Time
	finalStreams map[string]struct{}
	pending      map[string]*observationTaskOutputPending
}

const observationTaskOutputMarker = "[output truncated; read task logs for remaining output]"

// Task output events exist for live progress; the durable per-task log files
// remain the complete record. A chatty long-running process would otherwise
// write one observation row per pipe write (a 12-hour poll loop produced
// 50k events and hundreds of MB of SQLite), so raw chunks coalesce into one
// event per flush window and stop after a bounded event count per task.
const (
	observationTaskOutputFlushInterval = 2 * time.Second
	observationTaskOutputFlushBytes    = 16 << 10
	defaultTaskOutputEventLimit        = 300
)

// observationBridge is the single write boundary for the workspace observer.
// Store.Append always happens before Broker.Publish so a live observer can
// recover every event from SQLite after a disconnect or buffer overflow.
// Tool lifecycle and task output events are enqueued on async so tools/call and
// the command pipe copy path are not blocked by SQLite latency.
type observationBridge struct {
	store               *observation.Store
	broker              *observation.Broker
	async               *observation.AsyncRecorder
	resolve             func(context.Context, envelope.Request) (string, string)
	recordMu            sync.Mutex
	outputStateMu       sync.Mutex
	outputSanitizer     map[observationTaskStreamKey]*observation.TextStreamSanitizer
	outputBudget        map[observationTaskOutputKey]*observationTaskOutputState
	maxTaskOutputBytes  int64
	maxTaskOutputEvents int
}

func (b *observationBridge) Record(ctx context.Context, event observation.Event) error {
	if b == nil {
		return nil
	}
	event.Workspace = strings.TrimSpace(event.Workspace)
	event.CallID = strings.TrimSpace(event.CallID)
	event.Intent = observation.SanitizeIntent(event.Intent)
	event.Goal = observation.SanitizeIntent(event.Goal)
	event.Purpose = observation.SanitizeIntent(event.Purpose)
	event.ReasoningSummary = observation.SanitizeIntent(event.ReasoningSummary)
	event.Summary, _ = observation.SanitizeText(event.Summary, observationSummaryMaxBytes)
	event.ProgressSummary, _ = observation.SanitizeText(event.ProgressSummary, observationSummaryMaxBytes)
	event.NextStep = observation.SanitizeIntent(event.NextStep)
	event.PlanID = observation.SanitizeIntent(event.PlanID)
	event.PlanTaskID = observation.SanitizeIntent(event.PlanTaskID)
	event.ExecutionTaskID = observation.SanitizeIntent(event.ExecutionTaskID)
	if len(event.Input) > 0 {
		var truncated bool
		event.Input, truncated = observation.SanitizeJSON(event.Input, observation.MaxEventBytes)
		event.Truncated = event.Truncated || truncated
	}
	if len(event.Output) > 0 {
		var truncated bool
		outputLimit := observation.MaxEventBytes
		if event.Type == observation.TypeFileChanged {
			outputLimit = observation.MaxFileChangeEventBytes
		}
		event.Output, truncated = observation.SanitizeJSON(event.Output, outputLimit)
		event.Truncated = event.Truncated || truncated
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	if b.store == nil {
		return nil
	}
	// Keep append and publish in one critical section. Without this, two
	// direct producers could append sequence 1/2 and publish 2 before 1,
	// causing a socket client to discard the late sequence 1 as stale.
	b.recordMu.Lock()
	defer b.recordMu.Unlock()
	writeCtx, cancel := context.WithTimeout(observationContext(ctx), observationWriteTimeout)
	defer cancel()
	persisted, err := b.store.Append(writeCtx, event)
	if err != nil {
		logging.With("component", "workspace_observer").Error("persist event failed",
			"workspace", event.Workspace, "type", event.Type, "tool", event.Tool, "err", err)
		return nil
	}
	if b.broker != nil && persisted.Sequence > 0 {
		b.broker.Publish(persisted)
	}
	return nil
}

func (b *observationBridge) target(ctx context.Context, request envelope.Request) (string, string) {
	workspace := strings.TrimSpace(request.Workspace)
	remoteSessionID := remoteSessionID(request)
	if workspace == "" && b != nil && b.resolve != nil {
		// Preserve authentication values while detaching tools/call cancellation.
		workspace, remoteSessionID = b.resolve(observationContext(ctx), request)
	}
	return strings.TrimSpace(workspace), strings.TrimSpace(remoteSessionID)
}

func observationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func (b *observationBridge) enqueueOrRecord(ctx context.Context, event observation.Event) error {
	if b != nil && b.async != nil {
		b.async.Enqueue(event)
		return nil
	}
	return b.Record(ctx, event)
}

func observationCallID(req envelope.Request) string {
	return firstNonEmptyObservationID(req.CallID, req.RequestID)
}

func firstNonEmptyObservationID(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (b *observationBridge) RecordToolStarted(ctx context.Context, name string, req envelope.Request, args map[string]any) error {
	workspace, remoteID := b.target(ctx, req)
	input, truncated := observation.NormalizeToolInput(args, observation.MaxEventBytes)
	return b.enqueueOrRecord(ctx, observation.Event{
		Workspace:         workspace,
		RemoteSessionID:   remoteID,
		RequestID:         req.RequestID,
		CallID:            observationCallID(req),
		OperationID:       req.OperationID,
		ParentOperationID: req.ParentOperationID,
		StepID:            req.StepID,
		Tool:              name,
		Type:              observation.TypeToolStarted,
		Phase:             observation.PhaseActionStarted,
		Status:            "started",
		Purpose:           firstSemanticPurpose(req),
		Intent:            req.Intent,
		ReasoningSummary:  req.ReasoningSummary,
		ProgressSummary:   req.ProgressSummary,
		NextStep:          req.NextStep,
		PlanID:            req.PlanID,
		PlanTaskID:        req.PlanTaskID,
		ExecutionTaskID:   req.ExecutionTaskID,
		Input:             input,
		Truncated:         truncated,
	})
}

func (b *observationBridge) RecordToolCompleted(ctx context.Context, name string, req envelope.Request, args map[string]any, result *mcp.CallToolResult, callErr error, timing interactionTiming) error {
	workspace, remoteID := b.target(ctx, req)
	input, inputTruncated := observation.NormalizeToolInput(args, observation.MaxEventBytes)
	status := "succeeded"
	if callErr != nil || result == nil || result.IsError {
		status = "failed"
	} else if responseStatus := publicResultStatus(result); responseStatus != "" {
		status = responseStatus
	}
	facts := toolObservationFacts(name, args, result, timing)
	executionTaskID := firstNonEmptyObservationID(facts.ExecutionTaskID, req.ExecutionTaskID)
	summary := firstToolText(result)
	if summary == "" {
		summary = fmt.Sprintf("%s %s", name, status)
	}
	if callErr != nil {
		summary = observation.RedactText(callErr.Error())
	}
	snap := observation.HumanObsSnapshot{
		Tool:             name,
		Status:           status,
		Purpose:          firstSemanticPurpose(req),
		Intent:           req.Intent,
		ReasoningSummary: req.ReasoningSummary,
		ProgressSummary:  req.ProgressSummary,
		NextStep:         req.NextStep,
		PlanID:           req.PlanID,
		PlanTaskID:       req.PlanTaskID,
		ExecutionTaskID:  executionTaskID,
		Summary:          summary,
		Command:          facts.Command,
		WorkingDirectory: facts.WorkingDirectory,
		ExitCode:         facts.ExitCode,
		DurationMs:       facts.DurationMs,
		Path:             facts.Path,
		ResourceURI:      facts.ResourceURI,
		InputRedacted:    input,
		Truncated:        inputTruncated,
	}
	output, outTruncated := observation.NormalizeHumanToolOutput(snap, observation.MaxEventBytes)
	return b.enqueueOrRecord(ctx, observation.Event{
		Workspace:         workspace,
		RemoteSessionID:   remoteID,
		RequestID:         req.RequestID,
		CallID:            observationCallID(req),
		OperationID:       req.OperationID,
		ParentOperationID: req.ParentOperationID,
		StepID:            req.StepID,
		Tool:              name,
		Type:              observation.TypeToolCompleted,
		Status:            status,
		Purpose:           firstSemanticPurpose(req),
		Intent:            req.Intent,
		ReasoningSummary:  req.ReasoningSummary,
		ProgressSummary:   req.ProgressSummary,
		NextStep:          req.NextStep,
		PlanID:            req.PlanID,
		PlanTaskID:        req.PlanTaskID,
		ExecutionTaskID:   executionTaskID,
		Input:             input,
		Output:            output,
		Summary:           fmt.Sprintf("%s %s", name, status),
		Command:           facts.Command,
		WorkingDirectory:  facts.WorkingDirectory,
		ExitCode:          facts.ExitCode,
		DurationMs:        facts.DurationMs,
		SkillName:         facts.SkillName,
		MCPServer:         facts.MCPServer,
		MCPTool:           facts.MCPTool,
		Path:              facts.Path,
		ResourceURI:       facts.ResourceURI,
		Truncated:         inputTruncated || outTruncated,
	})
}

// RecordToolResult stores the exact normalized MCP result under request_id.
// Unlike timeline recording this is synchronous and detached from the caller,
// so a disconnected client cannot cause the recovery copy to be dropped.
func (b *observationBridge) RecordToolResult(ctx context.Context, name string, req envelope.Request, args map[string]any, result *mcp.CallToolResult, timing interactionTiming) error {
	if b == nil || b.store == nil || strings.TrimSpace(req.RequestID) == "" || result == nil {
		return nil
	}
	workspace, remoteID := b.target(ctx, req)
	if workspace == "" {
		// A result without a workspace cannot be safely scoped for later recovery.
		// It is still returned to the live caller and remains covered by ordinary
		// protocol/error logging; do not turn this into a noisy persistence error.
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal tool result: %w", err)
	}
	// Results beyond MaxToolResultBytes are truncated by SaveToolResult into a
	// valid-JSON preview with truncated=true instead of being rejected: this
	// row is the only request-addressable recovery copy after a disconnect.
	facts := toolObservationFacts(name, args, result, timing)
	status := "succeeded"
	if result.IsError {
		status = "failed"
	} else if value := publicResultStatus(result); value != "" {
		status = value
	}
	return b.store.SaveToolResult(ctx, observation.PersistedToolResult{
		RequestID: req.RequestID, Workspace: workspace, RemoteSessionID: remoteID,
		CallID: observationCallID(req), OperationID: req.OperationID, StepID: req.StepID,
		Tool: name, Status: status, Result: raw, Summary: firstToolText(result),
		ExecutionTaskID: facts.ExecutionTaskID, ExitCode: facts.ExitCode,
	})
}

func publicResultStatus(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	if sc, ok := result.StructuredContent.(map[string]any); ok {
		if status, _ := sc["status"].(string); status != "" {
			switch status {
			case string(envelope.StatusOK), string(envelope.StatusAccepted), string(envelope.StatusNeedConfirmation), string(envelope.StatusInterrupted):
				return status
			case string(envelope.StatusError): // wire value "failed"
				return "failed"
			}
		}
	}
	return operationResultStatus(result)
}

func firstToolText(result *mcp.CallToolResult) string {
	return mcpresult.FirstText(result)
}

func toolObservationFacts(name string, args map[string]any, result *mcp.CallToolResult, timing interactionTiming) observation.Event {
	facts := observation.Event{DurationMs: timing.ServerElapsedMs}
	if command, ok := args["command"].(string); ok {
		facts.Command = strings.TrimSpace(command)
	}
	if task, ok := args["task"].(string); ok && facts.Command == "" {
		facts.Command = strings.TrimSpace(task)
	}
	if path, ok := args["path"].(string); ok {
		facts.Path = strings.TrimSpace(path)
	}
	if name == "skill_tool" {
		facts.SkillName, _ = args["name"].(string)
	}
	if name == "mcp_tool" {
		facts.MCPServer, _ = args["server"].(string)
		facts.MCPTool, _ = args["tool"].(string)
	}
	// Pull a few human-useful fields from structured data only — never dump SC.
	data := businessDataFromResult(result)
	if workingDirectory, ok := data["working_directory"].(string); ok {
		facts.WorkingDirectory = workingDirectory
	}
	if executionTaskID, ok := data["execution_task_id"].(string); ok {
		facts.ExecutionTaskID = strings.TrimSpace(executionTaskID)
	}
	if command, ok := data["command"].(string); ok && facts.Command == "" {
		facts.Command = command
	}
	if duration, ok := numberValue(data["duration_ms"]); ok && duration >= 0 {
		facts.DurationMs = int64(duration)
	}
	if exitCode, ok := numberValue(data["exit_code"]); ok {
		code := int(exitCode)
		facts.ExitCode = &code
	}
	if path, ok := data["path"].(string); ok && facts.Path == "" {
		facts.Path = path
	}
	return facts
}

func businessDataFromResult(result *mcp.CallToolResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok || sc == nil {
		return map[string]any{}
	}
	if data, ok := sc["data"].(map[string]any); ok && data != nil {
		return data
	}
	return sc
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func (r *Runtime) observationTarget(ctx context.Context, req envelope.Request) (string, string) {
	workspace := strings.TrimSpace(req.Workspace)
	remoteID := remoteSessionID(req)
	if workspace != "" || remoteID == "" || r == nil || r.remote == nil {
		return workspace, remoteID
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return workspace, remoteID
	}
	session, err := r.remote.Get(ctx, principal, remoteID)
	if err != nil {
		return workspace, remoteID
	}
	return session.WorkspaceName, remoteID
}

func (r *Runtime) observeRemoteEvent(session remotesession.Session, event remotesession.Event) {
	if r == nil || r.observation == nil {
		return
	}
	payload := map[string]any{
		"source_type":     event.Type,
		"source_sequence": event.Sequence,
		"metadata":        event.Metadata,
	}
	encoded, truncated := marshalObservationValue(payload)
	eventType := observation.TypeObserverNotice
	if strings.HasPrefix(event.Type, "remote_session.") {
		eventType = observation.TypeSessionLifecycle
	}
	summary := event.Summary
	if summary == "" {
		summary = event.Type
	} else {
		summary = event.Type + ": " + summary
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:       session.WorkspaceName,
		RemoteSessionID: session.ID,
		OperationID:     event.OperationID,
		Type:            eventType,
		Status:          "succeeded",
		Output:          encoded,
		Summary:         summary,
		ResourceURI:     event.ResourceURI,
		Truncated:       truncated,
		CreatedAt:       event.CreatedAt,
	})
}

func (r *Runtime) observeOperationEvent(event operation.Event) {
	if r == nil || r.observation == nil {
		return
	}
	typeName := event.Type
	parentOperationID := ""
	if event.StepID != "" {
		parentOperationID = event.OperationID
	}
	status := string(event.State)
	if event.State == operation.StateQueued || event.State == operation.StateRunning {
		status = "running"
	}
	var lifecycle json.RawMessage
	if event.StateEventID != "" {
		lifecycle, _ = json.Marshal(map[string]any{"operation_id": event.OperationID,
			"run_id":         event.RunID,
			"state_event_id": event.StateEventID, "state_sequence": event.StateSequence, "state": event.State})
	}
	_ = r.observation.Record(context.Background(), observation.Event{
		Workspace:         event.WorkspaceName,
		RemoteSessionID:   event.RemoteSessionID,
		RequestID:         event.RequestID,
		CallID:            firstNonEmptyObservationID(event.RequestID),
		OperationID:       event.OperationID,
		ParentOperationID: parentOperationID,
		StepID:            event.StepID,
		Tool:              event.Tool,
		Type:              typeName,
		Status:            status,
		Summary:           event.Summary,
		Output:            lifecycle,
		CreatedAt:         event.CreatedAt,
	})
}

func (r *Runtime) observeTaskOutput(chunk terminal.OutputChunk) {
	if r == nil || r.observation == nil || (len(chunk.Data) == 0 && !chunk.Final) {
		return
	}
	for _, event := range r.observation.collectTaskOutputEvents(chunk) {
		_ = r.observation.enqueueOrRecord(context.Background(), event)
	}
}

// collectTaskOutputEvents buffers raw stream chunks per task and stream and
// returns the observation events to persist. It must be called once per chunk
// in stream order and returns at most one event per call.
func (b *observationBridge) collectTaskOutputEvents(chunk terminal.OutputChunk) []observation.Event {
	key := observationTaskOutputKey{taskID: chunk.TaskID, remoteSessionID: chunk.RemoteSessionID}
	b.outputStateMu.Lock()
	defer b.outputStateMu.Unlock()
	if b.outputBudget == nil {
		b.outputBudget = make(map[observationTaskOutputKey]*observationTaskOutputState)
	}
	state := b.outputBudget[key]
	if state == nil {
		state = &observationTaskOutputState{finalStreams: make(map[string]struct{}), pending: make(map[string]*observationTaskOutputPending)}
		b.outputBudget[key] = state
	}
	if chunk.Final {
		state.finalStreams[chunk.Stream] = struct{}{}
	}
	cleanup := func() {
		if len(state.finalStreams) >= 2 {
			delete(b.outputBudget, key)
		}
	}

	if chunk.Final {
		events := b.flushTaskOutputLocked(state, key, chunk, true)
		cleanup()
		return events
	}
	if state.truncated {
		cleanup()
		return nil
	}
	if state.events >= b.taskOutputEventLimit() {
		// Event budget exhausted. Mark truncation so the last emitted event
		// carries the flag and later chunks are dropped; task log files keep
		// the complete output for task_manage/observe logs.
		state.truncated = true
		cleanup()
		return nil
	}
	pending := state.pending[chunk.Stream]
	if pending == nil {
		pending = &observationTaskOutputPending{offset: chunk.Offset}
		state.pending[chunk.Stream] = pending
	}
	pending.text.Write(chunk.Data)
	pending.bytes += len(chunk.Data)
	if pending.bytes < observationTaskOutputFlushBytes && time.Since(state.lastEmit) < observationTaskOutputFlushInterval {
		return nil
	}
	return b.flushTaskOutputLocked(state, key, chunk, false)
}

// flushTaskOutputLocked sanitizes the buffered stream text and emits it as one
// observation event. Callers hold outputStateMu.
func (b *observationBridge) flushTaskOutputLocked(state *observationTaskOutputState, key observationTaskOutputKey, chunk terminal.OutputChunk, final bool) []observation.Event {
	pending := state.pending[chunk.Stream]
	delete(state.pending, chunk.Stream)
	text, byteCount, offset := "", 0, chunk.Offset
	if pending != nil {
		text, byteCount, offset = pending.text.String(), pending.bytes, pending.offset
	}
	sanitized, sanitizedTruncated := b.sanitizeTaskStreamLocked(observationTaskStreamKey{
		taskID: key.taskID, remoteSessionID: key.remoteSessionID, stream: chunk.Stream,
	}, text, final)
	if sanitized == "" {
		return nil
	}
	encoded, budgetTruncated, keep := b.budgetTaskOutputLocked(state, sanitized, byteCount)
	if !keep {
		return nil
	}
	state.events++
	state.lastEmit = time.Now()
	truncated := sanitizedTruncated || budgetTruncated
	if state.events >= b.taskOutputEventLimit() {
		state.truncated = true
		truncated = true
	}
	return []observation.Event{{
		Workspace:        chunk.WorkspaceName,
		RemoteSessionID:  chunk.RemoteSessionID,
		RequestID:        chunk.RequestID,
		CallID:           firstNonEmptyObservationID(chunk.CallID, chunk.RequestID),
		ExecutionTaskID:  chunk.TaskID,
		Tool:             chunk.Tool,
		Type:             observation.TypeCommandOutput,
		Phase:            observation.PhaseOutput,
		Status:           "running",
		Command:          chunk.Command,
		WorkingDirectory: chunk.WorkDir,
		Output:           encoded,
		Summary:          fmt.Sprintf("task %s %s output", chunk.TaskID, chunk.Stream),
		ResourceURI:      fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", chunk.RemoteSessionID, chunk.TaskID),
		Stream:           chunk.Stream,
		Offset:           offset,
		Truncated:        truncated,
	}}
}

// budgetTaskOutputLocked applies the cumulative byte budget for a task. It
// returns keep=false once the task exhausted its budget; state is marked
// truncated so later output is dropped instead of retried. Callers hold
// outputStateMu.
func (b *observationBridge) budgetTaskOutputLocked(state *observationTaskOutputState, sanitized string, byteCount int) (encoded []byte, truncated bool, keep bool) {
	if state.truncated {
		return nil, false, false
	}
	remaining := b.taskOutputLimit() - state.usedBytes
	if remaining <= 0 {
		state.truncated = true
		return nil, true, false
	}
	encoded = marshalTaskOutput(sanitized, byteCount)
	if int64(len(encoded)) <= remaining {
		state.usedBytes += int64(len(encoded))
		return encoded, false, true
	}
	encoded, _ = fitTaskOutputPayload(sanitized, byteCount, remaining)
	state.truncated = true
	if len(encoded) == 0 {
		return nil, true, false
	}
	state.usedBytes += int64(len(encoded))
	return encoded, true, true
}

func (b *observationBridge) taskOutputLimit() int64 {
	if b != nil && b.maxTaskOutputBytes > 0 {
		return b.maxTaskOutputBytes
	}
	return terminal.MaxPersistedTaskLogBytes
}

func (b *observationBridge) taskOutputEventLimit() int {
	if b != nil && b.maxTaskOutputEvents > 0 {
		return b.maxTaskOutputEvents
	}
	return defaultTaskOutputEventLimit
}

// sanitizeTaskStreamLocked runs the per-stream credential sanitizer. final
// must be true for the stream's final flush so incomplete state is released.
// Callers hold outputStateMu.
func (b *observationBridge) sanitizeTaskStreamLocked(key observationTaskStreamKey, text string, final bool) (string, bool) {
	if b.outputSanitizer == nil {
		b.outputSanitizer = make(map[observationTaskStreamKey]*observation.TextStreamSanitizer)
	}
	sanitizer := b.outputSanitizer[key]
	if sanitizer == nil {
		sanitizer = &observation.TextStreamSanitizer{}
		b.outputSanitizer[key] = sanitizer
	}
	sanitized, truncated := sanitizer.SanitizeChunk(text, final, observation.MaxEventBytes)
	if final {
		delete(b.outputSanitizer, key)
	}
	return sanitized, truncated
}

func marshalTaskOutput(text string, byteCount int) []byte {
	encoded, err := json.Marshal(map[string]any{"text": text, "bytes": byteCount})
	if err != nil {
		return []byte(`{"text":"[UNAVAILABLE]","bytes":0}`)
	}
	return encoded
}

func fitTaskOutputPayload(text string, byteCount int, maxBytes int64) ([]byte, bool) {
	full := marshalTaskOutput(text, byteCount)
	if int64(len(full)) <= maxBytes {
		return full, false
	}
	if maxBytes <= 0 {
		return nil, true
	}

	var best []byte
	low, high := 0, len(text)
	for low <= high {
		mid := low + (high-low)/2
		prefix := ""
		if mid > 0 {
			prefix, _ = TruncateUTF8(text, mid)
		}
		candidate := marshalTaskOutput(prefix, byteCount)
		if int64(len(candidate)) <= maxBytes {
			best = candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	if len(best) > 0 {
		return best, true
	}

	marker := marshalTaskOutput(observationTaskOutputMarker, 0)
	if int64(len(marker)) <= maxBytes {
		return marker, true
	}
	empty := marshalTaskOutput("", 0)
	if int64(len(empty)) <= maxBytes {
		return empty, true
	}
	return nil, true
}

func marshalObservationValue(value any) ([]byte, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"value":"[UNAVAILABLE]"}`), true
	}
	return observation.SanitizeJSON(encoded, observation.MaxEventBytes)
}
