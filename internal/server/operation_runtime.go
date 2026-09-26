package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/mcpresult"

	"mcpx/internal/envelope"
	"mcpx/internal/operation"
)

func executionMode(req *mcp.CallToolRequest) string {
	mode, _ := mcpresult.Arguments(req)["execution_mode"].(string)
	return strings.ToLower(strings.TrimSpace(mode))
}

func asyncEligibleTool(name string) bool {
	switch name {
	case "session", "operation_batch", "operation_manage", "exec_command", "write_stdin", "apply_patch":
		return false
	default:
		return true
	}
}

func (r *Runtime) submitAsyncTool(ctx context.Context, name string, req *mcp.CallToolRequest, envReq envelope.Request) (*mcp.CallToolResult, error) {
	if r.operations == nil {
		return r.terminalError(envReq, envReq.RemoteSessionID, envReq.Workspace, "operation_unavailable", "asynchronous operations are unavailable")
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return r.terminalError(envReq, "", envReq.Workspace, "unauthorized", "invalid or missing token")
	}
	remoteID := strings.TrimSpace(envReq.RemoteSessionID)
	if remoteID == "" {
		return r.terminalError(envReq, "", envReq.Workspace, "remote_session_required", "remote session id required for asynchronous execution")
	}
	session, err := r.remote.Get(ctx, principal, remoteID)
	if err != nil {
		return r.remoteError(envReq, remoteID, envReq.Workspace, err)
	}
	arguments := cloneArguments(mcpresult.Arguments(req))
	delete(arguments, "execution_mode")
	delete(arguments, "purpose") // The Operation owns intent; inject it only into child tools that accept it.
	record, err := r.operations.Submit(ctx, operation.SubmitSpec{
		RemoteSessionID: session.ID,
		WorkspaceName:   session.WorkspaceName,
		RequestID:       envReq.RequestID,
		Purpose:         envReq.Intent,
		Steps:           []operation.StepSpec{{ID: "main", Tool: name, Arguments: arguments, Exclusive: r.toolMeta[name].OpenWorld || !r.toolMeta[name].ReadOnly}},
	}, r.executeOperationStep)
	if err != nil {
		return r.terminalError(envReq, session.ID, session.WorkspaceName, "operation_submit_error", err.Error())
	}
	response := envelope.Accepted(envReq.RequestID, session.WorkspaceName, map[string]any{
		"operation_id": record.ID,
		"state":        record.State,
		"tool":         name,
	})
	response.RemoteSessionID = session.ID
	return r.resultJSON(response)
}

func (r *Runtime) executeOperationStep(ctx context.Context, input operation.ExecuteInput) operation.ExecuteResult {
	r.toolIndexMu.RLock()
	handler := r.toolHandlers[input.Tool]
	r.toolIndexMu.RUnlock()
	if handler == nil {
		return operation.ExecuteResult{Err: fmt.Errorf("tool %q is not registered", input.Tool)}
	}
	arguments := cloneArguments(input.Arguments)
	arguments["remote_session_id"] = input.RemoteSessionID
	if r.toolAcceptsArgument(input.Tool, "purpose") {
		arguments["purpose"] = input.Purpose
	}
	// The child context prevents recursive async submission. Read-only tools
	// must not receive an invented execution_mode field.
	delete(arguments, "execution_mode")
	request := mcpresult.Request(arguments)
	childCtx := r.operationChildContext(ctx, input)
	result, callErr := handler(childCtx, request)
	return operationResult(result, callErr)
}

func (r *Runtime) operationChildContext(ctx context.Context, input operation.ExecuteInput) context.Context {
	runtime, _ := runtimeContextFrom(ctx)
	runtime.RequestID = input.RequestID
	runtime.OperationID = input.OperationID
	runtime.ParentOperationID = input.OperationID
	runtime.StepID = input.StepID
	runtime.StartedAtMs = time.Now().UnixMilli()
	runtime.ReceivedAtMs = runtime.StartedAtMs
	return withOperationChild(withRuntimeContext(ctx, runtime))
}

