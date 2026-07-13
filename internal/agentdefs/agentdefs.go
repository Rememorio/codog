package agentdefs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/frontmatter"
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
	Format      string   `json:"format,omitempty"`
}

// AcceptedFormats returns the agent definition file extensions Codog loads.
func AcceptedFormats() []string {
	return []string{".json", ".md", ".markdown"}
}

type root struct {
	path   string
	source string
	prefix string
}

func Load(workspace string) ([]Definition, error) {
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return nil, err
	}
	return LoadWithManifests(workspace, manifests)
}

// LoadWithManifests loads file and plugin definitions from a resolved runtime
// plugin set.
func LoadWithManifests(workspace string, manifests []plugins.Manifest) ([]Definition, error) {
	var defs []Definition
	for _, root := range rootsWithManifests(workspace, manifests) {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !supportedAgentFile(entry.Name()) {
				continue
			}
			path := filepath.Join(root.path, entry.Name())
			def, err := loadDefinitionFile(path)
			if err != nil {
				return nil, err
			}
			if def.Name == "" {
				def.Name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
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

// ParseInline parses the JSON object accepted by the --agents CLI flag.
func ParseInline(raw string) ([]Definition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed := map[string]Definition{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("--agents must be a JSON object: %w", err)
	}
	definitions := make([]Definition, 0, len(parsed))
	for name, definition := range parsed {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("--agents contains an empty agent name")
		}
		definition.Name = name
		definition.Description = strings.TrimSpace(definition.Description)
		definition.Prompt = strings.TrimSpace(definition.Prompt)
		if definition.Description == "" {
			return nil, fmt.Errorf("--agents definition %q requires description", name)
		}
		if definition.Prompt == "" {
			return nil, fmt.Errorf("--agents definition %q requires prompt", name)
		}
		definition.Path = "--agents"
		definition.Source = "cli"
		definition.Format = "json"
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return strings.ToLower(definitions[i].Name) < strings.ToLower(definitions[j].Name)
	})
	return definitions, nil
}

// Merge overlays definitions by case-insensitive name, with later definitions
// taking precedence.
func Merge(base []Definition, overlays ...[]Definition) []Definition {
	merged := append([]Definition(nil), base...)
	index := map[string]int{}
	for i, definition := range merged {
		index[strings.ToLower(strings.TrimSpace(definition.Name))] = i
	}
	for _, definitions := range overlays {
		for _, definition := range definitions {
			key := strings.ToLower(strings.TrimSpace(definition.Name))
			if i, exists := index[key]; exists {
				merged[i] = definition
				continue
			}
			index[key] = len(merged)
			merged = append(merged, definition)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged
}

func roots(workspace string) []root {
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return rootsWithManifests(workspace, nil)
	}
	return rootsWithManifests(workspace, manifests)
}

func rootsWithManifests(workspace string, manifests []plugins.Manifest) []root {
	out := []root{
		{path: filepath.Join(workspace, ".codog", "agents"), source: "workspace"},
		{path: filepath.Join(workspace, ".claude", "agents"), source: "claude"},
		{path: filepath.Join(workspace, ".claw", "agents"), source: "claw"},
		{path: filepath.Join(workspace, ".omc", "agents"), source: "omc"},
	}
	for _, manifest := range manifests {
		out = append(out, agentRootsForPlugin(manifest)...)
	}
	return out
}

func agentRootsForPlugin(manifest plugins.Manifest) []root {
	if !manifest.Enabled {
		return nil
	}
	out := []root{{
		path:   filepath.Join(manifest.Root, "agents"),
		source: "plugin:" + manifest.ID,
		prefix: manifest.ID,
	}}
	seen := map[string]bool{filepath.Clean(out[0].path): true}
	for _, spec := range manifest.Agents {
		path, err := plugins.ResolveContentPath(manifest.Root, spec)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		rootPath := path
		if !info.IsDir() {
			if !supportedAgentFile(path) {
				continue
			}
			rootPath = filepath.Dir(path)
		}
		key := filepath.Clean(rootPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, root{path: rootPath, source: "plugin:" + manifest.ID, prefix: manifest.ID})
	}
	return out
}

func supportedAgentFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".json" || ext == ".md" || ext == ".markdown"
}

func loadDefinitionFile(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return parseMarkdownDefinition(string(data), path)
	default:
		var def Definition
		if err := json.Unmarshal(data, &def); err != nil {
			return Definition{}, err
		}
		def.Format = "json"
		return def, nil
	}
}

func parseMarkdownDefinition(text string, path string) (Definition, error) {
	body, values, err := frontmatter.Parse(text)
	if err != nil {
		return Definition{}, err
	}
	def := Definition{
		Name:        frontmatter.String(values, "name"),
		Description: frontmatter.FirstString(values, "description", "summary"),
		Model:       frontmatter.String(values, "model"),
		Tools:       frontmatter.StringList(values["tools"]),
		Prompt:      strings.TrimSpace(body),
		Format:      "markdown",
	}
	if def.Description == "" {
		def.Description = frontmatter.DescriptionFromMarkdown(body)
	}
	if def.Name == "" {
		def.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return def, nil
}

func namespacePluginName(prefix string, name string) string {
	prefix = strings.TrimSpace(prefix)
	name = strings.TrimSpace(name)
	if prefix == "" || name == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)+":") {
		return name
	}
	return prefix + ":" + name
}
