package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

func writeACPTestLSPMessage(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func acpTestLSPDocumentURI(params json.RawMessage) string {
	var payload struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(params, &payload)
	return payload.TextDocument.URI
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

func TestParseFlagsSupportsMaxBudgetUSDForPrompt(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "--max-budget-usd", "1.25", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, overrides.MaxBudgetUSD)
	require.InDelta(t, 1.25, *overrides.MaxBudgetUSD, 0.0001)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	_, _, _, err = parseFlags([]string{"--max-budget-usd", "0", "prompt", "hello"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--max-budget-usd", flagErr.Flag)

	_, _, _, err = parseFlags([]string{"--max-budget-usd", "1.25", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--max-budget-usd", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")
}

func TestParseFlagsSupportsInteractiveBareResume(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--resume"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, interactiveResumeValue, overrides.Resume)
	require.Empty(t, command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume", "--model", "glm52", "tui"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, interactiveResumeValue, overrides.Resume)
	require.Equal(t, "glm52", overrides.Model)
	require.Equal(t, "tui", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--model", "glm52", "--resume", "tui"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, interactiveResumeValue, overrides.Resume)
	require.Equal(t, "glm52", overrides.Model)
	require.Equal(t, "tui", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume", "/status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, interactiveResumeValue, overrides.Resume)
	require.Equal(t, "/status", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume", "/plugin/command"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, interactiveResumeValue, overrides.Resume)
	require.Equal(t, "/plugin/command", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume=session-id", "tui"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "session-id", overrides.Resume)
	require.Equal(t, "tui", command)
	require.Empty(t, rest)

	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(sessionPath, []byte(""), 0o600))
	overrides, command, rest, err = parseFlags([]string{"--resume", sessionPath, "/status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, sessionPath, overrides.Resume)
	require.Equal(t, "/status", command)
	require.Empty(t, rest)

	_, command, rest, err = parseFlags([]string{"compact", "--resume"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "compact", command)
	require.Equal(t, []string{"--resume"}, rest)
}

func TestTerminalInputRejectsNonTTYCharacterDevice(t *testing.T) {
	input, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer func() { _ = input.Close() }()

	_, ok := terminalInput(input)
	require.False(t, ok)
}

func TestInteractiveWorkspaceCommand(t *testing.T) {
	for _, command := range []string{"", "tui", "TUI", "repl", " REPL "} {
		require.True(t, interactiveWorkspaceCommand(command), command)
	}
	for _, command := range []string{"prompt", "status", "mcp"} {
		require.False(t, interactiveWorkspaceCommand(command), command)
	}
}

func TestWorkspaceMatchesTrustedRoots(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "trusted")
	child := filepath.Join(root, "nested", "project")
	require.NoError(t, os.MkdirAll(child, 0o755))

	require.True(t, workspaceMatchesTrustedRoots(root, []string{root}))
	require.True(t, workspaceMatchesTrustedRoots(child, []string{root}))
	require.False(t, workspaceMatchesTrustedRoots(root+"-other", []string{root}))
	require.False(t, workspaceMatchesTrustedRoots(child, []string{"", "  "}))

	alias := filepath.Join(parent, "trusted-alias")
	if err := os.Symlink(root, alias); err == nil {
		require.True(t, workspaceMatchesTrustedRoots(child, []string{alias}))
		require.True(t, workspaceMatchesTrustedRoots(filepath.Join(alias, "nested", "project"), []string{root}))
	}
}

func TestAppendUniqueTrustRootCanonicalizesAndDeduplicates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trusted")
	require.NoError(t, os.MkdirAll(root, 0o755))

	roots := appendUniqueTrustRoot([]string{"", root + string(os.PathSeparator), "  "}, root)
	require.Equal(t, []string{root + string(os.PathSeparator)}, roots)

	other := filepath.Join(t.TempDir(), "other")
	roots = appendUniqueTrustRoot(roots, other)
	require.Equal(t, []string{root + string(os.PathSeparator), other}, roots)
}

func TestParseFlagsSupportsDebugAndVerbose(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--debug", "--verbose", "--debug-file", "debug.log", "-p", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.Debug)
	require.True(t, overrides.Verbose)
	require.Equal(t, "debug.log", overrides.DebugFile)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"-v", "status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.False(t, overrides.Debug)
	require.True(t, overrides.Verbose)
	require.Equal(t, "status", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--debug-file=trace.log", "status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "trace.log", overrides.DebugFile)
	require.Equal(t, "status", command)
	require.Empty(t, rest)
}

func TestRenderDebugStartupOmitsSecrets(t *testing.T) {
	var out bytes.Buffer
	cfg := config.Config{
		ConfigHome:      "config-home",
		Model:           "mock-model",
		BaseURL:         "https://api.example.test",
		RuntimeProvider: "anthropic",
		PermissionMode:  "workspace-write",
		APIKey:          "secret-api-key",
		AuthToken:       "secret-auth-token",
		Debug:           true,
		Verbose:         true,
	}

	require.NoError(t, renderDebugStartup(&out, cfg, "prompt", []string{"hello"}, "workspace", config.FlagOverrides{SessionID: "session-1", Resume: "latest"}, "json"))

	line := strings.TrimSpace(out.String())
	require.Contains(t, line, "codog debug: ")
	require.NotContains(t, line, "secret-api-key")
	require.NotContains(t, line, "secret-auth-token")
	var report debugStartupReport
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "codog debug: ")), &report))
	require.Equal(t, "debug_startup", report.Kind)
	require.Equal(t, "prompt", report.Command)
	require.Equal(t, []string{"hello"}, report.Args)
	require.Equal(t, "workspace", report.Workspace)
	require.Equal(t, "config-home", report.ConfigHome)
	require.True(t, report.Debug)
	require.True(t, report.Verbose)
	require.True(t, report.APIKeyConfigured)
	require.True(t, report.AuthTokenProvided)
	require.Equal(t, "session-1", report.SessionID)
	require.Equal(t, "latest", report.Resume)
}

func TestRenderDebugStartupWritesDebugFile(t *testing.T) {
	var out bytes.Buffer
	debugFile := filepath.Join(t.TempDir(), "logs", "debug.log")
	cfg := config.Config{
		ConfigHome: "config-home",
		Model:      "mock-model",
		BaseURL:    "https://api.example.test",
		APIKey:     "secret-api-key",
		DebugFile:  debugFile,
	}

	require.NoError(t, renderDebugStartup(&out, cfg, "status", nil, "workspace", config.FlagOverrides{}, "json"))

	require.Empty(t, out.String())
	data, err := os.ReadFile(debugFile)
	require.NoError(t, err)
	line := strings.TrimSpace(string(data))
	require.Contains(t, line, "codog debug: ")
	require.NotContains(t, line, "secret-api-key")
	var report debugStartupReport
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "codog debug: ")), &report))
	require.Equal(t, "debug_startup", report.Kind)
	require.Equal(t, "status", report.Command)
	require.Equal(t, debugFile, report.DebugFile)
	require.True(t, report.APIKeyConfigured)
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
	require.Equal(t, "default", overrides.OutputFormatSource)
	require.Empty(t, overrides.OutputFormatRaw)
	require.False(t, overrides.OutputFormatOverridden)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--print", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
	require.False(t, overrides.SkipPermissions)

	_, command, rest, err = parseFlags([]string{"-p", "--attach", "notes.txt", "--file=image.png", "describe"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"describe", "--attach", "notes.txt", "--attach", "image.png"}, rest)
}

