package orchestrationparity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/plugins"
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

func TestBuildCountsSessionPluginSurfaces(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	pluginDir := filepath.Join(t.TempDir(), "session")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "skills", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
  "id": "session",
  "tools": [{"name": "session_check", "command": "echo"}],
  "skills": ["skills"]
}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "skills", "audit", "SKILL.md"), []byte("---\nname: audit\ndescription: Session audit\n---\nAudit.\n"), 0o644))

	manifests, err := plugins.LoadWithDirs(workspace, []string{pluginDir})
	require.NoError(t, err)
	report := Build(Options{
		ConfigHome:      configHome,
		Workspace:       workspace,
		PluginManifests: manifests,
	})

	require.Equal(t, "ready", report.Status)
	require.Equal(t, 1, report.PluginCount)
	require.Equal(t, 1, report.EnabledPluginCount)
	require.Equal(t, 1, report.PluginToolCount)
	require.Equal(t, 1, report.PluginSkillCount)
	require.Greater(t, report.ActiveSkillCount, 0)
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
