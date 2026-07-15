package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	require.Equal(t, "ready", call.Lifecycle.Phase)
	require.Nil(t, call.Lifecycle.Error)
	require.Contains(t, string(call.Result), "hi")

	resources := ListResources(context.Background(), "test", server)
	require.Empty(t, resources.Error)
	require.Equal(t, "ready", resources.Lifecycle.Phase)
	require.Contains(t, string(resources.Resources), "codog://note")

	templates := ListResourceTemplates(context.Background(), "test", server)
	require.Empty(t, templates.Error)
	require.Equal(t, "ready", templates.Lifecycle.Phase)
	require.Contains(t, string(templates.Templates), "codog://notes/{name}")

	read := ReadResource(context.Background(), "test", server, "codog://note")
	require.Empty(t, read.Error)
	require.Equal(t, "ready", read.Lifecycle.Phase)
	require.Contains(t, string(read.Result), "note body")

	prompts := ListPrompts(context.Background(), "test", server)
	require.Empty(t, prompts.Error)
	require.Equal(t, "ready", prompts.Lifecycle.Phase)
	require.Contains(t, string(prompts.Prompts), "review")

	prompt := GetPrompt(context.Background(), "test", server, "review", json.RawMessage(`{"topic":"hooks"}`))
	require.Empty(t, prompt.Error)
	require.Equal(t, "ready", prompt.Lifecycle.Phase)
	require.Contains(t, string(prompt.Result), "Review hooks")

	auth := InspectAuth(context.Background(), "test", server)
	require.Equal(t, "ok", auth.Status)
	require.Equal(t, "ready", auth.Lifecycle.Phase)
	require.Equal(t, "ready", auth.Lifecycle.LastSuccessfulPhase)
	require.Nil(t, auth.Lifecycle.Error)
	require.Contains(t, string(auth.ServerInfo), `"name":"test"`)
	require.Equal(t, 1, auth.ToolCount)
	require.Equal(t, 1, auth.ResourceCount)
}

func TestCallToolHandlesServerRequestsAndNotifications(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1", "CODOG_MCP_SERVER_REQUESTS=1"},
	}
	root, err := RootForPath(t.TempDir())
	require.NoError(t, err)
	var elicitation ElicitationRequest
	var notifications []Notification
	call := CallToolWithOptions(context.Background(), "test", server, "echo", json.RawMessage(`{"text":"hi"}`), ClientOptions{
		Roots: []Root{root},
		Elicit: func(_ context.Context, request ElicitationRequest) (ElicitationResult, error) {
			elicitation = request
			return ElicitationResult{Action: "accept", Content: map[string]any{"choice": "yes"}}, nil
		},
		OnNotification: func(notification Notification) {
			notifications = append(notifications, notification)
		},
	})
	require.Empty(t, call.Error)
	require.Equal(t, "Choose a value", elicitation.Message)
	require.Equal(t, "form", elicitation.Mode)
	require.Len(t, notifications, 1)
	require.Equal(t, "notifications/tools/list_changed", notifications[0].Method)
	require.Contains(t, string(call.Result), root.URI)
	require.Contains(t, string(call.Result), `"choice":"yes"`)
}

func TestCallToolDeclinesElicitationWithoutHandler(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1", "CODOG_MCP_SERVER_REQUESTS=1"},
	}
	call := CallTool(context.Background(), "test", server, "echo", json.RawMessage(`{}`))
	require.Empty(t, call.Error)
	require.Contains(t, string(call.Result), `"action":"decline"`)
}

