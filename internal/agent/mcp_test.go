package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/mcpauthdiag"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/stretchr/testify/require"
)

func TestMCPAuthRefreshCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	saveExpiredToken := func() {
		t.Helper()
		_, err := oauth.SaveToken(configHome, oauth.Token{
			AccessToken:  "old-access",
			RefreshToken: "refresh-1",
			ExpiresAt:    time.Now().UTC().Add(-time.Hour),
		})
		require.NoError(t, err)
	}
	saveExpiredToken()
	mcpServer := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{"test": mcpServer}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"auth", "--refresh", "test"}))
	var report mcpauthdiag.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "test", report.Server)
	require.True(t, report.Refreshed)
	require.Empty(t, report.RefreshError)
	require.NotNil(t, report.Token)
	require.Equal(t, "refr...cess", report.Token.AccessToken)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", loaded.AccessToken)
	require.Equal(t, "refresh-2", loaded.RefreshToken)
	out.Reset()

	saveExpiredToken()
	require.NoError(t, app.MCP(context.Background(), []string{"auth", "refresh"}))
	var aggregate mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregate))
	require.Equal(t, "mcp", aggregate.Kind)
	require.Equal(t, "auth", aggregate.Action)
	require.Equal(t, "ok", aggregate.Status)
	require.Len(t, aggregate.AuthStatuses, 1)
	require.True(t, aggregate.AuthStatuses[0].Refreshed)
	loaded, err = oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", loaded.AccessToken)
}

func TestMCPAuthClearCommand(t *testing.T) {
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "access-token-1234",
		RefreshToken: "refresh-token-1234",
	})
	require.NoError(t, err)
	mcpServer := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{"test": mcpServer}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"auth", "clear", "test"}))
	var report mcpauthdiag.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "test", report.Server)
	require.True(t, report.Cleared)
	require.Empty(t, report.ClearError)
	require.NotNil(t, report.Logout)
	require.True(t, report.Logout.Deleted)
	require.NotNil(t, report.OAuthStatus)
	require.False(t, report.OAuthStatus.TokenPresent)
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "second-access-token"})
	require.NoError(t, err)
	require.Error(t, app.MCP(context.Background(), []string{"auth", "clear", "--json"}))
	var missing mcpRemoteActionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missing))
	require.Equal(t, "missing_argument", missing.ErrorKind)
	require.Equal(t, "server", missing.Argument)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "second-access-token", loaded.AccessToken)
}

func TestMCPAddCommand(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "mcp", "add", "demo", "echo", "ok", "--env", "A=B"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "mcp"`)
	require.Contains(t, out, `"action": "add"`)
	require.Contains(t, out, `"name": "demo"`)
	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"mcp_servers"`)
	require.Contains(t, string(stored), `"demo"`)
	require.Contains(t, string(stored), `"A=B"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "mcp", "add", "remote", "--url", "https://example.test/mcp", "--header", "Authorization=Bearer token", "--headers-helper", "./headers-helper", "--required"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"name": "remote"`)
	require.Contains(t, out, `"required": true`)
	require.Contains(t, out, `"headers_helper": "./headers-helper"`)
	stored, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"url": "https://example.test/mcp"`)
	require.Contains(t, string(stored), `"Authorization": "Bearer token"`)
	require.Contains(t, string(stored), `"headers_helper": "./headers-helper"`)
	require.Contains(t, string(stored), `"required": true`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "mcp", "show", "remote"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var show mcpShowReport
	require.NoError(t, json.Unmarshal([]byte(out), &show))
	require.NotNil(t, show.Server)
	require.True(t, show.Server.Details.HeadersHelperConfigured)
	require.NotContains(t, out, "./headers-helper")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "mcp", "show", "remote"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Headers helper   configured")
	require.NotContains(t, out, "./headers-helper")
}

func TestMCPCommandAcceptsGlobalOutputFormatWithoutServers(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), nil))
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Configured servers 0")
	require.Contains(t, out.String(), "Total entries     0")
	require.Contains(t, out.String(), "Required entries  0")
	require.Contains(t, out.String(), "Optional entries  0")
	require.Contains(t, out.String(), "No valid MCP servers configured.")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"--output-format", "json"}))
	var report mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "mcp", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, 0, report.ServerCount)
	require.Equal(t, 0, report.MCPValidation.TotalConfigured)
	require.Equal(t, 0, report.MCPValidation.RequiredCount)
	require.Equal(t, 0, report.MCPValidation.OptionalCount)
	require.Equal(t, 0, report.MCPValidation.InvalidCount)
	require.NotEmpty(t, report.WorkingDirectory)
}

func TestMCPListTextReportsInvalidServers(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{"missing": {Required: true}}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Status           fatal")
	require.Contains(t, out.String(), "Startup gate      fatal (1 required failed)")
	require.Contains(t, out.String(), "Configured servers 0")
	require.Contains(t, out.String(), "Total entries     1")
	require.Contains(t, out.String(), "Required entries  1")
	require.Contains(t, out.String(), "Optional entries  0")
	require.Contains(t, out.String(), "Invalid entries   1")
	require.Contains(t, out.String(), "missing")
	require.Contains(t, out.String(), "missing_command")
	require.Contains(t, out.String(), "Invalid MCP servers")
	require.Contains(t, out.String(), "- missing: missing command")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"list", "--json"}))
	var report mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "fatal", report.Status)
	require.Equal(t, "fatal", report.Startup.Status)
	require.Equal(t, 1, report.Startup.RequiredFailedCount)
	require.Equal(t, 1, report.TotalConfigured)
	require.Equal(t, 1, report.RequiredCount)
	require.Equal(t, 0, report.OptionalCount)
	require.Equal(t, 1, report.InvalidCount)
	require.Equal(t, 1, report.MCPValidation.TotalConfigured)
	require.Equal(t, 1, report.MCPValidation.RequiredCount)
	require.Equal(t, 0, report.MCPValidation.OptionalCount)
	require.Equal(t, 1, report.MCPValidation.InvalidCount)
	require.Equal(t, "missing", report.MCPValidation.InvalidServers[0].Name)
	require.Equal(t, "missing_command", report.MCPValidation.InvalidServers[0].Kind)
	require.Equal(t, report.MCPValidation.InvalidServers, report.InvalidServers)
}

func TestMCPListReportsFatalRequiredStartupFailures(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-required-mcp")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{
			"optional": {Command: filepath.Join(t.TempDir(), "missing-optional-mcp")},
			"required": {Command: missingPath, Required: true},
		}},
		Out: &out,
		Err: io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	require.Contains(t, out.String(), "Status           fatal")
	require.Contains(t, out.String(), "Startup gate      fatal (1 required failed)")
	require.Contains(t, out.String(), "Failed required MCP servers")
	require.Contains(t, out.String(), "required: command_not_found | phase spawn_connect")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"list", "--json"}))
	var report mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "fatal", report.Status)
	require.Equal(t, "fatal", report.Startup.Status)
	require.Equal(t, 2, report.Startup.Total)
	require.Equal(t, 1, report.Startup.RequiredFailedCount)
	require.Equal(t, 1, report.Startup.OptionalFailedCount)
	require.Len(t, report.Startup.FailedRequired, 1)
	require.Equal(t, "required", report.Startup.FailedRequired[0].Name)
	require.Equal(t, "command_not_found", report.Startup.FailedRequired[0].Status)
	require.Equal(t, "spawn_connect", report.Startup.FailedRequired[0].Phase)
	require.True(t, report.Startup.FailedRequired[0].Recoverable)
}

func TestMCPRemoteActionErrorsAreStructured(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.ErrorContains(t, app.MCP(context.Background(), []string{"tools", "demo", "--json"}), "no_servers_configured")
	var report mcpRemoteActionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "mcp", report.Kind)
	require.Equal(t, "tools", report.Action)
	require.False(t, report.OK)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "no_servers_configured", report.ErrorKind)
	require.Equal(t, "tools demo", report.RequestedAction)
	require.Contains(t, report.Hint, "codog mcp add")
	require.Equal(t, "codog mcp tools [SERVER]", report.Usage.DirectCLI)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"tools", "demo"}), "no_servers_configured")
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Action           tools")
	require.Contains(t, out.String(), "Error            no_servers_configured")
	require.Contains(t, out.String(), "Usage            codog mcp tools [SERVER]")
	out.Reset()

	app.Config.MCPServers = map[string]config.MCPServerConfig{"configured": {Command: "demo-server"}}
	require.ErrorContains(t, app.MCP(context.Background(), []string{"resources", "missing", "--json"}), "server_not_found")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "resources", report.Action)
	require.Equal(t, "server_not_found", report.ErrorKind)
	require.Equal(t, "missing", report.ServerName)
	require.Equal(t, []string{"configured"}, report.AvailableServers)
	require.Equal(t, "codog mcp resources [SERVER]", report.Usage.DirectCLI)
	out.Reset()

	for _, tc := range []struct {
		name       string
		args       []string
		action     string
		unexpected []string
		usage      string
	}{
		{
			name:       "tools",
			args:       []string{"tools", "configured", "extra", "--json"},
			action:     "tools",
			unexpected: []string{"extra"},
			usage:      "codog mcp tools [SERVER]",
		},
		{
			name:       "call",
			args:       []string{"call", "configured", "echo", "{}", "extra", "--json"},
			action:     "call",
			unexpected: []string{"extra"},
			usage:      "codog mcp call SERVER TOOL JSON",
		},
		{
			name:       "read",
			args:       []string{"read", "configured", "file:///tmp/demo", "extra", "--json"},
			action:     "read",
			unexpected: []string{"extra"},
			usage:      "codog mcp read SERVER URI",
		},
		{
			name:       "prompt",
			args:       []string{"prompt", "configured", "review", "{}", "extra", "--json"},
			action:     "prompt",
			unexpected: []string{"extra"},
			usage:      "codog mcp prompt SERVER NAME [JSON]",
		},
	} {
		t.Run("unexpected extra args "+tc.name, func(t *testing.T) {
			require.ErrorContains(t, app.MCP(context.Background(), tc.args), "unexpected_argument")
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			require.Equal(t, tc.action, report.Action)
			require.Equal(t, "unexpected_argument", report.ErrorKind)
			require.Equal(t, tc.unexpected, report.UnexpectedArguments)
			require.Equal(t, strings.Join(tc.unexpected, " "), report.Argument)
			require.Equal(t, tc.usage, report.Usage.DirectCLI)
			out.Reset()
		})
	}

	require.ErrorContains(t, app.MCP(context.Background(), []string{"call", "configured", "--json"}), "missing_argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "call", report.Action)
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "tool", report.Argument)
	require.Equal(t, "codog mcp call SERVER TOOL JSON", report.Usage.DirectCLI)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"call", "configured", "echo", "--json"}), "missing_argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "json", report.Argument)
	require.Contains(t, report.Message, "tool input JSON")
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"call", "configured", "echo", "{bad", "--json"}), "invalid_json")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "call", report.Action)
	require.Equal(t, "invalid_json", report.ErrorKind)
	require.Equal(t, "json", report.Argument)
	require.Contains(t, report.Message, "JSON object")
	require.Contains(t, report.Hint, "codog mcp call SERVER TOOL JSON")
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"prompt", "configured", "review", "[]", "--json"}), "invalid_json")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prompt", report.Action)
	require.Equal(t, "invalid_json", report.ErrorKind)
	require.Equal(t, "json", report.Argument)
	require.Contains(t, report.Message, "JSON object")
	require.Contains(t, report.Hint, "codog mcp prompt SERVER NAME [JSON]")
}

func TestMCPHelpCommand(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"help", "--json"}))
	var report mcpUsageReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "mcp", report.Kind)
	require.Equal(t, "help", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.OK)
	require.Nil(t, report.ErrorKind)
	require.Contains(t, report.Usage.SlashCommand, "serve")
	require.Contains(t, report.Usage.SlashCommand, "self")
	require.Contains(t, report.Usage.SlashCommand, "add NAME COMMAND [ARG...]")
	require.Contains(t, report.Usage.SlashCommand, "add NAME --url URL")
	require.Contains(t, report.Usage.SlashCommand, "remove SERVER")
	require.Contains(t, report.Usage.SlashCommand, "tools [SERVER]")
	require.Contains(t, report.Usage.SlashCommand, "call SERVER TOOL JSON")
	require.Contains(t, report.Usage.SlashCommand, "resources [SERVER]")
	require.Contains(t, report.Usage.SlashCommand, "read SERVER URI")
	require.Contains(t, report.Usage.SlashCommand, "prompts [SERVER]")
	require.Contains(t, report.Usage.SlashCommand, "prompt SERVER NAME [JSON]")
	require.Contains(t, report.Usage.DirectCLI, "serve")
	require.Contains(t, report.Usage.DirectCLI, "self")
	require.Contains(t, report.Usage.DirectCLI, "add NAME COMMAND [ARG...]")
	require.Contains(t, report.Usage.DirectCLI, "add NAME --url URL")
	require.Contains(t, report.Usage.DirectCLI, "remove SERVER")
	require.Contains(t, report.Usage.DirectCLI, "tools [SERVER]")
	require.Contains(t, report.Usage.DirectCLI, "call SERVER TOOL JSON")
	require.Contains(t, report.Usage.DirectCLI, "resources [SERVER]")
	require.Contains(t, report.Usage.DirectCLI, "read SERVER URI")
	require.Contains(t, report.Usage.DirectCLI, "prompts [SERVER]")
	require.Contains(t, report.Usage.DirectCLI, "prompt SERVER NAME [JSON]")
	require.Contains(t, report.Usage.Sources, ".codog.json")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "--help", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "error", report.Status)
	require.False(t, report.OK)
	require.NotNil(t, report.ErrorKind)
	require.Equal(t, "unknown_mcp_action", *report.ErrorKind)
	require.NotNil(t, report.Unexpected)
	require.Equal(t, "show", *report.Unexpected)
	require.NotNil(t, report.Hint)
	require.Contains(t, *report.Hint, "add NAME COMMAND [ARG...]")
	require.Contains(t, *report.Hint, "add NAME --url URL")
	require.Contains(t, *report.Hint, "remove SERVER")
	require.Contains(t, *report.Hint, "call SERVER TOOL JSON")
	require.Contains(t, *report.Hint, "read SERVER URI")
	require.Contains(t, *report.Hint, "prompt SERVER NAME [JSON]")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp help", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "add NAME COMMAND [ARG...]")
	require.Contains(t, out.String(), "add NAME --url URL")
	require.Contains(t, out.String(), "remove SERVER")
	require.Contains(t, out.String(), "tools [SERVER]")
	require.Contains(t, out.String(), "call SERVER TOOL JSON")
	require.Contains(t, out.String(), "read SERVER URI")
	require.Contains(t, out.String(), "prompt SERVER NAME [JSON]")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"info", "missing", "--json"}))
	var showReport mcpShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "mcp", showReport.Kind)
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "error", showReport.Status)
	require.Equal(t, "server_not_found", showReport.ErrorKind)
	require.False(t, showReport.Found)
	require.Equal(t, "missing", showReport.ServerName)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"describe", "missing", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "server_not_found", showReport.ErrorKind)
	require.Equal(t, "missing", showReport.ServerName)
}

func TestMCPDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report mcpListReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "mcp", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, 0, report.ServerCount)
	require.Equal(t, 0, report.ConfiguredServers)
	require.Equal(t, 0, report.TotalConfigured)
	require.Equal(t, 0, report.ValidCount)
	require.Equal(t, 0, report.InvalidCount)
	require.NotNil(t, report.ConfigLoadError)
	require.Contains(t, *report.ConfigLoadError, "broken.json")
	require.Contains(t, *report.ConfigLoadError, "unexpected end of JSON input")
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/mcp"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp", "ls"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "list", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "mcp"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "MCP")
	require.Contains(t, out, "Config load")
	require.Contains(t, out, "broken.json")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp", "show", "demo"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var showReport mcpShowReport
	require.NoError(t, json.Unmarshal([]byte(out), &showReport))
	require.Equal(t, "mcp", showReport.Kind)
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "degraded", showReport.Status)
	require.Equal(t, "demo", showReport.ServerName)
	require.False(t, showReport.Found)
	require.NotNil(t, showReport.ConfigLoadError)
	require.Contains(t, *showReport.ConfigLoadError, "broken.json")
	require.Equal(t, "config_load_failed", showReport.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp", "describe", "demo"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "degraded", showReport.Status)
	require.Equal(t, "demo", showReport.ServerName)
	require.False(t, showReport.Found)
	require.NotNil(t, showReport.ConfigLoadError)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp", "help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var helpReport mcpUsageReport
	require.NoError(t, json.Unmarshal([]byte(out), &helpReport))
	require.Equal(t, "mcp", helpReport.Kind)
	require.Equal(t, "help", helpReport.Action)
	require.Equal(t, "ok", helpReport.Status)
	require.True(t, helpReport.OK)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mcp", "list", "extra"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport mcpUnsupportedActionReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "unsupported_action", errorReport.ErrorKind)
	require.Equal(t, "list extra", errorReport.RequestedAction)
	require.NotContains(t, out, "config_load_error")
}

func TestPluginsDegradeOnMalformedConfigFile(t *testing.T) {
	workspace := t.TempDir()
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "plugins"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report pluginsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "plugin", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, 1, report.Summary.Total)
	require.Equal(t, 1, report.Summary.Enabled)
	require.Equal(t, 0, report.Summary.Disabled)
	require.Len(t, report.Plugins, 1)
	require.Equal(t, "demo", report.Plugins[0].ID)
	require.NotNil(t, report.ConfigLoadError)
	require.Contains(t, *report.ConfigLoadError, "broken.json")
	require.Contains(t, *report.ConfigLoadError, "unexpected end of JSON input")
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/plugins"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "marketplace", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "plugins"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Plugins")
	require.Contains(t, out, "Config load")
	require.Contains(t, out, "broken.json")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "plugins", "list", "extra"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "unexpected_extra_args", errorReport.ErrorKind)
	require.NotContains(t, out, "config_load_error")
}

func TestRegisterMCPToolsContinuesAfterBrokenServer(t *testing.T) {
	workspace := t.TempDir()
	good := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:     []string{"CODOG_AGENT_MCP_HELPER=1"},
	}
	bad := config.MCPServerConfig{Command: filepath.Join(t.TempDir(), "missing-mcp")}
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{
			"bad":  bad,
			"good": good,
		}},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Err:       &errOut,
	}

	require.NoError(t, app.RegisterMCPTools(context.Background()))
	require.True(t, app.mcpToolsLoaded)
	require.True(t, app.Tools.Has(tools.NewMCPToolName("good", "echo")))
	require.False(t, app.Tools.Has(tools.NewMCPToolName("bad", "echo")))
	require.Contains(t, errOut.String(), "MCP server unavailable: bad:")

	app = &App{
		Config:    config.Config{MCPServers: map[string]config.MCPServerConfig{"bad": bad}},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Err:       io.Discard,
	}
	err := app.RegisterMCPTools(context.Background())
	require.ErrorContains(t, err, "no MCP tools registered")
	require.False(t, app.mcpToolsLoaded)
}

func TestMCPConfigCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{}},
		Out:    &out,
		Err:    io.Discard,
	}

	err := app.MCP(context.Background(), []string{"add", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var addNameError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &addNameError))
	require.Equal(t, "mcp", addNameError.Kind)
	require.Equal(t, "add", addNameError.Action)
	require.Equal(t, "missing_argument", addNameError.ErrorKind)
	require.Equal(t, "server_name", addNameError.Argument)
	out.Reset()

	err = app.MCP(context.Background(), []string{"add", "demo", "--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var addCommandError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &addCommandError))
	require.Equal(t, "mcp", addCommandError.Kind)
	require.Equal(t, "add", addCommandError.Action)
	require.Equal(t, "missing_argument", addCommandError.ErrorKind)
	require.Equal(t, "command_or_url", addCommandError.Argument)
	out.Reset()

	err = app.MCP(context.Background(), []string{"remove", "--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var removeError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &removeError))
	require.Equal(t, "mcp", removeError.Kind)
	require.Equal(t, "remove", removeError.Action)
	require.Equal(t, "missing_argument", removeError.ErrorKind)
	require.Equal(t, "server_name", removeError.Argument)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"add", "demo", "demo-server", "--env", "A=B", "--env=C=D", "--tool-call-timeout-ms", "15000", "--required", "arg1", "arg2"}))
	require.Contains(t, out.String(), `"action": "add"`)
	require.Contains(t, out.String(), `"name": "demo"`)
	require.Contains(t, out.String(), `"required": true`)
	require.Contains(t, out.String(), `"tool_call_timeout_ms": 15000`)
	require.Equal(t, config.MCPServerConfig{Command: "demo-server", Args: []string{"arg1", "arg2"}, Env: []string{"A=B", "C=D"}, ToolCallTimeoutMS: 15000, Required: true}, app.Config.MCPServers["demo"])
	configData, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `"mcp_servers"`)
	require.Contains(t, string(configData), `"demo-server"`)
	require.Contains(t, string(configData), `"tool_call_timeout_ms": 15000`)
	require.Contains(t, string(configData), `"required": true`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "demo", "--json"}))
	require.Contains(t, out.String(), `"action": "show"`)
	require.Contains(t, out.String(), `"command": "demo-server"`)
	require.Contains(t, out.String(), `"required": true`)
	require.Contains(t, out.String(), `"signature": "stdio:[demo-server|arg1|arg2]"`)
	require.Contains(t, out.String(), `"config_hash": "`)
	require.Contains(t, out.String(), `"args_count": 2`)
	require.Contains(t, out.String(), `"tool_call_timeout_ms": 15000`)
	require.Contains(t, out.String(), `"env_keys": [`)
	require.NotContains(t, out.String(), `"A=B"`)
	require.NotContains(t, out.String(), `"C=D"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"info", "demo", "--json"}))
	var aliasShow mcpShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aliasShow))
	require.Equal(t, "mcp", aliasShow.Kind)
	require.Equal(t, "show", aliasShow.Action)
	require.Equal(t, "ok", aliasShow.Status)
	require.True(t, aliasShow.Found)
	require.NotNil(t, aliasShow.Server)
	require.Equal(t, "demo", aliasShow.Server.Name)
	require.True(t, aliasShow.Server.Required)
	require.Equal(t, 15000, aliasShow.Server.Details.ToolCallTimeoutMS)
	require.Equal(t, "stdio:[demo-server|arg1|arg2]", aliasShow.Signature)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"describe", "demo", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &aliasShow))
	require.Equal(t, "show", aliasShow.Action)
	require.True(t, aliasShow.Found)
	require.Equal(t, "demo", aliasShow.Server.Name)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "missing", "--json"}))
	var missingShow mcpShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missingShow))
	require.Equal(t, "mcp", missingShow.Kind)
	require.Equal(t, "show", missingShow.Action)
	require.Equal(t, "error", missingShow.Status)
	require.Equal(t, "server_not_found", missingShow.ErrorKind)
	require.False(t, missingShow.Found)
	require.Equal(t, "missing", missingShow.ServerName)
	require.Contains(t, missingShow.AvailableServers, "demo")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"remove", "demo"}))
	require.Contains(t, out.String(), `"removed": true`)
	_, ok := app.Config.MCPServers["demo"]
	require.False(t, ok)
	configData, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(configData), `"demo"`)

	err = app.MCP(context.Background(), []string{"add", "bad.name", "cmd"})
	require.ErrorContains(t, err, "invalid MCP server name")
}

func TestMCPShowRedactsSensitiveTransportArguments(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{
			"stdio-secret": {
				Command: "uvx",
				Args: []string{
					"mcp-server",
					"--api-key",
					"sk-stdio-secret",
					"--tenant=public",
					"--access-token=stdio-access-secret",
				},
			},
			"http-secret": {
				URL:           "https://user:pass@example.test/mcp?token=query-secret&mode=ok",
				Headers:       map[string]string{"Authorization": "Bearer header-secret"},
				HeadersHelper: "./headers-helper --api-key helper-secret",
			},
		}},
		Out: &out,
		Err: io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"show", "stdio-secret", "--json"}))
	stdioJSON := out.String()
	require.Contains(t, stdioJSON, `"args_summary"`)
	require.Contains(t, stdioJSON, `"--tenant=public"`)
	require.Contains(t, stdioJSON, `"--api-key"`)
	require.Contains(t, stdioJSON, `"[redacted:`)
	require.NotContains(t, stdioJSON, "sk-stdio-secret")
	require.NotContains(t, stdioJSON, "stdio-access-secret")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "stdio-secret"}))
	stdioText := out.String()
	require.Contains(t, stdioText, "Signature")
	require.NotContains(t, stdioText, "sk-stdio-secret")
	require.NotContains(t, stdioText, "stdio-access-secret")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "http-secret", "--json"}))
	httpJSON := out.String()
	require.Contains(t, httpJSON, "token=%5Bredacted%5D")
	require.Contains(t, httpJSON, "https://%5Bredacted%5D@example.test")
	require.NotContains(t, httpJSON, "pass")
	require.NotContains(t, httpJSON, "query-secret")
	require.NotContains(t, httpJSON, "header-secret")
	require.NotContains(t, httpJSON, "helper-secret")
	require.NotContains(t, httpJSON, "./headers-helper")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "http-secret"}))
	httpText := out.String()
	require.NotContains(t, httpText, "pass")
	require.NotContains(t, httpText, "query-secret")
	require.NotContains(t, httpText, "header-secret")
	require.NotContains(t, httpText, "helper-secret")
	require.NotContains(t, httpText, "./headers-helper")
}

