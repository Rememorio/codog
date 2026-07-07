// Package projectscope resolves workspace-local discovery boundaries.
package projectscope

import (
	"os"
	"path/filepath"
	"strings"
)

// Ancestors returns workspace and its parents up to the nearest project
// boundary. When no boundary is found, it returns only workspace so discovery
// cannot leak into unrelated parent directories.
func Ancestors(workspace string) []string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	start, err := filepath.Abs(workspace)
	if err != nil {
		start = filepath.Clean(workspace)
	}
	start = filepath.Clean(start)
	out := []string{}
	for current := start; ; current = filepath.Dir(current) {
		out = append(out, current)
		if isBoundary(current) {
			return out
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return []string{start}
}

func isBoundary(dir string) bool {
	for _, marker := range []string{".git", ".jj", ".hg", ".codog.json", ".claude/settings.json", "CLAUDE.md", "AGENTS.md"} {
		if exists(filepath.Join(dir, marker)) {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
