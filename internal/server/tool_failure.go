package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
	"mcpx/internal/observation"
)

// Tool failures are results, not JSON-RPC errors. In particular the raw SDK
// discards a result returned alongside a non-nil error. Never return both.
func normalizeToolOutcome(ctx context.Context, name string, req *mcp.CallToolRequest, result *mcp.CallToolResult, callErr error) *mcp.CallToolResult {
	if result != nil {
		if _, err := marshalToolResult(result); err != nil {
			return toolFailure(ctx, name, req, "RESULT_ENCODING_FAILED", "The tool produced a result that could not be serialized. Its effects may already have occurred.", nil)
		}
	}
	if callErr != nil {
		code := "TOOL_EXECUTION_FAILED"
		switch {
		case errors.Is(callErr, context.DeadlineExceeded):
			code = "TOOL_TIMEOUT"
		case errors.Is(callErr, context.Canceled):
			code = "TOOL_CANCELLED"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			// A non-wrapped timeout (fmt.Errorf without %w) must not degrade to
			// TOOL_EXECUTION_FAILED; the execution context state classifies it.
			code = "TOOL_TIMEOUT"
		case errors.Is(ctx.Err(), context.Canceled):
			code = "TOOL_CANCELLED"
		}
		return toolFailure(ctx, name, req, code, callErr.Error(), result)
	}
	if result == nil {
		return toolFailure(ctx, name, req, "TOOL_EMPTY_RESULT", "The tool handler returned no result; execution outcome is unknown.", nil)
	}
	// Upstream business payloads are intentionally transparent. Their IsError
	// flag and content belong to the upstream server, not our ARC envelope.
	if transparentMCPToolResult(name, req, result) {
		return result
	}
	wire, _ := result.StructuredContent.(map[string]any)
	if wire["status"] == "failed" {
		result.IsError = true
	}
	if result.IsError {
		if wire["error"] == nil {
			text := strings.TrimSpace(mcpresult.FirstText(result))
			code := "TOOL_EXECUTION_FAILED"
			if strings.HasPrefix(text, "EXECUTION_RUNTIME_ERROR:") {
				code = "EXECUTION_RUNTIME_ERROR"
			}
			if text == "" {
				text = "The tool reported failure without diagnostic text."
			}
			return toolFailure(ctx, name, req, code, text, result)
		}
		wire["status"] = "failed"
	}
	return result
}

// This check runs before ARC recursively inspects a payload and again before
// sending it. A marshal error otherwise leaves some SDK clients awaiting a
// response forever. Custom marshalers are treated as untrusted tool output.
func marshalToolResult(result *mcp.CallToolResult) (raw []byte, err error) {
	defer func() {
		if recover() != nil {
			raw = nil
			err = errors.New("tool result marshaler panicked")
		}
	}()
	return json.Marshal(result)
}

func toolFailure(ctx context.Context, name string, req *mcp.CallToolRequest, code, message string, partial *mcp.CallToolResult) *mcp.CallToolResult {
	args := mcpresult.Arguments(req)
	session, _ := args["remote_session_id"].(string)
	workspace, _ := args["workspace"].(string)
	runtime, _ := runtimeContextFrom(ctx)
	message, _ = observation.SanitizeText(message, 8192)
	data := map[string]any{}
	if partial != nil {
		if wire, ok := partial.StructuredContent.(map[string]any); ok {
			if inner, ok := wire["data"].(map[string]any); ok {
				for k, v := range inner {
					data[k] = v
				}
			}
		}
		if text := mcpresult.FirstText(partial); strings.TrimSpace(text) != "" {
			data["partial_output"], _ = observation.SanitizeText(text, 16384)
		}
	}
	response := envelope.Fail(envelope.StatusError, runtime.RequestID, workspace, data, code, message)
	response.RemoteSessionID = session
	response.Error.Details["tool"] = name
	response.Error.Details["execution_state"] = "unknown"
	response.Error.Details["safe_to_retry_unchanged"] = false
	if response.Error.Details["retry_hint"] == "" {
		response.Error.Details["retry_hint"] = "Inspect affected state or Task logs before retrying or choosing another command; do not blindly replay side effects."
	}
	switch code {
	case "INVALID_ARGUMENTS":
		response.Error.Details["execution_state"] = "not_started"
		response.Error.Details["retry_hint"] = "Correct the named arguments against this tool's current input schema, then retry. The tool body was not executed; this is not a MCPX outage."
	case "TOOL_BUSY":
		response.Error.Details["execution_state"] = "not_started"
		response.Error.Details["retry_hint"] = "Too many tool calls are still in flight. Inspect their status and retry with bounded backoff; do not submit parallel duplicates."
	}
	response.Error.Details["request_id"] = runtime.RequestID
	response.Error.Details["remote_session_id"] = session
	if session != "" && code != "INVALID_ARGUMENTS" && code != "TOOL_BUSY" {
		query := map[string]any{"remote_session_id": session, "view": "history"}
		if runtime.RequestID != "" {
			query["request_ids"] = []string{runtime.RequestID}
		}
		addRecoveryAction(&response, "observe", "inspect this call's recorded outcome before retrying", query)
	}
	encoded, _ := envelope.Marshal(response)
	wire := map[string]any{}
	_ = json.Unmarshal(encoded, &wire)
	wire["type"] = "error"
	wire["context"] = map[string]any{"operation_id": runtime.OperationID}
	raw := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}, StructuredContent: wire}
	return raw // The instrumentation/final guard adds ARC exactly once.
}

