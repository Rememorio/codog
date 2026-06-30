package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/tools"
)

type Options struct {
	Version         string
	Workspace       string
	PermissionMode  string
	PermissionRules config.PermissionRules
}

const maxResourceReadBytes = 256 * 1024

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, registry *tools.Registry, opts Options) error {
	if registry == nil {
		return fmt.Errorf("tool registry is required")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := writeError(out, nil, -32700, err.Error()); err != nil {
				return err
			}
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		if err := handle(ctx, out, registry, opts, req); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handle(ctx context.Context, out io.Writer, registry *tools.Registry, opts Options, req request) error {
	switch req.Method {
	case "initialize":
		version := strings.TrimSpace(opts.Version)
		if version == "" {
			version = "0.1.0"
		}
		return writeResult(out, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
				"prompts":   map[string]any{},
			},
			"serverInfo": map[string]any{"name": "codog", "version": version},
		})
	case "tools/list":
		return writeResult(out, req.ID, map[string]any{"tools": ExposedTools(registry)})
	case "tools/call":
		return handleToolCall(ctx, out, registry, opts, req)
	case "resources/list":
		return writeResult(out, req.ID, map[string]any{"resources": LocalResources(opts)})
	case "resources/read":
		return handleResourceRead(out, registry, opts, req)
	case "resources/templates/list":
		return writeResult(out, req.ID, map[string]any{"resourceTemplates": LocalResourceTemplates()})
	case "prompts/list":
		return writeResult(out, req.ID, map[string]any{"prompts": LocalPrompts()})
	case "prompts/get":
		return handlePromptGet(out, req)
	default:
		return writeError(out, req.ID, -32601, "method not found: "+req.Method)
	}
}

func handleToolCall(ctx context.Context, out io.Writer, registry *tools.Registry, opts Options, req request) error {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	if strings.TrimSpace(params.Name) == "" {
		return writeError(out, req.ID, -32602, "tool name is required")
	}
	if !toolExposed(params.Name) {
		return writeError(out, req.ID, -32601, "tool not exposed over MCP: "+params.Name)
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	prompter := &tools.Prompter{
		Mode:        tools.Permission(opts.PermissionMode),
		AllowRules:  append([]string(nil), opts.PermissionRules.Allow...),
		DenyRules:   append([]string(nil), opts.PermissionRules.Deny...),
		AskRules:    append([]string(nil), opts.PermissionRules.Ask...),
		DeniedTools: append([]string(nil), opts.PermissionRules.DeniedTools...),
		Workspace:   opts.Workspace,
		In:          strings.NewReader("\n"),
		Err:         io.Discard,
	}
	text, err := registry.Execute(ctx, params.Name, params.Arguments, prompter)
	result := map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	}
	if err != nil {
		result["isError"] = true
		result["content"] = []map[string]string{{"type": "text", "text": err.Error()}}
	}
	return writeResult(out, req.ID, result)
}

func handleResourceRead(out io.Writer, registry *tools.Registry, opts Options, req request) error {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	content, err := readLocalResource(params.URI, registry, opts)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	return writeResult(out, req.ID, map[string]any{"contents": []map[string]string{content}})
}

func LocalResources(opts Options) []map[string]any {
	return []map[string]any{
		{
			"uri":         "codog://workspace",
			"name":        "workspace",
			"description": "Codog workspace metadata.",
			"mimeType":    "application/json",
		},
		{
			"uri":         "codog://status",
			"name":        "status",
			"description": "Codog MCP server status and permission mode.",
			"mimeType":    "application/json",
		},
		{
			"uri":         "codog://tools",
			"name":        "tools",
			"description": "Tools exposed by this Codog MCP server.",
			"mimeType":    "application/json",
		},
	}
}

func LocalResourceTemplates() []map[string]any {
	return []map[string]any{
		{
			"uriTemplate": "codog://file/{path}",
			"name":        "workspace file",
			"description": "Read a UTF-8 file under the current workspace by relative path.",
			"mimeType":    "text/plain",
		},
	}
}

