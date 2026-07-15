package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestAppCloseHandlesEmptyAndInitializedRuntimes(t *testing.T) {
	var nilApp *App
	require.NoError(t, nilApp.Close())
	require.NoError(t, (&App{}).Close())
	require.NoError(t, (&App{Tools: tools.NewRegistry(t.TempDir())}).Close())
}

func TestAppLSPClientPoolUsesRegistryOrOwnedFallback(t *testing.T) {
	standalone := &App{}
	first := standalone.lspClientPool()
	require.NotNil(t, first)
	require.Same(t, first, standalone.lspClientPool())
	require.NoError(t, standalone.Close())

	registry := tools.NewRegistry(t.TempDir())
	withRegistry := &App{Tools: registry}
	require.Same(t, registry.LSPClientPool(), withRegistry.lspClientPool())
	require.NoError(t, withRegistry.Close())
}

func TestResumedDebugToolCallAllowedFollowsRegistry(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	app := &App{Tools: registry}
	ctx := context.Background()

	for _, info := range registry.Infos() {
		allowed, err := app.resumedDebugToolCallAllowed(ctx, info.Name)
		require.NoError(t, err)
		require.Truef(t, allowed, "registered tool %q should be accepted by resumed debug-tool-call", info.Name)
	}

	for alias, canonical := range tools.ClaudeToolAliases() {
		if !registry.Has(canonical) {
			continue
		}
		allowed, err := app.resumedDebugToolCallAllowed(ctx, alias)
		require.NoError(t, err)
		require.Truef(t, allowed, "alias %q for %q should be accepted by resumed debug-tool-call", alias, canonical)
	}

	allowed, err := app.resumedDebugToolCallAllowed(ctx, "missing_debug_tool")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestResumeMCPToolHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_RESUME_MCP_HELPER") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var req map[string]any
		if err := json.Unmarshal([]byte(reader.Text()), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			writeResumeMCPResponse(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "resume", "version": "0.0.0"},
			})
		case "tools/list":
			writeResumeMCPResponse(id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text.",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			writeResumeMCPResponse(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "resume-echo"}}})
		case "resources/list":
			writeResumeMCPResponse(id, map[string]any{"resources": []map[string]any{{"uri": "codog://resume-note", "name": "resume note", "mimeType": "text/plain"}}})
		case "resources/templates/list":
			writeResumeMCPResponse(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://resume/{name}",
				"name":        "resume named note",
			}}})
		case "resources/read":
			params, _ := req["params"].(map[string]any)
			uri, _ := params["uri"].(string)
			writeResumeMCPResponse(id, map[string]any{"contents": []map[string]any{{"uri": uri, "mimeType": "text/plain", "text": "resume note body"}}})
		case "prompts/list":
			writeResumeMCPResponse(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a resume topic.",
			}}})
		case "prompts/get":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			topic, _ := args["topic"].(string)
			writeResumeMCPResponse(id, map[string]any{"messages": []map[string]any{{
				"role":    "user",
				"content": map[string]any{"type": "text", "text": "Review " + topic},
			}}})
		}
	}
	os.Exit(0)
}

func writeResumeMCPResponse(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(strings.TrimSpace(string(data)))
}

func TestInvalidPermissionModeJSONContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--permission-mode", "bogus", "status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_permission_mode", report.Kind)
	require.Equal(t, "invalid_permission_mode", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Contains(t, report.Message, "bogus")
	require.Contains(t, report.Hint, "workspace-write")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--permission-mode", "bogus", "status"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "invalid_permission_mode")
}

func TestDuplicateGlobalFlagJSONContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	tests := []struct {
		name   string
		args   []string
		option string
		values []string
	}{
		{
			name:   "permission mode",
			args:   []string{"--config", configPath, "--output-format", "json", "--permission-mode", "read-only", "--permission-mode", "danger-full-access", "status"},
			option: "--permission-mode",
			values: []string{"read-only", "danger-full-access"},
		},
		{
			name:   "output format",
			args:   []string{"--config", configPath, "--output-format", "json", "--output-format", "text", "status"},
			option: "--output-format",
			values: []string{"json", "text"},
		},
		{
			name:   "model",
			args:   []string{"--config", configPath, "--output-format", "json", "--model", "openai/gpt-4", "--model", "claude-test", "status"},
			option: "--model",
			values: []string{"openai/gpt-4", "claude-test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), tt.args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "duplicate_flag", report.Kind)
			require.Equal(t, "duplicate_flag", report.ErrorKind)
			require.Equal(t, tt.option, report.Option)
			require.Equal(t, tt.values, report.Values)
			require.Contains(t, report.Message, "specified multiple times")
		})
	}
}

func TestInvalidOutputFormatJSONContract(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "YAML", "status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_output_format", report.Kind)
	require.Equal(t, "invalid_output_format", report.ErrorKind)
	require.Equal(t, "YAML", report.Value)
	require.Equal(t, []string{"text", "json"}, report.Expected)
	require.Contains(t, report.Hint, "--output-format json")

	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, marshalErr := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, marshalErr)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "prompt", "hello", "--output-format", "YAML"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_output_format", report.Kind)
	require.Equal(t, "YAML", report.Value)
	require.Equal(t, []string{"text", "json", "stream-json"}, report.Expected)
}

func capabilityReportHasTool(report capabilitiesReport, name string) bool {
	_, ok := capabilityReportTool(report, name)
	return ok
}

func capabilityResolveMatch(report capabilityResolveReport, kind string, name string) (capabilityRegistryMatch, bool) {
	for _, match := range report.Matches {
		if match.Kind == kind && match.Name == name {
			return match, true
		}
	}
	return capabilityRegistryMatch{}, false
}

func modelRouteExists(routes []modelRouteReport, prefix string, provider string) bool {
	for _, route := range routes {
		if route.Prefix == prefix && route.Provider == provider {
			return true
		}
	}
	return false
}

func modelAliasExists(aliases []modelAliasReport, name string, model string, provider string) bool {
	for _, alias := range aliases {
		if alias.Name == name && alias.Model == model && alias.Provider == provider && alias.MaxOutputTokens > 0 && alias.ContextWindowTokens > 0 {
			return true
		}
	}
	return false
}

func capabilityReportTool(report capabilitiesReport, name string) (capabilityTool, bool) {
	for _, tool := range report.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return capabilityTool{}, false
}

func capabilityReportHasSlash(report capabilitiesReport, name string) bool {
	_, ok := capabilityReportSlash(report, name)
	return ok
}

func slashCommandReportHasCommand(report slashCommandReport, name string) bool {
	for _, command := range report.Commands {
		if command.Name == name {
			return true
		}
	}
	return false
}

func capabilityReportResumeSafeSlashNames(report capabilitiesReport) []string {
	names := []string{}
	for _, command := range report.SlashCommands {
		if command.ResumeSupported {
			names = append(names, command.Name)
		}
	}
	return names
}

func capabilityReportSlash(report capabilitiesReport, name string) (capabilitySlash, bool) {
	for _, command := range report.SlashCommands {
		if command.Name == name {
			return command, true
		}
	}
	return capabilitySlash{}, false
}

