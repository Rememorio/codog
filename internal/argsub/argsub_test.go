package argsub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseShellLikeArguments(t *testing.T) {
	require.Equal(t, []string{"foo", "hello world", "bar baz", "$HOME"}, Parse(`foo "hello world" 'bar baz' $HOME`))
	require.Equal(t, []string{"unterminated", `"quote`}, Parse(`unterminated "quote`))
}

func TestSubstituteClaudeStyleArguments(t *testing.T) {
	content := "file=$file focus=$focus first=$0 second=$ARGUMENTS[1] all=$ARGUMENTS missing=$missing keep=$filename braces={{ args }}"
	rendered := Substitute(content, `main.go "race condition"`, false, []string{"file", "focus"})

	require.Equal(t, `file=main.go focus=race condition first=main.go second=race condition all=main.go "race condition" missing=$missing keep=$filename braces=main.go "race condition"`, rendered)
}

func TestSubstituteAppendsArgumentsWhenRequested(t *testing.T) {
	require.Equal(t, "Review this\n\nARGUMENTS: file.go", Substitute("Review this", "file.go", true, nil))
	require.Equal(t, "Review this", Substitute("Review this", "", true, nil))
}

func TestSubstituteVariables(t *testing.T) {
	rendered := SubstituteVariables("root=${CLAUDE_PLUGIN_ROOT} missing=${UNKNOWN}", map[string]string{
		"CLAUDE_PLUGIN_ROOT": "/tmp/plugin",
	})
	require.Equal(t, "root=/tmp/plugin missing=${UNKNOWN}", rendered)

	values := SubstituteVariablesInList([]string{"Bash(${CLAUDE_PLUGIN_ROOT}/bin/*)", "Read"}, map[string]string{
		"CLAUDE_PLUGIN_ROOT": "/tmp/plugin",
	})
	require.Equal(t, []string{"Bash(/tmp/plugin/bin/*)", "Read"}, values)
}
