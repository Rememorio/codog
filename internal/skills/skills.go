package skills

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	Name   string
	Path   string
	Body   string
	Source string
}

func Load(configHome, workspace string) ([]Skill, error) {
	roots := []struct {
		path   string
		source string
	}{
		{filepath.Join(configHome, "skills"), "user"},
		{filepath.Join(workspace, ".codog", "skills"), "workspace"},
	}
	var out []Skill
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
			out = append(out, Skill{
				Name:   strings.TrimSuffix(entry.Name(), ".md"),
				Path:   path,
				Body:   string(data),
				Source: root.source,
			})
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
	return Skill{}, errors.New("skill not found")
}
