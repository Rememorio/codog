package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestAllocateWithOptionsAppliesSparseCheckoutAndSymlinks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows environments")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-q")
	runGit(t, workspace, "config", "user.email", "a@example.test")
	runGit(t, workspace, "config", "user.name", "A")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "guide.md"), []byte("guide\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "node_modules", "dep.txt"), []byte("dep\n"), 0o644))
	runGit(t, workspace, "add", "app", "docs")
	runGit(t, workspace, "commit", "-q", "-m", "init")

	allocation, err := AllocateWithOptions(workspace, "reviewer", Options{
		SymlinkDirectories: []string{"node_modules"},
		SparsePaths:        []string{"app"},
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(allocation.Path, "app", "main.go"))
	require.NoFileExists(t, filepath.Join(allocation.Path, "docs", "guide.md"))
	linkInfo, err := os.Lstat(filepath.Join(allocation.Path, "node_modules"))
	require.NoError(t, err)
	require.NotZero(t, linkInfo.Mode()&os.ModeSymlink)
	require.Equal(t, []string{"node_modules"}, allocation.SymlinkDirectories)
	require.Equal(t, []string{"app"}, allocation.SparsePaths)

	stored, err := Load(workspace, allocation.ID)
	require.NoError(t, err)
	require.Equal(t, allocation.SymlinkDirectories, stored.SymlinkDirectories)
	require.Equal(t, allocation.SparsePaths, stored.SparsePaths)
	require.NoError(t, Remove(workspace, allocation.ID))
}

func TestAllocateWithOptionsRejectsUnsafePaths(t *testing.T) {
	_, err := normalizeOptions(Options{SymlinkDirectories: []string{"../outside"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "inside the workspace")

	_, err = normalizeOptions(Options{SparsePaths: []string{filepath.Join(string(filepath.Separator), "tmp")}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be relative")
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
