package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
)

const claudeAIServerPrefix = "claude.ai "

var ccrProxyPathMarkers = []string{"/v2/session_ingress/shttp/mcp/", "/v2/ccr-sessions/"}

type ServerStatus struct {
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Command         string          `json:"command,omitempty"`
	URL             string          `json:"url,omitempty"`
	Signature       string          `json:"signature,omitempty"`
	ConfigHash      string          `json:"config_hash,omitempty"`
	ResolvedPath    string          `json:"resolved_path,omitempty"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	ServerInfo      json.RawMessage `json:"server_info,omitempty"`
	ToolCount       int             `json:"tool_count,omitempty"`
	Tools           []string        `json:"tools,omitempty"`
	Error           string          `json:"error,omitempty"`
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

type ServerTransport struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type ServerDetails struct {
	Command    string   `json:"command,omitempty"`
	URL        string   `json:"url,omitempty"`
	ArgsCount  int      `json:"args_count"`
	EnvKeys    []string `json:"env_keys,omitempty"`
	HeaderKeys []string `json:"header_keys,omitempty"`
}

type ServerDescriptor struct {
	Name      string          `json:"name"`
	Valid     bool            `json:"valid"`
	Transport ServerTransport `json:"transport"`
	Summary   string          `json:"summary"`
	Details   ServerDetails   `json:"details"`
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id,omitempty"`
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

func NormalizeNameForTooling(name string) string {
	var builder strings.Builder
	for _, r := range name {
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
	normalized := builder.String()
	if strings.HasPrefix(name, claudeAIServerPrefix) {
		normalized = strings.Trim(collapseUnderscores(normalized), "_")
	}
	return normalized
}

func ToolPrefix(serverName string) string {
	return "mcp__" + NormalizeNameForTooling(serverName) + "__"
}

func ToolName(serverName, toolName string) string {
	return ToolPrefix(serverName) + NormalizeNameForTooling(toolName)
}

func UnwrapCCRProxyURL(rawURL string) string {
	for _, marker := range ccrProxyPathMarkers {
		if strings.Contains(rawURL, marker) {
			return unwrapCCRProxyURLWithMarker(rawURL)
		}
	}
	return rawURL
}

func URLServerSignature(rawURL string) string {
	return "url:" + UnwrapCCRProxyURL(rawURL)
}

func ServerSignature(server config.MCPServerConfig) string {
	if isHTTPServer(server) {
		return "url:" + redactedURL(server.URL)
	}
	parts := []string{server.Command}
	parts = append(parts, server.Args...)
	return "stdio:" + renderCommandSignature(parts)
}

func ServerConfigHash(server config.MCPServerConfig) string {
	if isHTTPServer(server) {
		rendered := fmt.Sprintf(
			"http|%s|%s|",
			UnwrapCCRProxyURL(server.URL),
			renderHeaderSignature(server.Headers),
		)
		return stableHexHash("required:false|" + rendered)
	}
	rendered := fmt.Sprintf(
		"stdio|%s|%s|%s|",
		server.Command,
		renderCommandSignature(server.Args),
		renderEnvSignature(server.Env),
	)
	return stableHexHash("required:false|" + rendered)
}

func DescribeServer(name string, server config.MCPServerConfig) ServerDescriptor {
	if isHTTPServer(server) {
		return ServerDescriptor{
			Name:      name,
			Valid:     strings.TrimSpace(server.URL) != "",
			Transport: ServerTransport{ID: "http", Label: "http"},
			Summary:   httpServerSummary(server),
			Details: ServerDetails{
				URL:        redactedURL(server.URL),
				HeaderKeys: headerKeys(server.Headers),
			},
		}
	}
	return ServerDescriptor{
		Name:      name,
		Valid:     strings.TrimSpace(server.Command) != "",
		Transport: ServerTransport{ID: "stdio", Label: "stdio"},
		Summary:   stdioServerSummary(server),
		Details: ServerDetails{
			Command:   server.Command,
			ArgsCount: len(server.Args),
			EnvKeys:   envKeys(server.Env),
		},
	}
}

func isHTTPServer(server config.MCPServerConfig) bool {
	return strings.TrimSpace(server.URL) != ""
}

func stdioServerSummary(server config.MCPServerConfig) string {
	if strings.TrimSpace(server.Command) == "" {
		return "missing command"
	}
	if len(server.Args) == 0 {
		return server.Command
	}
	return fmt.Sprintf("%s (%d args)", server.Command, len(server.Args))
}

func httpServerSummary(server config.MCPServerConfig) string {
	url := redactedURL(server.URL)
	if url == "" {
		return "missing url"
	}
	if len(server.Headers) == 0 {
		return url
	}
	return fmt.Sprintf("%s (%d header keys)", url, len(server.Headers))
}

func unwrapCCRProxyURLWithMarker(rawURL string) string {
	queryStart := strings.Index(rawURL, "?")
	if queryStart < 0 {
		return rawURL
	}
	query := rawURL[queryStart+1:]
	for _, pair := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key != "mcp_url" {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			return value
		}
		return decoded
	}
	return rawURL
}

func redactedURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(UnwrapCCRProxyURL(rawURL)))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	if parsed.User != nil {
		parsed.User = url.User("[redacted]")
	}
	if parsed.RawQuery != "" {
		values := parsed.Query()
		redacted := url.Values{}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			redacted.Set(key, "[redacted]")
		}
		parsed.RawQuery = redacted.Encode()
	}
	return parsed.String()
}

