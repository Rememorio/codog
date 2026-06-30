package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Rememorio/codog/internal/config"
)

type HookConfigFile struct {
	PluginID string            `json:"plugin_id"`
	Path     string            `json:"path"`
	Config   config.HookConfig `json:"config"`
}

func LoadHookConfigs(workspace string) ([]HookConfigFile, error) {
	manifests, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	var out []HookConfigFile
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, path := range hookConfigPaths(manifest) {
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			var cfg config.HookConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("plugin %q hook config %s: %w", manifest.ID, path, err)
			}
			out = append(out, HookConfigFile{PluginID: manifest.ID, Path: path, Config: cfg})
		}
	}
	return out, nil
}

func hookConfigPaths(manifest Manifest) []string {
	paths := []string{filepath.Join(manifest.Root, "hooks", "hooks.json")}
	seen := map[string]bool{filepath.Clean(paths[0]): true}
	for _, spec := range manifest.Hooks {
		path, err := ResolveContentPath(manifest.Root, spec)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			path = filepath.Join(path, "hooks.json")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Clean(path)
		}
		key := filepath.Clean(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		paths = append(paths, path)
	}
	return paths
}
