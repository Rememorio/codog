package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rememorio/codog/internal/slash"
)

func looksLikeFromPRValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || looksLikeCommandName(value) {
		return false
	}
	if _, ok := parsePullRequestNumber(value); ok {
		return true
	}
	if strings.HasPrefix(value, "#") {
		_, ok := parsePullRequestNumber(strings.TrimPrefix(value, "#"))
		return ok
	}
	if strings.Contains(value, "/pull/") || strings.Contains(value, "#") || strings.HasPrefix(value, "github.com/") ||
		strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return true
	}
	return false
}

func rejectDuplicateScalarGlobalFlags(args []string) error {
	seen := map[string][]string{}
	order := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		consumedValue := false
		if key, value, consumesNext, ok := duplicateTrackedGlobalFlag(arg, args, index); ok {
			if _, exists := seen[key]; !exists {
				order = append(order, key)
			}
			seen[key] = append(seen[key], value)
			if consumesNext {
				index++
				consumedValue = true
			}
		}
		if !consumedValue && globalFlagConsumesNext(arg) && !strings.Contains(arg, "=") && index+1 < len(args) {
			index++
		}
	}
	for _, key := range order {
		values := seen[key]
		if len(values) <= 1 {
			continue
		}
		return duplicateFlagError{
			Flag:   key,
			Values: values,
			Usage:  duplicateFlagUsage(key),
		}
	}
	return nil
}

func duplicateTrackedGlobalFlag(arg string, args []string, index int) (key string, value string, consumesNext bool, ok bool) {
	if before, after, found := strings.Cut(arg, "="); found {
		key, ok = duplicateTrackedGlobalFlagKey(before)
		if !ok {
			return "", "", false, false
		}
		return key, after, false, true
	}
	key, ok = duplicateTrackedGlobalFlagKey(arg)
	if !ok {
		return "", "", false, false
	}
	switch key {
	case "--output-format":
		if arg == "--json" || arg == "-json" {
			return key, "json", false, true
		}
	case "--permission-mode":
		if arg == "--skip-permissions" || arg == "-skip-permissions" || arg == "--dangerously-skip-permissions" || arg == "-dangerously-skip-permissions" {
			return key, "allow", false, true
		}
	}
	if index+1 < len(args) {
		return key, args[index+1], true, true
	}
	return key, "", false, true
}

func duplicateTrackedGlobalFlagKey(arg string) (string, bool) {
	switch arg {
	case "--model", "-model":
		return "--model", true
	case "--fallback-model", "-fallback-model":
		return "--fallback-model", true
	case "--thinking", "-thinking":
		return "--thinking", true
	case "--resume", "-resume", "-r":
		return "--resume", true
	case "--from-pr", "-from-pr":
		return "--from-pr", true
	case "--resume-session-at", "-resume-session-at":
		return "--resume-session-at", true
	case "--debug-file", "-debug-file":
		return "--debug-file", true
	case "--agents", "-agents":
		return "--agents", true
	case "--max-budget-usd", "-max-budget-usd":
		return "--max-budget-usd", true
	case "--output-format", "-output-format", "-o", "--o", "--json", "-json":
		return "--output-format", true
	case "--permission-mode", "-permission-mode", "--skip-permissions", "-skip-permissions", "--dangerously-skip-permissions", "-dangerously-skip-permissions":
		return "--permission-mode", true
	default:
		return "", false
	}
}

func duplicateFlagUsage(flag string) string {
	switch flag {
	case "--model":
		return "codog --model MODEL COMMAND"
	case "--fallback-model":
		return "codog --fallback-model MODEL COMMAND"
	case "--thinking":
		return "codog --thinking enabled|adaptive|disabled COMMAND"
	case "--output-format":
		return "codog --output-format text|json COMMAND"
	case "--permission-mode":
		return "codog [--permission-mode MODE | --skip-permissions] COMMAND"
	case "--resume":
		return "codog --resume ID|latest COMMAND"
	case "--from-pr":
		return "codog --from-pr OWNER/REPO#123 COMMAND"
	case "--resume-session-at":
		return "codog --resume ID --resume-session-at MESSAGE_ID prompt TEXT"
	case "--debug-file":
		return "codog --debug-file debug.log COMMAND"
	case "--agents":
		return "codog --agents JSON COMMAND"
	case "--max-budget-usd":
		return "codog -p --max-budget-usd 1.50 TEXT"
	default:
		return "codog [flags] COMMAND"
	}
}

func globalFlagConsumesNext(arg string) bool {
	switch arg {
	case "--config", "-config", "--settings", "-settings", "--setting-sources", "-setting-sources", "--cwd", "-cwd", "-C", "--C", "--directory", "-directory",
		"--model", "-model", "--fallback-model", "-fallback-model", "--thinking", "-thinking", "--base-url", "-base-url", "--system-prompt", "-system-prompt",
		"--system-prompt-file", "-system-prompt-file", "--append-system-prompt", "-append-system-prompt",
		"--append-system-prompt-file", "-append-system-prompt-file", "--session", "-session",
		"--session-id", "-session-id", "--name", "-name", "--resume", "-resume", "-r",
		"--from-pr", "-from-pr", "--resume-session-at", "-resume-session-at", "--prefill", "-prefill", "--agents", "-agents", "--plugin-dir", "-plugin-dir", "--deep-link-repo", "-deep-link-repo",
		"--deep-link-last-fetch", "-deep-link-last-fetch", "--debug-file", "-debug-file", "--output-format", "-output-format", "-o", "--o",
		"--input-format", "-input-format", "--json-schema", "-json-schema",
		"--permission-mode", "-permission-mode", "--max-turns", "-max-turns",
		"--max-tokens", "-max-tokens", "--max-budget-usd", "-max-budget-usd", "--temperature", "-temperature",
		"--tools", "-tools", "--mcp-config", "-mcp-config",
		"--add-dir", "-add-dir",
		"--attach", "-attach", "--attachment", "-attachment", "--file", "-file":
		return true
	default:
		return false
	}
}

func hasExplicitEmptyPositional(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return index+1 < len(args) && strings.TrimSpace(args[index+1]) == ""
		}
		if globalFlagTakesValue(arg) {
			if !strings.Contains(arg, "=") {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return strings.TrimSpace(arg) == ""
	}
	return false
}

func globalFlagTakesValue(arg string) bool {
	name := arg
	if before, _, ok := strings.Cut(arg, "="); ok {
		name = before
	}
	switch name {
	case "--config", "--settings", "-settings", "--setting-sources", "-setting-sources", "--cwd", "-C", "--directory", "--model", "--fallback-model", "-fallback-model", "--thinking", "-thinking", "--base-url", "--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file", "--session", "--session-id", "-session-id", "--name", "-name", "--resume", "-r", "--from-pr", "-from-pr", "--resume-session-at", "-resume-session-at", "--prefill", "-prefill", "--agents", "-agents", "--plugin-dir", "-plugin-dir", "--deep-link-repo", "-deep-link-repo", "--deep-link-last-fetch", "-deep-link-last-fetch", "--debug-file", "-debug-file", "--output-format", "-o", "--input-format", "-input-format", "--json-schema", "-json-schema", "--permission-mode", "--allowed-tools", "--allowedTools", "--disallowed-tools", "--disallowedTools", "--add-dir", "-add-dir", "--tools", "--mcp-config", "-mcp-config", "--max-turns", "--max-tokens", "--max-budget-usd", "-max-budget-usd", "--temperature":
		return true
	default:
		return false
	}
}

func commandAcceptsPrefill(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "", "repl", "tui":
		return true
	default:
		return false
	}
}

func applyGlobalCWD(path string) (func(), error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return func() {}, nil
	}
	previous, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(path); err != nil {
		return nil, invalidCWDError{Path: path, Err: err}
	}
	return func() {
		_ = os.Chdir(previous)
	}, nil
}

func isKnownNonPromptCommand(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	value = strings.TrimPrefix(value, "/")
	if strings.EqualFold(value, "prompt") {
		return false
	}
	for _, command := range builtInCommandNames() {
		if strings.EqualFold(value, command) {
			return true
		}
	}
	return false
}

func isInteractiveInitialPrompt(command string, args []string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.HasPrefix(command, "/") || strings.EqualFold(command, "prompt") || isKnownNonPromptCommand(command) {
		return false
	}
	lower := strings.ToLower(command)
	if lower == "fork" || (len(args) == 0 && (lower == "quit" || bareApprovalSlashName(lower) != "")) {
		return false
	}
	for _, arg := range args {
		if strings.HasPrefix(strings.TrimSpace(arg), "-") {
			return false
		}
	}
	return true
}

func missingToolFlagArgument(args []string) (missingArgumentError, bool) {
	for index := 0; index < len(args); index++ {
		if argument, inlineValue, inline, ok := parseToolSelectionFlag(args[index]); ok {
			if inline {
				_ = inlineValue
				continue
			}
			nextIndex := index + 1
			if nextIndex >= len(args) {
				return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
			}
			next := strings.TrimSpace(args[nextIndex])
			if strings.HasPrefix(next, "-") || looksLikeCommandName(next) {
				return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
			}
			index = nextIndex
			continue
		}
		argument, inlineValue, inline, ok := parseToolRuleFlag(args[index])
		if !ok {
			continue
		}
		if inline {
			if strings.TrimSpace(inlineValue) == "" {
				return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
			}
			continue
		}
		nextIndex := index + 1
		if nextIndex >= len(args) {
			return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
		}
		next := strings.TrimSpace(args[nextIndex])
		if next == "" || strings.HasPrefix(next, "-") || looksLikeCommandName(next) {
			return missingArgumentError{Argument: argument, Example: toolRuleFlagExample(argument)}, true
		}
		index = nextIndex
	}
	return missingArgumentError{}, false
}

func parseToolSelectionFlag(arg string) (argument string, value string, inline bool, ok bool) {
	if arg == "--tools" {
		return "--tools", "", false, true
	}
	const prefix = "--tools="
	if strings.HasPrefix(arg, prefix) {
		return "--tools", strings.TrimPrefix(arg, prefix), true, true
	}
	return "", "", false, false
}

func parseToolRuleFlag(arg string) (argument string, value string, inline bool, ok bool) {
	for _, candidate := range []string{"--allowed-tools", "--allowedTools", "--disallowed-tools", "--disallowedTools"} {
		if arg == candidate {
			return candidate, "", false, true
		}
		prefix := candidate + "="
		if strings.HasPrefix(arg, prefix) {
			return candidate, strings.TrimPrefix(arg, prefix), true, true
		}
	}
	return "", "", false, false
}

func looksLikeCommandName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return true
	}
	for _, command := range builtInCommandNames() {
		if strings.EqualFold(value, command) {
			return true
		}
	}
	return false
}

func toolRuleFlagExample(argument string) string {
	switch argument {
	case "--tools":
		return argument + " read_file,grep"
	case "--allowedTools", "--disallowedTools":
		return argument + " read,glob"
	default:
		return argument + " read_file,grep"
	}
}

func normalizeOutputFormat(command, value string, expected []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	lower := strings.ToLower(value)
	for _, candidate := range expected {
		if lower == candidate {
			return lower, nil
		}
	}
	return "", outputFormatError{Command: command, Value: value, Expected: expected}
}

func globalOutputFormatProvenance(outputFormat string, jsonOutput bool) (source string, raw string, overridden bool) {
	envValue := strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT"))
	outputFormat = strings.TrimSpace(outputFormat)
	if outputFormat != "" {
		return "flag", outputFormat, envValue != ""
	}
	if jsonOutput {
		return "flag", "json", envValue != ""
	}
	if envValue != "" {
		return "env", envValue, false
	}
	return "default", "", false
}

func resolveGlobalOutputFormat(outputFormat string, jsonOutput bool) string {
	if strings.TrimSpace(outputFormat) != "" {
		return outputFormat
	}
	if jsonOutput {
		return "json"
	}
	return strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT"))
}

func injectGlobalOutputFormat(command string, rest []string, format string) []string {
	format = strings.TrimSpace(format)
	if format == "" || !commandAcceptsGlobalOutputFormat(command) || argsHaveOutputFormat(rest) {
		return rest
	}
	out := append([]string(nil), rest...)
	out = append(out, "--output-format", format)
	return out
}

func commandAcceptsGlobalOutputFormat(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "acp", "add-dir", "advisor", "agents", "subagent", "allowed-tools", "android", "ant-trace", "api", "api-key", "app", "auth", "autofix-pr", "backfill-sessions", "background", "base-check", "blame", "bookmarks", "branch", "branch-lock", "branchlock", "brief", "budget", "bughunter", "cache", "caches", "capabilities", "changelog", "chrome", "cost",
		"break-cache", "bridge", "bridge-kick", "bootstrap-plan", "bug", "checkpoint", "clear", "code-intel", "color", "commands", "commit", "commit-push-pr", "compact", "completion", "config", "continue", "context", "context-noninteractive", "conversation", "cron", "ctx_viz",
		"debug-tool-call", "deferred-init", "definition", "desktop", "diagnostics", "diff", "doctor", "dump-manifests", "effort", "enterprise", "env", "exit", "exit-plan",
		"extra-usage", "extra-usage-core", "extra-usage-noninteractive", "fast", "feedback", "files", "focus", "g004", "g004-conformance", "generate-session-name", "generatesessionname", "git", "good-claude", "green", "green-contract", "heapdump", "hooks", "language",
		"format", "help", "history", "hover", "ide", "init", "init-verifiers", "insights", "install", "install-github-app", "install-slack-app", "ios", "issue", "keybindings", "listen", "log", "map", "marketplace", "max-tokens", "max-turns",
		"mcp", "memory", "metrics", "mobile", "mock-limits", "mock-parity", "model", "models", "notebook-edit", "notebook-read", "notifications", "oauth", "oauth-refresh", "onboarding", "open", "output-style", "parity", "passes", "paste", "perf-issue", "pin", "plugin", "plugins", "prefetch", "pr",
		"pr-comments", "pr_comments", "profile", "prompt", "prompt-history", "privacy-settings", "project", "providers", "permissions", "quit", "rate-limit", "rate-limit-options", "reasoning", "reload-plugins",
		"remote", "remote-control", "remote-env", "remote-setup", "rename", "report-schema", "reset", "reset-limits", "resume", "review", "reviewremote", "review-remote", "rollback", "safer-scope", "sandbox-toggle",
		"references", "scope", "search", "security-review", "self-test", "server", "settings", "setup", "setup-token", "setupgithubactions", "session", "sessions", "slash", "skill", "skills", "speak", "ssh", "state", "status", "statusline", "symbols",
		"bashes", "stash", "stale-base", "startup-report", "stickers", "stats", "summary", "system-prompt", "tasks", "team", "temperature", "telemetry", "templates", "terminal-setup", "terminalsetup", "theme", "tokens", "tool-details", "trust",
		"think-back", "thinkback", "thinkback-play", "todos", "undo", "unfocus", "validation",
		"teleport", "ultraplan", "ultrareview", "unpin", "upgrade", "usage", "version", "vim", "voice", "web-setup", "workspace", "cwd", "rewind":
		return true
	default:
		return false
	}
}

