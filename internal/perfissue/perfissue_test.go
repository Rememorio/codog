package perfissue

import (
	"bytes"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/insights"
	"github.com/stretchr/testify/require"
)

func TestBuildSignalsAndRenderers(t *testing.T) {
	source := insights.Report{
		Kind:     "insights",
		Sessions: 1,
		Messages: 3,
		Prompts:  1,
		ToolUses: 3,
		Usage:    insights.TokenTotals{Input: 50, Output: 20, CacheCreation: 6, CacheRead: 0},
		TopTools: []insights.ToolCount{{Name: "read_file", Count: 3}},
		RecentSessions: []insights.SessionSummary{{
			ID:       "session-1",
			Messages: 3,
			Prompts:  1,
			ToolUses: 3,
			Usage:    insights.TokenTotals{Input: 50, Output: 20, CacheCreation: 6},
		}},
	}
	report := Build(source, Options{
		Workspace:      "repo",
		TokenThreshold: 40,
		ToolThreshold:  2,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})

	require.Equal(t, "perf_issue", report.Kind)
	require.Equal(t, "warn", report.Status)
	require.Equal(t, 76, report.TotalTokens)
	require.Contains(t, signalKinds(report.Signals), "high_token_usage")
	require.Contains(t, signalKinds(report.Signals), "high_tool_use")
	require.Contains(t, signalKinds(report.Signals), "cache_not_reused")

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Performance Issue")
	require.Contains(t, out.String(), "high_token_usage")
	require.Contains(t, out.String(), "read_file")

	markdown := RenderMarkdown(report)
	require.Contains(t, markdown, "# Codog Performance Issue")
	require.Contains(t, markdown, "high_tool_use")
	require.Contains(t, markdown, "Raw Data")
}

func TestBuildEmpty(t *testing.T) {
	report := Build(insights.Report{Kind: "insights"}, Options{})
	require.Equal(t, "empty", report.Status)
	require.Equal(t, "no_sessions", report.Signals[0].Kind)
}

func signalKinds(signals []Signal) []string {
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.Kind)
	}
	return kinds
}
