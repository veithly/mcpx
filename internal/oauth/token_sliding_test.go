package oauth

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newSlidingTestServer(t *testing.T) *Server {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 7)
	}
	s := NewServer("op-pass", "https://mcp.example.com", secret, 3600)
	if err := s.Registry.AddPreregistered("cli", []string{"http://127.0.0.1/cb"}, ""); err != nil {
		t.Fatal(err)
	}
	return s
}

// mintSignedToken signs a token with explicit claims so tests can pin exp
// without sleeping through real time.
func mintSignedToken(s *Server, clientID, audience, issuer string, exp time.Time) string {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": issuer, "aud": audience, "sub": clientID, "client_id": clientID,
		"iat": now.Unix(), "exp": exp.Unix(), "scope": DefaultScope,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.TokenSecret)
	if err != nil {
		panic(err)
	}
	return signed
}

const (
	testIssuer   = "https://mcp.example.com"
	testAudience = "https://mcp.example.com/mcp"
)

func TestSlidingValidationAcceptsExpiredTokenSeenRecently(t *testing.T) {
	s := newSlidingTestServer(t)
	token := mintSignedToken(s, "cli", testAudience, testIssuer, time.Now().Add(-time.Minute))
	digest := sha256.Sum256([]byte(token))
	// Simulate active use 30 minutes ago: lastSeen+1h window still covers now.
	s.mu.Lock()
	s.seen[digest] = time.Now().Add(-30 * time.Minute)
	s.mu.Unlock()
	if !s.ValidateAccessToken(token, testIssuer, testAudience) {
		t.Fatal("expired token seen within sliding window must validate")
	}
	// The validation must re-anchor the sliding window at the current time.
	s.mu.Lock()
	refreshed := s.seen[digest]
	s.mu.Unlock()
	if time.Since(refreshed) > time.Minute {
		t.Fatal("validation must re-anchor lastSeen")
	}
}

func TestSlidingValidationRejectsExpiredTokenNeverSeen(t *testing.T) {
	s := newSlidingTestServer(t)
	token := mintSignedToken(s, "cli", testAudience, testIssuer, time.Now().Add(-time.Minute))
	if s.ValidateAccessToken(token, testIssuer, testAudience) {
		t.Fatal("expired token with no prior use must be rejected")
	}
}

func TestSlidingValidationRejectsExpiredTokenAfterIdleWindow(t *testing.T) {
	s := newSlidingTestServer(t)
	token := mintSignedToken(s, "cli", testAudience, testIssuer, time.Now().Add(-2*time.Hour))
	digest := sha256.Sum256([]byte(token))
	// Last use 90 minutes ago: lastSeen+1h window no longer covers now.
	s.mu.Lock()
	s.seen[digest] = time.Now().Add(-90 * time.Minute)
	s.mu.Unlock()
	if s.ValidateAccessToken(token, testIssuer, testAudience) {
		t.Fatal("expired token idle beyond window must be rejected")
	}
	// The stale entry must be dropped, not resurrected by the failed check.
	s.mu.Lock()
	_, ok := s.seen[digest]
	s.mu.Unlock()
	if ok {
		t.Fatal("rejected sliding entry must be pruned")
	}
}

func TestUnexpiredTokenAlwaysValidates(t *testing.T) {
	s := newSlidingTestServer(t)
	token := mintSignedToken(s, "cli", testAudience, testIssuer, time.Now().Add(time.Hour))
	if !s.ValidateAccessToken(token, testIssuer, testAudience) {
		t.Fatal("unexpired token must validate")
	}
}