func renderCommandSignature(parts []string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ReplaceAll(part, `\`, `\\`)
		part = strings.ReplaceAll(part, "|", `\|`)
		escaped = append(escaped, part)
	}
	return "[" + strings.Join(escaped, "|") + "]"
}

func renderHeaderSignature(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	entries := make([]string, 0, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		entries = append(entries, key+"="+stableHexHash(value))
	}
	sort.Strings(entries)
	return strings.Join(entries, ";")
}

func renderEnvSignature(env []string) string {
	if len(env) == 0 {
		return ""
	}
	entries := append([]string(nil), env...)
	sort.Strings(entries)
	return strings.Join(entries, ";")
}

func headerKeys(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func envKeys(env []string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func stableHexHash(value string) string {
	hash := uint64(0xcbf29ce484222325)
	for _, b := range []byte(value) {
		hash ^= uint64(b)
		hash *= 0x100000001b3
	}
	return fmt.Sprintf("%016x", hash)
}

func collapseUnderscores(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if r == '_' {
			if !lastUnderscore {
				builder.WriteRune(r)
			}
			lastUnderscore = true
			continue
		}
		builder.WriteRune(r)
		lastUnderscore = false
	}
	return builder.String()
}

func InspectAll(ctx context.Context, servers map[string]config.MCPServerConfig) []ServerStatus {
	return inspectServers(ctx, servers, Inspect)
}

func PreflightAll(ctx context.Context, servers map[string]config.MCPServerConfig) []ServerStatus {
	return inspectServers(ctx, servers, Preflight)
}

func inspectServers(ctx context.Context, servers map[string]config.MCPServerConfig, inspect func(context.Context, string, config.MCPServerConfig) ServerStatus) []ServerStatus {
	statuses := make([]ServerStatus, 0, len(servers))
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		statuses = append(statuses, inspect(ctx, name, servers[name]))
	}
	return statuses
}

func Inspect(ctx context.Context, name string, server config.MCPServerConfig) ServerStatus {
	status := Preflight(ctx, name, server)
	if status.Error != "" {
		return status
	}
	result := ListTools(ctx, name, server)
	if result.Error != "" {
		status.Status = "error"
		status.Error = result.Error
		return status
	}
	tools := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		tools = append(tools, tool.Name)
	}
	sort.Strings(tools)
	status.ToolCount = len(tools)
	status.Tools = tools
	return status
}

func Preflight(ctx context.Context, name string, server config.MCPServerConfig) ServerStatus {
	status := ServerStatus{
		Name:       name,
		Command:    server.Command,
		URL:        redactedURL(server.URL),
		Signature:  ServerSignature(server),
		ConfigHash: ServerConfigHash(server),
	}
	if isHTTPServer(server) {
		initialized := Initialize(ctx, name, server)
		status.ProtocolVersion = initialized.ProtocolVersion
		status.ServerInfo = initialized.ServerInfo
		if initialized.Error != "" {
			status.Status = "error"
			status.Error = initialized.Error
			return status
		}
		status.Status = "ok"
		return status
	}
	if strings.TrimSpace(server.Command) == "" {
		status.Status = "missing_command"
		status.Error = "missing command"
		return status
	}
	resolved, err := exec.LookPath(server.Command)
	if err != nil {
		status.Status = "command_not_found"
		status.Error = err.Error()
		return status
	}
	status.ResolvedPath = resolved
	initialized := Initialize(ctx, name, server)
	status.ProtocolVersion = initialized.ProtocolVersion
	status.ServerInfo = initialized.ServerInfo
	if initialized.Error != "" {
		status.Status = "error"
		status.Error = initialized.Error
		return status
	}
	status.Status = "ok"
	return status
}

func Initialize(ctx context.Context, serverName string, server config.MCPServerConfig) InitializeResult {
	if isHTTPServer(server) {
		result, _ := initializeHTTP(ctx, serverName, server)
		return result
	}
	if server.Command == "" {
		return InitializeResult{Server: serverName, Status: "error", Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
		return InitializeResult{Server: serverName, Status: "error", Error: mcpError(err, &stderr).Error()}
	}
	resp, err := readResponse(reader)
	if err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: mcpError(err, &stderr).Error()}
	}
	if resp.Error != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: mcpError(errors.New(resp.Error.Message), &stderr).Error()}
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
	if isHTTPServer(server) {
		result, err := requestAfterInitialize(ctx, server, rpcRequest{
			JSONRPC: "2.0",
			ID:      2,
			Method:  "tools/list",
		})
		if err != nil {
			return ToolListResult{Server: serverName, Error: err.Error()}
		}
		return decodeToolListResult(serverName, result)
	}
	if server.Command == "" {
		return ToolListResult{Server: serverName, Error: "missing command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
		return ToolListResult{Server: serverName, Error: mcpError(err, &stderr).Error()}
	}
	if _, err := readResponse(reader); err != nil {
		return ToolListResult{Server: serverName, Error: mcpError(err, &stderr).Error()}
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	resp, err := readResponse(reader)
	if err != nil {
		return ToolListResult{Server: serverName, Error: mcpError(err, &stderr).Error()}
	}
	if resp.Error != nil {
		return ToolListResult{Server: serverName, Error: mcpError(errors.New(resp.Error.Message), &stderr).Error()}
	}
	return decodeToolListResult(serverName, resp.Result)
}

func decodeToolListResult(serverName string, result json.RawMessage) ToolListResult {
	var payload struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
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
	if isHTTPServer(server) {
		return requestAfterInitializeHTTP(ctx, server, req)
	}
	if server.Command == "" {
		return nil, fmt.Errorf("missing command")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	cmd.Env = append(os.Environ(), server.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
		return nil, mcpError(err, &stderr)
	}
	if _, err := readResponse(reader); err != nil {
		return nil, mcpError(err, &stderr)
	}
	_ = send(stdin, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	if err := send(stdin, req); err != nil {
		return nil, mcpError(err, &stderr)
	}
	resp, err := readResponse(reader)
	if err != nil {
		return nil, mcpError(err, &stderr)
	}
	if resp.Error != nil {
		return nil, mcpError(errors.New(resp.Error.Message), &stderr)
	}
	return resp.Result, nil
}

func initializeHTTP(ctx context.Context, serverName string, server config.MCPServerConfig) (InitializeResult, string) {
	if strings.TrimSpace(server.URL) == "" {
		return InitializeResult{Server: serverName, Status: "error", Error: "missing url"}, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, sessionID, err := sendHTTPRPC(ctx, server, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "codog", "version": "0.1.0"},
		},
	}, "")
	if err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}, sessionID
	}
	if resp.Error != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: resp.Error.Message}, sessionID
	}
	var payload struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      json.RawMessage `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return InitializeResult{Server: serverName, Status: "error", Error: err.Error()}, sessionID
	}
	return InitializeResult{
		Server:          serverName,
		Status:          "ok",
		ProtocolVersion: payload.ProtocolVersion,
		Capabilities:    payload.Capabilities,
		ServerInfo:      payload.ServerInfo,
	}, sessionID
}