// Final guard also covers early validation/admission returns and renderer
// failures. Recovery stays in content as well as structuredContent because not
// every host forwards metadata to its model.
func ensureToolResponse(ctx context.Context, name string, req *mcp.CallToolRequest, result *mcp.CallToolResult, callErr error) (out *mcp.CallToolResult) {
	defer func() {
		if recover() != nil {
			out = emergencyToolFailure()
		}
	}()
	result = normalizeToolOutcome(ctx, name, req, result, callErr)
	if transparentMCPToolResult(name, req, result) {
		if result.IsError {
			hint := "Upstream tool reported a business error. Inspect its diagnostic and verify any partial effects before retrying or changing the request; this alone does not mean MCPX is unavailable."
			if strings.TrimSpace(mcpresult.FirstText(result)) == "" {
				hint = "Upstream tool returned isError=true without diagnostic text. " + hint
			}
			already := false
			for _, part := range result.Content {
				if text, ok := part.(*mcp.TextContent); ok && text != nil && strings.HasPrefix(text.Text, "下一步：Upstream tool") {
					already = true
				}
			}
			if !already {
				result.Content = append(result.Content, &mcp.TextContent{Text: "下一步：" + hint})
			}
		}
		return result
	}
	wire, _ := result.StructuredContent.(map[string]any)
	if result.IsError && wire["timing"] == nil {
		result = wrapFailureResult(ctx, name, result)
		wire, _ = result.StructuredContent.(map[string]any)
	}
	if result.IsError {
		body, _ := wire["error"].(map[string]any)
		if body != nil {
			details, _ := body["details"].(map[string]any)
			if details == nil {
				details = map[string]any{}
				body["details"] = details
			}
			category, _ := body["category"].(string)
			if _, ok := details["origin"]; !ok {
				origin := "mcpx"
				switch category {
				case "validation":
					origin = "caller"
				case "execution":
					origin = "command"
				case "upstream":
					origin = "upstream"
				case "permission":
					origin = "policy"
				case "not_found", "conflict":
					origin = "target"
				}
				details["origin"] = origin
			}
			if _, ok := details["safe_to_retry_unchanged"]; !ok {
				details["safe_to_retry_unchanged"] = false
			}
			hint, _ := details["retry_hint"].(string)
			if hint == "" {
				hint = "Inspect the error and any partial effects, correct the cause, then retry safely or choose another command."
				details["retry_hint"] = hint
			}
			text := mcpresult.FirstText(result)
			code, _ := body["code"].(string)
			message, _ := body["message"].(string)
			if !strings.Contains(text, code) || strings.TrimSpace(text) == "" {
				text = fmt.Sprintf("%s: %s\n\n%s", code, message, text)
			}
			if !strings.Contains(text, "下一步：") {
				text += "\n\n下一步：" + hint
			}
			if category == "execution" && !strings.Contains(text, "不是 MCPX 服务崩溃") {
				text += "\n这是命令/任务的失败结果，不是 MCPX 服务崩溃的证据。"
			}
			if inner, ok := wire["data"].(map[string]any); ok {
				if partial, ok := inner["partial_output"].(string); ok && !strings.Contains(text, partial) {
					text += "\n\n部分输出：\n" + partial
				}
			}
			if len(result.Content) > 0 {
				if first, ok := result.Content[0].(*mcp.TextContent); ok && first != nil {
					first.Text = text
				} else {
					result.Content = append([]mcp.Content{&mcp.TextContent{Text: text}}, result.Content...)
				}
			} else {
				result.Content = []mcp.Content{&mcp.TextContent{Text: text}}
			}
		}
	}
	if len(result.Content) == 0 {
		result.Content = []mcp.Content{&mcp.TextContent{Text: name + " completed without textual output."}}
	}
	return result
}