func TestMCPServeCommand(t *testing.T) {
	workspace := t.TempDir()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{PermissionMode: "workspace-write"},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		In:        input,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"serve"}))
	require.Contains(t, out.String(), `"tools"`)
	require.Contains(t, out.String(), `"read_file"`)
}

func TestMCPSelfCommand(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{PermissionMode: "workspace-write"},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"self", "--json"}))
	var report mcpSelfReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "mcp", report.Kind)
	require.Equal(t, "self", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Contains(t, report.Tools, "read_file")
	require.Contains(t, report.Resources, "codog://workspace")
	require.Contains(t, report.Prompts, "review_changes")
	require.Greater(t, report.ToolCount, 0)
	require.Greater(t, report.ResourceCount, 0)
	require.Greater(t, report.PromptCount, 0)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"self"}))
	require.Contains(t, out.String(), "MCP Self")
	require.Contains(t, out.String(), "Resources")
}

func TestSlashAliasesForExistingSurfaces(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	for _, command := range []string{"/ide", "/agents", "/tasks", "/bashes", "/background", "/plugin", "/plugins", "/marketplace", "/providers"} {
		out.Reset()
		errOut.Reset()
		require.True(t, app.handleSlash(context.Background(), command, sess), command)
		require.NotEmpty(t, strings.TrimSpace(out.String()), command)
		require.Empty(t, errOut.String(), command)
	}
}