func operationResult(result *mcp.CallToolResult, callErr error) operation.ExecuteResult {
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte(`{"available":false}`)
		callErr = errors.Join(callErr, err)
	}
	output := operation.ExecuteResult{Result: encoded, Err: callErr}
	if result == nil {
		if output.Err == nil {
			output.Err = errors.New("tool returned no result")
		}
		return output
	}
	status := publicResultStatus(result)
	if status == "" {
		status = operationResultStatus(result)
	}
	if token := resultConfirmationToken(result); token != "" {
		output.WaitingConfirmation = status == string(envelope.StatusNeedConfirmation)
		output.ConfirmationToken = token
	}
	if output.Err == nil && status == "failed" {
		output.Err = publicToolFailure(result)
	}
	return output
}

// publicToolFailure rebuilds a step error from the failed tool's response
// envelope so the machine-readable code (PROCESS_EXIT, COMMAND_NOT_FOUND, ...)
// and its message survive into the durable step error instead of collapsing
// into a generic failure string that forces an extra result query.
func publicToolFailure(result *mcp.CallToolResult) error {
	code, message := resultErrorBody(result)
	switch {
	case code != "" && message != "":
		return fmt.Errorf("%s: %s", code, message)
	case code != "":
		return fmt.Errorf("%s: public tool execution failed", code)
	case message != "":
		return errors.New(message)
	default:
		return errors.New("public tool execution failed")
	}
}

// resultErrorBody extracts the normalized envelope error body {code,message}
// from a failed tool result. It reads structuredContent first, then the JSON
// envelope carried in text content, mirroring findStatusValue's traversal.
func resultErrorBody(result *mcp.CallToolResult) (string, string) {
	if result == nil {
		return "", ""
	}
	if sc, ok := result.StructuredContent.(map[string]any); ok && sc != nil {
		if code, message := findErrorBody(sc, 0); code != "" || message != "" {
			return code, message
		}
	}
	for _, content := range result.Content {
		textContent, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(textContent.Text), &value) != nil {
			continue
		}
		if code, message := findErrorBody(value, 0); code != "" || message != "" {
			return code, message
		}
	}
	return "", ""
}

// findErrorBody locates the envelope's error object and returns its code and
// message. The wire shape is {status, data, meta, error:{code,message,...}},
// possibly nested under {mcpx:{result:{...}}}.
func findErrorBody(value any, depth int) (string, string) {
	if depth > 8 {
		return "", ""
	}
	switch typed := value.(type) {
	case map[string]any:
		if raw, exists := typed["error"]; exists {
			if body, ok := raw.(map[string]any); ok {
				code, _ := body["code"].(string)
				message, _ := body["message"].(string)
				if code != "" || message != "" {
					return code, message
				}
			}
		}
		for _, key := range []string{"mcpx", "result", "data"} {
			if nested, exists := typed[key]; exists {
				if code, message := findErrorBody(nested, depth+1); code != "" || message != "" {
					return code, message
				}
			}
		}
	case []any:
		for _, nested := range typed {
			if code, message := findErrorBody(nested, depth+1); code != "" || message != "" {
				return code, message
			}
		}
	}
	return "", ""
}

func operationResultStatus(result *mcp.CallToolResult) string {
	for _, content := range result.Content {
		textContent, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(textContent.Text), &value) == nil {
			if status := findStatusValue(value); status != "" {
				return status
			}
		}
	}
	return ""
}

func findStatusValue(value any) string {
	mapValue, ok := value.(map[string]any)
	if !ok {
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if status := findStatusValue(item); status != "" {
					return status
				}
			}
		}
		return ""
	}
	if status, ok := mapValue["status"].(string); ok {
		switch status {
		case "succeeded", "accepted", "waiting_confirmation", "interrupted", "failed", "ok":
			return status
		}
	}
	for _, key := range []string{"mcpx", "result", "data", "error"} {
		if nested, exists := mapValue[key]; exists {
			if status := findStatusValue(nested); status != "" {
				return status
			}
		}
	}
	return ""
}

func resultConfirmationToken(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		textContent, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(textContent.Text), &value) == nil {
			if token := findStringValue(value, "confirmation_token"); token != "" {
				return token
			}
		}
	}
	return ""
}

func findStringValue(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if text, ok := typed[key].(string); ok {
			return strings.TrimSpace(text)
		}
		for _, nested := range typed {
			if result := findStringValue(nested, key); result != "" {
				return result
			}
		}
	case []any:
		for _, nested := range typed {
			if result := findStringValue(nested, key); result != "" {
				return result
			}
		}
	}
	return ""
}

func cloneArguments(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+3)
	for key, value := range input {
		output[key] = value
	}
	return output
}
