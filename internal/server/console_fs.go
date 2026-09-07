package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// browse is a read-only directory picker for the "add workspace" dialog. It
// lists directory names only — never file contents — and is gated by the same
// operator authentication as the rest of the console API.
func (c *consoleHandler) browse(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	home, homeErr := os.UserHomeDir()
	if target == "" {
		if homeErr != nil {
			consoleError(w, 500, "cannot resolve home directory")
			return
		}
		target = home
	}
	if !filepath.IsAbs(target) {
		consoleError(w, 400, "directory path must be absolute")
		return
	}
	path := filepath.Clean(target)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		consoleError(w, 404, "目录不存在或不是文件夹")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		consoleError(w, 500, "无法读取目录："+err.Error())
		return
	}
	dirs := []map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			// Include symlinks that resolve to directories; skip broken ones.
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			if resolved, statErr := os.Stat(filepath.Join(path, name)); statErr != nil || !resolved.IsDir() {
				continue
			}
		}
		dirs = append(dirs, map[string]string{"name": name, "path": filepath.Join(path, name)})
	}
	sort.Slice(dirs, func(i, j int) bool {
		hiddenI, hiddenJ := strings.HasPrefix(dirs[i]["name"], "."), strings.HasPrefix(dirs[j]["name"], ".")
		if hiddenI != hiddenJ {
			return !hiddenI
		}
		return strings.ToLower(dirs[i]["name"]) < strings.ToLower(dirs[j]["name"])
	})
	parent := ""
	if above := filepath.Dir(path); above != path {
		parent = above
	}
	consoleJSON(w, 200, map[string]any{
		"path":     path,
		"parent":   parent,
		"segments": pathSegments(path),
		"entries":  dirs,
		"home":     home,
	})
}

func pathSegments(path string) []map[string]string {
	separator := string(filepath.Separator)
	prefix := filepath.VolumeName(path)
	segments := []map[string]string{}
	current := prefix
	for index, part := range strings.Split(strings.TrimPrefix(path, prefix), separator) {
		if part == "" {
			if index == 0 {
				label := separator
				if prefix != "" {
					label = prefix + separator
				}
				current = prefix + separator
				segments = append(segments, map[string]string{"label": label, "path": current})
			}
			continue
		}
		current = filepath.Join(current, part)
		segments = append(segments, map[string]string{"label": part, "path": current})
	}
	return segments
}