func TestIDECommandReportsAndClearsEditorState(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	statePath := filepath.Join(configHome, "bridge", "editor-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	state := bridge.EditorState{
		Identity: &bridge.EditorIdentity{
			Editor:    "VS Code",
			Version:   "1.0",
			Workspace: workspace,
			Trusted:   true,
			TrustedAt: time.Now().UTC(),
		},
		OpenFile: &bridge.EditorOpenFile{Path: "main.go", OpenedAt: time.Now().UTC()},
		Selection: &bridge.EditorSelection{
			Path:      "main.go",
			StartLine: 3,
			EndLine:   4,
			Text:      "func main() {}",
			UpdatedAt: time.Now().UTC(),
		},
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0o644))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				EditorBridgeSocket: "codog.sock",
				EditorBridgeToken:  "secret",
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.connectActiveIDE())
	require.NotNil(t, app.ActiveIDE)
	prompt := app.systemPrompt()
	require.Contains(t, prompt, "<active_editor>")
	require.Contains(t, prompt, "Editor: VS Code 1.0")
	require.Contains(t, prompt, "Open file: main.go")
	require.Contains(t, prompt, "func main() {}")

	require.NoError(t, app.IDE([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "ide"`)
	require.Contains(t, out.String(), `"editor": "VS Code"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	require.Contains(t, out.String(), `"path": "main.go"`)

	out.Reset()
	require.NoError(t, app.IDE([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"cleared": true`)
	require.NoFileExists(t, statePath)

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/ide", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "IDE Bridge")
	require.Contains(t, out.String(), "Trusted editor   none")
	out.Reset()

	require.NoError(t, app.Bridge([]string{"status", "--json"}))
	var bridgeStatus ideReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeStatus))
	require.Equal(t, "ide", bridgeStatus.Kind)
	require.Equal(t, "status", bridgeStatus.Action)
	require.Equal(t, "codog bridge serve", bridgeStatus.Bridge.Command)
	out.Reset()

	require.NoError(t, app.Bridge([]string{"capabilities", "--json"}))
	var bridgeCapabilities bridgeCapabilitiesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeCapabilities))
	require.Equal(t, "bridge_capabilities", bridgeCapabilities.Kind)
	require.Equal(t, "capabilities", bridgeCapabilities.Action)
	require.Equal(t, "codog", bridgeCapabilities.Name)
	require.Equal(t, bridgeCapabilities.Count, len(bridgeCapabilities.Capabilities))
	require.Contains(t, bridgeCapabilities.Capabilities, "sessions/list")
	require.Contains(t, bridgeCapabilities.Capabilities, "mcp/resources")
	out.Reset()

	require.NoError(t, app.Bridge([]string{"caps", "--output-format", "text"}))
	require.Contains(t, out.String(), "Bridge Capabilities")
	require.Contains(t, out.String(), "Capability       sessions/list")
	out.Reset()

	require.ErrorContains(t, app.Bridge([]string{"serv", "--json"}), "unsupported_bridge_action")
	var bridgeError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeError))
	require.Equal(t, "bridge", bridgeError.Kind)
	require.Equal(t, "serv", bridgeError.Action)
	require.Equal(t, "unsupported_bridge_action", bridgeError.ErrorKind)
	require.Contains(t, bridgeError.Hint, "Did you mean `codog bridge serve`?")
	out.Reset()

	require.ErrorContains(t, app.Bridge([]string{"serve", "extra", "--json"}), "unexpected_argument")
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeError))
	require.Equal(t, "bridge", bridgeError.Kind)
	require.Equal(t, "serve", bridgeError.Action)
	require.Equal(t, "unexpected_argument", bridgeError.ErrorKind)
	require.Equal(t, "extra", bridgeError.Argument)
	require.Contains(t, bridgeError.Hint, "codog bridge serve")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bridge status --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeStatus))
	require.Equal(t, "ide", bridgeStatus.Kind)
	require.Equal(t, "status", bridgeStatus.Action)
	out.Reset()

	require.NoError(t, app.runResumedBridgeSlash("/bridge", []string{"status", "--json"}, config.FlagOverrides{Resume: "bridge-session"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeStatus))
	require.Equal(t, "ide", bridgeStatus.Kind)
	require.Equal(t, "status", bridgeStatus.Action)
	out.Reset()

	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	require.NoError(t, os.WriteFile(statePath, data, 0o644))
	require.NoError(t, app.runResumedBridgeSlash("/bridge", []string{"reset", "--json"}, config.FlagOverrides{Resume: "bridge-session"}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeStatus))
	require.Equal(t, "ide", bridgeStatus.Kind)
	require.Equal(t, "clear", bridgeStatus.Action)
	require.True(t, bridgeStatus.Cleared)
	require.NoFileExists(t, statePath)
	out.Reset()

	executableShim := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(executableShim, []byte("#!/bin/sh\necho bridge-shim \"$@\"\n"), 0o755))
	app.Executable = executableShim
	require.NoError(t, app.runResumedBridgeSlash("/remote-control", []string{"serve", "--json"}, config.FlagOverrides{Resume: "bridge-session"}, "json"))
	var bridgeTask backgroundCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &bridgeTask))
	require.Equal(t, "background", bridgeTask.Kind)
	require.Equal(t, "run", bridgeTask.Action)
	require.Equal(t, "ok", bridgeTask.Status)
	require.Equal(t, "bridge-session", bridgeTask.SessionID)
	require.NotEmpty(t, bridgeTask.TaskID)
	require.NotNil(t, bridgeTask.Task)
	require.Equal(t, "bridge", bridgeTask.Task.Kind)
	require.Equal(t, "bridge-session", bridgeTask.Task.SessionID)
	require.Contains(t, bridgeTask.Task.Command, "bridge serve")
	require.NotContains(t, bridgeTask.Task.Command, "--json")
	require.NotContains(t, bridgeTask.Task.Command, "--output-format")
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "bridge_kick"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bridge-kick poll 404", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bridge Kick")
	require.Contains(t, out.String(), "Status           ok")
	require.Contains(t, out.String(), "Recorded         poll 404")
	require.Contains(t, out.String(), "Fault severity   warn")
	require.Contains(t, out.String(), "Fault category   polling")
	require.Contains(t, out.String(), "Remediation")
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"action": "poll"`)
	require.Contains(t, out.String(), `"category": "polling"`)
	require.Contains(t, out.String(), `"severity": "warn"`)
	require.Contains(t, out.String(), `"recoverable": true`)
	require.Contains(t, out.String(), `"404"`)
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"cleared": true`)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestConnectActiveIDERequiresTrustedEditor(t *testing.T) {
	configHome := t.TempDir()
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
	}

	err := app.connectActiveIDE()
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires one trusted local editor bridge")
	var connectionErr ideConnectionError
	require.ErrorAs(t, err, &connectionErr)
	require.Equal(t, "ide_bridge_unavailable", connectionErr.Kind)
	report := buildCLIErrorReport(err)
	require.Equal(t, "ide_bridge_unavailable", report.ErrorKind)
	require.Equal(t, "ide", report.Error.Kind)
	require.Equal(t, "connect_editor_bridge", report.Error.Operation)
	require.True(t, report.Error.Retryable)

	statePath := filepath.Join(configHome, "bridge", "editor-state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	data, err := json.Marshal(bridge.EditorState{Identity: &bridge.EditorIdentity{
		Editor:    "VS Code",
		Workspace: t.TempDir(),
		Trusted:   true,
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0o644))
	err = app.connectActiveIDE()
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match active workspace")
	require.ErrorAs(t, err, &connectionErr)
	require.Equal(t, "ide_workspace_mismatch", connectionErr.Kind)
	report = buildCLIErrorReport(err)
	require.Equal(t, "ide_workspace_mismatch", report.ErrorKind)
	require.Equal(t, app.Workspace, report.ExpectedWorkspace)
	require.Equal(t, connectionErr.ActualWorkspace, report.ActualWorkspace)
}

func TestBriefCommandUsesToolPayloadAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("brief attachment\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Brief([]string{"--json"}))
	var status briefStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "brief", status.Kind)
	require.Equal(t, "status", status.Action)
	require.Equal(t, "ready", status.Status)
	require.Equal(t, workspace, status.Workspace)
	require.Equal(t, "codog brief MESSAGE", status.NextCommand)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--cwd", workspace, "--output-format", "json", "brief"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliStatus briefStatusReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliStatus))
	require.Equal(t, "brief", cliStatus.Kind)
	require.Equal(t, "status", cliStatus.Action)
	require.Equal(t, "ready", cliStatus.Status)
	expectedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	actualWorkspace, err := filepath.EvalSymlinks(cliStatus.Workspace)
	require.NoError(t, err)
	require.Equal(t, expectedWorkspace, actualWorkspace)

	require.NoError(t, app.Brief([]string{"Build", "passed", "--status", "proactive", "--attach", "notes.md", "--json"}))
	require.Contains(t, out.String(), `"message": "Build passed"`)
	require.Contains(t, out.String(), `"status": "proactive"`)
	require.Contains(t, out.String(), `"is_image": false`)

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/brief Ready for review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Ready for review")
	require.Contains(t, out.String(), "status: normal")
	require.Empty(t, errOut.String())
}

func TestAgentMCPHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_AGENT_MCP_HELPER") != "1" {
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
			writeAgentMCP(id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "test", "version": "0.0.0"},
			})
		case "tools/list":
			writeAgentMCP(id, map[string]any{"tools": []map[string]any{{
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
			writeAgentMCP(id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi"}}})
		case "resources/list":
			writeAgentMCP(id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			writeAgentMCP(id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			writeAgentMCP(id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			writeAgentMCP(id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
				"arguments": []map[string]any{{
					"name":     "topic",
					"required": true,
				}},
			}}})
		case "prompts/get":
			writeAgentMCP(id, map[string]any{"messages": []map[string]any{{
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

func writeAgentMCP(id any, result map[string]any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(payload)
	fmt.Println(string(data))
}

func TestPromptWritesCompletedWorkerState(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var gotHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	var gotEndHook struct {
		Event  string `json:"event"`
		Input  string `json:"input"`
		Reason string `json:"reason"`
	}
	var hookDecodeErr error
	var endHookDecodeErr error
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookDecodeErr = json.NewDecoder(r.Body).Decode(&gotHook)
		w.WriteHeader(http.StatusOK)
	}))
	defer hookServer.Close()
	endHookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endHookDecodeErr = json.NewDecoder(r.Body).Decode(&gotEndHook)
		w.WriteHeader(http.StatusOK)
	}))
	defer endHookServer.Close()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
			EnabledSkills:       nil,
			Hooks: config.HookConfig{
				SessionStartCommands: []config.HookCommand{{Type: "http", URL: hookServer.URL}},
				SessionEndCommands:   []config.HookCommand{{Type: "http", URL: endHookServer.URL}},
			},
			Future:    config.FutureConfig{},
			AuthToken: "",
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "hello", config.FlagOverrides{SessionID: "prompt-session"}))
	loaded, err := workerstate.Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "prompt", loaded.Mode)
	require.Equal(t, "completed", loaded.Status)
	require.Equal(t, "prompt-session", loaded.SessionID)
	require.Contains(t, out.String(), "done")
	require.NoError(t, hookDecodeErr)
	require.Equal(t, "session_start", gotHook.Event)
	require.Contains(t, gotHook.Input, `"source":"resume"`)
	require.Contains(t, gotHook.Input, `"title":"hello"`)
	require.Contains(t, gotHook.Input, `"purpose":"prompt"`)
	require.NoError(t, endHookDecodeErr)
	require.Equal(t, "session_end", gotEndHook.Event)
	require.Equal(t, "completed", gotEndHook.Reason)
	require.Contains(t, gotEndHook.Input, `"session_id":"prompt-session"`)
	opened, err := app.Sessions.Open("prompt-session")
	require.NoError(t, err)
	require.Equal(t, "hello", opened.Identity.Title)
	require.Equal(t, "prompt", opened.Identity.Purpose)
	require.Empty(t, opened.Identity.Placeholders)
	history, err := app.Sessions.PromptHistory("prompt-session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "hello", history[0].Text)
}

func TestPromptOutputFormats(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.PromptWithOutput(context.Background(), "json prompt", config.FlagOverrides{SessionID: "json-session"}, "json"))
	var report promptReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "run", report.Action)
	require.Equal(t, "completed", report.Status)
	require.Equal(t, "json-session", report.SessionID)
	require.Equal(t, "done", report.Response)
	require.Equal(t, "actual", report.Usage.Source)
	require.Equal(t, 10, report.Usage.InputTokens)
	require.Equal(t, 5, report.Usage.OutputTokens)
	require.Greater(t, report.CostUSD, 0.0)
	out.Reset()

	require.NoError(t, app.PromptWithOutput(context.Background(), "ephemeral prompt", config.FlagOverrides{SessionID: "ephemeral-session", NoSessionPersistence: true}, "json"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "completed", report.Status)
	require.Equal(t, "ephemeral-session", report.SessionID)
	require.Equal(t, "done", report.Response)
	exists, err := app.Sessions.Exists("ephemeral-session")
	require.NoError(t, err)
	require.False(t, exists)
	out.Reset()

	require.NoError(t, app.PromptWithOutput(context.Background(), "stream prompt", config.FlagOverrides{SessionID: "stream-session"}, "stream-json"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.NotContains(t, out.String(), `"type":"assistant_delta"`)
	var firstEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &firstEvent))
	require.Equal(t, "start", firstEvent["type"])
	var resultEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &resultEvent))
	require.Equal(t, "result", resultEvent["type"])
	out.Reset()

	require.NoError(t, app.promptWithOutput(context.Background(), "compact text", config.FlagOverrides{SessionID: "compact-text-session"}, "text", true))
	require.Equal(t, "done\n", out.String())
	require.NotContains(t, out.String(), `"response"`)
	require.NotContains(t, out.String(), `"tool_uses"`)
	out.Reset()

	require.NoError(t, app.promptWithOutput(context.Background(), "compact json", config.FlagOverrides{SessionID: "compact-json-session"}, "json", true))
	var compactReport promptCompactReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &compactReport))
	require.True(t, compactReport.Compact)
	require.Equal(t, "done", compactReport.Message)
	require.Equal(t, "mock", compactReport.Model)
	require.Equal(t, 10, compactReport.Usage.InputTokens)
	require.Equal(t, 5, compactReport.Usage.OutputTokens)
}

func TestPromptWithSessionNamePreservesExplicitTitle(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.PromptWithOutput(context.Background(), "input title should not win", config.FlagOverrides{SessionID: "sdk-session", SessionName: "SDK Display"}, "json"))
	opened, err := app.Sessions.Open("sdk-session")
	require.NoError(t, err)
	require.Equal(t, "SDK Display", opened.Identity.Title)
	require.Equal(t, "prompt", opened.Identity.Purpose)
	history, err := app.Sessions.PromptHistory("sdk-session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "input title should not win", history[0].Text)
}

func TestPromptMaxBudgetUSDPersistsPartialResult(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "over budget response"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	limit := 0.00001

	err := app.PromptWithOutput(context.Background(), "cost capped", config.FlagOverrides{SessionID: "budget-session", MaxBudgetUSD: &limit}, "text")
	require.Error(t, err)
	var budgetErr runloop.BudgetExceededError
	require.ErrorAs(t, err, &budgetErr)
	require.Contains(t, out.String(), "over budget response")
	opened, openErr := app.Sessions.Open("budget-session")
	require.NoError(t, openErr)
	require.Len(t, opened.Messages, 2)
	require.Equal(t, "user", opened.Messages[0].Role)
	require.Equal(t, "assistant", opened.Messages[1].Role)
	entries, usageErr := app.Sessions.Usage("budget-session")
	require.NoError(t, usageErr)
	require.Len(t, entries, 1)
	require.Equal(t, 10, entries[0].Usage.InputTokens)
	require.Equal(t, 5, entries[0].Usage.OutputTokens)
}

func TestPromptJSONSchemaOutputValidation(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Turns: []mockanthropic.Turn{
		{Text: `{"name":"codog"}`},
		{Text: `{"name":7}`},
		{Text: `{"name":7}`},
	}}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			PermissionRules:     config.PermissionRules{},
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	schema := `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`

	require.NoError(t, app.promptWithOutputOptions(context.Background(), "json prompt", config.FlagOverrides{SessionID: "schema-ok"}, "json", false, turnOptions{JSONSchema: schema}))
	var report promptReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "completed", report.Status)
	require.JSONEq(t, `{"name":"codog"}`, report.Response)
	out.Reset()

	err := app.promptWithOutputOptions(context.Background(), "json prompt", config.FlagOverrides{SessionID: "schema-fail-json"}, "json", false, turnOptions{JSONSchema: schema})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "json_schema_validation_failed", errorReport.ErrorKind)
	require.Equal(t, "$.name", errorReport.Path)
	out.Reset()

	err = app.promptWithOutputOptions(context.Background(), "stream prompt", config.FlagOverrides{SessionID: "schema-fail-stream"}, "stream-json", false, turnOptions{JSONSchema: schema})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.NotContains(t, out.String(), `"type":"assistant_delta"`)
	var lastEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &lastEvent))
	require.Equal(t, "error", lastEvent["type"])
	payload, ok := lastEvent["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "json_schema_validation_failed", payload["error_kind"])
}

func TestBTWUsesForkedSideSession(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "side done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	require.NoError(t, app.Sessions.Append("main-session", anthropic.TextMessage("user", "source context")))
	sess, err := app.Sessions.Open("main-session")
	require.NoError(t, err)
	sourceMessages := len(sess.Messages)

	require.True(t, app.handleSlash(context.Background(), "/btw answer a side question", sess))
	require.Contains(t, out.String(), "side done")
	require.Contains(t, errOut.String(), "btw session:")
	require.Contains(t, errOut.String(), "source session: main-session")
	require.Len(t, sess.Messages, sourceMessages)

	source, err := app.Sessions.Open("main-session")
	require.NoError(t, err)
	require.Len(t, source.Messages, sourceMessages)
	sideID := extractLineValue(errOut.String(), "btw session:")
	require.NotEmpty(t, sideID)
	side, err := app.Sessions.Open(sideID)
	require.NoError(t, err)
	require.Len(t, side.Messages, sourceMessages+2)
	require.Equal(t, "answer a side question", side.Messages[sourceMessages].Content[0].Text)
	require.Contains(t, side.Messages[sourceMessages+1].Content[0].Text, "side done")

	out.Reset()
	errOut.Reset()
	require.NoError(t, app.RunResumedSlash(context.Background(), "/btw", []string{"answer resumed side question"}, config.FlagOverrides{Resume: "main-session"}, "json"))
	var report btwReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "btw", report.Kind)
	require.Equal(t, "run", report.Action)
	require.Equal(t, "completed", report.Status)
	require.Equal(t, "main-session", report.SourceSessionID)
	require.Equal(t, "answer resumed side question", report.Question)
	require.Contains(t, report.Output, "side done")
	require.NotEmpty(t, report.SessionID)
	require.Empty(t, errOut.String())

	resumedSide, err := app.Sessions.Open(report.SessionID)
	require.NoError(t, err)
	require.Len(t, resumedSide.Messages, sourceMessages+2)
	require.Equal(t, "answer resumed side question", resumedSide.Messages[sourceMessages].Content[0].Text)
	require.Contains(t, resumedSide.Messages[sourceMessages+1].Content[0].Text, "side done")
}

func extractLineValue(text string, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func TestPromptExpandsFileReferencesForModelInput(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("note body"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "nested", "guide.txt"), []byte("guide body\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o644))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	originalPrompt := "summarize @notes.md and @docs"
	require.NoError(t, app.Prompt(context.Background(), originalPrompt, config.FlagOverrides{SessionID: "prompt-refs"}))
	loaded, err := app.Sessions.Open("prompt-refs")
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "<codog_file_references>")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "note body")
	require.Contains(t, loaded.Messages[0].Content[0].Text, `<directory path="docs" files="2"`)
	require.Contains(t, loaded.Messages[0].Content[0].Text, `<file path="README.md"`)
	require.Contains(t, loaded.Messages[0].Content[0].Text, `<file path="nested/guide.txt"`)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "guide body")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "binary.bin")
	history, err := app.Sessions.PromptHistory("prompt-refs")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, originalPrompt, history[0].Text)
}

func TestSystemPromptIncludesProjectMemory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Always run focused tests."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "CLAUDE.md"), []byte("Prefer Claude-compatible workflows."), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, "<project_memory>")
	require.Contains(t, prompt, "AGENTS.md")
	require.Contains(t, prompt, "Always run focused tests.")
	require.Contains(t, prompt, ".claude/CLAUDE.md")
	require.Contains(t, prompt, "Prefer Claude-compatible workflows.")
}

func TestSystemPromptRespectsRulesImportConfig(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".cursorrules"), []byte("Cursor-only rule."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".github", "copilot-instructions.md"), []byte("Copilot-only rule."), 0o644))

	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}
	prompt := app.systemPrompt()
	require.Contains(t, prompt, "Cursor-only rule.")
	require.Contains(t, prompt, "Copilot-only rule.")

	none := config.RulesImportConfig{Mode: "none"}
	app.Config.RulesImport = &none
	prompt = app.systemPrompt()
	require.NotContains(t, prompt, "Cursor-only rule.")
	require.NotContains(t, prompt, "Copilot-only rule.")

	copilotOnly := config.RulesImportConfig{Mode: "list", Frameworks: []string{"copilot"}}
	app.Config.RulesImport = &copilotOnly
	prompt = app.systemPrompt()
	require.NotContains(t, prompt, "Cursor-only rule.")
	require.Contains(t, prompt, "Copilot-only rule.")
}

func TestSystemPromptIncludesDateAndGitSnapshot(t *testing.T) {
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("changed\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("untracked\n"), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, "Today's date is ")
	require.Contains(t, prompt, "<git_context>")
	require.Contains(t, prompt, "Current branch: main")
	require.Contains(t, prompt, "Status:")
	require.Contains(t, prompt, "README.md")
	require.Contains(t, prompt, "notes.txt")
	require.Contains(t, prompt, "Recent commits:")
	require.Contains(t, prompt, "chore: base")
}

func TestSystemPromptSupportsOverrideAndAppend(t *testing.T) {
	app := &App{
		Config: config.Config{
			SystemPrompt:       "Custom base.",
			AppendSystemPrompt: "Extra instructions.",
		},
		Workspace: t.TempDir(),
	}

	prompt := app.systemPrompt()
	require.True(t, strings.HasPrefix(prompt, "Custom base."))
	require.Contains(t, prompt, "Extra instructions.")
	require.NotContains(t, prompt, "You are Codog")
}

func TestSystemPromptIncludesSkillFrontmatterMetadata(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	skillDir := filepath.Join(workspace, ".codog", "skills", "review")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: Reviews changed Go files.
allowed-tools: Read, Bash(go test:*)
argument-hint: FILE
paths:
  - internal/**
---
Review body.
`), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome, EnabledSkills: []string{"review"}},
		Workspace: workspace,
	}

	prompt := app.systemPrompt()

	require.Contains(t, prompt, `<skill name="review"`)
	require.Contains(t, prompt, "Description: Reviews changed Go files.")
	require.Contains(t, prompt, "Allowed tools: Read, Bash(go test:*)")
	require.Contains(t, prompt, "Argument hint: FILE")
	require.Contains(t, prompt, "Paths: internal")
	require.Contains(t, prompt, "Review body.")
	require.NotContains(t, prompt, "allowed-tools:")
	require.NotContains(t, prompt, "---")
}

func TestSystemPromptActivatesSkillsMatchingPromptAndFocusPaths(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	internalSkill := filepath.Join(workspace, ".codog", "skills", "internal-review")
	docsSkill := filepath.Join(workspace, ".codog", "skills", "docs-review")
	otherSkill := filepath.Join(workspace, ".codog", "skills", "script-review")
	require.NoError(t, os.MkdirAll(internalSkill, 0o755))
	require.NoError(t, os.MkdirAll(docsSkill, 0o755))
	require.NoError(t, os.MkdirAll(otherSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(internalSkill, "SKILL.md"), []byte(`---
paths:
  - internal/**
---
Internal review body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(docsSkill, "SKILL.md"), []byte(`---
paths:
  - docs/**/*.md
---
Docs review body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(otherSkill, "SKILL.md"), []byte(`---
paths:
  - scripts/**
---
Script review body.
`), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
	}

	prompt := app.systemPromptForInput("inspect @internal/agent.go")
	require.Contains(t, prompt, "Internal review body.")
	require.NotContains(t, prompt, "Docs review body.")
	require.NotContains(t, prompt, "Script review body.")

	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "guide.md"), []byte("guide"), 0o644))
	_, err := focus.Add(workspace, []string{"docs/guide.md"})
	require.NoError(t, err)

	prompt = app.systemPromptForInput("")
	require.Contains(t, prompt, "Docs review body.")
	require.NotContains(t, prompt, "Script review body.")
}

