package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcpx/internal/envelope"
	"mcpx/internal/observation"
	"mcpx/internal/remotesession"
)

const agentActivityProtocolVersion = "3"

var agentActivityKindNames = []string{
	"intent",
	"hypothesis",
	"evidence",
	"conclusion",
	"next",
	"status",
}

type agentActivityState struct {
	RemoteSessionID string
	TurnID          string
	Sequence        int64
	State           string
	Kind            string
	Summary         string
	RelatedCallID   string
	PersistedAt     time.Time
	StateSince      time.Time
	SeenAt          time.Time
}

type embeddedActivityUpdate struct {
	Kind    string
	Summary string
}

func embeddedActivityUpdates(input envelope.ActivityInput) ([]embeddedActivityUpdate, error) {
	values := []embeddedActivityUpdate{
		{Kind: "intent", Summary: input.Intent},
		{Kind: "hypothesis", Summary: input.Hypothesis},
		{Kind: "evidence", Summary: input.Evidence},
		{Kind: "conclusion", Summary: input.Conclusion},
		{Kind: "next", Summary: input.Next},
		{Kind: "status", Summary: input.Status},
	}
	updates := make([]embeddedActivityUpdate, 0, len(values))
	for _, update := range values {
		raw := strings.TrimSpace(update.Summary)
		if raw == "" {
			continue
		}
		if len(raw) > envelope.MaxActivityBytes {
			return nil, fmt.Errorf("activity.%s exceeds %d bytes", update.Kind, envelope.MaxActivityBytes)
		}
		update.Summary = observation.SanitizeIntent(raw)
		if update.Summary != "" {
			updates = append(updates, update)
		}
	}
	return updates, nil
}

func (r *Runtime) recordEmbeddedAgentActivity(ctx context.Context, request envelope.Request, runtime RuntimeContext, now time.Time) error {
	updates, err := embeddedActivityUpdates(request.Activity)
	if err != nil || len(updates) == 0 {
		return err
	}
	if r == nil || r.remote == nil || r.observation == nil || r.state == nil {
		return fmt.Errorf("activity observation is unavailable")
	}
	remoteSessionID := strings.TrimSpace(request.RemoteSessionID)
	if remoteSessionID == "" {
		return fmt.Errorf("remote_session_id is required when activity is provided")
	}
	principal, err := r.principalFromContext(ctx)
	if err != nil {
		return fmt.Errorf("authorization is required for activity")
	}
	session, err := r.remote.Get(ctx, principal, remoteSessionID)
	if err != nil {
		return fmt.Errorf("remote session is unavailable: %w", err)
	}
	return r.acceptEmbeddedAgentActivities(ctx, session, updates, strings.TrimSpace(request.Activity.Intent) != "", runtime.RequestID, now)
}

