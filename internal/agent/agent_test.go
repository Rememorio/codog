package agent

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/anttrace"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/autofixpr"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/mocklimits"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/perfissue"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionname"
	"github.com/Rememorio/codog/internal/skills"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/thinkback"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/stretchr/testify/require"
)

func TestEnterpriseAuditListsEvents(t *testing.T) {
	configHome := t.TempDir()
	require.NoError(t, audit.NewStore(configHome).Append(audit.Event{
		Type:      "permission",
		ToolName:  "bash",
		Allowed:   audit.Bool(false),
		SessionID: "session-1",
	}))

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Enterprise([]string{"audit", "10"}))
	require.Contains(t, out.String(), `"type": "permission"`)
	require.Contains(t, out.String(), `"allowed": false`)
}

func TestEnterpriseVerifyCommand(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	dir := t.TempDir()
	policy := config.ManagedPolicy{MaxPermissionMode: "read-only", DeniedTools: []string{"bash"}}
	payload, err := config.ManagedPolicyPayload(policy)
	require.NoError(t, err)
	policy.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	policyPath := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, data, 0o644))

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Enterprise([]string{"verify", policyPath, base64.StdEncoding.EncodeToString(publicKey)}))
	require.Contains(t, out.String(), `"signature_valid": true`)
	require.Contains(t, out.String(), `"max_permission_mode": "read-only"`)
	require.NotContains(t, out.String(), policy.Signature)
}

func TestVersionCommandOutputsTextAndJSON(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	require.NoError(t, renderVersion(&out, workspace, nil))
	require.Contains(t, out.String(), "Codog")
	require.Contains(t, out.String(), "Version          0.1.0")
	out.Reset()

	require.NoError(t, renderVersion(&out, workspace, []string{"--json"}))
	require.Contains(t, out.String(), `"kind": "version"`)
	require.Contains(t, out.String(), `"version": "0.1.0"`)
	require.Contains(t, out.String(), `"go_version":`)

	require.NoError(t, RunCLI(context.Background(), []string{"--version"}, config.FlagOverrides{}))
}

func TestHelpCommandOutputsTextAndJSON(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderHelpCommand(&out, nil))
	require.Contains(t, out.String(), "Usage:")
	require.Contains(t, out.String(), "prompt")
	out.Reset()

	require.NoError(t, renderHelpCommand(&out, []string{"doctor", "--output-format", "json"}))
	var report helpReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "help", report.Kind)
	require.Equal(t, "help", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "doctor", report.Topic)
	require.Equal(t, "doctor", report.Command)
	require.Equal(t, "codog doctor [--output-format text|json]", report.Usage)
	require.Contains(t, report.Help, "Doctor")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)
	require.Contains(t, report.OutputFields, "checks")
	require.Contains(t, report.StatusValues, "warn")
	require.Contains(t, report.CheckNames, "Auth")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"prompt", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "prompt", report.Topic)
	require.Equal(t, "prompt", report.Command)
	require.Contains(t, report.Usage, "stream-json")
	require.NotNil(t, report.LocalOnly)
	require.False(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresCredentials)
	require.True(t, *report.RequiresCredentials)
	require.NotNil(t, report.RequiresProviderRequest)
	require.True(t, *report.RequiresProviderRequest)
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)
	require.Contains(t, report.OutputFields, "tool_calls")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"speak", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "speak", report.Topic)
	require.Equal(t, "speak", report.Command)
	require.Contains(t, report.Help, "text-to-speech")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)
	require.Contains(t, report.OutputFields, "text_preview")
	require.Contains(t, report.StatusValues, "error")

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"acp", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "acp", report.Topic)
	require.Equal(t, "acp", report.Command)
	require.Contains(t, report.Help, "stdio JSON-RPC")
	require.Contains(t, report.Aliases, "--acp")
	require.Contains(t, report.Formats, "json")
	require.Contains(t, report.OutputFields, "protocol")
	require.Contains(t, report.ProtocolFields, "methods")
	require.Contains(t, report.ContractFields, "unsupported_invocation_kind")
	require.Contains(t, report.ProtocolMethods, "session/list")
	require.NotNil(t, report.ServeStartsDaemon)
	require.True(t, *report.ServeStartsDaemon)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"reasoning", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "reasoning", report.Topic)
	require.Equal(t, "reasoning", report.Command)
	require.Contains(t, report.Help, "reasoning effort")
	require.Contains(t, report.OutputFields, "effort")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"rate-limit", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "rate-limit", report.Topic)
	require.Equal(t, "rate-limit", report.Command)
	require.Contains(t, report.Help, "retry and backoff")
	require.Contains(t, report.OutputFields, "max_retries")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"budget", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "budget", report.Topic)
	require.Equal(t, "budget", report.Command)
	require.Contains(t, report.Help, "token budget")
	require.Contains(t, report.OutputFields, "max_tokens")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"profile", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "profile", report.Topic)
	require.Equal(t, "profile", report.Command)
	require.Contains(t, report.Help, "OAuth provider profile")
	require.Contains(t, report.OutputFields, "active_profile")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"oauth", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "oauth", report.Topic)
	require.Equal(t, "oauth", report.Command)
	require.Contains(t, report.Help, "stored tokens")
	require.Contains(t, report.OutputFields, "token_present")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"metrics", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "metrics", report.Topic)
	require.Equal(t, "metrics", report.Command)
	require.Contains(t, report.Help, "usage metrics")
	require.Contains(t, report.OutputFields, "workspace_metrics")
	require.NotNil(t, report.LocalOnly)
	require.True(t, *report.LocalOnly)
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"workspace", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "workspace", report.Topic)
	require.Equal(t, "workspace", report.Command)
	require.Contains(t, report.Help, "runtime workspace")
	require.Contains(t, report.OutputFields, "session_dir")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"clear", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "clear", report.Topic)
	require.Equal(t, "clear", report.Command)
	require.Contains(t, report.Help, "fresh empty local session")
	require.Contains(t, report.OutputFields, "continue_commands")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"reset", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "reset", report.Topic)
	require.Equal(t, "reset", report.Command)
	require.Contains(t, report.Help, "configuration sections")
	require.Contains(t, report.OutputFields, "reset_keys")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"language", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "language", report.Topic)
	require.Equal(t, "language", report.Command)
	require.Contains(t, report.Help, "interface language")
	require.Contains(t, report.OutputFields, "language")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"state", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "state", report.Topic)
	require.Equal(t, "state", report.Command)
	require.Contains(t, report.Help, "Produces state")
	require.Contains(t, report.Help, "codog prompt <text>")
	require.Contains(t, report.OutputFields, "worker_id")
	require.Contains(t, report.StatusValues, "running")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"code-intel", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "code-intel", report.Topic)
	require.Equal(t, "code-intel", report.Command)
	require.Contains(t, report.Help, "notebook-read")
	require.Contains(t, report.Help, "lsp")
	require.Contains(t, report.OutputFields, "symbols")
	require.NotNil(t, report.RequiresProviderRequest)
	require.False(t, *report.RequiresProviderRequest)

	out.Reset()
	require.NoError(t, renderHelpCommand(&out, []string{"notebook-edit", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "notebook-edit", report.Topic)
	require.Equal(t, "notebook-edit", report.Command)
	require.Contains(t, report.Help, "code-intel notebook-edit")
	require.NotNil(t, report.MutatesWorkspace)
	require.True(t, *report.MutatesWorkspace)
}

func TestCommandHelpShortCircuitsBeforeConfigLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	cases := []struct {
		name  string
		args  []string
		topic string
	}{
		{
			name:  "doctor global format",
			args:  []string{"--config", configPath, "--output-format", "json", "doctor", "--help"},
			topic: "doctor",
		},
		{
			name:  "compact suffix format",
			args:  []string{"--config", configPath, "compact", "--help", "--output-format", "json"},
			topic: "compact",
		},
		{
			name:  "session suffix format",
			args:  []string{"--config", configPath, "session", "--help", "--output-format", "json"},
			topic: "session",
		},
		{
			name:  "resume global flag",
			args:  []string{"--resume", "--help", "--output-format", "json"},
			topic: "resume",
		},
		{
			name:  "prompt provider help",
			args:  []string{"--config", configPath, "prompt", "--help", "--output-format", "json"},
			topic: "prompt",
		},
		{
			name:  "code intel help",
			args:  []string{"--config", configPath, "code-intel", "--help", "--output-format", "json"},
			topic: "code-intel",
		},
		{
			name:  "slash notebook read help",
			args:  []string{"--config", configPath, "/notebook-read", "--help", "--output-format", "json"},
			topic: "notebook-read",
		},
		{
			name:  "speak local help",
			args:  []string{"--config", configPath, "speak", "--help", "--output-format", "json"},
			topic: "speak",
		},
		{
			name:  "mcp local help",
			args:  []string{"--config", configPath, "mcp", "--help", "--output-format", "json"},
			topic: "mcp",
		},
		{
			name:  "mcp addCommand local help",
			args:  []string{"--config", configPath, "addCommand", "--help", "--output-format", "json"},
			topic: "addCommand",
		},
		{
			name:  "plugin helper local help",
			args:  []string{"--config", configPath, "PluginErrors", "--help", "--output-format", "json"},
			topic: "PluginErrors",
		},
		{
			name:  "review helper local help",
			args:  []string{"--config", configPath, "ultrareviewEnabled", "--help", "--output-format", "json"},
			topic: "ultrareviewEnabled",
		},
		{
			name:  "good claude local help",
			args:  []string{"--config", configPath, "good-claude", "--help", "--output-format", "json"},
			topic: "good-claude",
		},
		{
			name:  "setupGitHubActions local help",
			args:  []string{"--config", configPath, "setupGitHubActions", "--help", "--output-format", "json"},
			topic: "setupGitHubActions",
		},
		{
			name:  "github app step local help",
			args:  []string{"--config", configPath, "ApiKeyStep", "--help", "--output-format", "json"},
			topic: "ApiKeyStep",
		},
		{
			name:  "api local help",
			args:  []string{"--config", configPath, "api", "--help", "--output-format", "json"},
			topic: "api",
		},
		{
			name:  "cache local help",
			args:  []string{"--config", configPath, "cache", "--help", "--output-format", "json"},
			topic: "cache",
		},
		{
			name:  "caches local help",
			args:  []string{"--config", configPath, "caches", "--help", "--output-format", "json"},
			topic: "caches",
		},
		{
			name:  "validation local help",
			args:  []string{"--config", configPath, "validation", "--help", "--output-format", "json"},
			topic: "validation",
		},
		{
			name:  "reviewRemote local help",
			args:  []string{"--config", configPath, "reviewRemote", "--help", "--output-format", "json"},
			topic: "reviewRemote",
		},
		{
			name:  "autofix-pr local help",
			args:  []string{"--config", configPath, "autofix-pr", "--help", "--output-format", "json"},
			topic: "autofix-pr",
		},
		{
			name:  "context-noninteractive local help",
			args:  []string{"--config", configPath, "context-noninteractive", "--help", "--output-format", "json"},
			topic: "context-noninteractive",
		},
		{
			name:  "conversation local help",
			args:  []string{"--config", configPath, "conversation", "--help", "--output-format", "json"},
			topic: "conversation",
		},
		{
			name:  "break-cache local help",
			args:  []string{"--config", configPath, "break-cache", "--help", "--output-format", "json"},
			topic: "break-cache",
		},
		{
			name:  "extra-usage core local help",
			args:  []string{"--config", configPath, "extra-usage-core", "--help", "--output-format", "json"},
			topic: "extra-usage-core",
		},
		{
			name:  "extra-usage noninteractive local help",
			args:  []string{"--config", configPath, "extra-usage-noninteractive", "--help", "--output-format", "json"},
			topic: "extra-usage-noninteractive",
		},
		{
			name:  "notifications local help",
			args:  []string{"--config", configPath, "notifications", "--help", "--output-format", "json"},
			topic: "notifications",
		},
		{
			name:  "api-key local help",
			args:  []string{"--config", configPath, "api-key", "--help", "--output-format", "json"},
			topic: "api-key",
		},
		{
			name:  "temperature local help",
			args:  []string{"--config", configPath, "temperature", "--help", "--output-format", "json"},
			topic: "temperature",
		},
		{
			name:  "telemetry local help",
			args:  []string{"--config", configPath, "telemetry", "--help", "--output-format", "json"},
			topic: "telemetry",
		},
		{
			name:  "effort local help",
			args:  []string{"--config", configPath, "effort", "--help", "--output-format", "json"},
			topic: "effort",
		},
		{
			name:  "reasoning local help",
			args:  []string{"--config", configPath, "reasoning", "--help", "--output-format", "json"},
			topic: "reasoning",
		},
		{
			name:  "rate-limit local help",
			args:  []string{"--config", configPath, "rate-limit", "--help", "--output-format", "json"},
			topic: "rate-limit",
		},
		{
			name:  "ant-trace provider help",
			args:  []string{"--config", configPath, "ant-trace", "--help", "--output-format", "json"},
			topic: "ant-trace",
		},
		{
			name:  "budget local help",
			args:  []string{"--config", configPath, "budget", "--help", "--output-format", "json"},
			topic: "budget",
		},
		{
			name:  "profile local help",
			args:  []string{"--config", configPath, "profile", "--help", "--output-format", "json"},
			topic: "profile",
		},
		{
			name:  "metrics local help",
			args:  []string{"--config", configPath, "metrics", "--help", "--output-format", "json"},
			topic: "metrics",
		},
		{
			name:  "perf-issue local help",
			args:  []string{"--config", configPath, "perf-issue", "--help", "--output-format", "json"},
			topic: "perf-issue",
		},
		{
			name:  "reset local help",
			args:  []string{"--config", configPath, "reset", "--help", "--output-format", "json"},
			topic: "reset",
		},
		{
			name:  "settings alias help",
			args:  []string{"--config", configPath, "settings", "--help", "--output-format", "json"},
			topic: "settings",
		},
		{
			name:  "workspace local help",
			args:  []string{"--config", configPath, "workspace", "--help", "--output-format", "json"},
			topic: "workspace",
		},
		{
			name:  "memory local help",
			args:  []string{"--config", configPath, "memory", "--help", "--output-format", "json"},
			topic: "memory",
		},
		{
			name:  "keybindings local help",
			args:  []string{"--config", configPath, "keybindings", "--help", "--output-format", "json"},
			topic: "keybindings",
		},
		{
			name:  "language local help",
			args:  []string{"--config", configPath, "language", "--help", "--output-format", "json"},
			topic: "language",
		},
		{
			name:  "rate-limit-options local help",
			args:  []string{"--config", configPath, "rate-limit-options", "--help", "--output-format", "json"},
			topic: "rate-limit-options",
		},
		{
			name:  "mock-limits local help",
			args:  []string{"--config", configPath, "mock-limits", "--help", "--output-format", "json"},
			topic: "mock-limits",
		},
		{
			name:  "reset-limits local help",
			args:  []string{"--config", configPath, "reset-limits", "--help", "--output-format", "json"},
			topic: "reset-limits",
		},
		{
			name:  "generateSessionName local help",
			args:  []string{"--config", configPath, "generateSessionName", "--help", "--output-format", "json"},
			topic: "generateSessionName",
		},
		{
			name:  "onboarding local help",
			args:  []string{"--config", configPath, "onboarding", "--help", "--output-format", "json"},
			topic: "onboarding",
		},
		{
			name:  "state local help",
			args:  []string{"--config", configPath, "state", "--help", "--output-format", "json"},
			topic: "state",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), tc.args, config.FlagOverrides{})
			})
			require.NoError(t, err)
			var report helpReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "help", report.Kind)
			require.Equal(t, "help", report.Action)
			require.Equal(t, "ok", report.Status)
			require.Equal(t, tc.topic, report.Topic)
			require.NotContains(t, out, "config_parse_error")
			require.NotContains(t, out, "missing_credentials")
		})
	}

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "doctor", "--help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "Doctor\n"))
	require.Contains(t, out, "no provider request or session resume required")
}

func TestCapabilitiesCommandOutputsTextAndJSON(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "claude-test",
			PermissionMode:      "read-only",
			AutoCompactMessages: 12,
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Capabilities(nil))
	require.Contains(t, out.String(), "Codog Capabilities")
	require.Contains(t, out.String(), "Tool aliases")
	require.Contains(t, out.String(), "MCP local data")
	out.Reset()

	require.NoError(t, app.Capabilities([]string{"--json"}))
	var report capabilitiesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "capabilities", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "claude-test", report.Model)
	require.Equal(t, "read-only", report.PermissionMode)
	require.Contains(t, report.Commands, "prompt")
	require.Contains(t, report.Commands, "addCommand")
	require.Contains(t, report.Commands, "ant-trace")
	require.Contains(t, report.Commands, "api")
	require.Contains(t, report.Commands, "ApiKeyStep")
	require.Contains(t, report.Commands, "break-cache")
	require.Contains(t, report.Commands, "caches")
	require.Contains(t, report.Commands, "CheckExistingSecretStep")
	require.Contains(t, report.Commands, "CheckGitHubStep")
	require.Contains(t, report.Commands, "ChooseRepoStep")
	require.Contains(t, report.Commands, "CreatingStep")
	require.Contains(t, report.Commands, "extra-usage-core")
	require.Contains(t, report.Commands, "extra-usage-noninteractive")
	require.Contains(t, report.Commands, "ExistingWorkflowStep")
	require.Contains(t, report.Commands, "ErrorStep")
	require.Contains(t, report.Commands, "exit")
	require.Contains(t, report.Commands, "autofix-pr")
	require.Contains(t, report.Commands, "InstallAppStep")
	require.Contains(t, report.Commands, "resume")
	require.Contains(t, report.Commands, "session")
	require.Contains(t, report.Commands, "clear")
	require.Contains(t, report.Commands, "context-noninteractive")
	require.Contains(t, report.Commands, "conversation")
	require.Contains(t, report.Commands, "createMovedToPluginCommand")
	require.Contains(t, report.Commands, "validation")
	require.Contains(t, report.Commands, "reviewRemote")
	require.Contains(t, report.Commands, "permissions")
	require.Contains(t, report.Commands, "plan")
	require.Contains(t, report.Commands, "teleport")
	require.Contains(t, report.Commands, "bridge")
	require.Contains(t, report.Commands, "setupGitHubActions")
	require.Contains(t, report.Commands, "bridge-kick")
	require.Contains(t, report.Commands, "cron")
	require.Contains(t, report.Commands, "team")
	require.Contains(t, report.Commands, "budget")
	require.Contains(t, report.Commands, "capabilities")
	require.Contains(t, report.Commands, "bug")
	require.Contains(t, report.Commands, "checkpoint")
	require.Contains(t, report.Commands, "generateSessionName")
	require.Contains(t, report.Commands, "good-claude")
	require.Contains(t, report.Commands, "language")
	require.Contains(t, report.Commands, "metrics")
	require.Contains(t, report.Commands, "mock-limits")
	require.Contains(t, report.Commands, "onboarding")
	require.Contains(t, report.Commands, "perf-issue")
	require.Contains(t, report.Commands, "AddMarketplace")
	require.Contains(t, report.Commands, "BrowseMarketplace")
	require.Contains(t, report.Commands, "DiscoverPlugins")
	require.Contains(t, report.Commands, "ManageMarketplaces")
	require.Contains(t, report.Commands, "ManagePlugins")
	require.Contains(t, report.Commands, "PluginErrors")
	require.Contains(t, report.Commands, "PluginOptionsDialog")
	require.Contains(t, report.Commands, "PluginOptionsFlow")
	require.Contains(t, report.Commands, "PluginSettings")
	require.Contains(t, report.Commands, "PluginTrustWarning")
	require.Contains(t, report.Commands, "UnifiedInstalledCell")
	require.Contains(t, report.Commands, "ValidatePlugin")
	require.Contains(t, report.Commands, "parseArgs")
	require.Contains(t, report.Commands, "pluginDetailsHelpers")
	require.Contains(t, report.Commands, "profile")
	require.Contains(t, report.Commands, "rc")
	require.Contains(t, report.Commands, "rate-limit")
	require.Contains(t, report.Commands, "reasoning")
	require.Contains(t, report.Commands, "reset")
	require.Contains(t, report.Commands, "settings")
	require.Contains(t, report.Commands, "skill")
	require.Contains(t, report.Commands, "temperature")
	require.Contains(t, report.Commands, "telemetry")
	require.Contains(t, report.Commands, "workspace")
	require.Contains(t, report.Commands, "OAuthFlowStep")
	require.Contains(t, report.Commands, "SuccessStep")
	require.Contains(t, report.Commands, "WarningsStep")
	require.Contains(t, report.Commands, "usePagination")
	require.Contains(t, report.Commands, "ultrareviewCommand")
	require.Contains(t, report.Commands, "ultrareviewEnabled")
	require.Contains(t, report.Commands, "UltrareviewOverageDialog")
	require.Contains(t, report.Commands, "xaaIdpCommand")
	require.Contains(t, report.Commands, "cwd")
	require.Contains(t, report.Commands, "tool-details")
	require.Contains(t, report.Features, "approval_tokens")
	require.Contains(t, report.Features, "broad_cwd_guard")
	require.Contains(t, report.Features, "config_load_degraded")
	require.Contains(t, report.Features, "config_reset")
	require.Contains(t, report.Features, "doctor_config_load_degraded")
	require.Contains(t, report.Features, "doctor_config_validation")
	require.Contains(t, report.Features, "hooks_health")
	require.Contains(t, report.Features, "interface_language")
	require.Contains(t, report.Features, "lane_event_projection")
	require.Contains(t, report.Features, "mcp_server")
	require.Contains(t, report.Features, "mcp_config_load_degraded")
	require.Contains(t, report.Features, "metrics")
	require.Contains(t, report.Features, "policy_engine")
	require.Contains(t, report.Features, "plugins_config_load_degraded")
	require.Contains(t, report.Features, "providers_config_load_degraded")
	require.Contains(t, report.Features, "sampling_temperature")
	require.Contains(t, report.Features, "recovery_recipes_ledger")
	require.Contains(t, report.Features, "resume_safe_slash_metadata")
	require.Contains(t, report.Features, "session_identity_metadata")
	require.Contains(t, report.Features, "session_identity_reconciliation")
	require.Contains(t, report.Features, "stale_branch_guard")
	require.Contains(t, report.Features, "status_config_load_degraded")
	require.Contains(t, report.Features, "status_config_validation")
	require.Contains(t, report.Features, "team_watch")
	require.Contains(t, report.Features, "telemetry_preferences")
	require.Contains(t, report.Features, "typed_task_packets")
	require.Contains(t, report.Features, "worker_startup_no_evidence")
	require.Contains(t, report.Features, "workspace_switch")
	require.Contains(t, report.Protocols, "mcp_stdio_server")
	require.Contains(t, report.OutputFormats, "stream-json")
	require.Greater(t, report.CommandCount, 20)
	require.Greater(t, report.SlashCommandCount, 20)
	require.Greater(t, report.ToolCount, 10)
	require.Greater(t, report.ToolAliasCount, 40)
	statusSlash, ok := capabilityReportSlash(report, "/status")
	require.True(t, ok)
	require.True(t, statusSlash.ResumeSupported)
	advisorSlash, ok := capabilityReportSlash(report, "/advisor")
	require.True(t, ok)
	require.True(t, advisorSlash.ResumeSupported)
	systemPromptSlash, ok := capabilityReportSlash(report, "/system-prompt")
	require.True(t, ok)
	require.True(t, systemPromptSlash.ResumeSupported)
	toolDetailsSlash, ok := capabilityReportSlash(report, "/tool-details")
	require.True(t, ok)
	require.True(t, toolDetailsSlash.ResumeSupported)
	debugToolCallSlash, ok := capabilityReportSlash(report, "/debug-tool-call")
	require.True(t, ok)
	require.True(t, debugToolCallSlash.ResumeSupported)
	oauthSlash, ok := capabilityReportSlash(report, "/oauth")
	require.True(t, ok)
	require.True(t, oauthSlash.ResumeSupported)
	cronSlash, ok := capabilityReportSlash(report, "/cron")
	require.True(t, ok)
	require.True(t, cronSlash.ResumeSupported)
	teamSlash, ok := capabilityReportSlash(report, "/team")
	require.True(t, ok)
	require.True(t, teamSlash.ResumeSupported)
	hooksSlash, ok := capabilityReportSlash(report, "/hooks")
	require.True(t, ok)
	require.True(t, hooksSlash.ResumeSupported)
	skillSlash, ok := capabilityReportSlash(report, "/skill")
	require.True(t, ok)
	require.True(t, skillSlash.ResumeSupported)
	resetSlash, ok := capabilityReportSlash(report, "/reset")
	require.True(t, ok)
	require.True(t, resetSlash.ResumeSupported)
	planSlash, ok := capabilityReportSlash(report, "/plan")
	require.True(t, ok)
	require.True(t, planSlash.ResumeSupported)
	setupSlash, ok := capabilityReportSlash(report, "/setup")
	require.True(t, ok)
	require.True(t, setupSlash.ResumeSupported)
	sandboxToggleSlash, ok := capabilityReportSlash(report, "/sandbox-toggle")
	require.True(t, ok)
	require.True(t, sandboxToggleSlash.ResumeSupported)
	speakSlash, ok := capabilityReportSlash(report, "/speak")
	require.True(t, ok)
	require.True(t, speakSlash.ResumeSupported)
	terminalSetupSlash, ok := capabilityReportSlash(report, "/terminal-setup")
	require.True(t, ok)
	require.True(t, terminalSetupSlash.ResumeSupported)
	terminalSetupAliasSlash, ok := capabilityReportSlash(report, "/terminalSetup")
	require.True(t, ok)
	require.True(t, terminalSetupAliasSlash.ResumeSupported)
	remoteEnvSlash, ok := capabilityReportSlash(report, "/remote-env")
	require.True(t, ok)
	require.True(t, remoteEnvSlash.ResumeSupported)
	remoteSetupSlash, ok := capabilityReportSlash(report, "/remote-setup")
	require.True(t, ok)
	require.True(t, remoteSetupSlash.ResumeSupported)
	webSetupSlash, ok := capabilityReportSlash(report, "/web-setup")
	require.True(t, ok)
	require.True(t, webSetupSlash.ResumeSupported)
	voiceSlash, ok := capabilityReportSlash(report, "/voice")
	require.True(t, ok)
	require.True(t, voiceSlash.ResumeSupported)
	extraUsageSlash, ok := capabilityReportSlash(report, "/extra-usage")
	require.True(t, ok)
	require.True(t, extraUsageSlash.ResumeSupported)
	installSlackAppSlash, ok := capabilityReportSlash(report, "/install-slack-app")
	require.True(t, ok)
	require.True(t, installSlackAppSlash.ResumeSupported)
	stickersSlash, ok := capabilityReportSlash(report, "/stickers")
	require.True(t, ok)
	require.True(t, stickersSlash.ResumeSupported)
	passesSlash, ok := capabilityReportSlash(report, "/passes")
	require.True(t, ok)
	require.True(t, passesSlash.ResumeSupported)
	thinkBackSlash, ok := capabilityReportSlash(report, "/think-back")
	require.True(t, ok)
	require.True(t, thinkBackSlash.ResumeSupported)
	thinkbackSlash, ok := capabilityReportSlash(report, "/thinkback")
	require.True(t, ok)
	require.True(t, thinkbackSlash.ResumeSupported)
	thinkbackPlaySlash, ok := capabilityReportSlash(report, "/thinkback-play")
	require.True(t, ok)
	require.True(t, thinkbackPlaySlash.ResumeSupported)
	reloadPluginsSlash, ok := capabilityReportSlash(report, "/reload-plugins")
	require.True(t, ok)
	require.True(t, reloadPluginsSlash.ResumeSupported)
	heapdumpSlash, ok := capabilityReportSlash(report, "/heapdump")
	require.True(t, ok)
	require.True(t, heapdumpSlash.ResumeSupported)
	commitSlash, ok := capabilityReportSlash(report, "/commit")
	require.True(t, ok)
	require.False(t, commitSlash.ResumeSupported)
	require.Equal(t, len(report.ToolAliases), report.ToolAliasCount)
	require.Equal(t, "read_file", report.ToolAliases["Read"])
	require.Equal(t, "read_file", report.ToolAliases["FileReadTool"])
	require.Equal(t, "bash", report.ToolAliases["BashTool"])
	require.Equal(t, "tool_search", report.ToolAliases["ToolSearchTool"])
	readTool, ok := capabilityReportTool(report, "read_file")
	require.True(t, ok)
	require.Contains(t, readTool.Aliases, "Read")
	require.Contains(t, readTool.Aliases, "FileReadTool")
	patchTool, ok := capabilityReportTool(report, "apply_patch")
	require.True(t, ok)
	require.Contains(t, patchTool.Aliases, "ApplyPatch")
	bashTool, ok := capabilityReportTool(report, "bash")
	require.True(t, ok)
	require.Contains(t, bashTool.Aliases, "Bash")
	require.Contains(t, bashTool.Aliases, "BashTool")
	require.Equal(t, 3, report.MCP.LocalResourceCount)
	require.Equal(t, 1, report.MCP.LocalTemplateCount)
	require.Equal(t, 3, report.MCP.LocalPromptCount)
	require.Greater(t, report.MCP.ExposedToolCount, 10)
	require.True(t, capabilityReportHasTool(report, "read_file"))
	require.True(t, capabilityReportHasSlash(report, "/ant-trace"))
	require.True(t, capabilityReportHasSlash(report, "/bug"))
	require.True(t, capabilityReportHasSlash(report, "/generateSessionName"))
	require.True(t, capabilityReportHasSlash(report, "/onboarding"))
	require.True(t, capabilityReportHasSlash(report, "/capabilities"))
	require.True(t, capabilityReportHasSlash(report, "/checkpoint"))
	require.True(t, capabilityReportHasSlash(report, "/new"))
	require.True(t, capabilityReportHasSlash(report, "/quit"))
	require.True(t, capabilityReportHasSlash(report, "/rc"))
	require.True(t, capabilityReportHasSlash(report, "/settings"))
	require.True(t, capabilityReportHasSlash(report, "/skill"))
	require.True(t, capabilityReportHasSlash(report, "/workspace"))
	require.True(t, capabilityReportHasMCPResource(report, "codog://workspace"))
	require.True(t, capabilityReportHasMCPPrompt(report, "review_changes"))
	require.True(t, commandAcceptsGlobalOutputFormat("addCommand"))
	require.True(t, commandAcceptsGlobalOutputFormat("ant-trace"))
	require.True(t, commandAcceptsGlobalOutputFormat("ApiKeyStep"))
	require.True(t, commandAcceptsGlobalOutputFormat("capabilities"))
	require.True(t, commandAcceptsGlobalOutputFormat("CheckGitHubStep"))
	require.True(t, commandAcceptsGlobalOutputFormat("code-intel"))
	require.True(t, commandAcceptsGlobalOutputFormat("CreatingStep"))
	require.True(t, commandAcceptsGlobalOutputFormat("ExistingWorkflowStep"))
	require.True(t, commandAcceptsGlobalOutputFormat("generateSessionName"))
	require.True(t, commandAcceptsGlobalOutputFormat("good-claude"))
	require.True(t, commandAcceptsGlobalOutputFormat("SuccessStep"))
	require.True(t, commandAcceptsGlobalOutputFormat("onboarding"))
	require.True(t, commandAcceptsGlobalOutputFormat("AddMarketplace"))
	require.True(t, commandAcceptsGlobalOutputFormat("BrowseMarketplace"))
	require.True(t, commandAcceptsGlobalOutputFormat("DiscoverPlugins"))
	require.True(t, commandAcceptsGlobalOutputFormat("ManageMarketplaces"))
	require.True(t, commandAcceptsGlobalOutputFormat("ManagePlugins"))
	require.True(t, commandAcceptsGlobalOutputFormat("PluginErrors"))
	require.True(t, commandAcceptsGlobalOutputFormat("PluginSettings"))
	require.True(t, commandAcceptsGlobalOutputFormat("PluginTrustWarning"))
	require.True(t, commandAcceptsGlobalOutputFormat("ValidatePlugin"))
	require.True(t, commandAcceptsGlobalOutputFormat("settings"))
	require.True(t, commandAcceptsGlobalOutputFormat("skill"))
	require.True(t, commandAcceptsGlobalOutputFormat("ultrareviewEnabled"))
	require.True(t, commandAcceptsGlobalOutputFormat("xaaIdpCommand"))
	require.True(t, commandAcceptsGlobalOutputFormat("bug"))
	require.True(t, commandAcceptsGlobalOutputFormat("checkpoint"))
	require.True(t, commandAcceptsGlobalOutputFormat("notebook-read"))
	require.True(t, commandAcceptsGlobalOutputFormat("notebook-edit"))
	require.True(t, commandAcceptsGlobalOutputFormat("workspace"))
	require.True(t, commandAcceptsGlobalOutputFormat("cwd"))
}

func TestReasoningCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "reasoning", "high", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reasoning"`)
	require.Contains(t, out, `"effort": "high"`)

	storedConfigPath := filepath.Join(configHome, "config.json")
	stored, err := os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"reasoning_effort": "high"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "--output-format", "json", "reasoning"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reasoning"`)
	require.Contains(t, out, `"effort": "high"`)
	require.True(t, commandAcceptsGlobalOutputFormat("reasoning"))
}

func TestResetCommandResetsConfigSections(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "custom-model",
		"max_tokens": 123,
		"language": "Japanese",
		"theme": "dark"
	}`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "model"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reset"`)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"section": "model"`)
	require.Contains(t, out, `"model"`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"model"`)
	require.NotContains(t, string(stored), `"max_tokens"`)
	require.Contains(t, string(stored), `"language": "Japanese"`)
	require.Contains(t, string(stored), `"theme": "dark"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "all"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"confirm_required": true`)
	require.FileExists(t, configPath)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "reset", "all", "--confirm"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"section": "all"`)
	require.NoFileExists(t, configPath)
	require.True(t, commandAcceptsGlobalOutputFormat("reset"))
}

func TestConfigResetSubcommandResetsExplicitConfigSection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "custom-model",
		"language": "Japanese",
		"theme": "dark",
		"editorMode": "vim"
	}`), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "config", "reset", "interface"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "reset"`)
	require.Contains(t, out, `"section": "interface"`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"model": "custom-model"`)
	require.NotContains(t, string(stored), `"language"`)
	require.NotContains(t, string(stored), `"theme"`)
	require.NotContains(t, string(stored), `"editorMode"`)
}

func TestLanguageCommandPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "language", "Japanese", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "language"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"configured": true`)
	require.Contains(t, out, `"language": "Japanese"`)

	storedConfigPath := filepath.Join(configHome, "config.json")
	stored, err := os.ReadFile(storedConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"language": "Japanese"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", storedConfigPath, "--output-format", "json", "language", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "language"`)
	require.Contains(t, out, `"action": "status"`)
	require.Contains(t, out, `"language": "Japanese"`)
	require.True(t, commandAcceptsGlobalOutputFormat("language"))
}

func TestRateLimitCommandSetsShowsAndResetsConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"rate-limit", "set",
			"--path", configPath,
			"--max-retries", "5",
			"--initial-backoff-ms", "125",
			"--max-backoff-ms", "750",
			"--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "rate_limit"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"max_retries": 5`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"rate_limit"`)
	require.Contains(t, string(stored), `"max_retries": 5`)
	require.Contains(t, string(stored), `"initial_backoff_ms": 125`)
	require.Contains(t, string(stored), `"max_backoff_ms": 750`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "rate-limit", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "rate_limit"`)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"max_retries": 5`)
	require.True(t, commandAcceptsGlobalOutputFormat("rate-limit"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "rate-limit", "reset", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"previous"`)

	stored, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"rate_limit"`)
}

func TestBudgetCommandSetsShowsAndResetsConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"budget", "set",
			"--path", configPath,
			"--max-tokens", "8192",
			"--max-turns", "12",
			"--json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "budget"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"max_tokens": 8192`)
	require.Contains(t, out, `"max_turns": 12`)

	stored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"max_tokens": 8192`)
	require.Contains(t, string(stored), `"max_turns": 12`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "budget", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "budget"`)
	require.Contains(t, out, `"action": "show"`)
	require.Contains(t, out, `"max_tokens": 8192`)
	require.Contains(t, out, `"max_turns": 12`)
	require.True(t, commandAcceptsGlobalOutputFormat("budget"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "budget", "reset", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"action": "reset"`)
	require.Contains(t, out, `"previous"`)

	stored, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(stored), `"max_tokens"`)
	require.NotContains(t, string(stored), `"max_turns"`)
}

func TestUnknownCommandOutputContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "statuz"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report commandNotFoundReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "command_not_found", report.Kind)
	require.Equal(t, "command_not_found", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "statuz", report.Command)
	require.Contains(t, report.Hint, "status")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "foobar", "baz"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "command_not_found")
	require.Contains(t, err.Error(), "codog prompt")
}

func TestDirectSlashCLIContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var statusReport map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &statusReport))
	require.Equal(t, "status", statusReport["kind"])

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/settings", "paths"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var settingsReport map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &settingsReport))
	require.NotEmpty(t, settingsReport["paths"])

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/settings", "--help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var settingsHelp helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &settingsHelp))
	require.Equal(t, "config", settingsHelp.Topic)
	require.Equal(t, "config", settingsHelp.Command)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/model"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var directModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &directModel))
	require.Equal(t, "model", directModel.Kind)
	require.Equal(t, "show", directModel.Action)
	require.NotEmpty(t, directModel.Model)
	require.True(t, commandAcceptsGlobalOutputFormat("model"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "model", "claude-json", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var setModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &setModel))
	require.Equal(t, "set", setModel.Action)
	require.Equal(t, "claude-json", setModel.Model)
	require.NotEmpty(t, setModel.Previous)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/max-tokens"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var directMaxTokens maxTokensReport
	require.NoError(t, json.Unmarshal([]byte(out), &directMaxTokens))
	require.Equal(t, "max_tokens", directMaxTokens.Kind)
	require.Equal(t, "show", directMaxTokens.Action)
	require.True(t, commandAcceptsGlobalOutputFormat("max-tokens"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "max-turns", "11", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var setMaxTurns maxTurnsReport
	require.NoError(t, json.Unmarshal([]byte(out), &setMaxTurns))
	require.Equal(t, "max_turns", setMaxTurns.Kind)
	require.Equal(t, "set", setMaxTurns.Action)
	require.Equal(t, 11, setMaxTurns.MaxTurns)
	require.NotNil(t, setMaxTurns.PreviousMaxTurns)
	require.True(t, commandAcceptsGlobalOutputFormat("max-turns"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/permissions"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var directPermissions permissionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &directPermissions))
	require.Equal(t, "permissions", directPermissions.Kind)
	require.Equal(t, "show", directPermissions.Action)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/allowed-tools"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var directAllowedTools allowedToolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &directAllowedTools))
	require.Equal(t, "allowed_tools", directAllowedTools.Kind)
	require.Equal(t, "list", directAllowedTools.Action)

	for _, command := range []string{"/version", "/sandbox", "/diff"} {
		out, err = captureStdout(t, func() error {
			return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", command}, config.FlagOverrides{})
		})
		require.NoError(t, err, command)
		var localReport map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &localReport), command)
		require.NotEqual(t, "interactive_only", localReport["error_kind"], command)
		require.NotEmpty(t, localReport["kind"], command)
		require.NotEmpty(t, localReport["status"], command)
	}

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/statuz"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var slashReport slashErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "unknown_slash_command", slashReport.ErrorKind)
	require.Equal(t, "/statuz", slashReport.Command)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/approve"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "interactive_only", slashReport.ErrorKind)
	require.Equal(t, "/approve", slashReport.Command)
	require.NotContains(t, slashReport.Hint, "--resume")

	for _, command := range []string{"/commit", "/pr", "/issue", "/bughunter", "/new", "/quit", "/ultraplan"} {
		out, err = captureStdout(t, func() error {
			return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", command}, config.FlagOverrides{})
		})
		require.Error(t, err, command)
		require.ErrorAs(t, err, &exitErr, command)
		require.True(t, exitErr.Silent, command)
		require.NoError(t, json.Unmarshal([]byte(out), &slashReport), command)
		require.Equal(t, "interactive_only", slashReport.ErrorKind, command)
		require.Equal(t, command, slashReport.Command, command)
		require.NotContains(t, slashReport.Hint, "--resume", command)
	}

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/compact"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "interactive_only", slashReport.ErrorKind)
	require.Equal(t, "/compact", slashReport.Command)
	require.Contains(t, slashReport.Hint, "--resume")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/clear"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "interactive_only", slashReport.ErrorKind)
	require.Equal(t, "/clear", slashReport.Command)
	require.Contains(t, slashReport.Hint, "--resume")
}

func TestResumedSlashCLIContracts(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", "")
	var oauthServer *httptest.Server
	oauthServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + oauthServer.URL + `/authorize","token_endpoint":"` + oauthServer.URL + `/token"}`))
	}))
	t.Cleanup(oauthServer.Close)
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", oauthServer.URL, "client-resume", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "resume-oauth-access-1234",
		RefreshToken: "resume-oauth-refresh-1234",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	codePath := filepath.Join(workspace, "main.go")
	require.NoError(t, os.WriteFile(codePath, []byte(`package main

func main() {
	println(helper())
}

func helper() string {
	return "ok"
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(`{
  "cells": [
    {"cell_type":"code","id":"resume-cell","metadata":{},"source":["print(1)\n"],"outputs":[],"execution_count":null}
  ],
  "metadata": {"kernelspec":{"language":"python"}}
}`), 0o644))
	signalPath := filepath.Join(workspace, "signals.go")
	require.NoError(t, os.WriteFile(signalPath, []byte(`package main

import "os"

func risky(value any) {
	_ = os.WriteFile("tmp.txt", []byte("before"), 0o644)
	var secret = "1234567890abcdef"
	println(secret)
	println(value.(string))
}
`), 0o644))
	gitAvailable := false
	if _, err := exec.LookPath("git"); err == nil {
		gitAvailable = true
		runGit(t, workspace, "init")
		runGit(t, workspace, "config", "user.email", "codog@example.test")
		runGit(t, workspace, "config", "user.name", "Codog Test")
		trackedPath := filepath.Join(workspace, "tracked.txt")
		require.NoError(t, os.WriteFile(trackedPath, []byte("before\n"), 0o644))
		runGit(t, workspace, "add", "tracked.txt", "main.go", "signals.go")
		runGit(t, workspace, "commit", "-m", "initial")
		runGit(t, workspace, "tag", "v0.1.0")
		require.NoError(t, os.WriteFile(trackedPath, []byte("before\nafter\n"), 0o644))
		require.NoError(t, os.WriteFile(signalPath, []byte(`package main

import "os"

func risky(value any) {
	_ = os.WriteFile("tmp.txt", []byte("after"), 0o644)
	var secret = "1234567890abcdef"
	println(secret)
	println(value.(string))
	panic("boom")
}
`), 0o644))
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":           configHome,
		"auto_compact_messages": 2,
		"advisor_model":         "claude-advisor",
		"model":                 "claude-test",
		"api_key":               "test-key",
		"max_tokens":            1000,
		"max_turns":             3,
		"permission_mode":       "workspace-write",
		"permission_rules": map[string]any{
			"allow": []string{"read_file"},
		},
		"temperature": 0.4,
		"rate_limit": map[string]any{
			"max_retries": 4,
		},
		"language":         "Japanese",
		"theme":            "dark",
		"reasoning_effort": "high",
		"fast_mode":        true,
		"speech_command":   "codog-test-speech-helper",
		"voice_command":    "codog-test-voice-helper",
		"voice_enabled":    true,
		"editorMode":       "vim",
		"privacy_settings": map[string]any{
			"telemetry_enabled":      true,
			"prompt_history_enabled": false,
		},
		"future": map[string]any{
			"chrome_default_enabled": true,
			"notifications_enabled":  false,
			"remote_auth_token":      "remote-secret",
			"remote_enabled":         true,
			"remote_lease_seconds":   45,
			"sandbox_strategy":       "detect",
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("resume-slash", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("resume-slash", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("resume-slash", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("resume-slash", anthropic.TextMessage("assistant", "four")))
	cronEntry, err := cron.NewStore(configHome).Create("@daily", "resume cron", "daily check")
	require.NoError(t, err)
	teamEntry, err := team.NewStore(configHome).Create("resume-team", []team.TaskSpec{{
		Prompt: "check missing worker",
		TaskID: "missing-task",
	}}, []string{"missing-task"})
	require.NoError(t, err)
	_, err = planmode.Enter(workspace, "inspect before editing")
	require.NoError(t, err)
	terminalProfilePath := filepath.Join(workspace, ".zshrc")
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "resume-demo")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"resume-demo","tools":[{"name":"resume_demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	runResumedJSON := func(command string, args ...string) (string, error) {
		t.Helper()
		cliArgs := []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", command}
		cliArgs = append(cliArgs, args...)
		return captureStdout(t, func() error {
			return RunCLI(context.Background(), cliArgs, config.FlagOverrides{})
		})
	}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var statusReport struct {
		Kind    string `json:"kind"`
		Session struct {
			Active       bool   `json:"active"`
			ID           string `json:"id"`
			MessageCount int    `json:"message_count"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &statusReport))
	require.Equal(t, "status", statusReport.Kind)
	require.True(t, statusReport.Session.Active)
	require.Equal(t, "resume-slash", statusReport.Session.ID)
	require.Equal(t, 4, statusReport.Session.MessageCount)

	exportPath := filepath.Join(workspace, "resume-export.json")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "/export", exportPath, "--format", "json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var exportReport struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		Format    string `json:"format"`
		Messages  int    `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &exportReport))
	require.Equal(t, "resume-slash", exportReport.SessionID)
	require.Equal(t, exportPath, exportReport.File)
	require.Equal(t, "json", exportReport.Format)
	require.Equal(t, 4, exportReport.Messages)
	exported, err := os.ReadFile(exportPath)
	require.NoError(t, err)
	require.Contains(t, string(exported), `"id": "resume-slash"`)
	require.Contains(t, string(exported), `"text": "four"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "/compact", "--keep", "2", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var compactReport session.ReplaceResult
	require.NoError(t, json.Unmarshal([]byte(out), &compactReport))
	require.Equal(t, "resume-slash", compactReport.SessionID)
	require.Equal(t, 4, compactReport.OriginalMessages)
	require.Equal(t, 3, compactReport.RemainingMessages)
	opened, err := store.Open("resume-slash")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 3)

	shareDir := filepath.Join(workspace, "shared")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/share", "--output-dir", shareDir, "--format", "json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var shareReport shareReport
	require.NoError(t, json.Unmarshal([]byte(out), &shareReport))
	require.Equal(t, "resume-slash", shareReport.SessionID)
	require.Equal(t, "json", shareReport.Format)
	require.Equal(t, 3, shareReport.Messages)
	require.FileExists(t, filepath.Join(shareDir, "resume-slash.json"))

	var copied []byte
	previousClipboard := writeClipboard
	writeClipboard = func(_ context.Context, data []byte) (string, error) {
		copied = append([]byte(nil), data...)
		return "resume-test-clipboard", nil
	}
	t.Cleanup(func() { writeClipboard = previousClipboard })
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/copy"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var copyReport copyReport
	require.NoError(t, json.Unmarshal([]byte(out), &copyReport))
	require.Equal(t, "resume-slash", copyReport.SessionID)
	require.Equal(t, "resume-test-clipboard", copyReport.Clipboard)
	require.Equal(t, "four\n", string(copied))

	out, err = runResumedJSON("/help", "status")
	require.NoError(t, err)
	var resumedHelp helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHelp))
	require.Equal(t, "help", resumedHelp.Kind)
	require.Equal(t, "status", resumedHelp.Topic)
	require.Equal(t, "status", resumedHelp.Command)

	out, err = runResumedJSON("/init")
	require.NoError(t, err)
	var resumedInit struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedInit))
	require.Equal(t, "init", resumedInit.Kind)
	require.Equal(t, "init", resumedInit.Action)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))

	out, err = runResumedJSON("/memory", "list")
	require.NoError(t, err)
	var resumedMemory memory.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMemory))
	require.Equal(t, "memory", resumedMemory.Kind)
	require.Equal(t, "list", resumedMemory.Action)
	require.GreaterOrEqual(t, resumedMemory.InstructionFiles, 1)

	out, err = runResumedJSON("/version")
	require.NoError(t, err)
	var resumedVersion struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVersion))
	require.Equal(t, "version", resumedVersion.Kind)
	require.Equal(t, "show", resumedVersion.Action)
	require.Equal(t, "ok", resumedVersion.Status)

	out, err = runResumedJSON("/config", "paths")
	require.NoError(t, err)
	var configPaths struct {
		Paths []string `json:"paths"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &configPaths))
	require.NotEmpty(t, configPaths.Paths)

	out, err = runResumedJSON("/settings", "paths")
	require.NoError(t, err)
	var settingsPaths struct {
		Paths []string `json:"paths"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &settingsPaths))
	require.Equal(t, configPaths.Paths, settingsPaths.Paths)

	out, err = runResumedJSON("/api")
	require.NoError(t, err)
	var resumedAPI apiReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAPI))
	require.Equal(t, "api", resumedAPI.Kind)
	require.Equal(t, "routes", resumedAPI.Action)

	out, err = runResumedJSON("/api-key")
	require.NoError(t, err)
	var resumedAPIKey apiKeyReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAPIKey))
	require.Equal(t, "api_key", resumedAPIKey.Kind)
	require.Equal(t, "status", resumedAPIKey.Action)
	require.True(t, resumedAPIKey.Configured)
	require.NotContains(t, out, "test-key")

	out, err = runResumedJSON("/providers")
	require.NoError(t, err)
	var resumedProviders providersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProviders))
	require.Equal(t, "providers", resumedProviders.Kind)
	require.Equal(t, "status", resumedProviders.Action)
	require.Equal(t, "claude-test", resumedProviders.Active.Model)

	out, err = runResumedJSON("/oauth")
	require.NoError(t, err)
	var resumedOAuthStatus oauth.Status
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthStatus))
	require.Equal(t, "default", resumedOAuthStatus.ProfileName)
	require.True(t, resumedOAuthStatus.ProfileConfigured)
	require.True(t, resumedOAuthStatus.TokenPresent)
	require.True(t, resumedOAuthStatus.Ready)
	require.NotContains(t, out, "resume-oauth-access-1234")

	out, err = runResumedJSON("/oauth", "status", "default", "--json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthStatus))
	require.Equal(t, "default", resumedOAuthStatus.ProfileName)
	require.True(t, resumedOAuthStatus.Ready)

	out, err = runResumedJSON("/oauth", "provider", "list")
	require.NoError(t, err)
	var resumedOAuthProfiles []oauth.ProviderProfile
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProfiles))
	require.Len(t, resumedOAuthProfiles, 1)
	require.Equal(t, "default", resumedOAuthProfiles[0].Name)
	require.Equal(t, "client-resume", resumedOAuthProfiles[0].ClientID)

	out, err = runResumedJSON("/oauth", "provider", "show", "default")
	require.NoError(t, err)
	var resumedOAuthProfile oauth.ProviderProfile
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProfile))
	require.Equal(t, "default", resumedOAuthProfile.Name)
	require.Equal(t, "client-resume", resumedOAuthProfile.ClientID)

	out, err = runResumedJSON("/oauth", "token", "show", "--json")
	require.NoError(t, err)
	var resumedOAuthToken oauth.TokenView
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthToken))
	require.Equal(t, "resu...1234", resumedOAuthToken.AccessToken)
	require.False(t, resumedOAuthToken.Expired)
	require.NotContains(t, out, "resume-oauth-access-1234")

	out, err = runResumedJSON("/profile", "list")
	require.NoError(t, err)
	var resumedProfile profileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProfile))
	require.Equal(t, "profile", resumedProfile.Kind)
	require.Equal(t, "list", resumedProfile.Action)

	out, err = runResumedJSON("/budget")
	require.NoError(t, err)
	var resumedBudget budgetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBudget))
	require.Equal(t, "budget", resumedBudget.Kind)
	require.Equal(t, "show", resumedBudget.Action)
	require.Equal(t, 1000, resumedBudget.MaxTokens)
	require.Equal(t, 3, resumedBudget.MaxTurns)

	out, err = runResumedJSON("/max-tokens")
	require.NoError(t, err)
	var resumedMaxTokens maxTokensReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMaxTokens))
	require.Equal(t, "max_tokens", resumedMaxTokens.Kind)
	require.Equal(t, "show", resumedMaxTokens.Action)
	require.Equal(t, 1000, resumedMaxTokens.MaxTokens)

	out, err = runResumedJSON("/max-tokens", "4096")
	require.NoError(t, err)
	var requestedMaxTokens maxTokensReport
	require.NoError(t, json.Unmarshal([]byte(out), &requestedMaxTokens))
	require.Equal(t, "show", requestedMaxTokens.Action)
	require.Equal(t, 1000, requestedMaxTokens.MaxTokens)
	require.NotNil(t, requestedMaxTokens.RequestedMaxTokens)
	require.Equal(t, 4096, *requestedMaxTokens.RequestedMaxTokens)

	out, err = runResumedJSON("/max-turns", "9")
	require.NoError(t, err)
	var requestedMaxTurns maxTurnsReport
	require.NoError(t, json.Unmarshal([]byte(out), &requestedMaxTurns))
	require.Equal(t, "show", requestedMaxTurns.Action)
	require.Equal(t, 3, requestedMaxTurns.MaxTurns)
	require.NotNil(t, requestedMaxTurns.RequestedMaxTurns)
	require.Equal(t, 9, *requestedMaxTurns.RequestedMaxTurns)

	out, err = runResumedJSON("/temperature")
	require.NoError(t, err)
	var resumedTemperature temperatureReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTemperature))
	require.Equal(t, "temperature", resumedTemperature.Kind)
	require.Equal(t, "status", resumedTemperature.Action)
	require.True(t, resumedTemperature.Configured)

	out, err = runResumedJSON("/rate-limit")
	require.NoError(t, err)
	var resumedRateLimit rateLimitReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRateLimit))
	require.Equal(t, "rate_limit", resumedRateLimit.Kind)
	require.Equal(t, "show", resumedRateLimit.Action)
	require.Equal(t, 4, resumedRateLimit.MaxRetries)

	out, err = runResumedJSON("/rate-limit-options")
	require.NoError(t, err)
	var resumedRateLimitOptions rateLimitOptionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRateLimitOptions))
	require.Equal(t, "rate_limit_options", resumedRateLimitOptions.Kind)
	require.Equal(t, 4, resumedRateLimitOptions.MaxRetries)

	out, err = runResumedJSON("/permissions")
	require.NoError(t, err)
	var resumedPermissions permissionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPermissions))
	require.Equal(t, "permissions", resumedPermissions.Kind)
	require.Equal(t, "show", resumedPermissions.Action)
	require.Equal(t, "workspace-write", resumedPermissions.PermissionMode)
	require.Equal(t, []string{"read_file"}, resumedPermissions.PermissionRules.Allow)

	out, err = runResumedJSON("/allowed-tools")
	require.NoError(t, err)
	var resumedAllowedTools allowedToolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAllowedTools))
	require.Equal(t, "allowed_tools", resumedAllowedTools.Kind)
	require.Equal(t, "list", resumedAllowedTools.Action)
	require.Equal(t, []string{"read_file"}, resumedAllowedTools.Rules)

	out, err = runResumedJSON("/output-style")
	require.NoError(t, err)
	var resumedOutputStyle outputstyle.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOutputStyle))
	require.Equal(t, "output_style", resumedOutputStyle.Kind)
	require.Equal(t, "list", resumedOutputStyle.Action)

	out, err = runResumedJSON("/theme")
	require.NoError(t, err)
	var resumedTheme themeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTheme))
	require.Equal(t, "theme", resumedTheme.Kind)
	require.Equal(t, "status", resumedTheme.Action)
	require.Equal(t, "dark", resumedTheme.Theme)

	out, err = runResumedJSON("/color", "list")
	require.NoError(t, err)
	var resumedColor themeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedColor))
	require.Equal(t, "theme", resumedColor.Kind)
	require.Equal(t, "list", resumedColor.Action)

	out, err = runResumedJSON("/language")
	require.NoError(t, err)
	var resumedLanguage languageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLanguage))
	require.Equal(t, "language", resumedLanguage.Kind)
	require.Equal(t, "status", resumedLanguage.Action)
	require.Equal(t, "Japanese", resumedLanguage.Language)

	out, err = runResumedJSON("/effort")
	require.NoError(t, err)
	var resumedEffort effortReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedEffort))
	require.Equal(t, "effort", resumedEffort.Kind)
	require.Equal(t, "status", resumedEffort.Action)
	require.Equal(t, "high", resumedEffort.Effort)

	out, err = runResumedJSON("/reasoning", "list")
	require.NoError(t, err)
	var resumedReasoning effortReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReasoning))
	require.Equal(t, "reasoning", resumedReasoning.Kind)
	require.Equal(t, "list", resumedReasoning.Action)

	out, err = runResumedJSON("/fast")
	require.NoError(t, err)
	var resumedFast fastReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFast))
	require.Equal(t, "fast", resumedFast.Kind)
	require.Equal(t, "status", resumedFast.Action)
	require.True(t, resumedFast.Enabled)

	out, err = runResumedJSON("/voice")
	require.NoError(t, err)
	var resumedVoice voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoice))
	require.Equal(t, "voice", resumedVoice.Kind)
	require.Equal(t, "status", resumedVoice.Action)
	require.True(t, resumedVoice.Enabled)
	require.True(t, resumedVoice.CommandConfigured)
	require.Equal(t, "codog-test-voice-helper", resumedVoice.Command)

	out, err = runResumedJSON("/speak", "status")
	require.NoError(t, err)
	var resumedSpeak speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeak))
	require.Equal(t, "speak", resumedSpeak.Kind)
	require.Equal(t, "status", resumedSpeak.Action)
	require.True(t, resumedSpeak.CommandConfigured)
	require.Equal(t, "codog-test-speech-helper", resumedSpeak.Command)

	out, err = runResumedJSON("/vim")
	require.NoError(t, err)
	var resumedVim vimReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVim))
	require.Equal(t, "vim", resumedVim.Kind)
	require.Equal(t, "status", resumedVim.Action)
	require.True(t, resumedVim.Enabled)

	out, err = runResumedJSON("/chrome", "permissions")
	require.NoError(t, err)
	var resumedChrome chromeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedChrome))
	require.Equal(t, "chrome", resumedChrome.Kind)
	require.Equal(t, "permissions", resumedChrome.Action)
	require.True(t, resumedChrome.Enabled)

	out, err = runResumedJSON("/notifications")
	require.NoError(t, err)
	var resumedNotifications notificationsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedNotifications))
	require.Equal(t, "notifications", resumedNotifications.Kind)
	require.Equal(t, "status", resumedNotifications.Action)
	require.False(t, resumedNotifications.Enabled)

	out, err = runResumedJSON("/privacy-settings")
	require.NoError(t, err)
	var resumedPrivacy privacyReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPrivacy))
	require.Equal(t, "privacy_settings", resumedPrivacy.Kind)
	require.Equal(t, "show", resumedPrivacy.Action)
	require.True(t, resumedPrivacy.Settings["telemetry_enabled"])
	require.False(t, resumedPrivacy.Settings["prompt_history_enabled"])

	out, err = runResumedJSON("/telemetry")
	require.NoError(t, err)
	var resumedTelemetry telemetryReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTelemetry))
	require.Equal(t, "telemetry", resumedTelemetry.Kind)
	require.Equal(t, "status", resumedTelemetry.Action)
	require.True(t, resumedTelemetry.Enabled)

	out, err = runResumedJSON("/keybindings", "path")
	require.NoError(t, err)
	var resumedKeybindings keybindingsFileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedKeybindings))
	require.Equal(t, "keybindings", resumedKeybindings.Kind)
	require.Equal(t, "path", resumedKeybindings.Action)
	require.NotEmpty(t, resumedKeybindings.Path)

	out, err = runResumedJSON("/project")
	require.NoError(t, err)
	var resumedProject projectReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProject))
	require.Equal(t, "project", resumedProject.Kind)
	require.Equal(t, filepath.Base(workspace), resumedProject.Name)

	out, err = runResumedJSON("/env")
	require.NoError(t, err)
	var resumedEnv envReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedEnv))
	require.Equal(t, "env", resumedEnv.Kind)
	require.NotNil(t, resumedEnv.Variables)

	require.NoError(t, workerstate.Save(workspace, workerstate.New(workerstate.Options{
		WorkerID:  "resume-worker",
		Version:   "test",
		Mode:      "resume",
		Status:    "idle",
		Workspace: workspace,
		SessionID: "resume-slash",
	})))
	out, err = runResumedJSON("/state")
	require.NoError(t, err)
	var resumedState workerstate.State
	require.NoError(t, json.Unmarshal([]byte(out), &resumedState))
	require.Equal(t, "worker_state", resumedState.Kind)
	require.Equal(t, "resume-worker", resumedState.WorkerID)

	out, err = runResumedJSON("/onboarding")
	require.NoError(t, err)
	var resumedOnboarding onboarding.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOnboarding))
	require.Equal(t, "onboarding", resumedOnboarding.Kind)
	require.NotEmpty(t, resumedOnboarding.Checks)

	out, err = runResumedJSON("/setup", "status", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedSetup setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetup))
	require.Equal(t, "setup", resumedSetup.Kind)
	require.Equal(t, "status", resumedSetup.Action)
	require.NotNil(t, resumedSetup.Terminal)
	require.Equal(t, "status", resumedSetup.Terminal.Action)
	require.Equal(t, "zsh", resumedSetup.Terminal.Shell)
	require.Equal(t, terminalProfilePath, resumedSetup.Terminal.Path)

	out, err = runResumedJSON("/setup", "terminal", "snippet", "--shell", "zsh")
	require.NoError(t, err)
	var resumedSetupTerminal setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetupTerminal))
	require.Equal(t, "setup", resumedSetupTerminal.Kind)
	require.Equal(t, "terminal", resumedSetupTerminal.Action)
	require.NotNil(t, resumedSetupTerminal.Terminal)
	require.Equal(t, "snippet", resumedSetupTerminal.Terminal.Action)
	require.Contains(t, resumedSetupTerminal.Terminal.Snippet, "codog_statusline")

	out, err = runResumedJSON("/system-prompt")
	require.NoError(t, err)
	var resumedSystemPrompt struct {
		Kind         string `json:"kind"`
		Action       string `json:"action"`
		Status       string `json:"status"`
		SystemPrompt string `json:"system_prompt"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSystemPrompt))
	require.Equal(t, "system-prompt", resumedSystemPrompt.Kind)
	require.Equal(t, "show", resumedSystemPrompt.Action)
	require.Equal(t, "ok", resumedSystemPrompt.Status)
	require.NotEmpty(t, resumedSystemPrompt.SystemPrompt)

	out, err = runResumedJSON("/tool-details", "bash")
	require.NoError(t, err)
	var resumedToolDetails toolDetailsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedToolDetails))
	require.Equal(t, "tool_details", resumedToolDetails.Kind)
	require.Equal(t, "show", resumedToolDetails.Action)
	require.Equal(t, "bash", resumedToolDetails.Tool.Name)
	require.Contains(t, resumedToolDetails.Aliases, "Bash")

	out, err = runResumedJSON("/debug-tool-call", "read_file", `{"path":"main.go"}`)
	require.NoError(t, err)
	var resumedDebugToolCall debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugToolCall))
	require.Equal(t, "debug_tool_call", resumedDebugToolCall.Kind)
	require.Equal(t, "read_file", resumedDebugToolCall.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugToolCall.Permission)
	require.True(t, resumedDebugToolCall.Success)
	require.Contains(t, resumedDebugToolCall.Output, "helper")

	out, err = runResumedJSON("/terminal-setup", "status", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedTerminalSetup terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalSetup))
	require.Equal(t, "terminal_setup", resumedTerminalSetup.Kind)
	require.Equal(t, "status", resumedTerminalSetup.Action)
	require.Equal(t, "ok", resumedTerminalSetup.Status)
	require.Equal(t, "zsh", resumedTerminalSetup.Shell)
	require.Equal(t, terminalProfilePath, resumedTerminalSetup.Path)
	require.False(t, resumedTerminalSetup.Installed)

	out, err = runResumedJSON("/terminal-setup", "snippet", "--shell", "zsh")
	require.NoError(t, err)
	var resumedTerminalSnippet terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalSnippet))
	require.Equal(t, "terminal_setup", resumedTerminalSnippet.Kind)
	require.Equal(t, "snippet", resumedTerminalSnippet.Action)
	require.Equal(t, "zsh", resumedTerminalSnippet.Shell)
	require.Contains(t, resumedTerminalSnippet.Snippet, "codog_statusline")

	out, err = runResumedJSON("/terminalSetup", "status", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedTerminalAlias terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalAlias))
	require.Equal(t, "terminal_setup", resumedTerminalAlias.Kind)
	require.Equal(t, "status", resumedTerminalAlias.Action)

	out, err = runResumedJSON("/model")
	require.NoError(t, err)
	var resumedModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedModel))
	require.Equal(t, "model", resumedModel.Kind)
	require.Equal(t, "show", resumedModel.Action)
	require.Equal(t, "claude-test", resumedModel.Model)

	out, err = runResumedJSON("/model", "claude-requested")
	require.NoError(t, err)
	var requestedModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &requestedModel))
	require.Equal(t, "show", requestedModel.Action)
	require.Equal(t, "claude-test", requestedModel.Model)
	require.Equal(t, "claude-requested", requestedModel.RequestedModel)

	out, err = runResumedJSON("/advisor")
	require.NoError(t, err)
	var resumedAdvisor advisorReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAdvisor))
	require.Equal(t, "advisor", resumedAdvisor.Kind)
	require.Equal(t, "show", resumedAdvisor.Action)
	require.Equal(t, "claude-advisor", resumedAdvisor.Model)
	require.Equal(t, "claude-test", resumedAdvisor.MainModel)

	out, err = runResumedJSON("/sandbox")
	require.NoError(t, err)
	var resumedSandbox sandboxReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSandbox))
	require.Equal(t, "sandbox", resumedSandbox.Kind)
	require.Equal(t, "status", resumedSandbox.Action)

	out, err = runResumedJSON("/sandbox-toggle", "status")
	require.NoError(t, err)
	var resumedSandboxToggle sandboxToggleReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSandboxToggle))
	require.Equal(t, "sandbox_toggle", resumedSandboxToggle.Kind)
	require.Equal(t, "status", resumedSandboxToggle.Action)
	require.Equal(t, "detect", resumedSandboxToggle.ConfiguredStrategy)
	require.NotEmpty(t, resumedSandboxToggle.ResolutionStatus)

	out, err = runResumedJSON("/mcp", "list")
	require.NoError(t, err)
	var resumedMCP mcpListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMCP))
	require.Equal(t, "mcp", resumedMCP.Kind)
	require.Equal(t, "list", resumedMCP.Action)

	out, err = runResumedJSON("/skills", "list")
	require.NoError(t, err)
	var resumedSkills struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Skills []any  `json:"skills"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkills))
	require.Equal(t, "skills", resumedSkills.Kind)
	require.Equal(t, "list", resumedSkills.Action)
	require.NotNil(t, resumedSkills.Skills)

	out, err = runResumedJSON("/skill", "list")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkills))
	require.Equal(t, "skills", resumedSkills.Kind)
	require.Equal(t, "list", resumedSkills.Action)
	require.NotNil(t, resumedSkills.Skills)

	out, err = runResumedJSON("/skills", "sources")
	require.NoError(t, err)
	var resumedSkillSources struct {
		Kind   string                 `json:"kind"`
		Action string                 `json:"action"`
		Status string                 `json:"status"`
		Roots  []skills.DiscoveryRoot `json:"roots"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkillSources))
	require.Equal(t, "skills", resumedSkillSources.Kind)
	require.Equal(t, "sources", resumedSkillSources.Action)
	require.Equal(t, "ok", resumedSkillSources.Status)
	require.NotEmpty(t, resumedSkillSources.Roots)

	out, err = runResumedJSON("/commands", "list")
	require.NoError(t, err)
	var resumedCommands struct {
		Kind     string `json:"kind"`
		Commands []any  `json:"commands"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCommands))
	require.Equal(t, "commands", resumedCommands.Kind)
	require.NotNil(t, resumedCommands.Commands)

	out, err = runResumedJSON("/templates", "list")
	require.NoError(t, err)
	var resumedTemplates struct {
		Kind      string `json:"kind"`
		Templates []any  `json:"templates"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTemplates))
	require.Equal(t, "templates", resumedTemplates.Kind)
	require.NotNil(t, resumedTemplates.Templates)

	out, err = runResumedJSON("/todos", "list")
	require.NoError(t, err)
	var resumedTodos todos.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTodos))
	require.Equal(t, "todos", resumedTodos.Kind)
	require.Equal(t, "list", resumedTodos.Action)

	out, err = runResumedJSON("/hooks", "list")
	require.NoError(t, err)
	var resumedHooksList hooksListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHooksList))
	require.Equal(t, "hooks", resumedHooksList.Kind)
	require.Equal(t, "list", resumedHooksList.Action)

	out, err = runResumedJSON("/hooks", "health", "pre", "--tool", "read_file")
	require.NoError(t, err)
	var resumedHooksHealth hooksHealthReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHooksHealth))
	require.Equal(t, "hooks", resumedHooksHealth.Kind)
	require.Equal(t, "health", resumedHooksHealth.Action)
	require.Equal(t, "pre_tool_use", resumedHooksHealth.Event)
	require.Equal(t, "read_file", resumedHooksHealth.MatcherTarget)

	out, err = runResumedJSON("/agents", "list")
	require.NoError(t, err)
	var resumedAgents agentsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgents))
	require.Equal(t, "agents", resumedAgents.Kind)
	require.Equal(t, "list", resumedAgents.Action)

	out, err = runResumedJSON("/plugins", "list")
	require.NoError(t, err)
	var resumedPlugins pluginsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlugins))
	require.Equal(t, "plugin", resumedPlugins.Kind)
	require.Equal(t, "list", resumedPlugins.Action)

	out, err = runResumedJSON("/reload-plugins")
	require.NoError(t, err)
	var resumedReloadPlugins reloadPluginsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReloadPlugins))
	require.Equal(t, "reload_plugins", resumedReloadPlugins.Kind)
	require.True(t, resumedReloadPlugins.Reloaded)
	require.Equal(t, 1, resumedReloadPlugins.Plugins)
	require.Equal(t, 1, resumedReloadPlugins.PluginTools)
	require.Contains(t, resumedReloadPlugins.PluginIDs, "resume-demo")
	require.Contains(t, resumedReloadPlugins.EnabledPluginIDs, "resume-demo")
	require.GreaterOrEqual(t, resumedReloadPlugins.ToolCountAfter, resumedReloadPlugins.ToolCountBefore)

	out, err = runResumedJSON("/tasks")
	require.NoError(t, err)
	var resumedTasks []background.Task
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTasks))
	require.Empty(t, resumedTasks)

	out, err = runResumedJSON("/tasks", "board")
	require.NoError(t, err)
	var resumedTaskBoard background.LaneBoard
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTaskBoard))
	require.Empty(t, resumedTaskBoard.Active)
	require.Empty(t, resumedTaskBoard.Blocked)
	require.Empty(t, resumedTaskBoard.Finished)

	out, err = runResumedJSON("/cron", "list")
	require.NoError(t, err)
	var resumedCronList cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronList))
	require.Equal(t, "cron", resumedCronList.Kind)
	require.Equal(t, "list", resumedCronList.Action)
	require.Equal(t, 1, resumedCronList.Count)
	require.Equal(t, cronEntry.ID, resumedCronList.Entries[0].ID)

	out, err = runResumedJSON("/cron", "due", "--now", "2026-07-01T00:00:00Z")
	require.NoError(t, err)
	var resumedCronDue cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronDue))
	require.Equal(t, "due", resumedCronDue.Action)
	require.Equal(t, 1, resumedCronDue.Count)
	require.Equal(t, cronEntry.ID, resumedCronDue.Entries[0].ID)

	out, err = runResumedJSON("/team", "list")
	require.NoError(t, err)
	var resumedTeamList teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamList))
	require.Equal(t, "team", resumedTeamList.Kind)
	require.Equal(t, "list", resumedTeamList.Action)
	require.Equal(t, 1, resumedTeamList.Count)
	require.Equal(t, teamEntry.ID, resumedTeamList.Teams[0].ID)

	out, err = runResumedJSON("/team", "get", teamEntry.ID)
	require.NoError(t, err)
	var resumedTeamGet teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamGet))
	require.Equal(t, "get", resumedTeamGet.Action)
	require.Equal(t, teamEntry.ID, resumedTeamGet.Team.ID)

	out, err = runResumedJSON("/team", "status", teamEntry.ID)
	require.NoError(t, err)
	var resumedTeamStatus teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamStatus))
	require.Equal(t, "status", resumedTeamStatus.Action)
	require.Equal(t, "degraded", resumedTeamStatus.Team.Status)
	require.Equal(t, []string{"missing-task"}, resumedTeamStatus.MissingTasks)

	out, err = runResumedJSON("/team", "logs", teamEntry.ID)
	require.NoError(t, err)
	var resumedTeamLogs teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamLogs))
	require.Equal(t, "logs", resumedTeamLogs.Action)
	require.Len(t, resumedTeamLogs.Logs, 1)
	require.NotEmpty(t, resumedTeamLogs.Logs[0].Error)

	out, err = runResumedJSON("/team", "watch", teamEntry.ID, "--max-events", "1")
	require.NoError(t, err)
	require.Contains(t, out, `"kind":"team_watch"`)
	require.Contains(t, out, `"type":"error"`)

	out, err = runResumedJSON("/metrics")
	require.NoError(t, err)
	var resumedMetrics metricsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMetrics))
	require.Equal(t, "metrics", resumedMetrics.Kind)
	require.NotNil(t, resumedMetrics.Session)
	require.Equal(t, "resume-slash", resumedMetrics.Session.ID)

	out, err = runResumedJSON("/insights")
	require.NoError(t, err)
	var resumedInsights struct {
		Kind     string `json:"kind"`
		Sessions int    `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedInsights))
	require.Equal(t, "insights", resumedInsights.Kind)
	require.GreaterOrEqual(t, resumedInsights.Sessions, 1)

	out, err = runResumedJSON("/perf-issue")
	require.NoError(t, err)
	var resumedPerfIssue perfissue.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPerfIssue))
	require.Equal(t, "perf_issue", resumedPerfIssue.Kind)
	require.Empty(t, resumedPerfIssue.File)

	thinkBackPath := filepath.Join(workspace, "resume-think-back.html")
	out, err = runResumedJSON("/think-back", "--year", "2026", "--output", thinkBackPath)
	require.NoError(t, err)
	var resumedThinkBack thinkback.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedThinkBack))
	require.Equal(t, "think_back", resumedThinkBack.Kind)
	require.Equal(t, 2026, resumedThinkBack.Year)
	require.Equal(t, thinkBackPath, resumedThinkBack.Output)
	require.True(t, resumedThinkBack.Written)
	require.GreaterOrEqual(t, resumedThinkBack.Insights.Sessions, 1)
	require.FileExists(t, thinkBackPath)

	thinkbackPlayPath := filepath.Join(workspace, "resume-thinkback-play.html")
	out, err = runResumedJSON("/thinkback-play", "--year", "2026", "--output", thinkbackPlayPath)
	require.NoError(t, err)
	var resumedThinkbackPlay thinkback.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedThinkbackPlay))
	require.Equal(t, "think_back", resumedThinkbackPlay.Kind)
	require.True(t, resumedThinkbackPlay.Written)
	require.FileExists(t, thinkbackPlayPath)

	out, err = runResumedJSON("/desktop")
	require.NoError(t, err)
	var resumedDesktop desktopHandoffReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDesktop))
	require.Equal(t, "desktop_handoff", resumedDesktop.Kind)
	require.Equal(t, "resume-slash", resumedDesktop.SessionID)

	out, err = runResumedJSON("/mobile", "ios")
	require.NoError(t, err)
	var resumedMobile mobileHandoffReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMobile))
	require.Equal(t, "mobile_handoff", resumedMobile.Kind)
	require.Equal(t, "ios", resumedMobile.Platform)
	require.Equal(t, "resume-slash", resumedMobile.SessionID)

	out, err = runResumedJSON("/android")
	require.NoError(t, err)
	var resumedAndroid mobileHandoffReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAndroid))
	require.Equal(t, "mobile_handoff", resumedAndroid.Kind)
	require.Equal(t, "android", resumedAndroid.Platform)

	out, err = runResumedJSON("/remote-env", "status")
	require.NoError(t, err)
	var resumedRemoteEnv remoteEnvReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteEnv))
	require.Equal(t, "remote_env", resumedRemoteEnv.Kind)
	require.Equal(t, "show", resumedRemoteEnv.Action)
	require.True(t, resumedRemoteEnv.Enabled)
	require.True(t, resumedRemoteEnv.AuthTokenConfigured)
	require.Equal(t, 45, resumedRemoteEnv.LeaseSeconds)
	require.NotContains(t, out, "remote-secret")

	out, err = runResumedJSON("/remote-setup", "status", "--addr", "127.0.0.1:8792")
	require.NoError(t, err)
	var resumedRemoteSetup remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteSetup))
	require.Equal(t, "remote_setup", resumedRemoteSetup.Kind)
	require.Equal(t, "status", resumedRemoteSetup.Action)
	require.Equal(t, "ready", resumedRemoteSetup.Status)
	require.True(t, resumedRemoteSetup.Enabled)
	require.True(t, resumedRemoteSetup.Ready)
	require.Equal(t, "resume-slash", resumedRemoteSetup.SessionID)
	require.Equal(t, "http://127.0.0.1:8792", resumedRemoteSetup.RemoteURL)
	require.NotContains(t, out, "remote-secret")

	out, err = runResumedJSON("/web-setup", "status", "--addr", "127.0.0.1:8793")
	require.NoError(t, err)
	var resumedWebSetup remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWebSetup))
	require.Equal(t, "remote_setup", resumedWebSetup.Kind)
	require.Equal(t, "status", resumedWebSetup.Action)
	require.Equal(t, "http://127.0.0.1:8793", resumedWebSetup.RemoteURL)

	out, err = runResumedJSON("/ide")
	require.NoError(t, err)
	var resumedIDE ideReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedIDE))
	require.Equal(t, "ide", resumedIDE.Kind)
	require.Equal(t, "status", resumedIDE.Action)

	out, err = runResumedJSON("/bridge-kick")
	require.NoError(t, err)
	var resumedBridgeKick bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBridgeKick))
	require.Equal(t, "bridge_kick", resumedBridgeKick.Kind)
	require.Equal(t, "status", resumedBridgeKick.Action)

	out, err = runResumedJSON("/workspace")
	require.NoError(t, err)
	var resumedWorkspace workspaceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWorkspace))
	require.Equal(t, "workspace", resumedWorkspace.Kind)
	require.Equal(t, "status", resumedWorkspace.Action)
	expectedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, expectedWorkspace, resumedWorkspace.Workspace)

	out, err = runResumedJSON("/focus")
	require.NoError(t, err)
	var resumedFocus focus.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFocus))
	require.Equal(t, "focus", resumedFocus.Kind)

	out, err = runResumedJSON("/add-dir", "list")
	require.NoError(t, err)
	var resumedAddDir pathscope.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAddDir))
	require.Equal(t, "additional_dirs", resumedAddDir.Kind)
	require.Equal(t, "list", resumedAddDir.Action)

	out, err = runResumedJSON("/validation", "add-dir", ".")
	require.NoError(t, err)
	var resumedValidation pathscope.ValidationReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedValidation))
	require.Equal(t, "validation", resumedValidation.Kind)
	require.Equal(t, "add_dir", resumedValidation.Action)

	out, err = runResumedJSON("/ant-trace", "--no-request")
	require.NoError(t, err)
	var resumedAntTrace anttrace.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAntTrace))
	require.Equal(t, "ant_trace", resumedAntTrace.Kind)
	require.False(t, resumedAntTrace.RequestSent)

	out, err = runResumedJSON("/mock-limits")
	require.NoError(t, err)
	var resumedMockLimits mocklimits.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMockLimits))
	require.Equal(t, "mock_limits", resumedMockLimits.Kind)
	require.Equal(t, "show", resumedMockLimits.Action)

	out, err = runResumedJSON("/extra-usage", "--admin", "--no-open")
	require.NoError(t, err)
	var resumedExtraUsage extraUsageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExtraUsage))
	require.Equal(t, "extra_usage", resumedExtraUsage.Kind)
	require.Equal(t, "show", resumedExtraUsage.Action)
	require.Equal(t, "admin", resumedExtraUsage.Mode)
	require.Equal(t, extraUsageAdminURL, resumedExtraUsage.URL)
	require.False(t, resumedExtraUsage.Opened)
	require.Equal(t, 0, resumedExtraUsage.VisitCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/install-slack-app", "--no-open")
	require.NoError(t, err)
	var resumedSlack installSlackAppReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSlack))
	require.Equal(t, "install_slack_app", resumedSlack.Kind)
	require.Equal(t, "show", resumedSlack.Action)
	require.Equal(t, slackAppURL, resumedSlack.URL)
	require.False(t, resumedSlack.Opened)
	require.Equal(t, 0, resumedSlack.InstallCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/stickers", "--no-open")
	require.NoError(t, err)
	var resumedStickers stickersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedStickers))
	require.Equal(t, "stickers", resumedStickers.Kind)
	require.Equal(t, "show", resumedStickers.Action)
	require.Equal(t, stickerOrderURL, resumedStickers.URL)
	require.False(t, resumedStickers.Opened)
	require.Equal(t, 0, resumedStickers.OrderCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/passes", "show")
	require.NoError(t, err)
	var resumedPasses passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPasses))
	require.Equal(t, "passes", resumedPasses.Kind)
	require.Equal(t, "show", resumedPasses.Action)
	require.Equal(t, guestPassDocsURL, resumedPasses.URL)
	require.False(t, resumedPasses.Opened)
	require.Equal(t, 0, resumedPasses.VisitCount)
	require.Empty(t, openedURL)

	heapDumpPath := filepath.Join(workspace, "resume-heap.pprof")
	out, err = runResumedJSON("/heapdump", heapDumpPath, "--no-gc")
	require.NoError(t, err)
	var resumedHeapDump heapDumpReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHeapDump))
	require.Equal(t, "heapdump", resumedHeapDump.Kind)
	require.Equal(t, "ok", resumedHeapDump.Status)
	require.Equal(t, heapDumpPath, resumedHeapDump.Path)
	require.False(t, resumedHeapDump.GC)
	require.Greater(t, resumedHeapDump.Bytes, int64(0))
	require.FileExists(t, heapDumpPath)

	out, err = runResumedJSON("/files", "--glob", "*.go", "--limit", "5")
	require.NoError(t, err)
	var resumedFiles struct {
		Kind  string `json:"kind"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFiles))
	require.Equal(t, "files", resumedFiles.Kind)
	resumedFilePaths := []string{}
	for _, file := range resumedFiles.Files {
		resumedFilePaths = append(resumedFilePaths, file.Path)
	}
	require.Contains(t, resumedFilePaths, "main.go")

	out, err = runResumedJSON("/search", "helper", "--glob", "*.go")
	require.NoError(t, err)
	var resumedSearch searchReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSearch))
	require.Equal(t, "search", resumedSearch.Kind)
	require.GreaterOrEqual(t, resumedSearch.Total, 1)

	out, err = runResumedJSON("/security-review", "--limit", "20")
	require.NoError(t, err)
	var resumedSecurity struct {
		Kind  string `json:"kind"`
		Total int    `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSecurity))
	require.Equal(t, "security_review", resumedSecurity.Kind)
	require.GreaterOrEqual(t, resumedSecurity.Total, 1)

	out, err = runResumedJSON("/bughunter", ".", "--limit", "20")
	require.NoError(t, err)
	var resumedBughunter struct {
		Kind  string `json:"kind"`
		Total int    `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBughunter))
	require.Equal(t, "bughunter", resumedBughunter.Kind)
	require.GreaterOrEqual(t, resumedBughunter.Total, 1)

	out, err = runResumedJSON("/symbols")
	require.NoError(t, err)
	var resumedSymbols symbolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSymbols))
	require.Equal(t, "symbols", resumedSymbols.Kind)
	require.GreaterOrEqual(t, resumedSymbols.Total, 2)

	out, err = runResumedJSON("/map", "--depth", "1")
	require.NoError(t, err)
	var resumedMap mapReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMap))
	require.Equal(t, "map", resumedMap.Kind)
	require.GreaterOrEqual(t, resumedMap.Total, 1)

	out, err = runResumedJSON("/definition", "helper")
	require.NoError(t, err)
	var resumedDefinition definitionReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDefinition))
	require.Equal(t, "definition", resumedDefinition.Kind)
	require.True(t, resumedDefinition.Found)

	out, err = runResumedJSON("/references", "helper")
	require.NoError(t, err)
	var resumedReferences referencesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReferences))
	require.Equal(t, "references", resumedReferences.Kind)
	require.GreaterOrEqual(t, resumedReferences.Total, 1)

	out, err = runResumedJSON("/hover", "helper")
	require.NoError(t, err)
	var resumedHover struct {
		Kind  string `json:"kind"`
		Hover struct {
			Found bool `json:"found"`
		} `json:"hover"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHover))
	require.Equal(t, "hover", resumedHover.Kind)
	require.True(t, resumedHover.Hover.Found)

	out, err = runResumedJSON("/completion", "hel")
	require.NoError(t, err)
	var resumedCompletion completionReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCompletion))
	require.Equal(t, "completion", resumedCompletion.Kind)
	require.GreaterOrEqual(t, resumedCompletion.Total, 1)

	out, err = runResumedJSON("/teleport", "main.go")
	require.NoError(t, err)
	var resumedTeleport teleportReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeleport))
	require.Equal(t, "teleport", resumedTeleport.Kind)
	require.True(t, resumedTeleport.Found)
	require.Equal(t, "file", resumedTeleport.Mode)

	out, err = runResumedJSON("/format", "main.go")
	require.NoError(t, err)
	var resumedFormat formatReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFormat))
	require.Equal(t, "format", resumedFormat.Kind)
	require.False(t, resumedFormat.Write)

	out, err = runResumedJSON("/code-intel", "symbols")
	require.NoError(t, err)
	var resumedCodeIntelSymbols symbolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelSymbols))
	require.Equal(t, "symbols", resumedCodeIntelSymbols.Kind)
	require.GreaterOrEqual(t, resumedCodeIntelSymbols.Total, 2)

	out, err = runResumedJSON("/code-intel", "notebook-read", "analysis.ipynb", "--cell-index", "0")
	require.NoError(t, err)
	var resumedCodeIntelNotebook codeIntelNotebookReadReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelNotebook))
	require.Equal(t, "notebook_read", resumedCodeIntelNotebook.Kind)
	require.Equal(t, "analysis.ipynb", resumedCodeIntelNotebook.Result.Path)
	require.Len(t, resumedCodeIntelNotebook.Result.Cells, 1)
	require.Equal(t, "resume-cell", resumedCodeIntelNotebook.Result.Cells[0].CellID)

	out, err = runResumedJSON("/notebook-read", "analysis.ipynb", "--cell-index", "0")
	require.NoError(t, err)
	var resumedNotebookRead codeIntelNotebookReadReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedNotebookRead))
	require.Equal(t, "notebook_read", resumedNotebookRead.Kind)
	require.Equal(t, "resume-cell", resumedNotebookRead.Result.Cells[0].CellID)

	out, err = runResumedJSON("/code-intel", "notebook-edit", "analysis.ipynb", "--source", "changed")
	require.Error(t, err)
	require.Contains(t, out, `"kind": "unsupported_resumed_slash_command"`)
	require.Contains(t, out, `"/code-intel notebook-edit"`)

	out, err = runResumedJSON("/reset", "status")
	require.NoError(t, err)
	var resumedReset resetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReset))
	require.Equal(t, "reset", resumedReset.Kind)
	require.Equal(t, "status", resumedReset.Action)
	require.Equal(t, "all", resumedReset.Section)
	require.True(t, resumedReset.ConfirmRequired)
	require.Contains(t, resumedReset.AvailableSections, "model")

	out, err = runResumedJSON("/plan")
	require.NoError(t, err)
	var resumedPlan planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlan))
	require.Equal(t, "plan", resumedPlan.Kind)
	require.Equal(t, "show", resumedPlan.Action)
	require.Equal(t, "active", resumedPlan.Status)
	require.True(t, resumedPlan.State.Active)
	require.Equal(t, "inspect before editing", resumedPlan.State.Plan)

	for _, guarded := range []struct {
		Command string
		Args    []string
		Report  string
	}{
		{Command: "/api-key", Args: []string{"set", "secret"}, Report: "/api-key set"},
		{Command: "/providers", Args: []string{"set", "anthropic"}, Report: "/providers set"},
		{Command: "/profile", Args: []string{"set", "work"}, Report: "/profile set"},
		{Command: "/budget", Args: []string{"set", "--max-tokens", "2000"}, Report: "/budget set"},
		{Command: "/temperature", Args: []string{"set", "0.2"}, Report: "/temperature set"},
		{Command: "/rate-limit", Args: []string{"set", "--max-retries", "2"}, Report: "/rate-limit set"},
		{Command: "/permissions", Args: []string{"read-only"}, Report: "/permissions set"},
		{Command: "/allowed-tools", Args: []string{"add", "bash"}, Report: "/allowed-tools add"},
		{Command: "/oauth", Args: []string{"pkce"}, Report: "/oauth pkce"},
		{Command: "/oauth", Args: []string{"discover", "https://example.test"}, Report: "/oauth discover"},
		{Command: "/oauth", Args: []string{"provider", "save", "work", "https://example.test", "client"}, Report: "/oauth provider save"},
		{Command: "/oauth", Args: []string{"provider", "delete", "default"}, Report: "/oauth provider delete"},
		{Command: "/oauth", Args: []string{"token", "save", "access-token"}, Report: "/oauth token save"},
		{Command: "/oauth", Args: []string{"token", "refresh"}, Report: "/oauth token refresh"},
		{Command: "/oauth", Args: []string{"token", "delete"}, Report: "/oauth token delete"},
		{Command: "/oauth", Args: []string{"logout"}, Report: "/oauth logout"},
		{Command: "/oauth", Args: []string{"browser", "login", "default"}, Report: "/oauth browser"},
		{Command: "/oauth", Args: []string{"device", "login", "default"}, Report: "/oauth device"},
		{Command: "/advisor", Args: []string{"claude-opus"}, Report: "/advisor set"},
		{Command: "/advisor", Args: []string{"off"}, Report: "/advisor clear"},
		{Command: "/output-style", Args: []string{"set", "concise"}, Report: "/output-style set"},
		{Command: "/theme", Args: []string{"light"}, Report: "/theme set"},
		{Command: "/language", Args: []string{"French"}, Report: "/language set"},
		{Command: "/effort", Args: []string{"low"}, Report: "/effort set"},
		{Command: "/fast", Args: []string{"toggle"}, Report: "/fast toggle"},
		{Command: "/voice", Args: []string{"set-command", "say"}, Report: "/voice set-command"},
		{Command: "/voice", Args: []string{"on"}, Report: "/voice on"},
		{Command: "/voice", Args: []string{"off"}, Report: "/voice off"},
		{Command: "/voice", Args: []string{"toggle"}, Report: "/voice toggle"},
		{Command: "/voice", Args: []string{"test"}, Report: "/voice test"},
		{Command: "/voice", Args: []string{"listen"}, Report: "/voice listen"},
		{Command: "/voice", Args: []string{"clear"}, Report: "/voice clear"},
		{Command: "/listen", Args: nil, Report: "/listen"},
		{Command: "/speak", Args: nil, Report: "/speak speak"},
		{Command: "/speak", Args: []string{"hello"}, Report: "/speak speak"},
		{Command: "/speak", Args: []string{"last"}, Report: "/speak speak"},
		{Command: "/speak", Args: []string{"test"}, Report: "/speak test"},
		{Command: "/speak", Args: []string{"set-command", "say"}, Report: "/speak set-command"},
		{Command: "/speak", Args: []string{"clear"}, Report: "/speak clear"},
		{Command: "/vim", Args: []string{"toggle"}, Report: "/vim toggle"},
		{Command: "/chrome", Args: []string{"on"}, Report: "/chrome on"},
		{Command: "/notifications", Args: []string{"on"}, Report: "/notifications on"},
		{Command: "/privacy-settings", Args: []string{"set", "telemetry", "off"}, Report: "/privacy-settings set"},
		{Command: "/telemetry", Args: []string{"on"}, Report: "/telemetry on"},
		{Command: "/keybindings", Args: []string{"init"}, Report: "/keybindings init"},
		{Command: "/agents", Args: []string{"run", "reviewer", "check"}, Report: "/agents run"},
		{Command: "/plugins", Args: []string{"install", "example"}, Report: "/plugins install"},
		{Command: "/skills", Args: []string{"install", "main.go"}, Report: "/skills install"},
		{Command: "/skills", Args: []string{"add", "main.go"}, Report: "/skills add"},
		{Command: "/skills", Args: []string{"invoke", "debug"}, Report: "/skills invoke"},
		{Command: "/skill", Args: []string{"uninstall", "debug"}, Report: "/skill uninstall"},
		{Command: "/tasks", Args: []string{"run", "echo", "hi"}, Report: "/tasks run"},
		{Command: "/sandbox-toggle", Args: []string{"detect"}, Report: "/sandbox-toggle detect"},
		{Command: "/sandbox-toggle", Args: []string{"off"}, Report: "/sandbox-toggle off"},
		{Command: "/sandbox-toggle", Args: []string{"clear"}, Report: "/sandbox-toggle clear"},
		{Command: "/hooks", Args: []string{"run", "pre", "--tool", "read_file"}, Report: "/hooks run"},
		{Command: "/cron", Args: []string{"create", "@daily", "check"}, Report: "/cron create"},
		{Command: "/cron", Args: []string{"delete", cronEntry.ID}, Report: "/cron delete"},
		{Command: "/cron", Args: []string{"mark-run", cronEntry.ID}, Report: "/cron mark-run"},
		{Command: "/cron", Args: []string{"run-due"}, Report: "/cron run-due"},
		{Command: "/team", Args: []string{"create", "writers", "check"}, Report: "/team create"},
		{Command: "/team", Args: []string{"delete", teamEntry.ID}, Report: "/team delete"},
		{Command: "/setup", Args: []string{"init"}, Report: "/setup init"},
		{Command: "/setup", Args: []string{"all"}, Report: "/setup all"},
		{Command: "/setup", Args: []string{"terminal", "install", "--shell", "zsh", "--path", terminalProfilePath}, Report: "/setup terminal install"},
		{Command: "/setup", Args: []string{"terminal", "uninstall", "--shell", "zsh", "--path", terminalProfilePath}, Report: "/setup terminal uninstall"},
		{Command: "/terminal-setup", Args: []string{"install", "--shell", "zsh", "--path", terminalProfilePath}, Report: "/terminal-setup install"},
		{Command: "/terminal-setup", Args: []string{"uninstall", "--shell", "zsh", "--path", terminalProfilePath}, Report: "/terminal-setup uninstall"},
		{Command: "/remote-env", Args: []string{"set", "--enabled", "off"}, Report: "/remote-env set"},
		{Command: "/remote-env", Args: []string{"clear"}, Report: "/remote-env clear"},
		{Command: "/remote-env", Args: []string{"show", "--auth-token", "secret"}, Report: "/remote-env show"},
		{Command: "/remote-setup", Args: []string{"enable", "--addr", "127.0.0.1:8794"}, Report: "/remote-setup enable"},
		{Command: "/remote-setup", Args: []string{"disable"}, Report: "/remote-setup disable"},
		{Command: "/remote-setup", Args: []string{"clear"}, Report: "/remote-setup clear"},
		{Command: "/web-setup", Args: []string{"enable"}, Report: "/remote-setup enable"},
		{Command: "/format", Args: []string{"main.go", "--write"}, Report: "/format write"},
		{Command: "/reset", Args: []string{"model"}, Report: "/reset model"},
		{Command: "/reset", Args: []string{"all", "--confirm"}, Report: "/reset all"},
		{Command: "/plan", Args: []string{"inspect", "more"}, Report: "/plan enter"},
		{Command: "/plan", Args: []string{"enter", "inspect"}, Report: "/plan enter"},
		{Command: "/plan", Args: []string{"set", "ship"}, Report: "/plan set"},
		{Command: "/plan", Args: []string{"exit"}, Report: "/plan exit"},
		{Command: "/plan", Args: []string{"clear"}, Report: "/plan clear"},
		{Command: "/ultraplan", Args: []string{"inspect"}, Report: "/ultraplan"},
		{Command: "/exit-plan", Args: nil, Report: "/exit-plan"},
		{Command: "/perf-issue", Args: []string{"--write"}, Report: "/perf-issue write"},
		{Command: "/think-back", Args: []string{"--year", "2026"}, Report: "/think-back default-output"},
		{Command: "/thinkback", Args: nil, Report: "/thinkback default-output"},
		{Command: "/ide", Args: []string{"clear"}, Report: "/ide clear"},
		{Command: "/bridge-kick", Args: []string{"clear"}, Report: "/bridge-kick clear"},
		{Command: "/workspace", Args: []string{"set", workspace}, Report: "/workspace set"},
		{Command: "/focus", Args: []string{"main.go"}, Report: "/focus add"},
		{Command: "/add-dir", Args: []string{workspace}, Report: "/add-dir add"},
		{Command: "/ant-trace", Args: nil, Report: "/ant-trace request"},
		{Command: "/ant-trace", Args: []string{"--no-request", "--write"}, Report: "/ant-trace write"},
		{Command: "/mock-limits", Args: []string{"serve"}, Report: "/mock-limits serve"},
		{Command: "/extra-usage", Args: nil, Report: "/extra-usage open"},
		{Command: "/extra-usage", Args: []string{"--open"}, Report: "/extra-usage open"},
		{Command: "/install-slack-app", Args: nil, Report: "/install-slack-app open"},
		{Command: "/install-slack-app", Args: []string{"--open"}, Report: "/install-slack-app open"},
		{Command: "/stickers", Args: nil, Report: "/stickers open"},
		{Command: "/stickers", Args: []string{"--open"}, Report: "/stickers open"},
		{Command: "/passes", Args: nil, Report: "/passes open"},
		{Command: "/passes", Args: []string{"open"}, Report: "/passes open"},
		{Command: "/passes", Args: []string{"set-url", "https://example.test/guest"}, Report: "/passes set-url"},
		{Command: "/passes", Args: []string{"clear-url"}, Report: "/passes clear-url"},
		{Command: "/heapdump", Args: nil, Report: "/heapdump default-output"},
		{Command: "/debug-tool-call", Args: []string{"write_file", `{"path":"blocked.txt","content":"blocked"}`}, Report: "/debug-tool-call write_file"},
		{Command: "/debug-tool-call", Args: []string{"bash", `{"command":"echo blocked"}`}, Report: "/debug-tool-call bash"},
		{Command: "/branch", Args: []string{"create", "resume-test"}, Report: "/branch create"},
		{Command: "/tag", Args: []string{"create", "v9.9.9"}, Report: "/tag create"},
		{Command: "/stash", Args: []string{"push", "checkpoint"}, Report: "/stash push"},
	} {
		out, err = runResumedJSON(guarded.Command, guarded.Args...)
		require.Error(t, err, guarded.Command)
		var guardedExit *ExitError
		require.ErrorAs(t, err, &guardedExit, guarded.Command)
		require.True(t, guardedExit.Silent, guarded.Command)
		var guardedReport slashErrorReport
		require.NoError(t, json.Unmarshal([]byte(out), &guardedReport), guarded.Command)
		require.Equal(t, "unsupported_resumed_slash_command", guardedReport.ErrorKind, guarded.Command)
		require.Equal(t, guarded.Report, guardedReport.Command, guarded.Command)
	}

	out, err = runResumedJSON("/doctor")
	require.NoError(t, err)
	var resumedDoctor doctor.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDoctor))
	require.Equal(t, "doctor", resumedDoctor.Kind)
	require.NotEmpty(t, resumedDoctor.Checks)

	if gitAvailable {
		out, err = runResumedJSON("/diff")
		require.NoError(t, err)
		var resumedDiff diffReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedDiff))
		require.Equal(t, "diff", resumedDiff.Kind)
		require.False(t, resumedDiff.Empty)
		require.Contains(t, resumedDiff.Diff, "+after")

		out, err = runResumedJSON("/git", "status")
		require.NoError(t, err)
		var resumedGitStatus gitStatusReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedGitStatus))
		require.Equal(t, "git_status", resumedGitStatus.Kind)
		require.False(t, resumedGitStatus.Clean)
		require.NotEmpty(t, resumedGitStatus.Entries)

		out, err = runResumedJSON("/log", "1")
		require.NoError(t, err)
		var resumedLog gitLogReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedLog))
		require.Equal(t, "git_log", resumedLog.Kind)
		require.GreaterOrEqual(t, resumedLog.Count, 1)

		out, err = runResumedJSON("/blame", "tracked.txt", "1")
		require.NoError(t, err)
		var resumedBlame gitBlameReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedBlame))
		require.Equal(t, "git_blame", resumedBlame.Kind)
		require.GreaterOrEqual(t, resumedBlame.Count, 1)

		out, err = runResumedJSON("/changelog", "1")
		require.NoError(t, err)
		var resumedChangelog changelogReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedChangelog))
		require.Equal(t, "changelog", resumedChangelog.Kind)
		require.GreaterOrEqual(t, resumedChangelog.Count, 1)

		out, err = runResumedJSON("/release-notes", "--limit", "1")
		require.NoError(t, err)
		var resumedReleaseNotes struct {
			Kind string `json:"kind"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &resumedReleaseNotes))
		require.Equal(t, "release_notes", resumedReleaseNotes.Kind)

		out, err = runResumedJSON("/branch", "list")
		require.NoError(t, err)
		var resumedBranch branchReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedBranch))
		require.Equal(t, "branch", resumedBranch.Kind)
		require.Equal(t, "list", resumedBranch.Action)
		require.NotEmpty(t, resumedBranch.Current)

		out, err = runResumedJSON("/tag", "show", "v0.1.0")
		require.NoError(t, err)
		var resumedTag tagReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedTag))
		require.Equal(t, "tag", resumedTag.Kind)
		require.Equal(t, "show", resumedTag.Action)

		out, err = runResumedJSON("/stash", "list")
		require.NoError(t, err)
		var resumedStash stashReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedStash))
		require.Equal(t, "stash", resumedStash.Kind)
		require.Equal(t, "list", resumedStash.Action)

		out, err = runResumedJSON("/review", "--limit", "20")
		require.NoError(t, err)
		var resumedReview struct {
			Kind    string `json:"kind"`
			Summary struct {
				Files int `json:"files"`
			} `json:"summary"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &resumedReview))
		require.Equal(t, "review", resumedReview.Kind)
		require.GreaterOrEqual(t, resumedReview.Summary.Files, 1)
	}

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/clear"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var slashReport slashErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "confirmation_required", slashReport.ErrorKind)
	opened, err = store.Open("resume-slash")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 3)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/clear", "--confirm"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var clearReport clearResumedReport
	require.NoError(t, json.Unmarshal([]byte(out), &clearReport))
	require.Equal(t, "clear", clearReport.Kind)
	require.Equal(t, "clear_session", clearReport.Action)
	require.Equal(t, "resume-slash", clearReport.SessionID)
	require.Equal(t, 3, clearReport.OriginalMessages)
	require.Equal(t, 0, clearReport.RemainingMessages)
	require.Equal(t, 3, clearReport.RemovedMessages)
	require.FileExists(t, clearReport.Backup)
	backupData, err := os.ReadFile(clearReport.Backup)
	require.NoError(t, err)
	require.Contains(t, string(backupData), `"text":"four"`)
	opened, err = store.Open("resume-slash")
	require.NoError(t, err)
	require.Empty(t, opened.Messages)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/commit"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "unsupported_resumed_slash_command", slashReport.ErrorKind)
	require.Equal(t, "/commit", slashReport.Command)
	require.Contains(t, slashReport.Hint, "/help")
	require.Contains(t, slashReport.Hint, "/model")
	require.Contains(t, slashReport.Hint, "/status")
	require.NotContains(t, slashReport.Hint, "/commit")
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
	require.Contains(t, report.Protocol.Methods, "session/list")
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

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/get","params":{"session_id":"` + sess.ID + `"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/history","params":{"session_id":"` + sess.ID + `","limit":1}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/rename","params":{"session_id":"` + sess.ID + `","new_session_id":"renamed-acp-session"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/delete","params":{"session_id":"renamed-acp-session"}}`,
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
	listResult := responses[0]["result"].(map[string]any)
	require.Equal(t, "session_list", listResult["kind"])
	require.EqualValues(t, 1, listResult["count"])
	sessions := listResult["sessions"].([]any)
	require.Equal(t, sess.ID, sessions[0].(map[string]any)["session_id"])
	require.EqualValues(t, 1, sessions[0].(map[string]any)["message_count"])

	getResult := responses[1]["result"].(map[string]any)
	require.Equal(t, sess.ID, getResult["session_id"])
	require.EqualValues(t, 1, getResult["message_count"])
	require.Len(t, getResult["messages"].([]any), 1)

	historyResult := responses[2]["result"].(map[string]any)
	require.Equal(t, "session_history", historyResult["kind"])
	require.Equal(t, sess.ID, historyResult["session_id"])
	require.EqualValues(t, 1, historyResult["count"])
	entries := historyResult["entries"].([]any)
	require.Equal(t, "second prompt", entries[0].(map[string]any)["text"])

	renameResult := responses[3]["result"].(map[string]any)
	require.Equal(t, "session_mutation", renameResult["kind"])
	require.Equal(t, "rename", renameResult["action"])
	require.Equal(t, sess.ID, renameResult["session_id"])
	require.Equal(t, "renamed-acp-session", renameResult["new_session_id"])
	exists, err := store.Exists("renamed-acp-session")
	require.NoError(t, err)
	require.False(t, exists)

	deleteResult := responses[4]["result"].(map[string]any)
	require.Equal(t, "session_mutation", deleteResult["kind"])
	require.Equal(t, "delete", deleteResult["action"])
	require.Equal(t, "renamed-acp-session", deleteResult["session_id"])
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

func decodeJSONRPCResponses(t *testing.T, output string) []map[string]any {
	t.Helper()
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		responses = append(responses, response)
	}
	return responses
}

func TestParseFlagsSupportsPermissionSkipAliases(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--dangerously-skip-permissions", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.SkipPermissions)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--skip-permissions", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.SkipPermissions)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)
}

func TestParseFlagsSupportsBroadCWDOverride(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--allow-broad-cwd", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.AllowBroadCWD)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestParseFlagsSupportsTemperatureOverride(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--temperature", "0.4", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, overrides.Temperature)
	require.InDelta(t, 0.4, *overrides.Temperature, 0.0001)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestBroadWorkspaceGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	reason, normalized, broad := broadWorkspaceReason(home, home)
	require.True(t, broad)
	require.Equal(t, "home_directory", reason)
	require.Equal(t, filepath.Clean(home), normalized)

	root := filepath.VolumeName(home) + string(os.PathSeparator)
	reason, _, broad = broadWorkspaceReason(root, home)
	require.True(t, broad)
	require.Equal(t, "filesystem_root", reason)

	require.True(t, commandRequiresBroadCWDGuard("prompt", []string{"hello"}))
	require.True(t, commandRequiresBroadCWDGuard("team", []string{"create", "reviewers", "--task", "check"}))
	require.True(t, commandRequiresBroadCWDGuard("cron", []string{"run-due"}))
	require.False(t, commandRequiresBroadCWDGuard("team", []string{"list"}))
	require.False(t, commandRequiresBroadCWDGuard("status", nil))

	var out bytes.Buffer
	err := renderBroadCWDGuard(&out, "prompt", []string{"hello"}, home, false, "json")
	require.Error(t, err)
	var report broadCWDGuardReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "workspace_guard", report.Kind)
	require.Equal(t, "broad_cwd", report.ErrorKind)
	require.Equal(t, "home_directory", report.Reason)
	require.Contains(t, report.Hint, "--allow-broad-cwd")

	out.Reset()
	require.NoError(t, renderBroadCWDGuard(&out, "prompt", []string{"hello"}, home, true, "json"))
	require.Empty(t, out.String())

	out.Reset()
	require.NoError(t, renderBroadCWDGuard(&out, "status", nil, home, false, "json"))
	require.Empty(t, out.String())
}

func TestParseFlagsSupportsPrintAliases(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, config.FlagOverrides{}, overrides)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--print", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
	require.False(t, overrides.SkipPermissions)
}

func TestParseFlagsSupportsToolRuleOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--allowed-tools", "read_file,grep",
		"--allowedTools", "glob",
		"--disallowed-tools", "bash",
		"--disallowedTools", "write_file,edit_file",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"read_file", "grep", "glob"}, overrides.AllowedTools)
	require.Equal(t, []string{"bash", "write_file", "edit_file"}, overrides.DisallowedTools)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestGlobalToolRuleValidationContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--output-format", "json",
			"--allowedTools", "not_a_tool",
			"status",
		}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_tool_name", report.ErrorKind)
	require.Equal(t, "not_a_tool", report.ToolName)
	require.Equal(t, "--allowed-tools", report.Argument)
	require.Contains(t, report.Available, "web_fetch")
	require.Equal(t, "web_fetch", report.ToolAliases["WebFetch"])
	require.Contains(t, report.Hint, "canonical snake_case")
	require.Contains(t, report.Hint, "aliases")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--output-format", "json",
			"--allowed-tools", "Read,Bash(go test:*),mcp__playwright__*",
			"status",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &status))
	require.Equal(t, "status", status["kind"])

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--allowedTools", "status",
			"--output-format", "json",
		}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "--allowedTools", report.Argument)
	require.Contains(t, report.Hint, "read,glob")
}

func TestLocalRouteGuardContracts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	cases := [][]string{
		{"cost", "breakdown"},
		{"memory", "reset"},
		{"ultraplan", "bogus"},
		{"usage", "extra"},
		{"stats", "extra"},
		{"fork", "newbranch"},
	}
	for _, route := range cases {
		t.Run(strings.Join(route, " "), func(t *testing.T) {
			args := append([]string{"--config", configPath, "--output-format", "json"}, route...)
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.True(t, exitErr.Silent)
			var report slashErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "interactive_only", report.ErrorKind)
			require.NotEmpty(t, report.Hint)
			require.NotContains(t, out, "config_parse_error")
			require.NotContains(t, out, "missing_credentials")
		})
	}

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "model", "opus", "extra"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "unexpected_extra_args", report.ErrorKind)
	require.Equal(t, "model", report.Command)
	require.Equal(t, []string{"extra"}, report.Args)
	require.Contains(t, report.Hint, "codog model")
	require.NotContains(t, out, "config_parse_error")
	require.NotContains(t, out, "missing_credentials")
}

func TestConfigDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "config"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report struct {
		Kind                string        `json:"kind"`
		Action              string        `json:"action"`
		Status              string        `json:"status"`
		ErrorKind           string        `json:"error_kind"`
		Message             string        `json:"message"`
		Hint                string        `json:"hint"`
		ConfigLoadError     string        `json:"config_load_error"`
		ConfigLoadErrorKind string        `json:"config_load_error_kind"`
		Paths               []string      `json:"paths"`
		Config              config.Config `json:"config"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "config", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "config_load_failed", report.ErrorKind)
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)
	require.Contains(t, report.ConfigLoadError, "broken.json")
	require.Contains(t, report.Message, "unexpected end of JSON input")
	require.Contains(t, report.Hint, "codog doctor")
	require.Contains(t, report.Paths, configPath)
	require.NotEmpty(t, report.Config.Model)
	require.NotEmpty(t, report.Config.PermissionMode)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/config", "paths"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "paths", report.Action)
	require.Contains(t, report.Paths, configPath)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "config", "paths"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, out, "Config")
	require.Contains(t, out, "Config load")
	require.Contains(t, out, "broken.json")
}

func TestLocalSubcommandErrorContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name      string
		args      []string
		kind      string
		action    string
		errorKind string
		hintPart  string
	}{
		{
			name:      "agents unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "agents", "bogus"},
			kind:      "agents",
			action:    "bogus",
			errorKind: "unknown_agents_subcommand",
			hintPart:  "agents list",
		},
		{
			name:      "plugins unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "bogus"},
			kind:      "plugins",
			action:    "bogus",
			errorKind: "unknown_plugins_action",
			hintPart:  "plugins list",
		},
		{
			name:      "mcp unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "mcp", "bogus"},
			kind:      "mcp",
			action:    "error",
			errorKind: "unsupported_action",
			hintPart:  "codog mcp",
		},
		{
			name:      "mcp show missing",
			args:      []string{"--config", configPath, "--output-format", "json", "mcp", "show"},
			kind:      "mcp",
			action:    "show",
			errorKind: "missing_argument",
			hintPart:  "mcp show <server>",
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
			require.True(t, exitErr.Silent)
			var report actionErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, tc.kind, report.Kind)
			require.Equal(t, tc.action, report.Action)
			require.Equal(t, "error", report.Status)
			require.Equal(t, tc.errorKind, report.ErrorKind)
			require.Contains(t, report.Hint, tc.hintPart)
		})
	}
}

func TestLocalExtraArgumentErrorContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name    string
		args    []string
		command string
		extra   []string
		hint    string
	}{
		{
			name:    "config show extra",
			args:    []string{"--config", configPath, "--output-format", "json", "config", "show", "bogus-key"},
			command: "config show",
			extra:   []string{"bogus-key"},
			hint:    "codog config show",
		},
		{
			name:    "agents show extra",
			args:    []string{"--config", configPath, "--output-format", "json", "agents", "show", "some-agent", "--extra-flag"},
			command: "agents show",
			extra:   []string{"--extra-flag"},
			hint:    "codog agents show",
		},
		{
			name:    "skills show extra",
			args:    []string{"--config", configPath, "--output-format", "json", "skills", "show", "some-skill", "--extra-flag"},
			command: "skills show",
			extra:   []string{"--extra-flag"},
			hint:    "codog skills show",
		},
		{
			name:    "plugins show extra",
			args:    []string{"--config", configPath, "--output-format", "json", "plugins", "show", "some-plugin", "extra-arg"},
			command: "plugins show",
			extra:   []string{"extra-arg"},
			hint:    "codog plugins show",
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
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "unexpected_extra_args", report.ErrorKind)
			require.Equal(t, tc.command, report.Command)
			require.Equal(t, tc.extra, report.Args)
			require.Contains(t, report.Hint, tc.hint)
		})
	}
}

func TestLocalListFlagOptionContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name      string
		args      []string
		command   string
		errorKind string
		hint      string
	}{
		{
			name:      "agents list flag",
			args:      []string{"--config", configPath, "--output-format", "json", "agents", "list", "--unknown-flag"},
			command:   "agents list",
			errorKind: "unknown_option",
			hint:      "codog agents list",
		},
		{
			name:      "skills list flag",
			args:      []string{"--config", configPath, "--output-format", "json", "skills", "list", "--unknown-flag"},
			command:   "skills list",
			errorKind: "unknown_option",
			hint:      "codog skills list",
		},
		{
			name:      "plugins list flag",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "list", "--unknown-flag"},
			command:   "plugins list",
			errorKind: "cli_parse",
			hint:      "codog plugins list",
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
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, tc.errorKind, report.ErrorKind)
			require.Equal(t, "error", report.Status)
			require.Equal(t, tc.command, report.Command)
			require.Equal(t, "--unknown-flag", report.Option)
			require.Contains(t, report.Hint, tc.hint)
		})
	}
}

func TestPluginMutationErrorContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name      string
		args      []string
		action    string
		errorKind string
		hint      string
	}{
		{
			name:      "uninstall not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "uninstall", "no-such-plugin"},
			action:    "uninstall",
			errorKind: "plugin_not_found",
			hint:      "plugins list",
		},
		{
			name:      "remove not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "remove", "no-such-plugin"},
			action:    "remove",
			errorKind: "plugin_not_found",
			hint:      "plugins list",
		},
		{
			name:      "show not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "show", "no-such-plugin"},
			action:    "show",
			errorKind: "plugin_not_found",
			hint:      "plugins list",
		},
		{
			name:      "install source not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "install", filepath.Join(t.TempDir(), "missing-plugin")},
			action:    "install",
			errorKind: "plugin_source_not_found",
			hint:      "plugin.json",
		},
		{
			name:      "validate source not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "validate", filepath.Join(t.TempDir(), "missing-plugin")},
			action:    "validate",
			errorKind: "plugin_source_not_found",
			hint:      "plugin.json",
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
			require.True(t, exitErr.Silent)
			var report actionErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "plugins", report.Kind)
			require.Equal(t, tc.action, report.Action)
			require.Equal(t, "error", report.Status)
			require.Equal(t, tc.errorKind, report.ErrorKind)
			require.Contains(t, report.Hint, tc.hint)
		})
	}
}

func TestMarketplaceValidateCommand(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"demo","name":"demo","version":"1.0.0","description":"Demo","tools":[{"name":"demo_tool","command":"echo","permission":"read-only"}]}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out}
	require.NoError(t, app.Marketplace([]string{"validate", source, "--json"}))
	var report pluginValidationReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "plugin", report.Kind)
	require.Equal(t, "validate", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.Success)
	require.Empty(t, report.Errors)
	require.Equal(t, "demo", report.Manifest.ID)

	out.Reset()
	badSource := filepath.Join(t.TempDir(), "bad")
	require.NoError(t, os.MkdirAll(badSource, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badSource, "plugin.json"), []byte(`{"id":"bad","name":"bad","tools":[{"name":"bad_tool","command":"echo","permission":"root"}]}`), 0o644))
	err := app.Marketplace([]string{"validate", badSource, "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "error", report.Status)
	require.False(t, report.Success)
	require.Equal(t, "invalid_tool_permission", report.Errors[0].Code)
}

func TestParseFlagsSupportsSystemPromptOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--system-prompt", "base",
		"--append-system-prompt", "extra",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "base", overrides.SystemPrompt)
	require.Equal(t, "extra", overrides.AppendPrompt)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestParseFlagsSupportsGlobalOutputFormat(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--output-format", "json", "status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, config.FlagOverrides{}, overrides)
	require.Equal(t, "status", command)
	require.Equal(t, []string{"--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--json", "skills", "show", "review"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "skills", command)
	require.Equal(t, []string{"show", "review", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format=json", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "status", "--output-format", "text"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Equal(t, []string{"--output-format", "text"}, rest)

	_, command, rest, err = parseFlags([]string{"--json", "-p", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "plugins"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "plugins", command)
	require.Equal(t, []string{"--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "diff"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "diff", command)
	require.Equal(t, []string{"--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "log", "1"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "log", command)
	require.Equal(t, []string{"1", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "blame", "notes.txt", "1"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "blame", command)
	require.Equal(t, []string{"notes.txt", "1", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "changelog", "1"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "changelog", command)
	require.Equal(t, []string{"1", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "stash", "list"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "stash", command)
	require.Equal(t, []string{"list", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "text", "commit", "--all", "message"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "commit", command)
	require.Equal(t, []string{"--all", "message", "--output-format", "text"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "config", "get", "auth"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "config", command)
	require.Equal(t, []string{"get", "auth", "--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "help", "doctor"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "help", command)
	require.Equal(t, []string{"doctor", "--output-format", "json"}, rest)
}

func TestParseFlagsSupportsOutputFormatEnv(t *testing.T) {
	t.Setenv("CODOG_OUTPUT_FORMAT", "json")

	_, command, rest, err := parseFlags([]string{"status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Equal(t, []string{"--output-format", "json"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "text", "status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Equal(t, []string{"--output-format", "text"}, rest)

	_, command, rest, err = parseFlags([]string{"config", "get", "auth"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "config", command)
	require.Equal(t, []string{"get", "auth", "--output-format", "json"}, rest)
}

func TestParsePromptArgsExtractsOutputFormat(t *testing.T) {
	req, err := parsePromptArgs([]string{"hello", "--output-format", "json"})
	require.NoError(t, err)
	require.Equal(t, "hello", req.Prompt)
	require.Equal(t, "json", req.Format)
	require.True(t, req.PromptProvided)

	req, err = parsePromptArgs([]string{"--output-format=stream-json", "--", "--json", "literal"})
	require.NoError(t, err)
	require.Equal(t, "--json literal", req.Prompt)
	require.Equal(t, "stream-json", req.Format)
	require.True(t, req.PromptProvided)

	req, err = parsePromptArgs([]string{"--json"})
	require.NoError(t, err)
	require.Empty(t, req.Prompt)
	require.Equal(t, "json", req.Format)
	require.False(t, req.PromptProvided)
}

func TestPromptMissingPromptOutputContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	emptyStdin := filepath.Join(t.TempDir(), "stdin.txt")
	require.NoError(t, os.WriteFile(emptyStdin, nil, 0o644))
	stdinFile, err := os.Open(emptyStdin)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinFile
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, stdinFile.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "prompt"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report promptErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "abort", report.Action)
	require.Equal(t, "missing_prompt", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Contains(t, report.Hint, "codog prompt")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "prompt", ""}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_prompt", report.ErrorKind)
}

func TestDumpManifestsCommand(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "helper.json"), []byte(`{"prompt":"help"}`), 0o644))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.DumpManifests([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "dump-manifests"`)
	require.Contains(t, out.String(), `"source": "go-resolver"`)
	require.Contains(t, out.String(), `"name": "review"`)
	require.Contains(t, out.String(), `"name": "/status"`)
	require.Contains(t, out.String(), `"implemented": true`)
	require.Contains(t, out.String(), `"enabled": true`)
	out.Reset()

	require.NoError(t, app.DumpManifests(nil))
	require.Contains(t, out.String(), "Manifest Dump")
	out.Reset()

	otherWorkspace := t.TempDir()
	require.NoError(t, app.DumpManifests([]string{"--manifests-dir", otherWorkspace, "--json"}))
	require.Contains(t, out.String(), otherWorkspace)

	err := app.DumpManifests([]string{"--manifests-dir", filepath.Join(t.TempDir(), "missing")})
	require.ErrorContains(t, err, "missing_manifests")
}

func TestSystemPromptCommand(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			SystemPrompt:       "Custom base.",
			AppendSystemPrompt: "Extra instructions.",
		},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.SystemPromptCommand([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "system-prompt"`)
	require.Contains(t, out.String(), `"action": "show"`)
	require.Contains(t, out.String(), "Custom base.")
	out.Reset()

	require.NoError(t, app.SystemPromptCommand(nil))
	require.Contains(t, out.String(), "Custom base.")
	require.Contains(t, out.String(), "Extra instructions.")
}

func TestToolDetailsCommandReportsToolAndErrors(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
	}

	require.NoError(t, app.ToolDetails([]string{"bash", "--json"}))
	var report toolDetailsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "tool_details", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "bash", report.Tool.Name)
	require.Equal(t, tools.PermissionDanger, report.Tool.Permission)
	require.Contains(t, report.Aliases, "Bash")
	out.Reset()

	require.NoError(t, app.ToolDetails([]string{"Read"}))
	require.Contains(t, out.String(), "Name             read_file")
	require.Contains(t, out.String(), "Aliases")
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]any{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/tool-details", "bash"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cliReport toolDetailsReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliReport))
	require.Equal(t, "tool_details", cliReport.Kind)
	require.Equal(t, "bash", cliReport.Tool.Name)

	err = app.ToolDetails([]string{"missing_tool", "--json"})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "invalid_tool_name", errorReport.ErrorKind)
	require.Equal(t, "missing_tool", errorReport.ToolName)
	require.Contains(t, errorReport.Available, "bash")
	require.Equal(t, "bash", errorReport.ToolAliases["Bash"])
	out.Reset()

	err = app.ToolDetails([]string{"--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "missing_tool_name", errorReport.ErrorKind)
	require.Equal(t, "tool-details", errorReport.Command)
}

func TestSessionsCommandForkExistsAndDelete(t *testing.T) {
	configHome := t.TempDir()
	store := session.NewStore(configHome)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"exists", "source"}))
	require.Contains(t, out.String(), `"exists": true`)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"fork", "source", "branch"}))
	require.Contains(t, out.String(), `"ID":`)
	require.Contains(t, out.String(), "hello session")
	var forked session.Session
	require.NoError(t, json.Unmarshal(out.Bytes(), &forked))
	require.NotEmpty(t, forked.ID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"delete", forked.ID}))
	require.Contains(t, out.String(), `"deleted": true`)
}

func TestResumeCommandReportsSessionAndContinueCommands(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "hello back")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Executable: "codog"}

	require.NoError(t, app.ResumeCommand([]string{"source", "--json"}))
	var report resumeCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "source", report.RequestedSession)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 2, report.MessageCount)
	require.Contains(t, report.ContinueCommands[0], "--resume 'source' repl")
	out.Reset()

	require.NoError(t, app.ResumeCommand([]string{"latest"}))
	require.Contains(t, out.String(), "Resume Session")
	require.Contains(t, out.String(), "Session ID        source")
	require.Contains(t, out.String(), "Messages          2")
}

func TestClearCommandReportsFreshSessionWithoutDeletingHistory(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Executable: "codog"}

	require.NoError(t, app.ClearCommand([]string{"--confirm", "--json"}))
	var report clearCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "clear", report.Kind)
	require.Equal(t, "create_session", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotEmpty(t, report.SessionID)
	require.NotEqual(t, "source", report.SessionID)
	require.Equal(t, 0, report.MessageCount)
	require.Contains(t, report.ContinueCommands[0], "--session '"+report.SessionID+"' repl")
	newExists, err := store.Exists(report.SessionID)
	require.NoError(t, err)
	require.True(t, newExists)
	exists, err := store.Exists("source")
	require.NoError(t, err)
	require.True(t, exists)
	out.Reset()

	require.NoError(t, app.ClearCommand(nil))
	require.Contains(t, out.String(), "Clear Session")
	require.Contains(t, out.String(), "Messages          0")
}

func TestBreakCacheCreatesSessionWhenNoLatestExists(t *testing.T) {
	store := session.NewStore(t.TempDir())
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.BreakCache([]string{"--json"}, config.FlagOverrides{}))
	var report breakCacheReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "break_cache", report.Kind)
	require.True(t, report.CreatedSession)
	require.NotEmpty(t, report.SessionID)
	require.NotEmpty(t, report.Nonce)
	opened, err := store.Open(report.SessionID)
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Contains(t, opened.Messages[0].Content[0].Text, report.Nonce)
}

func TestRunCLISessionAliasAndResumeCommand(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "session", "exists", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"exists": true`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "resume", "source", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report resumeCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 1, report.MessageCount)
}

func TestResumeMissingSessionReportsTypedError(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "resume", "missing-session", "--json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report sessionRestoreErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "session_not_found", report.ErrorKind)
	require.Equal(t, "missing-session", report.RequestedSession)
	require.Contains(t, report.Hint, "codog sessions list")
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoFileExists(t, filepath.Join(store.Dir, "missing-session.jsonl"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "resume", "missing-session"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "session_not_found")
	require.Contains(t, err.Error(), "codog sessions list")
}

func TestResumeDirectoryPathReportsTypedError(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	directoryPath := filepath.Join(t.TempDir(), "session-dir")
	require.NoError(t, os.MkdirAll(directoryPath, 0o755))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", directoryPath, "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report sessionRestoreErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "session_path_is_directory", report.ErrorKind)
	require.Equal(t, directoryPath, report.Path)
	require.Contains(t, report.Hint, ".jsonl")
	require.Contains(t, report.Hint, "codog sessions list --json")
}

func TestRunCLIClearCommand(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "clear", "--confirm"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report clearCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "clear", report.Kind)
	require.Equal(t, "create_session", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotEmpty(t, report.SessionID)
	require.Contains(t, report.ContinueCommands[0], "--session '"+report.SessionID+"' repl")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "conversation", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "clear", report.Kind)
	require.Equal(t, "create_session", report.Action)
	require.NotEmpty(t, report.SessionID)
}

func TestBackfillSessionsCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("user", "legacy prompt")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.BackfillSessions([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "backfill_sessions"`)
	require.Contains(t, out.String(), `"sessions_updated": 1`)
	require.Contains(t, out.String(), `"inputs_added": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/backfill-sessions", &session.Session{ID: "legacy"}))
	require.Contains(t, out.String(), "Backfill Sessions")
	require.Contains(t, out.String(), "Sessions scanned 1")
	require.Empty(t, errOut.String())
}

func TestRewindCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "first answer")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "second prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "second answer")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.Rewind([]string{"2", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "rewind"`)
	require.Contains(t, out.String(), `"removed_messages": 2`)
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 2)
	out.Reset()

	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "third prompt")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, sess.Messages, 3)

	require.True(t, app.handleSlash(context.Background(), "/rewind 1", sess))
	require.Len(t, sess.Messages, 2)
	require.Contains(t, out.String(), "Removed          1")
	require.Empty(t, errOut.String())
}

func TestSessionSlashSwitchAndFork(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello slash")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("source")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session fork branch", sess))
	require.NotEqual(t, "source", sess.ID)
	require.Contains(t, errOut.String(), "session forked:")
	forkedID := sess.ID
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session switch source", sess))
	require.Equal(t, "source", sess.ID)
	require.Contains(t, errOut.String(), "session switched: source")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session delete "+forkedID, sess))
	require.Contains(t, errOut.String(), "session deleted: "+forkedID)
}

func TestRenameSessionCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "rename me")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.Rename([]string{"cli-renamed", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"old_id": "source"`)
	require.Contains(t, out.String(), `"new_id": "cli-renamed"`)
	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.False(t, ok)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"rename", "cli-renamed", "sessions-renamed"}))
	require.Contains(t, out.String(), `"new_id": "sessions-renamed"`)
	out.Reset()

	sess, err := store.Open("sessions-renamed")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/rename slash-renamed", sess))
	require.Equal(t, "slash-renamed", sess.ID)
	require.Contains(t, errOut.String(), "session renamed: sessions-renamed -> slash-renamed")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session rename final-renamed", sess))
	require.Equal(t, "final-renamed", sess.ID)
	require.Contains(t, errOut.String(), "session renamed: slash-renamed -> final-renamed")
	opened, err := store.Open("final-renamed")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "rename me", opened.Messages[0].Content[0].Text)
}

func TestGenerateSessionNameCommandAndSlash(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "Fix the HTTP 500 in API users endpoint"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "Fix the HTTP 500 in API users endpoint")))
	require.NoError(t, store.AppendInput("existing", "Fix the HTTP 500 in API users endpoint"))
	require.NoError(t, store.Append("existing", anthropic.TextMessage("user", "collision holder")))
	_, err := store.Rename("existing", "fix-http-500-api-users-endpoint")
	require.NoError(t, err)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.GenerateSessionName([]string{"--session", "source", "--json"}, config.FlagOverrides{}))
	var report sessionname.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_name", report.Kind)
	require.Equal(t, "generate", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, "fix-http-500-api-users-endpoint-2", report.SuggestedID)
	require.Equal(t, 1, report.CollisionCount)
	require.Equal(t, "first_prompt", report.Source)
	out.Reset()

	require.NoError(t, app.GenerateSessionName([]string{"--session", "source", "--rename", "--json"}, config.FlagOverrides{}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "renamed", report.Status)
	require.True(t, report.Renamed)
	require.Equal(t, "source", report.OldID)
	require.Equal(t, "fix-http-500-api-users-endpoint-2", report.NewID)
	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.Exists("fix-http-500-api-users-endpoint-2")
	require.NoError(t, err)
	require.True(t, ok)
	out.Reset()

	require.NoError(t, store.AppendInput("slash", "Add session name slash support"))
	require.NoError(t, store.Append("slash", anthropic.TextMessage("user", "Add session name slash support")))
	sess, err := store.Open("slash")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/generateSessionName --rename --json", sess))
	require.Equal(t, "add-session-name-slash-support", sess.ID)
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "renamed", report.Status)
	require.Equal(t, "add-session-name-slash-support", report.NewID)
	require.Empty(t, errOut.String())
}

func TestClearAndResumeSlashSwitchSessionState(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "resume me")))
	sess, err := store.Open("source")
	require.NoError(t, err)
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/clear", sess))
	require.NotEqual(t, "source", sess.ID)
	require.Empty(t, sess.Messages)
	require.Contains(t, errOut.String(), "session cleared:")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/resume source", sess))
	require.Equal(t, "source", sess.ID)
	require.Len(t, sess.Messages, 1)
	require.Contains(t, errOut.String(), "session resumed: source")
}

func TestResumeRestoresTodosFromTranscript(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	require.NoError(t, store.Append("source", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{
		Type:  "tool_use",
		Name:  "TodoWrite",
		Input: []byte(`{"todos":[{"content":"restore todo","status":"in_progress","priority":"high"}]}`),
	}}}))
	require.NoError(t, store.Append("done", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{
		Type:  "tool_use",
		Name:  "todo_write",
		Input: []byte(`{"todos":[{"content":"finished","status":"completed","priority":"low"}]}`),
	}}}))
	sess, err := store.Open("")
	require.NoError(t, err)
	app := &App{
		Config:    config.Config{Model: "mock", PermissionMode: "workspace-write"},
		Sessions:  store,
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}

	require.True(t, app.handleSlash(context.Background(), "/resume source", sess))
	state, err := todos.Load(workspace)
	require.NoError(t, err)
	require.Len(t, state.Items, 1)
	require.Equal(t, "restore todo", state.Items[0].Content)
	require.Equal(t, "in_progress", state.Items[0].Status)

	_, err = app.openSession(config.FlagOverrides{Resume: "done"})
	require.NoError(t, err)
	state, err = todos.Load(workspace)
	require.NoError(t, err)
	require.Empty(t, state.Items)
}

func TestRuntimeInfoSlashCommands(t *testing.T) {
	var out bytes.Buffer
	app := &App{Workspace: t.TempDir(), Out: &out, Err: io.Discard}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/version", sess))
	require.Contains(t, out.String(), "Codog")
	require.Contains(t, out.String(), "Version")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/acp --json", sess))
	require.Contains(t, out.String(), `"kind": "acp"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox", sess))
	require.Contains(t, out.String(), `"os":`)
	require.Contains(t, out.String(), `"strategy_statuses":`)
	require.Contains(t, out.String(), `"container":`)
	require.Contains(t, out.String(), `"namespace_supported":`)
	require.Contains(t, out.String(), `"requested":`)
	require.Contains(t, out.String(), `"filesystem_mode":`)
	require.Contains(t, out.String(), `"active_components":`)
}

func TestSandboxCommandReportsConfiguredRequest(t *testing.T) {
	workspace := t.TempDir()
	enabled := true
	namespace := false
	network := true
	var out bytes.Buffer
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			Sandbox: config.SandboxConfig{
				Enabled:               &enabled,
				NamespaceRestrictions: &namespace,
				NetworkIsolation:      &network,
				FilesystemMode:        "allow-list",
				AllowedMounts:         []string{"logs"},
			},
		}},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Sandbox())
	var report struct {
		Kind               string                         `json:"kind"`
		Action             string                         `json:"action"`
		Status             string                         `json:"status"`
		ConfiguredStrategy string                         `json:"configured_strategy"`
		Requested          bool                           `json:"requested"`
		RequestedNamespace bool                           `json:"requested_namespace"`
		RequestedNetwork   bool                           `json:"requested_network"`
		FilesystemMode     string                         `json:"filesystem_mode"`
		AllowedMounts      []string                       `json:"allowed_mounts"`
		Markers            []string                       `json:"markers"`
		Execution          sandbox.SandboxExecutionStatus `json:"execution"`
		ActiveComponents   map[string]bool                `json:"active_components"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "sandbox", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Contains(t, []string{"ok", "warn", "error"}, report.Status)
	require.Equal(t, "detect", report.ConfiguredStrategy)
	require.True(t, report.Requested)
	require.False(t, report.RequestedNamespace)
	require.True(t, report.RequestedNetwork)
	require.Equal(t, "allow-list", report.FilesystemMode)
	require.Equal(t, []string{filepath.Join(workspace, "logs")}, report.AllowedMounts)
	require.NotNil(t, report.Markers)
	require.NotNil(t, report.ActiveComponents)
	require.True(t, report.Execution.Requested.Enabled)
	require.False(t, report.Execution.Requested.NamespaceRestrictions)
	require.True(t, report.Execution.Requested.NetworkIsolation)
	require.Equal(t, sandbox.FilesystemIsolationAllowList, report.Execution.Requested.FilesystemMode)
}

func TestSandboxToggleCommandPersistsSettings(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.SandboxToggle([]string{"detect", "--json"}))
	require.Contains(t, out.String(), `"kind": "sandbox_toggle"`)
	require.Contains(t, out.String(), `"configured_strategy": "detect"`)
	require.Contains(t, out.String(), `"resolution_status":`)
	require.Contains(t, out.String(), `"namespace_supported":`)
	require.Equal(t, "detect", app.Config.Future.SandboxStrategy)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"sandbox_strategy": "detect"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/sandbox-toggle off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Sandbox Toggle")
	require.Contains(t, out.String(), "Configured       off")
	require.Contains(t, out.String(), "Namespace")
	require.Equal(t, "off", app.Config.Future.SandboxStrategy)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.SandboxToggle([]string{"restricted-token", "--json"}))
	require.Contains(t, out.String(), `"configured_strategy": "restricted-token"`)
	require.Equal(t, "restricted-token", app.Config.Future.SandboxStrategy)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"sandbox_strategy": "restricted-token"`)
	out.Reset()

	require.NoError(t, app.SandboxToggle([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"configured_strategy": ""`)
	require.Equal(t, "", app.Config.Future.SandboxStrategy)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "sandbox_strategy")
}

func TestHeapDumpCommandWritesProfile(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	path := filepath.Join(workspace, "heap.pprof")

	require.NoError(t, app.HeapDump([]string{path, "--json"}))
	require.Contains(t, out.String(), `"kind": "heapdump"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"gc": true`)
	stat, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, stat.Size(), int64(0))
	out.Reset()

	slashPath := filepath.Join(workspace, "slash.pprof")
	require.True(t, app.handleSlash(context.Background(), "/heapdump "+slashPath+" --no-gc", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Heap Dump")
	require.Contains(t, out.String(), "GC               false")
	stat, err = os.Stat(slashPath)
	require.NoError(t, err)
	require.Greater(t, stat.Size(), int64(0))
	require.Empty(t, errOut.String())
}

func TestSystemPromptAndToolDetailsSlashCommands(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use the project style."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("debug notes\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/system-prompt", sess))
	require.Contains(t, out.String(), "Use the project style.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tool-details bash", sess))
	require.Contains(t, out.String(), "Tool")
	require.Contains(t, out.String(), "Name             bash")
	require.Contains(t, out.String(), "Permission       danger-full-access")
	require.Contains(t, out.String(), `"command"`)
	out.Reset()

	require.NoError(t, app.DebugToolCall(context.Background(), []string{"read_file", `{"path":"notes.txt"}`, "--json"}, config.FlagOverrides{SessionID: "session"}))
	require.Contains(t, out.String(), `"kind": "debug_tool_call"`)
	require.Contains(t, out.String(), `"tool": "read_file"`)
	require.Contains(t, out.String(), `"success": true`)
	require.Contains(t, out.String(), "debug notes")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), `/debug-tool-call read_file {"path": "notes.txt"}`, sess))
	require.Contains(t, out.String(), "Tool Call")
	require.Contains(t, out.String(), "Tool             read_file")
	require.Contains(t, out.String(), "debug notes")
	require.Empty(t, errOut.String())
}

func TestGitCommandStatusDiffAndCommit(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Git([]string{"status"}))
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	require.NoError(t, app.Git([]string{"status", "--json"}))
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.Equal(t, "show", statusJSON.Action)
	require.Equal(t, "ok", statusJSON.Status)
	require.False(t, statusJSON.Clean)
	require.NotEmpty(t, statusJSON.BranchLine)
	require.Len(t, statusJSON.Entries, 1)
	require.Equal(t, "??", statusJSON.Entries[0].Code)
	require.Equal(t, "notes.txt", statusJSON.Entries[0].Path)
	out.Reset()

	require.NoError(t, app.Git([]string{"--output-format", "JSON", "status"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.False(t, statusJSON.Clean)
	out.Reset()

	require.NoError(t, app.Git([]string{"commit", "--all", "add", "notes"}))
	require.Contains(t, out.String(), `"commit":`)
	require.Contains(t, out.String(), "add notes")
	var commitJSON commitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &commitJSON))
	require.Equal(t, "commit", commitJSON.Kind)
	require.Equal(t, "create", commitJSON.Action)
	require.Equal(t, "ok", commitJSON.Status)
	require.True(t, commitJSON.All)
	require.Contains(t, commitJSON.Summary, "add notes")
	out.Reset()

	require.NoError(t, app.Git([]string{"log", "1"}))
	require.Contains(t, out.String(), "add notes")
	out.Reset()

	require.NoError(t, app.Git([]string{"log", "1", "--json"}))
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "show", logJSON.Action)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Limit)
	require.Equal(t, 1, logJSON.Count)
	require.Len(t, logJSON.Entries, 1)
	require.Equal(t, "add notes", logJSON.Entries[0].Subject)
	require.NotEmpty(t, logJSON.Entries[0].Commit)
	require.Contains(t, logJSON.Raw, "add notes")
	out.Reset()

	require.NoError(t, app.Changelog([]string{"1"}))
	require.Contains(t, out.String(), "add notes")
	require.Contains(t, out.String(), "notes.txt")
	out.Reset()

	require.NoError(t, app.Changelog([]string{"1", "--json"}))
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "show", changelogJSON.Action)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Limit)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "add notes", changelogJSON.Entries[0].Subject)
	require.Contains(t, changelogJSON.Raw, "notes.txt")
	out.Reset()

	runGit(t, workspace, "tag", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	require.NoError(t, app.Git([]string{"--output-format", "text", "commit", "--all", "feat: add feature"}))
	require.Contains(t, out.String(), "Commit")
	require.Contains(t, out.String(), "feat: add feature")
	out.Reset()
	require.NoError(t, app.ReleaseNotes([]string{"--from", "v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "release_notes"`)
	require.Contains(t, out.String(), `"name": "Features"`)
	require.Contains(t, out.String(), `"subject": "feat: add feature"`)
	out.Reset()

	require.NoError(t, app.Git([]string{"blame", "notes.txt", "1"}))
	require.Contains(t, out.String(), "hello")
	out.Reset()

	require.NoError(t, app.Git([]string{"blame", "notes.txt", "1", "--json"}))
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "show", blameJSON.Action)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, "notes.txt", blameJSON.Path)
	require.Equal(t, 1, blameJSON.Line)
	require.Equal(t, 1, blameJSON.Count)
	require.Len(t, blameJSON.Entries, 1)
	require.Equal(t, "hello", blameJSON.Entries[0].Line)
	require.Equal(t, "add notes", blameJSON.Entries[0].Summary)
	require.Contains(t, blameJSON.Raw, "hello")
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\nagain\n"), 0o644))
	require.NoError(t, app.Git([]string{"diff"}))
	require.Contains(t, out.String(), "+again")
	out.Reset()

	require.NoError(t, app.Git([]string{"diff", "--json", "notes.txt"}))
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "show", diffJSON.Action)
	require.Equal(t, "ok", diffJSON.Status)
	require.False(t, diffJSON.Staged)
	require.False(t, diffJSON.Empty)
	require.Equal(t, []string{"notes.txt"}, diffJSON.Paths)
	require.Contains(t, diffJSON.Diff, "+again")
	out.Reset()

	runGit(t, workspace, "add", "notes.txt")
	require.NoError(t, app.Git([]string{"diff", "--staged", "--output-format", "json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.True(t, diffJSON.Staged)
	require.False(t, diffJSON.Empty)
	require.Contains(t, diffJSON.Diff, "+again")
	out.Reset()

	require.NoError(t, app.Stash([]string{"push", "agent stash"}))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.NoError(t, app.Git([]string{"stash", "list"}))
	require.Contains(t, out.String(), "agent stash")
	out.Reset()

	require.NoError(t, app.Stash([]string{"list", "--json"}))
	var stashJSON stashReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, "list", stashJSON.Action)
	require.Equal(t, "ok", stashJSON.Status)
	require.Equal(t, 1, stashJSON.Count)
	require.Len(t, stashJSON.Stashes, 1)
	require.Equal(t, "stash@{0}", stashJSON.Stashes[0].Ref)
	require.Contains(t, stashJSON.Stashes[0].Subject, "agent stash")
	require.Contains(t, stashJSON.Output, "agent stash")
	out.Reset()

	require.NoError(t, app.Git([]string{"--json", "stash", "list"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, 1, stashJSON.Count)
}

func TestRunCLIRoutesTopLevelGitAliases(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello cli\n"), 0o644))
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "commit", "--all", "cli", "alias", "commit"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"commit":`)
	require.Contains(t, out, "cli alias commit")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "log", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "cli alias commit")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "log", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal([]byte(out), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Count)
	require.Equal(t, "cli alias commit", logJSON.Entries[0].Subject)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "git", "--json", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.True(t, statusJSON.Clean)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "stash", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var emptyStashJSON stashReport
	require.NoError(t, json.Unmarshal([]byte(out), &emptyStashJSON))
	require.Equal(t, "stash", emptyStashJSON.Kind)
	require.Equal(t, "ok", emptyStashJSON.Status)
	require.Equal(t, 0, emptyStashJSON.Count)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "changelog", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal([]byte(out), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "cli alias commit", changelogJSON.Entries[0].Subject)
	require.Contains(t, changelogJSON.Raw, "notes.txt")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "blame", "notes.txt", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "hello cli")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "blame", "notes.txt", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal([]byte(out), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, 1, blameJSON.Count)
	require.Equal(t, "hello cli", blameJSON.Entries[0].Line)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello cli\nagain\n"), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "+again")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "diff"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal([]byte(out), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "ok", diffJSON.Status)
	require.Contains(t, diffJSON.Diff, "+again")
}

func TestGitSlashDiffAndCommit(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/commit --all slash commit", sess))
	require.Contains(t, errOut.String(), "commit ")
	errOut.Reset()
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "slash-json.txt"), []byte("slash json\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/commit --json --all slash json commit", sess))
	var commitJSON commitReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &commitJSON))
	require.Equal(t, "commit", commitJSON.Kind)
	require.Equal(t, "ok", commitJSON.Status)
	require.Contains(t, commitJSON.Summary, "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/log 1", sess))
	require.Contains(t, out.String(), "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/log --json 1", sess))
	var logJSON gitLogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logJSON))
	require.Equal(t, "git_log", logJSON.Kind)
	require.Equal(t, "ok", logJSON.Status)
	require.Equal(t, 1, logJSON.Count)
	require.Equal(t, "slash json commit", logJSON.Entries[0].Subject)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/changelog 1", sess))
	require.Contains(t, out.String(), "slash json commit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/changelog --json 1", sess))
	var changelogJSON changelogReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &changelogJSON))
	require.Equal(t, "changelog", changelogJSON.Kind)
	require.Equal(t, "ok", changelogJSON.Status)
	require.Equal(t, 1, changelogJSON.Count)
	require.Equal(t, "slash json commit", changelogJSON.Entries[0].Subject)
	out.Reset()

	runGit(t, workspace, "tag", "v0.2.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: add slash feature")
	require.True(t, app.handleSlash(context.Background(), "/release-notes v0.2.0", sess))
	require.Contains(t, out.String(), "# Release Notes")
	require.Contains(t, out.String(), "feat: add slash feature")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/blame notes.txt 1", sess))
	require.Contains(t, out.String(), "hello slash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/blame notes.txt 1 --json", sess))
	var blameJSON gitBlameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &blameJSON))
	require.Equal(t, "git_blame", blameJSON.Kind)
	require.Equal(t, "ok", blameJSON.Status)
	require.Equal(t, 1, blameJSON.Count)
	require.Equal(t, "hello slash", blameJSON.Entries[0].Line)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git status", sess))
	require.Contains(t, out.String(), "## ")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git status --json", sess))
	var statusJSON gitStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.Equal(t, "ok", statusJSON.Status)
	require.True(t, statusJSON.Clean)
	require.Empty(t, statusJSON.Entries)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git --json status", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &statusJSON))
	require.Equal(t, "git_status", statusJSON.Kind)
	require.True(t, statusJSON.Clean)
	out.Reset()

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello slash\nchanged\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/diff", sess))
	require.Contains(t, out.String(), "+changed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/diff --json", sess))
	var diffJSON diffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &diffJSON))
	require.Equal(t, "diff", diffJSON.Kind)
	require.Equal(t, "ok", diffJSON.Status)
	require.False(t, diffJSON.Empty)
	require.Contains(t, diffJSON.Diff, "+changed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash push slash stash", sess))
	require.Contains(t, out.String(), "Saved working directory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash list", sess))
	require.Contains(t, out.String(), "slash stash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stash list --json", sess))
	var stashJSON stashReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, "ok", stashJSON.Status)
	require.Equal(t, 1, stashJSON.Count)
	require.Contains(t, stashJSON.Stashes[0].Subject, "slash stash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/git --json stash list", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &stashJSON))
	require.Equal(t, "stash", stashJSON.Kind)
	require.Equal(t, 1, stashJSON.Count)
}

func TestBranchCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello branch\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add branch notes")
	base, err := gitops.Branch(workspace)
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Branch([]string{"create", "feature/one", "--switch", "--json"}))
	require.Contains(t, out.String(), `"kind": "branch"`)
	require.Contains(t, out.String(), `"current": "feature/one"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch rename feature/two", sess))
	require.Contains(t, out.String(), "feature/two")
	out.Reset()

	require.NoError(t, app.Git([]string{"branch", "current"}))
	require.Contains(t, out.String(), "feature/two")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch switch "+base, sess))
	require.Contains(t, out.String(), base)
	out.Reset()

	require.NoError(t, app.Branch([]string{"delete", "feature/two", "--force"}))
	require.Contains(t, out.String(), "delete")
	require.Contains(t, out.String(), "Deleted branch")
	require.Empty(t, errOut.String())
}

func TestBranchFreshnessCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	base, err := gitops.Branch(workspace)
	require.NoError(t, err)
	runGit(t, workspace, "switch", "-c", "topic")
	runGit(t, workspace, "switch", base)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: main update")

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.Branch([]string{"freshness", "topic", base, "--json"}))
	require.Contains(t, out.String(), `"action": "freshness"`)
	require.Contains(t, out.String(), `"status": "stale"`)
	require.Contains(t, out.String(), `"behind": 1`)
	require.Contains(t, out.String(), `"verification_blocked": true`)
	require.Contains(t, out.String(), `"recovery_scenario": "stale_branch"`)
	require.Contains(t, out.String(), `"lane_event": "branch.stale_against_main"`)
	require.Contains(t, out.String(), `"suggested_commands": [`)
	require.Contains(t, out.String(), `"fix: main update"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/branch freshness topic "+base, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Freshness        stale")
	require.Contains(t, out.String(), "Behind           1")
	require.Contains(t, out.String(), "Verification     blocked until branch is updated")
	require.Contains(t, out.String(), "Recovery         stale_branch")
	require.Contains(t, out.String(), "Event            branch.stale_against_main")
	require.Contains(t, out.String(), "git merge --ff-only")
	require.Contains(t, out.String(), "fix: main update")
	require.Empty(t, errOut.String())
}

func TestTagCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello tag\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add tag notes")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Tag([]string{"create", "v0.1.0", "--message", "release v0.1.0", "--json"}))
	require.Contains(t, out.String(), `"kind": "tag"`)
	require.Contains(t, out.String(), `"name": "v0.1.0"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag list v0.*", sess))
	require.Contains(t, out.String(), "v0.1.0")
	out.Reset()

	require.NoError(t, app.Git([]string{"tag", "show", "v0.1.0"}))
	require.Contains(t, out.String(), "release v0.1.0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/tag delete v0.1.0", sess))
	require.Contains(t, out.String(), "Deleted tag")
	require.Empty(t, errOut.String())
}

func TestRuntimeConfigModelAndPermissionsSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			APIKey:         "api-key-secret",
			AuthToken:      "auth-token-secret",
			BaseURL:        "https://api.example.test",
			Model:          "model-a",
			MaxTokens:      1000,
			MaxTurns:       3,
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Allow: []string{"read_file"},
				Deny:  []string{"bash:rm"},
			},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/config auth", sess))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "api-key-secret")
	require.NotContains(t, out.String(), "auth-token-secret")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/model model-b", sess))
	require.Equal(t, "model-b", app.Config.Model)
	require.Contains(t, errOut.String(), "model=model-b")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/max-tokens 2048", sess))
	require.Equal(t, 2048, app.Config.MaxTokens)
	require.Contains(t, errOut.String(), "max_tokens=2048")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/max-turns 6", sess))
	require.Equal(t, 6, app.Config.MaxTurns)
	require.Contains(t, errOut.String(), "max_turns=6")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions", sess))
	require.Contains(t, out.String(), `"permission_mode": "workspace-write"`)
	require.Contains(t, out.String(), `"bash:rm"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions read-only", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, errOut.String(), "permission_mode=read-only")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/permissions invalid", sess))
	require.Equal(t, "read-only", app.Config.PermissionMode)
	require.Contains(t, errOut.String(), "unknown permission mode: invalid")
	errOut.Reset()

	require.NoError(t, app.Model([]string{"model-c"}))
	require.Equal(t, "model-c", app.Config.Model)
	require.Contains(t, out.String(), "model=model-c")
	out.Reset()

	require.NoError(t, app.MaxTokens([]string{"4096"}))
	require.Equal(t, 4096, app.Config.MaxTokens)
	require.Contains(t, out.String(), "max_tokens=4096")
	out.Reset()

	require.NoError(t, app.MaxTurns([]string{"8"}))
	require.Equal(t, 8, app.Config.MaxTurns)
	require.Contains(t, out.String(), "max_turns=8")
	out.Reset()

	require.NoError(t, app.Permissions([]string{"workspace-write"}))
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	require.Contains(t, out.String(), "permission_mode=workspace-write")
	out.Reset()

	require.NoError(t, app.AllowedTools([]string{"add", "grep"}))
	require.Contains(t, app.Config.PermissionRules.Allow, "grep")
	require.Contains(t, out.String(), "Allowed tools")
	out.Reset()

	require.NoError(t, app.AllowedTools([]string{"add", "Read", "Bash(go test:*)", "mcp__playwright__*"}))
	require.Contains(t, app.Config.PermissionRules.Allow, "Read")
	require.Contains(t, app.Config.PermissionRules.Allow, "Bash(go test:*)")
	require.Contains(t, app.Config.PermissionRules.Allow, "mcp__playwright__*")
	out.Reset()

	err := app.AllowedTools([]string{"add", "teleport"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_tool_name")
	require.NotContains(t, app.Config.PermissionRules.Allow, "teleport")
}

func TestAPIKeyCommandAndSlash(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CODOG_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.APIKey([]string{"status", "--json"}))
	var status apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "api_key", status.Kind)
	require.False(t, status.Configured)
	require.Empty(t, status.RedactedValue)
	out.Reset()

	require.NoError(t, app.APIKey([]string{"set", "sk-ant-test-secret", "--json"}))
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	var setReport apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &setReport))
	require.True(t, setReport.Configured)
	require.Equal(t, "config", setReport.Source)
	require.NotEmpty(t, setReport.RedactedValue)
	require.Equal(t, "sk-ant-test-secret", app.Config.APIKey)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "sk-ant-test-secret", persisted.APIKey)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/api-key clear --json", &session.Session{ID: "session"}))
	require.NotContains(t, out.String(), "sk-ant-test-secret")
	var clearReport apiKeyReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &clearReport))
	require.False(t, clearReport.Configured)
	require.Empty(t, app.Config.APIKey)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "sk-ant-test-secret")
	require.NotContains(t, string(data), "api_key")
	require.Empty(t, errOut.String())
}

func TestTemperatureCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.Temperature([]string{"--json"}))
	var status temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "temperature", status.Kind)
	require.False(t, status.Configured)
	require.Nil(t, status.Temperature)
	out.Reset()

	require.NoError(t, app.Temperature([]string{"0.7", "--json"}))
	var setReport temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &setReport))
	require.True(t, setReport.Configured)
	require.NotNil(t, setReport.Temperature)
	require.InDelta(t, 0.7, *setReport.Temperature, 0.0001)
	require.NotNil(t, app.Config.Temperature)
	require.InDelta(t, 0.7, *app.Config.Temperature, 0.0001)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.NotNil(t, persisted.Temperature)
	require.InDelta(t, 0.7, *persisted.Temperature, 0.0001)
	out.Reset()

	require.Error(t, app.Temperature([]string{"1.5"}))
	require.NotNil(t, app.Config.Temperature)
	require.InDelta(t, 0.7, *app.Config.Temperature, 0.0001)

	require.True(t, app.handleSlash(context.Background(), "/temperature clear --json", &session.Session{ID: "session"}))
	var clearReport temperatureReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &clearReport))
	require.False(t, clearReport.Configured)
	require.Nil(t, app.Config.Temperature)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "temperature")
	require.Empty(t, errOut.String())
}

func TestTelemetryCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	require.NoError(t, app.Telemetry([]string{"status", "--json"}))
	var status telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "telemetry", status.Kind)
	require.False(t, status.Enabled)
	require.False(t, status.Configured)
	out.Reset()

	require.NoError(t, app.Telemetry([]string{"on", "--json"}))
	var enabled telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &enabled))
	require.True(t, enabled.Enabled)
	require.True(t, enabled.Configured)
	require.NotNil(t, app.Config.Privacy.TelemetryEnabled)
	require.True(t, *app.Config.Privacy.TelemetryEnabled)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var persisted config.Config
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.NotNil(t, persisted.Privacy.TelemetryEnabled)
	require.True(t, *persisted.Privacy.TelemetryEnabled)
	out.Reset()

	require.NoError(t, app.Telemetry([]string{"toggle", "--json"}))
	var toggled telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &toggled))
	require.False(t, toggled.Enabled)
	require.True(t, toggled.Configured)
	require.NotNil(t, app.Config.Privacy.TelemetryEnabled)
	require.False(t, *app.Config.Privacy.TelemetryEnabled)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/telemetry clear --json", &session.Session{ID: "session"}))
	var cleared telemetryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cleared))
	require.False(t, cleared.Enabled)
	require.False(t, cleared.Configured)
	require.Nil(t, app.Config.Privacy.TelemetryEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "telemetry_enabled")
	require.Empty(t, errOut.String())
}

func TestAdvisorCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Model:      "claude-sonnet-main",
		},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Advisor([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "advisor"`)
	require.Contains(t, out.String(), `"main_model": "claude-sonnet-main"`)
	require.NotContains(t, out.String(), `"model":`)
	out.Reset()

	require.NoError(t, app.Advisor([]string{"claude-opus-advisor", "--json"}))
	require.Equal(t, "claude-opus-advisor", app.Config.AdvisorModel)
	require.Contains(t, out.String(), `"model": "claude-opus-advisor"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"advisor_model": "claude-opus-advisor"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/advisor off", &session.Session{ID: "session"}))
	require.Empty(t, app.Config.AdvisorModel)
	require.Contains(t, out.String(), "Advisor")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "advisor_model")
	require.Empty(t, errOut.String())
}

func TestSlashCompletionCandidatesIncludeRuntimeContext(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "team", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill"), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.AppendInput("source", "hello"))
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-test"},
		Sessions:  store,
		Workspace: workspace,
	}

	candidates := app.slashCompletionCandidates("active-session")
	require.Contains(t, candidates, "/model claude-test")
	require.Contains(t, candidates, "/resume active-session")
	require.Contains(t, candidates, "/session switch active-session")
	require.Contains(t, candidates, "/resume source")
	require.Contains(t, candidates, "/session switch source")
	require.Contains(t, candidates, "/permissions workspace-write")
	require.Contains(t, candidates, "/team/review ")
	require.Contains(t, candidates, "/team/audit ")
}

func TestSlashCompleterReturnsReadlineSuffixes(t *testing.T) {
	completer := slashCompleter{candidates: []string{"/model claude-test", "/resume latest"}}

	suffixes, length := completer.Do([]rune("/model "), len([]rune("/model ")))
	require.Equal(t, len([]rune("/model ")), length)
	require.Equal(t, [][]rune{[]rune("claude-test")}, suffixes)

	suffixes, length = completer.Do([]rune("model"), len([]rune("model")))
	require.Zero(t, length)
	require.Empty(t, suffixes)
}

func TestRenderConfigInspectionSections(t *testing.T) {
	cfg := redactedConfig(config.Config{
		APIKey:         "secret",
		AuthToken:      "token",
		BaseURL:        "https://api.example.test",
		Model:          "model-a",
		MaxTokens:      100,
		MaxTurns:       3,
		PermissionMode: "workspace-write",
	})
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"get", "auth"}))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"--output-format", "json", "get", "auth"}))
	require.Contains(t, out.String(), `"base_url": "https://api.example.test"`)
	require.NotContains(t, out.String(), "secret")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, []string{"user.json", "project.json"}, []string{"paths"}))
	require.Contains(t, out.String(), "project.json")
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"model"}))
	require.Contains(t, out.String(), `"model": "model-a"`)
	out.Reset()

	require.NoError(t, renderConfigInspection(&out, cfg, nil, []string{"model", "--output-format", "text"}))
	require.Contains(t, out.String(), "Config")
	require.Contains(t, out.String(), "model-a")
}

func TestSettingsAliasRunsConfigInspection(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-test"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "settings", "paths", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.NotEmpty(t, report["paths"])
}

func TestRenderConfigInspectionMutatesConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	var out bytes.Buffer

	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "model", "model-b"}))
	require.Contains(t, out.String(), `"action": "set"`)
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"set", "rate_limit.max_retries", "4"}))
	out.Reset()
	require.NoError(t, renderConfigInspection(&out, config.Config{}, []string{configPath}, []string{"unset", "model"}))
	require.Contains(t, out.String(), `"action": "unset"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"model"`)
	require.Contains(t, string(data), `"max_retries": 4`)
}

func TestAllowedToolsSlashMutatesRuntimeAllowRules(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionRules: config.PermissionRules{Allow: []string{"read_file"}},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools", sess))
	require.Contains(t, out.String(), "read_file")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools add bash grep bash", sess))
	require.ElementsMatch(t, []string{"read_file", "bash", "grep"}, app.Config.PermissionRules.Allow)
	require.Contains(t, out.String(), "bash")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools remove read_file", sess))
	require.ElementsMatch(t, []string{"bash", "grep"}, app.Config.PermissionRules.Allow)
	require.NotContains(t, out.String(), "read_file")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools clear", sess))
	require.Empty(t, app.Config.PermissionRules.Allow)
	require.Contains(t, out.String(), "no allow rules configured")
	require.Empty(t, errOut.String())
}

func TestAllowedToolsSlashRejectsUnknownToolRules(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionRules: config.PermissionRules{Allow: []string{"read_file"}},
		},
		Out: &out,
		Err: &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.True(t, app.handleSlash(context.Background(), "/allowed-tools add teleport", sess))
	require.ElementsMatch(t, []string{"read_file"}, app.Config.PermissionRules.Allow)
	require.Empty(t, out.String())
	require.Contains(t, errOut.String(), "invalid_tool_name")
	require.Contains(t, errOut.String(), "teleport")
}

func TestPlanCommandAndSlashEnforceReadOnlyPlanningMode(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			PermissionMode: "workspace-write",
			PermissionRules: config.PermissionRules{
				Allow: []string{"write_file"},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Plan([]string{"inspect", "then", "edit"}))
	require.Contains(t, out.String(), "Status           active")
	require.Contains(t, out.String(), "inspect then edit")
	require.Equal(t, "workspace-write", app.Config.PermissionMode)
	effective := app.effectiveConfig()
	require.Equal(t, "read-only", effective.PermissionMode)
	require.Empty(t, effective.PermissionRules.Allow)
	require.Contains(t, app.systemPrompt(), "<codog_plan_mode")
	require.Contains(t, app.systemPrompt(), "inspect then edit")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/exit-plan", sess))
	require.Contains(t, out.String(), "Status           inactive")
	require.Empty(t, errOut.String())
	require.Equal(t, "workspace-write", app.effectiveConfig().PermissionMode)
	require.NotContains(t, app.systemPrompt(), "<codog_plan_mode")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ultraplan inspect the release", sess))
	require.Contains(t, out.String(), "Status           active")
	require.Contains(t, out.String(), "inspect the release")
}

func TestDoctorCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Prefer focused changes."), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Doctor(nil))
	require.Contains(t, out.String(), "Doctor")
	require.Contains(t, out.String(), "Auth")
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "Permissions")
	out.Reset()

	require.NoError(t, app.Doctor([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "doctor"`)
	require.Contains(t, out.String(), `"name": "Auth"`)
	var doctorReport doctor.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &doctorReport))
	var sandboxCheck doctor.Check
	for _, check := range doctorReport.Checks {
		if check.Name == "Sandbox" {
			sandboxCheck = check
			break
		}
	}
	require.Equal(t, "Sandbox", sandboxCheck.Name)
	require.Contains(t, sandboxCheck.Data, "enabled")
	require.Contains(t, sandboxCheck.Data, "filesystem_mode")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/doctor", sess))
	require.Contains(t, out.String(), "Doctor")
	require.NotContains(t, errOut.String(), "unknown slash command")
}

func TestDoctorReportsConfigValidationChecks(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
			MCPServers:     map[string]config.MCPServerConfig{"missing": {}},
			Hooks:          config.HookConfig{PreToolUseCommands: []config.HookCommand{{Type: "http"}}},
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Doctor([]string{"--json"}))
	var report doctor.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	mcpValidation := doctor.Check{}
	hookValidation := doctor.Check{}
	for _, check := range report.Checks {
		switch check.Name {
		case "MCP validation":
			mcpValidation = check
		case "Hook validation":
			hookValidation = check
		}
	}
	require.Equal(t, doctor.StatusWarn, mcpValidation.Status)
	require.Equal(t, float64(1), mcpValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(mcpValidation.Details, "\n"), "missing command")
	require.Equal(t, doctor.StatusWarn, hookValidation.Status)
	require.Equal(t, float64(1), hookValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(hookValidation.Details, "\n"), "pre_tool_use")
}

func TestDoctorDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "doctor"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var report doctor.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "doctor", report.Kind)
	require.Equal(t, doctor.StatusFail, report.Status)
	require.True(t, report.HasFailures)
	configCheck := doctor.Check{}
	for _, check := range report.Checks {
		if check.Name == "Config" {
			configCheck = check
			break
		}
	}
	require.Equal(t, "Config", configCheck.Name)
	require.Equal(t, doctor.StatusFail, configCheck.Status)
	require.Contains(t, configCheck.Summary, "failed to load")
	loadError, ok := configCheck.Data["load_error"].(string)
	require.True(t, ok)
	require.Contains(t, loadError, "broken.json")
	require.Equal(t, "config_load_failed", configCheck.Data["load_error_kind"])

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/doctor"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, doctor.StatusFail, report.Status)
}

func TestStatusCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Status memory."), 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "status me")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "claude-test",
			BaseURL:             "https://api.example.test",
			APIKey:              "secret",
			PermissionMode:      "workspace-write",
			MaxTokens:           1000,
			MaxTurns:            4,
			AutoCompactMessages: 20,
		},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Status(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Status")
	require.Contains(t, out.String(), "Model            claude-test")
	require.Contains(t, out.String(), "Memory files     1")
	require.Contains(t, out.String(), "Task lanes       active=0 blocked=0 finished=0")
	require.Contains(t, out.String(), "Tools            82")
	out.Reset()

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{Resume: "source"}))
	require.Contains(t, out.String(), `"kind": "status"`)
	require.Contains(t, out.String(), `"memory_file_count": 1`)
	require.Contains(t, out.String(), `"id": "source"`)
	require.Contains(t, out.String(), `"message_count": 1`)
	require.Contains(t, out.String(), `"lane_board": {`)
	require.Contains(t, out.String(), `"status_json_supported": true`)
	require.Contains(t, out.String(), `"transport_dead"`)
	require.Contains(t, out.String(), `"active_count": 0`)
	out.Reset()

	sess := &session.Session{ID: "source", Messages: []anthropic.Message{anthropic.TextMessage("user", "slash")}}
	require.True(t, app.handleSlash(context.Background(), "/status", sess))
	require.Contains(t, out.String(), "Session          source (1 messages)")
	out.Reset()

	require.NoError(t, app.Statusline([]string{"--json"}, config.FlagOverrides{Resume: "source"}))
	require.Contains(t, out.String(), `"kind": "statusline"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	require.Contains(t, out.String(), `"model": "claude-test"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/statusline", sess))
	require.Contains(t, out.String(), "codog")
	require.Contains(t, out.String(), "claude-test")
	require.Contains(t, out.String(), "session=source(1)")
}

func TestStatusValidationReportsDegradedConfig(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Model:      "claude-test",
			MCPServers: map[string]config.MCPServerConfig{
				"missing": {},
				"ok":      {Command: "codog-test-mcp"},
			},
			Hooks: config.HookConfig{
				PreToolUseCommands: []config.HookCommand{
					{Type: "command", Command: "echo ok"},
					{Type: "command"},
					{Type: "http"},
					{Type: "webhook", Command: "echo no"},
					{Type: "prompt", Prompt: "summarize payload"},
					{Type: "agent", Prompt: "inspect payload"},
				},
				SessionStart: []string{"echo session"},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{}))
	var snapshot localstatus.Snapshot
	require.NoError(t, json.Unmarshal(out.Bytes(), &snapshot))
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, 2, snapshot.MCPValidation.TotalConfigured)
	require.Equal(t, 1, snapshot.MCPValidation.ValidCount)
	require.Equal(t, 1, snapshot.MCPValidation.InvalidCount)
	require.Len(t, snapshot.MCPValidation.InvalidServers, 1)
	require.Equal(t, "missing", snapshot.MCPValidation.InvalidServers[0].Name)
	require.Equal(t, "missing_command", snapshot.MCPValidation.InvalidServers[0].Kind)
	require.Equal(t, "command", snapshot.MCPValidation.InvalidServers[0].ErrorField)
	require.Equal(t, 4, snapshot.HookValidation.ValidCount)
	require.Equal(t, 3, snapshot.HookValidation.InvalidCount)
	require.Len(t, snapshot.HookValidation.InvalidHooks, 3)
	kinds := map[string]string{}
	for _, issue := range snapshot.HookValidation.InvalidHooks {
		require.Equal(t, "pre_tool_use", issue.Event)
		require.NotNil(t, issue.Index)
		require.NotNil(t, issue.HookIndex)
		kinds[issue.Kind] = issue.ErrorField
	}
	require.Equal(t, "command", kinds["missing_command"])
	require.Equal(t, "url", kinds["missing_url"])
	require.Equal(t, "type", kinds["unsupported_type"])
	out.Reset()

	require.NoError(t, app.Status(nil, config.FlagOverrides{}))
	require.Contains(t, out.String(), "MCP validation   valid=1 invalid=1")
	require.Contains(t, out.String(), "Hook validation  valid=4 invalid=3")
}

func TestStatusDegradesOnMalformedConfigFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var snapshot localstatus.Snapshot
	require.NoError(t, json.Unmarshal([]byte(out), &snapshot))
	require.Equal(t, "status", snapshot.Kind)
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "config_load_failed", snapshot.ConfigLoadErrorKind)
	require.Contains(t, snapshot.ConfigLoadError, "broken.json")
	require.Contains(t, snapshot.ConfigLoadError, "unexpected end of JSON input")
	require.NotEmpty(t, snapshot.Workspace.Path)
	require.NotEmpty(t, snapshot.Config.ConfigHome)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &snapshot))
	require.Equal(t, "degraded", snapshot.Status)
	require.Equal(t, "config_load_failed", snapshot.ConfigLoadErrorKind)
}

func TestStatusIncludesBranchFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	runGit(t, workspace, "switch", "-c", "topic")
	runGit(t, workspace, "switch", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: main update")
	runGit(t, workspace, "switch", "topic")

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{Model: "claude-test", BaseURL: "https://api.example.test"},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.Status([]string{"--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"status": "warn"`)
	require.Contains(t, out.String(), `"freshness": {`)
	require.Contains(t, out.String(), `"status": "stale"`)
	require.Contains(t, out.String(), `"behind": 1`)
	require.Contains(t, out.String(), `"fix: main update"`)
}

func TestHistoryCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.AppendInput("source", "first prompt"))
	require.NoError(t, store.AppendInput("source", "second prompt"))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.History([]string{"--session", "source", "--limit", "1"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Prompt history")
	require.Contains(t, out.String(), "Showing          1 most recent")
	require.Contains(t, out.String(), "second prompt")
	require.NotContains(t, out.String(), "first prompt")
	out.Reset()

	require.NoError(t, app.History([]string{"--session=source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "prompt_history"`)
	require.Contains(t, out.String(), `"total": 2`)
	require.Contains(t, out.String(), `"text": "first prompt"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/history 1", &session.Session{ID: "source"}))
	require.Contains(t, out.String(), "second prompt")
	require.Empty(t, errOut.String())
}

func TestSearchCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n// TODO: search me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("TODO: docs\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Search(context.Background(), []string{"todo", "--ignore-case", "--glob", "*.go", "--limit", "1"}))
	require.Contains(t, out.String(), "Search")
	require.Contains(t, out.String(), "Matches          1")
	require.Contains(t, out.String(), "main.go:2:// TODO: search me")
	require.NotContains(t, out.String(), "README.md")
	out.Reset()

	require.NoError(t, app.Search(context.Background(), []string{"TODO", "--json"}))
	require.Contains(t, out.String(), `"kind": "search"`)
	require.Contains(t, out.String(), `"total": 2`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/search TODO --glob=*.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "README.md:1:TODO: docs")
	require.Empty(t, errOut.String())
}

func TestFilesCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".hidden", "secret.go"), []byte("package hidden\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Files([]string{"--glob", "*.go", "--json"}))
	require.Contains(t, out.String(), `"kind": "files"`)
	require.Contains(t, out.String(), `"path": "pkg/main.go"`)
	require.NotContains(t, out.String(), "secret.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/files --glob=*.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Files")
	require.Contains(t, out.String(), "README.md")
	require.Empty(t, errOut.String())
}

func TestRunAndProjectCommandSurfaces(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/cmdsurf\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "add.go"), []byte("package cmdsurf\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "add_test.go"), []byte("package cmdsurf\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"bad add\") } }\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.RunCommand(context.Background(), []string{"--json", "go", "version"}))
	require.Contains(t, out.String(), `"kind": "run"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	nodeCommand, err := languageCommand(workspace, "node", []string{"console.log(1)"})
	require.NoError(t, err)
	require.Equal(t, []string{"node", "-e", "console.log(1)"}, nodeCommand)
	scriptPath := filepath.Join(workspace, "script.py")
	require.NoError(t, os.WriteFile(scriptPath, []byte("print(1)\n"), 0o644))
	pythonCommand, err := languageCommand(workspace, "python", []string{"script.py", "arg"})
	require.NoError(t, err)
	require.Len(t, pythonCommand, 3)
	require.Equal(t, scriptPath, pythonCommand[1])
	require.Equal(t, "arg", pythonCommand[2])

	require.NoError(t, app.ProjectCommand(context.Background(), "test", nil))
	require.Contains(t, out.String(), "Command")
	require.Contains(t, out.String(), "go test ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/build", sess))
	require.Contains(t, out.String(), "go build ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/lint", sess))
	require.Contains(t, out.String(), "go vet ./...")
	require.Contains(t, out.String(), "Exit code        0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/run go version", sess))
	require.Contains(t, out.String(), "go version")
	require.Empty(t, errOut.String())
}

func TestCodeIntelligenceCommandsAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/intel\n\ngo 1.22\n"), 0o644))
	source := "package intel\n\ntype Runner struct{}\n\nfunc Run() Runner { return Runner{} }\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "runner.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package intel\n\nfunc messy(){return}\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Symbols(nil))
	require.Contains(t, out.String(), "runner.go:3:type Runner")
	require.Contains(t, out.String(), "runner.go:5:function Run")
	out.Reset()

	require.NoError(t, app.Definition([]string{"Run"}))
	require.Contains(t, out.String(), "Location         runner.go:5")
	out.Reset()

	require.NoError(t, app.References([]string{"Runner", "--limit", "2"}))
	require.Contains(t, out.String(), "References")
	require.Contains(t, out.String(), "runner.go:3:type Runner")
	out.Reset()

	require.NoError(t, app.Hover([]string{"Run", "--context", "1"}))
	require.Contains(t, out.String(), "Hover")
	require.Contains(t, out.String(), "func Run()")
	out.Reset()

	require.NoError(t, app.Teleport([]string{"Run"}))
	require.Contains(t, out.String(), "Teleport")
	require.Contains(t, out.String(), "Location         runner.go:5")
	require.Contains(t, out.String(), "func Run()")
	out.Reset()

	require.NoError(t, app.Teleport([]string{"Runner", "--json"}))
	require.Contains(t, out.String(), `"kind": "teleport"`)
	require.Contains(t, out.String(), `"mode": "symbol"`)
	require.Contains(t, out.String(), `"found": true`)
	out.Reset()

	require.NoError(t, app.Completion([]string{"Run", "--limit", "5"}))
	require.Contains(t, out.String(), "Completion")
	require.Contains(t, out.String(), "runner.go:5:function Run")
	out.Reset()

	require.NoError(t, app.Format([]string{"messy.go"}))
	require.Contains(t, out.String(), "Format")
	require.Contains(t, out.String(), "Changed          true")
	require.Contains(t, out.String(), "func messy()")
	data, err := os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func messy(){return}")
	out.Reset()

	require.NoError(t, app.Map([]string{"--depth", "1"}))
	require.Contains(t, out.String(), "Map")
	require.Contains(t, out.String(), "file\tgo.mod")
	out.Reset()

	require.NoError(t, app.Diagnostics(context.Background(), []string{"./..."}))
	require.Contains(t, out.String(), "Diagnostics")
	require.Contains(t, out.String(), "Total            0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/definition Runner", sess))
	require.Contains(t, out.String(), "Location         runner.go:3")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/references Run --limit=1", sess))
	require.Contains(t, out.String(), "References")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/symbols", sess))
	require.Contains(t, out.String(), "runner.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel symbols --json", sess))
	require.Contains(t, out.String(), `"kind": "symbols"`)
	require.Contains(t, out.String(), `"runner.go"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/teleport runner.go", sess))
	require.Contains(t, out.String(), "Mode             file")
	require.Contains(t, out.String(), "package intel")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/completion Run --limit=5", sess))
	require.Contains(t, out.String(), "Completion")
	require.Contains(t, out.String(), "Run")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/format messy.go --write", sess))
	require.Contains(t, out.String(), "Written          true")
	data, err = os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func messy()")
	require.Empty(t, errOut.String())
	out.Reset()

	notebook := `{
  "cells": [
    {"cell_type":"code","id":"cell-a","metadata":{},"source":["print(1)\n"],"outputs":[],"execution_count":null}
  ],
  "metadata": {"kernelspec":{"language":"python"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(notebook), 0o644))
	require.NoError(t, app.CodeIntel([]string{"notebook-read", "analysis.ipynb", "--include-outputs", "--json"}))
	require.Contains(t, out.String(), `"kind": "notebook_read"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	require.Contains(t, out.String(), `"language": "python"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/code-intel notebook-read analysis.ipynb --cell-index 0 --json", sess))
	require.Contains(t, out.String(), `"kind": "notebook_read"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/notebook-read analysis.ipynb --cell-index 0", sess))
	require.Contains(t, out.String(), "Notebook Read")
	require.Contains(t, out.String(), "Cell 0")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "--cell-id", "cell-a", "--source", "print(2)\n", "--json"}))
	require.Contains(t, out.String(), `"kind": "notebook_edit"`)
	require.Contains(t, out.String(), `"cell_id": "cell-a"`)
	require.Contains(t, out.String(), `"language": "python"`)
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "print(2)")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "--mode", "insert", "--cell-id", "cell-a", "--cell-type", "markdown", "--source", "# Notes"}))
	require.Contains(t, out.String(), "Notebook Edit")
	require.Contains(t, out.String(), "Index            1")
	require.Contains(t, out.String(), "Cell type        markdown")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/notebook-edit analysis.ipynb --mode insert --cell-id 1 --cell-type markdown --source=SlashNotes", sess))
	require.Contains(t, out.String(), "Notebook Edit")
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "SlashNotes")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-read", "analysis.ipynb", "--cell-index", "1", "--limit", "1"}))
	require.Contains(t, out.String(), "Notebook Read")
	require.Contains(t, out.String(), "Cell 1")
	require.Contains(t, out.String(), "# Notes")
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"notebook-edit", "analysis.ipynb", "0", "markdown", "Legacy title"}))
	require.Contains(t, out.String(), "Notebook Edit")
	data, err = os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Legacy title")
	require.NotContains(t, string(data), `"outputs": []`)

	cliConfigPath := filepath.Join(t.TempDir(), "config.json")
	cliConfig, err := json.Marshal(map[string]any{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cliConfigPath, cliConfig, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	defer func() { require.NoError(t, os.Chdir(oldWD)) }()
	cliOut, cliErr := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", cliConfigPath, "--output-format", "json", "/notebook-read", "analysis.ipynb", "--cell-index", "0"}, config.FlagOverrides{})
	})
	require.NoError(t, cliErr)
	require.Contains(t, cliOut, `"kind": "notebook_read"`)
	require.Contains(t, cliOut, `"Legacy title"`)

	_, err = parseCodeIntelNotebookEditArgs([]string{"analysis.ipynb", "--mode", "insert"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "new_source is required")

	_, err = parseCodeIntelNotebookReadArgs([]string{"analysis.ipynb", "--limit", "0"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive integer")
}

func TestMemoryCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Memory first line\nsecret body"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Memory(nil))
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "Instruction files 1")
	require.Contains(t, out.String(), "preview=Memory first line")
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "memory"`)
	require.Contains(t, out.String(), `"instruction_files": 1`)
	require.Contains(t, out.String(), `"preview": "Memory first line"`)
	require.NotContains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"show", "AGENTS.md"}))
	require.Contains(t, out.String(), "Memory File")
	require.Contains(t, out.String(), "secret body")
	out.Reset()

	require.NoError(t, app.Memory([]string{"add", "Use", "focused", "tests."}))
	require.Contains(t, out.String(), "Memory Updated")
	data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Use focused tests.")
	out.Reset()

	require.NoError(t, app.Memory([]string{"search", "focused", "--limit", "1", "--json"}))
	require.Contains(t, out.String(), `"action": "search"`)
	require.Contains(t, out.String(), `"match_count": 1`)
	require.Contains(t, out.String(), `"line": "Use focused tests."`)
	out.Reset()

	require.NoError(t, app.Memory([]string{"relevant", "focused", "--limit=1"}))
	require.Contains(t, out.String(), "Memory Search")
	require.Contains(t, out.String(), "Use focused tests.")
	out.Reset()

	require.NoError(t, app.Memory([]string{"path", "AGENTS.md", "--json"}))
	require.Contains(t, out.String(), `"action": "path"`)
	require.Contains(t, out.String(), "AGENTS.md")
	out.Reset()

	require.NoError(t, app.Memory([]string{"ensure", ".codog/instructions.md"}))
	require.Contains(t, out.String(), "Memory File")
	_, err = os.Stat(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	out.Reset()

	require.NoError(t, app.Memory([]string{"edit", ".codog/instructions.md", "--no-open", "--json"}))
	require.Contains(t, out.String(), `"action": "edit"`)
	require.Contains(t, out.String(), `"Editor launch skipped."`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory show AGENTS.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory search focused", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory Search")
	require.Contains(t, out.String(), "Use focused tests.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/memory edit --no-open", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Memory File")
}

func TestFocusCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("focus body\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Focus([]string{"notes.md"}))
	require.Contains(t, out.String(), "Focus")
	require.Contains(t, out.String(), "notes.md")
	require.FileExists(t, focus.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<focused_context>")
	require.Contains(t, app.systemPrompt(), "focus body")
	out.Reset()

	require.NoError(t, app.Focus([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "focus"`)
	require.Contains(t, out.String(), `"path": "notes.md"`)
	out.Reset()

	require.NoError(t, app.Focus([]string{"pkg"}))
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus notes.md", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Focus")
	require.NotContains(t, app.systemPrompt(), "focus body")
	require.Contains(t, app.systemPrompt(), "- pkg/a.go")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/unfocus --all", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Entries          0")
	require.NotContains(t, app.systemPrompt(), "<focused_context>")
	require.Empty(t, errOut.String())
}

func TestAddDirCommandAndSlashUpdatesToolScope(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	extraFile := filepath.Join(extra, "notes.txt")
	require.NoError(t, os.WriteFile(extraFile, []byte("extra body\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AddDir([]string{extra, "--json"}))
	require.Contains(t, out.String(), `"kind": "additional_dirs"`)
	require.Contains(t, out.String(), extra)
	require.FileExists(t, pathscope.Path(workspace))
	require.Contains(t, app.systemPrompt(), "<additional_directories>")
	out.Reset()

	require.NoError(t, app.Validation([]string{"add-dir", extra, filepath.Join(workspace, "missing"), "--json"}))
	var validationReport pathscope.ValidationReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &validationReport))
	require.Equal(t, "validation", validationReport.Kind)
	require.Equal(t, "error", validationReport.Status)
	require.Equal(t, 2, validationReport.Total)
	require.Equal(t, 1, validationReport.ValidCount)
	require.Equal(t, 1, validationReport.InvalidCount)
	require.True(t, validationReport.Entries[0].AlreadyAllowed)
	out.Reset()

	input, _ := json.Marshal(map[string]string{"path": extraFile})
	toolOut, err := app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.NoError(t, err)
	require.Contains(t, toolOut, "extra body")

	require.True(t, app.handleSlash(context.Background(), "/add-dir remove "+extra, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Additional Directories")
	require.NotContains(t, app.systemPrompt(), "<additional_directories>")
	_, err = app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/validation add-dir "+extra, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Add-dir Validation")
	require.Contains(t, out.String(), "Valid            1")
	require.Empty(t, errOut.String())

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": app.Config.ConfigHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "validation", "add-dir", extra}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &validationReport))
	require.Equal(t, "validation", validationReport.Kind)
	require.Equal(t, "ok", validationReport.Status)
}

func TestWorkspaceCommandAndSlashSwitchesRuntimeWorkspace(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	next := t.TempDir()
	var err error
	workspace, err = filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	next, err = filepath.EvalSymlinks(next)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(next, "next.txt"), []byte("next body\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.WorkspaceCommand([]string{"--json"}))
	var report workspaceReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, workspace, report.Workspace)
	require.False(t, report.Changed)
	require.Equal(t, session.NewWorkspaceStore(configHome, workspace).Dir, report.SessionDir)
	out.Reset()

	require.NoError(t, app.WorkspaceCommand([]string{next, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, next, app.Workspace)
	require.Equal(t, next, report.Workspace)
	require.True(t, report.Changed)
	require.Equal(t, workspace, report.PreviousWorkspace)
	require.Equal(t, session.NewWorkspaceStore(configHome, next).Dir, app.Sessions.Dir)

	input, _ := json.Marshal(map[string]string{"path": "next.txt"})
	toolOut, err := app.Tools.Execute(context.Background(), "read_file", input, nil)
	require.NoError(t, err)
	require.Contains(t, toolOut, "next body")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/cwd "+workspace, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Workspace")
	require.Equal(t, workspace, app.Workspace)
	require.Empty(t, errOut.String())
}

func TestContextCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use focused tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("context body\n"), 0o644))
	_, err := focus.Add(workspace, []string{"notes.md"})
	require.NoError(t, err)
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("user", "hello")))
	require.NoError(t, store.Append("context-session", anthropic.TextMessage("assistant", "done")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			MaxTokens:      4096,
			MaxTurns:       8,
			APIKey:         "test-key",
		},
		Sessions:  store,
		Tools:     tools.NewRegistry(workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Context([]string{"--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "context"`)
	require.Contains(t, out.String(), `"focused_paths": 1`)
	require.Contains(t, out.String(), `"message_count": 2`)
	require.Contains(t, out.String(), `"total_tokens":`)
	out.Reset()

	require.NoError(t, app.Context([]string{"--json"}, config.FlagOverrides{SessionID: "context-session"}))
	var contextReport contextview.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &contextReport))
	require.Equal(t, "context", contextReport.Kind)
	require.Equal(t, "context-session", contextReport.Session.ID)
	require.Equal(t, 2, contextReport.Session.MessageCount)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-test"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--session", "context-session", "--output-format", "json", "context-noninteractive"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &contextReport))
	require.Equal(t, "context", contextReport.Kind)
	require.Equal(t, "context-session", contextReport.Session.ID)

	sess, err := store.Open("context-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/context", sess))
	require.Contains(t, out.String(), "Context")
	require.Contains(t, out.String(), "Session          context-session (2 messages)")
	require.Contains(t, out.String(), "Focused paths    1")
	require.Empty(t, errOut.String())
	out.Reset()

	vizPath := filepath.Join(workspace, "context.html")
	require.NoError(t, app.ContextViz([]string{"--output", vizPath, "--json"}, config.FlagOverrides{SessionID: "context-session"}))
	require.Contains(t, out.String(), `"kind": "ctx_viz"`)
	require.Contains(t, out.String(), `"bytes":`)
	data, err := os.ReadFile(vizPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "Codog Context")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ctx_viz --output "+filepath.Join(workspace, "slash-context.html"), sess))
	require.Contains(t, out.String(), "Context Viz")
	require.FileExists(t, filepath.Join(workspace, "slash-context.html"))
	require.Empty(t, errOut.String())
}

func TestUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("usage-session", anthropic.TextMessage("user", "hello usage")))
	providerUsage := anthropic.Usage{InputTokens: 50, OutputTokens: 11, CacheCreationInputTokens: 6, CacheReadInputTokens: 4}
	require.NoError(t, store.AppendWithUsage("usage-session", anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		}},
	}, &providerUsage))
	require.NoError(t, store.Append("usage-session", anthropic.ToolResultMessage("tool-1", "ok", false)))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome, Model: "claude-haiku"},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Usage([]string{"--json"}, config.FlagOverrides{SessionID: "usage-session"}))
	require.Contains(t, out.String(), `"kind": "usage"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"tool_results": 1`)
	require.Contains(t, out.String(), `"source": "actual"`)
	require.Contains(t, out.String(), `"input_tokens": 50`)
	require.Contains(t, out.String(), `"cache_creation_input_tokens": 6`)
	require.Contains(t, out.String(), `"cache_read_input_tokens": 4`)
	out.Reset()

	require.NoError(t, app.Cache([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	var cache cacheReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cache))
	require.Equal(t, "cache", cache.Kind)
	require.Equal(t, "usage-session", cache.SessionID)
	require.Equal(t, 1, cache.UsageRecords)
	require.Equal(t, 6, cache.CacheCreationInputTokens)
	require.Equal(t, 4, cache.CacheReadInputTokens)
	require.Equal(t, 10, cache.CacheTotalInputTokens)
	require.Equal(t, 0.0667, cache.CacheHitRatio)
	require.Equal(t, "actual", cache.Source)
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome, "model": "claude-haiku"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--session", "usage-session", "--output-format", "json", "caches"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cache))
	require.Equal(t, "cache", cache.Kind)
	require.Equal(t, "usage-session", cache.SessionID)
	require.Equal(t, 10, cache.CacheTotalInputTokens)

	require.NoError(t, app.BreakCache([]string{"--session", "usage-session", "--message", "force a new provider prompt prefix", "--json"}, config.FlagOverrides{}))
	var breakReport breakCacheReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &breakReport))
	require.Equal(t, "break_cache", breakReport.Kind)
	require.Equal(t, "append_marker", breakReport.Action)
	require.Equal(t, "ok", breakReport.Status)
	require.Equal(t, "usage-session", breakReport.SessionID)
	require.False(t, breakReport.CreatedSession)
	require.NotEmpty(t, breakReport.Nonce)
	require.Contains(t, breakReport.Marker, "force a new provider prompt prefix")
	require.Contains(t, breakReport.Marker, breakReport.Nonce)
	breakSession, err := store.Open("usage-session")
	require.NoError(t, err)
	require.Len(t, breakSession.Messages, 4)
	require.Equal(t, "user", breakSession.Messages[3].Role)
	require.Contains(t, breakSession.Messages[3].Content[0].Text, breakReport.Nonce)
	out.Reset()

	require.NoError(t, app.Metrics([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	var metrics metricsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &metrics))
	require.Equal(t, "metrics", metrics.Kind)
	require.Equal(t, "show", metrics.Action)
	require.NotNil(t, metrics.Session)
	require.Equal(t, "usage-session", metrics.Session.ID)
	require.Equal(t, 1, metrics.Session.UsageRecords)
	require.Equal(t, 71, metrics.Session.TotalTokens)
	require.Equal(t, "actual", metrics.Session.TokenSource)
	require.Equal(t, 1, metrics.Session.ToolUses)
	require.Equal(t, 1, metrics.Session.ToolResults)
	require.Equal(t, 0.0667, metrics.Session.CacheHitRatio)
	require.Equal(t, 1, metrics.WorkspaceMetrics.SessionCount)
	require.Equal(t, 71, metrics.WorkspaceMetrics.TotalTokens)
	require.Equal(t, 1, metrics.WorkspaceMetrics.UsageRecords)
	require.NotEmpty(t, metrics.TopTools)
	require.Equal(t, "read_file", metrics.TopTools[0].Name)
	require.True(t, commandAcceptsGlobalOutputFormat("metrics"))
	out.Reset()

	require.NoError(t, app.PerfIssue([]string{"--token-threshold", "40", "--tool-threshold", "1", "--json"}))
	var perf perfissue.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &perf))
	require.Equal(t, "perf_issue", perf.Kind)
	require.Equal(t, "warn", perf.Status)
	require.Equal(t, 71, perf.TotalTokens)
	require.Contains(t, perfSignalKinds(perf.Signals), "high_token_usage")
	require.Contains(t, perfSignalKinds(perf.Signals), "high_tool_use")
	require.True(t, commandAcceptsGlobalOutputFormat("perf-issue"))
	out.Reset()

	require.NoError(t, app.PerfIssue([]string{"--write", "--token-threshold=40", "--tool-threshold=1"}))
	require.Contains(t, out.String(), "Performance Issue")
	require.Contains(t, out.String(), "File")
	perfFiles, err := filepath.Glob(filepath.Join(workspace, ".codog", "perf", "*.md"))
	require.NoError(t, err)
	require.Len(t, perfFiles, 1)
	perfData, err := os.ReadFile(perfFiles[0])
	require.NoError(t, err)
	require.Contains(t, string(perfData), "# Codog Performance Issue")
	require.Contains(t, string(perfData), "high_token_usage")
	out.Reset()

	require.NoError(t, app.Summary([]string{"--session", "usage-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "summary"`)
	require.Contains(t, out.String(), `"session_id": "usage-session"`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"first_user":`)
	require.Contains(t, out.String(), `"hello usage"`)
	out.Reset()

	sess, err := store.Open("usage-session")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/usage", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Contains(t, out.String(), "Token source     actual")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stats", sess))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          usage-session")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/cache", sess))
	require.Contains(t, out.String(), "Prompt Cache")
	require.Contains(t, out.String(), "Cache created    6")
	require.Contains(t, out.String(), "Cache read       4")
	require.Contains(t, out.String(), "Hit ratio        6.67%")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/caches", sess))
	require.Contains(t, out.String(), "Prompt Cache")
	require.Contains(t, out.String(), "Cache created    6")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/break-cache slash marker", sess))
	require.Contains(t, out.String(), "Break Cache")
	require.Len(t, sess.Messages, 5)
	require.Contains(t, sess.Messages[4].Content[0].Text, "slash marker")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/metrics --limit 1", sess))
	require.Contains(t, out.String(), "Metrics")
	require.Contains(t, out.String(), "Current session")
	require.Contains(t, out.String(), "ID               usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/perf-issue --token-threshold 40 --tool-threshold 1", sess))
	require.Contains(t, out.String(), "Performance Issue")
	require.Contains(t, out.String(), "high_token_usage")
	out.Reset()

	require.NoError(t, app.Insights([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "insights"`)
	require.Contains(t, out.String(), `"sessions": 1`)
	require.Contains(t, out.String(), `"tool_uses": 1`)
	require.Contains(t, out.String(), `"name": "read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/insights --limit 1", sess))
	require.Contains(t, out.String(), "Insights")
	require.Contains(t, out.String(), "Recent prompts")
	out.Reset()

	thinkBackPath := filepath.Join(workspace, "think-back.html")
	require.NoError(t, app.ThinkBack([]string{"--year", "2026", "--output", thinkBackPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "think_back"`)
	require.Contains(t, out.String(), `"year": 2026`)
	_, err = os.Stat(thinkBackPath)
	require.NoError(t, err)
	out.Reset()

	slashThinkBackPath := filepath.Join(workspace, "slash-think-back.html")
	require.True(t, app.handleSlash(context.Background(), "/think-back --year 2026 --output "+slashThinkBackPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(slashThinkBackPath)
	require.NoError(t, err)
	out.Reset()

	playPath := filepath.Join(workspace, "thinkback-play.html")
	require.True(t, app.handleSlash(context.Background(), "/thinkback-play --year 2026 --output "+playPath, sess))
	require.Contains(t, out.String(), "Think Back")
	_, err = os.Stat(playPath)
	require.NoError(t, err)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/summary", sess))
	require.Contains(t, out.String(), "Summary")
	require.Contains(t, out.String(), "Session          usage-session")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=0")
	require.Empty(t, errOut.String())
}

func TestCompactCommandPersistsCompactedSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	compactPath := filepath.Join(workspace, "compact-hook.json")
	postCompactPath := filepath.Join(workspace, "post-compact-hook.json")
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("compact-session", anthropic.TextMessage("assistant", "four")))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			AutoCompactMessages: 2,
			Hooks: config.HookConfig{
				PreCompactCommands:  []config.HookCommand{{Command: "cat > " + shellQuote(compactPath)}},
				PostCompactCommands: []config.HookCommand{{Command: "cat > " + shellQuote(postCompactPath)}},
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Compact([]string{"--session", "compact-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"original_messages": 4`)
	require.Contains(t, out.String(), `"remaining_messages": 3`)
	opened, err := store.Open("compact-session")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 3)
	require.Contains(t, opened.Messages[0].Content[0].Text, "auto-compacted")
	require.Equal(t, "three", opened.Messages[1].Content[0].Text)
	require.Equal(t, "four", opened.Messages[2].Content[0].Text)
	hookPayload, err := os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(hookPayload, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"session_id":"compact-session"`)
	postHookPayload, err := os.ReadFile(postCompactPath)
	require.NoError(t, err)
	var postCompactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(postHookPayload, &postCompactHook))
	require.Equal(t, "post_compact", postCompactHook.Event)
	require.Contains(t, postCompactHook.Input, `"session_id":"compact-session"`)
}

func TestUndoCommandAndSlashRestoreFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	_, err := undo.Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("new\n"), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.Undo([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "undo"`)
	require.Contains(t, out.String(), `"restored": true`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))

	out.Reset()
	_, err = undo.Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("newer\n"), 0o644))
	require.True(t, app.handleSlash(context.Background(), "/undo", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Undo")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))
	require.Empty(t, errOut.String())
}

func TestRateLimitOptionsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			RateLimit: config.RateLimitConfig{
				MaxRetries:       4,
				InitialBackoffMS: 250,
				MaxBackoffMS:     2000,
			},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.RateLimitOptions([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "rate_limit_options"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	require.Contains(t, out.String(), `"initial_backoff_ms": 250`)
	out.Reset()

	require.NoError(t, app.RateLimit([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "rate_limit"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/rate-limit status", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Rate Limit")
	require.Contains(t, out.String(), "Max retries      4")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/rate-limit-options", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Rate Limit Options")
	require.Contains(t, out.String(), "Max retries      4")
	require.Contains(t, out.String(), "429,500,502,503,504")
	out.Reset()

	require.NoError(t, app.MockLimits([]string{"--json", "--addr", ":9099", "--failures", "3", "--retry-after-ms", "1500"}))
	var mockReport mocklimits.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &mockReport))
	require.Equal(t, "mock_limits", mockReport.Kind)
	require.Equal(t, "ready", mockReport.Status)
	require.Equal(t, "http://127.0.0.1:9099", mockReport.BaseURL)
	require.Equal(t, 3, mockReport.Failures)
	require.Equal(t, 1500, mockReport.RetryAfterMS)
	require.True(t, commandAcceptsGlobalOutputFormat("mock-limits"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mock-limits --addr :9098 --failures 2", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Mock Limits")
	require.Contains(t, out.String(), "127.0.0.1:9098")
	require.Contains(t, out.String(), "Failures         2")
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "mock-limits", "--addr", ":9097", "--failures", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "mock_limits"`)
	require.Contains(t, cliOut, `"failures": 1`)
	require.Empty(t, errOut.String())
}

func TestAntTraceCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var providerRequest json.RawMessage
	server := httptest.NewServer(mockanthropic.Server{
		Text: "trace ok",
		OnRequest: func(raw json.RawMessage) {
			providerRequest = raw
		},
	}.Handler())
	defer server.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			Model:      "claude-test",
			BaseURL:    server.URL,
			APIKey:     "test-key",
			RateLimit: config.RateLimitConfig{
				MaxRetries:       1,
				InitialBackoffMS: 1,
				MaxBackoffMS:     2,
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AntTrace(context.Background(), []string{"--message", "trace me", "--json"}))
	var report anttrace.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ant_trace", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.AuthConfigured)
	require.True(t, report.RequestSent)
	require.Equal(t, server.URL, report.BaseURL)
	require.Equal(t, "trace ok", report.TextPreview)
	require.Equal(t, 2, report.StreamEvents)
	require.Equal(t, 10, report.Usage.InputTokens)
	require.Equal(t, 5, report.Usage.OutputTokens)
	require.Equal(t, 1, report.RateLimit.MaxRetries)
	require.NotEmpty(t, providerRequest)
	var request map[string]any
	require.NoError(t, json.Unmarshal(providerRequest, &request))
	require.Equal(t, "claude-test", request["model"])
	require.Equal(t, float64(64), request["max_tokens"])
	out.Reset()

	require.NoError(t, app.AntTrace(context.Background(), []string{"--no-request", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skipped", report.Status)
	require.False(t, report.RequestSent)
	out.Reset()

	require.NoError(t, app.AntTrace(context.Background(), []string{"--write", "--message", "trace me"}))
	require.Contains(t, out.String(), "Anthropic Trace")
	require.Contains(t, out.String(), "File")
	traceFiles, err := filepath.Glob(filepath.Join(workspace, ".codog", "traces", "*.md"))
	require.NoError(t, err)
	require.Len(t, traceFiles, 1)
	traceData, err := os.ReadFile(traceFiles[0])
	require.NoError(t, err)
	require.Contains(t, string(traceData), "# Codog Anthropic Trace")
	require.Contains(t, string(traceData), "trace ok")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ant-trace --no-request --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "skipped", report.Status)
	require.Empty(t, errOut.String())
	require.True(t, commandAcceptsGlobalOutputFormat("ant-trace"))
}

func TestResetLimitsCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"test","rate_limit":{"max_retries":4,"initial_backoff_ms":250}}`), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			RateLimit:  config.RateLimitConfig{MaxRetries: 4, InitialBackoffMS: 250},
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.ResetLimits([]string{"--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"kind": "reset_limits"`)
	require.Contains(t, out.String(), `"max_retries": 4`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "rate_limit")
	out.Reset()

	require.NoError(t, os.WriteFile(configPath, []byte(`{"rate_limit":{"max_retries":3}}`), 0o644))
	app.Config.RateLimit = config.RateLimitConfig{MaxRetries: 3}
	require.True(t, app.handleSlash(context.Background(), "/reset-limits --path "+configPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Reset Limits")
	require.Contains(t, out.String(), "Previous retries 3")
	require.Empty(t, errOut.String())
}

func TestOutputStyleCommandAndSlashInjectsSystemPrompt(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "output-styles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "output-styles", "brief.md"), []byte("Answer in one compact paragraph.\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.OutputStyle(nil))
	require.Contains(t, out.String(), "Output Style")
	require.Contains(t, out.String(), "brief")
	require.Contains(t, out.String(), "concise")
	out.Reset()

	require.NoError(t, app.OutputStyle([]string{"set", "brief", "--json"}))
	require.Contains(t, out.String(), `"active": "brief"`)
	require.FileExists(t, outputstyle.StatePath(workspace))
	require.Contains(t, app.systemPrompt(), `<output_style name="brief" source="user">`)
	require.Contains(t, app.systemPrompt(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style show brief", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Body")
	require.Contains(t, out.String(), "Answer in one compact paragraph.")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/output-style clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Output Style")
	require.NotContains(t, app.systemPrompt(), "<output_style")
	require.Empty(t, errOut.String())
}

func TestThemeVimAndPrivacyCommandsPersistPreferences(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("session", anthropic.TextMessage("assistant", "assistant answer")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Sessions:  store,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Theme([]string{"dark", "--json"}))
	require.Contains(t, out.String(), `"kind": "theme"`)
	require.Contains(t, out.String(), `"theme": "dark"`)
	require.Equal(t, "dark", app.Config.Theme)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "dark"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/color light", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "light", app.Config.Theme)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"theme": "light"`)
	out.Reset()

	require.NoError(t, app.Language([]string{"Japanese", "--json"}))
	require.Contains(t, out.String(), `"kind": "language"`)
	require.Contains(t, out.String(), `"language": "Japanese"`)
	require.Equal(t, "Japanese", app.Config.Language)
	require.Contains(t, app.systemPrompt(), "<codog_interface_language>Japanese</codog_interface_language>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"language": "Japanese"`)
	out.Reset()

	require.NoError(t, app.Vim([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"kind": "vim"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Equal(t, "vim", app.Config.EditorMode)
	require.True(t, app.readlineVimMode())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"editorMode": "vim"`)
	out.Reset()

	require.NoError(t, app.Effort([]string{"high", "--json"}))
	require.Contains(t, out.String(), `"kind": "effort"`)
	require.Contains(t, out.String(), `"effort": "high"`)
	require.Equal(t, "high", app.Config.ReasoningEffort)
	require.Contains(t, app.systemPrompt(), "<codog_reasoning_effort>high</codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "high"`)
	out.Reset()

	require.NoError(t, app.Reasoning([]string{"medium", "--json"}))
	require.Contains(t, out.String(), `"kind": "reasoning"`)
	require.Contains(t, out.String(), `"effort": "medium"`)
	require.Equal(t, "medium", app.Config.ReasoningEffort)
	require.Contains(t, app.systemPrompt(), "<codog_reasoning_effort>medium</codog_reasoning_effort>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning_effort": "medium"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/effort low", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Effort")
	require.Equal(t, "low", app.Config.ReasoningEffort)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/fast on", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Fast Mode")
	require.NotNil(t, app.Config.FastMode)
	require.True(t, *app.Config.FastMode)
	require.Contains(t, app.systemPrompt(), "<codog_fast_mode>enabled</codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"fast_mode": true`)
	out.Reset()

	voiceCommand := os.Args[0] + " -test.run=TestVoiceCommandHelperProcess"
	require.NoError(t, app.Voice([]string{"set-command", voiceCommand, "--json"}))
	require.Contains(t, out.String(), `"kind": "voice"`)
	require.Contains(t, out.String(), `"command_configured": true`)
	require.Contains(t, out.String(), `"command_available": true`)
	require.Equal(t, voiceCommand, app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Voice([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.VoiceEnabled)
	require.True(t, *app.Config.VoiceEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"voice_enabled": true`)
	out.Reset()

	t.Setenv("CODOG_TEST_VOICE_HELPER", "1")
	require.NoError(t, app.Voice([]string{"test", "--input", "mic-check", "--json"}))
	require.Contains(t, out.String(), `"action": "test"`)
	require.Contains(t, out.String(), `"transcript": "voice:mic-check"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	require.NoError(t, app.Voice([]string{"listen", "--input", "listen-check"}))
	require.Contains(t, out.String(), "Transcript       voice:listen-check")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/listen --input slash-check", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Transcript       voice:slash-check")
	out.Reset()

	speakCommand := os.Args[0] + " -test.run=TestVoiceCommandHelperProcess"
	require.NoError(t, app.Speak(context.Background(), []string{"set-command", speakCommand, "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "speak"`)
	require.Contains(t, out.String(), `"command_configured": true`)
	require.Contains(t, out.String(), `"command_available": true`)
	require.Equal(t, speakCommand, app.Config.SpeechCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"speech_command"`)
	out.Reset()

	t.Setenv("CODOG_TEST_SPEAK_HELPER", "1")
	require.NoError(t, app.Speak(context.Background(), []string{"--input", "say this", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"action": "speak"`)
	require.Contains(t, out.String(), `"text_preview": "say this"`)
	require.Contains(t, out.String(), `"stdout": "speak:say this"`)
	require.Contains(t, out.String(), `"exit_code": 0`)
	out.Reset()

	require.NoError(t, app.Speak(context.Background(), []string{"--json"}, config.FlagOverrides{SessionID: "session"}))
	require.Contains(t, out.String(), `"session_id": "session"`)
	require.Contains(t, out.String(), `"text_preview": "assistant answer"`)
	require.Contains(t, out.String(), `"stdout": "speak:assistant answer"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/speak", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Text             assistant answer")
	require.Contains(t, out.String(), "Stdout           speak:assistant answer")
	out.Reset()

	require.NoError(t, app.Speak(context.Background(), []string{"clear-command", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "speak"`)
	require.Equal(t, "", app.Config.SpeechCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"speech_command"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.Contains(t, out.String(), `"install_url": "https://claude.ai/chrome"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"on", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.True(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"chrome_default_enabled": true`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"permissions"}))
	require.Contains(t, out.String(), "Permissions URL")
	require.Contains(t, out.String(), "https://clau.de/chrome/permissions")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/chrome off", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Chrome")
	require.NotNil(t, app.Config.Future.ChromeDefaultEnabled)
	require.False(t, *app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"chrome_default_enabled": false`)
	out.Reset()

	require.NoError(t, app.PrivacySettings([]string{"set", "prompt-history", "off", "--json"}))
	require.Contains(t, out.String(), `"kind": "privacy_settings"`)
	require.Contains(t, out.String(), `"prompt_history_enabled": false`)
	require.False(t, app.promptHistoryEnabled())
	require.Empty(t, app.replHistoryFile())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"prompt_history_enabled": false`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/theme clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Theme")
	require.Equal(t, "", app.Config.Theme)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/language clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Language")
	require.Equal(t, "", app.Config.Language)
	require.NotContains(t, app.systemPrompt(), "<codog_interface_language>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"language"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/reasoning clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Reasoning")
	require.Equal(t, "", app.Config.ReasoningEffort)
	require.NotContains(t, app.systemPrompt(), "<codog_reasoning_effort>")
	out.Reset()

	require.NoError(t, app.Fast([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "fast"`)
	require.Nil(t, app.Config.FastMode)
	require.NotContains(t, app.systemPrompt(), "<codog_fast_mode>")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"fast_mode"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/voice clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Voice")
	require.Nil(t, app.Config.VoiceEnabled)
	require.Equal(t, "", app.Config.VoiceCommand)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"voice_enabled"`)
	require.NotContains(t, string(data), `"voice_command"`)
	out.Reset()

	require.NoError(t, app.Chrome([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"kind": "chrome"`)
	require.Nil(t, app.Config.Future.ChromeDefaultEnabled)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"chrome_default_enabled"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/privacy-settings enable prompt-history", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Privacy Settings")
	require.True(t, app.promptHistoryEnabled())
	require.Empty(t, errOut.String())
}

func TestKeybindingsCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	configHome := t.TempDir()
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			EditorMode: "vim",
		},
		Out: &out,
		Err: &errOut,
	}

	require.NoError(t, app.Keybindings([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "keybindings"`)
	require.Contains(t, out.String(), `"editor_mode": "vim"`)
	require.Contains(t, out.String(), `"vim_mode": true`)
	require.Contains(t, out.String(), `"keybindings_exists": false`)
	out.Reset()

	keybindingsPath := filepath.Join(configHome, "keybindings.json")
	require.NoError(t, app.Keybindings([]string{"path"}))
	require.Equal(t, keybindingsPath+"\n", out.String())
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"resolve", "repl", "Control-R", "--json"}))
	require.Contains(t, out.String(), `"action": "resolve"`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+r"`)
	require.Contains(t, out.String(), `"found": true`)
	require.Contains(t, out.String(), `"source": "default"`)
	require.Contains(t, out.String(), `"binding_action": "reverse search prompt history"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--json"}))
	require.Contains(t, out.String(), `"status": "created"`)
	require.Contains(t, out.String(), `"created": true`)
	data, err := os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"context": "repl"`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"action": "validate"`)
	require.Contains(t, out.String(), `"valid": true`)
	require.Contains(t, out.String(), `"context_count": 4`)
	require.Contains(t, out.String(), `"binding_count": 19`)
	require.Contains(t, out.String(), `"normalized_key": "ctrl+r"`)
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte("custom\n"), 0o644))
	require.Error(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"status": "invalid"`)
	require.Contains(t, out.String(), `"valid": false`)
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init"}))
	require.Contains(t, out.String(), "already exists")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(data))
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--force"}))
	require.Contains(t, out.String(), "Wrote keybindings template:")
	data, err = os.ReadFile(keybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"bindings"`)
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"Ctrl-R":"custom history search"}}]}`), 0o644))
	require.NoError(t, app.Keybindings([]string{"resolve", "repl", "ctrl+r"}))
	require.Contains(t, out.String(), "Keybinding Resolve")
	require.Contains(t, out.String(), "Source           user")
	require.Contains(t, out.String(), "Action           custom history search")
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"Ctrl-R":"custom","ctrl+r":"duplicate"}}]}`), 0o644))
	require.Error(t, app.Keybindings([]string{"validate", "--json"}))
	require.Contains(t, out.String(), `"status": "invalid"`)
	require.Contains(t, out.String(), "duplicate binding")
	out.Reset()

	require.NoError(t, os.WriteFile(keybindingsPath, []byte(`{"bindings":[{"context":"repl","bindings":{"enter":"submit"}},{"context":"repl","bindings":{"enter":"duplicate"}},{"context":"slash","bindings":{"/empty":""}}]}`), 0o644))
	require.Error(t, app.Keybindings([]string{"validate"}))
	require.Contains(t, out.String(), "duplicate binding")
	require.Contains(t, out.String(), "action is required")
	out.Reset()

	require.NoError(t, app.Keybindings([]string{"init", "--force"}))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/keybindings", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Keybindings")
	require.Contains(t, out.String(), "Editor mode      vim")
	require.Contains(t, out.String(), "REPL vim")
	require.Contains(t, out.String(), "Config exists    true")
	require.Contains(t, out.String(), "User valid       true")
	require.Contains(t, out.String(), "User bindings    19")
	require.Empty(t, errOut.String())
}

func TestNotificationsCommandAndHookGate(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	notificationPath := filepath.Join(workspace, "notification.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				Notification: []string{"cat > " + shellQuote(notificationPath)},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Notifications([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "notifications"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"configured": false`)
	require.Contains(t, out.String(), `"hook_count": 1`)
	out.Reset()

	require.NoError(t, app.Notifications([]string{"off", "--json"}))
	require.Contains(t, out.String(), `"action": "set"`)
	require.Contains(t, out.String(), `"enabled": false`)
	require.NotNil(t, app.Config.Future.NotificationsEnabled)
	require.False(t, *app.Config.Future.NotificationsEnabled)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"notifications_enabled": false`)
	out.Reset()

	app.runNotificationHook(context.Background(), "background_task_started", "Started", "task started")
	require.NoFileExists(t, notificationPath)

	require.True(t, app.handleSlash(context.Background(), "/notifications on", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Notifications")
	require.NotNil(t, app.Config.Future.NotificationsEnabled)
	require.True(t, *app.Config.Future.NotificationsEnabled)
	out.Reset()

	app.runNotificationHook(context.Background(), "background_task_started", "Started", "task started")
	data, err = os.ReadFile(notificationPath)
	require.NoError(t, err)
	var payload struct {
		Event            string `json:"event"`
		NotificationType string `json:"notification_type"`
		Title            string `json:"title"`
		Message          string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "notification", payload.Event)
	require.Equal(t, "background_task_started", payload.NotificationType)
	require.Equal(t, "Started", payload.Title)
	require.Equal(t, "task started", payload.Message)
	out.Reset()

	require.NoError(t, app.Notifications([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"configured": false`)
	require.Nil(t, app.Config.Future.NotificationsEnabled)
	data, err = os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"notifications_enabled"`)
	require.Empty(t, errOut.String())
}

func TestTerminalSetupCommandAndSlash(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	path := filepath.Join(t.TempDir(), ".zshrc")
	app := &App{Out: &out, Err: &errOut}

	require.NoError(t, app.TerminalSetup([]string{"install", "--shell", "zsh", "--path", path, "--json"}))
	require.Contains(t, out.String(), `"kind": "terminal_setup"`)
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "codog_statusline")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/terminal-setup status --shell zsh --path "+path, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Terminal Setup")
	require.Contains(t, out.String(), "Installed        true")
	require.Empty(t, errOut.String())
}

func TestSetupCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/setup\n"), 0o644))
	terminalPath := filepath.Join(t.TempDir(), ".zshrc")
	var out bytes.Buffer
	var errOut bytes.Buffer
	var setupPayloads []struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	setupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Input string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		setupPayloads = append(setupPayloads, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer setupServer.Close()
	app := &App{
		Config: config.Config{
			APIKey:     "test-key",
			ConfigHome: t.TempDir(),
			Model:      "claude-test",
			Hooks: config.HookConfig{
				SetupCommands: []config.HookCommand{{Type: "http", URL: setupServer.URL}},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Setup(context.Background(), []string{"status", "--shell", "zsh", "--path", terminalPath, "--json"}))
	var report setupReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "setup", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "warn", report.Status)
	require.NotNil(t, report.Terminal)
	require.False(t, report.Terminal.Installed)
	requireSetupCheck(t, report.Checks, "Provider credentials", "ok")
	requireSetupCheck(t, report.Checks, "Project memory", "warn")
	requireSetupCheck(t, report.Checks, "Terminal integration", "warn")
	out.Reset()

	require.NoError(t, app.Setup(context.Background(), []string{"init", "--json"}))
	report = setupReport{}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "init", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotNil(t, report.Project)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.Len(t, setupPayloads, 1)
	require.Equal(t, "setup", setupPayloads[0].Event)
	require.Contains(t, setupPayloads[0].Input, `"source":"setup"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/setup status --shell zsh --path "+terminalPath, &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Setup")
	require.Contains(t, out.String(), "Terminal integration")
	require.Empty(t, errOut.String())
}

func TestOnboardingCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package onboarding\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Run tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"permission_mode":"workspace-write"}`), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0o755))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Onboarding([]string{"--json"}))
	var report onboarding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "onboarding", report.Kind)
	require.Equal(t, "inspect", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.HasReadme)
	require.True(t, report.HasTests)
	require.Equal(t, "Go", report.PrimaryLanguage)
	require.True(t, report.GitRepository)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/onboarding --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ready", report.Status)
	require.Empty(t, errOut.String())
	out.Reset()

	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "app.py"), []byte("print('hi')\n"), 0o644))
	require.NoError(t, app.Onboarding([]string{"--path", other, "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "needs_setup", report.Status)
	require.Equal(t, "Python", report.PrimaryLanguage)
	require.True(t, report.PythonFirst)
}

func requireSetupCheck(t *testing.T, checks []setupCheck, name string, status string) {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			require.Equal(t, status, check.Status)
			return
		}
	}
	require.Failf(t, "missing setup check", "check %q not found in %#v", name, checks)
}

func TestRemoteEnvCommandPersistsSettings(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Out: &out, Err: &errOut}

	require.NoError(t, app.RemoteEnv([]string{"set", "--enabled", "on", "--auth-token", "secret-token", "--lease-seconds", "60", "--json"}))
	require.Contains(t, out.String(), `"kind": "remote_env"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 60, app.Config.Future.RemoteLeaseSeconds)
	configPath := filepath.Join(configHome, "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote_enabled": true`)
	require.Contains(t, string(data), `"remote_auth_token": "secret-token"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-env clear", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Environment")
	require.False(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 0, app.Config.Future.RemoteLeaseSeconds)
	require.Empty(t, errOut.String())
}

func TestRemoteSetupCommandPersistsAndReports(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.RemoteSetup([]string{"enable", "--addr", ":8799", "--auth-token", "secret-token", "--lease-seconds", "120", "--json"}, config.FlagOverrides{SessionID: "setup-session"}))
	require.Contains(t, out.String(), `"kind": "remote_setup"`)
	require.Contains(t, out.String(), `"enabled": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"session_id": "setup-session"`)
	require.NotContains(t, out.String(), "secret-token")
	require.True(t, app.Config.Future.RemoteEnabled)
	require.Equal(t, "secret-token", app.Config.Future.RemoteAuthToken)
	require.Equal(t, 120, app.Config.Future.RemoteLeaseSeconds)
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"remote_enabled": true`)
	require.Contains(t, string(data), `"remote_auth_token": "secret-token"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/remote-setup disable --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "Enabled          false")
	require.Contains(t, out.String(), "127.0.0.1:9999")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/web-setup status --addr 127.0.0.1:8888", &session.Session{ID: "web-session"}))
	require.Contains(t, out.String(), "Remote Setup")
	require.Contains(t, out.String(), "127.0.0.1:8888")
	require.Contains(t, out.String(), "web-session")
	require.Empty(t, errOut.String())
}

func TestAPICommandReportsRemoteControlRoutes(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 90,
			},
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.API([]string{"routes", "--addr", ":8799", "--json"}))
	var report apiReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "api", report.Kind)
	require.Equal(t, "routes", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.AuthRequired)
	require.Equal(t, "http://127.0.0.1:8799", report.RemoteURL)
	require.Equal(t, "codog remote serve :8799", report.RemoteCommand)
	require.Equal(t, 90, report.LeaseSeconds)
	require.Equal(t, len(control.RouteSpecs()), report.RouteCount)
	require.NotEmpty(t, report.Routes)
	require.Contains(t, out.String(), `"/file/write"`)
	require.NotContains(t, out.String(), "secret-token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/api status --addr 127.0.0.1:8800", &session.Session{ID: "active"}))
	require.Contains(t, out.String(), "Remote API")
	require.Contains(t, out.String(), "Remote URL       http://127.0.0.1:8800")
	require.Contains(t, out.String(), "/health")
	require.Empty(t, errOut.String())
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home": configHome,
		"future": map[string]any{
			"remote_enabled":       true,
			"remote_auth_token":    "secret-token",
			"remote_lease_seconds": 90,
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "api", "routes", "--addr", "127.0.0.1:8810"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &report))
	require.Equal(t, "api", report.Kind)
	require.Equal(t, "http://127.0.0.1:8810", report.RemoteURL)
	require.True(t, report.AuthRequired)
	require.NotContains(t, cliOut, "secret-token")
}

func TestRunCLIRoutesWebSetupAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "web-setup", "status", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "remote_setup"`)
	require.Contains(t, out, `"remote_url": "http://127.0.0.1:8791"`)
}

func TestRunCLIRoutesRemoteControlAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	_, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "remote-control"}, config.FlagOverrides{})
	})
	require.ErrorContains(t, err, "usage: codog bridge serve")
}

func TestDesktopAndMobileHandoffCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("handoff-session", anthropic.TextMessage("user", "hello handoff")))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 90,
				EditorBridgeSocket: "codog.sock",
				EditorBridgeToken:  "bridge-secret",
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Desktop([]string{"--session", "handoff-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "desktop_handoff"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"command": "codog bridge serve"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	out.Reset()

	require.NoError(t, app.Mobile([]string{"ios", "--addr", ":8799", "--resume", "latest", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "mobile_handoff"`)
	require.Contains(t, out.String(), `"platform": "ios"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mobile android --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Mobile Handoff")
	require.Contains(t, out.String(), "android")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/desktop", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Desktop Handoff")
	require.Contains(t, out.String(), "codog bridge serve")
	require.Empty(t, errOut.String())
}

func TestPromptHistoryPreferenceSkipsInputRecords(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	disabled := false
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
			Privacy:             config.PrivacyConfig{PromptHistoryEnabled: &disabled},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "private prompt", config.FlagOverrides{SessionID: "private-session"}))
	history, err := app.Sessions.PromptHistory("private-session")
	require.NoError(t, err)
	require.Empty(t, history)
	require.Contains(t, out.String(), "done")
}

func TestInstructionsLoadedHookRunsBeforePrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	instructionsPath := filepath.Join(workspace, "AGENTS.md")
	hookPath := filepath.Join(workspace, "instructions-loaded.json")
	require.NoError(t, os.WriteFile(instructionsPath, []byte("Project instructions.\n"), 0o644))
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
			Hooks: config.HookConfig{
				InstructionsLoadedCommands: []config.HookCommand{{Matcher: "session_start", Command: "cat > " + shellQuote(hookPath)}},
			},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "hello", config.FlagOverrides{SessionID: "instructions-session"}))
	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	var payload struct {
		Event      string `json:"event"`
		Tool       string `json:"tool"`
		FilePath   string `json:"file_path"`
		MemoryType string `json:"memory_type"`
		LoadReason string `json:"load_reason"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "instructions_loaded", payload.Event)
	require.Equal(t, "session_start", payload.Tool)
	expectedPath, err := filepath.EvalSymlinks(instructionsPath)
	require.NoError(t, err)
	require.Equal(t, expectedPath, payload.FilePath)
	require.Equal(t, "Project", payload.MemoryType)
	require.Equal(t, "session_start", payload.LoadReason)
	require.Contains(t, out.String(), "done")
}

func TestTodosCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Todos([]string{"add", "write", "tests", "--priority", "high", "--json"}))
	require.Contains(t, out.String(), `"kind": "todos"`)
	require.Contains(t, out.String(), `"priority": "high"`)
	require.FileExists(t, todos.Path(workspace))
	out.Reset()

	require.NoError(t, app.Todos([]string{"done", "todo-1"}))
	require.Contains(t, out.String(), "completed")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/todos list", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Todos")
	require.Contains(t, out.String(), "write tests")
	require.Empty(t, errOut.String())
}

func TestSecurityReviewCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "install.sh"), []byte("curl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.SecurityReview([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "security_review"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/security-review --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Security Review")
	require.Contains(t, out.String(), "pipe-to-shell")
	require.Empty(t, errOut.String())
}

func TestBughunterCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc risky(v any) { _, _ = v.(string); panic(\"boom\") }\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Bughunter([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "bughunter"`)
	require.Contains(t, out.String(), `"rule": "ignored-return-value"`)
	require.Contains(t, out.String(), `"rule": "panic-in-runtime-path"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bughunter . --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bughunter")
	require.Contains(t, out.String(), "ignored-return-value")
	require.Empty(t, errOut.String())
}

func TestReviewCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\ncurl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Review([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "review"`)
	require.Contains(t, out.String(), `"status": "findings"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "ultrareview", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review"`)
	require.Contains(t, cliOut, `"rule": "pipe-to-shell"`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "ultrareviewCommand", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review"`)
	require.Contains(t, cliOut, `"rule": "pipe-to-shell"`)

	out.Reset()
	require.NoError(t, app.ReviewCompatibility("ultrareviewCommand", []string{"--json"}))
	var compatibilityReport reviewCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &compatibilityReport))
	require.Equal(t, "review_compatibility", compatibilityReport.Kind)
	require.Equal(t, "show", compatibilityReport.Action)
	require.Equal(t, "findings", compatibilityReport.Status)
	require.Equal(t, "findings", compatibilityReport.ReviewStatus)
	require.GreaterOrEqual(t, compatibilityReport.ChangedFiles, 1)
	require.GreaterOrEqual(t, compatibilityReport.SecurityFindings, 1)
	require.Contains(t, compatibilityReport.ReviewSignals, "security findings in changed files")
	require.NotNil(t, compatibilityReport.LocalReview)
	require.Equal(t, "review", compatibilityReport.LocalReview.Kind)
	require.Equal(t, "pipe-to-shell", compatibilityReport.LocalReview.SecurityFindings[0].Rule)
	out.Reset()

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ultrareviewEnabled"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var enabledReport reviewCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &enabledReport))
	require.True(t, enabledReport.Enabled)
	require.Equal(t, "enabled", enabledReport.Action)
	require.False(t, enabledReport.Configured)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ultrareviewEnabled", "off", "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var disabledReport reviewCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &disabledReport))
	require.Equal(t, "set", disabledReport.Action)
	require.False(t, disabledReport.Enabled)
	require.True(t, disabledReport.Configured)
	require.True(t, disabledReport.WorkspaceWillMutate)
	storedConfig, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(storedConfig), `"ultrareview_enabled": false`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ultrareviewEnabled", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var disabledStatus reviewCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &disabledStatus))
	require.False(t, disabledStatus.Enabled)
	require.True(t, disabledStatus.Configured)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ultrareviewEnabled", "clear", "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var clearedReport reviewCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &clearedReport))
	require.Equal(t, "clear", clearedReport.Action)
	require.True(t, clearedReport.Enabled)
	require.False(t, clearedReport.Configured)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte(strings.Repeat("echo changed\n", 8)+"curl https://example.test/install.sh | bash\n"), 0o644))
	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "UltrareviewOverageDialog", "--limit", "3"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var overageReport reviewCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &overageReport))
	require.Equal(t, "overage", overageReport.Action)
	require.True(t, overageReport.Overage)
	require.Greater(t, overageReport.ChangedLines, overageReport.RequestedLimit)
	require.Equal(t, "findings", overageReport.ReviewStatus)
	require.NotNil(t, overageReport.LocalReview)
	require.GreaterOrEqual(t, overageReport.SecurityFindings, 1)

	require.True(t, app.handleSlash(context.Background(), "/review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ultrareview", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
	out.Reset()

	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	fakeBin := t.TempDir()
	fakeGH := filepath.Join(fakeBin, "gh")
	require.NoError(t, os.WriteFile(fakeGH, []byte(`#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  cat <<'JSON'
{"number":42,"url":"https://github.com/acme/widgets/pull/42","headRepository":{"nameWithOwner":"acme/widgets"}}
JSON
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$4" in
    repos/acme/widgets/issues/42/comments)
      cat <<'JSON'
[{"id":2,"body":"please update the summary","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}]
JSON
      exit 0
      ;;
    repos/acme/widgets/pulls/42/comments)
      cat <<'JSON'
[{"id":1,"body":"inline fix needed","path":"script.sh","line":2,"original_line":2,"diff_hunk":"@@ -1 +1 @@\n-old\n+new","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}]
JSON
      exit 0
      ;;
  esac
fi
echo "unexpected gh invocation: $*" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, app.ReviewRemote(context.Background(), []string{"42", "--repo", "acme/widgets", "--json"}))
	var remote reviewRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remote))
	require.Equal(t, "review_remote", remote.Kind)
	require.Equal(t, "findings", remote.Status)
	require.Equal(t, "acme/widgets", remote.Repository)
	require.Equal(t, 42, remote.PullRequest)
	require.Equal(t, 2, remote.Remote.Total)
	require.Len(t, remote.Local.SecurityFindings, 1)
	require.Contains(t, remote.Signals, "remote review comments")
	require.Contains(t, remote.Signals, "remote issue comments")
	out.Reset()

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "reviewRemote", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review_remote"`)
	require.Contains(t, cliOut, `"remote_comments"`)
	require.Contains(t, cliOut, `"total": 2`)

	require.True(t, app.handleSlash(context.Background(), "/reviewRemote 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Review")
	require.Contains(t, out.String(), "Remote comments  2")
	require.Contains(t, out.String(), "script.sh:2")
	require.Contains(t, out.String(), "inline fix needed")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/review-remote 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Review")
	require.Contains(t, out.String(), "PR Comments")
	require.Empty(t, errOut.String())
}

func TestMiscCompatibilityCommands(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.ExitCompatibility([]string{"--json"}))
	var exitReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &exitReport))
	require.Equal(t, "exit", exitReport.Kind)
	require.Equal(t, "ok", exitReport.Status)
	require.False(t, exitReport.ProviderRequestMade)
	out.Reset()

	require.NoError(t, app.GoodClaude([]string{"nice", "--json"}))
	var goodReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &goodReport))
	require.Equal(t, "feedback", goodReport.Kind)
	require.Equal(t, "good_claude", goodReport.Action)
	require.True(t, goodReport.WorkspaceWillMutate)
	require.Contains(t, goodReport.NextCommand, "codog feedback")
	require.NotEmpty(t, goodReport.File)
	require.Greater(t, goodReport.Bytes, 0)
	require.FileExists(t, goodReport.File)
	goodData, err := os.ReadFile(goodReport.File)
	require.NoError(t, err)
	require.Contains(t, string(goodData), "Positive feedback from good-claude: nice")
	out.Reset()

	require.NoError(t, app.MovedToPluginCommand([]string{"legacy-tool", "--dry-run", "--json"}))
	var dryRunReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dryRunReport))
	require.Equal(t, "command_migration", dryRunReport.Kind)
	require.Equal(t, "moved_to_plugin", dryRunReport.Action)
	require.Equal(t, "legacy-tool", dryRunReport.PluginID)
	require.True(t, dryRunReport.DryRun)
	require.False(t, dryRunReport.Created)
	require.False(t, dryRunReport.WorkspaceWillMutate)
	require.Contains(t, dryRunReport.NextCommand, "legacy-tool:legacy-tool")
	require.NoFileExists(t, dryRunReport.ManifestFile)
	require.NoFileExists(t, dryRunReport.CommandFile)
	out.Reset()

	require.NoError(t, app.MovedToPluginCommand([]string{"legacy-tool", "--json"}))
	var movedReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &movedReport))
	require.Equal(t, "command_migration", movedReport.Kind)
	require.Equal(t, "moved_to_plugin", movedReport.Action)
	require.Equal(t, "ok", movedReport.Status)
	require.Equal(t, "legacy-tool", movedReport.PluginID)
	require.True(t, movedReport.WorkspaceWillMutate)
	require.True(t, movedReport.Created)
	require.False(t, movedReport.DryRun)
	require.Contains(t, movedReport.NextCommand, "commands show 'legacy-tool:legacy-tool'")
	require.FileExists(t, movedReport.ManifestFile)
	require.FileExists(t, movedReport.CommandFile)
	require.Greater(t, movedReport.Bytes, 0)
	validation, err := plugins.Validate(movedReport.PluginRoot)
	require.NoError(t, err)
	require.True(t, validation.Success)
	commandData, err := os.ReadFile(movedReport.CommandFile)
	require.NoError(t, err)
	require.Contains(t, string(commandData), "Arguments: $ARGUMENTS")
	require.Contains(t, string(commandData), "migration adapter")
	require.Contains(t, string(commandData), "codog commands show legacy-tool:legacy-tool")
	require.Contains(t, string(commandData), "missing plugin implementation")
	require.NotContains(t, string(commandData), "Replace this "+"body")
	out.Reset()

	require.NoError(t, app.Commands([]string{"list", "--json"}))
	var commandsReport struct {
		Commands []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"commands"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &commandsReport))
	require.Contains(t, commandsReport.Commands, struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}{Name: "legacy-tool:legacy-tool", Source: "plugin:legacy-tool"})
	out.Reset()

	require.NoError(t, app.Commands([]string{"show", "legacy-tool:legacy-tool", "--json"}))
	var commandReport struct {
		Name       string `json:"name"`
		Source     string `json:"source"`
		PluginRoot string `json:"plugin_root"`
		Body       string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &commandReport))
	require.Equal(t, "legacy-tool:legacy-tool", commandReport.Name)
	require.Equal(t, "plugin:legacy-tool", commandReport.Source)
	require.NotEmpty(t, commandReport.PluginRoot)
	require.Contains(t, commandReport.Body, "migration adapter")
	require.Contains(t, commandReport.Body, "missing plugin implementation")
	require.NotContains(t, commandReport.Body, "Replace this "+"body")
	out.Reset()

	require.NoError(t, app.Commands([]string{"run", "legacy-tool:legacy-tool", "file.go"}))
	require.Contains(t, out.String(), "Arguments: file.go")
}

func TestAutofixPRCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := initGitRepo(t)
	fakeBin := t.TempDir()
	fakeGH := filepath.Join(fakeBin, "gh")
	require.NoError(t, os.WriteFile(fakeGH, []byte(`#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  cat <<'JSON'
{"number":42,"url":"https://github.com/acme/widgets/pull/42","headRepository":{"nameWithOwner":"acme/widgets"}}
JSON
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$4" in
    repos/acme/widgets/issues/42/comments)
      cat <<'JSON'
[{"id":2,"body":"please update the summary","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}]
JSON
      exit 0
      ;;
    repos/acme/widgets/pulls/42/comments)
      cat <<'JSON'
[{"id":1,"body":"inline fix needed","path":"script.sh","line":2,"original_line":2,"diff_hunk":"@@ -1 +1 @@\n-old\n+new","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}]
JSON
      exit 0
      ;;
  esac
fi
echo "unexpected gh invocation: $*" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.AutofixPR(context.Background(), []string{"42", "--repo", "acme/widgets", "--json"}))
	var report autofixpr.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "autofix_pr", report.Kind)
	require.Equal(t, "ready", report.Status)
	require.Equal(t, "acme/widgets", report.Repository)
	require.Equal(t, 42, report.PullRequest)
	require.Equal(t, 2, report.Total)
	require.Equal(t, 2, report.Actionable)
	require.Len(t, report.Items, 2)
	require.Contains(t, report.Prompt, "Fix the GitHub pull request feedback")
	require.Contains(t, report.Prompt, "script.sh:2")
	out.Reset()

	require.NoError(t, app.AutofixPR(context.Background(), []string{"42", "--repo", "acme/widgets", "--write"}))
	require.Contains(t, out.String(), "Autofix PR")
	require.Contains(t, out.String(), "File")
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "autofix", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Autofix PR Task")
	require.Contains(t, string(data), "inline fix needed")
	require.Contains(t, string(data), "please update the summary")
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "autofix-pr", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "autofix_pr"`)
	require.Contains(t, cliOut, `"actionable_comments": 2`)

	require.True(t, app.handleSlash(context.Background(), "/autofix-pr 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Autofix PR")
	require.Contains(t, out.String(), "Fix items")
	require.Empty(t, errOut.String())
}

func TestFeedbackCommandAndSlashWritesReport(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "feedback context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Feedback([]string{"bug", "report", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "feedback"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Codog Feedback")
	require.Contains(t, string(data), "bug report")
	require.Contains(t, string(data), "source (1 messages)")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/feedback slash report", sess))
	require.Contains(t, out.String(), "Feedback")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 2)
}

func TestPullRequestAndIssueDraftCommands(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "draft context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.PullRequestDraft([]string{"ship", "readme", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "pr"`)
	require.Contains(t, out.String(), `"action": "draft"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "pr-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Pull Request Draft")
	require.Contains(t, string(data), "PR: ship readme")
	require.Contains(t, string(data), "README.md")
	require.Contains(t, string(data), "source (1 messages)")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/issue flaky workflow", sess))
	require.Contains(t, out.String(), "Issue Draft")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "issue-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err = os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Issue Draft")
	require.Contains(t, string(data), "Issue: flaky workflow")
}

func TestCommitPushPRDryRunCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "commit-push-pr", "feat: dry run", "--dry-run", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "commit_push_pr"`)
	require.Contains(t, cliOut, `"status": "planned"`)
	require.Contains(t, cliOut, `"pull_request"`)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.True(t, app.handleSlash(context.Background(), "/commit-push-pr feat: slash dry run --dry-run --no-pr", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Commit Push PR")
	require.Contains(t, out.String(), "Dry run          true")
	require.NotContains(t, out.String(), "pull_request")
	require.Empty(t, errOut.String())
}

func TestInstallGitHubAppCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow", "claude", "--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "install_github_app"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.False(t, fileExists(filepath.Join(workspace, ".github", "workflows", "claude.yml")))
	out.Reset()

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow=review", "--secret-name", "CLAUDE_KEY"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	path := filepath.Join(workspace, ".github", "workflows", "claude-code-review.yml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "anthropics/claude-code-action@v1")
	require.Contains(t, string(data), "${{ secrets.CLAUDE_KEY }}")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/install-github-app --workflow claude --dry-run", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	require.Empty(t, errOut.String())

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "setupGitHubActions", "--workflow", "all", "--dry-run"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "install_github_app"`)
	require.Contains(t, cliOut, `"dry_run": true`)
	require.Contains(t, cliOut, `"name": "claude"`)
	require.Contains(t, cliOut, `"name": "review"`)
}

func TestInstallGitHubAppStepCompatibilityCommands(t *testing.T) {
	workspace := t.TempDir()
	workflowPath := filepath.Join(workspace, ".github", "workflows", "claude.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte("custom workflow\n"), 0o644))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{APIKey: "test-key"},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.InstallGitHubAppStep("ApiKeyStep", []string{"--workflow", "claude", "--json"}))
	var apiReport installGitHubAppStepReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &apiReport))
	require.Equal(t, "install_github_app_step", apiReport.Kind)
	require.Equal(t, "ApiKeyStep", apiReport.Step)
	require.Equal(t, "ok", apiReport.Status)
	require.True(t, apiReport.APIKeyConfigured)
	require.False(t, apiReport.ProviderRequestMade)
	require.False(t, apiReport.WorkspaceWillMutate)
	require.True(t, apiReport.InstallCommandMutates)
	require.Contains(t, apiReport.NextCommand, "codog install-github-app")
	out.Reset()

	require.NoError(t, app.InstallGitHubAppStep("ExistingWorkflowStep", []string{"--workflow", "claude", "--json"}))
	var existingReport installGitHubAppStepReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &existingReport))
	require.Equal(t, "ExistingWorkflowStep", existingReport.Step)
	require.Equal(t, "warn", existingReport.Status)
	require.Contains(t, existingReport.ExistingWorkflows, workflowPath)
	require.Contains(t, existingReport.Messages[0], "workflow")
	out.Reset()

	require.NoError(t, app.InstallGitHubAppStep("CreatingStep", []string{"--workflow", "review", "--secret-name", "CLAUDE_KEY", "--json"}))
	var creatingReport installGitHubAppStepReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &creatingReport))
	require.Equal(t, "CreatingStep", creatingReport.Step)
	require.Equal(t, "ok", creatingReport.Status)
	require.True(t, creatingReport.WorkspaceWillMutate)
	require.Empty(t, creatingReport.NextCommand)
	require.Len(t, creatingReport.Workflows, 1)
	require.True(t, creatingReport.Workflows[0].Created)
	reviewPath := filepath.Join(workspace, ".github", "workflows", "claude-code-review.yml")
	reviewData, err := os.ReadFile(reviewPath)
	require.NoError(t, err)
	require.Contains(t, string(reviewData), "${{ secrets.CLAUDE_KEY }}")
	require.Contains(t, creatingReport.Messages, "Workflow creation completed.")
	out.Reset()

	dryWorkspace := t.TempDir()
	dryApp := &App{
		Config:    config.Config{APIKey: "test-key"},
		Workspace: dryWorkspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, dryApp.InstallGitHubAppStep("CreatingStep", []string{"--workflow", "review", "--dry-run", "--json"}))
	var dryReport installGitHubAppStepReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dryReport))
	require.Equal(t, "planned", dryReport.Status)
	require.False(t, dryReport.WorkspaceWillMutate)
	require.Contains(t, dryReport.NextCommand, "codog install-github-app")
	require.NotContains(t, dryReport.NextCommand, "--dry-run")
	require.False(t, fileExists(filepath.Join(dryWorkspace, ".github", "workflows", "claude-code-review.yml")))
	out.Reset()

	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("git"); err == nil {
			secretWorkspace := initGitRepo(t)
			runGit(t, secretWorkspace, "remote", "add", "origin", "git@github.com:acme/widgets.git")
			fakeBin := t.TempDir()
			fakeGH := filepath.Join(fakeBin, "gh")
			require.NoError(t, os.WriteFile(fakeGH, []byte(`#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo "Logged in to github.com"
  exit 0
fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then
  cat <<'JSON'
{"nameWithOwner":"acme/widgets"}
JSON
  exit 0
fi
if [ "$1" = "secret" ] && [ "$2" = "list" ]; then
  cat <<'JSON'
[{"name":"ANTHROPIC_API_KEY"},{"name":"OTHER_SECRET"}]
JSON
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "repos/acme/widgets/actions/permissions" ]; then
  cat <<'JSON'
{"enabled":true,"allowed_actions":"all"}
JSON
  exit 0
fi
echo "unexpected gh invocation: $*" >&2
exit 1
`), 0o755))
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			secretApp := &App{
				Config:    config.Config{APIKey: "test-key"},
				Workspace: secretWorkspace,
				Out:       &out,
				Err:       io.Discard,
			}
			require.NoError(t, secretApp.InstallGitHubAppStep("CheckExistingSecretStep", []string{"--json"}))
			var secretReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &secretReport))
			require.Equal(t, "CheckExistingSecretStep", secretReport.Step)
			require.Equal(t, "ok", secretReport.Status)
			require.True(t, secretReport.ProviderRequestMade)
			require.NotNil(t, secretReport.SecretCheck)
			require.True(t, secretReport.SecretCheck.Attempted)
			require.True(t, secretReport.SecretCheck.Available)
			require.True(t, secretReport.SecretCheck.Exists)
			require.Equal(t, "acme/widgets", secretReport.SecretCheck.Repo)
			require.Contains(t, secretReport.SecretCheck.Command, "--json")
			require.Contains(t, secretReport.Messages, "Repository secret ANTHROPIC_API_KEY exists on acme/widgets.")
			out.Reset()

			require.NoError(t, secretApp.InstallGitHubAppStep("CheckGitHubStep", []string{"--json"}))
			var githubReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &githubReport))
			require.Equal(t, "CheckGitHubStep", githubReport.Step)
			require.Equal(t, "ok", githubReport.Status)
			require.True(t, githubReport.ProviderRequestMade)
			require.NotNil(t, githubReport.GitHubCheck)
			require.True(t, githubReport.GitHubCheck.Attempted)
			require.True(t, githubReport.GitHubCheck.Authenticated)
			require.True(t, githubReport.GitHubCheck.RepoAccessible)
			require.Equal(t, "acme/widgets", githubReport.GitHubCheck.Repo)
			require.Contains(t, githubReport.GitHubCheck.AuthCommand, "status")
			require.Contains(t, githubReport.GitHubCheck.RepoCommand, "view")
			require.Contains(t, githubReport.Messages, "GitHub CLI authentication is active.")
			require.Contains(t, githubReport.Messages, "Repository acme/widgets is accessible through gh.")
			out.Reset()

			require.NoError(t, secretApp.InstallGitHubAppStep("InstallAppStep", []string{"--json"}))
			var installReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &installReport))
			require.Equal(t, "InstallAppStep", installReport.Step)
			require.Equal(t, "ok", installReport.Status)
			require.True(t, installReport.ProviderRequestMade)
			require.NotNil(t, installReport.ActionsCheck)
			require.True(t, installReport.ActionsCheck.Attempted)
			require.True(t, installReport.ActionsCheck.Available)
			require.True(t, installReport.ActionsCheck.Enabled)
			require.Equal(t, "all", installReport.ActionsCheck.AllowedActions)
			require.Contains(t, installReport.ActionsCheck.Command, "repos/acme/widgets/actions/permissions")
			require.Contains(t, installReport.Messages, "GitHub Actions is enabled for acme/widgets.")
			require.Contains(t, installReport.Messages, "Allowed actions policy: all.")
			out.Reset()

			require.NoError(t, secretApp.InstallGitHubAppStep("WarningsStep", []string{"--json"}))
			var deepWarningsReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &deepWarningsReport))
			require.Equal(t, "WarningsStep", deepWarningsReport.Step)
			require.Equal(t, "warn", deepWarningsReport.Status)
			require.True(t, deepWarningsReport.ProviderRequestMade)
			require.NotNil(t, deepWarningsReport.GitHubCheck)
			require.NotNil(t, deepWarningsReport.SecretCheck)
			require.NotNil(t, deepWarningsReport.ActionsCheck)
			require.Contains(t, deepWarningsReport.Warnings, "2 selected workflow file(s) are not present yet.")
			require.Contains(t, deepWarningsReport.Messages, "1 warning(s) need attention.")
			out.Reset()

			require.NoError(t, secretApp.InstallGitHubAppStep("ErrorStep", []string{"--json"}))
			var errorReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
			require.Equal(t, "ErrorStep", errorReport.Step)
			require.Equal(t, "error", errorReport.Status)
			require.True(t, errorReport.ProviderRequestMade)
			require.NotNil(t, errorReport.GitHubCheck)
			require.NotNil(t, errorReport.SecretCheck)
			require.NotNil(t, errorReport.ActionsCheck)
			require.Contains(t, errorReport.Errors, "2 selected workflow file(s) are not present yet.")
			require.Empty(t, errorReport.Warnings)
			require.Contains(t, errorReport.Messages, "1 setup error(s) need attention.")
			out.Reset()

			successWorkflowDir := filepath.Join(secretWorkspace, ".github", "workflows")
			require.NoError(t, os.MkdirAll(successWorkflowDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(successWorkflowDir, "claude.yml"), []byte("name: Claude Code\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(successWorkflowDir, "claude-code-review.yml"), []byte("name: Claude Code Review\n"), 0o644))
			require.NoError(t, secretApp.InstallGitHubAppStep("SuccessStep", []string{"--json"}))
			var successReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &successReport))
			require.Equal(t, "SuccessStep", successReport.Step)
			require.Equal(t, "ready", successReport.Status)
			require.True(t, successReport.ProviderRequestMade)
			require.NotNil(t, successReport.GitHubCheck)
			require.True(t, successReport.GitHubCheck.Authenticated)
			require.True(t, successReport.GitHubCheck.RepoAccessible)
			require.NotNil(t, successReport.SecretCheck)
			require.True(t, successReport.SecretCheck.Exists)
			require.NotNil(t, successReport.ActionsCheck)
			require.True(t, successReport.ActionsCheck.Enabled)
			require.Len(t, successReport.Workflows, 2)
			require.True(t, successReport.Workflows[0].Exists)
			require.True(t, successReport.Workflows[1].Exists)
			require.Contains(t, successReport.Messages, "All selected workflow files are present.")
			require.Contains(t, successReport.Messages, "Local GitHub App setup checks are ready.")
			require.Empty(t, successReport.Warnings)
			out.Reset()

			oauthHome := t.TempDir()
			_, err = oauth.SaveToken(oauthHome, oauth.Token{
				AccessToken: "oauth-access-1234",
				ExpiresAt:   time.Now().UTC().Add(time.Hour),
			})
			require.NoError(t, err)
			oauthApp := &App{
				Config:    config.Config{ConfigHome: oauthHome, APIKey: "test-key"},
				Workspace: secretWorkspace,
				Out:       &out,
				Err:       io.Discard,
			}
			require.NoError(t, oauthApp.InstallGitHubAppStep("OAuthFlowStep", []string{"--json"}))
			var oauthReport installGitHubAppStepReport
			require.NoError(t, json.Unmarshal(out.Bytes(), &oauthReport))
			require.Equal(t, "OAuthFlowStep", oauthReport.Step)
			require.Equal(t, "ok", oauthReport.Status)
			require.False(t, oauthReport.ProviderRequestMade)
			require.NotNil(t, oauthReport.OAuthStatus)
			require.True(t, oauthReport.OAuthStatus.TokenPresent)
			require.True(t, oauthReport.OAuthStatus.Ready)
			require.False(t, oauthReport.OAuthStatus.Expired)
			require.Contains(t, oauthReport.Messages, "OAuth token is ready for provider-backed setup.")
			require.Empty(t, oauthReport.Warnings)
			out.Reset()
		}
	}

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "WarningsStep", "--workflow", "claude"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var warningsReport installGitHubAppStepReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &warningsReport))
	require.Equal(t, "WarningsStep", warningsReport.Step)
	require.NotEmpty(t, warningsReport.Warnings)
}

func TestInstallSlackAppCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.InstallSlackApp([]string{"--json"}))
	require.Equal(t, slackAppURL, openedURL)
	require.Contains(t, out.String(), `"kind": "install_slack_app"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"install_count": 1`)
	require.Equal(t, 1, app.Config.Future.SlackAppInstallCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"slack_app_install_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/install-slack-app --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Slack App Setup")
	require.Contains(t, out.String(), slackAppURL)
	require.Equal(t, 2, app.Config.Future.SlackAppInstallCount)
	require.Empty(t, errOut.String())
}

func TestStickersCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.Stickers([]string{"--json"}))
	require.Equal(t, stickerOrderURL, openedURL)
	require.Contains(t, out.String(), `"kind": "stickers"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"order_count": 1`)
	require.Equal(t, 1, app.Config.Future.StickerOrderCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sticker_order_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/stickers --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Sticker Order")
	require.Contains(t, out.String(), stickerOrderURL)
	require.Equal(t, 2, app.Config.Future.StickerOrderCount)
	require.Empty(t, errOut.String())
}

func TestPassesCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	referralURL := "https://example.test/guest-pass"
	require.NoError(t, app.Passes([]string{"set-url", referralURL, "--json"}))
	require.Equal(t, referralURL, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), `"kind": "passes"`)
	require.Contains(t, out.String(), `"referral_url": "`+referralURL+`"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"guest_pass_referral_url": "`+referralURL+`"`)
	out.Reset()

	require.NoError(t, app.Passes([]string{"--json"}))
	require.Equal(t, referralURL, openedURL)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.GuestPassVisitCount)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/passes clear-url", &session.Session{ID: "session"}))
	require.Empty(t, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), "Guest Passes")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/passes --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), guestPassDocsURL)
	require.Equal(t, 2, app.Config.Future.GuestPassVisitCount)
}

func TestExtraUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.ExtraUsage([]string{"--admin", "--json"}))
	require.Equal(t, extraUsageAdminURL, openedURL)
	require.Contains(t, out.String(), `"kind": "extra_usage"`)
	require.Contains(t, out.String(), `"mode": "admin"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.ExtraUsageVisitCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"extra_usage_visit_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/extra-usage --personal --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Extra Usage")
	require.Contains(t, out.String(), extraUsagePersonalURL)
	require.Equal(t, 2, app.Config.Future.ExtraUsageVisitCount)
	require.Empty(t, errOut.String())

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(app.Workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	openedURL = ""
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "extra-usage-noninteractive", "--admin", "--open", "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Empty(t, openedURL)
	require.Contains(t, cliOut, `"kind": "extra_usage"`)
	require.Contains(t, cliOut, `"action": "show"`)
	require.Contains(t, cliOut, `"mode": "admin"`)
	require.Contains(t, cliOut, `"opened": false`)
	require.Contains(t, cliOut, `"visit_count":`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "extra-usage-core", "--personal", "--no-open", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "extra_usage"`)
	require.Contains(t, cliOut, `"mode": "personal"`)
	require.Contains(t, cliOut, `"visit_count":`)
}

func TestProjectCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/project\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Project instructions."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "initial project")
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Project(nil))
	require.Contains(t, out.String(), "Project")
	require.Contains(t, out.String(), "Go module")
	require.Contains(t, out.String(), "Memory files     1")
	out.Reset()

	require.NoError(t, app.Project([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "project"`)
	require.Contains(t, out.String(), `"available": true`)
	require.Contains(t, out.String(), `"go_module":`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/project", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Project")
}

func TestEnvCommandRedactsSensitiveValues(t *testing.T) {
	report := buildEnvReport([]string{
		"ALPHA=visible",
		"CODOG_SECRET_TOKEN=hidden",
		"NO_EQUALS",
	})
	require.Equal(t, 2, report.Total)
	require.Equal(t, 1, report.Redacted)
	require.Equal(t, "ALPHA", report.Variables[0].Name)
	require.Equal(t, "visible", report.Variables[0].Value)
	require.Equal(t, "CODOG_SECRET_TOKEN", report.Variables[1].Name)
	require.Equal(t, "[redacted]", report.Variables[1].Value)

	t.Setenv("CODOG_SECRET_TOKEN", "codog-super-secret-value-123")
	var out bytes.Buffer
	app := &App{Out: &out, Err: io.Discard}
	require.NoError(t, app.Env([]string{"--json"}))
	require.Contains(t, out.String(), `"name": "CODOG_SECRET_TOKEN"`)
	require.Contains(t, out.String(), `"value": "[redacted]"`)
	require.NotContains(t, out.String(), "codog-super-secret-value-123")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/env", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Environment")
}

func TestInitCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	var setupPayloads []struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	setupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Input string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		setupPayloads = append(setupPayloads, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer setupServer.Close()
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			Hooks: config.HookConfig{
				SetupCommands: []config.HookCommand{{Type: "http", URL: setupServer.URL}},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Init(nil))
	require.Contains(t, out.String(), "Init")
	require.Contains(t, out.String(), ".codog/instructions.md")
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.Len(t, setupPayloads, 1)
	require.Equal(t, "setup", setupPayloads[0].Event)
	require.Contains(t, setupPayloads[0].Input, `"source":"init"`)
	out.Reset()

	require.NoError(t, app.Init([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "init"`)
	require.Contains(t, out.String(), `"already_initialized": true`)
	require.Len(t, setupPayloads, 2)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Init")
	require.Len(t, setupPayloads, 3)
}

func TestInitVerifiersCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.InitVerifiers([]string{"--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "init_verifiers"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.NoError(t, app.InitVerifiers(nil))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init-verifiers --target codog --force", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "verifier-cli", "SKILL.md"))
	require.Empty(t, errOut.String())
}

func TestStateCommandAndREPLWritesWorkerState(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		In:        strings.NewReader("/exit\n"),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.REPL(context.Background(), config.FlagOverrides{SessionID: "session-1"}))
	require.FileExists(t, workerstate.Path(workspace))
	loaded, err := workerstate.Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "repl", loaded.Mode)
	require.Equal(t, "idle", loaded.Status)
	require.Equal(t, "session-1", loaded.SessionID)

	require.NoError(t, app.State(nil))
	require.Contains(t, out.String(), "State")
	require.Contains(t, out.String(), "Worker")
	out.Reset()

	require.NoError(t, app.State([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "worker_state"`)
	require.Contains(t, out.String(), `"mode": "repl"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/state", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), "State")
}

func TestStateCommandMissingStateReportsActionableErrors(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	err := renderWorkerState(&out, workspace, nil)
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.False(t, exitErr.Silent)
	require.Empty(t, out.String())
	require.Contains(t, err.Error(), "no worker state file found")
	require.Contains(t, err.Error(), "codog repl")
	require.Contains(t, err.Error(), "codog prompt <text>")
	require.Contains(t, err.Error(), "codog state [--json]")

	out.Reset()
	err = renderWorkerState(&out, workspace, []string{"--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report workerStateErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "worker_state", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "missing_worker_state", report.ErrorKind)
	require.Equal(t, workerstate.Path(workspace), report.Path)
	require.Contains(t, report.Message, "no worker state file found")
	require.Contains(t, report.Hint, "codog repl")
	require.Contains(t, report.Commands, "codog prompt <text>")
	require.Contains(t, report.Commands, "codog state [--json]")
}

func TestHooksCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "prompt.json")
	sessionPath := filepath.Join(workspace, "session.json")
	prePath := filepath.Join(workspace, "pre.json")
	postPath := filepath.Join(workspace, "post.json")
	postFailurePath := filepath.Join(workspace, "post-failure.json")
	permissionRequestPath := filepath.Join(workspace, "permission-request.json")
	permissionDeniedPath := filepath.Join(workspace, "permission-denied.json")
	sessionEndPath := filepath.Join(workspace, "session-end.json")
	setupPath := filepath.Join(workspace, "setup.json")
	stopPath := filepath.Join(workspace, "stop.json")
	stopFailurePath := filepath.Join(workspace, "stop-failure.json")
	compactPath := filepath.Join(workspace, "compact.json")
	postCompactPath := filepath.Join(workspace, "post-compact.json")
	notificationPath := filepath.Join(workspace, "notification.json")
	subagentStartPath := filepath.Join(workspace, "subagent-start.json")
	subagentStopPath := filepath.Join(workspace, "subagent-stop.json")
	worktreeCreatePath := filepath.Join(workspace, "worktree-create.json")
	worktreeRemovePath := filepath.Join(workspace, "worktree-remove.json")
	cwdChangedPath := filepath.Join(workspace, "cwd-changed.json")
	taskCreatedPath := filepath.Join(workspace, "task-created.json")
	taskCompletedPath := filepath.Join(workspace, "task-completed.json")
	instructionsLoadedPath := filepath.Join(workspace, "instructions-loaded.json")
	fileChangedPath := filepath.Join(workspace, "file-changed.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				UserPromptSubmit:   []string{"cat > " + shellQuote(promptPath)},
				SessionStart:       []string{"cat > " + shellQuote(sessionPath)},
				PreToolUse:         []string{"cat > " + shellQuote(prePath)},
				PostToolUse:        []string{"cat > " + shellQuote(postPath)},
				PostToolUseFailure: []string{"cat > " + shellQuote(postFailurePath)},
				PermissionRequest:  []string{"cat > " + shellQuote(permissionRequestPath)},
				PermissionDenied:   []string{"cat > " + shellQuote(permissionDeniedPath)},
				SessionEnd:         []string{"cat > " + shellQuote(sessionEndPath)},
				Setup:              []string{"cat > " + shellQuote(setupPath)},
				Stop:               []string{"cat > " + shellQuote(stopPath)},
				StopFailure:        []string{"cat > " + shellQuote(stopFailurePath)},
				PreCompact:         []string{"cat > " + shellQuote(compactPath)},
				PostCompact:        []string{"cat > " + shellQuote(postCompactPath)},
				Notification:       []string{"cat > " + shellQuote(notificationPath)},
				SubagentStart:      []string{"cat > " + shellQuote(subagentStartPath)},
				SubagentStop:       []string{"cat > " + shellQuote(subagentStopPath)},
				WorktreeCreate:     []string{"cat > " + shellQuote(worktreeCreatePath)},
				WorktreeRemove:     []string{"cat > " + shellQuote(worktreeRemovePath)},
				CwdChanged:         []string{"cat > " + shellQuote(cwdChangedPath)},
				TaskCreated:        []string{"cat > " + shellQuote(taskCreatedPath)},
				TaskCompleted:      []string{"cat > " + shellQuote(taskCompletedPath)},
				InstructionsLoaded: []string{"cat > " + shellQuote(instructionsLoadedPath)},
				FileChanged:        []string{"cat > " + shellQuote(fileChangedPath)},
				UserPromptSubmitCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(promptPath)},
				},
				SessionStartCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(sessionPath)},
				},
				PreToolUseCommands: []config.HookCommand{
					{Matcher: "read_*", Command: "cat > " + shellQuote(prePath)},
				},
				PostToolUseCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postPath)},
				},
				PostToolUseFailureCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postFailurePath)},
				},
				PermissionRequestCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(permissionRequestPath)},
				},
				PermissionDeniedCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(permissionDeniedPath)},
				},
				SessionEndCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(sessionEndPath)},
				},
				SetupCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(setupPath)},
				},
				StopCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(stopPath)},
				},
				StopFailureCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(stopFailurePath)},
				},
				PreCompactCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(compactPath)},
				},
				PostCompactCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(postCompactPath)},
				},
				NotificationCommands: []config.HookCommand{
					{Matcher: "background_*", Command: "cat > " + shellQuote(notificationPath)},
				},
				SubagentStartCommands: []config.HookCommand{
					{Matcher: "reviewer", Command: "cat > " + shellQuote(subagentStartPath)},
				},
				SubagentStopCommands: []config.HookCommand{
					{Matcher: "reviewer", Command: "cat > " + shellQuote(subagentStopPath)},
				},
				WorktreeCreateCommands: []config.HookCommand{
					{Matcher: "agent-*", Command: "cat > " + shellQuote(worktreeCreatePath)},
				},
				WorktreeRemoveCommands: []config.HookCommand{
					{Matcher: "agent-*", Command: "cat > " + shellQuote(worktreeRemovePath)},
				},
				CwdChangedCommands: []config.HookCommand{
					{Matcher: "*", Command: "cat > " + shellQuote(cwdChangedPath)},
				},
				TaskCreatedCommands: []config.HookCommand{
					{Matcher: "agent", Command: "cat > " + shellQuote(taskCreatedPath)},
				},
				TaskCompletedCommands: []config.HookCommand{
					{Matcher: "agent", Command: "cat > " + shellQuote(taskCompletedPath)},
				},
				InstructionsLoadedCommands: []config.HookCommand{
					{Matcher: "session_start", Command: "cat > " + shellQuote(instructionsLoadedPath)},
				},
				FileChangedCommands: []config.HookCommand{
					{Matcher: "write_file", Command: "cat > " + shellQuote(fileChangedPath)},
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Hooks(context.Background(), []string{"list", "--json"}))
	require.Contains(t, out.String(), `"user_prompt_submit"`)
	require.Contains(t, out.String(), `"session_start"`)
	require.Contains(t, out.String(), `"pre_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use_failure"`)
	require.Contains(t, out.String(), `"permission_request"`)
	require.Contains(t, out.String(), `"permission_denied"`)
	require.Contains(t, out.String(), `"session_end"`)
	require.Contains(t, out.String(), `"setup"`)
	require.Contains(t, out.String(), `"stop"`)
	require.Contains(t, out.String(), `"stop_failure"`)
	require.Contains(t, out.String(), `"pre_compact"`)
	require.Contains(t, out.String(), `"post_compact"`)
	require.Contains(t, out.String(), `"notification"`)
	require.Contains(t, out.String(), `"subagent_start"`)
	require.Contains(t, out.String(), `"subagent_stop"`)
	require.Contains(t, out.String(), `"worktree_create"`)
	require.Contains(t, out.String(), `"worktree_remove"`)
	require.Contains(t, out.String(), `"cwd_changed"`)
	require.Contains(t, out.String(), `"task_created"`)
	require.Contains(t, out.String(), `"task_completed"`)
	require.Contains(t, out.String(), `"instructions_loaded"`)
	require.Contains(t, out.String(), `"file_changed"`)
	var hooksList hooksListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &hooksList))
	require.Contains(t, hooksList.UserPromptSubmitCommands[0].Command, "cat >")
	require.Contains(t, hooksList.SessionStartCommands[0].Command, "cat >")
	require.Equal(t, "read_*", hooksList.PreToolUseCommands[0].Matcher)
	require.Contains(t, hooksList.PreToolUseCommands[0].Command, "cat >")
	require.Equal(t, "bash", hooksList.PostToolUseFailureCommands[0].Matcher)
	require.Equal(t, "bash", hooksList.PermissionRequestCommands[0].Matcher)
	require.Equal(t, "bash", hooksList.PermissionDeniedCommands[0].Matcher)
	require.Contains(t, hooksList.SessionEndCommands[0].Command, "cat >")
	require.Contains(t, hooksList.SetupCommands[0].Command, "cat >")
	require.Contains(t, hooksList.StopCommands[0].Command, "cat >")
	require.Contains(t, hooksList.StopFailureCommands[0].Command, "cat >")
	require.Contains(t, hooksList.PreCompactCommands[0].Command, "cat >")
	require.Contains(t, hooksList.PostCompactCommands[0].Command, "cat >")
	require.Equal(t, "background_*", hooksList.NotificationCommands[0].Matcher)
	require.Equal(t, "reviewer", hooksList.SubagentStartCommands[0].Matcher)
	require.Equal(t, "reviewer", hooksList.SubagentStopCommands[0].Matcher)
	require.Equal(t, "agent-*", hooksList.WorktreeCreateCommands[0].Matcher)
	require.Equal(t, "agent-*", hooksList.WorktreeRemoveCommands[0].Matcher)
	require.Equal(t, "*", hooksList.CwdChangedCommands[0].Matcher)
	require.Equal(t, "agent", hooksList.TaskCreatedCommands[0].Matcher)
	require.Equal(t, "agent", hooksList.TaskCompletedCommands[0].Matcher)
	require.Equal(t, "session_start", hooksList.InstructionsLoadedCommands[0].Matcher)
	require.Equal(t, "write_file", hooksList.FileChangedCommands[0].Matcher)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"health", "pre", "--tool", "read_file", "--json"}))
	var health hooksHealthReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &health))
	require.Equal(t, "health", health.Action)
	require.Equal(t, "pre_tool_use", health.Event)
	require.Equal(t, "read_file", health.MatcherTarget)
	require.Equal(t, 1, health.MatchedCount)
	require.Len(t, health.Matched, 1)
	require.Contains(t, health.Matched[0].Command, prePath)
	require.Greater(t, health.ConfiguredCount, 0)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "user-prompt-submit", "--input", "hello"}))
	data, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"user_prompt_submit"`)
	require.Contains(t, string(data), `"input":"hello"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "session-start", "--input", `{"source":"startup","session_id":"session"}`}))
	data, err = os.ReadFile(sessionPath)
	require.NoError(t, err)
	var sessionHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &sessionHook))
	require.Equal(t, "session_start", sessionHook.Event)
	require.Contains(t, sessionHook.Input, `"session_id"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "pre", "--tool", "read_file", "--input", `{"path":"README.md"}`}))
	require.Contains(t, out.String(), "Hook Run")
	data, err = os.ReadFile(prePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"pre_tool_use"`)
	require.Contains(t, string(data), `"tool":"read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post --tool=bash --output=done --error", sess))
	data, err = os.ReadFile(postPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post-failure --tool=bash --output=failed --error", sess))
	data, err = os.ReadFile(postFailurePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use_failure"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run permission-request --tool=bash --input={\"command\":\"git_status\"}", sess))
	data, err = os.ReadFile(permissionRequestPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"permission_request"`)
	require.Contains(t, string(data), `"tool_name":"bash"`)
	require.Contains(t, string(data), `"tool_input":{"command":"git_status"}`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run permission-denied --tool=bash --input={\"command\":\"blocked\"} --reason=deny_rule", sess))
	data, err = os.ReadFile(permissionDeniedPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"permission_denied"`)
	require.Contains(t, string(data), `"reason":"deny_rule"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run session-end --input={\"session_id\":\"session\"} --reason=exit", sess))
	data, err = os.ReadFile(sessionEndPath)
	require.NoError(t, err)
	var sessionEndHook struct {
		Event  string `json:"event"`
		Input  string `json:"input"`
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(data, &sessionEndHook))
	require.Equal(t, "session_end", sessionEndHook.Event)
	require.Equal(t, "exit", sessionEndHook.Reason)
	require.Contains(t, sessionEndHook.Input, `"session_id"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run setup --input={\"source\":\"init\"}", sess))
	data, err = os.ReadFile(setupPath)
	require.NoError(t, err)
	var setupHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &setupHook))
	require.Equal(t, "setup", setupHook.Event)
	require.Contains(t, setupHook.Input, `"source"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run stop --output=done", sess))
	data, err = os.ReadFile(stopPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"stop"`)
	require.Contains(t, string(data), `"output":"done"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run stop-failure --output=rate_limited --reason=model_error", sess))
	data, err = os.ReadFile(stopFailurePath)
	require.NoError(t, err)
	var stopFailureHook struct {
		Event   string `json:"event"`
		Output  string `json:"output"`
		IsError bool   `json:"is_error"`
		Reason  string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(data, &stopFailureHook))
	require.Equal(t, "stop_failure", stopFailureHook.Event)
	require.Equal(t, "rate_limited", stopFailureHook.Output)
	require.True(t, stopFailureHook.IsError)
	require.Equal(t, "model_error", stopFailureHook.Reason)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run pre-compact --input={\"source\":\"manual\"}", sess))
	data, err = os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"source"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post-compact --input={\"source\":\"manual\"}", sess))
	data, err = os.ReadFile(postCompactPath)
	require.NoError(t, err)
	var postCompactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &postCompactHook))
	require.Equal(t, "post_compact", postCompactHook.Event)
	require.Contains(t, postCompactHook.Input, `"source"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "notification", "--notification-type", "background_task_started", "--title", "Started", "--input", "task started"}))
	data, err = os.ReadFile(notificationPath)
	require.NoError(t, err)
	var notificationHook struct {
		Event            string `json:"event"`
		Tool             string `json:"tool"`
		Message          string `json:"message"`
		Title            string `json:"title"`
		NotificationType string `json:"notification_type"`
	}
	require.NoError(t, json.Unmarshal(data, &notificationHook))
	require.Equal(t, "notification", notificationHook.Event)
	require.Equal(t, "background_task_started", notificationHook.Tool)
	require.Equal(t, "task started", notificationHook.Message)
	require.Equal(t, "Started", notificationHook.Title)
	require.Equal(t, "background_task_started", notificationHook.NotificationType)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "subagent-start", "--agent-id", "task-1", "--agent-type", "reviewer"}))
	data, err = os.ReadFile(subagentStartPath)
	require.NoError(t, err)
	var subagentStartHook struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	require.NoError(t, json.Unmarshal(data, &subagentStartHook))
	require.Equal(t, "subagent_start", subagentStartHook.Event)
	require.Equal(t, "reviewer", subagentStartHook.Tool)
	require.Equal(t, "task-1", subagentStartHook.AgentID)
	require.Equal(t, "reviewer", subagentStartHook.AgentType)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "subagent-stop", "--agent-id", "task-1", "--agent-type", "reviewer", "--agent-transcript-path", "logs/task-1.log", "--last-assistant-message", "done", "--stop-hook-active"}))
	data, err = os.ReadFile(subagentStopPath)
	require.NoError(t, err)
	var subagentStopHook struct {
		Event          string `json:"event"`
		AgentID        string `json:"agent_id"`
		AgentType      string `json:"agent_type"`
		TranscriptPath string `json:"agent_transcript_path"`
		LastAssistant  string `json:"last_assistant_message"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	require.NoError(t, json.Unmarshal(data, &subagentStopHook))
	require.Equal(t, "subagent_stop", subagentStopHook.Event)
	require.Equal(t, "task-1", subagentStopHook.AgentID)
	require.Equal(t, "reviewer", subagentStopHook.AgentType)
	require.Equal(t, "logs/task-1.log", subagentStopHook.TranscriptPath)
	require.Equal(t, "done", subagentStopHook.LastAssistant)
	require.True(t, subagentStopHook.StopHookActive)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "worktree-create", "--worktree-id", "agent-1", "--worktree-path", filepath.Join(workspace, "wt"), "--ref", "abc123", "--input", `{"source":"agent"}`}))
	data, err = os.ReadFile(worktreeCreatePath)
	require.NoError(t, err)
	var worktreeCreateHook struct {
		Event        string `json:"event"`
		Tool         string `json:"tool"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Ref          string `json:"ref"`
	}
	require.NoError(t, json.Unmarshal(data, &worktreeCreateHook))
	require.Equal(t, "worktree_create", worktreeCreateHook.Event)
	require.Equal(t, "agent-1", worktreeCreateHook.Tool)
	require.Equal(t, "agent-1", worktreeCreateHook.WorktreeID)
	require.Equal(t, filepath.Join(workspace, "wt"), worktreeCreateHook.WorktreePath)
	require.Equal(t, "abc123", worktreeCreateHook.Ref)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "worktree-remove", "--worktree-id", "agent-1", "--worktree-path", filepath.Join(workspace, "wt"), "--ref", "abc123", "--reason", "manual"}))
	data, err = os.ReadFile(worktreeRemovePath)
	require.NoError(t, err)
	var worktreeRemoveHook struct {
		Event        string `json:"event"`
		Reason       string `json:"reason"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Ref          string `json:"ref"`
	}
	require.NoError(t, json.Unmarshal(data, &worktreeRemoveHook))
	require.Equal(t, "worktree_remove", worktreeRemoveHook.Event)
	require.Equal(t, "manual", worktreeRemoveHook.Reason)
	require.Equal(t, "agent-1", worktreeRemoveHook.WorktreeID)
	require.Equal(t, filepath.Join(workspace, "wt"), worktreeRemoveHook.WorktreePath)
	require.Equal(t, "abc123", worktreeRemoveHook.Ref)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "cwd-changed", "--old-cwd", workspace, "--new-cwd", filepath.Join(workspace, "sub"), "--input", `{"source":"bash"}`}))
	data, err = os.ReadFile(cwdChangedPath)
	require.NoError(t, err)
	var cwdChangedHook struct {
		Event  string `json:"event"`
		Tool   string `json:"tool"`
		OldCWD string `json:"old_cwd"`
		NewCWD string `json:"new_cwd"`
		Input  string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &cwdChangedHook))
	require.Equal(t, "cwd_changed", cwdChangedHook.Event)
	require.Equal(t, filepath.Join(workspace, "sub"), cwdChangedHook.Tool)
	require.Equal(t, workspace, cwdChangedHook.OldCWD)
	require.Equal(t, filepath.Join(workspace, "sub"), cwdChangedHook.NewCWD)
	require.Contains(t, cwdChangedHook.Input, `"source"`)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "task-created", "--task-id", "task-1", "--task-kind", "agent", "--task-status", "running", "--input", `{"id":"task-1"}`}))
	data, err = os.ReadFile(taskCreatedPath)
	require.NoError(t, err)
	var taskCreatedHook struct {
		Event      string `json:"event"`
		Tool       string `json:"tool"`
		TaskID     string `json:"task_id"`
		TaskKind   string `json:"task_kind"`
		TaskStatus string `json:"task_status"`
	}
	require.NoError(t, json.Unmarshal(data, &taskCreatedHook))
	require.Equal(t, "task_created", taskCreatedHook.Event)
	require.Equal(t, "agent", taskCreatedHook.Tool)
	require.Equal(t, "task-1", taskCreatedHook.TaskID)
	require.Equal(t, "agent", taskCreatedHook.TaskKind)
	require.Equal(t, "running", taskCreatedHook.TaskStatus)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "task-completed", "--task-id", "task-1", "--task-kind", "agent", "--task-status", "stopped", "--reason", "manual"}))
	data, err = os.ReadFile(taskCompletedPath)
	require.NoError(t, err)
	var taskCompletedHook struct {
		Event      string `json:"event"`
		Reason     string `json:"reason"`
		TaskID     string `json:"task_id"`
		TaskKind   string `json:"task_kind"`
		TaskStatus string `json:"task_status"`
	}
	require.NoError(t, json.Unmarshal(data, &taskCompletedHook))
	require.Equal(t, "task_completed", taskCompletedHook.Event)
	require.Equal(t, "manual", taskCompletedHook.Reason)
	require.Equal(t, "task-1", taskCompletedHook.TaskID)
	require.Equal(t, "agent", taskCompletedHook.TaskKind)
	require.Equal(t, "stopped", taskCompletedHook.TaskStatus)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "instructions-loaded", "--path", filepath.Join(workspace, "AGENTS.md"), "--memory-type", "Project", "--load-reason", "session_start", "--glob", "*.md", "--trigger-file-path", filepath.Join(workspace, "main.go")}))
	data, err = os.ReadFile(instructionsLoadedPath)
	require.NoError(t, err)
	var instructionsLoadedHook struct {
		Event           string   `json:"event"`
		Tool            string   `json:"tool"`
		Input           string   `json:"input"`
		FilePath        string   `json:"file_path"`
		MemoryType      string   `json:"memory_type"`
		LoadReason      string   `json:"load_reason"`
		Globs           []string `json:"globs"`
		TriggerFilePath string   `json:"trigger_file_path"`
	}
	require.NoError(t, json.Unmarshal(data, &instructionsLoadedHook))
	require.Equal(t, "instructions_loaded", instructionsLoadedHook.Event)
	require.Equal(t, "session_start", instructionsLoadedHook.Tool)
	require.Equal(t, filepath.Join(workspace, "AGENTS.md"), instructionsLoadedHook.FilePath)
	require.Equal(t, "Project", instructionsLoadedHook.MemoryType)
	require.Equal(t, "session_start", instructionsLoadedHook.LoadReason)
	require.Equal(t, []string{"*.md"}, instructionsLoadedHook.Globs)
	require.Equal(t, filepath.Join(workspace, "main.go"), instructionsLoadedHook.TriggerFilePath)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "file-changed", "--path", "docs/notes.md", "--operation", "write_file", "--input", `{"path":"docs/notes.md"}`}))
	data, err = os.ReadFile(fileChangedPath)
	require.NoError(t, err)
	var fileChangedHook struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		ToolName  string `json:"tool_name"`
		Input     string `json:"input"`
		FilePath  string `json:"file_path"`
		Operation string `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(data, &fileChangedHook))
	require.Equal(t, "file_changed", fileChangedHook.Event)
	require.Equal(t, "write_file", fileChangedHook.Tool)
	require.Equal(t, "write_file", fileChangedHook.ToolName)
	require.Contains(t, fileChangedHook.Input, `"docs/notes.md"`)
	require.Equal(t, "docs/notes.md", fileChangedHook.FilePath)
	require.Equal(t, "write_file", fileChangedHook.Operation)
	require.Empty(t, errOut.String())
}

func TestPluginHooksLoadedByRunCLI(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"demo","name":"demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "hooks", "hooks.json"), []byte(`{"user_prompt_submit":["${CLAUDE_PLUGIN_ROOT}/prompt"],"pre_tool_use":[{"matcher":"bash","command":"${CLAUDE_PLUGIN_DATA}/pre"}]}`), 0o644))

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "hooks", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report hooksListReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	actualWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	pluginRootSlash := filepath.ToSlash(filepath.Join(actualWorkspace, ".codog", "plugins", "demo"))
	pluginDataSlash := filepath.ToSlash(filepath.Join(actualWorkspace, ".codog", "plugin-data", "demo"))
	require.Contains(t, report.UserPromptSubmit, pluginRootSlash+"/prompt")
	require.Len(t, report.PreToolUseCommands, 1)
	require.Equal(t, "bash", report.PreToolUseCommands[0].Matcher)
	require.Equal(t, pluginDataSlash+"/pre", report.PreToolUseCommands[0].Command)
}

func TestPluginMCPServersMergeIntoRuntimeConfig(t *testing.T) {
	workspace := t.TempDir()
	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(pluginRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{
		"id":"demo",
		"name":"demo",
		"mcp_servers":{"local":{"command":"plugin-mcp","args":["--stdio"],"env":["A=B"]}}
	}`), 0o644))

	cfg := config.Config{
		MCPServers: map[string]config.MCPServerConfig{
			"user": {Command: "user-mcp"},
		},
	}
	require.NoError(t, applyPluginMCPServers(&cfg, workspace))
	require.Equal(t, "user-mcp", cfg.MCPServers["user"].Command)
	require.Equal(t, "plugin-mcp", cfg.MCPServers["plugin:demo:local"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["plugin:demo:local"].Args)
	require.Equal(t, []string{
		"CLAUDE_PLUGIN_ROOT=" + filepath.ToSlash(pluginRoot),
		"CLAUDE_PLUGIN_DATA=" + filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo")),
		"A=B",
	}, cfg.MCPServers["plugin:demo:local"].Env)
}

func TestPermissionHooksFromPrompter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	requestPath := filepath.Join(workspace, "permission-request.json")
	deniedPath := filepath.Join(workspace, "permission-denied.json")
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:      configHome,
			PermissionMode:  "prompt",
			PermissionRules: config.PermissionRules{},
			Hooks: config.HookConfig{
				PermissionRequestCommands: []config.HookCommand{{Matcher: "bash", Command: "cat > " + shellQuote(requestPath)}},
				PermissionDeniedCommands:  []config.HookCommand{{Matcher: "bash", Command: "cat > " + shellQuote(deniedPath)}},
			},
		},
		Workspace: workspace,
		In:        strings.NewReader("n\n"),
		Err:       &errOut,
	}
	prompter := app.prompterWithAllowedTools("session-1", nil)

	err := prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"echo ok"}`))
	require.Error(t, err)

	requestPayload, err := os.ReadFile(requestPath)
	require.NoError(t, err)
	require.Contains(t, string(requestPayload), `"event":"permission_request"`)
	require.Contains(t, string(requestPayload), `"tool_name":"bash"`)
	require.Contains(t, string(requestPayload), `"tool_input":{"command":"echo ok"}`)
	deniedPayload, err := os.ReadFile(deniedPath)
	require.NoError(t, err)
	require.Contains(t, string(deniedPayload), `"event":"permission_denied"`)
	require.Contains(t, string(deniedPayload), `"reason":"user_denied"`)
	require.Contains(t, errOut.String(), "Allow? [y/N/a=always for session]")
}

func TestMCPCommandToolsCallAndResources(t *testing.T) {
	server := config.MCPServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:     []string{"CODOG_AGENT_MCP_HELPER=1"},
	}
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{AccessToken: "oauth-access-token"})
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{"test": server}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Working directory")
	require.Contains(t, out.String(), "Configured servers 1")
	require.Contains(t, out.String(), "Total entries     1")
	require.Contains(t, out.String(), "Invalid entries   0")
	require.Contains(t, out.String(), "test")
	require.Contains(t, out.String(), "stdio")
	require.Contains(t, out.String(), "ok")
	require.Contains(t, out.String(), "1 tool")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"--output-format", "json", "list"}))
	var listReport mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "mcp", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, "ok", listReport.Status)
	require.NotEmpty(t, listReport.WorkingDirectory)
	require.Equal(t, 1, listReport.ServerCount)
	require.Equal(t, "test", listReport.Servers[0].Name)
	require.Equal(t, "ok", listReport.Servers[0].Status)
	require.Equal(t, mcp.ServerSignature(server), listReport.Servers[0].Signature)
	require.Equal(t, mcp.ServerConfigHash(server), listReport.Servers[0].ConfigHash)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"list", "extra", "--json"}), "unsupported_action")
	var unsupported mcpUnsupportedActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "mcp", unsupported.Kind)
	require.Equal(t, "error", unsupported.Action)
	require.False(t, unsupported.OK)
	require.Equal(t, "unsupported_action", unsupported.ErrorKind)
	require.Equal(t, "list extra", unsupported.RequestedAction)
	require.Contains(t, unsupported.Hint, "codog mcp list")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"tools", "test"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	require.Contains(t, out.String(), `"input_schema"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"auth", "test"}))
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"tool_count": 1`)
	out.Reset()

	require.NoError(t, app.XAAIDPCommand(context.Background(), []string{"--json"}))
	var xaaReport xaaIDPReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &xaaReport))
	require.Equal(t, "mcp_compatibility", xaaReport.Kind)
	require.Equal(t, "xaa_idp", xaaReport.Action)
	require.Equal(t, "ok", xaaReport.Status)
	require.Equal(t, 1, xaaReport.ServerCount)
	require.Contains(t, xaaReport.ConfiguredServers, "test")
	require.Len(t, xaaReport.AuthStatuses, 1)
	require.Equal(t, "test", xaaReport.AuthStatuses[0].Server)
	require.Equal(t, "ok", xaaReport.AuthStatuses[0].Status)
	require.Equal(t, 1, xaaReport.AuthStatuses[0].ToolCount)
	require.Equal(t, 1, xaaReport.AuthStatuses[0].ResourceCount)
	require.True(t, xaaReport.ProviderConfigured)
	require.True(t, xaaReport.OAuthReady)
	require.NotNil(t, xaaReport.OAuthStatus)
	require.True(t, xaaReport.OAuthStatus.TokenPresent)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp tools test", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"call", "test", "echo", `{"text":"hi"}`}))
	require.Contains(t, out.String(), `"text": "hi"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resources", "test"}))
	require.Contains(t, out.String(), "codog://note")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resource-templates", "test"}))
	require.Contains(t, out.String(), "codog://notes/{name}")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"read", "test", "codog://note"}))
	require.Contains(t, out.String(), "note body")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompts", "test"}))
	require.Contains(t, out.String(), `"name": "review"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompt", "test", "review", `{"topic":"hooks"}`}))
	require.Contains(t, out.String(), "Review hooks")
}

func TestMCPAddCommandCompatibility(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "addCommand", "demo", "echo", "ok", "--env", "A=B"}, config.FlagOverrides{})
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
	require.Contains(t, out.String(), "No valid MCP servers configured.")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"--output-format", "json"}))
	require.Contains(t, out.String(), `"kind": "mcp"`)
	require.Contains(t, out.String(), `"server_count": 0`)
	require.Contains(t, out.String(), `"working_directory":`)
}

func TestMCPListTextReportsInvalidServers(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{"missing": {}}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Status           degraded")
	require.Contains(t, out.String(), "Configured servers 0")
	require.Contains(t, out.String(), "Total entries     1")
	require.Contains(t, out.String(), "Invalid entries   1")
	require.Contains(t, out.String(), "missing")
	require.Contains(t, out.String(), "missing_command")
	require.Contains(t, out.String(), "Invalid MCP servers")
	require.Contains(t, out.String(), "- missing: missing command")
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
	require.Equal(t, "codog mcp tools SERVER", report.Usage.DirectCLI)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"tools", "demo"}), "no_servers_configured")
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Action           tools")
	require.Contains(t, out.String(), "Error            no_servers_configured")
	require.Contains(t, out.String(), "Usage            codog mcp tools SERVER")
	out.Reset()

	app.Config.MCPServers = map[string]config.MCPServerConfig{"configured": {Command: "demo-server"}}
	require.ErrorContains(t, app.MCP(context.Background(), []string{"resources", "missing", "--json"}), "server_not_found")
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "resources", report.Action)
	require.Equal(t, "server_not_found", report.ErrorKind)
	require.Equal(t, "missing", report.ServerName)
	require.Equal(t, []string{"configured"}, report.AvailableServers)
	require.Equal(t, "codog mcp resources SERVER", report.Usage.DirectCLI)
	out.Reset()

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
	require.Equal(t, "/mcp [list|show <server>|help]", report.Usage.SlashCommand)
	require.Equal(t, "codog mcp [list|show <server>|help]", report.Usage.DirectCLI)
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
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp help", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "/mcp [list|show <server>|help]")
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"info", "missing", "--json"}), "unsupported_action")
	var unsupported mcpUnsupportedActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "mcp", unsupported.Kind)
	require.Equal(t, "error", unsupported.Action)
	require.False(t, unsupported.OK)
	require.Equal(t, "unsupported_action", unsupported.ErrorKind)
	require.Equal(t, "info missing", unsupported.RequestedAction)
	require.Contains(t, unsupported.Hint, "mcp show")
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"describe", "missing", "--json"}), "unsupported_action")
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "describe missing", unsupported.RequestedAction)
	require.Contains(t, unsupported.Hint, "mcp show")
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

	require.NoError(t, app.MCP(context.Background(), []string{"add", "demo", "demo-server", "--env", "A=B", "--env=C=D", "arg1", "arg2"}))
	require.Contains(t, out.String(), `"action": "add"`)
	require.Contains(t, out.String(), `"name": "demo"`)
	require.Equal(t, config.MCPServerConfig{Command: "demo-server", Args: []string{"arg1", "arg2"}, Env: []string{"A=B", "C=D"}}, app.Config.MCPServers["demo"])
	configData, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `"mcp_servers"`)
	require.Contains(t, string(configData), `"demo-server"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"show", "demo", "--json"}))
	require.Contains(t, out.String(), `"action": "show"`)
	require.Contains(t, out.String(), `"command": "demo-server"`)
	require.Contains(t, out.String(), `"signature": "stdio:[demo-server|arg1|arg2]"`)
	require.Contains(t, out.String(), `"config_hash": "`)
	require.Contains(t, out.String(), `"args_count": 2`)
	require.Contains(t, out.String(), `"env_keys": [`)
	require.NotContains(t, out.String(), `"A=B"`)
	require.NotContains(t, out.String(), `"C=D"`)
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

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"kind": "bridge_kick"`)
	require.Contains(t, out.String(), `"status": "ok"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bridge-kick poll 404", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bridge Kick")
	require.Contains(t, out.String(), "Status           ok")
	require.Contains(t, out.String(), "Recorded         poll 404")
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"action": "poll"`)
	require.Contains(t, out.String(), `"404"`)
	out.Reset()

	require.NoError(t, app.BridgeKick([]string{"clear", "--json"}))
	require.Contains(t, out.String(), `"cleared": true`)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
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
	out.Reset()

	require.NoError(t, app.PromptWithOutput(context.Background(), "stream prompt", config.FlagOverrides{SessionID: "stream-session"}, "stream-json"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	var firstEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &firstEvent))
	require.Equal(t, "start", firstEvent["type"])
	var deltaEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &deltaEvent))
	require.Equal(t, "assistant_delta", deltaEvent["type"])
	var resultEvent map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &resultEvent))
	require.Equal(t, "result", resultEvent["type"])
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

	require.NoError(t, app.Prompt(context.Background(), "summarize @notes.md", config.FlagOverrides{SessionID: "prompt-refs"}))
	loaded, err := app.Sessions.Open("prompt-refs")
	require.NoError(t, err)
	require.Len(t, loaded.Messages, 2)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "<codog_file_references>")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "note body")
	history, err := app.Sessions.PromptHistory("prompt-refs")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "summarize @notes.md", history[0].Text)
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

	require.NoError(t, app.Skills([]string{"sources", "--json"}))
	var sourceReport struct {
		Kind      string                 `json:"kind"`
		Action    string                 `json:"action"`
		Status    string                 `json:"status"`
		RootCount int                    `json:"root_count"`
		Roots     []skills.DiscoveryRoot `json:"roots"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.String()), &sourceReport))
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
	require.NoError(t, json.Unmarshal([]byte(out.String()), &report))
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

func TestSkillsUnsupportedActionReportsTypedError(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
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
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "skills", "enable"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "skills", report.Kind)
	require.Equal(t, "enable", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "unsupported_skills_action", report.ErrorKind)
	require.Contains(t, report.Message, "unsupported skills action")
	require.Contains(t, report.Hint, "codog skills list")
	require.Contains(t, report.Hint, "codog skills add")
	require.Contains(t, report.Hint, "codog skills help")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "skills", "enable"}, config.FlagOverrides{})
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
	require.Contains(t, help.Usage, "sources")
	require.Contains(t, help.Usage, "help")
	require.Contains(t, help.Help, "roots")
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

	require.NoError(t, app.Templates([]string{"show", "review"}))
	require.Contains(t, out.String(), "Review {{target}} as {{role}}.")
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "review", "--var", "target=auth", "role=reviewer"}))
	require.Equal(t, "Review auth as reviewer.\n", out.String())
	out.Reset()

	require.NoError(t, app.Templates([]string{"apply", "plan", "--json", "--var=topic=tests"}))
	require.Contains(t, out.String(), `"kind": "template_apply"`)
	require.Contains(t, out.String(), `"rendered": "Plan tests."`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/templates apply plan topic=release", &session.Session{ID: "session"}))
	require.Equal(t, "Plan release.\n", out.String())
	require.Empty(t, errOut.String())
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

	require.NoError(t, app.Commands([]string{"show", "fix", "--json"}))
	require.Contains(t, out.String(), `"source": "workspace"`)
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

	require.NoError(t, app.Commands([]string{"run", "fix", "bug", "123"}))
	require.Equal(t, "Codog fix bug 123\n", out.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/commands run review file.go", &session.Session{ID: "session"}))
	require.Equal(t, "Review file.go\n", out.String())
	require.Empty(t, errOut.String())
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

	require.True(t, app.handleSlash(context.Background(), "/export --format html", sess))
	require.Contains(t, errOut.String(), "exported session source")
	data, err = os.ReadFile(filepath.Join(workspace, "slash-export.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
}

func TestShareCommandAndSlashWritesLocalArtifact(t *testing.T) {
	workspace := t.TempDir()
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
	sharedJSON := filepath.Join(workspace, ".codog", "share", "source.json")
	data, err := os.ReadFile(sharedJSON)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
	out.Reset()

	require.NoError(t, app.Share([]string{"--session", "source", "--format=html", "html-share"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), "Shared session source")
	data, err = os.ReadFile(filepath.Join(workspace, "html-share", "source.html"))
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/share shared", sess))
	require.Empty(t, errOut.String())
	require.Contains(t, out.String(), "Shared session source")
	sharedMarkdown := filepath.Join(workspace, "shared", "source.md")
	data, err = os.ReadFile(sharedMarkdown)
	require.NoError(t, err)
	require.Contains(t, string(data), "share me")
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

func TestBuildAgentCommandQuotesPrompt(t *testing.T) {
	command := buildAgentCommand("/tmp/codog", agentdefs.Definition{
		Name:   "reviewer",
		Model:  "mock-model",
		Prompt: "review carefully",
	}, "check '$HOME'")

	require.Contains(t, command, "'/tmp/codog'")
	require.Contains(t, command, "--model 'mock-model'")
	require.Contains(t, command, "prompt 'review carefully")
	require.Contains(t, command, "'\"'\"'$HOME'\"'\"'")
}

func TestAgentsCommandAcceptsOutputFormatFlags(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "planner.json"), []byte(`{"name":"planner","description":"plans work","prompt":"plan"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","description":"reviews code","prompt":"review"}`), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.AgentsWithOverrides(nil, config.FlagOverrides{}))
	require.True(t, strings.HasPrefix(strings.TrimSpace(out.String()), "["))
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"--output-format", "json", "list", "review"}, config.FlagOverrides{}))
	var listReport agentsListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "agents", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, 1, listReport.Count)
	require.Equal(t, "reviewer", listReport.Agents[0].Name)
	out.Reset()

	require.NoError(t, app.AgentsWithOverrides([]string{"show", "planner", "--json"}, config.FlagOverrides{}))
	var showReport agentShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "planner", showReport.Agent.Name)
}

func TestAgentsRunEmitsSubagentStartHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "reviewer.json"), []byte(`{"name":"reviewer","model":"agent-model","prompt":"Base review instructions"}`), 0o644))
	received := make(chan struct {
		Event     string `json:"event"`
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Event     string `json:"event"`
			AgentID   string `json:"agent_id"`
			AgentType string `json:"agent_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				SubagentStartCommands: []config.HookCommand{
					{Matcher: "reviewer", Type: "http", URL: server.URL},
				},
			},
		},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.AgentsWithOverrides([]string{"run", "reviewer", "check auth"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"agent": "reviewer"`)
	require.Contains(t, out.String(), `"kind": "agent"`)
	select {
	case payload := <-received:
		require.Equal(t, "subagent_start", payload.Event)
		require.NotEmpty(t, payload.AgentID)
		require.Equal(t, "reviewer", payload.AgentType)
	case <-time.After(2 * time.Second):
		t.Fatal("subagent start hook was not called")
	}
	require.Empty(t, errOut.String())
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	return workspace
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	runErr := fn()
	os.Stdout = original
	require.NoError(t, writer.Close())
	data, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	require.NoError(t, reader.Close())
	return string(data), runErr
}

func TestParseAgentRunArgs(t *testing.T) {
	req, err := parseAgentRunArgs([]string{"--worktree", "reviewer", "check", "this"})
	require.NoError(t, err)
	require.True(t, req.Worktree)
	require.Equal(t, "reviewer", req.Name)
	require.Equal(t, "check this", req.Prompt)

	_, err = parseAgentRunArgs([]string{"--worktree", "reviewer"})
	require.Error(t, err)
}

func TestBackgroundWatchCommandOutputsJSONLEvents(t *testing.T) {
	configHome := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.Run("echo cli-watch", t.TempDir())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "cli-watch")
	}, 2*time.Second, 50*time.Millisecond)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.Background([]string{"watch", task.ID}))
	require.Contains(t, out.String(), `"type":"status"`)
	require.Contains(t, out.String(), `"type":"log"`)
	require.Contains(t, out.String(), "cli-watch")
}

func TestBackgroundRunAttachesSessionFromOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"run", "echo", "attached"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"list"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
}

func TestBackgroundHeartbeatAndBoardCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("sleep 5", workspace, background.RunOptions{Kind: "agent", SessionID: "session-1"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop(task.ID) })

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: workspace,
		Out:       &out,
	}

	observedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, app.BackgroundWithOverrides([]string{"heartbeat", task.ID, "--status", "running", "--observed-at", observedAt.Format(time.RFC3339)}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"heartbeat": {`)
	require.Contains(t, out.String(), `"transport_alive": true`)
	out.Reset()

	require.NoError(t, app.BackgroundWithOverrides([]string{"board", "3600"}, config.FlagOverrides{}))
	var board background.LaneBoard
	require.NoError(t, json.Unmarshal(out.Bytes(), &board))
	require.Len(t, board.Active, 1)
	require.Equal(t, task.ID, board.Active[0].TaskID)
	require.Equal(t, background.LaneFreshnessHealthy, board.Active[0].Freshness)
}

func TestBackgroundRunEmitsNotificationHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	received := make(chan struct {
		Event            string `json:"event"`
		Message          string `json:"message"`
		Title            string `json:"title"`
		NotificationType string `json:"notification_type"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Event            string `json:"event"`
			Message          string `json:"message"`
			Title            string `json:"title"`
			NotificationType string `json:"notification_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				NotificationCommands: []config.HookCommand{
					{Matcher: "background_task_started", Type: "http", URL: server.URL},
				},
			},
		},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"run", "echo", "attached"}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"session_id": "session-1"`)
	select {
	case payload := <-received:
		require.Equal(t, "notification", payload.Event)
		require.Equal(t, "Background task started", payload.Title)
		require.Equal(t, "background_task_started", payload.NotificationType)
		require.Contains(t, payload.Message, "started")
		require.Contains(t, payload.Message, "echo attached")
	case <-time.After(2 * time.Second):
		t.Fatal("notification hook was not called")
	}
	require.Empty(t, errOut.String())
}

func TestBackgroundStopEmitsSubagentStopHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := background.NewStore(configHome)
	task, err := store.RunWithOptions("printf 'final line\\n'; sleep 5", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-1"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 4096)
		return err == nil && strings.Contains(logs, "final line")
	}, 2*time.Second, 50*time.Millisecond)
	received := make(chan struct {
		Event          string `json:"event"`
		AgentID        string `json:"agent_id"`
		AgentType      string `json:"agent_type"`
		TranscriptPath string `json:"agent_transcript_path"`
		LastAssistant  string `json:"last_assistant_message"`
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Event          string `json:"event"`
			AgentID        string `json:"agent_id"`
			AgentType      string `json:"agent_type"`
			TranscriptPath string `json:"agent_transcript_path"`
			LastAssistant  string `json:"last_assistant_message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				SubagentStopCommands: []config.HookCommand{
					{Matcher: "reviewer", Type: "http", URL: server.URL},
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.BackgroundWithOverrides([]string{"stop", task.ID}, config.FlagOverrides{SessionID: "session-1"}))
	require.Contains(t, out.String(), `"status": "stopped"`)
	select {
	case payload := <-received:
		require.Equal(t, "subagent_stop", payload.Event)
		require.Equal(t, task.ID, payload.AgentID)
		require.Equal(t, "reviewer", payload.AgentType)
		require.Equal(t, task.LogPath, payload.TranscriptPath)
		require.Equal(t, "final line", payload.LastAssistant)
	case <-time.After(2 * time.Second):
		t.Fatal("subagent stop hook was not called")
	}
	require.Empty(t, errOut.String())
}

func TestBackgroundSlashAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  session.NewStore(configHome),
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/bashes list", &session.Session{ID: "session-1"}))
	require.Equal(t, "[]\n", out.String())
	require.Empty(t, errOut.String())
}

func TestCronCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: configHome},
		Workspace:  workspace,
		Executable: script,
		Out:        &out,
		Err:        &errOut,
	}

	require.NoError(t, app.Cron([]string{"create", "0 9 * * 1", "review", "weekly", "--description", "weekly review", "--json"}))
	var created cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &created))
	require.Equal(t, "cron", created.Kind)
	require.Equal(t, "create", created.Action)
	require.NotNil(t, created.Entry)
	require.Equal(t, "0 9 * * 1", created.Entry.Schedule)
	require.Equal(t, "review weekly", created.Entry.Prompt)
	require.Equal(t, "weekly review", created.Entry.Description)
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var listed cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed))
	require.Equal(t, 1, listed.Count)
	require.Len(t, listed.Entries, 1)
	require.Equal(t, created.Entry.ID, listed.Entries[0].ID)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/cron list --json", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), `"kind": "cron"`)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.Cron([]string{"create", "@every 1h", "check", "due", "--json"}))
	var dueCreated cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dueCreated))
	require.NotNil(t, dueCreated.Entry)
	out.Reset()

	now := "2026-06-30T09:30:00Z"
	require.NoError(t, app.Cron([]string{"due", "--now", now, "--json"}))
	var due cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &due))
	require.Equal(t, "due", due.Action)
	require.Equal(t, 1, due.Count)
	require.Equal(t, dueCreated.Entry.ID, due.Entries[0].ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"run-due", "--now", now, "--json"}))
	var runDue cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &runDue))
	require.Equal(t, "run-due", runDue.Action)
	require.Equal(t, 1, runDue.Count)
	require.Len(t, runDue.Tasks, 1)
	require.Len(t, runDue.Entries, 1)
	require.Equal(t, 1, runDue.Entries[0].RunCount)
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var afterRun cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &afterRun))
	require.Len(t, afterRun.Entries, 2)
	var ran cron.Entry
	for _, entry := range afterRun.Entries {
		if entry.ID == dueCreated.Entry.ID {
			ran = entry
		}
	}
	require.Equal(t, 1, ran.RunCount)
	require.NotNil(t, ran.LastRunAt)
	out.Reset()

	require.NoError(t, app.Cron([]string{"delete", created.Entry.ID, "--json"}))
	var deleted cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleted))
	require.Equal(t, "delete", deleted.Action)
	require.Equal(t, created.Entry.ID, deleted.Entry.ID)
	out.Reset()

	require.NoError(t, app.Cron([]string{"delete", dueCreated.Entry.ID, "--json"}))
	out.Reset()

	require.NoError(t, app.Cron([]string{"list", "--json"}))
	var empty cronCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &empty))
	require.Zero(t, empty.Count)
	require.Empty(t, empty.Entries)
}

func TestTeamCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	configHome := t.TempDir()
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho team-log \"$@\"\nsleep 5\n"), 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:     config.Config{ConfigHome: configHome},
		Workspace:  workspace,
		Executable: script,
		Out:        &out,
		Err:        &errOut,
	}

	require.NoError(t, app.Team([]string{"create", "reviewers", "--task", "auth=check auth", "--task", "check tests", "--json"}))
	var created teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &created))
	require.Equal(t, "team", created.Kind)
	require.Equal(t, "create", created.Action)
	require.NotNil(t, created.Team)
	require.Equal(t, "reviewers", created.Team.Name)
	require.Len(t, created.Team.Tasks, 2)
	require.NotEmpty(t, created.Team.Tasks[0].TaskID)
	require.Len(t, created.Tasks, 2)
	out.Reset()

	require.NoError(t, app.Team([]string{"status", created.Team.ID, "--json"}))
	var status teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "status", status.Action)
	require.Equal(t, created.Team.ID, status.Team.ID)
	require.Len(t, status.Tasks, 2)
	require.Equal(t, "running", status.Team.Status)
	out.Reset()

	require.Eventually(t, func() bool {
		out.Reset()
		require.NoError(t, app.Team([]string{"logs", created.Team.ID, "--bytes", "4096", "--json"}))
		return strings.Contains(out.String(), "team-log")
	}, 2*time.Second, 20*time.Millisecond)
	var logs teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &logs))
	require.Equal(t, "logs", logs.Action)
	require.Len(t, logs.Logs, 2)
	require.Contains(t, logs.Logs[0].Log+logs.Logs[1].Log, "team-log")
	out.Reset()

	require.NoError(t, app.Team([]string{"watch", created.Team.ID, "--max-events", "2", "--json"}))
	require.Contains(t, out.String(), `"kind":"team_watch"`)
	require.Contains(t, out.String(), `"type":"status"`)
	out.Reset()

	require.NoError(t, app.Team([]string{"list", "--json"}))
	var listed teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed))
	require.Equal(t, 1, listed.Count)
	require.Equal(t, created.Team.ID, listed.Teams[0].ID)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/team list --json", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), `"kind": "team"`)
	require.Empty(t, errOut.String())
	out.Reset()

	require.NoError(t, app.Team([]string{"get", created.Team.ID, "--json"}))
	var fetched teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &fetched))
	require.Equal(t, created.Team.ID, fetched.Team.ID)
	out.Reset()

	require.NoError(t, app.Team([]string{"delete", created.Team.ID, "--json"}))
	var deleted teamCommandReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleted))
	require.Equal(t, "delete", deleted.Action)
	require.Equal(t, "deleted", deleted.Team.Status)
	require.Len(t, deleted.StoppedTasks, 2)
}

func TestParseBackgroundRunArgsWithRestartPolicy(t *testing.T) {
	command, options, err := parseBackgroundRunArgs([]string{"--restart=always", "--restart-limit", "2", "--restart-delay", "5", "echo", "restart"})
	require.NoError(t, err)
	require.Equal(t, "echo restart", command)
	require.NotNil(t, options.RestartPolicy)
	require.True(t, options.RestartPolicy.Enabled)
	require.Equal(t, "always", options.RestartPolicy.Mode)
	require.Equal(t, 2, options.RestartPolicy.MaxAttempts)
	require.Equal(t, 5, options.RestartPolicy.DelaySeconds)

	_, _, err = parseBackgroundRunArgs([]string{"--restart=never", "echo"})
	require.Error(t, err)
}

func TestCodeIntelLSPCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: t.TempDir(),
		Out:       &out,
	}

	require.NoError(t, app.CodeIntel([]string{"lsp", "discover"}))
	require.Contains(t, out.String(), `"language": "go"`)
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "start", "go", "sleep", "30"}))
	require.Contains(t, out.String(), `"language": "go"`)
	require.Contains(t, out.String(), `"status": "running"`)
	t.Cleanup(func() { _ = app.CodeIntel([]string{"lsp", "stop", "go"}) })
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "list"}))
	require.Contains(t, out.String(), `"language": "go"`)
	out.Reset()

	require.NoError(t, app.CodeIntel([]string{"lsp", "stop", "go"}))
	require.Contains(t, out.String(), `"status": "stopped"`)
}

func TestOAuthTokenCommands(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	require.NoError(t, app.OAuth([]string{"token", "save", "access-token-1234", "refresh-token-1234", expiresAt}))
	require.Contains(t, out.String(), `"access_token": "acce...1234"`)
	require.NotContains(t, out.String(), "access-token-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "show"}))
	require.Contains(t, out.String(), `"expired": false`)

	out.Reset()
	require.NoError(t, app.OAuth([]string{"token", "delete"}))
	require.Contains(t, out.String(), `"deleted":true`)
}

func TestLoginLogoutAliases(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
		Err:    &errOut,
	}

	err := app.Login(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth browser login PROFILE")

	err = app.Login([]string{"device"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth device")

	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-token-1234"})
	require.NoError(t, err)
	require.NoError(t, app.Logout(nil))
	require.Contains(t, out.String(), `"deleted": true`)
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)

	out.Reset()
	require.Empty(t, errOut.String())
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "slash-access-1234"})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/logout", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.Empty(t, errOut.String())
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
}

func TestOAuthDiscoverCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.OAuth([]string{"discover", server.URL}))
	require.Contains(t, out.String(), `"authorization_endpoint": "https://auth.example/authorize"`)
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
}

func TestOAuthDeviceCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"device_authorization_endpoint":"` + server.URL + `/device","token_endpoint":"` + server.URL + `/token"}`))
		case "/device":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			require.Equal(t, "profile", r.Form.Get("scope"))
			_, _ = w.Write([]byte(`{"device_code":"device-1","user_code":"ABCD-EFGH","verification_uri":"` + server.URL + `/verify","expires_in":600,"interval":1}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, oauth.DeviceCodeGrantType, r.Form.Get("grant_type"))
			require.Equal(t, "device-1", r.Form.Get("device_code"))
			_, _ = w.Write([]byte(`{"access_token":"device-access-1234","refresh_token":"device-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)
	require.NoError(t, app.OAuth([]string{"device", "start", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "start", "default"}))
	require.Contains(t, out.String(), `"user_code": "ABCD-EFGH"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", server.URL, "client-1", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"device", "poll", "default", "device-1"}))
	require.Contains(t, out.String(), `"access_token": "devi...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "device-access-1234", loaded.AccessToken)
}

func TestOAuthProviderCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"https://auth.example/authorize","token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: t.TempDir()},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"provider", "save", "default", server.URL, "client-1", "profile"}))
	require.Contains(t, out.String(), `"name": "default"`)
	require.Contains(t, out.String(), `"client_id": "client-1"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "list"}))
	require.Contains(t, out.String(), `"name": "default"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "show", "default"}))
	require.Contains(t, out.String(), `"token_endpoint": "https://auth.example/token"`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"provider", "delete", "default"}))
	require.Contains(t, out.String(), `"deleted": true`)
}

func TestProfileCommandSetsShowsAndClearsActiveOAuthProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/oauth-authorization-server", r.URL.Path)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"token_endpoint":"https://auth.example/token"}`))
	}))
	defer server.Close()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-default", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-work", []string{"profile", "email"})
	require.NoError(t, err)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "profile", "set", "work", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "profile"`)
	require.Contains(t, out, `"action": "set"`)
	require.Contains(t, out, `"active_profile": "work"`)
	require.Contains(t, out, `"client_id": "client-work"`)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"oauth_profile": "work"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "profile", "show"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"active_profile": "work"`)
	require.Contains(t, out, `"name": "work"`)
	require.True(t, commandAcceptsGlobalOutputFormat("profile"))

	var buffer bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, OAuthProfile: "work"},
		Out:    &buffer,
		Err:    &errOut,
	}
	require.True(t, app.handleSlash(context.Background(), "/profile clear --path "+configPath, &session.Session{ID: "session"}))
	require.Contains(t, buffer.String(), "Profile")
	require.Empty(t, errOut.String())
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"oauth_profile"`)
}

func TestProvidersStatusRedactsAuth(t *testing.T) {
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{AccessToken: "stored-access-token"})
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      "claude-sonnet-4-5",
			MaxTokens:  4096,
			MaxTurns:   8,
			APIKey:     "api-key-secret",
			AuthToken:  "stored-access-token",
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"name": "anthropic"`)
	require.Contains(t, out.String(), `"stored_oauth"`)
	require.Contains(t, out.String(), `"api_key": true`)
	require.NotContains(t, out.String(), "api-key-secret")
	require.NotContains(t, out.String(), "stored-access-token")
}

func TestProvidersDegradeOnMalformedConfigFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report providersReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "providers", report.Kind)
	require.Equal(t, "status", report.Action)
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, "anthropic", report.Active.Name)
	require.Equal(t, config.DefaultModel, report.Active.Model)
	require.NotNil(t, report.ConfigLoadError)
	require.Contains(t, *report.ConfigLoadError, "broken.json")
	require.Contains(t, *report.ConfigLoadError, "unexpected end of JSON input")
	require.Equal(t, "config_load_failed", report.ConfigLoadErrorKind)
	require.NotNil(t, report.Active.ConfigLoadError)
	require.Equal(t, report.ConfigLoadErrorKind, report.Active.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "degraded", report.Status)
	require.NotNil(t, report.ConfigLoadError)
	require.True(t, commandAcceptsGlobalOutputFormat("providers"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers", "show", "current"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var active activeProviderReport
	require.NoError(t, json.Unmarshal([]byte(out), &active))
	require.Equal(t, "anthropic", active.Name)
	require.NotNil(t, active.ConfigLoadError)
	require.Equal(t, "config_load_failed", active.ConfigLoadErrorKind)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "text", "providers"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Status: degraded")
	require.Contains(t, out, "Config load: degraded")
	require.Contains(t, out, "broken.json")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "providers", "set", "openai"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "config_load_failed", errorReport.ErrorKind)
	require.NotContains(t, out, "config_load_error")
}

func TestProvidersSetWritesConfig(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "provider.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			BaseURL:    config.DefaultBaseURL,
			Model:      config.DefaultModel,
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"set", "custom", "--base-url", "http://127.0.0.1:8080", "--model", "claude-local", "--path", configPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "custom"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "http://127.0.0.1:8080"`)
	require.Contains(t, string(data), `"model": "claude-local"`)
	out.Reset()

	openAIPath := filepath.Join(configHome, "openai-provider.json")
	require.NoError(t, app.Providers([]string{"set", "openai", "--path", openAIPath, "--json"}))
	require.Contains(t, out.String(), `"provider": "openai"`)
	data, err = os.ReadFile(openAIPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"base_url": "https://api.openai.com/v1"`)
	require.Contains(t, string(data), `"model": "openai/gpt-4o-mini"`)
}

func TestProvidersShowCurrent(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			BaseURL:    "https://provider.example",
			Model:      "claude-compatible",
			MaxTokens:  2048,
			MaxTurns:   4,
		},
		Out: &out,
	}

	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "custom"`)
	require.Contains(t, out.String(), `"base_url": "https://provider.example"`)
	require.Contains(t, out.String(), `"model": "claude-compatible"`)
	out.Reset()

	app.Config.BaseURL = "https://api.openai.com/v1"
	app.Config.Model = "openai/gpt-4o-mini"
	require.NoError(t, app.Providers([]string{"show", "current", "--json"}))
	require.Contains(t, out.String(), `"name": "openai"`)
	require.Contains(t, out.String(), `"protocol": "openai-compatible"`)
	require.Contains(t, out.String(), `"model": "openai/gpt-4o-mini"`)
}

func TestOAuthBrowserCommands(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			require.Equal(t, "code-1", r.Form.Get("code"))
			require.Equal(t, "verifier-1", r.Form.Get("code_verifier"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"browser-access-1234","refresh_token":"browser-refresh-1234","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", []string{"profile"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"browser", "start", "default", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"authorization_url":`)
	require.Contains(t, out.String(), "client_id=client-1")
	require.Contains(t, out.String(), "scope=profile")
	require.Contains(t, out.String(), `"code_verifier":`)
	out.Reset()

	require.NoError(t, app.OAuth([]string{"browser", "exchange", "default", "code-1", "verifier-1", "http://127.0.0.1:9999/oauth/callback"}))
	require.Contains(t, out.String(), `"access_token": "brow...1234"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "browser-access-1234", loaded.AccessToken)
}

func TestOAuthTokenRefreshCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "refresh"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	loaded, err := oauth.LoadToken(configHome)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", loaded.AccessToken)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, app.OAuthRefresh([]string{"--json"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
	out.Reset()

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/oauth-refresh", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"access_token": "refr...cess"`)
}

func TestOAuthStatusCommand(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "status-access-1234",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"status"}))
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.Contains(t, out.String(), `"access_token": "stat...1234"`)
	require.Contains(t, out.String(), `"can_refresh": true`)
	require.Contains(t, out.String(), `"ready": true`)
	require.NotContains(t, out.String(), "status-access-1234")

	out.Reset()
	require.NoError(t, app.OAuth([]string{"status", "--json"}))
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.NotContains(t, out.String(), "status-access-1234")

	out.Reset()
	require.True(t, app.handleSlash(context.Background(), "/oauth status --json", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"profile_name": "default"`)
	require.NotContains(t, out.String(), "status-access-1234")
}

func TestOAuthTokenRevokeAndLogoutCommands(t *testing.T) {
	server, revoked := oauthRevocationTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{AccessToken: "access-1", RefreshToken: "refresh-1"})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &out,
	}
	require.NoError(t, app.OAuth([]string{"token", "revoke", "default", "refresh"}))
	require.Contains(t, out.String(), `"revoked": true`)
	require.Contains(t, out.String(), `"token": "refresh"`)
	require.Contains(t, *revoked, "refresh_token:refresh-1")
	out.Reset()

	require.NoError(t, app.OAuth([]string{"logout"}))
	require.Contains(t, out.String(), `"deleted": true`)
	require.Contains(t, out.String(), `"access_revoked": true`)
	require.Contains(t, *revoked, "access_token:access-1")
	_, err = oauth.LoadToken(configHome)
	require.ErrorIs(t, err, oauth.ErrNoToken)
}

func TestApplyStoredOAuthToken(t *testing.T) {
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken: "stored-token",
		ExpiresAt:   now.Add(time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "stored-token", cfg.AuthToken)

	cfg = config.Config{ConfigHome: configHome, AuthToken: "explicit-token"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "explicit-token", cfg.AuthToken)
}

func TestApplyStoredOAuthTokenRefreshesExpiredToken(t *testing.T) {
	server := oauthRefreshTestServer(t)
	defer server.Close()
	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-1", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "refreshed-access", cfg.AuthToken)
}

func TestApplyStoredOAuthTokenUsesSelectedProfile(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			switch r.Form.Get("client_id") {
			case "client-work":
				_, _ = w.Write([]byte(`{"access_token":"work-access","refresh_token":"refresh-2","expires_in":3600}`))
			default:
				_, _ = w.Write([]byte(`{"access_token":"default-access","refresh_token":"refresh-2","expires_in":3600}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configHome := t.TempDir()
	now := time.Now().UTC()
	_, err := oauth.SaveProviderProfile(context.Background(), configHome, "default", server.URL, "client-default", nil)
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "work", server.URL, "client-work", nil)
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(-time.Hour),
	})
	require.NoError(t, err)

	cfg := config.Config{ConfigHome: configHome, OAuthProfile: "work"}
	applyStoredOAuthToken(&cfg, now)
	require.Equal(t, "work-access", cfg.AuthToken)
}

func oauthRefreshTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"token_endpoint":"` + server.URL + `/token"}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			require.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			require.Equal(t, "client-1", r.Form.Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"refreshed-access","refresh_token":"refresh-2","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func oauthRevocationTestServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	revoked := []string{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"revocation_endpoint":"` + server.URL + `/revoke","token_endpoint":"` + server.URL + `/token"}`))
		case "/revoke":
			require.NoError(t, r.ParseForm())
			revoked = append(revoked, r.Form.Get("token_type_hint")+":"+r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return server, &revoked
}

func TestMarketplaceAcceptsOutputFormatFlags(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo"}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out}
	require.NoError(t, app.Marketplace(nil))
	require.True(t, strings.HasPrefix(strings.TrimSpace(out.String()), "["))
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"--output-format", "json", "list"}))
	var report pluginsListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "plugin", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.Summary.Total)
	require.Equal(t, 1, report.Summary.Enabled)
	require.Equal(t, 0, report.Summary.Disabled)
	require.Nil(t, report.ConfigLoadError)
	require.Empty(t, report.LoadFailures)
	require.Equal(t, "demo", report.Plugins[0].ID)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"list", "--json"}))
	require.Contains(t, out.String(), `"summary"`)
}

func TestMarketplaceSourcesManageConfigAndBrowse(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	index := plugins.MarketplaceIndex{
		Name: "Test Marketplace",
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", Name: "Demo", Version: "0.1.0", URL: "demo.zip", SHA256: strings.Repeat("a", 64)},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/index.json", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(index))
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"sources", "add", indexURL, "public-key", "--target", "project", "--json"}))
	var addReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &addReport))
	require.Equal(t, "marketplace", addReport.Kind)
	require.Equal(t, "sources_add", addReport.Action)
	require.Equal(t, "ok", addReport.Status)
	require.Equal(t, "project", addReport.Target)
	require.True(t, addReport.Added)
	require.Equal(t, indexURL, addReport.URL)
	require.Equal(t, filepath.Join(workspace, ".codog.json"), addReport.Path)
	require.Len(t, addReport.Sources, 1)
	require.True(t, addReport.Sources[0].PublicKeyConfigured)
	require.Equal(t, []string{indexURL}, app.Config.Future.PluginMarketplaces)
	require.Equal(t, "public-key", app.Config.Future.PluginMarketplaceKeys[indexURL])
	configData, err := os.ReadFile(filepath.Join(workspace, ".codog.json"))
	require.NoError(t, err)
	require.Contains(t, string(configData), `"plugin_marketplaces"`)
	require.Contains(t, string(configData), indexURL)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"sources", "--json"}))
	var listReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "sources_list", listReport.Action)
	require.Len(t, listReport.Sources, 1)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"settings", "--json"}))
	var settingsReport marketplaceSettingsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &settingsReport))
	require.Equal(t, "settings", settingsReport.Action)
	require.Equal(t, plugins.Root(workspace), settingsReport.PluginRoot)
	require.Len(t, settingsReport.Sources, 1)
	out.Reset()

	app.Config.Future.PluginMarketplaceKeys = nil
	require.NoError(t, app.Marketplace([]string{"browse"}))
	var indexes []plugins.MarketplaceIndex
	require.NoError(t, json.Unmarshal(out.Bytes(), &indexes))
	require.Len(t, indexes, 1)
	require.Equal(t, "demo", indexes[0].Plugins[0].ID)
	out.Reset()

	require.NoError(t, app.Marketplace([]string{"sources", "remove", indexURL, "--target", "project", "--json"}))
	var removeReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &removeReport))
	require.True(t, removeReport.Removed)
	require.Empty(t, removeReport.Sources)
	require.Empty(t, app.Config.Future.PluginMarketplaces)
}

func TestMarketplaceCompatibilityCommands(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: strings.Repeat("b", 64)},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/index.json", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(index))
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "AddMarketplace", indexURL, "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var addReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal([]byte(out), &addReport))
	require.Equal(t, "sources_add", addReport.Action)
	require.True(t, addReport.Added)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ManageMarketplaces"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var listReport marketplaceSourcesReport
	require.NoError(t, json.Unmarshal([]byte(out), &listReport))
	require.Equal(t, "sources_list", listReport.Action)
	require.Len(t, listReport.Sources, 1)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "BrowseMarketplace"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var indexes []plugins.MarketplaceIndex
	require.NoError(t, json.Unmarshal([]byte(out), &indexes))
	require.Equal(t, "demo", indexes[0].Plugins[0].ID)

	pluginDir := filepath.Join(workspace, "source-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"source-plugin","name":"source-plugin","tools":[{"name":"source_tool","command":"echo ok","permission":"read-only"}]}`), 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "ValidatePlugin", pluginDir}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var validation pluginValidationReport
	require.NoError(t, json.Unmarshal([]byte(out), &validation))
	require.Equal(t, "validate", validation.Action)
	require.Equal(t, "ok", validation.Status)
	require.True(t, validation.Success)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--json", "PluginSettings"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var settings marketplaceSettingsReport
	require.NoError(t, json.Unmarshal([]byte(out), &settings))
	require.Equal(t, "settings", settings.Action)
	require.Len(t, settings.Sources, 1)
}

func TestPluginCompatibilityHelperCommands(t *testing.T) {
	workspace := t.TempDir()
	demoDir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(demoDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(demoDir, "commands"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(demoDir, "skills", "review"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(demoDir, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(demoDir, "commands", "fix.md"), []byte("# Fix\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(demoDir, "skills", "review", "SKILL.md"), []byte("# Review\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(demoDir, "hooks", "post.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(demoDir, "plugin.json"), []byte(`{
		"id":"demo",
		"name":"demo",
		"version":"0.1.0",
		"description":"Demo plugin",
		"tools":[{"name":"demo_tool","command":"echo ok","permission":"workspace-write"}],
		"commands":["commands/fix.md"],
		"skills":["skills/review/SKILL.md"],
		"hooks":["hooks/post.sh"],
		"mcp_servers":{"demo":{"command":"demo-mcp","args":["--stdio"],"env":["DEMO_TOKEN=secret"]}}
	}`), 0o644))
	badDir := filepath.Join(workspace, ".codog", "plugins", "bad")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "plugin.json"), []byte(`{
		"id":"bad",
		"name":"bad",
		"tools":[{"name":"bad_tool","command":"echo bad","permission":"root"}]
	}`), 0o644))

	var out bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: io.Discard}
	require.NoError(t, app.PluginCompatibility("PluginTrustWarning", []string{"--json"}))
	var trust pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &trust))
	require.Equal(t, "plugin_compatibility", trust.Kind)
	require.Equal(t, "trust_warning", trust.Action)
	require.Equal(t, "warn", trust.Status)
	require.GreaterOrEqual(t, trust.Summary.TrustItems, 1)
	require.NotEmpty(t, trust.TrustWarnings)
	out.Reset()

	require.NoError(t, app.PluginCompatibility("PluginErrors", []string{"--json"}))
	var errorsReport pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorsReport))
	require.Equal(t, "errors", errorsReport.Action)
	require.Equal(t, "error", errorsReport.Status)
	require.GreaterOrEqual(t, errorsReport.Summary.Errors, 1)
	out.Reset()

	require.NoError(t, app.PluginCompatibility("parseArgs", []string{"show", "demo", "--json"}))
	var parseReport pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &parseReport))
	require.Equal(t, "parse_args", parseReport.Action)
	require.Equal(t, "show", parseReport.NormalizedAction)
	require.NotNil(t, parseReport.SelectedPlugin)
	require.Equal(t, "demo", parseReport.SelectedPlugin.ID)
	require.NotNil(t, parseReport.ParsedArgs)
	require.Equal(t, "show", parseReport.ParsedArgs.Action)
	require.Equal(t, "demo", parseReport.ParsedArgs.Target)
	require.False(t, parseReport.ParsedArgs.Mutation)
	require.True(t, parseReport.ParsedArgs.RequiresTarget)
	require.Equal(t, []string{"codog", "plugins", "show", "demo"}, parseReport.ParsedArgs.LocalCommand)
	require.Equal(t, "codog plugins show demo", parseReport.ParsedArgs.NextCommand)
	out.Reset()

	require.NoError(t, app.PluginCompatibility("parseArgs", []string{"enable", "--json"}))
	var missingTarget pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missingTarget))
	require.Equal(t, "error", missingTarget.Status)
	require.NotNil(t, missingTarget.ParsedArgs)
	require.Equal(t, "enable", missingTarget.ParsedArgs.Action)
	require.True(t, missingTarget.ParsedArgs.Mutation)
	require.True(t, missingTarget.ParsedArgs.RequiresTarget)
	require.Equal(t, "plugin_target_required", missingTarget.ParsedArgs.ErrorKind)
	require.Equal(t, []string{"codog", "marketplace", "enable"}, missingTarget.ParsedArgs.LocalCommand)
	out.Reset()

	require.NoError(t, app.PluginCompatibility("parseArgs", []string{"bogus", "--json"}))
	var unknownAction pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unknownAction))
	require.Equal(t, "error", unknownAction.Status)
	require.NotNil(t, unknownAction.ParsedArgs)
	require.Equal(t, "bogus", unknownAction.ParsedArgs.Action)
	require.Equal(t, "unknown_plugins_action", unknownAction.ParsedArgs.ErrorKind)
	require.Contains(t, unknownAction.ParsedArgs.Error, "unknown plugins action")
	out.Reset()

	require.NoError(t, app.PluginCompatibility("pluginDetailsHelpers", []string{"show", "demo", "--json"}))
	var detailsReport pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &detailsReport))
	require.Equal(t, "details", detailsReport.Action)
	require.Equal(t, "ok", detailsReport.Status)
	require.NotNil(t, detailsReport.SelectedPlugin)
	require.Equal(t, "demo", detailsReport.SelectedPlugin.ID)
	require.NotNil(t, detailsReport.PluginDetails)
	require.Equal(t, "demo", detailsReport.PluginDetails.ID)
	require.Equal(t, filepath.Join(demoDir, "plugin.json"), detailsReport.PluginDetails.ManifestFile)
	require.Equal(t, filepath.Join(workspace, ".codog", "plugin-data", "demo"), detailsReport.PluginDetails.DataDir)
	require.Len(t, detailsReport.PluginDetails.Tools, 1)
	require.Equal(t, "demo_tool", detailsReport.PluginDetails.Tools[0].Name)
	require.True(t, detailsReport.PluginDetails.Tools[0].Executable)
	require.Contains(t, detailsReport.PluginDetails.Tools[0].Risks, "tool demo_tool executes a local command")
	require.Len(t, detailsReport.PluginDetails.Commands, 1)
	require.True(t, detailsReport.PluginDetails.Commands[0].Exists)
	require.Len(t, detailsReport.PluginDetails.Skills, 1)
	require.True(t, detailsReport.PluginDetails.Skills[0].Exists)
	require.Len(t, detailsReport.PluginDetails.Hooks, 1)
	require.True(t, detailsReport.PluginDetails.Hooks[0].Exists)
	require.Len(t, detailsReport.PluginDetails.MCPServers, 1)
	require.Equal(t, "demo", detailsReport.PluginDetails.MCPServers[0].Name)
	require.Equal(t, []string{"--stdio"}, detailsReport.PluginDetails.MCPServers[0].Args)
	require.Equal(t, []string{"DEMO_TOKEN"}, detailsReport.PluginDetails.MCPServers[0].EnvKeys)
	require.NotNil(t, detailsReport.PluginDetails.Validation)
	require.True(t, detailsReport.PluginDetails.Validation.Success)
	out.Reset()

	require.NoError(t, app.PluginCompatibility("pluginDetailsHelpers", []string{"missing", "--json"}))
	var missingDetails pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &missingDetails))
	require.Equal(t, "error", missingDetails.Status)
	require.Contains(t, missingDetails.DetailError, "missing")
	out.Reset()

	require.NoError(t, app.PluginCompatibility("usePagination", []string{"--page", "2", "--per-page", "1", "--json"}))
	var pageReport pluginCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &pageReport))
	require.Equal(t, "pagination", pageReport.Action)
	require.NotNil(t, pageReport.Pagination)
	require.Equal(t, 2, pageReport.Pagination.Page)
	require.Equal(t, 1, pageReport.Pagination.PerPage)
	require.Equal(t, 2, pageReport.Pagination.Total)
	require.Len(t, pageReport.Plugins, 1)
}

func TestMarketplaceDisableSkipsPluginToolRegistration(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat"}]}`), 0o644))

	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"disable", "demo"}))
	require.Contains(t, out.String(), `"enabled": false`)

	require.NoError(t, app.RegisterPluginTools())
	require.False(t, app.Tools.Has("demo_tool"))
}

func TestRegisterPluginToolsRejectsUnknownPermission(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"root"}]}`), 0o644))

	app := &App{
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Out:       io.Discard,
	}
	err := app.RegisterPluginTools()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported permission")
	require.False(t, app.Tools.Has("demo_tool"))
}

func TestReloadPluginsRebuildsCurrentToolRegistry(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Out:       &out,
		Err:       &errOut,
	}
	require.False(t, app.Tools.Has("demo_tool"))

	require.NoError(t, app.ReloadPlugins([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "reload_plugins"`)
	require.Contains(t, out.String(), `"plugins": 1`)
	require.Contains(t, out.String(), `"plugin_tools": 1`)
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	require.NoError(t, app.ReloadPlugins(nil))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.True(t, app.Tools.Has("demo_tool"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/reload-plugins", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Plugins Reloaded")
	require.Empty(t, errOut.String())
}

func TestMarketplaceInstallRemoteCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.1.0"}`,
		"demo/tool.sh":     "echo ok\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Marketplace([]string{"install-remote", "demo"}))
	require.Contains(t, out.String(), `"checksum_valid": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))
}

func TestMarketplaceUpdateCommandUsesConfiguredMarketplace(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tool.sh"), []byte("echo old\n"), 0o755))
	archive := makeAgentPluginZip(t, map[string]string{
		"demo/plugin.json": `{"id":"demo","name":"Demo","version":"0.2.0"}`,
		"demo/tool.sh":     "echo new\n",
	})
	sum := sha256.Sum256(archive)
	index := plugins.MarketplaceIndex{
		Plugins: []plugins.RemotePlugin{
			{ID: "demo", URL: "demo.zip", Version: "0.2.0", SHA256: hex.EncodeToString(sum[:])},
		},
	}
	payload, err := json.Marshal(index)
	require.NoError(t, err)
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			require.NoError(t, json.NewEncoder(w).Encode(index))
		case "/demo.zip":
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	indexURL := server.URL + "/index.json"
	app := &App{
		Config: config.Config{Future: config.FutureConfig{
			PluginMarketplaces:    []string{indexURL},
			PluginMarketplaceKeys: map[string]string{indexURL: base64.StdEncoding.EncodeToString(publicKey)},
		}},
		Workspace: workspace,
	}

	var out bytes.Buffer
	app.Out = &out
	require.NoError(t, app.Marketplace([]string{"updates"}))
	require.Contains(t, out.String(), `"latest_version": "0.2.0"`)

	out.Reset()
	require.NoError(t, app.Marketplace([]string{"update", "demo"}))
	require.Contains(t, out.String(), `"updated": true`)
	require.Contains(t, out.String(), `"signature_valid": true`)
	data, err := os.ReadFile(filepath.Join(dir, "tool.sh"))
	require.NoError(t, err)
	require.Equal(t, "echo new\n", string(data))
}

func TestUpdaterInstallAndRollbackCommands(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "codog-new")
	target := filepath.Join(dir, "codog")
	require.NoError(t, os.WriteFile(artifact, []byte("new"), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"install", artifact, target}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(data))

	out.Reset()
	require.NoError(t, app.Updater(context.Background(), []string{"rollback", target}))
	require.Contains(t, out.String(), `"rolled_back": true`)
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))

	aliasArtifact := filepath.Join(dir, "codog-alias-new")
	aliasTarget := filepath.Join(dir, "codog-alias")
	require.NoError(t, os.WriteFile(aliasArtifact, []byte("alias-new"), 0o755))
	require.NoError(t, os.WriteFile(aliasTarget, []byte("alias-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Install(context.Background(), []string{aliasArtifact, aliasTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(aliasTarget)
	require.NoError(t, err)
	require.Equal(t, "alias-new", string(data))

	upgradeArtifact := filepath.Join(dir, "codog-upgrade-new")
	upgradeTarget := filepath.Join(dir, "codog-upgrade")
	require.NoError(t, os.WriteFile(upgradeArtifact, []byte("upgrade-new"), 0o755))
	require.NoError(t, os.WriteFile(upgradeTarget, []byte("upgrade-old"), 0o755))
	out.Reset()
	require.NoError(t, app.Upgrade(context.Background(), []string{"install", upgradeArtifact, upgradeTarget}))
	require.Contains(t, out.String(), `"installed": true`)
	data, err = os.ReadFile(upgradeTarget)
	require.NoError(t, err)
	require.Equal(t, "upgrade-new", string(data))
}

func TestUpdaterVerifyCommand(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		manifest := updater.Manifest{Version: "0.2.0"}
		data, err := json.Marshal(manifest)
		require.NoError(t, err)
		manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
		require.NoError(t, json.NewEncoder(w).Encode(manifest))
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	require.NoError(t, app.Updater(context.Background(), []string{"verify", server.URL, base64.StdEncoding.EncodeToString(publicKey)}))
	require.Contains(t, out.String(), `"signature_valid": true`)
}

func makeAgentPluginZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		file, err := writer.CreateHeader(header)
		require.NoError(t, err)
		_, err = file.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func perfSignalKinds(signals []perfissue.Signal) []string {
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.Kind)
	}
	return kinds
}

func TestVoiceCommandHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_TEST_SPEAK_HELPER") == "1" {
		data, _ := io.ReadAll(os.Stdin)
		fmt.Printf("speak:%s", strings.TrimSpace(string(data)))
		os.Exit(0)
	}
	if os.Getenv("CODOG_TEST_VOICE_HELPER") != "1" {
		return
	}
	data, _ := io.ReadAll(os.Stdin)
	fmt.Printf("voice:%s", strings.TrimSpace(string(data)))
	os.Exit(0)
}
