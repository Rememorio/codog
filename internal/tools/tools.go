package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionWorkspace Permission = "workspace-write"
	PermissionDanger    Permission = "danger-full-access"
	PermissionPrompt    Permission = "prompt"
	PermissionAllow     Permission = "allow"
)

type Tool interface {
	Definition() anthropic.ToolDefinition
	Permission() Permission
	Execute(context.Context, json.RawMessage) (string, error)
}

type Registry struct {
	tools map[string]Tool
}

type Prompter struct {
	Mode Permission
	In   io.Reader
	Err  io.Writer
}

func NewRegistry(workspace string) *Registry {
	reg := &Registry{tools: map[string]Tool{}}
	reg.Register(BashTool{Workspace: workspace})
	reg.Register(ReadFileTool{Workspace: workspace})
	reg.Register(WriteFileTool{Workspace: workspace})
	reg.Register(EditFileTool{Workspace: workspace})
	reg.Register(GrepTool{Workspace: workspace})
	reg.Register(GlobTool{Workspace: workspace})
	return reg
}

func (r *Registry) Register(tool Tool) {
	r.tools[tool.Definition().Name] = tool
}

func (r *Registry) Definitions() []anthropic.ToolDefinition {
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage, prompter *Prompter) (string, error) {
	tool := r.tools[name]
	if tool == nil {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if prompter != nil {
		if err := prompter.Authorize(name, tool.Permission(), input); err != nil {
			return "", err
		}
	}
	return tool.Execute(ctx, input)
}

func (p *Prompter) Authorize(name string, required Permission, input json.RawMessage) error {
	mode := p.Mode
	if mode == "" {
		mode = PermissionWorkspace
	}
	if mode == PermissionAllow || permissionRank(mode) >= permissionRank(required) {
		return nil
	}
	if p.In == nil {
		p.In = os.Stdin
	}
	if p.Err == nil {
		p.Err = os.Stderr
	}
	fmt.Fprintf(p.Err, "\nTool %s requires %s permission.\nInput: %s\nAllow? [y/N] ", name, required, string(input))
	reader := bufio.NewReader(p.In)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "y" || answer == "yes" {
		return nil
	}
	return fmt.Errorf("permission denied for tool %s", name)
}

func permissionRank(p Permission) int {
	switch p {
	case PermissionReadOnly:
		return 1
	case PermissionWorkspace:
		return 2
	case PermissionDanger:
		return 3
	case PermissionAllow:
		return 4
	default:
		return 0
	}
}

type BashTool struct {
	Workspace string
}

func (BashTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "bash",
		Description: "Execute a shell command in the current workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":     map[string]any{"type": "string"},
				"timeout_ms":  map[string]any{"type": "integer", "minimum": 1},
				"description": map[string]any{"type": "string"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (BashTool) Permission() Permission { return PermissionDanger }

func (t BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Command) == "" {
		return "", errors.New("command is required")
	}
	timeout := time.Duration(payload.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-lc", payload.Command)
	cmd.Dir = t.Workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := map[string]any{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		result["interrupted"] = true
		result["error"] = "timeout"
		return pretty(result), nil
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

type ReadFileTool struct {
	Workspace string
}

func (ReadFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer", "minimum": 0},
				"limit":  map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	}
}

func (ReadFileTool) Permission() Permission { return PermissionReadOnly }

func (t ReadFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePath(t.Workspace, payload.Path, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.Contains(data[:min(len(data), 8192)], []byte{0}) {
		return "", errors.New("file appears to be binary")
	}
	lines := strings.Split(string(data), "\n")
	start := min(max(payload.Offset, 0), len(lines))
	end := len(lines)
	if payload.Limit > 0 {
		end = min(start+payload.Limit, len(lines))
	}
	return pretty(map[string]any{
		"path":       path,
		"start_line": start + 1,
		"total":      len(lines),
		"content":    strings.Join(lines[start:end], "\n"),
	}), nil
}

type WriteFileTool struct {
	Workspace string
}

func (WriteFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "write_file",
		Description: "Create or overwrite a UTF-8 text file in the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
	}
}

func (WriteFileTool) Permission() Permission { return PermissionWorkspace }

func (t WriteFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePath(t.Workspace, payload.Path, true)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	kind := "update"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		kind = "create"
	}
	if err := os.WriteFile(path, []byte(payload.Content), 0o644); err != nil {
		return "", err
	}
	return pretty(map[string]any{"path": path, "kind": kind, "bytes": len(payload.Content)}), nil
}

type EditFileTool struct {
	Workspace string
}

func (EditFileTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace text in a workspace file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string"},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required":             []string{"path", "old_string", "new_string"},
			"additionalProperties": false,
		},
	}
}

func (EditFileTool) Permission() Permission { return PermissionWorkspace }

func (t EditFileTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.OldString == "" {
		return "", errors.New("old_string is required")
	}
	path, err := safePath(t.Workspace, payload.Path, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, payload.OldString)
	if count == 0 {
		return "", errors.New("old_string not found")
	}
	if !payload.ReplaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times; set replace_all to true or provide more context", count)
	}
	next := strings.Replace(content, payload.OldString, payload.NewString, 1)
	if payload.ReplaceAll {
		next = strings.ReplaceAll(content, payload.OldString, payload.NewString)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return "", err
	}
	replaced := 1
	if payload.ReplaceAll {
		replaced = count
	}
	return pretty(map[string]any{"path": path, "replacements": replaced}), nil
}

