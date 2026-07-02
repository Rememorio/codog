// Package gitignore matches workspace paths against .gitignore files.
package gitignore

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Matcher evaluates .gitignore rules beneath one workspace root.
type Matcher struct {
	root  string
	cache map[string][]pattern
}

type pattern struct {
	base          string
	value         string
	negated       bool
	directoryOnly bool
	hasSlash      bool
}

// New returns a matcher rooted at workspace.
func New(workspace string) (*Matcher, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return &Matcher{root: root, cache: map[string][]pattern{}}, nil
}

// Ignored reports whether path is ignored by the applicable .gitignore files.
func (m *Matcher) Ignored(candidate string, isDir bool) bool {
	if m == nil {
		return false
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	if !within(m.root, candidate) || samePath(m.root, candidate) {
		return false
	}
	ignored := false
	for _, dir := range m.ruleDirs(candidate, isDir) {
		for _, rule := range m.patternsFor(dir) {
			if rule.matches(candidate, isDir) {
				ignored = !rule.negated
			}
		}
	}
	return ignored
}

func (m *Matcher) ruleDirs(candidate string, isDir bool) []string {
	parent := filepath.Dir(candidate)
	if isDir {
		parent = filepath.Dir(candidate)
	}
	rel, err := filepath.Rel(m.root, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil
	}
	dirs := []string{m.root}
	if rel == "." {
		return dirs
	}
	current := m.root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		dirs = append(dirs, current)
	}
	return dirs
}

func (m *Matcher) patternsFor(dir string) []pattern {
	if patterns, ok := m.cache[dir]; ok {
		return patterns
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		m.cache[dir] = nil
		return nil
	}
	patterns := parsePatterns(dir, string(data))
	m.cache[dir] = patterns
	return patterns
}

func parsePatterns(base string, data string) []pattern {
	var patterns []pattern
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		line = strings.TrimPrefix(filepath.ToSlash(line), "/")
		directoryOnly := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		patterns = append(patterns, pattern{
			base:          base,
			value:         line,
			negated:       negated,
			directoryOnly: directoryOnly,
			hasSlash:      strings.Contains(line, "/"),
		})
	}
	return patterns
}

func (p pattern) matches(candidate string, isDir bool) bool {
	if p.directoryOnly && !isDir {
		return false
	}
	rel, err := filepath.Rel(p.base, candidate)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	name := path.Base(rel)
	if !p.hasSlash {
		return pathMatches(p.value, name)
	}
	return pathMatches(p.value, rel)
}

func pathMatches(pattern string, value string) bool {
	if ok, _ := path.Match(pattern, value); ok {
		return true
	}
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			return strings.HasPrefix(value, strings.TrimSuffix(parts[0], "/")) &&
				strings.HasSuffix(value, strings.TrimPrefix(parts[1], "/"))
		}
	}
	return pattern == value
}

func within(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func samePath(left string, right string) bool {
	leftAbs, err := filepath.Abs(left)
	if err == nil {
		left = leftAbs
	}
	rightAbs, err := filepath.Abs(right)
	if err == nil {
		right = rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
