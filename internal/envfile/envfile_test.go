package envfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExtractsKeysAndHandlesCommentsQuotesAndExportPrefix(t *testing.T) {
	values := Parse(`
# ignored
ANTHROPIC_API_KEY=sk-ant-test
export OPENAI_API_KEY='single-quoted'
DASHSCOPE_API_KEY="double-quoted"
EMPTY=
NO_EQUALS
 =missing-key
`)

	require.Equal(t, "sk-ant-test", values["ANTHROPIC_API_KEY"])
	require.Equal(t, "single-quoted", values["OPENAI_API_KEY"])
	require.Equal(t, "double-quoted", values["DASHSCOPE_API_KEY"])
	require.Equal(t, "", values["EMPTY"])
	require.NotContains(t, values, "NO_EQUALS")
	require.NotContains(t, values, "")
}

func TestLookupPrefersNonEmptyEnvironmentOverDotenv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")

	value, ok := Lookup("OPENAI_API_KEY", map[string]string{"OPENAI_API_KEY": "from-dotenv"})

	require.True(t, ok)
	require.Equal(t, "from-env", value)
}

func TestLookupFallsBackToDotenvWhenEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	value, ok := Lookup("OPENAI_API_KEY", map[string]string{"OPENAI_API_KEY": "from-dotenv"})

	require.True(t, ok)
	require.Equal(t, "from-dotenv", value)
}

func TestCurrentLoadsDotenvFromWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("XAI_API_KEY=xai-dotenv\n"), 0o600))

	value, ok := LookupCurrent("XAI_API_KEY")

	require.True(t, ok)
	require.Equal(t, "xai-dotenv", value)
}
