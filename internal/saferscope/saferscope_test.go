package saferscope

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreviewBuildsActionableChoices(t *testing.T) {
	workspace := testWorkspace(t)

	report, err := Preview(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "safer_scope", report.Kind)
	require.Equal(t, "preview", report.Action)
	require.Equal(t, "actionable", report.Status)
	require.True(t, report.Advisory)
	require.False(t, report.Confirmed)
	require.Equal(t, "warn", report.Risk.Status)
	require.Equal(t, "high", report.Risk.Level)

	choices := choicesByID(report.Choices)
	require.Contains(t, choices, "workspace")
	require.Contains(t, choices, "ignore")
	require.Equal(t, filepath.Join(workspace, "app"), choices["workspace"].Target)
	require.Contains(t, choices["workspace"].PreviewIncludes, "app/main.go")
	require.Contains(t, choices["workspace"].PreviewExcludes, "node_modules")
	require.Equal(t, ActionCreateIgnoreFile, choices["ignore"].Action)
	require.Equal(t, ".codogignore", choices["ignore"].IgnoreFile)
	require.Contains(t, choices["ignore"].IgnoreEntries, "node_modules/")
	require.Contains(t, choices["ignore"].IgnoreEntries, "dist/")
}

func TestApplySwitchesWorkspaceAndRecordsRestoreState(t *testing.T) {
	workspace := testWorkspace(t)
	now := time.Date(2026, 7, 7, 1, 2, 3, 0, time.UTC)

	report, err := Apply(workspace, Options{Choice: "workspace", Now: now})
	require.NoError(t, err)
	require.Equal(t, "apply", report.Action)
	require.Equal(t, "applied", report.Status)
	require.False(t, report.Advisory)
	require.True(t, report.Confirmed)
	require.Equal(t, workspace, report.OriginalWorkspace)
	require.Equal(t, filepath.Join(workspace, "app"), report.ActiveWorkspace)
	require.Equal(t, "workspace", report.AppliedChoice)
	require.Contains(t, report.RestoreCommand, "scope restore")

	state, err := LoadState(filepath.Join(workspace, "app"))
	require.NoError(t, err)
	require.Equal(t, workspace, state.OriginalWorkspace)
	require.Equal(t, filepath.Join(workspace, "app"), state.ActiveWorkspace)
	require.Equal(t, now, state.AppliedAt)
	require.FileExists(t, StatePath(workspace))
	require.FileExists(t, StatePath(filepath.Join(workspace, "app")))

	restore, err := Restore(filepath.Join(workspace, "app"))
	require.NoError(t, err)
	require.Equal(t, "restore", restore.Action)
	require.True(t, restore.Restored)
	require.Equal(t, workspace, restore.ActiveWorkspace)
	require.NoFileExists(t, StatePath(workspace))
	require.NoFileExists(t, StatePath(filepath.Join(workspace, "app")))
	_, err = LoadState(filepath.Join(workspace, "app"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestApplyIgnoreWritesAndRestoresIgnoreBlock(t *testing.T) {
	workspace := testWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codogignore"), []byte("existing.tmp\n"), 0o644))

	report, err := Apply(workspace, Options{Choice: "ignore"})
	require.NoError(t, err)
	require.Equal(t, "ignore", report.AppliedChoice)
	require.Len(t, report.Applied, 1)
	require.Equal(t, ActionCreateIgnoreFile, report.Applied[0].Action)

	data, err := os.ReadFile(filepath.Join(workspace, ".codogignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), "existing.tmp")
	require.Contains(t, string(data), IgnoreMarker)
	require.Contains(t, string(data), "node_modules/")
	require.Contains(t, string(data), "dist/")

	_, err = Restore(workspace)
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(workspace, ".codogignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), "existing.tmp")
	require.NotContains(t, string(data), IgnoreMarker)
	require.NotContains(t, string(data), "node_modules/")
	require.NoFileExists(t, StatePath(workspace))
}

func TestApplyIgnoreAcceptsLegacyStubChoice(t *testing.T) {
	workspace := testWorkspace(t)

	report, err := Apply(workspace, Options{Choice: legacyWriteIgnoreStub})
	require.NoError(t, err)
	require.Equal(t, "ignore", report.AppliedChoice)
	require.Len(t, report.Applied, 1)
	require.Equal(t, ActionCreateIgnoreFile, report.Applied[0].Action)

	data, err := os.ReadFile(filepath.Join(workspace, ".codogignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), IgnoreMarker)
}

func TestApplyIgnoreAcceptsLegacyAppendBlockChoice(t *testing.T) {
	workspace := testWorkspace(t)

	report, err := Apply(workspace, Options{Choice: ActionAppendIgnoreBlock})
	require.NoError(t, err)
	require.Equal(t, "ignore", report.AppliedChoice)
	require.Len(t, report.Applied, 1)
	require.Equal(t, ActionCreateIgnoreFile, report.Applied[0].Action)

	data, err := os.ReadFile(filepath.Join(workspace, ".codogignore"))
	require.NoError(t, err)
	require.Contains(t, string(data), IgnoreMarker)
}

func testWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	resolved, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	workspace = resolved
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "dist"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app", "main.go"), []byte("package app\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app", "main_test.go"), []byte("package app\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "debug.log"), []byte("trace\n"), 0o644))
	return workspace
}

func choicesByID(choices []Choice) map[string]Choice {
	out := map[string]Choice{}
	for _, choice := range choices {
		out[choice.ID] = choice
	}
	return out
}
