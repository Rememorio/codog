package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusDiffAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))

	status, err := Status(workspace)
	require.NoError(t, err)
	require.Contains(t, status, "notes.txt")

	diff, err := Diff(workspace, false)
	require.NoError(t, err)
	require.Empty(t, diff)

	result, err := Commit(workspace, CommitOptions{All: true, Message: "add notes"})
	require.NoError(t, err)
	require.NotEmpty(t, result.Commit)
	require.Contains(t, result.Summary, "add notes")

	log, err := Log(workspace, 1)
	require.NoError(t, err)
	require.Contains(t, log, "add notes")

	blame, err := Blame(workspace, "notes.txt", 1)
	require.NoError(t, err)
	require.Contains(t, blame, "hello")
	require.Contains(t, blame, "Codog Test")

	status, err = Status(workspace)
	require.NoError(t, err)
	require.True(t, strings.Contains(status, "## main") || strings.Contains(status, "## master"))
}

func TestCommitRequiresStagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")

	_, err := Commit(workspace, CommitOptions{Message: "empty"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no staged changes")
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
