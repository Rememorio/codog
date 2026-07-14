package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/agentdefs"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/argsub"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/worktree"
)

func parseBackgroundLogsArgs(args []string) (string, int64, error) {
	if len(args) == 0 {
		return "", 0, errors.New("usage: codog background logs ID [bytes|--bytes N|--limit N]")
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", 0, errors.New("task id is required")
	}
	limit := int64(64 * 1024)
	positionals := []string{}
	for index := 1; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--bytes" || arg == "--limit":
			index++
			if index >= len(args) {
				return "", 0, errors.New("background log byte limit is required")
			}
			parsed, err := parseNonNegativeInt64(args[index], "background log byte limit")
			if err != nil {
				return "", 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--bytes="):
			parsed, err := parseNonNegativeInt64(strings.TrimPrefix(arg, "--bytes="), "background log byte limit")
			if err != nil {
				return "", 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--limit="):
			parsed, err := parseNonNegativeInt64(strings.TrimPrefix(arg, "--limit="), "background log byte limit")
			if err != nil {
				return "", 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "-"):
			return "", 0, fmt.Errorf("unknown background logs argument %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", 0, errors.New("usage: codog background logs ID [bytes|--bytes N|--limit N]")
	}
	if len(positionals) == 1 {
		parsed, err := parseNonNegativeInt64(positionals[0], "background log byte limit")
		if err != nil {
			return "", 0, err
		}
		limit = parsed
	}
	return id, limit, nil
}

func parseBackgroundWatchArgs(args []string) (string, int64, int, error) {
	if len(args) == 0 {
		return "", 0, 0, errors.New("usage: codog background watch ID [offset|--offset N] [--max-events N]")
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", 0, 0, errors.New("task id is required")
	}
	offset := int64(0)
	maxEvents := 0
	positionals := []string{}
	for index := 1; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--offset":
			index++
			if index >= len(args) {
				return "", 0, 0, errors.New("background watch offset is required")
			}
			parsed, err := parseNonNegativeInt64(args[index], "background watch offset")
			if err != nil {
				return "", 0, 0, err
			}
			offset = parsed
		case strings.HasPrefix(arg, "--offset="):
			parsed, err := parseNonNegativeInt64(strings.TrimPrefix(arg, "--offset="), "background watch offset")
			if err != nil {
				return "", 0, 0, err
			}
			offset = parsed
		case arg == "--max-events":
			index++
			if index >= len(args) {
				return "", 0, 0, errors.New("background watch max events is required")
			}
			parsed, err := parseNonNegativeInt(args[index], "background watch max events")
			if err != nil {
				return "", 0, 0, err
			}
			maxEvents = parsed
		case strings.HasPrefix(arg, "--max-events="):
			parsed, err := parseNonNegativeInt(strings.TrimPrefix(arg, "--max-events="), "background watch max events")
			if err != nil {
				return "", 0, 0, err
			}
			maxEvents = parsed
		case strings.HasPrefix(arg, "-"):
			return "", 0, 0, fmt.Errorf("unknown background watch argument %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return "", 0, 0, errors.New("usage: codog background watch ID [offset|--offset N] [--max-events N]")
	}
	if len(positionals) == 1 {
		parsed, err := parseNonNegativeInt64(positionals[0], "background watch offset")
		if err != nil {
			return "", 0, 0, err
		}
		offset = parsed
	}
	return id, offset, maxEvents, nil
}

func parseNonNegativeInt64(value string, name string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return parsed, nil
}

func parseBackgroundPruneArgs(args []string) (background.PruneOptions, error) {
	options := background.DefaultPruneOptions()
	if len(args) > 0 {
		days, err := strconv.Atoi(args[0])
		if err != nil {
			return options, err
		}
		if days < 0 {
			return options, errors.New("prune days must be non-negative")
		}
		options.OlderThan = time.Duration(days) * 24 * time.Hour
	}
	if len(args) > 1 {
		keep, err := strconv.Atoi(args[1])
		if err != nil {
			return options, err
		}
		if keep < 0 {
			return options, errors.New("prune keep must be non-negative")
		}
		options.Keep = keep
	}
	if len(args) > 2 {
		return options, errors.New("usage: codog background prune [days] [keep]")
	}
	return options, nil
}

func (a *App) RegisterPluginTools() error {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, tool := range manifest.Tools {
			if tool.Command == "" {
				continue
			}
			name := tool.Name
			if name == "" {
				continue
			}
			if !plugins.ValidToolPermission(tool.Permission) {
				return fmt.Errorf("plugin tool %q declares unsupported permission %q", name, tool.Permission)
			}
			if a.Tools.Has(name) {
				return fmt.Errorf("plugin tool %q conflicts with an existing tool", name)
			}
			variables := map[string]string{
				"CLAUDE_PLUGIN_ROOT": filepath.ToSlash(manifest.Root),
				"CLAUDE_PLUGIN_DATA": filepath.ToSlash(plugins.DataDirForManifest(manifest)),
			}
			a.Tools.Register(tools.CommandTool{
				Name:        name,
				Description: tool.Description,
				Schema:      tool.InputSchema,
				Required:    tools.Permission(tool.Permission),
				Command:     argsub.SubstituteVariables(tool.Command, variables),
				Args:        argsub.SubstituteVariablesInList(tool.Args, variables),
				Workspace:   manifest.Root,
			})
		}
	}
	return nil
}

type reloadPluginsRequest struct {
	Format string
}

type reloadPluginsReport struct {
	Kind             string   `json:"kind"`
	Action           string   `json:"action"`
	Status           string   `json:"status"`
	Workspace        string   `json:"workspace"`
	Plugins          int      `json:"plugins"`
	EnabledPlugins   int      `json:"enabled_plugins"`
	PluginTools      int      `json:"plugin_tools"`
	ToolCountBefore  int      `json:"tool_count_before"`
	ToolCountAfter   int      `json:"tool_count_after"`
	MCPToolsReloaded bool     `json:"mcp_tools_reloaded"`
	PluginIDs        []string `json:"plugin_ids,omitempty"`
	EnabledPluginIDs []string `json:"enabled_plugin_ids,omitempty"`
	Reloaded         bool     `json:"reloaded"`
}

func (a *App) ReloadPlugins(args []string) error {
	return a.ReloadPluginsWithFormat(args, "text")
}

func (a *App) ReloadPluginsWithFormat(args []string, defaultFormat string) error {
	req, err := parseReloadPluginsArgs(args, defaultFormat)
	if err != nil {
		return err
	}
	manifests, err := plugins.LoadWithDirs(a.Workspace, a.PluginDirs)
	if err != nil {
		return err
	}
	fileAgentDefinitions, err := agentdefs.LoadWithManifests(a.Workspace, manifests)
	if err != nil {
		return err
	}
	before := 0
	if a.Tools != nil {
		before = len(a.Tools.Infos())
	}
	oldRegistry := a.Tools
	oldMCPLoaded := a.mcpToolsLoaded
	oldManifests := a.PluginManifests
	oldAgentDefinitions := a.AgentDefinitions
	a.PluginManifests = manifests
	a.AgentDefinitions = agentdefs.Merge(fileAgentDefinitions, a.InlineAgents)
	nextRegistry, err := a.newToolRegistry()
	if err != nil {
		a.PluginManifests = oldManifests
		a.AgentDefinitions = oldAgentDefinitions
		return err
	}
	a.Tools = nextRegistry
	a.mcpToolsLoaded = false
	if err := a.RegisterPluginTools(); err != nil {
		a.Tools = oldRegistry
		a.mcpToolsLoaded = oldMCPLoaded
		a.PluginManifests = oldManifests
		a.AgentDefinitions = oldAgentDefinitions
		return err
	}
	report := buildReloadPluginsReport(a.Workspace, manifests, before, len(a.Tools.Infos()), oldMCPLoaded)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReloadPluginsReport(a.Out, report)
	return nil
}

func (a *App) newToolRegistry() (*tools.Registry, error) {
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		return nil, err
	}
	questionIn := a.In
	if questionIn == nil {
		questionIn = os.Stdin
	}
	questionOut := a.Err
	if questionOut == nil {
		questionOut = io.Discard
	}
	executable, err := a.executablePath()
	if err != nil {
		return nil, err
	}
	options := toolRegistryOptionsFromConfig(a.Config, additionalDirs, questionIn, questionOut, executable, a.AgentDefinitions)
	options.PluginDirs = append([]string(nil), a.PluginDirs...)
	return tools.NewRegistryWithOptions(a.Workspace, options), nil
}

const reloadPluginsUsage = "codog reload-plugins [reload|refresh] [--json|--output-format text|json]"

func parseReloadPluginsArgs(args []string, defaultFormat string) (reloadPluginsRequest, error) {
	if strings.TrimSpace(defaultFormat) == "" {
		defaultFormat = "text"
	}
	req := reloadPluginsRequest{Format: defaultFormat}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "reload-plugins", Flag: arg, Usage: reloadPluginsUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "reload" || arg == "refresh":
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "reload-plugins", Option: arg, Usage: reloadPluginsUsage}
			}
			return req, unexpectedExtraArgsError{Command: "reload-plugins", Args: []string{arg}, Usage: reloadPluginsUsage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("reload-plugins", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func buildReloadPluginsReport(workspace string, manifests []plugins.Manifest, before, after int, oldMCPLoaded bool) reloadPluginsReport {
	report := reloadPluginsReport{
		Kind:             "reload_plugins",
		Action:           "reload",
		Status:           "ok",
		Workspace:        workspace,
		Plugins:          len(manifests),
		ToolCountBefore:  before,
		ToolCountAfter:   after,
		MCPToolsReloaded: oldMCPLoaded,
		Reloaded:         true,
	}
	for _, manifest := range manifests {
		report.PluginIDs = append(report.PluginIDs, manifest.ID)
		if !manifest.Enabled {
			continue
		}
		report.EnabledPlugins++
		report.EnabledPluginIDs = append(report.EnabledPluginIDs, manifest.ID)
		for _, tool := range manifest.Tools {
			if strings.TrimSpace(tool.Name) != "" && strings.TrimSpace(tool.Command) != "" {
				report.PluginTools++
			}
		}
	}
	sort.Strings(report.PluginIDs)
	sort.Strings(report.EnabledPluginIDs)
	return report
}

func renderReloadPluginsReport(out io.Writer, report reloadPluginsReport) {
	fmt.Fprintln(out, "Plugins Reloaded")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Plugins          %d\n", report.Plugins)
	fmt.Fprintf(out, "  Enabled          %d\n", report.EnabledPlugins)
	fmt.Fprintf(out, "  Plugin tools     %d\n", report.PluginTools)
	fmt.Fprintf(out, "  Tools before     %d\n", report.ToolCountBefore)
	fmt.Fprintf(out, "  Tools after      %d\n", report.ToolCountAfter)
	if len(report.EnabledPluginIDs) > 0 {
		fmt.Fprintf(out, "  Enabled IDs      %s\n", strings.Join(report.EnabledPluginIDs, ", "))
	}
	if report.MCPToolsReloaded {
		fmt.Fprintln(out, "  MCP tools        will reload on next provider turn")
	}
}

func (a *App) RegisterMCPTools(ctx context.Context) error {
	if a.mcpToolsLoaded {
		return nil
	}
	failures := []string{}
	registered := 0
	for _, serverName := range sortedMCPServerNames(a.Config.MCPServers) {
		server := a.Config.MCPServers[serverName]
		result := mcp.ListTools(ctx, serverName, server)
		if result.Error != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", serverName, result.Error))
			continue
		}
		for _, remoteTool := range result.Tools {
			name := tools.NewMCPToolName(serverName, remoteTool.Name)
			if a.Tools.Has(name) {
				return fmt.Errorf("mcp tool %q conflicts with an existing tool", name)
			}
			a.Tools.Register(tools.MCPTool{
				Name:        name,
				Description: remoteTool.Description,
				Schema:      remoteTool.InputSchema,
				Required:    tools.PermissionWorkspace,
				ServerName:  serverName,
				Server:      server,
				RemoteName:  remoteTool.Name,
			})
			registered++
		}
	}
	if len(failures) != 0 {
		if a.Err != nil {
			for _, failure := range failures {
				fmt.Fprintf(a.Err, "MCP server unavailable: %s\n", failure)
			}
		}
		if registered == 0 {
			return fmt.Errorf("no MCP tools registered; %s", strings.Join(failures, "; "))
		}
	}
	a.mcpToolsLoaded = true
	return nil
}

func sortedMCPServerNames(servers map[string]config.MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *App) ListAgents() error {
	return a.listAgents("text", "")
}

type agentsListReport struct {
	Kind            string                 `json:"kind"`
	Action          string                 `json:"action"`
	Status          string                 `json:"status"`
	Count           int                    `json:"count"`
	AcceptedFormats []string               `json:"accepted_formats"`
	Agents          []agentdefs.Definition `json:"agents"`
}

type agentShowReport struct {
	Kind   string               `json:"kind"`
	Action string               `json:"action"`
	Status string               `json:"status"`
	Agent  agentdefs.Definition `json:"agent"`
}

type agentsHelpReport struct {
	Kind            string   `json:"kind"`
	Action          string   `json:"action"`
	Status          string   `json:"status"`
	Usage           string   `json:"usage"`
	AcceptedFormats []string `json:"accepted_formats"`
	Sources         []string `json:"sources"`
	Examples        []string `json:"examples"`
}

type agentCreateReport struct {
	Kind    string               `json:"kind"`
	Action  string               `json:"action"`
	Status  string               `json:"status"`
	Result  string               `json:"result"`
	Name    string               `json:"name"`
	Path    string               `json:"path"`
	Format  string               `json:"format"`
	Agent   agentdefs.Definition `json:"agent"`
	Message string               `json:"message,omitempty"`
}

type agentRunReport struct {
	Kind     string               `json:"kind"`
	Action   string               `json:"action"`
	Status   string               `json:"status"`
	Agent    string               `json:"agent"`
	RunID    string               `json:"run_id"`
	Run      agentruns.Run        `json:"run"`
	Task     background.Task      `json:"task"`
	Worktree *worktree.Allocation `json:"worktree,omitempty"`
}

type agentRunStatus = agentruns.Status

type agentRunsReport struct {
	Kind   string           `json:"kind"`
	Action string           `json:"action"`
	Status string           `json:"status"`
	Count  int              `json:"count"`
	Runs   []agentRunStatus `json:"runs,omitempty"`
	Run    *agentRunStatus  `json:"run,omitempty"`
}

type agentRunBoardEntry = agentruns.BoardEntry

type agentRunBoardReport struct {
	Kind        string               `json:"kind"`
	Action      string               `json:"action"`
	Status      string               `json:"status"`
	GeneratedAt time.Time            `json:"generated_at"`
	Active      []agentRunBoardEntry `json:"active"`
	Blocked     []agentRunBoardEntry `json:"blocked"`
	Finished    []agentRunBoardEntry `json:"finished"`
	Orphaned    []agentRunBoardEntry `json:"orphaned,omitempty"`
}

type agentRunActionReport struct {
	Kind    string          `json:"kind"`
	Action  string          `json:"action"`
	Status  string          `json:"status"`
	Run     agentruns.Run   `json:"run"`
	Task    background.Task `json:"task"`
	Message string          `json:"message,omitempty"`
	Output  string          `json:"output,omitempty"`
}

type agentRunPruneReport struct {
	Kind         string   `json:"kind"`
	Action       string   `json:"action"`
	Status       string   `json:"status"`
	Removed      []string `json:"removed"`
	RemovedCount int      `json:"removed_count"`
	Kept         int      `json:"kept"`
}

func (a *App) listAgents(format string, filter string) error {
	defs, err := a.runtimeAgentDefinitions()
	if err != nil {
		return err
	}
	if strings.TrimSpace(filter) != "" {
		defs = filterAgentDefinitions(defs, filter)
	}
	if format == "json" {
		data, _ := json.MarshalIndent(agentsListReport{
			Kind:            "agents",
			Action:          "list",
			Status:          "ok",
			Count:           len(defs),
			AcceptedFormats: agentdefs.AcceptedFormats(),
			Agents:          defs,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAgentsListText(a.Out, defs)
	return nil
}

func (a *App) showAgent(name string, format string) error {
	defs, err := a.runtimeAgentDefinitions()
	if err != nil {
		return err
	}
	for _, def := range defs {
		if strings.EqualFold(def.Name, name) {
			if format == "json" {
				data, _ := json.MarshalIndent(agentShowReport{
					Kind:   "agents",
					Action: "show",
					Status: "ok",
					Agent:  def,
				}, "", "  ")
				fmt.Fprintln(a.Out, string(data))
				return nil
			}
			renderAgentDefinitionText(a.Out, def)
			return nil
		}
	}
	return renderActionError(a.Out, actionErrorReport{
		Kind:      "agents",
		Action:    "show",
		Status:    "error",
		ErrorKind: "agent_not_found",
		Message:   fmt.Sprintf("agent %q was not found", name),
		Hint:      "Run `codog agents list` to see available agents.",
	}, format)
}

func renderAgentsListText(out io.Writer, definitions []agentdefs.Definition) {
	fmt.Fprintln(out, "Agents")
	fmt.Fprintf(out, "  Count            %d\n", len(definitions))
	for _, definition := range definitions {
		value := definition.Name
		if definition.Model != "" {
			value += " · " + definition.Model
		}
		if definition.Source != "" {
			value += " · " + definition.Source
		}
		fmt.Fprintf(out, "  %s\n", value)
		if definition.Description != "" {
			fmt.Fprintf(out, "    %s\n", trimSingleLine(definition.Description, 160))
		}
	}
}

func renderAgentDefinitionText(out io.Writer, definition agentdefs.Definition) {
	fmt.Fprintln(out, "Agent")
	fmt.Fprintf(out, "  Name             %s\n", definition.Name)
	if definition.Description != "" {
		fmt.Fprintf(out, "  Description      %s\n", definition.Description)
	}
	if definition.Model != "" {
		fmt.Fprintf(out, "  Model            %s\n", definition.Model)
	}
	if len(definition.Tools) > 0 {
		fmt.Fprintf(out, "  Tools            %s\n", strings.Join(definition.Tools, ", "))
	}
	if definition.Source != "" {
		fmt.Fprintf(out, "  Source           %s\n", definition.Source)
	}
	if definition.Format != "" {
		fmt.Fprintf(out, "  Format           %s\n", definition.Format)
	}
	if definition.Path != "" {
		fmt.Fprintf(out, "  Path             %s\n", definition.Path)
	}
	if definition.Prompt != "" {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Prompt")
		fmt.Fprintln(out, definition.Prompt)
	}
}

func (a *App) createAgent(rawName string, format string) error {
	name, ok := sanitizeAgentName(rawName)
	if !ok {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "create",
			Status:    "error",
			ErrorKind: "invalid_agent_name",
			Message:   "agent name must contain at least one alphanumeric character",
			Hint:      "Use `codog agents create NAME` with a simple alphanumeric, dash, underscore, or dot name.",
		}, format)
	}
	root := filepath.Join(a.Workspace, ".codog", "agents")
	path := filepath.Join(root, name+".json")
	if _, err := os.Stat(path); err == nil {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "create",
			Status:    "error",
			ErrorKind: "agent_already_exists",
			Message:   fmt.Sprintf("agent %q already exists at %s", name, path),
			Hint:      fmt.Sprintf("Run `codog agents show %s` to inspect the existing definition.", name),
		}, format)
	} else if !os.IsNotExist(err) {
		return err
	}
	def := agentdefs.Definition{
		Name:        name,
		Description: "Focused local subagent for scoped Codog tasks.",
		Prompt:      "Handle the assigned task as a focused Codog subagent. State assumptions, make concrete progress, and report verification results.",
	}
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	def.Path = path
	def.Source = "workspace"
	if err := a.activateCreatedAgentDefinition(def); err != nil {
		_ = os.Remove(path)
		return err
	}
	report := agentCreateReport{
		Kind:    "agents",
		Action:  "create",
		Status:  "ok",
		Result:  "created",
		Name:    name,
		Path:    path,
		Format:  "json",
		Agent:   def,
		Message: fmt.Sprintf("Created local agent definition %q.", name),
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAgentCreateReport(a.Out, report)
	return nil
}

func (a *App) activateCreatedAgentDefinition(definition agentdefs.Definition) error {
	previousDefinitions := append([]agentdefs.Definition(nil), a.AgentDefinitions...)
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return err
	}
	loaded, err := agentdefs.LoadWithManifests(a.Workspace, manifests)
	if err != nil {
		return err
	}
	a.AgentDefinitions = agentdefs.Merge(loaded, a.InlineAgents, a.AgentDefinitions, []agentdefs.Definition{definition})
	if a.Tools == nil {
		return nil
	}
	previousTools := a.Tools
	previousMCPLoaded := a.mcpToolsLoaded
	next, err := a.newToolRegistry()
	if err != nil {
		a.AgentDefinitions = previousDefinitions
		return err
	}
	a.Tools = next
	a.mcpToolsLoaded = false
	if err := a.RegisterPluginTools(); err != nil {
		a.AgentDefinitions = previousDefinitions
		a.Tools = previousTools
		a.mcpToolsLoaded = previousMCPLoaded
		return err
	}
	return nil
}