func TestSystemPromptDiscoversNestedSkillsForReferencedPaths(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "app", ".claude", "skills", "local-review"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app", ".claude", "skills", "local-review", "SKILL.md"), []byte(`---
description: Review files in this app subtree.
---
Nested app review body.
`), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
	}

	prompt := app.systemPromptForInput("inspect @src/app/main.go")
	require.Contains(t, prompt, `<skill name="local-review"`)
	require.Contains(t, prompt, "Description: Review files in this app subtree.")
	require.Contains(t, prompt, "Nested app review body.")

	prompt = app.systemPromptForInput("inspect @other/main.go")
	require.NotContains(t, prompt, "Nested app review body.")
}

func TestSystemPromptDiscoversNestedSkillsAfterToolContextPaths(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "app", ".claude", "skills", "local-review"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src", "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app", ".claude", "skills", "local-review", "SKILL.md"), []byte("Tool-discovered skill body."), 0o644))
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
	}

	require.NotContains(t, app.systemPromptForInput("continue"), "Tool-discovered skill body.")

	app.recordToolContextPaths(runloop.ToolCall{Name: "read_file", Input: `{"path":"src/app/main.go"}`})
	require.Contains(t, app.systemPromptForInput("continue"), "Tool-discovered skill body.")

	app.recordToolContextPaths(runloop.ToolCall{Name: "read_file", Input: `{"path":"other/main.go"}`, IsError: true})
	require.NotContains(t, app.dynamicSkillPaths, "other/main.go")
}

func TestToolContextPathsExtractsFilesystemToolInputs(t *testing.T) {
	require.Equal(t, []string{"src/app/main.go"}, toolContextPaths(runloop.ToolCall{Name: "Read", Input: `{"file_path":"src/app/main.go"}`}))
	require.Equal(t, []string{"notebooks/demo.ipynb"}, toolContextPaths(runloop.ToolCall{Name: "notebook_read", Input: `{"notebook_path":"notebooks/demo.ipynb"}`}))
	require.Equal(t, []string{"src/app"}, toolContextPaths(runloop.ToolCall{Name: "glob", Input: `{"pattern":"src/app/**/*.go"}`}))
	require.Equal(t, []string{"."}, toolContextPaths(runloop.ToolCall{Name: "ls", Input: `{}`}))
	require.Nil(t, toolContextPaths(runloop.ToolCall{Name: "bash", Input: `{"command":"cat src/app/main.go"}`}))
	require.Nil(t, toolContextPaths(runloop.ToolCall{Name: "read_file", Input: `{`}))
}

func TestSkillFrontmatterControlsInvocationAndSystemPrompt(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	visibleDir := filepath.Join(workspace, ".codog", "skills", "visible")
	hiddenDir := filepath.Join(workspace, ".codog", "skills", "hidden")
	disabledDir := filepath.Join(workspace, ".codog", "skills", "disabled")
	require.NoError(t, os.MkdirAll(visibleDir, 0o755))
	require.NoError(t, os.MkdirAll(hiddenDir, 0o755))
	require.NoError(t, os.MkdirAll(disabledDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(visibleDir, "SKILL.md"), []byte("Visible body."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenDir, "SKILL.md"), []byte(`---
user-invocable: false
---
Hidden body.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(disabledDir, "SKILL.md"), []byte(`---
disable-model-invocation: true
---
Disabled body.
`), 0o644))
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, EnabledSkills: []string{"visible", "disabled"}},
		Workspace: workspace,
		Err:       &errOut,
	}

	require.Contains(t, app.expandSkillInvocation("visible review this"), `<skill name="visible"`)
	require.Equal(t, "hidden review this", app.expandSkillInvocation("hidden review this"))
	require.False(t, app.handleSkillSlash(context.Background(), "/hidden review this", &session.Session{ID: "session"}))
	require.Empty(t, errOut.String())

	candidates := app.customSlashCompletionCandidates()
	require.Contains(t, candidates, "/visible ")
	require.NotContains(t, candidates, "/hidden ")

	prompt := app.systemPrompt()
	require.Contains(t, prompt, "Visible body.")
	require.NotContains(t, prompt, "Disabled body.")
}