func capabilityReportHasMCPResource(report capabilitiesReport, uri string) bool {
	for _, resource := range report.MCP.LocalResources {
		if resource["uri"] == uri {
			return true
		}
	}
	return false
}

func capabilityReportHasMCPPrompt(report capabilitiesReport, name string) bool {
	for _, prompt := range report.MCP.LocalPrompts {
		if prompt["name"] == name {
			return true
		}
	}
	return false
}

func reportSchemaRegistryEntry(t *testing.T, registry reportschema.Registry, id string) reportschema.RegistryReport {
	t.Helper()
	for _, candidate := range registry.Reports {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("missing report schema registry entry %q in %#v", id, registry.Reports)
	return reportschema.RegistryReport{}
}

func reportSchemaFieldIDs(registry reportschema.Registry) []string {
	ids := make([]string, 0, len(registry.Fields))
	for _, field := range registry.Fields {
		ids = append(ids, field.ID)
	}
	return ids
}

func TestACPStatusCommandOutputsTextJSONAndUnsupported(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, renderACPStatus(&out, nil))
	require.Contains(t, out.String(), "ACP / Zed")
	require.Contains(t, out.String(), "Supported        true")
	require.Contains(t, out.String(), "stdio JSON-RPC")
	out.Reset()

	require.NoError(t, renderACPStatus(&out, []string{"serve", "--output-format", "json"}))
	var report acpStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "1.0", report.SchemaVersion)
	require.Equal(t, "acp", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.Supported)
	require.NotNil(t, report.LaunchCommand)
	require.Equal(t, "ACP/Zed", report.Protocol.Name)
	require.True(t, report.Protocol.JSONRPC)
	require.False(t, report.Protocol.Daemon)
	require.NotNil(t, report.Protocol.Endpoint)
	require.True(t, report.Protocol.ServeStartsDaemon)
	require.Contains(t, report.Protocol.Methods, "initialize")
	require.Contains(t, report.Protocol.Methods, "workspace/info")
	require.Contains(t, report.Protocol.Methods, "workspace/files")
	require.Contains(t, report.Protocol.Methods, "workspace/search")
	require.Contains(t, report.Protocol.Methods, "file/read")
	require.Contains(t, report.Protocol.Methods, "file/write")
	require.Contains(t, report.Protocol.Methods, "file/edit")
	require.Contains(t, report.Protocol.Methods, "file/diff")
	require.Contains(t, report.Protocol.Methods, "editor/identify")
	require.Contains(t, report.Protocol.Methods, "editor/state")
	require.Contains(t, report.Protocol.Methods, "editor/open")
	require.Contains(t, report.Protocol.Methods, "editor/selection")
	require.Contains(t, report.Protocol.Methods, "bridge/faults/list")
	require.Contains(t, report.Protocol.Methods, "bridge/faults/record")
	require.Contains(t, report.Protocol.Methods, "bridge/faults/clear")
	require.Contains(t, report.Protocol.Methods, "diagnostics/go")
	require.Contains(t, report.Protocol.Methods, "code/symbols")
	require.Contains(t, report.Protocol.Methods, "code/references")
	require.Contains(t, report.Protocol.Methods, "code/definition")
	require.Contains(t, report.Protocol.Methods, "code/hover")
	require.Contains(t, report.Protocol.Methods, "code/completion")
	require.Contains(t, report.Protocol.Methods, "code/format")
	require.Contains(t, report.Protocol.Methods, "notebook/read")
	require.Contains(t, report.Protocol.Methods, "notebook/edit")
	require.Contains(t, report.Protocol.Methods, "lsp/actions")
	require.Contains(t, report.Protocol.Methods, "lsp/discover")
	require.Contains(t, report.Protocol.Methods, "lsp/list")
	require.Contains(t, report.Protocol.Methods, "lsp/start")
	require.Contains(t, report.Protocol.Methods, "lsp/status")
	require.Contains(t, report.Protocol.Methods, "lsp/stop")
	require.Contains(t, report.Protocol.Methods, "lsp/query")
	require.Contains(t, report.Protocol.Methods, "background/list")
	require.Contains(t, report.Protocol.Methods, "background/run")
	require.Contains(t, report.Protocol.Methods, "background/get")
	require.Contains(t, report.Protocol.Methods, "background/logs")
	require.Contains(t, report.Protocol.Methods, "background/board")
	require.Contains(t, report.Protocol.Methods, "background/heartbeat")
	require.Contains(t, report.Protocol.Methods, "background/stop")
	require.Contains(t, report.Protocol.Methods, "background/restart")
	require.Contains(t, report.Protocol.Methods, "background/prune")
	require.Contains(t, report.Protocol.Methods, "background/supervise")
	require.Contains(t, report.Protocol.Methods, "background/watch")
	require.Contains(t, report.Protocol.Methods, "agent-runs/list")
	require.Contains(t, report.Protocol.Methods, "agent-runs/get")
	require.Contains(t, report.Protocol.Methods, "agent-runs/logs")
	require.Contains(t, report.Protocol.Methods, "agent-runs/board")
	require.Contains(t, report.Protocol.Methods, "agent-runs/heartbeat")
	require.Contains(t, report.Protocol.Methods, "agent-runs/stop")
	require.Contains(t, report.Protocol.Methods, "agent-runs/prune")
	require.Contains(t, report.Protocol.Methods, "mcp/list")
	require.Contains(t, report.Protocol.Methods, "mcp/show")
	require.Contains(t, report.Protocol.Methods, "mcp/auth")
	require.Contains(t, report.Protocol.Methods, "mcp/tools")
	require.Contains(t, report.Protocol.Methods, "mcp/call")
	require.Contains(t, report.Protocol.Methods, "mcp/resources")
	require.Contains(t, report.Protocol.Methods, "mcp/resource-templates")
	require.Contains(t, report.Protocol.Methods, "mcp/read")
	require.Contains(t, report.Protocol.Methods, "mcp/prompts")
	require.Contains(t, report.Protocol.Methods, "mcp/prompt")
	require.Contains(t, report.Protocol.Methods, "session/open")
	require.Contains(t, report.Protocol.Methods, "session/list")
	require.Contains(t, report.Protocol.Methods, "session/append_message")
	require.Contains(t, report.Protocol.Methods, "session/append_input")
	require.Contains(t, report.Protocol.Methods, "session/rewind")
	require.Contains(t, report.Protocol.Methods, "session/fork")
	require.Contains(t, report.Protocol.Methods, "session/prune")
	require.Contains(t, report.Protocol.Methods, "prompt")
	require.Contains(t, report.Protocol.Methods, "shutdown")
	require.Equal(t, "unsupported_acp_invocation", report.Contracts.UnsupportedInvocationKind)
	require.Contains(t, report.Contracts.BlockingGates, "prompt")
	require.Contains(t, report.Aliases, "--acp")
	require.Contains(t, report.Aliases, "start")
	out.Reset()

	require.NoError(t, renderACPStatus(&out, []string{"start", "--output-format", "json"}))
	var startReport acpStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &startReport))
	require.Equal(t, "acp", startReport.Kind)
	require.Equal(t, "ok", startReport.Status)
	out.Reset()

	err := renderACPStatus(&out, []string{"bogus", "--json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported_acp_invocation")
	var unsupported acpUnsupportedReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "unsupported_acp_invocation", unsupported.Kind)
	require.Equal(t, "error", unsupported.Status)
	require.False(t, unsupported.Supported)
	require.Equal(t, []string{"bogus", "--json"}, unsupported.Invocation)

	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"acp", "--help", "--output-format", "json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var help helpReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &help))
	require.Equal(t, "help", help.Kind)
	require.Equal(t, "acp", help.Command)
	require.Contains(t, help.ProtocolMethods, "session/history")
	require.Contains(t, help.Aliases, "-acp")

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "acp", "--help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &help))
	require.Equal(t, "help", help.Kind)
	require.Equal(t, "acp", help.Command)
	require.Contains(t, help.ProtocolFields, "serve_starts_daemon")

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--acp", "--help", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &help))
	require.Equal(t, "help", help.Kind)
	require.Equal(t, "acp", help.Command)
	require.Contains(t, help.Aliases, "--acp")
}