func argsHaveOutputFormat(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "--output-format" || arg == "-o" || strings.HasPrefix(arg, "--output-format=") {
			return true
		}
	}
	return false
}

func argsHaveInputFormat(args []string) bool {
	for _, arg := range args {
		if arg == "--input-format" || strings.HasPrefix(arg, "--input-format=") {
			return true
		}
	}
	return false
}

func argsHaveJSONSchema(args []string) bool {
	for _, arg := range args {
		if arg == "--json-schema" || strings.HasPrefix(arg, "--json-schema=") {
			return true
		}
	}
	return false
}

func argsHaveReplayUserMessages(args []string) bool {
	for _, arg := range args {
		if arg == "--replay-user-messages" {
			return true
		}
	}
	return false
}

func argsHaveIncludePartialMessages(args []string) bool {
	for _, arg := range args {
		if arg == "--include-partial-messages" {
			return true
		}
	}
	return false
}

type helpReport struct {
	Kind                    string   `json:"kind"`
	Action                  string   `json:"action"`
	Status                  string   `json:"status"`
	SchemaVersion           string   `json:"schema_version,omitempty"`
	Topic                   string   `json:"topic,omitempty"`
	Command                 string   `json:"command,omitempty"`
	Usage                   string   `json:"usage"`
	Help                    string   `json:"help"`
	Aliases                 []string `json:"aliases,omitempty"`
	Formats                 []string `json:"formats,omitempty"`
	Related                 []string `json:"related,omitempty"`
	LocalOnly               *bool    `json:"local_only,omitempty"`
	RequiresCredentials     *bool    `json:"requires_credentials,omitempty"`
	RequiresProviderRequest *bool    `json:"requires_provider_request,omitempty"`
	RequiresSessionResume   *bool    `json:"requires_session_resume,omitempty"`
	MutatesWorkspace        *bool    `json:"mutates_workspace,omitempty"`
	ServeStartsDaemon       *bool    `json:"serve_starts_daemon,omitempty"`
	OutputFields            []string `json:"output_fields,omitempty"`
	WorkspaceFields         []string `json:"workspace_fields,omitempty"`
	ConfigFields            []string `json:"config_fields,omitempty"`
	SessionFields           []string `json:"session_fields,omitempty"`
	GitFields               []string `json:"git_fields,omitempty"`
	SandboxFields           []string `json:"sandbox_fields,omitempty"`
	StatusValues            []string `json:"status_values,omitempty"`
	CheckNames              []string `json:"check_names,omitempty"`
	ProtocolFields          []string `json:"protocol_fields,omitempty"`
	ContractFields          []string `json:"contract_fields,omitempty"`
	ProtocolMethods         []string `json:"protocol_methods,omitempty"`
}

type helpCatalogReport struct {
	Kind     string             `json:"kind"`
	Action   string             `json:"action"`
	Status   string             `json:"status"`
	Query    string             `json:"query,omitempty"`
	Count    int                `json:"count"`
	Commands []helpCatalogEntry `json:"commands"`
}

type helpCatalogEntry struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases,omitempty"`
}

func renderHelpCommand(out io.Writer, args []string) error {
	format, topic, err := parseHelpArgs(args)
	if err != nil {
		return err
	}
	if topic != "" {
		fields := strings.Fields(topic)
		if len(fields) > 0 && strings.EqualFold(fields[0], "all") {
			return renderHelpCatalog(out, strings.Join(fields[1:], " "), format)
		}
		if spec, ok := commandHelpSpecFor(topic); ok {
			return renderCommandHelpSpec(out, spec, format)
		}
	}
	help := helpText(filepath.Base(os.Args[0]))
	if format == "json" {
		usage := "codog [flags] COMMAND [ARGS...]"
		if topic != "" {
			usage = "codog " + topic + " [ARGS...]"
		}
		data, _ := json.MarshalIndent(helpReport{
			Kind:   "help",
			Action: "show",
			Status: "ok",
			Topic:  topic,
			Usage:  usage,
			Help:   help,
		}, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, help)
	return nil
}

func renderHelpCatalog(out io.Writer, query string, format string) error {
	report := buildHelpCatalog(query)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Codog Commands")
	if report.Query != "" {
		fmt.Fprintf(out, "  Filter           %s\n", report.Query)
	}
	fmt.Fprintf(out, "  Commands         %d\n", report.Count)
	fmt.Fprintln(out, "  Use `codog help COMMAND` for detailed usage.")
	for _, command := range report.Commands {
		fmt.Fprintf(out, "\n  %-24s %s\n", command.Name, command.Description)
		fmt.Fprintf(out, "    %s\n", command.Usage)
	}
	return nil
}

func buildHelpCatalog(query string) helpCatalogReport {
	query = strings.ToLower(strings.TrimSpace(query))
	commands := make([]helpCatalogEntry, 0, len(builtInCommandNames()))
	for _, name := range builtInCommandNames() {
		spec, ok := commandHelpSpecFor(name)
		if !ok {
			continue
		}
		entry := helpCatalogEntry{
			Name:        name,
			Usage:       spec.Usage,
			Description: commandHelpDescription(spec),
			Aliases:     append([]string(nil), spec.Aliases...),
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join(append([]string{entry.Name, entry.Usage, entry.Description}, entry.Aliases...), "\n"))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		commands = append(commands, entry)
	}
	return helpCatalogReport{
		Kind:     "help",
		Action:   "catalog",
		Status:   "ok",
		Query:    query,
		Count:    len(commands),
		Commands: commands,
	}
}

func commandHelpDescription(spec commandHelpSpec) string {
	paragraphs := strings.Split(strings.TrimSpace(spec.Text), "\n\n")
	for _, paragraph := range paragraphs[1:] {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" || strings.HasPrefix(paragraph, "Usage:") {
			continue
		}
		return strings.Join(strings.Fields(paragraph), " ")
	}
	return "Run this Codog command."
}

func parseHelpArgs(args []string) (string, string, error) {
	format := "text"
	topicParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", "", errors.New("help output format is required")
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown help flag %q", arg)
		default:
			topicParts = append(topicParts, arg)
		}
	}
	if err := validateTextOrJSON(format, "help"); err != nil {
		return "", "", err
	}
	return format, strings.TrimSpace(strings.Join(topicParts, " ")), nil
}

type commandHelpSpec struct {
	Topic                   string
	Command                 string
	Usage                   string
	Text                    string
	SchemaVersion           string
	Aliases                 []string
	Formats                 []string
	Related                 []string
	LocalOnly               bool
	RequiresCredentials     bool
	RequiresProviderRequest bool
	RequiresSessionResume   bool
	MutatesWorkspace        bool
	ServeStartsDaemon       *bool
	OutputFields            []string
	WorkspaceFields         []string
	ConfigFields            []string
	SessionFields           []string
	GitFields               []string
	SandboxFields           []string
	StatusValues            []string
	CheckNames              []string
	ProtocolFields          []string
	ContractFields          []string
	ProtocolMethods         []string
	OmitOperationalMetadata bool
}

func localCommandHelpSpec(topic, command, usage, text string, fields, statuses []string, mutates bool) commandHelpSpec {
	return commandHelpSpec{
		Topic:                   topic,
		Command:                 command,
		Usage:                   usage,
		Text:                    text,
		LocalOnly:               true,
		RequiresCredentials:     false,
		RequiresProviderRequest: false,
		RequiresSessionResume:   false,
		MutatesWorkspace:        mutates,
		OutputFields:            fields,
		StatusValues:            statuses,
	}
}

func providerCommandHelpSpec(topic, command, usage, text string, fields, statuses []string) commandHelpSpec {
	return commandHelpSpec{
		Topic:                   topic,
		Command:                 command,
		Usage:                   usage,
		Text:                    text,
		LocalOnly:               false,
		RequiresCredentials:     true,
		RequiresProviderRequest: true,
		RequiresSessionResume:   false,
		MutatesWorkspace:        true,
		OutputFields:            fields,
		StatusValues:            statuses,
	}
}

func renderGlobalResumeHelp(out io.Writer, args []string) (bool, error) {
	if len(args) < 2 || (args[0] != "--resume" && args[0] != "-r" && args[0] != "--continue" && args[0] != "-c") || !isHelpFlag(args[1]) {
		return false, nil
	}
	return true, renderCommandHelpTopic(out, "resume", args[2:], requestedOutputFormat(args))
}

func renderCommandHelpRequest(out io.Writer, command string, args []string, fallbackFormat string) (bool, error) {
	if _, ok := commandHelpSpecFor(command); !ok {
		return false, nil
	}
	helpRequested := false
	for _, arg := range args {
		if isHelpFlag(arg) {
			helpRequested = true
		}
	}
	if !helpRequested {
		return false, nil
	}
	return true, renderCommandHelpTopic(out, command, commandHelpArgsWithoutHelp(args), fallbackFormat)
}

func positionalHelpSubcommand(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			continue
		case arg == "--output-format" || arg == "-o":
			index++
			continue
		case strings.HasPrefix(arg, "--output-format="):
			continue
		default:
			return strings.EqualFold(arg, "help")
		}
	}
	return false
}