func requestAfterInitializeHTTP(ctx context.Context, server config.MCPServerConfig, req rpcRequest) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	initialized, sessionID := initializeHTTP(ctx, "", server)
	if initialized.Error != "" {
		return nil, errors.New(initialized.Error)
	}
	_, nextSessionID, err := sendHTTPRPC(ctx, server, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}, sessionID)
	if err != nil && !errors.Is(err, errHTTPNotificationNoBody) {
		return nil, err
	}
	if nextSessionID != "" {
		sessionID = nextSessionID
	}
	resp, _, err := sendHTTPRPC(ctx, server, req, sessionID)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, errors.New(resp.Error.Message)
	}
	return resp.Result, nil
}

var errHTTPNotificationNoBody = errors.New("mcp notification returned no body")

func sendHTTPRPC(ctx context.Context, server config.MCPServerConfig, rpc rpcRequest, sessionID string) (rpcResponse, string, error) {
	endpoint, err := validateHTTPServerURL(server.URL)
	if err != nil {
		return rpcResponse{}, "", err
	}
	data, err := json.Marshal(rpc)
	if err != nil {
		return rpcResponse{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return rpcResponse{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2024-11-05")
	for key, value := range server.Headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", strings.TrimSpace(sessionID))
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return rpcResponse{}, "", err
	}
	defer resp.Body.Close()
	nextSessionID := firstNonEmpty(resp.Header.Get("Mcp-Session-Id"), resp.Header.Get("mcp-session-id"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return rpcResponse{}, nextSessionID, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rpcResponse{}, nextSessionID, fmt.Errorf("http mcp request failed with status %d: %s", resp.StatusCode, clipMCPText(strings.TrimSpace(string(body)), 4096))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		if rpc.ID == 0 {
			return rpcResponse{}, nextSessionID, errHTTPNotificationNoBody
		}
		return rpcResponse{}, nextSessionID, errors.New("http mcp response body is empty")
	}
	payload := body
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") || bytes.Contains(body, []byte("data:")) {
		payload, err = firstSSEData(body)
		if err != nil {
			return rpcResponse{}, nextSessionID, err
		}
	}
	var decoded rpcResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return rpcResponse{}, nextSessionID, err
	}
	return decoded, nextSessionID, nil
}

func validateHTTPServerURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(UnwrapCCRProxyURL(rawURL)))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("mcp url must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("mcp url host is required")
	}
	return parsed.String(), nil
}

func firstSSEData(body []byte) ([]byte, error) {
	events := strings.Split(string(body), "\n\n")
	for _, event := range events {
		var builder strings.Builder
		for _, line := range strings.Split(event, "\n") {
			line = strings.TrimRight(line, "\r")
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		data := strings.TrimSpace(builder.String())
		if data == "" || data == "[DONE]" {
			continue
		}
		return []byte(data), nil
	}
	return nil, errors.New("text/event-stream response contained no JSON data")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mcpError(err error, stderr *bytes.Buffer) error {
	if err == nil || stderr == nil {
		return err
	}
	preview := strings.TrimSpace(stderr.String())
	if preview == "" {
		return err
	}
	return fmt.Errorf("%w; stderr: %s", err, clipMCPText(preview, 4096))
}

func clipMCPText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
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
