package observation

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HistoryQuery is the typed filter set exposed by workspace_history_read.
// Scalar filters are ANDed; values inside one slice are ORed.
type HistoryQuery struct {
	AfterSequence    int64
	Ascending        bool
	Workspace        string
	SessionID        string
	CallID           string
	EventIDs         []string
	RequestIDs       []string
	OperationIDs     []string
	PlanTaskIDs      []string
	ExecutionTaskIDs []string
	CreatedAfter     time.Time
	CreatedBefore    time.Time
	Keyword          string
	Kinds            []string
	Statuses         []string
	Limit            int
	Cursor           string
}

// Query returns newest-first events and a sequence cursor for the next page.
func (s *Store) Query(ctx context.Context, query HistoryQuery) ([]Event, string, error) {
	if s == nil || s.db == nil {
		return nil, "", fmt.Errorf("observation store database is required")
	}
	workspace := strings.TrimSpace(query.Workspace)
	if workspace == "" {
		return nil, "", fmt.Errorf("workspace is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultHistory
	}
	if limit > MaxHistory {
		limit = MaxHistory
	}

	where := []string{"workspace_name = ?"}
	args := []any{workspace}
	if query.SessionID != "" {
		where = append(where, "remote_session_id = ?")
		args = append(args, query.SessionID)
	}
	if query.CallID != "" {
		where = append(where, "call_id = ?")
		args = append(args, query.CallID)
	}
	if err := appendIntFilter(&where, &args, "sequence", query.EventIDs); err != nil {
		return nil, "", err
	}
	appendStringFilter(&where, &args, "request_id", query.RequestIDs)
	appendStringFilter(&where, &args, "operation_id", query.OperationIDs)
	appendStringFilter(&where, &args, "plan_task_id", query.PlanTaskIDs)
	appendStringFilter(&where, &args, "execution_task_id", query.ExecutionTaskIDs)
	appendKindFilter(&where, &args, query.Kinds)
	appendStringFilter(&where, &args, "status", query.Statuses)
	if !query.CreatedAfter.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, query.CreatedAfter.UnixMilli())
	}
	if !query.CreatedBefore.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, query.CreatedBefore.UnixMilli())
	}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		keyword = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(keyword)
		pattern := "%" + keyword + "%"
		where = append(where, `(summary LIKE ? ESCAPE '\' OR purpose LIKE ? ESCAPE '\' OR tool_name LIKE ? ESCAPE '\' OR command LIKE ? ESCAPE '\' OR skill_name LIKE ? ESCAPE '\' OR mcp_server LIKE ? ESCAPE '\' OR mcp_tool LIKE ? ESCAPE '\' OR path LIKE ? ESCAPE '\' OR resource_uri LIKE ? ESCAPE '\' OR input_json LIKE ? ESCAPE '\' OR output_json LIKE ? ESCAPE '\')`)
		for range 11 {
			args = append(args, pattern)
		}
	}
	if strings.TrimSpace(query.Cursor) != "" {
		cursor, err := strconv.ParseInt(strings.TrimSpace(query.Cursor), 10, 64)
		if err != nil || cursor <= 0 {
			return nil, "", fmt.Errorf("invalid history cursor")
		}
		where = append(where, "sequence < ?")
		args = append(args, cursor)
	}

	order := "DESC"
	if query.Ascending {
		order = "ASC"
		where = append(where, "sequence > ?")
		args = append(args, query.AfterSequence)
	}
	statement := `SELECT sequence, workspace_name,
	        remote_session_id, request_id, call_id, turn_id, activity_sequence, activity_kind, related_call_id, operation_id, tool_name, event_type, phase,
		intent, progress_summary, input_json, output_json, summary, status, purpose, goal, reasoning_summary, next_step, plan_id, plan_task_id, execution_task_id, parent_operation_id, step_id,
        command, working_directory, exit_code, duration_ms, skill_name, mcp_server, mcp_tool, path,
        resource_uri, stream, stream_offset, truncated, created_at
        FROM observation_events WHERE ` + strings.Join(where, " AND ") + `
        ORDER BY sequence ` + order + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events, err := scanEvents(rows, limit)
	if err != nil {
		return nil, "", err
	}
	nextCursor := ""
	if len(events) == limit && len(events) > 0 {
		nextCursor = strconv.FormatInt(events[len(events)-1].Sequence, 10)
	}
	return events, nextCursor, nil
}

func appendStringFilter(where *[]string, args *[]any, column string, values []string) {
	values = cleanValues(values)
	if len(values) == 0 {
		return
	}
	placeholders := make([]string, len(values))
	for i, value := range values {
		placeholders[i] = "?"
		*args = append(*args, value)
	}
	*where = append(*where, column+" IN ("+strings.Join(placeholders, ", ")+")")
}

func appendKindFilter(where *[]string, args *[]any, values []string) {
	values = cleanValues(values)
	if len(values) == 0 {
		return
	}
	clauses := make([]string, 0, len(values))
	for _, value := range values {
		switch strings.ToLower(value) {
		case "tool":
			clauses = append(clauses, "event_type IN ('tool.started', 'tool.completed')")
		case "command", "task":
			clauses = append(clauses, "event_type = 'command.output' OR tool_name = 'execute'")
		case "skill":
			clauses = append(clauses, "tool_name = 'skill_tool'")
		case "mcp":
			clauses = append(clauses, "tool_name = 'mcp_tool'")
		case "file_change":
			clauses = append(clauses, "event_type = 'file.changed'")
		case "session":
			clauses = append(clauses, "event_type = 'session.lifecycle'")
		case "confirmation":
			clauses = append(clauses, "status = 'waiting_confirmation' OR output_json LIKE '%CONFIRMATION%'")
		case "error":
			clauses = append(clauses, "status = 'failed'")
		default:
			clauses = append(clauses, "event_type = ?")
			*args = append(*args, value)
		}
	}
	if len(clauses) > 0 {
		*where = append(*where, "("+strings.Join(clauses, " OR ")+")")
	}
}

func appendIntFilter(where *[]string, args *[]any, column string, values []string) error {
	values = cleanValues(values)
	if len(values) == 0 {
		return nil
	}
	placeholders := make([]string, len(values))
	for i, value := range values {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid event id %q", value)
		}
		placeholders[i] = "?"
		*args = append(*args, parsed)
	}
	*where = append(*where, column+" IN ("+strings.Join(placeholders, ", ")+")")
	return nil
}

func cleanValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
