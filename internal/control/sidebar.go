package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrSidebarConflict = errors.New("sidebar changed; refresh and retry")

type SidebarItem struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Pinned    bool   `json:"pinned"`
	Position  int    `json:"position"`
	DeletedAt int64  `json:"deleted_at"`
}

type SidebarState struct {
	Revision int64         `json:"revision"`
	Items    []SidebarItem `json:"items"`
}

func (s *Store) Sidebar(ctx context.Context) (SidebarState, error) {
	out := SidebarState{Items: []SidebarItem{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM console_revision WHERE id=1`).Scan(&out.Revision); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,workspace,pinned,position,deleted_at FROM console_items ORDER BY kind,position,id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item SidebarItem
		if err = rows.Scan(&item.Kind, &item.ID, &item.Workspace, &item.Pinned, &item.Position, &item.DeletedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// SaveSidebar is a single versioned transaction. Reordering cannot restore a
// concurrently deleted entry or overwrite another browser's pin decision.
func (s *Store) SaveSidebar(ctx context.Context, revision int64, items []SidebarItem) error {
	if len(items) > 10000 {
		return errors.New("too many sidebar entries")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE console_revision SET revision=revision+1 WHERE id=1 AND revision=?`, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSidebarConflict
	}
	for _, item := range items {
		if (item.Kind != "workspace" && item.Kind != "session") || item.ID == "" || item.Workspace == "" || item.Position < 0 {
			return fmt.Errorf("invalid sidebar entry")
		}
		if item.DeletedAt > 0 {
			_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO operator_request_cancellations(request_id,cancelled_at)
			SELECT id,? FROM operator_requests WHERE workspace=? AND acknowledged_at=0 AND (?='workspace' OR session_id=?)`, item.DeletedAt, item.Workspace, item.Kind, item.ID)
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO console_items(kind,id,workspace,pinned,position,deleted_at) VALUES(?,?,?,?,?,?)
  ON CONFLICT(kind,id) DO UPDATE SET workspace=excluded.workspace,pinned=excluded.pinned,position=excluded.position,deleted_at=excluded.deleted_at`, item.Kind, item.ID, item.Workspace, item.Pinned, item.Position, item.DeletedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Deleted(ctx context.Context, workspace, session string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM console_items WHERE deleted_at>0 AND ((kind='workspace' AND id=?) OR (kind='session' AND id=?))`, workspace, session).Scan(&count)
	return count > 0, err
}

// RestoreWorkspace never restores conversations that the operator deleted.
func (s *Store) RestoreWorkspace(ctx context.Context, workspace string) error {
	state, err := s.Sidebar(ctx)
	if err != nil {
		return err
	}
	for _, item := range state.Items {
		if item.Kind == "workspace" && item.ID == workspace && item.DeletedAt > 0 {
			item.DeletedAt = 0
			item.Pinned = false
			item.Position = 0
			return s.SaveSidebar(ctx, state.Revision, []SidebarItem{item})
		}
	}
	return nil
}
