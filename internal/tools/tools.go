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
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/webaccess"
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
	ConfigHome      string
	MCPServers      map[string]config.MCPServerConfig
	QuestionIn      io.Reader
	QuestionOut     io.Writer
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
	reg.Register(MultiEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(WebFetchTool{})
	reg.Register(WebSearchTool{})
	reg.Register(NotebookEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	reg.Register(EnterPlanModeTool{Workspace: workspace})
	reg.Register(ExitPlanModeTool{Workspace: workspace})
	reg.Register(TaskCreateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskListTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskStatusTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskGetTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskUpdateTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskStopTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TaskOutputTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(TodoReadTool{Workspace: workspace})
	reg.Register(TodoWriteTool{Workspace: workspace})
	reg.Register(SkillTool{Workspace: workspace, ConfigHome: opts.ConfigHome})
	reg.Register(ListMCPResourcesTool{Servers: opts.MCPServers})
	reg.Register(ReadMCPResourceTool{Servers: opts.MCPServers})
	reg.Register(AskUserQuestionTool{In: opts.QuestionIn, Out: opts.QuestionOut})
	reg.Register(ToolSearchTool{Registry: reg})
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
	r.Register(MultiEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GrepTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(GlobTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(NotebookEditTool{Workspace: workspace, AdditionalDirs: opts.AdditionalDirs})
	r.Register(EnterPlanModeTool{Workspace: workspace})
	r.Register(ExitPlanModeTool{Workspace: workspace})
	r.Register(ListMCPResourcesTool{Servers: opts.MCPServers})
	r.Register(ReadMCPResourceTool{Servers: opts.MCPServers})
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

type ListMCPResourcesTool struct {
	Servers map[string]config.MCPServerConfig
}

type listMCPResourcesInput struct {
	Server string `json:"server,omitempty"`
}

func (t ListMCPResourcesTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "list_mcp_resources",
		Description: "List resources exposed by configured MCP servers. Pass server to query one server, or omit it to query all configured servers.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Optional MCP server name. When omitted, all configured servers are queried.",
				},
			},
		},
	}
}

func (ListMCPResourcesTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ListMCPResourcesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload listMCPResourcesInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if payload.Server != "" {
		server, ok := t.Servers[payload.Server]
		if !ok {
			return "", fmt.Errorf("unknown MCP server %q", payload.Server)
		}
		result := mcp.ListResources(ctx, payload.Server, server)
		if result.Error != "" {
			return "", errors.New(result.Error)
		}
		return pretty(result), nil
	}

	names := make([]string, 0, len(t.Servers))
	for name := range t.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	results := make([]mcp.ResourceListResult, 0, len(names))
	for _, name := range names {
		results = append(results, mcp.ListResources(ctx, name, t.Servers[name]))
	}
	return pretty(map[string]any{
		"kind":    "mcp_resources",
		"servers": results,
		"total":   len(results),
	}), nil
}

type ReadMCPResourceTool struct {
	Servers map[string]config.MCPServerConfig
}

type readMCPResourceInput struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

func (t ReadMCPResourceTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "read_mcp_resource",
		Description: "Read a resource URI exposed by a configured MCP server.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":        "string",
					"description": "Configured MCP server name.",
				},
				"uri": map[string]any{
					"type":        "string",
					"description": "Resource URI returned by list_mcp_resources.",
				},
			},
			"required": []string{"server", "uri"},
		},
	}
}

func (ReadMCPResourceTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ReadMCPResourceTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload readMCPResourceInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Server) == "" {
		return "", errors.New("server is required")
	}
	if strings.TrimSpace(payload.URI) == "" {
		return "", errors.New("uri is required")
	}
	server, ok := t.Servers[payload.Server]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", payload.Server)
	}
	result := mcp.ReadResource(ctx, payload.Server, server, payload.URI)
	if result.Error != "" {
		return "", errors.New(result.Error)
	}
	return pretty(result), nil
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

type MultiEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (MultiEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "multi_edit",
		Description: "Apply multiple text replacements to one workspace file atomically.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"edits": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_string":  map[string]any{"type": "string"},
							"new_string":  map[string]any{"type": "string"},
							"replace_all": map[string]any{"type": "boolean"},
						},
						"required":             []string{"old_string", "new_string"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"path", "edits"},
			"additionalProperties": false,
		},
	}
}