func sanitizeAgentName(candidate string) (string, bool) {
	trimmed := strings.TrimSpace(candidate)
	trimmed = strings.TrimLeft(trimmed, "/$")
	if trimmed == "" {
		return "", false
	}
	var builder strings.Builder
	lastSeparator := false
	for _, ch := range trimmed {
		switch {
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch + ('a' - 'A'))
			lastSeparator = false
		case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.':
			builder.WriteRune(ch)
			lastSeparator = false
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '/' || ch == '\\':
			if builder.Len() > 0 && !lastSeparator {
				builder.WriteByte('-')
				lastSeparator = true
			}
		}
	}
	name := strings.Trim(builder.String(), "-_.")
	return name, name != ""
}

func renderAgentCreateReport(out io.Writer, report agentCreateReport) {
	fmt.Fprintln(out, "Agents")
	fmt.Fprintf(out, "  Result           %s %s\n", report.Result, report.Name)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
	fmt.Fprintf(out, "  Format           %s\n", report.Format)
	fmt.Fprintf(out, "  Next             codog agents show %s\n", report.Name)
}

func renderAgentsHelp(out io.Writer, format string) error {
	report := agentsHelpReport{
		Kind:            "agents",
		Action:          "help",
		Status:          "ok",
		Usage:           "codog agents [list|show NAME|create NAME|run NAME PROMPT|runs|board|help] [--json|--output-format text|json]",
		AcceptedFormats: agentdefs.AcceptedFormats(),
		Sources:         []string{".codog/agents", ".claude/agents", ".claw/agents", ".omc/agents", "enabled plugin agents"},
		Examples: []string{
			"codog agents list --json",
			"codog agents show reviewer",
			"codog agents run reviewer \"review this change\"",
		},
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Agents")
	fmt.Fprintf(out, "  Usage            %s\n", report.Usage)
	fmt.Fprintf(out, "  Formats          %s\n", strings.Join(report.AcceptedFormats, ", "))
	fmt.Fprintf(out, "  Sources          %s\n", strings.Join(report.Sources, ", "))
	fmt.Fprintf(out, "  Examples         %s\n", strings.Join(report.Examples, "; "))
	return nil
}

func filterAgentDefinitions(defs []agentdefs.Definition, filter string) []agentdefs.Definition {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return defs
	}
	out := make([]agentdefs.Definition, 0, len(defs))
	for _, def := range defs {
		if strings.Contains(strings.ToLower(def.Name), filter) || strings.Contains(strings.ToLower(def.Description), filter) {
			out = append(out, def)
		}
	}
	return out
}

func (a *App) listAgentRuns(format string, filter string) error {
	runs, err := agentruns.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		return err
	}
	if strings.TrimSpace(filter) != "" {
		runs = filterAgentRuns(runs, filter)
	}
	statuses := make([]agentRunStatus, 0, len(runs))
	taskStore := background.NewStore(a.Config.ConfigHome)
	for _, run := range runs {
		statuses = append(statuses, agentruns.StatusForTask(taskStore, run))
	}
	report := agentRunsReport{
		Kind:   "agents",
		Action: "runs",
		Status: "ok",
		Count:  len(statuses),
		Runs:   statuses,
	}
	return renderAgentRunsReport(a.Out, report, format)
}

func (a *App) boardAgentRuns(stalledAfter time.Duration, format string) error {
	runs, err := agentruns.NewStore(a.Config.ConfigHome).List()
	if err != nil {
		return err
	}
	board := agentruns.BuildBoard(background.NewStore(a.Config.ConfigHome), runs, time.Now().UTC(), stalledAfter)
	report := agentRunBoardReport{
		Kind:        "agents",
		Action:      "board",
		Status:      "ok",
		GeneratedAt: board.GeneratedAt,
		Active:      board.Active,
		Blocked:     board.Blocked,
		Finished:    board.Finished,
		Orphaned:    board.Orphaned,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderAgentRunBoardReport(a.Out, report)
	return nil
}

func (a *App) showAgentRun(id string, format string) error {
	run, err := agentruns.NewStore(a.Config.ConfigHome).Get(id)
	if err != nil {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "status",
			Status:    "error",
			ErrorKind: "agent_run_not_found",
			Message:   fmt.Sprintf("agent run %q was not found", id),
			Hint:      "Run `codog agents runs` to list agent runs.",
		}, format)
	}
	status := agentruns.StatusForTask(background.NewStore(a.Config.ConfigHome), run)
	report := agentRunsReport{
		Kind:   "agents",
		Action: "status",
		Status: "ok",
		Count:  1,
		Run:    &status,
	}
	return renderAgentRunsReport(a.Out, report, format)
}

func (a *App) heartbeatAgentRun(id string, heartbeat background.LaneHeartbeat, format string) error {
	runStore := agentruns.NewStore(a.Config.ConfigHome)
	run, err := runStore.Get(id)
	if err != nil {
		return renderAgentRunNotFound(a.Out, "heartbeat", id, format)
	}
	task, err := background.NewStore(a.Config.ConfigHome).UpdateHeartbeat(run.TaskID, heartbeat)
	if err != nil {
		return err
	}
	run, err = runStore.Touch(run.ID)
	if err != nil {
		return err
	}
	return renderAgentRunActionReport(a.Out, agentRunActionReport{
		Kind:   "agents",
		Action: "heartbeat",
		Status: "ok",
		Run:    run,
		Task:   task,
	}, format)
}

func (a *App) removeAgentRun(id string, format string) error {
	store := agentruns.NewStore(a.Config.ConfigHome)
	if _, err := store.Get(id); err != nil {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "run-remove",
			Status:    "error",
			ErrorKind: "agent_run_not_found",
			Message:   fmt.Sprintf("agent run %q was not found", id),
			Hint:      "Run `codog agents runs` to list agent runs.",
		}, format)
	}
	if err := store.Remove(id); err != nil {
		return err
	}
	report := agentRunsReport{
		Kind:   "agents",
		Action: "run-remove",
		Status: "ok",
		Count:  0,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":    report.Kind,
			"action":  report.Action,
			"status":  report.Status,
			"removed": true,
			"id":      id,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Agent Run Removed")
	fmt.Fprintf(a.Out, "  ID               %s\n", id)
	return nil
}

func (a *App) pruneAgentRuns(options background.PruneOptions, format string) error {
	runStore := agentruns.NewStore(a.Config.ConfigHome)
	taskStore := background.NewStore(a.Config.ConfigHome)
	result, err := agentruns.Prune(runStore, taskStore, options)
	if err != nil {
		return err
	}
	report := agentRunPruneReport{
		Kind:         "agents",
		Action:       "prune",
		Status:       "ok",
		Removed:      result.Removed,
		RemovedCount: result.RemovedCount,
		Kept:         result.Kept,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Agent Runs Pruned")
	fmt.Fprintf(a.Out, "  Removed          %d\n", report.RemovedCount)
	fmt.Fprintf(a.Out, "  Kept             %d\n", report.Kept)
	if len(report.Removed) > 0 {
		fmt.Fprintf(a.Out, "  Removed IDs      %s\n", strings.Join(report.Removed, ", "))
	}
	return nil
}

func (a *App) stopAgentRun(id string, format string) error {
	runStore := agentruns.NewStore(a.Config.ConfigHome)
	run, err := runStore.Get(id)
	if err != nil {
		return renderAgentRunNotFound(a.Out, "stop", id, format)
	}
	task, err := background.NewStore(a.Config.ConfigHome).Stop(run.TaskID)
	if err != nil {
		return err
	}
	run, err = runStore.Touch(run.ID)
	if err != nil {
		return err
	}
	return renderAgentRunActionReport(a.Out, agentRunActionReport{
		Kind:   "agents",
		Action: "stop",
		Status: "ok",
		Run:    run,
		Task:   task,
	}, format)
}

func (a *App) updateAgentRun(id string, message string, format string) error {
	runStore := agentruns.NewStore(a.Config.ConfigHome)
	run, err := runStore.Get(id)
	if err != nil {
		return renderAgentRunNotFound(a.Out, "update", id, format)
	}
	task, err := background.NewStore(a.Config.ConfigHome).Update(run.TaskID, message)
	if err != nil {
		return err
	}
	run, err = runStore.Touch(run.ID)
	if err != nil {
		return err
	}
	return renderAgentRunActionReport(a.Out, agentRunActionReport{
		Kind:    "agents",
		Action:  "update",
		Status:  "ok",
		Run:     run,
		Task:    task,
		Message: message,
	}, format)
}

func (a *App) outputAgentRun(id string, limitBytes int64, format string) error {
	runStore := agentruns.NewStore(a.Config.ConfigHome)
	run, err := runStore.Get(id)
	if err != nil {
		return renderAgentRunNotFound(a.Out, "output", id, format)
	}
	taskStore := background.NewStore(a.Config.ConfigHome)
	task, err := taskStore.Status(run.TaskID)
	if err != nil {
		return err
	}
	output, err := taskStore.Logs(run.TaskID, limitBytes)
	if err != nil {
		return err
	}
	run, err = runStore.Touch(run.ID)
	if err != nil {
		return err
	}
	report := agentRunActionReport{
		Kind:   "agents",
		Action: "output",
		Status: "ok",
		Run:    run,
		Task:   task,
		Output: output,
	}
	if format == "json" {
		return renderAgentRunActionReport(a.Out, report, format)
	}
	fmt.Fprint(a.Out, output)
	return nil
}

func renderAgentRunNotFound(out io.Writer, action string, id string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "agents",
		Action:    action,
		Status:    "error",
		ErrorKind: "agent_run_not_found",
		Message:   fmt.Sprintf("agent run %q was not found", id),
		Hint:      "Run `codog agents runs` to list agent runs.",
	}, format)
}

