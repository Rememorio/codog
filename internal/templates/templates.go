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
	Name    string `json:"name"`
	Path    string `json:"path"`
	Source  string `json:"source"`
	Preview string `json:"preview"`
	Body    string `json:"body,omitempty"`
}

type Rendered struct {
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Source     string            `json:"source"`
	Vars       map[string]string `json:"vars,omitempty"`
	Rendered   string            `json:"rendered"`
	Unresolved []string          `json:"unresolved,omitempty"`
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
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
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
