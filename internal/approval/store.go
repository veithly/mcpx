package approval

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Pending holds a deferred tool action awaiting human approval.
type Pending struct {
	ID                string
	Tool              string
	Summary           string
	Command           string
	CommandYieldMs    int
	Purpose           string
	Scope             string
	CommandDigest     string
	WorkDir           string
	RequestID         string
	Workspace         string
	RemoteSessionID   string
	PrincipalID       string
	ConfirmationToken string
	ContentKey        string
	DeleteFiles       []DeleteFile `json:"delete_files,omitempty"`
	CreatedAt         time.Time
}

// DeleteFile is the immutable file snapshot shown before a clean edit delete
// is allowed to proceed. The snapshot prevents a confirmation for one file
// revision from authorizing a later revision.
type DeleteFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Store holds Remote Session-scoped pending approvals.
type Store struct {
	mu   sync.Mutex
	byID map[string]Pending
	db   *sql.DB
}

// PendingTTL is how long a pending approval stays actionable before it
// expires and the agent should be told to move on.
const PendingTTL = 30 * time.Minute

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{byID: map[string]Pending{}}
}

// NewPersistentStore stores Remote Session approvals in SQLite.
func NewPersistentStore(db *sql.DB) *Store {
	return &Store{byID: map[string]Pending{}, db: db}
}

// contentKey computes the dedup key for a pending approval. Transport request
// IDs are excluded; the semantic operation remains bound to its principal.
func contentKey(p Pending) string {
	switch p.Tool {
	case "command_execute":
		// Purpose is intentionally excluded: models rephrase the same intent
		// when retrying a confirmed command, and the confirmed action is the
		// command itself. Binding to purpose alone turns a user confirmation
		// into an unrecoverable token-mismatch loop.
		return strings.Join([]string{p.PrincipalID, p.Command, p.Scope}, "\x00")
	case "edit_delete":
		return p.ContentKey
	default:
		return ""
	}
}

// Put registers a pending action and returns its internal ID. The internal ID
// is not part of the public contract; public callers use ConfirmationToken.
func (s *Store) Put(p Pending) (string, error) {
	stored, err := s.PutPending(p)
	if err != nil {
		return "", err
	}
	return stored.ID, nil
}

