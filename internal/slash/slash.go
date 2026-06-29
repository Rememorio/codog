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
		{Name: "/config", Usage: "/config [section]", Description: "Show effective runtime config."},
		{Name: "/model", Usage: "/model [name]", Description: "Show or switch the current model."},
		{Name: "/permissions", Usage: "/permissions [mode]", Description: "Show or switch the current permission mode."},
		{Name: "/clear", Usage: "/clear [--confirm]", Description: "Start a fresh local session."},
		{Name: "/doctor", Usage: "/doctor", Description: "Run local auth, config, workspace, and runtime diagnostics."},
		{Name: "/cost", Usage: "/cost", Description: "Estimate token usage and cost for the current session."},
		{Name: "/compact", Usage: "/compact", Description: "Compact in-memory request context for long sessions."},
		{Name: "/diff", Usage: "/diff [--staged]", Description: "Show git working tree or staged diff."},
		{Name: "/commit", Usage: "/commit [--all] MESSAGE", Description: "Create a git commit from staged changes."},
		{Name: "/export", Usage: "/export [file]", Description: "Export the current session transcript."},
		{Name: "/history", Usage: "/history [limit]", Description: "Show recent prompts recorded for the current session."},
		{Name: "/session", Usage: "/session [list|exists|switch|fork|delete]", Description: "Manage saved sessions."},
		{Name: "/prompt-history", Usage: "/prompt-history [limit]", Description: "Alias for /history."},
		{Name: "/resume", Usage: "/resume [session-id|latest]", Description: "Load a saved session into the REPL."},
		{Name: "/sandbox", Usage: "/sandbox", Description: "Show local sandbox isolation status."},
		{Name: "/search", Usage: "/search PATTERN [--glob GLOB]", Description: "Search files in the workspace."},
		{Name: "/skills", Usage: "/skills", Description: "List discovered Markdown skills."},
		{Name: "/mcp", Usage: "/mcp", Description: "Inspect configured stdio MCP servers."},
		{Name: "/version", Usage: "/version", Description: "Show CLI version and build information."},
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
