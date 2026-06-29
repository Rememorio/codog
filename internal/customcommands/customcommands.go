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

type root struct {
	path   string
	source string
}

func Load(configHome, workspace string) ([]Command, error) {
	var commands []Command
	for _, root := range roots(configHome, workspace) {
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
			commands = append(commands, Command{
				Name:    strings.TrimSuffix(entry.Name(), ".md"),
				Path:    path,
				Source:  root.source,
				Preview: preview(body),
				Body:    body,
			})
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
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	if name == "" {
		return Command{}, errors.New("command name is required")
	}
	for _, root := range rootsByPrecedence(configHome, workspace) {
		path := filepath.Join(root.path, name+".md")
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
	return Command{}, fmt.Errorf("custom command %q not found", name)
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

func preview(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "<empty>"
}
