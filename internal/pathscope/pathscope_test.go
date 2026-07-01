package pathscope

import (
	"bytes"
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

func TestValidateReportsRequestedPathStatus(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	workspaceSubdir := filepath.Join(workspace, "pkg")
	require.NoError(t, os.MkdirAll(workspaceSubdir, 0o755))
	file := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(file, []byte("notes"), 0o644))

	report, err := Validate(workspace, nil, []string{extra, "pkg", file, "missing"})
	require.NoError(t, err)
	require.Equal(t, "validation", report.Kind)
	require.Equal(t, "add_dir", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, 4, report.Total)
	require.Equal(t, 2, report.ValidCount)
	require.Equal(t, 2, report.InvalidCount)
	require.True(t, report.Entries[0].Valid)
	require.False(t, report.Entries[0].AlreadyAllowed)
	require.True(t, report.Entries[1].Valid)
	require.True(t, report.Entries[1].AlreadyAllowed)
	require.False(t, report.Entries[2].Valid)
	require.Contains(t, report.Entries[2].Error, "not a directory")
	require.False(t, report.Entries[3].Valid)
	require.Contains(t, report.Entries[3].Error, "no such file")

	var out bytes.Buffer
	RenderValidationText(&out, report)
	require.Contains(t, out.String(), "Add-dir Validation")
	require.Contains(t, out.String(), "already-allowed")
	require.Contains(t, out.String(), "Invalid          2")
}

func TestValidateExistingAdditionalDirs(t *testing.T) {
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))
	missing := filepath.Join(t.TempDir(), "missing")
	require.NoError(t, Save(workspace, State{Dirs: []string{extra, missing}}))

	report, err := Validate(workspace, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "error", report.Status)
	require.Equal(t, 2, report.Total)
	require.Equal(t, 1, report.ValidCount)
	require.Equal(t, 1, report.InvalidCount)
	require.Equal(t, "workspace", report.Entries[0].Source)
}