func TestParseFlagsTreatsFreeTextAsInteractiveInitialPrompt(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--model", "glm52",
		"--attach", "screenshot.png",
		"inspect", "this", "repository",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Empty(t, command)
	require.Empty(t, rest)
	require.Equal(t, "inspect this repository", overrides.InitialPrompt)
	require.Equal(t, []string{"screenshot.png"}, overrides.InitialAttachments)
	require.Equal(t, "glm52", overrides.Model)

	_, command, rest, err = parseFlags([]string{"status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Empty(t, rest)

	_, command, rest, err = parseFlags([]string{"statsu", "--json"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "statsu", command)
	require.Equal(t, []string{"--json"}, rest)
}

func TestParseFlagsSupportsSessionRuntimeOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--agents", `{"reviewer":{"description":"Reviews","prompt":"Review."}}`,
		"--plugin-dir", "./plugin-one",
		"--plugin-dir", "./plugin-two",
		"--setting-sources", "project,local",
		"--ide",
		"status",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Empty(t, rest)
	require.Contains(t, overrides.Agents, "reviewer")
	require.Equal(t, []string{"./plugin-one", "./plugin-two"}, overrides.PluginDirs)
	require.Equal(t, []string{"project", "local"}, overrides.SettingSources)
	require.True(t, overrides.SettingSourcesSet)
	require.True(t, overrides.IDE)

	overrides, command, rest, err = parseFlags([]string{
		"--plugin-dir", "./plugin-one", "./plugin-two", "status",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Empty(t, rest)
	require.Equal(t, []string{"./plugin-one", "./plugin-two"}, overrides.PluginDirs)
}

func TestParseFlagsRejectsInteractivePromptOutputAndPrefillConflicts(t *testing.T) {
	_, _, _, err := parseFlags([]string{"--output-format", "json", "inspect this repository"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --print")

	_, _, _, err = parseFlags([]string{"--prefill", "draft", "inspect this repository"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be combined")
}

func TestParseFlagsSupportsCompactPromptMode(t *testing.T) {
	_, command, rest, err := parseFlags([]string{"--compact", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello", "--compact"}, rest)

	_, command, rest, err = parseFlags([]string{"--output-format", "json", "--compact", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello", "--output-format", "json", "--compact"}, rest)

	_, command, rest, err = parseFlags([]string{"--compact"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"--compact"}, rest)

	_, _, _, err = parseFlags([]string{"--compact", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--compact", flagErr.Flag)
	require.Equal(t, "status", flagErr.Value)
}

func TestParseFlagsSupportsToolRuleOverrides(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{
		"--allowed-tools", "read_file,grep",
		"--allowedTools", "glob",
		"--disallowed-tools", "bash",
		"--disallowedTools", "write_file,edit_file",
		"--tools", "Read,Glob",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"read_file", "grep", "glob"}, overrides.AllowedTools)
	require.Equal(t, []string{"bash", "write_file", "edit_file"}, overrides.DisallowedTools)
	require.True(t, overrides.ToolNamesSet)
	require.Equal(t, []string{"Read", "Glob"}, overrides.ToolNames)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--tools=", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.ToolNamesSet)
	require.Empty(t, overrides.ToolNames)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--tools", "default", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.False(t, overrides.ToolNamesSet)
	require.Empty(t, overrides.ToolNames)
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
			"--tools", "not_a_tool",
			"status",
		}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_tool_name", report.ErrorKind)
	require.Equal(t, "not_a_tool", report.ToolName)
	require.Equal(t, "--tools", report.Argument)
	require.Contains(t, report.Available, "read_file")
	require.Equal(t, "read_file", report.ToolAliases["Read"])

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

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--output-format", "json",
			"--tools",
			"status",
		}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "--tools", report.Argument)
	require.Contains(t, report.Hint, "read_file,grep")
}

func TestLocalRouteGuardContracts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	cases := [][]string{
		{"session", "bogus"},
		{"session", "nuke"},
		{"cost", "breakdown"},
		{"clear", "--force"},
		{"memory", "reset"},
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
			require.Equal(t, "usage", report.Error.Kind)
			require.Equal(t, "parse_args", report.Error.Operation)
			require.Equal(t, strings.Join(route, " "), report.Error.Target)
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

func TestGlobalResumeNonSlashTrailingArgumentContract(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{"), 0o644))

	cases := []struct {
		command string
		slash   string
	}{
		{command: "compact", slash: "/compact"},
		{command: "status", slash: "/status"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--resume", "latest", tc.command}, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.True(t, exitErr.Silent)
			var report cliErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "invalid_resume_argument", report.ErrorKind)
			require.Equal(t, tc.command, report.Command)
			require.Contains(t, report.Hint, tc.slash)
			require.Contains(t, report.Hint, "prompt")
			require.NotContains(t, out, "config_parse_error")
			require.NotContains(t, out, "missing_credentials")
		})
	}
}

func TestBuildCLIErrorReportMissingCredentials(t *testing.T) {
	report := buildCLIErrorReport(anthropic.MissingCredentialsError{
		Provider: "Anthropic",
		EnvVars:  []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		Hint:     "I see OPENAI_API_KEY is set; use an OpenAI-compatible model prefix.",
	})

	require.Equal(t, "missing_credentials", report.ErrorKind)
	require.Equal(t, "Anthropic", report.Provider)
	require.ElementsMatch(t, []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}, report.EnvVars)
	require.Contains(t, report.Message, "Anthropic credentials")
	require.Contains(t, report.Hint, "OPENAI_API_KEY")
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
		Kind                string                       `json:"kind"`
		Action              string                       `json:"action"`
		Status              string                       `json:"status"`
		ErrorKind           string                       `json:"error_kind"`
		Message             string                       `json:"message"`
		Hint                string                       `json:"hint"`
		ConfigLoadError     string                       `json:"config_load_error"`
		ConfigLoadErrorKind string                       `json:"config_load_error_kind"`
		Paths               []string                     `json:"paths"`
		Files               []configFileInspectionReport `json:"files"`
		Config              config.Config                `json:"config"`
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
	require.Len(t, report.Files, 1)
	require.Equal(t, configPath, report.Files[0].Path)
	require.Equal(t, "load_error", report.Files[0].Status)
	require.Equal(t, "parse_error", report.Files[0].Reason)
	require.Contains(t, report.Files[0].Detail, "unexpected end of JSON input")
	require.Equal(t, "parse_error", report.Files[0].ErrorKind)
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
		target    string
	}{
		{
			name:      "agents unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "agents", "creat"},
			kind:      "agents",
			action:    "creat",
			errorKind: "unknown_agents_subcommand",
			hintPart:  "Did you mean `codog agents create`?",
		},
		{
			name:      "plugins unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "instal"},
			kind:      "plugins",
			action:    "instal",
			errorKind: "unknown_plugins_action",
			hintPart:  "Did you mean one of: install, uninstall?",
		},
		{
			name:      "sessions unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "sessions", "serch"},
			kind:      "sessions",
			action:    "serch",
			errorKind: "unsupported_sessions_action",
			hintPart:  "Did you mean `codog sessions search`?",
		},
		{
			name:      "mcp unknown",
			args:      []string{"--config", configPath, "--output-format", "json", "mcp", "sho"},
			kind:      "mcp",
			action:    "error",
			errorKind: "unsupported_action",
			hintPart:  "Did you mean `codog mcp show`?",
			target:    "sho",
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
			require.Equal(t, "usage", report.Error.Kind)
			require.Equal(t, "parse_args", report.Error.Operation)
			if report.Argument != "" {
				require.Equal(t, report.Argument, report.Error.Target)
			} else if tc.target != "" {
				require.Equal(t, tc.target, report.Error.Target)
			} else {
				require.Equal(t, report.Kind, report.Error.Target)
			}
			require.False(t, report.Error.Retryable)
			require.Contains(t, report.Hint, tc.hintPart)
		})
	}
}

func TestConfigAndSettingsHelpJSONContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "config", "help"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var configHelp configHelpReport
	require.NoError(t, json.Unmarshal([]byte(out), &configHelp))
	require.Equal(t, "config", configHelp.Kind)
	require.Equal(t, "ok", configHelp.Status)
	require.Equal(t, "help", configHelp.Section)
	require.NotEmpty(t, configHelp.AvailableSections)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "settings", "help", "--output-format", "json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NotContains(t, out, "unknown config section")
	var settingsHelp helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &settingsHelp))
	require.Equal(t, "help", settingsHelp.Kind)
	require.Equal(t, "ok", settingsHelp.Status)
	require.Equal(t, "settings", settingsHelp.Topic)
	require.Equal(t, "config", settingsHelp.Command)
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

func TestLocalArgumentErrorJSONContracts(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name      string
		args      []string
		errorKind string
		command   string
		option    string
		extra     []string
		hintPart  string
	}{
		{
			name:      "diff unknown option",
			args:      []string{"--config", configPath, "--output-format", "json", "diff", "--bogus"},
			errorKind: "unknown_option",
			command:   "diff",
			option:    "--bogus",
			hintPart:  "codog diff",
		},
		{
			name:      "diff trailing json",
			args:      []string{"--config", configPath, "diff", "--bogus", "--output-format", "json"},
			errorKind: "unknown_option",
			command:   "diff",
			option:    "--bogus",
			hintPart:  "codog diff",
		},
		{
			name:      "export missing output",
			args:      []string{"--config", configPath, "--output-format", "json", "export", "--output"},
			errorKind: "missing_flag_value",
			command:   "export",
			option:    "--output",
			hintPart:  "codog export",
		},
		{
			name:      "export extra positional",
			args:      []string{"--config", configPath, "--output-format", "json", "export", "first.md", "second.md"},
			errorKind: "unexpected_extra_args",
			command:   "export",
			extra:     []string{"second.md"},
			hintPart:  "codog export",
		},
		{
			name:      "system-prompt unknown option",
			args:      []string{"--config", configPath, "--output-format", "json", "system-prompt", "bogus"},
			errorKind: "unknown_option",
			command:   "system-prompt",
			option:    "bogus",
			hintPart:  "system-prompt",
		},
		{
			name:      "init extra positional",
			args:      []string{"--config", configPath, "--output-format", "json", "init", "extraarg"},
			errorKind: "unknown_option",
			command:   "init",
			option:    "extraarg",
			hintPart:  "codog init",
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
			require.Equal(t, tc.command, report.Command)
			if tc.option != "" {
				require.Equal(t, tc.option, report.Option)
			}
			if tc.extra != nil {
				require.Equal(t, tc.extra, report.Args)
			}
			require.Contains(t, report.Hint, tc.hintPart)
			require.NotContains(t, out, "config_load_failed")
			require.NotContains(t, out, "missing_credentials")
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
			name:      "info not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "info", "no-such-plugin"},
			action:    "show",
			errorKind: "plugin_not_found",
			hint:      "plugins list",
		},
		{
			name:      "describe not found",
			args:      []string{"--config", configPath, "--output-format", "json", "plugins", "describe", "no-such-plugin"},
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
			require.Equal(t, "usage", report.Error.Kind)
			require.Equal(t, "parse_args", report.Error.Operation)
			require.Equal(t, "plugins", report.Error.Target)
			require.Contains(t, report.Hint, tc.hint)
		})
	}
}

func TestPluginsInfoAndDescribeAliasShow(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(pluginRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"demo","name":"Demo","version":"0.1.0","description":"Demo plugin"}`), 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "plugins", "info", "demo"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"id": "demo"`)
	require.Contains(t, out, `"description": "Demo plugin"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "plugins", "describe", "Demo"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report struct {
		Kind   string           `json:"kind"`
		Action string           `json:"action"`
		Status string           `json:"status"`
		Plugin plugins.Manifest `json:"plugin"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "plugin", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "demo", report.Plugin.ID)
	require.Equal(t, "Demo", report.Plugin.Name)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "plugins", "info"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var errorReport actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &errorReport))
	require.Equal(t, "plugins", errorReport.Kind)
	require.Equal(t, "show", errorReport.Action)
	require.Equal(t, "missing_argument", errorReport.ErrorKind)
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
		"--system-prompt-file", "base.txt",
		"--append-system-prompt", "extra",
		"--append-system-prompt-file", "extra.txt",
		"prompt", "hello",
	}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "base", overrides.SystemPrompt)
	require.Equal(t, "base.txt", overrides.SystemPromptFile)
	require.Equal(t, "extra", overrides.AppendPrompt)
	require.Equal(t, "extra.txt", overrides.AppendPromptFile)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestParseFlagsSupportsNoSessionPersistenceForPrompt(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "--no-session-persistence", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.NoSessionPersistence)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	_, _, _, err = parseFlags([]string{"--no-session-persistence", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--no-session-persistence", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")
}

