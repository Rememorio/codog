package doctor

import (
	"bytes"
	"testing"

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
