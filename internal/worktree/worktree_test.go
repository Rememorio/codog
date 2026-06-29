package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllocateListRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.email", "a@example.test")
	runGit(t, workspace, "config", "user.name", "A")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("hello\n"), 0o644))
	runGit(t, workspace, "add", "README.md")
	runGit(t, workspace, "commit", "-q", "-m", "init")

	allocation, err := Allocate(workspace, "reviewer")
	require.NoError(t, err)
	require.NotEmpty(t, allocation.ID)
	require.FileExists(t, filepath.Join(allocation.Path, "README.md"))

	allocations, err := List(workspace)
	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Equal(t, allocation.ID, allocations[0].ID)

	require.NoError(t, Remove(workspace, allocation.ID))
	require.NoDirExists(t, allocation.Path)
	allocations, err = List(workspace)
	require.NoError(t, err)
	require.Empty(t, allocations)
}

func TestRemoveRejectsUnsafeID(t *testing.T) {
	err := Remove(t.TempDir(), "../bad")
	require.Error(t, err)
	require.Contains(t, err.Error(), "single path component")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
