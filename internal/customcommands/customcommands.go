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
	"github.com/Rememorio/codog/internal/projectscope"
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
	return fmt.Sprintf("custom command source %q was not found", e.Source)
}

func (e SourceNotFoundError) Unwrap() error {
	return e.Err
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
	manifests, _ := plugins.Load(workspace)
	return LoadWithManifests(configHome, workspace, manifests)
}

// LoadWithManifests loads commands from a resolved runtime plugin set.
func LoadWithManifests(configHome, workspace string, manifests []plugins.Manifest) ([]Command, error) {
	var commands []Command
	for _, root := range rootsWithManifests(configHome, workspace, manifests) {
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
	manifests, _ := plugins.Load(workspace)
	return SourcesWithManifests(configHome, workspace, manifests)
}

// SourcesWithManifests reports command roots for a resolved runtime plugin set.
func SourcesWithManifests(configHome, workspace string, manifests []plugins.Manifest) []DiscoveryRoot {
	out := []DiscoveryRoot{}
	for _, root := range rootsWithManifests(configHome, workspace, manifests) {
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

func Install(source string, targetRoot string, explicitName string, targetLabel string) (InstallReport, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return InstallReport{}, errors.New("command install source is required")
	}
	targetRoot = strings.TrimSpace(targetRoot)
	if targetRoot == "" {
		return InstallReport{}, errors.New("command install target is required")
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
		return InstallReport{}, fmt.Errorf("command source %q must be a markdown file", source)
	}
	name := strings.TrimSpace(explicitName)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(resolvedSource), filepath.Ext(resolvedSource))
	}
	name = normalizeName(name)
	if err := validateCommandName(name); err != nil {
		return InstallReport{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return InstallReport{}, err
	}
	dest := filepath.Join(targetRoot, commandPathName(name)+".md")
	if err := copyFile(resolvedSource, dest, 0o644); err != nil {
		return InstallReport{}, err
	}
	return InstallReport{
		Kind:   "commands",
		Action: "install",
		Status: "ok",
		Name:   name,
		Source: resolvedSource,
		Path:   dest,
		Target: targetLabel,
	}, nil
}

func Uninstall(name string, roots []string) (UninstallReport, error) {
	name = normalizeName(name)
	if name == "" {
		return UninstallReport{}, errors.New("command name is required")
	}
	if err := validateCommandName(name); err != nil {
		return UninstallReport{}, err
	}
	pathName := commandPathName(name)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, pathName+".md")
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
			Kind:    "commands",
			Action:  "uninstall",
			Status:  "ok",
			Name:    name,
			Path:    candidate,
			Removed: true,
		}, nil
	}
	return UninstallReport{}, fmt.Errorf("%w: %s", ErrNotFound, name)
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

func rootsWithManifests(configHome, workspace string, manifests []plugins.Manifest) []root {
	out := []root{
		{path: filepath.Join(configHome, "commands"), source: "user"},
		{path: filepath.Join(workspace, ".claude", "commands"), source: "claude"},
		{path: filepath.Join(workspace, ".codog", "commands"), source: "workspace"},
	}
	out = append(out, compatibilityRoots(workspace, false)...)
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
	base = append(base, compatibilityRoots(workspace, true)...)
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
	case source == "omc":
		return "OMC-compatible commands"
	case source == "claw":
		return "Claw-compatible commands"
	case source == "codex":
		return "Codex-compatible commands"
	case source == "agents":
		return "Agents-compatible commands"
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
	case source == "omc", source == "claw", source == "codex", source == "agents":
		return 3
	case strings.HasPrefix(source, "plugin:"):
		return 4
	default:
		return 5
	}
}

func compatibilityRoots(workspace string, precedence bool) []root {
	seen := map[string]bool{}
	out := []root{}
	add := func(path string, source string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		if info, err := os.Stat(clean); err != nil || !info.IsDir() {
			return
		}
		seen[clean] = true
		out = append(out, root{path: clean, source: source})
	}
	addPrefixed := func(prefix string, source string) {
		if strings.TrimSpace(prefix) == "" {
			return
		}
		add(filepath.Join(prefix, "commands"), source)
	}
	addPrefixed(os.Getenv("CLAW_CONFIG_HOME"), "claw")
	addPrefixed(os.Getenv("CODEX_HOME"), "codex")
	if claudeConfigDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); claudeConfigDir != "" {
		add(filepath.Join(claudeConfigDir, "commands"), "claude")
	}
	for _, ancestor := range workspaceAncestors(workspace) {
		add(filepath.Join(ancestor, ".omc", "commands"), "omc")
		add(filepath.Join(ancestor, ".claw", "commands"), "claw")
		add(filepath.Join(ancestor, ".codex", "commands"), "codex")
		add(filepath.Join(ancestor, ".agents", "commands"), "agents")
	}
	if precedence {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sourceRank(out[i].source) == sourceRank(out[j].source) {
			return out[i].path < out[j].path
		}
		return sourceRank(out[i].source) < sourceRank(out[j].source)
	})
	return out
}

func workspaceAncestors(workspace string) []string {
	return projectscope.Ancestors(workspace)
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

func validateCommandName(name string) error {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid command name %q", name)
	}
	for _, part := range strings.Split(name, ":") {
		if strings.TrimSpace(part) == "" || part == "." {
			return fmt.Errorf("invalid command name %q", name)
		}
	}
	return nil
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