func TestServerRequestResponsesCoverProtocolOutcomes(t *testing.T) {
	id := json.RawMessage(`"request-1"`)
	ping := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "ping"}, ClientOptions{})
	require.Nil(t, ping.Error)
	require.Equal(t, map[string]any{}, ping.Result)

	roots := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "roots/list"}, ClientOptions{})
	require.Nil(t, roots.Error)
	require.Equal(t, map[string]any{"roots": []Root{}}, roots.Result)

	invalidParams := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "elicitation/create", Params: json.RawMessage(`{`)}, ClientOptions{})
	require.Equal(t, -32602, invalidParams.Error.Code)

	handlerFailure := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "elicitation/create", Params: json.RawMessage(`{"message":"choose"}`)}, ClientOptions{
		Elicit: func(context.Context, ElicitationRequest) (ElicitationResult, error) {
			return ElicitationResult{}, errors.New("interaction failed")
		},
	})
	require.Equal(t, -32603, handlerFailure.Error.Code)
	require.Contains(t, handlerFailure.Error.Message, "interaction failed")

	invalidAction := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "elicitation/create", Params: json.RawMessage(`{"message":"choose"}`)}, ClientOptions{
		Elicit: func(context.Context, ElicitationRequest) (ElicitationResult, error) {
			return ElicitationResult{Action: "later"}, nil
		},
	})
	require.Equal(t, -32603, invalidAction.Error.Code)

	unsupported := serverRequestResponse(context.Background(), rpcEnvelope{ID: id, Method: "sampling/createMessage"}, ClientOptions{})
	require.Equal(t, -32601, unsupported.Error.Code)
	require.False(t, validElicitationAction("later"))
	require.True(t, validElicitationAction("cancel"))
	require.False(t, rpcIDMatches(json.RawMessage(`"1"`), 1))
}

func TestHTTPSSEParserHandlesFragmentsAndFailures(t *testing.T) {
	response, err := readHTTPSSE(context.Background(), strings.NewReader("data: {\"jsonrpc\":\"2.0\",\n"+
		"data: \"id\":3,\"result\":{\"ok\":true}}\n\n"), config.MCPServerConfig{}, "", 3, ClientOptions{})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(response.Result))
	response, err = readHTTPSSE(context.Background(), strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{}}"), config.MCPServerConfig{}, "", 3, ClientOptions{})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(response.Result))

	_, err = readHTTPSSE(context.Background(), strings.NewReader("data: [DONE]\n\n"), config.MCPServerConfig{}, "", 3, ClientOptions{})
	require.ErrorContains(t, err, "ended before")
	_, _, err = processHTTPSSEData(context.Background(), config.MCPServerConfig{}, "", 3, "{", ClientOptions{})
	require.Error(t, err)
	_, done, err := processHTTPSSEData(context.Background(), config.MCPServerConfig{}, "", 3, `{"jsonrpc":"2.0","id":2,"result":{}}`, ClientOptions{})
	require.NoError(t, err)
	require.False(t, done)
	_, done, err = processHTTPSSEData(context.Background(), config.MCPServerConfig{}, "", 3, "", ClientOptions{})
	require.NoError(t, err)
	require.False(t, done)
}

func TestSendHTTPServerResponseRejectsBadEndpointAndStatus(t *testing.T) {
	err := sendHTTPServerResponse(context.Background(), config.MCPServerConfig{URL: "file:///tmp/mcp"}, "", rpcServerResponse{})
	require.ErrorContains(t, err, "must use http or https")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	err = sendHTTPServerResponse(context.Background(), config.MCPServerConfig{URL: server.URL}, "session", rpcServerResponse{JSONRPC: "2.0", ID: json.RawMessage(`1`)})
	require.ErrorContains(t, err, "status 403")
}

func TestHTTPMCPTransportListsCallsAndReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "Bearer dynamic", r.Header.Get("Authorization"))
		require.Equal(t, "helper-token", r.Header.Get("X-Helper"))
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-1")
			writeHTTPMCP(t, w, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "remote", "version": "1.0.0"},
			})
		case "notifications/initialized":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			w.Header().Set("Content-Type", "text/event-stream")
			response := map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text over HTTP.",
				"inputSchema": map[string]any{"type": "object"},
			}}}}
			data, _ := json.Marshal(response)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		case "tools/call":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeHTTPMCP(t, w, id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi remote"}}})
		case "resources/read":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeHTTPMCP(t, w, id, map[string]any{"contents": []map[string]any{{"uri": "codog://remote", "text": "remote note"}}})
		default:
			writeHTTPMCPError(t, w, id, "unsupported method")
		}
	}))
	defer server.Close()

	xaa := true
	cfg := config.MCPServerConfig{
		URL:           server.URL + "/mcp?token=secret",
		Headers:       map[string]string{"Authorization": "Bearer static"},
		HeadersHelper: headersHelperCommand(),
		OAuth: &config.MCPServerOAuthConfig{
			ClientID:              "mcp-client",
			CallbackPort:          7777,
			AuthServerMetadataURL: "https://issuer.test/.well-known/oauth-authorization-server",
			XAA:                   &xaa,
		},
	}
	ready := Preflight(context.Background(), "remote", cfg)
	require.Equal(t, "ok", ready.Status)
	require.Equal(t, "ready", ready.Lifecycle.Phase)
	require.Nil(t, ready.Lifecycle.Error)
	require.Contains(t, ready.URL, "token=%5Bredacted%5D")
	require.NotContains(t, ready.URL, "secret")
	require.Equal(t, "2024-11-05", ready.ProtocolVersion)
	require.Contains(t, string(ready.ServerInfo), `"name":"remote"`)

	tools := ListTools(context.Background(), "remote", cfg)
	require.Empty(t, tools.Error)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "echo", tools.Tools[0].Name)
	require.Equal(t, "Echo text over HTTP.", tools.Tools[0].Description)

	call := CallTool(context.Background(), "remote", cfg, "echo", json.RawMessage(`{"text":"hi"}`))
	require.Empty(t, call.Error)
	require.Equal(t, "ready", call.Lifecycle.Phase)
	require.Contains(t, string(call.Result), "hi remote")

	read := ReadResource(context.Background(), "remote", cfg, "codog://remote")
	require.Empty(t, read.Error)
	require.Equal(t, "ready", read.Lifecycle.Phase)
	require.Contains(t, string(read.Result), "remote note")

	description := DescribeServer("remote", cfg)
	require.True(t, description.Valid)
	require.Equal(t, "http", description.Transport.ID)
	require.Equal(t, []string{"Authorization"}, description.Details.HeaderKeys)
	require.True(t, description.Details.HeadersHelperConfigured)
	require.True(t, description.Details.OAuthConfigured)
	require.True(t, description.Details.OAuthClientIDConfigured)
	require.Equal(t, 7777, description.Details.OAuthCallbackPort)
	require.Equal(t, "https://issuer.test/.well-known/oauth-authorization-server", description.Details.OAuthAuthServerMetadataURL)
	require.NotNil(t, description.Details.OAuthXAA)
	require.True(t, *description.Details.OAuthXAA)
	require.Contains(t, description.Details.URL, "token=%5Bredacted%5D")
	require.NotContains(t, description.Details.URL, "secret")
	require.Contains(t, ServerSignature(cfg), "token=%5Bredacted%5D")
	require.NotContains(t, ServerSignature(cfg), "secret")
	require.NotContains(t, ServerConfigHash(cfg), "secret")
	require.NotContains(t, ServerConfigHash(cfg), "dynamic")
}

func TestHTTPMCPHandlesServerRequestsInSSEStream(t *testing.T) {
	serverResponses := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&message))
		method, _ := message["method"].(string)
		if method == "" {
			serverResponses <- message
			w.WriteHeader(http.StatusAccepted)
			return
		}
		id := message["id"]
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-stream")
			writeHTTPMCP(t, w, id, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stream"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)
			writeHTTPSSEEvent(t, w, map[string]any{"jsonrpc": "2.0", "id": 101, "method": "roots/list"})
			flusher.Flush()
			rootsResponse := <-serverResponses
			writeHTTPSSEEvent(t, w, map[string]any{
				"jsonrpc": "2.0", "id": "form-1", "method": "elicitation/create",
				"params": map[string]any{"mode": "form", "message": "Choose", "requestedSchema": map[string]any{
					"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
				}},
			})
			flusher.Flush()
			elicitationResponse := <-serverResponses
			writeHTTPSSEEvent(t, w, map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/list_changed"})
			writeHTTPSSEEvent(t, w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"roots": rootsResponse["result"], "elicitation": elicitationResponse["result"],
			}})
			flusher.Flush()
		default:
			writeHTTPMCPError(t, w, id, "unsupported method")
		}
	}))
	defer server.Close()

	root, err := RootForPath(t.TempDir())
	require.NoError(t, err)
	var notifications []Notification
	result := CallToolWithOptions(context.Background(), "stream", config.MCPServerConfig{URL: server.URL}, "echo", nil, ClientOptions{
		Roots: []Root{root},
		Elicit: func(context.Context, ElicitationRequest) (ElicitationResult, error) {
			return ElicitationResult{Action: "accept", Content: map[string]any{"value": "ok"}}, nil
		},
		OnNotification: func(notification Notification) { notifications = append(notifications, notification) },
	})
	require.Empty(t, result.Error)
	require.Contains(t, string(result.Result), root.URI)
	require.Contains(t, string(result.Result), `"value":"ok"`)
	require.Len(t, notifications, 1)
	require.Equal(t, "notifications/resources/list_changed", notifications[0].Method)
}

