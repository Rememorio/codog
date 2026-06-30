package hookenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAndMergeHookEnvironment(t *testing.T) {
	parsed := Parse(`
# ignored
export CODOG_ALPHA=one
CODOG_BETA="two words"
BAD-NAME=skip
export CODOG_GAMMA='three words'
`)
	require.Equal(t, []string{
		"CODOG_ALPHA=one",
		"CODOG_BETA=two words",
		"CODOG_GAMMA=three words",
	}, parsed)

	merged := Merge([]string{"PATH=/bin", "CODOG_ALPHA=old"}, parsed)
	require.Equal(t, []string{
		"PATH=/bin",
		"CODOG_ALPHA=one",
		"CODOG_BETA=two words",
		"CODOG_GAMMA=three words",
	}, merged)
}

func TestLoadSortsHookEnvironmentByEventPriority(t *testing.T) {
	configHome := t.TempDir()
	sessionID := "session-1"
	dir := Dir(configHome, sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "filechanged-hook-2.sh"), []byte("CODOG_ORDER=file\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "setup-hook-1.sh"), []byte("CODOG_ORDER=setup\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessionstart-hook-1.sh"), []byte("CODOG_SESSION=ready\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.sh"), []byte("CODOG_IGNORED=yes\n"), 0o600))

	loaded, err := Load(configHome, sessionID)
	require.NoError(t, err)
	require.Equal(t, []string{
		"CODOG_ORDER=setup",
		"CODOG_SESSION=ready",
		"CODOG_ORDER=file",
	}, loaded)
	require.Equal(t, []string{"CODOG_ORDER=file", "CODOG_SESSION=ready"}, Merge(nil, loaded))
}

func TestPathUsesSessionScopedDeterministicHookFile(t *testing.T) {
	configHome := t.TempDir()
	path, err := Path(configHome, "session/one", "session_start", 3)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(configHome, "session-env", "session_one", "sessionstart-hook-3.sh"), path)
	require.DirExists(t, filepath.Dir(path))

	path, err = Path(configHome, "session/one", "post_tool_use", 0)
	require.NoError(t, err)
	require.Empty(t, path)
}