func renderAgentRunActionReport(out io.Writer, report agentRunActionReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Agent Run")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Run              %s\n", report.Run.ID)
	fmt.Fprintf(out, "  Agent            %s\n", report.Run.Agent)
	fmt.Fprintf(out, "  Task             %s\n", report.Task.ID)
	fmt.Fprintf(out, "  Status           %s\n", report.Task.Status)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	if report.Output != "" {
		fmt.Fprintf(out, "  Output bytes     %d\n", len(report.Output))
	}
	return nil
}

func renderAgentRunBoardReport(out io.Writer, report agentRunBoardReport) {
	fmt.Fprintln(out, "Agent Run Board")
	fmt.Fprintf(out, "  Active           %d\n", len(report.Active))
	fmt.Fprintf(out, "  Blocked          %d\n", len(report.Blocked))
	fmt.Fprintf(out, "  Finished         %d\n", len(report.Finished))
	if len(report.Orphaned) > 0 {
		fmt.Fprintf(out, "  Orphaned         %d\n", len(report.Orphaned))
	}
	for _, group := range []struct {
		name    string
		entries []agentRunBoardEntry
	}{
		{"Active", report.Active},
		{"Blocked", report.Blocked},
		{"Finished", report.Finished},
		{"Orphaned", report.Orphaned},
	} {
		for _, entry := range group.entries {
			fmt.Fprintf(out, "  - %s %s\n", group.name, entry.Run.ID)
			fmt.Fprintf(out, "    Agent          %s\n", entry.Run.Agent)
			fmt.Fprintf(out, "    Status         %s\n", entry.Status)
			fmt.Fprintf(out, "    Freshness      %s\n", entry.Freshness)
			fmt.Fprintf(out, "    Lifecycle      %s\n", renderLifecycleResolution(entry.Lifecycle))
			fmt.Fprintf(out, "    Provenance     %s\n", renderEventProvenance(entry.Provenance))
			fmt.Fprintf(out, "    Scope          %s\n", renderScopeBinding(entry.ScopeBinding))
			if entry.TerminalOutcome != nil {
				fmt.Fprintf(out, "    Terminal       %s\n", renderTerminalOutcome(entry.TerminalOutcome))
			}
			if entry.Error != "" {
				fmt.Fprintf(out, "    Error          %s\n", entry.Error)
			}
		}
	}
}

func filterAgentRuns(runs []agentruns.Run, filter string) []agentruns.Run {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return runs
	}
	out := make([]agentruns.Run, 0, len(runs))
	for _, run := range runs {
		if strings.Contains(strings.ToLower(run.Agent), filter) ||
			strings.Contains(strings.ToLower(run.ID), filter) ||
			strings.Contains(strings.ToLower(run.TaskID), filter) {
			out = append(out, run)
		}
	}
	return out
}

func filterACPAgentRuns(runs []agentruns.Run, agent string, sessionID string) []agentruns.Run {
	agent = strings.TrimSpace(agent)
	sessionID = strings.TrimSpace(sessionID)
	if agent == "" && sessionID == "" {
		return runs
	}
	out := make([]agentruns.Run, 0, len(runs))
	for _, run := range runs {
		if agent != "" && !strings.EqualFold(run.Agent, agent) {
			continue
		}
		if sessionID != "" && run.SessionID != sessionID {
			continue
		}
		out = append(out, run)
	}
	return out
}

func renderAgentRunsReport(out io.Writer, report agentRunsReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	title := "Agent Runs"
	if report.Action == "status" {
		title = "Agent Run"
	}
	fmt.Fprintln(out, title)
	if report.Run != nil {
		renderAgentRunStatus(out, *report.Run)
		return nil
	}
	fmt.Fprintf(out, "  Count            %d\n", report.Count)
	for _, run := range report.Runs {
		renderAgentRunStatus(out, run)
	}
	return nil
}

func renderAgentRunStatus(out io.Writer, status agentRunStatus) {
	run := status.Run
	fmt.Fprintf(out, "  - %s\n", run.ID)
	fmt.Fprintf(out, "    Agent          %s\n", run.Agent)
	fmt.Fprintf(out, "    Status         %s\n", status.CurrentStatus)
	fmt.Fprintf(out, "    Freshness      %s\n", status.Freshness)
	fmt.Fprintf(out, "    Lifecycle      %s\n", renderLifecycleResolution(status.Lifecycle))
	fmt.Fprintf(out, "    Provenance     %s\n", renderEventProvenance(status.Provenance))
	fmt.Fprintf(out, "    Scope          %s\n", renderScopeBinding(status.ScopeBinding))
	if status.TerminalOutcome != nil {
		fmt.Fprintf(out, "    Terminal       %s\n", renderTerminalOutcome(status.TerminalOutcome))
	}
	fmt.Fprintf(out, "    Health         %s\n", status.Health.State)
	fmt.Fprintf(out, "    Task           %s\n", run.TaskID)
	if status.Health.RecommendedAction != "" {
		fmt.Fprintf(out, "    Next           %s\n", status.Health.RecommendedAction)
	}
	if run.SessionID != "" {
		fmt.Fprintf(out, "    Session        %s\n", run.SessionID)
	}
	if run.WorktreeID != "" {
		fmt.Fprintf(out, "    Worktree       %s\n", run.WorktreeID)
	}
	if run.Workspace != "" {
		fmt.Fprintf(out, "    Workspace      %s\n", run.Workspace)
	}
	if status.Error != "" {
		fmt.Fprintf(out, "    Error          %s\n", status.Error)
	}
}

func renderEventProvenance(provenance background.EventProvenance) string {
	provenance = background.NormalizeEventProvenance(provenance)
	return strings.Join([]string{
		provenance.SourceKind,
		provenance.Environment,
		provenance.Channel,
		provenance.Emitter,
		provenance.Confidence,
	}, " ")
}

func renderScopeBinding(binding background.ScopeBinding) string {
	binding = background.NormalizeScopeBinding(binding)
	parts := []string{binding.WorkflowScope, binding.WatcherAction}
	if binding.Owner != "" {
		parts = append(parts, "owner="+binding.Owner)
	}
	if binding.Actionable {
		parts = append(parts, "actionable")
	} else {
		parts = append(parts, "not_actionable")
	}
	return strings.Join(parts, " ")
}

func renderTerminalOutcome(outcome *background.TerminalOutcome) string {
	if outcome == nil {
		return ""
	}
	parts := []string{outcome.Status, outcome.Fingerprint}
	if outcome.DuplicateCount > 0 {
		parts = append(parts, fmt.Sprintf("duplicates=%d", outcome.DuplicateCount))
	}
	if outcome.ConflictCount > 0 {
		parts = append(parts, fmt.Sprintf("conflicts=%d", outcome.ConflictCount))
	}
	if outcome.MateriallyDifferent {
		parts = append(parts, "materially_different")
	}
	return strings.Join(parts, " ")
}

func renderLifecycleResolution(lifecycle background.LifecycleResolution) string {
	status := strings.TrimSpace(lifecycle.Status)
	if status == "" {
		status = "unknown"
	}
	parts := []string{status}
	if lifecycle.Terminal {
		parts = append(parts, "terminal")
	}
	if lifecycle.TerminalStateUnknown {
		parts = append(parts, "terminal_state_unknown")
	}
	if lifecycle.Reason != "" {
		parts = append(parts, lifecycle.Reason)
	}
	return strings.Join(parts, " ")
}

func (a *App) Agents(args []string) error {
	return a.AgentsWithOverrides(args, config.FlagOverrides{})
}

func (a *App) AgentsWithOverrides(args []string, overrides config.FlagOverrides) error {
	cleanArgs, format, err := stripJSONOnlyOutputFormat("agents", args)
	if err != nil {
		return err
	}
	args = cleanArgs
	if len(args) == 0 {
		return a.listAgents(format, "")
	}
	normalizedAction := normalizeAgentsAction(args[0])
	if normalizedAction != args[0] {
		args = append([]string{normalizedAction}, args[1:]...)
	}
	switch args[0] {
	case "help":
		return a.agentsHelpCommand(args, overrides, format)
	case "list":
		return a.agentsListCommand(args, overrides, format)
	case "show":
		return a.agentsShowCommand(args, overrides, format)
	case "create":
		return a.agentsCreateCommand(args, overrides, format)
	case "runs":
		return a.agentsRunsCommand(args, overrides, format)
	case "board":
		return a.agentsBoardCommand(args, overrides, format)
	case "status":
		return a.agentsStatusCommand(args, overrides, format)
	case "heartbeat":
		return a.agentsHeartbeatCommand(args, overrides, format)
	case "stop":
		return a.agentsStopCommand(args, overrides, format)
	case "update":
		return a.agentsUpdateCommand(args, overrides, format)
	case "output":
		return a.agentsOutputCommand(args, overrides, format)
	case "prune":
		return a.agentsPruneCommand(args, overrides, format)
	case "run-remove":
		return a.agentsRunRemoveCommand(args, overrides, format)
	case "worktrees":
		return a.agentsWorktreesCommand(args, overrides, format)
	case "worktree-remove":
		return a.agentsWorktreeRemoveCommand(args, overrides, format)
	case "run":
		return a.agentsRunCommand(args, overrides, format)
	default:
		return renderActionError(a.Out, actionErrorReport{
			Kind: "agents", Action: args[0], Status: "error", ErrorKind: "unknown_agents_subcommand",
			Message: fmt.Sprintf("unknown agents command %q", args[0]), Hint: unknownAgentsCommandHint(args[0]),
		}, format)
	}
}

func (a *App) agentsHelpCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) > 1 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents help",
			Args:    append([]string(nil), args[1:]...),
			Usage:   "codog agents help [--json|--output-format text|json]",
		}, format)
	}
	return renderAgentsHelp(a.Out, format)
}

func (a *App) agentsListCommand(args []string, overrides config.FlagOverrides, format string) error {
	filter, err := parseListFilterArgs("agents list", args[1:], "codog agents list [FILTER] [--json|--output-format text|json]", "unknown_option")
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.listAgents(format, filter)
}

func (a *App) agentsShowCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "show",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents show requires a name",
			Hint:      "Usage: codog agents show NAME.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents show",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog agents show NAME [--json|--output-format text|json]",
		}, format)
	}
	return a.showAgent(args[1], format)
}

func (a *App) agentsCreateCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "create",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents create requires a name",
			Hint:      "Usage: codog agents create NAME.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents create",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog agents create NAME [--json|--output-format text|json]",
		}, format)
	}
	return a.createAgent(args[1], format)
}

func (a *App) agentsRunsCommand(args []string, overrides config.FlagOverrides, format string) error {
	filter, err := parseListFilterArgs("agents runs", args[1:], "codog agents runs [AGENT] [--json|--output-format text|json]", "unknown_option")
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.listAgentRuns(format, filter)
}

func (a *App) agentsBoardCommand(args []string, overrides config.FlagOverrides, format string) error {
	stalledAfter, err := parseBackgroundBoardArgs(args[1:])
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.boardAgentRuns(stalledAfter, format)
}

func (a *App) agentsStatusCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "status",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents status requires a run id",
			Hint:      "Usage: codog agents status RUN_ID.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents status",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog agents status RUN_ID [--json|--output-format text|json]",
		}, format)
	}
	return a.showAgentRun(args[1], format)
}

func (a *App) agentsHeartbeatCommand(args []string, overrides config.FlagOverrides, format string) error {
	id, heartbeat, err := parseAgentRunHeartbeatArgs(args[1:])
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.heartbeatAgentRun(id, heartbeat, format)
}

func (a *App) agentsStopCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "stop",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents stop requires a run id",
			Hint:      "Usage: codog agents stop RUN_ID.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents stop",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog agents stop RUN_ID [--json|--output-format text|json]",
		}, format)
	}
	return a.stopAgentRun(args[1], format)
}

func (a *App) agentsUpdateCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 3 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "update",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents update requires a run id and message",
			Hint:      "Usage: codog agents update RUN_ID MESSAGE.",
		}, format)
	}
	return a.updateAgentRun(args[1], strings.Join(args[2:], " "), format)
}

func (a *App) agentsOutputCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "output",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents output requires a run id",
			Hint:      "Usage: codog agents output RUN_ID [bytes|--bytes N|--limit N].",
		}, format)
	}
	if len(args) > 4 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents output",
			Args:    append([]string(nil), args[4:]...),
			Usage:   "codog agents output RUN_ID [bytes|--bytes N|--limit N] [--json|--output-format text|json]",
		}, format)
	}
	id, limit, err := parseAgentRunOutputArgs(args[1:])
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.outputAgentRun(id, limit, format)
}

func (a *App) agentsPruneCommand(args []string, overrides config.FlagOverrides, format string) error {
	options, err := parseBackgroundPruneArgs(args[1:])
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	return a.pruneAgentRuns(options, format)
}

func (a *App) agentsRunRemoveCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "agents",
			Action:    "run-remove",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "agents run-remove requires a run id",
			Hint:      "Usage: codog agents run-remove RUN_ID.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "agents run-remove",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog agents run-remove RUN_ID [--json|--output-format text|json]",
		}, format)
	}
	return a.removeAgentRun(args[1], format)
}

