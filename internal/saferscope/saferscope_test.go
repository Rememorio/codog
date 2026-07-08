package saferscope

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestPreviewHonorsExplicitTargetAndRenderText(t *testing.T) {
	workspace := testWorkspace(t)

	report, err := Preview(workspace, Options{Target: "app"})
	require.NoError(t, err)
	require.Equal(t, "preview", report.Action)
	require.Equal(t, "actionable", report.Status)
	choices := choicesByID(report.Choices)
	require.Contains(t, choices, "workspace")
	require.Equal(t, filepath.Join(workspace, "app"), choices["workspace"].Target)
	require.Contains(t, choices["workspace"].PreviewIncludes, "app/main.go")

	var out bytes.Buffer
	RenderText(&out, report)
	text := out.String()
	require.Contains(t, text, "Safer Scope")
	require.Contains(t, text, "Status           actionable")
	require.Contains(t, text, "Choice           workspace available")
	require.Contains(t, text, "Target         "+filepath.Join(workspace, "app"))
	require.Contains(t, text, "Restore          codog scope restore")
}

func TestPreviewRejectsUnsafeTarget(t *testing.T) {
	workspace := testWorkspace(t)
	outside := filepath.Join(filepath.Dir(workspace), "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))

	_, err := Preview(workspace, Options{Target: outside})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope target escapes workspace")

	_, err = Preview(workspace, Options{Target: "app/main.go"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope target is not a directory")
}

func TestPreviewCleanWorkspaceReturnsNoAction(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o644))

	report, err := Preview(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "clean", report.Status)
	require.Empty(t, report.Choices)
}

func TestApplySwitchesWorkspaceAndRecordsRestoreState(t *testing.T) {
	workspace := testWorkspace(t)
	now := time.Date(2026, 7, 7, 1, 2, 3, 0, time.UTC)

	status, err := Status(workspace)
	require.NoError(t, err)
	require.Equal(t, "status", status.Action)
	require.Equal(t, "inactive", status.Status)
	require.False(t, status.Confirmed)
	require.Equal(t, "no safer scope state found", status.Message)

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

	status, err = Status(filepath.Join(workspace, "app"))
	require.NoError(t, err)
	require.Equal(t, "status", status.Action)
	require.Equal(t, "applied", status.Status)
	require.True(t, status.Confirmed)
	require.Equal(t, workspace, status.OriginalWorkspace)
	require.Equal(t, filepath.Join(workspace, "app"), status.ActiveWorkspace)
	require.Equal(t, "workspace", status.AppliedChoice)
	require.Len(t, status.Applied, 1)
	require.Equal(t, "workspace", status.Applied[0].ID)
	require.Equal(t, "switch_workspace", status.Applied[0].Action)

	restore, err := Restore(filepath.Join(workspace, "app"))
	require.NoError(t, err)
	require.Equal(t, "restore", restore.Action)
	require.True(t, restore.Restored)
	require.Equal(t, workspace, restore.ActiveWorkspace)
	require.NoFileExists(t, StatePath(workspace))
	require.NoFileExists(t, StatePath(filepath.Join(workspace, "app")))
	_, err = LoadState(filepath.Join(workspace, "app"))
	require.ErrorIs(t, err, os.ErrNotExist)

	status, err = Status(workspace)
	require.NoError(t, err)
	require.Equal(t, "inactive", status.Status)
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

	status, err := Status(workspace)
	require.NoError(t, err)
	require.Equal(t, "applied", status.Status)
	require.Equal(t, "ignore", status.AppliedChoice)
	require.Len(t, status.Applied, 1)
	require.Equal(t, ActionCreateIgnoreFile, status.Applied[0].Action)
	require.Equal(t, ".codogignore", status.Applied[0].IgnoreFile)
	require.Contains(t, status.Applied[0].IgnoreEntries, "node_modules/")

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

func TestApplyBothRecordsAllChoices(t *testing.T) {
	workspace := testWorkspace(t)

	report, err := Apply(workspace, Options{Choice: "both"})
	require.NoError(t, err)
	require.Equal(t, "workspace,ignore", report.AppliedChoice)
	require.Len(t, report.Applied, 2)
	require.Equal(t, filepath.Join(workspace, "app"), report.ActiveWorkspace)

	state, err := LoadState(filepath.Join(workspace, "app"))
	require.NoError(t, err)
	require.Equal(t, "workspace,ignore", state.AppliedChoice)
	require.Equal(t, ".codogignore", state.IgnoreFile)
	require.True(t, strings.Contains(strings.Join(state.IgnoreEntries, "\n"), "node_modules/"))
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
