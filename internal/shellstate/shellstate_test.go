package shellstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentCWDDefaultsAndPersistsPerSession(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	physicalWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	physicalNested, err := filepath.EvalSymlinks(nested)
	require.NoError(t, err)

	cwd, err := CurrentCWD(configHome, "session/one", workspace)
	require.NoError(t, err)
	require.Equal(t, physicalWorkspace, cwd)

	saved, err := SaveCWD(configHome, "session/one", nested)
	require.NoError(t, err)
	require.Equal(t, physicalNested, saved)

	cwd, err = CurrentCWD(configHome, "session/one", workspace)
	require.NoError(t, err)
	require.Equal(t, physicalNested, cwd)
	require.FileExists(t, filepath.Join(configHome, "session-state", "session_one", "cwd"))
}

func TestCurrentCWDFallsBackForInvalidSavedPath(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	physicalWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(CWDPath(configHome, "session")), 0o700))
	require.NoError(t, os.WriteFile(CWDPath(configHome, "session"), []byte(filepath.Join(workspace, "missing")+"\n"), 0o600))

	cwd, err := CurrentCWD(configHome, "session", workspace)
	require.NoError(t, err)
	require.Equal(t, physicalWorkspace, cwd)
}
