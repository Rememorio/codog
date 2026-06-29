package prompthistory

import (
	"bytes"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestBuildLimitsToMostRecentEntries(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	report := Build("session-1", []session.PromptEntry{
		{Index: 1, Time: now, Text: "first", SessionID: "session-1"},
		{Index: 2, Time: now.Add(time.Minute), Text: "second", SessionID: "session-1"},
		{Index: 3, Time: now.Add(2 * time.Minute), Text: "third", SessionID: "session-1"},
	}, 2)

	require.Equal(t, "prompt_history", report.Kind)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 3, report.Total)
	require.Equal(t, 2, report.Showing)
	require.Len(t, report.Entries, 2)
	require.Equal(t, 2, report.Entries[0].Index)
	require.Equal(t, "second", report.Entries[0].Preview)
	require.Equal(t, 3, report.Entries[1].Index)
}

func TestRenderTextForEmptyHistory(t *testing.T) {
	var out bytes.Buffer
	RenderText(&out, Build("", nil, 20))

	require.Contains(t, out.String(), "Prompt history")
	require.Contains(t, out.String(), "no prompts recorded yet")
}

func TestPreviewUsesFirstLineAndTruncates(t *testing.T) {
	text := "\n\n  abcdefghijklmnopqrstuvwxyz  \nsecond"

	require.Equal(t, "abcdefg...", Preview(text, 10))
	require.Equal(t, "abc", Preview(text, 3))
}
