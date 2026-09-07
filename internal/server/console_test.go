package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpx/internal/remotesession"
)

type consoleTestClient struct {
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func (c *consoleTestClient) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "http://localhost"+consoleAPI+path, strings.NewReader(string(data)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MCPX-Console", "1")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-MCPX-CSRF", c.csrf)
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	return rec
}
func consoleLogin(t *testing.T, rt *Runtime) *consoleTestClient {
	t.Helper()
	c := &consoleTestClient{handler: rt.consoleHandler()}
	response := c.request(t, "POST", "session", map[string]string{"credential": ""})
	if response.Code != 200 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected operator cookie")
	}
	c.cookie = cookies[0]
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode || c.cookie.Path != consoleRoot {
		t.Fatalf("insecure cookie: %+v", c.cookie)
	}
	var data struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil || data.CSRF == "" {
		t.Fatal("missing CSRF token")
	}
	c.csrf = data.CSRF
	return c
}
func consoleRemote(t *testing.T, rt *Runtime, workspace string) string {
	t.Helper()
	p, err := rt.principalFromContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := rt.reg.Get(workspace)
	result, err := rt.remote.Create(context.Background(), p, remotesession.CreateInput{WorkspaceName: workspace, WorkspacePath: ws.Path, Label: "console test"})
	if err != nil {
		t.Fatal(err)
	}
	return result.Session.ID
}
func TestConsoleAuthenticationCSRFAndLogout(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	c := &consoleTestClient{handler: rt.consoleHandler()}
	if res := c.request(t, "GET", "state", nil); res.Code != 401 {
		t.Fatalf("anonymous state=%d", res.Code)
	}
	c = consoleLogin(t, rt)
	if res := c.request(t, "GET", "state", nil); res.Code != 200 {
		t.Fatalf("state=%d %s", res.Code, res.Body.String())
	}
	original := c.csrf
	c.csrf = "wrong"
	if res := c.request(t, "PUT", "access", map[string]any{"workspace": "alpha", "mode": "full_access", "confirm_full_access": true}); res.Code != 403 {
		t.Fatalf("invalid csrf=%d", res.Code)
	}
	c.csrf = original
	req := httptest.NewRequest("GET", "http://localhost"+consoleAPI+"session", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.AddCookie(c.cookie)
	req.Header.Set("Origin", "https://untrusted.example")
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("cross origin=%d", rec.Code)
	}
	if res := c.request(t, "DELETE", "session", nil); res.Code != 200 {
		t.Fatalf("logout=%d", res.Code)
	}
	if res := c.request(t, "GET", "state", nil); res.Code != 401 {
		t.Fatalf("old cookie remains valid=%d", res.Code)
	}
}
func TestConsoleOpenModeRejectsRemoteAndRebindingHosts(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	handler := rt.consoleHandler()
	for _, pair := range [][2]string{{"192.0.2.5:12345", "localhost"}, {"127.0.0.1:12345", "untrusted.example"}} {
		req := httptest.NewRequest("POST", "http://"+pair[1]+consoleAPI+"session", strings.NewReader(`{"credential":""}`))
		req.RemoteAddr = pair[0]
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MCPX-Console", "1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("remote=%s host=%s status=%d", pair[0], pair[1], rec.Code)
		}
	}
}
func TestConsoleBearerUsesMasterCredentialNotClientToken(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	rt.cfg.Auth.Mode = "bearer"
	rt.cfg.Auth.Token = "test-master-credential"
	c := &consoleTestClient{handler: rt.consoleHandler()}
	if res := c.request(t, "POST", "session", map[string]string{"credential": "ordinary-client-token"}); res.Code != 401 {
		t.Fatalf("client token accepted=%d", res.Code)
	}
	if res := c.request(t, "POST", "session", map[string]string{"credential": "test-master-credential"}); res.Code != 200 {
		t.Fatalf("master login=%d %s", res.Code, res.Body.String())
	}
}
func TestConsoleAccessRequiresOptInAndStaysInWorkspace(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	c := consoleLogin(t, rt)
	input := map[string]any{"workspace": "alpha", "mode": "full_access"}
	if res := c.request(t, "PUT", "access", input); res.Code != 400 {
		t.Fatalf("full access without opt-in=%d", res.Code)
	}
	input["confirm_full_access"] = true
	if res := c.request(t, "PUT", "access", input); res.Code != 200 {
		t.Fatalf("set access=%d %s", res.Code, res.Body.String())
	}
	if !rt.workspaceFullAccess(context.Background(), "alpha") || rt.workspaceFullAccess(context.Background(), "beta") {
		t.Fatal("workspace policy isolation failed")
	}
}
func TestConsoleRequestsRejectCrossWorkspaceSession(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha", "beta")
	sid := consoleRemote(t, rt, "alpha")
	c := consoleLogin(t, rt)
	input := map[string]any{"workspace": "beta", "session_id": sid, "kind": "steer", "body": "change direction", "client_key": "key"}
	if res := c.request(t, "POST", "requests", input); res.Code != 404 {
		t.Fatalf("cross-workspace request=%d", res.Code)
	}
	input["workspace"] = "alpha"
	if res := c.request(t, "POST", "requests", input); res.Code != 202 {
		t.Fatalf("queue=%d %s", res.Code, res.Body.String())
	}
	input["kind"] = "interrupt"
	input["session_id"] = ""
	input["client_key"] = "interrupt"
	if res := c.request(t, "POST", "requests", input); res.Code != 400 {
		t.Fatalf("unscoped stop accepted=%d", res.Code)
	}
	if res := c.request(t, "GET", "detail?workspace=beta&session_id="+sid, nil); res.Code != 404 {
		t.Fatalf("cross-workspace detail=%d", res.Code)
	}
}
