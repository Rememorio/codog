package projectscope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAncestorsStopAtProjectBoundary(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	workspace := filepath.Join(repo, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	ancestors := Ancestors(workspace)

	require.Equal(t, []string{workspace, repo}, ancestors)
	require.NotContains(t, ancestors, parent)
}

func TestAncestorsWithoutBoundaryStayAtWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "loose", "app")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	require.Equal(t, []string{workspace}, Ancestors(workspace))
}
