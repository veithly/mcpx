package patch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type target struct {
	display, relative, physical string
	nodes                       []pathNode
	info                        os.FileInfo
	contents                    []byte
}
type pathNode struct {
	path string
	info os.FileInfo
	link string
}
type workspace struct {
	root                     *os.Root
	original, physical, work string
}

func openWorkspace(req Request) (*workspace, error) {
	if req.WorkspaceRoot == "" {
		return nil, fmt.Errorf("patch workspace root is required")
	}
	original, err := filepath.Abs(req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	physical, err := filepath.EvalSymlinks(original)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(physical)
	if err != nil {
		return nil, err
	}
	w := &workspace{root: root, original: original, physical: physical, work: "."}
	work := req.WorkDir
	if work == "" {
		work = "."
	}
	rel, err := w.relative(work)
	if err == nil {
		var t *target
		t, err = w.inspect(rel)
		if err == nil {
			if t.info == nil || !t.info.IsDir() {
				err = fmt.Errorf("patch work directory is not a directory")
			} else {
				w.work = t.physical
			}
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return w, nil
}
func descendant(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return rel, nil
}
func (w *workspace) relative(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid patch path %q", name)
	}
	if filepath.IsAbs(name) {
		rel, err := descendant(w.physical, name)
		if err != nil {
			rel, err = descendant(w.original, name)
		}
		if err != nil {
			return "", fmt.Errorf("patch path %q escapes workspace", name)
		}
		return rel, nil
	}
	if filepath.VolumeName(name) != "" || os.IsPathSeparator(name[0]) {
		return "", fmt.Errorf("ambiguous patch path %q", name)
	}
	rel := filepath.Join(w.work, name)
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("patch path %q escapes workspace", name)
	}
	return rel, nil
}

// Resolve links through the root descriptor, never through an unconfined OS
// path. Record each alias for file policy and later identity checks. Absolute
// symlinks are deliberately refused, matching os.Root's containment contract.
func (w *workspace) inspect(rel string) (*target, error) {
	t := &target{relative: rel}
	pending := strings.Split(rel, string(filepath.Separator))
	current := "."
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		candidate := filepath.Join(current, part)
		if !filepath.IsLocal(candidate) {
			return nil, fmt.Errorf("patch path %q escapes workspace", rel)
		}
		info, err := w.root.Lstat(candidate)
		if os.IsNotExist(err) {
			t.nodes = append(t.nodes, pathNode{path: candidate})
			current = candidate
			for _, part := range pending {
				current = filepath.Join(current, part)
			}
			if !filepath.IsLocal(current) {
				return nil, fmt.Errorf("patch path %q escapes workspace", rel)
			}
			t.physical = current
			return t, nil
		}
		if err != nil {
			return nil, err
		}
		node := pathNode{path: candidate, info: info}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return nil, fmt.Errorf("too many patch symlinks in %q", rel)
			}
			link, err := w.root.Readlink(candidate)
			if err != nil {
				return nil, err
			}
			node.link = link
			t.nodes = append(t.nodes, node)
			if filepath.IsAbs(link) || filepath.VolumeName(link) != "" {
				return nil, fmt.Errorf("patch symlink %q must be relative and stay in workspace", candidate)
			}
			resolved := filepath.Join(filepath.Dir(candidate), link)
			if !filepath.IsLocal(resolved) {
				return nil, fmt.Errorf("patch symlink %q escapes workspace", candidate)
			}
			pending = append(strings.Split(resolved, string(filepath.Separator)), pending...)
			current = "."
			continue
		}
		t.nodes = append(t.nodes, node)
		current = candidate
		if len(pending) > 0 && !info.IsDir() {
			return nil, fmt.Errorf("patch parent %q is not a directory", candidate)
		}
	}
	t.physical = current
	info, err := w.root.Lstat(current)
	if err != nil {
		return nil, err
	}
	t.info = info
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("patch path %q is not a regular file", rel)
	}
	if info.Mode().IsRegular() {
		if multipleLinks(info) {
			return nil, fmt.Errorf("patch path %q has multiple hard links", rel)
		}
		f, err := w.root.Open(current)
		if err != nil {
			return nil, err
		}
		actual, err := f.Stat()
		if err == nil && !sameInfo(info, actual) {
			err = fmt.Errorf("patch path %q changed during validation", rel)
		}
		if err == nil {
			t.contents, err = io.ReadAll(f)
		}
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}
func sameInfo(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if !os.SameFile(a, b) || a.Mode() != b.Mode() {
		return false
	}
	// Creating another patch target changes parent directory mtime/size.
	return a.IsDir() || a.Size() == b.Size() && a.ModTime() == b.ModTime()
}
func (w *workspace) check(t *target) error {
	for _, n := range t.nodes {
		info, err := w.root.Lstat(n.path)
		if os.IsNotExist(err) && n.info == nil {
			continue
		}
		if err != nil || !sameInfo(n.info, info) {
			return fmt.Errorf("patch path %q changed during validation", t.relative)
		}
		if n.link != "" {
			link, err := w.root.Readlink(n.path)
			if err != nil || link != n.link {
				return fmt.Errorf("patch path %q changed during validation", t.relative)
			}
		}
	}
	if t.info != nil && t.info.Mode().IsRegular() {
		data, err := w.root.ReadFile(t.physical)
		if err != nil || !bytes.Equal(data, t.contents) {
			return fmt.Errorf("patch path %q changed during validation", t.relative)
		}
	}
	return nil
}
func (w *workspace) validate(t *target, validate func(string) error) error {
	if validate == nil {
		return nil
	}
	seen := map[string]bool{}
	// Check lexical endpoint, every symlink alias, and its resolved endpoint.
	names := []string{t.relative, t.physical}
	for _, node := range t.nodes {
		if node.link != "" {
			names = append(names, node.path)
		}
	}
	for _, name := range names {
		abs := filepath.Join(w.physical, name)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if err := validate(abs); err != nil {
			return fmt.Errorf("patch path %q: %w", t.relative, err)
		}
	}
	return nil
}
