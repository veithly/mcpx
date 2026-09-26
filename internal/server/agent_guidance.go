package server

import (
	"strings"
	"sync"

	"mcpx/internal/envelope"
	"mcpx/internal/server/guidance"
)

const agentGuidanceVersion = "3.0"

// agentGuidanceConfig mirrors guidance.Config for existing call sites.
type agentGuidanceConfig = guidance.Config

var (
	defaultAgentGuidance     agentGuidanceConfig
	defaultAgentGuidanceOnce sync.Once
)

func loadDefaultAgentGuidance() agentGuidanceConfig {
	defaultAgentGuidanceOnce.Do(func() {
		defaultAgentGuidance = guidance.MustLoadAgent()
		if defaultAgentGuidance.Version != agentGuidanceVersion {
			// Keep code constant and YAML in lockstep during local edits.
			defaultAgentGuidance.Version = agentGuidanceVersion
		}
	})
	return defaultAgentGuidance
}

// agentGuidance is the compact engineering contract shown at bootstrap.
// Tool schemas, Runtime policy and structured recovery remain authoritative for
// protocol details; guidance only carries stable decision principles.
func agentGuidance() map[string]any {
	config := loadDefaultAgentGuidance()
	return map[string]any{
		"version":      config.Version,
		"priority":     config.Priority,
		"summary":      config.Summary,
		"rules":        config.Rules,
		"tool_routing": config.ToolRouting,
	}
}

func agentGuidanceRevision() string { return hashRevision(agentGuidance()) }

func agentGuidanceInstructions() string {
	config := loadDefaultAgentGuidance()
	lines := []string{"MCPX Agent 指引（高优先级）："}
	for _, rule := range config.Rules {
		lines = append(lines, "- "+rule)
	}
	return strings.Join(lines, "\n")
}

func nextActionWithReason(tool, reason string, arguments map[string]any) map[string]any {
	tool, arguments = normalizePublicAction(tool, arguments)
	return map[string]any{
		"tool":      tool,
		"reason":    reason,
		"arguments": argumentsOrEmpty(arguments),
	}
}

func addRecoveryAction(response *envelope.Response, tool, reason string, arguments map[string]any) {
	if response == nil || response.Error == nil || strings.TrimSpace(tool) == "" {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	tool, arguments = normalizePublicAction(tool, arguments)
	response.Error.Recovery = &envelope.Recovery{Action: tool, Tool: tool, Arguments: argumentsOrEmpty(arguments)}
	response.Error.Details["next_action"] = map[string]any{
		"tool":      tool,
		"reason":    reason,
		"arguments": argumentsOrEmpty(arguments),
	}
}

func normalizePublicAction(tool string, arguments map[string]any) (string, map[string]any) {
	// Callers supply the current public contract. In particular, an integer
	// write_stdin session_id is a process handle, never Remote Session routing.
	return tool, argumentsOrEmpty(arguments)
}

func isCleanPublicTool(tool string) bool {
	for _, definition := range toolCapabilityDefinitions {
		if definition.Name == tool {
			return true
		}
	}
	return false
}

func addRecoveryActions(response *envelope.Response, actions ...map[string]any) {
	if response == nil || response.Error == nil || len(actions) == 0 {
		return
	}
	if response.Error.Details == nil {
		response.Error.Details = map[string]any{}
	}
	response.Error.Details["next_actions"] = actions
	if len(actions) > 0 {
		if tool, _ := actions[0]["tool"].(string); tool != "" {
			arguments, _ := actions[0]["arguments"].(map[string]any)
			response.Error.Recovery = &envelope.Recovery{Action: tool, Tool: tool, Arguments: argumentsOrEmpty(arguments)}
		}
	}
}

func argumentsOrEmpty(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}
	return arguments
}
