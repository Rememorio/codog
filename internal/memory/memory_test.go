package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverBetweenLoadsBoundaryToWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "cmd", "tool")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("workspace instructions"), 0o644))

	files, err := discoverBetween(workspace, root)

	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, "AGENTS.md", files[0].Name)
	require.Equal(t, "root instructions", strings.TrimSpace(files[0].Body))
	require.Equal(t, "CLAUDE.md", files[1].Name)
	require.Equal(t, "workspace instructions", strings.TrimSpace(files[1].Body))
}

func TestDiscoverUsesGitRootAsBoundary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	runGit(t, root, "init")
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("git root instructions"), 0o644))

	files, err := Discover(workspace)

	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "git root instructions", strings.TrimSpace(files[0].Body))
}

func TestDiscoverTruncatesLargeFiles(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("a", MaxFileBytes+10)
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(body), 0o644))

	files, err := discoverBetween(root, root)

	require.NoError(t, err)
	require.Len(t, files, 1)
	require.True(t, files[0].Truncated)
	require.Len(t, files[0].Body, MaxFileBytes)
	require.Equal(t, MaxFileBytes, files[0].Chars)
}

func TestRenderMemoryBlock(t *testing.T) {
	files := []File{{
		Path: "/repo/AGENTS.md",
		Name: "AGENTS.md",
		Body: "Use concise commit messages.",
	}}

	rendered := Render(files)

	require.Contains(t, rendered, "<project_memory>")
	require.Contains(t, rendered, `path="/repo/AGENTS.md"`)
	require.Contains(t, rendered, "Use concise commit messages.")
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
