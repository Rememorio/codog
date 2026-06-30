package plugins

import (
	"strings"

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
			out[PluginMCPServerName(manifest.ID, name)] = server
		}
	}
	return out, nil
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
