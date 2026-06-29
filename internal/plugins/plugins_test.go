package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPluginManifest(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"Demo","version":"0.1.0","commands":["demo"],"tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`), 0o644))

	manifests, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	require.Equal(t, "demo", manifests[0].ID)
	require.Equal(t, "Demo", manifests[0].Name)
	require.Equal(t, []string{"demo"}, manifests[0].Commands)
	require.Len(t, manifests[0].Tools, 1)
	require.Equal(t, "demo_tool", manifests[0].Tools[0].Name)
	require.Equal(t, "cat", manifests[0].Tools[0].Command)
}
