package slash

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type Spec struct {
	Name        string
	Usage       string
	Description string
}

func Specs() []Spec {
	specs := []Spec{
		{Name: "/help", Usage: "/help", Description: "Show slash command help."},
		{Name: "/status", Usage: "/status", Description: "Show local workspace, session, config, git, and runtime status."},
		{Name: "/init", Usage: "/init", Description: "Initialize Codog project config and instruction files."},
		{Name: "/state", Usage: "/state", Description: "Show the current worker state file."},
		{Name: "/memory", Usage: "/memory", Description: "List loaded project memory files."},
		{Name: "/project", Usage: "/project", Description: "Show project detection info."},
		{Name: "/env", Usage: "/env", Description: "Show environment variables visible to tools with sensitive values redacted."},
		{Name: "/context", Usage: "/context", Description: "Show prompt context, memory, focus, session, and token preflight."},
		{Name: "/config", Usage: "/config [section]", Description: "Show effective runtime config."},
		{Name: "/model", Usage: "/model [name]", Description: "Show or switch the current model."},
		{Name: "/max-tokens", Usage: "/max-tokens [count]", Description: "Show or switch max output tokens."},
		{Name: "/max-turns", Usage: "/max-turns [count]", Description: "Show or switch max model/tool turns."},
		{Name: "/permissions", Usage: "/permissions [mode]", Description: "Show or switch the current permission mode."},
		{Name: "/allowed-tools", Usage: "/allowed-tools [list|add|remove|clear]", Description: "Show or modify runtime allow rules."},
		{Name: "/clear", Usage: "/clear [--confirm]", Description: "Start a fresh local session."},
		{Name: "/doctor", Usage: "/doctor", Description: "Run local auth, config, workspace, and runtime diagnostics."},
		{Name: "/cost", Usage: "/cost", Description: "Estimate token usage and cost for the current session."},
		{Name: "/compact", Usage: "/compact", Description: "Compact in-memory request context for long sessions."},
		{Name: "/diff", Usage: "/diff [--staged]", Description: "Show git working tree or staged diff."},
		{Name: "/commit", Usage: "/commit [--all] MESSAGE", Description: "Create a git commit from staged changes."},
		{Name: "/git", Usage: "/git status|diff|log|blame", Description: "Run a supported git workflow."},
		{Name: "/log", Usage: "/log [count]", Description: "Show recent git commits."},
		{Name: "/changelog", Usage: "/changelog [count]", Description: "Show recent git changes with stats."},
		{Name: "/stash", Usage: "/stash [list|push|apply|pop]", Description: "Manage git stashes."},
		{Name: "/blame", Usage: "/blame FILE [line]", Description: "Show git blame for a file."},
		{Name: "/run", Usage: "/run COMMAND [ARG...]", Description: "Run a command in the workspace."},
		{Name: "/test", Usage: "/test [ARGS...]", Description: "Run go test for the workspace."},
		{Name: "/build", Usage: "/build [ARGS...]", Description: "Run go build for the workspace."},
		{Name: "/lint", Usage: "/lint [ARGS...]", Description: "Run go vet for the workspace."},
		{Name: "/symbols", Usage: "/symbols", Description: "List Go symbols in the workspace."},
		{Name: "/diagnostics", Usage: "/diagnostics [patterns...]", Description: "Show Go test diagnostics."},
		{Name: "/map", Usage: "/map [--depth N]", Description: "Show a shallow workspace file map."},
		{Name: "/references", Usage: "/references SYMBOL", Description: "Find references to a symbol."},
		{Name: "/definition", Usage: "/definition SYMBOL", Description: "Find the definition of a symbol."},
		{Name: "/hover", Usage: "/hover SYMBOL", Description: "Show definition context for a symbol."},
		{Name: "/export", Usage: "/export [file]", Description: "Export the current session transcript."},
		{Name: "/history", Usage: "/history [limit]", Description: "Show recent prompts recorded for the current session."},
		{Name: "/todos", Usage: "/todos [list|add|start|done|pending|clear]", Description: "Show or update the workspace todo list."},
		{Name: "/session", Usage: "/session [list|exists|switch|fork|delete]", Description: "Manage saved sessions."},
		{Name: "/prompt-history", Usage: "/prompt-history [limit]", Description: "Alias for /history."},
		{Name: "/resume", Usage: "/resume [session-id|latest]", Description: "Load a saved session into the REPL."},
		{Name: "/sandbox", Usage: "/sandbox", Description: "Show local sandbox isolation status."},
		{Name: "/search", Usage: "/search PATTERN [--glob GLOB]", Description: "Search files in the workspace."},
		{Name: "/security-review", Usage: "/security-review [--limit N]", Description: "Run a local security heuristic scan."},
		{Name: "/focus", Usage: "/focus [PATH...]", Description: "Show or add focused context paths."},
		{Name: "/unfocus", Usage: "/unfocus [PATH...|--all]", Description: "Remove focused context paths."},
		{Name: "/output-style", Usage: "/output-style [list|show|set|clear]", Description: "Show or change the active output style."},
		{Name: "/skills", Usage: "/skills", Description: "List discovered Markdown skills."},
		{Name: "/mcp", Usage: "/mcp", Description: "Inspect configured stdio MCP servers."},
		{Name: "/system-prompt", Usage: "/system-prompt", Description: "Show the active system prompt."},
		{Name: "/tool-details", Usage: "/tool-details TOOL", Description: "Show detailed information for a tool."},
		{Name: "/tokens", Usage: "/tokens", Description: "Alias for /cost."},
		{Name: "/version", Usage: "/version", Description: "Show CLI version and build information."},
		{Name: "/templates", Usage: "/templates [list|show|apply]", Description: "List, show, or render prompt templates."},
		{Name: "/exit", Usage: "/exit", Description: "Exit the REPL."},
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

func Lookup(name string) (Spec, bool) {
	for _, spec := range Specs() {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return Spec{}, false
}

func RenderHelp(w io.Writer) {
	for _, spec := range Specs() {
		fmt.Fprintf(w, "%-10s %s\n", spec.Usage, spec.Description)
	}
}
