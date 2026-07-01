package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/sandbox"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/stretchr/testify/require"
)

func TestRunWarnsWhenAuthMissing(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	require.False(t, report.HasFailures)
	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusWarn, auth.Status)
	require.Contains(t, auth.Summary, "No Anthropic credentials")
}

func TestRunFailsInvalidPermissionMode(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "root",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusFail, report.Status)
	require.True(t, report.HasFailures)
	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusFail, permissions.Status)
	require.Contains(t, permissions.Hint, "workspace-write")
}

func TestRenderText(t *testing.T) {
	report := NewReport([]Check{
		{Name: "Auth", Status: StatusOK, Summary: "ready"},
		{Name: "Git", Status: StatusWarn, Summary: "not a worktree", Details: []string{"Inside worktree: false"}, Hint: "Run from a worktree."},
	})

	var out bytes.Buffer
	RenderText(&out, report)

	require.Contains(t, out.String(), "Doctor")
	require.Contains(t, out.String(), "Warnings         1")
	require.Contains(t, out.String(), "Git")
	require.Contains(t, out.String(), "Inside worktree: false")
	require.Contains(t, out.String(), "Run from a worktree.")
}

func TestRunWarnsForMissingHookPath(t *testing.T) {
	workspace := t.TempDir()
	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		PreToolUse:     []string{"./hooks/missing.sh"},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	hooks := findCheck(t, report, "Hooks")
	require.Equal(t, StatusWarn, hooks.Status)
	require.Contains(t, hooks.Summary, "could not be found")
	require.Contains(t, strings.Join(hooks.Details, "\n"), filepath.Join(workspace, "hooks", "missing.sh"))
}

func TestRunAcceptsExistingHookPath(t *testing.T) {
	workspace := t.TempDir()
	hooksDir := filepath.Join(workspace, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		PreToolUse:     []string{"./hooks/pre.sh"},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	hooks := findCheck(t, report, "Hooks")
	require.Equal(t, StatusOK, hooks.Status)
	require.Contains(t, hooks.Summary, "runnable")
}

func TestRunWarnsForStaleBranchFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runTestGit(t, workspace, "init", "-b", "main")
	runTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "chore: base")
	runTestGit(t, workspace, "switch", "-c", "topic")
	runTestGit(t, workspace, "switch", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "fix: main update")
	runTestGit(t, workspace, "switch", "topic")

	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	git := findCheck(t, report, "Git")
	require.Equal(t, StatusWarn, git.Status)
	require.Contains(t, git.Summary, "behind or diverged")
	require.Contains(t, strings.Join(git.Details, "\n"), "Freshness: stale")
	require.Contains(t, strings.Join(git.Details, "\n"), "Behind: 1")
	require.Contains(t, strings.Join(git.Details, "\n"), "Missing: fix: main update")
}

func TestRunWarnsForUnavailableMCPServer(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		MCPServerStatuses: []mcp.ServerStatus{
			{Name: "ready", Status: "ok", ToolCount: 2, ResolvedPath: "echo"},
			{Name: "missing", Status: "command_not_found", Error: "missing command"},
		},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	check := findCheck(t, report, "MCP")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "1 MCP server")
	require.Contains(t, strings.Join(check.Details, "\n"), "missing: command_not_found")
}

func TestRunReportsConfigValidationChecks(t *testing.T) {
	hookIndex := 0
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		MCPValidation: localstatus.MCPValidationStatus{
			TotalConfigured: 2,
			ValidCount:      1,
			InvalidCount:    1,
			InvalidServers: []localstatus.ValidationIssue{{
				Name:       "missing",
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command",
			}},
		},
		HookValidation: localstatus.HookValidationStatus{
			ValidCount:   1,
			InvalidCount: 1,
			InvalidHooks: []localstatus.ValidationIssue{{
				Event:      "pre_tool_use",
				Index:      &hookIndex,
				HookIndex:  &hookIndex,
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command",
			}},
		},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	mcpValidation := findCheck(t, report, "MCP validation")
	require.Equal(t, StatusWarn, mcpValidation.Status)
	require.Contains(t, mcpValidation.Summary, "1 MCP server entry is invalid")
	require.Equal(t, 2, mcpValidation.Data["total_configured"])
	require.Equal(t, 1, mcpValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(mcpValidation.Details, "\n"), "Invalid server: missing")
	require.Contains(t, mcpValidation.Hint, "mcp_validation.invalid_servers")

	hookValidation := findCheck(t, report, "Hook validation")
	require.Equal(t, StatusWarn, hookValidation.Status)
	require.Contains(t, hookValidation.Summary, "1 hook entry is invalid")
	require.Equal(t, 1, hookValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(hookValidation.Details, "\n"), "Invalid hook: pre_tool_use")
	require.Contains(t, hookValidation.Hint, "hook_validation.invalid_hooks")
}

func TestRunReportsSandboxFallbackDetails(t *testing.T) {
	report := Run(Options{
		Workspace:       t.TempDir(),
		ConfigHome:      t.TempDir(),
		Model:           "claude-test",
		BaseURL:         "https://api.example.test",
		APIKey:          "secret",
		PermissionMode:  "workspace-write",
		ToolCount:       6,
		SessionCount:    0,
		SandboxFallback: "bwrap: command not found",
	})

	check := findCheck(t, report, "Sandbox")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, strings.Join(check.Details, "\n"), "Fallback: bwrap: command not found")
	require.Contains(t, strings.Join(check.Details, "\n"), "In container: false")
}

func TestRunReportsSandboxRuntimeStatus(t *testing.T) {
	status := sandbox.SandboxExecutionStatus{
		Enabled:            true,
		Active:             false,
		Supported:          false,
		NamespaceSupported: false,
		NamespaceActive:    false,
		NetworkSupported:   false,
		NetworkActive:      false,
		FilesystemMode:     "workspace-only",
		FilesystemActive:   false,
		AllowedMounts:      []string{},
		InContainer:        true,
		ContainerMarkers:   []string{"/.dockerenv"},
		FallbackReason:     "sandbox strategy unavailable",
	}
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxRuntime: &status,
	})

	check := findCheck(t, report, "Sandbox")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "not currently active")
	require.Contains(t, strings.Join(check.Details, "\n"), "Enabled: true")
	require.Contains(t, strings.Join(check.Details, "\n"), "Filesystem mode: workspace-only")
	require.Contains(t, strings.Join(check.Details, "\n"), "Fallback: sandbox strategy unavailable")
	require.NotNil(t, check.Data)
	require.Equal(t, true, check.Data["enabled"])
	require.Equal(t, false, check.Data["active"])
	require.Equal(t, "workspace-only", check.Data["filesystem_mode"])
	require.Equal(t, []string{}, check.Data["allowed_mounts"])
	require.Contains(t, check.Hint, "supported sandbox strategy")
}

func runTestGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}

func findCheck(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q in %#v", name, report.Checks)
	return Check{}
}
