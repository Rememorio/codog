package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "codog", "version": version},
		})
	case "tools/list":
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
		return writeResult(out, req.ID, map[string]any{"tools": list})
	case "tools/call":
		return handleToolCall(ctx, out, registry, opts, req)
	case "resources/list":
		return writeResult(out, req.ID, map[string]any{"resources": []any{}})
	case "resources/templates/list":
		return writeResult(out, req.ID, map[string]any{"resourceTemplates": []any{}})
	case "prompts/list":
		return writeResult(out, req.ID, map[string]any{"prompts": []any{}})
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
