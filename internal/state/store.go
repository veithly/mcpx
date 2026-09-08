package state

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store owns the durable MCPX state database.
type Store struct {
	db   *sql.DB
	path string
}

// sqliteDSN builds a file DSN whose connection-scoped PRAGMAs apply to every
// pooled connection, not just the first one. _txlock=immediate makes writers
// queue politely under busy_timeout instead of failing fast with SQLITE_BUSY
// when a deferred transaction upgrades mid-flight under concurrency.
func sqliteDSN(path string) string {
	query := url.Values{}
	query.Add("_txlock", "immediate")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "busy_timeout(3000)")
	query.Add("_pragma", "synchronous(NORMAL)")
	return "file:" + path + "?" + query.Encode()
}

// Open creates or opens a SQLite database and applies all schema migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("state database path required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure state directory: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	// PRAGMAs ride the DSN so every pooled connection gets them; a small pool
	// keeps one slow or leaked reader from starving every other caller.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open state database: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure state database: %w", err)
	}
	secureSidecars(path)
	return &Store{db: db, path: path}, nil
}

func secureSidecars(path string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			_ = os.Chmod(path+suffix, 0o600)
		}
	}
}

// DB exposes the database handle to bounded domain repositories.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// Close flushes and closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
