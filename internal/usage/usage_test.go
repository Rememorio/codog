package usage

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/stretchr/testify/require"
)

func TestBuildReportGroupsRolesBlocksAndToolUse(t *testing.T) {
	report := BuildReport("session-1", "claude-haiku", []anthropic.Message{
		anthropic.TextMessage("user", "hello world"),
		{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type:  "tool_use",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"README.md"}`),
			}},
		},
		anthropic.ToolResultMessage("tool-1", "failed", true),
	})

	require.Equal(t, "usage", report.Kind)
	require.Equal(t, "session-1", report.SessionID)
	require.Equal(t, 3, report.MessageCount)
	require.Equal(t, 1, report.ToolUse.ToolUses)
	require.Equal(t, 1, report.ToolUse.ToolResults)
	require.Equal(t, 1, report.ToolUse.Errors)
	require.NotZero(t, report.Summary.TotalTokens)
	require.Contains(t, report.Roles, RoleUsage{Role: "assistant", Messages: 1, Tokens: report.Roles[0].Tokens})
	requireBlock(t, report.Blocks, "text", 1)
	requireBlock(t, report.Blocks, "tool_result", 1)
	requireBlock(t, report.Blocks, "tool_use", 1)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Usage")
	require.Contains(t, out.String(), "Session          session-1")
	require.Contains(t, out.String(), "Tool use         calls=1 results=1 errors=1")
}

func requireBlock(t *testing.T, blocks []BlockUsage, blockType string, count int) {
	t.Helper()
	for _, block := range blocks {
		if block.Type == blockType {
			require.Equal(t, count, block.Count)
			return
		}
	}
	require.Failf(t, "missing block", "expected block %s in %#v", blockType, blocks)
}
