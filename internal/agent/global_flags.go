package agent

import (
	"flag"
	"io"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/config"
)

type globalFlagParser struct {
	base               config.FlagOverrides
	flags              *flag.FlagSet
	printMode          bool
	compactPromptMode  bool
	continueMode       bool
	jsonOutput         bool
	outputFormat       string
	inputFormat        string
	jsonSchema         string
	deepLinkLastFetch  string
	allowedTools       stringListFlag
	disallowedTools    stringListFlag
	additionalDirs     stringListFlag
	mcpConfigs         appendStringFlag
	pluginDirs         appendStringFlag
	settingSources     []string
	toolNames          []string
	promptAttachments  stringListFlag
	settingSourcesFlag trackedStringListFlag
	toolSelection      toolSelectionFlag
}

func newGlobalFlagParser(base config.FlagOverrides) *globalFlagParser {
	p := &globalFlagParser{
		base:              base,
		flags:             flag.NewFlagSet("codog", flag.ContinueOnError),
		inputFormat:       strings.TrimSpace(base.InputFormat),
		jsonSchema:        strings.TrimSpace(base.JSONSchema),
		allowedTools:      stringListFlag(base.AllowedTools),
		disallowedTools:   stringListFlag(base.DisallowedTools),
		additionalDirs:    stringListFlag(base.AdditionalDirs),
		mcpConfigs:        appendStringFlag(base.MCPConfigs),
		pluginDirs:        appendStringFlag(base.PluginDirs),
		settingSources:    append([]string(nil), base.SettingSources...),
		toolNames:         append([]string(nil), base.ToolNames...),
		promptAttachments: stringListFlag{},
	}
	if base.DeepLinkLastFetchMS > 0 {
		p.deepLinkLastFetch = strconv.FormatInt(base.DeepLinkLastFetchMS, 10)
	}
	p.settingSourcesFlag = trackedStringListFlag{values: &p.settingSources, set: &p.base.SettingSourcesSet}
	p.toolSelection = toolSelectionFlag{values: &p.toolNames, set: &p.base.ToolNamesSet}
	p.flags.SetOutput(io.Discard)
	p.registerFlags()
	return p
}

func (p *globalFlagParser) registerFlags() {
	p.registerConfigAndModelFlags()
	p.registerSessionFlags()
	p.registerPromptFlags()
	p.registerToolFlags()
}

func (p *globalFlagParser) registerConfigAndModelFlags() {
	p.flags.StringVar(&p.base.ConfigPath, "config", p.base.ConfigPath, "config path")
	p.flags.StringVar(&p.base.Settings, "settings", p.base.Settings, "load additional settings from a JSON file or JSON object")
	p.flags.Var(p.settingSourcesFlag, "setting-sources", "configuration sources: user, project, local")
	p.flags.StringVar(&p.base.CWD, "cwd", p.base.CWD, "run as if Codog was started in this directory")
	p.flags.StringVar(&p.base.CWD, "C", p.base.CWD, "alias for --cwd")
	p.flags.StringVar(&p.base.CWD, "directory", p.base.CWD, "alias for --cwd")
	p.flags.StringVar(&p.base.Model, "model", p.base.Model, "model name")
	p.flags.StringVar(&p.base.FallbackModel, "fallback-model", p.base.FallbackModel, "fallback model when the primary model is overloaded")
	p.flags.StringVar(&p.base.Thinking, "thinking", p.base.Thinking, "thinking mode: enabled, adaptive, or disabled")
	p.flags.StringVar(&p.base.BaseURL, "base-url", p.base.BaseURL, "Anthropic-compatible base URL")
	p.flags.StringVar(&p.base.SystemPrompt, "system-prompt", p.base.SystemPrompt, "override the base system prompt")
	p.flags.StringVar(&p.base.SystemPromptFile, "system-prompt-file", p.base.SystemPromptFile, "read the base system prompt from a file")
	p.flags.StringVar(&p.base.AppendPrompt, "append-system-prompt", p.base.AppendPrompt, "append text to the system prompt")
	p.flags.StringVar(&p.base.AppendPromptFile, "append-system-prompt-file", p.base.AppendPromptFile, "read appended system prompt text from a file")
	p.flags.BoolVar(&p.base.Debug, "debug", p.base.Debug, "write startup diagnostics to stderr")
	p.flags.BoolVar(&p.base.Verbose, "verbose", p.base.Verbose, "enable verbose diagnostics")
	p.flags.BoolVar(&p.base.Verbose, "v", p.base.Verbose, "alias for --verbose")
	p.flags.StringVar(&p.base.DebugFile, "debug-file", p.base.DebugFile, "write startup diagnostics to a file")
}

