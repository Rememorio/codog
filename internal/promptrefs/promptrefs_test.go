package promptrefs

import (
	"os"
	"path/filepath"
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
