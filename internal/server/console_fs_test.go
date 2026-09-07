package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConsoleBrowseListsDirectoriesOnly(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	root := filepath.Join(t.TempDir(), "picker")
	if err := os.MkdirAll(filepath.Join(root, "project-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".hidden-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := consoleLogin(t, rt)
	response := c.request(t, "GET", "fs?path="+root, nil)
	if response.Code != 200 {
		t.Fatalf("browse=%d %s", response.Code, response.Body.String())
	}
	var page struct {
		Path     string
		Parent   string
		Segments []map[string]string
		Entries  []struct{ Name, Path string }
		Home     string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Path != root {
		t.Fatalf("path=%q", page.Path)
	}
	if page.Parent == "" {
		t.Fatal("parent missing")
	}
	var names []string
	for _, entry := range page.Entries {
		names = append(names, entry.Name)
		if want := filepath.Join(root, entry.Name); entry.Path != want {
			t.Fatalf("entry path=%q want %q", entry.Path, want)
		}
	}
	// Visible directories first, hidden ones after, files never listed.
	if len(names) != 2 || names[0] != "project-a" || names[1] != ".hidden-dir" {
		t.Fatalf("entries=%v", names)
	}
	if len(page.Segments) == 0 || page.Segments[len(page.Segments)-1]["path"] != root {
		t.Fatalf("segments=%v", page.Segments)
	}
	if page.Home == "" {
		t.Fatal("home missing")
	}
}
func TestConsoleBrowseRejectsRelativeAndMissingPaths(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	c := consoleLogin(t, rt)
	if res := c.request(t, "GET", "fs?path=relative/path", nil); res.Code != 400 {
		t.Fatalf("relative=%d", res.Code)
	}
	if res := c.request(t, "GET", "fs?path="+filepath.Join(t.TempDir(), "missing"), nil); res.Code != 404 {
		t.Fatalf("missing=%d", res.Code)
	}
	anonymous := &consoleTestClient{handler: rt.consoleHandler()}
	if res := anonymous.request(t, "GET", "fs", nil); res.Code != 401 {
		t.Fatalf("anonymous=%d", res.Code)
	}
}
func TestConsoleBrowseFallsBackToHome(t *testing.T) {
	rt := newWorkspaceRuntime(t, "alpha")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	c := consoleLogin(t, rt)
	response := c.request(t, "GET", "fs", nil)
	if response.Code != 200 {
		t.Fatalf("browse=%d %s", response.Code, response.Body.String())
	}
	var page struct{ Path string }
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Path != filepath.Clean(home) {
		t.Fatalf("default path=%q want home %q", page.Path, home)
	}
}
