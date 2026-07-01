package plugins

import (
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/argsub"
	"github.com/Rememorio/codog/internal/config"
)

func LoadMCPServers(workspace string) (map[string]config.MCPServerConfig, error) {
	manifests, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	out := map[string]config.MCPServerConfig{}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for name, server := range manifest.MCPServers {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			out[PluginMCPServerName(manifest.ID, name)] = resolvePluginMCPServer(manifest, server)
		}
	}
	return out, nil
}

func resolvePluginMCPServer(manifest Manifest, server config.MCPServerConfig) config.MCPServerConfig {
	variables := pluginVariables(manifest)
	server.Command = argsub.SubstituteVariables(server.Command, variables)
	server.Args = argsub.SubstituteVariablesInList(server.Args, variables)
	env := []string{
		"CLAUDE_PLUGIN_ROOT=" + variables["CLAUDE_PLUGIN_ROOT"],
		"CLAUDE_PLUGIN_DATA=" + variables["CLAUDE_PLUGIN_DATA"],
	}
	env = append(env, argsub.SubstituteVariablesInList(server.Env, variables)...)
	server.Env = compactEnv(env)
	return server
}

func pluginVariables(manifest Manifest) map[string]string {
	return map[string]string{
		"CLAUDE_PLUGIN_ROOT": filepath.ToSlash(manifest.Root),
		"CLAUDE_PLUGIN_DATA": filepath.ToSlash(DataDirForManifest(manifest)),
	}
}

func compactEnv(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func PluginMCPServerName(pluginID string, serverName string) string {
	pluginID = strings.TrimSpace(pluginID)
	serverName = strings.TrimSpace(serverName)
	if pluginID == "" {
		pluginID = "plugin"
	}
	if strings.HasPrefix(serverName, "plugin:") {
		return serverName
	}
	return "plugin:" + pluginID + ":" + serverName
}
