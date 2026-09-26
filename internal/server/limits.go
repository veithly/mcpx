package server

import (
	"mcpx/internal/operation"
)

const MaxMoveOutTargets = 10000
const MaxMoveOutResponsePreviewTargets = 20

// publishedLimits is the single source for hard request limits exposed to
// models through runtime capabilities. Tool schemas repeat the limits where a
// JSON Schema validator can enforce them before invocation.
func publishedLimits() map[string]any {
	return map[string]any{
		"operation_batch": map[string]any{
			"max_steps": operation.MaxSteps,
		},
		"move_out": map[string]any{
			"max_targets":                  MaxMoveOutTargets,
			"max_response_preview_targets": MaxMoveOutResponsePreviewTargets,
		},
	}
}