// PutPending registers a pending action and returns the durable item including
// its semantic confirmation token. The token is bound by the caller to the
// same session, principal and operation digest; it is not an auth credential.
func (s *Store) PutPending(p Pending) (Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.pruneLocked(now)
	if p.ID == "" {
		p.ID = newID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.ContentKey == "" {
		p.ContentKey = contentKey(p)
	}
	if p.ConfirmationToken == "" {
		p.ConfirmationToken = newConfirmationToken()
	}
	// An incoming approval that is already past its TTL is stale: never fold it
	// onto a live approval_id.
	if p.ContentKey != "" && now.Sub(p.CreatedAt) < PendingTTL {
		if existing, ok := s.findPendingLocked(p.RemoteSessionID, p.ContentKey); ok {
			return existing, nil
		}
	}
	if s.db != nil {
		payload, err := json.Marshal(p)
		if err != nil {
			return Pending{}, fmt.Errorf("encode approval: %w", err)
		}
		_, err = s.db.Exec(`INSERT OR REPLACE INTO approvals
            (id, remote_session_id, principal_id, tool, summary, payload_json, content_key, status, created_at, expires_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
			p.ID, p.RemoteSessionID, p.PrincipalID, p.Tool, p.Summary, string(payload), p.ContentKey,
			p.CreatedAt.UnixMilli(), p.CreatedAt.Add(PendingTTL).UnixMilli())
		if err != nil {
			return Pending{}, fmt.Errorf("persist approval: %w", err)
		}
	}
	s.byID[p.ID] = p
	return p, nil
}

// Consume removes and returns a pending approval by id after the action succeeds.
func (s *Store) Consume(id string) (Pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now().UTC())
	p, ok := s.getLocked(id)
	if ok {
		if s.db != nil {
			result, err := s.db.Exec(`UPDATE approvals SET status = 'consumed', consumed_at = ? WHERE id = ? AND status = 'pending' AND expires_at > ?`,
				time.Now().UTC().UnixMilli(), id, time.Now().UTC().UnixMilli())
			if err != nil {
				return Pending{}, false
			}
			rows, _ := result.RowsAffected()
			if rows != 1 {
				return Pending{}, false
			}
		}
		delete(s.byID, id)
	}
	return p, ok
}

// Take is retained for internal callers that explicitly need one-shot removal.
func (s *Store) Take(id string) (Pending, bool) {
	return s.Consume(id)
}

// Get peeks without remove.
func (s *Store) Get(id string) (Pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now().UTC())
	return s.getLocked(id)
}

// ListRemoteSession returns durable pending approvals for a Remote Session.
func (s *Store) ListRemoteSession(remoteSessionID string) []Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now().UTC())
	if s.db != nil {
		return s.listLocked(`remote_session_id = ?`, remoteSessionID)
	}
	var out []Pending
	for _, pending := range s.byID {
		if pending.RemoteSessionID == remoteSessionID {
			out = append(out, pending)
		}
	}
	return out
}

func (s *Store) pruneLocked(now time.Time) {
	for id, p := range s.byID {
		if !p.CreatedAt.IsZero() && now.Sub(p.CreatedAt) >= PendingTTL {
			delete(s.byID, id)
		}
	}
	if s.db != nil {
		_, _ = s.db.Exec(`UPDATE approvals SET status = 'expired' WHERE status = 'pending' AND expires_at <= ?`, now.UnixMilli())
	}
}

func (s *Store) findPendingLocked(remoteSessionID, contentKey string) (Pending, bool) {
	for _, p := range s.byID {
		if p.RemoteSessionID == remoteSessionID && p.ContentKey == contentKey {
			return p, true
		}
	}
	if s.db == nil {
		return Pending{}, false
	}
	var payload string
	err := s.db.QueryRow(`SELECT payload_json FROM approvals
        WHERE remote_session_id = ? AND content_key = ? AND status = 'pending' AND expires_at > ?
        ORDER BY created_at LIMIT 1`,
		remoteSessionID, contentKey, time.Now().UTC().UnixMilli()).Scan(&payload)
	if err != nil {
		return Pending{}, false
	}
	var pending Pending
	if json.Unmarshal([]byte(payload), &pending) != nil {
		return Pending{}, false
	}
	return pending, true
}

func (s *Store) getLocked(id string) (Pending, bool) {
	if pending, ok := s.byID[id]; ok {
		return pending, true
	}
	if s.db == nil {
		return Pending{}, false
	}
	var payload string
	err := s.db.QueryRow(`SELECT payload_json FROM approvals WHERE id = ? AND status = 'pending' AND expires_at > ?`, id, time.Now().UTC().UnixMilli()).Scan(&payload)
	if err != nil {
		return Pending{}, false
	}
	var pending Pending
	if json.Unmarshal([]byte(payload), &pending) != nil {
		return Pending{}, false
	}
	return pending, true
}

func (s *Store) listLocked(where string, value string) []Pending {
	rows, err := s.db.Query(`SELECT payload_json FROM approvals WHERE `+where+` AND status = 'pending' AND expires_at > ? ORDER BY created_at`, value, time.Now().UTC().UnixMilli())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []Pending
	for rows.Next() {
		var payload string
		var pending Pending
		if rows.Scan(&payload) == nil && json.Unmarshal([]byte(payload), &pending) == nil {
			result = append(result, pending)
		}
	}
	return result
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("a_%s", hex.EncodeToString(b[:]))
}

func newConfirmationToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("ct_%s", hex.EncodeToString(b[:]))
}