func TestMCPRuntimeExpandsEnvironmentAndHomeSyntax(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODOG_MCP_COMMAND", os.Args[0])
	t.Setenv("CODOG_MCP_ARG", "-test.run=TestMCPHelperProcess")
	t.Setenv("CODOG_MCP_HELPER_VALUE", "1")

	resolved := resolveMCPServerConfig(config.MCPServerConfig{
		Command:       "~/bin/server",
		Args:          []string{"--config=~/mcp.json"},
		Env:           []string{"CONFIG=~/mcp.json"},
		HeadersHelper: "~/bin/helper",
	})
	require.Equal(t, filepath.Join(home, "bin", "server"), resolved.Command)
	require.Equal(t, []string{"--config=" + filepath.Join(home, "mcp.json")}, resolved.Args)
	require.Equal(t, []string{"CONFIG=" + filepath.Join(home, "mcp.json")}, resolved.Env)
	require.Equal(t, filepath.Join(home, "bin", "helper"), resolved.HeadersHelper)

	cfg := config.MCPServerConfig{
		Command: "${CODOG_MCP_COMMAND}",
		Args:    []string{"${CODOG_MCP_ARG}"},
		Env:     []string{"CODOG_MCP_HELPER=${CODOG_MCP_HELPER_VALUE}"},
	}
	status := Preflight(context.Background(), "expanded", cfg)
	require.Equal(t, "ok", status.Status)
	require.Equal(t, os.Args[0], status.Command)
	require.NotEmpty(t, status.ResolvedPath)

	tools := ListTools(context.Background(), "expanded", cfg)
	require.Empty(t, tools.Error)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "echo", tools.Tools[0].Name)
}

func TestHTTPMCPRuntimeExpandsURLAndHeaderValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer expanded-token", r.Header.Get("Authorization"))
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		id := req["id"]
		switch req["method"] {
		case "initialize":
			writeHTTPMCP(t, w, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "expanded-http", "version": "1.0.0"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeHTTPMCP(t, w, id, map[string]any{"tools": []map[string]any{{"name": "remote_echo"}}})
		default:
			writeHTTPMCPError(t, w, id, "unsupported method")
		}
	}))
	defer server.Close()

	t.Setenv("CODOG_MCP_BASE_URL", server.URL)
	t.Setenv("CODOG_MCP_SECRET_TOKEN", "secret-token")
	t.Setenv("CODOG_MCP_AUTH_TOKEN", "expanded-token")

	cfg := config.MCPServerConfig{
		URL:     "${CODOG_MCP_BASE_URL}/mcp?token=${CODOG_MCP_SECRET_TOKEN}",
		Headers: map[string]string{"Authorization": "Bearer ${CODOG_MCP_AUTH_TOKEN}"},
	}
	status := Preflight(context.Background(), "expanded-http", cfg)
	require.Equal(t, "ok", status.Status)
	require.Contains(t, status.URL, "token=%5Bredacted%5D")
	require.NotContains(t, status.URL, "secret-token")

	tools := ListTools(context.Background(), "expanded-http", cfg)
	require.Empty(t, tools.Error)
	require.Len(t, tools.Tools, 1)
	require.Equal(t, "remote_echo", tools.Tools[0].Name)

	description := DescribeServer("expanded-http", cfg)
	require.Contains(t, description.Details.URL, "token=%5Bredacted%5D")
	data, err := json.Marshal(description)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-token")
	require.NotContains(t, string(data), "expanded-token")
}

func TestParseHeadersHelperOutput(t *testing.T) {
	fromJSON, err := parseHeadersHelperOutput([]byte(`{"Authorization":"Bearer token","X-Trace":"trace"}`))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Authorization": "Bearer token", "X-Trace": "trace"}, fromJSON)

	fromLines, err := parseHeadersHelperOutput([]byte("Authorization: Bearer token\nX-Trace=trace\n"))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Authorization": "Bearer token", "X-Trace": "trace"}, fromLines)

	_, err = parseHeadersHelperOutput([]byte("not-a-header"))
	require.ErrorContains(t, err, "KEY=VALUE")
}