func TestParseFlagsSupportsInputFormatForPrompt(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--replay-user-messages"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "stream-json", overrides.InputFormat)
	require.True(t, overrides.ReplayUserMessages)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"--output-format", "stream-json", "--input-format", "stream-json", "--replay-user-messages"}, rest)

	_, _, _, err = parseFlags([]string{"--input-format", "stream-json", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--input-format", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")

	_, _, _, err = parseFlags([]string{"--replay-user-messages", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--replay-user-messages", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")
}

func TestParseFlagsSupportsIncludePartialMessagesForPrompt(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"-p", "--output-format", "stream-json", "--include-partial-messages", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.IncludePartialMessages)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello", "--output-format", "stream-json", "--include-partial-messages"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--include-partial-messages", "prompt", "--output-format", "stream-json", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.IncludePartialMessages)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"--output-format", "stream-json", "hello", "--include-partial-messages"}, rest)

	_, _, _, err = parseFlags([]string{"--include-partial-messages", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--include-partial-messages", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")
}

func TestParseFlagsSupportsJSONSchemaForPrompt(t *testing.T) {
	rawSchema := `{"type":"object"}`
	overrides, command, rest, err := parseFlags([]string{"-p", "--json-schema", rawSchema, "--output-format", "json", "{}"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, rawSchema, overrides.JSONSchema)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"{}", "--output-format", "json", "--json-schema", rawSchema}, rest)

	_, command, rest, err = parseFlags([]string{"--json-schema", rawSchema, "prompt", "{}"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"{}", "--json-schema", rawSchema}, rest)

	_, _, _, err = parseFlags([]string{"--json-schema", rawSchema, "status"}, config.FlagOverrides{})
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--json-schema", flagErr.Flag)
	require.Contains(t, flagErr.Message, "prompt mode")
}

func TestParseFlagsSupportsMCPConfigOverrides(t *testing.T) {
	inline := `{"mcpServers":{"one":{"command":"one","args":["--stdio","--flag"]},"two":{"url":"https://two.example/mcp"}}}`
	overrides, command, rest, err := parseFlags([]string{"--mcp-config", "mcp.json", "--mcp-config", inline, "--strict-mcp-config", "mcp", "list", "--json"}, config.FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, []string{"mcp.json", inline}, overrides.MCPConfigs)
	require.True(t, overrides.StrictMCPConfig)
	require.Equal(t, "mcp", command)
	require.Equal(t, []string{"list", "--json"}, rest)
}

func TestParseFlagsSupportsSettingsOverride(t *testing.T) {
	inline := `{"model":"settings-model","mcpServers":{"one":{"command":"one","args":["--stdio","--flag"]}}}`
	overrides, command, rest, err := parseFlags([]string{"--settings", inline, "config", "get", "model", "--json"}, config.FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, inline, overrides.Settings)
	require.Equal(t, "config", command)
	require.Equal(t, []string{"get", "model", "--json"}, rest)
}

func TestParseFlagsSupportsSessionIDAndNameAliases(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--session-id", "sdk-session", "--name", "SDK Display", "-p", "hello"}, config.FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "sdk-session", overrides.SessionID)
	require.Equal(t, "SDK Display", overrides.SessionName)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)
}

func TestParseFlagsSupportsGlobalOutputFormat(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--output-format", "json", "status"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "flag", overrides.OutputFormatSource)
	require.Equal(t, "json", overrides.OutputFormatRaw)
	require.False(t, overrides.OutputFormatOverridden)
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

	for _, name := range []string{"bridge", "bridge-kick", "ide", "install", "teleport", "upgrade"} {
		_, command, rest, err = parseFlags([]string{"--output-format", "json", name, "arg"}, config.FlagOverrides{})
		require.NoError(t, err)
		require.Equal(t, name, command)
		require.Equal(t, []string{"arg", "--output-format", "json"}, rest)
	}
}

func TestParseFlagsRejectsDuplicateScalarGlobalFlags(t *testing.T) {
	_, _, _, err := parseFlags([]string{"--permission-mode", "read-only", "--permission-mode=danger-full-access", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate_flag")
	require.Contains(t, err.Error(), "--permission-mode")

	_, _, _, err = parseFlags([]string{"--permission-mode", "workspace-write", "--skip-permissions", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--permission-mode")

	_, _, _, err = parseFlags([]string{"--json", "--output-format", "text", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--output-format")

	_, _, _, err = parseFlags([]string{"--model", "openai/gpt-4", "--model", "claude-test", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--model")

	_, _, _, err = parseFlags([]string{"--resume", "first", "-r", "second", "status"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--resume")

	_, command, rest, err := parseFlags([]string{"--output-format", "json", "status", "--output-format", "text"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "status", command)
	require.Equal(t, []string{"--output-format", "text"}, rest)
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

	req, err = parsePromptArgs([]string{"hello", "--compact"})
	require.NoError(t, err)
	require.Equal(t, "hello", req.Prompt)
	require.True(t, req.Compact)
	require.True(t, req.PromptProvided)

	req, err = parsePromptArgs([]string{"Review this", "--stdin", "--prompt-stdin"})
	require.NoError(t, err)
	require.Equal(t, "Review this", req.Prompt)
	require.True(t, req.UseStdin)
	require.True(t, req.PromptProvided)
	require.False(t, strings.Contains(req.Prompt, "--stdin"))

	req, err = parsePromptArgs([]string{"Describe", "--attach", "notes.txt", "--attachment=image.png", "--file=report.pdf"})
	require.NoError(t, err)
	require.Equal(t, "Describe", req.Prompt)
	require.Equal(t, []string{"notes.txt", "image.png", "report.pdf"}, req.Attachments)

	req, err = parsePromptArgs([]string{"--input-format", "stream-json", "--output-format", "stream-json", "--replay-user-messages"})
	require.NoError(t, err)
	require.Equal(t, "stream-json", req.InputFormat)
	require.Equal(t, "stream-json", req.Format)
	require.True(t, req.ReplayUserMessages)

	req, err = parsePromptArgs([]string{"--output-format", "stream-json", "--include-partial-messages", "hello"})
	require.NoError(t, err)
	require.Equal(t, "stream-json", req.Format)
	require.True(t, req.IncludePartialMessages)
	require.Equal(t, "hello", req.Prompt)

	req, err = parsePromptArgs([]string{"--output-format", "json", "--verbose", "hello"})
	require.NoError(t, err)
	require.Equal(t, "json", req.Format)
	require.True(t, req.Verbose)
	require.Equal(t, "hello", req.Prompt)

	rawSchema := `{"type":"object","required":["name"]}`
	req, err = parsePromptArgs([]string{"--json-schema", rawSchema, "--output-format", "json", "return json"})
	require.NoError(t, err)
	require.Equal(t, rawSchema, req.JSONSchema)
	require.Equal(t, "json", req.Format)
	require.Equal(t, "return json", req.Prompt)

	_, err = parsePromptArgs([]string{"--input-format", "stream-json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --output-format=stream-json")

	_, err = parsePromptArgs([]string{"--input-format", "xml", "--output-format", "stream-json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown prompt input format")

	_, err = parsePromptArgs([]string{"--replay-user-messages", "--output-format", "stream-json"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --input-format=stream-json")

	_, err = parsePromptArgs([]string{"--include-partial-messages", "hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --output-format=stream-json")

	_, err = parsePromptArgs([]string{"--json-schema"})
	require.Error(t, err)
	var missing missingFlagValueError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "--json-schema", missing.Flag)

	_, err = parsePromptArgs([]string{"--compact", "--json-schema", rawSchema})
	require.Error(t, err)
	var invalid invalidFlagValueError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "--json-schema", invalid.Flag)
}

func TestPromptJSONSchemaValidation(t *testing.T) {
	schema := `{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"count":{"type":"integer"},"tags":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`
	require.NoError(t, validatePromptJSONSchema(`{"name":"codog","count":2,"tags":["go"]}`, schema))

	err := validatePromptJSONSchema(`{"count":2}`, schema)
	require.Error(t, err)
	var schemaErr promptJSONSchemaValidationError
	require.ErrorAs(t, err, &schemaErr)
	require.Equal(t, "$.name", schemaErr.Path)
	require.Contains(t, schemaErr.Reason, "required")

	err = validatePromptJSONSchema(`{"name":7}`, schema)
	require.Error(t, err)
	require.ErrorAs(t, err, &schemaErr)
	require.Equal(t, "$.name", schemaErr.Path)
	require.Contains(t, schemaErr.Reason, "expected string")

	require.NoError(t, validatePromptJSONSchema(`"ok"`, `{"enum":["ok","done"]}`))
	err = validatePromptJSONSchema(`{bad json}`, schema)
	require.Error(t, err)
	require.ErrorAs(t, err, &schemaErr)
	require.Equal(t, "$", schemaErr.Path)

	err = validatePromptJSONSchema(`{"name":"codog"}`, `{bad schema}`)
	require.Error(t, err)
	var flagErr invalidFlagValueError
	require.ErrorAs(t, err, &flagErr)
	require.Equal(t, "--json-schema", flagErr.Flag)
}

func TestReadPromptStreamJSONInputExtractsSDKUserMessages(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"user","message":{"role":"user","content":"first prompt"},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second prompt"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"role":"user","content":"synthetic"},"parent_tool_use_id":null,"isSynthetic":true}`,
		`{"type":"user","message":{"role":"user","content":"replay"},"parent_tool_use_id":null,"isReplay":true}`,
		`{"type":"user","message":{"role":"user","content":"tool result"},"parent_tool_use_id":"tool-1"}`,
	}, "\n")

	prompt, err := readPromptStreamJSONInput(strings.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, "first prompt\n\nsecond prompt", prompt)
	state, err := readPromptStreamJSONInputState(strings.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, prompt, state.Prompt)
	require.Len(t, state.ReplayMessages, 2)
	require.True(t, state.ReplayMessages[0].IsReplay)
	require.Equal(t, "first prompt", state.ReplayMessages[0].Message.Content[0].Text)
	require.Equal(t, "second prompt", state.ReplayMessages[1].Message.Content[0].Text)

	_, err = readPromptStreamJSONInput(strings.NewReader("{bad json}\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "line 1")
}

func TestPromptUsesStreamJSONInputForModelRequest(t *testing.T) {
	var requestBody json.RawMessage
	server := httptest.NewServer(mockanthropic.Server{
		Text: "done",
		OnRequest: func(body json.RawMessage) {
			requestBody = append([]byte(nil), body...)
		},
	}.Handler())
	defer server.Close()

	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          t.TempDir(),
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
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	streamInput := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"stream prompt body"}]},"parent_tool_use_id":null}` + "\n"
	inputState, err := readPromptStreamJSONInputState(strings.NewReader(streamInput))
	require.NoError(t, err)
	require.NoError(t, app.promptWithOutputOptions(context.Background(), inputState.Prompt, config.FlagOverrides{SessionID: "stream-input-session"}, "stream-json", false, turnOptions{ReplayUserMessages: inputState.ReplayMessages}))

	var request struct {
		Messages []anthropic.Message `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(requestBody, &request))
	require.NotEmpty(t, request.Messages)
	require.Equal(t, "user", request.Messages[len(request.Messages)-1].Role)
	require.Contains(t, request.Messages[len(request.Messages)-1].Content[0].Text, "stream prompt body")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	require.Contains(t, lines[1], `"type":"user"`)
	require.Contains(t, lines[1], `"isReplay":true`)
	require.Contains(t, lines[1], "stream prompt body")
	require.NotContains(t, out.String(), `"type":"assistant_delta"`)
	require.Contains(t, out.String(), `"type":"result"`)
}

func TestPromptStreamJSONIncludePartialMessages(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "partial chunks"}.Handler())
	defer server.Close()

	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          t.TempDir(),
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
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.promptWithOutputOptions(context.Background(), "say hi", config.FlagOverrides{SessionID: "partial-session"}, "stream-json", false, turnOptions{IncludePartialMessages: true}))
	require.Contains(t, out.String(), `"type":"assistant_delta"`)
	require.Contains(t, out.String(), `"delta":"partial "`)
	require.Contains(t, out.String(), `"type":"result"`)
}

func TestPromptVerboseEnrichesStructuredOutput(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "verbose chunks"}.Handler())
	defer server.Close()

	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          t.TempDir(),
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
		Sessions:  session.NewWorkspaceStore(t.TempDir(), workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.promptWithOutputOptions(context.Background(), "say hi", config.FlagOverrides{SessionID: "verbose-json-session"}, "json", false, turnOptions{Verbose: true}))
	var report promptReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "verbose chunks", report.Response)
	require.Equal(t, "actual", report.Usage.Source)
	require.Equal(t, 10, report.Usage.InputTokens)
	require.Equal(t, 5, report.Usage.OutputTokens)
	require.Greater(t, report.CostUSD, 0.0)
	require.Len(t, report.Messages, 2)
	require.Equal(t, "user", report.Messages[0].Role)
	require.Equal(t, "assistant", report.Messages[1].Role)
	out.Reset()

	require.NoError(t, app.promptWithOutputOptions(context.Background(), "say stream", config.FlagOverrides{SessionID: "verbose-stream-session"}, "stream-json", false, turnOptions{Verbose: true}))
	require.Contains(t, out.String(), `"type":"assistant_delta"`)
	require.Contains(t, out.String(), `"delta":"verbose "`)
	require.Contains(t, out.String(), `"type":"result"`)
}

func TestMergePromptWithStdin(t *testing.T) {
	require.Equal(t, "Review this", mergePromptWithStdin("Review this", "   \n\t\n  "))
	require.Equal(t, "standalone body", mergePromptWithStdin("", "standalone body\n"))
	require.Equal(t, "Review this\n\nfn main() {}", mergePromptWithStdin("Review this", "\nfn main() {}\n"))
}

func TestParseAttachSlashArgs(t *testing.T) {
	prompt, attachments, err := parseAttachSlashArgs([]string{"notes.txt", "pixel.png", "--", "Describe", "these"})
	require.NoError(t, err)
	require.Equal(t, "Describe these", prompt)
	require.Equal(t, []string{"notes.txt", "pixel.png"}, attachments)

	prompt, attachments, err = parseAttachSlashArgs([]string{"notes.txt"})
	require.NoError(t, err)
	require.Equal(t, "Inspect the attached file(s) and summarize what matters.", prompt)
	require.Equal(t, []string{"notes.txt"}, attachments)

	_, _, err = parseAttachSlashArgs(nil)
	require.ErrorContains(t, err, "usage: /attach")
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
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "prompt", report.Error.Target)
	require.False(t, report.Error.Retryable)
	require.Contains(t, report.Hint, "codog prompt")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "prompt", ""}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_prompt", report.ErrorKind)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
}

func TestCompactFlagMissingArgumentOutputContract(t *testing.T) {
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
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--compact"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report promptErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "abort", report.Action)
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "prompt or subcommand", report.Argument)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "prompt or subcommand", report.Error.Target)
	require.Contains(t, report.Hint, "--compact")
}