func (p *globalFlagParser) registerSessionFlags() {
	p.flags.StringVar(&p.base.SessionID, "session", p.base.SessionID, "session id")
	p.flags.StringVar(&p.base.SessionID, "session-id", p.base.SessionID, "alias for --session")
	p.flags.StringVar(&p.base.SessionName, "name", p.base.SessionName, "display name for the current session")
	p.flags.StringVar(&p.base.Resume, "resume", p.base.Resume, "resume session id or latest")
	p.flags.StringVar(&p.base.Resume, "r", p.base.Resume, "alias for --resume")
	p.flags.StringVar(&p.base.FromPR, "from-pr", p.base.FromPR, "resume a session linked to a pull request")
	p.flags.StringVar(&p.base.ResumeSessionAt, "resume-session-at", p.base.ResumeSessionAt, "resume up to an assistant message id")
	p.flags.StringVar(&p.base.Prefill, "prefill", p.base.Prefill, "pre-fill the next interactive input")
	p.flags.StringVar(&p.base.Agents, "agents", p.base.Agents, "JSON object defining session agents")
	p.flags.Var(&p.pluginDirs, "plugin-dir", "load a plugin directory for this session; repeatable")
	p.flags.BoolVar(&p.base.IDE, "ide", p.base.IDE, "connect to the active local editor bridge")
	p.flags.BoolVar(&p.base.DeepLinkOrigin, "deep-link-origin", p.base.DeepLinkOrigin, "signal launch from a deep link")
	p.flags.StringVar(&p.base.DeepLinkRepo, "deep-link-repo", p.base.DeepLinkRepo, "repo slug resolved by a deep link")
	p.flags.StringVar(&p.deepLinkLastFetch, "deep-link-last-fetch", p.deepLinkLastFetch, "deep link fetch timestamp in epoch milliseconds")
	p.flags.BoolVar(&p.continueMode, "continue", false, "resume the latest session")
	p.flags.BoolVar(&p.continueMode, "c", false, "alias for --continue")
	p.flags.BoolVar(&p.base.ForkSession, "fork-session", p.base.ForkSession, "fork the resumed session before continuing")
}

func (p *globalFlagParser) registerPromptFlags() {
	p.flags.BoolVar(&p.printMode, "p", false, "run a one-shot prompt")
	p.flags.BoolVar(&p.printMode, "print", false, "run a one-shot prompt")
	p.flags.BoolVar(&p.compactPromptMode, "compact", false, "run a compact one-shot prompt")
	p.flags.BoolVar(&p.base.NoSessionPersistence, "no-session-persistence", p.base.NoSessionPersistence, "disable session persistence for prompt mode")
	p.flags.BoolVar(&p.base.ReplayUserMessages, "replay-user-messages", p.base.ReplayUserMessages, "replay stream-json user messages to prompt output")
	p.flags.BoolVar(&p.base.IncludePartialMessages, "include-partial-messages", p.base.IncludePartialMessages, "include assistant partial message chunks in stream-json prompt output")
	p.flags.BoolVar(&p.jsonOutput, "json", false, "alias for --output-format json for local commands")
	p.flags.StringVar(&p.outputFormat, "output-format", "", "text or json output for local commands")
	p.flags.StringVar(&p.outputFormat, "o", "", "text or json output for local commands")
	p.flags.StringVar(&p.inputFormat, "input-format", p.inputFormat, "text or stream-json input for prompt mode")
	p.flags.StringVar(&p.jsonSchema, "json-schema", p.jsonSchema, "JSON Schema for prompt structured output validation")
	p.flags.IntVar(&p.base.MaxTurns, "max-turns", p.base.MaxTurns, "max model/tool loop iterations")
	p.flags.IntVar(&p.base.MaxTokens, "max-tokens", p.base.MaxTokens, "maximum output tokens")
	p.flags.Var(optionalFloatFlag{value: &p.base.MaxBudgetUSD}, "max-budget-usd", "maximum estimated USD budget for prompt mode")
	p.flags.Var(optionalFloatFlag{value: &p.base.Temperature}, "temperature", "sampling temperature from 0 to 1")
}

