package agentdefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/plugins"
)

type Definition struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Path        string   `json:"path,omitempty"`
	Source      string   `json:"source,omitempty"`
	Plugin      string   `json:"plugin,omitempty"`
}

type root struct {
	path   string
	source string
	prefix string
}

func Load(workspace string) ([]Definition, error) {
	var defs []Definition
	for _, root := range roots(workspace) {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(root.path, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var def Definition
			if err := json.Unmarshal(data, &def); err != nil {
				return nil, err
			}
			if def.Name == "" {
				def.Name = strings.TrimSuffix(entry.Name(), ".json")
			}
			def.Name = namespacePluginName(root.prefix, def.Name)
			def.Path = path
			def.Source = root.source
			if root.prefix != "" {
				def.Plugin = root.prefix
			}
			defs = append(defs, def)
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func roots(workspace string) []root {
	out := []root{{path: filepath.Join(workspace, ".codog", "agents"), source: "workspace"}}
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return out
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		out = append(out, root{
			path:   filepath.Join(manifest.Root, "agents"),
			source: "plugin:" + manifest.ID,
			prefix: manifest.ID,
		})
	}
	return out
}

func namespacePluginName(prefix string, name string) string {
	prefix = strings.TrimSpace(prefix)
	name = strings.TrimSpace(name)
	if prefix == "" || name == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)+":") {
		return name
	}
	return prefix + ":" + name
}
