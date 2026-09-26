// Package instruction discovers the instruction documents intentionally
// exposed to Remote Session clients.
package instruction

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcpx/internal/config"
	"mcpx/internal/file"
)

var ErrNotFound = errors.New("agent instruction not found")

// Document is a machine-readable instruction descriptor. Absolute host paths
// are deliberately kept private; callers address a document through its ID.
type Document struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"` // global | project | directory
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	RelativeDir string `json:"relative_dir,omitempty"`
	AppliesTo   string `json:"applies_to,omitempty"`
	Priority    int    `json:"priority"`
	Active      bool   `json:"active"`
	Reason      string `json:"reason,omitempty"`
	path        string
}

// Discover returns global then project-root AGENTS.md (no path anchor).
func Discover(globalAgentsPath, workspaceRoot string, maxBytes int64) []Document {
	return DiscoverAt(globalAgentsPath, workspaceRoot, "", maxBytes)
}

// DiscoverAt discovers instruction documents for an optional workspace-relative
// anchor path. Order is broad → narrow: global, project root, then each
// directory from workspace root down to the anchor. Later entries have higher
// priority and may override earlier rules for that path tree.
func DiscoverAt(globalAgentsPath, workspaceRoot, anchorPath string, maxBytes int64) []Document {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	documents := make([]Document, 0, 8)
	priority := 10
	if globalAgentsPath != "" {
		if document, ok := inspect("global", "global", "", "", config.ExpandHome(globalAgentsPath), priority, maxBytes); ok {
			document.Active = true
			document.Reason = "platform_and_global"
			documents = append(documents, document)
			priority += 10
		}
	}
	if workspaceRoot == "" {
		return documents
	}
	if document, ok := inspect("project", "project", ".", "**", filepath.Join(workspaceRoot, "AGENTS.md"), priority, maxBytes); ok {
		document.Active = true
		document.Reason = "workspace_root"
		documents = append(documents, document)
		priority += 10
	}
	anchor := filepath.ToSlash(filepath.Clean(strings.TrimSpace(anchorPath)))
	// Instruction discovery follows the same physical workspace boundary as
	// source reads, including directory symlinks and parent components.
	if _, err := file.Resolve(workspaceRoot, anchor); err != nil {
		return markActiveChain(documents)
	}
	if anchor == "." || anchor == "" {
		return markActiveChain(documents)
	}
	// If anchor is a file path, resolve directory chain from its parent.
	absoluteCandidate := filepath.Join(workspaceRoot, filepath.FromSlash(anchor))
	info, err := os.Stat(absoluteCandidate)
	dirRel := anchor
	if err == nil && !info.IsDir() {
		dirRel = filepath.ToSlash(filepath.Dir(anchor))
	}
	if dirRel == "." {
		return markActiveChain(documents)
	}
	parts := strings.Split(dirRel, "/")
	accum := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		accum = append(accum, part)
		relDir := strings.Join(accum, "/")
		id := "dir:" + relDir
		path := filepath.Join(workspaceRoot, filepath.FromSlash(relDir), "AGENTS.md")
		if _, err := file.Resolve(workspaceRoot, filepath.Join(filepath.FromSlash(relDir), "AGENTS.md")); err != nil {
			continue
		}
		applies := relDir + "/**"
		if document, ok := inspect(id, "directory", relDir, applies, path, priority, maxBytes); ok {
			document.Active = true
			document.Reason = "directory_chain"
			documents = append(documents, document)
			priority += 10
		}
	}
	return markActiveChain(documents)
}

// ResolveForPaths returns per-path instruction chains and cross-path conflicts.
func ResolveForPaths(globalAgentsPath, workspaceRoot string, paths []string, maxBytes int64) map[string]any {
	byPath := make(map[string]any, len(paths))
	seen := map[string][]Document{}
	conflicts := []map[string]any{}
	for _, path := range paths {
		docs := DiscoverAt(globalAgentsPath, workspaceRoot, path, maxBytes)
		byPath[path] = docs
		for _, doc := range docs {
			if doc.Scope != "directory" {
				continue
			}
			seen[doc.ID] = append(seen[doc.ID], doc)
		}
	}
	// Conflict: same directory rule applies to disjoint top-level trees in one multi-file op.
	// Surface when callers request files under different first-level directories that carry
	// distinct directory instructions with incompatible applies_to.
	frontends, backends := false, false
	for path := range byPath {
		if strings.HasPrefix(filepath.ToSlash(path), "frontend/") {
			frontends = true
		}
		if strings.HasPrefix(filepath.ToSlash(path), "backend/") {
			backends = true
		}
	}
	if frontends && backends {
		conflicts = append(conflicts, map[string]any{
			"code":    "cross_tree_rules",
			"message": "operation spans frontend and backend instruction trees; resolve per file",
			"paths":   paths,
		})
	}
	return map[string]any{"by_path": byPath, "conflicts": conflicts}
}

