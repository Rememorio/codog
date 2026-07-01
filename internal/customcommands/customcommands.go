package customcommands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rememorio/codog/internal/argsub"
	"github.com/Rememorio/codog/internal/frontmatter"
	"github.com/Rememorio/codog/internal/plugins"
)

type Command struct {
	Name             string   `json:"name"`
	Path             string   `json:"path"`
	Source           string   `json:"source"`
	PluginRoot       string   `json:"plugin_root,omitempty"`
	PluginData       string   `json:"plugin_data,omitempty"`
	Description      string   `json:"description,omitempty"`
	AllowedTools     []string `json:"allowed_tools,omitempty"`
	ArgumentHint     string   `json:"argument_hint,omitempty"`
	Arguments        []string `json:"arguments,omitempty"`
	FrontmatterError string   `json:"frontmatter_error,omitempty"`
	Preview          string   `json:"preview"`
	Body             string   `json:"body,omitempty"`
	Active           bool     `json:"active"`
	ShadowedBy       string   `json:"shadowed_by,omitempty"`
	ShadowedByPath   string   `json:"shadowed_by_path,omitempty"`
}

type DiscoveryRoot struct {
	Source     string `json:"source"`
	Label      string `json:"label"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	PluginID   string `json:"plugin_id,omitempty"`
	PluginRoot string `json:"plugin_root,omitempty"`
}

type Rendered struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Source       string   `json:"source"`
	PluginRoot   string   `json:"plugin_root,omitempty"`
	PluginData   string   `json:"plugin_data,omitempty"`
	Description  string   `json:"description,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	ArgumentHint string   `json:"argument_hint,omitempty"`
	Arguments    []string `json:"arguments,omitempty"`
	Args         string   `json:"args,omitempty"`
	Rendered     string   `json:"rendered"`
}

var ErrNotFound = errors.New("custom command not found")

type root struct {
	path       string
	source     string
	prefix     string
	pluginRoot string
	pluginData string
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
			commands = append(commands, parseCommandDocument(name, path, root, string(data)))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if strings.EqualFold(commands[i].Name, commands[j].Name) {
			leftRank := sourceRank(commands[i].Source)
			rightRank := sourceRank(commands[j].Source)
			if leftRank == rightRank {
				return commands[i].Path < commands[j].Path
			}
			return leftRank < rightRank
		}
		return strings.ToLower(commands[i].Name) < strings.ToLower(commands[j].Name)
	})
	annotateActiveCommands(commands)
	return commands, nil
}

func Sources(configHome, workspace string) []DiscoveryRoot {
	out := []DiscoveryRoot{}
	for _, root := range roots(configHome, workspace) {
		out = append(out, discoveryRoot(root))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sourceRank(out[i].Source) == sourceRank(out[j].Source) {
			return out[i].Path < out[j].Path
		}
		return sourceRank(out[i].Source) < sourceRank(out[j].Source)
	})
	return out
}

