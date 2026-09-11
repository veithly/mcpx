package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
	"mcpx/internal/security"
)

func TestObservationRecordsToolLifecycleAndRedacts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	request := mcpresult.Request(map[string]any{
		"intent":    "inspect the project configuration",
		"workspace": "demo",
		"token":     "do-not-store-this-token",
		"path":      "config.yaml",
	})

	wrapper := rt.instrumentTool("observer_test", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText(`{"message":"visible output","password":"do-not-store-this-password"}`), nil
	})
	if _, err := wrapper(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	var started, completed *observationEventView
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		started, completed = nil, nil
		for _, event := range events {
			if event.Tool != "observer_test" {
				continue
			}
			view := observationEventView{Type: event.Type, Sequence: event.Sequence, CallID: event.CallID, Phase: event.Phase, Intent: event.Intent, Input: string(event.Input), Output: string(event.Output)}
			switch event.Type {
			case "tool.started":
				started = &view
			case "tool.completed":
				completed = &view
			}
		}
		if started != nil && completed != nil && started.Sequence < completed.Sequence {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if started == nil || completed == nil || started.Sequence >= completed.Sequence {
		t.Fatalf("tool lifecycle order invalid: started=%+v completed=%+v", started, completed)
	}
	if started.Intent != "inspect the project configuration" {
		t.Fatalf("intent=%q", started.Intent)
	}
	if started.CallID == "" || started.CallID != completed.CallID || started.Phase != observation.PhaseActionStarted || completed.Phase != observation.PhaseResult {
		t.Fatalf("event correlation/phase invalid: started=%+v completed=%+v", started, completed)
	}
	if strings.Contains(started.Input, "do-not-store-this-token") || strings.Contains(completed.Output, "do-not-store-this-password") {
		t.Fatalf("sensitive tool data leaked: input=%s output=%s", started.Input, completed.Output)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(completed.Output), &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "succeeded" {
		t.Fatalf("completed status=%+v", output)
	}

	errorRequest := mcpresult.Request(map[string]any{"intent": "run the failing observer operation", "workspace": "demo"})

	errorWrapper := rt.instrumentTool("observer_error_test", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("observer operation failed")
	})
	failed, err := errorWrapper(context.Background(), errorRequest)
	if err != nil || failed == nil || !failed.IsError || !strings.Contains(mcpresult.FirstText(failed), "observer operation failed") {
		t.Fatalf("handler error must be an informative tool result: %+v %v", failed, err)
	}
	var errorOutput string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		errorOutput = ""
		for _, event := range events {
			if event.Tool == "observer_error_test" && event.Type == "tool.completed" {
				errorOutput = string(event.Output)
			}
		}
		if errorOutput != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var errorView map[string]any
	if err := json.Unmarshal([]byte(errorOutput), &errorView); err != nil {
		t.Fatal(err)
	}
	if errorView["status"] != "failed" || !strings.Contains(errorOutput, "observer operation failed") {
		t.Fatalf("error completion was not observable: %s", errorOutput)
	}
}

func TestObservationPersistsRequestAddressableToolResult(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	var requestID string
	wrapped := rt.instrumentTool("observer_result_test", func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		runtime, ok := runtimeContextFrom(ctx)
		if !ok {
			t.Fatal("handler did not receive runtime request identity")
		}
		requestID = runtime.RequestID
		return mcpresult.NewStructured(map[string]any{
			"status": "succeeded",
			"data":   map[string]any{"value": "persisted"},
		}, "persisted result"), nil
	})
	result, err := wrapped(context.Background(), mcpresult.Request(map[string]any{"workspace": "demo"}))
	if err != nil || result == nil || result.IsError || requestID == "" {
		t.Fatalf("tool call failed: result=%+v err=%v request_id=%q", result, err, requestID)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, lookupErr := rt.observation.store.GetToolResult(context.Background(), "demo", "", requestID)
		if lookupErr == nil {
			if stored.Tool != "observer_result_test" || stored.Status != "succeeded" || stored.Summary != "persisted result" {
				t.Fatalf("stored result metadata=%+v", stored)
			}
			var wire map[string]any
			if err := json.Unmarshal(stored.Result, &wire); err != nil {
				t.Fatalf("stored result is not valid MCP JSON: %v", err)
			}
			if wire["content"] == nil {
				t.Fatalf("stored result lost content: %+v", wire)
			}
			return
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			t.Fatalf("lookup persisted result: %v", lookupErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request-addressable result was not persisted for %q", requestID)
}

func TestWorkspaceHistoryReadReturnsPersistedToolResult(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	if err := rt.observation.store.SaveToolResult(context.Background(), observation.PersistedToolResult{
		RequestID: "req_recover", Workspace: "demo", RemoteSessionID: "", Tool: "read", Status: "succeeded",
		Result: json.RawMessage(`{"content":[{"type":"text","text":"recovered"}],"isError":false}`), Summary: "recovered",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := rt.toolWorkspaceHistoryRead(context.Background(), mcpresult.Request(map[string]any{
		"workspace": "demo", "request_ids": []any{"req_recover"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	wire := decodeToolResult(t, result)
	data, ok := wire["data"].(map[string]any)
	if !ok {
		t.Fatalf("history data=%+v", wire["data"])
	}
	items, ok := data["results"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("history results=%+v", data["results"])
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["request_id"] != "req_recover" || item["tool"] != "read" {
		t.Fatalf("recovered result=%+v", items[0])
	}
	recovered, ok := item["result"].(map[string]any)
	if !ok || recovered["content"] == nil {
		t.Fatalf("recovered wire result=%+v", item["result"])
	}
}
func TestObservationAggregatesRemoteSessionLifecycleByWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	for i := 0; i < 2; i++ {
		created, createErr := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
			WorkspaceName: "demo", WorkspacePath: registered.Path,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if created.Session.ID == "" {
			t.Fatal("remote session id missing")
		}
	}
	events, err := rt.observation.store.History(context.Background(), "demo", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if event.Type == "session.lifecycle" && strings.Contains(string(event.Output), `"source_type":"remote_session.created"`) {
			seen[event.RemoteSessionID] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("workspace history did not aggregate both sessions: %+v", events)
	}
}

func TestObservationRecordsCleanEditWithFileDiff(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent := "apply the observed file change"
	edited := callEnvelope(t, rt.toolEdit, context.Background(), map[string]any{
		"intent": intent, "remote_session_id": created.Session.ID, "purpose": intent,
		"edits": []any{map[string]any{"operation": "create", "path": "observed.txt", "content": "visible change\n"}},
	})
	if !statusOK(edited) {
		t.Fatalf("edit failed: %+v", edited)
	}
	editID, _ := edited["data"].(map[string]any)["edit_id"].(string)
	if editID == "" {
		t.Fatalf("edit id missing: %+v", edited)
	}
	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var changed *observationEventView
	for _, event := range events {
		if event.Type != observation.TypeFileChanged || event.Tool != "edit" {
			continue
		}
		if !strings.Contains(string(event.Output), editID) {
			continue
		}
		view := observationEventView{Intent: event.Intent, Output: string(event.Output)}
		changed = &view
	}
	if changed == nil || changed.Intent != intent {
		t.Fatalf("file change event missing: %+v", changed)
	}
	if !strings.Contains(changed.Output, "observed.txt") || !strings.Contains(changed.Output, "+visible change") {
		t.Fatalf("file change event lacks concrete diff: %s", changed.Output)
	}
}

func TestObservationRecordsRuntimeTaskOutput(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	const requestID = "req_runtime_output"
	const tool = "command_execute"
	command := "printf 'runtime-out token=do-not-store-this-token'; printf 'runtime-err password=do-not-store-this-password' >&2"
	task, err := rt.tasks.StartRemoteWithObservation(context.Background(), requestID, tool, created.Session.ID, "demo", registered.Path, command)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("runtime task did not exit")
	}
	expectedResourceURI := fmt.Sprintf("mcpx://remote-sessions/%s/tasks/%s/logs", created.Session.ID, task.ID)
	expectedStreams := map[string]string{
		"stdout": "runtime-out token=[REDACTED]",
		"stderr": "runtime-err password=[REDACTED]",
	}
	// Output persistence is async and the stderr text now flushes at the
	// task's final chunk, so poll briefly instead of racing the recorder.
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		streams := map[string]string{}
		offsets := map[string]int64{}
		for _, event := range events {
			if event.Type != "command.output" || event.ExecutionTaskID != task.ID {
				continue
			}
			if event.Workspace != "demo" || event.RemoteSessionID != created.Session.ID || event.RequestID != requestID || event.Tool != tool {
				t.Fatalf("runtime output identity=%+v", event)
			}
			if event.Stream != "stdout" && event.Stream != "stderr" {
				t.Fatalf("runtime output stream=%q", event.Stream)
			}
			if event.Offset != offsets[event.Stream] {
				t.Fatalf("runtime output offset=%d for %s, want %d", event.Offset, event.Stream, offsets[event.Stream])
			}
			if event.ResourceURI != expectedResourceURI {
				t.Fatalf("runtime output resource=%q, want %q", event.ResourceURI, expectedResourceURI)
			}
			var output map[string]any
			if err := json.Unmarshal(event.Output, &output); err != nil {
				t.Fatal(err)
			}
			text, ok := output["text"].(string)
			if !ok {
				t.Fatalf("runtime output text=%+v", output["text"])
			}
			if strings.Contains(string(event.Output), command) || strings.Contains(string(event.Output), "do-not-store-this-token") || strings.Contains(string(event.Output), "do-not-store-this-password") {
				t.Fatalf("runtime output leaked command or credential: %s", event.Output)
			}
			streams[event.Stream] += text
			bytesValue, ok := output["bytes"].(float64)
			if !ok {
				t.Fatalf("runtime output bytes=%+v", output["bytes"])
			}
			offsets[event.Stream] += int64(bytesValue)
		}
		if streams["stdout"] == expectedStreams["stdout"] && streams["stderr"] == expectedStreams["stderr"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime output events=%+v", streams)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestObservationRecordsCommandTaskRequestIdentity(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	principal, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := rt.reg.Get("demo")
	if !ok {
		t.Fatal("workspace was not registered")
	}
	created, err := rt.remote.Create(context.Background(), principal, remotesession.CreateInput{
		WorkspaceName: "demo", WorkspacePath: registered.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	envReq := envelope.Request{
		RequestID:       "req_command_observed",
		Intent:          "observe command output",
		RemoteSessionID: created.Session.ID,
		Workspace:       "demo",
		Payload:         map[string]any{},
	}
	analysis := security.CommandAnalysis{
		Decision: security.Allow,
		Segments: []security.CommandSegmentDecision{{Command: "printf 'command-out'", Decision: security.Allow}},
	}
	if _, err := rt.executeCommandTask(context.Background(), envReq, principal, created.Session, "printf 'command-out'", time.Second, "test", "workspace", "sha256:test", analysis); err != nil {
		t.Fatal(err)
	}

	events, err := rt.observation.store.History(context.Background(), "demo", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != observation.TypeCommandOutput {
			continue
		}
		var output map[string]any
		if err := json.Unmarshal(event.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output["text"] != "command-out" {
			continue
		}
		found = true
		if event.RequestID != envReq.RequestID || event.Tool != "command_execute" {
			t.Fatalf("command output identity=%+v, want request=%q tool=%q", event, envReq.RequestID, "command_execute")
		}
	}
	if !found {
		t.Fatal("command output event missing")
	}
}

func TestObservationSocketEndToEndDeliversToolEvents(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	socketPath := testObservationSocketPath(t)
	rt.observerSocket = observation.NewSocketServer(socketPath, rt.observation.store, rt.observation.broker, func(name string) bool {
		_, ok := rt.reg.Get(name)
		return ok
	})
	if err := rt.observerSocket.Start(); err != nil {
		t.Fatal(err)
	}
	defer rt.observerSocket.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frames := make(chan observation.Frame, 8)
	client := observation.NewClient(socketPath)
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx, observation.SubscribeRequest{Workspace: "demo", HistoryLimit: 20}, func(frame observation.Frame) error {
			frames <- frame
			if frame.Type == "event" && frame.Event != nil && frame.Event.Tool == "observer_e2e" && frame.Event.Type == observation.TypeToolCompleted {
				cancel()
			}
			return nil
		})
	}()
	select {
	case frame := <-frames:
		if frame.Type != "hello" {
			t.Fatalf("first observer frame=%+v", frame)
		}
	case <-ctx.Done():
		t.Fatal("observer did not connect")
	}
	wrapper := rt.instrumentTool("observer_e2e", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("e2e output"), nil
	})
	request := mcpresult.Request(map[string]any{"intent": "verify observer delivery", "workspace": "demo"})

	if _, err := wrapper(ctx, request); err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	eventCount := 0
	deadline := time.After(2 * time.Second)
	for len(events) < 2 {
		select {
		case frame := <-frames:
			if frame.Type == "event" && frame.Event != nil && frame.Event.Tool == "observer_e2e" {
				eventCount++
				if frame.Event.Sequence <= 0 {
					t.Fatalf("live event was not durable: %+v", frame.Event)
				}
				events[frame.Event.Type] = true
			}
		case <-deadline:
			t.Fatalf("observer events=%+v", events)
		}
	}
	if !events[observation.TypeToolStarted] || !events[observation.TypeToolCompleted] {
		t.Fatalf("missing tool lifecycle events=%+v", events)
	}
	if eventCount != 2 {
		t.Fatalf("durable-first bridge duplicated live events: count=%d", eventCount)
	}
	cancel()
	select {
	case err := <-clientErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer client did not stop after cancellation")
	}
}

type observationEventView struct {
	Type     string
	Sequence int64
	CallID   string
	Phase    string
	Intent   string
	Input    string
	Output   string
}
