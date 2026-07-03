package frontmatter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExtractsFrontmatter(t *testing.T) {
	body, values, err := Parse("\ufeff---\r\nname: Build\r\npaths:\r\n  - src/**\r\n  - docs\r\n---\r\n# Title\r\nBody")
	require.NoError(t, err)
	require.Equal(t, "# Title\r\nBody", body)
	require.Equal(t, "Build", String(values, "name"))
	require.Equal(t, []string{"src", "docs"}, NormalizePaths(StringList(values["paths"])))
}

func TestParseWithoutCompleteFrontmatterReturnsOriginalText(t *testing.T) {
	text := "---\nname: incomplete\nbody"
	body, values, err := Parse(text)
	require.NoError(t, err)
	require.Nil(t, values)
	require.Equal(t, text, body)
}

func TestParseInvalidFrontmatterReturnsBodyAndError(t *testing.T) {
	body, values, err := Parse("---\nname: [broken\n---\nBody")
	require.Error(t, err)
	require.Nil(t, values)
	require.Equal(t, "Body", body)
}

func TestFrontmatterValueHelpers(t *testing.T) {
	values := map[string]any{
		"primary":  "",
		"fallback": " value ",
		"tools":    []any{"bash, read", "write\nedit", "bash"},
		"args":     " --foo, bar\nbaz ",
		"enabled":  "YES",
		"unknown":  "maybe",
	}

	require.Equal(t, "value", FirstString(values, "primary", "fallback"))
	require.Equal(t, []string{"bash", "read", "write", "edit"}, StringList(values["tools"]))
	require.Equal(t, []string{"--foo", "bar", "baz"}, ArgumentList(values["args"]))
	require.Equal(t, []string{"a", "b"}, CompactStrings([]string{" a ", "", "b", "a"}))

	enabled, ok := Bool(values["enabled"])
	require.True(t, ok)
	require.True(t, enabled)

	_, ok = Bool(values["unknown"])
	require.False(t, ok)
}

func TestDescriptionFromMarkdown(t *testing.T) {
	require.Equal(t, "Title", DescriptionFromMarkdown("\n\n## Title\nbody"))
	require.Empty(t, DescriptionFromMarkdown("\n\t\n"))
}