func (MultiEditTool) Permission() Permission { return PermissionWorkspace }

func (t MultiEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Path  string `json:"path"`
		Edits []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if len(payload.Edits) == 0 {
		return "", errors.New("edits are required")
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
	total := 0
	for index, edit := range payload.Edits {
		if edit.OldString == "" {
			return "", fmt.Errorf("edits[%d].old_string is required", index)
		}
		count := strings.Count(content, edit.OldString)
		if count == 0 {
			return "", fmt.Errorf("edits[%d].old_string not found", index)
		}
		if !edit.ReplaceAll && count > 1 {
			return "", fmt.Errorf("edits[%d].old_string appears %d times; set replace_all to true or provide more context", index, count)
		}
		replacements := 1
		if edit.ReplaceAll {
			replacements = count
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
		} else {
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		}
		total += replacements
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return pretty(map[string]any{"path": path, "edits": len(payload.Edits), "replacements": total}), nil
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

type WebFetchTool struct{}

func (WebFetchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch an HTTP or HTTPS URL and return extracted text, metadata, and a bounded summary.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":        map[string]any{"type": "string"},
				"prompt":     map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
				"max_bytes":  map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}
}

func (WebFetchTool) Permission() Permission { return PermissionReadOnly }

func (WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload webaccess.FetchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	result, err := webaccess.Fetch(ctx, payload)
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type WebSearchTool struct{}

func (WebSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web using the configured search endpoint and return result titles and URLs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":           map[string]any{"type": "string"},
				"max_results":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
				"allowed_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"blocked_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"timeout_ms":      map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}

func (WebSearchTool) Permission() Permission { return PermissionReadOnly }

func (WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload webaccess.SearchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	result, err := webaccess.Search(ctx, payload)
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type NotebookEditTool struct {
	Workspace      string
	AdditionalDirs []string
}

func (NotebookEditTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "notebook_edit",
		Description: "Replace, insert, or delete a cell in a Jupyter .ipynb notebook inside the workspace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"notebook_path": map[string]any{"type": "string"},
				"cell_index":    map[string]any{"type": "integer", "minimum": 0},
				"cell_type":     map[string]any{"type": "string", "enum": []string{"code", "markdown", "raw"}},
				"new_source":    map[string]any{"type": "string"},
				"edit_mode":     map[string]any{"type": "string", "enum": []string{"replace", "insert", "delete"}},
			},
			"required":             []string{"notebook_path", "cell_index"},
			"additionalProperties": false,
		},
	}
}

func (NotebookEditTool) Permission() Permission { return PermissionWorkspace }

func (t NotebookEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		NotebookPath string `json:"notebook_path"`
		CellIndex    int    `json:"cell_index"`
		CellType     string `json:"cell_type"`
		NewSource    string `json:"new_source"`
		EditMode     string `json:"edit_mode"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	path, err := safePathInScope(t.Workspace, t.AdditionalDirs, payload.NotebookPath, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return "", errors.New("notebook_path must point to a .ipynb file")
	}
	result, err := codeintel.EditNotebook(path, codeintel.NotebookEditOptions{
		Index:    payload.CellIndex,
		CellType: payload.CellType,
		Source:   payload.NewSource,
		Mode:     payload.EditMode,
	})
	if err != nil {
		return "", err
	}
	return pretty(result), nil
}

type EnterPlanModeTool struct {
	Workspace string
}

type planModeInput struct {
	Plan string `json:"plan,omitempty"`
}

func (EnterPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "enter_plan_mode",
		Description: "Enter plan mode and optionally persist the current implementation plan. While plan mode is active, future tool permission checks are read-only until exit_plan_mode is called or the user exits plan mode.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional plan text to store with the active plan-mode state.",
				},
			},
		},
	}
}

func (EnterPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t EnterPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	report, err := planmode.Enter(t.Workspace, payload.Plan)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type ExitPlanModeTool struct {
	Workspace string
}

func (ExitPlanModeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "exit_plan_mode",
		Description: "Exit plan mode. Include the final implementation plan to persist it before returning to normal tool permissions on the next user turn.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"plan": map[string]any{
					"type":        "string",
					"description": "Optional final plan text to store before leaving plan mode.",
				},
			},
		},
	}
}

func (ExitPlanModeTool) Permission() Permission {
	return PermissionReadOnly
}

func (t ExitPlanModeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload planModeInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(payload.Plan) != "" {
		if _, err := planmode.Set(t.Workspace, payload.Plan); err != nil {
			return "", err
		}
	}
	report, err := planmode.Exit(t.Workspace)
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type TaskCreateTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_create",
		Description: "Start a background shell task in the workspace and return its task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string"},
				"kind":       map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (TaskCreateTool) Permission() Permission { return PermissionDanger }

func (t TaskCreateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command   string `json:"command"`
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(payload.Command, t.Workspace, background.RunOptions{
		Kind:      payload.Kind,
		SessionID: payload.SessionID,
	})
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskListTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_list",
		Description: "List background tasks, optionally filtered by session or kind.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
				"kind":       map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (TaskListTool) Permission() Permission { return PermissionReadOnly }

func (t TaskListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	tasks, err := taskStore(t.ConfigHome, t.Workspace).List()
	if err != nil {
		return "", err
	}
	tasks = background.FilterBySession(tasks, payload.SessionID)
	tasks = background.FilterByKind(tasks, payload.Kind)
	return pretty(map[string]any{"tasks": tasks, "total": len(tasks)}), nil
}

type TaskStatusTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_status",
		Description: "Get background task metadata by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskStatusTool) Permission() Permission { return PermissionReadOnly }

func (t TaskStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(payload.ID)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskGetTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskGetTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_get",
		Description: "Get background task metadata and stored task messages by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
	}
}

func (TaskGetTool) Permission() Permission { return PermissionReadOnly }

func (t TaskGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := payload.TaskID
	if id == "" {
		id = payload.ID
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(id)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskUpdateTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskUpdateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_update",
		Description: "Append a message update to a background task registry entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
			"required":             []string{"task_id", "message"},
			"additionalProperties": false,
		},
	}
}

func (TaskUpdateTool) Permission() Permission { return PermissionDanger }

func (t TaskUpdateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID  string `json:"task_id"`
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := payload.TaskID
	if id == "" {
		id = payload.ID
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Update(id, payload.Message)
	if err != nil {
		return "", err
	}
	last := ""
	if len(task.Messages) > 0 {
		last = task.Messages[len(task.Messages)-1].Message
	}
	return pretty(map[string]any{
		"id":            task.ID,
		"status":        task.Status,
		"message_count": len(task.Messages),
		"last_message":  last,
	}), nil
}

type TaskStopTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStopTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_stop",
		Description: "Stop a running background task by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskStopTool) Permission() Permission { return PermissionWorkspace }

func (t TaskStopTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Stop(payload.ID)
	if err != nil {
		return "", err
	}
	return pretty(task), nil
}

type TaskOutputTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskOutputTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_output",
		Description: "Read recent background task log output by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string"},
				"limit_bytes": map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (TaskOutputTool) Permission() Permission { return PermissionReadOnly }

func (t TaskOutputTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID         string `json:"id"`
		LimitBytes int64  `json:"limit_bytes"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.LimitBytes <= 0 {
		payload.LimitBytes = 64 * 1024
	}
	output, err := taskStore(t.ConfigHome, t.Workspace).Logs(payload.ID, payload.LimitBytes)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"id": payload.ID, "output": output}), nil
}

