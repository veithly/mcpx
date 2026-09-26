package secrets

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// PendingSecret is a need_secret hang.
type PendingSecret struct {
	ID              string
	RemoteSessionID string
	PrincipalID     string
	Tool            string
	Reason          string
	Prompt          string
	RequestID       string
	Workspace       string
	Command         string
	WorkDir         string
	// resume terminal.exec after provide
	ResumeExec bool
	CreatedAt  time.Time
}

// Store holds Remote Session secrets and pending need_secret requests.
type Store struct {
	mu         sync.Mutex
	values     map[string]map[string]string // remoteSessionID -> ref -> value
	pending    map[string]PendingSecret
	expiry     map[string]time.Time // remoteSessionID|ref -> expiry
	defaultTTL time.Duration
	db         *sql.DB
}

// NewPersistentStore persists only resumable request metadata. Secret values
// remain memory-only and expire after a short TTL.
func NewPersistentStore(db *sql.DB) *Store {
	store := NewStore()
	store.db = db
	return store
}

// NewStore creates a secrets store.
func NewStore() *Store {
	return &Store{
		values:     map[string]map[string]string{},
		pending:    map[string]PendingSecret{},
		expiry:     map[string]time.Time{},
		defaultTTL: 15 * time.Minute,
	}
}

// PutOnce injects one-shot env pairs for a single exec (caller clears).
func OneShotEnv(password string, inject string) []string {
	if password == "" {
		return nil
	}
	// default askpass uses SSH_ASKPASS style via env MCPX_PASSWORD
	switch inject {
	case "", "askpass":
		return []string{"MCPX_SECRET_PASSWORD=" + password}
	default:
		if len(inject) > 4 && inject[:4] == "env:" {
			return []string{inject[4:] + "=" + password}
		}
		return []string{"MCPX_SECRET_PASSWORD=" + password}
	}
}

// Set caches a ref for a Remote Session.
func (s *Store) Set(remoteSessionID, ref, value string, cache bool, ttl time.Duration) {
	if !cache || ref == "" || value == "" {
		return
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[remoteSessionID] == nil {
		s.values[remoteSessionID] = map[string]string{}
	}
	s.values[remoteSessionID][ref] = value
	s.expiry[remoteSessionID+"|"+ref] = time.Now().Add(ttl)
}

// Get returns cached secret.
func (s *Store) Get(remoteSessionID, ref string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := remoteSessionID + "|" + ref
	if exp, ok := s.expiry[key]; ok && time.Now().After(exp) {
		delete(s.values[remoteSessionID], ref)
		delete(s.expiry, key)
		return "", false
	}
	m := s.values[remoteSessionID]
	if m == nil {
		return "", false
	}
	v, ok := m[ref]
	return v, ok
}

// PutPending registers need_secret.
func (s *Store) PutPending(p PendingSecret) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		p.ID = "sec_" + hex.EncodeToString(b[:])
	}
	p.CreatedAt = time.Now().UTC()
	s.pending[p.ID] = p
	if s.db != nil {
		payload, _ := json.Marshal(p)
		_, _ = s.db.Exec(`INSERT OR REPLACE INTO secret_requests
            (id, remote_session_id, principal_id, payload_json, status, created_at, expires_at)
            VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
			p.ID, p.RemoteSessionID, p.PrincipalID, string(payload), p.CreatedAt.UnixMilli(), p.CreatedAt.Add(s.defaultTTL).UnixMilli())
	}
	return p.ID
}

// TakePending removes pending.
func (s *Store) TakePending(id string) (PendingSecret, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok && s.db != nil {
		var payload string
		err := s.db.QueryRow(`SELECT payload_json FROM secret_requests WHERE id = ? AND status = 'pending' AND expires_at > ?`, id, time.Now().UTC().UnixMilli()).Scan(&payload)
		if err == nil && json.Unmarshal([]byte(payload), &p) == nil {
			ok = true
		}
	}
	if ok {
		if s.db != nil {
			result, err := s.db.Exec(`UPDATE secret_requests SET status = 'consumed', consumed_at = ? WHERE id = ? AND status = 'pending' AND expires_at > ?`,
				time.Now().UTC().UnixMilli(), id, time.Now().UTC().UnixMilli())
			if err != nil {
				return PendingSecret{}, false
			}
			rows, _ := result.RowsAffected()
			if rows != 1 {
				return PendingSecret{}, false
			}
		}
		delete(s.pending, id)
	}
	return p, ok
}

// Provide stores values under secret_id as ref names and returns pending if any.
func (s *Store) Provide(remoteSessionID, secretID string, values map[string]string) (PendingSecret, error) {
	if len(values) == 0 {
		return PendingSecret{}, fmt.Errorf("secret values are required")
	}
	for name, value := range values {
		if name == "" || value == "" {
			return PendingSecret{}, fmt.Errorf("secret names and values must be non-empty")
		}
	}
	p, ok := s.TakePending(secretID)
	if !ok {
		return PendingSecret{}, fmt.Errorf("unknown secret_id")
	}
	if p.RemoteSessionID != remoteSessionID {
		// put back
		s.mu.Lock()
		s.pending[secretID] = p
		if s.db != nil {
			_, _ = s.db.Exec(`UPDATE secret_requests SET status = 'pending', consumed_at = NULL WHERE id = ?`, secretID)
		}
		s.mu.Unlock()
		return PendingSecret{}, fmt.Errorf("secret belongs to another Remote Session")
	}
	for k, v := range values {
		s.Set(remoteSessionID, k, v, true, s.defaultTTL)
		if k == "password" {
			s.Set(remoteSessionID, "ssh_password", v, true, s.defaultTTL)
		}
	}
	return p, nil
}

// Cache stores explicitly provided values in memory for a Remote Session.
func (s *Store) Cache(remoteSessionID string, values map[string]string) []string {
	refs := make([]string, 0, len(values))
	for ref, value := range values {
		if ref == "" || value == "" {
			continue
		}
		s.Set(remoteSessionID, ref, value, true, s.defaultTTL)
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}
