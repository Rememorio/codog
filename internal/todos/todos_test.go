package todos

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddUpdateReplaceClearAndRender(t *testing.T) {
	workspace := t.TempDir()

	report, err := Add(workspace, "write tests", "high")
	require.NoError(t, err)
	require.Equal(t, "add", report.Action)
	require.Equal(t, 1, report.Total)
	require.Equal(t, "todo-1", report.Items[0].ID)
	require.Equal(t, "pending", report.Items[0].Status)
	require.FileExists(t, Path(workspace))

	report, err = UpdateStatus(workspace, "todo-1", "in_progress")
	require.NoError(t, err)
	require.Equal(t, "in_progress", report.Items[0].Status)

	report, err = Replace(workspace, []Item{
		{Content: "first"},
		{ID: "custom", Content: "second", ActiveForm: "finishing second", Status: "completed", Priority: "low"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, report.Total)
	require.Equal(t, "todo-1", report.Items[0].ID)
	require.Equal(t, "first", report.Items[0].ActiveForm)
	require.Equal(t, "custom", report.Items[1].ID)
	require.Equal(t, "finishing second", report.Items[1].ActiveForm)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Todos")
	require.Contains(t, out.String(), "second")

	report, err = Clear(workspace)
	require.NoError(t, err)
	require.Equal(t, 0, report.Total)
	require.NoFileExists(t, Path(workspace))
}

func TestReplaceValidatesItems(t *testing.T) {
	_, err := Replace(t.TempDir(), []Item{{Content: "x", Status: "blocked"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid todo status")

	_, err = Replace(t.TempDir(), []Item{{Content: "x", Priority: "urgent"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid todo priority")
}
