package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/envelope"
	"mcpx/internal/observation"
	"mcpx/internal/state"
	"mcpx/internal/terminal"
)

func TestObservationTargetPreservesValuesWithoutCancellation(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	parent, cancel := context.WithTimeout(context.WithValue(context.Background(), key, "authorization"), time.Hour)
	cancel()

	bridge := &observationBridge{
		resolve: func(ctx context.Context, request envelope.Request) (string, string) {
			if value := ctx.Value(key); value != "authorization" {
				t.Fatalf("context value=%v, want authorization", value)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("resolution inherited cancellation: %v", err)
			}
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("resolution inherited request deadline")
			}
			return "demo", request.RemoteSessionID
		},
	}

	workspace, remoteID := bridge.target(parent, envelope.Request{RemoteSessionID: "session-1"})
	if workspace != "demo" || remoteID != "session-1" {
		t.Fatalf("target=(%q, %q), want (demo, session-1)", workspace, remoteID)
	}
}

func TestCollectTaskOutputBoundsCombinedTaskPayload(t *testing.T) {
	const limit = 128
	bridge := &observationBridge{maxTaskOutputBytes: limit}
	chunk := func(taskID, stream, value string, final bool) terminal.OutputChunk {
		return terminal.OutputChunk{
			TaskID:          taskID,
			RemoteSessionID: "session-1",
			Stream:          stream,
			Data:            []byte(value),
			Final:           final,
		}
	}

	var stored int64
	events := bridge.collectTaskOutputEvents(chunk("task-1", "stdout", "first output", false))
	if len(events) != 1 || events[0].Truncated {
		t.Fatalf("first output events=%d truncated=%v", len(events), events[0].Truncated)
	}
	stored += int64(len(events[0].Output))

	// A second chunk inside the coalesce window buffers instead of emitting.
	if events := bridge.collectTaskOutputEvents(chunk("task-1", "stderr", "second output", false)); len(events) != 0 {
		t.Fatalf("buffered chunk emitted %d events", len(events))
	}

	// Rewind the flush window so the next chunk flushes instead of buffering.
	state := bridge.outputBudget[observationTaskOutputKey{taskID: "task-1", remoteSessionID: "session-1"}]
	state.lastEmit = time.Time{}

	events = bridge.collectTaskOutputEvents(chunk("task-1", "stdout", strings.Repeat("x", 256), false))
	if len(events) != 1 || !events[0].Truncated {
		t.Fatalf("over-budget output events=%d truncated=%v", len(events), len(events) > 0 && events[0].Truncated)
	}
	stored += int64(len(events[0].Output))
	if stored > limit {
		t.Fatalf("stored payload bytes=%d, want <= %d", stored, limit)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Output, &payload); err != nil {
		t.Fatalf("truncated payload is invalid JSON: %v", err)
	}

	if events := bridge.collectTaskOutputEvents(chunk("task-1", "stdout", "discarded", false)); len(events) != 0 {
		t.Fatal("output after the first truncation was retained")
	}

	bridge.collectTaskOutputEvents(chunk("task-1", "stdout", "", true))
	bridge.collectTaskOutputEvents(chunk("task-1", "stderr", "", true))
	if len(bridge.outputBudget) != 0 {
		t.Fatalf("finished task output state remains: %+v", bridge.outputBudget)
	}
	if events := bridge.collectTaskOutputEvents(chunk("task-2", "stdout", "independent", false)); len(events) != 1 {
		t.Fatal("a different task inherited the first task's budget")
	}
}

