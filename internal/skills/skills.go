package skills

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
	Source string `json:"source"`
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

var ErrNotFound = errors.New("skill not found")

func Load(configHome, workspace string) ([]Skill, error) {
	roots := []struct {
		path   string
		source string
	}{
		{filepath.Join(configHome, "skills"), "user"},
		{filepath.Join(workspace, ".codog", "skills"), "workspace"},
		{filepath.Join(workspace, ".claude", "skills"), "claude"},
	}
	var out []Skill
	for _, root := range roots {
		if _, err := os.Stat(root.path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !isSkillFile(path, entry.Name()) {
				return nil
			}
			name, err := skillName(root.path, path)
			if err != nil {
				return err
			}
			if name == "" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out = append(out, Skill{
				Name:   name,
				Path:   path,
				Body:   string(data),
				Source: root.source,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func Find(configHome, workspace, name string) (Skill, error) {
	all, err := Load(configHome, workspace)
	if err != nil {
		return Skill{}, err
	}
	for _, skill := range all {
		if strings.EqualFold(skill.Name, name) {
			return skill, nil
		}
	}
	return Skill{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func Install(source string, targetRoot string, explicitName string, targetLabel string) (InstallReport, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return InstallReport{}, errors.New("skill install source is required")
	}
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		return InstallReport{}, errors.New("skill install target is required")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return InstallReport{}, err
	}
	resolvedSource, err := filepath.EvalSymlinks(absSource)
	if err != nil {
		return InstallReport{}, fmt.Errorf("skill source %q not found: %w", source, err)
	}
	info, err := os.Stat(resolvedSource)
	if err != nil {
		return InstallReport{}, err
	}
	name := strings.TrimSpace(explicitName)
	if name == "" {
		name = defaultInstallName(resolvedSource, info)
	}
	if err := validateSkillName(name); err != nil {
		return InstallReport{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return InstallReport{}, err
	}
	targetName := skillNamePath(name)
	var dest string
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(resolvedSource, "SKILL.md")); err != nil {
			return InstallReport{}, fmt.Errorf("skill directory %q must contain SKILL.md", source)
		}
		dest = filepath.Join(targetRoot, targetName)
		if err := os.RemoveAll(dest); err != nil {
			return InstallReport{}, err
		}
		if err := copyDir(resolvedSource, dest); err != nil {
			return InstallReport{}, err
		}
	} else {
		if !strings.EqualFold(filepath.Ext(resolvedSource), ".md") {
			return InstallReport{}, fmt.Errorf("skill source %q must be a markdown file or directory with SKILL.md", source)
		}
		dest = filepath.Join(targetRoot, targetName+".md")
		if err := copyFile(resolvedSource, dest, 0o644); err != nil {
			return InstallReport{}, err
		}
	}
	return InstallReport{
		Kind:   "skills",
		Action: "install",
		Status: "ok",
		Name:   name,
		Source: resolvedSource,
		Path:   dest,
		Target: targetLabel,
	}, nil
}

func Uninstall(name string, roots []string) (UninstallReport, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return UninstallReport{}, errors.New("skill name is required")
	}
	if err := validateSkillName(name); err != nil {
		return UninstallReport{}, err
	}
	pathName := skillNamePath(name)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidates := []string{
			filepath.Join(root, pathName+".md"),
			filepath.Join(root, pathName),
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return UninstallReport{}, err
			}
			if err := os.RemoveAll(candidate); err != nil {
				return UninstallReport{}, err
			}
			return UninstallReport{
				Kind:    "skills",
				Action:  "uninstall",
				Status:  "ok",
				Name:    name,
				Path:    candidate,
				Removed: true,
			}, nil
		}
	}
	return UninstallReport{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func RenderInvocation(skill Skill, args string) string {
	args = strings.TrimSpace(args)
	var builder strings.Builder
	builder.WriteString("Use the following Codog skill for this request.\n\n")
	builder.WriteString("<skill name=\"")
	builder.WriteString(escapeAttr(skill.Name))
	builder.WriteString("\" source=\"")
	builder.WriteString(escapeAttr(skill.Source))
	builder.WriteString("\" path=\"")
	builder.WriteString(escapeAttr(skill.Path))
	builder.WriteString("\">\n")
	builder.WriteString(strings.TrimSpace(skill.Body))
	builder.WriteString("\n</skill>\n\n")
	if args == "" {
		builder.WriteString("User request: apply this skill.")
	} else {
		builder.WriteString("User request: ")
		builder.WriteString(args)
	}
	return builder.String()
}

func defaultInstallName(source string, info os.FileInfo) string {
	if info.IsDir() {
		return filepath.Base(source)
	}
	return strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
}

func validateSkillName(name string) error {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	for _, part := range strings.Split(name, ":") {
		if strings.TrimSpace(part) == "" || part == "." {
			return fmt.Errorf("invalid skill name %q", name)
		}
	}
	return nil
}

func skillNamePath(name string) string {
	return filepath.FromSlash(strings.ReplaceAll(name, ":", "/"))
}

func copyDir(source string, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
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

func isSkillFile(path string, name string) bool {
	if name == "SKILL.md" {
		return true
	}
	return strings.HasSuffix(name, ".md") && filepath.Base(filepath.Dir(path)) != ""
}

func skillName(root string, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if pathpkg.Base(rel) == "SKILL.md" {
		dir := strings.TrimSuffix(pathpkg.Dir(rel), ".")
		return strings.ReplaceAll(dir, "/", ":"), nil
	}
	rel = strings.TrimSuffix(rel, ".md")
	return strings.ReplaceAll(rel, "/", ":"), nil
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