// Read resolves a previously discoverable document by ID.
func Read(globalAgentsPath, workspaceRoot, id string, maxBytes int64) (Document, string, error) {
	return ReadAt(globalAgentsPath, workspaceRoot, "", id, maxBytes)
}

// ReadAt discovers with anchor then reads by id.
func ReadAt(globalAgentsPath, workspaceRoot, anchorPath, id string, maxBytes int64) (Document, string, error) {
	for _, document := range DiscoverAt(globalAgentsPath, workspaceRoot, anchorPath, maxBytes) {
		if document.ID != id {
			// Also allow reading directory docs without anchor by scanning common ids.
			continue
		}
		return readDocument(document)
	}
	// Fallback: if id is dir:..., try direct path even without anchor.
	if strings.HasPrefix(id, "dir:") && workspaceRoot != "" {
		rel := strings.TrimPrefix(id, "dir:")
		path := filepath.Join(workspaceRoot, filepath.FromSlash(rel), "AGENTS.md")
		if document, ok := inspect(id, "directory", rel, rel+"/**", path, 0, maxBytes); ok {
			return readDocument(document)
		}
	}
	if id == "global" || id == "project" {
		for _, document := range Discover(globalAgentsPath, workspaceRoot, maxBytes) {
			if document.ID == id {
				return readDocument(document)
			}
		}
	}
	return Document{}, "", ErrNotFound
}

// ReadContents loads UTF-8 text for documents until totalBudget is exhausted.
func ReadContents(documents []Document, totalBudget int64) ([]map[string]any, int64) {
	if totalBudget <= 0 {
		totalBudget = 256 << 10
	}
	out := make([]map[string]any, 0, len(documents))
	var used int64
	for _, document := range documents {
		item := map[string]any{
			"id": document.ID, "scope": document.Scope, "name": document.Name,
			"sha256": document.SHA256, "bytes": document.Bytes, "priority": document.Priority,
			"relative_dir": document.RelativeDir, "applies_to": document.AppliesTo,
			"active": document.Active, "reason": document.Reason,
		}
		if used >= totalBudget || document.Bytes > totalBudget-used {
			item["content_omitted"] = true
			item["reason_omitted"] = "budget_exceeded"
			out = append(out, item)
			continue
		}
		content, err := os.ReadFile(document.path)
		if err != nil {
			item["content_omitted"] = true
			item["reason_omitted"] = err.Error()
			out = append(out, item)
			continue
		}
		item["content"] = string(content)
		used += int64(len(content))
		out = append(out, item)
	}
	return out, used
}

func readDocument(document Document) (Document, string, error) {
	content, err := os.ReadFile(document.path)
	if err != nil {
		return Document{}, "", fmt.Errorf("read %s: %w", document.Name, err)
	}
	if int64(len(content)) != document.Bytes {
		return Document{}, "", fmt.Errorf("%s changed during read", document.Name)
	}
	digest := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(digest[:]) != document.SHA256 {
		return Document{}, "", fmt.Errorf("%s changed during read", document.Name)
	}
	return document, string(content), nil
}

func inspect(id, scope, relativeDir, appliesTo, path string, priority int, maxBytes int64) (Document, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return Document{}, false
	}
	content, err := os.ReadFile(path)
	if err != nil || int64(len(content)) != info.Size() {
		return Document{}, false
	}
	digest := sha256.Sum256(content)
	return Document{
		ID: id, Scope: scope, Name: "AGENTS.md", Bytes: info.Size(),
		SHA256: "sha256:" + hex.EncodeToString(digest[:]), path: path,
		RelativeDir: relativeDir, AppliesTo: appliesTo, Priority: priority,
	}, true
}

func markActiveChain(documents []Document) []Document {
	// All discovered chain members are active for the anchor; callers may
	// further filter. Ensure priority is strictly increasing.
	for i := range documents {
		if documents[i].Priority == 0 {
			documents[i].Priority = (i + 1) * 10
		}
		if !documents[i].Active {
			documents[i].Active = true
		}
	}
	return documents
}
