package sessionsummary

import (
	"bytes"
	"encoding/json"
	"strings"
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

func TestCompressTextKeepsCoreLinesAndReportsOmissions(t *testing.T) {
	summary := strings.Join([]string{
		"Conversation summary:",
		"",
		"- Scope:   compact   earlier   messages.",
		"- Scope: compact earlier messages.",
		"- Current work: finish summary compression.",
		"- Key timeline:",
		"  - user: asked for a working implementation.",
		"  - assistant: inspected runtime compaction flow.",
		"  - tool: go test ./... succeeded.",
	}, "\n")

	result := CompressText(summary, CompressionBudget{
		MaxChars:     132,
		MaxLines:     4,
		MaxLineChars: 80,
	})

	require.Equal(t, 1, result.RemovedDuplicateLines)
	require.Greater(t, result.OmittedLines, 0)
	require.True(t, result.Truncated)
	require.Contains(t, result.Summary, "Conversation summary:")
	require.Contains(t, result.Summary, "- Scope: compact earlier messages.")
	require.Contains(t, result.Summary, "- Current work: finish summary compression.")
	require.NotContains(t, result.Summary, "  compact   earlier")
}

func TestBuildCompactionSummaryIncludesActionableContext(t *testing.T) {
	result := BuildCompactionSummary([]anthropic.Message{
		anthropic.TextMessage("user", "investigate failing tests"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type:  "tool_use",
				ID:    "tool-1",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"go test ./..."}`),
			}},
		},
		anthropic.ToolResultMessage("tool-1", "package failed", true),
		anthropic.TextMessage("assistant", "The failure is in internal/runloop."),
	}, 2)

	require.Contains(t, result.Summary, "auto-compacted")
	require.Contains(t, result.Summary, "- Current work: investigate failing tests")
	require.Contains(t, result.Summary, "- Last assistant response: The failure is in internal/runloop.")
	require.Contains(t, result.Summary, "- Tools mentioned: bash")
	require.Contains(t, result.Summary, "- Tool results: 1 result message(s), 1 error result(s).")
}
