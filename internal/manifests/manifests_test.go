package manifests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestBuildResolverManifest(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "review.md"), []byte("Review body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "agents", "helper.json"), []byte(`{"description":"helper","prompt":"help"}`), 0o644))

	report, err := Build(workspace, configHome, tools.NewRegistry(workspace))
	require.NoError(t, err)

	require.Equal(t, "dump-manifests", report.Kind)
	require.Equal(t, "go-resolver", report.Source)
	require.Greater(t, report.Commands, 0)
	require.Greater(t, report.Tools, 0)
	require.Equal(t, 1, report.Agents)
	require.Equal(t, 1, report.Skills)
	require.Greater(t, report.BootstrapPhases, 0)
	require.Contains(t, manifestCommandNames(report.CommandManifests), "/help")
	require.Contains(t, manifestToolNames(report.ToolManifests), "bash")
	require.Equal(t, "helper", report.AgentManifests[0].Name)
	require.Equal(t, "review", report.SkillManifests[0].Name)
}

func manifestCommandNames(entries []CommandManifest) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func manifestToolNames(entries []tools.ToolInfo) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}
