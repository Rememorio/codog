package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/stretchr/testify/require"
)

func TestParseVisualizeArgs(t *testing.T) {
	request, err := parseVisualizeArgs([]string{"show", "chart.html", "--json"})
	require.NoError(t, err)
	require.Equal(t, visualizeRequest{Action: "show", File: "chart.html", Format: "json"}, request)

	for _, args := range [][]string{
		{"show"},
		{"list", "extra.html"},
		{"list", "--output-format", "yaml"},
		{"--unknown"},
		{"path", "one", "two"},
	} {
		_, err = parseVisualizeArgs(args)
		require.Error(t, err, args)
	}
}

func TestVisualizeListShowAndPath(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	sourceDir := filepath.Join(workspace, ".codog", "visualizations")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "chart.html"), []byte("<h1>Chart</h1>"), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Config: config.Config{ConfigHome: configHome}, Out: &out}

	require.NoError(t, app.Visualize([]string{"list", "--json"}))
	var listed visualizeReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed))
	require.Len(t, listed.Items, 1)
	require.Equal(t, "chart.html", listed.Items[0].File)

	out.Reset()
	require.NoError(t, app.Visualize([]string{"show", "chart.html"}))
	require.Contains(t, out.String(), "chart.viewer.html")
	require.Contains(t, out.String(), "file://")

	out.Reset()
	require.NoError(t, app.Visualize([]string{"path"}))
	require.Contains(t, out.String(), sourceDir)
}

func TestRewriteVisualizationEntriesOnlyChangesAssistantContent(t *testing.T) {
	workspace := t.TempDir()
	sourceDir := filepath.Join(workspace, ".codog", "visualizations")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "view.html"), []byte("ok"), 0o644))
	app := &App{Workspace: workspace, Config: config.Config{ConfigHome: t.TempDir()}}
	directive := `::codog-inline-vis{"file":"view.html"}`

	entries := app.rewriteVisualizationEntries([]tui.Entry{
		{Role: "user", Text: directive},
		{Role: "assistant", Text: directive},
	})
	require.Equal(t, directive, entries[0].Text)
	require.Contains(t, entries[1].Text, "Open visualization")
	require.NotContains(t, entries[1].Text, "::codog-inline-vis")
}

func TestTUITurnResponseRewritesCompletedVisualization(t *testing.T) {
	workspace := t.TempDir()
	sourceDir := filepath.Join(workspace, ".codog", "visualizations")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "view.html"), []byte("ok"), 0o644))
	app := &App{Workspace: workspace, Config: config.Config{ConfigHome: t.TempDir()}}
	submission := tuiTurnSubmission{submitter: tuiTurnSubmitter{app: app}}
	submission.out.WriteString(`Done` + "\n\n" + `::codog-inline-vis{"file":"view.html"}`)
	submission.streamOut.emitted = true
	submission.toolCalls = []runloop.ToolCall{{Name: "write_file", Output: "ok"}}

	response, err := submission.response(nil)
	require.NoError(t, err)
	require.Contains(t, response, "Tools")
	require.Contains(t, response, "Open visualization")
	require.NotContains(t, response, "::codog-inline-vis")
}

func TestSystemPromptDocumentsVisualizationContract(t *testing.T) {
	app := &App{Workspace: t.TempDir()}
	prompt := app.systemPrompt()
	require.Contains(t, prompt, "<codog_visualizations>")
	require.Contains(t, prompt, ".codog/visualizations/")
	require.Contains(t, prompt, "::codog-inline-vis")
}

func TestResumedVisualizeSlashUsesLocalCommand(t *testing.T) {
	workspace := t.TempDir()
	sourceDir := filepath.Join(workspace, ".codog", "visualizations")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "view.html"), []byte("ok"), 0o644))
	var out bytes.Buffer
	app := &App{Workspace: workspace, Config: config.Config{ConfigHome: t.TempDir()}, Out: &out}

	err := app.runResumedSlashSessions(context.Background(), "/visualize", []string{"show", "view.html"}, config.FlagOverrides{}, "json")
	require.NoError(t, err)
	require.Contains(t, out.String(), `"kind": "visualization"`)
}
