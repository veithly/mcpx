package workspacechanges

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpx/internal/auth"
	"mcpx/internal/remotesession"
	"mcpx/internal/state"
)

func TestInspectAttributesRemoteSessionChanges(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "MCPX Test")
	write(t, root, "preexisting.txt", "base\n")
	write(t, root, "mcpx.txt", "base\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "base")

	// This edit exists before the Remote Session baseline is captured.
	write(t, root, "preexisting.txt", "changed before session\n")
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal := auth.Principal{ID: "principal-test", Kind: "test", SubjectHash: "subject-test"}
	created, err := remotesession.NewService(store.DB()).Create(ctx, principal, remotesession.CreateInput{
		WorkspaceName: "test", WorkspacePath: root, Label: "attribution test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	if err := service.CaptureBaseline(ctx, created.Session.ID, root); err != nil {
		t.Fatal(err)
	}

	write(t, root, "mcpx.txt", "changed by mcpx\n")
	seedPatchReceipt(t, store.DB(), created.Session.ID, "apply_patch", "succeeded", 0, "patch-test", map[string]any{
		"_meta":             map[string]any{"mcpx/request_id": "patch-test", "mcpx/patch_paths": []string{"mcpx.txt"}},
		"structuredContent": map[string]any{"output": "Success", "exit_code": 0},
	})
	write(t, root, "external.txt", "outside edit\n")

	report, err := service.Inspect(ctx, created.Session.ID, "test", root, true)
	if err != nil {
		t.Fatal(err)
	}
	attribution := map[string]string{}
	for _, entry := range report.Entries {
		attribution[entry.Path] = entry.Attribution
	}
	for path, want := range map[string]string{
		"preexisting.txt": "preexisting",
		"mcpx.txt":        "mcpx",
		"external.txt":    "unknown",
	} {
		if got := attribution[path]; got != want {
			t.Fatalf("%s attribution=%q, want %q; report=%+v", path, got, want, report.Entries)
		}
	}
	if report.UnifiedDiff == "" {
		t.Fatal("expected tracked Unified Diff")
	}
}

func TestNonGitWorkspaceHasNoGitBaselineButCanBeInspected(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal := auth.Principal{ID: "principal-test", Kind: "test", SubjectHash: "subject-test"}
	created, err := remotesession.NewService(store.DB()).Create(ctx, principal, remotesession.CreateInput{
		WorkspaceName: "plain", WorkspacePath: root, Label: "non-git workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	if err := service.CaptureBaseline(ctx, created.Session.ID, root); err != nil {
		t.Fatalf("non-Git baseline should be a no-op: %v", err)
	}
	report, err := service.Inspect(ctx, created.Session.ID, "plain", root, true)
	if err != nil {
		t.Fatalf("non-Git inspection should return a structured report: %v", err)
	}
	if report.GitAvailable || report.GitHead != "" || len(report.Entries) != 0 {
		t.Fatalf("unexpected non-Git report: %+v", report)
	}
}

func TestInspectHandlesGitRepositoryCreatedAfterSessionOpen(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal := auth.Principal{ID: "principal-test", Kind: "test", SubjectHash: "subject-test"}
	created, err := remotesession.NewService(store.DB()).Create(ctx, principal, remotesession.CreateInput{
		WorkspaceName: "late-git", WorkspacePath: root, Label: "repository created after session",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	if err := service.CaptureBaseline(ctx, created.Session.ID, root); err != nil {
		t.Fatal(err)
	}

	git(t, root, "init")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "MCPX Test")
	write(t, root, "tracked.txt", "base\n")
	git(t, root, "add", "tracked.txt")
	git(t, root, "commit", "-m", "base")
	write(t, root, "created-after-session.txt", "changed\n")

	report, err := service.Inspect(ctx, created.Session.ID, "late-git", root, true)
	if err != nil {
		t.Fatalf("inspection after Git initialization must not require a missing baseline: %v", err)
	}
	if !report.GitAvailable {
		t.Fatalf("Git repository was not detected: %+v", report)
	}
	if report.BaselineHead == "" {
		t.Fatalf("fallback baseline head is missing: %+v", report)
	}
	if len(report.Entries) != 1 || report.Entries[0].Attribution != "unknown" {
		t.Fatalf("late Git change attribution=%+v, want one unknown entry", report.Entries)
	}
}

func TestInspectAggregatesNestedGitRoots(t *testing.T) {
	root := t.TempDir()
	// The aggregate root itself is not a Git repository; a and b are two
	// nested repositories, mirroring a workspace that hosts multiple repos.
	for _, sub := range []string{"a", "b"} {
		subRoot := filepath.Join(root, sub)
		if err := os.MkdirAll(subRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, subRoot, "init")
		git(t, subRoot, "config", "user.email", "test@example.invalid")
		git(t, subRoot, "config", "user.name", "MCPX Test")
		write(t, subRoot, "file.txt", "base\n")
		git(t, subRoot, "add", ".")
		git(t, subRoot, "commit", "-m", "base")
		// Untracked file: drives the entries assertion.
		write(t, subRoot, "dirty.txt", "changed\n")
		// Tracked modification: drives the unified diff assertion.
		write(t, subRoot, "file.txt", "base\nchanged\n")
	}

	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal := auth.Principal{ID: "principal-test", Kind: "test", SubjectHash: "subject-test"}
	created, err := remotesession.NewService(store.DB()).Create(ctx, principal, remotesession.CreateInput{
		WorkspaceName: "aggregate", WorkspacePath: root, Label: "multi-root test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.DB())
	if err := service.CaptureBaseline(ctx, created.Session.ID, root); err != nil {
		t.Fatal(err)
	}

	report, err := service.Inspect(ctx, created.Session.ID, "aggregate", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.GitRoots) != 2 {
		t.Fatalf("expected 2 git roots, got %+v", report.GitRoots)
	}
	for _, gitRoot := range report.GitRoots {
		if gitRoot.Path != "a" && gitRoot.Path != "b" {
			t.Fatalf("unexpected git root %q", gitRoot.Path)
		}
		if gitRoot.Head == "" {
			t.Fatalf("git root %q has no head", gitRoot.Path)
		}
	}
	if !strings.Contains(report.GitHead, "a:") || !strings.Contains(report.GitHead, "b:") {
		t.Fatalf("aggregate head must name both roots: %q", report.GitHead)
	}
	paths := map[string]bool{}
	for _, entry := range report.Entries {
		paths[entry.Path] = true
	}
	if !paths["a/dirty.txt"] || !paths["b/dirty.txt"] {
		t.Fatalf("entries must include both nested repos: %+v", report.Entries)
	}
	if !strings.Contains(report.UnifiedDiff, "### a") || !strings.Contains(report.UnifiedDiff, "### b") {
		t.Fatalf("unified diff must cover both repos: %q", report.UnifiedDiff)
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func seedPatchReceipt(t *testing.T, db *sql.DB, sessionID, tool, status string, exitCode any, requestID string, result any) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_results (request_id, workspace_name, remote_session_id, tool_name, status, exit_code, result_json, created_at, updated_at)
		VALUES (?, 'test', ?, ?, ?, ?, ?, 1, 1)`, requestID, sessionID, tool, status, exitCode, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestPatchAttributionRequiresCompleteSessionReceipt(t *testing.T) {
	for _, name := range []string{"valid-move", "other-session", "failed", "running", "nonzero", "missing-code", "wrong-tool", "missing-paths", "wrong-request", "error-result", "truncated", "malformed", "unsafe-paths"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CODEX_HOME", t.TempDir())
			store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			session, tool, status := "rs_test", "apply_patch", "succeeded"
			var code any = 0
			meta := map[string]any{"mcpx/request_id": "patch", "mcpx/patch_paths": []string{"old.txt", "new.txt"}}
			content := map[string]any{"output": "Success", "exit_code": 0}
			result := map[string]any{"_meta": meta, "structuredContent": content}
			switch name {
			case "other-session":
				session = "rs_other"
			case "failed":
				status = "failed"
			case "running":
				status, code = "accepted", nil
			case "nonzero":
				code = 1
				content["exit_code"] = 1
			case "missing-code":
				delete(content, "exit_code")
			case "wrong-tool":
				tool = "exec_command"
			case "missing-paths":
				delete(meta, "mcpx/patch_paths")
			case "wrong-request":
				meta["mcpx/request_id"] = "different-request"
			case "error-result":
				result["isError"] = true
			case "unsafe-paths":
				meta["mcpx/patch_paths"] = []string{"../outside", "/tmp/outside", ".", ""}
			}
			seedPatchReceipt(t, store.DB(), session, tool, status, code, "patch", result)
			if name == "truncated" {
				if _, err := store.DB().Exec("UPDATE tool_results SET truncated = 1"); err != nil {
					t.Fatal(err)
				}
			}
			if name == "malformed" {
				if _, err := store.DB().Exec("UPDATE tool_results SET result_json = '{'"); err != nil {
					t.Fatal(err)
				}
			}
			paths, err := NewService(store.DB()).mcpxPaths(context.Background(), "rs_test")
			if err != nil {
				t.Fatal(err)
			}
			if name == "valid-move" {
				if len(paths) != 2 || !paths["old.txt"] || !paths["new.txt"] {
					t.Fatalf("move receipt lost: %+v", paths)
				}
			} else if len(paths) != 0 {
				t.Fatalf("unproven attribution: %+v", paths)
			}
		})
	}
}
