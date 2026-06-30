package insights

import (
	"bytes"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestBuildSummarizesSessionsPromptsToolsAndUsage(t *testing.T) {
	store := session.NewWorkspaceStore(t.TempDir(), t.TempDir())
	first, err := store.Open("session-a")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(first.ID, "inspect the repo"))
	require.NoError(t, store.Append(first.ID, anthropic.TextMessage("user", "inspect the repo")))
	require.NoError(t, store.AppendWithUsage(first.ID, anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "bash",
			ID:    "tool-1",
			Input: []byte(`{"command":"go test ./..."}`),
		}},
	}, &anthropic.Usage{InputTokens: 10, OutputTokens: 3, CacheReadInputTokens: 2}))

	second, err := store.Open("session-b")
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(second.ID, "write tests"))
	require.NoError(t, store.Append(second.ID, anthropic.TextMessage("user", "write tests")))
	require.NoError(t, store.AppendWithUsage(second.ID, anthropic.Message{
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type:  "tool_use",
			Name:  "edit_file",
			ID:    "tool-2",
			Input: []byte(`{"path":"main.go"}`),
		}},
	}, &anthropic.Usage{InputTokens: 7, OutputTokens: 5, CacheCreationInputTokens: 1}))

	report, err := Build(store, Options{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, "insights", report.Kind)
	require.Equal(t, 2, report.Sessions)
	require.Equal(t, 4, report.Messages)
	require.Equal(t, 2, report.Prompts)
	require.Equal(t, 2, report.ToolUses)
	require.Equal(t, 17, report.Usage.Input)
	require.Equal(t, 8, report.Usage.Output)
	require.Equal(t, 1, report.Usage.CacheCreation)
	require.Equal(t, 2, report.Usage.CacheRead)
	require.Len(t, report.TopTools, 2)
	require.Len(t, report.RecentSessions, 2)
	require.Len(t, report.RecentPrompts, 2)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Insights")
	require.Contains(t, out.String(), "Tool uses")
	require.Contains(t, out.String(), "Recent prompts")

	out.Reset()
	require.NoError(t, RenderJSON(&out, report))
	require.Contains(t, out.String(), `"kind": "insights"`)
}
