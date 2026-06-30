package customcommands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/frontmatter"
	"github.com/Rememorio/codog/internal/plugins"
)

type Command struct {
	Name             string   `json:"name"`
	Path             string   `json:"path"`
	Source           string   `json:"source"`
	Description      string   `json:"description,omitempty"`
	AllowedTools     []string `json:"allowed_tools,omitempty"`
	ArgumentHint     string   `json:"argument_hint,omitempty"`
	Arguments        []string `json:"arguments,omitempty"`
	FrontmatterError string   `json:"frontmatter_error,omitempty"`
	Preview          string   `json:"preview"`
	Body             string   `json:"body,omitempty"`
}

type Rendered struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Source       string   `json:"source"`
	Description  string   `json:"description,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	ArgumentHint string   `json:"argument_hint,omitempty"`
	Arguments    []string `json:"arguments,omitempty"`
	Args         string   `json:"args,omitempty"`
	Rendered     string   `json:"rendered"`
}

var ErrNotFound = errors.New("custom command not found")

type root struct {
	path   string
	source string
	prefix string
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
			name, err := commandName(root, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			commands = append(commands, parseCommandDocument(name, path, root.source, string(data)))
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
		rootName, ok := commandNameForRoot(root, name)
		if !ok {
			continue
		}
		path := filepath.Join(root.path, commandPathName(rootName)+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Command{}, err
		}
		return parseCommandDocument(name, path, root.source, string(data)), nil
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
		Name:         command.Name,
		Path:         command.Path,
		Source:       command.Source,
		Description:  command.Description,
		AllowedTools: append([]string(nil), command.AllowedTools...),
		ArgumentHint: command.ArgumentHint,
		Arguments:    append([]string(nil), command.Arguments...),
		Args:         args,
		Rendered:     rendered,
	}
}

func parseCommandDocument(name string, path string, source string, text string) Command {
	body, values, parseErr := frontmatter.Parse(text)
	command := Command{
		Name:   name,
		Path:   path,
		Source: source,
		Body:   body,
	}
	if parseErr != nil {
		command.FrontmatterError = parseErr.Error()
	}
	if len(values) > 0 {
		command.Description = frontmatter.String(values, "description")
		command.AllowedTools = frontmatter.StringList(values["allowed-tools"])
		command.ArgumentHint = frontmatter.String(values, "argument-hint")
		command.Arguments = frontmatter.ArgumentList(values["arguments"])
	}
	if command.Description == "" {
		command.Description = frontmatter.DescriptionFromMarkdown(command.Body)
	}
	command.Preview = command.Description
	if command.Preview == "" {
		command.Preview = preview(command.Body)
	}
	return command
}

func roots(configHome, workspace string) []root {
	out := []root{
		{path: filepath.Join(configHome, "commands"), source: "user"},
		{path: filepath.Join(workspace, ".claude", "commands"), source: "claude"},
		{path: filepath.Join(workspace, ".codog", "commands"), source: "workspace"},
	}
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return out
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		out = append(out, root{
			path:   filepath.Join(manifest.Root, "commands"),
			source: "plugin:" + manifest.ID,
			prefix: manifest.ID,
		})
	}
	return out
}

func rootsByPrecedence(configHome, workspace string) []root {
	base := []root{
		{path: filepath.Join(workspace, ".codog", "commands"), source: "workspace"},
		{path: filepath.Join(workspace, ".claude", "commands"), source: "claude"},
		{path: filepath.Join(configHome, "commands"), source: "user"},
	}
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return base
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		base = append(base, root{
			path:   filepath.Join(manifest.Root, "commands"),
			source: "plugin:" + manifest.ID,
			prefix: manifest.ID,
		})
	}
	return base
}

func commandName(root root, path string) (string, error) {
	rel, err := filepath.Rel(root.path, path)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return namespacePluginName(root.prefix, strings.Join(parts, ":")), nil
}

func commandNameForRoot(root root, name string) (string, bool) {
	if root.prefix == "" {
		return name, true
	}
	prefix := strings.ToLower(root.prefix) + ":"
	if !strings.HasPrefix(strings.ToLower(name), prefix) {
		return "", false
	}
	return name[len(root.prefix)+1:], true
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

func namespacePluginName(prefix string, name string) string {
	prefix = strings.TrimSpace(prefix)
	name = strings.TrimSpace(name)
	if prefix == "" || name == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)+":") {
		return name
	}
	return prefix + ":" + name
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