func TestACPServeExposesSessionQueries(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	sess, err := store.Open("")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(sess.ID, "first prompt"))
	require.NoError(t, store.AppendInput(sess.ID, "second prompt"))
	require.NoError(t, store.Append(sess.ID, anthropic.Message{
		Role:    "assistant",
		Content: []anthropic.ContentBlock{{Type: "text", Text: "answer"}},
	}))
	_, err = store.Create("empty-acp-session")
	require.NoError(t, err)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/append_input","params":{"session_id":"` + sess.ID + `","input":"third prompt"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/history","params":{"session_id":"` + sess.ID + `","limit":1}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/append_message","params":{"session_id":"` + sess.ID + `","role":"user","text":"manual note"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/rewind","params":{"session_id":"` + sess.ID + `","remove_messages":1}}`,
		`{"jsonrpc":"2.0","id":6,"method":"session/fork","params":{"session_id":"` + sess.ID + `","branch_name":"acp-branch"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"session/rename","params":{"session_id":"` + sess.ID + `","new_session_id":"renamed-acp-session"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/delete","params":{"session_id":"renamed-acp-session"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"session/prune","params":{}}`,
		`{"jsonrpc":"2.0","id":10,"method":"session/prune","params":{"confirm":true}}`,
		`{"jsonrpc":"2.0","id":11,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 11)
	listResult := responses[0]["result"].(map[string]any)
	require.Equal(t, "session_list", listResult["kind"])
	require.EqualValues(t, 2, listResult["count"])
	sessions := listResult["sessions"].([]any)
	sessionSummaries := map[string]map[string]any{}
	for _, raw := range sessions {
		summary := raw.(map[string]any)
		sessionSummaries[summary["session_id"].(string)] = summary
		require.NotZero(t, summary["created_at_ms"])
		require.NotZero(t, summary["updated_at_ms"])
		require.NotZero(t, summary["modified_epoch_millis"])
		if summary["session_id"] == "empty-acp-session" {
			require.Equal(t, "empty", summary["lifecycle"].(map[string]any)["kind"])
		} else {
			require.Equal(t, "saved_only", summary["lifecycle"].(map[string]any)["kind"])
		}
	}
	require.EqualValues(t, 1, sessionSummaries[sess.ID]["message_count"])

	appendInputResult := responses[1]["result"].(map[string]any)
	require.Equal(t, "session_mutation", appendInputResult["kind"])
	require.Equal(t, "append_input", appendInputResult["action"])
	require.Equal(t, sess.ID, appendInputResult["session_id"])

	historyResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "session_history", historyResult["kind"])
	require.Equal(t, sess.ID, historyResult["session_id"])
	require.EqualValues(t, 1, historyResult["count"])
	entries := historyResult["entries"].([]any)
	require.Equal(t, "third prompt", entries[0].(map[string]any)["text"])

	appendMessageResult := responses[3]["result"].(map[string]any)
	require.Equal(t, "session_mutation", appendMessageResult["kind"])
	require.Equal(t, "append_message", appendMessageResult["action"])
	require.Equal(t, sess.ID, appendMessageResult["session_id"])
	require.EqualValues(t, 2, appendMessageResult["message_count"])

	rewindResult := responses[4]["result"].(map[string]any)
	require.Equal(t, "session_mutation", rewindResult["kind"])
	require.Equal(t, "rewind", rewindResult["action"])
	require.Equal(t, sess.ID, rewindResult["session_id"])
	require.EqualValues(t, 2, rewindResult["original_messages"])
	require.EqualValues(t, 1, rewindResult["remaining_messages"])
	require.EqualValues(t, 1, rewindResult["removed_messages"])

	forkResult := responses[5]["result"].(map[string]any)
	require.Equal(t, "session_mutation", forkResult["kind"])
	require.Equal(t, "fork", forkResult["action"])
	forkedID := forkResult["session_id"].(string)
	require.NotEmpty(t, forkedID)
	forked, err := store.OpenExisting(forkedID)
	require.NoError(t, err)
	require.Equal(t, sess.ID, forked.Metadata.ParentSessionID)
	require.Equal(t, "acp-branch", forked.Metadata.BranchName)
	require.Equal(t, "fork:acp-branch", forked.Identity.Purpose)
	require.Len(t, forked.Messages, 1)

	renameResult := responses[6]["result"].(map[string]any)
	require.Equal(t, "session_mutation", renameResult["kind"])
	require.Equal(t, "rename", renameResult["action"])
	require.Equal(t, sess.ID, renameResult["session_id"])
	require.Equal(t, "renamed-acp-session", renameResult["new_session_id"])
	exists, err := store.Exists("renamed-acp-session")
	require.NoError(t, err)
	require.False(t, exists)

	deleteResult := responses[7]["result"].(map[string]any)
	require.Equal(t, "session_mutation", deleteResult["kind"])
	require.Equal(t, "delete", deleteResult["action"])
	require.Equal(t, "renamed-acp-session", deleteResult["session_id"])
	dryRun := responses[8]["result"].(map[string]any)
	require.Equal(t, "session_prune", dryRun["kind"])
	require.Equal(t, "dry_run", dryRun["status"])
	require.Equal(t, true, dryRun["dry_run"])
	require.EqualValues(t, 1, dryRun["candidate_count"])
	confirmed := responses[9]["result"].(map[string]any)
	require.Equal(t, "session_prune", confirmed["kind"])
	require.Equal(t, "ok", confirmed["status"])
	require.EqualValues(t, 1, confirmed["deleted_count"])
	emptyExists, err := store.Exists("empty-acp-session")
	require.NoError(t, err)
	require.False(t, emptyExists)
	forkedExists, err := store.Exists(forkedID)
	require.NoError(t, err)
	require.True(t, forkedExists)
}

func TestACPServeOpenCreatesAndGetRequiresExisting(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/open","params":{"sessionId":"opened-from-acp"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/get","params":{"session_id":"opened-from-acp"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/get","params":{"session_id":"missing-from-acp"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 4)
	openResult := responses[0]["result"].(map[string]any)
	require.Equal(t, "opened-from-acp", openResult["session_id"])
	require.EqualValues(t, 0, openResult["message_count"])
	getResult := responses[1]["result"].(map[string]any)
	require.Equal(t, "opened-from-acp", getResult["session_id"])
	require.EqualValues(t, 0, getResult["message_count"])
	errPayload := responses[2]["error"].(map[string]any)
	require.EqualValues(t, -32603, errPayload["code"])
	require.Contains(t, errPayload["message"], "missing-from-acp")
	exists, err := store.Exists("missing-from-acp")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestACPServeExposesWorkspaceQueries(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("package main\n// needle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("needle in docs\n"), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"workspace/info","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"workspace/files","params":{"path":"src","pattern":"*.go","limit":5}}`,
		`{"jsonrpc":"2.0","id":3,"method":"workspace/search","params":{"query":"needle","glob":"*.go","limit":5}}`,
		`{"jsonrpc":"2.0","id":4,"method":"workspace/search","params":{}}`,
		`{"jsonrpc":"2.0","id":5,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 5)
	info := responses[0]["result"].(map[string]any)
	require.Equal(t, workspace, info["path"])
	require.Equal(t, filepath.Base(workspace), info["name"])
	files := responses[1]["result"].(map[string]any)
	require.Equal(t, "src", files["root"])
	fileEntries := files["files"].([]any)
	require.Len(t, fileEntries, 1)
	require.Equal(t, "src/main.go", fileEntries[0].(map[string]any)["path"])
	search := responses[2]["result"].(map[string]any)
	matches := search["matches"].([]any)
	require.Len(t, matches, 1)
	require.Equal(t, "src/main.go", matches[0].(map[string]any)["path"])
	require.EqualValues(t, 2, matches[0].(map[string]any)["line"])
	errPayload := responses[3]["error"].(map[string]any)
	require.EqualValues(t, -32603, errPayload["code"])
	require.Contains(t, errPayload["message"], "query is required")
}

func TestACPServeExposesFileOperations(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src"), 0o755))
	outsidePath := filepath.Join(filepath.Dir(workspace), "outside-acp-file.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("outside"), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	outsideRel := "../" + filepath.Base(outsidePath)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"file/write","params":{"path":"src/main.go","content":"package main\nfunc main() {}\n"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"file/read","params":{"path":"src/main.go","limit":12}}`,
		`{"jsonrpc":"2.0","id":3,"method":"file/diff","params":{"path":"src/main.go","old_string":"main()","new_string":"run()"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"file/edit","params":{"path":"src/main.go","old_string":"main()","new_string":"run()"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"file/read","params":{"path":"src/main.go"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"file/read","params":{"path":"` + outsideRel + `"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 7)
	writeResult := responses[0]["result"].(map[string]any)
	require.Equal(t, "src/main.go", writeResult["path"])
	require.EqualValues(t, len("package main\nfunc main() {}\n"), writeResult["bytes"])
	readWindow := responses[1]["result"].(map[string]any)
	require.Equal(t, "src/main.go", readWindow["path"])
	require.Equal(t, "package main", readWindow["content"])
	require.Equal(t, true, readWindow["truncated"])
	diffResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "src/main.go", diffResult["path"])
	require.Contains(t, diffResult["diff"], "-func main() {}")
	require.Contains(t, diffResult["diff"], "+func run() {}")
	editResult := responses[3]["result"].(map[string]any)
	require.Equal(t, "src/main.go", editResult["path"])
	require.EqualValues(t, 1, editResult["replacements"])
	readUpdated := responses[4]["result"].(map[string]any)
	require.Contains(t, readUpdated["content"], "func run() {}")
	errPayload := responses[5]["error"].(map[string]any)
	require.EqualValues(t, -32603, errPayload["code"])
	require.Contains(t, errPayload["message"], "escapes workspace")
	data, err := os.ReadFile(filepath.Join(workspace, "src", "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func run() {}")
}

func TestACPServeExposesEditorBridgeControls(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"editor/identify","params":{"editor":"VS Code","version":"1.0","workspace":"` + filepath.ToSlash(workspace) + `","token":"secret"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"editor/open","params":{"path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"editor/selection","params":{"start_line":1,"start_column":1,"end_line":1,"end_column":8}}`,
		`{"jsonrpc":"2.0","id":4,"method":"editor/state","params":{}}`,
		`{"jsonrpc":"2.0","id":5,"method":"bridge/faults/record","params":{"action":"latency","args":["250ms"]}}`,
		`{"jsonrpc":"2.0","id":6,"method":"bridge/faults/list","params":{}}`,
		`{"jsonrpc":"2.0","id":7,"method":"bridge/faults/clear","params":{}}`,
		`{"jsonrpc":"2.0","id":8,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				EditorBridgeToken: "secret",
			},
		},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 8)
	identity := responses[0]["result"].(map[string]any)
	require.Equal(t, "VS Code", identity["editor"])
	require.Equal(t, true, identity["trusted"])
	openFile := responses[1]["result"].(map[string]any)
	require.Equal(t, "main.go", openFile["path"])
	selection := responses[2]["result"].(map[string]any)
	require.Equal(t, "main.go", selection["path"])
	require.Equal(t, "package", selection["text"])
	state := responses[3]["result"].(map[string]any)
	require.NotNil(t, state["identity"])
	require.NotNil(t, state["open_file"])
	require.NotNil(t, state["selection"])
	recorded := responses[4]["result"].(map[string]any)
	require.Equal(t, "bridge_faults", recorded["kind"])
	require.EqualValues(t, 1, recorded["total"])
	require.Equal(t, "latency", recorded["recorded"].(map[string]any)["action"])
	list := responses[5]["result"].(map[string]any)
	require.EqualValues(t, 1, list["total"])
	cleared := responses[6]["result"].(map[string]any)
	require.Equal(t, true, cleared["cleared"])
	require.NotNil(t, responses[7]["result"])
	require.FileExists(t, filepath.Join(configHome, "bridge", "editor-state.json"))
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestACPServeExposesCodeIntelQueries(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/acp\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(`package main

type Runner struct{}

func Run() string { return "ok" }

func main(){ _ = Run() }
`), 0o644))
	unformatted, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"code/symbols","params":{"path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"code/definition","params":{"symbol":"Run"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"code/references","params":{"symbol":"Run","limit":5}}`,
		`{"jsonrpc":"2.0","id":4,"method":"code/hover","params":{"symbol":"Run","context_lines":1}}`,
		`{"jsonrpc":"2.0","id":5,"method":"code/completion","params":{"query":"Ru","limit":5}}`,
		`{"jsonrpc":"2.0","id":6,"method":"code/format","params":{"path":"main.go"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 7)
	symbols := responses[0]["result"].(map[string]any)
	require.Equal(t, "symbols", symbols["kind"])
	require.GreaterOrEqual(t, int(symbols["total"].(float64)), 2)
	definition := responses[1]["result"].(map[string]any)
	require.Equal(t, "definition", definition["kind"])
	require.Equal(t, true, definition["found"])
	definitionPayload := definition["definition"].(map[string]any)
	require.Equal(t, "Run", definitionPayload["name"])
	require.Equal(t, "main.go", definitionPayload["path"])
	references := responses[2]["result"].(map[string]any)
	require.Equal(t, "references", references["kind"])
	require.Equal(t, "Run", references["symbol"])
	require.GreaterOrEqual(t, int(references["total"].(float64)), 2)
	hover := responses[3]["result"].(map[string]any)
	require.Equal(t, "hover", hover["kind"])
	hoverPayload := hover["hover"].(map[string]any)
	require.Equal(t, true, hoverPayload["found"])
	require.Equal(t, "Run", hoverPayload["symbol"])
	completion := responses[4]["result"].(map[string]any)
	require.Equal(t, "completion", completion["kind"])
	require.Equal(t, "Ru", completion["query"])
	require.GreaterOrEqual(t, int(completion["total"].(float64)), 1)
	formatPreview := responses[5]["result"].(map[string]any)
	require.Equal(t, "format", formatPreview["kind"])
	require.Equal(t, false, formatPreview["write"])
	previewResult := formatPreview["result"].(map[string]any)
	require.Equal(t, true, previewResult["changed"])
	require.Contains(t, previewResult["content"], "func main()")
	data, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Equal(t, string(unformatted), string(data))

	writeInput := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"code/format","params":{"path":"main.go","write":true}}`,
		`{"jsonrpc":"2.0","id":2,"method":"diagnostics/go","params":{"patterns":["./..."]}}`,
		`{"jsonrpc":"2.0","id":3,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	out.Reset()
	app.In = strings.NewReader(writeInput)
	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses = decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 3)
	formatWrite := responses[0]["result"].(map[string]any)
	require.Equal(t, "format", formatWrite["kind"])
	require.Equal(t, true, formatWrite["write"])
	writeResult := formatWrite["result"].(map[string]any)
	require.Equal(t, true, writeResult["changed"])
	data, err = os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Equal(t, writeResult["content"], string(data))
	diagnostics := responses[1]["result"].(map[string]any)
	require.Equal(t, "diagnostics", diagnostics["kind"])
	require.EqualValues(t, 0, diagnostics["total"])
}

func TestACPServeExposesNotebookReadAndEdit(t *testing.T) {
	workspace := t.TempDir()
	notebookPath := filepath.Join(workspace, "notes.ipynb")
	require.NoError(t, os.WriteFile(notebookPath, []byte(`{
  "metadata": {"kernelspec": {"language": "python"}},
  "cells": [
    {
      "cell_type": "markdown",
      "id": "intro",
      "metadata": {},
      "source": ["# Title\n"]
    }
  ]
}`), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	insertSource := "print('hello')\n"
	replaceSource := "print('updated')\n"
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"notebook/read","params":{"path":"notes.ipynb","limit":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"notebook/edit","params":{"path":"notes.ipynb","mode":"insert","cell_id":"intro","cell_type":"code","new_source":` + fmt.Sprintf("%q", insertSource) + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"notebook/read","params":{"path":"notes.ipynb","cell_index":1}}`,
		`{"jsonrpc":"2.0","id":4,"method":"notebook/edit","params":{"notebook_path":"notes.ipynb","edit_mode":"replace","cell_id":"cell-2","type":"code","source":` + fmt.Sprintf("%q", replaceSource) + `}}`,
		`{"jsonrpc":"2.0","id":5,"method":"notebook/read","params":{"path":"notes.ipynb","index":1}}`,
		`{"jsonrpc":"2.0","id":6,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 6)
	readInitial := responses[0]["result"].(map[string]any)
	require.Equal(t, "notebook_read", readInitial["kind"])
	require.Equal(t, "notes.ipynb", readInitial["path"])
	require.EqualValues(t, 1, readInitial["cell_count"])
	insert := responses[1]["result"].(map[string]any)
	require.Equal(t, "notebook_edit", insert["kind"])
	insertResult := insert["result"].(map[string]any)
	require.Equal(t, "notes.ipynb", insertResult["path"])
	require.Equal(t, "insert", insertResult["mode"])
	require.Equal(t, "cell-2", insertResult["cell_id"])
	readInserted := responses[2]["result"].(map[string]any)
	insertedCells := readInserted["cells"].([]any)
	require.Equal(t, insertSource, insertedCells[0].(map[string]any)["source"])
	replace := responses[3]["result"].(map[string]any)
	require.Equal(t, "notebook_edit", replace["kind"])
	replaceResult := replace["result"].(map[string]any)
	require.Equal(t, "replace", replaceResult["mode"])
	readReplaced := responses[4]["result"].(map[string]any)
	replacedCells := readReplaced["cells"].([]any)
	require.Equal(t, replaceSource, replacedCells[0].(map[string]any)["source"])
	data, err := os.ReadFile(notebookPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "print('updated')")
}

func TestACPServeExposesLSPMetadata(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"lsp/actions","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"lsp/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"lsp/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 4)
	actions := responses[0]["result"].(map[string]any)
	require.Equal(t, "lsp_actions", actions["kind"])
	require.Equal(t, "ok", actions["status"])
	require.Greater(t, int(actions["count"].(float64)), 0)
	require.NotEmpty(t, actions["actions"].([]any))
	discover := responses[1]["result"].(map[string]any)
	require.Equal(t, "lsp_discover", discover["kind"])
	require.Greater(t, int(discover["count"].(float64)), 0)
	require.NotEmpty(t, discover["candidates"].([]any))
	list := responses[2]["result"].(map[string]any)
	require.Equal(t, "lsp_list", list["kind"])
	require.EqualValues(t, 0, list["count"])
	require.Empty(t, list["servers"].([]any))
}

func TestACPServeExposesLSPLifecycleAndQuery(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(strings.Join([]string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"hi\")",
		"}",
		"",
	}, "\n")), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	fakeCommand := "CODOG_AGENT_FAKE_LSP=1 " + shellQuote(os.Args[0]) + " -test.run '^TestACPFakeLSPServer$'"
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"lsp/start","params":{"language":"go","command":` + strconv.Quote(fakeCommand) + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"lsp/query","params":{"language":"go","action":"hover","path":"main.go","line":2,"character":5}}`,
		`{"jsonrpc":"2.0","id":3,"method":"lsp/query","params":{"language":"go","action":"diagnostics","file_path":"main.go","timeout_ms":1000}}`,
		`{"jsonrpc":"2.0","id":4,"method":"lsp/query","params":{"language":"go","action":"rename","path":"main.go","line":2,"character":5,"new_name":"Start","apply":true}}`,
		`{"jsonrpc":"2.0","id":5,"method":"lsp/status","params":{"language":"go"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"lsp/list","params":{}}`,
		`{"jsonrpc":"2.0","id":7,"method":"lsp/stop","params":{"language":"go"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 8)
	start := responses[0]["result"].(map[string]any)
	require.Equal(t, "lsp_start", start["kind"])
	require.Equal(t, "ok", start["status"])
	startServer := start["server"].(map[string]any)
	require.Equal(t, "go", startServer["language"])
	hover := responses[1]["result"].(map[string]any)
	require.Equal(t, "lsp_query", hover["kind"])
	require.Equal(t, "hover", hover["action"])
	require.Equal(t, "textDocument/hover", hover["method"])
	hoverResult := hover["result"].(map[string]any)
	hoverContents := hoverResult["contents"].(map[string]any)
	require.Equal(t, "agent fake hover", hoverContents["value"])
	diagnostics := responses[2]["result"].(map[string]any)
	require.Equal(t, "diagnostics", diagnostics["action"])
	require.Len(t, diagnostics["diagnostics"].([]any), 1)
	rename := responses[3]["result"].(map[string]any)
	require.Equal(t, "rename", rename["action"])
	require.Equal(t, "textDocument/rename", rename["method"])
	require.EqualValues(t, 1, rename["file_edits"])
	require.EqualValues(t, 1, rename["text_edits"])
	require.Equal(t, true, rename["applied"])
	renameEdits := rename["edits"].([]any)
	require.Contains(t, renameEdits[0].(map[string]any)["content"], "func Start()")
	data, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func Start()")
	status := responses[4]["result"].(map[string]any)
	require.Equal(t, "lsp_status", status["kind"])
	statusServer := status["server"].(map[string]any)
	require.Equal(t, "go", statusServer["language"])
	list := responses[5]["result"].(map[string]any)
	require.Equal(t, "lsp_list", list["kind"])
	require.EqualValues(t, 1, list["count"])
	stop := responses[6]["result"].(map[string]any)
	require.Equal(t, "lsp_stop", stop["kind"])
	require.Equal(t, "ok", stop["status"])
}

func TestACPServeExposesBackgroundControls(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	backgroundStore := background.NewStore(configHome)
	t.Cleanup(func() {
		require.Eventually(t, func() bool {
			tasks, err := backgroundStore.List()
			if err != nil {
				return false
			}
			active := false
			for _, task := range tasks {
				if !background.IsActiveStatus(task.Status) {
					continue
				}
				active = true
				_, _ = backgroundStore.Stop(task.ID)
			}
			return !active
		}, 2*time.Second, 20*time.Millisecond)
	})
	now := time.Now().UTC().Truncate(time.Second)
	oldCompleted := now.Add(-48 * time.Hour)
	bgDir := filepath.Join(configHome, "background")
	require.NoError(t, os.MkdirAll(bgDir, 0o755))
	oldLog := filepath.Join(bgDir, "old.log")
	failedLog := filepath.Join(bgDir, "failed.log")
	require.NoError(t, os.WriteFile(oldLog, []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(failedLog, []byte("failed"), 0o644))
	for _, task := range []background.Task{
		{
			ID:          "old",
			Command:     "printf old",
			Status:      "completed",
			StartedAt:   oldCompleted.Add(-time.Minute),
			CompletedAt: &oldCompleted,
			LogPath:     oldLog,
		},
		{
			ID:            "failed",
			Command:       "printf acp-supervise",
			Status:        "failed",
			Workspace:     workspace,
			StartedAt:     now.Add(-time.Minute),
			CompletedAt:   &now,
			LogPath:       failedLog,
			RestartPolicy: &background.RestartPolicy{Enabled: true, MaxAttempts: 1},
		},
	} {
		data, err := json.MarshalIndent(task, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(bgDir, task.ID+".json"), append(data, '\n'), 0o644))
	}

	runInput := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/run","params":{"command":"printf acp-bg","kind":"terminal","session_id":"ide-session"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(runInput),
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 2)
	runTask := responses[0]["result"].(map[string]any)
	taskID := runTask["id"].(string)
	require.NotEmpty(t, taskID)
	require.Equal(t, "terminal", runTask["kind"])
	require.Equal(t, "ide-session", runTask["session_id"])
	require.Eventually(t, func() bool {
		logs, err := backgroundStore.Logs(taskID, 4096)
		return err == nil && strings.Contains(logs, "acp-bg")
	}, 2*time.Second, 50*time.Millisecond)

	observedAt := now.Format(time.RFC3339)
	out.Reset()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/list","params":{"session_id":"ide-session","kind":"terminal"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"background/get","params":{"id":"` + taskID + `"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"background/logs","params":{"id":"` + taskID + `","limit":4096}}`,
		`{"jsonrpc":"2.0","id":4,"method":"background/heartbeat","params":{"id":"` + taskID + `","status":"working","transport_alive":true,"observed_at":"` + observedAt + `"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"background/board","params":{"stalled_after_seconds":3600}}`,
		`{"jsonrpc":"2.0","id":6,"method":"background/restart","params":{"id":"` + taskID + `"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"background/prune","params":{"older_than_days":1,"keep":0}}`,
		`{"jsonrpc":"2.0","id":8,"method":"background/supervise","params":{"now":"` + observedAt + `"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	app.In = strings.NewReader(input)
	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses = decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 9)
	list := responses[0]["result"].([]any)
	require.Len(t, list, 1)
	get := responses[1]["result"].(map[string]any)
	require.Equal(t, taskID, get["id"])
	logs := responses[2]["result"].(map[string]any)
	require.Equal(t, taskID, logs["id"])
	require.Contains(t, logs["logs"], "acp-bg")
	heartbeat := responses[3]["result"].(map[string]any)
	heartbeatPayload := heartbeat["heartbeat"].(map[string]any)
	require.Equal(t, "working", heartbeatPayload["status"])
	board := responses[4]["result"].(map[string]any)
	require.NotNil(t, board["generated_at"])
	restart := responses[5]["result"].(map[string]any)
	restartedID := restart["id"].(string)
	require.NotEmpty(t, restartedID)
	require.Equal(t, taskID, restart["restarted_from"])
	prune := responses[6]["result"].(map[string]any)
	require.EqualValues(t, 1, prune["removed_count"])
	removed := prune["removed"].([]any)
	require.Contains(t, removed, "old")
	require.NoFileExists(t, filepath.Join(bgDir, "old.json"))
	supervise := responses[7]["result"].(map[string]any)
	restarted := supervise["restarted"].([]any)
	require.Len(t, restarted, 1)
	require.Equal(t, "failed", restarted[0].(map[string]any)["restarted_from"])

	out.Reset()
	app.In = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/stop","params":{"id":"` + restartedID + `"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}`,
		"",
	}, "\n"))
	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses = decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 2)
	stopped := responses[0]["result"].(map[string]any)
	require.Equal(t, restartedID, stopped["id"])
}

