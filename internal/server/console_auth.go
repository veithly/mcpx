package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/oauth"
	"mcpx/internal/webui"
)

const consoleRoot = "/mcp/app/"
const consoleAPI = consoleRoot + "api/"
const consoleCookie = "mcpx_operator"

type operatorSession struct {
	CSRF    string
	Expires time.Time
}
type loginWindow struct {
	Started time.Time
	Count   int
}
type consoleHandler struct {
	runtime  *Runtime
	mu       sync.Mutex
	sessions map[string]operatorSession
	attempts map[string]loginWindow
	streams  int
}

func (r *Runtime) consoleHandler() http.Handler {
	c := &consoleHandler{runtime: r, sessions: map[string]operatorSession{}, attempts: map[string]loginWindow{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+consoleAPI+"session", c.sessionInfo)
	mux.HandleFunc("POST "+consoleAPI+"session", c.login)
	mux.HandleFunc("DELETE "+consoleAPI+"session", c.require(c.logout))
	mux.HandleFunc("GET "+consoleAPI+"state", c.require(c.state))
	mux.HandleFunc("POST "+consoleAPI+"sidebar", c.require(c.sidebar))
	mux.HandleFunc("GET "+consoleAPI+"fs", c.require(c.browse))
	mux.HandleFunc("POST "+consoleAPI+"workspaces", c.require(c.addWorkspace))
	mux.HandleFunc("PUT "+consoleAPI+"access", c.require(c.setAccess))
	mux.HandleFunc("GET "+consoleAPI+"detail", c.require(c.detail))
	mux.HandleFunc("GET "+consoleAPI+"events", c.require(c.events))
	mux.HandleFunc("GET "+consoleAPI+"stream", c.require(c.stream))
	mux.HandleFunc("GET "+consoleAPI+"logs", c.require(c.logs))
	mux.HandleFunc("POST "+consoleAPI+"requests", c.require(c.sendRequest))
	mux.HandleFunc("POST "+consoleAPI+"approvals", c.require(c.decideApproval))
	mux.Handle(consoleRoot, http.StripPrefix(consoleRoot, webui.Handler()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, consoleAPI) {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}

func consoleJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func consoleError(w http.ResponseWriter, status int, message string) {
	consoleJSON(w, status, map[string]string{"error": message})
}
func consoleDecode(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON content type required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("invalid JSON request")
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func randomConsoleToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func equalSecret(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func localConsoleRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	host = r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
func (c *consoleHandler) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == oauth.OriginFromRequest(r, c.runtime.cfg.Server.TrustProxyHeaders)
}
func (c *consoleHandler) authorized(r *http.Request) (operatorSession, bool) {
	if config.EffectiveAuthMode(c.runtime.cfg.Auth) == "open" && !localConsoleRequest(r) {
		return operatorSession{}, false
	}
	cookie, err := r.Cookie(consoleCookie)
	if err != nil {
		return operatorSession{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[cookie.Value]
	if ok && !time.Now().Before(session.Expires) {
		delete(c.sessions, cookie.Value)
		ok = false
	}
	return session, ok
}
func (c *consoleHandler) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := c.authorized(r)
		if !ok {
			consoleError(w, 401, "请重新登录控制台")
			return
		}
		if !c.originOK(r) {
			consoleError(w, 403, "cross-origin console access rejected")
			return
		}
		if r.Method != "GET" && !equalSecret(r.Header.Get("X-MCPX-CSRF"), session.CSRF) {
			consoleError(w, 403, "invalid CSRF token")
			return
		}
		next(w, r)
	}
}
func (c *consoleHandler) sessionInfo(w http.ResponseWriter, r *http.Request) {
	if !c.originOK(r) {
		consoleError(w, 403, "cross-origin console access rejected")
		return
	}
	session, ok := c.authorized(r)
	data := map[string]any{"authenticated": ok, "auth_mode": config.EffectiveAuthMode(c.runtime.cfg.Auth), "local": localConsoleRequest(r)}
	if ok {
		data["csrf"] = session.CSRF
		data["expires_at"] = session.Expires
	}
	consoleJSON(w, 200, data)
}
func (c *consoleHandler) login(w http.ResponseWriter, r *http.Request) {
	if !c.originOK(r) || r.Header.Get("X-MCPX-Console") != "1" {
		consoleError(w, 403, "same-origin console login required")
		return
	}
	var input struct {
		Credential string `json:"credential"`
	}
	if err := consoleDecode(w, r, &input); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	c.mu.Lock()
	for key, entry := range c.attempts {
		if now.Sub(entry.Started) > 5*time.Minute {
			delete(c.attempts, key)
		}
	}
	window, knownHost := c.attempts[host]
	if !knownHost && len(c.attempts) >= 1024 {
		c.mu.Unlock()
		w.Header().Set("Retry-After", "300")
		consoleError(w, 429, "登录尝试过多，请稍后重试")
		return
	}
	if window.Started.IsZero() {
		window.Started = now
	}
	window.Count++
	c.attempts[host] = window
	limited := window.Count > 10
	c.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "300")
		consoleError(w, 429, "登录尝试过多，请稍后重试")
		return
	}
	mode := config.EffectiveAuthMode(c.runtime.cfg.Auth)
	ok := mode == "open" && localConsoleRequest(r)
	if (mode == "bearer" || mode == "dual") && equalSecret(input.Credential, c.runtime.cfg.Auth.Token) {
		ok = true
	}
	if (mode == "oauth" || mode == "dual") && c.runtime.oauth != nil && c.runtime.oauth.CheckPassword(input.Credential) {
		ok = true
	}
	if !ok {
		consoleError(w, 401, "口令或令牌不正确；open 模式仅允许本机访问")
		return
	}
	token, err := randomConsoleToken()
	if err != nil {
		consoleError(w, 500, "cannot create login session")
		return
	}
	csrf, err := randomConsoleToken()
	if err != nil {
		consoleError(w, 500, "cannot create login session")
		return
	}
	session := operatorSession{CSRF: csrf, Expires: now.Add(12 * time.Hour)}
	c.mu.Lock()
	for key, s := range c.sessions {
		if !now.Before(s.Expires) {
			delete(c.sessions, key)
		}
	}
	if len(c.sessions) >= 64 {
		c.mu.Unlock()
		consoleError(w, 429, "too many active operator sessions")
		return
	}
	if old, err := r.Cookie(consoleCookie); err == nil {
		delete(c.sessions, old.Value)
	}
	c.sessions[token] = session
	delete(c.attempts, host)
	c.mu.Unlock()
	secure := strings.HasPrefix(oauth.OriginFromRequest(r, c.runtime.cfg.Server.TrustProxyHeaders), "https://")
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Value: token, Path: consoleRoot, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
	consoleJSON(w, 200, map[string]any{"authenticated": true, "csrf": csrf, "expires_at": session.Expires})
}
func (c *consoleHandler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(consoleCookie); err == nil {
		c.mu.Lock()
		delete(c.sessions, cookie.Value)
		c.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Path: consoleRoot, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	consoleJSON(w, 200, map[string]bool{"authenticated": false})
}
