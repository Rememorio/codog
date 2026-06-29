package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`
	Path        string   `json:"path,omitempty"`
}

func Load(workspace string) ([]Manifest, error) {
	root := filepath.Join(workspace, ".codog", "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Manifest{}, nil
		}
		return nil, err
	}
	manifests := []Manifest{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "plugin.json")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, err
		}
		if manifest.ID == "" {
			manifest.ID = entry.Name()
		}
		if manifest.Name == "" {
			manifest.Name = manifest.ID
		}
		manifest.Path = path
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	return manifests, nil
}