func (a *App) agentsWorktreesCommand(args []string, overrides config.FlagOverrides, format string) error {
	allocations, err := worktree.List(a.Workspace)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(allocations, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) agentsWorktreeRemoveCommand(args []string, overrides config.FlagOverrides, format string) error {
	if len(args) < 2 {
		return errors.New("usage: codog agents worktree-remove ID")
	}
	allocation, err := worktree.Load(a.Workspace, args[1])
	if err != nil {
		return err
	}
	if err := worktree.Remove(a.Workspace, args[1]); err != nil {
		return err
	}
	if err := a.runWorktreeRemoveHook(context.Background(), allocation, "manual"); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{"removed": true, "id": args[1]}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) agentsRunCommand(args []string, overrides config.FlagOverrides, format string) error {
	req, err := parseAgentRunArgs(args[1:])
	if err != nil {
		return err
	}
	defs, err := a.runtimeAgentDefinitions()
	if err != nil {
		return err
	}
	var selected *agentdefs.Definition
	for i := range defs {
		if strings.EqualFold(defs[i].Name, req.Name) {
			selected = &defs[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown agent %q", req.Name)
	}
	exe, err := a.executablePath()
	if err != nil {
		return err
	}
	runWorkspace := a.Workspace
	var allocation *worktree.Allocation
	if req.Worktree {
		next, err := worktree.AllocateWithOptions(a.Workspace, selected.Name, worktree.Options{
			SymlinkDirectories: a.Config.Worktree.SymlinkDirectories,
			SparsePaths:        a.Config.Worktree.SparsePaths,
		})
		if err != nil {
			return err
		}
		allocation = &next
		runWorkspace = next.Path
		if err := a.runWorktreeCreateHook(context.Background(), next, "agent"); err != nil {
			_ = a.removeAllocatedWorktree(context.Background(), next, "create_hook_failed")
			return err
		}
	}
	command := buildAgentCommandWithPluginDirs(exe, *selected, req.Prompt, a.PluginDirs)
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		if allocation != nil {
			_ = a.removeAllocatedWorktree(context.Background(), *allocation, "run_failed")
		}
		return err
	}
	backgroundStore := background.NewStore(a.Config.ConfigHome)
	task, err := backgroundStore.RunWithOptions(command, runWorkspace, background.RunOptions{
		Kind:        "agent",
		AgentType:   selected.Name,
		SessionID:   sessionID,
		Prompt:      req.Prompt,
		Description: selected.Description,
	})
	if err != nil {
		if allocation != nil {
			_ = a.removeAllocatedWorktree(context.Background(), *allocation, "run_failed")
		}
		return err
	}
	run := agentruns.Run{
		ID:        "run-" + task.ID,
		Agent:     selected.Name,
		Prompt:    req.Prompt,
		Workspace: runWorkspace,
		SessionID: sessionID,
		TaskID:    task.ID,
		CreatedAt: task.StartedAt,
		UpdatedAt: task.StartedAt,
	}
	if allocation != nil {
		run.WorktreeID = allocation.ID
		run.WorktreePath = allocation.Path
		run.WorktreeRef = allocation.Ref
	}
	run, err = agentruns.NewStore(a.Config.ConfigHome).Save(run)
	if err != nil {
		_, _ = backgroundStore.Stop(task.ID)
		if allocation != nil {
			_ = a.removeAllocatedWorktree(context.Background(), *allocation, "run_registry_failed")
		}
		return err
	}
	report := agentRunReport{
		Kind:   "agents",
		Action: "run",
		Status: "ok",
		Agent:  selected.Name,
		RunID:  run.ID,
		Run:    run,
		Task:   task,
	}
	if allocation != nil {
		report.Worktree = allocation
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	a.runTaskCreatedHook(context.Background(), task)
	a.runSubagentStartHook(context.Background(), task.ID, selected.Name)
	return nil
}

// Subagent maps Claude-style subagent run controls onto Codog agent runs.
func (a *App) Subagent(args []string, overrides config.FlagOverrides) error {
	agentsArgs, err := normalizeSubagentArgs(args)
	if err != nil {
		clean, format, stripErr := stripJSONOnlyOutputFormat("subagent", args)
		if stripErr != nil {
			return stripErr
		}
		action := "list"
		if len(clean) > 0 {
			action = strings.ToLower(strings.TrimSpace(clean[0]))
		}
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "subagent",
			Action:    action,
			Status:    "error",
			ErrorKind: "invalid_subagent_command",
			Message:   err.Error(),
			Hint:      "Usage: codog subagent [list [AGENT]|steer RUN_ID MESSAGE|kill RUN_ID|status RUN_ID|logs RUN_ID] [--output-format text|json].",
		}, format)
	}
	return a.AgentsWithOverrides(agentsArgs, overrides)
}

func normalizeAgentsAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "help", "-h", "--help":
		return "help"
	case "", "list", "ls":
		return "list"
	case "show", "info", "describe", "details":
		return "show"
	case "create", "new", "add":
		return "create"
	case "runs", "tasks":
		return "runs"
	case "board", "lane-board", "lanes":
		return "board"
	case "status", "run-status":
		return "status"
	case "update", "message":
		return "update"
	case "output", "logs":
		return "output"
	case "run-remove", "run-rm":
		return "run-remove"
	case "worktree-remove", "worktree-rm":
		return "worktree-remove"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

var agentsActionCandidates = []string{
	"help", "list", "ls", "show", "info", "describe", "details", "create", "new", "add",
	"run", "runs", "tasks", "board", "lane-board", "lanes", "status", "run-status",
	"heartbeat", "stop", "update", "message", "output", "logs", "prune", "run-remove",
	"run-rm", "worktrees", "worktree-remove", "worktree-rm",
}

func unknownAgentsCommandHint(action string) string {
	suggestions := toolnames.Suggestions(action, agentsActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog agents %s`? Use `codog agents help` to list supported commands.", suggestions[0])
	case 0:
		return "Use `codog agents list`, `codog agents show|info|describe NAME`, `codog agents create NAME`, `codog agents run NAME PROMPT`, `codog agents runs`, `codog agents board`, `codog agents status RUN_ID`, or `codog agents worktrees`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog agents help` to list supported commands.", strings.Join(suggestions, ", "))
	}
}

func normalizeSubagentArgs(args []string) ([]string, error) {
	clean, format, err := stripJSONOnlyOutputFormat("subagent", args)
	if err != nil {
		return nil, err
	}
	action := "list"
	rest := clean
	if len(rest) > 0 {
		action = strings.ToLower(strings.TrimSpace(rest[0]))
		rest = rest[1:]
	}
	var mapped []string
	switch action {
	case "", "list", "ls":
		mapped = append([]string{"runs"}, rest...)
	case "steer", "message", "update":
		if len(rest) < 2 {
			return nil, errors.New("subagent steer requires a run id and message")
		}
		mapped = append([]string{"update"}, rest...)
	case "kill", "stop":
		if len(rest) != 1 {
			return nil, errors.New("subagent kill requires exactly one run id")
		}
		mapped = append([]string{"stop"}, rest...)
	case "status", "show":
		if len(rest) != 1 {
			return nil, errors.New("subagent status requires exactly one run id")
		}
		mapped = append([]string{"status"}, rest...)
	case "logs", "output":
		if len(rest) < 1 {
			return nil, errors.New("subagent logs requires a run id")
		}
		mapped = append([]string{"output"}, rest...)
	default:
		return nil, fmt.Errorf("unknown subagent command %q", action)
	}
	if format != "" {
		mapped = append(mapped, "--output-format", format)
	}
	return mapped, nil
}

type agentRunRequest struct {
	Name     string
	Prompt   string
	Worktree bool
}

func parseAgentRunArgs(args []string) (agentRunRequest, error) {
	var req agentRunRequest
	if len(args) > 0 && args[0] == "--worktree" {
		req.Worktree = true
		args = args[1:]
	}
	if len(args) < 2 {
		return agentRunRequest{}, errors.New("usage: codog agents run [--worktree] NAME PROMPT")
	}
	req.Name = args[0]
	req.Prompt = strings.Join(args[1:], " ")
	return req, nil
}

func parseAgentRunOutputArgs(args []string) (string, int64, error) {
	if len(args) == 0 {
		return "", 0, errors.New("usage: codog agents output RUN_ID [bytes|--bytes N|--limit N]")
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", 0, errors.New("agent run id is required")
	}
	_, limit, err := parseBackgroundLogsArgs(append([]string{id}, args[1:]...))
	return id, limit, err
}

func parseAgentRunHeartbeatArgs(args []string) (string, background.LaneHeartbeat, error) {
	if len(args) == 0 {
		return "", background.LaneHeartbeat{}, errors.New("usage: codog agents heartbeat RUN_ID [--status STATUS] [--transport-alive true|false] [--observed-at RFC3339] [--source-kind KIND] [--environment ENV] [--channel CHANNEL] [--emitter EMITTER] [--confidence LEVEL]")
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		return "", background.LaneHeartbeat{}, errors.New("agent run id is required")
	}
	_, heartbeat, err := parseBackgroundHeartbeatArgs(append([]string{id}, args[1:]...))
	return id, heartbeat, err
}

func buildAgentCommand(exe string, def agentdefs.Definition, prompt string) string {
	return buildAgentCommandWithPluginDirs(exe, def, prompt, nil)
}

func buildAgentCommandWithPluginDirs(exe string, def agentdefs.Definition, prompt string, pluginDirs []string) string {
	combined := strings.TrimSpace(strings.Join([]string{def.Prompt, prompt}, "\n\n"))
	args := []string{shellQuote(exe)}
	if def.Model != "" {
		args = append(args, "--model", shellQuote(def.Model))
	}
	if len(def.Tools) > 0 {
		args = append(args, "--tools", shellQuote(strings.Join(def.Tools, ",")))
	}
	for _, dir := range pluginDirs {
		if strings.TrimSpace(dir) != "" {
			args = append(args, "--plugin-dir", shellQuote(strings.TrimSpace(dir)))
		}
	}
	args = append(args, "prompt", shellQuote(combined))
	return strings.Join(args, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (a *App) ListPlugins() error {
	return a.listPlugins("text")
}

type pluginsListSummary struct {
	Total               int `json:"total"`
	Enabled             int `json:"enabled"`
	Disabled            int `json:"disabled"`
	LifecycleConfigured int `json:"lifecycle_configured"`
}

type pluginsListReport struct {
	Kind                string              `json:"kind"`
	Action              string              `json:"action"`
	Status              string              `json:"status"`
	Summary             pluginsListSummary  `json:"summary"`
	Plugins             []plugins.Manifest  `json:"plugins"`
	ConfigLoadError     *string             `json:"config_load_error"`
	ConfigLoadErrorKind string              `json:"config_load_error_kind,omitempty"`
	LoadFailures        []map[string]string `json:"load_failures"`
}

type marketplaceSourceInfo struct {
	URL                 string `json:"url"`
	PublicKeyConfigured bool   `json:"public_key_configured"`
}

type marketplaceSourcesReport struct {
	Kind    string                  `json:"kind"`
	Action  string                  `json:"action"`
	Status  string                  `json:"status"`
	Target  string                  `json:"target,omitempty"`
	Path    string                  `json:"path,omitempty"`
	URL     string                  `json:"url,omitempty"`
	Added   bool                    `json:"added,omitempty"`
	Removed bool                    `json:"removed,omitempty"`
	Cleared bool                    `json:"cleared,omitempty"`
	Sources []marketplaceSourceInfo `json:"sources"`
}

type marketplaceSettingsReport struct {
	Kind             string                  `json:"kind"`
	Action           string                  `json:"action"`
	Status           string                  `json:"status"`
	PluginRoot       string                  `json:"plugin_root"`
	InstalledPlugins int                     `json:"installed_plugins"`
	EnabledPlugins   int                     `json:"enabled_plugins"`
	Sources          []marketplaceSourceInfo `json:"sources"`
}

type marketplaceRemoteRequest struct {
	Action    string
	URL       string
	PublicKey string
	Query     string
	ID        string
	Page      int
	PerPage   int
}

type marketplaceRemotePlugin struct {
	MarketplaceURL string `json:"marketplace_url"`
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	Version        string `json:"version,omitempty"`
	Description    string `json:"description,omitempty"`
	URL            string `json:"url,omitempty"`
	ResolvedURL    string `json:"resolved_url,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	SignatureValid bool   `json:"signature_valid,omitempty"`
	InstallCommand string `json:"install_command,omitempty"`
	UpdateCommand  string `json:"update_command,omitempty"`
}

type marketplacePagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
	Start      int `json:"start"`
	End        int `json:"end"`
}

type marketplaceRemoteReport struct {
	Kind        string                    `json:"kind"`
	Action      string                    `json:"action"`
	Status      string                    `json:"status"`
	Message     string                    `json:"message,omitempty"`
	NextCommand string                    `json:"next_command,omitempty"`
	Query       string                    `json:"query,omitempty"`
	ID          string                    `json:"id,omitempty"`
	Sources     []marketplaceSourceInfo   `json:"sources"`
	Plugins     []marketplaceRemotePlugin `json:"plugins,omitempty"`
	Plugin      *marketplaceRemotePlugin  `json:"plugin,omitempty"`
	Pagination  *marketplacePagination    `json:"pagination,omitempty"`
	Total       int                       `json:"total"`
}

type marketplaceSourcesRequest struct {
	Action    string
	URL       string
	PublicKey string
	Target    string
	Path      string
}

func normalizePluginAction(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "list", "ls":
		return "list"
	case "health", "healthcheck", "lifecycle":
		return "health"
	case "show", "info", "describe", "details", "detail":
		return "show"
	case "sources", "source", "marketplaces", "manage-marketplaces":
		return "sources"
	case "settings", "config":
		return "settings"
	case "install", "add":
		return "install"
	case "install-remote", "remote-install":
		return "install-remote"
	case "update", "upgrade":
		return "update"
	case "remove", "rm", "delete", "uninstall":
		return "remove"
	case "enable", "on":
		return "enable"
	case "disable", "off":
		return "disable"
	case "validate", "check":
		return "validate"
	case "remote", "browse", "discover":
		return "remote"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

var pluginsActionCandidates = []string{
	"list", "ls", "health", "healthcheck", "lifecycle", "show", "info", "describe", "details", "detail",
	"sources", "source", "marketplaces", "manage-marketplaces", "add-marketplace", "remove-marketplace",
	"delete-marketplace", "settings", "config", "remote", "browse", "discover", "updates", "install",
	"add", "install-remote", "remote-install", "update", "upgrade", "enable", "on", "disable", "off",
	"remove", "rm", "delete", "uninstall", "validate", "check",
}

func unknownPluginsActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, pluginsActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog plugins %s`? Use `codog plugins list` or `codog plugins help` to inspect supported actions.", suggestions[0])
	case 0:
		return "Use `codog plugins list`, `health`, `show|info|describe`, `validate`, `sources`, `remote`, `updates`, `install`, `enable`, `disable`, or `remove`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog plugins list` or `codog plugins help` to inspect supported actions.", strings.Join(suggestions, ", "))
	}
}

func normalizeMarketplaceAction(raw string) string {
	action := strings.ToLower(strings.TrimSpace(raw))
	switch action {
	case "", "list", "show", "info", "describe", "health", "healthcheck", "lifecycle", "validate",
		"sources", "source", "marketplaces", "manage-marketplaces", "add-marketplace", "remove-marketplace",
		"delete-marketplace", "settings", "remote", "browse", "discover", "updates", "install", "install-remote",
		"update", "enable", "disable", "remove", "uninstall":
		return action
	default:
		return normalizePluginAction(raw)
	}
}

func pluginManifestSurfaces(manifest plugins.Manifest) []string {
	surfaces := []string{}
	if len(manifest.Tools) > 0 {
		surfaces = append(surfaces, "tools")
	}
	if len(manifest.Commands) > 0 {
		surfaces = append(surfaces, "commands")
	}
	if len(manifest.Skills) > 0 {
		surfaces = append(surfaces, "skills")
	}
	if len(manifest.Agents) > 0 {
		surfaces = append(surfaces, "agents")
	}
	if len(manifest.Hooks) > 0 {
		surfaces = append(surfaces, "hooks")
	}
	if len(manifest.MCPServers) > 0 {
		surfaces = append(surfaces, "mcp_servers")
	}
	return surfaces
}

func validatePluginSource(source string) plugins.ValidationResult {
	result, err := plugins.Validate(source)
	if err != nil {
		result.Success = false
		result.Errors = []plugins.ValidationMessage{{Path: "file", Message: err.Error(), Code: "validation_failed"}}
	}
	return result
}

func (a *App) listPlugins(format string) error {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return err
	}
	if format == "json" {
		renderPluginsListReport(a.Out, format, buildPluginsListReport(manifests, "", ""))
		return nil
	}
	data, _ := json.MarshalIndent(manifests, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func buildPluginsListReport(manifests []plugins.Manifest, configLoadError string, configLoadErrorKind string) pluginsListReport {
	summary := pluginsListSummary{Total: len(manifests)}
	for _, manifest := range manifests {
		if manifest.Enabled {
			summary.Enabled++
		} else {
			summary.Disabled++
		}
		if !manifest.Lifecycle.Empty() {
			summary.LifecycleConfigured++
		}
	}
	status := "ok"
	var loadError *string
	if strings.TrimSpace(configLoadError) != "" {
		status = "degraded"
		value := strings.TrimSpace(configLoadError)
		loadError = &value
		if strings.TrimSpace(configLoadErrorKind) == "" {
			configLoadErrorKind = "config_load_failed"
		}
	}
	return pluginsListReport{
		Kind:                "plugin",
		Action:              "list",
		Status:              status,
		Summary:             summary,
		Plugins:             append([]plugins.Manifest(nil), manifests...),
		ConfigLoadError:     loadError,
		ConfigLoadErrorKind: strings.TrimSpace(configLoadErrorKind),
		LoadFailures:        []map[string]string{},
	}
}

func renderPluginsListReport(out io.Writer, format string, report pluginsListReport) {
	if format == "text" {
		if report.ConfigLoadError == nil {
			data, _ := json.MarshalIndent(report.Plugins, "", "  ")
			fmt.Fprintln(out, string(data))
			return
		}
		fmt.Fprintln(out, "Plugins")
		fmt.Fprintf(out, "  Status           %s\n", report.Status)
		fmt.Fprintf(out, "  Total            %d\n", report.Summary.Total)
		fmt.Fprintf(out, "  Enabled          %d\n", report.Summary.Enabled)
		fmt.Fprintf(out, "  Disabled         %d\n", report.Summary.Disabled)
		fmt.Fprintf(out, "  Config load      degraded: %s\n", *report.ConfigLoadError)
		fmt.Fprintln(out, "  Hint             Fix the listed config file or run `codog doctor` for details.")
		return
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(out, string(data))
}

func (a *App) Marketplace(args []string) error {
	cleanArgs, format, err := stripJSONOnlyOutputFormat("marketplace", args)
	if err != nil {
		return err
	}
	args = cleanArgs
	if len(args) == 0 {
		return a.marketplaceListCommand(args, format)
	}
	normalizedAction := normalizeMarketplaceAction(args[0])
	if normalizedAction != args[0] {
		args = append([]string{normalizedAction}, args[1:]...)
	}
	switch args[0] {
	case "list":
		return a.marketplaceListCommand(args, format)
	case "sources", "source", "marketplaces", "manage-marketplaces":
		return a.marketplaceSourcesRouteCommand(args, format)
	case "add-marketplace":
		return a.marketplaceAddMarketplaceCommand(args, format)
	case "remove-marketplace", "delete-marketplace":
		return a.marketplaceRemoveMarketplaceCommand(args, format)
	case "settings":
		return a.marketplaceSettingsCommand(args, format)
	case "health", "healthcheck", "lifecycle":
		return a.marketplaceHealthCommand(args, format)
	case "remote", "browse", "discover":
		return a.marketplaceRemoteCommand(args, format)
	case "updates":
		return a.marketplaceUpdatesCommand(args, format)
	case "install":
		return a.marketplaceInstallCommand(args, format)
	case "validate":
		return a.marketplaceValidateCommand(args, format)
	case "install-remote":
		return a.marketplaceInstallRemoteCommand(args, format)
	case "update":
		return a.marketplaceUpdateCommand(args, format)
	case "enable":
		return a.marketplaceEnableCommand(args, format)
	case "disable":
		return a.marketplaceDisableCommand(args, format)
	case "remove", "uninstall":
		return a.marketplaceRemoveCommand(args, format)
	case "show", "info", "describe":
		return a.marketplaceShowCommand(args, format)
	default:
		return renderActionError(a.Out, actionErrorReport{
			Kind: "plugins", Action: args[0], Status: "error", ErrorKind: "unknown_plugins_action",
			Message: fmt.Sprintf("unknown plugins action %q", args[0]), Hint: unknownPluginsActionHint(args[0]),
		}, format)
	}
}

func (a *App) marketplaceListCommand(args []string, format string) error {
	if len(args) > 1 {
		if option := firstFlagShapedArg(args[1:]); option != "" {
			return renderCLIError(a.Out, unknownOptionError{
				Kind: "cli_parse", Command: "plugins list", Option: option,
				Usage: "codog plugins list [--json|--output-format text|json]",
			}, format)
		}
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "plugins list", Args: append([]string(nil), args[1:]...),
			Usage: "codog plugins list [--json|--output-format text|json]",
		}, format)
	}
	return a.listPlugins(format)
}

func (a *App) marketplaceSourcesRouteCommand(args []string, format string) error {
	return a.marketplaceSourcesCommand(args[1:], format)
}

func (a *App) marketplaceAddMarketplaceCommand(args []string, format string) error {
	return a.marketplaceSourcesCommand(append([]string{"add"}, args[1:]...), format)
}

func (a *App) marketplaceRemoveMarketplaceCommand(args []string, format string) error {
	return a.marketplaceSourcesCommand(append([]string{"remove"}, args[1:]...), format)
}

func (a *App) marketplaceSettingsCommand(_ []string, format string) error {
	return a.marketplaceSettings(format)
}

func (a *App) marketplaceHealthCommand(args []string, format string) error {
	if args[0] == "lifecycle" && len(args) > 1 {
		return a.pluginLifecycleRun(context.Background(), args[1:], format)
	}
	report := a.pluginHealthReport(args[0])
	return renderPluginHealthReport(a.Out, report, format)
}

func (a *App) marketplaceRemoteCommand(args []string, format string) error {
	var payload any
	structuredRemote := marketplaceRemoteUsesStructuredReport(args[1:])
	if args[0] != "remote" && len(args) == 1 {
		structuredRemote = false
	}
	if structuredRemote {
		report, err := a.marketplaceRemoteReport(args[1:])
		if err != nil {
			return err
		}
		payload = report
	} else {
		indexes, err := a.marketplaceRemote(args[1:])
		if err != nil {
			return err
		}
		payload = indexes
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceUpdatesCommand(args []string, format string) error {
	var payload any
	updates, err := a.marketplaceUpdates(args[1:])
	if err != nil {
		return err
	}
	payload = updates
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceInstallCommand(args []string, format string) error {
	var payload any
	if len(args) < 2 {
		return errors.New("usage: codog marketplace install PATH")
	}
	manifest, err := plugins.Install(a.Workspace, args[1])
	if err != nil {
		if os.IsNotExist(err) {
			return renderPluginSourceNotFound(a.Out, args[0], args[1], format)
		}
		return err
	}
	payload = manifest
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceValidateCommand(args []string, format string) error {
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "plugins",
			Action:    args[0],
			Status:    "error",
			ErrorKind: "plugin_source_required",
			Message:   "plugin validation requires a source path",
			Hint:      "Usage: codog plugins validate PATH [--json|--output-format text|json]",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "plugins validate",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog plugins validate PATH [--json|--output-format text|json]",
		}, format)
	}
	result, err := plugins.Validate(args[1])
	if err != nil {
		if os.IsNotExist(err) {
			return renderPluginSourceNotFound(a.Out, args[0], args[1], format)
		}
		return err
	}
	return renderPluginValidation(a.Out, args[1], result, format)
}

func (a *App) marketplaceInstallRemoteCommand(args []string, format string) error {
	var payload any
	result, err := a.marketplaceInstallRemote(args[1:])
	if err != nil {
		return err
	}
	payload = result
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceUpdateCommand(args []string, format string) error {
	var payload any
	result, err := a.marketplaceUpdate(args[1:])
	if err != nil {
		return err
	}
	payload = result
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceEnableCommand(args []string, format string) error {
	var payload any
	if len(args) < 2 {
		return errors.New("usage: codog marketplace enable ID")
	}
	manifest, err := plugins.Enable(a.Workspace, args[1])
	if err != nil {
		if os.IsNotExist(err) {
			return renderPluginNotFound(a.Out, args[0], args[1], format)
		}
		return err
	}
	payload = manifest
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceDisableCommand(args []string, format string) error {
	var payload any
	if len(args) < 2 {
		return errors.New("usage: codog marketplace disable ID")
	}
	manifest, err := plugins.Disable(a.Workspace, args[1])
	if err != nil {
		if os.IsNotExist(err) {
			return renderPluginNotFound(a.Out, args[0], args[1], format)
		}
		return err
	}
	payload = manifest
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceRemoveCommand(args []string, format string) error {
	var payload any
	if len(args) < 2 {
		return errors.New("usage: codog marketplace remove ID")
	}
	if err := plugins.Remove(a.Workspace, args[1]); err != nil {
		if os.IsNotExist(err) {
			return renderPluginNotFound(a.Out, args[0], args[1], format)
		}
		return err
	}
	payload = map[string]any{"removed": true, "id": args[1]}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) marketplaceShowCommand(args []string, format string) error {
	var payload any
	if len(args) < 2 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "plugins",
			Action:    "show",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "plugins show requires an ID",
			Hint:      "Usage: codog plugins show ID.",
		}, format)
	}
	if len(args) > 2 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "plugins show",
			Args:    append([]string(nil), args[2:]...),
			Usage:   "codog plugins show ID [--json|--output-format text|json]",
		}, format)
	}
	manifest, err := a.findPlugin(args[1])
	if err != nil {
		if errors.Is(err, errPluginNotFound) {
			return renderPluginNotFound(a.Out, "show", args[1], format)
		}
		return err
	}
	payload = map[string]any{"kind": "plugin", "action": "show", "status": "ok", "plugin": manifest}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

var errPluginNotFound = errors.New("plugin not found")

func (a *App) findPlugin(id string) (plugins.Manifest, error) {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return plugins.Manifest{}, err
	}
	for _, manifest := range manifests {
		if strings.EqualFold(manifest.ID, id) || strings.EqualFold(manifest.Name, id) {
			return manifest, nil
		}
	}
	return plugins.Manifest{}, errPluginNotFound
}