func TestCollectTaskOutputCoalescesBursts(t *testing.T) {
	bridge := &observationBridge{maxTaskOutputBytes: 1 << 20}
	chunk := func(value string, final bool) terminal.OutputChunk {
		return terminal.OutputChunk{
			TaskID:          "task-1",
			RemoteSessionID: "session-1",
			WorkspaceName:   "demo",
			Tool:            "command_execute",
			Stream:          "stderr",
			Data:            []byte(value),
			Final:           final,
		}
	}

	first := bridge.collectTaskOutputEvents(chunk("one\n", false))
	if len(first) != 1 {
		t.Fatalf("first chunk must flush immediately, got %d events", len(first))
	}
	var firstPayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(first[0].Output, &firstPayload); err != nil || !strings.Contains(firstPayload.Text, "one\n") {
		t.Fatalf("first event text=%q err=%v", firstPayload.Text, err)
	}
	for index := 0; index < 50; index++ {
		if events := bridge.collectTaskOutputEvents(chunk("tick\n", false)); len(events) != 0 {
			t.Fatalf("chunk %d inside the coalesce window emitted %d events", index, len(events))
		}
	}
	events := bridge.collectTaskOutputEvents(chunk("", true))
	if len(events) != 1 {
		t.Fatalf("final flush events=%d, want 1", len(events))
	}
	bridge.collectTaskOutputEvents(terminal.OutputChunk{
		TaskID: "task-1", RemoteSessionID: "session-1", WorkspaceName: "demo", Stream: "stdout", Final: true,
	})
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(events[0].Output, &payload); err != nil {
		t.Fatalf("flush payload is invalid JSON: %v", err)
	}
	if strings.Count(payload.Text, "tick\n") != 50 {
		t.Fatalf("flushed text lost chunks: %q", payload.Text)
	}
	if len(bridge.outputBudget) != 0 || len(bridge.outputSanitizer) != 0 {
		t.Fatalf("finished task state remains: budget=%d sanitizer=%d", len(bridge.outputBudget), len(bridge.outputSanitizer))
	}
}

func TestCollectTaskOutputStopsAtEventLimit(t *testing.T) {
	bridge := &observationBridge{maxTaskOutputBytes: 1 << 20, maxTaskOutputEvents: 3}
	chunk := func(value string, final bool) terminal.OutputChunk {
		return terminal.OutputChunk{
			TaskID:          "task-1",
			RemoteSessionID: "session-1",
			WorkspaceName:   "demo",
			Stream:          "stderr",
			Data:            []byte(value),
			Final:           final,
		}
	}

	emitted := 0
	for index := 0; index < 100; index++ {
		// Oversize each chunk past the flush threshold so every call flushes.
		if events := bridge.collectTaskOutputEvents(chunk(strings.Repeat("x", observationTaskOutputFlushBytes+1), false)); len(events) > 0 {
			emitted += len(events)
		}
	}
	if emitted != 3 {
		t.Fatalf("emitted events=%d, want 3", emitted)
	}
	if events := bridge.collectTaskOutputEvents(chunk("tail", false)); len(events) != 0 {
		t.Fatal("output continued past the event limit")
	}
	bridge.collectTaskOutputEvents(terminal.OutputChunk{
		TaskID: "task-1", RemoteSessionID: "session-1", WorkspaceName: "demo", Stream: "stdout", Final: true,
	})
	bridge.collectTaskOutputEvents(chunk("", true))
	if len(bridge.outputBudget) != 0 {
		t.Fatalf("finished task output state remains: %+v", bridge.outputBudget)
	}
}

// TestRecordToolResultPersistsTruncatedOversizedResult pins the recovery
// guarantee: a result beyond MaxToolResultBytes must land in tool_results with
// a truncated flag instead of being rejected, because observe(request_ids=...)
// has no other recovery source for a disconnected client.
func TestRecordToolResultPersistsTruncatedOversizedResult(t *testing.T) {
	stateStore, err := state.Open(filepath.Join(t.TempDir(), "state", "mcpx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	bridge := &observationBridge{store: observation.NewStore(stateStore.DB())}

	text := strings.Repeat("x", observation.MaxToolResultBytes)
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	req := envelope.Request{RequestID: "req_bridge_oversize", Workspace: "demo"}
	if err := bridge.RecordToolResult(context.Background(), "execute", req, map[string]any{}, result, interactionTiming{}); err != nil {
		t.Fatalf("oversized result must persist truncated, not be rejected: %v", err)
	}

	stored, err := bridge.store.GetToolResult(context.Background(), "demo", "", "req_bridge_oversize")
	if err != nil {
		t.Fatalf("recovery row missing for oversized result: %v", err)
	}
	if !stored.Truncated {
		t.Fatal("truncated flag was not persisted")
	}
	if len(stored.Result) > observation.MaxToolResultBytes {
		t.Fatalf("stored result len=%d, want <= %d", len(stored.Result), observation.MaxToolResultBytes)
	}
	if !json.Valid(stored.Result) {
		t.Fatal("stored truncated result is not valid JSON")
	}
}
