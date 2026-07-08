package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEditorDirectAPIsPersistAndClearState(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	source := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(source), 0o644))
	server := Server{Workspace: workspace, ConfigHome: configHome, TrustToken: "secret"}

	identity, err := server.IdentifyEditor(rawJSON(t, map[string]any{
		"editor":    "  VS Code  ",
		"version":   "  2.0  ",
		"workspace": "  " + workspace + "  ",
		"token":     "secret",
	}))
	require.NoError(t, err)
	require.Equal(t, "VS Code", identity.Editor)
	require.Equal(t, "2.0", identity.Version)
	require.Equal(t, workspace, identity.Workspace)
	require.True(t, identity.Trusted)

	opened, err := server.OpenEditorFile(rawJSON(t, map[string]any{"path": "main.go"}))
	require.NoError(t, err)
	require.Equal(t, "main.go", opened.Path)

	selection, err := server.SetEditorSelection(rawJSON(t, EditorSelection{
		StartLine:   3,
		StartColumn: 6,
		EndLine:     4,
		EndColumn:   11,
	}))
	require.NoError(t, err)
	require.Equal(t, "main.go", selection.Path)
	require.Equal(t, "main() {\n\tprintln(\"", selection.Text)

	state, err := server.EditorState()
	require.NoError(t, err)
	require.Equal(t, "VS Code", state.Identity.Editor)
	require.Equal(t, "main.go", state.OpenFile.Path)
	require.Equal(t, selection.Text, state.Selection.Text)

	path, err := server.EditorStatePath()
	require.NoError(t, err)
	require.FileExists(t, path)
	require.NoError(t, server.ClearEditorState())
	require.NoFileExists(t, path)
}

func TestEditorDirectAPIsRejectUntrustedAndInvalidSelections(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o644))
	server := Server{Workspace: workspace, ConfigHome: configHome}

	_, err := server.OpenEditorFile(rawJSON(t, map[string]any{"path": "main.go"}))
	require.ErrorContains(t, err, "editor is not trusted")

	_, err = server.IdentifyEditor(rawJSON(t, map[string]any{"editor": "Code"}))
	require.NoError(t, err)
	_, err = server.SetEditorSelection(rawJSON(t, EditorSelection{Path: "main.go", StartLine: 1, StartColumn: -1}))
	require.ErrorContains(t, err, "selection columns must be non-negative")

	_, err = server.SetEditorSelection(rawJSON(t, EditorSelection{Path: "main.go", StartLine: 1, StartColumn: 8, EndColumn: 2}))
	require.ErrorContains(t, err, "end_column must be after start_column")

	_, err = server.OpenEditorFile(rawJSON(t, map[string]any{"path": "."}))
	require.ErrorContains(t, err, "path must be a file")
}

func TestEditorStatePathRequiresConfigHome(t *testing.T) {
	server := Server{Workspace: t.TempDir()}

	_, err := server.EditorStatePath()
	require.ErrorContains(t, err, "config home is required")
	require.ErrorContains(t, server.ClearEditorState(), "config home is required")
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
