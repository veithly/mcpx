package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/mcpresult"
)

type toolInputValidator func(*mcp.CallToolRequest) error

// Compile once from the same schema published in tools/list. No defaults,
// coercion or remote schema loading: invalid input must never become an action.
func toolValidator(tool mcp.Tool) toolInputValidator {
	var schema jsonschema.Schema
	if err := json.Unmarshal(mcpresult.ToolSchemaJSON(tool), &schema); err != nil {
		panic(fmt.Sprintf("tool %s schema: %v", tool.Name, err))
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s schema: %v", tool.Name, err))
	}
	diagnose := branchDiagnostics(mcpresult.ToolSchemaJSON(tool))
	return func(req *mcp.CallToolRequest) error {
		if req == nil || req.Params == nil {
			return errors.New("tool call parameters are required")
		}
		raw := req.Params.Arguments
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		if args == nil {
			return errors.New("arguments must be a JSON object, not null")
		}
		if err := resolved.Validate(args); err != nil {
			if strings.Contains(err.Error(), "oneOf") {
				return fmt.Errorf("%v; %s", err, diagnose(args))
			}
			return err
		}
		return nil
	}
}

func (r *Runtime) toolAcceptsArgument(name, field string) bool {
	r.toolIndexMu.RLock()
	tool, ok := r.toolIndex[name]
	r.toolIndexMu.RUnlock()
	if !ok {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(mcpresult.ToolSchemaJSON(tool), &schema) != nil {
		return false
	}
	_, ok = schema.Properties[field]
	return ok
}
