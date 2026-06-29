package fileinventory

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildListsFilesWithGlobLimitAndHiddenPolicy(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("readme"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".hidden", "secret.go"), []byte("package hidden\n"), 0o644))

	report, err := Build(workspace, Options{Glob: "*.go", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, "files", report.Kind)
	require.Equal(t, 1, report.Total)
	require.Len(t, report.Files, 1)
	require.Equal(t, "pkg/main.go", report.Files[0].Path)
	require.Equal(t, "go", report.Files[0].Ext)
	require.False(t, report.Truncated)

	report, err = Build(workspace, Options{Glob: "*.go", IncludeHidden: true, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, report.Total)
	require.Len(t, report.Files, 1)
	require.True(t, report.Truncated)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Files")
	require.Contains(t, out.String(), "Listed           1")
}

func TestBuildRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	_, err := Build(workspace, Options{Path: outside})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}
