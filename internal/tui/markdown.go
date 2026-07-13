package tui

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
)

var markdownSyntaxPattern = regexp.MustCompile(`[#*` + "`" + `|\[>_~]|\n\n|(?m)^\s*(?:[-+] |\d+\. )`)

func renderAssistantMarkdown(text string, width int, themed themeStyles) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if !hasMarkdownSyntax(text) {
		return wrapTranscriptText(text, width)
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyles(themed)),
		glamour.WithWordWrap(max(8, width)),
		glamour.WithTableWrap(true),
		glamour.WithInlineTableLinks(true),
	)
	if err != nil {
		return wrapTranscriptText(text, width)
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return wrapTranscriptText(text, width)
	}
	return trimMarkdownPadding(rendered)
}

func hasMarkdownSyntax(text string) bool {
	const sampleLimit = 500
	if len(text) > sampleLimit {
		text = text[:sampleLimit]
	}
	return markdownSyntaxPattern.MatchString(text)
}

func markdownStyles(themed themeStyles) ansi.StyleConfig {
	if themed.palette.noColor {
		style := glamourstyles.ASCIIStyleConfig
		style.Document.Margin = markdownPointer(uint(0))
		style.CodeBlock.Margin = markdownPointer(uint(0))
		return style
	}

	style := glamourstyles.DarkStyleConfig
	if !themeBackgroundIsDark(themed) {
		style = glamourstyles.LightStyleConfig
	}
	zero := uint(0)
	style.Document.Margin = &zero
	style.Document.Color = nil
	style.CodeBlock.Margin = &zero
	style.CodeBlock.Color = nil
	style.Heading.Color = markdownPointer(themed.palette.accent)
	style.Heading.BlockSuffix = "\n"
	style.H1.Prefix = ""
	style.H1.Suffix = ""
	style.H1.Color = markdownPointer(themed.palette.accent)
	style.H1.BackgroundColor = nil
	style.H1.Bold = markdownPointer(true)
	style.H1.Italic = markdownPointer(true)
	style.H1.Underline = markdownPointer(true)
	for _, heading := range []*ansi.StyleBlock{&style.H2, &style.H3, &style.H4, &style.H5, &style.H6} {
		heading.Prefix = ""
		heading.Suffix = ""
		heading.Color = markdownPointer(themed.palette.accent)
		heading.Bold = markdownPointer(true)
	}
	style.Item.BlockPrefix = "- "
	style.Link.Color = markdownPointer(themed.palette.accent)
	style.LinkText.Color = markdownPointer(themed.palette.accent)
	style.Code.Color = markdownPointer(themed.palette.assistant)
	style.Code.BackgroundColor = markdownPointer(themed.palette.statusBackground)
	style.HorizontalRule.Color = markdownPointer(themed.palette.muted)
	return style
}

func themeBackgroundIsDark(themed themeStyles) bool {
	switch themed.palette.statusBackground {
	case "7", "254":
		return false
	default:
		return true
	}
}

func trimMarkdownPadding(rendered string) string {
	lines := strings.Split(strings.Trim(rendered, "\n"), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func markdownPointer[T any](value T) *T {
	return &value
}
