package githubsetup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupDryRunDoesNotWriteWorkflows(t *testing.T) {
	workspace := t.TempDir()

	report, err := Setup(Options{Workspace: workspace, DryRun: true})

	require.NoError(t, err)
	require.True(t, report.DryRun)
	require.Len(t, report.Workflows, 2)
	require.False(t, fileExists(filepath.Join(workspace, ".github", "workflows", "claude.yml")))
	require.Contains(t, report.Instructions[0], "gh secret set ANTHROPIC_API_KEY")
}

func TestSetupWritesSelectedWorkflow(t *testing.T) {
	workspace := t.TempDir()

	report, err := Setup(Options{Workspace: workspace, SecretName: "CLAUDE_KEY", Workflows: []string{"claude"}})

	require.NoError(t, err)
	require.Len(t, report.Workflows, 1)
	require.True(t, report.Workflows[0].Created)
	data, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "claude.yml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "anthropics/claude-code-action@v1")
	require.Contains(t, string(data), "${{ secrets.CLAUDE_KEY }}")
}

func TestSetupDoesNotOverwriteExistingWorkflowWithoutForce(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".github", "workflows", "claude.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("custom\n"), 0o644))

	report, err := Setup(Options{Workspace: workspace, Workflows: []string{"claude"}})

	require.NoError(t, err)
	require.Len(t, report.Warnings, 2)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(data))
}

func TestSetupOverwritesWithForce(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".github", "workflows", "claude.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("custom\n"), 0o644))

	report, err := Setup(Options{Workspace: workspace, Workflows: []string{"claude"}, Force: true})

	require.NoError(t, err)
	require.True(t, report.Workflows[0].Overwritten)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "name: Claude Code")
}

func TestParseGitHubRepo(t *testing.T) {
	require.Equal(t, "owner/repo", parseGitHubRepo("git@github.com:owner/repo.git"))
	require.Equal(t, "owner/repo", parseGitHubRepo("https://github.com/owner/repo.git"))
	require.Equal(t, "owner/repo", parseGitHubRepo("ssh://git@github.com/owner/repo.git"))
	require.Empty(t, parseGitHubRepo("https://example.com/owner/repo.git"))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
