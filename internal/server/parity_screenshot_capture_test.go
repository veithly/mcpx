package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpx/internal/remotesession"
	"mcpx/internal/screenshot"
)

type parityScreenshotCapturer func(context.Context, screenshot.Request) (screenshot.Result, error)

func (f parityScreenshotCapturer) Capture(ctx context.Context, r screenshot.Request) (screenshot.Result, error) {
	return f(ctx, r)
}

func TestParityscreenshot_captureProtocol(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	principal, err := rt.principalFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := rt.reg.Get("demo")
	created, err := rt.remote.Create(ctx, principal, remotesession.CreateInput{WorkspaceName: "demo", WorkspacePath: ws.Path})
	if err != nil {
		t.Fatal(err)
	}
	protocol := mcp.NewServer(&mcp.Implementation{Name: "parity-screenshot", Version: "test"}, nil)
	rt.registerTools(protocol)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return protocol }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "parity", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(extra map[string]any) *mcp.CallToolResult {
		t.Helper()
		args := map[string]any{"remote_session_id": created.Session.ID, "purpose": "validate synthetic screenshot fixture"}
		for k, v := range extra {
			args[k] = v
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "screenshot_capture", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	// A zero Service has no native capturer: any validation bypass panics instead of accessing a screen.
	rt.screenshot = &screenshot.Service{}
	for _, args := range []map[string]any{{"purpose": ""}, {"mode": "window"}, {"mode": "region", "width": 0, "height": 10}, {"display": -1}, {"format": "gif"}, {"quality": 101}, {"max_width": 16385}} {
		res := call(args)
		if !res.IsError {
			t.Fatalf("invalid input accepted: %v", args)
		}
		wire := res.StructuredContent.(map[string]any)
		failure := wire["error"].(map[string]any)
		if failure["category"] != "validation" {
			t.Errorf("invalid screenshot arguments misclassified: %+v", failure)
		}
		t.Logf("invalid=%v result=%v", args, res.StructuredContent)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	calls := 0
	rt.screenshot = parityScreenshotCapturer(func(_ context.Context, req screenshot.Request) (screenshot.Result, error) {
		calls++
		t.Logf("mock call=%d display=%d mode=%q", calls, req.Display, req.Mode)
		if calls == 1 {
			return screenshot.Result{}, errors.New("synthetic capture failure")
		}
		return screenshot.Result{Data: data.Bytes(), Metadata: screenshot.Metadata{Mode: "fullscreen", Format: "png", MIMEType: "image/png", OutputWidth: 2, OutputHeight: 2, Bytes: data.Len()}}, nil
	})
	failed := call(nil)
	if !failed.IsError {
		t.Fatal("expected mock failure")
	}
	t.Logf("failure=%v", failed.StructuredContent)
	success := call(nil)
	if success.IsError {
		t.Fatalf("recovery: %v", success.StructuredContent)
	}
	images := 0
	for _, content := range success.Content {
		if img, ok := content.(*mcp.ImageContent); ok {
			images++
			if img.MIMEType != "image/png" || !bytes.Equal(img.Data, data.Bytes()) {
				t.Fatal("image altered")
			}
		}
	}
	if images != 1 || calls != 2 {
		t.Fatalf("images=%d calls=%d", images, calls)
	}
	t.Logf("recovery images=%d calls=%d", images, calls)
	// Reject fractional display indices at the public boundary, before capture.
	fraction, fractionErr := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "screenshot_capture",
		Arguments: map[string]any{
			"remote_session_id": created.Session.ID,
			"purpose":           "validate synthetic screenshot fixture",
			"display":           -0.5,
		},
	})
	rejected := fractionErr != nil || fraction != nil && fraction.IsError
	if !rejected || calls != 2 {
		t.Fatalf("display=-0.5 must be rejected before capture: rejected=%t calls=%d want=2 protocol_error=%v", rejected, calls, fractionErr)
	}
	t.Logf("fractional display rejected; capturer calls=%d", calls)
}
