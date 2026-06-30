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

type CandidateOptions struct {
	Model            string
	ActiveSessionID  string
	RecentSessionIDs []string
	Extra            []string
}

func Specs() []Spec {
	specs := []Spec{
		{Name: "/help", Usage: "/help", Description: "Show slash command help."},
		{Name: "/status", Usage: "/status", Description: "Show local workspace, session, config, git, and runtime status."},
		{Name: "/statusline", Usage: "/statusline", Description: "Print a compact one-line workspace status."},
		{Name: "/init", Usage: "/init", Description: "Initialize Codog project config and instruction files."},
		{Name: "/state", Usage: "/state", Description: "Show the current worker state file."},
		{Name: "/memory", Usage: "/memory [list|show|add]", Description: "List, show, or append project memory."},
		{Name: "/project", Usage: "/project", Description: "Show project detection info."},
		{Name: "/env", Usage: "/env", Description: "Show environment variables visible to tools with sensitive values redacted."},
		{Name: "/context", Usage: "/context", Description: "Show prompt context, memory, focus, session, and token preflight."},
		{Name: "/config", Usage: "/config [section]", Description: "Show effective runtime config."},
		{Name: "/model", Usage: "/model [name]", Description: "Show or switch the current model."},
		{Name: "/max-tokens", Usage: "/max-tokens [count]", Description: "Show or switch max output tokens."},
		{Name: "/max-turns", Usage: "/max-turns [count]", Description: "Show or switch max model/tool turns."},
		{Name: "/permissions", Usage: "/permissions [mode]", Description: "Show or switch the current permission mode."},
		{Name: "/allowed-tools", Usage: "/allowed-tools [list|add|remove|clear]", Description: "Show or modify runtime allow rules."},
		{Name: "/brief", Usage: "/brief MESSAGE", Description: "Send a short user-facing brief with optional attachment metadata."},
		{Name: "/clear", Usage: "/clear [--confirm]", Description: "Start a fresh local session."},
		{Name: "/doctor", Usage: "/doctor", Description: "Run local auth, config, workspace, and runtime diagnostics."},
		{Name: "/cost", Usage: "/cost", Description: "Estimate token usage and cost for the current session."},
		{Name: "/usage", Usage: "/usage", Description: "Show detailed token, role, block, and tool usage for the current session."},
		{Name: "/stats", Usage: "/stats", Description: "Alias for /usage."},
		{Name: "/rate-limit-options", Usage: "/rate-limit-options", Description: "Show provider retry and backoff settings."},
		{Name: "/plan", Usage: "/plan [TEXT|show|exit|clear]", Description: "Enter or inspect read-only planning mode."},
		{Name: "/exit-plan", Usage: "/exit-plan", Description: "Leave read-only planning mode."},
		{Name: "/compact", Usage: "/compact", Description: "Compact in-memory request context for long sessions."},
		{Name: "/diff", Usage: "/diff [--staged]", Description: "Show git working tree or staged diff."},
		{Name: "/commit", Usage: "/commit [--all] MESSAGE", Description: "Create a git commit from staged changes."},
		{Name: "/branch", Usage: "/branch [list|current|create|switch|delete|rename]", Description: "Manage local git branches."},
		{Name: "/tag", Usage: "/tag [list|create|show|delete]", Description: "Manage local git tags."},
		{Name: "/git", Usage: "/git status|diff|log|blame", Description: "Run a supported git workflow."},
		{Name: "/log", Usage: "/log [count]", Description: "Show recent git commits."},
		{Name: "/changelog", Usage: "/changelog [count]", Description: "Show recent git changes with stats."},
		{Name: "/release-notes", Usage: "/release-notes [FROM [TO]]", Description: "Generate Markdown release notes from git commits."},
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
		{Name: "/teleport", Usage: "/teleport SYMBOL|PATH", Description: "Jump to a file or Go symbol."},
		{Name: "/completion", Usage: "/completion PREFIX", Description: "List Go completion candidates for a prefix."},
		{Name: "/format", Usage: "/format PATH [--write]", Description: "Preview or write gofmt output for a Go file."},
		{Name: "/export", Usage: "/export [file]", Description: "Export the current session transcript."},
		{Name: "/copy", Usage: "/copy [last|all]", Description: "Copy the latest response or session transcript to the clipboard."},
		{Name: "/history", Usage: "/history [limit]", Description: "Show recent prompts recorded for the current session."},
		{Name: "/summary", Usage: "/summary", Description: "Summarize the current session."},
		{Name: "/todos", Usage: "/todos [list|add|start|done|pending|clear]", Description: "Show or update the workspace todo list."},
		{Name: "/session", Usage: "/session [list|exists|switch|fork|delete]", Description: "Manage saved sessions."},
		{Name: "/prompt-history", Usage: "/prompt-history [limit]", Description: "Alias for /history."},
		{Name: "/rename", Usage: "/rename NEW_ID", Description: "Rename the active session."},
		{Name: "/resume", Usage: "/resume [session-id|latest]", Description: "Load a saved session into the REPL."},
		{Name: "/rewind", Usage: "/rewind [messages]", Description: "Remove recent messages from the current session."},
		{Name: "/share", Usage: "/share [DIR]", Description: "Write a local share artifact for the current session."},
		{Name: "/sandbox", Usage: "/sandbox", Description: "Show local sandbox isolation status."},
		{Name: "/files", Usage: "/files [PATH] [--glob GLOB]", Description: "List workspace files."},
		{Name: "/search", Usage: "/search PATTERN [--glob GLOB]", Description: "Search files in the workspace."},
		{Name: "/security-review", Usage: "/security-review [--limit N]", Description: "Run a local security heuristic scan."},
		{Name: "/bughunter", Usage: "/bughunter [PATH] [--limit N]", Description: "Scan for likely correctness bugs."},
		{Name: "/review", Usage: "/review [--staged|--base REF]", Description: "Review changed files with diff summary and security findings."},
		{Name: "/feedback", Usage: "/feedback [MESSAGE]", Description: "Write a local feedback report with workspace diagnostics."},
		{Name: "/pr", Usage: "/pr [CONTEXT]", Description: "Write a local pull request draft from git and session context."},
		{Name: "/issue", Usage: "/issue [CONTEXT]", Description: "Write a local issue draft from git and session context."},
		{Name: "/focus", Usage: "/focus [PATH...]", Description: "Show or add focused context paths."},
		{Name: "/unfocus", Usage: "/unfocus [PATH...|--all]", Description: "Remove focused context paths."},
		{Name: "/add-dir", Usage: "/add-dir [PATH...|remove PATH|clear]", Description: "Allow tools to access additional directories."},
		{Name: "/output-style", Usage: "/output-style [list|show|set|clear]", Description: "Show or change the active output style."},
		{Name: "/theme", Usage: "/theme [list|NAME|clear]", Description: "Show or change the terminal theme preference."},
		{Name: "/color", Usage: "/color [list|NAME|clear]", Description: "Alias for /theme."},
		{Name: "/vim", Usage: "/vim [on|off|toggle|status]", Description: "Toggle vim keybinding preference."},
		{Name: "/effort", Usage: "/effort [auto|low|medium|high|clear]", Description: "Show or change reasoning effort preference."},
		{Name: "/fast", Usage: "/fast [on|off|toggle|status|clear]", Description: "Show or change fast mode preference."},
		{Name: "/privacy-settings", Usage: "/privacy-settings [show|set KEY on|off|clear KEY]", Description: "Show or change local privacy preferences."},
		{Name: "/keybindings", Usage: "/keybindings", Description: "Show REPL and TUI keyboard shortcuts."},
		{Name: "/skills", Usage: "/skills [list|show|invoke|install|uninstall]", Description: "List, show, install, remove, or render Markdown skills."},
		{Name: "/commands", Usage: "/commands [list|show|run]", Description: "List, show, or render custom Markdown slash commands."},
		{Name: "/hooks", Usage: "/hooks [list|run pre|post]", Description: "Inspect or test configured tool hooks."},
		{Name: "/mcp", Usage: "/mcp [list|serve|show|add|remove|tools]", Description: "Serve, manage, or inspect stdio MCP servers."},
		{Name: "/ide", Usage: "/ide [status|clear]", Description: "Inspect or clear trusted editor bridge state."},
		{Name: "/system-prompt", Usage: "/system-prompt", Description: "Show the active system prompt."},
		{Name: "/tool-details", Usage: "/tool-details TOOL", Description: "Show detailed information for a tool."},
		{Name: "/tokens", Usage: "/tokens", Description: "Alias for /cost."},
		{Name: "/version", Usage: "/version", Description: "Show CLI version and build information."},
		{Name: "/templates", Usage: "/templates [list|show|apply]", Description: "List, show, or render prompt templates."},
		{Name: "/exit", Usage: "/exit", Description: "Exit the REPL."},
		{Name: "/agents", Usage: "/agents [list|run|worktrees]", Description: "List or launch local agent definitions."},
		{Name: "/tasks", Usage: "/tasks [list|status|stop|logs|watch]", Description: "Alias for background task management."},
		{Name: "/background", Usage: "/background [run|list|status|stop|logs|watch]", Description: "Manage local background tasks."},
		{Name: "/plugin", Usage: "/plugin [list|install|enable|disable|remove]", Description: "Alias for marketplace plugin management."},
		{Name: "/plugins", Usage: "/plugins [list|install|enable|disable|remove]", Description: "Alias for marketplace plugin management."},
		{Name: "/marketplace", Usage: "/marketplace [list|remote|updates|install|update]", Description: "Manage local and remote plugins."},
		{Name: "/providers", Usage: "/providers [status|list|show|set]", Description: "Inspect and configure LLM providers."},
		{Name: "/login", Usage: "/login [browser|device] PROFILE", Description: "Start OAuth login through a configured provider profile."},
		{Name: "/logout", Usage: "/logout [PROFILE]", Description: "Revoke and delete local OAuth credentials."},
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

func Candidates(prefix string) []string {
	return CandidatesWithOptions(prefix, CandidateOptions{})
}

func CandidatesWithOptions(prefix string, options CandidateOptions) []string {
	prefix = strings.Trim(prefix, "\r\n\t")
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	return FilterCandidates(prefix, AllCandidates(options))
}

func AllCandidates(options CandidateOptions) []string {
	seen := map[string]bool{}
	candidates := []string{}
	add := func(candidate string) {
		candidate = strings.Trim(candidate, "\r\n\t")
		if strings.TrimSpace(candidate) == "" || !strings.HasPrefix(candidate, "/") || seen[candidate] {
			return
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	for _, spec := range Specs() {
		add(spec.Name)
	}
	for _, candidate := range []string{
		"/add-dir ",
		"/allowed-tools add ",
		"/allowed-tools clear",
		"/allowed-tools list",
		"/allowed-tools remove ",
		"/brief ",
		"/bughunter ",
		"/clear --confirm",
		"/commands list",
		"/commands run ",
		"/config auth",
		"/config env",
		"/config model",
		"/config paths",
		"/color dark",
		"/effort high",
		"/fast on",
		"/feedback ",
		"/focus ",
		"/history 10",
		"/hooks list",
		"/hooks run pre",
		"/hooks run post",
		"/ide status",
		"/ide clear",
		"/issue ",
		"/keybindings",
		"/marketplace list",
		"/mcp list",
		"/mcp tools ",
		"/mcp resources ",
		"/model ",
		"/permissions read-only",
		"/permissions workspace-write",
		"/permissions danger-full-access",
		"/privacy-settings show",
		"/privacy-settings set prompt-history off",
		"/pr ",
		"/plugin list",
		"/plugin install ",
		"/plugin enable ",
		"/plugin disable ",
		"/plugin remove ",
		"/resume latest",
		"/rename ",
		"/session list",
		"/session rename ",
		"/session switch ",
		"/session fork ",
		"/share ",
		"/skills list",
		"/skills show ",
		"/skills invoke ",
		"/statusline --json",
		"/teleport ",
		"/templates list",
		"/templates apply ",
		"/theme list",
		"/theme dark",
		"/unfocus --all",
		"/vim toggle",
	} {
		add(candidate)
	}
	model := strings.TrimSpace(options.Model)
	if model != "" {
		add("/model " + model)
	}
	activeSessionID := strings.TrimSpace(options.ActiveSessionID)
	if activeSessionID != "" {
		add("/resume " + activeSessionID)
		add("/session switch " + activeSessionID)
	}
	for index, sessionID := range options.RecentSessionIDs {
		if index >= 10 {
			break
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		add("/resume " + sessionID)
		add("/session switch " + sessionID)
	}
	for _, candidate := range options.Extra {
		add(candidate)
	}
	sort.Strings(candidates)
	return candidates
}

func FilterCandidates(prefix string, candidates []string) []string {
	prefix = strings.Trim(prefix, "\r\n\t")
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.Trim(candidate, "\r\n\t")
		if candidate == "" || seen[candidate] || !strings.HasPrefix(candidate, prefix) {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out
}

func RenderHelp(w io.Writer) {
	for _, spec := range Specs() {
		fmt.Fprintf(w, "%-10s %s\n", spec.Usage, spec.Description)
	}
}
