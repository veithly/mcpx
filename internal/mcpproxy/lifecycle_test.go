package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/config"
)

// Test binary doubles as a real stdio MCP peer; never print testing output to
// its protocol stdout. Only the specifically selected child process enters it.
func TestMCPProcessFixture(t *testing.T) {
	mode := os.Getenv("MCPX_LIFECYCLE_TEST")
	if mode == "" {
		t.Skip("subprocess fixture")
	}
	if mode == "hang" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "lifecycle-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "alive"}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func fixtureServer(t *testing.T, mode string) config.MCPServer {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.MCPServer{Command: exe, Args: []string{"-test.run=^TestMCPProcessFixture$"}, Env: map[string]string{"MCPX_LIFECYCLE_TEST": mode}}
}
func TestMCPHandshakeDeadlineDoesNotKillConnectedProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop, err := connect(ctx, fixtureServer(t, "serve"), 500*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); _ = session.Close() }()
	time.Sleep(650 * time.Millisecond)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ping", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("connected process died at handshake deadline: %+v %v", result, err)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "alive" {
		t.Fatalf("unexpected peer result: %+v", result)
	}
}
func TestMCPUnresponsiveHandshakeReturnsDeadline(t *testing.T) {
	started := time.Now()
	session, stop, err := connect(context.Background(), fixtureServer(t, "hang"), 80*time.Millisecond, nil)
	defer stop()
	if session != nil {
		defer session.Close()
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing handshake timeout: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("handshake timeout did not bound connection")
	}
}
