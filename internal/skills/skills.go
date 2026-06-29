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
