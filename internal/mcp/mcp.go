package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/Rememorio/codog/internal/config"
)

type ServerStatus struct {
	Name  string   `json:"name"`
	Tools []string `json:"tools,omitempty"`
	Error string   `json:"error,omitempty"`
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