func (r *Runtime) acceptEmbeddedAgentActivities(ctx context.Context, session remotesession.Session, updates []embeddedActivityUpdate, startNewTurn bool, relatedCallID string, now time.Time) error {
	if len(updates) == 0 {
		return nil
	}
	if r == nil || r.state == nil || r.state.DB() == nil || r.observation == nil {
		return fmt.Errorf("activity state store is unavailable")
	}
	relatedCallID = strings.TrimSpace(relatedCallID)
	if relatedCallID == "" {
		return fmt.Errorf("runtime call identity is required for activity")
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()

	tx, err := r.state.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	previous, exists, err := loadLatestAgentActivityState(ctx, tx, session.ID)
	if err != nil {
		return err
	}
	if !previous.SeenAt.IsZero() && now.UnixMilli() <= previous.SeenAt.UnixMilli() {
		// seen_at is persisted with millisecond precision. Advance the next
		// activity batch by a full persisted tick so ORDER BY seen_at cannot
		// select an older turn when two tool calls arrive in the same millisecond.
		now = previous.SeenAt.Add(time.Millisecond)
	}
	turnID := ""
	sequence := int64(0)
	stateSince := now
	if !startNewTurn && exists {
		turnID = previous.TurnID
		sequence = previous.Sequence
		if previous.State == "preparing_action" && !previous.StateSince.IsZero() {
			stateSince = previous.StateSince
		}
	}
	if turnID == "" {
		turnID = "turn_" + strings.TrimPrefix(relatedCallID, "req_")
	}

	events := make([]observation.Event, 0, len(updates))
	for index, update := range updates {
		sequence++
		createdAt := now.Add(time.Duration(index) * time.Nanosecond)
		state := agentActivityState{
			RemoteSessionID: session.ID,
			TurnID:          turnID,
			Sequence:        sequence,
			State:           "preparing_action",
			Kind:            update.Kind,
			Summary:         update.Summary,
			RelatedCallID:   relatedCallID,
			PersistedAt:     createdAt,
			StateSince:      stateSince,
			SeenAt:          createdAt,
		}
		if err := saveAgentActivityState(ctx, tx, state); err != nil {
			return err
		}
		events = append(events, observation.Event{
			Workspace:        session.WorkspaceName,
			RemoteSessionID:  session.ID,
			TurnID:           turnID,
			ActivitySequence: sequence,
			ActivityKind:     update.Kind,
			RelatedCallID:    relatedCallID,
			Type:             observation.TypeAgentActivity,
			Phase:            observation.PhaseThoughtSummary,
			Status:           "preparing_action",
			ProgressSummary:  update.Summary,
			Summary:          update.Summary,
			CreatedAt:        createdAt,
		})
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, event := range events {
		if err := r.observation.Record(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func loadLatestAgentActivityState(ctx context.Context, tx *sql.Tx, remoteSessionID string) (agentActivityState, bool, error) {
	var state agentActivityState
	var persistedAt, stateSince, seenAt int64
	err := tx.QueryRowContext(ctx, `SELECT remote_session_id, turn_id, sequence, state, kind, summary, related_call_id, persisted_at, state_since, seen_at
		FROM agent_activity_turns WHERE remote_session_id = ? ORDER BY seen_at DESC, turn_id DESC LIMIT 1`, remoteSessionID).Scan(
		&state.RemoteSessionID, &state.TurnID, &state.Sequence, &state.State, &state.Kind, &state.Summary, &state.RelatedCallID,
		&persistedAt, &stateSince, &seenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentActivityState{}, false, nil
	}
	if err != nil {
		return agentActivityState{}, false, err
	}
	state.PersistedAt = activityTime(persistedAt)
	state.StateSince = activityTime(stateSince)
	state.SeenAt = activityTime(seenAt)
	return state, true, nil
}

func saveAgentActivityState(ctx context.Context, tx *sql.Tx, state agentActivityState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_activity_turns
		(remote_session_id, turn_id, sequence, state, kind, summary, related_call_id, persisted_at, state_since, seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(remote_session_id, turn_id) DO UPDATE SET
			sequence = excluded.sequence,
			state = excluded.state,
			kind = excluded.kind,
			summary = excluded.summary,
			related_call_id = excluded.related_call_id,
			persisted_at = excluded.persisted_at,
			state_since = excluded.state_since,
			seen_at = excluded.seen_at`,
		state.RemoteSessionID, state.TurnID, state.Sequence, state.State, state.Kind, state.Summary, state.RelatedCallID,
		activityMillis(state.PersistedAt), activityMillis(state.StateSince), activityMillis(state.SeenAt),
	)
	return err
}

func (r *Runtime) agentActivitySnapshot(ctx context.Context, remoteSessionID string) (map[string]any, error) {
	state, err := r.currentAgentActivity(ctx, remoteSessionID)
	if err != nil || state == nil {
		return nil, err
	}
	return map[string]any{
		"turn_id":         state.TurnID,
		"sequence":        state.Sequence,
		"state":           state.State,
		"kind":            state.Kind,
		"summary":         state.Summary,
		"related_call_id": state.RelatedCallID,
	}, nil
}

func (r *Runtime) currentAgentActivity(ctx context.Context, remoteSessionID string) (*agentActivityState, error) {
	if r == nil || r.state == nil || r.state.DB() == nil || strings.TrimSpace(remoteSessionID) == "" {
		return nil, nil
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	var state agentActivityState
	err := r.state.DB().QueryRowContext(ctx, `SELECT turn_id, sequence, state, kind, summary, related_call_id
		FROM agent_activity_turns WHERE remote_session_id = ? ORDER BY seen_at DESC, turn_id DESC LIMIT 1`, remoteSessionID).Scan(
		&state.TurnID, &state.Sequence, &state.State, &state.Kind, &state.Summary, &state.RelatedCallID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func activityMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func activityTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
