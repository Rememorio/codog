package planmode

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnterExitClearAndRenderPrompt(t *testing.T) {
	workspace := t.TempDir()

	report, err := Enter(workspace, "inspect first\nthen edit")
	require.NoError(t, err)
	require.Equal(t, "plan", report.Kind)
	require.Equal(t, "enter", report.Action)
	require.Equal(t, "active", report.Status)
	require.True(t, report.State.Active)
	require.Equal(t, "inspect first\nthen edit", report.State.Plan)
	require.NotEmpty(t, report.State.UpdatedAt)

	state, err := Load(workspace)
	require.NoError(t, err)
	require.True(t, state.Active)
	require.Contains(t, RenderPrompt(state), "Do not modify files")
	require.Contains(t, RenderPrompt(state), "inspect first")

	report, err = Exit(workspace)
	require.NoError(t, err)
	require.Equal(t, "exit", report.Action)
	require.Equal(t, "inactive", report.Status)
	require.False(t, report.State.Active)
	require.NotEmpty(t, report.State.ExitedAt)
	require.Empty(t, RenderPrompt(report.State))

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Plan")
	require.Contains(t, out.String(), "Status           inactive")

	report, err = Clear(workspace)
	require.NoError(t, err)
	require.Equal(t, "clear", report.Action)
	require.NoFileExists(t, Path(workspace))
}

func TestSetRequiresPlanTextAndShowMissing(t *testing.T) {
	workspace := t.TempDir()

	report, err := Show(workspace)
	require.NoError(t, err)
	require.Equal(t, "inactive", report.Status)

	_, err = Set(workspace, " ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan text")

	report, err = Set(workspace, "ship it carefully")
	require.NoError(t, err)
	require.True(t, report.State.Active)
	require.Equal(t, "ship it carefully", report.State.Plan)
	require.FileExists(t, Path(workspace))

	require.NoError(t, os.WriteFile(Path(workspace), []byte(`{"active":true,"plan":"reload"}`), 0o644))
	state, err := Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "reload", state.Plan)
}