func TestSkillAllowedToolsApplyOnlyToActiveTurn(t *testing.T) {
	workspace := t.TempDir()
	app := &App{
		Config: config.Config{
			PermissionMode: "read-only",
		},
		Workspace: workspace,
		Err:       io.Discard,
	}
	active := &skills.Skill{AllowedTools: []string{"Bash(go test:*)", "Read"}}

	prompter := app.prompterWithSkill("session", active)
	require.Contains(t, prompter.AllowRules, "bash:go test")
	require.Contains(t, prompter.AllowRules, "read_file")
	require.NoError(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go test ./..."}`)))
	require.Error(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go build ./..."}`)))
	require.Empty(t, app.Config.PermissionRules.Allow)
}

func TestSkillAllowedToolsDoNotBypassPlanMode(t *testing.T) {
	workspace := t.TempDir()
	_, err := planmode.Enter(workspace, "inspect before changing anything")
	require.NoError(t, err)
	app := &App{
		Config: config.Config{
			PermissionMode: "workspace-write",
		},
		Workspace: workspace,
		Err:       io.Discard,
	}
	active := &skills.Skill{AllowedTools: []string{"Bash(go test:*)"}}

	prompter := app.prompterWithSkill("session", active)

	require.Equal(t, tools.PermissionReadOnly, prompter.Mode)
	require.NotContains(t, prompter.AllowRules, "bash:go test")
	require.Error(t, prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"go test ./..."}`)))
}

func TestConfiguredPlanModeRestrictsSkillAllowedTools(t *testing.T) {
	workspace := t.TempDir()
	app := &App{
		Config: config.Config{
			PermissionMode: "workspace-write",
			PlanMode:       true,
		},
		Workspace: workspace,
		Err:       io.Discard,
	}
	active := &skills.Skill{AllowedTools: []string{"Bash(go test:*)"}}

	prompter := app.prompterWithSkill("session", active)

	require.True(t, app.planModeActive())
	require.Equal(t, tools.PermissionReadOnly, prompter.Mode)
	require.NotContains(t, prompter.AllowRules, "bash:go test")
}

func TestSkillsCommandSlashAndBareInvocation(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "skill done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review skill body ${CLAUDE_SESSION_ID}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "deploy.md"), []byte("Deploy command body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill body"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Skills([]string{"list", "--json"}))
	require.Contains(t, out.String(), `"name": "team:audit"`)
	require.Contains(t, out.String(), `"name": "deploy"`)
	require.Contains(t, out.String(), `"id": "legacy_commands_dir"`)
	require.Contains(t, out.String(), `"name": "debug"`)
	require.Contains(t, out.String(), `"source": "bundled"`)
	out.Reset()

	require.NoError(t, app.Skills([]string{"search", "audit", "--json"}))
	var searchReport struct {
		Kind   string         `json:"kind"`
		Action string         `json:"action"`
		Query  string         `json:"query"`
		Count  int            `json:"count"`
		Skills []skills.Skill `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &searchReport))
	require.Equal(t, "skills", searchReport.Kind)
	require.Equal(t, "search", searchReport.Action)
	require.Equal(t, "audit", searchReport.Query)
	require.Equal(t, len(searchReport.Skills), searchReport.Count)
	require.NotEmpty(t, skillReportEntry(searchReport.Skills, "team:audit", "claude").Name)
	out.Reset()

	require.NoError(t, app.Skills([]string{"sources", "--json"}))
	var sourceReport struct {
		Kind      string                 `json:"kind"`
		Action    string                 `json:"action"`
		Status    string                 `json:"status"`
		RootCount int                    `json:"root_count"`
		Roots     []skills.DiscoveryRoot `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &sourceReport))
	require.Equal(t, "skills", sourceReport.Kind)
	require.Equal(t, "sources", sourceReport.Action)
	require.Equal(t, "ok", sourceReport.Status)
	require.Equal(t, len(sourceReport.Roots), sourceReport.RootCount)
	requireSkillSourceRoot(t, sourceReport.Roots, "user", filepath.Join(configHome, "skills"), true)
	requireSkillSourceRoot(t, sourceReport.Roots, "claude", filepath.Join(workspace, ".claude", "skills"), true)
	requireSkillSourceRoot(t, sourceReport.Roots, "workspace", filepath.Join(workspace, ".codog", "skills"), false)
	commandRoot := skillSourceRootByPath(sourceReport.Roots, filepath.Join(workspace, ".codog", "commands"))
	require.NotNil(t, commandRoot.Origin)
	require.Equal(t, "legacy_commands_dir", commandRoot.Origin.ID)
	require.Equal(t, "legacy /commands", commandRoot.Origin.DetailLabel)
	out.Reset()

	require.NoError(t, app.Skills([]string{"show", "review"}))
	require.Equal(t, "Review skill body ${CLAUDE_SESSION_ID}\n", out.String())
	out.Reset()

	require.NoError(t, app.Skills([]string{"show", "deploy"}))
	require.Equal(t, "Deploy command body\n", out.String())
	out.Reset()

	require.NoError(t, app.Skills([]string{"show", "--json"}))
	var showListReport struct {
		Kind   string         `json:"kind"`
		Action string         `json:"action"`
		Skills []skills.Skill `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &showListReport))
	require.Equal(t, "skills", showListReport.Kind)
	require.Equal(t, "list", showListReport.Action)
	require.NotEmpty(t, showListReport.Skills)
	require.NotEmpty(t, skillReportEntry(showListReport.Skills, "review", "user").Path)
	out.Reset()

	err := app.Skills([]string{"invoke", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var invokeError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &invokeError))
	require.Equal(t, "skills", invokeError.Kind)
	require.Equal(t, "invoke", invokeError.Action)
	require.Equal(t, "missing_argument", invokeError.ErrorKind)
	require.Equal(t, "skill_name", invokeError.Argument)
	out.Reset()

	require.NoError(t, app.Skills([]string{"invoke", "debug", "failing test"}))
	require.Contains(t, out.String(), `<skill name="debug" source="bundled"`)
	require.Contains(t, out.String(), "User request: failing test")
	out.Reset()

	require.NoError(t, app.Skills([]string{"invoke", "team:audit", "auth"}))
	require.Contains(t, out.String(), `<skill name="team:audit"`)
	require.Contains(t, out.String(), "User request: auth")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills show team:audit", &session.Session{ID: "session"}))
	require.Equal(t, "Audit skill body\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills find deploy --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &searchReport))
	require.Equal(t, "search", searchReport.Action)
	require.Equal(t, "deploy", searchReport.Query)
	require.Equal(t, "deploy", searchReport.Skills[0].Name)
	out.Reset()

	require.NoError(t, app.Prompt(context.Background(), "review auth flow", config.FlagOverrides{SessionID: "skill-session"}))
	require.Contains(t, out.String(), "skill done")
	loaded, err := app.Sessions.Open("skill-session")
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)
	require.Contains(t, loaded.Messages[0].Content[0].Text, `<skill name="review"`)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "Review skill body skill-session")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "User request: auth flow")
	require.Contains(t, errOut.String(), "session: skill-session")
}

func requireSkillSourceRoot(t *testing.T, roots []skills.DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "skill source root not found", "source=%s path=%s roots=%v", source, path, roots)
}

func skillSourceRootByPath(roots []skills.DiscoveryRoot, path string) skills.DiscoveryRoot {
	for _, root := range roots {
		if root.Path == path {
			return root
		}
	}
	return skills.DiscoveryRoot{}
}

func TestSkillsListMarksShadowedEntries(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", "")
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills", "mismatch"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "debug.md"), []byte("User debug override."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "mismatch", "SKILL.md"), []byte(`---
name: external-review
---
Mismatch body.`), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Skills([]string{"list", "--json"}))
	var report struct {
		Kind               string                 `json:"kind"`
		Action             string                 `json:"action"`
		Status             string                 `json:"status"`
		MetadataDriftCount int                    `json:"metadata_drift_count"`
		MetadataDrift      []skills.MetadataDrift `json:"metadata_drift"`
		Skills             []skills.Skill         `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skills", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, 1, report.MetadataDriftCount)
	require.Contains(t, report.MetadataDrift, skills.MetadataDrift{
		InvocationName:  "mismatch",
		FrontmatterName: "external-review",
		Path:            filepath.Join(configHome, "skills", "mismatch", "SKILL.md"),
		Source:          "user",
	})
	userDebug := skillReportEntry(report.Skills, "debug", "user")
	require.True(t, userDebug.Active)
	bundledDebug := skillReportEntry(report.Skills, "debug", "bundled")
	require.False(t, bundledDebug.Active)
	require.Equal(t, "user", bundledDebug.ShadowedBy)
	require.Equal(t, userDebug.Path, bundledDebug.ShadowedByPath)
	out.Reset()

	require.NoError(t, app.Skills([]string{"audit", "--json"}))
	var audit skillAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &audit))
	require.Equal(t, "skills", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "degraded", audit.Status)
	require.GreaterOrEqual(t, audit.SkillCount, 2)
	require.GreaterOrEqual(t, audit.ActiveSkillCount, 1)
	require.GreaterOrEqual(t, audit.ShadowedSkillCount, 1)
	require.GreaterOrEqual(t, audit.SourceCount, 1)
	require.Equal(t, 1, audit.MetadataDriftCount)
	require.Contains(t, audit.MetadataDrift, skills.MetadataDrift{
		InvocationName:  "mismatch",
		FrontmatterName: "external-review",
		Path:            filepath.Join(configHome, "skills", "mismatch", "SKILL.md"),
		Source:          "user",
	})
	require.Contains(t, audit.Message, "metadata drift")
	out.Reset()

	require.NoError(t, app.Skills([]string{"list"}))
	require.Contains(t, out.String(), "debug\tuser\tskills_dir\tactive")
	require.Contains(t, out.String(), "debug\tbundled\tskills_dir\tshadowed by user")
	require.Contains(t, out.String(), "mismatch\tuser\tskills_dir\tactive\t\tname drift: external-review")
}

func skillReportEntry(all []skills.Skill, name string, source string) skills.Skill {
	for _, skill := range all {
		if skill.Name == name && skill.Source == source {
			return skill
		}
	}
	return skills.Skill{}
}

func TestSkillsInstallAndUninstallCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourceFile := filepath.Join(sourceRoot, "review.md")
	sourceDir := filepath.Join(sourceRoot, "audit")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review body"), 0o644))
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("Audit body"), 0o644))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Skills([]string{"install", sourceFile, "--json"}))
	require.Contains(t, out.String(), `"action": "install"`)
	require.Contains(t, out.String(), `"target": "user"`)
	require.FileExists(t, filepath.Join(configHome, "skills", "review.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"add", "--project", "--name", "review-copy", sourceFile, "--json"}))
	require.Contains(t, out.String(), `"action": "install"`)
	require.Contains(t, out.String(), `"name": "review-copy"`)
	require.Contains(t, out.String(), `"target": "workspace"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "review-copy.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"install", "--claude", "--name", "team:audit-copy", sourceDir}))
	require.Contains(t, out.String(), "Skill Installed")
	require.FileExists(t, filepath.Join(workspace, ".claude", "skills", "team", "audit-copy", "SKILL.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skills install --project "+sourceFile+" --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"target": "workspace"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "review.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"uninstall", "review", "--project", "--json"}))
	require.Contains(t, out.String(), `"removed": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "skills", "review.md"))
	out.Reset()

	require.NoError(t, app.Skills([]string{"uninstall", "review-copy", "--project", "--json"}))
	require.Contains(t, out.String(), `"removed": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "skills", "review-copy.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/skill uninstall team:audit-copy --claude", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Skill Uninstalled")
	require.NoDirExists(t, filepath.Join(workspace, ".claude", "skills", "team", "audit-copy"))
}

func TestSkillsInstallMissingSourceReportsTypedJSON(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := [][]string{
		{"--config", configPath, "--output-format", "json", "skills", "install"},
		{"--config", configPath, "skills", "install", "--output-format", "json"},
	}
	for _, args := range cases {
		out, err := captureStdout(t, func() error {
			return RunCLI(context.Background(), args, config.FlagOverrides{})
		})
		require.Error(t, err)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.Code)
		require.True(t, exitErr.Silent)

		var report actionErrorReport
		require.NoError(t, json.Unmarshal([]byte(out), &report))
		require.Equal(t, "skills", report.Kind)
		require.Equal(t, "install", report.Action)
		require.Equal(t, "error", report.Status)
		require.Equal(t, "missing_argument", report.ErrorKind)
		require.Equal(t, "install_source", report.Argument)
		require.Contains(t, report.Hint, "skills install")
	}
}

func TestSkillsUninstallMissingNameReportsTypedJSON(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := [][]string{
		{"--config", configPath, "--output-format", "json", "skills", "uninstall"},
		{"--config", configPath, "skills", "uninstall", "--json"},
	}
	for _, args := range cases {
		out, err := captureStdout(t, func() error {
			return RunCLI(context.Background(), args, config.FlagOverrides{})
		})
		require.Error(t, err)
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.Code)
		require.True(t, exitErr.Silent)

		var report actionErrorReport
		require.NoError(t, json.Unmarshal([]byte(out), &report))
		require.Equal(t, "skills", report.Kind)
		require.Equal(t, "uninstall", report.Action)
		require.Equal(t, "error", report.Status)
		require.Equal(t, "missing_argument", report.ErrorKind)
		require.Equal(t, "skill_name", report.Argument)
		require.Contains(t, report.Hint, "skills uninstall")
	}
}

func TestSkillsInfoAndDescribeAliasShow(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte(`---
description: Review changes.
---
Review body.
`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "info", "review"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Review body.")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skill", "describe", "review"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var skill skills.Skill
	require.NoError(t, json.Unmarshal([]byte(out), &skill))
	require.Equal(t, "review", skill.Name)
	require.Equal(t, "Review changes.", skill.Description)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "info", "missing"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var report actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "skills", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "skill_not_found", report.ErrorKind)
}

func TestSkillsCommandActionAliases(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{
		"config_home": configHome,
		"workspace":   workspace,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review body."), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "ls", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var listReport struct {
		Kind   string         `json:"kind"`
		Action string         `json:"action"`
		Skills []skills.Skill `json:"skills"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listReport))
	require.Equal(t, "skills", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.NotEmpty(t, skillReportEntry(listReport.Skills, "review", "user").Path)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "view", "review"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Equal(t, "Review body.\n", out)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "exec", "review", "audit"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `<skill name="review"`)
	require.Contains(t, out, "User request: audit")

	sourceFile := filepath.Join(t.TempDir(), "lint.md")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Lint body."), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "add", sourceFile, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var install skills.InstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &install))
	require.Equal(t, "install", install.Action)
	require.Equal(t, "lint", install.Name)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "rm", "lint", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var uninstall skills.UninstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &uninstall))
	require.Equal(t, "uninstall", uninstall.Action)
	require.Equal(t, "lint", uninstall.Name)
	require.True(t, uninstall.Removed)

	var slashOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &slashOut,
		Err:       io.Discard,
	}
	require.True(t, app.handleSlash(context.Background(), "/skills cat review", &session.Session{ID: "session"}))
	require.Equal(t, "Review body.\n", slashOut.String())
	slashOut.Reset()

	require.NoError(t, app.runResumedSkillsSlash("/skills", []string{"root", "--json"}, "json"))
	var rootsReport struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
	}
	require.NoError(t, json.Unmarshal(slashOut.Bytes(), &rootsReport))
	require.Equal(t, "skills", rootsReport.Kind)
	require.Equal(t, "sources", rootsReport.Action)
}