func argsWithoutHelpSubcommand(args []string) []string {
	out := make([]string, 0, len(args))
	removed := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !removed {
			switch {
			case arg == "--json":
				out = append(out, arg)
				continue
			case arg == "--output-format" || arg == "-o":
				out = append(out, arg)
				if index+1 < len(args) {
					index++
					out = append(out, args[index])
				}
				continue
			case strings.HasPrefix(arg, "--output-format="):
				out = append(out, arg)
				continue
			case strings.EqualFold(arg, "help"):
				removed = true
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

func commandHelpArgsWithoutHelp(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if isHelpFlag(arg) {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func renderCommandHelpTopic(out io.Writer, topic string, args []string, fallbackFormat string) error {
	spec, ok := commandHelpSpecFor(topic)
	if !ok {
		return fmt.Errorf("unknown help topic %q", topic)
	}
	format, err := parseCommandHelpFormat(spec.Command, args, fallbackFormat)
	if err != nil {
		return renderCLIError(out, err, fallbackFormat)
	}
	return renderCommandHelpSpec(out, spec, format)
}

func parseCommandHelpFormat(command string, args []string, fallbackFormat string) (string, error) {
	format := strings.TrimSpace(fallbackFormat)
	if format == "" {
		format = "text"
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", missingArgumentError{Argument: "--output-format", Example: "--output-format json"}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("unknown %s help flag %q", command, arg)
		default:
			return "", fmt.Errorf("unknown %s help argument %q", command, arg)
		}
	}
	normalized, err := normalizeOutputFormat(command+" help", format, []string{"text", "json"})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func renderCommandHelpSpec(out io.Writer, spec commandHelpSpec, format string) error {
	if format == "json" {
		var localOnly, requiresCredentials, requiresProviderRequest, requiresSessionResume, mutatesWorkspace *bool
		if !spec.OmitOperationalMetadata {
			localOnly = boolPtr(spec.LocalOnly)
			requiresCredentials = boolPtr(spec.RequiresCredentials)
			requiresProviderRequest = boolPtr(spec.RequiresProviderRequest)
			requiresSessionResume = boolPtr(spec.RequiresSessionResume)
			mutatesWorkspace = boolPtr(spec.MutatesWorkspace)
		}
		data, _ := json.MarshalIndent(helpReport{
			Kind:                    "help",
			Action:                  "help",
			Status:                  "ok",
			SchemaVersion:           spec.SchemaVersion,
			Topic:                   spec.Topic,
			Command:                 spec.Command,
			Usage:                   spec.Usage,
			Help:                    spec.Text,
			Aliases:                 append([]string(nil), spec.Aliases...),
			Formats:                 append([]string(nil), spec.Formats...),
			Related:                 append([]string(nil), spec.Related...),
			LocalOnly:               localOnly,
			RequiresCredentials:     requiresCredentials,
			RequiresProviderRequest: requiresProviderRequest,
			RequiresSessionResume:   requiresSessionResume,
			MutatesWorkspace:        mutatesWorkspace,
			ServeStartsDaemon:       spec.ServeStartsDaemon,
			OutputFields:            append([]string(nil), spec.OutputFields...),
			WorkspaceFields:         append([]string(nil), spec.WorkspaceFields...),
			ConfigFields:            append([]string(nil), spec.ConfigFields...),
			SessionFields:           append([]string(nil), spec.SessionFields...),
			GitFields:               append([]string(nil), spec.GitFields...),
			SandboxFields:           append([]string(nil), spec.SandboxFields...),
			StatusValues:            append([]string(nil), spec.StatusValues...),
			CheckNames:              append([]string(nil), spec.CheckNames...),
			ProtocolFields:          append([]string(nil), spec.ProtocolFields...),
			ContractFields:          append([]string(nil), spec.ContractFields...),
			ProtocolMethods:         append([]string(nil), spec.ProtocolMethods...),
		}, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprint(out, spec.Text)
	return nil
}

func commandHelpSpecFor(topic string) (commandHelpSpec, bool) {
	switch strings.ToLower(strings.TrimSpace(topic)) {
	case "prompt", "p", "print":
		return providerCommandHelpSpec(
			"prompt",
			"prompt",
			`codog [flags] prompt [--stdin] [--attach PATH] [--input-format text|stream-json] [--max-budget-usd USD] "MESSAGE" [--json|--output-format text|json|stream-json]`,
			"Prompt\n\nUsage:\n  codog [flags] prompt [--stdin] [--attach PATH] [--input-format text|stream-json] [--max-budget-usd USD] \"MESSAGE\" [--json|--output-format text|json|stream-json]\n  codog -p \"MESSAGE\"\n\nRuns one provider-backed agent turn, streams assistant text by default, executes approved tools, and persists the turn to a JSONL session. Pass --stdin to append piped input to the prompt, --input-format stream-json to read Claude SDK user-message NDJSON, --max-budget-usd to cap estimated provider spend for the session turn, and --attach to include local text, image, or PDF files in the model request.\n",
			[]string{"session_id", "message", "tool_calls", "usage", "cost"},
			[]string{"ok", "error"},
		), true
	case "repl":
		return providerCommandHelpSpec(
			"repl",
			"repl",
			"codog [flags] repl",
			"REPL\n\nUsage:\n  codog [flags] repl\n\nStarts an interactive provider-backed session with slash commands, prompt history, permissions, hooks, and JSONL resume support.\n",
			[]string{"session_id", "message", "tool_calls", "usage"},
			[]string{"ok", "error"},
		), true
	case "tui":
		return providerCommandHelpSpec(
			"tui",
			"tui",
			"codog [flags] tui",
			"TUI\n\nUsage:\n  codog [flags] tui\n  codog [flags]\n\nStarts the inline Bubble Tea agent session and keeps completed turns in terminal scrollback. Enter sends the prompt, Alt+Enter or Ctrl+J inserts a newline, slash commands run inside the active session, and JSONL resume state is preserved.\n",
			[]string{"session_id", "message", "tool_calls", "usage"},
			[]string{"ok", "error"},
		), true
	case "paste":
		return localCommandHelpSpec(
			"paste",
			"paste",
			"codog paste [--print|--json] [--session ID] [--max-bytes N]",
			"Paste\n\nUsage:\n  codog paste [--print|--json] [--session ID] [--max-bytes N]\n  /paste [--print|--json]\n\nReads text from the system clipboard. Top-level `codog paste` prints clipboard text or a JSON report. In the REPL, `/paste` submits clipboard text as the next user message; use `/paste --print` or `/paste --json` to inspect without starting a provider turn.\n",
			[]string{"bytes", "lines", "clipboard", "submitted", "preview"},
			[]string{"ok", "error"},
			false,
		), true
	case "pin":
		return localCommandHelpSpec(
			"pin",
			"pin",
			"codog pin [message-index|last] [--session ID] [--output-format text|json]",
			"Pin\n\nUsage:\n  codog pin [message-index|last] [--session ID] [--output-format text|json]\n  /pin [message-index|last]\n\nPins a saved session message so manual compaction keeps it even when it is outside the recent-message window. Message indexes are entered as 1-based numbers; JSON reports include both zero-based `message_index` and 1-based `display_index`.\n",
			[]string{"session_id", "message_index", "display_index", "pinned_messages", "message_count"},
			[]string{"ok", "error"},
			true,
		), true
	case "unpin":
		spec, _ := commandHelpSpecFor("pin")
		spec.Topic = "unpin"
		spec.Command = "unpin"
		spec.Usage = "codog unpin [message-index|last] [--session ID] [--output-format text|json]"
		spec.Text = "Unpin\n\nUsage:\n  codog unpin [message-index|last] [--session ID] [--output-format text|json]\n  /unpin [message-index|last]\n\nRemoves a message pin from a saved session. Message indexes are entered as 1-based numbers; JSON reports include both zero-based `message_index` and 1-based `display_index`.\n"
		return spec, true
	case "bootstrap-plan":
		return localCommandHelpSpec(
			"bootstrap-plan",
			"bootstrap-plan",
			"codog bootstrap-plan [--output-format text|json]",
			"Bootstrap Plan\n\nUsage:\n  codog bootstrap-plan [--output-format text|json]\n\nLists the ordered local startup phases Codog prepares before dispatching a prompt or REPL turn. The report includes workspace resolution, config, memory, hooks, MCP, plugins, tool registration, session storage, startup hooks, and provider dispatch readiness without making a provider request.\n",
			[]string{"kind", "status", "workspace", "phase_count", "phases", "evidence"},
			[]string{"ok", "warn"},
			false,
		), true
	case "prefetch":
		return localCommandHelpSpec(
			"prefetch",
			"prefetch",
			"codog prefetch [run|status] [--output-format text|json]",
			"Prefetch\n\nUsage:\n  codog prefetch [run|status] [--output-format text|json]\n  /prefetch [run|status]\n\nRuns a read-only local startup preflight that warms workspace file metadata, effective config, project memory, MCP validation, plugin manifests, and the session store. The JSON report is stable evidence for startup readiness and degraded local surfaces without contacting a provider.\n",
			[]string{"kind", "status", "workspace", "task_count", "tasks", "evidence"},
			[]string{"ok", "warn"},
			false,
		), true
	case "deferred-init", "startup-report":
		spec := localCommandHelpSpec(
			"deferred-init",
			"deferred-init",
			"codog deferred-init [status|run] [--output-format text|json]",
			"Deferred Init\n\nUsage:\n  codog deferred-init [status|run] [--output-format text|json]\n  codog startup-report [same flags]\n\nReports the trust-gated deferred startup decisions Codog would apply after local preflight. The report mirrors claw-code's plugin_init, skill_init, mcp_prefetch, and session_hooks booleans, then adds task-level evidence for plugins, skills, MCP, hooks, notifications, and background startup. `run` executes the trust-gated local prefetch and embeds its report when the workspace is trusted and config loaded cleanly.\n",
			[]string{"kind", "status", "trusted", "plugin_init", "skill_init", "mcp_prefetch", "session_hooks", "executed", "prefetch", "tasks", "config_load_error"},
			[]string{"ready", "skipped", "warn"},
			false,
		)
		spec.Aliases = []string{"startup-report"}
		if topic == "startup-report" {
			spec.Topic = "startup-report"
			spec.Command = "startup-report"
			spec.Usage = "codog startup-report [status|run] [--output-format text|json]"
		}
		return spec, true
	case "status":
		return commandHelpSpec{
			Topic:                   "status",
			Command:                 "status",
			Usage:                   "codog status [--output-format text|json]",
			Text:                    "Status\n\nUsage:\n  codog status [--output-format text|json]\n\nShows local workspace, session, config, git, hook, MCP, and runtime status without making a provider request.\n",
			SchemaVersion:           "1.0",
			Formats:                 []string{"text", "json"},
			Related:                 []string{"/status", "codog --resume latest /status", "codog doctor"},
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"kind", "action", "status", "format_source", "format_raw", "format_overridden", "version", "workspace", "config", "session", "plan", "tools", "allowed_tools", "git", "lane_board", "sandbox", "runtime", "mcp_validation", "hook_validation"},
			WorkspaceFields:         []string{"path", "name", "memory_file_count", "memory_files"},
			ConfigFields:            []string{"config_home", "model", "model_env_var", "fast_mode", "base_url", "permission_mode", "permission_mode_raw", "permission_mode_source", "permission_mode_env_var", "permission_rules", "max_tokens", "max_turns", "auto_compact_messages", "auth_configured", "mcp_server_count", "hook_counts", "enabled_skill_count"},
			SessionFields:           []string{"active", "id", "path", "message_count", "saved_count", "created_at_ms", "updated_at_ms", "modified_epoch_millis", "parent_session_id", "branch_name", "lifecycle"},
			GitFields:               []string{"available", "error", "branch", "clean", "staged", "unstaged", "untracked", "conflicts", "freshness", "raw"},
			SandboxFields:           []string{"os", "default", "strategies", "available"},
			StatusValues:            []string{"ok", "warn", "degraded", "error"},
		}, true
	case "statusline":
		return localCommandHelpSpec(
			"statusline",
			"statusline",
			"codog statusline [--output-format text|json]",
			"Statusline\n\nUsage:\n  codog statusline [--output-format text|json]\n\nPrints a compact one-line local workspace status for shell prompts and editor integrations. When Claude Code statusLine JSON is piped on stdin, Codog reads compatible session, model, workspace, context, cost, agent, and worktree fields before rendering the line.\n",
			[]string{"source", "workspace", "session_id", "model", "permission_mode", "git_branch", "claude_statusline_input"},
			[]string{"ok", "error"},
			false,
		), true
	case "branch-lock", "branchlock":
		return localCommandHelpSpec(
			"branch-lock",
			"branch-lock",
			"codog branch-lock [check] [FILE|JSON] [--file PATH|--input JSON|--stdin] [--output-format text|json]",
			"Branch Lock\n\nUsage:\n  codog branch-lock [check] [FILE|JSON] [--file PATH|--input JSON|--stdin] [--output-format text|json]\n  codog branchlock [same flags]\n\nDetects branch/module collisions across concurrent lane intents. Input is either a JSON array of intents or an object with an `intents` array. Each intent accepts `lane_id` or `laneId`, `branch`, optional `worktree`, and `modules`.\n",
			[]string{"intent_count", "collision_count", "collisions", "branch", "module", "lane_ids"},
			[]string{"ok", "collision", "error"},
			false,
		), true
	case "stale-base", "base-check":
		return localCommandHelpSpec(
			"stale-base",
			"stale-base",
			"codog stale-base [check] [BASE_COMMIT] [--base-commit REF] [--output-format text|json]",
			"Stale Base\n\nUsage:\n  codog stale-base [check] [BASE_COMMIT] [--base-commit REF] [--output-format text|json]\n  codog base-check [same flags]\n\nChecks whether the current worktree HEAD still matches an expected base commit. The expected base is resolved from `--base-commit`, `.codog-base`, or compatible `.claw-base` in that order.\n",
			[]string{"status", "matches", "source", "expected", "actual", "warning"},
			[]string{"matches", "diverged", "no_expected_base", "not_git_repo", "error"},
			false,
		), true
	case "green-contract", "green":
		return localCommandHelpSpec(
			"green-contract",
			"green-contract",
			"codog green-contract [check] [--merge-ready] [--required-level LEVEL] [--observed-level LEVEL] [--test-command COMMAND] [--test-result COMMAND=EXIT] [--base-branch-fresh] [--recovery-context] [--blocking-flake NAME] [--output-format text|json]",
			"Green Contract\n\nUsage:\n  codog green-contract [check] [--merge-ready] [--required-level LEVEL] [--observed-level LEVEL] [--test-command COMMAND] [--test-result COMMAND=EXIT] [--base-branch-fresh] [--recovery-context] [--blocking-flake NAME] [--output-format text|json]\n  codog green [same flags]\n\nEvaluates structured evidence against a green/merge-ready contract. `--merge-ready` requires a passing test command, fresh base branch evidence, recovery attempt context, and no blocking known flakes.\n",
			[]string{"status", "contract", "evidence", "outcome", "missing", "blocking_flakes"},
			[]string{"satisfied", "unsatisfied", "error"},
			false,
		), true
	case "g004-conformance", "g004":
		return localCommandHelpSpec(
			"g004-conformance",
			"g004-conformance",
			"codog g004-conformance [validate] [FILE|JSON] [--input JSON|--file PATH|--stdin] [--output-format text|json]",
			"G004 Conformance\n\nUsage:\n  codog g004-conformance [validate] [FILE|JSON] [--input JSON|--file PATH|--stdin] [--output-format text|json]\n  codog g004 [same flags]\n\nValidates a G004 contract bundle with laneEvents, reports, and approvalTokens sections. The validator checks stable event metadata, terminal event fingerprints, report schema/projection/redaction fields, finding labels, field deltas, one-time approval tokens, and delegation-chain provenance.\n",
			[]string{"valid", "schema", "error_count", "errors", "path", "message"},
			[]string{"ok", "invalid", "error"},
			false,
		), true
	case "report-schema":
		return localCommandHelpSpec(
			"report-schema",
			"report-schema",
			"codog report-schema [registry|canonicalize|project] [--input JSON|--file PATH|--stdin] [--consumer NAME] [--field-family NAME] [--max-sensitivity public|internal|operator_only|secret] [--output-format text|json]",
			"Report Schema\n\nUsage:\n  codog report-schema registry [--output-format text|json]\n  codog report-schema canonicalize [--input JSON|--file PATH|--stdin] [--output-format text|json]\n  codog report-schema project [--input JSON|--file PATH|--stdin] [--consumer NAME] [--field-family NAME] [--max-sensitivity public|internal|operator_only|secret] [--output-format text|json]\n\nExposes the canonical report schema registry, canonicalizes reports with stable content hashes, and projects reports for consumers while recording omitted field families and redaction provenance.\n",
			[]string{"schema_version", "registry", "report", "projection", "provenance", "redactions"},
			[]string{"ok", "error"},
			false,
		), true
	case "trust":
		return localCommandHelpSpec(
			"trust",
			"trust",
			"codog trust [resolve] [SCREEN_TEXT] [--cwd PATH] [--worktree PATH] [--screen TEXT] [--allow PATTERN] [--deny PATH] [--no-events] [--output-format text|json]",
			"Trust\n\nUsage:\n  codog trust [resolve] [SCREEN_TEXT] [--cwd PATH] [--worktree PATH] [--screen TEXT] [--allow PATTERN] [--deny PATH] [--no-events] [--output-format text|json]\n\nResolves a detected workspace trust prompt against explicit allow and deny rules. This command reports the decision only; it does not change permissions or write trust state.\n",
			[]string{"status", "prompt_detected", "trusted", "policy", "resolution", "events"},
			[]string{"not_required", "auto_trusted", "requires_approval", "denied", "error"},
			false,
		), true
	case "acp":
		return commandHelpSpec{
			Topic:                   "acp",
			Command:                 "acp",
			Usage:                   "codog acp [serve|start|stdio] [--output-format text|json]",
			Text:                    "ACP / Zed\n\nUsage:\n  codog acp [serve|start|stdio] [--output-format text|json]\n  codog --acp [serve]\n  codog -acp [serve]\n\nShows or starts the editor-facing ACP/Zed bridge. Without a serve alias it reports the local launch contract; with `serve`, `start`, or `stdio` it starts a stdio JSON-RPC server for initialize, status, session, prompt, and shutdown requests.\n",
			Aliases:                 append([]string(nil), acpSlashAliases...),
			Formats:                 []string{"text", "json"},
			Related:                 []string{"/acp", "codog acp --output-format json", "codog acp serve"},
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			ServeStartsDaemon:       boolPtr(true),
			OutputFields:            []string{"schema_version", "kind", "action", "status", "supported", "launch_command", "protocol", "contracts", "aliases"},
			StatusValues:            []string{"ok", "error"},
			ProtocolFields:          []string{"name", "json_rpc", "daemon", "endpoint", "serve_starts_daemon", "methods"},
			ContractFields:          []string{"blocking_gates", "stable_status_surface", "unsupported_invocation_kind"},
			ProtocolMethods:         append([]string(nil), acpJSONRPCMethods...),
		}, true
	case "setup":
		return localCommandHelpSpec(
			"setup",
			"setup",
			"codog setup [status|init|terminal|all] [--shell zsh|bash|fish|powershell] [--path PATH] [--output-format text|json]",
			"Setup\n\nUsage:\n  codog setup [status|init|terminal|all] [--shell zsh|bash|fish|powershell] [--path PATH] [--output-format text|json]\n\nChecks local Codog setup, reports provider credentials, project memory, config paths, and terminal integration, and can initialize project guidance with `codog setup init`.\n",
			[]string{"workspace", "config_home", "checks", "project", "terminal", "messages"},
			[]string{"ok", "warn", "error"},
			true,
		), true
	case "onboarding":
		return localCommandHelpSpec(
			"onboarding",
			"onboarding",
			"codog onboarding [--path PATH] [--output-format text|json]",
			"Onboarding\n\nUsage:\n  codog onboarding [--path PATH] [--output-format text|json]\n\nInspects a repository for README, tests, language markers, Codog guidance, project config, git metadata, and repo-scope token guidance, then reports whether the workspace is ready for a productive Codog session.\n\nScope guidance: start Codog from the smallest useful package or service directory instead of the whole monorepo when possible, then add needed sibling paths with `codog add-dir`. Codog reports heavy/generated paths such as node_modules, dist, build, .next, coverage, logs, dumps, generated, and reports. `.gitignore` is honored by grep and glob when respectGitignore is enabled and by ls listings; `.codogignore`, `.claudeignore`, and `.clawignore` are honored by ls listings for local pruning.\n",
			[]string{"workspace", "has_readme", "has_tests", "primary_language", "checks", "recommendations", "scope_guidance"},
			[]string{"ready", "needs_setup", "error"},
			false,
		), true
	case "state":
		return localCommandHelpSpec(
			"state",
			"state",
			"codog state [--output-format text|json]",
			"State\n\nUsage:\n  codog state [--output-format text|json]\n\nShows the latest local worker state written by `codog repl` or `codog prompt <text>`. Produces state after an interactive REPL turn or a non-interactive prompt; if no state exists yet, rerun one of those commands first.\n",
			[]string{"worker_id", "mode", "status", "session_id", "model", "permission_mode", "updated_at"},
			[]string{"idle", "running", "completed", "error"},
			false,
		), true
	case "completion":
		return localCommandHelpSpec(
			"completion",
			"completion",
			"codog completion PREFIX [--limit N] [--output-format text|json] | codog completion bash|zsh|fish [--output PATH]",
			"Completion\n\nUsage:\n  codog completion PREFIX [--limit N] [--output-format text|json]\n  codog completion bash|zsh|fish [--output PATH]\n\nWith a regular prefix, lists local Go code completion candidates. With `bash`, `zsh`, or `fish`, prints a Claude-compatible shell completion script; pass `--output PATH` to write the script directly.\n",
			[]string{"kind", "query", "total", "completions"},
			[]string{"ok", "error"},
			false,
		), true
	case "code-intel":
		return localCommandHelpSpec(
			"code-intel",
			"code-intel",
			"codog code-intel [symbols|diagnostics|map|references|definition|hover|teleport|completion|format|notebook-read|notebook-edit|lsp] [ARGS...] [--output-format text|json]",
			"Code Intel\n\nUsage:\n  codog code-intel symbols [--output-format text|json]\n  codog code-intel diagnostics [patterns...] [--output-format text|json]\n  codog code-intel map [--depth N] [--limit N] [--output-format text|json]\n  codog code-intel references SYMBOL [--limit N] [--output-format text|json]\n  codog code-intel definition SYMBOL [--output-format text|json]\n  codog code-intel hover SYMBOL [--context N] [--output-format text|json]\n  codog code-intel teleport TARGET [--limit N] [--output-format text|json]\n  codog code-intel completion PREFIX [--output-format text|json]\n  codog code-intel format PATH [--write] [--output-format text|json]\n  codog code-intel notebook-read NOTEBOOK [--cell-index N] [--include-outputs] [--output-format text|json]\n  codog code-intel notebook-edit NOTEBOOK [--mode replace|insert|delete] [--cell-index N|--cell-id ID] [--cell-type code|markdown|raw] [--source TEXT] [--output-format text|json]\n  codog code-intel lsp [actions|discover|list|status|query|start|stop]\n\nRuns local code intelligence helpers, notebook inspection/editing, and LSP bridge operations without making a provider request.\n",
			[]string{"kind", "total", "symbols", "diagnostics", "references", "definition", "hover", "result", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "notebook-read":
		return localCommandHelpSpec(
			"notebook-read",
			"notebook-read",
			"codog notebook-read NOTEBOOK [--cell-index N] [--limit N] [--include-outputs] [--output-format text|json]",
			"Notebook Read\n\nUsage:\n  codog notebook-read NOTEBOOK [--cell-index N] [--limit N] [--include-outputs] [--output-format text|json]\n  codog code-intel notebook-read NOTEBOOK [same flags]\n\nReads Jupyter notebook metadata and cell source through the code intelligence notebook reader.\n",
			[]string{"path", "cell_count", "cells", "source_lines"},
			[]string{"ok", "error"},
			false,
		), true
	case "notebook-edit":
		return localCommandHelpSpec(
			"notebook-edit",
			"notebook-edit",
			"codog notebook-edit NOTEBOOK [--mode replace|insert|delete] [--cell-index N|--cell-id ID] [--cell-type code|markdown|raw] [--source TEXT] [--output-format text|json]",
			"Notebook Edit\n\nUsage:\n  codog notebook-edit NOTEBOOK [--mode replace|insert|delete] [--cell-index N|--cell-id ID] [--cell-type code|markdown|raw] [--source TEXT] [--output-format text|json]\n  codog code-intel notebook-edit NOTEBOOK [same flags]\n\nEdits Jupyter notebook cells through the code intelligence notebook writer.\n",
			[]string{"path", "mode", "cell_index", "cell_type", "cell_count", "source_lines"},
			[]string{"ok", "error"},
			true,
		), true
	case "context":
		return localCommandHelpSpec(
			"context",
			"context",
			"codog context [--session ID|--resume ID|latest] [--output-format text|json]",
			"Context\n\nUsage:\n  codog context [--session ID|--resume ID|latest] [--output-format text|json]\n\nShows the prompt preflight context assembled from memory, focused files, session history, skills, and token estimates.\n",
			[]string{"session_id", "memory", "focus", "messages", "token_estimate"},
			[]string{"ok", "error"},
			false,
		), true
	case "context-noninteractive":
		spec, _ := commandHelpSpecFor("context")
		spec.Topic = "context-noninteractive"
		spec.Command = "context-noninteractive"
		spec.Usage = "codog context-noninteractive [--session ID|--resume ID|latest] [--output-format text|json]"
		spec.Text = "Context Noninteractive\n\nUsage:\n  codog context-noninteractive [--session ID|--resume ID|latest] [--output-format text|json]\n\nAlias for `codog context`; prints the same local prompt preflight context without entering the REPL or making a provider request.\n"
		return spec, true
	case "memory":
		return localCommandHelpSpec(
			"memory",
			"memory",
			"codog memory [list|select|show|search|relevant|add|path|ensure|edit|reset] [ARGS...] [--all] [--confirm] [--limit N] [--editor COMMAND] [--no-open] [--output-format text|json]",
			"Memory\n\nUsage:\n  codog memory [list|select|show|search|relevant|add|path|ensure|edit|reset] [ARGS...] [--all] [--confirm] [--limit N] [--editor COMMAND] [--no-open] [--output-format text|json]\n\nDiscovers project memory files, previews selector candidates, shows or edits files, appends guidance, searches loaded memory lines, and clears selected memory files with confirmation.\n",
			[]string{"working_directory", "instruction_files", "files", "matches", "path", "selected", "options"},
			[]string{"ok", "ready", "created", "opened", "error"},
			true,
		), true
	case "ctx_viz", "context-viz":
		return localCommandHelpSpec(
			"ctx_viz",
			"ctx_viz",
			"codog ctx_viz [--session ID|--resume ID|latest] [--output PATH] [--output-format text|json]",
			"Context Visualization\n\nUsage:\n  codog ctx_viz [--session ID|--resume ID|latest] [--output PATH] [--output-format text|json]\n\nWrites an HTML context report that visualizes prompt inputs and session state.\n",
			[]string{"path", "session_id", "nodes", "edges"},
			[]string{"ok", "error"},
			true,
		), true
	case "workspace", "cwd":
		command := strings.ToLower(strings.TrimSpace(topic))
		if command == "cwd" {
			command = "cwd"
		} else {
			command = "workspace"
		}
		return localCommandHelpSpec(
			command,
			command,
			"codog workspace [status|PATH|set PATH] [--output-format text|json]",
			"Workspace\n\nUsage:\n  codog workspace [status|PATH|set PATH] [--output-format text|json]\n  codog cwd [PATH]\n\nShows or changes the current Codog runtime workspace. In REPL this updates subsequent slash commands, tool scope, and the workspace-scoped session store.\n",
			[]string{"workspace", "previous_workspace", "session_dir", "git_worktree", "effective_additional_dirs"},
			[]string{"ok", "error"},
			false,
		), true
	case "files":
		return localCommandHelpSpec(
			"files",
			"files",
			"codog files [PATH] [--glob GLOB] [--limit N] [--hidden] [--output-format text|json]",
			"Files\n\nUsage:\n  codog files [PATH] [--glob GLOB] [--limit N] [--hidden] [--output-format text|json]\n\nLists workspace-scoped files with optional glob filtering and includes a lightweight `scope_risk` preflight. The preflight warns when the current tree contains likely token sinks such as vendored dependencies, generated output, logs, dumps, archives, or large files, and recommends safer scope choices before a broad prompt flow.\n",
			[]string{"root", "files", "count", "truncated", "scope_risk"},
			[]string{"ok", "error"},
			false,
		), true
	case "scope", "safer-scope":
		spec := localCommandHelpSpec(
			"scope",
			"scope",
			"codog scope [status|preview|apply|restore] [--choice auto|workspace|ignore|both] [--target PATH] [--output-format text|json]",
			"Scope\n\nUsage:\n  codog scope [status|preview|apply|restore] [--choice auto|workspace|ignore|both] [--target PATH] [--output-format text|json]\n  codog safer-scope [same flags]\n  /scope [status|preview|apply|restore]\n\nTurns workspace token-risk warnings into reversible actions. `status` shows the current safer-scope state without mutating files. `preview` lists actionable safer-scope choices with included and excluded paths. `apply` records the broader workspace, then either switches the current runtime workspace to the safer source subdirectory, appends a reversible `.codogignore` block, or does both. `restore` returns to the recorded broader workspace and removes the generated ignore block when one was applied.\n",
			[]string{"status", "workspace", "active_workspace", "scope_risk", "choices", "applied", "restore_command"},
			[]string{"clean", "actionable", "applied", "restored", "no_action", "error"},
			true,
		)
		spec.Aliases = []string{"safer-scope", "/scope", "/safer-scope"}
		if topic == "safer-scope" {
			spec.Topic = "safer-scope"
			spec.Command = "safer-scope"
			spec.Usage = "codog safer-scope [status|preview|apply|restore] [--choice auto|workspace|ignore|both] [--target PATH] [--output-format text|json]"
		}
		return spec, true
	case "search":
		return localCommandHelpSpec(
			"search",
			"search",
			"codog search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--output-format text|json]",
			"Search\n\nUsage:\n  codog search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--output-format text|json]\n\nSearches workspace files with grep-style output and JSON result metadata.\n",
			[]string{"pattern", "matches", "count", "truncated"},
			[]string{"ok", "error"},
			false,
		), true
	case "validation":
		return localCommandHelpSpec(
			"validation",
			"validation",
			"codog validation [add-dir] [PATH...] [--output-format text|json]",
			"Validation\n\nUsage:\n  codog validation [add-dir] [PATH...] [--output-format text|json]\n\nValidates add-dir path candidates without mutating workspace configuration. With no paths, validates currently configured additional directories.\n",
			[]string{"workspace", "entries", "valid_count", "invalid_count", "already_allowed"},
			[]string{"ok", "error"},
			false,
		), true
	case "config", "settings":
		helpTopic := "config"
		if strings.EqualFold(strings.TrimSpace(topic), "settings") {
			helpTopic = "settings"
		}
		return localCommandHelpSpec(
			helpTopic,
			"config",
			"codog config|settings [show|get SECTION|paths|validate|set KEY VALUE|unset KEY|reset SECTION] [--output-format text|json]",
			"Config\n\nUsage:\n  codog config [show|get SECTION|paths|validate|set KEY VALUE|unset KEY|reset SECTION] [--output-format text|json]\n  codog config help [--output-format text|json]\n  codog config validate [--target user|project|local|all|--path PATH]\n  codog settings [same flags]\n\nInspects merged configuration, validates config JSON files, and updates user, project, or local config files. Use `codog config help` to list supported sections.\n",
			[]string{"paths", "config", "key", "value", "target", "errors", "warnings"},
			[]string{"ok", "error"},
			true,
		), true
	case "reset":
		return localCommandHelpSpec(
			"reset",
			"reset",
			"codog reset [status|SECTION|all --confirm] [--target user|project|local] [--output-format text|json]",
			"Reset\n\nUsage:\n  codog reset [status|SECTION|all --confirm] [--target user|project|local] [--output-format text|json]\n  codog config reset [SECTION|all --confirm]\n\nResets selected configuration sections to defaults. Whole-file reset requires `all --confirm`.\n",
			[]string{"section", "reset_keys", "changes", "path", "confirm_required"},
			[]string{"ok", "error"},
			true,
		), true
	case "api-key":
		return localCommandHelpSpec(
			"api-key",
			"api-key",
			"codog api-key [status|set KEY|clear] [--target user|project|local] [--output-format text|json]",
			"API Key\n\nUsage:\n  codog api-key [status|set KEY|clear] [--target user|project|local] [--output-format text|json]\n  codog api-key KEY\n\nShows, stores, or clears the configured API key. Text and JSON output always redact secret values.\n",
			[]string{"configured", "redacted_value", "source", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "auth":
		return localCommandHelpSpec(
			"auth",
			"auth",
			"codog auth [status|login|logout] [--json|--text]",
			"Auth\n\nUsage:\n  codog auth status [--json|--text]\n  codog auth login [--console|--claudeai] [PROFILE]\n  codog auth logout [PROFILE]\n\nClaude-compatible authentication entrypoint. `status` reports API key, auth token, and OAuth readiness without exposing secrets. `login` and `logout` delegate to Codog's OAuth login and logout flows.\n",
			[]string{"kind", "action", "ready", "auth_method", "api_key_configured", "auth_token_configured", "oauth_status"},
			[]string{"ok", "error"},
			true,
		), true
	case "setup-token":
		return localCommandHelpSpec(
			"setup-token",
			"setup-token",
			setupTokenUsage,
			"Setup Token\n\nUsage:\n  codog setup-token [TOKEN|--token TOKEN|--stdin] [--target user|project|local] [--path PATH] [--output-format text|json]\n\nImports a long-lived Claude OAuth token and stores it as `auth_token` for provider requests. Codog also honors `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_AUTH_TOKEN`, and `CODOG_AUTH_TOKEN` when the token is provided by the environment. Text and JSON output always redact token values.\n",
			[]string{"kind", "action", "status", "configured", "redacted_value", "path", "env_vars"},
			[]string{"ok", "error"},
			true,
		), true
	case "profile":
		return localCommandHelpSpec(
			"profile",
			"profile",
			"codog profile [list|show [NAME]|set NAME|clear] [--target user|project|local] [--output-format text|json]",
			"Profile\n\nUsage:\n  codog profile [list|show [NAME]|set NAME|clear] [--target user|project|local] [--output-format text|json]\n\nShows or switches the active OAuth provider profile used for stored-token refresh.\n",
			[]string{"active_profile", "active_configured", "resolved_profile", "resolved_source", "profile_count", "profile", "profiles", "oauth_status", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "oauth":
		return localCommandHelpSpec(
			"oauth",
			"oauth",
			"codog oauth pkce | discover ISSUER_URL | provider save|list|show|delete | device start|poll|login | browser start|exchange|login | status [PROFILE] | logout [PROFILE] | token save|show|refresh|revoke|delete [ARGS...]",
			"OAuth\n\nUsage:\n  codog oauth status [PROFILE]\n  codog oauth provider save NAME ISSUER_URL CLIENT_ID [SCOPE...]\n  codog oauth provider list|show NAME|delete NAME\n  codog oauth browser start|exchange|login [ARGS...]\n  codog oauth device start|poll|login [ARGS...]\n  codog oauth token save|show|refresh|revoke|delete [ARGS...]\n  codog login [browser|device] PROFILE\n  codog oauth-refresh [PROFILE]\n  codog logout [PROFILE]\n\nManages OAuth provider profiles and local stored tokens. Status, provider list/show, and token show are local read-only diagnostics; login, refresh, revoke, delete, and provider save/delete mutate local credentials or contact OAuth endpoints.\n",
			[]string{"profile_name", "profile_configured", "token_present", "ready", "provider_profiles", "token"},
			[]string{"ready", "missing", "expired", "error"},
			true,
		), true
	case "language":
		return localCommandHelpSpec(
			"language",
			"language",
			"codog language [status|show|LANGUAGE|set|use LANGUAGE|clear|off] [--target user|project|local] [--output-format text|json]",
			"Language\n\nUsage:\n  codog language [status|show|LANGUAGE|set|use LANGUAGE|clear|off] [--target user|project|local] [--output-format text|json]\n\nShows or changes the interface language preference stored as `language`. The preference is injected into provider prompts as runtime context. `current`, `get`, and `view` are aliases for `status`; `use`, `select`, `enable`, and `on` are aliases for `set`; `reset`, `unset`, `disable`, `off`, and `default` are aliases for `clear`.\n",
			[]string{"configured", "language", "previous", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "api":
		return localCommandHelpSpec(
			"api",
			"api",
			"codog api [routes|list|show|status|serve|listen|start] [ADDR|--addr ADDR] [--output-format text|json]",
			"API\n\nUsage:\n  codog api [routes|list|show|status] [--addr ADDR] [--output-format text|json]\n  codog api serve|listen|start [ADDR|--addr ADDR] [--output-format text|json]\n\nReports the local remote-control HTTP API URL, auth state, startup command, and route manifest. `serve`, `listen`, and `start` start the same local control API used by remote-control and IDE bridge clients.\n",
			[]string{"remote_url", "auth_required", "listening", "route_count", "routes", "remote_command"},
			[]string{"disabled", "enabled_without_auth", "ready", "serving"},
			false,
		), true
	case "server":
		return localCommandHelpSpec(
			"server",
			"server",
			serverUsage,
			"Server\n\nUsage:\n  codog server [--host HOST] [--port PORT] [--auth-token TOKEN] [--unix PATH] [--workspace DIR] [--idle-timeout MS] [--max-sessions N] [--output-format text|json]\n\nStarts the local Codog control API through a Claude Code-compatible entrypoint. The server exposes sessions, workspace files, terminal/background tasks, agents, MCP, code intelligence, notebook, and editor bridge routes. When --auth-token is omitted Codog generates a bearer token and prints it in the startup banner.\n",
			[]string{"kind", "status", "workspace", "network", "addr", "http_url", "auth_token", "idle_timeout_ms", "max_sessions", "max_sessions_enforced", "route_count"},
			[]string{"serving", "error"},
			false,
		), true
	case "open":
		return localCommandHelpSpec(
			"open",
			"open",
			openUsage,
			"Open\n\nUsage:\n  codog open <cc-url|http-url> [-p|--print [PROMPT]] [--output-format text|json|stream-json]\n  codog <cc-url|cc+unix-url> [-p|--print [PROMPT]] [--output-format text|json|stream-json]\n\nConnects to a Codog control server through a Claude Code-compatible direct-connect URL. `cc://HOST:PORT?authToken=TOKEN` maps to `http://HOST:PORT`; `cc://...?url=http://HOST:PORT` and plain HTTP(S) URLs are also accepted. A `cc://` or `cc+unix://` URL passed directly on the command line is routed to `open`; with `-p PROMPT`, Codog creates a remote session and submits the prompt through `/sessions/{id}/prompt`.\n",
			[]string{"kind", "status", "server_url", "session_id", "auth_token_configured", "prompt_submitted", "prompt_task"},
			[]string{"connected", "error"},
			false,
		), true
	case "ssh":
		return localCommandHelpSpec(
			"ssh",
			"ssh",
			sshUsage,
			"SSH\n\nUsage:\n  codog ssh <host> [dir] [-p|--print [PROMPT]] [--continue|-c] [--resume ID|latest] [--model MODEL] [--permission-mode MODE] [--dangerously-skip-permissions] [--local] [--execute] [--output-format text|json]\n\nStarts Codog on a remote host through SSH using a Claude Code-compatible command shape. Text mode deploys the local binary, then executes `ssh HOST 'cd DIR && env ... codog ... repl'` and forwards local provider credentials, model, base URL, Claude remote bootstrap variables, and session/model startup flags. With `-p/--print [PROMPT]`, the remote entrypoint uses `prompt [PROMPT]` for a headless one-shot run instead of `repl`. JSON mode renders a redacted plan by default; pass `--execute` with JSON output to run the command and capture stdout, stderr, exit code, and duration. `--local` runs the child Codog process locally for end-to-end plumbing tests.\n",
			[]string{"kind", "status", "host", "directory", "local", "print", "prompt_configured", "extra_args", "command", "deploy_command", "remote_shell", "remote_executable", "remote_env_keys", "remote_auth_forwarded", "executed", "exit_code", "duration_ms", "stdout", "stderr"},
			[]string{"planned", "completed", "failed", "canceled", "error"},
			false,
		), true
	case "model":
		return localCommandHelpSpec(
			"model",
			"model",
			modelUsage,
			"Model\n\nUsage:\n  codog model [MODEL|clear|reset|unset] [--target user|project|local] [--path PATH] [--output-format text|json]\n\nShows, changes, or clears the configured default model for future provider requests.\n",
			[]string{"model", "previous", "requested_model", "cleared"},
			[]string{"ok", "error"},
			true,
		), true
	case "models":
		spec := localCommandHelpSpec(
			"models",
			"models",
			modelsUsage,
			"Models\n\nUsage:\n  codog models [list|ls|aliases|shortcuts|routes|routing|search|find QUERY|show|view|inspect [MODEL]|current|set MODEL|clear|reset|help] [--target user|project|local] [--path PATH] [--output-format text|json]\n  codog model help [--output-format text|json]\n\nShows bounded local model-selection guidance, built-in aliases, routing rules, searchable model metadata, and local model diagnostics without making a provider request. `models set MODEL` stores the configured model through the same preference path as `codog model MODEL`; `models clear` removes that preference so the default model path is used again. Common discovery aliases such as `catalog`, `lookup`, `query`, `get`, and `describe` are normalized to canonical actions.\n",
			[]string{"default_model", "aliases", "routes", "configured_model", "resolved_configured_model", "provider", "wire_model", "requires_provider_request"},
			[]string{"ok", "error"},
			false,
		)
		spec.Aliases = []string{"model help"}
		spec.Related = []string{"codog model MODEL", "codog providers status", "codog config model"}
		return spec, true
	case "temperature":
		return localCommandHelpSpec(
			"temperature",
			"temperature",
			"codog temperature [VALUE|set VALUE|clear] [--target user|project|local] [--output-format text|json]",
			"Temperature\n\nUsage:\n  codog temperature [VALUE|set VALUE|clear] [--target user|project|local] [--output-format text|json]\n\nShows, stores, or clears the sampling temperature used for provider requests. Values must be between 0 and 1.\n",
			[]string{"configured", "temperature", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "permissions":
		return localCommandHelpSpec(
			"permissions",
			"permissions",
			"codog permissions [show|MODE|set MODE|clear] [--target user|project|local] [--output-format text|json]",
			"Permissions\n\nUsage:\n  codog permissions [show|read-only|workspace-write|danger-full-access|prompt|allow]\n  codog permissions set MODE [--target user|project|local|--path PATH]\n  codog permissions clear [--target user|project|local|--path PATH]\n\nShows, stores, or clears the default tool permission mode used for subsequent tool calls. Text output lists available modes and marks the current mode.\n",
			[]string{"permission_mode", "previous_mode", "path", "modes", "permission_rules"},
			[]string{"ok", "error"},
			true,
		), true
	case "capabilities":
		return localCommandHelpSpec(
			"capabilities",
			"capabilities",
			"codog capabilities [show|list|resolve NAME|audit] [--commands-snapshot PATH] [--tools-snapshot PATH] [--output-format text|json]",
			"Capabilities\n\nUsage:\n  codog capabilities [show|list] [--output-format text|json]\n  codog capabilities resolve NAME [--output-format text|json]\n  codog capabilities audit [--commands-snapshot PATH] [--tools-snapshot PATH] [--output-format text|json]\n\nReports the commands, slash commands, tools, protocols, MCP resources, and feature flags supported by this build. `resolve` projects the live execution registry for one command, slash command, tool, or tool alias. `audit` compares the live registry against explicit public JSON snapshots; it does not inspect another product's source tree.\n",
			[]string{"commands", "slash_commands", "tools", "features", "protocols", "mcp", "matches", "suggestions", "covered_count", "missing_count"},
			[]string{"ok", "gap", "not_found", "error"},
			false,
		), true
	case "cost":
		return localCommandHelpSpec(
			"cost",
			"cost",
			"codog [--session ID|--resume ID|latest] cost [--output-format text|json]",
			"Cost\n\nUsage:\n  codog [--session ID|--resume ID|latest] cost [--output-format text|json]\n\nPrints cumulative token usage and estimated cost for the selected session.\n",
			[]string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "total_tokens", "estimated_usd"},
			[]string{"ok", "error"},
			false,
		), true
	case "cache":
		return localCommandHelpSpec(
			"cache",
			"cache",
			"codog cache [--session ID|--resume ID|latest] [--output-format text|json]",
			"Cache\n\nUsage:\n  codog cache [--session ID|--resume ID|latest] [--output-format text|json]\n\nShows prompt cache creation and cache read token statistics recorded in the selected session JSONL usage records.\n",
			[]string{"session_id", "usage_records", "cache_creation_input_tokens", "cache_read_input_tokens", "cache_hit_ratio", "source"},
			[]string{"ok", "error"},
			false,
		), true
	case "caches":
		spec, _ := commandHelpSpecFor("cache")
		spec.Topic = "caches"
		spec.Command = "caches"
		spec.Usage = "codog caches [--session ID|--resume ID|latest] [--output-format text|json]"
		spec.Text = "Caches\n\nUsage:\n  codog caches [--session ID|--resume ID|latest] [--output-format text|json]\n\nAlias for `codog cache`; shows prompt cache creation and cache read token statistics recorded in the selected session JSONL usage records.\n"
		return spec, true
	case "break-cache":
		return localCommandHelpSpec(
			"break-cache",
			"break-cache",
			"codog break-cache [--session ID|--resume ID|latest] [MESSAGE] [--output-format text|json]",
			"Break Cache\n\nUsage:\n  codog break-cache [--session ID|--resume ID|latest] [MESSAGE] [--output-format text|json]\n\nAppends a local cache-breaker user message with a random nonce to the selected session so the next provider request has a different prompt prefix.\n",
			[]string{"session_id", "message_count", "nonce", "marker", "created_session"},
			[]string{"ok", "error"},
			true,
		), true
	case "extra-usage":
		return localCommandHelpSpec(
			"extra-usage",
			"extra-usage",
			"codog extra-usage [status|list|personal|admin] [--admin|--personal] [--no-open] [--output-format text|json]",
			"Extra Usage\n\nUsage:\n  codog extra-usage [status|list|personal|admin] [--admin|--personal] [--no-open] [--output-format text|json]\n\nOpens or reports the Claude extra usage settings URL and records a local visit count in Codog config. `status` and `list` read the current URL and visit count without mutating config.\n",
			[]string{"mode", "url", "opened", "visit_count", "path"},
			[]string{"ok", "open_failed", "error"},
			true,
		), true
	case "extra-usage-core":
		spec, _ := commandHelpSpecFor("extra-usage")
		spec.Topic = "extra-usage-core"
		spec.Command = "extra-usage-core"
		spec.Usage = "codog extra-usage-core [status|list|--admin|--personal] [--no-open] [--output-format text|json]"
		spec.Text = "Extra Usage Core\n\nUsage:\n  codog extra-usage-core [status|list|--admin|--personal] [--no-open] [--output-format text|json]\n\nCompatibility entrypoint for `codog extra-usage`; opens or reports the Claude extra usage settings URL. Use `status` for a non-mutating read.\n"
		return spec, true
	case "extra-usage-noninteractive":
		spec, _ := commandHelpSpecFor("extra-usage")
		spec.Topic = "extra-usage-noninteractive"
		spec.Command = "extra-usage-noninteractive"
		spec.Usage = "codog extra-usage-noninteractive [status|list|--admin|--personal] [--output-format text|json]"
		spec.Text = "Extra Usage Noninteractive\n\nUsage:\n  codog extra-usage-noninteractive [status|list|--admin|--personal] [--output-format text|json]\n\nCompatibility entrypoint for `codog extra-usage --no-open`; reports the Claude extra usage settings URL without launching a browser. Use `status` for a non-mutating read.\n"
		return spec, true
	case "install-slack-app":
		return localCommandHelpSpec(
			"install-slack-app",
			"install-slack-app",
			"codog install-slack-app [status|list] [--no-open] [--output-format text|json]",
			"Install Slack App\n\nUsage:\n  codog install-slack-app [status|list] [--no-open] [--output-format text|json]\n\nOpens or reports the Slack Marketplace URL for the Claude app and records a local install-page visit count. `status` and `list` read the URL and count without mutating config.\n",
			[]string{"url", "opened", "install_count", "path"},
			[]string{"ok", "open_failed", "error"},
			true,
		), true
	case "stickers":
		return localCommandHelpSpec(
			"stickers",
			"stickers",
			"codog stickers [status|list] [--no-open] [--output-format text|json]",
			"Stickers\n\nUsage:\n  codog stickers [status|list] [--no-open] [--output-format text|json]\n\nOpens or reports the Claude Code sticker order URL and records a local order-page visit count. `status` and `list` read the URL and count without mutating config.\n",
			[]string{"url", "opened", "order_count", "path"},
			[]string{"ok", "open_failed", "error"},
			true,
		), true
	case "passes":
		return localCommandHelpSpec(
			"passes",
			"passes",
			"codog passes [status|list|show|open|set-url URL|clear-url] [--docs] [--no-open] [--output-format text|json]",
			"Guest Passes\n\nUsage:\n  codog passes [status|list|show|open|set-url URL|clear-url] [--docs] [--no-open] [--output-format text|json]\n\nReports or opens Claude Code guest pass links. `status` and `list` read the configured referral URL and visit count without mutating config. `set-url` and `clear-url` manage the local referral URL stored under compatibility settings.\n",
			[]string{"url", "url_source", "docs_url", "referral_url", "referral_configured", "opened", "visit_count", "path"},
			[]string{"ok", "open_failed", "error"},
			true,
		), true
	case "metrics":
		return localCommandHelpSpec(
			"metrics",
			"metrics",
			"codog metrics [--session ID|--resume ID|latest] [--limit N] [--output-format text|json]",
			"Metrics\n\nUsage:\n  codog metrics [--session ID|--resume ID|latest] [--limit N] [--output-format text|json]\n\nShows local workspace and session performance and usage metrics from JSONL sessions, including token totals, cache hit ratio, top tools, and recent sessions.\n",
			[]string{"workspace_metrics", "session", "top_tools", "recent_sessions"},
			[]string{"ok", "error"},
			false,
		), true
	case "perf-issue":
		return localCommandHelpSpec(
			"perf-issue",
			"perf-issue",
			"codog perf-issue [--limit N] [--token-threshold N] [--tool-threshold N] [--write|--output PATH] [--output-format text|json]",
			"Performance Issue\n\nUsage:\n  codog perf-issue [--limit N] [--token-threshold N] [--tool-threshold N] [--write|--output PATH] [--output-format text|json]\n\nBuilds a local performance diagnosis bundle from saved JSONL sessions, token usage, prompt cache statistics, and tool counts. Use `--write` or `--output PATH` to save a Markdown report.\n",
			[]string{"workspace", "signals", "insights", "total_tokens", "file"},
			[]string{"ok", "warn", "empty", "error"},
			true,
		), true
	case "tokens":
		return localCommandHelpSpec(
			"tokens",
			"tokens",
			"codog [--session ID|--resume ID|latest] tokens [--output-format text|json]",
			"Tokens\n\nUsage:\n  codog [--session ID|--resume ID|latest] tokens [--output-format text|json]\n\nShows cumulative token usage for the selected session.\n",
			[]string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "total_tokens", "estimated_usd"},
			[]string{"ok", "error"},
			false,
		), true
	case "budget":
		return localCommandHelpSpec(
			"budget",
			"budget",
			"codog budget [status|show|ls|set|use|reset|clear|off] [--max-tokens N] [--max-turns N] [--target user|project|local] [--output-format text|json]",
			"Budget\n\nUsage:\n  codog budget [status|show|ls|set|use|reset|clear|off] [--max-tokens N] [--max-turns N] [--target user|project|local] [--output-format text|json]\n\nShows or changes token budget limits backed by `max_tokens` and `max_turns`. `current`, `view`, and `get` are aliases for `show`; `use`, `update`, and `configure` are aliases for `set`; `unset`, `disable`, `off`, and `default` are aliases for `reset`.\n",
			[]string{"max_tokens", "max_turns", "previous", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "max-tokens":
		return localCommandHelpSpec(
			"max-tokens",
			"max-tokens",
			"codog max-tokens [N] [--output-format text|json]",
			"Max Tokens\n\nUsage:\n  codog max-tokens [N] [--output-format text|json]\n\nShows or changes the runtime max output token limit for the current process. Use `codog budget set --max-tokens N` to persist the preference.\n",
			[]string{"max_tokens", "previous_max_tokens", "requested_max_tokens"},
			[]string{"ok", "error"},
			false,
		), true
	case "max-turns":
		return localCommandHelpSpec(
			"max-turns",
			"max-turns",
			"codog max-turns [N] [--output-format text|json]",
			"Max Turns\n\nUsage:\n  codog max-turns [N] [--output-format text|json]\n\nShows or changes the runtime model/tool loop limit for the current process. Use `codog budget set --max-turns N` to persist the preference.\n",
			[]string{"max_turns", "previous_max_turns", "requested_max_turns"},
			[]string{"ok", "error"},
			false,
		), true
	case "rate-limit":
		return localCommandHelpSpec(
			"rate-limit",
			"rate-limit",
			"codog rate-limit [status|set|reset] [--max-retries N] [--initial-backoff-ms N] [--max-backoff-ms N] [--target user|project|local] [--output-format text|json]",
			"Rate Limit\n\nUsage:\n  codog rate-limit [status|set|reset] [--max-retries N] [--initial-backoff-ms N] [--max-backoff-ms N] [--target user|project|local] [--output-format text|json]\n\nShows or changes provider retry and backoff settings stored under `rate_limit`.\n",
			[]string{"max_retries", "initial_backoff_ms", "max_backoff_ms", "retryable_statuses", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "ant-trace":
		return commandHelpSpec{
			Topic:                   "ant-trace",
			Command:                 "ant-trace",
			Usage:                   "codog ant-trace [--no-request] [--message TEXT] [--timeout-ms N] [--write|--output PATH] [--output-format text|json]",
			Text:                    "Anthropic Trace\n\nUsage:\n  codog ant-trace [--no-request] [--message TEXT] [--timeout-ms N] [--write|--output PATH] [--output-format text|json]\n\nDiagnoses the configured Anthropic-compatible provider. With `--no-request`, it only reports local provider configuration, credential presence, and retry settings. Without `--no-request`, it sends a small streaming request and records elapsed time, stream events, usage, text preview, and provider errors.\n",
			LocalOnly:               false,
			RequiresCredentials:     false,
			RequiresProviderRequest: true,
			RequiresSessionResume:   false,
			MutatesWorkspace:        true,
			OutputFields:            []string{"provider", "model", "base_url", "auth_configured", "request_sent", "elapsed_ms", "stream_events", "usage", "rate_limit", "error"},
			StatusValues:            []string{"ok", "skipped", "error"},
		}, true
	case "rate-limit-options":
		return localCommandHelpSpec(
			"rate-limit-options",
			"rate-limit-options",
			"codog rate-limit-options [--output-format text|json]",
			"Rate Limit Options\n\nUsage:\n  codog rate-limit-options [--output-format text|json]\n\nShows effective provider retry and backoff settings.\n",
			[]string{"max_retries", "initial_backoff_ms", "max_backoff_ms", "retryable_statuses"},
			[]string{"ok", "error"},
			false,
		), true
	case "mock-limits", "mock-server":
		spec := localCommandHelpSpec(
			"mock-limits",
			"mock-limits",
			"codog mock-limits [show|status|plan|serve|server|start|ADDR] [--failures N] [--retry-after-ms N] [--addr ADDR] [--output-format text|json]",
			"Mock Limits\n\nUsage:\n  codog mock-limits [show|status|plan|serve|server|start|ADDR] [--failures N] [--retry-after-ms N] [--addr ADDR] [--output-format text|json]\n\nShows or starts an Anthropic-compatible local mock server that returns HTTP 429 for the first N requests and then streams a normal response. `status` and `plan` are aliases for `show`; `server` and `start` are aliases for `serve`. Use it to test provider retry and backoff behavior with `codog rate-limit` settings.\n",
			[]string{"addr", "base_url", "failures", "retry_after_ms", "endpoint"},
			[]string{"ready", "serving", "error"},
			false,
		)
		spec.Aliases = []string{"mock-server"}
		if strings.EqualFold(strings.TrimSpace(topic), "mock-server") {
			spec.Topic = "mock-server"
			spec.Command = "mock-server"
			spec.Usage = "codog mock-server [ADDR] [--failures N] [--retry-after-ms N] [--output-format text|json]"
			spec.Text = "Mock Server\n\nUsage:\n  codog mock-server [ADDR] [--failures N] [--retry-after-ms N] [--output-format text|json]\n\nAlias for `codog mock-limits serve`; starts the local retry and streaming test server.\n"
			spec.Aliases = []string{"mock-limits"}
		}
		return spec, true
	case "mock-parity", "parity", "self-test":
		spec := localCommandHelpSpec(
			"mock-parity",
			"mock-parity",
			"codog mock-parity [run|check|manifest] [--report PATH] [--output-format text|json]",
			"Mock Parity\n\nUsage:\n  codog mock-parity [run|check|manifest] [--report PATH] [--output-format text|json]\n  codog parity [same flags]\n  codog self-test [same flags]\n\nRuns the deterministic mock provider parity harness against the local agent loop. It exercises streaming, tool calls, permission prompts, plugin tools, auto-compaction, and usage/cost accounting without contacting a real provider. Use `manifest` to print scenario metadata without running the harness. Set MOCK_PARITY_REPORT_PATH or pass --report to write the machine-readable report to disk.\n",
			[]string{"schema_version", "ok", "passed", "total", "scenario_count", "request_count", "coverage", "capability_coverage", "scenarios", "usage_summary", "estimated_cost"},
			[]string{"ok", "error"},
			false,
		)
		spec.Aliases = []string{"parity", "self-test"}
		spec.Related = []string{"codog capabilities", "codog mock-limits"}
		return spec, true
	case "reset-limits":
		return localCommandHelpSpec(
			"reset-limits",
			"reset-limits",
			"codog reset-limits [--target user|project|local] [--path PATH] [--output-format text|json]",
			"Reset Limits\n\nUsage:\n  codog reset-limits [--target user|project|local] [--path PATH] [--output-format text|json]\n\nClears local provider retry and backoff overrides stored under `rate_limit`.\n",
			[]string{"previous", "current", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "rollback":
		return localCommandHelpSpec(
			"rollback",
			"rollback",
			"codog rollback [TARGET] [--output-format text|json]",
			"Rollback\n\nUsage:\n  codog rollback [TARGET] [--output-format text|json]\n\nRestores TARGET from the updater backup at TARGET.bak. When TARGET is omitted, Codog uses the current executable path, matching `codog updater rollback`.\n",
			[]string{"kind", "action", "status", "result", "target", "backup_path", "rolled_back"},
			[]string{"ok", "error"},
			true,
		), true
	case "updater":
		return localCommandHelpSpec(
			"updater",
			"updater",
			"codog updater [status|show|check|verify|download|install|rollback] [ARGS...] [--output-format text|json]",
			"Updater\n\nUsage:\n  codog updater status\n  codog updater check [URL] [PUBLIC_KEY]\n  codog updater verify URL PUBLIC_KEY\n  codog updater download [URL] [PLATFORM] [DEST] [PUBLIC_KEY]\n  codog updater install ARTIFACT [TARGET]\n  codog updater rollback [TARGET]\n\nInspects local update artifacts and performs explicit manifest checks, downloads, installs, or backup rollback. No official update channel is bundled.\n",
			[]string{"kind", "action", "status", "result"},
			[]string{"ok", "error"},
			true,
		), true
	case "enterprise":
		return localCommandHelpSpec(
			"enterprise",
			"enterprise",
			"codog enterprise [audit|status|show] [LIMIT] | codog enterprise verify POLICY PUBLIC_KEY",
			"Enterprise\n\nUsage:\n  codog enterprise [audit|status|show] [LIMIT]\n  codog enterprise verify POLICY PUBLIC_KEY\n\nAudits local permission events and verifies explicitly supplied signed policy files. Codog does not connect to an organization policy service.\n",
			[]string{"kind", "action", "status", "events", "summary", "warnings"},
			[]string{"ok", "warn", "error"},
			false,
		), true
	case "dump-manifests":
		return localCommandHelpSpec(
			"dump-manifests",
			"dump-manifests",
			"codog dump-manifests [--manifests-dir PATH] [--output-format text|json]",
			"Dump Manifests\n\nUsage:\n  codog dump-manifests [--manifests-dir PATH] [--output-format text|json]\n\nDiscovers local tool, command, skill, agent, hook, plugin, and MCP manifests for the selected workspace.\n",
			[]string{"workspace", "tools", "commands", "skills", "agents", "hooks", "plugins", "mcp_servers"},
			[]string{"ok", "error"},
			false,
		), true
	case "notifications":
		return localCommandHelpSpec(
			"notifications",
			"notifications",
			"codog notifications [on|off|toggle|status|clear] [--target user|project|local] [--output-format text|json]",
			"Notifications\n\nUsage:\n  codog notifications [on|off|toggle|status|clear] [--target user|project|local] [--output-format text|json]\n\nShows or changes whether Codog notification hooks run. Notification hooks remain configured under `hooks.notification`; this command controls the runtime preference.\n",
			[]string{"enabled", "configured", "hook_count", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "telemetry":
		return localCommandHelpSpec(
			"telemetry",
			"telemetry",
			"codog telemetry [on|off|toggle|status|clear] [--target user|project|local] [--output-format text|json]",
			"Telemetry\n\nUsage:\n  codog telemetry [on|off|toggle|status|clear] [--target user|project|local] [--output-format text|json]\n\nShows or changes the local telemetry privacy preference stored as `privacy_settings.telemetry_enabled`.\n",
			[]string{"enabled", "configured", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "effort", "reasoning":
		command := strings.ToLower(strings.TrimSpace(topic))
		title := "Effort"
		if command == "reasoning" {
			title = "Reasoning"
		}
		return localCommandHelpSpec(
			command,
			command,
			fmt.Sprintf("codog %s [auto|low|medium|high|clear] [--target user|project|local] [--output-format text|json]", command),
			fmt.Sprintf("%s\n\nUsage:\n  codog %s [auto|low|medium|high|clear] [--target user|project|local] [--output-format text|json]\n\nShows or changes the reasoning effort preference stored as `reasoning_effort`. The preference is injected into provider prompts as runtime context.\n", title, command),
			[]string{"effort", "previous", "available", "path"},
			[]string{"ok", "error"},
			true,
		), true
	case "hooks":
		return localCommandHelpSpec(
			"hooks",
			"hooks",
			"codog hooks [list|health EVENT|run EVENT|watch-paths list|check] [--output-format text|json]",
			"Hooks\n\nUsage:\n  codog hooks [list|health EVENT|run EVENT] [--output-format text|json]\n  codog hooks watch-paths [list|check] [SESSION] [--output-format text|json]\n\nLists configured hooks, reports hook health, runs a hook event with supplied local metadata, or checks SessionStart watch paths for file changes.\n",
			[]string{"event", "hooks", "health", "results", "changes"},
			[]string{"ok", "warn", "error"},
			true,
		), true
	case "marketplace", "plugin", "plugins":
		command := strings.TrimSpace(topic)
		if command == "" {
			command = "marketplace"
		}
		return localCommandHelpSpec(
			command,
			"marketplace",
			"codog marketplace [list|health|show|info|describe ID|validate PATH|sources [list|add|remove|clear]|remote [list|search|show]|browse|updates|install|install-remote|update|enable|disable|remove|settings]",
			"Marketplace\n\nUsage:\n  codog marketplace list\n  codog marketplace health\n  codog marketplace show|info|describe ID\n  codog marketplace sources [list|add URL [PUBLIC_KEY]|remove URL|clear] [--target user|project|local]\n  codog marketplace remote [URL] [PUBLIC_KEY]\n  codog marketplace remote list|search QUERY|show ID [--url URL] [--public-key KEY] [--page N] [--per-page N]\n  codog marketplace install PATH\n  codog marketplace install-remote ID [URL] [PUBLIC_KEY]\n  codog marketplace update ID [URL] [PUBLIC_KEY]\n  codog marketplace settings\n\nManages local plugins, validates plugin manifests, reports plugin startup health, configures trusted marketplace index URLs, browses and searches remote marketplace indexes, installs signed remote plugins, and updates installed plugins. `info` and `describe` are aliases for `show`.\n",
			[]string{"plugins", "plugin_health", "sources", "marketplace_url", "signature_valid", "checksum_valid", "path"},
			[]string{"ok", "healthy", "degraded", "failed", "error"},
			true,
		), true
	case "mcp":
		return localCommandHelpSpec(
			"mcp",
			"mcp",
			"codog mcp [list|ls|serve|self|show|info|describe|inspect|add|remove|tools [SERVER]|auth [--refresh|refresh|--clear|clear|logout] [SERVER]|call|invoke|resources [SERVER]|resource-templates [SERVER]|read|prompts [SERVER]|prompt]",
			"MCP\n\nUsage:\n  codog mcp list|ls\n  codog mcp show|info|describe|inspect SERVER\n  codog mcp add NAME COMMAND [ARG...] [--env KEY=VALUE] [--tool-call-timeout-ms N] [--required]\n  codog mcp add NAME --url URL [--header KEY=VALUE] [--headers-helper COMMAND] [--required]\n  codog mcp tools|tool|resources|resource|resource-templates|templates|prompts [SERVER]\n  codog mcp auth|oauth [--refresh|refresh|--clear|clear|logout] [SERVER]\n  codog mcp call|invoke SERVER TOOL JSON\n  codog mcp read|read-resource SERVER URI\n  codog mcp prompt|get-prompt SERVER NAME [JSON]\n\nServes Codog tools over stdio MCP and manages configured stdio and HTTP MCP clients, tools, resources, prompts, and OAuth readiness. Discovery commands without SERVER aggregate all configured servers. Common singular forms and `info`, `describe`, and `inspect` are normalized to the canonical actions.\n",
			[]string{"servers", "tools", "resources", "prompts", "result"},
			[]string{"ok", "error"},
			true,
		), true
	case "good-claude":
		return localCommandHelpSpec(
			"good-claude",
			"good-claude",
			"codog good-claude [MESSAGE] [--output-format text|json]",
			"good-claude\n\nUsage:\n  codog good-claude [MESSAGE] [--output-format text|json]\n\nCompatibility entrypoint for positive feedback. It returns a local acknowledgement and points to `codog feedback` for persisted feedback drafts.\n",
			[]string{"message", "next_command", "args"},
			[]string{"ok", "error"},
			false,
		), true
	case "exit":
		return localCommandHelpSpec(
			"exit",
			"exit",
			"codog exit [--output-format text|json]",
			"Exit\n\nUsage:\n  codog exit [--output-format text|json]\n\nCompatibility entrypoint for the REPL exit command. In a non-interactive CLI invocation it reports success and exits with status 0.\n",
			[]string{"message", "args"},
			[]string{"ok"},
			false,
		), true
	case "reviewremote", "review-remote":
		return localCommandHelpSpec(
			"reviewRemote",
			"reviewRemote",
			"codog reviewRemote [PR|URL|NUMBER] [--repo OWNER/REPO] [--staged] [--base REF] [--limit N] [--output-format text|json]",
			"Review Remote\n\nUsage:\n  codog reviewRemote [PR|URL|NUMBER] [--repo OWNER/REPO] [--staged] [--base REF] [--limit N] [--output-format text|json]\n\nRuns the local changed-file review and fetches GitHub pull request issue/review comments through `gh`, then returns a combined local and remote review report.\n",
			[]string{"local_review", "remote_comments", "repository", "pull_request", "signals"},
			[]string{"ok", "clean", "comments", "findings", "error"},
			false,
		), true
	case "autofix-pr":
		return localCommandHelpSpec(
			"autofix-pr",
			"autofix-pr",
			"codog autofix-pr [PR|URL|NUMBER] [--repo OWNER/REPO] [--limit N] [--write|--output PATH] [--output-format text|json]",
			"Autofix PR\n\nUsage:\n  codog autofix-pr [PR|URL|NUMBER] [--repo OWNER/REPO] [--limit N] [--write|--output PATH] [--output-format text|json]\n\nFetches GitHub pull request comments with `gh` and prepares a focused Codog fix prompt. Use `--write` or `--output PATH` to save the task package as Markdown under the workspace.\n",
			[]string{"repository", "pull_request", "items", "prompt", "file"},
			[]string{"ready", "no_comments", "error"},
			true,
		), true
	case "install-github-app":
		return localCommandHelpSpec(
			"install-github-app",
			"install-github-app",
			"codog install-github-app [--workflow claude|review|all] [--secret-name NAME] [--dry-run] [--force] [--output-format text|json]",
			"Install GitHub App\n\nUsage:\n  codog install-github-app [--workflow claude|review|all] [--secret-name NAME] [--dry-run] [--force] [--output-format text|json]\n\nCreates Claude Code GitHub Actions workflow files for issue comments and pull request review automation.\n",
			[]string{"workspace", "repo", "secret_name", "workflows", "instructions", "warnings"},
			[]string{"ok", "error"},
			true,
		), true
	case "setupgithubactions":
		spec, _ := commandHelpSpecFor("install-github-app")
		spec.Topic = "setupGitHubActions"
		spec.Command = "setupGitHubActions"
		spec.Usage = "codog setupGitHubActions [--workflow claude|review|all] [--secret-name NAME] [--dry-run] [--force] [--output-format text|json]"
		spec.Text = "Setup GitHub Actions\n\nUsage:\n  codog setupGitHubActions [--workflow claude|review|all] [--secret-name NAME] [--dry-run] [--force] [--output-format text|json]\n\nCompatibility entrypoint for `codog install-github-app`; creates Claude Code GitHub Actions workflow files for issue comments and pull request review automation.\n"
		return spec, true
	case "skill", "skills":
		spec := localCommandHelpSpec(
			"skills",
			"skills",
			"codog skill|skills [list|ls|search|find|audit|doctor|sources|roots|status|enable|disable|show|info|describe|view|invoke|run|exec|add|install|uninstall|remove|rm|help]",
			"Skills\n\nUsage:\n  codog skills [list|ls|search|find|audit|doctor|sources|roots|status|enable|disable|show|info|describe|view|invoke|run|exec|add|install|uninstall|remove|rm|help]\n  codog skills search QUERY [--output-format text|json]\n  codog skills audit [--output-format text|json]\n  codog skill [same actions]\n\nLists, searches, audits sources, enables, disables, renders, invokes, installs, or removes bundled, user, workspace, plugin, compatible Claude Markdown skills, and legacy `/commands` Markdown exposed as skill-like compatibility entries. `ls` is an alias for `list`; `search`, `find`, `query`, and `lookup` filter skills by name, display name, or description; `audit`, `doctor`, `check`, and `validate` report source health, enabled skill resolution, shadowing, and metadata drift; `root` and `roots` are aliases for `sources`; `info`, `describe`, `get`, `view`, and `cat` are aliases for `show`; `run`, `exec`, `execute`, and `call` are aliases for `invoke`; `add` is an alias for `install`; `remove`, `rm`, and `del` are aliases for `uninstall`; `on` and `off` are aliases for `enable` and `disable`. Run `codog skills help` for this local command reference.\n",
			[]string{"skills", "roots", "sources", "enabled_skills", "resolved_skills", "missing_skills", "name", "path", "body", "origin", "active", "shadowed_by", "metadata_drift", "metadata_drift_count"},
			[]string{"ok", "degraded", "error"},
			true,
		)
		spec.Aliases = []string{"skill"}
		if strings.EqualFold(strings.TrimSpace(topic), "skill") {
			spec.Topic = "skill"
			spec.Command = "skill"
			spec.Usage = "codog skill [list|ls|search|find|audit|doctor|sources|roots|status|enable|disable|show|view|invoke|run|exec|add|install|uninstall|remove|rm|help]"
			spec.Aliases = []string{"skills"}
		}
		return spec, true
	case "commands":
		return localCommandHelpSpec(
			"commands",
			"commands",
			"codog commands [list|ls|search|find|audit|doctor|sources|roots|show|view|run|render|exec|add|install|uninstall|remove|rm]",
			"Commands\n\nUsage:\n  codog commands [list|ls|search|find|audit|doctor|sources|roots|show|view|run|render|exec|add|install|uninstall|remove|rm]\n  codog commands search QUERY [--output-format text|json]\n  codog commands audit [--output-format text|json]\n  codog commands install [--project|--user|--claude] [--name NAME] SOURCE [--output-format text|json]\n\nLists, searches, audits sources, shows, renders, installs, or removes custom Markdown slash commands from Codog and compatible Claude command directories. `ls` is an alias for `list`; `search`, `find`, `query`, and `lookup` filter commands by name, source, preview, or body; `audit`, `doctor`, `check`, and `validate` report source health, active and shadowed commands, and frontmatter parse errors; `root` and `roots` are aliases for `sources`; `info`, `describe`, `get`, `view`, and `cat` are aliases for `show`; `render`, `exec`, `execute`, `call`, and `invoke` are aliases for `run`; `add` is an alias for `install`; `remove`, `rm`, and `del` are aliases for `uninstall`.\n",
			[]string{"commands", "roots", "sources", "query", "name", "path", "body", "active", "shadowed_by", "frontmatter_errors", "frontmatter_error_count"},
			[]string{"ok", "degraded", "error"},
			false,
		), true
	case "slash":
		return localCommandHelpSpec(
			"slash",
			"slash",
			"codog slash [list|show COMMAND|candidates PREFIX] [--output-format text|json]",
			"Slash\n\nUsage:\n  codog slash [list] [--output-format text|json]\n  codog slash show /status [--output-format text|json]\n  codog slash candidates /st [--output-format text|json]\n\nLists built-in slash commands, runtime custom command entries, resume-safe slash commands, and TUI/REPL completion candidates. This is a non-interactive discovery surface for the same slash registry used by the REPL and TUI.\n",
			[]string{"commands", "runtime", "candidates", "resume_safe"},
			[]string{"ok", "error"},
			false,
		), true
	case "templates":
		return localCommandHelpSpec(
			"templates",
			"templates",
			"codog templates [list|ls|search|find|audit|doctor|sources|roots|show|view|apply|render|run|add|install|uninstall|remove|rm]",
			"Templates\n\nUsage:\n  codog templates [list|ls|search|find|audit|doctor|sources|roots|show|view|apply|render|run|add|install|uninstall|remove|rm]\n  codog templates search QUERY [--output-format text|json]\n  codog templates audit [--output-format text|json]\n  codog templates install [--project|--user] [--name NAME] SOURCE [--output-format text|json]\n\nLists, searches, audits sources, shows, renders, installs, or removes parameterized prompt templates. `ls` is an alias for `list`; `search`, `find`, `query`, and `lookup` filter templates by name, source, preview, or body; `audit`, `doctor`, `check`, and `validate` report source health plus active and shadowed templates; `root` and `roots` are aliases for `sources`; `info`, `describe`, `get`, `view`, and `cat` are aliases for `show`; `render`, `run`, `exec`, `execute`, `call`, and `invoke` are aliases for `apply`; `add` is an alias for `install`; `remove`, `rm`, and `del` are aliases for `uninstall`.\n",
			[]string{"templates", "roots", "sources", "query", "name", "path", "body", "active", "shadowed_by"},
			[]string{"ok", "error"},
			false,
		), true
	case "keybindings":
		return localCommandHelpSpec(
			"keybindings",
			"keybindings",
			"codog keybindings [show|path|init|open|edit|validate|resolve CONTEXT KEY] [--force] [--path PATH] [--output-format text|json]",
			"Keybindings\n\nUsage:\n  codog keybindings [show|path|init|open|edit|validate|resolve CONTEXT KEY] [--force] [--path PATH] [--output-format text|json]\n\nShows default shortcuts, creates or opens a keybindings config file, validates it, and resolves an effective action for a context/key pair after applying user overrides.\n",
			[]string{"editor_mode", "keybindings_path", "sections", "normalized_key", "binding_action"},
			[]string{"ok", "missing", "invalid", "created", "written", "opened", "created_opened", "open_failed"},
			true,
		), true
	case "voice", "listen":
		spec := localCommandHelpSpec(
			"voice",
			"voice",
			"codog voice [status|set-command|on|off|toggle|test|listen|transcribe|clear] [--input TEXT] [--output-format text|json]",
			"Voice\n\nUsage:\n  codog voice [status|set-command|on|off|toggle|test|listen|transcribe|clear] [--input TEXT] [--output-format text|json]\n  codog listen [--input TEXT]\n\nConfigures and runs an external speech-to-text command for voice input.\n",
			[]string{"enabled", "command_configured", "command_available", "transcript", "exit_code"},
			[]string{"ok", "error"},
			true,
		)
		if strings.EqualFold(strings.TrimSpace(topic), "listen") {
			spec.Topic = "listen"
			spec.Command = "listen"
			spec.Usage = "codog listen [--input TEXT] [--output-format text|json]"
		}
		return spec, true
	case "speak":
		return localCommandHelpSpec(
			"speak",
			"speak",
			"codog speak [TEXT|last|status|set-command|clear] [--session ID|--resume latest] [--nth N] [--input TEXT] [--output-format text|json]",
			"Speak\n\nUsage:\n  codog speak [TEXT|last|status|set-command|clear] [--session ID|--resume latest] [--nth N] [--input TEXT] [--output-format text|json]\n\nConfigures and runs an external text-to-speech command for explicit text or the latest assistant response.\n",
			[]string{"command_configured", "command_available", "session_id", "nth", "text_preview", "exit_code"},
			[]string{"ok", "error"},
			true,
		), true
	case "doctor":
		return commandHelpSpec{
			Topic:                   "doctor",
			Command:                 "doctor",
			Usage:                   "codog doctor [--output-format text|json]",
			Text:                    "Doctor\n\nUsage:\n  codog doctor [--output-format text|json]\n\nRuns local diagnostics for auth, config, memory, MCP, hooks, git, sandbox, and runtime state; no provider request or session resume required.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"checks", "has_failures", "summary"},
			StatusValues:            []string{"ok", "warn", "fail"},
			CheckNames:              []string{"Auth", "Base URL", "Config home", "Workspace", "Memory", "Model", "Permissions", "Tools", "MCP", "Sessions", "Hooks", "Git", "Sandbox", "Go toolchain", "Runtime"},
		}, true
	case "compact":
		return commandHelpSpec{
			Topic:                   "compact",
			Command:                 "compact",
			Usage:                   "codog compact [--session ID|--resume ID|latest] [--keep N] [--output-format text|json]",
			Text:                    "Compact\n\nUsage:\n  codog compact [--session ID|--resume ID|latest] [--keep N] [--output-format text|json]\n\nCompacts a saved session by keeping the most recent messages. Help is local and does not resume a session.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "path", "original_messages", "remaining_messages", "removed_messages"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "session":
		return commandHelpSpec{
			Topic:                   "session",
			Command:                 "session",
			Usage:                   "codog sessions [list|show|exists|search|audit|repair|export|import|fork|switch|rename|pin|unpin|prune|delete] [ARGS...]",
			Text:                    "Session\n\nUsage:\n  codog sessions [list|show|exists|search|audit|repair|export|import|fork|switch|rename|pin|unpin|prune|delete] [ARGS...]\n  codog sessions search QUERY [--limit N] [--output-format text|json]\n  codog sessions audit [--output-format text|json]\n  codog sessions repair [--output-format text|json]\n  codog sessions switch ID [--output-format text|json]\n  codog sessions pin ID [message-index|last] [--output-format text|json]\n  codog sessions unpin ID [message-index|last] [--output-format text|json]\n  codog sessions import PATH [--id ID|--name ID] [--force] [--output-format text|json]\n  codog sessions prune [--empty|--keep N] [--confirm] [--session ID|--resume ID] [--output-format text|json]\n\nInspects, audits, repairs, imports, exports, searches, and mutates saved session metadata. `audit` reports session hygiene, identity placeholders, lineage, pin drift, and JSONL bloat. `repair` appends enriched session identity records when saved prompt history can replace typed placeholders. `switch` is local and returns continue commands for the selected session instead of opening an interactive REPL. Message indexes for `pin` and `unpin` are entered as 1-based numbers.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"sessions", "session_details", "session_id", "messages", "query", "matches", "issues", "next_actions", "message_index", "snippet", "created_at", "updated_at", "pinned_messages"},
			StatusValues:            []string{"ok", "warn", "error"},
		}, true
	case "bookmarks":
		return commandHelpSpec{
			Topic:                   "bookmarks",
			Command:                 "bookmarks",
			Usage:                   "codog bookmarks [list|add|show|delete|clear] [NAME|ID] [--session ID|latest] [--message N|last] [--all] [--output-format text|json]",
			Text:                    "Bookmarks\n\nUsage:\n  codog bookmarks list [--all] [--output-format text|json]\n  codog bookmarks add NAME [--session ID|latest] [--message N|last] [--note TEXT] [--output-format text|json]\n  codog bookmarks show ID_OR_NAME [--output-format text|json]\n  codog bookmarks delete ID_OR_NAME [--output-format text|json]\n  codog bookmarks clear [--all] [--output-format text|json]\n  /bookmarks [list|add|show|delete|clear]\n\nStores local named pointers to workspace sessions. `add` defaults to the latest/current session and its last message; message indexes are entered as 1-based numbers. `--all` lists or clears bookmarks across all workspaces.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"bookmarks", "bookmark", "session_id", "message_index", "path", "resume_command"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "sessions":
		spec, _ := commandHelpSpecFor("session")
		spec.Topic = "sessions"
		spec.Command = "sessions"
		return spec, true
	case "generatesessionname", "generate-session-name":
		return commandHelpSpec{
			Topic:                   "generateSessionName",
			Command:                 "generateSessionName",
			Usage:                   "codog generateSessionName [--session ID|--resume ID|latest] [--source first|last] [--max-words N] [--prefix TEXT] [--rename] [--output-format text|json]",
			Text:                    "Generate Session Name\n\nUsage:\n  codog generateSessionName [--session ID|--resume ID|latest] [--source first|last] [--max-words N] [--prefix TEXT] [--rename] [--output-format text|json]\n  codog generate-session-name [same flags]\n\nGenerates a readable, collision-free session id from saved JSONL prompt history. With `--rename`, it applies the generated id through the session store.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   true,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "suggested_id", "source", "source_text", "collision_count", "renamed"},
			StatusValues:            []string{"ok", "renamed", "unchanged", "error"},
		}, true
	case "clear":
		return commandHelpSpec{
			Topic:                   "clear",
			Command:                 "clear",
			Usage:                   "codog clear [--confirm] [--output-format text|json]",
			Text:                    "Clear\n\nUsage:\n  codog clear [--confirm] [--output-format text|json]\n  codog --resume ID|latest /clear --confirm [--output-format text|json]\n\nCreates and reports a fresh empty local session id without deleting existing session JSONL files. In an interactive REPL, `/clear` switches the active conversation to a fresh session. With `--resume`, `/clear --confirm` clears that saved session after writing a backup.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "message_count", "path", "continue_commands"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "conversation":
		return commandHelpSpec{
			Topic:                   "conversation",
			Command:                 "conversation",
			Usage:                   "codog conversation [status|show|export|clear] [session-id] [--session ID] [--output-format text|json]",
			Text:                    "Conversation\n\nUsage:\n  codog conversation [status|show] [session-id] [--output-format text|json]\n  codog conversation export [PATH] [--session ID] [--format markdown|json|jsonl|html]\n  codog conversation clear [--confirm] [--output-format text|json]\n\nInspects, previews, exports, or starts a fresh local conversation. Without a subcommand, it reports the latest saved session instead of mutating session state. `conversation --confirm` remains compatible with the old clear alias behavior.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "message_count", "path", "pinned_messages", "continue_commands"},
			StatusValues:            []string{"ok", "error"},
		}, true
	case "resume":
		return commandHelpSpec{
			Topic:                   "resume",
			Command:                 "resume",
			Usage:                   "codog --resume|-r [ID|latest] [--fork-session] [--resume-session-at MESSAGE_ID] [prompt TEXT|repl|/slash-command] | codog --continue|-c [--fork-session] [prompt TEXT|repl|/slash-command]",
			Text:                    "Resume\n\nUsage:\n  codog --resume|-r [ID|latest] [--fork-session] [--resume-session-at MESSAGE_ID] [prompt TEXT|repl|/slash-command]\n  codog --continue|-c [--fork-session] [prompt TEXT|repl|/slash-command]\n\nSelects an existing session before running prompt, REPL, or a resume-safe slash command such as /status, /clear, /compact, /summary, /usage, /cache, /context, /history, /rewind, /export, /share, /copy, /paste, /bookmarks, or /session. Omit the session id in an interactive terminal to open the searchable resume picker. `-r` is an alias for `--resume`; `--continue` and `-c` resume the latest session. With `--fork-session`, Codog copies the resumed transcript into a new session before continuing; combine it with `--session-id` to choose the fork ID. With `--resume-session-at`, prompt mode resumes only through the assistant message with the requested message id. Help is local and does not open a session.\n",
			LocalOnly:               true,
			RequiresCredentials:     false,
			RequiresProviderRequest: false,
			RequiresSessionResume:   false,
			MutatesWorkspace:        false,
			OutputFields:            []string{"session_id", "message_count"},
			StatusValues:            []string{"ok", "error"},
		}, true
	default:
		if isBuiltInCommandName(topic) {
			return genericBuiltInCommandHelpSpec(topic), true
		}
		return commandHelpSpec{}, false
	}
}

func isBuiltInCommandName(topic string) bool {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return false
	}
	for _, command := range builtInCommandNames() {
		if strings.EqualFold(topic, command) {
			return true
		}
	}
	return false
}

func genericBuiltInCommandHelpSpec(topic string) commandHelpSpec {
	command := strings.TrimSpace(topic)
	usage := "codog " + command + " [ARGS...]"
	description := fmt.Sprintf("No detailed command metadata is registered for %s.", command)
	related := []string{"codog capabilities resolve " + command}
	schemaVersion := "codog.help.fallback.v1"
	if slashSpec, ok := slash.Lookup("/" + command); ok {
		usage = "codog " + strings.TrimPrefix(strings.TrimSpace(slashSpec.Usage), "/")
		description = strings.TrimSpace(slashSpec.Description)
		related = append([]string{slashSpec.Name}, related...)
		schemaVersion = "codog.help.generated.v1"
	}
	if commandAcceptsGlobalOutputFormat(command) {
		if !strings.Contains(usage, "--output-format") {
			usage += " [--output-format text|json]"
		}
	}
	title := titleCaseASCII(command)
	return commandHelpSpec{
		Topic:                   command,
		Command:                 command,
		Usage:                   usage,
		Text:                    fmt.Sprintf("%s\n\nUsage:\n  %s\n\n%s\n", title, usage, description),
		SchemaVersion:           schemaVersion,
		Formats:                 []string{"text", "json"},
		Related:                 related,
		OmitOperationalMetadata: true,
	}
}

func isHelpFlag(arg string) bool {
	return arg == "--help" || arg == "-h"
}

func boolPtr(value bool) *bool {
	return &value
}

func helpText(exe string) string {
	help := `%s is a Go-native interactive coding agent.

Usage:
  %s [options] [prompt]
  %s [options] -p|--print [prompt]
  %s [options] command [args...]

Arguments:
  prompt                              Start the TUI and submit an initial prompt.

Core commands:
  tui                                 Start the inline interactive session.
  repl                                Start the line-oriented interactive session.
  prompt                              Run one provider-backed turn and exit.
  sessions                            List, inspect, resume, fork, or repair sessions.
  mcp                                 Configure MCP servers, tools, resources, and prompts.
  plugins                             Inspect and manage local plugins.
  skills                              Inspect and invoke local skills.
  config                              Inspect or update layered configuration.
  doctor                              Diagnose local runtime and integration health.
  capabilities                        Inspect commands, tools, protocols, and feature support.
  completion                          Generate shell completion or list code completions.
  help all [query]                    List all commands, optionally filtered.
  version                             Print the Codog version.

Options:
  -p, --print                         Print a response and exit.
  --output-format text|json|stream-json
                                      Select prompt output; stream-json requires --print.
  --input-format text|stream-json     Select prompt input; requires --print.
  --include-partial-messages          Emit partial assistant events with stream-json.
  --replay-user-messages              Replay stream-json user messages to stdout.
  --json-schema schema                Validate structured prompt output.
  --model name                        Select the model for this session.
  --fallback-model name               Use a fallback model when the primary is overloaded.
  --thinking enabled|adaptive|disabled
                                      Select provider thinking behavior.
  --base-url url                      Use an Anthropic-compatible provider endpoint.
  --system-prompt text                Replace the base system prompt.
  --system-prompt-file path           Read the base system prompt from a file.
  --append-system-prompt text         Append text to the system prompt.
  --append-system-prompt-file path    Read appended system prompt text from a file.
  --permission-mode mode              read-only, workspace-write, danger-full-access,
                                      prompt, or allow.
  --dangerously-skip-permissions      Bypass permission checks.
  --allowed-tools rule                Allow a tool or tool rule; repeat or comma-separate.
  --disallowed-tools rule             Deny a tool or tool rule; repeat or comma-separate.
  --tools names                       Restrict tools exposed to the model.
  --add-dir path                      Add an allowed tool directory; repeatable.
  --attach path                       Attach a text, image, or PDF file.
  --mcp-config path|json              Merge MCP configuration; repeatable.
  --strict-mcp-config                 Ignore configured MCP servers not passed explicitly.
  --settings path|json                Load additional settings.
  --setting-sources sources           Load user, project, and/or local settings layers.
  --agents json                       Define session-only agents as a JSON object.
  --plugin-dir path                   Load a plugin directory for this session; repeatable.
  --ide                               Require and use the active local editor bridge.
  --config path                       Use a specific Codog config file.
  --cwd path, -C path                 Run from another working directory.
  --session-id id                     Select a session id.
  --name name                         Set the session display name.
  --resume [ID|latest], -r [ID|latest]
                                      Resume a saved session; omit ID to choose.
  --continue, -c                      Resume the latest session.
  --fork-session                      Fork the resumed session before continuing.
  --resume-session-at message-id      Resume through a specific assistant message.
  --prefill text                      Pre-fill interactive input without sending it.
  --max-turns n                       Limit model/tool loop iterations.
  --max-tokens n                      Limit output tokens.
  --max-budget-usd usd                Limit estimated spend in print mode.
  --temperature value                 Set sampling temperature from 0 to 1.
  --no-session-persistence            Do not save a print-mode session.
  --debug                             Print startup diagnostics to stderr.
  --verbose                           Enable verbose diagnostics.
  -h, --help                          Show help.
  -v, --version                       Print the version.

Examples:
  %s
  %s "inspect this repository and summarize its architecture"
  %s -p "explain the failing test"
  %s -p --output-format stream-json "run the tests and fix failures"
  %s --continue
  %s --resume
  %s --resume latest "continue the previous task"
  %s help all mcp
  %s help background

Deep links:
  %s <cc-url|cc+unix-url> [-p|--print [prompt]]

Environment:
  CODOG_CONFIG_HOME, CODOG_MODEL, CODOG_BASE_URL, CODOG_OUTPUT_FORMAT,
  CODOG_EXTRA_BODY, ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN,
  ANTHROPIC_BASE_URL, OPENAI_API_KEY, OPENAI_BASE_URL, OLLAMA_HOST

Run %s help command for command-specific help and %s help all for the full catalog.
`
	return strings.ReplaceAll(help, "%s", exe)
}
func redact(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "[redacted]"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
