package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/Rememorio/codog/internal/config"
)

type ServerStatus struct {
	Name  string   `json:"name"`
	Tools []string `json:"tools,omitempty"`
	Error string   `json:"error,omitempty"`
}

type InitializeResult struct {
	Server          string          `json:"server"`
	Status          string          `json:"status"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ServerInfo      json.RawMessage `json:"server_info,omitempty"`
	Error           string          `json:"error,omitempty"`
}

type AuthStatusResult struct {
	Server        string          `json:"server"`
	Status        string          `json:"status"`
	ServerInfo    json.RawMessage `json:"server_info,omitempty"`
	Capabilities  json.RawMessage `json:"capabilities,omitempty"`
	ToolCount     int             `json:"tool_count"`
	ResourceCount int             `json:"resource_count"`
	ResourceError string          `json:"resource_error,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type ToolListResult struct {
	Server string     `json:"server"`
	Tools  []ToolInfo `json:"tools,omitempty"`
	Error  string     `json:"error,omitempty"`
}

type ToolCallResult struct {
	Server string          `json:"server"`
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type ResourceListResult struct {
	Server    string          `json:"server"`
	Resources json.RawMessage `json:"resources,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type ResourceReadResult struct {
	Server string          `json:"server"`
	URI    string          `json:"uri"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type ResourceTemplateListResult struct {
	Server    string          `json:"server"`
	Templates json.RawMessage `json:"templates,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type PromptListResult struct {
	Server  string          `json:"server"`
	Prompts json.RawMessage `json:"prompts,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type PromptGetResult struct {
	Server string          `json:"server"`
	Prompt string          `json:"prompt"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func InspectAll(ctx context.Context, servers map[string]config.MCPServerConfig) []ServerStatus {
	statuses := make([]ServerStatus, 0, len(servers))
	for name, server := range servers {
		statuses = append(statuses, Inspect(ctx, name, server))
	}
	return statuses
}

func Inspect(ctx context.Context, name string, server config.MCPServerConfig) ServerStatus {
	result := ListTools(ctx, name, server)
	if result.Error != "" {
		return ServerStatus{Name: name, Error: result.Error}
	}
	tools := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		tools = append(tools, tool.Name)
	}
	return ServerStatus{Name: name, Tools: tools}
}

func Initialize(ctx context.Context, serverName string, server config.MCPServerConfig) InitializeResult {
	if server.Command == "" {
		return InitializeResult{Server: serverName, Status: "error", Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)
	if err := send(stdin, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "codog", "version": "0.1.0"},
		},
	}); err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	resp, err := readResponse(reader)
	if err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	if resp.Error != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: resp.Error.Message}
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})

	var payload struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      json.RawMessage `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}
	}
	return InitializeResult{
		Server:          serverName,
		Status:          "ok",
		ProtocolVersion: payload.ProtocolVersion,
		Capabilities:    payload.Capabilities,
		ServerInfo:      payload.ServerInfo,
	}
}

func InspectAuth(ctx context.Context, serverName string, server config.MCPServerConfig) AuthStatusResult {
	initialized := Initialize(ctx, serverName, server)
	if initialized.Error != "" {
		return AuthStatusResult{Server: serverName, Status: "error", Error: initialized.Error}
	}
	result := AuthStatusResult{
		Server:       serverName,
		Status:       initialized.Status,
		ServerInfo:   initialized.ServerInfo,
		Capabilities: initialized.Capabilities,
	}
	tools := ListTools(ctx, serverName, server)
	if tools.Error != "" {
		result.Status = "error"
		result.Error = tools.Error
		return result
	}
	result.ToolCount = len(tools.Tools)
	resources := ListResources(ctx, serverName, server)
	if resources.Error != "" {
		result.ResourceError = resources.Error
		return result
	}
	result.ResourceCount = countJSONArrayField(resources.Resources, "resources")
	return result
}

