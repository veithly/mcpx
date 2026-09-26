// Copyright 2025 OpenAI. Licensed under Apache-2.0.
// Derived from codex-rs/apply-patch/src/lib.rs at
// 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2; Go port with MCPX path policy.
// Package patch implements raw apply-patch without an external executable.
package patch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type FileUpdateMode uint8

const (
	NormalizeToLF FileUpdateMode = iota
	PreserveLineEndings
)

type Request struct {
	WorkDir, WorkspaceRoot, Input string
	ValidatePath                  func(string) error
	// UpdateMode defaults to upstream NormalizeToLf; preserve is explicit.
	UpdateMode FileUpdateMode
}
type Result struct {
	Output   string
	ExitCode int
	// Unique workspace-relative candidate paths, not a completed-write receipt.
	Paths []string
}

// Apply parses the entire input and validates every target before writing.
// Syntax and application failures have ExitCode 1 and diagnostic Output.
// Policy, containment, cancellation and detected concurrent changes return Go
// errors. Paths are candidates; a later failure never rolls back earlier files.
func Apply(ctx context.Context, request Request) (Result, error) {
	result := Result{ExitCode: -1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.UpdateMode != NormalizeToLF && request.UpdateMode != PreserveLineEndings {
		return result, fmt.Errorf("invalid patch update mode")
	}
	parsed, err := parse(request.Input)
	if err != nil {
		result.ExitCode = 1
		result.Output = err.Error()
		return result, nil
	}
	// Upstream standalone discards this field (lib.rs apply_patch_with_options).
	// MCPX has exactly one selected workspace and cannot route environment IDs.
	if parsed.environmentID != "" && parsed.environmentID != "local" {
		return result, fmt.Errorf("patch environment_id %q is unsupported; only local (the selected workspace) is supported", parsed.environmentID)
	}
	hunks := parsed.hunks
	w, err := openWorkspace(request)
	if err != nil {
		return result, err
	}
	defer w.root.Close()
	targets := map[string]*target{}
	ordered := []*target{}
	for _, h := range hunks {
		names := []string{h.path}
		if h.hasMove {
			names = append(names, h.move)
		}
		for _, name := range names {
			if _, ok := targets[name]; ok {
				continue
			}
			rel, err := w.relative(name)
			if err != nil {
				return result, err
			}
			t, err := w.inspect(rel)
			if err != nil {
				return result, err
			}
			t.display = name
			targets[name] = t
			ordered = append(ordered, t)
			if err := w.validate(t, request.ValidatePath); err != nil {
				return result, err
			}
			relative := filepath.ToSlash(rel)
			found := false
			for _, p := range result.Paths {
				if p == relative {
					found = true
					break
				}
			}
			if !found {
				result.Paths = append(result.Paths, relative)
			}
		}
	}
	// Callback changes cannot be approved by a second parse or a fresh snapshot.
	for _, t := range ordered {
		if err := w.check(t); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(hunks) == 0 {
		result.ExitCode = 1
		result.Output = "No files were modified.\n"
		return result, nil
	}
	summaries := map[byte][]string{}
	for _, h := range hunks {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		t := targets[h.path]
		if err := w.check(t); err != nil {
			return result, err
		}
		dest := t
		if h.hasMove {
			dest = targets[h.move]
			if err := w.check(dest); err != nil {
				return result, err
			}
		}
		abs := filepath.Join(w.physical, t.relative)
		var changed []string
		switch h.kind {
		case 'A':
			changed, err = w.write(ctx, t, []byte(h.contents.String()), true)
			if err != nil {
				err = fmt.Errorf("Failed to write file %s", abs)
			}
		case 'D':
			err = w.remove(t)
			if err != nil {
				err = fmt.Errorf("Failed to delete file %s", abs)
			} else {
				changed = []string{t.relative}
			}
		case 'M':
			if t.info == nil || !t.info.Mode().IsRegular() {
				err = fmt.Errorf("Failed to read file to update %s: %s", abs, missingOrDirectory(t))
				break
			}
			if !utf8.Valid(t.contents) {
				err = fmt.Errorf("Failed to read file to update %s: stream did not contain valid UTF-8", abs)
				break
			}
			var contents string
			contents, err = updatedContents(string(t.contents), abs, h.chunks, request.UpdateMode)
			if err != nil {
				break
			}
			changed, err = w.write(ctx, dest, []byte(contents), h.hasMove)
			if err != nil {
				err = fmt.Errorf("Failed to write file %s", filepath.Join(w.physical, dest.relative))
				break
			}
			if h.hasMove {
				// Upstream writes the destination first. A failed source removal leaves
				// both files. Even move-to-self follows this write-then-remove contract.
				source := t
				if t.physical == dest.physical {
					source, err = w.inspect(t.relative)
				}
				if err == nil {
					err = w.remove(source)
				}
				if err != nil {
					err = fmt.Errorf("Failed to remove original %s", abs)
				} else {
					changed = append(changed, t.relative)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.ExitCode = 1
			result.Output = err.Error() + "\n"
			return result, nil
		}
		// Only refresh identities affected by our own successful mutation. Other
		// targets retain their original bytes and identity for conflict detection.
		for _, candidate := range ordered {
			affected := false
			for _, name := range changed {
				if candidate.physical == name {
					affected = true
				}
				for _, node := range candidate.nodes {
					if node.path == name {
						affected = true
					}
				}
			}
			if affected {
				fresh, e := w.inspect(candidate.relative)
				if e != nil {
					return result, e
				}
				fresh.display = candidate.display
				*candidate = *fresh
			}
		}
		name := h.path
		if h.hasMove {
			name = h.move
		}
		summaries[h.kind] = append(summaries[h.kind], name)
	}
	var output strings.Builder
	output.WriteString("Success. Updated the following files:\n")
	for _, kind := range []byte{'A', 'M', 'D'} {
		for _, name := range summaries[kind] {
			fmt.Fprintf(&output, "%c %s\n", kind, name)
		}
	}
	result.Output = output.String()
	result.ExitCode = 0
	return result, nil
}

func missingOrDirectory(t *target) string {
	if t.info == nil {
		return "No such file or directory (os error 2)"
	}
	return "Is a directory (os error 21)"
}

func (w *workspace) remove(t *target) error {
	if t.info == nil {
		return os.ErrNotExist
	}
	if !t.info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if err := w.check(t); err != nil {
		return err
	}
	// os.Root confines this unlink even if a parent becomes an escaping symlink.
	// Concurrent hostile renames within the root are not an OS sandbox boundary.
	// Upstream local_file_system.rs remove unlinks the named symlink rather
	// than its referent. Reads/writes follow it; Delete and Move-source do not.
	return w.root.Remove(t.relative)
}
