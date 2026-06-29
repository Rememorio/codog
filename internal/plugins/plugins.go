package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type Manifest struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Tools       []ToolManifest `json:"tools,omitempty"`
	Commands    []string       `json:"commands,omitempty"`
	Hooks       []string       `json:"hooks,omitempty"`
	Path        string         `json:"path,omitempty"`
	Root        string         `json:"root,omitempty"`
}

type ToolManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Command     string         `json:"command,omitempty"`
	Args        []string       `json:"args,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	Permission  string         `json:"permission,omitempty"`
}

type rawManifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	Commands    []string          `json:"commands,omitempty"`
	Hooks       []string          `json:"hooks,omitempty"`
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
		var raw rawManifest
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		manifest := Manifest{
			ID:          raw.ID,
			Name:        raw.Name,
			Version:     raw.Version,
			Description: raw.Description,
			Commands:    raw.Commands,
			Hooks:       raw.Hooks,
		}
		for _, rawTool := range raw.Tools {
			var name string
			if err := json.Unmarshal(rawTool, &name); err == nil {
				manifest.Tools = append(manifest.Tools, ToolManifest{Name: name})
				continue
			}
			var tool ToolManifest
			if err := json.Unmarshal(rawTool, &tool); err != nil {
				return nil, err
			}
			manifest.Tools = append(manifest.Tools, tool)
		}
		if manifest.ID == "" {
			manifest.ID = entry.Name()
		}
		if manifest.Name == "" {
			manifest.Name = manifest.ID
		}
		manifest.Path = path
		manifest.Root = filepath.Dir(path)
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	return manifests, nil
}