func renderPluginNotFound(out io.Writer, action string, id string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "plugins",
		Action:    action,
		Status:    "error",
		ErrorKind: "plugin_not_found",
		Message:   fmt.Sprintf("plugin %q was not found", id),
		Hint:      "Run `codog plugins list` to see installed plugins, then retry with one of those IDs.",
	}, format)
}

func renderPluginSourceNotFound(out io.Writer, action string, source string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "plugins",
		Action:    action,
		Status:    "error",
		ErrorKind: "plugin_source_not_found",
		Message:   fmt.Sprintf("plugin source %q was not found", source),
		Hint:      "Pass a directory containing plugin.json or the path to a plugin.json file.",
	}, format)
}

type pluginValidationReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Source string `json:"source"`
	plugins.ValidationResult
}

type pluginHealthReport struct {
	Kind         string              `json:"kind"`
	Action       string              `json:"action"`
	Status       string              `json:"status"`
	Workspace    string              `json:"workspace"`
	PluginRoot   string              `json:"plugin_root"`
	Total        int                 `json:"total"`
	Healthy      int                 `json:"healthy"`
	Degraded     int                 `json:"degraded"`
	Failed       int                 `json:"failed"`
	Stopped      int                 `json:"stopped"`
	Unconfigured int                 `json:"unconfigured"`
	Plugins      []pluginHealthcheck `json:"plugins"`
	LoadError    string              `json:"load_error,omitempty"`
	Message      string              `json:"message,omitempty"`
}

type pluginHealthcheck struct {
	PluginID       string               `json:"plugin_id"`
	Name           string               `json:"name,omitempty"`
	Enabled        bool                 `json:"enabled"`
	State          string               `json:"state"`
	Lifecycle      pluginLifecycleInfo  `json:"lifecycle"`
	LifecycleState string               `json:"lifecycle_state"`
	StartupEvent   string               `json:"startup_event,omitempty"`
	Servers        []pluginServerHealth `json:"servers,omitempty"`
	DegradedMode   *pluginDegradedMode  `json:"degraded_mode,omitempty"`
	Surfaces       []string             `json:"surfaces,omitempty"`
	Errors         []string             `json:"errors,omitempty"`
	Warnings       []string             `json:"warnings,omitempty"`
	Available      []string             `json:"available_capabilities,omitempty"`
	Unavailable    []string             `json:"unavailable_capabilities,omitempty"`
	LastCheckUnix  int64                `json:"last_check_unix"`
}

type pluginLifecycleInfo struct {
	Configured bool `json:"configured"`
	Init       struct {
		Configured   bool `json:"configured"`
		CommandCount int  `json:"command_count"`
	} `json:"init"`
	Shutdown struct {
		Configured   bool `json:"configured"`
		CommandCount int  `json:"command_count"`
	} `json:"shutdown"`
}