func TestListToolsIncludesProcessStderr(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_FAIL_STDERR=1"},
	}

	tools := ListTools(context.Background(), "test", server)
	require.Contains(t, tools.Error, "mcp boot failed")

	inspected := Inspect(context.Background(), "test", server)
	require.Equal(t, "error", inspected.Status)
	require.Equal(t, "error_surfacing", inspected.Lifecycle.Phase)
	require.Equal(t, "initialize_handshake", inspected.Lifecycle.Error.Phase)
	require.True(t, inspected.Lifecycle.Error.Recoverable)
}

func TestInspectClassifiesToolDiscoveryFailures(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1", "CODOG_MCP_FAIL_TOOLS=1"},
	}

	inspected := Inspect(context.Background(), "test", server)
	require.Equal(t, "error", inspected.Status)
	require.Equal(t, "error_surfacing", inspected.Lifecycle.Phase)
	require.Equal(t, "tool_discovery", inspected.Lifecycle.Error.Phase)
	require.True(t, inspected.Lifecycle.Error.Recoverable)
	require.Contains(t, inspected.Error, "tool discovery failed")
}

func TestCallToolClassifiesInvocationFailures(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1", "CODOG_MCP_FAIL_CALL=1"},
	}

	call := CallTool(context.Background(), "test", server, "echo", json.RawMessage(`{"text":"hi"}`))
	require.Equal(t, "test", call.Server)
	require.Equal(t, "echo", call.Tool)
	require.Contains(t, call.Error, "tool call failed")
	require.Equal(t, "error_surfacing", call.Lifecycle.Phase)
	require.Equal(t, "invocation", call.Lifecycle.Error.Phase)
	require.True(t, call.Lifecycle.Error.Recoverable)
	require.Equal(t, "echo", call.Lifecycle.Error.Context["tool"])
}

func TestResourceAndPromptCallsClassifyLifecycleFailures(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1", "CODOG_MCP_FAIL_RESOURCES=1", "CODOG_MCP_FAIL_PROMPTS=1"},
	}

	resources := ListResources(context.Background(), "test", server)
	require.Contains(t, resources.Error, "resource discovery failed")
	require.Equal(t, "resource_discovery", resources.Lifecycle.Error.Phase)
	require.True(t, resources.Lifecycle.Error.Recoverable)

	templates := ListResourceTemplates(context.Background(), "test", server)
	require.Contains(t, templates.Error, "resource templates failed")
	require.Equal(t, "resource_discovery", templates.Lifecycle.Error.Phase)

	read := ReadResource(context.Background(), "test", server, "codog://note")
	require.Contains(t, read.Error, "resource read failed")
	require.Equal(t, "invocation", read.Lifecycle.Error.Phase)
	require.Equal(t, "codog://note", read.Lifecycle.Error.Context["uri"])

	prompts := ListPrompts(context.Background(), "test", server)
	require.Contains(t, prompts.Error, "prompt discovery failed")
	require.Equal(t, "resource_discovery", prompts.Lifecycle.Error.Phase)

	prompt := GetPrompt(context.Background(), "test", server, "review", json.RawMessage(`{}`))
	require.Contains(t, prompt.Error, "prompt render failed")
	require.Equal(t, "invocation", prompt.Lifecycle.Error.Phase)
	require.Equal(t, "review", prompt.Lifecycle.Error.Context["prompt"])
}

func TestPreflightReportsReadinessAndMissingCommand(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     []string{"CODOG_MCP_HELPER=1"},
	}
	ready := Preflight(context.Background(), "test", server)
	require.Equal(t, "ok", ready.Status)
	require.Equal(t, "ready", ready.Lifecycle.Phase)
	require.Equal(t, "ready", ready.Lifecycle.LastSuccessfulPhase)
	require.Nil(t, ready.Lifecycle.Error)
	require.NotEmpty(t, ready.ResolvedPath)
	require.Equal(t, "2024-11-05", ready.ProtocolVersion)
	require.Contains(t, string(ready.ServerInfo), `"name":"test"`)

	missing := Preflight(context.Background(), "missing", config.MCPServerConfig{Command: filepath.Join(t.TempDir(), "missing-mcp")})
	require.Equal(t, "command_not_found", missing.Status)
	require.Equal(t, "error_surfacing", missing.Lifecycle.Phase)
	require.Equal(t, "spawn_connect", missing.Lifecycle.Error.Phase)
	require.True(t, missing.Lifecycle.Error.Recoverable)
	require.Contains(t, missing.Error, "missing-mcp")

	statuses := InspectAll(context.Background(), map[string]config.MCPServerConfig{
		"test":    server,
		"missing": {Command: filepath.Join(t.TempDir(), "missing-mcp")},
	})
	require.Equal(t, []string{"missing", "test"}, []string{statuses[0].Name, statuses[1].Name})
	require.Equal(t, "command_not_found", statuses[0].Status)
	require.Equal(t, "spawn_connect", statuses[0].Lifecycle.Error.Phase)
	require.Equal(t, "ok", statuses[1].Status)
	require.Equal(t, "ready", statuses[1].Lifecycle.Phase)
	require.Equal(t, 1, statuses[1].ToolCount)
	require.Equal(t, []string{"echo"}, statuses[1].Tools)
}

