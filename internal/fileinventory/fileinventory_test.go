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

func TestBuildRespectsGitignoreWhenEnabled(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "ignored-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("ignored.txt\n*.log\nignored-dir/\n!important.log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "kept.txt"), []byte("kept"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored.txt"), []byte("ignored"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "trace.log"), []byte("ignored"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "important.log"), []byte("kept"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ignored-dir", "nested.txt"), []byte("ignored"), 0o644))

	report, err := Build(workspace, Options{Limit: 20, RespectGitignore: true})
	require.NoError(t, err)
	require.True(t, report.RespectGitignore)
	require.Equal(t, []Entry{
		{Path: "important.log", Size: 4, Ext: "log", Depth: 1},
		{Path: "kept.txt", Size: 4, Ext: "txt", Depth: 1},
	}, report.Files)

	report, err = Build(workspace, Options{Limit: 20, RespectGitignore: false})
	require.NoError(t, err)
	require.False(t, report.RespectGitignore)
	paths := make([]string, 0, len(report.Files))
	for _, file := range report.Files {
		paths = append(paths, file.Path)
	}
	require.Contains(t, paths, "ignored.txt")
	require.Contains(t, paths, "trace.log")
	require.Contains(t, paths, "ignored-dir/nested.txt")
}

func TestBuildRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	_, err := Build(workspace, Options{Path: outside})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
}