type pluginServerHealth struct {
	ServerName   string   `json:"server_name"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
}

type pluginDegradedMode struct {
	AvailableCapabilities   []string `json:"available_capabilities,omitempty"`
	UnavailableCapabilities []string `json:"unavailable_capabilities,omitempty"`
	Reason                  string   `json:"reason"`
}

func (a *App) pluginHealthReport(action string) pluginHealthReport {
	manifests, err := a.runtimePluginManifests()
	report := pluginHealthReport{
		Kind:       "plugin_health",
		Action:     normalizePluginHealthAction(action),
		Status:     "healthy",
		Workspace:  a.Workspace,
		PluginRoot: plugins.Root(a.Workspace),
	}
	if err != nil {
		report.Status = "failed"
		report.LoadError = err.Error()
		report.Message = "plugin health could not load installed plugin manifests"
		return report
	}
	report.Total = len(manifests)
	report.Plugins = make([]pluginHealthcheck, 0, len(manifests))
	for _, manifest := range manifests {
		check := pluginHealthcheckForManifest(manifest)
		report.Plugins = append(report.Plugins, check)
		switch check.State {
		case "healthy":
			report.Healthy++
		case "degraded":
			report.Degraded++
		case "failed":
			report.Failed++
		case "stopped":
			report.Stopped++
		default:
			report.Unconfigured++
		}
	}
	switch {
	case report.Failed > 0:
		report.Status = "failed"
	case report.Degraded > 0:
		report.Status = "degraded"
	case report.Total == 0:
		report.Status = "unconfigured"
	case report.Healthy > 0:
		report.Status = "healthy"
	default:
		report.Status = "stopped"
	}
	report.Message = pluginHealthMessage(report)
	return report
}

func normalizePluginHealthAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "healthcheck":
		return "healthcheck"
	case "lifecycle":
		return "lifecycle"
	default:
		return "health"
	}
}

func pluginHealthcheckForManifest(manifest plugins.Manifest) pluginHealthcheck {
	validation := validatePluginSource(manifest.Root)
	check := pluginHealthcheck{
		PluginID:       manifest.ID,
		Name:           manifest.Name,
		Enabled:        manifest.Enabled,
		State:          "healthy",
		Lifecycle:      pluginLifecycleInfoForManifest(manifest),
		LifecycleState: pluginLifecycleState(manifest),
		Surfaces:       pluginManifestSurfaces(manifest),
		Servers:        pluginServerHealthForManifest(manifest),
		LastCheckUnix:  time.Now().Unix(),
	}
	for _, issue := range validation.Errors {
		check.Errors = append(check.Errors, issue.Message)
	}
	for _, issue := range validation.Warnings {
		check.Warnings = append(check.Warnings, issue.Message)
	}
	check.Available = pluginAvailableCapabilities(manifest, check.Servers)
	check.Unavailable = pluginUnavailableCapabilities(check.Servers)
	switch {
	case !manifest.Enabled:
		check.State = "stopped"
	case len(check.Errors) > 0:
		if len(check.Available) == 0 {
			check.State = "failed"
		} else {
			check.State = "degraded"
			check.DegradedMode = pluginDegradedModeFor(check.Available, check.Unavailable, "manifest validation errors leave only partial plugin functionality available")
		}
	case len(check.Surfaces) == 0:
		check.State = "unconfigured"
	case pluginServerFailureCount(check.Servers) > 0:
		if len(check.Available) == 0 {
			check.State = "failed"
		} else {
			check.State = "degraded"
			check.DegradedMode = pluginDegradedModeFor(check.Available, check.Unavailable, fmt.Sprintf("%d capabilities available, %d unavailable", len(check.Available), len(check.Unavailable)))
		}
	}
	check.StartupEvent = pluginStartupEvent(check.State)
	sort.Strings(check.Available)
	sort.Strings(check.Unavailable)
	return check
}

func pluginLifecycleInfoForManifest(manifest plugins.Manifest) pluginLifecycleInfo {
	info := pluginLifecycleInfo{Configured: !manifest.Lifecycle.Empty()}
	info.Init.Configured = len(manifest.Lifecycle.Init) != 0
	info.Init.CommandCount = len(manifest.Lifecycle.Init)
	info.Shutdown.Configured = len(manifest.Lifecycle.Shutdown) != 0
	info.Shutdown.CommandCount = len(manifest.Lifecycle.Shutdown)
	return info
}

func pluginLifecycleState(manifest plugins.Manifest) string {
	if !manifest.Enabled {
		return "disabled"
	}
	if manifest.Lifecycle.Empty() {
		return "unconfigured"
	}
	return "ready"
}

func pluginServerHealthForManifest(manifest plugins.Manifest) []pluginServerHealth {
	names := make([]string, 0, len(manifest.MCPServers))
	for name := range manifest.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]pluginServerHealth, 0, len(names))
	for _, name := range names {
		server := manifest.MCPServers[name]
		status := "healthy"
		lastError := ""
		if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
			status = "failed"
			lastError = "missing command or url"
		}
		capabilities := []string{"mcp:" + name}
		out = append(out, pluginServerHealth{
			ServerName:   name,
			Status:       status,
			Capabilities: capabilities,
			LastError:    lastError,
		})
	}
	return out
}

func pluginAvailableCapabilities(manifest plugins.Manifest, servers []pluginServerHealth) []string {
	capabilities := []string{}
	for _, tool := range manifest.Tools {
		if strings.TrimSpace(tool.Name) != "" {
			capabilities = append(capabilities, "tool:"+tool.Name)
		}
	}
	for _, command := range manifest.Commands {
		if strings.TrimSpace(command) != "" {
			capabilities = append(capabilities, "command:"+command)
		}
	}
	for _, skill := range manifest.Skills {
		if strings.TrimSpace(skill) != "" {
			capabilities = append(capabilities, "skill:"+skill)
		}
	}
	for _, agent := range manifest.Agents {
		if strings.TrimSpace(agent) != "" {
			capabilities = append(capabilities, "agent:"+agent)
		}
	}
	for _, hook := range manifest.Hooks {
		if strings.TrimSpace(hook) != "" {
			capabilities = append(capabilities, "hook:"+hook)
		}
	}
	if len(manifest.Lifecycle.Init) != 0 {
		capabilities = append(capabilities, "lifecycle:init")
	}
	if len(manifest.Lifecycle.Shutdown) != 0 {
		capabilities = append(capabilities, "lifecycle:shutdown")
	}
	for _, server := range servers {
		if server.Status != "failed" {
			capabilities = append(capabilities, server.Capabilities...)
		}
	}
	sort.Strings(capabilities)
	return capabilities
}

func pluginUnavailableCapabilities(servers []pluginServerHealth) []string {
	capabilities := []string{}
	for _, server := range servers {
		if server.Status == "failed" {
			capabilities = append(capabilities, server.Capabilities...)
		}
	}
	sort.Strings(capabilities)
	return capabilities
}

func pluginDegradedModeFor(available []string, unavailable []string, reason string) *pluginDegradedMode {
	return &pluginDegradedMode{
		AvailableCapabilities:   append([]string(nil), available...),
		UnavailableCapabilities: append([]string(nil), unavailable...),
		Reason:                  reason,
	}
}

func pluginServerFailureCount(servers []pluginServerHealth) int {
	count := 0
	for _, server := range servers {
		if server.Status == "failed" {
			count++
		}
	}
	return count
}

func pluginStartupEvent(state string) string {
	switch state {
	case "healthy":
		return "startup_healthy"
	case "degraded":
		return "startup_degraded"
	case "failed":
		return "startup_failed"
	default:
		return ""
	}
}

func pluginHealthMessage(report pluginHealthReport) string {
	switch report.Status {
	case "healthy":
		return "all enabled plugin startup surfaces are healthy"
	case "degraded":
		return "some plugin startup surfaces are degraded but usable capabilities remain"
	case "failed":
		return "one or more plugin startup surfaces failed"
	case "stopped":
		return "installed plugins are disabled"
	default:
		return "no installed plugins were found"
	}
}

func renderPluginHealthReport(out io.Writer, report pluginHealthReport, format string) error {
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Plugin Health")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Plugins          total=%d healthy=%d degraded=%d failed=%d stopped=%d unconfigured=%d\n", report.Total, report.Healthy, report.Degraded, report.Failed, report.Stopped, report.Unconfigured)
	if report.LoadError != "" {
		fmt.Fprintf(out, "  Load error       %s\n", report.LoadError)
	}
	for _, check := range report.Plugins {
		fmt.Fprintf(out, "  - %s %-10s enabled=%t", check.PluginID, check.State, check.Enabled)
		if check.StartupEvent != "" {
			fmt.Fprintf(out, " event=%s", check.StartupEvent)
		}
		if check.LifecycleState != "" {
			fmt.Fprintf(out, " lifecycle=%s", check.LifecycleState)
		}
		fmt.Fprintln(out)
		if len(check.Errors) > 0 {
			fmt.Fprintf(out, "    errors=%s\n", strings.Join(check.Errors, "; "))
		}
		if check.DegradedMode != nil {
			fmt.Fprintf(out, "    degraded=%s\n", check.DegradedMode.Reason)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	return nil
}

type pluginLifecycleRunRequest struct {
	Phase     string
	PluginID  string
	TimeoutMS int
}

type pluginLifecycleRunReport struct {
	Kind      string                       `json:"kind"`
	Action    string                       `json:"action"`
	Status    string                       `json:"status"`
	Phase     string                       `json:"phase"`
	PluginID  string                       `json:"plugin_id,omitempty"`
	TimeoutMS int                          `json:"timeout_ms"`
	Results   []plugins.LifecycleRunResult `json:"results"`
	Message   string                       `json:"message,omitempty"`
}

func (a *App) pluginLifecycleRun(ctx context.Context, args []string, format string) error {
	req, err := parsePluginLifecycleRunArgs(args)
	if err != nil {
		return renderCLIError(a.Out, err, format)
	}
	report, runErr := a.buildPluginLifecycleRunReport(ctx, req)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		renderPluginLifecycleRunReport(a.Out, report)
	}
	return runErr
}

func parsePluginLifecycleRunArgs(args []string) (pluginLifecycleRunRequest, error) {
	const usage = "codog plugins lifecycle run init|shutdown [PLUGIN_ID] [--timeout-ms N] [--json|--output-format text|json]"
	req := pluginLifecycleRunRequest{TimeoutMS: int(plugins.LifecycleDefaultTimeout / time.Millisecond)}
	rest := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--timeout-ms":
			index++
			if index >= len(args) {
				return req, requiredArgumentError{Command: "plugins lifecycle run", Argument: "--timeout-ms", Usage: usage}
			}
			timeout, err := strconv.Atoi(args[index])
			if err != nil || timeout < 0 {
				return req, fmt.Errorf("plugins lifecycle --timeout-ms must be a non-negative integer")
			}
			req.TimeoutMS = timeout
		case strings.HasPrefix(arg, "--timeout-ms="):
			timeout, err := strconv.Atoi(strings.TrimPrefix(arg, "--timeout-ms="))
			if err != nil || timeout < 0 {
				return req, fmt.Errorf("plugins lifecycle --timeout-ms must be a non-negative integer")
			}
			req.TimeoutMS = timeout
		default:
			rest = append(rest, arg)
		}
	}
	if len(rest) > 0 && strings.EqualFold(rest[0], "run") {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return req, requiredArgumentError{Command: "plugins lifecycle run", Argument: "init|shutdown", Usage: usage}
	}
	phase, err := plugins.NormalizeLifecyclePhase(rest[0])
	if err != nil {
		return req, err
	}
	req.Phase = phase
	if len(rest) > 1 {
		req.PluginID = strings.TrimSpace(rest[1])
	}
	if len(rest) > 2 {
		return req, unexpectedExtraArgsError{Command: "plugins lifecycle run", Args: append([]string(nil), rest[2:]...), Usage: usage}
	}
	return req, nil
}

func (a *App) buildPluginLifecycleRunReport(ctx context.Context, req pluginLifecycleRunRequest) (pluginLifecycleRunReport, error) {
	report := pluginLifecycleRunReport{
		Kind:      "plugin_lifecycle",
		Action:    "run",
		Status:    "ok",
		Phase:     req.Phase,
		PluginID:  req.PluginID,
		TimeoutMS: req.TimeoutMS,
	}
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		report.Status = "failed"
		report.Message = err.Error()
		return report, err
	}
	selected := make([]plugins.Manifest, 0, len(manifests))
	for _, manifest := range manifests {
		if req.PluginID == "" || strings.EqualFold(manifest.ID, req.PluginID) || strings.EqualFold(manifest.Name, req.PluginID) {
			selected = append(selected, manifest)
		}
	}
	if req.PluginID != "" && len(selected) == 0 {
		report.Status = "failed"
		report.Message = fmt.Sprintf("plugin %q was not found", req.PluginID)
		return report, &ExitError{Code: 1, Err: errors.New(report.Message), Silent: true}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	for _, manifest := range selected {
		result := plugins.RunLifecycle(ctx, manifest, req.Phase, timeout)
		report.Results = append(report.Results, result)
		if result.Status == "failed" {
			report.Status = "failed"
		}
	}
	if len(report.Results) == 0 {
		report.Status = "skipped"
		report.Message = "no installed plugins were found"
		return report, nil
	}
	if report.Status == "failed" {
		report.Message = "one or more plugin lifecycle commands failed"
		return report, &ExitError{Code: 1, Err: errors.New(report.Message), Silent: true}
	}
	allSkipped := true
	for _, result := range report.Results {
		if result.Status != "skipped" {
			allSkipped = false
			break
		}
	}
	if allSkipped {
		report.Status = "skipped"
		report.Message = "no lifecycle commands were executed"
	}
	return report, nil
}

func renderPluginLifecycleRunReport(out io.Writer, report pluginLifecycleRunReport) {
	fmt.Fprintln(out, "Plugin Lifecycle")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Phase            %s\n", report.Phase)
	if report.PluginID != "" {
		fmt.Fprintf(out, "  Plugin           %s\n", report.PluginID)
	}
	for _, result := range report.Results {
		fmt.Fprintf(out, "  - %s %s commands=%d\n", result.PluginID, result.Status, result.CommandCount)
		if result.Message != "" {
			fmt.Fprintf(out, "    message=%s\n", result.Message)
		}
		for _, command := range result.Commands {
			fmt.Fprintf(out, "    [%d] %s exit=%d duration=%dms\n", command.Index, command.Status, command.ExitCode, command.DurationMS)
			if command.Error != "" {
				fmt.Fprintf(out, "        error=%s\n", command.Error)
			}
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func renderPluginValidation(out io.Writer, source string, result plugins.ValidationResult, format string) error {
	status := "ok"
	if !result.Success {
		status = "error"
	}
	report := pluginValidationReport{
		Kind:             "plugin",
		Action:           "validate",
		Status:           status,
		Source:           source,
		ValidationResult: result,
	}
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		if !result.Success {
			return &ExitError{Code: 1, Err: errors.New("plugin validation failed"), Silent: true}
		}
		return nil
	}
	fmt.Fprintf(out, "Plugin Validation\n\n")
	fmt.Fprintf(out, "  Source   %s\n", source)
	fmt.Fprintf(out, "  File     %s\n", result.FilePath)
	fmt.Fprintf(out, "  Status   %s\n", status)
	if len(result.Errors) > 0 {
		fmt.Fprintln(out, "\nErrors:")
		for _, item := range result.Errors {
			fmt.Fprintf(out, "  - %s: %s\n", item.Path, item.Message)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, item := range result.Warnings {
			fmt.Fprintf(out, "  - %s: %s\n", item.Path, item.Message)
		}
	}
	if !result.Success {
		return &ExitError{Code: 1, Err: errors.New("plugin validation failed"), Silent: true}
	}
	return nil
}

func (a *App) marketplaceSourcesCommand(args []string, format string) error {
	req, err := parseMarketplaceSourcesArgs(args)
	if err != nil {
		return err
	}
	urls, keys := a.marketplaceSourceState()
	report := marketplaceSourcesReport{
		Kind:    "marketplace",
		Action:  "sources_" + req.Action,
		Status:  "ok",
		Sources: marketplaceSourceInfos(urls, keys),
	}
	switch req.Action {
	case "list":
		return renderMarketplaceSources(a.Out, report, format)
	case "add":
		sourceURL, err := normalizeMarketplaceSourceURL(req.URL)
		if err != nil {
			return err
		}
		report.URL = sourceURL
		report.Added = !containsString(urls, sourceURL)
		if report.Added {
			urls = append(urls, sourceURL)
		}
		if strings.TrimSpace(req.PublicKey) != "" {
			if keys == nil {
				keys = map[string]string{}
			}
			keys[sourceURL] = strings.TrimSpace(req.PublicKey)
		}
		path, err := a.writeMarketplaceSources(req, urls, keys)
		if err != nil {
			return err
		}
		report.Target = normalizedConfigTarget(req.Target)
		report.Path = path
		report.Sources = marketplaceSourceInfos(urls, keys)
		return renderMarketplaceSources(a.Out, report, format)
	case "remove":
		sourceURL, err := normalizeMarketplaceSourceURL(req.URL)
		if err != nil {
			return err
		}
		report.URL = sourceURL
		next := make([]string, 0, len(urls))
		for _, value := range urls {
			if value == sourceURL {
				report.Removed = true
				continue
			}
			next = append(next, value)
		}
		urls = next
		delete(keys, sourceURL)
		path, err := a.writeMarketplaceSources(req, urls, keys)
		if err != nil {
			return err
		}
		report.Target = normalizedConfigTarget(req.Target)
		report.Path = path
		report.Sources = marketplaceSourceInfos(urls, keys)
		return renderMarketplaceSources(a.Out, report, format)
	case "clear":
		report.Cleared = len(urls) > 0 || len(keys) > 0
		urls = []string{}
		keys = map[string]string{}
		path, err := a.writeMarketplaceSources(req, urls, keys)
		if err != nil {
			return err
		}
		report.Target = normalizedConfigTarget(req.Target)
		report.Path = path
		report.Sources = marketplaceSourceInfos(urls, keys)
		return renderMarketplaceSources(a.Out, report, format)
	default:
		return fmt.Errorf("unknown marketplace sources action %q", req.Action)
	}
}

func parseMarketplaceSourcesArgs(args []string) (marketplaceSourcesRequest, error) {
	req := marketplaceSourcesRequest{Action: "list", Target: "user"}
	actionSet := false
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace sources target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace sources path is required")
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--public-key" || arg == "--key":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace public key is required")
			}
			req.PublicKey = args[index]
		case strings.HasPrefix(arg, "--public-key="):
			req.PublicKey = strings.TrimPrefix(arg, "--public-key=")
		case strings.HasPrefix(arg, "--key="):
			req.PublicKey = strings.TrimPrefix(arg, "--key=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown marketplace sources flag %q", arg)
		default:
			if !actionSet && isMarketplaceSourcesAction(arg) {
				req.Action = normalizeMarketplaceSourcesAction(arg)
				actionSet = true
				continue
			}
			positionals = append(positionals, arg)
		}
	}
	if err := validateMarketplaceSourcesTarget(req.Target); err != nil {
		return req, err
	}
	switch req.Action {
	case "list":
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected marketplace sources argument %q", positionals[0])
		}
	case "add":
		if len(positionals) == 0 {
			return req, errors.New("usage: codog marketplace sources add URL [PUBLIC_KEY]")
		}
		req.URL = positionals[0]
		if len(positionals) > 1 {
			if strings.TrimSpace(req.PublicKey) != "" {
				return req, fmt.Errorf("unexpected marketplace sources argument %q", positionals[1])
			}
			req.PublicKey = positionals[1]
		}
		if len(positionals) > 2 {
			return req, fmt.Errorf("unexpected marketplace sources argument %q", positionals[2])
		}
	case "remove":
		if len(positionals) != 1 {
			return req, errors.New("usage: codog marketplace sources remove URL")
		}
		req.URL = positionals[0]
	case "clear":
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected marketplace sources argument %q", positionals[0])
		}
	}
	return req, nil
}

func isMarketplaceSourcesAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "list", "show", "ls", "add", "set", "remove", "rm", "delete", "del", "clear":
		return true
	default:
		return false
	}
}

func normalizeMarketplaceSourcesAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "show", "ls":
		return "list"
	case "set":
		return "add"
	case "rm", "delete", "del":
		return "remove"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func validateMarketplaceSourcesTarget(target string) error {
	switch normalizedConfigTarget(target) {
	case "user", "project", "local":
		return nil
	default:
		return fmt.Errorf("unknown marketplace sources target %q", target)
	}
}

func normalizedConfigTarget(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user", "global":
		return "user"
	case "project", "workspace":
		return "project"
	case "local":
		return "local"
	default:
		return strings.ToLower(strings.TrimSpace(target))
	}
}

func normalizeMarketplaceSourceURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("marketplace URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("marketplace URL %q must include scheme and host", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("unsupported marketplace URL scheme %q", parsed.Scheme)
	}
}

func (a *App) marketplaceSourceState() ([]string, map[string]string) {
	urls := make([]string, 0, len(a.Config.Future.PluginMarketplaces))
	seen := map[string]bool{}
	for _, value := range a.Config.Future.PluginMarketplaces {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		urls = append(urls, value)
	}
	keys := map[string]string{}
	for urlValue, publicKey := range a.Config.Future.PluginMarketplaceKeys {
		urlValue = strings.TrimSpace(urlValue)
		publicKey = strings.TrimSpace(publicKey)
		if urlValue == "" || publicKey == "" {
			continue
		}
		keys[urlValue] = publicKey
	}
	return urls, keys
}

func (a *App) writeMarketplaceSources(req marketplaceSourcesRequest, urls []string, keys map[string]string) (string, error) {
	path, err := a.preferenceConfigPath(normalizedConfigTarget(req.Target), req.Path)
	if err != nil {
		return "", err
	}
	urls = dedupeStrings(urls)
	cleanKeys := map[string]string{}
	for _, sourceURL := range urls {
		if publicKey := strings.TrimSpace(keys[sourceURL]); publicKey != "" {
			cleanKeys[sourceURL] = publicKey
		}
	}
	if _, err := config.SetFileValue(path, "marketplace.sources", urls); err != nil {
		return "", err
	}
	if _, err := config.UnsetFileValue(path, legacyPluginMarketplacesKey); err != nil {
		return "", err
	}
	if len(cleanKeys) == 0 {
		if _, err := config.UnsetFileValue(path, "marketplace.public_keys"); err != nil {
			return "", err
		}
		if _, err := config.UnsetFileValue(path, legacyPluginMarketplacePublicKeysKey); err != nil {
			return "", err
		}
		a.Config.Future.PluginMarketplaceKeys = nil
	} else {
		if _, err := config.SetFileValue(path, "marketplace.public_keys", cleanKeys); err != nil {
			return "", err
		}
		a.Config.Future.PluginMarketplaceKeys = cleanKeys
	}
	a.Config.Future.PluginMarketplaces = append([]string(nil), urls...)
	return path, nil
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func marketplaceSourceInfos(urls []string, keys map[string]string) []marketplaceSourceInfo {
	urls = dedupeStrings(urls)
	out := make([]marketplaceSourceInfo, 0, len(urls))
	for _, sourceURL := range urls {
		out = append(out, marketplaceSourceInfo{
			URL:                 sourceURL,
			PublicKeyConfigured: strings.TrimSpace(keys[sourceURL]) != "",
		})
	}
	return out
}

func renderMarketplaceSources(out io.Writer, report marketplaceSourcesReport, format string) error {
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Marketplace Sources")
	fmt.Fprintf(out, "  Status   %s\n", report.Status)
	if report.Target != "" {
		fmt.Fprintf(out, "  Target   %s\n", report.Target)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Path     %s\n", report.Path)
	}
	if report.URL != "" {
		fmt.Fprintf(out, "  URL      %s\n", report.URL)
	}
	if report.Action == "sources_add" {
		fmt.Fprintf(out, "  Added    %t\n", report.Added)
	}
	if report.Action == "sources_remove" {
		fmt.Fprintf(out, "  Removed  %t\n", report.Removed)
	}
	if report.Action == "sources_clear" {
		fmt.Fprintf(out, "  Cleared  %t\n", report.Cleared)
	}
	fmt.Fprintln(out, "\nSources:")
	if len(report.Sources) == 0 {
		fmt.Fprintln(out, "  (none)")
		return nil
	}
	for _, source := range report.Sources {
		keyStatus := "no public key"
		if source.PublicKeyConfigured {
			keyStatus = "public key configured"
		}
		fmt.Fprintf(out, "  - %s (%s)\n", source.URL, keyStatus)
	}
	return nil
}

func (a *App) marketplaceSettings(format string) error {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return err
	}
	urls, keys := a.marketplaceSourceState()
	report := marketplaceSettingsReport{
		Kind:             "marketplace",
		Action:           "settings",
		Status:           "ok",
		PluginRoot:       plugins.Root(a.Workspace),
		InstalledPlugins: len(manifests),
		Sources:          marketplaceSourceInfos(urls, keys),
	}
	for _, manifest := range manifests {
		if manifest.Enabled {
			report.EnabledPlugins++
		}
	}
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Plugin Settings")
	fmt.Fprintf(a.Out, "  Plugin root       %s\n", report.PluginRoot)
	fmt.Fprintf(a.Out, "  Installed plugins %d\n", report.InstalledPlugins)
	fmt.Fprintf(a.Out, "  Enabled plugins   %d\n", report.EnabledPlugins)
	fmt.Fprintln(a.Out, "\nMarketplace sources:")
	if len(report.Sources) == 0 {
		fmt.Fprintln(a.Out, "  (none)")
		return nil
	}
	for _, source := range report.Sources {
		keyStatus := "no public key"
		if source.PublicKeyConfigured {
			keyStatus = "public key configured"
		}
		fmt.Fprintf(a.Out, "  - %s (%s)\n", source.URL, keyStatus)
	}
	return nil
}

func marketplaceRemoteUsesStructuredReport(args []string) bool {
	if len(args) == 0 {
		return true
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	switch first {
	case "list", "ls", "search", "find", "show", "info", "describe":
		return true
	}
	for _, arg := range args {
		switch {
		case arg == "--query" || arg == "--id" || arg == "--page" || arg == "--per-page" || arg == "--limit" || arg == "--url" || arg == "--marketplace" || arg == "--public-key" || arg == "--key":
			return true
		case strings.HasPrefix(arg, "--query=") || strings.HasPrefix(arg, "--id=") || strings.HasPrefix(arg, "--page=") || strings.HasPrefix(arg, "--per-page=") || strings.HasPrefix(arg, "--limit=") || strings.HasPrefix(arg, "--url=") || strings.HasPrefix(arg, "--marketplace=") || strings.HasPrefix(arg, "--public-key=") || strings.HasPrefix(arg, "--key="):
			return true
		}
	}
	return false
}

func parseMarketplaceRemoteArgs(args []string) (marketplaceRemoteRequest, error) {
	req := marketplaceRemoteRequest{Action: "list", Page: 1, PerPage: 20}
	positionals := []string{}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--url" || arg == "--marketplace":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace remote URL is required")
			}
			req.URL = args[index]
		case strings.HasPrefix(arg, "--url="):
			req.URL = strings.TrimPrefix(arg, "--url=")
		case strings.HasPrefix(arg, "--marketplace="):
			req.URL = strings.TrimPrefix(arg, "--marketplace=")
		case arg == "--public-key" || arg == "--key":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace public key is required")
			}
			req.PublicKey = args[index]
		case strings.HasPrefix(arg, "--public-key="):
			req.PublicKey = strings.TrimPrefix(arg, "--public-key=")
		case strings.HasPrefix(arg, "--key="):
			req.PublicKey = strings.TrimPrefix(arg, "--key=")
		case arg == "--query" || arg == "-q":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace remote query is required")
			}
			req.Query = args[index]
		case strings.HasPrefix(arg, "--query="):
			req.Query = strings.TrimPrefix(arg, "--query=")
		case arg == "--id":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace remote plugin id is required")
			}
			req.ID = args[index]
		case strings.HasPrefix(arg, "--id="):
			req.ID = strings.TrimPrefix(arg, "--id=")
		case arg == "--page":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace remote page is required")
			}
			value, err := strconv.Atoi(args[index])
			if err != nil {
				return req, err
			}
			req.Page = value
		case strings.HasPrefix(arg, "--page="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "--page="))
			if err != nil {
				return req, err
			}
			req.Page = value
		case arg == "--per-page" || arg == "--limit":
			index++
			if index >= len(args) {
				return req, errors.New("marketplace remote page size is required")
			}
			value, err := strconv.Atoi(args[index])
			if err != nil {
				return req, err
			}
			req.PerPage = value
		case strings.HasPrefix(arg, "--per-page="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "--per-page="))
			if err != nil {
				return req, err
			}
			req.PerPage = value
		case strings.HasPrefix(arg, "--limit="):
			value, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return req, err
			}
			req.PerPage = value
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown marketplace remote flag %q", arg)
		default:
			if !actionSet {
				switch strings.ToLower(strings.TrimSpace(arg)) {
				case "list", "ls":
					req.Action = "list"
					actionSet = true
					continue
				case "search", "find":
					req.Action = "search"
					actionSet = true
					continue
				case "show", "info", "describe":
					req.Action = "show"
					actionSet = true
					continue
				}
			}
			positionals = append(positionals, arg)
		}
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PerPage < 1 {
		req.PerPage = 20
	}
	switch req.Action {
	case "list":
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected marketplace remote argument %q", positionals[0])
		}
	case "search":
		if strings.TrimSpace(req.Query) == "" && len(positionals) > 0 {
			req.Query = positionals[0]
			positionals = positionals[1:]
		}
		if strings.TrimSpace(req.Query) == "" {
			return req, errors.New("usage: codog marketplace remote search QUERY [--url URL] [--public-key KEY]")
		}
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected marketplace remote argument %q", positionals[0])
		}
	case "show":
		if strings.TrimSpace(req.ID) == "" && len(positionals) > 0 {
			req.ID = positionals[0]
			positionals = positionals[1:]
		}
		if strings.TrimSpace(req.ID) == "" {
			return req, errors.New("usage: codog marketplace remote show ID [--url URL] [--public-key KEY]")
		}
		if len(positionals) > 0 {
			return req, fmt.Errorf("unexpected marketplace remote argument %q", positionals[0])
		}
	default:
		return req, fmt.Errorf("unknown marketplace remote action %q", req.Action)
	}
	return req, nil
}

func (a *App) marketplaceRemoteReport(args []string) (marketplaceRemoteReport, error) {
	req, err := parseMarketplaceRemoteArgs(args)
	if err != nil {
		return marketplaceRemoteReport{}, err
	}
	sources := a.marketplaceSources()
	if strings.TrimSpace(req.URL) != "" {
		sourceURL, err := normalizeMarketplaceSourceURL(req.URL)
		if err != nil {
			return marketplaceRemoteReport{}, err
		}
		source := plugins.MarketplaceSource{URL: sourceURL, PublicKey: a.marketplacePublicKey(sourceURL)}
		if strings.TrimSpace(req.PublicKey) != "" {
			source.PublicKey = strings.TrimSpace(req.PublicKey)
		}
		sources = []plugins.MarketplaceSource{source}
	}
	if len(sources) == 0 {
		return marketplaceRemoteReport{
			Kind:        "marketplace",
			Action:      "remote_" + req.Action,
			Status:      "needs_source",
			Message:     "No marketplace sources are configured.",
			NextCommand: "codog marketplace sources add URL [PUBLIC_KEY]",
			Query:       strings.TrimSpace(req.Query),
			ID:          strings.TrimSpace(req.ID),
			Sources:     []marketplaceSourceInfo{},
			Plugins:     []marketplaceRemotePlugin{},
			Total:       0,
			Pagination: &marketplacePagination{
				Page:       req.Page,
				PerPage:    req.PerPage,
				Total:      0,
				TotalPages: 0,
				Start:      0,
				End:        0,
			},
		}, nil
	}
	indexes := make([]plugins.MarketplaceIndex, 0, len(sources))
	for _, source := range sources {
		index, err := plugins.FetchMarketplace(context.Background(), source.URL, source.PublicKey)
		if err != nil {
			return marketplaceRemoteReport{}, err
		}
		indexes = append(indexes, index)
	}
	all := marketplaceRemotePlugins(indexes)
	filtered := filterMarketplaceRemotePlugins(all, req)
	report := marketplaceRemoteReport{
		Kind:    "marketplace",
		Action:  "remote_" + req.Action,
		Status:  "ok",
		Query:   strings.TrimSpace(req.Query),
		ID:      strings.TrimSpace(req.ID),
		Sources: marketplaceSourceInfosFromSources(sources),
		Total:   len(filtered),
	}
	if req.Action == "show" {
		if len(filtered) == 0 {
			report.Status = "not_found"
			return report, fmt.Errorf("remote plugin %q was not found", req.ID)
		}
		report.Plugin = &filtered[0]
		return report, nil
	}
	pageItems, pagination := paginateMarketplaceRemotePlugins(filtered, req.Page, req.PerPage)
	report.Plugins = pageItems
	report.Pagination = &pagination
	if len(filtered) == 0 {
		report.Status = "empty"
	}
	return report, nil
}

func marketplaceRemotePlugins(indexes []plugins.MarketplaceIndex) []marketplaceRemotePlugin {
	out := []marketplaceRemotePlugin{}
	for _, index := range indexes {
		for _, plugin := range index.Plugins {
			item := marketplaceRemotePlugin{
				MarketplaceURL: index.Source,
				ID:             plugin.ID,
				Name:           plugin.Name,
				Version:        plugin.Version,
				Description:    plugin.Description,
				URL:            plugin.URL,
				SHA256:         plugin.SHA256,
				SignatureValid: index.SignatureValid,
				InstallCommand: strings.Join(shellQuoteArgs([]string{"codog", "marketplace", "install-remote", plugin.ID}), " "),
				UpdateCommand:  strings.Join(shellQuoteArgs([]string{"codog", "marketplace", "update", plugin.ID}), " "),
			}
			if resolved, err := resolveMarketplaceEntryURL(index.Source, plugin.URL); err == nil {
				item.ResolvedURL = resolved
			}
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].MarketplaceURL < out[j].MarketplaceURL
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func filterMarketplaceRemotePlugins(items []marketplaceRemotePlugin, req marketplaceRemoteRequest) []marketplaceRemotePlugin {
	switch req.Action {
	case "show":
		id := strings.ToLower(strings.TrimSpace(req.ID))
		out := []marketplaceRemotePlugin{}
		for _, item := range items {
			if strings.EqualFold(item.ID, id) || strings.EqualFold(item.Name, id) {
				out = append(out, item)
			}
		}
		return out
	case "search":
		query := strings.ToLower(strings.TrimSpace(req.Query))
		out := []marketplaceRemotePlugin{}
		for _, item := range items {
			haystack := strings.ToLower(strings.Join([]string{item.ID, item.Name, item.Version, item.Description}, "\n"))
			if strings.Contains(haystack, query) {
				out = append(out, item)
			}
		}
		return out
	default:
		return append([]marketplaceRemotePlugin(nil), items...)
	}
}

func paginateMarketplaceRemotePlugins(items []marketplaceRemotePlugin, page int, perPage int) ([]marketplaceRemotePlugin, marketplacePagination) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	total := len(items)
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return append([]marketplaceRemotePlugin(nil), items[start:end]...), marketplacePagination{
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
		Start:      start,
		End:        end,
	}
}

func marketplaceSourceInfosFromSources(sources []plugins.MarketplaceSource) []marketplaceSourceInfo {
	urls := make([]string, 0, len(sources))
	keys := map[string]string{}
	for _, source := range sources {
		sourceURL := strings.TrimSpace(source.URL)
		if sourceURL == "" {
			continue
		}
		urls = append(urls, sourceURL)
		if strings.TrimSpace(source.PublicKey) != "" {
			keys[sourceURL] = strings.TrimSpace(source.PublicKey)
		}
	}
	return marketplaceSourceInfos(urls, keys)
}

func resolveMarketplaceEntryURL(indexURL string, entryURL string) (string, error) {
	if strings.TrimSpace(entryURL) == "" {
		return "", nil
	}
	parsed, err := url.Parse(entryURL)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	base, err := url.Parse(indexURL)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(parsed).String(), nil
}

func (a *App) marketplaceRemote(args []string) ([]plugins.MarketplaceIndex, error) {
	sources := a.marketplaceSources()
	if len(args) > 0 {
		source := plugins.MarketplaceSource{URL: args[0], PublicKey: a.marketplacePublicKey(args[0])}
		if len(args) > 1 {
			source.PublicKey = args[1]
		}
		sources = []plugins.MarketplaceSource{source}
	}
	if len(sources) == 0 {
		return nil, errors.New("usage: codog marketplace remote [URL] [PUBLIC_KEY]")
	}
	indexes := make([]plugins.MarketplaceIndex, 0, len(sources))
	for _, source := range sources {
		index, err := plugins.FetchMarketplace(context.Background(), source.URL, source.PublicKey)
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}

func (a *App) marketplaceUpdates(args []string) ([]plugins.MarketplaceUpdate, error) {
	sources := a.marketplaceSources()
	if len(args) > 0 {
		source := plugins.MarketplaceSource{URL: args[0], PublicKey: a.marketplacePublicKey(args[0])}
		if len(args) > 1 {
			source.PublicKey = args[1]
		}
		sources = []plugins.MarketplaceSource{source}
	}
	if len(sources) == 0 {
		return nil, errors.New("usage: codog marketplace updates [URL] [PUBLIC_KEY]")
	}
	return plugins.CheckUpdates(context.Background(), a.Workspace, sources)
}

func (a *App) marketplaceInstallRemote(args []string) (plugins.RemoteInstallResult, error) {
	if len(args) < 1 {
		return plugins.RemoteInstallResult{}, errors.New("usage: codog marketplace install-remote ID [URL] [PUBLIC_KEY]")
	}
	id := args[0]
	if len(args) > 1 {
		source := plugins.MarketplaceSource{URL: args[1], PublicKey: a.marketplacePublicKey(args[1])}
		if len(args) > 2 {
			source.PublicKey = args[2]
		}
		return plugins.InstallRemote(context.Background(), a.Workspace, source.URL, id, source.PublicKey)
	}
	sources := a.marketplaceSources()
	if len(sources) == 0 {
		return plugins.RemoteInstallResult{}, errors.New("usage: codog marketplace install-remote ID [URL] [PUBLIC_KEY]")
	}
	for _, source := range sources {
		index, err := plugins.FetchMarketplace(context.Background(), source.URL, source.PublicKey)
		if err != nil {
			return plugins.RemoteInstallResult{}, err
		}
		if _, ok := index.Find(id); ok {
			return plugins.InstallRemoteFromIndex(context.Background(), a.Workspace, index, id)
		}
	}
	return plugins.RemoteInstallResult{}, fmt.Errorf("plugin %q not found in configured marketplaces", id)
}

func (a *App) marketplaceUpdate(args []string) (plugins.RemoteUpdateResult, error) {
	if len(args) < 1 {
		return plugins.RemoteUpdateResult{}, errors.New("usage: codog marketplace update ID [URL] [PUBLIC_KEY]")
	}
	id := args[0]
	if len(args) > 1 {
		source := plugins.MarketplaceSource{URL: args[1], PublicKey: a.marketplacePublicKey(args[1])}
		if len(args) > 2 {
			source.PublicKey = args[2]
		}
		return plugins.UpdateRemote(context.Background(), a.Workspace, []plugins.MarketplaceSource{source}, id)
	}
	sources := a.marketplaceSources()
	if len(sources) == 0 {
		return plugins.RemoteUpdateResult{}, errors.New("usage: codog marketplace update ID [URL] [PUBLIC_KEY]")
	}
	return plugins.UpdateRemote(context.Background(), a.Workspace, sources, id)
}

func (a *App) marketplaceSources() []plugins.MarketplaceSource {
	sources := make([]plugins.MarketplaceSource, 0, len(a.Config.Future.PluginMarketplaces))
	for _, marketplaceURL := range a.Config.Future.PluginMarketplaces {
		marketplaceURL = strings.TrimSpace(marketplaceURL)
		if marketplaceURL == "" {
			continue
		}
		sources = append(sources, plugins.MarketplaceSource{
			URL:       marketplaceURL,
			PublicKey: a.marketplacePublicKey(marketplaceURL),
		})
	}
	return sources
}

func (a *App) marketplacePublicKey(marketplaceURL string) string {
	if a.Config.Future.PluginMarketplaceKeys == nil {
		return ""
	}
	return a.Config.Future.PluginMarketplaceKeys[marketplaceURL]
}

type authRequest struct {
	Action string
	Format string
	Rest   []string
}

type authReport struct {
	Kind                string        `json:"kind"`
	Action              string        `json:"action"`
	Status              string        `json:"status"`
	Ready               bool          `json:"ready"`
	AuthMethod          string        `json:"auth_method"`
	APIKeyConfigured    bool          `json:"api_key_configured"`
	APIKeySource        string        `json:"api_key_source,omitempty"`
	AuthTokenConfigured bool          `json:"auth_token_configured"`
	OAuthStatus         *oauth.Status `json:"oauth_status,omitempty"`
	OAuthProfile        string        `json:"oauth_profile,omitempty"`
	Message             string        `json:"message,omitempty"`
}

const authUsage = "codog auth [status|login|logout] [--json|--text]"

func (a *App) Auth(args []string) error {
	req, err := parseAuthArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
		report := a.authReport()
		if req.Format == "text" {
			renderAuthReport(a.Out, report)
			return nil
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	case "login":
		return a.Login(req.Rest)
	case "logout":
		return a.Logout(req.Rest)
	default:
		return unexpectedExtraArgsError{Command: "auth", Args: []string{req.Action}, Usage: authUsage}
	}
}

func parseAuthArgs(args []string) (authRequest, error) {
	req := authRequest{Action: "status", Format: "json"}
	rest := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--text":
			req.Format = "text"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "auth", Flag: arg, Usage: authUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			if len(rest) > 0 && strings.EqualFold(rest[0], "login") {
				rest = append(rest, arg)
				continue
			}
			return req, unknownOptionError{Command: "auth", Option: arg, Usage: authUsage}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("auth", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(strings.TrimSpace(rest[0])) {
	case "status", "show", "whoami":
		req.Action = "status"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "auth status", Args: rest[1:], Usage: authUsage}
		}
	case "login", "signin", "sign-in":
		req.Action = "login"
		req.Rest = normalizeAuthLoginArgs(rest[1:])
	case "logout", "signout", "sign-out":
		req.Action = "logout"
		req.Rest = rest[1:]
	default:
		return req, unexpectedExtraArgsError{Command: "auth", Args: []string{rest[0]}, Usage: authUsage}
	}
	return req, nil
}

func normalizeAuthLoginArgs(args []string) []string {
	out := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--email":
			if index+1 < len(args) {
				index++
			}
		case strings.HasPrefix(arg, "--email="):
		case arg == "--sso":
		default:
			out = append(out, arg)
		}
	}
	return out
}

func (a *App) authReport() authReport {
	apiSource := ""
	apiKey := strings.TrimSpace(a.Config.APIKey)
	envName, envValue := apiKeyEnvValue(a.Config.Model)
	if apiKey != "" {
		if envValue != "" && apiKey == strings.TrimSpace(envValue) {
			apiSource = envName
		} else {
			apiSource = "config"
		}
	}
	oauthStatus := oauth.InspectStatus(a.Config.ConfigHome, a.Config.OAuthProfile, time.Now().UTC())
	report := authReport{
		Kind:                "auth",
		Action:              "status",
		Status:              "ok",
		APIKeyConfigured:    apiKey != "",
		APIKeySource:        apiSource,
		AuthTokenConfigured: strings.TrimSpace(a.Config.AuthToken) != "",
		OAuthStatus:         &oauthStatus,
		OAuthProfile:        a.Config.OAuthProfile,
	}
	switch {
	case report.AuthTokenConfigured:
		report.Ready = true
		report.AuthMethod = "auth_token"
	case report.APIKeyConfigured:
		report.Ready = true
		report.AuthMethod = "api_key"
	case oauthStatus.Ready:
		report.Ready = true
		report.AuthMethod = "oauth_token"
	default:
		report.AuthMethod = "none"
	}
	if report.Ready {
		report.Message = "Authentication is configured."
	} else {
		report.Message = "No authentication credentials are configured. Run `codog auth login` or `codog api-key set KEY`."
	}
	return report
}

func renderAuthReport(out io.Writer, report authReport) {
	fmt.Fprintln(out, "Auth")
	fmt.Fprintf(out, "  Ready            %t\n", report.Ready)
	fmt.Fprintf(out, "  Method           %s\n", report.AuthMethod)
	fmt.Fprintf(out, "  API key          %t\n", report.APIKeyConfigured)
	if report.APIKeySource != "" {
		fmt.Fprintf(out, "  API key source   %s\n", report.APIKeySource)
	}
	fmt.Fprintf(out, "  Auth token       %t\n", report.AuthTokenConfigured)
	if report.OAuthStatus != nil {
		fmt.Fprintf(out, "  OAuth ready      %t\n", report.OAuthStatus.Ready)
		if report.OAuthStatus.ProfileName != "" {
			fmt.Fprintf(out, "  OAuth profile    %s\n", report.OAuthStatus.ProfileName)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type setupTokenRequest struct {
	Token    string
	Stdin    bool
	Target   string
	Path     string
	Format   string
	Prompted bool
}

type setupTokenReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Configured    bool     `json:"configured"`
	RedactedValue string   `json:"redacted_value,omitempty"`
	Path          string   `json:"path,omitempty"`
	EnvVars       []string `json:"env_vars,omitempty"`
	Message       string   `json:"message,omitempty"`
}

const setupTokenUsage = "codog setup-token [TOKEN|--token TOKEN|--stdin] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) SetupToken(args []string) error {
	req, err := parseSetupTokenArgs(args)
	if err != nil {
		return err
	}
	token, prompted, err := a.setupTokenValue(req)
	if err != nil {
		return err
	}
	req.Token = token
	req.Prompted = prompted
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	if _, err := config.SetFileValue(path, "auth_token", token); err != nil {
		return err
	}
	a.Config.AuthToken = token
	report := setupTokenReport{
		Kind:          "setup_token",
		Action:        "save",
		Status:        "ok",
		Configured:    true,
		RedactedValue: redact(token),
		Path:          path,
		EnvVars:       []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "CODOG_AUTH_TOKEN"},
		Message:       "Long-lived authentication token saved as auth_token. Command output redacts the stored value.",
	}
	if req.Prompted {
		report.Message = "Long-lived authentication token saved as auth_token from prompt input. Command output redacts the stored value."
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSetupTokenReport(a.Out, report)
	return nil
}
