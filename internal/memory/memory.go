package memory

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const MaxFileBytes = 64 * 1024

var CandidateNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CLAW.md",
	filepath.Join(".codog", "instructions.md"),
}

type File struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated,omitempty"`
	Body      string `json:"-"`
}

func Discover(workspace string) ([]File, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, nil
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	absWorkspace = canonicalPath(absWorkspace)
	boundary := absWorkspace
	if root, ok := gitRoot(absWorkspace); ok && isWithin(absWorkspace, root) {
		boundary = root
	}
	return discoverBetween(absWorkspace, boundary)
}

func Render(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<project_memory>\n")
	for _, file := range files {
		builder.WriteString("<file path=\"")
		builder.WriteString(escapeAttr(file.Path))
		builder.WriteString("\"")
		if file.Truncated {
			builder.WriteString(" truncated=\"true\"")
		}
		builder.WriteString(">\n")
		builder.WriteString(strings.TrimSpace(file.Body))
		builder.WriteString("\n</file>\n")
	}
	builder.WriteString("</project_memory>")
	return builder.String()
}

func discoverBetween(workspace string, boundary string) ([]File, error) {
	dirs := dirsFromBoundary(workspace, boundary)
	seen := map[string]struct{}{}
	var files []File
	for _, dir := range dirs {
		for _, name := range CandidateNames {
			path := filepath.Join(dir, name)
			if _, ok := seen[path]; ok {
				continue
			}
			file, ok, err := readCandidate(path, dir, name)
			if err != nil {
				return nil, err
			}
			if ok {
				files = append(files, file)
				seen[path] = struct{}{}
			}
		}
	}
	return files, nil
}

func readCandidate(path string, scope string, name string) (File, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	if info.IsDir() {
		return File{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, false, err
	}
	truncated := false
	if len(data) > MaxFileBytes {
		data = data[:MaxFileBytes]
		truncated = true
	}
	body := string(data)
	return File{
		Path:      path,
		Name:      filepath.ToSlash(name),
		Scope:     scope,
		Chars:     len([]rune(body)),
		Truncated: truncated,
		Body:      body,
	}, true, nil
}

func dirsFromBoundary(workspace string, boundary string) []string {
	var dirs []string
	cursor := filepath.Clean(workspace)
	boundary = filepath.Clean(boundary)
	for {
		dirs = append(dirs, cursor)
		if cursor == boundary {
			break
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func gitRoot(workspace string) (string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	cmd := exec.Command("git", "-C", workspace, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false
	}
	root := strings.TrimSpace(stdout.String())
	if root == "" {
		return "", false
	}
	return canonicalPath(root), true
}

func isWithin(path string, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func canonicalPath(path string) string {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return clean
	}
	return resolved
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return value
}
