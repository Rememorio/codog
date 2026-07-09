package orchestrationparity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildReportsReadyForEmptyLocalStores(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()

	report := Build(Options{
		ConfigHome: configHome,
		Workspace:  workspace,
		MCPServers: map[string]config.MCPServerConfig{},
	})

	require.Equal(t, "ready", report.Status)
	require.True(t, report.AgentStoreReady)
	require.True(t, report.BackgroundStoreReady)
	require.True(t, report.TeamStoreReady)
	require.True(t, report.SkillsDiscoveryReady)
	require.True(t, report.PluginDiscoveryReady)
	require.True(t, report.MCPLifecycleReady)
	require.True(t, report.MarketplaceTrustReady)
	require.Greater(t, report.SkillCount, 0)
	require.Equal(t, report.SkillCount, report.ActiveSkillCount)
	require.Empty(t, report.MissingSurfaces)
	require.Empty(t, report.LoadErrors)
}

func TestBuildCountsPluginSurfaces(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	pluginDir := filepath.Join(workspace, ".codog", "plugins", "ops")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
  "id": "ops",
  "name": "Ops",
  "version": "1.0.0",
  "lifecycle": {"init": ["echo init"]},
  "tools": [{"name": "ops_check", "command": "echo"}],
  "skills": ["skills/deploy.md"],
  "agents": ["agents/reviewer.md"],
  "mcp_servers": {"ops": {"command": "ops-mcp"}}
}`), 0o644))

	report := Build(Options{
		ConfigHome: configHome,
		Workspace:  workspace,
		MCPServers: map[string]config.MCPServerConfig{"local": {Command: "mcp"}},
	})

	require.Equal(t, "ready", report.Status)
	require.Equal(t, 1, report.PluginCount)
	require.Equal(t, 1, report.EnabledPluginCount)
	require.Equal(t, 1, report.PluginLifecycleCount)
	require.Equal(t, 1, report.PluginToolCount)
	require.Equal(t, 1, report.PluginSkillCount)
	require.Equal(t, 1, report.PluginAgentCount)
	require.Equal(t, 1, report.PluginMCPServerCount)
	require.Equal(t, 1, report.ConfiguredMCPCount)
}

func TestBuildReportsDegradedForBrokenStores(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "background"), []byte("not a dir"), 0o644))

	report := Build(Options{
		ConfigHome: configHome,
		Workspace:  workspace,
	})

	require.Equal(t, "degraded", report.Status)
	require.False(t, report.BackgroundStoreReady)
	require.True(t, report.MCPLifecycleReady)
	require.Contains(t, report.MissingSurfaces, "background_store")
	require.NotEmpty(t, report.LoadErrors)
}
