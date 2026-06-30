package undo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestoreLastRestoresExistingFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))

	record, err := Push(workspace, "edit_file", path, true, []byte("old\n"))
	require.NoError(t, err)
	require.NotEmpty(t, record.ID)
	require.NoError(t, os.WriteFile(path, []byte("new\n"), 0o644))

	report, err := RestoreLast(workspace)
	require.NoError(t, err)
	require.Equal(t, "undo", report.Kind)
	require.Equal(t, "edit_file", report.Tool)
	require.Equal(t, "notes.txt", report.Path)
	require.True(t, report.Restored)
	require.Equal(t, 4, report.Bytes)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old\n", string(data))
}

func TestRestoreLastRemovesCreatedFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "created.txt")
	_, err := Push(workspace, "write_file", path, false, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("created\n"), 0o644))

	report, err := RestoreLast(workspace)
	require.NoError(t, err)
	require.True(t, report.Removed)
	require.NoFileExists(t, path)
}

func TestRestoreLastNoUndo(t *testing.T) {
	_, err := RestoreLast(t.TempDir())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUndo))
}