func TestCompactFlagReadsPromptFromStdin(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "stdin done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	require.NoError(t, os.WriteFile(stdinPath, []byte("prompt from stdin\n"), 0o644))
	stdinFile, err := os.Open(stdinPath)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinFile
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, stdinFile.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--compact"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptCompactReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.True(t, report.Compact)
	require.Equal(t, "stdin done", report.Message)
	require.Equal(t, "mock", report.Model)
	require.Equal(t, 10, report.Usage.InputTokens)
	require.Equal(t, 5, report.Usage.OutputTokens)
}

func TestPromptStdinFlagAppendsPipeContext(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "merged stdin done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	require.NoError(t, os.WriteFile(stdinPath, []byte("\npipe context body\n"), 0o644))
	stdinFile, err := os.Open(stdinPath)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinFile
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, stdinFile.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"prompt", "Use stdin context",
			"--stdin",
			"--output-format", "json",
			"--compact",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptCompactReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "merged stdin done", report.Message)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	body := string(raw)
	require.Contains(t, body, "Use stdin context")
	require.Contains(t, body, "pipe context body")
}

func TestPromptWithAttachmentsBuildsStructuredUserContent(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "attachments done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("attachment notes\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pixel.png"), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"prompt", "Describe attachments",
			"--attach", "notes.txt",
			"--attach", "pixel.png",
			"--output-format", "json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "attachments done", report.Response)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Title  string `json:"title"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Len(t, body.Messages, 1)
	require.Equal(t, "user", body.Messages[0].Role)
	require.Len(t, body.Messages[0].Content, 3)
	require.Equal(t, "text", body.Messages[0].Content[0].Type)
	require.Equal(t, "Describe attachments", body.Messages[0].Content[0].Text)
	require.Equal(t, "text", body.Messages[0].Content[1].Type)
	require.Contains(t, body.Messages[0].Content[1].Text, `<attachment path="notes.txt"`)
	require.Contains(t, body.Messages[0].Content[1].Text, "attachment notes")
	require.Equal(t, "image", body.Messages[0].Content[2].Type)
	require.Equal(t, "pixel.png", body.Messages[0].Content[2].Title)
	require.NotNil(t, body.Messages[0].Content[2].Source)
	require.Equal(t, "base64", body.Messages[0].Content[2].Source.Type)
	require.Equal(t, "image/png", body.Messages[0].Content[2].Source.MediaType)
	require.NotEmpty(t, body.Messages[0].Content[2].Source.Data)
}

func TestPromptWithDirectoryAttachmentBuildsTextContext(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "directory attachment done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "nested", "guide.txt"), []byte("nested guide\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"prompt", "Describe directory",
			"--attach", "docs",
			"--output-format", "json",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "directory attachment done", report.Response)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	var body struct {
		Messages []struct {
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Title string `json:"title"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Len(t, body.Messages, 1)
	require.Len(t, body.Messages[0].Content, 2)
	attachment := body.Messages[0].Content[1]
	require.Equal(t, "text", attachment.Type)
	require.Equal(t, "docs", attachment.Title)
	require.Contains(t, attachment.Text, `<attachment_directory path="docs" files=2`)
	require.Contains(t, attachment.Text, `<file path="README.md"`)
	require.Contains(t, attachment.Text, "# Docs")
	require.Contains(t, attachment.Text, `<file path="nested/guide.txt"`)
	require.Contains(t, attachment.Text, "nested guide")
	require.Contains(t, attachment.Text, "<skipped>")
	require.Contains(t, attachment.Text, "binary.bin")
}

func TestPrintPromptAcceptsGlobalAttachments(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "print attachment done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("print attachment notes\n"), 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{
			"--config", configPath,
			"--cwd", workspace,
			"-p",
			"--attach", "notes.txt",
			"--output-format", "json",
			"Describe print attachment",
		}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "print attachment done", report.Response)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	require.Contains(t, string(raw), "Describe print attachment")
	require.Contains(t, string(raw), "print attachment notes")
}

func TestAttachSlashSendsStructuredUserContent(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "slash attachment done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	sess, err := store.Create("attach-slash")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("slash attachment notes\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pixel.png"), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "mock",
			MaxTokens:      128,
			MaxTurns:       1,
			PermissionMode: "read-only",
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome}),
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.True(t, app.handleSlash(context.Background(), "/attach notes.txt pixel.png -- Explain the attached files", sess))
	require.Empty(t, errOut.String())
	require.Contains(t, out.String(), "slash attachment done")
	require.Len(t, sess.Messages, 2)
	require.Len(t, sess.Messages[0].Content, 3)
	require.Equal(t, "Explain the attached files", sess.Messages[0].Content[0].Text)
	require.Equal(t, "image", sess.Messages[0].Content[2].Type)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					MediaType string `json:"media_type"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Len(t, body.Messages, 1)
	require.Equal(t, "user", body.Messages[0].Role)
	require.Equal(t, "Explain the attached files", body.Messages[0].Content[0].Text)
	require.Contains(t, body.Messages[0].Content[1].Text, "slash attachment notes")
	require.Equal(t, "image", body.Messages[0].Content[2].Type)
	require.NotNil(t, body.Messages[0].Content[2].Source)
	require.Equal(t, "image/png", body.Messages[0].Content[2].Source.MediaType)
}

func TestTopLevelPipedStdinRunsOneShotPrompt(t *testing.T) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "top stdin done",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	require.NoError(t, os.WriteFile(stdinPath, []byte("top-level stdin prompt\n"), 0o644))
	stdinFile, err := os.Open(stdinPath)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinFile
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, stdinFile.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report promptReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "run", report.Action)
	require.Equal(t, "completed", report.Status)
	require.Equal(t, "top stdin done", report.Response)

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		require.FailNow(t, "expected provider request to be captured")
	}
	require.Contains(t, string(raw), "top-level stdin prompt")
}

