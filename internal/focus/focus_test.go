package focus

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddRemoveClearAndRenderPrompt(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\n\nfunc A() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("remember this\n"), 0o644))

	report, err := Add(workspace, []string{"pkg/a.go", "notes.md"})
	require.NoError(t, err)
	require.Equal(t, "add", report.Action)
	require.Equal(t, 2, report.Total)
	require.FileExists(t, Path(workspace))

	loaded, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 2)
	require.Equal(t, "notes.md", loaded.Entries[0].Path)
	require.Equal(t, "file", loaded.Entries[0].Kind)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "notes.md")

	prompt := RenderPrompt(workspace)
	require.Contains(t, prompt, "<focused_context>")
	require.Contains(t, prompt, "func A()")
	require.Contains(t, prompt, "remember this")

	report, err = Remove(workspace, []string{"notes.md"})
	require.NoError(t, err)
	require.Equal(t, 1, report.Total)
	require.NotContains(t, RenderPrompt(workspace), "remember this")

	report, err = Clear(workspace)
	require.NoError(t, err)
	require.Equal(t, 0, report.Total)
	require.Empty(t, RenderPrompt(workspace))
}

func TestFocusDirectoriesAndRejectsEscapes(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "pkg", "a.go"), []byte("package pkg\n"), 0o644))

	_, err := Add(workspace, []string{outside})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")

	report, err := Add(workspace, []string{"pkg"})
	require.NoError(t, err)
	require.Equal(t, "dir", report.Entries[0].Kind)
	prompt := RenderPrompt(workspace)
	require.Contains(t, prompt, "- pkg/a.go")
}

func TestMissingFocusedPathIsReportedAsMissingOnLoad(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("notes\n"), 0o644))
	require.NoError(t, Save(workspace, State{
		Entries: []Entry{{Path: "notes.md", Kind: "file", Exists: true}},
	}))
	require.NoError(t, os.Remove(filepath.Join(workspace, "notes.md")))

	report, err := BuildReport(workspace)
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	require.False(t, report.Entries[0].Exists)
}
