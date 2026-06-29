package customcommands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Command struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Source  string `json:"source"`
	Preview string `json:"preview"`
	Body    string `json:"body,omitempty"`
}

type Rendered struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Source   string `json:"source"`
	Args     string `json:"args,omitempty"`
	Rendered string `json:"rendered"`
}

var ErrNotFound = errors.New("custom command not found")

type root struct {
	path   string
	source string
}

func Load(configHome, workspace string) ([]Command, error) {
	var commands []Command
	for _, root := range roots(configHome, workspace) {
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
			if !strings.HasSuffix(entry.Name(), ".md") {
				return nil
			}
			name, err := commandName(root.path, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body := string(data)
			commands = append(commands, Command{
				Name:    name,
				Path:    path,
				Source:  root.source,
				Preview: preview(body),
				Body:    body,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Name == commands[j].Name {
			return commands[i].Source < commands[j].Source
		}
		return commands[i].Name < commands[j].Name
	})
	return commands, nil
}

func Find(configHome, workspace, name string) (Command, error) {
	name = normalizeName(name)
	if name == "" {
		return Command{}, errors.New("command name is required")
	}
	for _, root := range rootsByPrecedence(configHome, workspace) {
		path := filepath.Join(root.path, commandPathName(name)+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Command{}, err
		}
		body := string(data)
		return Command{Name: name, Path: path, Source: root.source, Preview: preview(body), Body: body}, nil
	}
	return Command{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func Render(command Command, args string) Rendered {
	args = strings.TrimSpace(args)
	rendered := command.Body
	for _, marker := range []string{"$ARGUMENTS", "{{args}}", "{{ args }}", "{{ARGUMENTS}}", "{{ ARGUMENTS }}"} {
		rendered = strings.ReplaceAll(rendered, marker, args)
	}
	return Rendered{
		Name:     command.Name,
		Path:     command.Path,
		Source:   command.Source,
		Args:     args,
		Rendered: rendered,
	}
}

func roots(configHome, workspace string) []root {
	return []root{
		{filepath.Join(configHome, "commands"), "user"},
		{filepath.Join(workspace, ".claude", "commands"), "claude"},
		{filepath.Join(workspace, ".codog", "commands"), "workspace"},
	}
}

func rootsByPrecedence(configHome, workspace string) []root {
	return []root{
		{filepath.Join(workspace, ".codog", "commands"), "workspace"},
		{filepath.Join(workspace, ".claude", "commands"), "claude"},
		{filepath.Join(configHome, "commands"), "user"},
	}
}

func commandName(root string, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return strings.Join(parts, ":"), nil
}

func commandPathName(name string) string {
	return filepath.FromSlash(strings.ReplaceAll(name, ":", "/"))
}

func normalizeName(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	name = strings.TrimSuffix(name, ".md")
	name = strings.ReplaceAll(filepath.ToSlash(name), "/", ":")
	return name
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
