package server

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parityArtifactHTTP(t *testing.T, content []byte) (func(map[string]any) *mcp.CallToolResult, string, string) {
	t.Helper()
	rt := newWorkspaceRuntime(t, "demo")
	remote := operationTestSession(t, rt, "demo")
	path := filepath.Join(remote.WorkspacePath, "report.txt")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-artifact", Version: "1"}, nil)
	rt.registerTools(protocol)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "parity-artifact-client", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		args["remote_session_id"] = remote.ID
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "artifact", Arguments: args})
		if err != nil {
			t.Fatalf("HTTP/protocol error: %v", err)
		}
		return result
	}
	registered := call(map[string]any{"action": "register", "path": "report.txt", "purpose": "verify isolated artifact", "idempotency_key": "artifact-fixture"})
	data := parityArtifactData(t, registered)
	id, _ := data["artifact_id"].(string)
	if id == "" {
		t.Fatalf("missing artifact_id: %+v", data)
	}
	foundLink := false
	for _, content := range registered.Content {
		if _, ok := content.(*mcp.ResourceLink); ok {
			foundLink = true
		}
	}
	if !foundLink {
		t.Fatal("registration missing resource link")
	}
	return call, id, path
}

func parityArtifactData(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	wire, ok := result.StructuredContent.(map[string]any)
	if !ok || result.IsError || wire["status"] != "succeeded" {
		t.Fatalf("unexpected tool result: %+v", result.StructuredContent)
	}
	data, ok := wire["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data: %+v", wire)
	}
	return data
}

func TestParityartifactRegisterAndPaging(t *testing.T) {
	call, id, _ := parityArtifactHTTP(t, []byte("abcdef"))
	listed := parityArtifactData(t, call(map[string]any{"action": "list", "limit": 10}))
	if items, ok := listed["artifacts"].([]any); !ok || len(items) != 1 {
		t.Fatalf("list: %+v", listed)
	}
	first := parityArtifactData(t, call(map[string]any{"action": "read", "artifact_id": id, "offset": 0, "limit": 3}))
	second := parityArtifactData(t, call(map[string]any{"action": "read", "artifact_id": id, "offset": first["next_source_offset"], "limit": 3}))
	t.Logf("page1 text=%q next=%v eof=%v; page2 text=%q next=%v eof=%v", first["text"], first["next_source_offset"], first["eof"], second["text"], second["next_source_offset"], second["eof"])
	if first["text"] != "abc" || first["next_source_offset"] != float64(3) || first["eof"] != false || second["text"] != "def" || second["eof"] != true {
		t.Fatal("pagination lost bytes")
	}
}

func TestParityartifactMissingAndDrift(t *testing.T) {
	call, id, path := parityArtifactHTTP(t, []byte("original"))
	for _, tc := range []struct{ id, message string }{{"art_missing", "artifact not found"}, {id, "artifact content changed after registration"}} {
		if tc.id == id {
			if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		result := call(map[string]any{"action": "read", "artifact_id": tc.id})
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("isError=%v result=%s", result.IsError, raw)
		if !result.IsError || !strings.Contains(string(raw), tc.message) {
			t.Fatalf("missing recoverable failure: %s", raw)
		}
	}
	parityArtifactData(t, call(map[string]any{"action": "list"}))
}

func TestParityartifactUTF16PaginationPreservesSurrogatePair(t *testing.T) {
	// UTF-16LE BOM followed by U+1F600 and A; the first four bytes end in a high surrogate.
	call, id, _ := parityArtifactHTTP(t, []byte{0xff, 0xfe, 0x3d, 0xd8, 0x00, 0xde, 0x41, 0x00})
	full := parityArtifactData(t, call(map[string]any{"action": "read", "artifact_id": id}))
	if full["text"] != "😀A" {
		t.Fatalf("fixture decode: %+v", full)
	}
	first := parityArtifactData(t, call(map[string]any{"action": "read", "artifact_id": id, "offset": 0, "limit": 4}))
	second := parityArtifactData(t, call(map[string]any{"action": "read", "artifact_id": id, "offset": first["next_source_offset"], "limit": 4}))
	t.Logf("full=%q page1=%q next=%v page2=%q next=%v eof=%v", full["text"], first["text"], first["next_source_offset"], second["text"], second["next_source_offset"], second["eof"])
	joined := first["text"].(string) + second["text"].(string)
	if joined != full["text"] {
		t.Fatalf("UTF-16 pagination corrupted text: got %q want %q", joined, full["text"])
	}
}
