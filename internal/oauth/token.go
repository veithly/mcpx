package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	CodeTTLSeconds         = 300
	MaxPendingCodes        = 256
	RefreshTokenTTLSeconds = 30 * 24 * 3600
	MaxRefreshTokens       = 4096
	DefaultScope           = "mcp"
)

// Server holds process-local OAuth state.
type Server struct {
	Password    string
	ServerURL   string // configured origin; may be empty until request
	TokenSecret []byte
	TokenTTL    int // seconds
	Registry    *Registry
	CIMD        *CIMDResolver // Client ID Metadata Documents (ChatGPT / OpenAI)

	mu      sync.Mutex
	codes   map[string]*authCode
	refresh map[string]*refreshGrant
	seen    map[[32]byte]time.Time
}

type authCode struct {
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
	ExpiresAt           time.Time
}

// refreshGrant is a long-lived opaque refresh token bound to a client.
type refreshGrant struct {
	ClientID  string
	Resource  string
	Scope     string
	ExpiresAt time.Time
}

// NewServer builds an OAuth server. tokenSecret must be non-empty.
func NewServer(password, serverURL string, tokenSecret []byte, tokenTTL int) *Server {
	if tokenTTL <= 0 {
		tokenTTL = 604800
	}
	return &Server{
		Password:    password,
		ServerURL:   trimSlash(serverURL),
		TokenSecret: tokenSecret,
		TokenTTL:    tokenTTL,
		Registry:    NewRegistry(),
		CIMD:        NewCIMDResolver(),
		codes:       map[string]*authCode{},
		refresh:     map[string]*refreshGrant{},
		seen:        map[[32]byte]time.Time{},
	}
}

// ResolveClient returns a DCR/preregistered client or a CIMD-backed client.
func (s *Server) ResolveClient(clientID string) (*Client, error) {
	if c, ok := s.Registry.Get(clientID); ok {
		return c, nil
	}
	if s.CIMD != nil && IsClientIDMetadataURL(clientID) {
		return s.CIMD.Resolve(clientID)
	}
	return nil, fmt.Errorf("unknown client")
}

// AcceptsClientRedirect checks redirect_uri for DCR or CIMD clients.
func (s *Server) AcceptsClientRedirect(clientID, redirectURI string) bool {
	if s.Registry.AcceptsRedirect(clientID, redirectURI) {
		return true
	}
	if s.CIMD == nil || !IsClientIDMetadataURL(clientID) {
		return false
	}
	c, err := s.CIMD.Resolve(clientID)
	if err != nil || c == nil {
		return false
	}
	for _, u := range c.RedirectURIs {
		if u == redirectURI {
			return true
		}
	}
	// ChatGPT production callback is path-scoped: /connector/oauth/{id}.
	// Public CIMD may only list the legacy redirect; also accept same-origin
	// connector OAuth callbacks under chatgpt.com / chatgpt.openai.com.
	if allowsChatGPTConnectorRedirect(c, redirectURI) {
		return true
	}
	return false
}

// AuthenticatesClient validates token-endpoint client auth for DCR or CIMD.
func (s *Server) AuthenticatesClient(clientID, clientSecret, authMethod string) bool {
	if s.Registry.Authenticates(clientID, clientSecret, authMethod) {
		return true
	}
	if s.CIMD == nil || !IsClientIDMetadataURL(clientID) {
		return false
	}
	c, err := s.CIMD.Resolve(clientID)
	if err != nil || c == nil {
		return false
	}
	// Force public-client path for CIMD when method is none (AS policy).
	if authMethod == "" {
		authMethod = "none"
	}
	if c.TokenEndpointAuthMethod != authMethod {
		// Allow none when CIMD prefers private_key_jwt but AS only offers none.
		if authMethod == "none" && clientSecret == "" {
			return true
		}
		return false
	}
	if c.TokenEndpointAuthMethod == "none" {
		return clientSecret == ""
	}
	return false
}