func countJSONArrayField(raw json.RawMessage, field string) int {
	if len(raw) == 0 {
		return 0
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal(payload[field], &items); err != nil {
		return 0
	}
	return len(items)
}

func ListTools(ctx context.Context, serverName string, server config.MCPServerConfig) ToolListResult {
	if server.Command == "" {
		return ToolListResult{Server: serverName, Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)
	if err := send(stdin, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "codog", "version": "0.1.0"},
		},
	}); err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	if _, err := readResponse(reader); err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	resp, err := readResponse(reader)
	if err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	if resp.Error != nil {
		return ToolListResult{Server: serverName, Error: resp.Error.Message}
	}
	var payload struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return ToolListResult{Server: serverName, Error: err.Error()}
	}
	tools := make([]ToolInfo, 0, len(payload.Tools))
	for _, rawTool := range payload.Tools {
		tool, err := decodeTool(rawTool)
		if err != nil {
			return ToolListResult{Server: serverName, Error: err.Error()}
		}
		if tool.Name == "" {
			continue
		}
		tools = append(tools, tool)
	}
	return ToolListResult{Server: serverName, Tools: tools}
}

func decodeTool(raw map[string]json.RawMessage) (ToolInfo, error) {
	var tool ToolInfo
	if data := raw["name"]; len(data) != 0 {
		if err := json.Unmarshal(data, &tool.Name); err != nil {
			return ToolInfo{}, err
		}
	}
	if data := raw["description"]; len(data) != 0 {
		_ = json.Unmarshal(data, &tool.Description)
	}
	schema := raw["inputSchema"]
	if len(schema) == 0 {
		schema = raw["input_schema"]
	}
	if len(schema) != 0 && string(schema) != "null" {
		if err := json.Unmarshal(schema, &tool.InputSchema); err != nil {
			return ToolInfo{}, err
		}
	}
	if tool.InputSchema == nil {
		tool.InputSchema = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	return tool, nil
}

func CallTool(ctx context.Context, serverName string, server config.MCPServerConfig, toolName string, arguments json.RawMessage) ToolCallResult {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": json.RawMessage(arguments),
		},
	})
	if err != nil {
		return ToolCallResult{Server: serverName, Tool: toolName, Error: err.Error()}
	}
	return ToolCallResult{Server: serverName, Tool: toolName, Result: result}
}

func ListResources(ctx context.Context, serverName string, server config.MCPServerConfig) ResourceListResult {
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "resources/list",
	})
	if err != nil {
		return ResourceListResult{Server: serverName, Error: err.Error()}
	}
	return ResourceListResult{Server: serverName, Resources: result}
}

func ReadResource(ctx context.Context, serverName string, server config.MCPServerConfig, uri string) ResourceReadResult {
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "resources/read",
		Params:  map[string]any{"uri": uri},
	})
	if err != nil {
		return ResourceReadResult{Server: serverName, URI: uri, Error: err.Error()}
	}
	return ResourceReadResult{Server: serverName, URI: uri, Result: result}
}

func ListResourceTemplates(ctx context.Context, serverName string, server config.MCPServerConfig) ResourceTemplateListResult {
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "resources/templates/list",
	})
	if err != nil {
		return ResourceTemplateListResult{Server: serverName, Error: err.Error()}
	}
	return ResourceTemplateListResult{Server: serverName, Templates: result}
}

func ListPrompts(ctx context.Context, serverName string, server config.MCPServerConfig) PromptListResult {
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "prompts/list",
	})
	if err != nil {
		return PromptListResult{Server: serverName, Error: err.Error()}
	}
	return PromptListResult{Server: serverName, Prompts: result}
}

func GetPrompt(ctx context.Context, serverName string, server config.MCPServerConfig, promptName string, arguments json.RawMessage) PromptGetResult {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	result, err := requestAfterInitialize(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "prompts/get",
		Params: map[string]any{
			"name":      promptName,
			"arguments": json.RawMessage(arguments),
		},
	})
	if err != nil {
		return PromptGetResult{Server: serverName, Prompt: promptName, Error: err.Error()}
	}
	return PromptGetResult{Server: serverName, Prompt: promptName, Result: result}
}

func requestAfterInitialize(ctx context.Context, server config.MCPServerConfig, req rpcRequest) (json.RawMessage, error) {
	if server.Command == "" {
		return nil, fmt.Errorf("missing command")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer cmd.Process.Kill()

	reader := bufio.NewReader(stdout)
	if err := send(stdin, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "codog", "version": "0.1.0"},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := readResponse(reader); err != nil {
		return nil, err
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	if err := send(stdin, req); err != nil {
		return nil, err
	}
	resp, err := readResponse(reader)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, errors.New(resp.Error.Message)
	}
	return resp.Result, nil
}

func send(w io.Writer, req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func readResponse(r *bufio.Reader) (rpcResponse, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return rpcResponse{}, err
	}
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return rpcResponse{}, err
	}
	return resp, nil
}
