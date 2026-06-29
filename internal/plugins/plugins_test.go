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
	require.True(t, manifests[0].Enabled)
}

func TestInstallDisableEnableRemovePlugin(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"demo","name":"Demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "tool.sh"), []byte("echo ok\n"), 0o755))

	installed, err := Install(workspace, source)
	require.NoError(t, err)
	require.Equal(t, "demo", installed.ID)
	require.True(t, installed.Enabled)
	require.FileExists(t, filepath.Join(workspace, ".codog", "plugins", "demo", "tool.sh"))

	disabled, err := Disable(workspace, "demo")
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.FileExists(t, filepath.Join(disabled.Root, DisabledMarker))

	enabled, err := Enable(workspace, "demo")
	require.NoError(t, err)
	require.True(t, enabled.Enabled)
	require.NoFileExists(t, filepath.Join(enabled.Root, DisabledMarker))

	require.NoError(t, Remove(workspace, "demo"))
	require.NoDirExists(t, filepath.Join(workspace, ".codog", "plugins", "demo"))
}

func TestInstallRejectsUnsafePluginID(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"../bad","name":"Bad"}`), 0o644))

	_, err := Install(workspace, source)
	require.Error(t, err)
	require.Contains(t, err.Error(), "single path component")
}
