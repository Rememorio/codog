package promptrefs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpandAppendsWorkspaceFileReferences(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("note body\n"), 0o644))

	expanded := Expand("summarize @notes.md", workspace, nil)

	require.Contains(t, expanded, "summarize @notes.md")
	require.Contains(t, expanded, "<codog_file_references>")
	require.Contains(t, expanded, `<file path="notes.md"`)
	require.Contains(t, expanded, "note body")
}

func TestReferencesExtractsPathTokens(t *testing.T) {
	require.Equal(t, []string{"notes.md", "internal/app.go"}, References("read @notes.md, then @internal/app.go"))
	require.Empty(t, References("email a@example.com"))
}

func TestExpandRejectsEscapingReferences(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret body"), 0o644))

	expanded := Expand("read @../secret.txt", workspace, nil)

	require.Contains(t, expanded, `unavailable=`)
	require.NotContains(t, expanded, "secret body")
}

func TestExpandUsesAdditionalDirectories(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(extra, "shared.md"), []byte("shared body"), 0o644))

	expanded := Expand("read @"+filepath.Join(extra, "shared.md"), workspace, []string{extra})

	require.Contains(t, expanded, "shared body")
	require.Equal(t, 1, strings.Count(expanded, "<file "))
}

func TestExpandAppendsDirectoryReferences(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Docs\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "nested", "guide.txt"), []byte("guide body\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs", ".cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", ".cache", "secret.txt"), []byte("hidden secret\n"), 0o644))

	expanded := Expand("summarize @docs", workspace, nil)

	require.Contains(t, expanded, `<directory path="docs" files="2"`)
	require.Contains(t, expanded, `<file path="README.md"`)
	require.Contains(t, expanded, "# Docs")
	require.Contains(t, expanded, `<file path="nested/guide.txt"`)
	require.Contains(t, expanded, "guide body")
	require.Contains(t, expanded, "<skipped>")
	require.Contains(t, expanded, "binary.bin")
	require.Contains(t, expanded, ".cache/")
	require.NotContains(t, expanded, "hidden secret")
	require.NotContains(t, expanded, `unavailable="path is a directory"`)
}

func TestExpandDirectoryReferenceRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs", "safe.txt"), []byte("safe body\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret body\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(workspace, "docs", "secret-link.txt")))

	expanded := Expand("summarize @docs", workspace, nil)

	require.Contains(t, expanded, "safe body")
	require.Contains(t, expanded, "secret-link.txt")
	require.NotContains(t, expanded, "secret body")
}