func TestSkillsActivationCommandsAndUnsupportedAction(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", "")
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{
		"config_home": configHome,
		"workspace":   workspace,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	sourceFile := filepath.Join(t.TempDir(), "review.md")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review body"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skill", "add", sourceFile}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var install skills.InstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &install))
	require.Equal(t, "skills", install.Kind)
	require.Equal(t, "install", install.Action)
	require.Equal(t, "review", install.Name)
	require.FileExists(t, filepath.Join(configHome, "skills", "review.md"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "enable", "review", "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var enabled skillActivationReport
	require.NoError(t, json.Unmarshal([]byte(out), &enabled))
	require.Equal(t, "skills", enabled.Kind)
	require.Equal(t, "enable", enabled.Action)
	require.Equal(t, "ok", enabled.Status)
	require.Equal(t, []string{"review"}, enabled.Added)
	require.Equal(t, []string{"review"}, enabled.EnabledSkills)
	var diskConfig struct {
		EnabledSkills []string `json:"enabled_skills"`
	}
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(configData, &diskConfig))
	require.Equal(t, []string{"review"}, diskConfig.EnabledSkills)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "status", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var status skillActivationReport
	require.NoError(t, json.Unmarshal([]byte(out), &status))
	require.Equal(t, "status", status.Action)
	require.Equal(t, "ok", status.Status)
	require.Equal(t, []string{"review"}, status.EnabledSkills)
	require.Equal(t, []string{"review"}, status.ResolvedSkills)
	require.GreaterOrEqual(t, status.AvailableSkillCount, 1)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "doctor", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var audit skillAuditReport
	require.NoError(t, json.Unmarshal([]byte(out), &audit))
	require.Equal(t, "skills", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "ok", audit.Status)
	require.Equal(t, []string{"review"}, audit.EnabledSkills)
	require.Equal(t, []string{"review"}, audit.ResolvedSkills)
	require.Empty(t, audit.MissingSkills)
	require.Contains(t, audit.Message, "passed")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "enable", "review", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var unchanged skillActivationReport
	require.NoError(t, json.Unmarshal([]byte(out), &unchanged))
	require.Empty(t, unchanged.Added)
	require.Equal(t, []string{"review"}, unchanged.Unchanged)
	require.Equal(t, []string{"review"}, unchanged.EnabledSkills)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "disable", "review", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var disabled skillActivationReport
	require.NoError(t, json.Unmarshal([]byte(out), &disabled))
	require.Equal(t, "disable", disabled.Action)
	require.Equal(t, []string{"review"}, disabled.Removed)
	require.Empty(t, disabled.EnabledSkills)
	configData, err = os.ReadFile(configPath)
	require.NoError(t, err)
	diskConfig = struct {
		EnabledSkills []string `json:"enabled_skills"`
	}{}
	require.NoError(t, json.Unmarshal(configData, &diskConfig))
	require.Empty(t, diskConfig.EnabledSkills)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "enable"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var missing actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &missing))
	require.Equal(t, "skills", missing.Kind)
	require.Equal(t, "enable", missing.Action)
	require.Equal(t, "missing_skill_name", missing.ErrorKind)
	require.Contains(t, missing.Hint, "codog skills enable NAME")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "serch"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "skills", report.Kind)
	require.Equal(t, "serch", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "unsupported_skills_action", report.ErrorKind)
	require.Contains(t, report.Message, "unsupported skills action")
	require.Contains(t, report.Hint, "Did you mean `codog skills search`?")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "search"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var searchMissing actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &searchMissing))
	require.Equal(t, "skills", searchMissing.Kind)
	require.Equal(t, "search", searchMissing.Action)
	require.Equal(t, "missing_argument", searchMissing.ErrorKind)
	require.Equal(t, "query", searchMissing.Argument)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "bogus"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "unsupported_skills_action")
	require.Contains(t, err.Error(), "codog skills list")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "help", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var help helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &help))
	require.Equal(t, "skills", help.Topic)
	require.Equal(t, "skills", help.Command)
	require.Contains(t, help.Usage, "audit")
	require.Contains(t, help.Usage, "doctor")
	require.Contains(t, help.Usage, "sources")
	require.Contains(t, help.Usage, "status")
	require.Contains(t, help.Usage, "enable")
	require.Contains(t, help.Usage, "disable")
	require.Contains(t, help.Usage, "info")
	require.Contains(t, help.Usage, "describe")
	require.Contains(t, help.Usage, "help")
	require.Contains(t, help.Help, "roots")
	require.Contains(t, help.Help, "metadata drift")
	require.Contains(t, help.Help, "aliases for `show`")
	require.Contains(t, help.Help, "aliases for `enable` and `disable`")
	require.Contains(t, help.Help, "codog skills help")
}

func TestSkillsNotFoundReportsTypedError(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	missingSource := filepath.Join(t.TempDir(), "missing-skill")

	cases := []struct {
		name    string
		args    []string
		action  string
		message string
	}{
		{
			name:    "show missing skill",
			args:    []string{"--config", configPath, "--output-format", "json", "skills", "show", "missing-skill"},
			action:  "show",
			message: `skill "missing-skill" was not found`,
		},
		{
			name:    "invoke missing skill",
			args:    []string{"--config", configPath, "--output-format", "json", "skills", "invoke", "missing-skill", "args"},
			action:  "invoke",
			message: `skill "missing-skill" was not found`,
		},
		{
			name:    "uninstall missing skill",
			args:    []string{"--config", configPath, "--output-format", "json", "skills", "uninstall", "missing-skill"},
			action:  "uninstall",
			message: `skill "missing-skill" was not found`,
		},
		{
			name:    "install missing source",
			args:    []string{"--config", configPath, "--output-format", "json", "skills", "install", missingSource},
			action:  "install",
			message: "skill source",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), tc.args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 1, exitErr.Code)
			require.True(t, exitErr.Silent)
			var report actionErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "skills", report.Kind)
			require.Equal(t, tc.action, report.Action)
			require.Equal(t, "error", report.Status)
			require.Equal(t, "skill_not_found", report.ErrorKind)
			require.Contains(t, report.Message, tc.message)
			require.Contains(t, report.Hint, "codog skills list")
			require.Contains(t, report.Hint, "codog skills install <path>")
		})
	}

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "show", "missing-skill"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "skill_not_found")
	require.Contains(t, err.Error(), "codog skills list")
}

func TestTemplatesCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "templates"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "templates", "review.md"), []byte("Review {{target}} as {{role}}."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "templates", "plan.md"), []byte("Plan {{topic}}."), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Templates(nil))
	require.Contains(t, out.String(), "plan\tworkspace")
	require.Contains(t, out.String(), "review\tuser")
	out.Reset()

	require.NoError(t, app.Templates([]string{"show", "--json"}))
	var templateList struct {
		Kind    string `json:"kind"`
		Action  string `json:"action"`
		Status  string `json:"status"`
		Query   string `json:"query"`
		Count   int    `json:"count"`
		Summary struct {
			Total    int `json:"total"`
			Active   int `json:"active"`
			Shadowed int `json:"shadowed"`
		} `json:"summary"`
		Templates []prompttemplates.Template `json:"templates"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateList))
	require.Equal(t, "templates", templateList.Kind)
	require.Equal(t, "list", templateList.Action)
	require.Equal(t, "ok", templateList.Status)
	require.Equal(t, 2, templateList.Count)
	require.Equal(t, 2, templateList.Summary.Total)
	require.Equal(t, 2, templateList.Summary.Active)
	require.Zero(t, templateList.Summary.Shadowed)
	out.Reset()

	require.NoError(t, app.Templates([]string{"ls", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateList))
	require.Equal(t, "templates", templateList.Kind)
	require.Equal(t, "list", templateList.Action)
	require.Equal(t, 2, templateList.Count)
	out.Reset()

	require.NoError(t, app.Templates([]string{"search", "plan", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateList))
	require.Equal(t, "templates", templateList.Kind)
	require.Equal(t, "search", templateList.Action)
	require.Equal(t, "plan", templateList.Query)
	require.Equal(t, 1, templateList.Count)
	require.Equal(t, "plan", templateList.Templates[0].Name)
	out.Reset()

	require.NoError(t, app.Templates([]string{"sources", "--json"}))
	var templateSources struct {
		Kind      string                          `json:"kind"`
		Action    string                          `json:"action"`
		Status    string                          `json:"status"`
		RootCount int                             `json:"root_count"`
		Roots     []prompttemplates.DiscoveryRoot `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateSources))
	require.Equal(t, "templates", templateSources.Kind)
	require.Equal(t, "sources", templateSources.Action)
	require.Equal(t, "ok", templateSources.Status)
	require.Equal(t, len(templateSources.Roots), templateSources.RootCount)
	requireTemplateSourceRoot(t, templateSources.Roots, "workspace", filepath.Join(workspace, ".codog", "templates"), true)
	requireTemplateSourceRoot(t, templateSources.Roots, "user", filepath.Join(configHome, "templates"), true)
	out.Reset()

	require.NoError(t, app.Templates([]string{"audit", "--json"}))
	var templateAudit templateAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateAudit))
	require.Equal(t, "templates", templateAudit.Kind)
	require.Equal(t, "audit", templateAudit.Action)
	require.Equal(t, "ok", templateAudit.Status)
	require.Equal(t, 2, templateAudit.TemplateCount)
	require.Equal(t, 2, templateAudit.ActiveTemplateCount)
	require.Zero(t, templateAudit.ShadowedTemplateCount)
	require.Equal(t, len(templateAudit.Sources), templateAudit.SourceCount)
	require.Contains(t, templateAudit.Message, "passed")
	out.Reset()

	require.NoError(t, app.Templates([]string{"doctor"}))
	require.Contains(t, out.String(), "Template Audit")
	require.Contains(t, out.String(), "Templates           2")
	out.Reset()

	require.NoError(t, app.Templates([]string{"show", "review"}))
	require.Contains(t, out.String(), "Review {{target}} as {{role}}.")
	out.Reset()

	require.NoError(t, app.Templates([]string{"view", "review"}))
	require.Contains(t, out.String(), "Review {{target}} as {{role}}.")
	out.Reset()

	err := app.Templates([]string{"apply", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var applyError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &applyError))
	require.Equal(t, "templates", applyError.Kind)
	require.Equal(t, "apply", applyError.Action)
	require.Equal(t, "missing_argument", applyError.ErrorKind)
	require.Equal(t, "template_name", applyError.Argument)
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "review", "--var", "target=auth", "role=reviewer"}))
	require.Equal(t, "Review auth as reviewer.\n", out.String())
	out.Reset()

	require.NoError(t, app.Templates([]string{"run", "review", "--var", "target=api", "role=maintainer"}))
	require.Equal(t, "Review api as maintainer.\n", out.String())
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "plan", "--json", "--var=topic=tests"}))
	require.Contains(t, out.String(), `"kind": "template_apply"`)
	require.Contains(t, out.String(), `"rendered": "Plan tests."`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates render plan topic=release", &session.Session{ID: "session"}))
	require.Equal(t, "Plan release.\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates find review --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateList))
	require.Equal(t, "search", templateList.Action)
	require.Equal(t, "review", templateList.Query)
	require.Equal(t, 1, templateList.Count)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates root --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &templateSources))
	require.Equal(t, "sources", templateSources.Action)
	require.Empty(t, errOut.String())
}

