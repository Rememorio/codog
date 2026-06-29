package pathscope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddRemoveAndEffectiveDirs(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	configDir := filepath.Join(t.TempDir(), "config-extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	extraResolved, err := filepath.EvalSymlinks(extra)
	require.NoError(t, err)
	configResolved, err := filepath.EvalSymlinks(configDir)
	require.NoError(t, err)

	report, err := Add(workspace, []string{extra})
	require.NoError(t, err)
	require.Equal(t, "add", report.Action)
	require.Len(t, report.Entries, 1)
	require.Equal(t, extraResolved, report.Entries[0].Path)
	require.Equal(t, "workspace", report.Entries[0].Source)

	dirs, err := EffectiveDirs(workspace, []string{configDir})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{configResolved, extraResolved}, dirs)

	report, err = Remove(workspace, []string{extra})
	require.NoError(t, err)
	require.Empty(t, report.Entries)
}

func TestNormalizeDirRejectsFilesAndMissingPaths(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(file, []byte("notes"), 0o644))

	_, err := NormalizeDir(workspace, file)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")

	_, err = NormalizeDir(workspace, "missing")
	require.Error(t, err)
}

func TestEffectiveDirsSkipsMissingStoredDirs(t *testing.T) {
	workspace := t.TempDir()
	missing := filepath.Join(workspace, "missing")
	require.NoError(t, Save(workspace, State{Dirs: []string{missing}}))

	dirs, err := EffectiveDirs(workspace, nil)
	require.NoError(t, err)
	require.Empty(t, dirs)

	report, err := BuildReport(workspace, nil, "list")
	require.NoError(t, err)
	require.Len(t, report.Entries, 1)
	require.False(t, report.Entries[0].Exists)
}
