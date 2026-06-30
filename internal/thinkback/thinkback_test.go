package thinkback

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestWriteBuildsLocalYearInReviewHTML(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	sess, err := store.Open("source")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(sess.ID, "review <repo>"))
	require.NoError(t, store.Append(sess.ID, anthropic.TextMessage("user", "review <repo>")))
	require.NoError(t, store.AppendWithUsage(sess.ID, anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type: "tool_use",
			Name: "bash",
			ID:   "tool-1",
		}},
	}, &anthropic.Usage{InputTokens: 12, OutputTokens: 5}))

	report, err := Write(store, Options{Workspace: workspace, Year: 2026, Limit: 3})
	require.NoError(t, err)
	require.Equal(t, "think_back", report.Kind)
	require.Equal(t, 2026, report.Year)
	require.True(t, report.Written)
	require.Equal(t, filepath.Join(workspace, ".codog", "think-back-2026.html"), report.Output)
	require.Equal(t, 1, report.Insights.Sessions)
	require.Equal(t, 1, report.Insights.Prompts)
	require.Equal(t, 1, report.Insights.ToolUses)

	data, err := os.ReadFile(report.Output)
	require.NoError(t, err)
	require.Contains(t, string(data), "Codog Think Back 2026")
	require.Contains(t, string(data), "bash")
	require.Contains(t, string(data), "review &lt;repo&gt;")

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Think Back")
	out.Reset()
	require.NoError(t, RenderJSON(&out, report))
	require.Contains(t, out.String(), `"kind": "think_back"`)
}
