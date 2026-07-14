package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/anttrace"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/commandrun"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/cron"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/githubsetup"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/mocklimits"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/perfissue"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/prworkflow"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/team"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/thinkback"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
	"github.com/Rememorio/codog/internal/verifiers"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/Rememorio/codog/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestUnknownCommandOutputContract(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "statuz", "--not-a-command-option"}, config.FlagOverrides{})
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
	require.Equal(t, []string{"--not-a-command-option"}, report.Args)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "statuz", report.Error.Target)
	require.False(t, report.Error.Retryable)
	require.Contains(t, report.Hint, "status")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "foobar", "baz", "--not-a-command-option"}, config.FlagOverrides{})
	})
	require.Empty(t, out)
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.False(t, exitErr.Silent)
	require.Contains(t, err.Error(), "command_not_found")
	require.Contains(t, err.Error(), "codog prompt")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "no", "thanks", "--not-a-command-option"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "command_not_found", report.Kind)
	require.Equal(t, "no", report.Command)
	require.Equal(t, []string{"thanks", "--not-a-command-option"}, report.Args)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "no", report.Error.Target)
	require.Contains(t, report.Hint, "codog prompt")
}

func TestGlobalCWDFlagChangesWorkspaceAndConfigRoot(t *testing.T) {
	originalCWD, err := os.Getwd()
	require.NoError(t, err)
	for _, flagName := range []string{"--cwd", "-C", "--directory"} {
		t.Run(flagName, func(t *testing.T) {
			workspace := t.TempDir()
			configHome := t.TempDir()
			data, err := json.Marshal(map[string]string{
				"config_home": configHome,
				"model":       "cwd-model",
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), data, 0o644))

			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), []string{flagName, workspace, "--output-format", "json", "status"}, config.FlagOverrides{})
			})

			require.NoError(t, err)
			currentCWD, err := os.Getwd()
			require.NoError(t, err)
			require.Equal(t, originalCWD, currentCWD)
			var report struct {
				Workspace struct {
					Path string `json:"path"`
				} `json:"workspace"`
				Config struct {
					ConfigHome string `json:"config_home"`
					Model      string `json:"model"`
				} `json:"config"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			expectedWorkspace, err := filepath.EvalSymlinks(workspace)
			require.NoError(t, err)
			require.Equal(t, expectedWorkspace, report.Workspace.Path)
			require.Equal(t, configHome, report.Config.ConfigHome)
			require.Equal(t, "cwd-model", report.Config.Model)
		})
	}
}

func TestGlobalCWDInvalidPathJSONContract(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--cwd", missing, "--output-format", "json", "status"}, config.FlagOverrides{})
	})

	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "invalid_cwd", report.Kind)
	require.Equal(t, "invalid_cwd", report.ErrorKind)
	require.Equal(t, "error", report.Status)
	require.Equal(t, missing, report.Path)
	require.Contains(t, report.Hint, "--cwd")
}

func TestCLIErrorTypedEnvelopeMatrix(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cases := []struct {
		name      string
		err       error
		kind      string
		operation string
		target    string
		errno     string
		retryable bool
	}{
		{
			name:      "filesystem",
			err:       &os.PathError{Op: "write", Path: missing, Err: os.ErrNotExist},
			kind:      "filesystem",
			operation: "write",
			target:    missing,
			errno:     "ENOENT",
			retryable: true,
		},
		{
			name: "auth",
			err: anthropic.MissingCredentialsError{
				Provider: "anthropic",
				EnvVars:  []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
				Hint:     "export ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN",
			},
			kind:      "auth",
			operation: "resolve_anthropic_auth",
			target:    "ANTHROPIC_API_KEY",
		},
		{
			name:      "session",
			err:       fmt.Errorf("%w: missing-session", session.ErrSessionNotFound),
			kind:      "session",
			operation: "resolve_session_id",
			target:    "missing-session",
		},
		{
			name:      "parse",
			err:       promptJSONSchemaValidationError{Path: "$.answer", Reason: "must be a string"},
			kind:      "parse",
			operation: "parse",
			target:    "$.answer",
		},
		{
			name:      "usage",
			err:       requiredArgumentError{Command: "commit", Argument: "commit message", Usage: "codog commit MESSAGE"},
			kind:      "usage",
			operation: "parse_args",
			target:    "commit message",
		},
		{
			name:      "policy",
			err:       errors.New("invalid_permission_mode: unknown permission mode \"bogus\""),
			kind:      "policy",
			operation: "evaluate_policy",
		},
		{
			name:      "mcp",
			err:       errors.New("mcp initialize handshake failed"),
			kind:      "mcp",
			operation: "mcp",
			retryable: true,
		},
		{
			name:      "delivery",
			err:       errors.New("github pull request delivery failed"),
			kind:      "delivery",
			operation: "deliver",
			retryable: true,
		},
		{
			name:      "runtime",
			err:       errors.New("worker exited before startup completed"),
			kind:      "runtime",
			operation: "run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := buildCLIErrorReport(tc.err)
			require.Equal(t, "error", report.Type)
			require.Equal(t, "error", report.Status)
			require.NotEmpty(t, report.Message)
			require.NotEmpty(t, report.Error.Detail)
			require.Equal(t, tc.kind, report.Error.Kind)
			require.Equal(t, tc.operation, report.Error.Operation)
			require.Equal(t, tc.target, report.Error.Target)
			require.Equal(t, tc.errno, report.Error.Errno)
			require.Equal(t, tc.retryable, report.Error.Retryable)
		})
	}
}

func TestCLIErrorTypedEnvelopeJSONOutput(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "YAML", "status"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)

	var report cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "error", report.Type)
	require.Equal(t, "usage", report.Error.Kind)
	require.Equal(t, "parse_args", report.Error.Operation)
	require.Equal(t, "--output-format", report.Error.Target)
	require.Equal(t, report.Hint, report.Error.Hint)
	require.False(t, report.Error.Retryable)
}

func TestCLIErrorTextUsageTrailerOnlyForUsage(t *testing.T) {
	usageReport := buildCLIErrorReport(requiredArgumentError{Command: "commit", Argument: "commit message"})
	usageText := renderCLIErrorText(usageReport)
	require.Contains(t, usageText, "kind=usage")
	require.Contains(t, usageText, "Run `codog --help` for usage.")

	filesystemReport := buildCLIErrorReport(&os.PathError{Op: "write", Path: filepath.Join(t.TempDir(), "missing", "out.md"), Err: os.ErrNotExist})
	filesystemText := renderCLIErrorText(filesystemReport)
	require.Contains(t, filesystemText, "kind=filesystem")
	require.Contains(t, filesystemText, "errno=ENOENT")
	require.NotContains(t, filesystemText, "Run `codog --help` for usage.")
}

func TestCLIErrorSessionLookupEnvelopeIncludesNamespace(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	storeA := session.NewWorkspaceStore(configHome, workspaceA)
	storeB := session.NewWorkspaceStore(configHome, workspaceB)
	require.NoError(t, storeB.Append("other", anthropic.TextMessage("user", "from b")))

	_, err := storeA.LatestID()
	require.ErrorIs(t, err, session.ErrNoSessions)
	report := buildCLIErrorReport(err)
	require.Equal(t, "no_managed_sessions", report.ErrorKind)
	require.Equal(t, storeA.Dir, report.SessionSearchPath)
	require.Equal(t, storeA.Workspace, report.Workspace)
	require.Equal(t, session.WorkspaceFingerprint(storeA.Workspace), report.WorkspaceFingerprint)
	require.Equal(t, 1, report.OtherWorkspacePartitions)
	require.Equal(t, 1, report.OtherWorkspaceSessions)
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, storeA.Dir, report.Error.Target)
	require.Contains(t, report.Hint, "current workspace session namespace")
	require.Contains(t, report.Hint, "other workspace partition")
}

func TestSessionRestoreErrorReportIncludesLookupNamespace(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	_, err := store.OpenExisting("missing-session")
	require.ErrorIs(t, err, session.ErrSessionNotFound)

	report := buildSessionRestoreErrorReport("status", "missing-session", err)
	require.Equal(t, "session_not_found", report.ErrorKind)
	require.Equal(t, "missing-session", report.RequestedSession)
	require.Equal(t, store.Dir, report.SessionSearchPath)
	require.Equal(t, store.Workspace, report.Workspace)
	require.Equal(t, session.WorkspaceFingerprint(store.Workspace), report.WorkspaceFingerprint)
	require.Contains(t, report.Hint, "current workspace session namespace")
	require.Equal(t, "session", report.Error.Kind)
	require.Equal(t, "resolve_session_id", report.Error.Operation)
	require.Equal(t, "missing-session", report.Error.Target)
	require.False(t, report.Error.Retryable)
	require.Contains(t, report.Error.Hint, "current workspace session namespace")

	cliReport := buildCLIErrorReport(err)
	require.Equal(t, `session "missing-session" was not found`, cliReport.Message)
	require.Equal(t, "missing-session", cliReport.Error.Target)
	require.NotContains(t, cliReport.Message, "searched")
}

func TestApprovalSlashAliasesReturnInteractiveOnly(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	cases := []struct {
		name    string
		command string
		slash   string
	}{
		{name: "bare approve", command: "approve", slash: "/approve"},
		{name: "bare yes", command: "yes", slash: "/yes"},
		{name: "bare y", command: "y", slash: "/y"},
		{name: "bare deny", command: "deny", slash: "/deny"},
		{name: "bare no", command: "no", slash: "/no"},
		{name: "bare n", command: "n", slash: "/n"},
		{name: "slash approve", command: "/approve", slash: "/approve"},
		{name: "slash yes", command: "/yes", slash: "/yes"},
		{name: "slash y", command: "/y", slash: "/y"},
		{name: "slash deny", command: "/deny", slash: "/deny"},
		{name: "slash no", command: "/no", slash: "/no"},
		{name: "slash n", command: "/n", slash: "/n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", tc.command}, config.FlagOverrides{})
			})
			require.Error(t, err)
			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 1, exitErr.Code)
			require.True(t, exitErr.Silent)
			var report slashErrorReport
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, "interactive_only", report.Kind)
			require.Equal(t, "interactive_only", report.ErrorKind)
			require.Equal(t, "error", report.Status)
			require.Equal(t, tc.slash, report.Command)
			require.Contains(t, report.Hint, "codog repl")
			require.NotContains(t, out, "command_not_found")
		})
	}
}

func TestMockParityCommandAndHelp(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "reports", "mock-parity.json")
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"mock-parity", "--json", "--report", reportPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report harness.Report
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, harness.ReportSchemaVersion, report.SchemaVersion)
	require.True(t, report.OK, failedMockParityScenarioSummaries(report))
	require.Equal(t, report.Total, report.Passed)
	require.Equal(t, report.Total, report.ScenarioCount)
	require.Greater(t, report.RequestCount, 0)
	require.GreaterOrEqual(t, report.Total, 12)
	require.NotEmpty(t, report.Scenarios)
	require.NotEmpty(t, report.Coverage)
	require.NotEmpty(t, report.CapabilityCoverage)
	require.Equal(t, report.Total, mockParityCoverageTotal(report.Coverage))
	fileToolsCapability := findMockParityCapability(t, report.CapabilityCoverage, "file tools")
	require.Equal(t, "passing", fileToolsCapability.Status)
	require.Contains(t, fileToolsCapability.CoveredRefs, "File tools")
	tuiCapability := findMockParityCapability(t, report.CapabilityCoverage, "TUI and interactive rendering")
	require.Equal(t, "passing", tuiCapability.Status)
	require.Contains(t, tuiCapability.Scenarios, "tui_prompt_completion_roundtrip")
	readFile := findMockParityScenario(t, report, "read_file_roundtrip")
	require.Equal(t, "file-tools", readFile.Category)
	require.NotEmpty(t, readFile.Description)
	require.Contains(t, readFile.ParityRefs, "File tools")
	require.Equal(t, []string{"read_file"}, readFile.ToolUses)
	require.Equal(t, "codog harness ok", readFile.FinalMessage)
	require.Greater(t, report.UsageSummary.TotalTokens, 0)

	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var persisted harness.Report
	require.NoError(t, json.Unmarshal(reportData, &persisted))
	require.Equal(t, harness.ReportSchemaVersion, persisted.SchemaVersion)
	require.Equal(t, report.Total, persisted.Total)
	require.Equal(t, report.ScenarioCount, persisted.ScenarioCount)
	require.Equal(t, report.RequestCount, persisted.RequestCount)
	require.Equal(t, readFile.FinalMessage, findMockParityScenario(t, persisted, "read_file_roundtrip").FinalMessage)

	var text bytes.Buffer
	renderMockParityText(&text, harness.Report{
		OK:            true,
		SchemaVersion: harness.ReportSchemaVersion,
		Passed:        1,
		Total:         1,
		ScenarioCount: 1,
		RequestCount:  1,
		Coverage:      []harness.CategoryReport{{Category: "baseline", OK: true, Passed: 1, Total: 1, Scenarios: []string{"streaming_text"}}},
		CapabilityCoverage: []harness.CapabilityCoverage{
			{Capability: "one-shot prompt and streaming", Status: "passing", Scenarios: []string{"streaming_text"}},
			{Capability: "TUI and interactive rendering", Status: "passing", Scenarios: []string{"tui_prompt_completion_roundtrip"}},
		},
		ToolCalls:     2,
		MessageCount:  3,
		UsageSummary:  usage.Summary{TotalTokens: 42},
		EstimatedCost: 0.001,
		Scenarios:     []harness.ScenarioReport{{Name: "streaming_text", Category: "baseline", Description: "Validates streamed text.", OK: true}},
	})
	require.Contains(t, text.String(), "Mock Parity Harness")
	require.Contains(t, text.String(), "Schema        "+harness.ReportSchemaVersion)
	require.Contains(t, text.String(), "1/1 passed")
	require.Contains(t, text.String(), "Coverage      1 categories")
	require.Contains(t, text.String(), "Capabilities  2/2 passing")
	require.Contains(t, text.String(), "Requests      1")
	require.Contains(t, text.String(), "streaming_text [baseline] - Validates streamed text.: ok")

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"mock-parity", "manifest", "--json", "--report", manifestPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var manifest harness.Manifest
	require.NoError(t, json.Unmarshal([]byte(out), &manifest))
	require.Equal(t, harness.ManifestSchemaVersion, manifest.SchemaVersion)
	require.Equal(t, report.Total, manifest.ScenarioCount)
	require.NotEmpty(t, manifest.CapabilityCoverage)
	require.Equal(t, "mapped", findMockParityCapability(t, manifest.CapabilityCoverage, "file tools").Status)
	require.Equal(t, "mapped", findMockParityCapability(t, manifest.CapabilityCoverage, "TUI and interactive rendering").Status)
	require.Equal(t, "read_file_roundtrip", findMockParityManifestScenario(t, manifest, "read_file_roundtrip").Name)
	manifestData, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var persistedManifest harness.Manifest
	require.NoError(t, json.Unmarshal(manifestData, &persistedManifest))
	require.Equal(t, manifest.ScenarioCount, persistedManifest.ScenarioCount)

	text.Reset()
	renderMockParityManifestText(&text, harness.Manifest{
		SchemaVersion: harness.ManifestSchemaVersion,
		ScenarioCount: 1,
		Categories:    []harness.ManifestCategory{{Category: "baseline", Count: 1, Scenarios: []string{"streaming_text"}}},
		CapabilityCoverage: []harness.CapabilityCoverage{
			{Capability: "one-shot prompt and streaming", Status: "mapped", Scenarios: []string{"streaming_text"}},
			{Capability: "TUI and interactive rendering", Status: "mapped", Scenarios: []string{"tui_prompt_completion_roundtrip"}},
		},
		Scenarios: []harness.ManifestScenario{{Name: "streaming_text", Category: "baseline", Description: "Validates streamed text."}},
	})
	require.Contains(t, text.String(), "Mock Parity Manifest")
	require.Contains(t, text.String(), "Schema        "+harness.ManifestSchemaVersion)
	require.Contains(t, text.String(), "Scenarios     1")
	require.Contains(t, text.String(), "Capabilities  2/2 mapped")
	require.Contains(t, text.String(), "streaming_text [baseline] - Validates streamed text.")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"mock-parity", "--help", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var help helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &help))
	require.Equal(t, "mock-parity", help.Topic)
	require.Contains(t, help.Aliases, "parity")
	require.Contains(t, help.Aliases, "self-test")
	require.Contains(t, help.Usage, "manifest")
	require.Contains(t, help.OutputFields, "schema_version")
	require.Contains(t, help.OutputFields, "request_count")
	require.Contains(t, help.OutputFields, "scenario_count")
	require.Contains(t, help.OutputFields, "coverage")
	require.Contains(t, help.OutputFields, "capability_coverage")
	require.NotNil(t, help.RequiresProviderRequest)
	require.False(t, *help.RequiresProviderRequest)
	require.True(t, commandAcceptsGlobalOutputFormat("mock-parity"))
}

func TestMockParityResumedSlashContracts(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("resume-parity", anthropic.TextMessage("user", "run parity manifest")))

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-parity", "--output-format", "json", "/mock-parity", "manifest"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var manifest harness.Manifest
	require.NoError(t, json.Unmarshal([]byte(out), &manifest))
	require.Equal(t, harness.ManifestSchemaVersion, manifest.SchemaVersion)
	require.Equal(t, harness.ScenarioManifest().ScenarioCount, manifest.ScenarioCount)
	require.Equal(t, "streaming_text", manifest.Scenarios[0].Name)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-parity", "/self-test", "manifest"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &manifest))
	require.Equal(t, harness.ManifestSchemaVersion, manifest.SchemaVersion)
}

func TestMockParityInteractiveSlashContracts(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session-parity"}

	require.True(t, app.handleSlash(context.Background(), "/mock-parity manifest --json", sess))
	var manifest harness.Manifest
	require.NoError(t, json.Unmarshal(out.Bytes(), &manifest))
	require.Equal(t, harness.ManifestSchemaVersion, manifest.SchemaVersion)
	require.Equal(t, harness.ScenarioManifest().ScenarioCount, manifest.ScenarioCount)
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/SELF-TEST manifest", sess))
	require.NoError(t, json.Unmarshal(out.Bytes(), &manifest))
	require.Equal(t, harness.ManifestSchemaVersion, manifest.SchemaVersion)
	require.Empty(t, errOut.String())
}

func mockParityCoverageTotal(coverage []harness.CategoryReport) int {
	total := 0
	for _, category := range coverage {
		total += category.Total
	}
	return total
}

func findMockParityScenario(t *testing.T, report harness.Report, name string) harness.ScenarioReport {
	t.Helper()
	for _, scenario := range report.Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("missing mock parity scenario %q in %#v", name, report.Scenarios)
	return harness.ScenarioReport{}
}

func findMockParityManifestScenario(t *testing.T, manifest harness.Manifest, name string) harness.ManifestScenario {
	t.Helper()
	for _, scenario := range manifest.Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("missing mock parity manifest scenario %q in %#v", name, manifest.Scenarios)
	return harness.ManifestScenario{}
}

func findMockParityCapability(t *testing.T, coverage []harness.CapabilityCoverage, capability string) harness.CapabilityCoverage {
	t.Helper()
	for _, item := range coverage {
		if item.Capability == capability {
			return item
		}
	}
	t.Fatalf("missing mock parity capability %q in %#v", capability, coverage)
	return harness.CapabilityCoverage{}
}

func TestParseMockParityReportPath(t *testing.T) {
	t.Setenv("MOCK_PARITY_REPORT_PATH", "from-env.json")
	req, err := parseMockParityArgs([]string{"check", "--output-format=json"}, "", "text")
	require.NoError(t, err)
	require.Equal(t, "run", req.Action)
	require.Equal(t, "json", req.Format)
	require.Equal(t, "from-env.json", req.ReportPath)

	req, err = parseMockParityArgs([]string{"--report", "from-flag.json"}, "", "text")
	require.NoError(t, err)
	require.Equal(t, "run", req.Action)
	require.Equal(t, "text", req.Format)
	require.Equal(t, "from-flag.json", req.ReportPath)

	req, err = parseMockParityArgs([]string{"manifest", "--json"}, "", "text")
	require.NoError(t, err)
	require.Equal(t, "manifest", req.Action)
	require.Equal(t, "json", req.Format)

	_, err = parseMockParityArgs([]string{"--report"}, "", "text")
	var missing missingFlagValueError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "--report", missing.Flag)
}

func TestMockParityErrorsHonorGlobalJSONFormat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		kind      string
		errorKind string
		contains  []string
	}{
		{
			name:      "unknown argument",
			args:      []string{"mock-parity", "bogus"},
			kind:      "unexpected_extra_args",
			errorKind: "unexpected_extra_args",
			contains:  []string{`"command": "mock-parity"`, `"bogus"`},
		},
		{
			name:      "missing output format",
			args:      []string{"mock-parity", "--output-format"},
			kind:      "missing_flag_value",
			errorKind: "missing_flag_value",
			contains:  []string{`"command": "mock-parity"`, `"option": "--output-format"`},
		},
		{
			name:      "invalid output format",
			args:      []string{"mock-parity", "--output-format", "yaml"},
			kind:      "invalid_output_format",
			errorKind: "invalid_output_format",
			contains:  []string{`"value": "yaml"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				args := append([]string{"--output-format", "json"}, tc.args...)
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), tc.kind, tc.errorKind)
			for _, expected := range tc.contains {
				require.Contains(t, out, expected)
			}
		})
	}
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
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/STATUS"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &statusReport))
	require.Equal(t, "status", statusReport["kind"])

	terminalProfilePath := filepath.Join(t.TempDir(), ".zshrc")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/TERMINALSETUP", "status", "--shell", "zsh", "--path", terminalProfilePath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var terminalReport terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &terminalReport))
	require.Equal(t, "terminal_setup", terminalReport.Kind)
	require.Equal(t, "status", terminalReport.Action)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/rc", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var bridgeReport ideReport
	require.NoError(t, json.Unmarshal([]byte(out), &bridgeReport))
	require.Equal(t, "ide", bridgeReport.Kind)
	require.Equal(t, "status", bridgeReport.Action)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/remote-control", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &bridgeReport))
	require.Equal(t, "ide", bridgeReport.Kind)
	require.Equal(t, "status", bridgeReport.Action)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/app", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var handoffStatus handoffStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &handoffStatus))
	require.Equal(t, "handoff_status", handoffStatus.Kind)
	require.Equal(t, "desktop", handoffStatus.Surface)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/ios", "status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &handoffStatus))
	require.Equal(t, "handoff_status", handoffStatus.Kind)
	require.Equal(t, "mobile", handoffStatus.Surface)
	require.Equal(t, "ios", handoffStatus.Platform)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/exit_plan_mode"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var exitPlanReport planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &exitPlanReport))
	require.Equal(t, "plan", exitPlanReport.Kind)
	require.Equal(t, "exit", exitPlanReport.Action)

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

	modelConfigPath := filepath.Join(t.TempDir(), "single-model.json")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "model", "opus", "--path", modelConfigPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &setModel))
	require.Equal(t, "set", setModel.Action)
	require.Equal(t, "opus", setModel.Model)
	modelConfigData, err := os.ReadFile(modelConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(modelConfigData), `"model": "opus"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "model", "reset", "--path", modelConfigPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var clearModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &clearModel))
	require.Equal(t, "model", clearModel.Kind)
	require.Equal(t, "clear", clearModel.Action)
	require.Equal(t, config.DefaultModel, clearModel.Model)
	require.True(t, clearModel.Cleared)
	modelConfigData, err = os.ReadFile(modelConfigPath)
	require.NoError(t, err)
	require.NotContains(t, string(modelConfigData), `"model"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "models"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var models modelsReport
	require.NoError(t, json.Unmarshal([]byte(out), &models))
	require.Equal(t, "models", models.Kind)
	require.Equal(t, "list", models.Action)
	require.Equal(t, "ok", models.Status)
	require.Equal(t, config.DefaultModel, models.DefaultModel)
	require.NotEmpty(t, models.Aliases)
	require.True(t, modelAliasExists(models.Aliases, "kimi", "kimi-k2.5", modelrouting.ProviderDashScope))
	require.True(t, modelAliasExists(models.Aliases, "grok", "grok-3", modelrouting.ProviderXAI))
	require.True(t, modelAliasExists(models.Aliases, "grok-mini", "grok-3-mini", modelrouting.ProviderXAI))
	require.True(t, modelAliasExists(models.Aliases, "opus", "claude-opus-4-7", modelrouting.ProviderAnthropic))
	require.NotEmpty(t, models.Routes)
	require.True(t, modelRouteExists(models.Routes, "openai/", modelrouting.ProviderOpenAI))
	require.True(t, modelRouteExists(models.Routes, "local/", modelrouting.ProviderOpenAI))
	require.True(t, modelRouteExists(models.Routes, "glm or glm/", modelrouting.ProviderOpenAI))
	require.True(t, modelRouteExists(models.Routes, "grok or xai/", modelrouting.ProviderXAI))
	require.True(t, modelRouteExists(models.Routes, "qwen/ or qwen-", modelrouting.ProviderDashScope))
	require.True(t, modelRouteExists(models.Routes, "kimi/ or kimi-", modelrouting.ProviderDashScope))
	require.False(t, models.RequiresCredentials)
	require.False(t, models.RequiresProviderRequest)
	require.True(t, models.LocalOnly)
	require.True(t, commandAcceptsGlobalOutputFormat("models"))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "aliases", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var aliases modelAliasesInventoryReport
	require.NoError(t, json.Unmarshal([]byte(out), &aliases))
	require.Equal(t, "models", aliases.Kind)
	require.Equal(t, "aliases", aliases.Action)
	require.Equal(t, "ok", aliases.Status)
	require.Equal(t, len(aliases.Aliases), aliases.Count)
	require.True(t, modelAliasExists(aliases.Aliases, "kimi", "kimi-k2.5", modelrouting.ProviderDashScope))
	require.False(t, aliases.RequiresProviderRequest)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "routes", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var routes modelRoutesInventoryReport
	require.NoError(t, json.Unmarshal([]byte(out), &routes))
	require.Equal(t, "models", routes.Kind)
	require.Equal(t, "routes", routes.Action)
	require.Equal(t, "ok", routes.Status)
	require.Equal(t, len(routes.Routes), routes.Count)
	require.True(t, modelRouteExists(routes.Routes, "glm or glm/", modelrouting.ProviderOpenAI))
	require.True(t, modelRouteExists(routes.Routes, "qwen/ or qwen-", modelrouting.ProviderDashScope))
	require.False(t, routes.RequiresProviderRequest)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "search", "kimi", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var search modelSearchReport
	require.NoError(t, json.Unmarshal([]byte(out), &search))
	require.Equal(t, "models", search.Kind)
	require.Equal(t, "search", search.Action)
	require.Equal(t, "ok", search.Status)
	require.Equal(t, "kimi", search.Query)
	require.GreaterOrEqual(t, search.MatchCount, 3)
	require.True(t, modelAliasExists(search.Aliases, "kimi", "kimi-k2.5", modelrouting.ProviderDashScope))
	require.True(t, modelRouteExists(search.Routes, "kimi/ or kimi-", modelrouting.ProviderDashScope))
	require.NotEmpty(t, search.Models)
	require.Equal(t, "kimi-k2.5", search.Models[0].ResolvedModel)
	require.False(t, search.RequiresProviderRequest)

	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:9090/v1")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "show", "glm52", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var glmDetail modelDetailReport
	require.NoError(t, json.Unmarshal([]byte(out), &glmDetail))
	require.Equal(t, "glm52", glmDetail.RequestedModel)
	require.Equal(t, "glm52", glmDetail.ResolvedModel)
	require.Equal(t, modelrouting.ProviderOpenAI, glmDetail.Provider)
	require.Equal(t, "openai_chat_completions", glmDetail.WireProtocol)
	require.Equal(t, "http://127.0.0.1:9090/v1", glmDetail.BaseURL)
	require.Equal(t, "glm52", glmDetail.WireModel)
	require.True(t, glmDetail.OpenAICompatible)
	require.False(t, glmDetail.RequiresProviderRequest)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "search", "glm52", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var glmSearch modelSearchReport
	require.NoError(t, json.Unmarshal([]byte(out), &glmSearch))
	require.Equal(t, "glm52", glmSearch.Query)
	require.NotEmpty(t, glmSearch.Models)
	require.Equal(t, "glm52", glmSearch.Models[0].ResolvedModel)
	require.Equal(t, modelrouting.ProviderOpenAI, glmSearch.Models[0].Provider)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "models", "search"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var searchError actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &searchError))
	require.Equal(t, "models", searchError.Kind)
	require.Equal(t, "search", searchError.Action)
	require.Equal(t, "missing_argument", searchError.ErrorKind)
	require.Equal(t, "query", searchError.Argument)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "show", "kimi", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var kimiDetail modelDetailReport
	require.NoError(t, json.Unmarshal([]byte(out), &kimiDetail))
	require.Equal(t, "models", kimiDetail.Kind)
	require.Equal(t, "show", kimiDetail.Action)
	require.Equal(t, "ok", kimiDetail.Status)
	require.Equal(t, "kimi", kimiDetail.RequestedModel)
	require.Equal(t, "kimi-k2.5", kimiDetail.ResolvedModel)
	require.Equal(t, "kimi", kimiDetail.Alias)
	require.Equal(t, modelrouting.ProviderDashScope, kimiDetail.Provider)
	require.Equal(t, "openai_chat_completions", kimiDetail.WireProtocol)
	require.Equal(t, "kimi-k2.5", kimiDetail.WireModel)
	require.True(t, kimiDetail.OpenAICompatible)
	require.True(t, kimiDetail.RejectsToolResultIsErrorField)
	require.False(t, kimiDetail.RequiresProviderRequest)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "current", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var currentDetail modelDetailReport
	require.NoError(t, json.Unmarshal([]byte(out), &currentDetail))
	require.Equal(t, "show", currentDetail.Action)
	require.NotEmpty(t, currentDetail.RequestedModel)
	require.Equal(t, currentDetail.ResolvedModel, currentDetail.WireModel)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "model", "help", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var modelHelp helpReport
	require.NoError(t, json.Unmarshal([]byte(out), &modelHelp))
	require.Equal(t, "models", modelHelp.Topic)
	require.Equal(t, "models", modelHelp.Command)
	require.Contains(t, modelHelp.Usage, "aliases")
	require.Contains(t, modelHelp.Usage, "routes")
	require.Contains(t, modelHelp.Usage, "search")
	require.Contains(t, modelHelp.Usage, "show")
	require.NotNil(t, modelHelp.RequiresProviderRequest)
	require.False(t, *modelHelp.RequiresProviderRequest)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "models", "serch", "claude"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var modelsError actionErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &modelsError))
	require.Equal(t, "models", modelsError.Kind)
	require.Equal(t, "serch", modelsError.Action)
	require.Equal(t, "unsupported_models_action", modelsError.ErrorKind)
	require.Contains(t, modelsError.Hint, "Did you mean `codog models search`?")

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
	require.Equal(t, "usage", slashReport.Error.Kind)
	require.Equal(t, "parse_args", slashReport.Error.Operation)
	require.Equal(t, "/statuz", slashReport.Error.Target)
	require.False(t, slashReport.Error.Retryable)
	require.Contains(t, slashReport.Suggestions, "/status")
	require.Contains(t, slashReport.Suggestions, "/stats")
	require.Empty(t, slashReport.CompatibilityNote)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/oh-my-claudecode:hud"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "unknown_slash_command", slashReport.ErrorKind)
	require.Equal(t, "/oh-my-claudecode:hud", slashReport.Command)
	require.Contains(t, slashReport.CompatibilityNote, "loads compatible Markdown commands")
	require.Contains(t, slashReport.CompatibilityNote, ".omc/settings.json")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/approve"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "interactive_only", slashReport.ErrorKind)
	require.Equal(t, "/approve", slashReport.Command)
	require.Equal(t, "usage", slashReport.Error.Kind)
	require.Equal(t, "parse_args", slashReport.Error.Operation)
	require.Equal(t, "/approve", slashReport.Error.Target)
	require.NotContains(t, slashReport.Hint, "--resume")

	newConfigPath := filepath.Join(t.TempDir(), "config.json")
	newConfigData, err := json.Marshal(map[string]string{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(newConfigPath, newConfigData, 0o644))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", newConfigPath, "--output-format", "json", "/new"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var newReport clearCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &newReport))
	require.Equal(t, "clear", newReport.Kind)
	require.Equal(t, "create_session", newReport.Action)
	require.Equal(t, "ok", newReport.Status)
	require.NotEmpty(t, newReport.SessionID)
	require.Equal(t, 0, newReport.MessageCount)
	require.NotEmpty(t, newReport.Path)
	require.Contains(t, newReport.ContinueCommands[0], "--session")

	for _, command := range []string{"/exit", "/quit"} {
		out, err = captureStdout(t, func() error {
			return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", command}, config.FlagOverrides{})
		})
		require.NoError(t, err, command)
		var exitReport simpleCompatibilityReport
		require.NoError(t, json.Unmarshal([]byte(out), &exitReport), command)
		require.Equal(t, "exit", exitReport.Kind, command)
		require.Equal(t, "exit", exitReport.Action, command)
		require.Equal(t, "ok", exitReport.Status, command)
		require.False(t, exitReport.ProviderRequestMade, command)
		require.False(t, exitReport.WorkspaceWillMutate, command)
	}

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "quit"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var quitReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal([]byte(out), &quitReport))
	require.Equal(t, "exit", quitReport.Kind)
	require.Equal(t, "ok", quitReport.Status)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/commit"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var commitError cliErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &commitError))
	require.Equal(t, "missing_argument", commitError.ErrorKind)
	require.Equal(t, "commit", commitError.Command)
	require.Equal(t, "commit message", commitError.Argument)
	require.Contains(t, commitError.Hint, "codog commit")
	require.NotContains(t, out, "config_load_failed")

	_, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/compact"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no saved sessions")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	store := session.NewWorkspaceStore(configHome, cwd)
	require.NoError(t, store.Append("direct-compact", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("direct-compact", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("direct-compact", anthropic.TextMessage("user", "three")))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/compact", "--session", "direct-compact", "--keep", "1"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var compactReport session.ReplaceResult
	require.NoError(t, json.Unmarshal([]byte(out), &compactReport))
	require.Equal(t, "direct-compact", compactReport.SessionID)
	require.Equal(t, 3, compactReport.OriginalMessages)
	require.Equal(t, 2, compactReport.RemainingMessages)
	require.Equal(t, 1, compactReport.RemovedMessages)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/clear"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var clearReport clearCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &clearReport))
	require.Equal(t, "clear", clearReport.Kind)
	require.Equal(t, "create_session", clearReport.Action)
	require.Equal(t, "ok", clearReport.Status)
	require.NotEmpty(t, clearReport.SessionID)
	require.NotEmpty(t, clearReport.Path)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/ultraplan", "inspect", "release"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var directUltraPlan planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &directUltraPlan))
	require.Equal(t, "plan", directUltraPlan.Kind)
	require.Equal(t, "enter", directUltraPlan.Action)
	require.True(t, directUltraPlan.State.Active)
	require.Equal(t, "inspect release", directUltraPlan.State.Plan)
}

func TestBridgeFaultsAliasRecordsListsAndClearsDiagnostics(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "bridge", "faults", "record", "latency", "250ms", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var recorded bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &recorded))
	require.Equal(t, "bridge_kick", recorded.Kind)
	require.Equal(t, "latency", recorded.Action)
	require.NotNil(t, recorded.Recorded)
	require.Equal(t, "latency", recorded.Recorded.Action)
	require.Equal(t, []string{"250ms"}, recorded.Recorded.Args)
	require.Equal(t, "warn", recorded.Recorded.Severity)
	require.Len(t, recorded.Faults, 1)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "bridge", "faults", "list", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var listed bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &listed))
	require.Equal(t, "status", listed.Action)
	require.Len(t, listed.Faults, 1)
	require.Equal(t, "latency", listed.Faults[0].Action)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "bridge", "faults", "clear", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var cleared bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &cleared))
	require.Equal(t, "clear", cleared.Action)
	require.True(t, cleared.Cleared)
	require.Empty(t, cleared.Faults)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestSlashCommandNameCanonicalizesAliases(t *testing.T) {
	for input, expected := range map[string]string{
		"/app":                 "desktop",
		"/remote-control":      "bridge",
		"/rc":                  "bridge",
		"/terminalSetup":       "terminal-setup",
		"/pr_comments":         "pr-comments",
		"/web-setup":           "remote-setup",
		"/color":               "theme",
		"/caches":              "cache",
		"/stats":               "stats",
		"/tokens":              "tokens",
		"/thinkback":           "think-back",
		"/thinkback-play":      "think-back",
		"/parity":              "mock-parity",
		"/branchlock":          "branch-lock",
		"/base-check":          "stale-base",
		"/green":               "green-contract",
		"/g004":                "g004-conformance",
		"/prompt-history":      "history",
		"/continue":            "resume",
		"/reviewRemote":        "reviewRemote",
		"/review-remote":       "reviewRemote",
		"/cwd":                 "workspace",
		"/session":             "sessions",
		"/sessions":            "sessions",
		"/generateSessionName": "generateSessionName",
		"/exit_plan_mode":      "exit-plan",
	} {
		require.Equal(t, expected, slashCommandName(input), input)
		require.Equal(t, "/"+strings.ToLower(expected), slashSwitchName(slashCommandName(input)), input)
	}
}

