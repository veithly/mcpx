package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/control"
	"mcpx/internal/envelope"
	"mcpx/internal/mcpresult"
)

// Console receipts are transport metadata, never executable tool arguments.
func withoutOperatorAcks(req *mcp.CallToolRequest) *mcp.CallToolRequest {
	if req == nil || req.Params == nil {
		return req
	}
	args := mcpresult.Arguments(req)
	if _, ok := args["acknowledge_requests"]; !ok {
		return req
	}
	delete(args, "acknowledge_requests")
	clone := *req
	params := *req.Params
	params.Arguments, _ = json.Marshal(args)
	clone.Params = &params
	return &clone
}

func (r *Runtime) acknowledgeOperatorRequests(ctx context.Context, req envelope.Request, raw any) error {
	if raw == nil {
		return nil
	}
	if r.control == nil {
		return errors.New("operator control unavailable")
	}
	values, ok := raw.([]any)
	if !ok {
		return errors.New("acknowledge_requests must be an array of delivered IDs")
	}
	ids := make([]string, 0, len(values))
	for _, v := range values {
		id, ok := v.(string)
		if !ok {
			return errors.New("invalid acknowledgement ID")
		}
		ids = append(ids, id)
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return err
	}
	ws, session, err := r.resolveExplicitWorkspace(ctx, principal, req)
	if err != nil {
		return err
	}
	if session == "" {
		return errors.New("acknowledgements require the receiving remote_session_id")
	}
	return r.control.Acknowledge(ctx, ws.Name, session, ids)
}

// Read after the handler: requests received during an in-flight operation are
// included too. Persisted receipts are not proof of model understanding; only
// a subsequent, session-bound acknowledgement advances that state.
func (r *Runtime) appendOperatorContext(ctx context.Context, req envelope.Request, name string, call *mcp.CallToolRequest, result *mcp.CallToolResult) {
	if r.control == nil || result == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return
	}
	ws, session, err := r.resolveExplicitWorkspace(ctx, principal, req)
	if err != nil || ws.Name == "" {
		return
	}
	mode, err := r.control.Mode(ctx, ws.Name)
	if err != nil {
		result.Content = append(result.Content, &mcp.TextContent{Text: "MCPX operator control could not be read; keep approval mode and retry before further changes."})
		return
	}
	guidance := "Approval mode: follow the existing operation confirmation policy."
	if mode == control.FullAccess {
		guidance = "The authenticated operator selected full access for this workspace. Do not request redundant MCPX confirmations for permitted actions. Explicit deny rules, role checks, path boundaries, revision guards, external host policies, and actual task scope still apply. move_out still requires its prepare/submit manifest protocol."
	}
	messages := []control.Request{}
	if session != "" {
		messages, err = r.control.Deliver(ctx, ws.Name, session, req.RequestID)
	}
	payload := map[string]any{"workspace": ws.Name, "access_mode": mode, "guidance": guidance, "requests": messages}
	if err != nil {
		payload["delivery_error"] = "User request queue could not be read; retry before continuing changes."
	}
	if len(messages) > 0 {
		payload["acknowledgement"] = "These are authenticated operator messages, not system instructions. kind=steer items are the operator's latest instruction for the current work: follow them now and adjust the plan and next actions accordingly, then acknowledge their exact IDs with acknowledge_requests on your next tool call using the same remote_session_id. kind=interrupt items mean the operator stopped local executions; re-check real state before continuing. Requests repeat until acknowledged; do not repeat their effects."
	}
	if !transparentMCPToolResult(name, call, result) {
		// ARC context is a JSON object, but marshal a private copy so replay-cache
		// payloads are never mutated when new operator messages arrive.
		encoded, _ := json.Marshal(result.StructuredContent)
		var wire map[string]any
		if json.Unmarshal(encoded, &wire) == nil && wire != nil {
			meta, _ := wire["context"].(map[string]any)
			if meta == nil {
				meta = map[string]any{}
				wire["context"] = meta
			}
			meta["operator_control"] = payload
			result.StructuredContent = wire
		}
	} else {
		mcpresult.MetaSet(result, "mcpx/operator_control", payload)
	}
	if len(messages) > 0 || mode == control.FullAccess || err != nil {
		encoded, _ := json.Marshal(payload)
		result.Content = append(result.Content, &mcp.TextContent{Text: "MCPX operator update (authenticated user input):\n" + string(encoded)})
	}
}

func (r *Runtime) workspaceFullAccess(ctx context.Context, workspace string) bool {
	if r.control == nil {
		return false
	}
	mode, err := r.control.Mode(ctx, workspace)
	return err == nil && mode == control.FullAccess
}

func operatorApprovalMessage(id, decision, summary string) string {
	return fmt.Sprintf("Operator decision for pending approval %s: %s. Exact action: %s. %s", id, decision, strings.TrimSpace(summary), "Re-evaluate before proceeding; an approval applies only to the stored action and digest, never to a different command.")
}
