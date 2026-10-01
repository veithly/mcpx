package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"

	"mcpx/internal/auth"
	"mcpx/internal/config"
)

const testOpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"

func TestOpenAITunnelPrincipalIsIsolatedFromHTTP(t *testing.T) {
	t.Setenv("MCPX_TEST_BEARER", "")
	rt := &Runtime{cfg: config.DefaultConfig()}
	rt.cfg.Auth = config.AuthConfig{Mode: "bearer", Token: "http-only-secret"}
	ctx := openAITunnelContext(context.Background(), testOpenAITunnelID)
	principal, err := rt.principalFromContext(ctx)
	if err != nil || principal.Kind != "openai_tunnel" {
		t.Fatalf("tunnel identity failed: %v", err)
	}
	again, _ := rt.principalFromContext(openAITunnelContext(context.Background(), testOpenAITunnelID))
	other, _ := rt.principalFromContext(openAITunnelContext(context.Background(), "another-tunnel"))
	if principal != again || principal.ID == other.ID {
		t.Fatal("principal must be stable and tunnel-scoped")
	}
	if _, err := rt.principalFromContext(context.Background()); err == nil {
		t.Fatal("ordinary context bypassed HTTP auth")
	}
	httpPrincipal, err := rt.principalFromContext(auth.ContextWithAuthorization(context.Background(), "Bearer http-only-secret"))
	if err != nil || httpPrincipal.ID == principal.ID {
		t.Fatal("HTTP and tunnel identities are not isolated")
	}
	gateway := NewGateway(rt.cfg, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).Handler()
	for _, token := range []string{"", "tunnel-key", "http-only-secret"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-OpenAI-Tunnel-ID", testOpenAITunnelID)
		req.Header.Set("X-MCPX-Principal", principal.ID)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		want := http.StatusUnauthorized
		if token == "http-only-secret" {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("HTTP status %d, want %d", response.Code, want)
		}
	}
}

func TestOpenAITunnelForwardsRealMCPXToolsAndCloses(t *testing.T) {
	rt := newWorkspaceRuntime(t, "tunnel-workspace")
	rt.cfg.Auth = config.AuthConfig{Mode: "bearer", Token: "local-http-only"}
	var discovered, called atomic.Bool
	command := func(id, payload string, check func(testing.TB, mocktunnelservice.ReceivedResponse)) mocktunnelservice.CommandResponse {
		return mocktunnelservice.CommandResponse{
			Command:           mocktunnelservice.NewCommand(id, json.RawMessage(payload), nil),
			ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: id, Assert: check}},
		}
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithTunnelID(testOpenAITunnelID),
		mocktunnelservice.WithAPIKey("fake-control-plane-key"),
		mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
		mocktunnelservice.WithCommandResponses(
			command("discover", `{"jsonrpc":"2.0","id":9,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"tunnel-probe","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				if response.ResponseCode != 200 || !bytes.Contains(response.JSONResponse, []byte(`"supportedVersions"`)) {
					tb.Errorf("server/discover failed over tunnel: %s", response.JSONResponse)
				}
			}),
			command("list", `{"jsonrpc":"2.0","id":10,"method":"tools/list","params":{}}`, func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				if response.ResponseCode != 200 || !bytes.Contains(response.JSONResponse, []byte(`"workspace"`)) {
					tb.Errorf("MCPX tools were not discovered: %s", response.JSONResponse)
				}
				discovered.Store(true)
			}),
			command("call", `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"workspace","arguments":{}}}`, func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				if response.ResponseCode != 200 || !bytes.Contains(response.JSONResponse, []byte(`"tunnel-workspace"`)) || bytes.Contains(response.JSONResponse, []byte(`"isError":true`)) {
					tb.Errorf("real workspace call failed: %s", response.JSONResponse)
				}
				called.Store(true)
			}),
		),
	)
	controlPlane.Start(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpx", Version: "test"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
	})
	rt.registerTools(server)
	tunnel, err := connectOpenAITunnel(server, tunnelclient.Config{
		TunnelID: testOpenAITunnelID, APIKey: "fake-control-plane-key",
		ControlPlaneBaseURL: controlPlane.BaseURL().String(), PollTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.openAITunnel = tunnel
	t.Cleanup(func() { _ = tunnel.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tunnel.client.WaitUntilReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controlPlane.WaitUntilIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if !discovered.Load() || !called.Load() {
		t.Fatal("protocol assertions did not run")
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := tunnel.client.Start(context.Background()); !errors.Is(err, tunnelclient.ErrClosed) {
		t.Fatalf("client did not stop: %v", err)
	}
}

func TestOpenAITunnelRetriesAndReadinessRequiresSuccessfulPoll(t *testing.T) {
	var polls atomic.Int32
	allow := make(chan struct{})
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("missing tunnel credential")
		}
		if strings.HasSuffix(req.URL.Path, "/poll") {
			polls.Add(1)
			select {
			case <-allow:
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer cp.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpx", Version: "test"}, nil)
	tunnel, err := connectOpenAITunnel(server, tunnelclient.Config{TunnelID: testOpenAITunnelID, APIKey: "fake-key", ControlPlaneBaseURL: cp.URL, PollTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := tunnel.client.WaitUntilReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unverified readiness: %v", err)
	}
	close(allow)
	readyCtx, cancelReady := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReady()
	if err := tunnel.client.WaitUntilReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	if polls.Load() < 2 {
		t.Fatal("transient failure was not retried")
	}
}

func TestOpenAITunnelDisabledAndIncompleteConfig(t *testing.T) {
	for _, key := range []string{"MCPX_OPENAI_TUNNEL_ID", "MCPX_OPENAI_TUNNEL_KEY"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	rt := &Runtime{cfg: config.DefaultConfig()}
	if err := rt.startOpenAITunnel(nil); err != nil || rt.openAITunnel != nil {
		t.Fatal("unconfigured tunnel started")
	}
	rt.cfg.OpenAITunnel.ID = testOpenAITunnelID
	if err := rt.startOpenAITunnel(nil); err == nil {
		t.Fatal("partial credentials accepted")
	}
}

func TestOpenAITunnelRedactsSDKDiagnostics(t *testing.T) {
	var output bytes.Buffer
	writer := &tunnelRedactingWriter{writer: &output, key: "private-key"}
	input := []byte("failure private-key private-key\n")
	if n, err := writer.Write(input); err != nil || n != len(input) {
		t.Fatalf("write: %d %v", n, err)
	}
	if strings.Contains(output.String(), "private-key") {
		t.Fatal("SDK log exposed key")
	}
	if err := redactTunnelError(errors.New("bad private-key"), "private-key"); strings.Contains(err.Error(), "private-key") {
		t.Fatal("error exposed key")
	}
}
