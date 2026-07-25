package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParseRawOutputArgs(t *testing.T) {
	request, err := parseRawOutputArgs([]string{"on", "--target", "local", "--json"}, "status")
	require.NoError(t, err)
	require.Equal(t, rawOutputRequest{Action: "on", Format: "json", Target: "local"}, request)

	request, err = parseRawOutputArgs([]string{
		"toggle",
		"--output-format", "text",
		"--target", "project",
		"--path", "config.json",
	}, "status")
	require.NoError(t, err)
	require.Equal(t, rawOutputRequest{
		Action: "toggle",
		Format: "text",
		Target: "project",
		Path:   "config.json",
	}, request)

	request, err = parseRawOutputArgs([]string{
		"off",
		"--output-format=json",
		"--target=user",
		"--path=config.json",
	}, "status")
	require.NoError(t, err)
	require.Equal(t, rawOutputRequest{
		Action: "off",
		Format: "json",
		Target: "user",
		Path:   "config.json",
	}, request)

	for _, args := range [][]string{
		{"invalid"},
		{"on", "off"},
		{"--target"},
		{"--path"},
		{"--output-format"},
		{"--output-format", "yaml"},
		{"--unknown"},
	} {
		_, err = parseRawOutputArgs(args, "status")
		require.Error(t, err, args)
	}
}

func TestRawOutputPersistsExplicitFalse(t *testing.T) {
	configHome := t.TempDir()
	enabled := true
	var out bytes.Buffer
	app := &App{
		Workspace: t.TempDir(),
		Config:    config.Config{ConfigHome: configHome, TUIRawOutputMode: &enabled},
		Out:       &out,
	}

	require.NoError(t, app.RawOutput([]string{"off", "--json"}))
	var report rawOutputReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.False(t, report.Enabled)
	require.False(t, rawOutputEnabled(app.Config.TUIRawOutputMode))

	var persisted map[string]any
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, false, persisted["tui_raw_output_mode"])
}

func TestRawOutputStatusToggleTextAndErrors(t *testing.T) {
	configHome := t.TempDir()
	var out bytes.Buffer
	app := &App{
		Workspace: t.TempDir(),
		Config:    config.Config{ConfigHome: configHome},
		Out:       &out,
	}

	require.NoError(t, app.RawOutput([]string{"status"}))
	require.Contains(t, out.String(), "Enabled      false")

	out.Reset()
	require.NoError(t, app.RawOutput([]string{"toggle"}))
	require.True(t, rawOutputEnabled(app.Config.TUIRawOutputMode))
	require.Contains(t, out.String(), "Config")

	out.Reset()
	require.NoError(t, app.RawOutput([]string{"on", "--json"}))
	var report rawOutputReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.True(t, report.Enabled)

	require.Error(t, app.RawOutput([]string{"--unknown"}))
	_, err := app.applyRawOutput(rawOutputRequest{Action: "off", Target: "invalid"})
	require.Error(t, err)
}
