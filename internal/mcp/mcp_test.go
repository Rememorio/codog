package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	tools := ListTools(context.Background(), "test", server)
	require.Empty(t, tools.Error)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "echo", tools.Tools[0].Name)
	require.Equal(t, "Echo text.", tools.Tools[0].Description)
	require.Equal(t, "object", tools.Tools[0].InputSchema["type"])

	call := CallTool(context.Background(), "test", server, "echo", json.RawMessage(`{"text":"hi"}`))
	require.Empty(t, call.Error)
	require.Contains(t, string(call.Result), "hi")

	resources := ListResources(context.Background(), "test", server)
	require.Empty(t, resources.Error)
	require.Contains(t, string(resources.Resources), "codog://note")

	templates := ListResourceTemplates(context.Background(), "test", server)
	require.Empty(t, templates.Error)
	require.Contains(t, string(templates.Templates), "codog://notes/{name}")

	read := ReadResource(context.Background(), "test", server, "codog://note")
	require.Empty(t, read.Error)
	require.Contains(t, string(read.Result), "note body")

	prompts := ListPrompts(context.Background(), "test", server)
	require.Empty(t, prompts.Error)
	require.Contains(t, string(prompts.Prompts), "review")

	prompt := GetPrompt(context.Background(), "test", server, "review", json.RawMessage(`{"topic":"hooks"}`))
	require.Empty(t, prompt.Error)
	require.Contains(t, string(prompt.Result), "Review hooks")

	auth := InspectAuth(context.Background(), "test", server)
	require.Equal(t, "ok", auth.Status)
	require.Contains(t, string(auth.ServerInfo), `"name":"test"`)
	require.Equal(t, 1, auth.ToolCount)
	require.Equal(t, 1, auth.ResourceCount)
}

func TestListToolsIncludesProcessStderr(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_FAIL_STDERR=1"},
	}

	tools := ListTools(context.Background(), "test", server)
	require.Contains(t, tools.Error, "mcp boot failed")
}

func TestPreflightReportsReadinessAndMissingCommand(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1"},
	}
	ready := Preflight(context.Background(), "test", server)
	require.Equal(t, "ok", ready.Status)
	require.NotEmpty(t, ready.ResolvedPath)
	require.Equal(t, "2024-11-05", ready.ProtocolVersion)
	require.Contains(t, string(ready.ServerInfo), `"name":"test"`)

	missing := Preflight(context.Background(), "missing", config.MCPServerConfig{Command: filepath.Join(t.TempDir(), "missing-mcp")})
	require.Equal(t, "command_not_found", missing.Status)
	require.Contains(t, missing.Error, "missing-mcp")

	statuses := InspectAll(context.Background(), map[string]config.MCPServerConfig{
		"test":    server,
		"missing": {Command: filepath.Join(t.TempDir(), "missing-mcp")},
	})
	require.Equal(t, []string{"missing", "test"}, []string{statuses[0].Name, statuses[1].Name})
	require.Equal(t, "command_not_found", statuses[0].Status)
	require.Equal(t, "ok", statuses[1].Status)
	require.Equal(t, 1, statuses[1].ToolCount)
	require.Equal(t, []string{"echo"}, statuses[1].Tools)
}

func TestToolNameNormalizationMatchesMCPCompatibility(t *testing.T) {
	require.Equal(t, "github_com", NormalizeNameForTooling("github.com"))
	require.Equal(t, "tool_name_", NormalizeNameForTooling("tool name!"))
	require.Equal(t, "claude_ai_Example_Server", NormalizeNameForTooling("claude.ai Example   Server!!"))
	require.Equal(t, "mcp__claude_ai_Example_Server__weather_tool", ToolName("claude.ai Example Server", "weather tool"))
}

func TestServerSignatureMatchesStdioCompatibility(t *testing.T) {
	server := config.MCPServerConfig{
		Command: `uv\x`,
		Args:    []string{"mcp|server", "--stdio"},
		Env:     []string{"TOKEN=secret"},
	}
	require.Equal(t, `stdio:[uv\\x|mcp\|server|--stdio]`, ServerSignature(server))
	require.NotContains(t, ServerSignature(server), "secret")
}

func TestServerConfigHashTracksContentWithoutLeakingEnv(t *testing.T) {
	base := config.MCPServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-server"},
		Env:     []string{"TOKEN=secret", "MODE=stdio"},
	}
	reorderedEnv := config.MCPServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-server"},
		Env:     []string{"MODE=stdio", "TOKEN=secret"},
	}
	changedEnv := config.MCPServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-server"},
		Env:     []string{"TOKEN=changed", "MODE=stdio"},
	}
	changedArgs := config.MCPServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-server", "--verbose"},
		Env:     []string{"TOKEN=secret", "MODE=stdio"},
	}

	hash := ServerConfigHash(base)
	require.Len(t, hash, 16)
	require.Equal(t, hash, ServerConfigHash(reorderedEnv))
	require.NotEqual(t, hash, ServerConfigHash(changedEnv))
	require.NotEqual(t, hash, ServerConfigHash(changedArgs))
	require.NotContains(t, hash, "secret")
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_MCP_FAIL_STDERR") == "1" {
		fmt.Fprintln(os.Stderr, "mcp boot failed")
		os.Exit(2)
	}
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
			writeMCP(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string"},
					},
				},
			}}})
		case "tools/call":
			writeMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
		case "resources/list":
			writeMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			writeMCP(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			writeMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			writeMCP(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
				"arguments": []map[string]any{{
					"name":     "topic",
					"required": true,
				}},
			}}})
		case "prompts/get":
			writeMCP(id, map[string]any{"messages": []map[string]any{{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": "Review hooks",
				},
			}}})
		}
	}
	os.Exit(0)
}

func writeMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}
