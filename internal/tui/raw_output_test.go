package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRenderRawTranscriptEntryIsPlainAndLogical(t *testing.T) {
	rendered := renderRawTranscriptEntry(transcriptEntry{
		Role: "assistant",
		Text: "\x1b[31m# Heading\x1b[0m\r\n\r\n**bold**",
	})
	require.Equal(t, "assistant\n# Heading\n\n**bold**", rendered)
	require.Equal(t, rendered, ansi.Strip(rendered))
}

func TestRawOutputViewportIncludesPrintedHistory(t *testing.T) {
	m := newModel(context.Background(), newPromptTextarea(""), nil, []transcriptEntry{
		{Role: "welcome"},
		{Role: "assistant", Text: "**first**"},
		{Role: "user", Text: "second"},
	})
	m.inline = true
	m.printedEntries = len(m.transcript)
	m.rawOutput = true
	m.refreshViewport()

	content := m.viewport.View()
	require.Contains(t, content, "**first**")
	require.Contains(t, content, "second")
	require.NotContains(t, content, "\x1b[")
	require.NotContains(t, content, "welcome")
	require.True(t, strings.Contains(content, "assistant"))
}