func TestTemplatesAuditReportsShadowedEntries(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "templates"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "templates", "review.md"), []byte("User review {{target}}."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "templates", "review.md"), []byte("Workspace review {{target}}."), 0o644))

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Templates([]string{"check", "--json"}))
	var audit templateAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &audit))
	require.Equal(t, "templates", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "ok", audit.Status)
	require.Equal(t, 2, audit.TemplateCount)
	require.Equal(t, 1, audit.ActiveTemplateCount)
	require.Equal(t, 1, audit.ShadowedTemplateCount)

	out.Reset()
	require.NoError(t, app.Templates([]string{"list", "--json"}))
	var list struct {
		Templates []prompttemplates.Template `json:"templates"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &list))
	userReview := templateReportEntry(list.Templates, "review", "user")
	require.False(t, userReview.Active)
	require.Equal(t, "workspace", userReview.ShadowedBy)
	require.NotEmpty(t, userReview.ShadowedByPath)
}

func TestTemplatesInstallAndUninstall(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourceFile := filepath.Join(sourceRoot, "review.md")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review {{target}}."), 0o644))

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Templates([]string{"install", sourceFile, "--json"}))
	var install prompttemplates.InstallReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &install))
	require.Equal(t, "templates", install.Kind)
	require.Equal(t, "install", install.Action)
	require.Equal(t, "review", install.Name)
	require.Equal(t, "user", install.Target)
	require.FileExists(t, filepath.Join(configHome, "templates", "review.md"))
	out.Reset()

	require.NoError(t, app.Templates([]string{"add", "--project", "--name", "brief", sourceFile, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &install))
	require.Equal(t, "brief", install.Name)
	require.Equal(t, "workspace", install.Target)
	require.FileExists(t, filepath.Join(workspace, ".codog", "templates", "brief.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates install --project "+sourceFile+" --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"target": "workspace"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "templates", "review.md"))
	out.Reset()

	require.NoError(t, app.Templates([]string{"uninstall", "review", "--project", "--json"}))
	var uninstall prompttemplates.UninstallReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &uninstall))
	require.Equal(t, "templates", uninstall.Kind)
	require.Equal(t, "uninstall", uninstall.Action)
	require.True(t, uninstall.Removed)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "templates", "review.md"))
	out.Reset()

	require.NoError(t, app.Templates([]string{"rm", "brief", "--project"}))
	require.Contains(t, out.String(), "Template Uninstalled")
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "templates", "brief.md"))
}

func TestTemplateRouterBoundaries(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	templateRoot := filepath.Join(configHome, "templates")
	require.NoError(t, os.MkdirAll(templateRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(templateRoot, "review.md"), []byte("Review {{target}}."), 0o644))
	source := filepath.Join(t.TempDir(), "brief.md")
	require.NoError(t, os.WriteFile(source, []byte("Brief {{topic}}."), 0o644))

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Templates([]string{"show", "review", "--json"}))
	require.Contains(t, out.String(), `"name": "review"`)
	out.Reset()

	err := app.Templates([]string{"show", "review", "extra", "--json"})
	require.Error(t, err)
	require.Contains(t, out.String(), "unexpected_extra_args")
	out.Reset()

	require.Error(t, app.Templates([]string{"show", "missing"}))
	require.Error(t, app.Templates([]string{"show", "review", "--output-format", "yaml"}))
	require.Error(t, app.Templates([]string{"apply", "review", "--var", "invalid"}))
	require.Error(t, app.Templates([]string{"apply", "missing", "target=test"}))
	require.Error(t, app.Templates([]string{"install", source, "--target", "invalid"}))
	require.Error(t, app.Templates([]string{"install", source, "--output-format", "yaml"}))

	require.NoError(t, app.Templates([]string{"install", source}))
	require.Contains(t, out.String(), "Template Installed")
	out.Reset()
	require.NoError(t, app.Templates([]string{"uninstall", "brief"}))
	require.Contains(t, out.String(), "Template Uninstalled")
	require.Error(t, app.Templates([]string{"uninstall", "missing"}))
	require.Error(t, app.Templates([]string{"uninstall", "brief", "--output-format", "yaml"}))
}

func templateReportEntry(all []prompttemplates.Template, name string, source string) prompttemplates.Template {
	for _, tmpl := range all {
		if tmpl.Name == name && tmpl.Source == source {
			return tmpl
		}
	}
	return prompttemplates.Template{}
}

func requireTemplateSourceRoot(t *testing.T, roots []prompttemplates.DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "template source root not found", "source=%s path=%s roots=%v", source, path, roots)
}

func TestCommandsCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "commands", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "fix.md"), []byte("Claude fix {{args}}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "fix.md"), []byte("Codog fix {{ ARGUMENTS }}"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Commands([]string{"list"}))
	require.Contains(t, out.String(), "fix\tclaude\tshadowed by workspace")
	require.Contains(t, out.String(), "fix\tworkspace\tactive")
	require.Contains(t, out.String(), "review\tuser\tactive")
	out.Reset()

	require.NoError(t, app.Commands([]string{"list", "--json"}))
	var listReport struct {
		Kind    string `json:"kind"`
		Action  string `json:"action"`
		Status  string `json:"status"`
		Query   string `json:"query"`
		Count   int    `json:"count"`
		Summary struct {
			Total    int `json:"total"`
			Active   int `json:"active"`
			Shadowed int `json:"shadowed"`
		} `json:"summary"`
		Commands []customcommands.Command `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "commands", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, "ok", listReport.Status)
	require.Equal(t, len(listReport.Commands), listReport.Count)
	require.Equal(t, 3, listReport.Summary.Total)
	require.Equal(t, 2, listReport.Summary.Active)
	require.Equal(t, 1, listReport.Summary.Shadowed)
	require.False(t, commandReportEntry(listReport.Commands, "fix", "claude").Active)
	require.Equal(t, "workspace", commandReportEntry(listReport.Commands, "fix", "claude").ShadowedBy)
	out.Reset()

	require.NoError(t, app.Commands([]string{"ls", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "commands", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, 3, listReport.Summary.Total)
	out.Reset()

	require.NoError(t, app.Commands([]string{"search", "codog", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "commands", listReport.Kind)
	require.Equal(t, "search", listReport.Action)
	require.Equal(t, "codog", listReport.Query)
	require.Equal(t, 1, listReport.Count)
	require.Equal(t, "fix", listReport.Commands[0].Name)
	require.Equal(t, "workspace", listReport.Commands[0].Source)
	out.Reset()

	require.NoError(t, app.Commands([]string{"show", "fix", "--json"}))
	require.Contains(t, out.String(), `"source": "workspace"`)
	out.Reset()

	require.NoError(t, app.Commands([]string{"view", "fix"}))
	require.Equal(t, "Codog fix {{ ARGUMENTS }}\n", out.String())
	out.Reset()

	require.NoError(t, app.Commands([]string{"show", "--json"}))
	var showListReport struct {
		Kind     string                   `json:"kind"`
		Action   string                   `json:"action"`
		Status   string                   `json:"status"`
		Count    int                      `json:"count"`
		Commands []customcommands.Command `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &showListReport))
	require.Equal(t, "commands", showListReport.Kind)
	require.Equal(t, "list", showListReport.Action)
	require.Equal(t, "ok", showListReport.Status)
	require.Equal(t, 3, showListReport.Count)
	out.Reset()

	require.NoError(t, app.Commands([]string{"sources", "--json"}))
	var sourceReport struct {
		Kind      string                         `json:"kind"`
		Action    string                         `json:"action"`
		Status    string                         `json:"status"`
		RootCount int                            `json:"root_count"`
		Roots     []customcommands.DiscoveryRoot `json:"roots"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &sourceReport))
	require.Equal(t, "commands", sourceReport.Kind)
	require.Equal(t, "sources", sourceReport.Action)
	require.Equal(t, "ok", sourceReport.Status)
	require.Equal(t, len(sourceReport.Roots), sourceReport.RootCount)
	requireCommandSourceRoot(t, sourceReport.Roots, "workspace", filepath.Join(workspace, ".codog", "commands"), true)
	requireCommandSourceRoot(t, sourceReport.Roots, "claude", filepath.Join(workspace, ".claude", "commands"), true)
	requireCommandSourceRoot(t, sourceReport.Roots, "user", filepath.Join(configHome, "commands"), true)
	out.Reset()

	require.NoError(t, app.Commands([]string{"audit", "--json"}))
	var audit commandAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &audit))
	require.Equal(t, "commands", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "ok", audit.Status)
	require.Equal(t, 3, audit.CommandCount)
	require.Equal(t, 2, audit.ActiveCommandCount)
	require.Equal(t, 1, audit.ShadowedCommandCount)
	require.Equal(t, len(audit.Sources), audit.SourceCount)
	require.Zero(t, audit.FrontmatterErrorCount)
	require.Contains(t, audit.Message, "passed")
	out.Reset()

	require.NoError(t, app.Commands([]string{"doctor"}))
	require.Contains(t, out.String(), "Command Audit")
	require.Contains(t, out.String(), "Shadowed            1")
	out.Reset()

	require.NoError(t, app.Commands([]string{"root", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &sourceReport))
	require.Equal(t, "commands", sourceReport.Kind)
	require.Equal(t, "sources", sourceReport.Action)
	require.Equal(t, len(sourceReport.Roots), sourceReport.RootCount)
	out.Reset()

	err := app.Commands([]string{"run", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var runError actionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runError))
	require.Equal(t, "commands", runError.Kind)
	require.Equal(t, "run", runError.Action)
	require.Equal(t, "missing_argument", runError.ErrorKind)
	require.Equal(t, "command_name", runError.Argument)
	out.Reset()

	require.NoError(t, app.Commands([]string{"run", "fix", "bug", "123"}))
	require.Equal(t, "Codog fix bug 123\n", out.String())
	out.Reset()

	require.NoError(t, app.Commands([]string{"exec", "fix", "bug", "456"}))
	require.Equal(t, "Codog fix bug 456\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands render review file.go", &session.Session{ID: "session"}))
	require.Equal(t, "Review file.go\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands find review --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "search", listReport.Action)
	require.Equal(t, "review", listReport.Query)
	require.Equal(t, 1, listReport.Count)
	require.Equal(t, "review", listReport.Commands[0].Name)
	require.Empty(t, errOut.String())
}

func TestCommandsAuditReportsFrontmatterErrors(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "commands", "broken.md"), []byte("---\ndescription: [broken\n---\nBody"), 0o644))

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Commands([]string{"check", "--json"}))
	var audit commandAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &audit))
	require.Equal(t, "commands", audit.Kind)
	require.Equal(t, "audit", audit.Action)
	require.Equal(t, "degraded", audit.Status)
	require.Equal(t, 1, audit.CommandCount)
	require.Equal(t, 1, audit.FrontmatterErrorCount)
	require.Len(t, audit.FrontmatterErrors, 1)
	require.Equal(t, "broken", audit.FrontmatterErrors[0].Name)
	require.NotEmpty(t, audit.FrontmatterErrors[0].FrontmatterError)
	require.Empty(t, audit.FrontmatterErrors[0].Body)
	require.Contains(t, audit.Message, "frontmatter")
}

func TestCommandsInstallAndUninstall(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	sourceRoot := t.TempDir()
	sourceFile := filepath.Join(sourceRoot, "review.md")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review $ARGUMENTS"), 0o644))

	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Commands([]string{"install", sourceFile, "--json"}))
	var install customcommands.InstallReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &install))
	require.Equal(t, "commands", install.Kind)
	require.Equal(t, "install", install.Action)
	require.Equal(t, "review", install.Name)
	require.Equal(t, "user", install.Target)
	require.FileExists(t, filepath.Join(configHome, "commands", "review.md"))
	out.Reset()

	require.NoError(t, app.Commands([]string{"add", "--project", "--name", "team:audit", sourceFile, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &install))
	require.Equal(t, "team:audit", install.Name)
	require.Equal(t, "workspace", install.Target)
	require.FileExists(t, filepath.Join(workspace, ".codog", "commands", "team", "audit.md"))
	out.Reset()

	require.NoError(t, app.Commands([]string{"install", "--claude", "--name", "legacy:review", sourceFile}))
	require.Contains(t, out.String(), "Command Installed")
	require.FileExists(t, filepath.Join(workspace, ".claude", "commands", "legacy", "review.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands install --project "+sourceFile+" --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"target": "workspace"`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "commands", "review.md"))
	out.Reset()

	require.NoError(t, app.Commands([]string{"uninstall", "review", "--project", "--json"}))
	var uninstall customcommands.UninstallReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &uninstall))
	require.Equal(t, "commands", uninstall.Kind)
	require.Equal(t, "uninstall", uninstall.Action)
	require.True(t, uninstall.Removed)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "commands", "review.md"))
	out.Reset()

	require.NoError(t, app.Commands([]string{"rm", "team:audit", "--project"}))
	require.Contains(t, out.String(), "Command Uninstalled")
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "commands", "team", "audit.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands remove legacy:review --claude --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"removed": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".claude", "commands", "legacy", "review.md"))
}

func TestResourceCatalogErrorsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "commands unknown action",
			args:      []string{"commands", "serch"},
			kind:      "commands",
			errorKind: "unsupported_commands_action",
			contains:  []string{`"action": "serch"`, "Did you mean `codog commands search`?"},
		},
		{
			name:      "templates unknown action",
			args:      []string{"templates", "serch"},
			kind:      "templates",
			errorKind: "unsupported_templates_action",
			contains:  []string{`"action": "serch"`, "Did you mean `codog templates search`?"},
		},
		{
			name:      "commands search missing query",
			args:      []string{"commands", "search"},
			kind:      "commands",
			errorKind: "missing_argument",
			contains:  []string{`"action": "search"`, `"argument": "query"`},
		},
		{
			name:      "commands install missing source",
			args:      []string{"commands", "install"},
			kind:      "commands",
			errorKind: "missing_argument",
			contains:  []string{`"action": "install"`, `"argument": "install_source"`},
		},
		{
			name:      "commands uninstall missing name",
			args:      []string{"commands", "uninstall"},
			kind:      "commands",
			errorKind: "missing_argument",
			contains:  []string{`"action": "uninstall"`, `"argument": "command_name"`},
		},
		{
			name:      "templates search missing query",
			args:      []string{"templates", "find"},
			kind:      "templates",
			errorKind: "missing_argument",
			contains:  []string{`"action": "search"`, `"argument": "query"`},
		},
		{
			name:      "templates install missing source",
			args:      []string{"templates", "install"},
			kind:      "templates",
			errorKind: "missing_argument",
			contains:  []string{`"action": "install"`, `"argument": "install_source"`},
		},
		{
			name:      "templates uninstall missing name",
			args:      []string{"templates", "uninstall"},
			kind:      "templates",
			errorKind: "missing_argument",
			contains:  []string{`"action": "uninstall"`, `"argument": "template_name"`},
		},
		{
			name:      "commands sources extra",
			args:      []string{"commands", "sources", "bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "commands sources"`, `"option": "bogus"`},
		},
		{
			name:      "skills sources extra",
			args:      []string{"skills", "sources", "bogus"},
			kind:      "unknown_option",
			errorKind: "unknown_option",
			contains:  []string{`"command": "skills sources"`, `"option": "bogus"`},
		},
		{
			name:      "skills sources invalid format",
			args:      []string{"skills", "sources", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`, `"text"`, `"json"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
}

func commandReportEntry(commands []customcommands.Command, name string, source string) customcommands.Command {
	for _, command := range commands {
		if command.Name == name && command.Source == source {
			return command
		}
	}
	return customcommands.Command{}
}

func requireCommandSourceRoot(t *testing.T, roots []customcommands.DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "command source root not found", "source=%s path=%s roots=%v", source, path, roots)
}

func TestCustomSlashRunsRenderedPrompt(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "custom done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "review.md"), []byte("Review this target: $ARGUMENTS (${CLAUDE_SESSION_ID})"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("custom-slash")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/team/review target.go", sess))
	require.Contains(t, out.String(), "custom done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "user", sess.Messages[0].Role)
	require.Equal(t, "Review this target: target.go (custom-slash)", sess.Messages[0].Content[0].Text)
	history, err := app.Sessions.PromptHistory("custom-slash")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "Review this target: target.go (custom-slash)", history[0].Text)
}

func TestOMCSlashRunsCompatibleCommand(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "omc done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".omc", "commands", "oh-my-claudecode", "hud.md"), []byte("OMC HUD $ARGUMENTS (${CLAUDE_SESSION_ID})"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("omc-slash")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/oh-my-claudecode:hud panel", sess))
	require.Contains(t, out.String(), "omc done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "OMC HUD panel (omc-slash)", sess.Messages[0].Content[0].Text)
}

