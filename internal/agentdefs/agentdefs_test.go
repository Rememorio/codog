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

	defs, err := Load(workspace)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.Equal(t, "reviewer", defs[0].Name)
	require.Equal(t, []string{"grep"}, defs[0].Tools)
}