func TestTopLevelEmptyNonTTYStdinReturnsInteractiveOnly(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	require.NoError(t, os.WriteFile(stdinPath, nil, 0o644))
	stdinFile, err := os.Open(stdinPath)
	require.NoError(t, err)
	originalStdin := os.Stdin
	os.Stdin = stdinFile
	defer func() {
		os.Stdin = originalStdin
		require.NoError(t, stdinFile.Close())
	}()

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report slashErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "interactive_only", report.ErrorKind)
	require.Equal(t, "repl", report.Command)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "repl", report.Error.Target)
	require.Contains(t, report.Hint, "echo 'task' | codog")
}

func TestCompactFlagShorthandStaysOnPromptPath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("DASHSCOPE_API_KEY", "")

	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--compact", "hello"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_credentials", report.ErrorKind)
	require.NotEqual(t, "command_not_found", report.ErrorKind)
	require.NotEqual(t, "config_load_failed", report.ErrorKind)
}

func TestCompactFlagRejectsKnownNonPromptCommand(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "--compact", "status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_flag_value", report.ErrorKind)
	require.Equal(t, "--compact", report.Option)
	require.Equal(t, "status", report.Value)
	require.Contains(t, report.Hint, "codog --compact")
}

func TestExplicitEmptyTopLevelPromptOutputContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", ""}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report promptErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "prompt", report.Kind)
	require.Equal(t, "abort", report.Action)
	require.Equal(t, "empty_prompt", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "prompt", report.Error.Target)
	require.Contains(t, report.Hint, "codog prompt")
}

func TestHasExplicitEmptyPositionalSkipsFlagValues(t *testing.T) {
	require.True(t, hasExplicitEmptyPositional([]string{"--output-format", "json", ""}))
	require.True(t, hasExplicitEmptyPositional([]string{"--config", "config.json", "--", ""}))
	require.False(t, hasExplicitEmptyPositional(nil))
	require.False(t, hasExplicitEmptyPositional([]string{"--output-format", "json"}))
	require.False(t, hasExplicitEmptyPositional([]string{"--config", "", "status"}))
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
	out.Reset()

	err := app.DumpManifests([]string{"--manifests-dir", filepath.Join(t.TempDir(), "missing")})
	require.ErrorContains(t, err, "missing_manifests")

	err = app.DumpManifests([]string{"--manifests-dir", filepath.Join(t.TempDir(), "missing"), "--json"})
	requireStructuredCLIError(t, err, out.Bytes(), "missing_manifests", "missing_manifests")
}

func TestDumpManifestsMissingDirHonorsGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "dump-manifests", "--manifests-dir"}, config.FlagOverrides{})
	})
	requireStructuredCLIError(t, err, []byte(out), "missing_flag_value", "missing_flag_value")
	require.Contains(t, out, `"command": "dump-manifests"`)
	require.Contains(t, out, `"option": "--manifests-dir"`)
}

func requireStructuredCLIError(t *testing.T, err error, data []byte, kind string, errorKind string) {
	t.Helper()
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, kind, report.Kind)
	require.Equal(t, errorKind, report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.NotEmpty(t, report.Message)
	require.NotEmpty(t, report.Hint)
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

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "tool-details", "bash"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &cliReport))
	require.Equal(t, "tool_details", cliReport.Kind)
	require.Equal(t, "bash", cliReport.Tool.Name)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "tool-details", "--output-format"}, config.FlagOverrides{})
	})
	requireStructuredCLIError(t, err, []byte(cliOut), "missing_flag_value", "missing_flag_value")
	require.Contains(t, cliOut, `"command": "tool-details"`)
	require.Contains(t, cliOut, `"option": "--output-format"`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "tool-details", "bash", "--output-format", "yaml"}, config.FlagOverrides{})
	})
	requireStructuredCLIError(t, err, []byte(cliOut), "invalid_output_format", "invalid_output_format")
	require.Contains(t, cliOut, `"option": "--output-format"`)
	require.Contains(t, cliOut, `"value": "yaml"`)

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
	var existsReport sessionExistsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &existsReport))
	require.Equal(t, "session_exists", existsReport.Kind)
	require.Equal(t, "exists", existsReport.Action)
	require.Equal(t, "source", existsReport.SessionID)
	require.Equal(t, "source", existsReport.Requested)
	require.True(t, existsReport.Exists)
	require.False(t, existsReport.Active)
	require.NotEmpty(t, existsReport.Path)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"switch", "source"}))
	var switchReport sessionSwitchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &switchReport))
	require.Equal(t, "session_switch", switchReport.Kind)
	require.Equal(t, "switch", switchReport.Action)
	require.Equal(t, "ok", switchReport.Status)
	require.Empty(t, switchReport.PreviousSessionID)
	require.Equal(t, "source", switchReport.RequestedSession)
	require.Equal(t, "source", switchReport.SessionID)
	require.Equal(t, 1, switchReport.MessageCount)
	require.NotEmpty(t, switchReport.Path)
	require.Contains(t, switchReport.ContinueCommands[0], "--resume 'source' repl")
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"switch", "source", "--output-format", "text"}))
	require.Contains(t, out.String(), "Session switched")
	require.Contains(t, out.String(), "Session          source")
	require.Contains(t, out.String(), "--resume 'source' repl")
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"fork", "source", "branch"}))
	var forkReport sessionForkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &forkReport))
	require.Equal(t, "session_fork", forkReport.Kind)
	require.Equal(t, "fork", forkReport.Action)
	require.Equal(t, "ok", forkReport.Status)
	require.Equal(t, "source", forkReport.ParentSessionID)
	require.Equal(t, "branch", forkReport.BranchName)
	require.NotEmpty(t, forkReport.SessionID)
	require.Equal(t, 1, forkReport.MessageCount)
	require.NotEmpty(t, forkReport.Path)
	out.Reset()

	err := app.SessionsCommand([]string{"delete", forkReport.SessionID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "confirmation required")
	ok, err := store.Exists(forkReport.SessionID)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, app.SessionsCommand([]string{"delete", forkReport.SessionID, "--force"}))
	var deleteReport sessionDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleteReport))
	require.Equal(t, "session_delete", deleteReport.Kind)
	require.Equal(t, "delete", deleteReport.Action)
	require.Equal(t, "ok", deleteReport.Status)
	require.True(t, deleteReport.Deleted)
	require.Equal(t, forkReport.SessionID, deleteReport.SessionID)
	require.NotEmpty(t, deleteReport.Path)
}

