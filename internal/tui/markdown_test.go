package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRenderAssistantMarkdownFormatsGFM(t *testing.T) {
	input := "# Heading\n\nUse **bold** and `code`.\n\n- first\n- second\n\n```go\nfmt.Println(\"ok\")\n```\n\n| Name | State |\n| --- | --- |\n| Codog | ready |"
	rendered := renderAssistantMarkdown(input, 72, stylesForTheme("dark"))
	plain := ansi.Strip(rendered)

	require.Contains(t, plain, "Heading")
	require.NotContains(t, plain, "# Heading")
	require.NotContains(t, plain, "Heading\n\n\n")
	require.Contains(t, plain, "Use bold")
	require.Contains(t, plain, "code")
	require.NotContains(t, plain, "**bold**")
	require.Contains(t, plain, "- first")
	require.Contains(t, plain, `fmt.Println("ok")`)
	require.NotContains(t, plain, "```")
	require.Contains(t, plain, "Codog")
	require.Contains(t, plain, "ready")
	require.Contains(t, rendered, "\x1b[")
	require.Less(t, strings.Count(rendered, "\x1b["), 100)
	for _, line := range strings.Split(plain, "\n") {
		require.Equal(t, strings.TrimRight(line, " \t"), line)
		require.LessOrEqual(t, ansi.StringWidth(line), 72, line)
	}
}

func TestRenderAssistantMarkdownHandlesStreamingFence(t *testing.T) {
	rendered := renderAssistantMarkdown("```go\nfunc main() {", 32, stylesForTheme("dark"))
	plain := ansi.Strip(rendered)

	require.Contains(t, plain, "func main() {")
	require.NotContains(t, plain, "```")
}

func TestRenderAssistantMarkdownNoColorAndWidth(t *testing.T) {
	rendered := renderAssistantMarkdown("## Narrow\n\n- a moderately long list item that must wrap cleanly", 24, stylesForTheme("no-color"))

	require.NotContains(t, rendered, "\x1b[")
	for _, line := range strings.Split(rendered, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 24, line)
	}
}

func TestRenderAssistantMarkdownWrapsWideCharacters(t *testing.T) {
	rendered := renderAssistantMarkdown("- 你好世界你好世界你好世界", 16, stylesForTheme("dark"))

	for _, line := range strings.Split(rendered, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 16, ansi.Strip(line))
	}
}

func TestRenderAssistantMarkdownPlainTextFastPath(t *testing.T) {
	rendered := renderAssistantMarkdown("plain response that wraps", 12, stylesForTheme("dark"))

	require.Equal(t, "plain\nresponse\nthat wraps", rendered)
	require.NotContains(t, rendered, "\x1b[")
}

func TestRenderTranscriptEntryUsesMarkdownForAssistantOnly(t *testing.T) {
	styles := stylesForTheme("dark")
	assistant := ansi.Strip(renderTranscriptEntry(transcriptEntry{Role: "assistant", Text: "**ready**"}, 40, 0, 1, false, styles))
	expanded := ansi.Strip(renderTranscriptEntry(transcriptEntry{Role: "assistant", Text: "# Expanded"}, 40, 0, 1, true, styles))
	user := ansi.Strip(renderTranscriptEntry(transcriptEntry{Role: "user", Text: "**literal**"}, 40, 0, 1, false, styles))

	require.Contains(t, assistant, "● ready")
	require.NotContains(t, assistant, "**ready**")
	require.Contains(t, expanded, "assistant · 1 line")
	require.Contains(t, expanded, "Expanded")
	require.NotContains(t, expanded, "# Expanded")
	require.Contains(t, user, "❯ **literal**")
}