func allowsChatGPTConnectorRedirect(c *Client, redirectURI string) bool {
	if c == nil {
		return false
	}
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme != "https" || u.Fragment != "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "chatgpt.com" && host != "www.chatgpt.com" && host != "chat.openai.com" {
		return false
	}
	path := u.EscapedPath()
	if path == "/connector_platform_oauth_redirect" {
		return true
	}
	// /connector/oauth/{callback_id}
	if strings.HasPrefix(path, "/connector/oauth/") && len(path) > len("/connector/oauth/") {
		return true
	}
	return false
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// IssueCode creates a one-time authorization code after password approval.
func (s *Server) IssueCode(clientID, redirectURI, challenge, method, resource, scope string) (string, error) {
	if method != "" && method != "S256" {
		return "", fmt.Errorf("only S256 PKCE is supported")
	}
	if !ValidChallenge(challenge) {
		return "", fmt.Errorf("invalid code_challenge")
	}
	if !s.AcceptsClientRedirect(clientID, redirectURI) {
		return "", fmt.Errorf("invalid redirect_uri")
	}
	if scope == "" {
		scope = DefaultScope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneCodesLocked()
	if len(s.codes) >= MaxPendingCodes {
		return "", fmt.Errorf("too many pending authorization codes")
	}
	code := TokenURLSafe(32)
	s.codes[code] = &authCode{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Resource:            resource,
		Scope:               scope,
		ExpiresAt:           time.Now().Add(CodeTTLSeconds * time.Second),
	}
	return code, nil
}

// CheckPassword constant-time compares operator password.
func (s *Server) CheckPassword(got string) bool {
	return subtle.ConstantTimeCompare([]byte(s.Password), []byte(got)) == 1
}

// ExchangeCode validates code+PKCE and returns an access token JWT.
func (s *Server) ExchangeCode(code, redirectURI, clientID, codeVerifier, resource string) (string, int, error) {
	s.mu.Lock()
	ac, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	s.mu.Unlock()
	if !ok {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if time.Now().After(ac.ExpiresAt) {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if ac.ClientID != clientID || ac.RedirectURI != redirectURI {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	if !VerifyS256(codeVerifier, ac.CodeChallenge) {
		return "", 0, fmt.Errorf("invalid_grant")
	}
	res := ac.Resource
	if resource != "" {
		res = resource
	}
	issuer := s.EffectiveIssuer("")
	if issuer == "" {
		issuer = issuerFromResource(res)
	}
	if res == "" {
		res = s.ResourceURL(issuer)
	}
	if issuer == "" {
		return "", 0, fmt.Errorf("cannot determine token issuer; set auth.oauth.server_url")
	}
	tok, err := s.CreateAccessToken(clientID, res, issuer)
	if err != nil {
		return "", 0, err
	}
	return tok, s.TokenTTL, nil
}

// IssueRefreshToken creates an opaque refresh token for the client/resource.
func (s *Server) IssueRefreshToken(clientID, resource, scope string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRefreshLocked()
	if len(s.refresh) >= MaxRefreshTokens {
		// Evict the oldest grant so a busy deployment cannot block reconnects.
		var oldestKey string
		var oldest time.Time
		for k, g := range s.refresh {
			if oldestKey == "" || g.ExpiresAt.Before(oldest) {
				oldestKey, oldest = k, g.ExpiresAt
			}
		}
		if oldestKey != "" {
			delete(s.refresh, oldestKey)
		}
	}
	if scope == "" {
		scope = DefaultScope
	}
	tok := TokenURLSafe(32)
	for s.refresh[tok] != nil {
		tok = TokenURLSafe(32)
	}
	s.refresh[tok] = &refreshGrant{
		ClientID:  clientID,
		Resource:  resource,
		Scope:     scope,
		ExpiresAt: time.Now().Add(RefreshTokenTTLSeconds * time.Second),
	}
	return tok
}

// ExchangeRefreshToken rotates a refresh token and mints a new access token.
func (s *Server) ExchangeRefreshToken(refreshToken, clientID, resource string) (string, int, string, error) {
	s.mu.Lock()
	g, ok := s.refresh[refreshToken]
	if ok {
		delete(s.refresh, refreshToken)
	}
	s.mu.Unlock()
	if !ok || time.Now().After(g.ExpiresAt) {
		return "", 0, "", fmt.Errorf("invalid_grant")
	}
	if g.ClientID != clientID {
		return "", 0, "", fmt.Errorf("invalid_grant")
	}
	res := g.Resource
	if resource != "" {
		res = resource
	}
	issuer := s.EffectiveIssuer("")
	if issuer == "" {
		issuer = issuerFromResource(res)
	}
	if res == "" {
		res = s.ResourceURL(issuer)
	}
	tok, err := s.CreateAccessToken(clientID, res, issuer)
	if err != nil {
		return "", 0, "", err
	}
	next := s.IssueRefreshToken(clientID, res, g.Scope)
	return tok, s.TokenTTL, next, nil
}

func issuerFromResource(resource string) string {
	resource = trimSlash(resource)
	if strings.HasSuffix(resource, "/mcp") {
		return trimSlash(strings.TrimSuffix(resource, "/mcp"))
	}
	return resource
}

// CreateAccessToken issues HS256 JWT.
func (s *Server) CreateAccessToken(clientID, audience, issuer string) (string, error) {
	if len(s.TokenSecret) == 0 {
		return "", fmt.Errorf("token secret not configured")
	}
	if issuer == "" {
		issuer = s.EffectiveIssuer("")
	}
	if audience == "" {
		audience = s.ResourceURL(issuer)
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":       issuer,
		"aud":       audience,
		"sub":       clientID,
		"client_id": clientID,
		"iat":       now.Unix(),
		"exp":       now.Add(time.Duration(s.TokenTTL) * time.Second).Unix(),
		"scope":     DefaultScope,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString(s.TokenSecret)
}

// ValidateAccessToken checks JWT signature, exp, iss, aud, and live client.
func (s *Server) ValidateAccessToken(token, issuer, audience string) bool {
	_, ok := s.ValidateAccessTokenIdentity(token, issuer, audience)
	return ok
}

// ValidateAccessTokenIdentity validates a token and returns its stable subject.
// Signature, issuer, and audience are enforced exactly as before; expiration is
// enforced through the sliding window below, so a client in active use never
// has to re-authenticate while an abandoned token expires on schedule.
func (s *Server) ValidateAccessTokenIdentity(token, issuer, audience string) (string, bool) {
	if token == "" || len(s.TokenSecret) == 0 {
		return "", false
	}
	// WithoutClaimsValidation disables the parser's time checks; iss/aud/exp
	// are validated manually so expiration can slide on activity.
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}), jwt.WithoutClaimsValidation()).Parse(token, func(t *jwt.Token) (any, error) {
		return s.TokenSecret, nil
	})
	if err != nil || !parsed.Valid {
		return "", false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}
	gotIssuer, err := claims.GetIssuer()
	if err != nil || gotIssuer == "" || gotIssuer != issuer {
		return "", false
	}
	aud, err := claims.GetAudience()
	if err != nil {
		return "", false
	}
	audOK := false
	for _, a := range aud {
		if a == audience {
			audOK = true
			break
		}
	}
	if !audOK {
		return "", false
	}
	cid, _ := claims["client_id"].(string)
	if cid == "" {
		cid, _ = claims["sub"].(string)
	}
	if cid == "" {
		return "", false
	}
	// Accept DCR clients and CIMD clients (ChatGPT uses HTTPS client_id URLs).
	// Requiring Registry.Get only caused 401 after successful OAuth for CIMD.
	if _, err := s.ResolveClient(cid); err != nil {
		return "", false
	}
	return cid, s.slideTokenLifetime(token, claims)
}

// slideTokenLifetime enforces token expiry as a sliding window: every accepted
// validation re-anchors the window at the current time, so active clients keep
// working indefinitely while an idle token is rejected once its last use plus
// TokenTTL has passed. Reports whether the token remains valid.
func (s *Server) slideTokenLifetime(token string, claims jwt.MapClaims) bool {
	window := time.Duration(s.TokenTTL) * time.Second
	now := time.Now()
	exp, err := claims.GetExpirationTime()
	expired := err != nil || (exp != nil && !now.Before(exp.Time))
	if !expired {
		s.recordTokenSeen(token, now, window)
		return true
	}
	if window <= 0 {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSeenLocked(now, window)
	last, seen := s.seen[digest]
	if seen && last.Add(window).Before(now) {
		delete(s.seen, digest)
		return false
	}
	if seen {
		s.seen[digest] = now
		return true
	}
	// Expired with no recorded use (e.g. fresh process): reject without
	// recording, so a leaked expired token cannot bootstrap sliding state.
	return false
}

func (s *Server) recordTokenSeen(token string, now time.Time, window time.Duration) {
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSeenLocked(now, window)
	if len(s.seen) >= MaxRefreshTokens {
		return
	}
	s.seen[digest] = now
}

func (s *Server) pruneSeenLocked(now time.Time, window time.Duration) {
	for k, last := range s.seen {
		if now.Sub(last) > window {
			delete(s.seen, k)
		}
	}
}

// EffectiveIssuer returns configured server URL or fallback origin.
func (s *Server) EffectiveIssuer(fallbackOrigin string) string {
	if s.ServerURL != "" {
		return s.ServerURL
	}
	return trimSlash(fallbackOrigin)
}

// ResourceURL is {issuer}/mcp.
func (s *Server) ResourceURL(issuer string) string {
	return trimSlash(issuer) + "/mcp"
}

func (s *Server) pruneCodesLocked() {
	now := time.Now()
	for k, v := range s.codes {
		if now.After(v.ExpiresAt) {
			delete(s.codes, k)
		}
	}
}

func (s *Server) pruneRefreshLocked() {
	now := time.Now()
	for k, v := range s.refresh {
		if now.After(v.ExpiresAt) {
			delete(s.refresh, k)
		}
	}
}
