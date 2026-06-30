package prworkflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunDryRunPlansSafeBranchAndPR(t *testing.T) {
	workspace := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))

	report, err := Run(context.Background(), Options{
		Workspace: workspace,
		Message:   "feat: ship dry run",
		All:       true,
		DryRun:    true,
	})
	require.NoError(t, err)
	require.Equal(t, "planned", report.Status)
	require.True(t, report.DryRun)
	require.Equal(t, "codog_feat_ship_dry_run", report.Branch)
	require.Equal(t, "main", report.Base)
	require.Equal(t, "origin", report.Remote)
	require.Equal(t, "feat: ship dry run", report.Title)
	require.Empty(t, report.Commit)
	require.Contains(t, stepNames(report.Steps), "branch")
	require.Contains(t, stepNames(report.Steps), "stage")
	require.Contains(t, stepNames(report.Steps), "commit")
	require.Contains(t, stepNames(report.Steps), "push")
	require.Contains(t, stepNames(report.Steps), "pull_request")

	current := git(t, workspace, "branch", "--show-current")
	require.Equal(t, "main", strings.TrimSpace(current))
}

func TestRunRequiresWorkspace(t *testing.T) {
	_, err := Run(context.Background(), Options{Message: "feat: missing workspace", All: true, DryRun: true})
	require.ErrorContains(t, err, "workspace is required")
}

func TestRunCommitsAndPushesWithoutPR(t *testing.T) {
	workspace := initRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	git(t, "", "init", "--bare", bare)
	git(t, workspace, "remote", "add", "origin", bare)
	git(t, workspace, "push", "-u", "origin", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))

	report, err := Run(context.Background(), Options{
		Workspace: workspace,
		Message:   "feat: ship workflow",
		Branch:    "ship_workflow",
		All:       true,
		NoPR:      true,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "ship_workflow", report.Branch)
	require.NotEmpty(t, report.Commit)
	require.Empty(t, report.PRURL)
	require.NotContains(t, stepNames(report.Steps), "pull_request")
	require.Equal(t, "ship_workflow", strings.TrimSpace(git(t, workspace, "branch", "--show-current")))
	require.NotEmpty(t, strings.TrimSpace(git(t, workspace, "ls-remote", "--heads", "origin", "ship_workflow")))
}

func initRepo(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	git(t, "", "init", "-b", "main", workspace)
	git(t, workspace, "config", "user.email", "codog@example.test")
	git(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "chore: base")
	return workspace
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
	return string(data)
}

func stepNames(steps []Step) []string {
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.Name)
	}
	return names
}