func (p *globalFlagParser) registerToolFlags() {
	p.flags.StringVar(&p.base.PermissionMode, "permission-mode", p.base.PermissionMode, "read-only, workspace-write, danger-full-access, prompt, allow")
	p.flags.BoolVar(&p.base.PlanModeRequired, "plan-mode-required", p.base.PlanModeRequired, "require read-only plan mode before implementation")
	p.flags.BoolVar(&p.base.SkipPermissions, "dangerously-skip-permissions", p.base.SkipPermissions, "alias for --permission-mode allow")
	p.flags.BoolVar(&p.base.SkipPermissions, "skip-permissions", p.base.SkipPermissions, "alias for --permission-mode allow")
	p.flags.BoolVar(&p.base.AllowBroadCWD, "allow-broad-cwd", p.base.AllowBroadCWD, "allow model or agent commands from the home directory or filesystem root")
	p.flags.Var(&p.allowedTools, "allowed-tools", "allow a tool or tool rule; repeat or comma-separate")
	p.flags.Var(&p.allowedTools, "allowedTools", "allow a tool or tool rule; repeat or comma-separate")
	p.flags.Var(&p.disallowedTools, "disallowed-tools", "deny a tool; repeat or comma-separate")
	p.flags.Var(&p.disallowedTools, "disallowedTools", "deny a tool; repeat or comma-separate")
	p.flags.Var(&p.additionalDirs, "add-dir", "additional directory to allow tool access; repeat or comma-separate")
	p.flags.Var(&p.toolSelection, "tools", "restrict tools exposed to the model; use default for all or empty string for none")
	p.flags.Var(&p.mcpConfigs, "mcp-config", "load MCP servers from a JSON file or JSON object; repeat to merge")
	p.flags.BoolVar(&p.base.StrictMCPConfig, "strict-mcp-config", p.base.StrictMCPConfig, "only use MCP servers from --mcp-config")
	p.flags.Var(&p.promptAttachments, "attach", "attach a local text, image, or PDF file to a prompt; repeat or comma-separate")
	p.flags.Var(&p.promptAttachments, "attachment", "alias for --attach")
	p.flags.Var(&p.promptAttachments, "file", "alias for --attach")
}

func prepareGlobalFlagArgs(args []string) ([]string, error) {
	args = normalizeOptionalResumeFlag(args)
	args = normalizeOptionalFromPRFlag(args)
	args = normalizeVariadicAddDirFlag(args)
	args = normalizeVariadicPluginDirFlag(args)
	if missing, ok := missingToolFlagArgument(args); ok {
		return nil, missing
	}
	if err := rejectDuplicateScalarGlobalFlags(args); err != nil {
		return nil, err
	}
	return args, nil
}

func (p *globalFlagParser) parse(args []string) (config.FlagOverrides, string, []string, error) {
	if err := p.flags.Parse(args); err != nil {
		return p.base, "", nil, err
	}
	p.applyParsedValues()
	if err := p.validateParsedValues(); err != nil {
		return p.base, "", nil, err
	}
	return p.resolveCommand(p.flags.Args())
}

