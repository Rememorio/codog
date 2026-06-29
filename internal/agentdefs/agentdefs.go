package agentdefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Definition struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Path        string   `json:"path,omitempty"`
}

func Load(workspace string) ([]Definition, error) {
	dir := filepath.Join(workspace, ".codog", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Definition{}, nil
		}
		return nil, err
	}
	defs := []Definition{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
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
		def.Path = path
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}