func TestACPServeStreamsBackgroundWatch(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	bg := background.NewStore(configHome)
	task, err := bg.Run("printf acp-watch", workspace)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := bg.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "acp-watch")
	}, 2*time.Second, 50*time.Millisecond)
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"background/watch","params":{"id":"` + task.ID + `","max_events":2}}`,
		`{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 4)
	require.Equal(t, "background/event", responses[0]["method"])
	statusEvent := responses[0]["params"].(map[string]any)
	require.Equal(t, "status", statusEvent["type"])
	require.Equal(t, task.ID, statusEvent["id"])
	require.Equal(t, "background/event", responses[1]["method"])
	logEvent := responses[1]["params"].(map[string]any)
	require.Equal(t, "log", logEvent["type"])
	require.Equal(t, task.ID, logEvent["id"])
	require.Contains(t, logEvent["data"], "acp-watch")
	result := responses[2]["result"].(map[string]any)
	require.Equal(t, task.ID, result["id"])
	require.EqualValues(t, 2, result["events"])
	require.NotNil(t, responses[3]["result"])
}

func TestACPServeExposesAgentRunsControls(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	bg := background.NewStore(configHome)
	runs := agentruns.NewStore(configHome)
	store := session.NewWorkspaceStore(t.TempDir(), workspace)

	task, err := bg.RunWithOptions("printf agent-acp", workspace, background.RunOptions{
		Kind:      "agent",
		AgentType: "reviewer",
		SessionID: "session-acp",
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := bg.Logs(task.ID, 4096)
		return err == nil && strings.Contains(logs, "agent-acp")
	}, 2*time.Second, 50*time.Millisecond)
	run, err := runs.Save(agentruns.Run{
		ID:        "run-" + task.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "session-acp",
		TaskID:    task.ID,
		CreatedAt: task.StartedAt,
		UpdatedAt: task.StartedAt,
	})
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	orphan, err := runs.Save(agentruns.Run{
		ID:        "orphan-acp",
		Agent:     "reviewer",
		Workspace: workspace,
		SessionID: "old-session",
		TaskID:    "missing-task",
		CreatedAt: now.Add(-2 * time.Hour),
		UpdatedAt: now.Add(-2 * time.Hour),
	})
	require.NoError(t, err)

	longTask, err := bg.RunWithOptions("sleep 5", workspace, background.RunOptions{
		Kind:      "agent",
		AgentType: "reviewer",
	})
	require.NoError(t, err)
	longRun, err := runs.Save(agentruns.Run{
		ID:        "run-" + longTask.ID,
		Agent:     "reviewer",
		Workspace: workspace,
		TaskID:    longTask.ID,
		CreatedAt: longTask.StartedAt,
		UpdatedAt: longTask.StartedAt,
	})
	require.NoError(t, err)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"agent-runs/list","params":{"agent":"reviewer","session_id":"session-acp"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"agent-runs/get","params":{"id":"` + run.ID + `"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"agent-runs/logs","params":{"id":"` + run.ID + `","limit":4096}}`,
		`{"jsonrpc":"2.0","id":4,"method":"agent-runs/heartbeat","params":{"id":"` + run.ID + `","status":"working","transport_alive":true,"observed_at":"` + now.Format(time.RFC3339) + `"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"agent-runs/board","params":{"agent":"reviewer","session_id":"session-acp","stalled_after_seconds":3600}}`,
		`{"jsonrpc":"2.0","id":6,"method":"agent-runs/prune","params":{"older_than_seconds":3600,"keep":1}}`,
		`{"jsonrpc":"2.0","id":7,"method":"agent-runs/stop","params":"` + longRun.ID + `"}`,
		`{"jsonrpc":"2.0","id":8,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Sessions:  store,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 8)
	list := responses[0]["result"].([]any)
	require.Len(t, list, 1)
	listRun := list[0].(map[string]any)["run"].(map[string]any)
	require.Equal(t, run.ID, listRun["id"])
	get := responses[1]["result"].(map[string]any)
	getRun := get["run"].(map[string]any)
	require.Equal(t, run.ID, getRun["id"])
	logs := responses[2]["result"].(map[string]any)
	require.Equal(t, run.ID, logs["id"])
	require.Equal(t, task.ID, logs["task_id"])
	require.Contains(t, logs["logs"], "agent-acp")
	heartbeat := responses[3]["result"].(map[string]any)
	heartbeatTask := heartbeat["task"].(map[string]any)
	heartbeatPayload := heartbeatTask["heartbeat"].(map[string]any)
	require.Equal(t, "working", heartbeatPayload["status"])
	board := responses[4]["result"].(map[string]any)
	require.NotNil(t, board["generated_at"])
	require.NotNil(t, board["active"])
	require.NotNil(t, board["finished"])
	prune := responses[5]["result"].(map[string]any)
	require.EqualValues(t, 1, prune["removed_count"])
	require.Contains(t, prune["removed"].([]any), orphan.ID)
	require.NoFileExists(t, filepath.Join(configHome, "agent-runs", orphan.ID+".json"))
	stop := responses[6]["result"].(map[string]any)
	stopRun := stop["run"].(map[string]any)
	stopTask := stop["task"].(map[string]any)
	require.Equal(t, longRun.ID, stopRun["id"])
	require.Equal(t, "stopped", stopTask["status"])
	require.NotNil(t, responses[7]["result"])
}

func TestACPServeExposesMCPControls(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	server := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"mcp/list","params":{"inspect":false}}`,
		`{"jsonrpc":"2.0","id":2,"method":"mcp/show","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"mcp/auth","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"mcp/tools","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"mcp/call","params":{"server":"test","tool":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"mcp/resources","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"mcp/resource-templates","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"mcp/read","params":{"server":"test","uri":"codog://note"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"mcp/prompts","params":{"server":"test"}}`,
		`{"jsonrpc":"2.0","id":10,"method":"mcp/prompt","params":{"server":"test","prompt":"review","arguments":{"topic":"hooks"}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"shutdown","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			MCPServers: map[string]config.MCPServerConfig{"test": server},
		},
		Workspace: workspace,
		In:        strings.NewReader(input),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.ACP(context.Background(), []string{"serve"}))
	responses := decodeJSONRPCResponses(t, out.String())
	require.Len(t, responses, 11)
	list := responses[0]["result"].(map[string]any)
	require.Equal(t, "mcp_list", list["kind"])
	require.EqualValues(t, 1, list["count"])
	require.NotContains(t, list, "statuses")
	show := responses[1]["result"].(map[string]any)
	require.Equal(t, "mcp_show", show["kind"])
	require.Equal(t, "test", show["server"])
	descriptor := show["descriptor"].(map[string]any)
	require.Equal(t, "test", descriptor["name"])
	auth := responses[2]["result"].(map[string]any)
	require.Equal(t, "mcp_auth", auth["kind"])
	authResult := auth["result"].(map[string]any)
	require.Equal(t, "ok", authResult["status"])
	require.EqualValues(t, 1, authResult["tool_count"])
	tools := responses[3]["result"].(map[string]any)
	toolResult := tools["result"].(map[string]any)
	toolList := toolResult["tools"].([]any)
	require.Equal(t, "echo", toolList[0].(map[string]any)["name"])
	call := responses[4]["result"].(map[string]any)
	callResult := call["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(callResult["result"]), "hi")
	resources := responses[5]["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(resources["result"]), "codog://note")
	templates := responses[6]["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(templates["result"]), "codog://notes/{name}")
	read := responses[7]["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(read["result"]), "note body")
	prompts := responses[8]["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(prompts["result"]), "review")
	prompt := responses[9]["result"].(map[string]any)
	require.Contains(t, fmt.Sprint(prompt["result"]), "Review hooks")
	require.NotNil(t, responses[10]["result"])
}

func TestACPServeAliasesStartAndStdio(t *testing.T) {
	for _, alias := range []string{"start", "stdio"} {
		t.Run(alias, func(t *testing.T) {
			workspace := t.TempDir()
			store := session.NewWorkspaceStore(t.TempDir(), workspace)
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
				`{"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}`,
				"",
			}, "\n")
			var out bytes.Buffer
			app := &App{
				Workspace: workspace,
				Sessions:  store,
				In:        strings.NewReader(input),
				Out:       &out,
				Err:       io.Discard,
			}

			require.NoError(t, app.ACP(context.Background(), []string{alias}))
			responses := decodeJSONRPCResponses(t, out.String())
			require.Len(t, responses, 2)
			result := responses[0]["result"].(map[string]any)
			require.Equal(t, "codog-acp-0.1", result["protocolVersion"])
			capabilities := result["capabilities"].(map[string]any)
			workspaceCaps := capabilities["workspace"].(map[string]any)
			require.Equal(t, true, workspaceCaps["info"])
			require.Equal(t, true, workspaceCaps["files"])
			require.Equal(t, true, workspaceCaps["search"])
			fileCaps := capabilities["file"].(map[string]any)
			require.Equal(t, true, fileCaps["read"])
			require.Equal(t, true, fileCaps["write"])
			require.Equal(t, true, fileCaps["edit"])
			require.Equal(t, true, fileCaps["diff"])
			editorCaps := capabilities["editor"].(map[string]any)
			require.Equal(t, true, editorCaps["identify"])
			require.Equal(t, true, editorCaps["state"])
			require.Equal(t, true, editorCaps["open"])
			require.Equal(t, true, editorCaps["selection"])
			bridgeFaultCaps := capabilities["bridge_faults"].(map[string]any)
			require.Equal(t, true, bridgeFaultCaps["list"])
			require.Equal(t, true, bridgeFaultCaps["record"])
			require.Equal(t, true, bridgeFaultCaps["clear"])
			diagnosticsCaps := capabilities["diagnostics"].(map[string]any)
			require.Equal(t, true, diagnosticsCaps["go"])
			codeCaps := capabilities["code"].(map[string]any)
			require.Equal(t, true, codeCaps["symbols"])
			require.Equal(t, true, codeCaps["references"])
			require.Equal(t, true, codeCaps["definition"])
			require.Equal(t, true, codeCaps["hover"])
			require.Equal(t, true, codeCaps["completion"])
			require.Equal(t, true, codeCaps["format"])
			notebookCaps := capabilities["notebook"].(map[string]any)
			require.Equal(t, true, notebookCaps["read"])
			require.Equal(t, true, notebookCaps["edit"])
			lspCaps := capabilities["lsp"].(map[string]any)
			require.Equal(t, true, lspCaps["actions"])
			require.Equal(t, true, lspCaps["discover"])
			require.Equal(t, true, lspCaps["list"])
			require.Equal(t, true, lspCaps["start"])
			require.Equal(t, true, lspCaps["status"])
			require.Equal(t, true, lspCaps["stop"])
			require.Equal(t, true, lspCaps["query"])
			backgroundCaps := capabilities["background"].(map[string]any)
			require.Equal(t, true, backgroundCaps["list"])
			require.Equal(t, true, backgroundCaps["run"])
			require.Equal(t, true, backgroundCaps["get"])
			require.Equal(t, true, backgroundCaps["logs"])
			require.Equal(t, true, backgroundCaps["board"])
			require.Equal(t, true, backgroundCaps["heartbeat"])
			require.Equal(t, true, backgroundCaps["stop"])
			require.Equal(t, true, backgroundCaps["restart"])
			require.Equal(t, true, backgroundCaps["prune"])
			require.Equal(t, true, backgroundCaps["supervise"])
			require.Equal(t, true, backgroundCaps["watch"])
			agentRunCaps := capabilities["agent_runs"].(map[string]any)
			require.Equal(t, true, agentRunCaps["list"])
			require.Equal(t, true, agentRunCaps["get"])
			require.Equal(t, true, agentRunCaps["logs"])
			require.Equal(t, true, agentRunCaps["board"])
			require.Equal(t, true, agentRunCaps["heartbeat"])
			require.Equal(t, true, agentRunCaps["stop"])
			require.Equal(t, true, agentRunCaps["prune"])
			mcpCaps := capabilities["mcp"].(map[string]any)
			require.Equal(t, true, mcpCaps["list"])
			require.Equal(t, true, mcpCaps["show"])
			require.Equal(t, true, mcpCaps["auth"])
			require.Equal(t, true, mcpCaps["tools"])
			require.Equal(t, true, mcpCaps["call"])
			require.Equal(t, true, mcpCaps["resources"])
			require.Equal(t, true, mcpCaps["resource_templates"])
			require.Equal(t, true, mcpCaps["read"])
			require.Equal(t, true, mcpCaps["prompts"])
			require.Equal(t, true, mcpCaps["prompt"])
			sessions := capabilities["sessions"].(map[string]any)
			require.Equal(t, true, sessions["open"])
			require.Equal(t, true, sessions["append"])
			require.Equal(t, true, sessions["rewind"])
			require.Equal(t, true, sessions["fork"])
			require.Equal(t, true, sessions["prune"])
		})
	}
}