func (p *globalFlagParser) applyParsedValues() {
	p.base.OutputFormatSource, p.base.OutputFormatRaw, p.base.OutputFormatOverridden = globalOutputFormatProvenance(p.outputFormat, p.jsonOutput)
	p.base.InputFormat = strings.TrimSpace(p.inputFormat)
	p.base.JSONSchema = strings.TrimSpace(p.jsonSchema)
	p.base.AllowedTools = []string(p.allowedTools)
	p.base.DisallowedTools = []string(p.disallowedTools)
	p.base.AdditionalDirs = []string(p.additionalDirs)
	p.base.MCPConfigs = []string(p.mcpConfigs)
	p.base.PluginDirs = []string(p.pluginDirs)
	p.base.SettingSources = p.settingSources
	p.base.ToolNames = append([]string(nil), p.toolNames...)
	p.base.DeepLinkLastFetchMS = positiveInt64(p.deepLinkLastFetch)
	if p.continueMode && strings.TrimSpace(p.base.Resume) == "" {
		p.base.Resume = "latest"
	}
}

func positiveInt64(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func (p *globalFlagParser) validateParsedValues() error {
	if p.base.MaxBudgetUSD != nil && *p.base.MaxBudgetUSD <= 0 {
		return invalidFlagValueError{
			Flag:    "--max-budget-usd",
			Value:   strconv.FormatFloat(*p.base.MaxBudgetUSD, 'f', -1, 64),
			Message: "--max-budget-usd must be greater than 0",
			Usage:   "codog -p --max-budget-usd 1.50 \"<prompt>\"",
		}
	}
	return p.validateSessionFlags()
}

func (p *globalFlagParser) validateSessionFlags() error {
	resume := strings.TrimSpace(p.base.Resume)
	fromPR := strings.TrimSpace(p.base.FromPR)
	if fromPR != "" && resume != "" {
		return invalidFlagValueError{Flag: "--from-pr", Value: p.base.FromPR, Message: "--from-pr cannot be combined with --resume or --continue", Usage: "codog --from-pr OWNER/REPO#123 [repl|prompt TEXT]"}
	}
	if p.base.ForkSession && resume == "" && fromPR == "" {
		return invalidFlagValueError{Flag: "--fork-session", Message: "--fork-session requires --resume, --continue, or --from-pr", Usage: "codog --resume ID --fork-session [repl|prompt TEXT]"}
	}
	if (resume != "" || fromPR != "") && strings.TrimSpace(p.base.SessionID) != "" && !p.base.ForkSession {
		return invalidFlagValueError{Flag: "--session-id", Value: p.base.SessionID, Message: "--session-id can only be used with --resume, --continue, or --from-pr when --fork-session is also specified", Usage: "codog --resume ID --fork-session --session-id NEW_ID [repl|prompt TEXT]"}
	}
	if strings.TrimSpace(p.base.ResumeSessionAt) != "" && resume == "" && fromPR == "" {
		return invalidFlagValueError{Flag: "--resume-session-at", Value: p.base.ResumeSessionAt, Message: "--resume-session-at requires --resume, --continue, or --from-pr", Usage: "codog --resume ID --resume-session-at MESSAGE_ID prompt TEXT"}
	}
	return nil
}

func (p *globalFlagParser) resolveCommand(rest []string) (config.FlagOverrides, string, []string, error) {
	if err := p.validateCompactCommand(rest); err != nil {
		return p.base, "", nil, err
	}
	if p.printMode || p.compactPromptMode {
		return p.resolvePromptMode(rest)
	}
	if len(rest) == 0 {
		return p.resolveNoCommand()
	}
	return p.resolveNamedCommand(rest[0], rest[1:])
}

func (p *globalFlagParser) validateCompactCommand(rest []string) error {
	if p.compactPromptMode && len(rest) > 0 && !strings.EqualFold(rest[0], "prompt") && isKnownNonPromptCommand(rest[0]) {
		return invalidFlagValueError{Flag: "--compact", Value: rest[0], Message: "--compact is only supported with prompt mode", Usage: "codog --compact \"<prompt>\" or echo \"<prompt>\" | codog --compact"}
	}
	return nil
}

func (p *globalFlagParser) resolvePromptMode(rest []string) (config.FlagOverrides, string, []string, error) {
	if strings.TrimSpace(p.base.Prefill) != "" {
		return p.base, "", nil, invalidFlagValueError{Flag: "--prefill", Message: "--prefill is only supported with interactive REPL or TUI mode", Usage: "codog --prefill TEXT [repl|tui]"}
	}
	if strings.TrimSpace(p.base.ResumeSessionAt) != "" && len(rest) > 0 && rest[0] != "prompt" && isKnownNonPromptCommand(rest[0]) {
		return p.base, "", nil, invalidFlagValueError{Flag: "--resume-session-at", Value: rest[0], Message: "--resume-session-at is only supported with prompt mode", Usage: "codog --resume ID --resume-session-at MESSAGE_ID prompt TEXT"}
	}
	if len(rest) > 0 && rest[0] == "prompt" {
		rest = rest[1:]
	}
	format := resolveGlobalOutputFormat(p.outputFormat, p.jsonOutput)
	normalized, err := normalizeOutputFormat("prompt", format, []string{"text", "json", "stream-json"})
	if err != nil {
		return p.base, "", nil, err
	}
	rest = injectGlobalOutputFormat("prompt", rest, normalized)
	for _, attachment := range p.promptAttachments {
		rest = append(rest, "--attach", attachment)
	}
	return p.base, "prompt", p.appendPromptModeFlags(rest), nil
}

func (p *globalFlagParser) appendPromptModeFlags(rest []string) []string {
	if p.compactPromptMode {
		rest = append(rest, "--compact")
	}
	if p.base.InputFormat != "" {
		rest = append(rest, "--input-format", p.base.InputFormat)
	}
	if p.base.ReplayUserMessages {
		rest = append(rest, "--replay-user-messages")
	}
	if p.base.IncludePartialMessages {
		rest = append(rest, "--include-partial-messages")
	}
	if p.base.JSONSchema != "" {
		rest = append(rest, "--json-schema", p.base.JSONSchema)
	}
	return rest
}

func (p *globalFlagParser) resolveNoCommand() (config.FlagOverrides, string, []string, error) {
	if err := promptOnlyFlagError(p.base, ""); err != nil {
		return p.base, "", nil, err
	}
	return p.base, "", nil, nil
}

func promptOnlyFlagError(base config.FlagOverrides, command string) error {
	if base.ResumeSessionAt != "" {
		return invalidFlagValueError{Flag: "--resume-session-at", Value: command, Message: "--resume-session-at is only supported with prompt mode", Usage: "codog --resume ID --resume-session-at MESSAGE_ID prompt TEXT"}
	}
	if base.NoSessionPersistence {
		return invalidFlagValueError{Flag: "--no-session-persistence", Value: command, Message: "--no-session-persistence is only supported with prompt mode", Usage: "codog -p --no-session-persistence \"<prompt>\""}
	}
	if base.InputFormat != "" {
		value := command
		if command == "" {
			value = base.InputFormat
		}
		return invalidFlagValueError{Flag: "--input-format", Value: value, Message: "--input-format is only supported with prompt mode", Usage: "codog -p --input-format text|stream-json \"<prompt>\""}
	}
	if base.ReplayUserMessages {
		return invalidFlagValueError{Flag: "--replay-user-messages", Value: command, Message: "--replay-user-messages is only supported with prompt mode", Usage: "codog -p --input-format stream-json --output-format stream-json --replay-user-messages"}
	}
	if base.IncludePartialMessages {
		return invalidFlagValueError{Flag: "--include-partial-messages", Value: command, Message: "--include-partial-messages is only supported with prompt mode", Usage: "codog -p --output-format stream-json --include-partial-messages"}
	}
	if base.JSONSchema != "" {
		return invalidFlagValueError{Flag: "--json-schema", Value: command, Message: "--json-schema is only supported with prompt mode", Usage: "codog -p --json-schema '{\"type\":\"object\"}' --output-format json"}
	}
	if base.MaxBudgetUSD != nil {
		return invalidFlagValueError{Flag: "--max-budget-usd", Value: command, Message: "--max-budget-usd is only supported with prompt mode", Usage: "codog -p --max-budget-usd 1.50 \"<prompt>\""}
	}
	return nil
}

func (p *globalFlagParser) resolveNamedCommand(command string, rest []string) (config.FlagOverrides, string, []string, error) {
	var err error
	command, rest, err = p.resolveInitialPrompt(command, rest)
	if err != nil {
		return p.base, "", nil, err
	}
	if strings.TrimSpace(p.base.Prefill) != "" && !commandAcceptsPrefill(command) {
		return p.base, "", nil, invalidFlagValueError{Flag: "--prefill", Value: command, Message: "--prefill is only supported with interactive REPL or TUI mode", Usage: "codog --prefill TEXT [repl|tui]"}
	}
	if !strings.EqualFold(command, "prompt") {
		if err := promptOnlyFlagError(p.base, command); err != nil {
			return p.base, "", nil, err
		}
	}
	format, err := p.commandOutputFormat(command, rest)
	if err != nil {
		return p.base, "", nil, err
	}
	rest = injectGlobalOutputFormat(command, rest, format)
	return p.base, command, p.appendNamedPromptFlags(command, rest), nil
}

func (p *globalFlagParser) resolveInitialPrompt(command string, rest []string) (string, []string, error) {
	if !isInteractiveInitialPrompt(command, rest) {
		return command, rest, nil
	}
	if strings.TrimSpace(p.base.Prefill) != "" {
		return "", nil, invalidFlagValueError{Flag: "--prefill", Value: command, Message: "--prefill cannot be combined with an initial prompt", Usage: "codog --prefill TEXT or codog \"<prompt>\""}
	}
	if strings.TrimSpace(p.outputFormat) != "" || p.jsonOutput {
		return "", nil, invalidFlagValueError{Flag: "--output-format", Value: resolveGlobalOutputFormat(p.outputFormat, p.jsonOutput), Message: "--output-format requires --print for a positional prompt", Usage: "codog --print --output-format text|json|stream-json \"<prompt>\""}
	}
	p.base.InitialPrompt = strings.TrimSpace(strings.Join(append([]string{command}, rest...), " "))
	p.base.InitialAttachments = append([]string(nil), p.promptAttachments...)
	return "", nil, nil
}

func (p *globalFlagParser) commandOutputFormat(command string, rest []string) (string, error) {
	format := resolveGlobalOutputFormat(p.outputFormat, p.jsonOutput)
	p.base.OutputFormatSubcommandExplicit = argsHaveOutputFormat(rest)
	if format == "" || !commandAcceptsGlobalOutputFormat(command) || p.base.OutputFormatSubcommandExplicit {
		return format, nil
	}
	expected := []string{"text", "json"}
	if strings.EqualFold(command, "prompt") {
		expected = append(expected, "stream-json")
	}
	return normalizeOutputFormat(command, format, expected)
}

func (p *globalFlagParser) appendNamedPromptFlags(command string, rest []string) []string {
	if !strings.EqualFold(command, "prompt") {
		return rest
	}
	if p.base.InputFormat != "" && !argsHaveInputFormat(rest) {
		rest = append(rest, "--input-format", p.base.InputFormat)
	}
	if p.base.ReplayUserMessages && !argsHaveReplayUserMessages(rest) {
		rest = append(rest, "--replay-user-messages")
	}
	if p.base.IncludePartialMessages && !argsHaveIncludePartialMessages(rest) {
		rest = append(rest, "--include-partial-messages")
	}
	if p.base.JSONSchema != "" && !argsHaveJSONSchema(rest) {
		rest = append(rest, "--json-schema", p.base.JSONSchema)
	}
	return rest
}
