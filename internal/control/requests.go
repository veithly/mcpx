package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func (s *Store) Enqueue(ctx context.Context, workspace, session, kind, body, key string) (Request, error) {
	body, key = strings.TrimSpace(body), strings.TrimSpace(key)
	if workspace == "" || body == "" || !utf8.ValidString(body) || len(body) > MaxBodyBytes || len(key) > 128 || key == "" {
		return Request{}, errors.New("workspace, idempotency key and 1–8000 UTF-8 bytes of text required")
	}
	if kind != "steer" && kind != "interrupt" && kind != "approval" {
		return Request{}, errors.New("invalid request kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback()
	var existing Request
	err = tx.QueryRowContext(ctx, `SELECT id, workspace, session_id, kind, body, created_at, acknowledged_at FROM operator_requests WHERE workspace=? AND client_key=?`, workspace, key).Scan(&existing.ID, &existing.Workspace, &existing.SessionID, &existing.Kind, &existing.Body, &existing.CreatedAt, &existing.AcknowledgedAt)
	if err == nil {
		if existing.SessionID != session || existing.Kind != kind || existing.Body != body {
			return Request{}, ErrConflict
		}
		existing.Status = "queued"
		if existing.AcknowledgedAt > 0 {
			existing.Status = "acknowledged"
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Request{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operator_requests WHERE workspace=? AND acknowledged_at=0`, workspace).Scan(&count); err != nil {
		return Request{}, err
	}
	if count >= 200 {
		return Request{}, errors.New("workspace already has 200 unacknowledged requests")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return Request{}, err
	}
	item := Request{ID: id.String(), Workspace: workspace, SessionID: session, Kind: kind, Body: body, Status: "queued", CreatedAt: time.Now().UnixMilli()}
	_, err = tx.ExecContext(ctx, `INSERT INTO operator_requests(id,workspace,session_id,kind,body,client_key,created_at) VALUES(?,?,?,?,?,?,?)`, item.ID, workspace, session, kind, body, key, item.CreatedAt)
	if err != nil {
		return Request{}, err
	}
	return item, tx.Commit()
}

// Deliver is deliberately at-least-once. A lost HTTP response never consumes a
// request. Acknowledgements require a receipt for this exact remote session.
func (s *Store) Deliver(ctx context.Context, workspace, session, call string) ([]Request, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace,session_id,kind,body,created_at FROM operator_requests
 WHERE workspace=? AND (session_id='' OR session_id=?) AND acknowledged_at=0 ORDER BY created_at,id LIMIT ?`, workspace, session, DeliveryLimit)
	if err != nil {
		return nil, err
	}
	items := []Request{}
	for rows.Next() {
		var m Request
		if err = rows.Scan(&m.ID, &m.Workspace, &m.SessionID, &m.Kind, &m.Body, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Status = "delivered"
		m.DeliveredAt = time.Now().UnixMilli()
		items = append(items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, m := range items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO operator_deliveries(request_id,session_id,call_id,delivered_at) VALUES(?,?,?,?)
   ON CONFLICT(request_id,session_id) DO UPDATE SET call_id=excluded.call_id`, m.ID, session, call, m.DeliveredAt); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit()
}

func (s *Store) Acknowledge(ctx context.Context, workspace, session string, ids []string) error {
	if len(ids) > DeliveryLimit {
		return errors.New("too many request acknowledgements")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err = uuid.Parse(id); err != nil {
			return errors.New("invalid request acknowledgement")
		}
		result, err := tx.ExecContext(ctx, `UPDATE operator_requests SET acknowledged_at=CASE WHEN acknowledged_at=0 THEN ? ELSE acknowledged_at END
  WHERE id=? AND workspace=? AND (session_id='' OR session_id=?) AND EXISTS(SELECT 1 FROM operator_deliveries d WHERE d.request_id=operator_requests.id AND d.session_id=?)`, time.Now().UnixMilli(), id, workspace, session, session)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return errors.New("request was not delivered to this session")
		}
	}
	return tx.Commit()
}

func (s *Store) List(ctx context.Context, workspace, session string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.workspace,r.session_id,r.kind,r.body,r.created_at,r.acknowledged_at,
 COALESCE((SELECT MIN(delivered_at) FROM operator_deliveries d WHERE d.request_id=r.id),0)
 FROM operator_requests r WHERE workspace=? AND (?='' OR session_id='' OR session_id=?) ORDER BY created_at DESC,id DESC LIMIT 100`, workspace, session, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Request{}
	for rows.Next() {
		var m Request
		if err = rows.Scan(&m.ID, &m.Workspace, &m.SessionID, &m.Kind, &m.Body, &m.CreatedAt, &m.AcknowledgedAt, &m.DeliveredAt); err != nil {
			return nil, err
		}
		m.Status = "queued"
		if m.DeliveredAt > 0 {
			m.Status = "delivered"
		}
		if m.AcknowledgedAt > 0 {
			m.Status = "acknowledged"
		}
		items = append(items, m)
	}
	return items, rows.Err()
}