func annotateActiveCommands(commands []Command) {
	winners := map[string]int{}
	for index := range commands {
		key := strings.ToLower(strings.TrimSpace(commands[index].Name))
		if key == "" {
			commands[index].Active = false
			continue
		}
		winnerIndex, ok := winners[key]
		if !ok {
			winners[key] = index
			commands[index].Active = true
			continue
		}
		winner := commands[winnerIndex]
		commands[index].Active = false
		commands[index].ShadowedBy = winner.Source
		commands[index].ShadowedByPath = winner.Path
	}
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
		return parseCommandDocument(name, path, root, string(data)), nil
	}
	return Command{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func Render(command Command, args string) Rendered {
	return RenderWithSession(command, args, "")
}

func RenderWithSession(command Command, args string, sessionID string) Rendered {
	args = strings.TrimSpace(args)
	rendered := argsub.Substitute(command.Body, args, true, command.Arguments)
	rendered = argsub.SubstituteVariables(rendered, commandVariablesWithSession(command, sessionID))
	return Rendered{
		Name:         command.Name,
		Path:         command.Path,
		Source:       command.Source,
		PluginRoot:   command.PluginRoot,
		PluginData:   command.PluginData,
		Description:  command.Description,
		AllowedTools: append([]string(nil), command.AllowedTools...),
		ArgumentHint: command.ArgumentHint,
		Arguments:    append([]string(nil), command.Arguments...),
		Args:         args,
		Rendered:     rendered,
	}
}

func parseCommandDocument(name string, path string, root root, text string) Command {
	body, values, parseErr := frontmatter.Parse(text)
	command := Command{
		Name:       name,
		Path:       path,
		Source:     root.source,
		PluginRoot: normalizedPathVariable(root.pluginRoot),
		PluginData: normalizedPathVariable(root.pluginData),
		Body:       body,
		Active:     true,
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
	command.AllowedTools = argsub.SubstituteVariablesInList(command.AllowedTools, commandVariables(command))
	if command.Description == "" {
		command.Description = frontmatter.DescriptionFromMarkdown(command.Body)
	}
	command.Preview = command.Description
	if command.Preview == "" {
		command.Preview = preview(command.Body)
	}
	return command
}

func commandVariables(command Command) map[string]string {
	return commandVariablesWithSession(command, "")
}

func commandVariablesWithSession(command Command, sessionID string) map[string]string {
	variables := map[string]string{}
	if command.PluginRoot != "" {
		variables["CLAUDE_PLUGIN_ROOT"] = command.PluginRoot
	}
	if command.PluginData != "" {
		variables["CLAUDE_PLUGIN_DATA"] = command.PluginData
	}
	if strings.TrimSpace(sessionID) != "" {
		variables["CLAUDE_SESSION_ID"] = strings.TrimSpace(sessionID)
	}
	return variables
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
		out = append(out, commandRootsForPlugin(manifest)...)
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
		base = append(base, commandRootsForPlugin(manifest)...)
	}
	return base
}

func discoveryRoot(root root) DiscoveryRoot {
	exists := false
	if root.path != "" {
		if _, err := os.Stat(root.path); err == nil {
			exists = true
		}
	}
	return DiscoveryRoot{
		Source:     root.source,
		Label:      sourceLabel(root.source),
		Path:       root.path,
		Exists:     exists,
		PluginID:   pluginIDFromSource(root.source),
		PluginRoot: root.pluginRoot,
	}
}

func sourceLabel(source string) string {
	switch {
	case source == "user":
		return "User commands"
	case source == "workspace":
		return "Workspace commands"
	case source == "claude":
		return "Claude-compatible workspace commands"
	case strings.HasPrefix(source, "plugin:"):
		return "Plugin commands"
	default:
		return source
	}
}

func pluginIDFromSource(source string) string {
	id, ok := strings.CutPrefix(source, "plugin:")
	if !ok {
		return ""
	}
	return id
}

func sourceRank(source string) int {
	switch {
	case source == "workspace":
		return 0
	case source == "claude":
		return 1
	case source == "user":
		return 2
	case strings.HasPrefix(source, "plugin:"):
		return 3
	default:
		return 4
	}
}

func commandRootsForPlugin(manifest plugins.Manifest) []root {
	if !manifest.Enabled {
		return nil
	}
	out := []root{{
		path:       filepath.Join(manifest.Root, "commands"),
		source:     "plugin:" + manifest.ID,
		prefix:     manifest.ID,
		pluginRoot: manifest.Root,
		pluginData: plugins.DataDirForManifest(manifest),
	}}
	seen := map[string]bool{filepath.Clean(out[0].path): true}
	for _, spec := range manifest.Commands {
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
			if !strings.EqualFold(filepath.Ext(path), ".md") {
				continue
			}
			rootPath = filepath.Dir(path)
		}
		key := filepath.Clean(rootPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, root{path: rootPath, source: "plugin:" + manifest.ID, prefix: manifest.ID, pluginRoot: manifest.Root, pluginData: plugins.DataDirForManifest(manifest)})
	}
	return out
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

func normalizedPathVariable(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(path)
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
