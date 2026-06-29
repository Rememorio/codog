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
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/todos"
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

type CommandTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	Command     string
	Args        []string
	Workspace   string
}

type MCPTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Required    Permission
	ServerName  string
	Server      config.MCPServerConfig
	RemoteName  string
}

type Registry struct {
	tools map[string]Tool
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Permission  Permission     `json:"permission"`
	InputSchema map[string]any `json:"input_schema"`
}

type RegistryOptions struct {
	SandboxStrategy string
	AdditionalDirs  []string
}

type Prompter struct {
	Mode        Permission
	AllowRules  []string
	DenyRules   []string
	AskRules    []string
	DeniedTools []string
	In          io.Reader
	Err         io.Writer
	OnDecision  func(PermissionDecision)
}

type PermissionDecision struct {
	ToolName string
	Required Permission
	Mode     Permission
	Input    string
	Allowed  bool
	Reason   string
}

func NewRegistry(workspace string) *Registry {
	return NewRegistryWithOptions(workspace, RegistryOptions{})
}

func NewRegistryWithOptions(workspace string, opts RegistryOptions) *Registry {
	reg := &Registry{tools: map[string]Tool{}}
	reg.Register(BashTool{Workspace: workspace, SandboxStrategy: opts.SandboxStrategy})
	reg.Register(ReadFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(WriteFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(EditFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(TodoReadTool{Workspace: workspace})
	reg.Register(TodoWriteTool{Workspace: workspace})
	return reg
}

func (r *Registry) Register(tool Tool) {
	r.tools[tool.Definition().Name] = tool
}

func (r *Registry) UpdateBuiltinScope(workspace string, opts RegistryOptions) {
	r.Register(BashTool{Workspace: workspace, SandboxStrategy: opts.SandboxStrategy})
	r.Register(ReadFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(WriteFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(EditFileTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
}

func (r *Registry) Has(name string) bool {
	return r.tools[name] != nil
}

func (r *Registry) Definitions() []anthropic.ToolDefinition {
	defs := make([]anthropic.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *Registry) Infos() []ToolInfo {
	infos := make([]ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		def := tool.Definition()
		infos = append(infos, ToolInfo{
			Name:        def.Name,
			Description: def.Description,
			Permission:  tool.Permission(),
			InputSchema: def.InputSchema,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func (r *Registry) Info(name string) (ToolInfo, bool) {
	for _, info := range r.Infos() {
		if strings.EqualFold(info.Name, name) {
			return info, true
		}
	}
	return ToolInfo{}, false
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
	inputText := string(input)
	if ruleMatchesTool(p.DeniedTools, name) {
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: false, Reason: "denied_tools"})
		return fmt.Errorf("permission denied for tool %s by denied_tools", name)
	}
	if ruleMatches(p.DenyRules, name, inputText) {
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: false, Reason: "deny_rule"})
		return fmt.Errorf("permission denied for tool %s by deny rule", name)
	}
	if ruleMatches(p.AllowRules, name, inputText) {
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: true, Reason: "allow_rule"})
		return nil
	}
	ask := mode == PermissionPrompt || ruleMatches(p.AskRules, name, inputText)
	if !ask && (mode == PermissionAllow || permissionRank(mode) >= permissionRank(required)) {
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: true, Reason: "permission_mode"})
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
		p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: true, Reason: "user_approved"})
		return nil
	}
	p.emitDecision(PermissionDecision{ToolName: name, Required: required, Mode: mode, Input: inputText, Allowed: false, Reason: "user_denied"})
	return fmt.Errorf("permission denied for tool %s", name)
}

func (p *Prompter) emitDecision(decision PermissionDecision) {
	if p.OnDecision != nil {
		p.OnDecision(decision)
	}
}

func ruleMatches(rules []string, toolName, input string) bool {
	for _, rule := range rules {
		if rule == "*" || strings.EqualFold(rule, toolName) {
			return true
		}
		prefix, needle, ok := strings.Cut(rule, ":")
		if ok && strings.EqualFold(prefix, toolName) && strings.Contains(input, needle) {
			return true
		}
	}
	return false
}

func ruleMatchesTool(rules []string, toolName string) bool {
	for _, rule := range rules {
		if rule == "*" || strings.EqualFold(rule, toolName) {
			return true
		}
	}
	return false
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

func (t CommandTool) Definition() anthropic.ToolDefinition {
	schema := t.Schema
	if schema == nil {
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	return anthropic.ToolDefinition{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: schema,
	}
}

func (t CommandTool) Permission() Permission {
	if t.Required == "" {
		return PermissionWorkspace
	}
	return t.Required
}

func (t CommandTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if strings.TrimSpace(t.Command) == "" {
		return "", fmt.Errorf("plugin tool %s has no command", t.Name)
	}
	cmd := exec.CommandContext(ctx, t.Command, t.Args...)
	cmd.Dir = t.Workspace
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := map[string]any{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return pretty(result), nil
}

func NewMCPToolName(serverName, toolName string) string {
	return "mcp__" + toolNameComponent(serverName, "server") + "__" + toolNameComponent(toolName, "tool")
}

func toolNameComponent(value, fallback string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	component := strings.Trim(builder.String(), "_-")
	if component == "" {
		return fallback
	}
	return component
}

func (t MCPTool) Definition() anthropic.ToolDefinition {
	schema := t.Schema
	if schema == nil {
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	description := t.Description
	if description == "" {
		description = fmt.Sprintf("Call MCP tool %s on server %s.", t.RemoteName, t.ServerName)
	}
	return anthropic.ToolDefinition{
		Name:        t.Name,
		Description: description,
		InputSchema: schema,
	}
}

func (t MCPTool) Permission() Permission {
	if t.Required == "" {
		return PermissionWorkspace
	}
	return t.Required
}

func (t MCPTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	result := mcp.CallTool(ctx, t.ServerName, t.Server, t.RemoteName, input)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	if len(result.Result) == 0 {
		return "{}", nil
	}
	return string(result.Result), nil
}

type BashTool struct {
	Workspace       string
	SandboxStrategy string
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
	command, args, effectiveSandbox, err := sandbox.ShellCommand(t.SandboxStrategy, t.Workspace, payload.Command)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = t.Workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := map[string]any{
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if effectiveSandbox != "" {
		result["sandbox"] = effectiveSandbox
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
	Workspace      string
	AdditionalDirs []string
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
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
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
	Workspace      string
	AdditionalDirs []string
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
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, true)
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
	Workspace      string
	AdditionalDirs []string
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
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
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
	Workspace      string
	AdditionalDirs []string
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
		root, err = safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
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
				matches = append(matches, map[string]any{"path": displayPath(t.Workspace, path), "line": i + 1, "text": line})
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
	Workspace      string
	AdditionalDirs []string
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
		root, err = safePathInScope(t.Workspace, t.AdditionalDirs, payload.Path, false)
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
			files = append(files, displayPath(t.Workspace, path))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	return pretty(map[string]any{"files": files, "truncated": len(files) >= limit}), nil
}

type TodoReadTool struct {
	Workspace string
}

func (TodoReadTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_read",
		Description: "Read the workspace todo list for the current task.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}
}

func (TodoReadTool) Permission() Permission { return PermissionReadOnly }

func (t TodoReadTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	report, err := todos.List(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type TodoWriteTool struct {
	Workspace string
}

func (TodoWriteTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "todo_write",
		Description: "Replace the workspace todo list. Use pending, in_progress, or completed status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string"},
							"content":  map[string]any{"type": "string"},
							"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
							"priority": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
						},
						"required":             []string{"content"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
	}
}

func (TodoWriteTool) Permission() Permission { return PermissionWorkspace }

func (t TodoWriteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Todos []todos.Item `json:"todos"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	report, err := todos.Replace(t.Workspace, payload.Todos)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

func safePath(workspace, requested string, allowMissing bool) (string, error) {
	return safePathInScope(workspace, nil, requested, allowMissing)
}

func safePathInScope(workspace string, additionalDirs []string, requested string, allowMissing bool) (string, error) {
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
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots[0] = resolved
	} else {
		return "", err
	}
	for _, dir := range additionalDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		roots = append(roots, resolved)
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	resolved := ""
	if allowMissing {
		resolved, err = resolveMissingCandidate(candidate)
		if err != nil {
			return "", err
		}
	} else {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", err
		}
	}
	for _, root := range roots {
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path escapes workspace scope: %s", requested)
}

func resolveMissingCandidate(candidate string) (string, error) {
	var missing []string
	cursor := candidate
	for {
		resolved, err := filepath.EvalSymlinks(cursor)
		if err == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", err
		}
		missing = append([]string{filepath.Base(cursor)}, missing...)
		cursor = parent
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func displayPath(workspace string, path string) string {
	root, err := filepath.Abs(workspace)
	if err == nil {
		if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
			root = resolved
		}
		if rel, relErr := filepath.Rel(root, path); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return rel
		}
	}
	return path
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
