// Package control persists human-authored workspace settings and requests.
// Only the authenticated operator console can change these settings.
package control

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	Approval      = "approval"
	FullAccess    = "full_access"
	MaxBodyBytes  = 8000
	DeliveryLimit = 16
)

var ErrConflict = errors.New("request key already belongs to different content")

type Store struct{ db *sql.DB }

type Request struct {
	Replayed       bool   `json:"replayed,omitempty"`
	ID             string `json:"id"`
	Workspace      string `json:"workspace"`
	SessionID      string `json:"remote_session_id,omitempty"`
	Kind           string `json:"kind"`
	Body           string `json:"body"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
	DeliveredAt    int64  `json:"delivered_at,omitempty"`
	AcknowledgedAt int64  `json:"acknowledged_at,omitempty"`
	CancelledAt    int64  `json:"cancelled_at,omitempty"`
}

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("control requires a database")
	}
	_, err := db.Exec(`
 CREATE TABLE IF NOT EXISTS console_items (
  kind TEXT NOT NULL CHECK(kind IN ('workspace','session')), id TEXT NOT NULL,
  workspace TEXT NOT NULL, pinned INTEGER NOT NULL DEFAULT 0,
  position INTEGER NOT NULL DEFAULT 0, deleted_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(kind,id)
 );
 CREATE TABLE IF NOT EXISTS console_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
 INSERT OR IGNORE INTO console_revision(id,revision) VALUES(1,0);
 CREATE TABLE IF NOT EXISTS workspace_access (
  workspace TEXT PRIMARY KEY, mode TEXT NOT NULL CHECK(mode IN ('approval','full_access')), updated_at INTEGER NOT NULL
 );
 CREATE TABLE IF NOT EXISTS operator_requests (
  id TEXT PRIMARY KEY, workspace TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK(kind IN ('steer','interrupt','approval')), body TEXT NOT NULL,
  client_key TEXT NOT NULL, created_at INTEGER NOT NULL, acknowledged_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(workspace, client_key)
 );
 CREATE TABLE IF NOT EXISTS operator_request_cancellations (
  request_id TEXT PRIMARY KEY REFERENCES operator_requests(id) ON DELETE CASCADE, cancelled_at INTEGER NOT NULL
 );
 CREATE INDEX IF NOT EXISTS operator_requests_pending ON operator_requests(workspace, acknowledged_at, created_at);
 CREATE TABLE IF NOT EXISTS operator_deliveries (
  request_id TEXT NOT NULL REFERENCES operator_requests(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL, call_id TEXT NOT NULL, delivered_at INTEGER NOT NULL,
  PRIMARY KEY(request_id, session_id)
 );
 CREATE TABLE IF NOT EXISTS operator_approvals (
  pending_id TEXT PRIMARY KEY, digest TEXT NOT NULL, decision TEXT NOT NULL CHECK(decision IN ('approved','denied')), updated_at INTEGER NOT NULL
 );`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Mode(ctx context.Context, workspace string) (string, error) {
	var mode string
	err := s.db.QueryRowContext(ctx, `SELECT mode FROM workspace_access WHERE workspace = ?`, workspace).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval, nil
	}
	return mode, err
}

func (s *Store) SetMode(ctx context.Context, workspace, mode string) error {
	if workspace == "" || (mode != Approval && mode != FullAccess) {
		return errors.New("invalid workspace access mode")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_access(workspace, mode, updated_at) VALUES(?,?,?)
 ON CONFLICT(workspace) DO UPDATE SET mode=excluded.mode, updated_at=excluded.updated_at`, workspace, mode, time.Now().UnixMilli())
	return err
}

func (s *Store) Decide(ctx context.Context, id, digest, decision string) error {
	if id == "" || digest == "" || (decision != "approved" && decision != "denied") {
		return errors.New("invalid approval decision")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO operator_approvals(pending_id,digest,decision,updated_at) VALUES(?,?,?,?)
 ON CONFLICT(pending_id) DO UPDATE SET decision=excluded.decision,updated_at=excluded.updated_at WHERE operator_approvals.digest=excluded.digest`, id, digest, decision, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("approval digest mismatch")
	}
	return nil
}

func (s *Store) Decision(ctx context.Context, id, digest string) (string, error) {
	var decision string
	err := s.db.QueryRowContext(ctx, `SELECT decision FROM operator_approvals WHERE pending_id=? AND digest=?`, id, digest).Scan(&decision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return decision, err
}
