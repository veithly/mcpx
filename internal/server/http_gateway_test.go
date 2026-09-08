package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/arc"
	"mcpx/internal/mcpresult"

	"mcpx/internal/config"
	"mcpx/internal/oauth"
)

func TestGatewayBearer401AndOK(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "static-secret"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	gw := NewGateway(cfg, nil, inner)
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("WWW-Authenticate: %s", res.Header.Get("WWW-Authenticate"))
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer static-secret")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res2.Body)
		t.Fatalf("status %d body %s", res2.StatusCode, b)
	}
}

func TestGatewayDoesNotExposeLegacyActivityHTTPRoute(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ts := httptest.NewServer(NewGateway(cfg, nil, inner).Handler())
	defer ts.Close()

	response, err := http.Post(ts.URL+"/mcp/activity", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy /mcp/activity status=%d, want 404", response.StatusCode)
	}
}

func TestGatewayStreamableActionDiscovery(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "static-secret"

	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	protocol.AddTool(&mcp.Tool{Name: "remote_session_list", InputSchema: map[string]any{"type": "object"}, OutputSchema: arc.OutputSchema()}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("ok"), nil
	})
	protocol.AddTool(&mcp.Tool{Name: "screenshot_capture", InputSchema: map[string]any{"type": "object"}, OutputSchema: arc.OutputSchema()}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcpresult.NewText("ok"), nil
	})

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true, Stateless: true})
	gw := NewGateway(cfg, nil, streamable)
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpClient := &http.Client{Transport: roundTripperWithAuth(http.DefaultTransport, "Bearer static-secret")}
	client := mcp.NewClient(&mcp.Implementation{Name: "streamable-discovery-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatalf("tools/list result=%+v err=%v", listed, err)
	}
	for _, tool := range listed.Tools {
		if tool.OutputSchema == nil {
			t.Fatalf("tools/list must expose OutputSchema: %s", tool.Name)
		}
		encoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("marshal output schema %s: %v", tool.Name, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil || schema["$id"] != "urn:mcpx:structured-content:v2.0" {
			t.Fatalf("invalid output schema %s: %s", tool.Name, encoded)
		}
	}
}

func TestStreamableDiscoverIncludes20260728WhenStateless(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "bearer"
	cfg.Auth.Token = "static-secret"
	protocol := mcp.NewServer(&mcp.Implementation{Name: "mcpx-test", Version: "0.1.0"}, nil)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true,
		Stateless:                  true,
	})
	ts := httptest.NewServer(NewGateway(cfg, nil, streamable).Handler())
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":"d1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"probe","version":"0.1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer static-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "server/discover")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", res.StatusCode, raw)
	}
	// SSE or JSON
	text := string(raw)
	if !strings.Contains(text, "2026-07-28") {
		t.Fatalf("discover must advertise 2026-07-28 when Stateless: %s", text)
	}
	if !strings.Contains(text, `"resultType"`) && !strings.Contains(text, "supportedVersions") {
		t.Fatalf("discover missing supportedVersions: %s", text)
	}
}

func TestGatewayRemovedCompatibilityRoutes(t *testing.T) {
	secret := make([]byte, 32)
	osrv := oauth.NewServer("op-pass", "", secret, 3600)
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "open"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(NewGateway(cfg, osrv, inner).Handler())
	defer ts.Close()

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/sse"},
		{http.MethodPost, "/message"},
		{http.MethodGet, "/mcp/sse"},
		{http.MethodPost, "/mcp/message"},
		{http.MethodPost, "/oauth/register"},
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodPost, "/oauth/token"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, ts.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", res.StatusCode)
			}
		})
	}
}

func TestGatewayOAuthMetadataUsesConfiguredIssuer(t *testing.T) {
	secret := make([]byte, 32)
	osrv := oauth.NewServer("op-pass", "https://mcp.example.com", secret, 3600)
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "oauth"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(NewGateway(cfg, osrv, inner).Handler())
	defer server.Close()

	response, err := http.Get(server.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	var resource map[string]any
	if err := json.NewDecoder(response.Body).Decode(&resource); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if resource["resource"] != "https://mcp.example.com/mcp" {
		t.Fatalf("resource metadata = %+v", resource)
	}

	response, err = http.Get(server.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if metadata["issuer"] != "https://mcp.example.com" || metadata["registration_endpoint"] != "https://mcp.example.com/mcp/oauth/register" || metadata["token_endpoint"] != "https://mcp.example.com/mcp/oauth/token" {
		t.Fatalf("authorization metadata = %+v", metadata)
	}
}

func TestGatewayOAuthFlow(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 3)
	}
	osrv := oauth.NewServer("op-pass", "", secret, 3600)
	cfg := config.DefaultConfig()
	cfg.Auth.Mode = "oauth"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mcp-ok"))
	})
	gw := NewGateway(cfg, osrv, inner)
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// metadata
	res, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("prm %d", res.StatusCode)
	}

	// AS metadata must advertise registration_endpoint (RFC7591 discovery)
	res, err = http.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var asMeta map[string]any
	_ = json.NewDecoder(res.Body).Decode(&asMeta)
	res.Body.Close()
	regEP, _ := asMeta["registration_endpoint"].(string)
	if !strings.Contains(regEP, "/mcp/oauth/register") {
		t.Fatalf("registration_endpoint=%v meta=%+v", regEP, asMeta)
	}

	// DCR via /mcp/oauth/register (primary path)
	body := `{"redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"none","client_name":"test"}`
	res, err = http.Post(ts.URL+"/mcp/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	clientID, _ := reg["client_id"].(string)
	if clientID == "" {
		t.Fatalf("register: %+v", reg)
	}

	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	form := url.Values{}
	form.Set("password", "op-pass")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1/cb")
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", "xyz")
	form.Set("resource", ts.URL+"/mcp")
	form.Set("scope", "mcp")
	// don't follow redirect
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err = client.PostForm(ts.URL+"/mcp/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("authorize %d %s", res.StatusCode, b)
	}
	loc := res.Header.Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}

	tokForm := url.Values{}
	tokForm.Set("grant_type", "authorization_code")
	tokForm.Set("code", code)
	tokForm.Set("redirect_uri", "http://127.0.0.1/cb")
	tokForm.Set("client_id", clientID)
	tokForm.Set("code_verifier", verifier)
	tokForm.Set("resource", ts.URL+"/mcp")
	res, err = http.PostForm(ts.URL+"/mcp/oauth/token", tokForm)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var tokResp map[string]any
	_ = json.NewDecoder(res.Body).Decode(&tokResp)
	access, _ := tokResp["access_token"].(string)
	if access == "" {
		t.Fatalf("token: %+v", tokResp)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+access)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(b) != "mcp-ok" {
		t.Fatalf("mcp %d %s", res.StatusCode, b)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s, tr := TruncateUTF8("hello世界", 7)
	if !tr || len(s) > 7 {
		t.Fatalf("%q tr=%v", s, tr)
	}
}
