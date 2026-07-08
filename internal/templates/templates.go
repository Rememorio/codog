package templates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Template struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Source         string `json:"source"`
	Preview        string `json:"preview"`
	Body           string `json:"body,omitempty"`
	Active         bool   `json:"active"`
	ShadowedBy     string `json:"shadowed_by,omitempty"`
	ShadowedByPath string `json:"shadowed_by_path,omitempty"`
}

type Rendered struct {
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Source     string            `json:"source"`
	Vars       map[string]string `json:"vars,omitempty"`
	Rendered   string            `json:"rendered"`
	Unresolved []string          `json:"unresolved,omitempty"`
}

type DiscoveryRoot struct {
	Source string `json:"source"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

var variableRe = regexp.MustCompile(`\{\{\s*\.?([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func Load(configHome, workspace string) ([]Template, error) {
	roots := roots(configHome, workspace)
	var out []Template
	for _, root := range roots {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(root.path, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			body := string(data)
			out = append(out, Template{
				Name:    strings.TrimSuffix(entry.Name(), ".md"),
				Path:    path,
				Source:  root.source,
				Preview: preview(body),
				Body:    body,
				Active:  true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			leftRank := sourceRank(out[i].Source)
			rightRank := sourceRank(out[j].Source)
			if leftRank == rightRank {
				return out[i].Path < out[j].Path
			}
			return leftRank < rightRank
		}
		return out[i].Name < out[j].Name
	})
	annotateActiveTemplates(out)
	return out, nil
}

func Sources(configHome, workspace string) []DiscoveryRoot {
	out := []DiscoveryRoot{}
	for _, root := range roots(configHome, workspace) {
		_, err := os.Stat(root.path)
		out = append(out, DiscoveryRoot{
			Source: root.source,
			Label:  root.source + " templates",
			Path:   root.path,
			Exists: err == nil,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sourceRank(out[i].Source) == sourceRank(out[j].Source) {
			return out[i].Path < out[j].Path
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

func annotateActiveTemplates(all []Template) {
	winners := map[string]int{}
	for index := range all {
		key := strings.ToLower(strings.TrimSpace(all[index].Name))
		if key == "" {
			all[index].Active = false
			continue
		}
		winnerIndex, ok := winners[key]
		if !ok {
			winners[key] = index
			all[index].Active = true
			continue
		}
		winner := all[winnerIndex]
		all[index].Active = false
		all[index].ShadowedBy = winner.Source
		all[index].ShadowedByPath = winner.Path
	}
}

func Find(configHome, workspace, name string) (Template, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Template{}, errors.New("template name is required")
	}
	for _, root := range rootsByPrecedence(configHome, workspace) {
		path := filepath.Join(root.path, name+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Template{}, err
		}
		body := string(data)
		return Template{
			Name:    name,
			Path:    path,
			Source:  root.source,
			Preview: preview(body),
			Body:    body,
			Active:  true,
		}, nil
	}
	return Template{}, fmt.Errorf("template %q not found", name)
}

func Render(template Template, vars map[string]string) (Rendered, error) {
	if vars == nil {
		vars = map[string]string{}
	}
	missingSet := map[string]struct{}{}
	rendered := variableRe.ReplaceAllStringFunc(template.Body, func(match string) string {
		parts := variableRe.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, ok := vars[parts[1]]
		if !ok {
			missingSet[parts[1]] = struct{}{}
			return match
		}
		return value
	})
	missing := make([]string, 0, len(missingSet))
	for name := range missingSet {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	result := Rendered{
		Name:       template.Name,
		Path:       template.Path,
		Source:     template.Source,
		Vars:       cloneMap(vars),
		Rendered:   rendered,
		Unresolved: missing,
	}
	if len(missing) != 0 {
		return result, fmt.Errorf("missing template variables: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

type root struct {
	path   string
	source string
}

func roots(configHome, workspace string) []root {
	return []root{
		{filepath.Join(configHome, "templates"), "user"},
		{filepath.Join(workspace, ".codog", "templates"), "workspace"},
	}
}

func rootsByPrecedence(configHome, workspace string) []root {
	return []root{
		{filepath.Join(workspace, ".codog", "templates"), "workspace"},
		{filepath.Join(configHome, "templates"), "user"},
	}
}

func sourceRank(source string) int {
	switch source {
	case "workspace":
		return 0
	case "user":
		return 10
	default:
		return 100
	}
}

func preview(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "<empty>"
}

func cloneMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