func TestSessionsCommandActionAliases(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello aliases")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Executable: "codog"}

	require.NoError(t, app.SessionsCommand([]string{"ls", "--json"}))
	var listReport sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "sessions", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Contains(t, listReport.Sessions, "source")
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"get", "source"}))
	var showReport sessionShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "session_show", showReport.Kind)
	require.Equal(t, "show", showReport.Action)
	require.Equal(t, "source", showReport.SessionID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"has", "source"}))
	var existsReport sessionExistsReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &existsReport))
	require.Equal(t, "session_exists", existsReport.Kind)
	require.Equal(t, "exists", existsReport.Action)
	require.True(t, existsReport.Exists)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"use", "source"}))
	var switchReport sessionSwitchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &switchReport))
	require.Equal(t, "session_switch", switchReport.Kind)
	require.Equal(t, "switch", switchReport.Action)
	require.Equal(t, "source", switchReport.SessionID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"clone", "source", "alias-branch"}))
	var forkReport sessionForkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &forkReport))
	require.Equal(t, "session_fork", forkReport.Kind)
	require.Equal(t, "fork", forkReport.Action)
	require.Equal(t, "alias-branch", forkReport.BranchName)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"mv", forkReport.SessionID, "alias-moved"}))
	var renameReport sessionRenameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &renameReport))
	require.Equal(t, "session_rename", renameReport.Kind)
	require.Equal(t, "rename", renameReport.Action)
	require.Equal(t, "alias-moved", renameReport.NewSessionID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"rm", "alias-moved", "--force"}))
	var deleteReport sessionDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleteReport))
	require.Equal(t, "session_delete", deleteReport.Kind)
	require.Equal(t, "delete", deleteReport.Action)
	require.True(t, deleteReport.Deleted)
	ok, err := store.Exists("alias-moved")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSessionsCommandExportWritesFormats(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "export through sessions")))
	var out bytes.Buffer
	app := &App{Sessions: store, Workspace: workspace, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"export", "source"}))
	require.Contains(t, out.String(), "# Conversation Export")
	require.Contains(t, out.String(), "export through sessions")
	out.Reset()

	jsonPath := filepath.Join(workspace, "session-export.json")
	require.NoError(t, app.SessionsCommand([]string{"export", "source", jsonPath, "--format", "json"}))
	var jsonReport struct {
		SessionID string `json:"session_id"`
		File      string `json:"file"`
		Format    string `json:"format"`
		Messages  int    `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &jsonReport))
	require.Equal(t, "source", jsonReport.SessionID)
	require.Equal(t, jsonPath, jsonReport.File)
	require.Equal(t, "json", jsonReport.Format)
	require.Equal(t, 1, jsonReport.Messages)
	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "source"`)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"export", "--session=source", "--output", "session-export.html", "--output-format", "html"}))
	var htmlReport struct {
		File   string `json:"file"`
		Format string `json:"format"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &htmlReport))
	require.Equal(t, filepath.Join(workspace, "session-export.html"), htmlReport.File)
	require.Equal(t, "html", htmlReport.Format)
	data, err = os.ReadFile(htmlReport.File)
	require.NoError(t, err)
	require.Contains(t, string(data), "<!doctype html>")
	require.Contains(t, string(data), "export through sessions")
}

func TestSessionsCommandImportWritesManagedSession(t *testing.T) {
	sourceStore := session.NewStore(t.TempDir())
	require.NoError(t, sourceStore.Append("external", anthropic.TextMessage("user", "import through sessions command")))
	jsonData, _, err := sourceStore.Export("external", "json")
	require.NoError(t, err)
	sourcePath := filepath.Join(t.TempDir(), "external.json")
	require.NoError(t, os.WriteFile(sourcePath, jsonData, 0o644))

	configHome := t.TempDir()
	workspace := t.TempDir()
	targetStore := session.NewWorkspaceStore(configHome, workspace)
	var out bytes.Buffer
	app := &App{Sessions: targetStore, Workspace: workspace, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"import", sourcePath, "--id", "imported"}))
	var report sessionImportReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_import", report.Kind)
	require.Equal(t, "import", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, sourcePath, report.Source)
	require.Equal(t, "external", report.OriginalSessionID)
	require.Equal(t, "imported", report.SessionID)
	require.Equal(t, 1, report.MessageCount)
	require.False(t, report.Overwritten)
	require.NotEmpty(t, report.Path)
	out.Reset()

	imported, err := targetStore.OpenExisting("imported")
	require.NoError(t, err)
	require.Equal(t, "import through sessions command", imported.Messages[0].Content[0].Text)
	require.Equal(t, report.Identity.Workspace, imported.Identity.Workspace)
	require.NotEmpty(t, imported.Identity.Workspace)

	require.NoError(t, app.SessionsCommand([]string{"import", sourcePath, "--id", "imported", "--force", "--output-format", "text"}))
	require.Contains(t, out.String(), "Session imported")
	require.Contains(t, out.String(), "Overwritten      yes")
}

func TestSessionsCommandPruneDefaultsToDryRun(t *testing.T) {
	store := session.NewStore(t.TempDir())
	_, err := store.Create("empty-session")
	require.NoError(t, err)
	require.NoError(t, store.Append("kept-session", anthropic.TextMessage("user", "keep me")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"prune"}))
	var dryRun session.PruneReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dryRun))
	require.Equal(t, "session_prune", dryRun.Kind)
	require.Equal(t, "dry_run", dryRun.Status)
	require.True(t, dryRun.DryRun)
	require.True(t, dryRun.EmptyOnly)
	require.Equal(t, 1, dryRun.CandidateCount)
	require.Equal(t, 0, dryRun.DeletedCount)
	require.Equal(t, "empty-session", dryRun.Candidates[0].ID)
	ok, err := store.Exists("empty-session")
	require.NoError(t, err)
	require.True(t, ok)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"prune", "--confirm", "--output-format", "text"}))
	require.Contains(t, out.String(), "Session prune")
	require.Contains(t, out.String(), "Deleted          1")
	ok, err = store.Exists("empty-session")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = store.Exists("kept-session")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSessionSlashPruneSkipsActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	active, err := store.Open("active-empty")
	require.NoError(t, err)
	_, err = store.Create("other-empty")
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.True(t, app.handleSlash(context.Background(), "/session prune --confirm --json", active))
	require.Empty(t, errOut.String())
	var report session.PruneReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.DeletedCount)
	require.Equal(t, "other-empty", report.Deleted[0].ID)
	ok, err := store.Exists("active-empty")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.Exists("other-empty")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestSessionsShowJSONUsesStableReport(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "hello back")))
	forked, err := store.Fork("source", "investigation")
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"show", forked.ID, "--json"}))
	var report sessionShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_show", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, forked.ID, report.SessionID)
	require.Equal(t, 2, report.MessageCount)
	require.Len(t, report.Messages, 2)
	require.NotEmpty(t, report.Path)
	require.NotZero(t, report.CreatedAtMS)
	require.NotZero(t, report.UpdatedAtMS)
	require.NotZero(t, report.ModifiedEpochMillis)
	require.Equal(t, "source", report.ParentSessionID)
	require.Equal(t, "investigation", report.BranchName)
	require.NotEmpty(t, report.Lifecycle.Kind)
	require.True(t, report.Lifecycle.Saved)
}

func TestSessionsListJSONIncludesDetails(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello session")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "hello back")))
	forked, err := store.Fork("source", "investigation")
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Workspace: "/workspace"}

	require.NoError(t, app.SessionsCommand([]string{"list", "--json"}))
	var report sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "sessions", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "list", report.Action)
	require.Contains(t, report.Sessions, "source")
	require.Contains(t, report.Sessions, forked.ID)
	require.Equal(t, 2, report.Count)
	require.Equal(t, 2, report.Total)
	require.Equal(t, 2, report.Limit)
	require.Equal(t, 0, report.Offset)
	require.False(t, report.HasMore)
	require.Nil(t, report.NextOffset)
	require.Equal(t, "/workspace", report.Workspace)
	require.Len(t, report.SessionDetails, 2)
	details := map[string]sessionListDetail{}
	for _, detail := range report.SessionDetails {
		details[detail.ID] = detail
		require.NotEmpty(t, detail.Path)
		require.NotZero(t, detail.CreatedAtMS)
		require.NotZero(t, detail.UpdatedAtMS)
		require.NotZero(t, detail.ModifiedEpochMillis)
		require.NotEmpty(t, detail.Lifecycle.Kind)
		require.True(t, detail.Lifecycle.Saved)
	}
	require.Equal(t, 2, details["source"].MessageCount)
	require.Equal(t, "source", details[forked.ID].ParentSessionID)
	require.Equal(t, "investigation", details[forked.ID].BranchName)
	require.Equal(t, 2, details[forked.ID].MessageCount)
}

func TestSessionsListJSONPaginates(t *testing.T) {
	store := session.NewStore(t.TempDir())
	for _, id := range []string{"one", "two", "three"} {
		require.NoError(t, store.Append(id, anthropic.TextMessage("user", id)))
	}
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.SessionsCommand([]string{"list", "--json", "--offset", "0", "--limit", "1"}))
	var firstPage sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &firstPage))
	require.Equal(t, 3, firstPage.Total)
	require.Equal(t, 1, firstPage.Count)
	require.Equal(t, 1, firstPage.Limit)
	require.Equal(t, 0, firstPage.Offset)
	require.True(t, firstPage.HasMore)
	require.NotNil(t, firstPage.NextOffset)
	require.Equal(t, 1, *firstPage.NextOffset)
	require.Len(t, firstPage.Sessions, 1)
	require.Len(t, firstPage.SessionDetails, 1)
	firstID := firstPage.Sessions[0]
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"list", "--json", "--offset=1", "--limit=2"}))
	var secondPage sessionListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &secondPage))
	require.Equal(t, 3, secondPage.Total)
	require.Equal(t, 2, secondPage.Count)
	require.Equal(t, 2, secondPage.Limit)
	require.Equal(t, 1, secondPage.Offset)
	require.False(t, secondPage.HasMore)
	require.Nil(t, secondPage.NextOffset)
	require.Len(t, secondPage.Sessions, 2)
	require.NotContains(t, secondPage.Sessions, firstID)
}

func TestSessionsExistsMissingIDReportsStructuredError(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "sessions", "exists"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_argument", report.Kind)
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "sessions exists", report.Command)
	require.Equal(t, "ID", report.Argument)
	require.Contains(t, report.Hint, "codog sessions exists ID")
}

func TestSessionsShowMissingIDReportsStructuredError(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "sessions", "show"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "missing_argument", report.Kind)
	require.Equal(t, "missing_argument", report.ErrorKind)
	require.Equal(t, "sessions show", report.Command)
	require.Equal(t, "ID", report.Argument)
	require.Contains(t, report.Hint, "codog sessions show ID")
}

func TestSessionsSearchJSONIncludesIdentityAndMessageMatches(t *testing.T) {
	store := session.NewStore(t.TempDir())
	_, err := store.CreateWithIdentity("alpha", session.SessionIdentity{
		Title:   "Billing migration",
		Purpose: "follow up on invoice tooling",
	})
	require.NoError(t, err)
	require.NoError(t, store.Append("alpha", anthropic.TextMessage("user", "investigate the payment retry path")))
	require.NoError(t, store.Append("alpha", anthropic.TextMessage("assistant", "the retry path touches invoice reconciliation")))
	require.NoError(t, store.Append("beta", anthropic.TextMessage("user", "unrelated notes")))

	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}
	require.NoError(t, app.SessionsCommand([]string{"search", "invoice", "--json"}))

	var report sessionSearchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_search", report.Kind)
	require.Equal(t, "search", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "invoice", report.Query)
	require.Equal(t, 2, report.ScannedSessions)
	require.Equal(t, 2, report.Count)
	require.False(t, report.Truncated)

	fields := map[string]sessionSearchMatch{}
	for _, match := range report.Matches {
		fields[match.Field] = match
		require.Equal(t, "alpha", match.SessionID)
		require.NotEmpty(t, match.Path)
		require.Equal(t, 2, match.MessageCount)
		require.Contains(t, strings.ToLower(match.Snippet), "invoice")
	}
	require.Equal(t, "purpose", fields["purpose"].Field)
	require.Equal(t, "message", fields["message"].Field)
	require.Equal(t, 2, fields["message"].MessageIndex)
	require.Equal(t, "assistant", fields["message"].Role)
}

func TestSessionsSearchTextHonorsLimit(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("alpha", anthropic.TextMessage("user", "needle one")))
	require.NoError(t, store.Append("alpha", anthropic.TextMessage("assistant", "needle two")))

	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}
	require.NoError(t, app.SessionsCommand([]string{"find", "needle", "--limit", "1", "--output-format", "text"}))

	text := out.String()
	require.Contains(t, text, "Session Search")
	require.Contains(t, text, "Matches          1")
	require.Contains(t, text, "Truncated        yes, limit=1")
	require.Contains(t, text, "alpha")
	require.Contains(t, text, "needle one")
	require.NotContains(t, text, "needle two")
}

func TestSessionsAuditReportsHygieneIssuesAndNextActions(t *testing.T) {
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(t.TempDir(), workspace)
	_, err := store.CreateWithIdentity("healthy", session.SessionIdentity{
		Title:     "Healthy",
		Workspace: workspace,
		Worktree:  workspace,
		Purpose:   "exercise audit",
	})
	require.NoError(t, err)
	require.NoError(t, store.Append("healthy", anthropic.TextMessage("user", "hello audit")))
	forked, err := store.Fork("healthy", "investigation")
	require.NoError(t, err)
	_, err = store.CreateWithIdentity("empty", session.SessionIdentity{})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Workspace: workspace}
	require.NoError(t, app.SessionsCommand([]string{"doctor", "--json"}))

	var report sessionAuditReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_audit", report.Kind)
	require.Equal(t, "audit", report.Action)
	require.Equal(t, "warn", report.Status)
	require.Equal(t, workspace, report.Workspace)
	require.Equal(t, 3, report.SessionCount)
	require.Equal(t, 2, report.MessageCount)
	require.Equal(t, 1, report.EmptyCount)
	require.Equal(t, 1, report.BranchCount)
	require.GreaterOrEqual(t, report.PlaceholderIdentityCount, 1)
	require.Equal(t, 0, report.RepairableIdentityCount)
	require.GreaterOrEqual(t, report.ManualIdentityReviewCount, 1)
	require.Equal(t, 0, report.PinnedOutOfRangeCount)
	require.Contains(t, report.NextActions, "codog sessions prune --empty --confirm")
	require.Contains(t, report.NextActions, "codog sessions show 'empty' --json")
	require.NotContains(t, report.NextActions, "codog sessions repair")

	issues := map[string]sessionAuditIssue{}
	for _, issue := range report.Issues {
		issues[issue.Kind+":"+issue.SessionID] = issue
	}
	require.Equal(t, "empty_session", issues["empty_session:empty"].Kind)
	require.Equal(t, "info", issues["empty_session:empty"].Severity)
	require.Equal(t, "identity_placeholder", issues["identity_placeholder:empty"].Kind)
	require.Equal(t, "warn", issues["identity_placeholder:empty"].Severity)
	require.Equal(t, "purpose", issues["identity_placeholder:empty"].Field)
	require.Contains(t, issues["identity_placeholder:empty"].Message, "no saved user prompt")
	require.NotContains(t, issues, "empty_session:"+forked.ID)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"audit", "--output-format", "text"}))
	text := out.String()
	require.Contains(t, text, "Session Audit")
	require.Contains(t, text, "Status           warn")
	require.Contains(t, text, "Sessions         3")
	require.Contains(t, text, "Manual id review")
	require.Contains(t, text, "Next actions")
	require.Contains(t, text, "codog sessions prune --empty --confirm")
	require.Contains(t, text, "codog sessions show 'empty' --json")
	require.NotContains(t, text, "codog sessions repair")
}

func TestDoctorSurfacesSessionAuditWarnings(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	_, err := store.CreateWithIdentity("empty", session.SessionIdentity{})
	require.NoError(t, err)

	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			BaseURL:        "https://api.example.test",
			APIKey:         "secret",
			PermissionMode: "workspace-write",
		},
		Workspace: workspace,
		Tools:     tools.NewRegistry(workspace),
		Sessions:  store,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Doctor([]string{"--json"}))
	var report doctor.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "doctor", report.Kind)
	var sessions doctor.Check
	for _, check := range report.Checks {
		if check.Name == "Sessions" {
			sessions = check
			break
		}
	}
	require.Equal(t, "Sessions", sessions.Name)
	require.Equal(t, doctor.StatusWarn, sessions.Status)
	require.Contains(t, sessions.Hint, "codog sessions audit")
	require.Equal(t, float64(1), sessions.Data["session_count"])
	hygiene, ok := sessions.Data["hygiene"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "warn", hygiene["status"])
	require.Equal(t, float64(1), hygiene["session_count"])
	require.Equal(t, float64(1), hygiene["empty_count"])
	require.NotZero(t, hygiene["placeholder_identity_count"])
	require.NotZero(t, hygiene["manual_identity_review_count"])
	require.Contains(t, strings.Join(sessions.Details, "\n"), "Identity placeholders")
	require.Contains(t, strings.Join(sessions.Details, "\n"), "Manual identity review")
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
	out.Reset()

	require.NoError(t, app.ResumeCommand([]string{"recent", "--json"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "recent", report.RequestedSession)
	require.Equal(t, "source", report.SessionID)
}

func TestResumeSlashRoutesToResumeCommand(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active session")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other session")))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/resume", "other"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var direct resumeCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &direct))
	require.Equal(t, "resume", direct.Kind)
	require.Equal(t, "other", direct.SessionID)
	require.NotEmpty(t, direct.ContinueCommands)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "active", "--output-format", "json", "/resume", "other"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var resumed resumeCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumed))
	require.Equal(t, "resume", resumed.Kind)
	require.Equal(t, "other", resumed.SessionID)
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

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "continue", "source", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "source", report.SessionID)
	require.Equal(t, 1, report.MessageCount)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "continue", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "source", report.SessionID)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "resume", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NotContains(t, out, "Resume Session")
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "resume", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
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
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, "resolve_session_id", report.Error.Operation)
	require.Equal(t, "missing-session", report.Error.Target)
	require.False(t, report.Error.Retryable)
	require.Contains(t, report.Hint, "codog sessions list")
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoFileExists(t, filepath.Join(store.Dir, "missing-session.jsonl"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "resume", "missing-session"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "session_not_found", report.ErrorKind)
	require.Equal(t, "missing-session", report.RequestedSession)
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, "missing-session", report.Error.Target)

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
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, "resolve_session_id", report.Error.Operation)
	require.Equal(t, directoryPath, report.Error.Target)
	require.Contains(t, report.Hint, ".jsonl")
	require.Contains(t, report.Hint, "codog sessions list --json")
}

func TestResumeCrossWorkspaceSessionReportsTypedError(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	storeA := session.NewWorkspaceStore(configHome, workspaceA)
	created, err := storeA.CreateWithIdentity("cross-workspace", session.SessionIdentity{Purpose: "test"})
	require.NoError(t, err)
	require.NoError(t, storeA.Append(created.ID, anthropic.TextMessage("user", "from workspace a")))
	canonicalA, err := filepath.EvalSymlinks(workspaceA)
	require.NoError(t, err)
	canonicalB, err := filepath.EvalSymlinks(workspaceB)
	require.NoError(t, err)
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspaceB))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", created.Path, "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report sessionRestoreErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "session_workspace_mismatch", report.ErrorKind)
	require.Equal(t, created.Path, report.Path)
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, "resolve_session_id", report.Error.Operation)
	require.Equal(t, created.Path, report.Error.Target)
	require.Equal(t, canonicalB, report.ExpectedWorkspace)
	require.Equal(t, canonicalA, report.ActualWorkspace)
	require.Contains(t, report.Hint, "original workspace")
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
	var conversation conversationReport
	require.NoError(t, json.Unmarshal([]byte(out), &conversation))
	require.Equal(t, "conversation", conversation.Kind)
	require.Equal(t, "status", conversation.Action)
	require.Equal(t, report.SessionID, conversation.SessionID)
	require.Equal(t, 0, conversation.MessageCount)

	exportPath := filepath.Join(workspace, "conversation.md")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "conversation", "export", exportPath, "--session", report.SessionID}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"format": "markdown"`)
	exported, err := os.ReadFile(exportPath)
	require.NoError(t, err)
	require.Contains(t, string(exported), "# Conversation Export")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "conversation", "--confirm", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "clear", report.Kind)
	require.Equal(t, "create_session", report.Action)
	require.NotEqual(t, conversation.SessionID, report.SessionID)
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
	out.Reset()

	require.NoError(t, store.Append("resumed-legacy", anthropic.TextMessage("user", "resume backfill prompt")))
	require.NoError(t, app.RunResumedSlash(context.Background(), "/backfill-sessions", nil, config.FlagOverrides{Resume: "resumed-legacy"}, "json"))
	var report session.BackfillReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "backfill_sessions", report.Kind)
	require.Equal(t, 2, report.SessionsScanned)
	require.Equal(t, 1, report.SessionsUpdated)
	require.Equal(t, 1, report.InputsAdded)
}