func TestBuildStartupReportClassifiesRequiredFailures(t *testing.T) {
	statuses := []ServerStatus{
		{
			Name:     "optional-missing",
			Status:   "command_not_found",
			Required: false,
			Error:    "optional command not found",
			Lifecycle: lifecycleFailure(
				"spawn_connect",
				"optional command not found",
				true,
				map[string]string{"server": "optional-missing"},
			),
		},
		{
			Name:     "required-missing",
			Status:   "command_not_found",
			Required: true,
			Error:    "required command not found",
			Lifecycle: lifecycleFailure(
				"spawn_connect",
				"required command not found",
				true,
				map[string]string{"server": "required-missing"},
			),
		},
		{Name: "required-ready", Status: "ok", Required: true, Lifecycle: lifecycleReady("ready")},
	}

	report := BuildStartupReport(statuses)

	require.Equal(t, "fatal", report.Status)
	require.Equal(t, 3, report.Total)
	require.Equal(t, 1, report.OKCount)
	require.Equal(t, 2, report.ErrorCount)
	require.Equal(t, 2, report.RequiredCount)
	require.Equal(t, 1, report.RequiredOKCount)
	require.Equal(t, 1, report.RequiredFailedCount)
	require.Equal(t, 1, report.OptionalFailedCount)
	require.Len(t, report.FailedRequired, 1)
	require.Equal(t, "required-missing", report.FailedRequired[0].Name)
	require.Equal(t, "spawn_connect", report.FailedRequired[0].Phase)
	require.True(t, report.FailedRequired[0].Recoverable)
	require.Len(t, report.FailedOptional, 1)
	require.Equal(t, "optional-missing", report.FailedOptional[0].Name)
}

func TestBuildStartupReportAllowsOptionalFailuresToDegrade(t *testing.T) {
	report := BuildStartupReport([]ServerStatus{
		{Name: "optional-missing", Status: "command_not_found"},
	})

	require.Equal(t, "degraded", report.Status)
	require.Equal(t, 1, report.OptionalFailedCount)
	require.Empty(t, report.FailedRequired)
}

func TestToolNameNormalizationMatchesMCPCompatibility(t *testing.T) {
	require.Equal(t, "github_com", NormalizeNameForTooling("github.com"))
	require.Equal(t, "tool_name_", NormalizeNameForTooling("tool name!"))
	require.Equal(t, "claude_ai_Example_Server", NormalizeNameForTooling("claude.ai Example   Server!!"))
	require.Equal(t, "mcp__claude_ai_Example_Server__weather_tool", ToolName("claude.ai Example Server", "weather tool"))
}

