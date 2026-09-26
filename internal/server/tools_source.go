package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpx/internal/winproc"
)

func (r *Runtime) toolProjectInspect(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	envReq, principal, fail := r.remoteRequest(ctx, req)
	if fail != nil {
		return fail, nil
	}
	ws, remoteID, err := r.resolveExplicitWorkspace(ctx, principal, envReq)
	if err != nil {
		return r.remoteError(envReq, remoteID, ws.Name, err)
	}
	if ws.Path == "" {
		return r.terminalError(envReq, remoteID, ws.Name, "WORKSPACE_REQUIRED", "Specify workspace or remote_session_id to read project information; use workspace to list registered projects.")
	}
	data := inspectProject(ctx, ws.Path)
	data["agent_instructions"] = r.agentInstructions(ws.Path)
	return r.remoteResult(envReq, remoteID, ws.Name, data)
}

func inspectProject(ctx context.Context, root string) map[string]any {
	manifestStacks := map[string]string{
		"go.mod": "go", "package.json": "node", "Cargo.toml": "rust", "pyproject.toml": "python",
		"requirements.txt": "python", "pom.xml": "java-maven", "build.gradle": "java-gradle",
		"composer.json": "php", "Gemfile": "ruby", "Makefile": "make",
	}
	var manifests, stacks []string
	for manifest, stack := range manifestStacks {
		if info, err := os.Stat(filepath.Join(root, manifest)); err == nil && info.Mode().IsRegular() {
			manifests = append(manifests, manifest)
			stacks = append(stacks, stack)
		}
	}
	sort.Strings(manifests)
	sort.Strings(stacks)
	var instructions []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "CODEX.md", "CONTRIBUTING.md", "README.md"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			instructions = append(instructions, name)
		}
	}
	entryCandidates := []string{"main.go", "cmd", "src/main.go", "src/index.ts", "src/index.js", "app", "pages", "manage.py"}
	var entrypoints []string
	for _, name := range entryCandidates {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			entrypoints = append(entrypoints, name)
		}
	}
	tasks := map[string]string{}
	if content, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var packageFile struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(content, &packageFile) == nil {
			for name := range packageFile.Scripts {
				tasks[name] = "npm run " + name
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		tasks["test"] = "go test ./..."
		tasks["build"] = "go build ./..."
	}
	gitStatus := boundedCommand(ctx, root, "git", "status", "--short", "--branch")
	return map[string]any{
		"stacks": stacks, "manifests": manifests, "entrypoints": entrypoints,
		"instructions": instructions, "tasks": tasks, "git_status": gitStatus,
	}
}

func boundedCommand(parent context.Context, workDir, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	winproc.ConfigureNoWindow(command)
	command.Dir = workDir
	output, err := command.CombinedOutput()
	if err != nil && len(output) == 0 {
		return ""
	}
	value := strings.TrimSpace(string(output))
	if len(value) > 20_000 {
		value = value[:20_000]
	}
	return value
}