func taskStore(configHome string, workspace string) background.Store {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return background.NewStore(configHome)
}

type AskUserQuestionTool struct {
	In  io.Reader
	Out io.Writer
}

func (AskUserQuestionTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "ask_user_question",
		Description: "Ask the user a concise question and return their answer to the model.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"choices":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"default":  map[string]any{"type": "string"},
			},
			"required":             []string{"question"},
			"additionalProperties": false,
		},
	}
}

func (AskUserQuestionTool) Permission() Permission { return PermissionReadOnly }

func (t AskUserQuestionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices"`
		Default  string   `json:"default"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	payload.Question = strings.TrimSpace(payload.Question)
	if payload.Question == "" {
		return "", errors.New("question is required")
	}
	in := t.In
	if in == nil {
		in = os.Stdin
	}
	out := t.Out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "\n%s\n", payload.Question)
	choices := normalizeQuestionChoices(payload.Choices)
	for index, choice := range choices {
		fmt.Fprintf(out, "  %d. %s\n", index+1, choice)
	}
	if strings.TrimSpace(payload.Default) != "" {
		fmt.Fprintf(out, "Default: %s\n", strings.TrimSpace(payload.Default))
	}
	fmt.Fprint(out, "Answer: ")

	answerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			errCh <- err
			return
		}
		answerCh <- strings.TrimSpace(line)
	}()
	var answer string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case answer = <-answerCh:
	}
	if answer == "" {
		answer = strings.TrimSpace(payload.Default)
	}
	answer = resolveQuestionChoice(answer, choices)
	return pretty(map[string]any{
		"question": payload.Question,
		"answer":   answer,
	}), nil
}

func normalizeQuestionChoices(choices []string) []string {
	out := make([]string, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		choice = strings.TrimSpace(choice)
		if choice == "" {
			continue
		}
		key := strings.ToLower(choice)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, choice)
	}
	return out
}

func resolveQuestionChoice(answer string, choices []string) string {
	if answer == "" || len(choices) == 0 {
		return answer
	}
	if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(choices) {
		return choices[index-1]
	}
	for _, choice := range choices {
		if strings.EqualFold(answer, choice) {
			return choice
		}
	}
	return answer
}

type SkillTool struct {
	Workspace  string
	ConfigHome string
}

func (SkillTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "skill",
		Description: "Load a local Codog or Claude-style skill definition and render its invocation text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill": map[string]any{
					"type":        "string",
					"description": "Skill name, such as review or team:audit.",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional user request or arguments to render with the skill.",
				},
			},
			"required":             []string{"skill"},
			"additionalProperties": false,
		},
	}
}

func (SkillTool) Permission() Permission {
	return PermissionReadOnly
}

func (t SkillTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Skill string `json:"skill"`
		Args  string `json:"args"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Skill) == "" {
		return "", errors.New("skill is required")
	}
	skill, err := skills.Find(t.ConfigHome, t.Workspace, payload.Skill)
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"kind":     "skill",
		"skill":    skill.Name,
		"source":   skill.Source,
		"path":     skill.Path,
		"args":     strings.TrimSpace(payload.Args),
		"prompt":   skill.Body,
		"rendered": skills.RenderInvocation(skill, payload.Args),
	}), nil
}

