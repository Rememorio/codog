package sessionsummary

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/stretchr/testify/require"
)

func TestBuildSessionSummary(t *testing.T) {
	report := Build("session-1", "/tmp/session.jsonl", "claude-haiku", []anthropic.Message{
		anthropic.TextMessage("user", "first prompt"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type:  "tool_use",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			}},
		},
		anthropic.ToolResultMessage("tool-1", "failed", true),
		anthropic.TextMessage("user", "second prompt\nwith detail"),
		anthropic.TextMessage("assistant", "final answer"),
	})

	require.Equal(t, "summary", report.Kind)
	require.Equal(t, "session-1", report.SessionID)
	require.Equal(t, 5, report.MessageCount)
	require.Equal(t, 3, report.UserMessages)
	require.Equal(t, 2, report.AssistantMessages)
	require.Equal(t, 1, report.ToolUses)
	require.Equal(t, 1, report.ToolResults)
	require.Equal(t, 1, report.ToolErrors)
	require.NotZero(t, report.TokenEstimate.TotalTokens)
	require.Equal(t, "first prompt", report.FirstUser.Text)
	require.Equal(t, "second prompt with detail", report.LastUser.Text)
	require.Equal(t, "final answer", report.LastAssistant.Text)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Summary")
	require.Contains(t, out.String(), "Session          session-1")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=1")
}