func TestSessionsRepairCommandAndSlash(t *testing.T) {
	store := session.NewWorkspaceStore(t.TempDir(), t.TempDir())
	created, err := store.Open("needs-repair")
	require.NoError(t, err)
	require.NotEmpty(t, created.Identity.Placeholders)
	require.NoError(t, store.Append(created.ID, anthropic.TextMessage("user", "repair identity from saved prompt")))
	_, err = store.CreateWithIdentity("empty-placeholder", session.SessionIdentity{})
	require.NoError(t, err)
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}

	require.NoError(t, app.SessionsCommand([]string{"repair", "--json"}))
	var report session.BackfillReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "backfill_sessions", report.Kind)
	require.Equal(t, "identity_repair", report.Action)
	require.Equal(t, 2, report.SessionsScanned)
	require.Equal(t, 1, report.SessionsUpdated)
	require.Equal(t, 1, report.IdentityUpdates)
	require.Len(t, report.SkippedSessionDetails, 1)
	require.Equal(t, "empty-placeholder", report.SkippedSessionDetails[0].ID)
	require.Equal(t, "no_user_prompt", report.SkippedSessionDetails[0].Reason)
	repaired, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Equal(t, "repair identity from saved prompt", repaired.Identity.Title)
	require.Equal(t, "repair identity from saved prompt", repaired.Identity.Purpose)
	require.Empty(t, repaired.Identity.Placeholders)
	out.Reset()

	require.NoError(t, app.SessionsCommand([]string{"fix", "--output-format", "text"}))
	require.Contains(t, out.String(), "Repair Sessions")
	require.Contains(t, out.String(), "Sessions updated 0")
	require.Contains(t, out.String(), "Skipped sessions")
	require.Contains(t, out.String(), "empty-placeholder no_user_prompt")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session repair --json", &session.Session{ID: created.ID}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "identity_repair", report.Action)
	require.Equal(t, 0, report.SessionsUpdated)
	require.Empty(t, errOut.String())
}

