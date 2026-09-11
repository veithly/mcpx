package observation

import (
	"encoding/json"
	"strconv"
	"time"
)

const (
	// MaxIntentBytes bounds purpose / progress_summary / intent from the model.
	// Keep generous so workspace observers see full model-authored notes.
	MaxIntentBytes = 8 << 10
	MaxEventBytes  = 64 << 10
	// MaxFileChangeEventBytes lets durable edit observations retain a useful
	// diff while ordinary tool/output events keep the smaller generic budget.
	// 1MiB is 16x the generic budget; multi-MiB diff rows were a real share of
	// state DB growth and full file content remains in file_snapshots.
	MaxFileChangeEventBytes = 1 << 20
	DefaultHistory          = 100
	// MaxObserverHistory bounds initial and reconnect replay for the human
	// terminal observer independently from broader history APIs.
	MaxObserverHistory = 100
	MaxHistory         = 200
	DefaultBuffer      = 128
)

const (
	TypeToolStarted            = "tool.started"
	TypeToolCompleted          = "tool.completed"
	TypeCommandOutput          = "command.output"
	TypeFileChanged            = "file.changed"
	TypeSessionLifecycle       = "session.lifecycle"
	TypeObserverNotice         = "observer.notice"
	TypeAgentActivity          = "agent.activity"
	TypeOperationStarted       = "operation.started"
	TypeOperationStepStarted   = "operation.step.started"
	TypeOperationStepCompleted = "operation.step.completed"
	TypeOperationCompleted     = "operation.completed"
)

const (
	// Event phases are transport-neutral lifecycle labels. thought_summary is
	// reserved for concise, model-authored progress—not hidden chain-of-thought.
	PhaseThoughtSummary = "thought_summary"
	PhaseActionStarted  = "action_started"
	PhaseOutput         = "output"
	PhaseResult         = "result"
	PhaseError          = "error"
)

// Event is the durable, sanitized timeline item consumed by workspace observers.
// Input and Output must already be normalized before they reach Store.Append.
type Event struct {
	Sequence          int64           `json:"sequence"`
	EventID           string          `json:"event_id,omitempty"`
	Workspace         string          `json:"workspace"`
	RemoteSessionID   string          `json:"remote_session_id,omitempty"`
	RequestID         string          `json:"request_id,omitempty"`
	CallID            string          `json:"call_id,omitempty"`
	TurnID            string          `json:"turn_id,omitempty"`
	ActivitySequence  int64           `json:"activity_sequence,omitempty"`
	ActivityKind      string          `json:"activity_kind,omitempty"`
	RelatedCallID     string          `json:"related_call_id,omitempty"`
	OperationID       string          `json:"operation_id,omitempty"`
	ParentOperationID string          `json:"parent_operation_id,omitempty"`
	StepID            string          `json:"step_id,omitempty"`
	Tool              string          `json:"tool,omitempty"`
	Type              string          `json:"type"`
	Phase             string          `json:"phase,omitempty"`
	Status            string          `json:"status,omitempty"`
	Goal              string          `json:"goal,omitempty"`
	Purpose           string          `json:"purpose,omitempty"`
	Intent            string          `json:"intent,omitempty"`
	ReasoningSummary  string          `json:"reasoning_summary,omitempty"`
	ProgressSummary   string          `json:"progress_summary,omitempty"`
	NextStep          string          `json:"next_step,omitempty"`
	PlanID            string          `json:"plan_id,omitempty"`
	PlanTaskID        string          `json:"plan_task_id,omitempty"`
	ExecutionTaskID   string          `json:"execution_task_id,omitempty"`
	Input             json.RawMessage `json:"input,omitempty"`
	Output            json.RawMessage `json:"output,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	Command           string          `json:"command,omitempty"`
	WorkingDirectory  string          `json:"working_directory,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	DurationMs        int64           `json:"duration_ms,omitempty"`
	SkillName         string          `json:"skill_name,omitempty"`
	MCPServer         string          `json:"mcp_server,omitempty"`
	MCPTool           string          `json:"mcp_tool,omitempty"`
	Path              string          `json:"path,omitempty"`
	ResourceURI       string          `json:"resource_uri,omitempty"`
	Stream            string          `json:"stream,omitempty"`
	Offset            int64           `json:"offset,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// EventFilter narrows a text observer without changing the durable event
// stream. Empty fields do not constrain matching.
type EventFilter struct {
	Tool        string
	Status      string
	OperationID string
	Path        string
}

func (e *Event) setEventID() {
	if e != nil && e.Sequence > 0 && e.EventID == "" {
		e.EventID = strconv.FormatInt(e.Sequence, 10)
	}
}

// SetDefaults fills correlation and lifecycle fields for legacy producers.
// It is called at the durable boundary so every JSONL/history consumer sees
// the same normalized contract even when an internal caller only sets Type.
func (e *Event) SetDefaults() {
	if e == nil {
		return
	}
	if e.CallID == "" {
		e.CallID = e.RequestID
	}
	if e.Phase == "" {
		e.Phase = PhaseForEvent(e.Type, e.Status)
	}
}

// PhaseForEvent maps existing event types and statuses to the shared phase
// vocabulary without requiring every internal producer to know the schema.
func PhaseForEvent(eventType, status string) string {
	switch eventType {
	case TypeToolStarted, TypeOperationStarted, TypeOperationStepStarted:
		return PhaseActionStarted
	case TypeCommandOutput:
		return PhaseOutput
	case TypeToolCompleted, TypeOperationCompleted, TypeOperationStepCompleted, TypeFileChanged, TypeSessionLifecycle, TypeObserverNotice, TypeAgentActivity:
		if isErrorStatus(status) {
			return PhaseError
		}
		return PhaseResult
	default:
		if isErrorStatus(status) {
			return PhaseError
		}
		switch status {
		case "started", "queued", "running", "accepted", "in_progress":
			return PhaseActionStarted
		default:
			return PhaseResult
		}
	}
}

func isErrorStatus(status string) bool {
	switch status {
	case "failed", "error", "cancelled", "canceled", "interrupted":
		return true
	default:
		return false
	}
}

// Gap identifies a sequence range that a live subscriber must recover from
// the durable Store before continuing with pushed events.
type Gap struct {
	FromSequence int64 `json:"from_sequence"`
	ToSequence   int64 `json:"to_sequence"`
}
