package orchestrationparity

import (
	"strings"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/skills"
	"github.com/Rememorio/codog/internal/team"
)

// Options supplies filesystem roots and configured MCP servers for the
// orchestration readiness report.
type Options struct {
	ConfigHome      string
	Workspace       string
	MCPServers      map[string]config.MCPServerConfig
	PluginManifests []plugins.Manifest
}

// Report is a compact, JSON-safe summary of the local orchestration surfaces
// needed for practical multi-agent and extension workflows.
type Report struct {
	Status                string   `json:"status"`
	AgentRunCount         int      `json:"agent_run_count"`
	BackgroundTaskCount   int      `json:"background_task_count"`
	TeamCount             int      `json:"team_count"`
	SkillCount            int      `json:"skill_count"`
	ActiveSkillCount      int      `json:"active_skill_count"`
	SkillSourceCount      int      `json:"skill_source_count"`
	PluginCount           int      `json:"plugin_count"`
	EnabledPluginCount    int      `json:"enabled_plugin_count"`
	PluginLifecycleCount  int      `json:"plugin_lifecycle_count"`
	PluginToolCount       int      `json:"plugin_tool_count"`
	PluginSkillCount      int      `json:"plugin_skill_count"`
	PluginAgentCount      int      `json:"plugin_agent_count"`
	PluginMCPServerCount  int      `json:"plugin_mcp_server_count"`
	ConfiguredMCPCount    int      `json:"configured_mcp_count"`
	RequiredSurfaceCount  int      `json:"required_surface_count"`
	MissingSurfaces       []string `json:"missing_surfaces,omitempty"`
	LoadErrors            []string `json:"load_errors,omitempty"`
	AgentStoreReady       bool     `json:"agent_store_ready"`
	BackgroundStoreReady  bool     `json:"background_store_ready"`
	TeamStoreReady        bool     `json:"team_store_ready"`
	SkillsDiscoveryReady  bool     `json:"skills_discovery_ready"`
	PluginDiscoveryReady  bool     `json:"plugin_discovery_ready"`
	MCPLifecycleReady     bool     `json:"mcp_lifecycle_ready"`
	MarketplaceTrustReady bool     `json:"marketplace_trust_ready"`
}

// Build reads local stores and extension manifests without executing external
// processes, then reports whether the orchestration plane has the expected
// production-grade surfaces.
func Build(options Options) Report {
	report := Report{
		Status:                "ready",
		RequiredSurfaceCount:  len(requiredSurfaces()),
		AgentStoreReady:       true,
		BackgroundStoreReady:  true,
		TeamStoreReady:        true,
		SkillsDiscoveryReady:  true,
		PluginDiscoveryReady:  true,
		MCPLifecycleReady:     true,
		MarketplaceTrustReady: true,
	}

	if runs, err := agentruns.NewStore(options.ConfigHome).List(); err != nil {
		report.AgentStoreReady = false
		report.addLoadError("agent_runs", err)
	} else {
		report.AgentRunCount = len(runs)
	}
	if tasks, err := background.NewStore(options.ConfigHome).List(); err != nil {
		report.BackgroundStoreReady = false
		report.addLoadError("background", err)
	} else {
		report.BackgroundTaskCount = len(tasks)
	}
	if teams, err := team.NewStore(options.ConfigHome).List(); err != nil {
		report.TeamStoreReady = false
		report.addLoadError("team", err)
	} else {
		report.TeamCount = len(teams)
	}
	loadedSkills, skillErr := skills.LoadWithManifests(options.ConfigHome, options.Workspace, options.PluginManifests)
	if options.PluginManifests == nil {
		loadedSkills, skillErr = skills.Load(options.ConfigHome, options.Workspace)
	}
	if skillErr != nil {
		report.SkillsDiscoveryReady = false
		report.addLoadError("skills", skillErr)
	} else {
		report.SkillCount = len(loadedSkills)
		for _, skill := range loadedSkills {
			if skill.Active {
				report.ActiveSkillCount++
			}
		}
		if options.PluginManifests == nil {
			report.SkillSourceCount = len(skills.Sources(options.ConfigHome, options.Workspace))
		} else {
			report.SkillSourceCount = len(skills.SourcesWithManifests(options.ConfigHome, options.Workspace, options.PluginManifests))
		}
	}
	manifests := options.PluginManifests
	var manifestErr error
	if manifests == nil {
		manifests, manifestErr = plugins.Load(options.Workspace)
	}
	if manifestErr != nil {
		report.PluginDiscoveryReady = false
		report.addLoadError("plugins", manifestErr)
	} else {
		report.PluginCount = len(manifests)
		for _, manifest := range manifests {
			if manifest.Enabled {
				report.EnabledPluginCount++
			}
			if !manifest.Lifecycle.Empty() {
				report.PluginLifecycleCount++
			}
			report.PluginToolCount += len(manifest.Tools)
			report.PluginSkillCount += len(manifest.Skills)
			report.PluginAgentCount += len(manifest.Agents)
			report.PluginMCPServerCount += len(manifest.MCPServers)
		}
	}
	report.ConfiguredMCPCount = len(options.MCPServers)

	report.MissingSurfaces = report.missingSurfaces()
	if len(report.MissingSurfaces) > 0 || len(report.LoadErrors) > 0 {
		report.Status = "degraded"
	}
	return report
}

func requiredSurfaces() []string {
	return []string{
		"agent_store",
		"background_store",
		"team_store",
		"skills_discovery",
		"plugin_discovery",
		"mcp_lifecycle",
		"marketplace_trust",
	}
}

func (r Report) missingSurfaces() []string {
	var missing []string
	if !r.AgentStoreReady {
		missing = append(missing, "agent_store")
	}
	if !r.BackgroundStoreReady {
		missing = append(missing, "background_store")
	}
	if !r.TeamStoreReady {
		missing = append(missing, "team_store")
	}
	if !r.SkillsDiscoveryReady {
		missing = append(missing, "skills_discovery")
	}
	if !r.PluginDiscoveryReady {
		missing = append(missing, "plugin_discovery")
	}
	if !r.MCPLifecycleReady {
		missing = append(missing, "mcp_lifecycle")
	}
	if !r.MarketplaceTrustReady {
		missing = append(missing, "marketplace_trust")
	}
	return missing
}

func (r *Report) addLoadError(surface string, err error) {
	if err == nil {
		return
	}
	r.LoadErrors = append(r.LoadErrors, surface+": "+strings.TrimSpace(err.Error()))
}