func TestBackfillSessionsHonorsGlobalJSONOutputFormat(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("user", "legacy prompt")))
	t.Chdir(workspace)

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "backfill-sessions"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NotContains(t, out, "Backfill Sessions")

	var report session.BackfillReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "backfill_sessions", report.Kind)
	require.Equal(t, "prompt_history", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.SessionsScanned)
	require.Equal(t, 1, report.SessionsUpdated)
	require.Equal(t, 1, report.InputsAdded)
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
	require.Contains(t, errOut.String(), "Session forked")
	forkedID := sess.ID
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session switch source", sess))
	require.Equal(t, "source", sess.ID)
	require.Contains(t, errOut.String(), "session switched: source")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session delete "+forkedID, sess))
	require.Contains(t, errOut.String(), "confirmation required")
	ok, err := store.Exists(forkedID)
	require.NoError(t, err)
	require.True(t, ok)
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session delete "+forkedID+" --force", sess))
	require.Contains(t, errOut.String(), "Session deleted")
	require.Contains(t, errOut.String(), forkedID)
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session delete source --force", sess))
	require.Contains(t, errOut.String(), `refusing to delete the active session "source"`)
	ok, err = store.Exists("source")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSessionSlashForkJSONReportsNewSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello slash")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("source")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session fork incident --json", sess))
	require.Empty(t, errOut.String())
	var report sessionForkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_fork", report.Kind)
	require.Equal(t, "source", report.ParentSessionID)
	require.Equal(t, "incident", report.BranchName)
	require.Equal(t, sess.ID, report.SessionID)
	require.Equal(t, 1, report.MessageCount)
	require.NotEmpty(t, report.Path)
	require.NotEqual(t, "source", sess.ID)
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
	var renameReport sessionRenameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &renameReport))
	require.Equal(t, "session_rename", renameReport.Kind)
	require.Equal(t, "rename", renameReport.Action)
	require.Equal(t, "ok", renameReport.Status)
	require.Equal(t, "cli-renamed", renameReport.OldSessionID)
	require.Equal(t, "sessions-renamed", renameReport.NewSessionID)
	require.Equal(t, 1, renameReport.MessageCount)
	require.NotEmpty(t, renameReport.NewPath)
	out.Reset()

	sess, err := store.Open("sessions-renamed")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/rename slash-renamed", sess))
	require.Equal(t, "slash-renamed", sess.ID)
	require.Contains(t, errOut.String(), "session renamed: sessions-renamed -> slash-renamed")
	errOut.Reset()

	require.True(t, app.handleSlash(context.Background(), "/session rename final-renamed", sess))
	require.Equal(t, "final-renamed", sess.ID)
	require.Contains(t, errOut.String(), "Session renamed")
	require.Contains(t, errOut.String(), "final-renamed")
	opened, err := store.Open("final-renamed")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "rename me", opened.Messages[0].Content[0].Text)
}

func TestSessionSlashRenameJSONReportsRenamedSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "rename me")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("source")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session rename renamed --json", sess))
	require.Empty(t, errOut.String())
	var report sessionRenameReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_rename", report.Kind)
	require.Equal(t, "source", report.OldSessionID)
	require.Equal(t, "renamed", report.NewSessionID)
	require.Equal(t, "renamed", sess.ID)
	require.Equal(t, 1, report.MessageCount)
	require.NotEmpty(t, report.NewPath)
}

func TestSessionsDeleteRequiresForce(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("delete-me", anthropic.TextMessage("user", "delete me")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	err := app.SessionsCommand([]string{"delete", "delete-me"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "confirmation required")
	ok, existsErr := store.Exists("delete-me")
	require.NoError(t, existsErr)
	require.True(t, ok)

	require.NoError(t, app.SessionsCommand([]string{"delete", "delete-me", "--force"}))
	var deleteReport sessionDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleteReport))
	require.Equal(t, "session_delete", deleteReport.Kind)
	require.Equal(t, "delete-me", deleteReport.SessionID)
	require.True(t, deleteReport.Deleted)
	require.NotEmpty(t, deleteReport.Path)
	ok, existsErr = store.Exists("delete-me")
	require.NoError(t, existsErr)
	require.False(t, ok)
}

func TestSessionSlashDeleteJSONReportsDeletedSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Sessions: store, Out: &out, Err: &errOut}
	sess, err := store.Open("active")
	require.NoError(t, err)

	require.True(t, app.handleSlash(context.Background(), "/session delete other --force --json", sess))
	require.Empty(t, errOut.String())
	var report sessionDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_delete", report.Kind)
	require.Equal(t, "delete", report.Action)
	require.Equal(t, "ok", report.Status)
	require.True(t, report.Deleted)
	require.Equal(t, "other", report.SessionID)
	require.NotEmpty(t, report.Path)
	ok, existsErr := store.Exists("other")
	require.NoError(t, existsErr)
	require.False(t, ok)
}

func TestResumedSessionDeleteRefusesActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	err := app.runResumedSessionSlash([]string{"delete", "other"}, config.FlagOverrides{Resume: "active"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "confirmation required")
	ok, existsErr := store.Exists("other")
	require.NoError(t, existsErr)
	require.True(t, ok)

	err = app.runResumedSessionSlash([]string{"delete", "active", "--force"}, config.FlagOverrides{Resume: "active"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to delete the active session")
	ok, existsErr = store.Exists("active")
	require.NoError(t, existsErr)
	require.True(t, ok)

	require.NoError(t, app.runResumedSessionSlash([]string{"delete", "other", "--force"}, config.FlagOverrides{Resume: "active"}))
	var deleteReport sessionDeleteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &deleteReport))
	require.Equal(t, "session_delete", deleteReport.Kind)
	require.Equal(t, "other", deleteReport.SessionID)
	require.True(t, deleteReport.Deleted)
	require.NotEmpty(t, deleteReport.Path)
	ok, existsErr = store.Exists("other")
	require.NoError(t, existsErr)
	require.False(t, ok)
}

func TestResumedSessionForkAndSwitchReportsTargetSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("active", anthropic.TextMessage("user", "active")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other")))
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out, Executable: "codog"}

	require.NoError(t, app.runResumedSessionSlash([]string{"fork", "incident", "--json"}, config.FlagOverrides{Resume: "active"}))
	var forkReport sessionForkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &forkReport))
	require.Equal(t, "session_fork", forkReport.Kind)
	require.Equal(t, "active", forkReport.ParentSessionID)
	require.Equal(t, "incident", forkReport.BranchName)
	require.NotEmpty(t, forkReport.SessionID)
	require.NotEqual(t, "active", forkReport.SessionID)
	require.Equal(t, 1, forkReport.MessageCount)
	require.NotEmpty(t, forkReport.Path)
	out.Reset()

	require.NoError(t, app.runResumedSessionSlash([]string{"clone", "alias-incident", "--json"}, config.FlagOverrides{Resume: "active"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &forkReport))
	require.Equal(t, "session_fork", forkReport.Kind)
	require.Equal(t, "fork", forkReport.Action)
	require.Equal(t, "active", forkReport.ParentSessionID)
	require.Equal(t, "alias-incident", forkReport.BranchName)
	out.Reset()

	require.NoError(t, app.runResumedSessionSlash([]string{"switch", "other", "--json"}, config.FlagOverrides{Resume: "active"}))
	var switchReport sessionSwitchReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &switchReport))
	require.Equal(t, "session_switch", switchReport.Kind)
	require.Equal(t, "switch", switchReport.Action)
	require.Equal(t, "ok", switchReport.Status)
	require.Equal(t, "active", switchReport.PreviousSessionID)
	require.Equal(t, "other", switchReport.RequestedSession)
	require.Equal(t, "other", switchReport.SessionID)
	require.Equal(t, 1, switchReport.MessageCount)
	require.Contains(t, switchReport.ContinueCommands[0], "--resume 'other' repl")
	out.Reset()

	require.NoError(t, app.runResumedSessionSlash([]string{"checkout", "other", "--json"}, config.FlagOverrides{Resume: "active"}))
	require.NoError(t, json.Unmarshal(out.Bytes(), &switchReport))
	require.Equal(t, "session_switch", switchReport.Kind)
	require.Equal(t, "switch", switchReport.Action)
	require.Equal(t, "other", switchReport.SessionID)
	out.Reset()

	require.NoError(t, app.runResumedSessionSlash([]string{"switch", "other"}, config.FlagOverrides{Resume: "active"}))
	require.Contains(t, out.String(), "Session switched")
	require.Contains(t, out.String(), "Previous         active")
	require.Contains(t, out.String(), "Session          other")

	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 4)
}

func TestResumedSessionPruneSkipsActiveSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	_, err := store.Create("active-empty")
	require.NoError(t, err)
	_, err = store.Create("other-empty")
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{Sessions: store, Out: &out}

	require.NoError(t, app.runResumedSessionSlash([]string{"prune", "--confirm", "--json"}, config.FlagOverrides{Resume: "active-empty"}))
	var report session.PruneReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "session_prune", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.False(t, report.DryRun)
	require.Equal(t, 1, report.DeletedCount)
	require.Equal(t, "other-empty", report.Deleted[0].ID)
	ok, existsErr := store.Exists("active-empty")
	require.NoError(t, existsErr)
	require.True(t, ok)
	ok, existsErr = store.Exists("other-empty")
	require.NoError(t, existsErr)
	require.False(t, ok)
}

func TestSessionsCommandPruneResumeFlagSkipsActiveSession(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	store := session.NewWorkspaceStore(configHome, workspace)
	_, err = store.Create("active-empty")
	require.NoError(t, err)
	_, err = store.Create("other-empty")
	require.NoError(t, err)
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "sessions", "prune", "--resume", "active-empty", "--confirm", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report session.PruneReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.DeletedCount)
	require.Equal(t, "other-empty", report.Deleted[0].ID)
	ok, existsErr := store.Exists("active-empty")
	require.NoError(t, existsErr)
	require.True(t, ok)
	ok, existsErr = store.Exists("other-empty")
	require.NoError(t, existsErr)
	require.False(t, ok)
}