func readLocalResource(uri string, registry *tools.Registry, opts Options) (map[string]string, error) {
	uri = strings.TrimSpace(uri)
	switch uri {
	case "codog://workspace":
		return jsonResource(uri, map[string]any{
			"kind":       "workspace",
			"workspace":  opts.Workspace,
			"server":     "codog",
			"permission": opts.PermissionMode,
		})
	case "codog://status":
		return jsonResource(uri, map[string]any{
			"kind":            "mcp_status",
			"status":          "ok",
			"workspace":       opts.Workspace,
			"permission_mode": opts.PermissionMode,
			"tool_count":      len(ExposedTools(registry)),
		})
	case "codog://tools":
		return jsonResource(uri, map[string]any{
			"kind":  "mcp_tools",
			"tools": ExposedTools(registry),
		})
	default:
		if strings.HasPrefix(uri, "codog://file/") {
			return readWorkspaceFileResource(uri, opts)
		}
		return nil, fmt.Errorf("unknown resource URI %q", uri)
	}
}

func jsonResource(uri string, payload any) (map[string]string, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return map[string]string{"uri": uri, "mimeType": "application/json", "text": string(data)}, nil
}

func readWorkspaceFileResource(uri string, opts Options) (map[string]string, error) {
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		return nil, fmt.Errorf("workspace is not configured")
	}
	rawPath := strings.TrimPrefix(uri, "codog://file/")
	relPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, err
	}
	path, err := resolveWorkspaceResourcePath(workspace, relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)
	if len(data) > maxResourceReadBytes {
		text = string(data[:maxResourceReadBytes]) + "\n[truncated]"
	}
	return map[string]string{"uri": uri, "mimeType": "text/plain", "text": text}, nil
}

func resolveWorkspaceResourcePath(workspace, relPath string) (string, error) {
	relPath = strings.TrimPrefix(filepath.Clean(strings.TrimSpace(relPath)), string(filepath.Separator))
	if relPath == "." || relPath == "" {
		return "", fmt.Errorf("file path is required")
	}
	if filepath.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file path escapes workspace")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, relPath))
	if err != nil {
		return "", err
	}
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("file path escapes workspace")
	}
	return path, nil
}

func ExposedTools(registry *tools.Registry) []map[string]any {
	if registry == nil {
		return []map[string]any{}
	}
	infos := registry.Infos()
	list := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		if !toolExposed(info.Name) {
			continue
		}
		list = append(list, map[string]any{
			"name":        info.Name,
			"description": info.Description,
			"inputSchema": info.InputSchema,
		})
	}
	return list
}

func LocalPrompts() []map[string]any {
	return []map[string]any{
		{
			"name":        "review_changes",
			"description": "Review current workspace changes and identify correctness risks.",
			"arguments": []map[string]any{{
				"name":        "focus",
				"description": "Optional review focus.",
				"required":    false,
			}},
		},
		{
			"name":        "explain_workspace",
			"description": "Explain the current workspace structure and important entry points.",
		},
		{
			"name":        "summarize_file",
			"description": "Summarize a workspace file.",
			"arguments": []map[string]any{{
				"name":        "path",
				"description": "Workspace-relative file path.",
				"required":    true,
			}},
		},
	}
}

func handlePromptGet(out io.Writer, req request) error {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	text, err := localPromptText(params.Name, params.Arguments)
	if err != nil {
		return writeError(out, req.ID, -32602, err.Error())
	}
	return writeResult(out, req.ID, map[string]any{
		"description": "Codog prompt template " + strings.TrimSpace(params.Name),
		"messages": []map[string]any{{
			"role":    "user",
			"content": map[string]string{"type": "text", "text": text},
		}},
	})
}

func localPromptText(name string, arguments map[string]any) (string, error) {
	switch strings.TrimSpace(name) {
	case "review_changes":
		focus := strings.TrimSpace(fmt.Sprint(arguments["focus"]))
		if focus == "" || focus == "<nil>" {
			return "Review the current workspace changes. Prioritize bugs, regressions, security issues, and missing tests.", nil
		}
		return "Review the current workspace changes with this focus: " + focus, nil
	case "explain_workspace":
		return "Explain the current workspace structure, main packages, entry points, and how to run tests.", nil
	case "summarize_file":
		path := strings.TrimSpace(fmt.Sprint(arguments["path"]))
		if path == "" || path == "<nil>" {
			return "", fmt.Errorf("path argument is required")
		}
		return "Summarize the workspace file `" + path + "` and call out important APIs, side effects, and tests.", nil
	default:
		return "", fmt.Errorf("unknown prompt %q", name)
	}
}

func toolExposed(name string) bool {
	switch name {
	case "ask_user_question":
		return false
	default:
		return true
	}
}

func writeResult(out io.Writer, id json.RawMessage, result any) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}

func writeError(out io.Writer, id json.RawMessage, code int, message string) error {
	data, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &responseError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}