type ToolSearchTool struct {
	Registry *Registry
}

func (ToolSearchTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "tool_search",
		Description: "Search the currently available Codog tools by name, description, or permission.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string"},
				"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
			"additionalProperties": false,
		},
	}
}

func (ToolSearchTool) Permission() Permission { return PermissionReadOnly }

func (t ToolSearchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", errors.New("tool registry is not available")
	}
	var payload struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	limit := payload.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	matches := searchToolInfos(t.Registry.Infos(), payload.Query, limit)
	return pretty(map[string]any{
		"query":   strings.TrimSpace(payload.Query),
		"matches": matches,
		"total":   len(matches),
	}), nil
}

func searchToolInfos(infos []ToolInfo, query string, limit int) []ToolInfo {
	query = strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(query)
	type scored struct {
		info  ToolInfo
		score int
	}
	scoredMatches := make([]scored, 0, len(infos))
	for _, info := range infos {
		score := 1
		if query != "" {
			score = toolInfoScore(info, terms, query)
			if score == 0 {
				continue
			}
		}
		scoredMatches = append(scoredMatches, scored{info: info, score: score})
	}
	sort.Slice(scoredMatches, func(i, j int) bool {
		if scoredMatches[i].score != scoredMatches[j].score {
			return scoredMatches[i].score > scoredMatches[j].score
		}
		return scoredMatches[i].info.Name < scoredMatches[j].info.Name
	})
	if len(scoredMatches) > limit {
		scoredMatches = scoredMatches[:limit]
	}
	matches := make([]ToolInfo, 0, len(scoredMatches))
	for _, match := range scoredMatches {
		matches = append(matches, match.info)
	}
	return matches
}

func toolInfoScore(info ToolInfo, terms []string, query string) int {
	haystack := strings.ToLower(info.Name + " " + info.Description + " " + string(info.Permission))
	score := 0
	if strings.EqualFold(info.Name, query) {
		score += 20
	}
	if strings.Contains(strings.ToLower(info.Name), query) {
		score += 10
	}
	for _, term := range terms {
		if strings.Contains(strings.ToLower(info.Name), term) {
			score += 6
		}
		if strings.Contains(haystack, term) {
			score += 2
		} else {
			return 0
		}
	}
	return score
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
