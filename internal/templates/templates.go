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

type InstallReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Target string `json:"target"`
}

type UninstallReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
}

type SourceNotFoundError struct {
	Source string
	Err    error
}

func (e SourceNotFoundError) Error() string {
	return fmt.Sprintf("template source %q was not found", e.Source)
}

func (e SourceNotFoundError) Unwrap() error {
	return e.Err
}

var ErrNotFound = errors.New("template not found")

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
	return Template{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func Install(source string, targetRoot string, explicitName string, targetLabel string) (InstallReport, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return InstallReport{}, errors.New("template install source is required")
	}
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		return InstallReport{}, errors.New("template install target is required")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return InstallReport{}, err
	}
	resolvedSource, err := filepath.EvalSymlinks(absSource)
	if err != nil {
		return InstallReport{}, SourceNotFoundError{Source: source, Err: err}
	}
	info, err := os.Stat(resolvedSource)
	if err != nil {
		if os.IsNotExist(err) {
			return InstallReport{}, SourceNotFoundError{Source: source, Err: err}
		}
		return InstallReport{}, err
	}
	if info.IsDir() || !strings.EqualFold(filepath.Ext(resolvedSource), ".md") {
		return InstallReport{}, fmt.Errorf("template source %q must be a markdown file", source)
	}
	name := strings.TrimSpace(explicitName)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(resolvedSource), filepath.Ext(resolvedSource))
	}
	if err := validateTemplateName(name); err != nil {
		return InstallReport{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return InstallReport{}, err
	}
	dest := filepath.Join(targetRoot, name+".md")
	if err := copyFile(resolvedSource, dest, 0o644); err != nil {
		return InstallReport{}, err
	}
	return InstallReport{
		Kind:   "templates",
		Action: "install",
		Status: "ok",
		Name:   name,
		Source: resolvedSource,
		Path:   dest,
		Target: targetLabel,
	}, nil
}

func Uninstall(name string, roots []string) (UninstallReport, error) {
	name = strings.TrimSpace(strings.TrimSuffix(name, ".md"))
	if name == "" {
		return UninstallReport{}, errors.New("template name is required")
	}
	if err := validateTemplateName(name); err != nil {
		return UninstallReport{}, err
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, name+".md")
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return UninstallReport{}, err
		}
		if err := os.Remove(candidate); err != nil {
			return UninstallReport{}, err
		}
		return UninstallReport{
			Kind:    "templates",
			Action:  "uninstall",
			Status:  "ok",
			Name:    name,
			Path:    candidate,
			Removed: true,
		}, nil
	}
	return UninstallReport{}, fmt.Errorf("%w: %s", ErrNotFound, name)
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

func validateTemplateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\:`) || name == "." {
		return fmt.Errorf("invalid template name %q", name)
	}
	return nil
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

func copyFile(source string, dest string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, mode)
}