type GrepTool struct {
	Workspace string
}

func (GrepTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents with a regular expression.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"glob":        map[string]any{"type": "string"},
				"ignore_case": map[string]any{"type": "boolean"},
				"limit":       map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GrepTool) Permission() Permission { return PermissionReadOnly }

func (t GrepTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	pattern := payload.Pattern
	if payload.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	root := t.Workspace
	if payload.Path != "" {
		root, err = safePath(t.Workspace, payload.Path, false)
		if err != nil {
			return "", err
		}
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 100
	}
	var matches []map[string]any
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if payload.Glob != "" {
			ok, _ := filepath.Match(payload.Glob, filepath.Base(path))
			if !ok {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data[:min(len(data), 4096)], []byte{0}) {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(t.Workspace, path)
				matches = append(matches, map[string]any{"path": rel, "line": i + 1, "text": line})
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"matches": matches, "truncated": len(matches) >= limit}), nil
}

type GlobTool struct {
	Workspace string
}

func (GlobTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "glob",
		Description: "Find workspace files by glob pattern.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (GlobTool) Permission() Permission { return PermissionReadOnly }

func (t GlobTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	root := t.Workspace
	var err error
	if payload.Path != "" {
		root, err = safePath(t.Workspace, payload.Path, false)
		if err != nil {
			return "", err
		}
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 200
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(files) >= limit {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if ignoredDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ok, _ := filepath.Match(payload.Pattern, rel)
		if !ok {
			ok, _ = filepath.Match(payload.Pattern, filepath.Base(path))
		}
		if ok {
			workspaceRel, _ := filepath.Rel(t.Workspace, path)
			files = append(files, workspaceRel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	return pretty(map[string]any{"files": files, "truncated": len(files) >= limit}), nil
}

func safePath(workspace, requested string, allowMissing bool) (string, error) {
	if requested == "" {
		return "", errors.New("path is required")
	}
	if workspace == "" {
		workspace = "."
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved := candidate
	if allowMissing {
		parent := filepath.Dir(candidate)
		parentResolved, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", err
		}
		resolved = filepath.Join(parentResolved, filepath.Base(candidate))
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", requested)
	}
	return resolved, nil
}

func ignoredDir(name string) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", "coverage", ".next", ".cache":
		return true
	default:
		return false
	}
}

func pretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
