package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/companion"
	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTUIRawSlashTogglesAndPersistsPreference(t *testing.T) {
	configHome := t.TempDir()
	app := &App{Workspace: t.TempDir(), Config: config.Config{ConfigHome: configHome}}
	request := tuiSlashRequest{command: "/raw"}

	result, handled, err := app.tuiDisplayControlSlashResult(request, nil, newTUIModeState(app.Config))
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, result.RawOutput)
	require.True(t, *result.RawOutput)
	require.True(t, rawOutputEnabled(app.Config.TUIRawOutputMode))
	require.FileExists(t, filepath.Join(configHome, "config.json"))
}

func TestTUIPetsPickerAndSelection(t *testing.T) {
	configHome := t.TempDir()
	app := &App{Workspace: t.TempDir(), Config: config.Config{ConfigHome: configHome}}
	mode := newTUIModeState(app.Config)

	picker, handled, err := app.tuiDisplayControlSlashResult(tuiSlashRequest{command: "/pets"}, nil, mode)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, picker.CommandView)
	require.NotEmpty(t, picker.CommandView.Tabs[0].Items)

	selected, handled, err := app.tuiDisplayControlSlashResult(tuiSlashRequest{
		command: "/pets",
		args:    []string{"use", companion.BuiltinID},
	}, nil, mode)
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, selected.CompanionChanged)
	require.Equal(t, companion.BuiltinID, selected.Companion.ID)

	disabled, handled, err := app.tuiDisplayControlSlashResult(tuiSlashRequest{
		command: "/pets",
		args:    []string{"off"},
	}, nil, mode)
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, disabled.CompanionChanged)
	require.Nil(t, disabled.Companion)
}

func TestToggleTUIRawOutputReturnsModelState(t *testing.T) {
	app := &App{Workspace: t.TempDir(), Config: config.Config{ConfigHome: t.TempDir()}}
	result, err := app.toggleTUIRawOutput(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result.RawOutput)
	require.True(t, *result.RawOutput)
	require.Equal(t, "raw", result.Setting)

	data, err := os.ReadFile(filepath.Join(app.Config.ConfigHome, "config.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"tui_raw_output_mode": true`)
}