func TestUnwrapCCRProxyURLMatchesCompatibility(t *testing.T) {
	wrapped := "https://api.anthropic.com/v2/session_ingress/shttp/mcp/123?mcp_url=https%3A%2F%2Fvendor.example%2Fmcp&other=1"
	require.Equal(t, "https://vendor.example/mcp", UnwrapCCRProxyURL(wrapped))
	require.Equal(t, "url:https://vendor.example/mcp", URLServerSignature(wrapped))

	wrappedWS := "https://api.anthropic.com/v2/ccr-sessions/1?mcp_url=wss%3A%2F%2Fvendor.example%2Fmcp"
	require.Equal(t, "wss://vendor.example/mcp", UnwrapCCRProxyURL(wrappedWS))
	require.Equal(t, "https://vendor.example/mcp", UnwrapCCRProxyURL("https://vendor.example/mcp"))
	require.Equal(t, "https://api.anthropic.com/v2/ccr-sessions/1", UnwrapCCRProxyURL("https://api.anthropic.com/v2/ccr-sessions/1"))
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

func TestServerSignatureRedactsSensitiveArgs(t *testing.T) {
	server := config.MCPServerConfig{
		Command: "uvx",
		Args: []string{
			"mcp-server",
			"--api-key",
			"sk-secret-token",
			"--tenant=public",
			"--access-token=access-secret",
			"https://user:pass@example.test/mcp?token=query-secret",
		},
	}

	signature := ServerSignature(server)
	require.Contains(t, signature, "mcp-server")
	require.Contains(t, signature, "--tenant=public")
	require.Contains(t, signature, "--api-key|[redacted:")
	require.Contains(t, signature, "--access-token=[redacted:")
	require.Contains(t, signature, "https://%5Bredacted%5D@example.test/mcp?token=%5Bredacted%5D")
	require.NotContains(t, signature, "sk-secret-token")
	require.NotContains(t, signature, "access-secret")
	require.NotContains(t, signature, "pass")
	require.NotContains(t, signature, "query-secret")
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
	changedTimeout := config.MCPServerConfig{
		Command:           "uvx",
		Args:              []string{"mcp-server"},
		Env:               []string{"TOKEN=secret", "MODE=stdio"},
		ToolCallTimeoutMS: 15000,
	}
	required := config.MCPServerConfig{
		Command:  "uvx",
		Args:     []string{"mcp-server"},
		Env:      []string{"TOKEN=secret", "MODE=stdio"},
		Required: true,
	}

	hash := ServerConfigHash(base)
	require.Len(t, hash, 16)
	require.Equal(t, hash, ServerConfigHash(reorderedEnv))
	require.NotEqual(t, hash, ServerConfigHash(changedEnv))
	require.NotEqual(t, hash, ServerConfigHash(changedArgs))
	require.NotEqual(t, hash, ServerConfigHash(changedTimeout))
	require.NotEqual(t, hash, ServerConfigHash(required))
	require.NotContains(t, hash, "secret")
}

func TestServerConfigHashTracksRemoteOAuthConfig(t *testing.T) {
	xaa := true
	base := config.MCPServerConfig{
		URL:           "https://example.test/mcp",
		Headers:       map[string]string{"Authorization": "Bearer secret"},
		HeadersHelper: "helper.sh",
		OAuth: &config.MCPServerOAuthConfig{
			ClientID:              "mcp-client",
			CallbackPort:          7777,
			AuthServerMetadataURL: "https://issuer.test/.well-known/oauth-authorization-server",
			XAA:                   &xaa,
		},
	}
	withoutOAuth := base
	withoutOAuth.OAuth = nil
	changedOAuth := base
	changedOAuth.OAuth = &config.MCPServerOAuthConfig{
		ClientID:              "other-client",
		CallbackPort:          7777,
		AuthServerMetadataURL: "https://issuer.test/.well-known/oauth-authorization-server",
		XAA:                   &xaa,
	}

	hash := ServerConfigHash(base)
	require.Len(t, hash, 16)
	require.NotEqual(t, hash, ServerConfigHash(withoutOAuth))
	require.NotEqual(t, hash, ServerConfigHash(changedOAuth))
	require.NotContains(t, ServerSignature(base), "secret")
}

func TestDescribeServerRedactsSensitiveConfigValues(t *testing.T) {
	server := config.MCPServerConfig{
		Command:           "uvx",
		Args:              []string{"mcp-server", "--token=secret", "--api-key", "sk-secret"},
		Env:               []string{"TOKEN=secret", "MODE=stdio"},
		ToolCallTimeoutMS: 15000,
	}
	description := DescribeServer("alpha", server)
	require.Equal(t, "alpha", description.Name)
	require.True(t, description.Valid)
	require.False(t, description.Required)
	require.Equal(t, "stdio", description.Transport.ID)
	require.Equal(t, "uvx (4 args)", description.Summary)
	require.Equal(t, "uvx", description.Details.Command)
	require.Equal(t, 4, description.Details.ArgsCount)
	require.Equal(t, []string{"mcp-server", "--token=[redacted:6-char-token]", "--api-key", "[redacted:9-char-token]"}, description.Details.ArgsSummary)
	require.Equal(t, 15000, description.Details.ToolCallTimeoutMS)
	require.Equal(t, []string{"MODE", "TOKEN"}, description.Details.EnvKeys)
	data, err := json.Marshal(description)
	require.NoError(t, err)
	require.NotContains(t, string(data), "TOKEN=secret")
	require.NotContains(t, string(data), "--token=secret")
	require.NotContains(t, string(data), "sk-secret")
}

func TestStdioRequestTimeoutUsesConfiguredMCPTimeout(t *testing.T) {
	require.Equal(t, 30*time.Second, stdioRequestTimeout(config.MCPServerConfig{}))
	require.Equal(t, 1500*time.Millisecond, stdioRequestTimeout(config.MCPServerConfig{ToolCallTimeoutMS: 1500}))
}

func TestDescribeAndPreflightReportRequiredMCPServer(t *testing.T) {
	server := config.MCPServerConfig{
		Command:  filepath.Join(t.TempDir(), "missing-mcp"),
		Required: true,
	}

	description := DescribeServer("critical", server)
	require.True(t, description.Required)

	status := Preflight(context.Background(), "critical", server)
	require.True(t, status.Required)
	require.Equal(t, "command_not_found", status.Status)
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
			if os.Getenv("CODOG_MCP_FAIL_TOOLS") == "1" {
				writeMCPError(id, "tool discovery failed")
				continue
			}
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
			handleMCPHelperToolCall(reader, id)
		case "resources/list":
			if os.Getenv("CODOG_MCP_FAIL_RESOURCES") == "1" {
				writeMCPError(id, "resource discovery failed")
				continue
			}
			writeMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			if os.Getenv("CODOG_MCP_FAIL_RESOURCES") == "1" {
				writeMCPError(id, "resource templates failed")
				continue
			}
			writeMCP(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			if os.Getenv("CODOG_MCP_FAIL_RESOURCES") == "1" {
				writeMCPError(id, "resource read failed")
				continue
			}
			writeMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			if os.Getenv("CODOG_MCP_FAIL_PROMPTS") == "1" {
				writeMCPError(id, "prompt discovery failed")
				continue
			}
			writeMCP(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
				"arguments": []map[string]any{{
					"name":     "topic",
					"required": true,
				}},
			}}})
		case "prompts/get":
			if os.Getenv("CODOG_MCP_FAIL_PROMPTS") == "1" {
				writeMCPError(id, "prompt render failed")
				continue
			}
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

