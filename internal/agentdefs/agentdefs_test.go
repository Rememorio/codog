package agentdefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAgentDefinitions(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".codog", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewer.json"), []byte(`{"description":"reviews code","tools":["grep"]}`), 0o644))
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"demo","name":"demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "agents", "helper.json"), []byte(`{"description":"plugin helper","tools":["read_file"]}`), 0o644))

	defs, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, defs, 2)
	require.Equal(t, "demo:helper", defs[0].Name)
	require.Equal(t, "plugin:demo", defs[0].Source)
	require.Equal(t, "demo", defs[0].Plugin)
	require.Equal(t, []string{"read_file"}, defs[0].Tools)
	require.Equal(t, "reviewer", defs[1].Name)
	require.Equal(t, "workspace", defs[1].Source)
	require.Equal(t, []string{"grep"}, defs[1].Tools)
}
