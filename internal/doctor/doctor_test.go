package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/mcp"
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
