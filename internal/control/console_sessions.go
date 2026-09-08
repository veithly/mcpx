package control

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Console sessions survive process restarts: the operator should not have to
// re-login after every deploy. Only the SHA-256 of the cookie token is stored.
var ErrNoConsoleSession = sql.ErrNoRows

func (s *Store) SaveConsoleSession(ctx context.Context, tokenHash, csrf string, expiresAt time.Time) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO console_sessions(token_hash,csrf,expires_at) VALUES(?,?,?)
 ON CONFLICT(token_hash) DO UPDATE SET csrf=excluded.csrf, expires_at=excluded.expires_at`,
		tokenHash, csrf, expiresAt.Unix())
	return err
}

func (s *Store) LoadConsoleSession(ctx context.Context, tokenHash string) (csrf string, expiresAt time.Time, err error) {
	if s == nil {
		return "", time.Time{}, ErrNoConsoleSession
	}
	var expires int64
	err = s.db.QueryRowContext(ctx, `SELECT csrf, expires_at FROM console_sessions WHERE token_hash=?`, tokenHash).Scan(&csrf, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNoConsoleSession
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return csrf, time.Unix(expires, 0), nil
}

func (s *Store) DeleteConsoleSession(ctx context.Context, tokenHash string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM console_sessions WHERE token_hash=?`, tokenHash)
	return err
}
