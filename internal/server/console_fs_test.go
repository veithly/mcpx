package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/nativeui"
)

func TestConsoleNativeFolderSelectionReturnsExactPathAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目 with spaces")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		err        error
		want       int
		cancelled  bool
	}{
		{"selected", path, nil, 200, false}, {"cancelled", "", nativeui.ErrCancelled, 200, true}, {"unavailable", "", errors.New("no desktop"), 503, false}, {"relative", "relative", nil, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &consoleHandler{nativePicker: func(context.Context) (string, error) { return tc.path, tc.err }}
			req := httptest.NewRequest("POST", consoleAPI+"native/folder", strings.NewReader(`{"confirm":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c.chooseNativeFolder(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%d: %s", rec.Code, rec.Body.String())
			}
			if rec.Code == 200 {
				var out struct {
					Path      string
					Cancelled bool
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Cancelled != tc.cancelled || (!out.Cancelled && out.Path != path) {
					t.Fatalf("response=%+v", out)
				}
			}
		})
	}
}
func TestConsoleNativeDialogIsExplicitAndAuthenticated(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	anonymous := &consoleTestClient{handler: rt.consoleHandler()}
	if res := anonymous.request(t, "POST", "native/folder", map[string]any{"confirm": true}); res.Code != 401 {
		t.Fatalf("anonymous=%d", res.Code)
	}
	c := consoleLogin(t, rt)
	if res := c.request(t, "POST", "native/folder", map[string]any{}); res.Code != 400 {
		t.Fatalf("unsolicited=%d", res.Code)
	}
	c.csrf = "invalid"
	if res := c.request(t, "POST", "native/folder", map[string]any{"confirm": true}); res.Code != 403 {
		t.Fatalf("csrf=%d", res.Code)
	}
	if res := c.request(t, "GET", "fs?path=/", nil); res.Code != 404 {
		t.Fatalf("obsolete directory enumeration route=%d", res.Code)
	}
}
func TestSelectedDirectoryValidationDoesNotBrowse(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not inspected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSelectedDirectory(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", file, filepath.Join(root, "missing")} {
		if validateSelectedDirectory(path) == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