func TestModelsCommandActionAliases(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "catalog", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var models modelsReport
	require.NoError(t, json.Unmarshal([]byte(out), &models))
	require.Equal(t, "models", models.Kind)
	require.Equal(t, "list", models.Action)
	require.NotEmpty(t, models.Aliases)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "shortcuts", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var aliases modelAliasesInventoryReport
	require.NoError(t, json.Unmarshal([]byte(out), &aliases))
	require.Equal(t, "aliases", aliases.Action)
	require.True(t, modelAliasExists(aliases.Aliases, "kimi", "kimi-k2.5", modelrouting.ProviderDashScope))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "routing", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var routes modelRoutesInventoryReport
	require.NoError(t, json.Unmarshal([]byte(out), &routes))
	require.Equal(t, "routes", routes.Action)
	require.True(t, modelRouteExists(routes.Routes, "qwen/ or qwen-", modelrouting.ProviderDashScope))

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "lookup", "kimi", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var search modelSearchReport
	require.NoError(t, json.Unmarshal([]byte(out), &search))
	require.Equal(t, "search", search.Action)
	require.Equal(t, "kimi", search.Query)
	require.NotEmpty(t, search.Models)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "view", "kimi", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var detail modelDetailReport
	require.NoError(t, json.Unmarshal([]byte(out), &detail))
	require.Equal(t, "show", detail.Action)
	require.Equal(t, "kimi", detail.RequestedModel)
	require.Equal(t, "kimi-k2.5", detail.ResolvedModel)

	modelPath := filepath.Join(t.TempDir(), "model.json")
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "set", "opus", "--path", modelPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var setModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &setModel))
	require.Equal(t, "model", setModel.Kind)
	require.Equal(t, "set", setModel.Action)
	require.Equal(t, "opus", setModel.Model)
	require.Equal(t, modelPath, setModel.Path)
	modelData, err := os.ReadFile(modelPath)
	require.NoError(t, err)
	require.Contains(t, string(modelData), `"model": "opus"`)

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "clear", "--path", modelPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var clearModel modelReport
	require.NoError(t, json.Unmarshal([]byte(out), &clearModel))
	require.Equal(t, "model", clearModel.Kind)
	require.Equal(t, "clear", clearModel.Action)
	require.Equal(t, config.DefaultModel, clearModel.Model)
	require.Equal(t, config.DefaultModel, clearModel.Previous)
	require.True(t, clearModel.Cleared)
	modelData, err = os.ReadFile(modelPath)
	require.NoError(t, err)
	require.NotContains(t, string(modelData), `"model"`)

	var slashOut bytes.Buffer
	slashPath := filepath.Join(t.TempDir(), "slash-model.json")
	app := &App{
		Config: config.Config{ConfigHome: configHome},
		Out:    &slashOut,
		Err:    io.Discard,
	}
	require.True(t, app.handleSlash(context.Background(), "/models get kimi --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(slashOut.Bytes(), &detail))
	require.Equal(t, "show", detail.Action)
	require.Equal(t, "kimi-k2.5", detail.ResolvedModel)

	slashOut.Reset()
	require.True(t, app.handleSlash(context.Background(), "/models set grok --path "+slashPath+" --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(slashOut.Bytes(), &setModel))
	require.Equal(t, "set", setModel.Action)
	require.Equal(t, "grok", setModel.Model)
	require.Equal(t, slashPath, setModel.Path)
	require.Equal(t, "grok", app.Config.Model)
	slashData, err := os.ReadFile(slashPath)
	require.NoError(t, err)
	require.Contains(t, string(slashData), `"model": "grok"`)

	slashOut.Reset()
	require.True(t, app.handleSlash(context.Background(), "/models reset --path "+slashPath+" --json", &session.Session{ID: "session"}))
	require.NoError(t, json.Unmarshal(slashOut.Bytes(), &clearModel))
	require.Equal(t, "clear", clearModel.Action)
	require.Equal(t, config.DefaultModel, clearModel.Model)
	require.Equal(t, "grok", clearModel.Previous)
	require.True(t, clearModel.Cleared)
	require.Equal(t, config.DefaultModel, app.Config.Model)
	slashData, err = os.ReadFile(slashPath)
	require.NoError(t, err)
	require.NotContains(t, string(slashData), `"model"`)
}

func TestModelsShowReportsProviderDiagnostics(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OPENAI_BASE_URL", modelrouting.DefaultOpenAIBaseURL)
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":      configHome,
		"temperature":      0.4,
		"reasoning_effort": "high",
		"extra_body": map[string]any{
			"parallel_tool_calls": false,
			"model":               "bad-override",
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "show", "openai/o4-mini", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var detail modelDetailReport
	require.NoError(t, json.Unmarshal([]byte(out), &detail))
	require.Equal(t, "openai/o4-mini", detail.RequestedModel)
	require.Equal(t, "o4-mini", detail.WireModel)
	require.True(t, detail.OpenAICompatible)
	require.True(t, detail.ReasoningModel)
	require.True(t, detail.StripsTuningParams)
	require.True(t, detail.SupportsStreamUsage)
	require.True(t, detail.SupportsExtraBodyParams)
	require.ElementsMatch(t, []string{"model", "parallel_tool_calls"}, detail.ExtraBodyKeys)
	require.ElementsMatch(t, []string{"parallel_tool_calls"}, detail.ExtraBodyForwardedKeys)
	require.ElementsMatch(t, []string{"model"}, detail.ExtraBodyIgnoredKeys)
	require.Contains(t, providerDiagnosticCodes(detail.Diagnostics), "reasoning_model_fixed_sampling")
	require.Contains(t, providerDiagnosticCodes(detail.Diagnostics), "extra_body_keys_ignored")
	require.NotContains(t, providerDiagnosticCodes(detail.Diagnostics), "reasoning_effort_unsupported")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "models", "show", "openai/deepseek-v4-pro", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &detail))
	require.True(t, detail.RequiresReasoningContentHistory)
	require.Contains(t, providerDiagnosticCodes(detail.Diagnostics), "reasoning_history_required")
}

func TestDirectSlashSuggestsProjectCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands", "team"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "team", "review.md"), []byte("Review $ARGUMENTS"), 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/team/reveiw"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.True(t, exitErr.Silent)
	var slashReport slashErrorReport
	require.NoError(t, json.Unmarshal([]byte(out), &slashReport))
	require.Equal(t, "unknown_slash_command", slashReport.ErrorKind)
	require.Equal(t, "/team/reveiw", slashReport.Command)
	require.Equal(t, "usage", slashReport.Error.Kind)
	require.Equal(t, "parse_args", slashReport.Error.Operation)
	require.Equal(t, "/team/reveiw", slashReport.Error.Target)
	require.Contains(t, slashReport.Suggestions, "/team/review")
}

func TestResumedSlashCLIContracts(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("CODOG_OAUTH_STORAGE", "file")
	t.Cleanup(func() {
		store := background.NewStore(configHome)
		tasks, err := store.List()
		if err != nil {
			return
		}
		for _, task := range tasks {
			if task.Status == "running" {
				_, _ = store.Stop(task.ID)
			}
		}
	})
	externalContext := filepath.Join(t.TempDir(), "external-context")
	require.NoError(t, os.MkdirAll(externalContext, 0o755))
	externalContext, err := filepath.EvalSymlinks(externalContext)
	require.NoError(t, err)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", "")
	var oauthServer *httptest.Server
	oauthRevoked := []string{}
	oauthServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"authorization_endpoint":"` + oauthServer.URL + `/authorize","token_endpoint":"` + oauthServer.URL + `/token","device_authorization_endpoint":"` + oauthServer.URL + `/device","revocation_endpoint":"` + oauthServer.URL + `/revoke"}`))
		case "/device":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-resume", r.Form.Get("client_id"))
			require.Equal(t, "profile", r.Form.Get("scope"))
			_, _ = w.Write([]byte(`{"device_code":"resume-device-1","user_code":"ABCD-EFGH","verification_uri":"` + oauthServer.URL + `/verify","verification_uri_complete":"` + oauthServer.URL + `/verify?user_code=ABCD-EFGH","expires_in":600,"interval":1}`))
		case "/token":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-resume", r.Form.Get("client_id"))
			switch r.Form.Get("grant_type") {
			case "refresh_token":
				require.Equal(t, "resume-oauth-refresh-1234", r.Form.Get("refresh_token"))
				_, _ = w.Write([]byte(`{"access_token":"refreshed-access-1234","refresh_token":"refreshed-refresh-1234","token_type":"Bearer","expires_in":3600}`))
			case "authorization_code":
				require.Equal(t, "resume-browser-code-1", r.Form.Get("code"))
				require.NotEmpty(t, r.Form.Get("code_verifier"))
				require.Equal(t, "http://127.0.0.1:18080/oauth/callback", r.Form.Get("redirect_uri"))
				_, _ = w.Write([]byte(`{"access_token":"browser-access-1234","refresh_token":"browser-refresh-1234","token_type":"Bearer","expires_in":3600}`))
			case oauth.DeviceCodeGrantType:
				require.Equal(t, "resume-device-1", r.Form.Get("device_code"))
				_, _ = w.Write([]byte(`{"access_token":"device-access-1234","refresh_token":"device-refresh-1234","token_type":"Bearer","expires_in":3600}`))
			default:
				http.Error(w, "unsupported grant", http.StatusBadRequest)
			}
		case "/revoke":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "client-resume", r.Form.Get("client_id"))
			oauthRevoked = append(oauthRevoked, r.Form.Get("token_type_hint")+":"+r.Form.Get("token"))
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(oauthServer.Close)
	antTraceServer := httptest.NewServer(mockanthropic.Server{Text: "resumed trace ok"}.Handler())
	t.Cleanup(antTraceServer.Close)
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Resume Web</title></head><body><p>resumed web fetch body.</p></body></html>`)
		case "/search":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<a class="result__a" href="https://example.com/resume">Resume Search</a><div class="result__snippet">A resumed search summary.</div>`)
		case "/trigger":
			w.Header().Set("Content-Type", "application/json")
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			fmt.Fprintf(w, `{"method":%q,"body":%q,"source":"resume-trigger"}`, r.Method, string(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(webServer.Close)
	t.Setenv("CODOG_WEB_SEARCH_BASE_URL", webServer.URL+"/search")
	var ragQuery struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	ragServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/query", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&ragQuery))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"phase":"1-sqlite","hits":[{"path":"internal/agent/agent.go","score":0.75,"snippet":"func resumedDebugToolCallAllowed(name string) bool {}"}]}`)
	}))
	t.Cleanup(ragServer.Close)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "default", oauthServer.URL, "client-resume", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "work", oauthServer.URL, "client-work", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveProviderProfile(context.Background(), configHome, "delete-me", oauthServer.URL, "client-delete", []string{"profile"})
	require.NoError(t, err)
	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "resume-oauth-access-1234",
		RefreshToken: "resume-oauth-refresh-1234",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	codePath := filepath.Join(workspace, "main.go")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/resume\n\ngo 1.22\n"), 0o644))
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
			"allow": []string{"read_file", "edit_file", "multi_edit"},
		},
		"mcp_servers": map[string]any{
			"resume": map[string]any{
				"command": os.Args[0],
				"args":    []string{"-test.run=TestResumeMCPToolHelperProcess"},
				"env":     []string{"CODOG_RESUME_MCP_HELPER=1"},
			},
		},
		"hooks": map[string]any{
			"pre_tool_use": []map[string]any{{
				"matcher": "read_*",
				"command": "echo hook-ok",
			}},
		},
		"temperature": 0.4,
		"rate_limit": map[string]any{
			"max_retries": 4,
		},
		"rag_base_url":        ragServer.URL,
		"rag_timeout_seconds": 2,
		"rag_top_k_max":       3,
		"language":            "Japanese",
		"theme":               "dark",
		"reasoning_effort":    "high",
		"fast_mode":           true,
		"speech_command":      "codog-test-speech-helper",
		"voice_command":       "codog-test-voice-helper",
		"voice_enabled":       true,
		"editorMode":          "vim",
		"privacy_settings": map[string]any{
			"telemetry_enabled":      true,
			"prompt_history_enabled": false,
		},
		"future": map[string]any{
			"chrome_default_enabled": true,
			"notifications_enabled":  false,
		},
		"remote": map[string]any{
			"auth_token":    "remote-secret",
			"enabled":       true,
			"lease_seconds": 45,
		},
		"sandbox": map[string]any{
			"strategy": "detect",
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
	_, err = focus.Add(workspace, []string{"main.go"})
	require.NoError(t, err)
	terminalProfilePath := filepath.Join(workspace, ".zshrc")
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "resume-demo")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"resume-demo","tools":[{"name":"resume_demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))
	pluginInstallSource := filepath.Join(t.TempDir(), "plugin-source")
	require.NoError(t, os.MkdirAll(pluginInstallSource, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginInstallSource, "plugin.json"), []byte(`{"id":"resume-install","name":"Resume Install","version":"0.1.0","tools":[{"name":"resume_install_tool","command":"cat","permission":"read-only"}]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginInstallSource, "tool.sh"), []byte("echo ok\n"), 0o755))
	skillSourcePath := filepath.Join(t.TempDir(), "review.md")
	require.NoError(t, os.WriteFile(skillSourcePath, []byte("Review resumed skill body."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "resume-debug.md"), []byte("---\ndescription: Resume debug skill.\n---\nUse this resume debug skill for {{args}}."), 0o644))
	executableShim := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(executableShim, []byte("#!/bin/sh\necho codog-shim \"$@\"\n"), 0o755))
	powerShellShimDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(powerShellShimDir, "pwsh"), []byte("#!/bin/sh\nprintf 'ps:%s\\n' \"$*\"\n"), 0o755))
	t.Setenv("PATH", powerShellShimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	previousExecutableResolver := resolveExecutablePath
	resolveExecutablePath = func() (string, error) { return executableShim, nil }
	t.Cleanup(func() { resolveExecutablePath = previousExecutableResolver })

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
	runResumedJSONWithFlags := func(flags []string, command string, args ...string) (string, error) {
		t.Helper()
		cliArgs := []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json"}
		cliArgs = append(cliArgs, flags...)
		cliArgs = append(cliArgs, command)
		cliArgs = append(cliArgs, args...)
		return captureStdout(t, func() error {
			return RunCLI(context.Background(), cliArgs, config.FlagOverrides{})
		})
	}
	runResumedJSONWithStdin := func(input string, command string, args ...string) (string, error) {
		t.Helper()
		stdinFile, err := os.CreateTemp(t.TempDir(), "stdin-*.txt")
		require.NoError(t, err)
		_, err = stdinFile.WriteString(input)
		require.NoError(t, err)
		_, err = stdinFile.Seek(0, io.SeekStart)
		require.NoError(t, err)
		originalStdin := os.Stdin
		os.Stdin = stdinFile
		defer func() {
			os.Stdin = originalStdin
			require.NoError(t, stdinFile.Close())
		}()
		return runResumedJSON(command, args...)
	}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })
	previousBrowserCallback := startBrowserCallbackServer
	startBrowserCallbackServer = func(_ context.Context, _, _, state string) (oauth.BrowserCallbackServer, error) {
		results := make(chan oauth.BrowserCallbackResult, 1)
		results <- oauth.BrowserCallbackResult{Callback: oauth.BrowserCallback{Code: "resume-browser-code-1", State: state}}
		return oauth.BrowserCallbackServer{
			RedirectURI: "http://127.0.0.1:18080/oauth/callback",
			Results:     results,
			Close:       func() error { return nil },
		}, nil
	}
	t.Cleanup(func() { startBrowserCallbackServer = previousBrowserCallback })
	voiceCommand := filepath.Join(t.TempDir(), "voice-helper.sh")
	require.NoError(t, os.WriteFile(voiceCommand, []byte("#!/bin/sh\ninput=$(cat)\nprintf 'voice:%s' \"$input\"\n"), 0o755))
	speakCommand := filepath.Join(t.TempDir(), "speak-helper.sh")
	require.NoError(t, os.WriteFile(speakCommand, []byte("#!/bin/sh\ninput=$(cat)\nprintf 'speak:%s' \"$input\"\n"), 0o755))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/status"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var statusReport struct {
		Kind    string `json:"kind"`
		Session struct {
			Active              bool   `json:"active"`
			ID                  string `json:"id"`
			MessageCount        int    `json:"message_count"`
			CreatedAtMS         int64  `json:"created_at_ms"`
			UpdatedAtMS         int64  `json:"updated_at_ms"`
			ModifiedEpochMillis int64  `json:"modified_epoch_millis"`
			Lifecycle           struct {
				Kind   string `json:"kind"`
				Signal string `json:"signal"`
				Saved  bool   `json:"saved"`
			} `json:"lifecycle"`
		} `json:"session"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &statusReport))
	require.Equal(t, "status", statusReport.Kind)
	require.True(t, statusReport.Session.Active)
	require.Equal(t, "resume-slash", statusReport.Session.ID)
	require.Equal(t, 4, statusReport.Session.MessageCount)
	require.NotZero(t, statusReport.Session.CreatedAtMS)
	require.NotZero(t, statusReport.Session.UpdatedAtMS)
	require.NotZero(t, statusReport.Session.ModifiedEpochMillis)
	require.Equal(t, "saved_only", statusReport.Session.Lifecycle.Kind)
	require.Equal(t, "saved only", statusReport.Session.Lifecycle.Signal)
	require.True(t, statusReport.Session.Lifecycle.Saved)

	out, err = runResumedJSON("/conversation")
	require.NoError(t, err)
	var convReport conversationReport
	require.NoError(t, json.Unmarshal([]byte(out), &convReport))
	require.Equal(t, "conversation", convReport.Kind)
	require.Equal(t, "status", convReport.Action)
	require.Equal(t, "resume-slash", convReport.SessionID)
	require.Equal(t, 4, convReport.MessageCount)

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

	previousReadClipboard := readClipboard
	readClipboard = func(_ context.Context) ([]byte, string, error) {
		return []byte("resumed paste text\nsecond line"), "resume-read-clipboard", nil
	}
	t.Cleanup(func() { readClipboard = previousReadClipboard })
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/paste"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var resumedPaste pasteReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPaste))
	require.Equal(t, "resume-slash", resumedPaste.SessionID)
	require.Equal(t, "resume-read-clipboard", resumedPaste.Clipboard)
	require.Equal(t, 2, resumedPaste.Lines)
	require.False(t, resumedPaste.Submitted)

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

	out, err = runResumedJSON("/init-verifiers", "--target", "codog", "--force")
	require.NoError(t, err)
	var resumedVerifierInit verifiers.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVerifierInit))
	require.Equal(t, "init_verifiers", resumedVerifierInit.Kind)
	require.Equal(t, "init", resumedVerifierInit.Action)
	require.Equal(t, "codog", resumedVerifierInit.Target)
	require.False(t, resumedVerifierInit.DryRun)
	require.NotEmpty(t, resumedVerifierInit.Artifacts)
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "verifier-cli", "SKILL.md"))

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

	out, err = runResumedJSON("/upgrade")
	require.NoError(t, err)
	var resumedUpgrade struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
		Result struct {
			CurrentVersion string   `json:"current_version"`
			Platform       string   `json:"platform"`
			Commands       []string `json:"commands"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUpgrade))
	require.Equal(t, "updater", resumedUpgrade.Kind)
	require.Equal(t, "status", resumedUpgrade.Action)
	require.Equal(t, "ok", resumedUpgrade.Status)
	require.Equal(t, version, resumedUpgrade.Result.CurrentVersion)
	require.Equal(t, updater.PlatformKey(), resumedUpgrade.Result.Platform)
	require.Contains(t, resumedUpgrade.Result.Commands, "install")

	out, err = runResumedJSON("/install")
	require.NoError(t, err)
	var resumedInstall struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
		Result struct {
			Usage            string `json:"usage"`
			RequiresArtifact bool   `json:"requires_artifact"`
			Updater          struct {
				CurrentVersion string   `json:"current_version"`
				Commands       []string `json:"commands"`
			} `json:"updater"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedInstall))
	require.Equal(t, "install", resumedInstall.Kind)
	require.Equal(t, "status", resumedInstall.Action)
	require.Equal(t, "ok", resumedInstall.Status)
	require.Equal(t, "codog install ARTIFACT [TARGET]", resumedInstall.Result.Usage)
	require.True(t, resumedInstall.Result.RequiresArtifact)
	require.Equal(t, version, resumedInstall.Result.Updater.CurrentVersion)
	require.Contains(t, resumedInstall.Result.Updater.Commands, "install")

	out, err = runResumedJSON("/brief")
	require.NoError(t, err)
	var resumedBriefStatus briefStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBriefStatus))
	require.Equal(t, "brief", resumedBriefStatus.Kind)
	require.Equal(t, "status", resumedBriefStatus.Action)
	require.Equal(t, "ready", resumedBriefStatus.Status)
	expectedBriefWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	actualBriefWorkspace, err := filepath.EvalSymlinks(resumedBriefStatus.Workspace)
	require.NoError(t, err)
	require.Equal(t, expectedBriefWorkspace, actualBriefWorkspace)
	require.Equal(t, "codog brief MESSAGE", resumedBriefStatus.NextCommand)

	out, err = runResumedJSON("/brief", "Resume", "brief", "--status", "proactive")
	require.NoError(t, err)
	var resumedBrief briefReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBrief))
	require.Equal(t, "Resume brief", resumedBrief.Message)
	require.Equal(t, "proactive", resumedBrief.Status)

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

	out, err = runResumedJSON("/settings", "inspect")
	require.NoError(t, err)
	var settingsInspect struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Config struct {
			APIKey string `json:"api_key"`
			Model  string `json:"model"`
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &settingsInspect))
	require.Equal(t, "config", settingsInspect.Kind)
	require.Equal(t, "inspect", settingsInspect.Action)
	require.Equal(t, "claude-test", settingsInspect.Config.Model)
	require.NotContains(t, out, "test-key")

	out, err = runResumedJSON("/session", "list")
	require.NoError(t, err)
	var resumedSessionList sessionListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSessionList))
	require.Equal(t, "sessions", resumedSessionList.Kind)
	require.Equal(t, "list", resumedSessionList.Action)
	require.Contains(t, resumedSessionList.Sessions, "resume-slash")

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

	out, err = runResumedJSON("/api-key", "set", "secret")
	require.NoError(t, err)
	var resumedAPIKeySet apiKeyReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAPIKeySet))
	require.Equal(t, "api_key", resumedAPIKeySet.Kind)
	require.Equal(t, "set", resumedAPIKeySet.Action)
	require.True(t, resumedAPIKeySet.Configured)
	require.NotEmpty(t, resumedAPIKeySet.Path)
	require.NotContains(t, out, "secret")

	out, err = runResumedJSON("/providers")
	require.NoError(t, err)
	var resumedProviders providersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProviders))
	require.Equal(t, "providers", resumedProviders.Kind)
	require.Equal(t, "status", resumedProviders.Action)
	require.Equal(t, "claude-test", resumedProviders.Active.Model)

	out, err = runResumedJSON("/providers", "set", "anthropic")
	require.NoError(t, err)
	var resumedProviderSet providerSetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProviderSet))
	require.Equal(t, "provider", resumedProviderSet.Kind)
	require.Equal(t, "set", resumedProviderSet.Action)
	require.Equal(t, "anthropic", resumedProviderSet.Provider)
	require.Equal(t, config.DefaultBaseURL, resumedProviderSet.BaseURL)
	require.Equal(t, config.DefaultModel, resumedProviderSet.Model)
	require.NotEmpty(t, resumedProviderSet.Path)
	require.NotEmpty(t, resumedProviderSet.Changes)

	out, err = runResumedJSON("/oauth")
	require.NoError(t, err)
	var resumedOAuthStatus oauth.Status
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthStatus))
	require.Equal(t, "oauth", resumedOAuthStatus.Kind)
	require.Equal(t, "status", resumedOAuthStatus.Action)
	require.Equal(t, "ok", resumedOAuthStatus.Status)
	require.Equal(t, "default", resumedOAuthStatus.ProfileName)
	require.True(t, resumedOAuthStatus.ProfileConfigured)
	require.True(t, resumedOAuthStatus.TokenPresent)
	require.True(t, resumedOAuthStatus.Ready)
	require.NotContains(t, out, "resume-oauth-access-1234")

	out, err = runResumedJSON("/oauth", "pkce")
	require.NoError(t, err)
	var resumedOAuthPKCE oauth.PKCE
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthPKCE))
	require.NotEmpty(t, resumedOAuthPKCE.CodeVerifier)
	require.NotEmpty(t, resumedOAuthPKCE.CodeChallenge)

	out, err = runResumedJSON("/oauth", "status", "default", "--json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthStatus))
	require.Equal(t, "default", resumedOAuthStatus.ProfileName)
	require.True(t, resumedOAuthStatus.Ready)

	out, err = runResumedJSON("/oauth", "provider", "list")
	require.NoError(t, err)
	var resumedOAuthProfiles []oauth.ProviderProfile
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProfiles))
	require.Len(t, resumedOAuthProfiles, 3)
	resumedOAuthProfileClients := map[string]string{}
	for _, profile := range resumedOAuthProfiles {
		resumedOAuthProfileClients[profile.Name] = profile.ClientID
	}
	require.Equal(t, "client-resume", resumedOAuthProfileClients["default"])
	require.Equal(t, "client-work", resumedOAuthProfileClients["work"])
	require.Equal(t, "client-delete", resumedOAuthProfileClients["delete-me"])

	out, err = runResumedJSON("/oauth", "provider", "show", "default")
	require.NoError(t, err)
	var resumedOAuthProfile oauth.ProviderProfile
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProfile))
	require.Equal(t, "default", resumedOAuthProfile.Name)
	require.Equal(t, "client-resume", resumedOAuthProfile.ClientID)

	out, err = runResumedJSON("/oauth", "provider", "delete", "delete-me")
	require.NoError(t, err)
	var resumedOAuthProviderDelete oauthProviderDeleteReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProviderDelete))
	require.Equal(t, "oauth_provider", resumedOAuthProviderDelete.Kind)
	require.Equal(t, "delete", resumedOAuthProviderDelete.Action)
	require.Equal(t, "ok", resumedOAuthProviderDelete.Status)
	require.True(t, resumedOAuthProviderDelete.Deleted)
	require.Equal(t, "delete-me", resumedOAuthProviderDelete.Name)

	out, err = runResumedJSON("/oauth", "token", "show", "--json")
	require.NoError(t, err)
	var resumedOAuthToken oauth.TokenView
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthToken))
	require.Equal(t, "resu...1234", resumedOAuthToken.AccessToken)
	require.False(t, resumedOAuthToken.Expired)
	require.NotContains(t, out, "resume-oauth-access-1234")

	out, err = runResumedJSON("/oauth", "token", "status")
	require.NoError(t, err)
	var resumedOAuthTokenStatus oauth.Status
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenStatus))
	require.Equal(t, "oauth", resumedOAuthTokenStatus.Kind)
	require.Equal(t, "status", resumedOAuthTokenStatus.Action)
	require.Equal(t, "ok", resumedOAuthTokenStatus.Status)
	require.Equal(t, "default", resumedOAuthTokenStatus.ProfileName)
	require.True(t, resumedOAuthTokenStatus.TokenPresent)
	require.True(t, resumedOAuthTokenStatus.Ready)
	require.NotContains(t, out, "resume-oauth-access-1234")

	out, err = runResumedJSON("/oauth", "token", "revoke", "default", "refresh")
	require.NoError(t, err)
	var resumedOAuthTokenRevoke oauthTokenRevokeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenRevoke))
	require.Equal(t, "oauth_token", resumedOAuthTokenRevoke.Kind)
	require.Equal(t, "revoke", resumedOAuthTokenRevoke.Action)
	require.Equal(t, "ok", resumedOAuthTokenRevoke.Status)
	require.True(t, resumedOAuthTokenRevoke.Revoked)
	require.Equal(t, "default", resumedOAuthTokenRevoke.Profile)
	require.Equal(t, "refresh", resumedOAuthTokenRevoke.Token)
	require.Contains(t, oauthRevoked, "refresh_token:resume-oauth-refresh-1234")

	out, err = runResumedJSON("/oauth", "token", "save", "resume-oauth-new-access", "resume-oauth-new-refresh")
	require.NoError(t, err)
	var resumedOAuthTokenSave oauth.TokenView
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenSave))
	require.Equal(t, "resu...cess", resumedOAuthTokenSave.AccessToken)
	require.Equal(t, "resu...resh", resumedOAuthTokenSave.RefreshToken)
	require.NotContains(t, out, "resume-oauth-new-access")

	out, err = runResumedJSON("/oauth", "token", "delete")
	require.NoError(t, err)
	var resumedOAuthTokenDelete oauthTokenDeleteReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenDelete))
	require.Equal(t, "oauth_token", resumedOAuthTokenDelete.Kind)
	require.Equal(t, "delete", resumedOAuthTokenDelete.Action)
	require.Equal(t, "ok", resumedOAuthTokenDelete.Status)
	require.True(t, resumedOAuthTokenDelete.Deleted)

	out, err = runResumedJSON("/oauth", "token", "save", "resume-oauth-logout-access")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenSave))

	out, err = runResumedJSON("/oauth", "logout", "default")
	require.NoError(t, err)
	var resumedOAuthLogout oauth.LogoutResult
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthLogout))
	require.Equal(t, "oauth", resumedOAuthLogout.Kind)
	require.Equal(t, "logout", resumedOAuthLogout.Action)
	require.Equal(t, "ok", resumedOAuthLogout.Status)
	require.True(t, resumedOAuthLogout.Deleted)
	require.Equal(t, "revoked", resumedOAuthLogout.Revocation)
	require.True(t, resumedOAuthLogout.AccessRevoked)
	require.Contains(t, oauthRevoked, "access_token:resume-oauth-logout-access")

	out, err = runResumedJSON("/oauth", "token", "save", "resume-oauth-logout-alias-access")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenSave))

	out, err = runResumedJSON("/logout", "default")
	require.NoError(t, err)
	var resumedLogoutAlias oauth.LogoutResult
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLogoutAlias))
	require.Equal(t, "oauth", resumedLogoutAlias.Kind)
	require.Equal(t, "logout", resumedLogoutAlias.Action)
	require.Equal(t, "ok", resumedLogoutAlias.Status)
	require.True(t, resumedLogoutAlias.Deleted)
	require.Equal(t, "revoked", resumedLogoutAlias.Revocation)
	require.True(t, resumedLogoutAlias.AccessRevoked)
	require.Contains(t, oauthRevoked, "access_token:resume-oauth-logout-alias-access")

	out, err = runResumedJSON("/profile", "list")
	require.NoError(t, err)
	var resumedProfile profileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProfile))
	require.Equal(t, "profile", resumedProfile.Kind)
	require.Equal(t, "list", resumedProfile.Action)
	require.Len(t, resumedProfile.Profiles, 2)
	require.Equal(t, 2, resumedProfile.ProfileCount)
	require.False(t, resumedProfile.ActiveConfigured)

	out, err = runResumedJSON("/profile", "set", "work")
	require.NoError(t, err)
	var resumedProfileSet profileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedProfileSet))
	require.Equal(t, "profile", resumedProfileSet.Kind)
	require.Equal(t, "set", resumedProfileSet.Action)
	require.Equal(t, "work", resumedProfileSet.ActiveProfile)
	require.True(t, resumedProfileSet.ActiveConfigured)
	require.Equal(t, "work", resumedProfileSet.ResolvedProfile)
	require.Equal(t, "active", resumedProfileSet.ResolvedSource)
	require.Equal(t, 2, resumedProfileSet.ProfileCount)
	require.NotNil(t, resumedProfileSet.Profile)
	require.Equal(t, "work", resumedProfileSet.Profile.Name)
	require.NotEmpty(t, resumedProfileSet.Path)

	out, err = runResumedJSON("/budget", "ls")
	require.NoError(t, err)
	var resumedBudget budgetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBudget))
	require.Equal(t, "budget", resumedBudget.Kind)
	require.Equal(t, "show", resumedBudget.Action)
	require.Equal(t, 1000, resumedBudget.MaxTokens)
	require.Equal(t, 3, resumedBudget.MaxTurns)

	out, err = runResumedJSON("/budget", "use", "--max-tokens", "2000")
	require.NoError(t, err)
	var resumedBudgetSet budgetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBudgetSet))
	require.Equal(t, "budget", resumedBudgetSet.Kind)
	require.Equal(t, "set", resumedBudgetSet.Action)
	require.Equal(t, 2000, resumedBudgetSet.MaxTokens)
	require.Equal(t, 3, resumedBudgetSet.MaxTurns)
	require.NotNil(t, resumedBudgetSet.Previous)
	require.Equal(t, 1000, resumedBudgetSet.Previous.MaxTokens)
	require.NotEmpty(t, resumedBudgetSet.Path)

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

	out, err = runResumedJSON("/temperature", "set", "0.2")
	require.NoError(t, err)
	var resumedTemperatureSet temperatureReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTemperatureSet))
	require.Equal(t, "temperature", resumedTemperatureSet.Kind)
	require.Equal(t, "set", resumedTemperatureSet.Action)
	require.True(t, resumedTemperatureSet.Configured)
	require.NotNil(t, resumedTemperatureSet.Temperature)
	require.InDelta(t, 0.2, *resumedTemperatureSet.Temperature, 0.0001)
	require.NotEmpty(t, resumedTemperatureSet.Path)

	out, err = runResumedJSON("/rate-limit")
	require.NoError(t, err)
	var resumedRateLimit rateLimitReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRateLimit))
	require.Equal(t, "rate_limit", resumedRateLimit.Kind)
	require.Equal(t, "show", resumedRateLimit.Action)
	require.Equal(t, 4, resumedRateLimit.MaxRetries)

	out, err = runResumedJSON("/rate-limit", "set", "--max-retries", "2")
	require.NoError(t, err)
	var resumedRateLimitSet rateLimitReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRateLimitSet))
	require.Equal(t, "rate_limit", resumedRateLimitSet.Kind)
	require.Equal(t, "set", resumedRateLimitSet.Action)
	require.Equal(t, 2, resumedRateLimitSet.MaxRetries)
	require.NotNil(t, resumedRateLimitSet.Previous)
	require.Equal(t, 4, resumedRateLimitSet.Previous.MaxRetries)
	require.NotEmpty(t, resumedRateLimitSet.Path)

	out, err = runResumedJSON("/rate-limit-options")
	require.NoError(t, err)
	var resumedRateLimitOptions rateLimitOptionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRateLimitOptions))
	require.Equal(t, "rate_limit_options", resumedRateLimitOptions.Kind)
	require.Equal(t, 4, resumedRateLimitOptions.MaxRetries)

	out, err = runResumedJSON("/reset-limits", "--path", configPath)
	require.NoError(t, err)
	var resumedResetLimits resetLimitsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedResetLimits))
	require.Equal(t, "reset_limits", resumedResetLimits.Kind)
	require.Equal(t, "reset", resumedResetLimits.Action)
	require.Equal(t, 4, resumedResetLimits.Previous.MaxRetries)
	require.Equal(t, config.DefaultRateLimitConfig().MaxRetries, resumedResetLimits.Current.MaxRetries)
	configData, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(configData), "rate_limit")

	out, err = runResumedJSON("/permissions")
	require.NoError(t, err)
	var resumedPermissions permissionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPermissions))
	require.Equal(t, "permissions", resumedPermissions.Kind)
	require.Equal(t, "show", resumedPermissions.Action)
	require.Equal(t, "workspace-write", resumedPermissions.PermissionMode)
	require.Equal(t, []string{"read_file", "edit_file", "multi_edit"}, resumedPermissions.PermissionRules.Allow)

	out, err = runResumedJSON("/permissions", "read-only")
	require.NoError(t, err)
	var resumedPermissionsSet permissionsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPermissionsSet))
	require.Equal(t, "permissions", resumedPermissionsSet.Kind)
	require.Equal(t, "set", resumedPermissionsSet.Action)
	require.Equal(t, "read-only", resumedPermissionsSet.PermissionMode)
	require.Equal(t, "workspace-write", resumedPermissionsSet.PreviousMode)
	require.NotEmpty(t, resumedPermissionsSet.Path)

	out, err = runResumedJSON("/allowed-tools")
	require.NoError(t, err)
	var resumedAllowedTools allowedToolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAllowedTools))
	require.Equal(t, "allowed_tools", resumedAllowedTools.Kind)
	require.Equal(t, "list", resumedAllowedTools.Action)
	require.Equal(t, []string{"read_file", "edit_file", "multi_edit"}, resumedAllowedTools.Rules)

	out, err = runResumedJSON("/allowed-tools", "add", "bash")
	require.NoError(t, err)
	var resumedAllowedToolsAdd allowedToolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAllowedToolsAdd))
	require.Equal(t, "allowed_tools", resumedAllowedToolsAdd.Kind)
	require.Equal(t, "add", resumedAllowedToolsAdd.Action)
	require.Equal(t, []string{"read_file", "edit_file", "multi_edit", "bash"}, resumedAllowedToolsAdd.Rules)
	require.Equal(t, 4, resumedAllowedToolsAdd.Count)
	require.NotEmpty(t, resumedAllowedToolsAdd.Path)

	out, err = runResumedJSON("/output-style")
	require.NoError(t, err)
	var resumedOutputStyle outputstyle.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOutputStyle))
	require.Equal(t, "output_style", resumedOutputStyle.Kind)
	require.Equal(t, "list", resumedOutputStyle.Action)

	out, err = runResumedJSON("/output-style", "ls")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOutputStyle))
	require.Equal(t, "output_style", resumedOutputStyle.Kind)
	require.Equal(t, "list", resumedOutputStyle.Action)

	out, err = runResumedJSON("/output-style", "enable", "concise")
	require.NoError(t, err)
	var resumedOutputStyleSet outputstyle.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOutputStyleSet))
	require.Equal(t, "output_style", resumedOutputStyleSet.Kind)
	require.Equal(t, "set", resumedOutputStyleSet.Action)
	require.Equal(t, "concise", resumedOutputStyleSet.Active)
	require.NotNil(t, resumedOutputStyleSet.Style)
	require.Equal(t, "concise", resumedOutputStyleSet.Style.Name)

	out, err = runResumedJSON("/output-style", "disable")
	require.NoError(t, err)
	var resumedOutputStyleClear outputstyle.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOutputStyleClear))
	require.Equal(t, "output_style", resumedOutputStyleClear.Kind)
	require.Equal(t, "clear", resumedOutputStyleClear.Action)

	out, err = runResumedJSON("/theme")
	require.NoError(t, err)
	var resumedTheme themeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTheme))
	require.Equal(t, "theme", resumedTheme.Kind)
	require.Equal(t, "status", resumedTheme.Action)
	require.Equal(t, "dark", resumedTheme.Theme)

	out, err = runResumedJSON("/theme", "use", "light")
	require.NoError(t, err)
	var resumedThemeSet themeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedThemeSet))
	require.Equal(t, "theme", resumedThemeSet.Kind)
	require.Equal(t, "set", resumedThemeSet.Action)
	require.Equal(t, "light", resumedThemeSet.Theme)
	require.Equal(t, "dark", resumedThemeSet.Previous)
	require.NotEmpty(t, resumedThemeSet.Path)

	out, err = runResumedJSON("/color", "ls")
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

	out, err = runResumedJSON("/language", "use", "French")
	require.NoError(t, err)
	var resumedLanguageSet languageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLanguageSet))
	require.Equal(t, "language", resumedLanguageSet.Kind)
	require.Equal(t, "set", resumedLanguageSet.Action)
	require.True(t, resumedLanguageSet.Configured)
	require.Equal(t, "French", resumedLanguageSet.Language)
	require.Equal(t, "Japanese", resumedLanguageSet.Previous)
	require.NotEmpty(t, resumedLanguageSet.Path)

	out, err = runResumedJSON("/language", "off")
	require.NoError(t, err)
	var resumedLanguageClear languageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLanguageClear))
	require.Equal(t, "language", resumedLanguageClear.Kind)
	require.Equal(t, "clear", resumedLanguageClear.Action)
	require.False(t, resumedLanguageClear.Configured)

	out, err = runResumedJSON("/effort")
	require.NoError(t, err)
	var resumedEffort effortReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedEffort))
	require.Equal(t, "effort", resumedEffort.Kind)
	require.Equal(t, "status", resumedEffort.Action)
	require.Equal(t, "high", resumedEffort.Effort)

	out, err = runResumedJSON("/effort", "low")
	require.NoError(t, err)
	var resumedEffortSet effortReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedEffortSet))
	require.Equal(t, "effort", resumedEffortSet.Kind)
	require.Equal(t, "set", resumedEffortSet.Action)
	require.Equal(t, "low", resumedEffortSet.Effort)
	require.Equal(t, "high", resumedEffortSet.Previous)
	require.NotEmpty(t, resumedEffortSet.Path)

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

	out, err = runResumedJSON("/fast", "toggle")
	require.NoError(t, err)
	var resumedFastToggle fastReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFastToggle))
	require.Equal(t, "fast", resumedFastToggle.Kind)
	require.Equal(t, "set", resumedFastToggle.Action)
	require.False(t, resumedFastToggle.Enabled)
	require.True(t, resumedFastToggle.Previous)
	require.NotEmpty(t, resumedFastToggle.Path)

	out, err = runResumedJSON("/voice")
	require.NoError(t, err)
	var resumedVoice voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoice))
	require.Equal(t, "voice", resumedVoice.Kind)
	require.Equal(t, "status", resumedVoice.Action)
	require.True(t, resumedVoice.Enabled)
	require.True(t, resumedVoice.CommandConfigured)
	require.Equal(t, "codog-test-voice-helper", resumedVoice.Command)

	out, err = runResumedJSON("/voice", "set-command", voiceCommand, "--path", configPath)
	require.NoError(t, err)
	var resumedVoiceSetCommand voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceSetCommand))
	require.Equal(t, "voice", resumedVoiceSetCommand.Kind)
	require.Equal(t, "set-command", resumedVoiceSetCommand.Action)
	require.True(t, resumedVoiceSetCommand.CommandConfigured)
	require.True(t, resumedVoiceSetCommand.CommandAvailable)
	require.Equal(t, voiceCommand, resumedVoiceSetCommand.Command)
	require.NotEmpty(t, resumedVoiceSetCommand.Path)

	out, err = runResumedJSON("/voice", "on")
	require.NoError(t, err)
	var resumedVoiceOn voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceOn))
	require.Equal(t, "voice", resumedVoiceOn.Kind)
	require.Equal(t, "on", resumedVoiceOn.Action)
	require.True(t, resumedVoiceOn.Enabled)
	require.True(t, resumedVoiceOn.CommandAvailable)

	out, err = runResumedJSON("/voice", "test", "--input", "resume-mic")
	require.NoError(t, err)
	var resumedVoiceTest voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceTest))
	require.Equal(t, "voice", resumedVoiceTest.Kind)
	require.Equal(t, "test", resumedVoiceTest.Action)
	require.Equal(t, "voice:resume-mic", resumedVoiceTest.Transcript)
	require.NotNil(t, resumedVoiceTest.ExitCode)
	require.Equal(t, 0, *resumedVoiceTest.ExitCode)

	out, err = runResumedJSON("/voice", "listen", "--input", "resume-listen")
	require.NoError(t, err)
	var resumedVoiceListen voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceListen))
	require.Equal(t, "voice", resumedVoiceListen.Kind)
	require.Equal(t, "listen", resumedVoiceListen.Action)
	require.Equal(t, "voice:resume-listen", resumedVoiceListen.Transcript)

	out, err = runResumedJSON("/listen", "--input", "resume-alias")
	require.NoError(t, err)
	var resumedListen voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedListen))
	require.Equal(t, "voice", resumedListen.Kind)
	require.Equal(t, "listen", resumedListen.Action)
	require.Equal(t, "voice:resume-alias", resumedListen.Transcript)

	out, err = runResumedJSON("/voice", "toggle")
	require.NoError(t, err)
	var resumedVoiceToggle voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceToggle))
	require.Equal(t, "voice", resumedVoiceToggle.Kind)
	require.Equal(t, "toggle", resumedVoiceToggle.Action)
	require.False(t, resumedVoiceToggle.Enabled)

	out, err = runResumedJSON("/voice", "off")
	require.NoError(t, err)
	var resumedVoiceOff voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceOff))
	require.Equal(t, "voice", resumedVoiceOff.Kind)
	require.Equal(t, "off", resumedVoiceOff.Action)
	require.False(t, resumedVoiceOff.Enabled)
	require.NotEmpty(t, resumedVoiceOff.Path)

	out, err = runResumedJSON("/voice", "clear")
	require.NoError(t, err)
	var resumedVoiceClear voiceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVoiceClear))
	require.Equal(t, "voice", resumedVoiceClear.Kind)
	require.Equal(t, "clear", resumedVoiceClear.Action)
	require.False(t, resumedVoiceClear.Enabled)
	require.False(t, resumedVoiceClear.CommandConfigured)
	require.NotEmpty(t, resumedVoiceClear.Path)

	out, err = runResumedJSON("/speak", "status")
	require.NoError(t, err)
	var resumedSpeak speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeak))
	require.Equal(t, "speak", resumedSpeak.Kind)
	require.Equal(t, "status", resumedSpeak.Action)
	require.True(t, resumedSpeak.CommandConfigured)
	require.Equal(t, "codog-test-speech-helper", resumedSpeak.Command)

	out, err = runResumedJSON("/speak", "set-command", speakCommand, "--path", configPath)
	require.NoError(t, err)
	var resumedSpeakSetCommand speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakSetCommand))
	require.Equal(t, "speak", resumedSpeakSetCommand.Kind)
	require.Equal(t, "set-command", resumedSpeakSetCommand.Action)
	require.True(t, resumedSpeakSetCommand.CommandConfigured)
	require.True(t, resumedSpeakSetCommand.CommandAvailable)
	require.Equal(t, speakCommand, resumedSpeakSetCommand.Command)
	require.NotEmpty(t, resumedSpeakSetCommand.Path)

	out, err = runResumedJSON("/speak", "test")
	require.NoError(t, err)
	var resumedSpeakTest speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakTest))
	require.Equal(t, "speak", resumedSpeakTest.Kind)
	require.Equal(t, "test", resumedSpeakTest.Action)
	require.Equal(t, "speech check", resumedSpeakTest.TextPreview)
	require.Equal(t, "speak:speech check", resumedSpeakTest.Stdout)
	require.NotNil(t, resumedSpeakTest.ExitCode)
	require.Equal(t, 0, *resumedSpeakTest.ExitCode)

	out, err = runResumedJSON("/speak", "hello")
	require.NoError(t, err)
	var resumedSpeakHello speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakHello))
	require.Equal(t, "speak", resumedSpeakHello.Kind)
	require.Equal(t, "speak", resumedSpeakHello.Action)
	require.Equal(t, "hello", resumedSpeakHello.TextPreview)
	require.Equal(t, "speak:hello", resumedSpeakHello.Stdout)

	out, err = runResumedJSON("/speak", "last")
	require.NoError(t, err)
	var resumedSpeakLast speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakLast))
	require.Equal(t, "speak", resumedSpeakLast.Kind)
	require.Equal(t, "speak", resumedSpeakLast.Action)
	require.Equal(t, "resume-slash", resumedSpeakLast.SessionID)
	require.Equal(t, "four", resumedSpeakLast.TextPreview)
	require.Equal(t, "speak:four", resumedSpeakLast.Stdout)

	out, err = runResumedJSON("/speak")
	require.NoError(t, err)
	var resumedSpeakDefault speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakDefault))
	require.Equal(t, "speak", resumedSpeakDefault.Kind)
	require.Equal(t, "speak", resumedSpeakDefault.Action)
	require.Equal(t, "resume-slash", resumedSpeakDefault.SessionID)
	require.Equal(t, "four", resumedSpeakDefault.TextPreview)
	require.Equal(t, "speak:four", resumedSpeakDefault.Stdout)

	out, err = runResumedJSON("/speak", "clear")
	require.NoError(t, err)
	var resumedSpeakClear speakReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSpeakClear))
	require.Equal(t, "speak", resumedSpeakClear.Kind)
	require.Equal(t, "clear", resumedSpeakClear.Action)
	require.False(t, resumedSpeakClear.CommandConfigured)
	require.NotEmpty(t, resumedSpeakClear.Path)

	out, err = runResumedJSON("/vim")
	require.NoError(t, err)
	var resumedVim vimReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVim))
	require.Equal(t, "vim", resumedVim.Kind)
	require.Equal(t, "status", resumedVim.Action)
	require.True(t, resumedVim.Enabled)

	out, err = runResumedJSON("/vim", "toggle")
	require.NoError(t, err)
	var resumedVimToggle vimReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedVimToggle))
	require.Equal(t, "vim", resumedVimToggle.Kind)
	require.Equal(t, "set", resumedVimToggle.Action)
	require.False(t, resumedVimToggle.Enabled)
	require.Equal(t, "default", resumedVimToggle.EditorMode)
	require.Equal(t, "vim", resumedVimToggle.Previous)
	require.NotEmpty(t, resumedVimToggle.Path)

	out, err = runResumedJSON("/chrome", "permissions")
	require.NoError(t, err)
	var resumedChrome chromeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedChrome))
	require.Equal(t, "chrome", resumedChrome.Kind)
	require.Equal(t, "permissions", resumedChrome.Action)
	require.True(t, resumedChrome.Enabled)

	out, err = runResumedJSON("/chrome", "on")
	require.NoError(t, err)
	var resumedChromeOn chromeReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedChromeOn))
	require.Equal(t, "chrome", resumedChromeOn.Kind)
	require.Equal(t, "set", resumedChromeOn.Action)
	require.True(t, resumedChromeOn.Enabled)
	require.True(t, resumedChromeOn.Configured)
	require.NotEmpty(t, resumedChromeOn.Path)

	out, err = runResumedJSON("/notifications")
	require.NoError(t, err)
	var resumedNotifications notificationsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedNotifications))
	require.Equal(t, "notifications", resumedNotifications.Kind)
	require.Equal(t, "status", resumedNotifications.Action)
	require.False(t, resumedNotifications.Enabled)

	out, err = runResumedJSON("/notifications", "on")
	require.NoError(t, err)
	var resumedNotificationsOn notificationsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedNotificationsOn))
	require.Equal(t, "notifications", resumedNotificationsOn.Kind)
	require.Equal(t, "set", resumedNotificationsOn.Action)
	require.True(t, resumedNotificationsOn.Enabled)
	require.False(t, resumedNotificationsOn.Previous)
	require.NotEmpty(t, resumedNotificationsOn.Path)

	out, err = runResumedJSON("/privacy-settings")
	require.NoError(t, err)
	var resumedPrivacy privacyReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPrivacy))
	require.Equal(t, "privacy_settings", resumedPrivacy.Kind)
	require.Equal(t, "show", resumedPrivacy.Action)
	require.True(t, resumedPrivacy.Settings["telemetry_enabled"])
	require.False(t, resumedPrivacy.Settings["prompt_history_enabled"])

	out, err = runResumedJSON("/privacy-settings", "set", "telemetry", "off")
	require.NoError(t, err)
	var resumedPrivacySet privacyReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPrivacySet))
	require.Equal(t, "privacy_settings", resumedPrivacySet.Kind)
	require.Equal(t, "set", resumedPrivacySet.Action)
	require.Equal(t, "telemetry_enabled", resumedPrivacySet.Key)
	require.NotNil(t, resumedPrivacySet.Value)
	require.False(t, *resumedPrivacySet.Value)
	require.False(t, resumedPrivacySet.Settings["telemetry_enabled"])
	require.NotEmpty(t, resumedPrivacySet.Path)

	out, err = runResumedJSON("/telemetry")
	require.NoError(t, err)
	var resumedTelemetry telemetryReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTelemetry))
	require.Equal(t, "telemetry", resumedTelemetry.Kind)
	require.Equal(t, "status", resumedTelemetry.Action)
	require.True(t, resumedTelemetry.Enabled)

	out, err = runResumedJSON("/telemetry", "on")
	require.NoError(t, err)
	var resumedTelemetryOn telemetryReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTelemetryOn))
	require.Equal(t, "telemetry", resumedTelemetryOn.Kind)
	require.Equal(t, "set", resumedTelemetryOn.Action)
	require.True(t, resumedTelemetryOn.Enabled)
	require.True(t, resumedTelemetryOn.Previous)
	require.NotEmpty(t, resumedTelemetryOn.Path)

	out, err = runResumedJSON("/keybindings", "path")
	require.NoError(t, err)
	var resumedKeybindings keybindingsFileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedKeybindings))
	require.Equal(t, "keybindings", resumedKeybindings.Kind)
	require.Equal(t, "path", resumedKeybindings.Action)
	require.NotEmpty(t, resumedKeybindings.Path)

	out, err = runResumedJSON("/keybindings", "init")
	require.NoError(t, err)
	var resumedKeybindingsInit keybindingsFileReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedKeybindingsInit))
	require.Equal(t, "keybindings", resumedKeybindingsInit.Kind)
	require.Equal(t, "init", resumedKeybindingsInit.Action)
	require.Equal(t, "created", resumedKeybindingsInit.Status)
	require.True(t, resumedKeybindingsInit.Created)
	require.True(t, resumedKeybindingsInit.Exists)
	require.NotEmpty(t, resumedKeybindingsInit.Path)
	require.FileExists(t, resumedKeybindingsInit.Path)

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

	out, err = runResumedJSON("/setup", "init")
	require.NoError(t, err)
	var resumedSetupInit setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetupInit))
	require.Equal(t, "setup", resumedSetupInit.Kind)
	require.Equal(t, "init", resumedSetupInit.Action)
	require.NotNil(t, resumedSetupInit.Project)
	require.Equal(t, "init", resumedSetupInit.Project.Action)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))

	out, err = runResumedJSON("/setup", "all", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedSetupAll setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetupAll))
	require.Equal(t, "setup", resumedSetupAll.Kind)
	require.Equal(t, "all", resumedSetupAll.Action)
	require.NotNil(t, resumedSetupAll.Project)
	require.NotNil(t, resumedSetupAll.Terminal)
	require.Equal(t, "status", resumedSetupAll.Terminal.Action)
	require.Equal(t, terminalProfilePath, resumedSetupAll.Terminal.Path)

	out, err = runResumedJSON("/setup", "terminal", "install", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedSetupTerminalInstall setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetupTerminalInstall))
	require.Equal(t, "setup", resumedSetupTerminalInstall.Kind)
	require.Equal(t, "terminal", resumedSetupTerminalInstall.Action)
	require.NotNil(t, resumedSetupTerminalInstall.Terminal)
	require.Equal(t, "install", resumedSetupTerminalInstall.Terminal.Action)
	require.True(t, resumedSetupTerminalInstall.Terminal.Installed)
	require.True(t, resumedSetupTerminalInstall.Terminal.Changed)
	require.Equal(t, terminalProfilePath, resumedSetupTerminalInstall.Terminal.Path)
	profileData, err := os.ReadFile(terminalProfilePath)
	require.NoError(t, err)
	require.Contains(t, string(profileData), "codog shell integration")

	out, err = runResumedJSON("/setup", "terminal", "uninstall", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedSetupTerminalUninstall setupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSetupTerminalUninstall))
	require.Equal(t, "setup", resumedSetupTerminalUninstall.Kind)
	require.Equal(t, "terminal", resumedSetupTerminalUninstall.Action)
	require.NotNil(t, resumedSetupTerminalUninstall.Terminal)
	require.Equal(t, "uninstall", resumedSetupTerminalUninstall.Terminal.Action)
	require.False(t, resumedSetupTerminalUninstall.Terminal.Installed)
	require.True(t, resumedSetupTerminalUninstall.Terminal.Changed)
	profileData, err = os.ReadFile(terminalProfilePath)
	require.NoError(t, err)
	require.NotContains(t, string(profileData), "codog shell integration")

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

	out, err = runResumedJSON("/debug-tool-call", "LSPTool", `{"action":"symbols","path":"main.go"}`)
	require.NoError(t, err)
	var resumedDebugLSP debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugLSP))
	require.Equal(t, "debug_tool_call", resumedDebugLSP.Kind)
	require.Equal(t, "lsp", resumedDebugLSP.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugLSP.Permission)
	require.True(t, resumedDebugLSP.Success)
	require.Contains(t, resumedDebugLSP.Output, `"action": "symbols"`)
	require.Contains(t, resumedDebugLSP.Output, `"source": "static"`)
	require.Contains(t, resumedDebugLSP.Output, "helper")

	out, err = runResumedJSON("/debug-tool-call", "StructuredOutputTool", `{"status":"ok","items":[1,2]}`)
	require.NoError(t, err)
	var resumedDebugStructuredOutput debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugStructuredOutput))
	require.Equal(t, "debug_tool_call", resumedDebugStructuredOutput.Kind)
	require.Equal(t, "structured_output", resumedDebugStructuredOutput.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugStructuredOutput.Permission)
	require.True(t, resumedDebugStructuredOutput.Success)
	require.Contains(t, resumedDebugStructuredOutput.Output, `"status": "ok"`)
	require.Contains(t, resumedDebugStructuredOutput.Output, `"Structured output provided successfully"`)

	out, err = runResumedJSON("/debug-tool-call", "ToolSearchTool", `{"query":"web fetch","max_results":2}`)
	require.NoError(t, err)
	var resumedDebugToolSearch debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugToolSearch))
	require.Equal(t, "debug_tool_call", resumedDebugToolSearch.Kind)
	require.Equal(t, "tool_search", resumedDebugToolSearch.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugToolSearch.Permission)
	require.True(t, resumedDebugToolSearch.Success)
	require.Contains(t, resumedDebugToolSearch.Output, `"query": "web fetch"`)
	require.Contains(t, resumedDebugToolSearch.Output, `"name": "web_fetch"`)

	out, err = runResumedJSONWithStdin("2\n", "/debug-tool-call", "AskUserQuestionTool", `{"question":"Pick a resume path","choices":["alpha","beta"],"default":"alpha"}`)
	require.NoError(t, err)
	var resumedDebugAskUserQuestion debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugAskUserQuestion))
	require.Equal(t, "debug_tool_call", resumedDebugAskUserQuestion.Kind)
	require.Equal(t, "ask_user_question", resumedDebugAskUserQuestion.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugAskUserQuestion.Permission)
	require.True(t, resumedDebugAskUserQuestion.Success)
	require.Contains(t, resumedDebugAskUserQuestion.Output, `"question": "Pick a resume path"`)
	require.Contains(t, resumedDebugAskUserQuestion.Output, `"answer": "beta"`)

	out, err = runResumedJSON("/debug-tool-call", "SkillTool", `{"action":"list","query":"resume","max_results":5}`)
	require.NoError(t, err)
	var resumedDebugSkillList debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSkillList))
	require.Equal(t, "debug_tool_call", resumedDebugSkillList.Kind)
	require.Equal(t, "skill", resumedDebugSkillList.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugSkillList.Permission)
	require.True(t, resumedDebugSkillList.Success)
	require.Contains(t, resumedDebugSkillList.Output, `"action": "list"`)
	require.Contains(t, resumedDebugSkillList.Output, `"name": "resume-debug"`)

	out, err = runResumedJSON("/debug-tool-call", "SkillTool", `{"action":"show","skill":"resume-debug"}`)
	require.NoError(t, err)
	var resumedDebugSkillShow debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSkillShow))
	require.Equal(t, "debug_tool_call", resumedDebugSkillShow.Kind)
	require.Equal(t, "skill", resumedDebugSkillShow.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugSkillShow.Permission)
	require.True(t, resumedDebugSkillShow.Success)
	require.Contains(t, resumedDebugSkillShow.Output, `"action": "show"`)
	require.Contains(t, resumedDebugSkillShow.Output, `"skill": "resume-debug"`)
	require.Contains(t, resumedDebugSkillShow.Output, "Resume debug skill.")

	out, err = runResumedJSON("/debug-tool-call", "SkillTool", `{"skill":"resume-debug","args":"contract coverage"}`)
	require.NoError(t, err)
	var resumedDebugSkillInvoke debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSkillInvoke))
	require.Equal(t, "debug_tool_call", resumedDebugSkillInvoke.Kind)
	require.Equal(t, "skill", resumedDebugSkillInvoke.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugSkillInvoke.Permission)
	require.True(t, resumedDebugSkillInvoke.Success)
	require.Contains(t, resumedDebugSkillInvoke.Output, `"action": "invoke"`)
	require.Contains(t, resumedDebugSkillInvoke.Output, "contract coverage")

	out, err = runResumedJSON("/debug-tool-call", "BriefTool", `{"message":"resume debug brief","status":"normal","attachments":["main.go"]}`)
	require.NoError(t, err)
	var resumedDebugBrief debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugBrief))
	require.Equal(t, "debug_tool_call", resumedDebugBrief.Kind)
	require.Equal(t, "brief", resumedDebugBrief.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugBrief.Permission)
	require.True(t, resumedDebugBrief.Success)
	require.Contains(t, resumedDebugBrief.Output, `"message": "resume debug brief"`)
	require.Contains(t, resumedDebugBrief.Output, `"status": "normal"`)
	require.Contains(t, resumedDebugBrief.Output, `"is_image": false`)

	out, err = runResumedJSON("/debug-tool-call", "SendUserMessageTool", `{"message":"resume debug user message","status":"proactive","attachments":["main.go"]}`)
	require.NoError(t, err)
	var resumedDebugSendUserMessage debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSendUserMessage))
	require.Equal(t, "debug_tool_call", resumedDebugSendUserMessage.Kind)
	require.Equal(t, "send_user_message", resumedDebugSendUserMessage.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugSendUserMessage.Permission)
	require.True(t, resumedDebugSendUserMessage.Success)
	require.Contains(t, resumedDebugSendUserMessage.Output, `"message": "resume debug user message"`)
	require.Contains(t, resumedDebugSendUserMessage.Output, `"status": "proactive"`)
	require.Contains(t, resumedDebugSendUserMessage.Output, `"is_image": false`)

	out, err = runResumedJSON("/debug-tool-call", "SleepTool", `{"duration_ms":1}`)
	require.NoError(t, err)
	var resumedDebugSleep debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSleep))
	require.Equal(t, "debug_tool_call", resumedDebugSleep.Kind)
	require.Equal(t, "sleep", resumedDebugSleep.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugSleep.Permission)
	require.True(t, resumedDebugSleep.Success)
	require.Contains(t, resumedDebugSleep.Output, "Slept for 1ms")

	out, err = runResumedJSON("/debug-tool-call", "PolicyEvaluateTool", `{"lane_id":"resume-lane","green_level":3,"green_contract_satisfied":true,"review_status":"approved","diff_scope":"scoped","branch_status":"stale","branch_behind":2,"verification_blocked":true,"completed":true}`)
	require.NoError(t, err)
	var resumedDebugPolicyEvaluate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugPolicyEvaluate))
	require.Equal(t, "debug_tool_call", resumedDebugPolicyEvaluate.Kind)
	require.Equal(t, "policy_evaluate", resumedDebugPolicyEvaluate.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugPolicyEvaluate.Permission)
	require.True(t, resumedDebugPolicyEvaluate.Success)
	require.Contains(t, resumedDebugPolicyEvaluate.Output, `"kind": "policy_evaluation"`)
	require.Contains(t, resumedDebugPolicyEvaluate.Output, `"kind": "merge_forward"`)
	require.Contains(t, resumedDebugPolicyEvaluate.Output, `"kind": "closeout_lane"`)

	out, err = runResumedJSON("/debug-tool-call", "PermissionCheck", `{"target_tool":"Bash","input":{"command":"pwd"}}`)
	require.NoError(t, err)
	var resumedDebugPermissionCheck debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugPermissionCheck))
	require.Equal(t, "debug_tool_call", resumedDebugPermissionCheck.Kind)
	require.Equal(t, "permission_check", resumedDebugPermissionCheck.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugPermissionCheck.Permission)
	require.True(t, resumedDebugPermissionCheck.Success)
	require.Contains(t, resumedDebugPermissionCheck.Output, `"kind": "permission_check"`)
	require.Contains(t, resumedDebugPermissionCheck.Output, `"canonical_tool": "bash"`)
	require.Contains(t, resumedDebugPermissionCheck.Output, `"allowed": true`)

	out, err = runResumedJSON("/debug-tool-call", "ApprovalTokenTool", `{"action":"grant","scope":{"policy":"resume-debug","action":"inspect","repository":"codog","branch":"main"},"approving_actor":"reviewer","approved_executor":"codog","max_uses":1}`)
	require.NoError(t, err)
	var resumedDebugApprovalToken debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugApprovalToken))
	require.Equal(t, "debug_tool_call", resumedDebugApprovalToken.Kind)
	require.Equal(t, "approval_token", resumedDebugApprovalToken.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugApprovalToken.Permission)
	require.True(t, resumedDebugApprovalToken.Success)
	require.Contains(t, resumedDebugApprovalToken.Output, `"kind": "approval_token"`)
	require.Contains(t, resumedDebugApprovalToken.Output, `"action": "grant"`)
	require.Contains(t, resumedDebugApprovalToken.Output, `"status": "ok"`)
	require.Contains(t, resumedDebugApprovalToken.Output, `"status": "approval_granted"`)

	out, err = runResumedJSON("/debug-tool-call", "ListMcpResourcesTool", `{}`)
	require.NoError(t, err)
	var resumedDebugMCPResources debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMCPResources))
	require.Equal(t, "debug_tool_call", resumedDebugMCPResources.Kind)
	require.Equal(t, "list_mcp_resources", resumedDebugMCPResources.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugMCPResources.Permission)
	require.True(t, resumedDebugMCPResources.Success)
	require.Contains(t, resumedDebugMCPResources.Output, `"kind": "mcp_resources"`)
	require.Contains(t, resumedDebugMCPResources.Output, `"server": "resume"`)
	require.Contains(t, resumedDebugMCPResources.Output, "codog://resume-note")

	out, err = runResumedJSON("/debug-tool-call", "ReadMcpResourceTool", `{"server":"resume","uri":"codog://resume-note"}`)
	require.NoError(t, err)
	var resumedDebugReadMCPResource debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugReadMCPResource))
	require.Equal(t, "debug_tool_call", resumedDebugReadMCPResource.Kind)
	require.Equal(t, "read_mcp_resource", resumedDebugReadMCPResource.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugReadMCPResource.Permission)
	require.True(t, resumedDebugReadMCPResource.Success)
	require.Contains(t, resumedDebugReadMCPResource.Output, `"uri": "codog://resume-note"`)
	require.Contains(t, resumedDebugReadMCPResource.Output, "resume note body")

	out, err = runResumedJSON("/debug-tool-call", "ListMcpResourceTemplatesTool", `{}`)
	require.NoError(t, err)
	var resumedDebugMCPResourceTemplates debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMCPResourceTemplates))
	require.Equal(t, "debug_tool_call", resumedDebugMCPResourceTemplates.Kind)
	require.Equal(t, "list_mcp_resource_templates", resumedDebugMCPResourceTemplates.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugMCPResourceTemplates.Permission)
	require.True(t, resumedDebugMCPResourceTemplates.Success)
	require.Contains(t, resumedDebugMCPResourceTemplates.Output, `"kind": "mcp_resource_templates"`)
	require.Contains(t, resumedDebugMCPResourceTemplates.Output, `"server": "resume"`)
	require.Contains(t, resumedDebugMCPResourceTemplates.Output, `"uriTemplate": "codog://resume/{name}"`)

	out, err = runResumedJSON("/debug-tool-call", "ListMcpPromptsTool", `{}`)
	require.NoError(t, err)
	var resumedDebugMCPPrompts debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMCPPrompts))
	require.Equal(t, "debug_tool_call", resumedDebugMCPPrompts.Kind)
	require.Equal(t, "list_mcp_prompts", resumedDebugMCPPrompts.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugMCPPrompts.Permission)
	require.True(t, resumedDebugMCPPrompts.Success)
	require.Contains(t, resumedDebugMCPPrompts.Output, `"kind": "mcp_prompts"`)
	require.Contains(t, resumedDebugMCPPrompts.Output, `"server": "resume"`)
	require.Contains(t, resumedDebugMCPPrompts.Output, `"name": "review"`)

	out, err = runResumedJSON("/debug-tool-call", "GetMcpPromptTool", `{"server":"resume","prompt":"review","arguments":{"topic":"resume"}}`)
	require.NoError(t, err)
	var resumedDebugGetMCPPrompt debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugGetMCPPrompt))
	require.Equal(t, "debug_tool_call", resumedDebugGetMCPPrompt.Kind)
	require.Equal(t, "get_mcp_prompt", resumedDebugGetMCPPrompt.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugGetMCPPrompt.Permission)
	require.True(t, resumedDebugGetMCPPrompt.Success)
	require.Contains(t, resumedDebugGetMCPPrompt.Output, `"prompt": "review"`)
	require.Contains(t, resumedDebugGetMCPPrompt.Output, "Review resume")

	out, err = runResumedJSONWithStdin("y\n", "/debug-tool-call", "McpAuthTool", `{"server":"resume"}`)
	require.NoError(t, err)
	var resumedDebugMCPAuth debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMCPAuth))
	require.Equal(t, "debug_tool_call", resumedDebugMCPAuth.Kind)
	require.Equal(t, "mcp_auth", resumedDebugMCPAuth.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugMCPAuth.Permission)
	require.True(t, resumedDebugMCPAuth.Success)
	require.Contains(t, resumedDebugMCPAuth.Output, `"server": "resume"`)
	require.Contains(t, resumedDebugMCPAuth.Output, `"status": "ok"`)
	require.Contains(t, resumedDebugMCPAuth.Output, `"tool_count": 1`)

	out, err = runResumedJSONWithStdin("y\n", "/debug-tool-call", "MCP", `{"server":"resume","tool":"echo","arguments":{"text":"hi"}}`)
	require.NoError(t, err)
	var resumedDebugMCPDispatch debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMCPDispatch))
	require.Equal(t, "debug_tool_call", resumedDebugMCPDispatch.Kind)
	require.Equal(t, "mcp", resumedDebugMCPDispatch.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugMCPDispatch.Permission)
	require.True(t, resumedDebugMCPDispatch.Success)
	require.Contains(t, resumedDebugMCPDispatch.Output, `"text":"resume-echo"`)

	if gitAvailable {
		currentBranch, err := gitops.Branch(workspace)
		require.NoError(t, err)
		branchFreshnessInput, err := json.Marshal(map[string]string{"branch": currentBranch, "base": currentBranch})
		require.NoError(t, err)
		out, err = runResumedJSON("/debug-tool-call", "BranchFreshnessTool", string(branchFreshnessInput))
		require.NoError(t, err)
		var resumedDebugBranchFreshness debugToolCallReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugBranchFreshness))
		require.Equal(t, "debug_tool_call", resumedDebugBranchFreshness.Kind)
		require.Equal(t, "branch_freshness", resumedDebugBranchFreshness.Tool)
		require.Equal(t, tools.PermissionReadOnly, resumedDebugBranchFreshness.Permission)
		require.True(t, resumedDebugBranchFreshness.Success)
		require.Contains(t, resumedDebugBranchFreshness.Output, `"kind": "branch_freshness"`)
		require.Contains(t, resumedDebugBranchFreshness.Output, `"branch": "`+currentBranch+`"`)
		require.Contains(t, resumedDebugBranchFreshness.Output, `"base": "`+currentBranch+`"`)
	}

	out, err = runResumedJSON("/debug-tool-call", "RecoveryRecipeTool", `{"scenario":"stale_branch"}`)
	require.NoError(t, err)
	var resumedDebugRecoveryRecipe debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRecoveryRecipe))
	require.Equal(t, "debug_tool_call", resumedDebugRecoveryRecipe.Kind)
	require.Equal(t, "recovery_recipe", resumedDebugRecoveryRecipe.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugRecoveryRecipe.Permission)
	require.True(t, resumedDebugRecoveryRecipe.Success)
	require.Contains(t, resumedDebugRecoveryRecipe.Output, `"kind": "recovery_recipe"`)
	require.Contains(t, resumedDebugRecoveryRecipe.Output, `"kind": "merge_forward_branch"`)

	out, err = runResumedJSON("/debug-tool-call", "RecoveryStatusTool", `{"scenario":"stale_branch"}`)
	require.NoError(t, err)
	var resumedDebugRecoveryStatus debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRecoveryStatus))
	require.Equal(t, "debug_tool_call", resumedDebugRecoveryStatus.Kind)
	require.Equal(t, "recovery_status", resumedDebugRecoveryStatus.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugRecoveryStatus.Permission)
	require.True(t, resumedDebugRecoveryStatus.Success)
	require.Contains(t, resumedDebugRecoveryStatus.Output, `"kind": "recovery_status"`)
	require.Contains(t, resumedDebugRecoveryStatus.Output, `"attempted": false`)

	out, err = runResumedJSON("/debug-tool-call", "RecoveryAttemptTool", `{"scenario":"stale_branch","failure_summary":"resume debug recovery"}`)
	require.NoError(t, err)
	var resumedDebugRecoveryAttempt debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRecoveryAttempt))
	require.Equal(t, "debug_tool_call", resumedDebugRecoveryAttempt.Kind)
	require.Equal(t, "recovery_attempt", resumedDebugRecoveryAttempt.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugRecoveryAttempt.Permission)
	require.True(t, resumedDebugRecoveryAttempt.Success)
	require.Contains(t, resumedDebugRecoveryAttempt.Output, `"kind": "recovery_attempt"`)
	require.Contains(t, resumedDebugRecoveryAttempt.Output, `"kind": "recovered"`)
	require.Contains(t, resumedDebugRecoveryAttempt.Output, `"state": "succeeded"`)

	out, err = runResumedJSON("/debug-tool-call", "RetrieveContextTool", `{"query":" resumed debug routing ","top_k":9}`)
	require.NoError(t, err)
	var resumedDebugRetrieveContext debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRetrieveContext))
	require.Equal(t, "debug_tool_call", resumedDebugRetrieveContext.Kind)
	require.Equal(t, "retrieve_context", resumedDebugRetrieveContext.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugRetrieveContext.Permission)
	require.True(t, resumedDebugRetrieveContext.Success)
	require.Equal(t, "resumed debug routing", ragQuery.Query)
	require.Equal(t, 3, ragQuery.TopK)
	require.Contains(t, resumedDebugRetrieveContext.Output, "phase: 1-sqlite")
	require.Contains(t, resumedDebugRetrieveContext.Output, "path=internal/agent/agent.go")

	out, err = runResumedJSON("/debug-tool-call", "WebFetch", `{"url":"`+webServer.URL+`/page","prompt":"title"}`)
	require.NoError(t, err)
	var resumedDebugWebFetch debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWebFetch))
	require.Equal(t, "debug_tool_call", resumedDebugWebFetch.Kind)
	require.Equal(t, "web_fetch", resumedDebugWebFetch.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWebFetch.Permission)
	require.True(t, resumedDebugWebFetch.Success)
	require.Contains(t, resumedDebugWebFetch.Output, "Resume Web")
	require.Contains(t, resumedDebugWebFetch.Output, `"code": 200`)

	out, err = runResumedJSON("/debug-tool-call", "WebSearch", `{"query":"resume search"}`)
	require.NoError(t, err)
	var resumedDebugWebSearch debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWebSearch))
	require.Equal(t, "debug_tool_call", resumedDebugWebSearch.Kind)
	require.Equal(t, "web_search", resumedDebugWebSearch.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWebSearch.Permission)
	require.True(t, resumedDebugWebSearch.Success)
	require.Contains(t, resumedDebugWebSearch.Output, "Resume Search")
	require.Contains(t, resumedDebugWebSearch.Output, "A resumed search summary.")

	out, err = runResumedJSON("/debug-tool-call", "bash", `{"command":"echo resumed-debug-bash"}`)
	require.NoError(t, err)
	var resumedDebugBash debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugBash))
	require.Equal(t, "debug_tool_call", resumedDebugBash.Kind)
	require.Equal(t, "bash", resumedDebugBash.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugBash.Permission)
	require.True(t, resumedDebugBash.Success)
	require.Contains(t, resumedDebugBash.Output, "resumed-debug-bash")

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "PowerShellTool", `{"command":"Get-Content main.go","timeout_ms":10000}`)
	require.NoError(t, err)
	var resumedDebugPowerShell debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugPowerShell))
	require.Equal(t, "debug_tool_call", resumedDebugPowerShell.Kind)
	require.Equal(t, "powershell", resumedDebugPowerShell.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugPowerShell.Permission)
	require.True(t, resumedDebugPowerShell.Success)
	require.Contains(t, resumedDebugPowerShell.Output, `ps:-NoProfile -NonInteractive -Command Get-Content main.go`)
	require.Contains(t, resumedDebugPowerShell.Output, `"exit_code": 0`)

	out, err = runResumedJSON("/allowed-tools", "add", "TodoWrite", "--path", configPath)
	require.NoError(t, err)
	var resumedAllowedToolsAddTodo allowedToolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAllowedToolsAddTodo))
	require.Equal(t, "allowed_tools", resumedAllowedToolsAddTodo.Kind)
	require.Equal(t, "add", resumedAllowedToolsAddTodo.Action)
	require.Contains(t, resumedAllowedToolsAddTodo.Rules, "TodoWrite")
	require.Equal(t, configPath, resumedAllowedToolsAddTodo.Path)
	configData, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(configData), "TodoWrite")

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

	out, err = runResumedJSON("/terminal-setup", "install", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedTerminalInstall terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalInstall))
	require.Equal(t, "terminal_setup", resumedTerminalInstall.Kind)
	require.Equal(t, "install", resumedTerminalInstall.Action)
	require.Equal(t, "ok", resumedTerminalInstall.Status)
	require.Equal(t, "zsh", resumedTerminalInstall.Shell)
	require.Equal(t, terminalProfilePath, resumedTerminalInstall.Path)
	require.True(t, resumedTerminalInstall.Installed)
	require.True(t, resumedTerminalInstall.Changed)
	profileData, err = os.ReadFile(terminalProfilePath)
	require.NoError(t, err)
	require.Contains(t, string(profileData), "codog shell integration")

	out, err = runResumedJSON("/terminal-setup", "uninstall", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedTerminalUninstall terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalUninstall))
	require.Equal(t, "terminal_setup", resumedTerminalUninstall.Kind)
	require.Equal(t, "uninstall", resumedTerminalUninstall.Action)
	require.Equal(t, "ok", resumedTerminalUninstall.Status)
	require.Equal(t, terminalProfilePath, resumedTerminalUninstall.Path)
	require.False(t, resumedTerminalUninstall.Installed)
	require.True(t, resumedTerminalUninstall.Changed)
	profileData, err = os.ReadFile(terminalProfilePath)
	require.NoError(t, err)
	require.NotContains(t, string(profileData), "codog shell integration")

	out, err = runResumedJSON("/terminalSetup", "status", "--shell", "zsh", "--path", terminalProfilePath)
	require.NoError(t, err)
	var resumedTerminalAlias terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalAlias))
	require.Equal(t, "terminal_setup", resumedTerminalAlias.Kind)
	require.Equal(t, "status", resumedTerminalAlias.Action)

	terminalKeybindingsPath := filepath.Join(workspace, "keybindings.json")
	out, err = runResumedJSON("/terminal-setup", "install", "--target", "vscode", "--path", terminalKeybindingsPath)
	require.NoError(t, err)
	var resumedTerminalTarget terminalsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTerminalTarget))
	require.Equal(t, "terminal_setup", resumedTerminalTarget.Kind)
	require.Equal(t, "install", resumedTerminalTarget.Action)
	require.Equal(t, "vscode", resumedTerminalTarget.Target)
	require.Equal(t, terminalKeybindingsPath, resumedTerminalTarget.Path)
	require.True(t, resumedTerminalTarget.Installed)
	require.True(t, resumedTerminalTarget.Changed)
	keybindingData, err := os.ReadFile(terminalKeybindingsPath)
	require.NoError(t, err)
	require.Contains(t, string(keybindingData), `"key": "shift+enter"`)
	require.Contains(t, string(keybindingData), `workbench.action.terminal.sendSequence`)

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

	out, err = runResumedJSON("/advisor", "claude-opus")
	require.NoError(t, err)
	var resumedAdvisorSet advisorReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAdvisorSet))
	require.Equal(t, "advisor", resumedAdvisorSet.Kind)
	require.Equal(t, "set", resumedAdvisorSet.Action)
	require.Equal(t, "claude-opus", resumedAdvisorSet.Model)
	require.Equal(t, "claude-test", resumedAdvisorSet.MainModel)
	require.NotEmpty(t, resumedAdvisorSet.Path)

	out, err = runResumedJSON("/advisor", "off")
	require.NoError(t, err)
	var resumedAdvisorClear advisorReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAdvisorClear))
	require.Equal(t, "advisor", resumedAdvisorClear.Kind)
	require.Equal(t, "clear", resumedAdvisorClear.Action)
	require.Empty(t, resumedAdvisorClear.Model)
	require.Equal(t, "claude-test", resumedAdvisorClear.MainModel)
	require.NotEmpty(t, resumedAdvisorClear.Path)

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

	out, err = runResumedJSON("/sandbox-toggle", "off")
	require.NoError(t, err)
	var resumedSandboxToggleOff sandboxToggleReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSandboxToggleOff))
	require.Equal(t, "sandbox_toggle", resumedSandboxToggleOff.Kind)
	require.Equal(t, "set", resumedSandboxToggleOff.Action)
	require.Equal(t, "off", resumedSandboxToggleOff.ConfiguredStrategy)
	require.False(t, resumedSandboxToggleOff.Enabled)
	require.NotEmpty(t, resumedSandboxToggleOff.Path)

	out, err = runResumedJSON("/sandbox-toggle", "clear")
	require.NoError(t, err)
	var resumedSandboxToggleClear sandboxToggleReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSandboxToggleClear))
	require.Equal(t, "sandbox_toggle", resumedSandboxToggleClear.Kind)
	require.Equal(t, "clear", resumedSandboxToggleClear.Action)
	require.Empty(t, resumedSandboxToggleClear.ConfiguredStrategy)
	require.NotEmpty(t, resumedSandboxToggleClear.Path)

	out, err = runResumedJSON("/sandbox-toggle", "detect")
	require.NoError(t, err)
	var resumedSandboxToggleDetect sandboxToggleReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSandboxToggleDetect))
	require.Equal(t, "sandbox_toggle", resumedSandboxToggleDetect.Kind)
	require.Equal(t, "set", resumedSandboxToggleDetect.Action)
	require.Equal(t, "detect", resumedSandboxToggleDetect.ConfiguredStrategy)
	require.NotEmpty(t, resumedSandboxToggleDetect.Path)

	out, err = runResumedJSON("/mcp", "list")
	require.NoError(t, err)
	var resumedMCP mcpListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMCP))
	require.Equal(t, "mcp", resumedMCP.Kind)
	require.Equal(t, "list", resumedMCP.Action)

	out, err = runResumedJSON("/capabilities")
	require.NoError(t, err)
	var resumedCapabilities capabilitiesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCapabilities))
	require.Equal(t, "capabilities", resumedCapabilities.Kind)
	require.Equal(t, "show", resumedCapabilities.Action)
	require.True(t, capabilityReportHasSlash(resumedCapabilities, "/capabilities"))

	out, err = runResumedJSON("/acp")
	require.NoError(t, err)
	var resumedACP acpStatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedACP))
	require.Equal(t, "acp", resumedACP.Kind)
	require.Equal(t, "status", resumedACP.Action)
	require.False(t, resumedACP.Protocol.Daemon)

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

	out, err = runResumedJSON("/skills", "install", skillSourcePath)
	require.NoError(t, err)
	var resumedSkillInstall skills.InstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkillInstall))
	require.Equal(t, "skills", resumedSkillInstall.Kind)
	require.Equal(t, "install", resumedSkillInstall.Action)
	require.Equal(t, "review", resumedSkillInstall.Name)
	require.Equal(t, "user", resumedSkillInstall.Target)
	require.FileExists(t, filepath.Join(configHome, "skills", "review.md"))

	out, err = runResumedJSON("/skills", "add", "--project", "--name", "review-copy", skillSourcePath)
	require.NoError(t, err)
	var resumedSkillAdd skills.InstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkillAdd))
	require.Equal(t, "skills", resumedSkillAdd.Kind)
	require.Equal(t, "install", resumedSkillAdd.Action)
	require.Equal(t, "review-copy", resumedSkillAdd.Name)
	require.Equal(t, "workspace", resumedSkillAdd.Target)
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "review-copy.md"))

	out, err = runResumedJSON("/skills", "invoke", "debug", "failing test")
	require.NoError(t, err)
	var resumedSkillInvoke struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Rendered string `json:"rendered"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkillInvoke))
	require.Equal(t, "skill_invocation", resumedSkillInvoke.Kind)
	require.Equal(t, "debug", resumedSkillInvoke.Name)
	require.Contains(t, resumedSkillInvoke.Rendered, "User request: failing test")

	out, err = runResumedJSON("/skill", "uninstall", "review")
	require.NoError(t, err)
	var resumedSkillUninstall skills.UninstallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSkillUninstall))
	require.Equal(t, "skills", resumedSkillUninstall.Kind)
	require.Equal(t, "uninstall", resumedSkillUninstall.Action)
	require.Equal(t, "review", resumedSkillUninstall.Name)
	require.True(t, resumedSkillUninstall.Removed)
	require.NoFileExists(t, filepath.Join(configHome, "skills", "review.md"))

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

	out, err = runResumedJSON("/hooks", "run", "pre", "--tool", "read_file", "--input", `{"path":"README.md"}`)
	require.NoError(t, err)
	var resumedHooksRun hooks.RunReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHooksRun))
	require.Equal(t, "hooks", resumedHooksRun.Kind)
	require.Equal(t, "pre_tool_use", resumedHooksRun.Event)
	require.Equal(t, "read_file", resumedHooksRun.Tool)
	require.Equal(t, "ok", resumedHooksRun.Status)
	require.Equal(t, 1, resumedHooksRun.Count)
	require.Len(t, resumedHooksRun.Results, 1)
	require.True(t, resumedHooksRun.Results[0].Success)
	require.Contains(t, resumedHooksRun.Results[0].Stdout, "hook-ok")

	watchPathsDir := filepath.Join(configHome, "hooks", "watch-paths")
	require.NoError(t, os.MkdirAll(watchPathsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(watchPathsDir, "resume-slash.json"), []byte(`{"kind":"session_start_watch_paths","session_id":"resume-slash","paths":["main.go"]}`), 0o644))
	out, err = runResumedJSON("/hooks", "watch-paths", "list", "resume-slash")
	require.NoError(t, err)
	var resumedHooksWatchPaths hooksWatchPathsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHooksWatchPaths))
	require.Equal(t, "hooks_watch_paths", resumedHooksWatchPaths.Kind)
	require.Equal(t, "list", resumedHooksWatchPaths.Action)
	require.Equal(t, "ok", resumedHooksWatchPaths.Status)
	require.Equal(t, "resume-slash", resumedHooksWatchPaths.SessionID)
	require.Equal(t, []string{"main.go"}, resumedHooksWatchPaths.Paths)

	out, err = runResumedJSON("/agents", "ls")
	require.NoError(t, err)
	var resumedAgents agentsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgents))
	require.Equal(t, "agents", resumedAgents.Kind)
	require.Equal(t, "list", resumedAgents.Action)

	out, err = runResumedJSON("/agents", "runs")
	require.NoError(t, err)
	var resumedAgentRuns agentRunsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgentRuns))
	require.Equal(t, "agents", resumedAgentRuns.Kind)
	require.Equal(t, "runs", resumedAgentRuns.Action)

	out, err = runResumedJSON("/agents", "create", "reviewer")
	require.NoError(t, err)
	var resumedAgentCreate agentCreateReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgentCreate))
	require.Equal(t, "agents", resumedAgentCreate.Kind)
	require.Equal(t, "create", resumedAgentCreate.Action)
	require.Equal(t, "reviewer", resumedAgentCreate.Name)
	require.Equal(t, "created", resumedAgentCreate.Result)
	require.FileExists(t, filepath.Join(workspace, ".codog", "agents", "reviewer.json"))

	if gitAvailable {
		allocation, err := worktree.Allocate(workspace, "resume-agent")
		require.NoError(t, err)
		require.DirExists(t, allocation.Path)

		out, err = runResumedJSON("/agents", "worktree-rm", allocation.ID)
		require.NoError(t, err)
		var resumedAgentWorktreeRemove struct {
			Removed bool   `json:"removed"`
			ID      string `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &resumedAgentWorktreeRemove))
		require.True(t, resumedAgentWorktreeRemove.Removed)
		require.Equal(t, allocation.ID, resumedAgentWorktreeRemove.ID)
		require.NoDirExists(t, allocation.Path)
	}

	out, err = runResumedJSON("/subagent", "list")
	require.NoError(t, err)
	var resumedSubagentRuns agentRunsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSubagentRuns))
	require.Equal(t, "agents", resumedSubagentRuns.Kind)
	require.Equal(t, "runs", resumedSubagentRuns.Action)

	out, err = runResumedJSON("/plugins", "ls")
	require.NoError(t, err)
	var resumedPlugins pluginsListReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlugins))
	require.Equal(t, "plugin", resumedPlugins.Kind)
	require.Equal(t, "list", resumedPlugins.Action)

	out, err = runResumedJSON("/plugins", "health")
	require.NoError(t, err)
	var resumedPluginHealth pluginHealthReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginHealth))
	require.Equal(t, "plugin_health", resumedPluginHealth.Kind)
	require.Equal(t, "health", resumedPluginHealth.Action)
	require.Equal(t, "healthy", resumedPluginHealth.Status)
	require.Equal(t, 1, resumedPluginHealth.Total)
	require.Equal(t, 1, resumedPluginHealth.Healthy)

	out, err = runResumedJSON("/plugins", "add", pluginInstallSource)
	require.NoError(t, err)
	var resumedPluginInstall plugins.Manifest
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginInstall))
	require.Equal(t, "resume-install", resumedPluginInstall.ID)
	require.Equal(t, "Resume Install", resumedPluginInstall.Name)
	require.True(t, resumedPluginInstall.Enabled)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "resume-install", "plugin.json"))
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "resume-install", "tool.sh"))

	out, err = runResumedJSON("/plugins", "off", "resume-install")
	require.NoError(t, err)
	var resumedPluginDisable plugins.Manifest
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginDisable))
	require.Equal(t, "resume-install", resumedPluginDisable.ID)
	require.False(t, resumedPluginDisable.Enabled)

	out, err = runResumedJSON("/plugins", "on", "resume-install")
	require.NoError(t, err)
	var resumedPluginEnable plugins.Manifest
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginEnable))
	require.Equal(t, "resume-install", resumedPluginEnable.ID)
	require.True(t, resumedPluginEnable.Enabled)

	out, err = runResumedJSON("/plugins", "details", "resume-install")
	require.NoError(t, err)
	var resumedPluginShow struct {
		Kind   string           `json:"kind"`
		Action string           `json:"action"`
		Status string           `json:"status"`
		Plugin plugins.Manifest `json:"plugin"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginShow))
	require.Equal(t, "plugin", resumedPluginShow.Kind)
	require.Equal(t, "show", resumedPluginShow.Action)
	require.Equal(t, "ok", resumedPluginShow.Status)
	require.Equal(t, "resume-install", resumedPluginShow.Plugin.ID)

	out, err = runResumedJSON("/reload-plugins")
	require.NoError(t, err)
	var resumedReloadPlugins reloadPluginsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReloadPlugins))
	require.Equal(t, "reload_plugins", resumedReloadPlugins.Kind)
	require.True(t, resumedReloadPlugins.Reloaded)
	require.Equal(t, 2, resumedReloadPlugins.Plugins)
	require.Equal(t, 2, resumedReloadPlugins.PluginTools)
	require.Contains(t, resumedReloadPlugins.PluginIDs, "resume-demo")
	require.Contains(t, resumedReloadPlugins.PluginIDs, "resume-install")
	require.Contains(t, resumedReloadPlugins.EnabledPluginIDs, "resume-demo")
	require.Contains(t, resumedReloadPlugins.EnabledPluginIDs, "resume-install")
	require.GreaterOrEqual(t, resumedReloadPlugins.ToolCountAfter, resumedReloadPlugins.ToolCountBefore)

	out, err = runResumedJSON("/plugins", "rm", "resume-install")
	require.NoError(t, err)
	var resumedPluginRemove struct {
		Removed bool   `json:"removed"`
		ID      string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPluginRemove))
	require.True(t, resumedPluginRemove.Removed)
	require.Equal(t, "resume-install", resumedPluginRemove.ID)
	require.NoFileExists(t, filepath.Join(workspace, ".codog", "plugins", "resume-install", "plugin.json"))

	out, err = runResumedJSON("/tasks", "ls")
	require.NoError(t, err)
	var resumedTasks backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTasks))
	require.Equal(t, "background", resumedTasks.Kind)
	require.Equal(t, "list", resumedTasks.Action)
	require.Equal(t, "ok", resumedTasks.Status)
	require.Empty(t, resumedTasks.Tasks)

	out, err = runResumedJSON("/tasks", "board")
	require.NoError(t, err)
	var resumedTaskBoard backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTaskBoard))
	require.Equal(t, "background", resumedTaskBoard.Kind)
	require.Equal(t, "board", resumedTaskBoard.Action)
	require.Equal(t, "ok", resumedTaskBoard.Status)
	require.NotNil(t, resumedTaskBoard.Board)
	require.Empty(t, resumedTaskBoard.Board.Active)
	require.Empty(t, resumedTaskBoard.Board.Blocked)
	require.Empty(t, resumedTaskBoard.Board.Finished)

	out, err = runResumedJSON("/tasks", "run", "echo", "resumed-task")
	require.NoError(t, err)
	var resumedTaskRunReport backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTaskRunReport))
	require.Equal(t, "background", resumedTaskRunReport.Kind)
	require.Equal(t, "run", resumedTaskRunReport.Action)
	require.Equal(t, "ok", resumedTaskRunReport.Status)
	require.NotNil(t, resumedTaskRunReport.Task)
	resumedTaskRun := *resumedTaskRunReport.Task
	require.Equal(t, "resume-slash", resumedTaskRun.SessionID)
	require.Equal(t, "echo resumed-task", resumedTaskRun.Command)
	resumedTaskWorkspace, err := filepath.EvalSymlinks(resumedTaskRun.Workspace)
	require.NoError(t, err)
	expectedTaskWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, expectedTaskWorkspace, resumedTaskWorkspace)

	out, err = runResumedJSON("/tasks", "get", resumedTaskRun.ID)
	require.NoError(t, err)
	var resumedTaskGetReport backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTaskGetReport))
	require.Equal(t, "background", resumedTaskGetReport.Kind)
	require.Equal(t, "status", resumedTaskGetReport.Action)
	require.Equal(t, "ok", resumedTaskGetReport.Status)
	require.NotNil(t, resumedTaskGetReport.Task)
	resumedTaskGet := *resumedTaskGetReport.Task
	require.Equal(t, resumedTaskRun.ID, resumedTaskGet.ID)
	require.Equal(t, "echo resumed-task", resumedTaskGet.Command)

	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedTaskRun.ID, 1024)
		return err == nil && strings.Contains(logs, "resumed-task")
	}, 2*time.Second, 50*time.Millisecond)
	out, err = runResumedJSON("/tasks", "log", resumedTaskRun.ID, "--bytes", "1024")
	require.NoError(t, err)
	require.Contains(t, out, "resumed-task")

	out, err = runResumedJSON("/agents", "run", "reviewer", "check auth")
	require.NoError(t, err)
	var resumedAgentRun agentRunReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgentRun))
	require.Equal(t, "agents", resumedAgentRun.Kind)
	require.Equal(t, "run", resumedAgentRun.Action)
	require.Equal(t, "ok", resumedAgentRun.Status)
	require.Equal(t, "reviewer", resumedAgentRun.Agent)
	require.Equal(t, "check auth", resumedAgentRun.Run.Prompt)
	require.Equal(t, "agent", resumedAgentRun.Task.Kind)
	require.Equal(t, resumedAgentRun.Task.ID, resumedAgentRun.Run.TaskID)
	require.Equal(t, "resume-slash", resumedAgentRun.Task.SessionID)

	out, err = runResumedJSON("/agents", "run-remove", resumedAgentRun.Run.ID)
	require.NoError(t, err)
	var resumedAgentRunRemove struct {
		Kind    string `json:"kind"`
		Action  string `json:"action"`
		Status  string `json:"status"`
		Removed bool   `json:"removed"`
		ID      string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAgentRunRemove))
	require.Equal(t, "agents", resumedAgentRunRemove.Kind)
	require.Equal(t, "run-remove", resumedAgentRunRemove.Action)
	require.Equal(t, "ok", resumedAgentRunRemove.Status)
	require.True(t, resumedAgentRunRemove.Removed)
	require.Equal(t, resumedAgentRun.Run.ID, resumedAgentRunRemove.ID)
	_, err = agentruns.NewStore(configHome).Get(resumedAgentRun.Run.ID)
	require.Error(t, err)

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

	out, err = runResumedJSON("/cron", "run-due", "--now", "2026-07-01T00:00:00Z")
	require.NoError(t, err)
	var resumedCronRunDue cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronRunDue))
	require.Equal(t, "cron", resumedCronRunDue.Kind)
	require.Equal(t, "run-due", resumedCronRunDue.Action)
	require.Equal(t, 1, resumedCronRunDue.Count)
	require.Len(t, resumedCronRunDue.Tasks, 1)
	require.Equal(t, "cron", resumedCronRunDue.Tasks[0].Kind)
	require.Equal(t, 1, resumedCronRunDue.Entries[0].RunCount)

	out, err = runResumedJSON("/cron", "create", "@hourly", "resume created cron")
	require.NoError(t, err)
	var resumedCronCreate cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronCreate))
	require.Equal(t, "cron", resumedCronCreate.Kind)
	require.Equal(t, "create", resumedCronCreate.Action)
	require.NotNil(t, resumedCronCreate.Entry)
	require.Equal(t, "@hourly", resumedCronCreate.Entry.Schedule)
	require.Equal(t, "resume created cron", resumedCronCreate.Entry.Prompt)

	out, err = runResumedJSON("/cron", "mark-run", resumedCronCreate.Entry.ID, "--now", "2026-07-01T01:00:00Z")
	require.NoError(t, err)
	var resumedCronMarkRun cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronMarkRun))
	require.Equal(t, "cron", resumedCronMarkRun.Kind)
	require.Equal(t, "mark-run", resumedCronMarkRun.Action)
	require.NotNil(t, resumedCronMarkRun.Entry)
	require.Equal(t, resumedCronCreate.Entry.ID, resumedCronMarkRun.Entry.ID)
	require.Equal(t, 1, resumedCronMarkRun.Entry.RunCount)
	require.NotNil(t, resumedCronMarkRun.Entry.LastRunAt)

	out, err = runResumedJSON("/cron", "delete", cronEntry.ID)
	require.NoError(t, err)
	var resumedCronDelete cronCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCronDelete))
	require.Equal(t, "cron", resumedCronDelete.Kind)
	require.Equal(t, "delete", resumedCronDelete.Action)
	require.NotNil(t, resumedCronDelete.Entry)
	require.Equal(t, cronEntry.ID, resumedCronDelete.Entry.ID)

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

	out, err = runResumedJSON("/team", "delete", teamEntry.ID)
	require.NoError(t, err)
	var resumedTeamDelete teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamDelete))
	require.Equal(t, "team", resumedTeamDelete.Kind)
	require.Equal(t, "delete", resumedTeamDelete.Action)
	require.Equal(t, teamEntry.ID, resumedTeamDelete.Team.ID)
	require.Equal(t, "deleted", resumedTeamDelete.Team.Status)
	require.Equal(t, "Team deleted", resumedTeamDelete.Message)

	out, err = runResumedJSON("/team", "create", "writers", "check draft")
	require.NoError(t, err)
	var resumedTeamCreate teamCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTeamCreate))
	require.Equal(t, "team", resumedTeamCreate.Kind)
	require.Equal(t, "create", resumedTeamCreate.Action)
	require.NotNil(t, resumedTeamCreate.Team)
	require.Equal(t, "writers", resumedTeamCreate.Team.Name)
	require.Equal(t, "Team created", resumedTeamCreate.Message)
	require.Len(t, resumedTeamCreate.Tasks, 1)
	require.Equal(t, "team", resumedTeamCreate.Tasks[0].Kind)

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

	out, err = runResumedJSON("/perf-issue", "--write")
	require.NoError(t, err)
	var resumedPerfIssueWrite perfissue.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPerfIssueWrite))
	require.Equal(t, "perf_issue", resumedPerfIssueWrite.Kind)
	require.NotEmpty(t, resumedPerfIssueWrite.File)
	require.Greater(t, resumedPerfIssueWrite.Bytes, 0)
	require.FileExists(t, resumedPerfIssueWrite.File)

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

	out, err = runResumedJSON("/think-back", "--year", "2026")
	require.NoError(t, err)
	var resumedThinkBackDefault thinkback.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedThinkBackDefault))
	require.Equal(t, "think_back", resumedThinkBackDefault.Kind)
	require.True(t, strings.HasSuffix(resumedThinkBackDefault.Output, filepath.Join(".codog", "think-back-2026.html")))
	require.True(t, resumedThinkBackDefault.Written)
	require.FileExists(t, resumedThinkBackDefault.Output)

	out, err = runResumedJSON("/thinkback")
	require.NoError(t, err)
	var resumedThinkbackDefault thinkback.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedThinkbackDefault))
	require.Equal(t, "think_back", resumedThinkbackDefault.Kind)
	require.True(t, resumedThinkbackDefault.Written)
	require.Contains(t, resumedThinkbackDefault.Output, filepath.Join(".codog", "think-back-"))
	require.FileExists(t, resumedThinkbackDefault.Output)

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

	out, err = runResumedJSON("/remote-env", "show", "--auth-token", "secret")
	require.NoError(t, err)
	var resumedRemoteEnvAuth remoteEnvReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteEnvAuth))
	require.Equal(t, "remote_env", resumedRemoteEnvAuth.Kind)
	require.Equal(t, "show", resumedRemoteEnvAuth.Action)
	require.True(t, resumedRemoteEnvAuth.AuthTokenConfigured)
	require.NotContains(t, out, "secret")

	out, err = runResumedJSON("/remote-env", "set", "--enabled", "off")
	require.NoError(t, err)
	var resumedRemoteEnvSet remoteEnvReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteEnvSet))
	require.Equal(t, "remote_env", resumedRemoteEnvSet.Kind)
	require.Equal(t, "set", resumedRemoteEnvSet.Action)
	require.False(t, resumedRemoteEnvSet.Enabled)
	require.True(t, resumedRemoteEnvSet.AuthTokenConfigured)
	require.NotEmpty(t, resumedRemoteEnvSet.Path)

	out, err = runResumedJSON("/remote-env", "clear")
	require.NoError(t, err)
	var resumedRemoteEnvClear remoteEnvReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteEnvClear))
	require.Equal(t, "remote_env", resumedRemoteEnvClear.Kind)
	require.Equal(t, "clear", resumedRemoteEnvClear.Action)
	require.False(t, resumedRemoteEnvClear.Enabled)
	require.False(t, resumedRemoteEnvClear.AuthTokenConfigured)
	require.Equal(t, 0, resumedRemoteEnvClear.LeaseSeconds)
	require.NotEmpty(t, resumedRemoteEnvClear.Path)

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

	out, err = runResumedJSON("/remote-setup", "enable", "--addr", "127.0.0.1:8794")
	require.NoError(t, err)
	var resumedRemoteSetupEnable remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteSetupEnable))
	require.Equal(t, "remote_setup", resumedRemoteSetupEnable.Kind)
	require.Equal(t, "enable", resumedRemoteSetupEnable.Action)
	require.True(t, resumedRemoteSetupEnable.Enabled)
	require.True(t, resumedRemoteSetupEnable.Ready)
	require.Equal(t, "http://127.0.0.1:8794", resumedRemoteSetupEnable.RemoteURL)
	require.NotEmpty(t, resumedRemoteSetupEnable.Path)

	out, err = runResumedJSON("/remote-setup", "disable")
	require.NoError(t, err)
	var resumedRemoteSetupDisable remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteSetupDisable))
	require.Equal(t, "remote_setup", resumedRemoteSetupDisable.Kind)
	require.Equal(t, "disable", resumedRemoteSetupDisable.Action)
	require.False(t, resumedRemoteSetupDisable.Enabled)
	require.NotEmpty(t, resumedRemoteSetupDisable.Path)

	out, err = runResumedJSON("/remote-setup", "clear")
	require.NoError(t, err)
	var resumedRemoteSetupClear remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoteSetupClear))
	require.Equal(t, "remote_setup", resumedRemoteSetupClear.Kind)
	require.Equal(t, "clear", resumedRemoteSetupClear.Action)
	require.False(t, resumedRemoteSetupClear.Enabled)
	require.False(t, resumedRemoteSetupClear.AuthTokenConfigured)
	require.Equal(t, 0, resumedRemoteSetupClear.LeaseSeconds)
	require.NotEmpty(t, resumedRemoteSetupClear.Path)

	out, err = runResumedJSON("/web-setup", "status", "--addr", "127.0.0.1:8793")
	require.NoError(t, err)
	var resumedWebSetup remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWebSetup))
	require.Equal(t, "remote_setup", resumedWebSetup.Kind)
	require.Equal(t, "status", resumedWebSetup.Action)
	require.Equal(t, "http://127.0.0.1:8793", resumedWebSetup.RemoteURL)

	out, err = runResumedJSON("/web-setup", "enable")
	require.NoError(t, err)
	var resumedWebSetupEnable remoteSetupReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWebSetupEnable))
	require.Equal(t, "remote_setup", resumedWebSetupEnable.Kind)
	require.Equal(t, "enable", resumedWebSetupEnable.Action)
	require.True(t, resumedWebSetupEnable.Enabled)
	require.NotEmpty(t, resumedWebSetupEnable.Path)

	out, err = runResumedJSON("/ide")
	require.NoError(t, err)
	var resumedIDE ideReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedIDE))
	require.Equal(t, "ide", resumedIDE.Kind)
	require.Equal(t, "status", resumedIDE.Action)

	out, err = runResumedJSON("/ide", "clear")
	require.NoError(t, err)
	var resumedIDEClear ideReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedIDEClear))
	require.Equal(t, "ide", resumedIDEClear.Kind)
	require.Equal(t, "clear", resumedIDEClear.Action)
	require.True(t, resumedIDEClear.Cleared)

	out, err = runResumedJSON("/bridge-kick")
	require.NoError(t, err)
	var resumedBridgeKick bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBridgeKick))
	require.Equal(t, "bridge_kick", resumedBridgeKick.Kind)
	require.Equal(t, "status", resumedBridgeKick.Action)

	out, err = runResumedJSON("/bridge-kick", "clear")
	require.NoError(t, err)
	var resumedBridgeKickClear bridgeKickReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBridgeKickClear))
	require.Equal(t, "bridge_kick", resumedBridgeKickClear.Kind)
	require.Equal(t, "clear", resumedBridgeKickClear.Action)
	require.True(t, resumedBridgeKickClear.Cleared)

	out, err = runResumedJSON("/workspace")
	require.NoError(t, err)
	var resumedWorkspace workspaceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWorkspace))
	require.Equal(t, "workspace", resumedWorkspace.Kind)
	require.Equal(t, "status", resumedWorkspace.Action)
	expectedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, expectedWorkspace, resumedWorkspace.Workspace)

	out, err = runResumedJSON("/workspace", "set", externalContext)
	require.NoError(t, err)
	var resumedWorkspaceSet workspaceReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedWorkspaceSet))
	require.Equal(t, "workspace", resumedWorkspaceSet.Kind)
	require.Equal(t, "set", resumedWorkspaceSet.Action)
	require.Equal(t, externalContext, resumedWorkspaceSet.Workspace)
	require.Equal(t, expectedWorkspace, resumedWorkspaceSet.PreviousWorkspace)
	require.True(t, resumedWorkspaceSet.Changed)
	require.True(t, resumedWorkspaceSet.Exists)
	require.True(t, resumedWorkspaceSet.IsDir)

	out, err = runResumedJSON("/scope", "preview")
	require.NoError(t, err)
	var resumedScope map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resumedScope))
	require.Equal(t, "safer_scope", resumedScope["kind"])
	require.Equal(t, "preview", resumedScope["action"])

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

	out, err = runResumedJSON("/ant-trace", "--no-request", "--write")
	require.NoError(t, err)
	var resumedAntTraceWrite anttrace.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAntTraceWrite))
	require.Equal(t, "ant_trace", resumedAntTraceWrite.Kind)
	require.False(t, resumedAntTraceWrite.RequestSent)
	require.NotEmpty(t, resumedAntTraceWrite.File)
	require.Greater(t, resumedAntTraceWrite.Bytes, 0)
	require.FileExists(t, resumedAntTraceWrite.File)

	out, err = runResumedJSON("/ant-trace", "--base-url", antTraceServer.URL, "--message", "resumed trace", "--timeout-ms", "1000")
	require.NoError(t, err)
	var resumedAntTraceRequest anttrace.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAntTraceRequest))
	require.Equal(t, "ant_trace", resumedAntTraceRequest.Kind)
	require.Equal(t, "ok", resumedAntTraceRequest.Status)
	require.True(t, resumedAntTraceRequest.RequestSent)
	require.Equal(t, antTraceServer.URL, resumedAntTraceRequest.BaseURL)
	require.Equal(t, "resumed trace ok", resumedAntTraceRequest.TextPreview)

	out, err = runResumedJSON("/mock-limits")
	require.NoError(t, err)
	var resumedMockLimits mocklimits.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMockLimits))
	require.Equal(t, "mock_limits", resumedMockLimits.Kind)
	require.Equal(t, "show", resumedMockLimits.Action)

	out, err = runResumedJSON("/mock-limits", "serve", "--addr", "127.0.0.1:0")
	require.NoError(t, err)
	var resumedMockLimitsServe backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMockLimitsServe))
	require.Equal(t, "background", resumedMockLimitsServe.Kind)
	require.Equal(t, "run", resumedMockLimitsServe.Action)
	require.Equal(t, "ok", resumedMockLimitsServe.Status)
	require.Equal(t, "resume-slash", resumedMockLimitsServe.SessionID)
	require.NotNil(t, resumedMockLimitsServe.Task)
	require.Equal(t, "mock_limits", resumedMockLimitsServe.Task.Kind)
	require.Contains(t, resumedMockLimitsServe.Task.Command, "mock-limits serve")
	require.Contains(t, resumedMockLimitsServe.Task.Command, "127.0.0.1:0")

	out, err = runResumedJSON("/mock-limits", "start", "--addr", "127.0.0.1:0")
	require.NoError(t, err)
	var resumedMockLimitsStart backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMockLimitsStart))
	require.Equal(t, "background", resumedMockLimitsStart.Kind)
	require.Equal(t, "run", resumedMockLimitsStart.Action)
	require.Equal(t, "ok", resumedMockLimitsStart.Status)
	require.NotNil(t, resumedMockLimitsStart.Task)
	require.Equal(t, "mock_limits", resumedMockLimitsStart.Task.Kind)
	require.Contains(t, resumedMockLimitsStart.Task.Command, "mock-limits serve")
	require.NotContains(t, resumedMockLimitsStart.Task.Command, "mock-limits start")
	require.Contains(t, resumedMockLimitsStart.Task.Command, "127.0.0.1:0")

	out, err = runResumedJSON("/api", "start", "--addr", "127.0.0.1:0")
	require.NoError(t, err)
	var resumedAPIServe backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAPIServe))
	require.Equal(t, "background", resumedAPIServe.Kind)
	require.Equal(t, "run", resumedAPIServe.Action)
	require.Equal(t, "ok", resumedAPIServe.Status)
	require.Equal(t, "resume-slash", resumedAPIServe.SessionID)
	require.NotNil(t, resumedAPIServe.Task)
	require.Equal(t, "api", resumedAPIServe.Task.Kind)
	require.Equal(t, "resume-slash", resumedAPIServe.Task.SessionID)
	require.Contains(t, resumedAPIServe.Task.Command, "api serve 127.0.0.1:0")
	require.NotContains(t, resumedAPIServe.Task.Command, "api start")

	out, err = runResumedJSON("/mcp", "serve")
	require.NoError(t, err)
	var resumedMCPServe backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedMCPServe))
	require.Equal(t, "background", resumedMCPServe.Kind)
	require.Equal(t, "run", resumedMCPServe.Action)
	require.Equal(t, "ok", resumedMCPServe.Status)
	require.Equal(t, "resume-slash", resumedMCPServe.SessionID)
	require.NotNil(t, resumedMCPServe.Task)
	require.Equal(t, "mcp", resumedMCPServe.Task.Kind)
	require.Equal(t, "resume-slash", resumedMCPServe.Task.SessionID)
	require.Contains(t, resumedMCPServe.Task.Command, "mcp serve")

	out, err = runResumedJSON("/acp", "serve")
	require.NoError(t, err)
	var resumedACPServe backgroundCommandReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedACPServe))
	require.Equal(t, "background", resumedACPServe.Kind)
	require.Equal(t, "run", resumedACPServe.Action)
	require.Equal(t, "ok", resumedACPServe.Status)
	require.Equal(t, "resume-slash", resumedACPServe.SessionID)
	require.NotNil(t, resumedACPServe.Task)
	require.Equal(t, "acp", resumedACPServe.Task.Kind)
	require.Contains(t, resumedACPServe.Task.Command, "acp serve")

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

	out, err = runResumedJSON("/extra-usage", "status", "--admin")
	require.NoError(t, err)
	var resumedExtraUsageStatus extraUsageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExtraUsageStatus))
	require.Equal(t, "extra_usage", resumedExtraUsageStatus.Kind)
	require.Equal(t, "status", resumedExtraUsageStatus.Action)
	require.Equal(t, "admin", resumedExtraUsageStatus.Mode)
	require.Equal(t, extraUsageAdminURL, resumedExtraUsageStatus.URL)
	require.False(t, resumedExtraUsageStatus.Opened)
	require.Equal(t, 0, resumedExtraUsageStatus.VisitCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/extra-usage")
	require.NoError(t, err)
	var resumedExtraUsageDefault extraUsageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExtraUsageDefault))
	require.Equal(t, "extra_usage", resumedExtraUsageDefault.Kind)
	require.Equal(t, "show", resumedExtraUsageDefault.Action)
	require.Equal(t, "personal", resumedExtraUsageDefault.Mode)
	require.Equal(t, extraUsagePersonalURL, resumedExtraUsageDefault.URL)
	require.False(t, resumedExtraUsageDefault.Opened)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/extra-usage", "--open")
	require.NoError(t, err)
	var resumedExtraUsageOpen extraUsageReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExtraUsageOpen))
	require.Equal(t, "extra_usage", resumedExtraUsageOpen.Kind)
	require.Equal(t, "show", resumedExtraUsageOpen.Action)
	require.False(t, resumedExtraUsageOpen.Opened)
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

	out, err = runResumedJSON("/install-slack-app", "status")
	require.NoError(t, err)
	var resumedSlackStatus installSlackAppReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSlackStatus))
	require.Equal(t, "install_slack_app", resumedSlackStatus.Kind)
	require.Equal(t, "status", resumedSlackStatus.Action)
	require.Equal(t, slackAppURL, resumedSlackStatus.URL)
	require.False(t, resumedSlackStatus.Opened)
	require.Equal(t, 0, resumedSlackStatus.InstallCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/install-slack-app")
	require.NoError(t, err)
	var resumedSlackDefault installSlackAppReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSlackDefault))
	require.Equal(t, "install_slack_app", resumedSlackDefault.Kind)
	require.Equal(t, "show", resumedSlackDefault.Action)
	require.Equal(t, slackAppURL, resumedSlackDefault.URL)
	require.False(t, resumedSlackDefault.Opened)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/install-slack-app", "--open")
	require.NoError(t, err)
	var resumedSlackOpen installSlackAppReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedSlackOpen))
	require.Equal(t, "install_slack_app", resumedSlackOpen.Kind)
	require.Equal(t, "show", resumedSlackOpen.Action)
	require.False(t, resumedSlackOpen.Opened)
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

	out, err = runResumedJSON("/stickers", "status")
	require.NoError(t, err)
	var resumedStickersStatus stickersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedStickersStatus))
	require.Equal(t, "stickers", resumedStickersStatus.Kind)
	require.Equal(t, "status", resumedStickersStatus.Action)
	require.Equal(t, stickerOrderURL, resumedStickersStatus.URL)
	require.False(t, resumedStickersStatus.Opened)
	require.Equal(t, 0, resumedStickersStatus.OrderCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/stickers")
	require.NoError(t, err)
	var resumedStickersDefault stickersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedStickersDefault))
	require.Equal(t, "stickers", resumedStickersDefault.Kind)
	require.Equal(t, "show", resumedStickersDefault.Action)
	require.Equal(t, stickerOrderURL, resumedStickersDefault.URL)
	require.False(t, resumedStickersDefault.Opened)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/stickers", "--open")
	require.NoError(t, err)
	var resumedStickersOpen stickersReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedStickersOpen))
	require.Equal(t, "stickers", resumedStickersOpen.Kind)
	require.Equal(t, "show", resumedStickersOpen.Action)
	require.False(t, resumedStickersOpen.Opened)
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

	out, err = runResumedJSON("/passes", "status")
	require.NoError(t, err)
	var resumedPassesStatus passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPassesStatus))
	require.Equal(t, "passes", resumedPassesStatus.Kind)
	require.Equal(t, "status", resumedPassesStatus.Action)
	require.Equal(t, guestPassDocsURL, resumedPassesStatus.URL)
	require.Equal(t, "docs", resumedPassesStatus.URLSource)
	require.False(t, resumedPassesStatus.ReferralConfigured)
	require.False(t, resumedPassesStatus.Opened)
	require.Equal(t, 0, resumedPassesStatus.VisitCount)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/passes")
	require.NoError(t, err)
	var resumedPassesDefault passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPassesDefault))
	require.Equal(t, "passes", resumedPassesDefault.Kind)
	require.Equal(t, "show", resumedPassesDefault.Action)
	require.Equal(t, guestPassDocsURL, resumedPassesDefault.URL)
	require.False(t, resumedPassesDefault.Opened)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/passes", "open")
	require.NoError(t, err)
	var resumedPassesOpen passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPassesOpen))
	require.Equal(t, "passes", resumedPassesOpen.Kind)
	require.Equal(t, "show", resumedPassesOpen.Action)
	require.Equal(t, guestPassDocsURL, resumedPassesOpen.URL)
	require.False(t, resumedPassesOpen.Opened)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/passes", "set-url", "https://example.test/guest")
	require.NoError(t, err)
	var resumedPassesSet passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPassesSet))
	require.Equal(t, "passes", resumedPassesSet.Kind)
	require.Equal(t, "set-url", resumedPassesSet.Action)
	require.Equal(t, "https://example.test/guest", resumedPassesSet.URL)
	require.Equal(t, "https://example.test/guest", resumedPassesSet.ReferralURL)
	require.False(t, resumedPassesSet.Opened)
	require.NotEmpty(t, resumedPassesSet.Path)
	require.Empty(t, openedURL)

	out, err = runResumedJSON("/passes", "clear-url")
	require.NoError(t, err)
	var resumedPassesClear passesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPassesClear))
	require.Equal(t, "passes", resumedPassesClear.Kind)
	require.Equal(t, "clear-url", resumedPassesClear.Action)
	require.Equal(t, guestPassDocsURL, resumedPassesClear.URL)
	require.Empty(t, resumedPassesClear.ReferralURL)
	require.False(t, resumedPassesClear.Opened)
	require.NotEmpty(t, resumedPassesClear.Path)
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

	out, err = runResumedJSON("/heapdump")
	require.NoError(t, err)
	var resumedHeapDumpDefault heapDumpReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedHeapDumpDefault))
	require.Equal(t, "heapdump", resumedHeapDumpDefault.Kind)
	require.Equal(t, "ok", resumedHeapDumpDefault.Status)
	require.Contains(t, resumedHeapDumpDefault.Path, filepath.Join(".codog", "heap")+string(os.PathSeparator))
	require.True(t, resumedHeapDumpDefault.GC)
	require.Greater(t, resumedHeapDumpDefault.Bytes, int64(0))
	require.FileExists(t, resumedHeapDumpDefault.Path)

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

	out, err = runResumedJSON("/feedback", "resumed feedback")
	require.NoError(t, err)
	var resumedFeedback feedbackReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFeedback))
	require.Equal(t, "feedback", resumedFeedback.Kind)
	require.Equal(t, "write", resumedFeedback.Action)
	require.Equal(t, "resume-slash", resumedFeedback.SessionID)
	require.Equal(t, 3, resumedFeedback.SessionMessages)
	require.FileExists(t, resumedFeedback.File)
	feedbackData, err := os.ReadFile(resumedFeedback.File)
	require.NoError(t, err)
	require.Contains(t, string(feedbackData), "resumed feedback")
	require.Contains(t, string(feedbackData), "resume-slash")

	out, err = runResumedJSON("/bug", "resumed bug report")
	require.NoError(t, err)
	var resumedBug feedbackReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBug))
	require.Equal(t, "feedback", resumedBug.Kind)
	require.Equal(t, "resume-slash", resumedBug.SessionID)
	require.FileExists(t, resumedBug.File)

	out, err = runResumedJSON("/pr", "ship resumed changes")
	require.NoError(t, err)
	var resumedPR draftReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPR))
	require.Equal(t, "pr", resumedPR.Kind)
	require.Equal(t, "draft", resumedPR.Action)
	require.Equal(t, "resume-slash", resumedPR.SessionID)
	require.Equal(t, 3, resumedPR.SessionMessages)
	require.FileExists(t, resumedPR.File)
	prData, err := os.ReadFile(resumedPR.File)
	require.NoError(t, err)
	require.Contains(t, string(prData), "# Pull Request Draft")
	require.Contains(t, string(prData), "PR: ship resumed changes")
	require.Contains(t, string(prData), "resume-slash")

	if gitAvailable {
		out, err = runResumedJSON("/commit-push-pr", "resumed dry run", "--dry-run", "--no-pr")
		require.NoError(t, err)
		var resumedCommitPushPR prworkflow.Report
		require.NoError(t, json.Unmarshal([]byte(out), &resumedCommitPushPR))
		require.Equal(t, "commit_push_pr", resumedCommitPushPR.Kind)
		require.Equal(t, "planned", resumedCommitPushPR.Status)
		require.True(t, resumedCommitPushPR.DryRun)
		require.Equal(t, "resumed dry run", resumedCommitPushPR.Message)
		require.Empty(t, resumedCommitPushPR.PRURL)
		require.NotContains(t, commitPushPRStepNames(resumedCommitPushPR.Steps), "pull_request")
	}

	out, err = runResumedJSON("/install-github-app", "--workflow", "claude", "--dry-run")
	require.NoError(t, err)
	var resumedGitHubApp githubsetup.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedGitHubApp))
	require.Equal(t, "install_github_app", resumedGitHubApp.Kind)
	require.Equal(t, "setup", resumedGitHubApp.Action)
	require.True(t, resumedGitHubApp.DryRun)
	require.Len(t, resumedGitHubApp.Workflows, 1)
	require.Equal(t, "claude", resumedGitHubApp.Workflows[0].Name)
	require.NoFileExists(t, filepath.Join(workspace, ".github", "workflows", "claude.yml"))

	out, err = runResumedJSON("/issue", "track resumed issue")
	require.NoError(t, err)
	var resumedIssue draftReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedIssue))
	require.Equal(t, "issue", resumedIssue.Kind)
	require.Equal(t, "draft", resumedIssue.Action)
	require.Equal(t, "resume-slash", resumedIssue.SessionID)
	require.FileExists(t, resumedIssue.File)
	issueData, err := os.ReadFile(resumedIssue.File)
	require.NoError(t, err)
	require.Contains(t, string(issueData), "# Issue Draft")
	require.Contains(t, string(issueData), "Issue: track resumed issue")
	require.Contains(t, string(issueData), "resume-slash")

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

	formatWritePath := filepath.Join(workspace, "format_write.go")
	require.NoError(t, os.WriteFile(formatWritePath, []byte("package main\nfunc messy(){println(\"x\")}\n"), 0o644))
	out, err = runResumedJSON("/format", "format_write.go", "--write")
	require.NoError(t, err)
	var resumedFormatWrite formatReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFormatWrite))
	require.Equal(t, "format", resumedFormatWrite.Kind)
	require.True(t, resumedFormatWrite.Write)
	require.True(t, resumedFormatWrite.Result.Changed)
	formattedData, err := os.ReadFile(formatWritePath)
	require.NoError(t, err)
	require.Contains(t, string(formattedData), "func messy() {")

	out, err = runResumedJSON("/code-intel", "symbols")
	require.NoError(t, err)
	var resumedCodeIntelSymbols symbolsReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelSymbols))
	require.Equal(t, "symbols", resumedCodeIntelSymbols.Kind)
	require.GreaterOrEqual(t, resumedCodeIntelSymbols.Total, 2)

	out, err = runResumedJSON("/code-intel", "definition", "helper")
	require.NoError(t, err)
	var resumedCodeIntelDefinition definitionReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelDefinition))
	require.Equal(t, "definition", resumedCodeIntelDefinition.Kind)
	require.True(t, resumedCodeIntelDefinition.Found)

	out, err = runResumedJSON("/code-intel", "references", "helper")
	require.NoError(t, err)
	var resumedCodeIntelReferences referencesReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelReferences))
	require.Equal(t, "references", resumedCodeIntelReferences.Kind)
	require.GreaterOrEqual(t, resumedCodeIntelReferences.Total, 1)

	out, err = runResumedJSON("/code-intel", "teleport", "main.go")
	require.NoError(t, err)
	var resumedCodeIntelTeleport teleportReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelTeleport))
	require.Equal(t, "teleport", resumedCodeIntelTeleport.Kind)
	require.True(t, resumedCodeIntelTeleport.Found)

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
	require.NoError(t, err)
	var resumedCodeIntelNotebookEdit codeIntelNotebookEditReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedCodeIntelNotebookEdit))
	require.Equal(t, "notebook_edit", resumedCodeIntelNotebookEdit.Kind)
	require.Equal(t, "replace", resumedCodeIntelNotebookEdit.Result.Mode)
	require.Equal(t, 0, resumedCodeIntelNotebookEdit.Result.Index)
	require.Equal(t, 1, resumedCodeIntelNotebookEdit.Result.CellCount)

	out, err = runResumedJSON("/notebook-edit", "analysis.ipynb", "--mode", "insert", "--cell-type", "markdown", "--source", "inserted note")
	require.NoError(t, err)
	var resumedNotebookEdit codeIntelNotebookEditReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedNotebookEdit))
	require.Equal(t, "notebook_edit", resumedNotebookEdit.Kind)
	require.Equal(t, "insert", resumedNotebookEdit.Result.Mode)
	require.Equal(t, 1, resumedNotebookEdit.Result.Index)
	require.Equal(t, "markdown", resumedNotebookEdit.Result.CellType)
	require.Equal(t, 2, resumedNotebookEdit.Result.CellCount)

	out, err = runResumedJSON("/reset", "status")
	require.NoError(t, err)
	var resumedReset resetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedReset))
	require.Equal(t, "reset", resumedReset.Kind)
	require.Equal(t, "status", resumedReset.Action)
	require.Equal(t, "all", resumedReset.Section)
	require.True(t, resumedReset.ConfirmRequired)
	require.Contains(t, resumedReset.AvailableSections, "model")

	resetConfigPath := filepath.Join(workspace, ".codog", "resume-reset.json")
	require.NoError(t, os.WriteFile(resetConfigPath, []byte(`{"model":"reset-model","max_tokens":1234,"theme":"dark"}`), 0o644))

	out, err = runResumedJSON("/reset", "model", "--path", resetConfigPath)
	require.NoError(t, err)
	var resumedResetModel resetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedResetModel))
	require.Equal(t, "reset", resumedResetModel.Kind)
	require.Equal(t, "reset", resumedResetModel.Action)
	require.Equal(t, "model", resumedResetModel.Section)
	require.NotEmpty(t, resumedResetModel.Changes)
	require.Equal(t, resetConfigPath, resumedResetModel.Path)
	resetData, err := os.ReadFile(resetConfigPath)
	require.NoError(t, err)
	require.NotContains(t, string(resetData), `"model"`)
	require.NotContains(t, string(resetData), `"max_tokens"`)
	require.Contains(t, string(resetData), `"theme"`)

	require.NoError(t, os.WriteFile(resetConfigPath, []byte(`{"model":"reset-model","theme":"dark"}`), 0o644))
	out, err = runResumedJSON("/reset", "all", "--confirm", "--path", resetConfigPath)
	require.NoError(t, err)
	var resumedResetAll resetReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedResetAll))
	require.Equal(t, "reset", resumedResetAll.Kind)
	require.Equal(t, "reset", resumedResetAll.Action)
	require.Equal(t, "all", resumedResetAll.Section)
	require.Equal(t, []string{"*"}, resumedResetAll.ResetKeys)
	require.NotEmpty(t, resumedResetAll.Changes)
	require.NoFileExists(t, resetConfigPath)

	out, err = runResumedJSON("/plan")
	require.NoError(t, err)
	var resumedPlan planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlan))
	require.Equal(t, "plan", resumedPlan.Kind)
	require.Equal(t, "show", resumedPlan.Action)
	require.Equal(t, "active", resumedPlan.Status)
	require.True(t, resumedPlan.State.Active)
	require.Equal(t, "inspect before editing", resumedPlan.State.Plan)

	out, err = runResumedJSON("/plan", "inspect", "more")
	require.NoError(t, err)
	var resumedPlanEnterText planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlanEnterText))
	require.Equal(t, "plan", resumedPlanEnterText.Kind)
	require.Equal(t, "enter", resumedPlanEnterText.Action)
	require.Equal(t, "active", resumedPlanEnterText.Status)
	require.True(t, resumedPlanEnterText.State.Active)
	require.Equal(t, "inspect more", resumedPlanEnterText.State.Plan)
	require.NotEmpty(t, resumedPlanEnterText.Path)

	out, err = runResumedJSON("/plan", "enter", "inspect")
	require.NoError(t, err)
	var resumedPlanEnter planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlanEnter))
	require.Equal(t, "plan", resumedPlanEnter.Kind)
	require.Equal(t, "enter", resumedPlanEnter.Action)
	require.Equal(t, "inspect", resumedPlanEnter.State.Plan)

	out, err = runResumedJSON("/plan", "set", "ship")
	require.NoError(t, err)
	var resumedPlanSet planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlanSet))
	require.Equal(t, "plan", resumedPlanSet.Kind)
	require.Equal(t, "set", resumedPlanSet.Action)
	require.True(t, resumedPlanSet.State.Active)
	require.Equal(t, "ship", resumedPlanSet.State.Plan)

	out, err = runResumedJSON("/plan", "exit")
	require.NoError(t, err)
	var resumedPlanExit planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlanExit))
	require.Equal(t, "plan", resumedPlanExit.Kind)
	require.Equal(t, "exit", resumedPlanExit.Action)
	require.Equal(t, "inactive", resumedPlanExit.Status)
	require.False(t, resumedPlanExit.State.Active)
	require.Equal(t, "ship", resumedPlanExit.State.Plan)

	out, err = runResumedJSON("/ultraplan", "inspect")
	require.NoError(t, err)
	var resumedUltraPlan planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUltraPlan))
	require.Equal(t, "plan", resumedUltraPlan.Kind)
	require.Equal(t, "enter", resumedUltraPlan.Action)
	require.True(t, resumedUltraPlan.State.Active)
	require.Equal(t, "inspect", resumedUltraPlan.State.Plan)

	out, err = runResumedJSON("/exit-plan")
	require.NoError(t, err)
	var resumedExitPlan planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExitPlan))
	require.Equal(t, "plan", resumedExitPlan.Kind)
	require.Equal(t, "exit", resumedExitPlan.Action)
	require.False(t, resumedExitPlan.State.Active)

	out, err = runResumedJSON("/ultraplan", "inspect again")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUltraPlan))
	require.True(t, resumedUltraPlan.State.Active)

	out, err = runResumedJSON("/exit_plan_mode")
	require.NoError(t, err)
	var resumedExitPlanMode planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedExitPlanMode))
	require.Equal(t, "plan", resumedExitPlanMode.Kind)
	require.Equal(t, "exit", resumedExitPlanMode.Action)
	require.False(t, resumedExitPlanMode.State.Active)

	out, err = runResumedJSON("/plan", "clear")
	require.NoError(t, err)
	var resumedPlanClear planmode.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPlanClear))
	require.Equal(t, "plan", resumedPlanClear.Kind)
	require.Equal(t, "clear", resumedPlanClear.Action)
	require.Equal(t, "inactive", resumedPlanClear.Status)
	require.False(t, resumedPlanClear.State.Active)
	require.NoFileExists(t, planmode.Path(workspace))

	out, err = runResumedJSON("/debug-tool-call", "EnterPlanModeTool", `{"plan":"resume debug tool plan"}`)
	require.NoError(t, err)
	var resumedDebugEnterPlan debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugEnterPlan))
	require.Equal(t, "debug_tool_call", resumedDebugEnterPlan.Kind)
	require.Equal(t, "enter_plan_mode", resumedDebugEnterPlan.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugEnterPlan.Permission)
	require.True(t, resumedDebugEnterPlan.Success)
	require.Contains(t, resumedDebugEnterPlan.Output, `"kind": "plan"`)
	require.Contains(t, resumedDebugEnterPlan.Output, `"active": true`)
	require.Contains(t, resumedDebugEnterPlan.Output, "resume debug tool plan")

	out, err = runResumedJSON("/debug-tool-call", "ExitPlanModeTool", `{"plan":"resume debug final plan"}`)
	require.NoError(t, err)
	var resumedDebugExitPlan debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugExitPlan))
	require.Equal(t, "debug_tool_call", resumedDebugExitPlan.Kind)
	require.Equal(t, "exit_plan_mode", resumedDebugExitPlan.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugExitPlan.Permission)
	require.True(t, resumedDebugExitPlan.Success)
	require.Contains(t, resumedDebugExitPlan.Output, `"kind": "plan"`)
	require.Contains(t, resumedDebugExitPlan.Output, `"active": false`)
	require.FileExists(t, planmode.Path(workspace))
	resumedDebugPlanState, err := planmode.Load(workspace)
	require.NoError(t, err)
	require.False(t, resumedDebugPlanState.Active)
	require.Equal(t, "resume debug final plan", resumedDebugPlanState.Plan)

	if gitAvailable {
		out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "EnterWorktreeTool", `{"name":"resume-debug"}`)
		require.NoError(t, err)
		var resumedDebugEnterWorktree debugToolCallReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugEnterWorktree))
		require.Equal(t, "debug_tool_call", resumedDebugEnterWorktree.Kind)
		require.Equal(t, "enter_worktree", resumedDebugEnterWorktree.Tool)
		require.Equal(t, tools.PermissionDanger, resumedDebugEnterWorktree.Permission)
		require.True(t, resumedDebugEnterWorktree.Success)
		require.Contains(t, resumedDebugEnterWorktree.Output, `"kind": "worktree"`)
		require.Contains(t, resumedDebugEnterWorktree.Output, `"operation": "enter"`)
		var worktreeEnter struct {
			Allocation worktree.Allocation `json:"allocation"`
		}
		require.NoError(t, json.Unmarshal([]byte(resumedDebugEnterWorktree.Output), &worktreeEnter))
		require.NotEmpty(t, worktreeEnter.Allocation.ID)
		require.DirExists(t, worktreeEnter.Allocation.Path)
		require.FileExists(t, filepath.Join(worktreeEnter.Allocation.Path, "main.go"))
		t.Cleanup(func() { _ = worktree.Remove(workspace, worktreeEnter.Allocation.ID) })

		worktreeExitInput, err := json.Marshal(map[string]string{"id": worktreeEnter.Allocation.ID})
		require.NoError(t, err)
		out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "ExitWorktreeTool", string(worktreeExitInput))
		require.NoError(t, err)
		var resumedDebugExitWorktree debugToolCallReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugExitWorktree))
		require.Equal(t, "debug_tool_call", resumedDebugExitWorktree.Kind)
		require.Equal(t, "exit_worktree", resumedDebugExitWorktree.Tool)
		require.Equal(t, tools.PermissionDanger, resumedDebugExitWorktree.Permission)
		require.True(t, resumedDebugExitWorktree.Success)
		require.Contains(t, resumedDebugExitWorktree.Output, `"operation": "exit"`)
		require.Contains(t, resumedDebugExitWorktree.Output, `"removed": true`)
		require.NoDirExists(t, worktreeEnter.Allocation.Path)
	}

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "RemoteTriggerTool", `{"url":"`+webServer.URL+`/trigger","method":"POST","body":"resume trigger body","headers":{"X-Codog-Test":"resume"},"timeout_ms":1000}`)
	require.NoError(t, err)
	var resumedDebugRemoteTrigger debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRemoteTrigger))
	require.Equal(t, "debug_tool_call", resumedDebugRemoteTrigger.Kind)
	require.Equal(t, "remote_trigger", resumedDebugRemoteTrigger.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugRemoteTrigger.Permission)
	require.True(t, resumedDebugRemoteTrigger.Success)
	require.Contains(t, resumedDebugRemoteTrigger.Output, `"method": "POST"`)
	require.Contains(t, resumedDebugRemoteTrigger.Output, `"status_code": 200`)
	require.Contains(t, resumedDebugRemoteTrigger.Output, "resume-trigger")
	require.Contains(t, resumedDebugRemoteTrigger.Output, "resume trigger body")

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskCreateTool", `{"command":"printf resumed-task-output","kind":"resume-debug","session_id":"resume-slash"}`)
	require.NoError(t, err)
	var resumedDebugTaskCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskCreate))
	require.Equal(t, "debug_tool_call", resumedDebugTaskCreate.Kind)
	require.Equal(t, "task_create", resumedDebugTaskCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTaskCreate.Permission)
	require.True(t, resumedDebugTaskCreate.Success)
	var resumedDebugTask background.Task
	require.NoError(t, json.Unmarshal([]byte(resumedDebugTaskCreate.Output), &resumedDebugTask))
	require.NotEmpty(t, resumedDebugTask.ID)
	require.Equal(t, "resume-debug", resumedDebugTask.Kind)
	require.Equal(t, "resume-slash", resumedDebugTask.SessionID)

	taskOutputInput, err := json.Marshal(map[string]any{"task_id": resumedDebugTask.ID, "block": true, "timeout_ms": 2000})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "TaskOutputTool", string(taskOutputInput))
	require.NoError(t, err)
	var resumedDebugTaskOutput debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskOutput))
	require.Equal(t, "debug_tool_call", resumedDebugTaskOutput.Kind)
	require.Equal(t, "task_output", resumedDebugTaskOutput.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTaskOutput.Permission)
	require.True(t, resumedDebugTaskOutput.Success)
	require.Contains(t, resumedDebugTaskOutput.Output, "resumed-task-output")
	require.Contains(t, resumedDebugTaskOutput.Output, `"task_id": "`+resumedDebugTask.ID+`"`)

	taskUpdateInput, err := json.Marshal(map[string]string{"task_id": resumedDebugTask.ID, "message": "resume task update"})
	require.NoError(t, err)
	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskUpdateTool", string(taskUpdateInput))
	require.NoError(t, err)
	var resumedDebugTaskUpdate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskUpdate))
	require.Equal(t, "debug_tool_call", resumedDebugTaskUpdate.Kind)
	require.Equal(t, "task_update", resumedDebugTaskUpdate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTaskUpdate.Permission)
	require.True(t, resumedDebugTaskUpdate.Success)
	require.Contains(t, resumedDebugTaskUpdate.Output, `"last_message": "resume task update"`)

	taskGetInput, err := json.Marshal(map[string]string{"task_id": resumedDebugTask.ID})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "TaskGetTool", string(taskGetInput))
	require.NoError(t, err)
	var resumedDebugTaskGet debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskGet))
	require.Equal(t, "debug_tool_call", resumedDebugTaskGet.Kind)
	require.Equal(t, "task_get", resumedDebugTaskGet.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTaskGet.Permission)
	require.True(t, resumedDebugTaskGet.Success)
	require.Contains(t, resumedDebugTaskGet.Output, `"task_id": "`+resumedDebugTask.ID+`"`)
	require.Contains(t, resumedDebugTaskGet.Output, "resume task update")

	out, err = runResumedJSON("/debug-tool-call", "TaskListTool", `{"session_id":"resume-slash","kind":"resume-debug"}`)
	require.NoError(t, err)
	var resumedDebugTaskList debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskList))
	require.Equal(t, "debug_tool_call", resumedDebugTaskList.Kind)
	require.Equal(t, "task_list", resumedDebugTaskList.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTaskList.Permission)
	require.True(t, resumedDebugTaskList.Success)
	require.Contains(t, resumedDebugTaskList.Output, `"total": 1`)
	require.Contains(t, resumedDebugTaskList.Output, `"task_id": "`+resumedDebugTask.ID+`"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskCreateTool", `{"command":"sleep 5","kind":"resume-lane","session_id":"resume-slash"}`)
	require.NoError(t, err)
	var resumedDebugLaneTaskCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugLaneTaskCreate))
	require.Equal(t, "debug_tool_call", resumedDebugLaneTaskCreate.Kind)
	require.Equal(t, "task_create", resumedDebugLaneTaskCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugLaneTaskCreate.Permission)
	require.True(t, resumedDebugLaneTaskCreate.Success)
	var resumedDebugLaneTask background.Task
	require.NoError(t, json.Unmarshal([]byte(resumedDebugLaneTaskCreate.Output), &resumedDebugLaneTask))
	require.NotEmpty(t, resumedDebugLaneTask.ID)
	t.Cleanup(func() { _, _ = background.NewStore(configHome).Stop(resumedDebugLaneTask.ID) })

	heartbeatAt := time.Now().UTC().Truncate(time.Second)
	taskHeartbeatInput, err := json.Marshal(map[string]any{"task_id": resumedDebugLaneTask.ID, "status": "running", "transport_alive": true, "observed_at": heartbeatAt.Format(time.RFC3339)})
	require.NoError(t, err)
	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskHeartbeatTool", string(taskHeartbeatInput))
	require.NoError(t, err)
	var resumedDebugTaskHeartbeat debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskHeartbeat))
	require.Equal(t, "debug_tool_call", resumedDebugTaskHeartbeat.Kind)
	require.Equal(t, "task_heartbeat", resumedDebugTaskHeartbeat.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTaskHeartbeat.Permission)
	require.True(t, resumedDebugTaskHeartbeat.Success)
	require.Contains(t, resumedDebugTaskHeartbeat.Output, `"task_id": "`+resumedDebugLaneTask.ID+`"`)
	require.Contains(t, resumedDebugTaskHeartbeat.Output, `"transport_alive": true`)

	out, err = runResumedJSON("/debug-tool-call", "TaskLaneBoardTool", `{"stalled_after_seconds":3600}`)
	require.NoError(t, err)
	var resumedDebugTaskLaneBoard debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskLaneBoard))
	require.Equal(t, "debug_tool_call", resumedDebugTaskLaneBoard.Kind)
	require.Equal(t, "task_lane_board", resumedDebugTaskLaneBoard.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTaskLaneBoard.Permission)
	require.True(t, resumedDebugTaskLaneBoard.Success)
	require.Contains(t, resumedDebugTaskLaneBoard.Output, `"task_id": "`+resumedDebugLaneTask.ID+`"`)
	require.Contains(t, resumedDebugTaskLaneBoard.Output, `"freshness": "healthy"`)

	taskStatusInput, err := json.Marshal(map[string]string{"task_id": resumedDebugLaneTask.ID})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "TaskStatusTool", string(taskStatusInput))
	require.NoError(t, err)
	var resumedDebugTaskStatus debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskStatus))
	require.Equal(t, "debug_tool_call", resumedDebugTaskStatus.Kind)
	require.Equal(t, "task_status", resumedDebugTaskStatus.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTaskStatus.Permission)
	require.True(t, resumedDebugTaskStatus.Success)
	require.Contains(t, resumedDebugTaskStatus.Output, `"task_id": "`+resumedDebugLaneTask.ID+`"`)

	taskStopInput, err := json.Marshal(map[string]string{"task_id": resumedDebugLaneTask.ID})
	require.NoError(t, err)
	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskStopTool", string(taskStopInput))
	require.NoError(t, err)
	var resumedDebugTaskStop debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskStop))
	require.Equal(t, "debug_tool_call", resumedDebugTaskStop.Kind)
	require.Equal(t, "task_stop", resumedDebugTaskStop.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugTaskStop.Permission)
	require.True(t, resumedDebugTaskStop.Success)
	require.Contains(t, resumedDebugTaskStop.Output, `"task_id": "`+resumedDebugLaneTask.ID+`"`)
	require.Contains(t, resumedDebugTaskStop.Output, `"message": "Task stopped"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskCreateTool", `{"command":"printf supervise-failed && exit 2","kind":"resume-supervise","session_id":"resume-slash","restart_policy":{"enabled":true,"mode":"on-failure","max_attempts":1}}`)
	require.NoError(t, err)
	var resumedDebugSupervisedTaskCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugSupervisedTaskCreate))
	require.Equal(t, "debug_tool_call", resumedDebugSupervisedTaskCreate.Kind)
	require.Equal(t, "task_create", resumedDebugSupervisedTaskCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugSupervisedTaskCreate.Permission)
	require.True(t, resumedDebugSupervisedTaskCreate.Success)
	var resumedDebugSupervisedTask background.Task
	require.NoError(t, json.Unmarshal([]byte(resumedDebugSupervisedTaskCreate.Output), &resumedDebugSupervisedTask))
	require.NotEmpty(t, resumedDebugSupervisedTask.ID)
	require.Eventually(t, func() bool {
		task, err := background.NewStore(configHome).Status(resumedDebugSupervisedTask.ID)
		return err == nil && task.Status == "failed"
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskSuperviseTool", `{}`)
	require.NoError(t, err)
	var resumedDebugTaskSupervise debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTaskSupervise))
	require.Equal(t, "debug_tool_call", resumedDebugTaskSupervise.Kind)
	require.Equal(t, "task_supervise", resumedDebugTaskSupervise.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTaskSupervise.Permission)
	require.True(t, resumedDebugTaskSupervise.Success)
	var resumedDebugTaskSuperviseResult background.SuperviseResult
	require.NoError(t, json.Unmarshal([]byte(resumedDebugTaskSupervise.Output), &resumedDebugTaskSuperviseResult))
	require.Len(t, resumedDebugTaskSuperviseResult.Restarted, 1)
	require.Equal(t, resumedDebugSupervisedTask.ID, resumedDebugTaskSuperviseResult.Restarted[0].RestartedFrom)
	require.Equal(t, 1, resumedDebugTaskSuperviseResult.Restarted[0].RestartCount)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TaskCreateTool", `{"prompt":"resume prompt task","description":"exercise executable shim","kind":"resume-prompt","session_id":"resume-slash"}`)
	require.NoError(t, err)
	var resumedDebugPromptTaskCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugPromptTaskCreate))
	require.Equal(t, "debug_tool_call", resumedDebugPromptTaskCreate.Kind)
	require.Equal(t, "task_create", resumedDebugPromptTaskCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugPromptTaskCreate.Permission)
	require.True(t, resumedDebugPromptTaskCreate.Success)
	var resumedDebugPromptTask struct {
		TaskID string          `json:"task_id"`
		Status string          `json:"status"`
		Task   background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugPromptTaskCreate.Output), &resumedDebugPromptTask))
	require.NotEmpty(t, resumedDebugPromptTask.TaskID)
	require.Equal(t, "resume-prompt", resumedDebugPromptTask.Task.Kind)
	require.Contains(t, resumedDebugPromptTask.Task.Command, "codog-shim")
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedDebugPromptTask.TaskID, 4096)
		return err == nil && strings.Contains(logs, "codog-shim prompt") && strings.Contains(logs, "resume prompt task")
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "AgentTool", `{"description":"resume agent","prompt":"inspect executable routing","subagent_type":"reviewer","session_id":"resume-slash"}`)
	require.NoError(t, err)
	var resumedDebugAgent debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugAgent))
	require.Equal(t, "debug_tool_call", resumedDebugAgent.Kind)
	require.Equal(t, "agent", resumedDebugAgent.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugAgent.Permission)
	require.True(t, resumedDebugAgent.Success)
	var resumedDebugAgentOutput struct {
		Kind  string          `json:"kind"`
		Agent string          `json:"agent"`
		Task  background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugAgent.Output), &resumedDebugAgentOutput))
	require.Equal(t, "agent", resumedDebugAgentOutput.Kind)
	require.Equal(t, "reviewer", resumedDebugAgentOutput.Agent)
	require.NotEmpty(t, resumedDebugAgentOutput.Task.ID)
	require.Equal(t, "agent", resumedDebugAgentOutput.Task.Kind)
	require.Contains(t, resumedDebugAgentOutput.Task.Command, "codog-shim")
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedDebugAgentOutput.Task.ID, 4096)
		return err == nil && strings.Contains(logs, "codog-shim prompt") && strings.Contains(logs, "inspect executable routing")
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TeamCreateTool", `{"name":"resume-debug-team","session_id":"resume-slash","tasks":[{"description":"first","prompt":"inspect first route"},{"description":"second","prompt":"inspect second route"}]}`)
	require.NoError(t, err)
	var resumedDebugTeamCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTeamCreate))
	require.Equal(t, "debug_tool_call", resumedDebugTeamCreate.Kind)
	require.Equal(t, "team_create", resumedDebugTeamCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTeamCreate.Permission)
	require.True(t, resumedDebugTeamCreate.Success)
	var resumedDebugTeam struct {
		TeamID    string   `json:"team_id"`
		Name      string   `json:"name"`
		TaskCount int      `json:"task_count"`
		TaskIDs   []string `json:"task_ids"`
		Status    string   `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugTeamCreate.Output), &resumedDebugTeam))
	require.NotEmpty(t, resumedDebugTeam.TeamID)
	require.Equal(t, "resume-debug-team", resumedDebugTeam.Name)
	require.Equal(t, 2, resumedDebugTeam.TaskCount)
	require.Len(t, resumedDebugTeam.TaskIDs, 2)
	require.Equal(t, "running", resumedDebugTeam.Status)
	for _, taskID := range resumedDebugTeam.TaskIDs {
		task, err := background.NewStore(configHome).Get(taskID)
		require.NoError(t, err)
		require.Equal(t, "team", task.Kind)
		require.Contains(t, task.Command, "codog-shim")
	}
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedDebugTeam.TaskIDs[0], 4096)
		return err == nil && strings.Contains(logs, "codog-shim prompt") && strings.Contains(logs, "inspect first route")
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSON("/debug-tool-call", "TeamListTool", `{"status":"running"}`)
	require.NoError(t, err)
	var resumedDebugTeamList debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTeamList))
	require.Equal(t, "debug_tool_call", resumedDebugTeamList.Kind)
	require.Equal(t, "team_list", resumedDebugTeamList.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTeamList.Permission)
	require.True(t, resumedDebugTeamList.Success)
	require.Contains(t, resumedDebugTeamList.Output, `"kind": "team_list"`)
	require.Contains(t, resumedDebugTeamList.Output, `"team_id": "`+resumedDebugTeam.TeamID+`"`)

	teamIDInput, err := json.Marshal(map[string]string{"team_id": resumedDebugTeam.TeamID})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "TeamGetTool", string(teamIDInput))
	require.NoError(t, err)
	var resumedDebugTeamGet debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTeamGet))
	require.Equal(t, "debug_tool_call", resumedDebugTeamGet.Kind)
	require.Equal(t, "team_get", resumedDebugTeamGet.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTeamGet.Permission)
	require.True(t, resumedDebugTeamGet.Success)
	require.Contains(t, resumedDebugTeamGet.Output, `"kind": "team"`)
	require.Contains(t, resumedDebugTeamGet.Output, `"team_id": "`+resumedDebugTeam.TeamID+`"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "TeamDeleteTool", string(teamIDInput))
	require.NoError(t, err)
	var resumedDebugTeamDelete debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTeamDelete))
	require.Equal(t, "debug_tool_call", resumedDebugTeamDelete.Kind)
	require.Equal(t, "team_delete", resumedDebugTeamDelete.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugTeamDelete.Permission)
	require.True(t, resumedDebugTeamDelete.Success)
	require.Contains(t, resumedDebugTeamDelete.Output, `"status": "deleted"`)
	require.Contains(t, resumedDebugTeamDelete.Output, `"team_id": "`+resumedDebugTeam.TeamID+`"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "RunTaskPacketTool", `{"objective":"Route resume packet through shim","scope":"README only","repo":"codog","branch_policy":"main only","acceptance_tests":["go test ./..."],"commit_policy":"single verified commit","reporting_contract":"summarize result","escalation_policy":"ask if blocked"}`)
	require.NoError(t, err)
	var resumedDebugRunTaskPacket debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugRunTaskPacket))
	require.Equal(t, "debug_tool_call", resumedDebugRunTaskPacket.Kind)
	require.Equal(t, "run_task_packet", resumedDebugRunTaskPacket.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugRunTaskPacket.Permission)
	require.True(t, resumedDebugRunTaskPacket.Success)
	var resumedDebugTaskPacket struct {
		TaskID string          `json:"task_id"`
		Status string          `json:"status"`
		Task   background.Task `json:"task"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugRunTaskPacket.Output), &resumedDebugTaskPacket))
	require.NotEmpty(t, resumedDebugTaskPacket.TaskID)
	require.Equal(t, "task_packet", resumedDebugTaskPacket.Task.Kind)
	require.Contains(t, resumedDebugTaskPacket.Task.Command, "codog-shim")
	require.Contains(t, resumedDebugTaskPacket.Task.Prompt, "Route resume packet through shim")
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedDebugTaskPacket.TaskID, 4096)
		return err == nil && strings.Contains(logs, "codog-shim prompt") && strings.Contains(logs, "Route resume packet through shim")
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerCreateTool", `{"cwd":".","trusted_roots":["."],"auto_recover_prompt_misdelivery":false}`)
	require.NoError(t, err)
	var resumedDebugWorkerCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerCreate))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerCreate.Kind)
	require.Equal(t, "worker_create", resumedDebugWorkerCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugWorkerCreate.Permission)
	require.True(t, resumedDebugWorkerCreate.Success)
	var resumedDebugWorker struct {
		WorkerID       string   `json:"worker_id"`
		Status         string   `json:"status"`
		ReadyForPrompt bool     `json:"ready_for_prompt"`
		TrustedRoots   []string `json:"trusted_roots"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugWorkerCreate.Output), &resumedDebugWorker))
	require.NotEmpty(t, resumedDebugWorker.WorkerID)
	require.Equal(t, "ready_for_prompt", resumedDebugWorker.Status)
	require.True(t, resumedDebugWorker.ReadyForPrompt)
	require.Equal(t, []string{"."}, resumedDebugWorker.TrustedRoots)

	out, err = runResumedJSON("/debug-tool-call", "WorkerListTool", `{"status":"ready_for_prompt"}`)
	require.NoError(t, err)
	var resumedDebugWorkerList debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerList))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerList.Kind)
	require.Equal(t, "worker_list", resumedDebugWorkerList.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerList.Permission)
	require.True(t, resumedDebugWorkerList.Success)
	require.Contains(t, resumedDebugWorkerList.Output, `"kind": "worker_list"`)
	require.Contains(t, resumedDebugWorkerList.Output, `"worker_id": "`+resumedDebugWorker.WorkerID+`"`)

	workerIDInput, err := json.Marshal(map[string]string{"worker_id": resumedDebugWorker.WorkerID})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "WorkerGetTool", string(workerIDInput))
	require.NoError(t, err)
	var resumedDebugWorkerGet debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerGet))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerGet.Kind)
	require.Equal(t, "worker_get", resumedDebugWorkerGet.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerGet.Permission)
	require.True(t, resumedDebugWorkerGet.Success)
	require.Contains(t, resumedDebugWorkerGet.Output, `"worker_id": "`+resumedDebugWorker.WorkerID+`"`)
	require.Contains(t, resumedDebugWorkerGet.Output, `"status": "ready_for_prompt"`)

	workerObserveInput, err := json.Marshal(map[string]string{"worker_id": resumedDebugWorker.WorkerID, "screen_text": "Do you trust this folder?"})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "WorkerObserveTool", string(workerObserveInput))
	require.NoError(t, err)
	var resumedDebugWorkerObserve debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerObserve))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerObserve.Kind)
	require.Equal(t, "worker_observe", resumedDebugWorkerObserve.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerObserve.Permission)
	require.True(t, resumedDebugWorkerObserve.Success)
	require.Contains(t, resumedDebugWorkerObserve.Output, `"status": "trust_prompt"`)
	require.Contains(t, resumedDebugWorkerObserve.Output, `"ready_for_prompt": false`)

	out, err = runResumedJSON("/debug-tool-call", "WorkerAwaitReadyTool", string(workerIDInput))
	require.NoError(t, err)
	var resumedDebugWorkerAwaitReady debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerAwaitReady))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerAwaitReady.Kind)
	require.Equal(t, "worker_await_ready", resumedDebugWorkerAwaitReady.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerAwaitReady.Permission)
	require.True(t, resumedDebugWorkerAwaitReady.Success)
	require.Contains(t, resumedDebugWorkerAwaitReady.Output, `"status": "trust_prompt"`)
	require.Contains(t, resumedDebugWorkerAwaitReady.Output, `"ready_for_prompt": false`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerResolveTrustTool", string(workerIDInput))
	require.NoError(t, err)
	var resumedDebugWorkerResolveTrust debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerResolveTrust))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerResolveTrust.Kind)
	require.Equal(t, "worker_resolve_trust", resumedDebugWorkerResolveTrust.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugWorkerResolveTrust.Permission)
	require.True(t, resumedDebugWorkerResolveTrust.Success)
	require.Contains(t, resumedDebugWorkerResolveTrust.Output, `"status": "ready_for_prompt"`)
	require.Contains(t, resumedDebugWorkerResolveTrust.Output, `"ready_for_prompt": true`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerSendPromptTool", `{"worker_id":"`+resumedDebugWorker.WorkerID+`","prompt":"resume worker prompt","task_receipt":{"repo":"codog","task_kind":"resume-debug","source_surface":"tool","objective_preview":"resume worker prompt"}}`)
	require.NoError(t, err)
	var resumedDebugWorkerSendPrompt debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerSendPrompt))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerSendPrompt.Kind)
	require.Equal(t, "worker_send_prompt", resumedDebugWorkerSendPrompt.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugWorkerSendPrompt.Permission)
	require.True(t, resumedDebugWorkerSendPrompt.Success)
	var resumedDebugWorkerSent struct {
		WorkerID string `json:"worker_id"`
		Status   string `json:"status"`
		TaskID   string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugWorkerSendPrompt.Output), &resumedDebugWorkerSent))
	require.Equal(t, resumedDebugWorker.WorkerID, resumedDebugWorkerSent.WorkerID)
	require.Equal(t, "running", resumedDebugWorkerSent.Status)
	require.NotEmpty(t, resumedDebugWorkerSent.TaskID)
	sentTask, err := background.NewStore(configHome).Get(resumedDebugWorkerSent.TaskID)
	require.NoError(t, err)
	require.Equal(t, "worker", sentTask.Kind)
	require.Contains(t, sentTask.Command, "codog-shim")
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(configHome).Logs(resumedDebugWorkerSent.TaskID, 4096)
		return err == nil && strings.Contains(logs, "codog-shim prompt") && strings.Contains(logs, "resume worker prompt")
	}, 5*time.Second, 50*time.Millisecond)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerRestartTool", string(workerIDInput))
	require.NoError(t, err)
	var resumedDebugWorkerRestart debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerRestart))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerRestart.Kind)
	require.Equal(t, "worker_restart", resumedDebugWorkerRestart.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugWorkerRestart.Permission)
	require.True(t, resumedDebugWorkerRestart.Success)
	var resumedDebugWorkerRestarted struct {
		WorkerID string `json:"worker_id"`
		Status   string `json:"status"`
		TaskID   string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugWorkerRestart.Output), &resumedDebugWorkerRestarted))
	require.Equal(t, resumedDebugWorker.WorkerID, resumedDebugWorkerRestarted.WorkerID)
	require.Equal(t, "running", resumedDebugWorkerRestarted.Status)
	require.NotEmpty(t, resumedDebugWorkerRestarted.TaskID)
	require.NotEqual(t, resumedDebugWorkerSent.TaskID, resumedDebugWorkerRestarted.TaskID)
	restartedTask, err := background.NewStore(configHome).Get(resumedDebugWorkerRestarted.TaskID)
	require.NoError(t, err)
	require.Equal(t, resumedDebugWorkerSent.TaskID, restartedTask.RestartedFrom)
	require.Contains(t, restartedTask.Command, "codog-shim")

	out, err = runResumedJSON("/debug-tool-call", "WorkerObserveCompletionTool", `{"worker_id":"`+resumedDebugWorker.WorkerID+`","finish_reason":"stop","tokens_output":12}`)
	require.NoError(t, err)
	var resumedDebugWorkerObserveCompletion debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerObserveCompletion))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerObserveCompletion.Kind)
	require.Equal(t, "worker_observe_completion", resumedDebugWorkerObserveCompletion.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerObserveCompletion.Permission)
	require.True(t, resumedDebugWorkerObserveCompletion.Success)
	require.Contains(t, resumedDebugWorkerObserveCompletion.Output, `"status": "finished"`)
	require.Contains(t, resumedDebugWorkerObserveCompletion.Output, `"finish_reason": "stop"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerTerminateTool", string(workerIDInput))
	require.NoError(t, err)
	var resumedDebugWorkerTerminate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerTerminate))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerTerminate.Kind)
	require.Equal(t, "worker_terminate", resumedDebugWorkerTerminate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugWorkerTerminate.Permission)
	require.True(t, resumedDebugWorkerTerminate.Success)
	require.Contains(t, resumedDebugWorkerTerminate.Output, `"status": "terminated"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "WorkerCreateTool", `{"cwd":"."}`)
	require.NoError(t, err)
	var resumedDebugTimeoutWorkerCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTimeoutWorkerCreate))
	require.Equal(t, "debug_tool_call", resumedDebugTimeoutWorkerCreate.Kind)
	require.Equal(t, "worker_create", resumedDebugTimeoutWorkerCreate.Tool)
	require.True(t, resumedDebugTimeoutWorkerCreate.Success)
	var resumedDebugTimeoutWorker struct {
		WorkerID string `json:"worker_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugTimeoutWorkerCreate.Output), &resumedDebugTimeoutWorker))
	require.NotEmpty(t, resumedDebugTimeoutWorker.WorkerID)

	workerStartupTimeoutInput, err := json.Marshal(map[string]any{
		"worker_id":             resumedDebugTimeoutWorker.WorkerID,
		"last_lifecycle_state":  "trust_prompt",
		"pane_command":          "codog repl",
		"transport_healthy":     true,
		"mcp_healthy":           true,
		"elapsed_seconds":       42,
		"trust_prompt_detected": true,
	})
	require.NoError(t, err)
	out, err = runResumedJSON("/debug-tool-call", "WorkerStartupTimeoutTool", string(workerStartupTimeoutInput))
	require.NoError(t, err)
	var resumedDebugWorkerStartupTimeout debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWorkerStartupTimeout))
	require.Equal(t, "debug_tool_call", resumedDebugWorkerStartupTimeout.Kind)
	require.Equal(t, "worker_startup_timeout", resumedDebugWorkerStartupTimeout.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugWorkerStartupTimeout.Permission)
	require.True(t, resumedDebugWorkerStartupTimeout.Success)
	require.Contains(t, resumedDebugWorkerStartupTimeout.Output, `"status": "failed"`)
	require.Contains(t, resumedDebugWorkerStartupTimeout.Output, `"classification": "trust_required"`)
	require.Contains(t, resumedDebugWorkerStartupTimeout.Output, `"lane_event": "lane.blocked"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "ConfigTool", `{"setting":"model"}`)
	require.NoError(t, err)
	var resumedDebugConfig debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugConfig))
	require.Equal(t, "debug_tool_call", resumedDebugConfig.Kind)
	require.Equal(t, "config", resumedDebugConfig.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugConfig.Permission)
	require.True(t, resumedDebugConfig.Success)
	require.Contains(t, resumedDebugConfig.Output, `"success": true`)
	require.Contains(t, resumedDebugConfig.Output, `"operation": "get"`)
	require.Contains(t, resumedDebugConfig.Output, `"setting": "model"`)

	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "CronCreateTool", `{"schedule":"@hourly","prompt":"resume debug cron","description":"debug cron entry"}`)
	require.NoError(t, err)
	var resumedDebugCronCreate debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugCronCreate))
	require.Equal(t, "debug_tool_call", resumedDebugCronCreate.Kind)
	require.Equal(t, "cron_create", resumedDebugCronCreate.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugCronCreate.Permission)
	require.True(t, resumedDebugCronCreate.Success)
	require.Contains(t, resumedDebugCronCreate.Output, `"schedule": "@hourly"`)
	require.Contains(t, resumedDebugCronCreate.Output, `"prompt": "resume debug cron"`)
	var debugCronEntry struct {
		CronID string `json:"cron_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugCronCreate.Output), &debugCronEntry))
	require.NotEmpty(t, debugCronEntry.CronID)

	out, err = runResumedJSON("/debug-tool-call", "CronListTool", `{}`)
	require.NoError(t, err)
	var resumedDebugCronList debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugCronList))
	require.Equal(t, "debug_tool_call", resumedDebugCronList.Kind)
	require.Equal(t, "cron_list", resumedDebugCronList.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugCronList.Permission)
	require.True(t, resumedDebugCronList.Success)
	require.Contains(t, resumedDebugCronList.Output, `"prompt": "resume debug cron"`)
	require.Contains(t, resumedDebugCronList.Output, `"count":`)

	cronDeleteInput, err := json.Marshal(map[string]string{"cron_id": debugCronEntry.CronID})
	require.NoError(t, err)
	out, err = runResumedJSONWithFlags([]string{"--permission-mode=allow"}, "/debug-tool-call", "CronDeleteTool", string(cronDeleteInput))
	require.NoError(t, err)
	var resumedDebugCronDelete debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugCronDelete))
	require.Equal(t, "debug_tool_call", resumedDebugCronDelete.Kind)
	require.Equal(t, "cron_delete", resumedDebugCronDelete.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugCronDelete.Permission)
	require.True(t, resumedDebugCronDelete.Success)
	require.Contains(t, resumedDebugCronDelete.Output, `"cron_id": "`+debugCronEntry.CronID+`"`)
	require.Contains(t, resumedDebugCronDelete.Output, `"status": "deleted"`)

	out, err = runResumedJSON("/debug-tool-call", "TodoWrite", `{"todos":[{"content":"resume debug todo","activeForm":"tracking resume debug todo","status":"in_progress","priority":"high"}]}`)
	require.NoError(t, err)
	var resumedDebugTodoWrite debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTodoWrite))
	require.Equal(t, "debug_tool_call", resumedDebugTodoWrite.Kind)
	require.Equal(t, "todo_write", resumedDebugTodoWrite.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugTodoWrite.Permission)
	require.True(t, resumedDebugTodoWrite.Success)
	require.Contains(t, resumedDebugTodoWrite.Output, "resume debug todo")
	require.FileExists(t, todos.Path(workspace))

	out, err = runResumedJSON("/debug-tool-call", "TodoRead", `{}`)
	require.NoError(t, err)
	var resumedDebugTodoRead debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugTodoRead))
	require.Equal(t, "debug_tool_call", resumedDebugTodoRead.Kind)
	require.Equal(t, "todo_read", resumedDebugTodoRead.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugTodoRead.Permission)
	require.True(t, resumedDebugTodoRead.Success)
	require.Contains(t, resumedDebugTodoRead.Output, "resume debug todo")

	out, err = runResumedJSON("/permissions", "set", "allow", "--path", configPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedPermissionsSet))
	require.Equal(t, "allow", resumedPermissionsSet.PermissionMode)

	out, err = runResumedJSON("/debug-tool-call", "REPLTool", `{"language":"sh","code":"printf resumed-repl","timeout_ms":1000}`)
	require.NoError(t, err)
	var resumedDebugREPL debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugREPL))
	require.Equal(t, "debug_tool_call", resumedDebugREPL.Kind)
	require.Equal(t, "repl", resumedDebugREPL.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugREPL.Permission)
	require.True(t, resumedDebugREPL.Success)
	require.Contains(t, resumedDebugREPL.Output, `"stdout": "resumed-repl"`)

	out, err = runResumedJSON("/debug-tool-call", "Bash", `{"command":"printf resumed-bg; sleep 5","run_in_background":true}`)
	require.NoError(t, err)
	var resumedDebugBackgroundBash debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugBackgroundBash))
	require.Equal(t, "debug_tool_call", resumedDebugBackgroundBash.Kind)
	require.Equal(t, "bash", resumedDebugBackgroundBash.Tool)
	require.Equal(t, tools.PermissionDanger, resumedDebugBackgroundBash.Permission)
	require.True(t, resumedDebugBackgroundBash.Success)
	var resumedBackgroundBash struct {
		Task             background.Task `json:"task"`
		BackgroundTaskID string          `json:"backgroundTaskId"`
	}
	require.NoError(t, json.Unmarshal([]byte(resumedDebugBackgroundBash.Output), &resumedBackgroundBash))
	require.NotEmpty(t, resumedBackgroundBash.Task.ID)
	require.Equal(t, resumedBackgroundBash.Task.ID, resumedBackgroundBash.BackgroundTaskID)

	out, err = runResumedJSON("/debug-tool-call", "BashOutput", `{"bash_id":"`+resumedBackgroundBash.Task.ID+`","offset":0,"block":true,"timeout_ms":2000}`)
	require.NoError(t, err)
	var resumedDebugBashOutput debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugBashOutput))
	require.Equal(t, "debug_tool_call", resumedDebugBashOutput.Kind)
	require.Equal(t, "bash_output", resumedDebugBashOutput.Tool)
	require.Equal(t, tools.PermissionReadOnly, resumedDebugBashOutput.Permission)
	require.True(t, resumedDebugBashOutput.Success)
	require.Contains(t, resumedDebugBashOutput.Output, "resumed-bg")

	out, err = runResumedJSON("/debug-tool-call", "KillBash", `{"bash_id":"`+resumedBackgroundBash.Task.ID+`"}`)
	require.NoError(t, err)
	var resumedDebugKillBash debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugKillBash))
	require.Equal(t, "debug_tool_call", resumedDebugKillBash.Kind)
	require.Equal(t, "kill_bash", resumedDebugKillBash.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugKillBash.Permission)
	require.True(t, resumedDebugKillBash.Success)
	require.Contains(t, resumedDebugKillBash.Output, `"status": "stopped"`)

	out, err = runResumedJSON("/debug-tool-call", "write_file", `{"path":"debug-write.txt","content":"resumed debug write"}`)
	require.NoError(t, err)
	var resumedDebugWrite debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugWrite))
	require.Equal(t, "debug_tool_call", resumedDebugWrite.Kind)
	require.Equal(t, "write_file", resumedDebugWrite.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugWrite.Permission)
	require.True(t, resumedDebugWrite.Success)
	require.FileExists(t, filepath.Join(workspace, "debug-write.txt"))
	debugWriteData, err := os.ReadFile(filepath.Join(workspace, "debug-write.txt"))
	require.NoError(t, err)
	require.Equal(t, "resumed debug write", string(debugWriteData))

	out, err = runResumedJSON("/undo")
	require.NoError(t, err)
	var resumedUndo undo.RestoreReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUndo))
	require.Equal(t, "undo", resumedUndo.Kind)
	require.Equal(t, "restore", resumedUndo.Action)
	require.True(t, resumedUndo.Removed)
	require.Equal(t, "debug-write.txt", resumedUndo.Path)
	require.NoFileExists(t, filepath.Join(workspace, "debug-write.txt"))

	editTargetPath := filepath.Join(workspace, "debug-edit.txt")
	require.NoError(t, os.WriteFile(editTargetPath, []byte("alpha\nbeta\ngamma\n"), 0o644))
	out, err = runResumedJSON("/debug-tool-call", "EditFile", `{"path":"debug-edit.txt","old_string":"beta","new_string":"BETA"}`)
	require.NoError(t, err)
	var resumedDebugEdit debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugEdit))
	require.Equal(t, "debug_tool_call", resumedDebugEdit.Kind)
	require.Equal(t, "edit_file", resumedDebugEdit.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugEdit.Permission)
	require.True(t, resumedDebugEdit.Success)
	editData, err := os.ReadFile(editTargetPath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nBETA\ngamma\n", string(editData))

	out, err = runResumedJSON("/debug-tool-call", "MultiEdit", `{"path":"debug-edit.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"gamma","new_string":"GAMMA"}]}`)
	require.NoError(t, err)
	var resumedDebugMultiEdit debugToolCallReport
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDebugMultiEdit))
	require.Equal(t, "debug_tool_call", resumedDebugMultiEdit.Kind)
	require.Equal(t, "multi_edit", resumedDebugMultiEdit.Tool)
	require.Equal(t, tools.PermissionWorkspace, resumedDebugMultiEdit.Permission)
	require.True(t, resumedDebugMultiEdit.Success)
	editData, err = os.ReadFile(editTargetPath)
	require.NoError(t, err)
	require.Equal(t, "ALPHA\nBETA\nGAMMA\n", string(editData))

	out, err = runResumedJSON("/unfocus")
	require.NoError(t, err)
	var resumedUnfocus focus.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUnfocus))
	require.Equal(t, "focus", resumedUnfocus.Kind)
	require.Equal(t, "list", resumedUnfocus.Action)
	require.Equal(t, 1, resumedUnfocus.Total)
	require.Equal(t, "main.go", resumedUnfocus.Entries[0].Path)

	out, err = runResumedJSON("/focus", "main.go")
	require.NoError(t, err)
	var resumedFocusAdd focus.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFocusAdd))
	require.Equal(t, "focus", resumedFocusAdd.Kind)
	require.Equal(t, "add", resumedFocusAdd.Action)
	require.Equal(t, 1, resumedFocusAdd.Total)
	require.Equal(t, "main.go", resumedFocusAdd.Entries[0].Path)

	out, err = runResumedJSON("/unfocus", "main.go")
	require.NoError(t, err)
	var resumedUnfocusRemove focus.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUnfocusRemove))
	require.Equal(t, "focus", resumedUnfocusRemove.Kind)
	require.Equal(t, "remove", resumedUnfocusRemove.Action)
	require.Equal(t, 0, resumedUnfocusRemove.Total)

	out, err = runResumedJSON("/focus", "main.go")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedFocusAdd))
	require.Equal(t, "add", resumedFocusAdd.Action)
	require.Equal(t, 1, resumedFocusAdd.Total)

	out, err = runResumedJSON("/unfocus", "--all")
	require.NoError(t, err)
	var resumedUnfocusClear focus.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedUnfocusClear))
	require.Equal(t, "focus", resumedUnfocusClear.Kind)
	require.Equal(t, "clear", resumedUnfocusClear.Action)
	require.Equal(t, 0, resumedUnfocusClear.Total)

	out, err = runResumedJSON("/add-dir", externalContext)
	require.NoError(t, err)
	var resumedAddDirMutation pathscope.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAddDirMutation))
	require.Equal(t, "additional_dirs", resumedAddDirMutation.Kind)
	require.Equal(t, "add", resumedAddDirMutation.Action)
	require.Equal(t, 1, resumedAddDirMutation.Total)
	require.Equal(t, externalContext, resumedAddDirMutation.Entries[0].Path)
	require.Equal(t, "workspace", resumedAddDirMutation.Entries[0].Source)
	require.True(t, resumedAddDirMutation.Entries[0].Exists)

	out, err = runResumedJSON("/add-dir", "remove", externalContext)
	require.NoError(t, err)
	var resumedRemoveDir pathscope.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRemoveDir))
	require.Equal(t, "additional_dirs", resumedRemoveDir.Kind)
	require.Equal(t, "remove", resumedRemoveDir.Action)
	require.Equal(t, 0, resumedRemoveDir.Total)

	out, err = runResumedJSON("/add-dir", externalContext)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &resumedAddDirMutation))
	require.Equal(t, "add", resumedAddDirMutation.Action)
	require.Equal(t, 1, resumedAddDirMutation.Total)

	out, err = runResumedJSON("/add-dir", "clear")
	require.NoError(t, err)
	var resumedClearDir pathscope.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedClearDir))
	require.Equal(t, "additional_dirs", resumedClearDir.Kind)
	require.Equal(t, "clear", resumedClearDir.Action)
	require.Equal(t, 0, resumedClearDir.Total)

	out, err = runResumedJSON("/oauth", "discover", oauthServer.URL)
	require.NoError(t, err)
	var resumedOAuthDiscovery oauth.ProviderMetadata
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthDiscovery))
	require.Equal(t, oauthServer.URL+"/authorize", resumedOAuthDiscovery.AuthorizationEndpoint)
	require.Equal(t, oauthServer.URL+"/token", resumedOAuthDiscovery.TokenEndpoint)
	require.Equal(t, oauthServer.URL+"/.well-known/oauth-authorization-server", resumedOAuthDiscovery.SourceURL)

	out, err = runResumedJSON("/oauth", "provider", "save", "resumed-work", oauthServer.URL, "client-resumed-work", "profile", "email")
	require.NoError(t, err)
	var resumedOAuthProviderSave oauth.ProviderProfile
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthProviderSave))
	require.Equal(t, "resumed-work", resumedOAuthProviderSave.Name)
	require.Equal(t, "client-resumed-work", resumedOAuthProviderSave.ClientID)
	require.Equal(t, []string{"profile", "email"}, resumedOAuthProviderSave.Scopes)
	require.Equal(t, oauthServer.URL+"/token", resumedOAuthProviderSave.Metadata.TokenEndpoint)

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "resume-oauth-access-1234",
		RefreshToken: "resume-oauth-refresh-1234",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	out, err = runResumedJSON("/oauth", "token", "refresh")
	require.NoError(t, err)
	var resumedOAuthTokenRefresh oauth.TokenView
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthTokenRefresh))
	require.Equal(t, "refr...1234", resumedOAuthTokenRefresh.AccessToken)
	require.Equal(t, "refr...1234", resumedOAuthTokenRefresh.RefreshToken)
	require.Equal(t, "Bearer", resumedOAuthTokenRefresh.TokenType)
	require.False(t, resumedOAuthTokenRefresh.ExpiresAt.IsZero())

	_, err = oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "resume-oauth-access-1234",
		RefreshToken: "resume-oauth-refresh-1234",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)
	out, err = runResumedJSON("/oauth-refresh", "default")
	require.NoError(t, err)
	var resumedOAuthRefreshAlias oauth.TokenView
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthRefreshAlias))
	require.Equal(t, "refr...1234", resumedOAuthRefreshAlias.AccessToken)
	require.Equal(t, "refr...1234", resumedOAuthRefreshAlias.RefreshToken)
	require.Equal(t, "Bearer", resumedOAuthRefreshAlias.TokenType)

	out, err = runResumedJSON("/oauth", "device", "login", "default")
	require.NoError(t, err)
	var resumedOAuthDeviceLogin struct {
		Device oauth.DeviceAuthorization `json:"device"`
		Token  oauth.TokenView           `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthDeviceLogin))
	require.Equal(t, "resume-device-1", resumedOAuthDeviceLogin.Device.DeviceCode)
	require.Equal(t, "ABCD-EFGH", resumedOAuthDeviceLogin.Device.UserCode)
	require.Equal(t, "devi...1234", resumedOAuthDeviceLogin.Token.AccessToken)
	require.Equal(t, "devi...1234", resumedOAuthDeviceLogin.Token.RefreshToken)

	out, err = runResumedJSON("/login", "device", "default")
	require.NoError(t, err)
	var resumedLoginDeviceAlias struct {
		Device oauth.DeviceAuthorization `json:"device"`
		Token  oauth.TokenView           `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLoginDeviceAlias))
	require.Equal(t, "resume-device-1", resumedLoginDeviceAlias.Device.DeviceCode)
	require.Equal(t, "devi...1234", resumedLoginDeviceAlias.Token.AccessToken)

	out, err = runResumedJSON("/oauth", "browser", "login", "default")
	require.NoError(t, err)
	var resumedOAuthBrowserLogin struct {
		RedirectURI string          `json:"redirect_uri"`
		Token       oauth.TokenView `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedOAuthBrowserLogin))
	require.Equal(t, "http://127.0.0.1:18080/oauth/callback", resumedOAuthBrowserLogin.RedirectURI)
	require.Equal(t, "brow...1234", resumedOAuthBrowserLogin.Token.AccessToken)
	require.Equal(t, "brow...1234", resumedOAuthBrowserLogin.Token.RefreshToken)

	out, err = runResumedJSON("/login", "browser", "default")
	require.NoError(t, err)
	var resumedLoginBrowserAlias struct {
		RedirectURI string          `json:"redirect_uri"`
		Token       oauth.TokenView `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLoginBrowserAlias))
	require.Equal(t, "http://127.0.0.1:18080/oauth/callback", resumedLoginBrowserAlias.RedirectURI)
	require.Equal(t, "brow...1234", resumedLoginBrowserAlias.Token.AccessToken)

	out, err = runResumedJSON("/doctor")
	require.NoError(t, err)
	var resumedDoctor doctor.Report
	require.NoError(t, json.Unmarshal([]byte(out), &resumedDoctor))
	require.Equal(t, "doctor", resumedDoctor.Kind)
	require.NotEmpty(t, resumedDoctor.Checks)

	out, err = runResumedJSON("/run", "go", "version")
	require.NoError(t, err)
	var resumedRun commandrun.Result
	require.NoError(t, json.Unmarshal([]byte(out), &resumedRun))
	require.Equal(t, "run", resumedRun.Kind)
	require.Equal(t, 0, resumedRun.ExitCode)
	require.Contains(t, strings.Join(resumedRun.Command, " "), "go version")

	out, err = runResumedJSON("/test")
	require.NoError(t, err)
	var resumedTest commandrun.Result
	require.NoError(t, json.Unmarshal([]byte(out), &resumedTest))
	require.Equal(t, "test", resumedTest.Kind)
	require.Equal(t, 0, resumedTest.ExitCode)
	require.Equal(t, []string{"go", "test", "./..."}, resumedTest.Command)

	out, err = runResumedJSON("/build")
	require.NoError(t, err)
	var resumedBuild commandrun.Result
	require.NoError(t, json.Unmarshal([]byte(out), &resumedBuild))
	require.Equal(t, "build", resumedBuild.Kind)
	require.Equal(t, 0, resumedBuild.ExitCode)
	require.Equal(t, []string{"go", "build", "./..."}, resumedBuild.Command)

	out, err = runResumedJSON("/lint")
	require.NoError(t, err)
	var resumedLint commandrun.Result
	require.NoError(t, json.Unmarshal([]byte(out), &resumedLint))
	require.Equal(t, "lint", resumedLint.Kind)
	require.Equal(t, 0, resumedLint.ExitCode)
	require.Equal(t, []string{"go", "vet", "./..."}, resumedLint.Command)

	if _, lookupErr := exec.LookPath(pythonExecutable()); lookupErr == nil {
		out, err = runResumedJSON("/python", "print('resume-python')")
		require.NoError(t, err)
		var resumedPython commandrun.Result
		require.NoError(t, json.Unmarshal([]byte(out), &resumedPython))
		require.Equal(t, "python", resumedPython.Kind)
		require.Equal(t, 0, resumedPython.ExitCode)
		require.Contains(t, resumedPython.Stdout, "resume-python")
	}

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

		out, err = runResumedJSON("/branch", "create", "resume-test")
		require.NoError(t, err)
		var resumedBranchCreate branchReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedBranchCreate))
		require.Equal(t, "branch", resumedBranchCreate.Kind)
		require.Equal(t, "create", resumedBranchCreate.Action)
		require.NotEmpty(t, resumedBranchCreate.Current)
		createdBranchFound := false
		for _, branch := range resumedBranchCreate.Branches {
			if branch.Name == "resume-test" {
				createdBranchFound = true
				break
			}
		}
		require.True(t, createdBranchFound)

		out, err = runResumedJSON("/tag", "release candidate")
		require.NoError(t, err)
		var resumedTag sessionTagReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedTag))
		require.Equal(t, "session_tag", resumedTag.Kind)
		require.Equal(t, "set", resumedTag.Action)
		require.Equal(t, "release candidate", resumedTag.Tag)

		out, err = runResumedJSON("/tag", "release candidate")
		require.NoError(t, err)
		var resumedTagConfirm sessionTagReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedTagConfirm))
		require.Equal(t, "confirmation_required", resumedTagConfirm.Status)
		require.Equal(t, "remove", resumedTagConfirm.Action)

		out, err = runResumedJSON("/tag", "release candidate", "--confirm")
		require.NoError(t, err)
		var resumedTagRemove sessionTagReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedTagRemove))
		require.Equal(t, "remove", resumedTagRemove.Action)
		require.Empty(t, resumedTagRemove.Tag)

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

		out, err = runResumedJSON("/stash", "push", "checkpoint")
		require.NoError(t, err)
		var resumedStashPush stashReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedStashPush))
		require.Equal(t, "stash", resumedStashPush.Kind)
		require.Equal(t, "push", resumedStashPush.Action)
		require.GreaterOrEqual(t, resumedStashPush.Count, 1)
		require.NotEmpty(t, resumedStashPush.Stashes)
		require.Contains(t, resumedStashPush.Stashes[0].Subject, "checkpoint")

		require.NoError(t, os.WriteFile(filepath.Join(workspace, "resumed-commit.txt"), []byte("resume commit\n"), 0o644))
		out, err = runResumedJSON("/commit", "--all", "resumed", "commit")
		require.NoError(t, err)
		var resumedCommit commitReport
		require.NoError(t, json.Unmarshal([]byte(out), &resumedCommit))
		require.Equal(t, "commit", resumedCommit.Kind)
		require.Equal(t, "create", resumedCommit.Action)
		require.Equal(t, "ok", resumedCommit.Status)
		require.True(t, resumedCommit.All)
		require.Contains(t, resumedCommit.Summary, "resumed commit")
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

	require.NoError(t, store.Append("resume-new", anthropic.TextMessage("user", "new one")))
	require.NoError(t, store.Append("resume-new", anthropic.TextMessage("assistant", "new two")))
	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-new", "--output-format", "json", "/new", "--confirm"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var newReport clearResumedReport
	require.NoError(t, json.Unmarshal([]byte(out), &newReport))
	require.Equal(t, "clear", newReport.Kind)
	require.Equal(t, "clear_session", newReport.Action)
	require.Equal(t, "resume-new", newReport.SessionID)
	require.Equal(t, 2, newReport.RemovedMessages)
	require.FileExists(t, newReport.Backup)

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

	_, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "resume-slash", "--output-format", "json", "/commit"}, config.FlagOverrides{})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "commit")
}
