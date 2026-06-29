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
	if server.Command == "" {
		return ServerStatus{Name: name, Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
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
		return ServerStatus{Name: name, Error: err.Error()}
	}
	if _, err := readResponse(reader); err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	resp, err := readResponse(reader)
	if err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
	}
	if resp.Error != nil {
		return ServerStatus{Name: name, Error: resp.Error.Message}
	}
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return ServerStatus{Name: name, Error: err.Error()}
	}
	var tools []string
	for _, tool := range payload.Tools {
		tools = append(tools, tool.Name)
	}
	return ServerStatus{Name: name, Tools: tools}
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