func TestParseACPGlobalInvocationSupportsOutputFormatBeforeCommand(t *testing.T) {
	args, ok, err := parseACPGlobalInvocation([]string{"--output-format", "json", "acp", "serve"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--output-format", "json", "serve"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--json", "acp"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--json"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--output-format=json", "acp"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--output-format=json"}, args)

	args, ok, err = parseACPGlobalInvocation([]string{"--output-format=json", "acp", "start"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"--output-format=json", "start"}, args)
	require.True(t, acpServeRequested(args))

	args, ok, err = parseACPGlobalInvocation([]string{"--json", "prompt", "hello"})
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, args)
}

func TestACPFakeLSPServer(t *testing.T) {
	if os.Getenv("CODOG_AGENT_FAKE_LSP") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	currentURI := ""
	for {
		raw, err := readACPTestLSPMessage(reader)
		if err != nil {
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id,omitempty"`
			Method  string          `json:"method,omitempty"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		switch msg.Method {
		case "initialize":
			_ = writeACPTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "textDocument/didOpen":
			uri := acpTestLSPDocumentURI(msg.Params)
			currentURI = uri
			_ = writeACPTestLSPMessage(os.Stdout, map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": uri,
					"diagnostics": []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 0},
							"end":   map[string]any{"line": 2, "character": 4},
						},
						"severity": 2,
						"source":   "agent-fake-lsp",
						"message":  "agent fake diagnostic",
					}},
				},
			})
		case "textDocument/hover":
			_ = writeACPTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"contents": map[string]any{"kind": "markdown", "value": "agent fake hover"}}})
		case "textDocument/rename":
			var params struct {
				NewName string `json:"newName"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if currentURI == "" {
				currentURI = "file:///workspace/main.go"
			}
			_ = writeACPTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"changes": map[string]any{
					currentURI: []map[string]any{{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 9},
						},
						"newText": params.NewName,
					}},
				},
			}})
		case "shutdown":
			_ = writeACPTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			return
		default:
			if msg.ID != nil {
				_ = writeACPTestLSPMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			}
		}
	}
}

func readACPTestLSPMessage(reader *bufio.Reader) (json.RawMessage, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		contentLength = parsed
	}
	if contentLength <= 0 {
		return nil, io.ErrUnexpectedEOF
	}
	data := make([]byte, contentLength)
	_, err := io.ReadFull(reader, data)
	return data, err
}