func TestCustomSlashAllowedToolsApplyToActiveTurn(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Turns: []mockanthropic.Turn{
		{ToolUses: []mockanthropic.ToolUse{{
			ID:    "toolu_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"echo command-ok"}`),
		}}},
		{Text: "custom done"},
	}}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "check.md"), []byte(`---
allowed-tools: Bash(echo:*)
---
Check this target: $ARGUMENTS`), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            2,
			AutoCompactMessages: 40,
			PermissionMode:      "read-only",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("custom-allowed")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/check target.go", sess))

	require.Contains(t, out.String(), "custom done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 4)
	require.Equal(t, "tool_result", sess.Messages[2].Content[0].Type)
	require.False(t, sess.Messages[2].Content[0].IsError)
	require.Contains(t, sess.Messages[2].Content[0].Content, "command-ok")
}

func TestSkillSlashRunsRenderedPrompt(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "skill done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill body"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess, err := app.Sessions.Open("skill-slash")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/team/audit auth", sess))
	require.Contains(t, out.String(), "skill done")
	require.Empty(t, errOut.String())
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "user", sess.Messages[0].Role)
	require.Contains(t, sess.Messages[0].Content[0].Text, `<skill name="team:audit"`)
	require.Contains(t, sess.Messages[0].Content[0].Text, "User request: auth")
}

func TestExportCommandWritesFormats(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "export me")))
	var out bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Export([]string{"--session", "source"}))
	require.Contains(t, out.String(), "# Conversation Export")
	require.Contains(t, out.String(), "export me")
	out.Reset()

	output := filepath.Join(workspace, "transcript.json")
	require.NoError(t, app.Export([]string{"--session=source", "--format=json", "--output", output}))
	require.Contains(t, out.String(), `"format": "json"`)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
	out.Reset()

	htmlOutput := filepath.Join(workspace, "transcript.html")
	require.NoError(t, app.Export([]string{"--session=source", "--format=html", "--output", htmlOutput}))
	require.Contains(t, out.String(), `"format": "html"`)
	data, err = os.ReadFile(htmlOutput)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "export me")
}

func TestExportCommandAvoidsOverwritingExistingOutput(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first export")))
	var out bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: io.Discard}

	output := filepath.Join(workspace, "transcript.custom")
	require.NoError(t, os.WriteFile(output, []byte("keep me\n"), 0o644))
	require.NoError(t, app.Export([]string{"--session=source", "--format=json", "--output", output}))

	var report struct {
		File   string `json:"file"`
		Format string `json:"format"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "json", report.Format)
	require.Equal(t, filepath.Join(workspace, "transcript-2.custom"), report.File)
	original, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "keep me\n", string(original))
	exported, err := os.ReadFile(report.File)
	require.NoError(t, err)
	require.Contains(t, string(exported), `"id": "source"`)
}

func TestExportTUIConversationWritesMarkdownFile(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "export from tui")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	app := &App{Sessions: store, Workspace: workspace, Out: io.Discard, Err: io.Discard}

	result, err := app.exportTUIConversation(context.Background(), sess)
	require.NoError(t, err)
	require.Equal(t, "Conversation Exported", result.Title)
	require.Equal(t, "exported", result.Status)
	require.Contains(t, result.Lines, "Session: source")
	require.Contains(t, result.Lines, "File: .codog/exports/source.md")
	require.Contains(t, result.Lines, "Format: markdown")
	require.Contains(t, result.Lines, "Messages: 1")

	data, err := os.ReadFile(filepath.Join(workspace, ".codog", "exports", "source.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "# Conversation Export")
	require.Contains(t, string(data), "export from tui")

	custom, err := app.exportTUIConversationTo(context.Background(), sess, "conversation.md")
	require.NoError(t, err)
	require.Contains(t, custom.Lines, "File: conversation.md")
	data, err = os.ReadFile(filepath.Join(workspace, "conversation.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "export from tui")
}

func TestCopyTUIConversationWritesClipboard(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "copy from tui")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "copied response")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var copied []byte
	previousClipboard := writeClipboard
	writeClipboard = func(_ context.Context, data []byte) (string, error) {
		copied = append([]byte(nil), data...)
		return "test-clipboard", nil
	}
	t.Cleanup(func() { writeClipboard = previousClipboard })
	app := &App{Sessions: store, Workspace: workspace, Out: io.Discard, Err: io.Discard}

	result, err := app.copyTUIConversation(context.Background(), sess)
	require.NoError(t, err)
	require.Equal(t, "Conversation Copied", result.Title)
	require.Equal(t, "copied", result.Status)
	require.Contains(t, result.Lines, "Session: source")
	require.Contains(t, result.Lines, "Clipboard: test-clipboard")
	require.Contains(t, result.Lines, "Format: markdown")
	require.Contains(t, result.Lines, "Messages: 2")
	require.Contains(t, string(copied), "# Conversation Export")
	require.Contains(t, string(copied), "copy from tui")
	require.Contains(t, string(copied), "copied response")
}

func TestCopyTUIMessageWritesClipboard(t *testing.T) {
	workspace := t.TempDir()
	var copied []byte
	previousClipboard := writeClipboard
	writeClipboard = func(_ context.Context, data []byte) (string, error) {
		copied = append([]byte(nil), data...)
		return "test-clipboard", nil
	}
	t.Cleanup(func() { writeClipboard = previousClipboard })
	app := &App{Workspace: workspace, Out: io.Discard, Err: io.Discard}

	result, err := app.copyTUIMessage(context.Background(), "  message from tui\nsecond line  ")
	require.NoError(t, err)
	require.Equal(t, "Message Copied", result.Title)
	require.Equal(t, "message copied", result.Status)
	require.Contains(t, result.Lines, "Clipboard: test-clipboard")
	require.Contains(t, result.Lines, "Lines: 2")
	require.Equal(t, "message from tui\nsecond line\n", string(copied))
}

func TestExportRejectsRelativePathTraversal(t *testing.T) {
	configHome := t.TempDir()
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "do not escape")))
	outside := filepath.Join(parent, "escaped.md")
	var out bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: io.Discard}

	err := app.Export([]string{"--session", "source", "--output", "../escaped.md"})
	require.ErrorContains(t, err, "escapes workspace")
	require.NoFileExists(t, outside)
	require.Empty(t, out.String())

	absolute := filepath.Join(workspace, "explicit.md")
	require.NoError(t, app.Export([]string{"--session", "source", "--output", absolute}))
	require.FileExists(t, absolute)
}

func TestExportMissingSessionReportsTypedJSON(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	t.Chdir(workspace)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "export", "--session", "does-not-exist"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	require.NotContains(t, out, "# Conversation Export")

	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "session_not_found", report.Kind)
	require.Equal(t, "session_not_found", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "abort", report.Action)
	require.Equal(t, `session "does-not-exist" was not found`, report.Message)
	require.Contains(t, report.Hint, "codog sessions list")

	store := session.NewWorkspaceStore(configHome, workspace)
	_, statErr := os.Stat(filepath.Join(store.Dir, "does-not-exist.jsonl"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExportWriteFailureReportsFilesystemEnvelope(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "export me")))
	t.Chdir(workspace)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "export", "--session", "source", "--output", "missing-dir/out.md"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)

	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	expectedPath := filepath.Join(workspace, "missing-dir", "out.md")
	require.Equal(t, "export_validate_output_path_failed", report.ErrorKind)
	require.Equal(t, "export", report.Command)
	require.Equal(t, "write", report.Action)
	require.Equal(t, expectedPath, report.Path)
	require.Equal(t, "filesystem", report.Error.Kind)
	require.Equal(t, "validate_output_path", report.Error.Operation)
	require.Equal(t, expectedPath, report.Error.Target)
	require.Equal(t, "ENOENT", report.Error.Errno)
	require.True(t, report.Error.Retryable)
	require.Contains(t, report.Hint, "parent directory")
	require.Contains(t, report.Message, expectedPath)
	require.NoFileExists(t, expectedPath)
}

func TestExportSlashWritesCurrentSession(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "slash export")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.True(t, app.handleSlash(context.Background(), "/export notes.md", sess))
	require.Contains(t, errOut.String(), "exported session source")
	data, err := os.ReadFile(filepath.Join(workspace, "notes.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "slash export")
	errOut.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes-2.md"), []byte("existing\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/export notes.md", sess))
	require.Contains(t, errOut.String(), "notes-3.md")
	data, err = os.ReadFile(filepath.Join(workspace, "notes.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "slash export")
	data, err = os.ReadFile(filepath.Join(workspace, "notes-2.md"))
	require.NoError(t, err)
	require.Equal(t, "existing\n", string(data))
	data, err = os.ReadFile(filepath.Join(workspace, "notes-3.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "slash export")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/export ../escape.md", sess))
	require.Contains(t, errOut.String(), "escapes workspace")
	require.NoFileExists(t, filepath.Join(filepath.Dir(workspace), "escape.md"))
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/export --format html", sess))
	require.Contains(t, errOut.String(), "exported session source")
	data, err = os.ReadFile(filepath.Join(workspace, "slash-export.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
}

func TestShareCommandAndSlashWritesLocalArtifact(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nshare change\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "share-note.txt"), []byte("share note\n"), 0o644))
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "share me")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Share([]string{"--session", "source", "--format=json", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "share"`)
	require.Contains(t, out.String(), `"format": "json"`)
	require.Contains(t, out.String(), `"git_state_file":`)
	require.Contains(t, out.String(), `"git_state": {`)
	require.Contains(t, out.String(), `"untracked_files": 1`)
	sharedJSON := filepath.Join(workspace, ".codog", "share", "source.json")
	data, err := os.ReadFile(sharedJSON)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
	gitStatePath := filepath.Join(workspace, ".codog", "share", "source.git-state.json")
	data, err = os.ReadFile(gitStatePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "+share change")
	require.Contains(t, string(data), "share-note.txt")
	out.Reset()

	require.NoError(t, app.Share([]string{"--session", "source", "--format=html", "html-share"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Shared session source")
	require.Contains(t, out.String(), "Git state saved")
	data, err = os.ReadFile(filepath.Join(workspace, "html-share", "source.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	_, err = os.Stat(filepath.Join(workspace, "html-share", "source.git-state.json"))
	require.NoError(t, err)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/share shared", sess))
	require.Empty(t, errOut.String())
	require.Contains(t, out.String(), "Shared session source")
	sharedMarkdown := filepath.Join(workspace, "shared", "source.md")
	data, err = os.ReadFile(sharedMarkdown)
	require.NoError(t, err)
	require.Contains(t, string(data), "share me")
	_, err = os.Stat(filepath.Join(workspace, "shared", "source.git-state.json"))
	require.NoError(t, err)
}

func TestCopyCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "copy prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "copy response")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "copy followup")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "latest copy response")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var copied []byte
	previousClipboard := writeClipboard
	writeClipboard = func(_ context.Context, data []byte) (string, error) {
		copied = append([]byte(nil), data...)
		return "test-clipboard", nil
	}
	t.Cleanup(func() { writeClipboard = previousClipboard })
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Copy(context.Background(), []string{"last", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Equal(t, "latest copy response\n", string(copied))
	require.Contains(t, out.String(), `"clipboard": "test-clipboard"`)
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"2", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Equal(t, "copy response\n", string(copied))
	require.Contains(t, out.String(), `"scope": "nth"`)
	require.Contains(t, out.String(), `"nth": 2`)
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"all", "--session=source", "--format=json"}, config.FlagOverrides{}))
	require.Contains(t, string(copied), `"id": "source"`)
	require.Contains(t, out.String(), "Copied all")
	out.Reset()

	require.NoError(t, app.Copy(context.Background(), []string{"all", "--session=source", "--format=html"}, config.FlagOverrides{}))
	require.Contains(t, string(copied), "<!doctype html>")
	require.Contains(t, out.String(), "Copied all")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/copy 2", sess))
	require.Equal(t, "copy response\n", string(copied))
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/copy all", sess))
	require.Contains(t, string(copied), "# Conversation Export")
	require.Empty(t, errOut.String())
}

func TestPasteCommandAndSlashPrint(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	sess, err := store.Open("source")
	require.NoError(t, err)
	previousReadClipboard := readClipboard
	readClipboard = func(_ context.Context) ([]byte, string, error) {
		return []byte("paste prompt\nsecond line"), "test-read-clipboard", nil
	}
	t.Cleanup(func() { readClipboard = previousReadClipboard })
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Paste(context.Background(), []string{"--json", "--session", "source"}, config.FlagOverrides{}))
	var report pasteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "paste", report.Kind)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, "test-read-clipboard", report.Clipboard)
	require.Equal(t, 2, report.Lines)
	require.False(t, report.Submitted)
	require.Contains(t, report.Preview, "paste prompt")
	out.Reset()

	require.NoError(t, app.Paste(context.Background(), nil, config.FlagOverrides{}))
	require.Equal(t, "paste prompt\nsecond line", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/paste --print", sess))
	require.Equal(t, "paste prompt\nsecond line", out.String())
	require.Empty(t, errOut.String())
	out.Reset()

	require.ErrorContains(t, app.Paste(context.Background(), []string{"--max-bytes", "4"}, config.FlagOverrides{}), "over paste max")
}

func TestReadTUIClipboardStagesImageAttachment(t *testing.T) {
	workspace := t.TempDir()
	previousReadClipboardImage := readClipboardImage
	previousReadClipboard := readClipboard
	readClipboardImage = func(context.Context) (clipboardImage, error) {
		return clipboardImage{
			Data:      []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
			MediaType: "image/png",
			Extension: ".png",
			Clipboard: "test-image-clipboard",
		}, nil
	}
	readClipboard = func(context.Context) ([]byte, string, error) {
		t.Fatal("text clipboard should not be read when an image is available")
		return nil, "", nil
	}
	t.Cleanup(func() {
		readClipboardImage = previousReadClipboardImage
		readClipboard = previousReadClipboard
	})
	app := &App{Workspace: workspace}

	content, err := app.readTUIClipboard(context.Background())
	require.NoError(t, err)
	require.Empty(t, content.Text)
	require.Equal(t, "image/png", content.MediaType)
	require.NotEmpty(t, content.AttachmentPath)
	require.Contains(t, content.AttachmentPath, filepath.Join(".codog", "attachments", "clipboard"))
	data, err := os.ReadFile(content.AttachmentPath)
	require.NoError(t, err)
	require.Equal(t, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, data)
}
