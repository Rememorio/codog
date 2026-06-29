package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCallToolAndReadResource(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1"},
	}
	call := CallTool(context.Background(), "test", server, "echo", json.RawMessage(`{"text":"hi"}`))
	require.Empty(t, call.Error)
	require.Contains(t, string(call.Result), "hi")

	resources := ListResources(context.Background(), "test", server)
	require.Empty(t, resources.Error)
	require.Contains(t, string(resources.Resources), "codog://note")

	read := ReadResource(context.Background(), "test", server, "codog://note")
	require.Empty(t, read.Error)
	require.Contains(t, string(read.Result), "note body")
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_MCP_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		line := reader.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeMCP(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "test", "version": "0.0.0"},
			})
		case "tools/list":
			writeMCP(id, map[string]any{"tools": []map[string]any{{"name": "echo"}}})
		case "tools/call":
			writeMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
		case "resources/list":
			writeMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/read":
			writeMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		}
	}
	os.Exit(0)
}

func writeMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}