func handleMCPHelperToolCall(reader *bufio.Scanner, id any) {
	if os.Getenv("CODOG_MCP_FAIL_CALL") == "1" {
		writeMCPError(id, "tool call failed")
		return
	}
	if os.Getenv("CODOG_MCP_SERVER_REQUESTS") != "1" {
		writeMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
		return
	}
	writeMCPRequest(101, "roots/list", nil)
	rootsResponse := readMCPHelperMessage(reader)
	writeMCPRequest("elicitation-1", "elicitation/create", map[string]any{
		"mode": "form", "message": "Choose a value",
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"type": "string"}}},
	})
	elicitationResponse := readMCPHelperMessage(reader)
	writeMCPNotification("notifications/tools/list_changed", map[string]any{"reason": "test"})
	writeMCP(id, map[string]any{
		"content":     []map[string]any{{"type": "text", "text": "hi"}},
		"roots":       rootsResponse["result"],
		"elicitation": elicitationResponse["result"],
	})
}

func TestMCPHeadersHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_MCP_HEADERS_HELPER") != "1" {
		return
	}
	fmt.Println(`{"Authorization":"Bearer dynamic","X-Helper":"helper-token"}`)
	os.Exit(0)
}

func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func headersHelperCommand() string {
	command := shellQuote(os.Args[0]) + " -test.run=TestMCPHeadersHelperProcess"
	if runtime.GOOS == "windows" {
		return "set CODOG_MCP_HEADERS_HELPER=1&& " + command
	}
	return "CODOG_MCP_HEADERS_HELPER=1 " + command
}

func writeMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func writeMCPError(id any, message string) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]string{"message": message}}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func writeMCPRequest(id any, method string, params map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func writeMCPNotification(method string, params map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func readMCPHelperMessage(reader *bufio.Scanner) map[string]any {
	if !reader.Scan() {
		return nil
	}
	var payload map[string]any
	_ = json.Unmarshal(reader.Bytes(), &payload)
	return payload
}

func writeHTTPMCP(t *testing.T, w http.ResponseWriter, id any, result map[string]any) {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
}

func writeHTTPMCPError(t *testing.T, w http.ResponseWriter, id any, message string) {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]string{"message": message}}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
}

func writeHTTPSSEEvent(t *testing.T, w http.ResponseWriter, payload map[string]any) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	require.NoError(t, err)
}
