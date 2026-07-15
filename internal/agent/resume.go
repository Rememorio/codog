package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/prompthistory"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/slash"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/workspaceops"
)

func resumeSlashArgs(_ string, args []string, format string) []string {
	format = strings.TrimSpace(format)
	if format == "" || argsHaveOutputFormat(args) {
		return args
	}
	out := append([]string(nil), args...)
	out = append(out, "--output-format", format)
	return out
}

func resumeSlashJSONArgs(args []string, format string) []string {
	if !strings.EqualFold(strings.TrimSpace(format), "json") || argsHaveOutputFormat(args) {
		return args
	}
	out := append([]string(nil), args...)
	out = append(out, "--json")
	return out
}

func (a *App) runResumedSessionSlash(args []string, overrides config.FlagOverrides) error {
	if len(args) == 0 || strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		return a.ResumeCommand(append([]string{overrides.Resume}, args...))
	}
	switch normalizeSessionAction(args[0]) {
	case "exists":
		return a.SessionExists(args[1:], overrides.Resume)
	case "fork":
		return a.runResumedSessionFork(args[1:], overrides.Resume)
	case "switch":
		return a.runResumedSessionSwitch(args[1:], overrides.Resume)
	case "delete":
		return a.runResumedSessionDelete(args, overrides.Resume)
	case "prune":
		return a.runResumedSessionPrune(args[1:], overrides.Resume)
	case "pin", "unpin":
		return a.runResumedSessionPin(normalizeSessionAction(args[0]), args[1:], overrides.Resume)
	case "show", "rename":
		if len(args) == 1 || strings.HasPrefix(strings.TrimSpace(args[1]), "-") {
			withSession := append([]string{args[0], overrides.Resume}, args[1:]...)
			return a.SessionsCommand(withSession)
		}
		return a.SessionsCommand(args)
	case "list", "ls":
		return a.ListSessionsWithActive(args[1:], overrides.Resume)
	default:
		withSession := append([]string{args[0], overrides.Resume}, args[1:]...)
		return a.SessionsCommand(withSession)
	}
}

func (a *App) runResumedSessionFork(args []string, activeID string) error {
	req, err := parseSessionForkArgs("codog sessions fork", args, activeID, "text")
	if err != nil {
		return err
	}
	report, _, err := a.forkSessionWithReport(req.SourceID, req.BranchName)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONLine(a.Out, report)
	}
	renderSessionForkText(a.Out, report)
	return nil
}

func (a *App) runResumedSessionSwitch(args []string, activeID string) error {
	req, err := parseSessionSwitchArgs("codog sessions switch", args, "text")
	if err != nil {
		return err
	}
	report, err := a.switchSessionWithReport(activeID, req.ID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONLine(a.Out, report)
	}
	renderSessionSwitchText(a.Out, report)
	return nil
}

func (a *App) runResumedSessionDelete(args []string, activeID string) error {
	req, err := parseSessionDeleteArgs("codog sessions delete", args[1:])
	if err != nil {
		return err
	}
	active, err := a.Sessions.OpenExisting(activeID)
	if err != nil {
		return err
	}
	target, err := a.Sessions.OpenExisting(req.ID)
	if err != nil {
		return err
	}
	if target.ID == active.ID || target.Path == active.Path {
		return fmt.Errorf("delete: refusing to delete the active session %q", target.ID)
	}
	return a.SessionsCommand(args)
}

func (a *App) runResumedSessionPrune(args []string, activeID string) error {
	req, err := parseSessionPruneArgs("codog sessions prune", args, "text")
	if err != nil {
		return err
	}
	report, err := a.pruneSessionsWithReport(req, activeID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONLine(a.Out, report)
	}
	renderSessionPruneText(a.Out, report)
	return nil
}

func (a *App) runResumedSessionPin(action string, args []string, activeID string) error {
	req, err := parseSessionPinArgs("codog sessions "+action, args, activeID, "text")
	if err != nil {
		return err
	}
	return a.runPinRequest(action, req)
}

func renderJSONLine(out io.Writer, value any) error {
	data, _ := json.MarshalIndent(value, "", "  ")
	_, err := fmt.Fprintln(out, string(data))
	return err
}

func slashSwitchName(mapped string) string {
	mapped = strings.TrimSpace(mapped)
	if mapped == "" {
		return ""
	}
	return "/" + strings.ToLower(mapped)
}

func (a *App) runResumedBackgroundSlash(args []string, overrides config.FlagOverrides, format string) error {
	action := ""
	if meaningful := routeMeaningfulArgs(args); len(meaningful) > 0 {
		action = normalizeBackgroundAction(meaningful[0])
	}
	switch action {
	case "", "list", "run", "status", "logs", "board",
		"heartbeat", "stop", "restart", "prune", "supervise":
		return a.BackgroundWithFormat(args, overrides, format)
	case "watch":
		if err := validateResumedBackgroundWatch(args); err != nil {
			return err
		}
		return a.BackgroundWithFormat(args, overrides, format)
	default:
		command := "/tasks"
		if action != "" {
			command += " " + action
		}
		return renderUnsupportedResumedSlashCommand(a.Out, command, format)
	}
}

func validateResumedBackgroundWatch(args []string) error {
	cleanArgs, _, err := parseBackgroundOutputFormat(args, "text")
	if err != nil {
		return err
	}
	if len(cleanArgs) == 0 || normalizeBackgroundAction(cleanArgs[0]) != "watch" {
		return errors.New("usage: codog background watch ID [offset|--offset N] [--max-events N]")
	}
	_, _, maxEvents, err := parseBackgroundWatchArgs(cleanArgs[1:])
	if err != nil {
		return err
	}
	if maxEvents <= 0 {
		return errors.New("resumed background watch requires --max-events N to avoid blocking indefinitely")
	}
	return nil
}

func (a *App) runResumedSkillsSlash(command string, args []string, format string) error {
	action := "list"
	if meaningful := routeMeaningfulArgs(args); len(meaningful) > 0 && !strings.HasPrefix(strings.TrimSpace(meaningful[0]), "-") {
		action = normalizeSkillsAction(meaningful[0])
	}
	switch action {
	case "list", "show", "help", "sources", "invoke", "install", "uninstall", "enable", "disable", "status":
		return a.Skills(args)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel(command, action), format)
	}
}

func (a *App) runResumedDebugToolCallSlash(ctx context.Context, args []string, overrides config.FlagOverrides, format string) error {
	req, err := parseDebugToolCallArgs(args, overrides)
	if err != nil {
		return err
	}
	allowed, err := a.resumedDebugToolCallAllowed(ctx, req.Tool)
	if err != nil {
		return err
	}
	if !allowed {
		toolName := strings.TrimSpace(tools.CanonicalToolName(req.Tool))
		if toolName == "" {
			toolName = req.Tool
		}
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/debug-tool-call", toolName), format)
	}
	return a.DebugToolCall(ctx, args, overrides)
}

func (a *App) resumedDebugToolCallAllowed(ctx context.Context, name string) (bool, error) {
	if a.Tools == nil {
		return false, nil
	}
	if a.Tools.Has(name) {
		return true, nil
	}
	if a.mcpToolsAreLoaded() || len(a.Config.MCPServers) == 0 {
		return false, nil
	}
	if err := a.RegisterMCPTools(ctx); err != nil {
		return false, err
	}
	return a.Tools.Has(name), nil
}

func (a *App) runResumedHooksSlash(ctx context.Context, args []string, format string) error {
	req, err := parseHooksArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list", "health", "run", "watch-paths":
		return a.Hooks(ctx, args)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/hooks", req.Action), format)
	}
}

func (a *App) runResumedCronSlash(args []string, format string) error {
	req, err := parseCronArgsWithDefault(args, format)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list", "create", "delete", "due", "mark-run", "run-due":
		return a.CronWithFormat(args, format)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/cron", req.Action), format)
	}
}

func (a *App) runResumedTeamSlash(args []string, format string) error {
	req, err := parseTeamArgsWithDefault(args, format)
	if err != nil {
		return err
	}
	switch req.Action {
	case "list", "get", "status", "logs", "create", "delete":
		return a.TeamWithFormat(args, format)
	case "watch":
		if req.MaxEvents <= 0 {
			return errors.New("resumed team watch requires --max-events N to avoid blocking indefinitely")
		}
		return a.TeamWithFormat(args, format)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/team", req.Action), format)
	}
}

func (a *App) runResumedTerminalSetupSlash(args []string, format string) error {
	req, err := parseTerminalSetupArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status", "snippet", "print", "install", "uninstall", "remove":
		return a.TerminalSetup(args)
	}
	return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/terminal-setup", req.Action), format)
}

func (a *App) runResumedSetupSlash(ctx context.Context, args []string, format string) error {
	req, err := parseSetupArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status", "init", "all":
		return a.Setup(ctx, args)
	case "terminal":
		switch req.TerminalAction {
		case "status", "snippet", "print", "install", "uninstall", "remove":
			return a.Setup(ctx, args)
		default:
			return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/setup terminal", req.TerminalAction), format)
		}
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/setup", req.Action), format)
	}
}

func (a *App) runResumedRemoteEnvSlash(args []string, format string) error {
	return a.RemoteEnv(args)
}

func (a *App) runResumedMCPSlash(ctx context.Context, args []string, overrides config.FlagOverrides, format string) error {
	if meaningful := routeMeaningfulArgs(args); len(meaningful) > 0 && normalizeMCPAction(meaningful[0]) == "serve" {
		cleanArgs, _, err := stripJSONOnlyOutputFormat("mcp", args)
		if err != nil {
			return err
		}
		if len(cleanArgs) != 1 || normalizeMCPAction(cleanArgs[0]) != "serve" {
			return errors.New("usage: codog mcp serve")
		}
		return a.startResumedServerTask("mcp", []string{"serve"}, "mcp", "MCP server started", overrides)
	}
	return a.MCP(ctx, args)
}

func (a *App) runResumedRemoteSlash(args []string, overrides config.FlagOverrides, format string) error {
	if len(routeMeaningfulArgs(args)) > 0 && strings.EqualFold(strings.TrimSpace(routeMeaningfulArgs(args)[0]), "serve") {
		serveArgs, err := resumedRemoteServeArgs(args)
		if err != nil {
			return err
		}
		return a.startResumedServerTask("remote", serveArgs, "remote", "Remote control server started", overrides)
	}
	return a.runResumedRemoteSetupSlash(args, overrides, format)
}

func resumedRemoteServeArgs(args []string) ([]string, error) {
	const usage = "codog remote serve [ADDR] [--addr ADDR] [--json|--output-format text|json]"
	addr := ""
	actionSeen := false
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case strings.EqualFold(arg, "serve"):
			if actionSeen {
				return nil, unexpectedExtraArgsError{Command: "remote serve", Args: []string{arg}, Usage: usage}
			}
			actionSeen = true
		case arg == "--json":
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return nil, missingFlagValueError{Command: "remote serve", Flag: arg, Usage: usage}
			}
			if _, err := normalizeOutputFormat("remote serve", args[index], []string{"text", "json"}); err != nil {
				return nil, err
			}
		case strings.HasPrefix(arg, "--output-format="):
			if _, err := normalizeOutputFormat("remote serve", strings.TrimPrefix(arg, "--output-format="), []string{"text", "json"}); err != nil {
				return nil, err
			}
		case arg == "--addr":
			index++
			if missingFlagValueAt(args, index) {
				return nil, missingFlagValueError{Command: "remote serve", Flag: arg, Usage: usage}
			}
			if strings.TrimSpace(addr) != "" {
				return nil, unexpectedExtraArgsError{Command: "remote serve", Args: []string{args[index]}, Usage: usage}
			}
			addr = args[index]
		case strings.HasPrefix(arg, "--addr="):
			if strings.TrimSpace(addr) != "" {
				return nil, unexpectedExtraArgsError{Command: "remote serve", Args: []string{arg}, Usage: usage}
			}
			addr = strings.TrimPrefix(arg, "--addr=")
		case strings.HasPrefix(arg, "-"):
			return nil, unknownOptionError{Command: "remote serve", Option: arg, Usage: usage}
		default:
			if strings.TrimSpace(addr) != "" {
				return nil, unexpectedExtraArgsError{Command: "remote serve", Args: []string{arg}, Usage: usage}
			}
			addr = arg
		}
	}
	out := []string{"serve"}
	if strings.TrimSpace(addr) != "" {
		out = append(out, strings.TrimSpace(addr))
	}
	return out, nil
}

func (a *App) runResumedRemoteSetupSlash(args []string, overrides config.FlagOverrides, format string) error {
	return a.RemoteSetup(args, overrides)
}

func (a *App) runResumedAdvisorSlash(args []string, format string) error {
	return a.Advisor(args)
}

func (a *App) runResumedVoiceSlash(args []string, format string) error {
	return a.Voice(args)
}

func (a *App) runResumedSpeakSlash(ctx context.Context, args []string, overrides config.FlagOverrides, format string) error {
	return a.Speak(ctx, args, overrides)
}

func (a *App) runResumedSandboxToggleSlash(args []string, format string) error {
	return a.SandboxToggle(args)
}

func (a *App) runResumedResetSlash(args []string, format string) error {
	return a.Reset(args)
}

func (a *App) runResumedPlanSlash(args []string, format string) error {
	return a.Plan(args)
}

func (a *App) runResumedFormatSlash(args []string, format string) error {
	return a.Format(args)
}

func (a *App) runResumedCodeIntelSlash(ctx context.Context, args []string, format string) error {
	if len(args) == 0 || strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		return a.Symbols(resumeSlashArgs("symbols", args, format))
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch action {
	case "symbols":
		return a.Symbols(resumeSlashArgs("symbols", rest, format))
	case "diagnostics":
		return a.Diagnostics(ctx, resumeSlashArgs("diagnostics", rest, format))
	case "map":
		return a.Map(resumeSlashArgs("map", rest, format))
	case "references":
		return a.References(resumeSlashArgs("references", rest, format))
	case "definition":
		return a.Definition(resumeSlashArgs("definition", rest, format))
	case "hover":
		return a.Hover(resumeSlashArgs("hover", rest, format))
	case "teleport":
		return a.Teleport(resumeSlashArgs("teleport", rest, format))
	case "completion", "completions":
		return a.Completion(resumeSlashArgs("completion", rest, format))
	case "format", "formatting":
		return a.runResumedFormatSlash(resumeSlashArgs("format", rest, format), format)
	case "notebook", "notebook-read":
		return a.CodeIntel(resumeSlashArgs("notebook-read", append([]string{"notebook-read"}, rest...), format))
	case "notebook-edit":
		return a.CodeIntel(resumeSlashArgs("notebook-edit", append([]string{"notebook-edit"}, rest...), format))
	case "lsp":
		return a.runResumedCodeIntelLSPSlash(rest, format)
	default:
		return unknownCodeIntelActionError(args[0])
	}
}

func (a *App) runResumedCodeIntelLSPSlash(args []string, format string) error {
	if len(args) == 0 || strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		return a.CodeIntelLSP(args)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "list", "actions", "capabilities", "discover", "status", "query", "request", "start", "stop":
		return a.CodeIntelLSP(args)
	default:
		return unknownCodeIntelLSPActionError(args[0])
	}
}

func (a *App) runResumedPerfIssueSlash(args []string, format string) error {
	return a.PerfIssue(args)
}

func (a *App) runResumedThinkBackSlash(command string, args []string, format string) error {
	return a.ThinkBack(args)
}

func (a *App) runResumedIDESlash(args []string, format string) error {
	return a.IDE(args)
}

func (a *App) runResumedBridgeSlash(command string, args []string, overrides config.FlagOverrides, format string) error {
	if len(routeMeaningfulArgs(args)) > 0 && strings.EqualFold(strings.TrimSpace(routeMeaningfulArgs(args)[0]), "serve") {
		serveArgs, err := resumedBridgeServeArgs(args)
		if err != nil {
			return err
		}
		return a.startResumedServerTask("bridge", serveArgs, "bridge", "Bridge server started", overrides)
	}
	bridgeArgs := bridgeStatusArgs(args)
	req, err := parseIDEArgs(bridgeArgs)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status", "clear":
		return a.IDE(bridgeArgs)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel(command, req.Action), format)
	}
}

func resumedBridgeServeArgs(args []string) ([]string, error) {
	const usage = "codog bridge serve [--json|--output-format text|json]"
	actionSeen := false
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case strings.EqualFold(arg, "serve"):
			if actionSeen {
				return nil, unexpectedExtraArgsError{Command: "bridge serve", Args: []string{arg}, Usage: usage}
			}
			actionSeen = true
		case arg == "--json":
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return nil, missingFlagValueError{Command: "bridge serve", Flag: arg, Usage: usage}
			}
			if _, err := normalizeOutputFormat("bridge serve", args[index], []string{"text", "json"}); err != nil {
				return nil, err
			}
		case strings.HasPrefix(arg, "--output-format="):
			if _, err := normalizeOutputFormat("bridge serve", strings.TrimPrefix(arg, "--output-format="), []string{"text", "json"}); err != nil {
				return nil, err
			}
		case strings.HasPrefix(arg, "-"):
			return nil, unknownOptionError{Command: "bridge serve", Option: arg, Usage: usage}
		default:
			return nil, unexpectedExtraArgsError{Command: "bridge serve", Args: []string{arg}, Usage: usage}
		}
	}
	return []string{"serve"}, nil
}

func (a *App) runResumedACPSlash(ctx context.Context, args []string, overrides config.FlagOverrides, format string) error {
	if acpServeRequested(args) {
		return a.startResumedServerTask("acp", args, "acp", "ACP server started", overrides)
	}
	return a.ACP(ctx, args)
}

func (a *App) runResumedBridgeKickSlash(args []string, format string) error {
	return a.BridgeKick(args)
}

func (a *App) runResumedWorkspaceSlash(args []string, format string) error {
	return a.WorkspaceCommand(args)
}

func (a *App) runResumedFocusSlash(args []string, format string) error {
	_, paths, err := parseFocusArgs("focus", args)
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return a.Focus(args)
	}
	return a.Focus(args)
}

func (a *App) runResumedUnfocusSlash(args []string, format string) error {
	_, paths, err := parseFocusArgs("unfocus", args)
	if err != nil {
		return err
	}
	if len(paths) != 0 {
		return a.Unfocus(args)
	}
	report, err := focus.BuildReport(a.Workspace)
	if err != nil {
		return err
	}
	return a.renderFocusReport(format, report)
}

func (a *App) runResumedAddDirSlash(args []string, format string) error {
	return a.AddDir(args)
}

func (a *App) runResumedAntTraceSlash(ctx context.Context, args []string, format string) error {
	return a.AntTrace(ctx, args)
}

func (a *App) runResumedMockLimitsSlash(args []string, overrides config.FlagOverrides, format string) error {
	req, err := parseMockLimitsArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show":
		return a.MockLimits(args)
	case "serve":
		serveArgs := resumedMockLimitsServeArgs(req)
		return a.startResumedMockLimitsServer(serveArgs, overrides)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/mock-limits", req.Action), format)
	}
}

func resumedMockLimitsServeArgs(req mockLimitsRequest) []string {
	return []string{
		"serve",
		"--addr", req.Addr,
		"--failures", strconv.Itoa(req.Failures),
		"--retry-after-ms", strconv.Itoa(req.RetryAfterMS),
		"--text", req.Text,
	}
}

func (a *App) startResumedMockLimitsServer(args []string, overrides config.FlagOverrides) error {
	return a.startResumedServerTask("mock-limits", args, "mock_limits", "Mock limits server started", overrides)
}

func (a *App) startResumedServerTask(commandName string, args []string, kind string, message string, overrides config.FlagOverrides) error {
	executable, err := a.executablePath()
	if err != nil {
		return err
	}
	sessionID, err := a.sessionIDFromOverrides(overrides)
	if err != nil {
		return err
	}
	commandParts := []string{"CODOG_CONFIG_HOME=" + shellQuote(a.Config.ConfigHome), shellQuote(executable), commandName}
	commandParts = append(commandParts, shellQuoteArgs(args)...)
	command := strings.Join(commandParts, " ")
	task, err := background.NewStore(a.Config.ConfigHome).RunWithOptions(command, a.Workspace, background.RunOptions{
		Kind:        kind,
		SessionID:   sessionID,
		Description: commandName + " server",
	})
	if err != nil {
		return err
	}
	if err := renderBackgroundReport(a.Out, backgroundCommandReport{
		Kind:      "background",
		Action:    "run",
		Status:    "ok",
		Count:     1,
		SessionID: sessionID,
		TaskID:    task.ID,
		Task:      &task,
		Message:   message,
	}); err != nil {
		return err
	}
	a.runTaskCreatedHook(context.Background(), task)
	a.runNotificationHook(context.Background(), "background_task_started", message, fmt.Sprintf("Background task %s started: %s", task.ID, task.Command))
	return nil
}

func (a *App) runResumedExtraUsageSlash(args []string, format string) error {
	req, err := parseExtraUsageArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report := extraUsageReport{
		Kind:       "extra_usage",
		Action:     resumedStatusAction(req.Action),
		Status:     "ok",
		Mode:       req.Mode,
		URL:        extraUsageURL(req.Mode),
		Opened:     false,
		VisitCount: a.Config.Future.ExtraUsageVisitCount,
		Path:       path,
		Message:    extraUsageMessage(req.Mode),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderExtraUsageReport(a.Out, report)
	return nil
}

func (a *App) runResumedInstallSlackAppSlash(args []string, format string) error {
	req, err := parseInstallSlackAppArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report := installSlackAppReport{
		Kind:         "install_slack_app",
		Action:       resumedStatusAction(req.Action),
		Status:       "ok",
		URL:          slackAppURL,
		Opened:       false,
		InstallCount: a.Config.Future.SlackAppInstallCount,
		Path:         path,
		Message:      "Visit the Slack Marketplace URL to install the Claude app.",
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderInstallSlackAppReport(a.Out, report)
	return nil
}

func (a *App) runResumedStickersSlash(args []string, format string) error {
	req, err := parseStickersArgs(args)
	if err != nil {
		return err
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	report := stickersReport{
		Kind:       "stickers",
		Action:     resumedStatusAction(req.Action),
		Status:     "ok",
		URL:        stickerOrderURL,
		Opened:     false,
		OrderCount: a.Config.Future.StickerOrderCount,
		Path:       path,
		Message:    "Visit the sticker page to order Claude Code stickers.",
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderStickersReport(a.Out, report)
	return nil
}

func resumedStatusAction(action string) string {
	if strings.EqualFold(strings.TrimSpace(action), "status") {
		return "status"
	}
	return "show"
}

func (a *App) runResumedPassesSlash(args []string, format string) error {
	req, err := parsePassesArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show", "status":
	case "set-url", "clear-url":
		return a.Passes(args)
	case "open":
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/passes", req.Action), format)
	}
	path, err := a.preferenceConfigPath(req.Target, req.Path)
	if err != nil {
		return err
	}
	referralURL := firstNonEmpty(req.ReferralURL, a.Config.Future.GuestPassReferralURL)
	resolvedURL, urlSource := passesURLWithSource(referralURL, req.Docs)
	action := req.Action
	if action == "open" {
		action = "show"
	}
	report := passesReport{
		Kind:               "passes",
		Action:             action,
		Status:             "ok",
		URL:                resolvedURL,
		URLSource:          urlSource,
		DocsURL:            guestPassDocsURL,
		ReferralURL:        referralURL,
		ReferralConfigured: strings.TrimSpace(referralURL) != "",
		Opened:             false,
		VisitCount:         a.Config.Future.GuestPassVisitCount,
		Path:               path,
	}
	if req.Action == "status" {
		report.Message = "Guest pass status loaded."
	} else if report.ReferralURL == "" || req.Docs {
		report.Message = "No guest pass referral URL is configured. Showing Claude Code guest pass documentation."
	} else {
		report.Message = "Showing configured guest pass referral URL."
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPassesReport(a.Out, report)
	return nil
}

func (a *App) runResumedHeapDumpSlash(args []string, format string) error {
	return a.HeapDump(args)
}

func (a *App) runResumedBranchSlash(args []string, format string) error {
	return a.Branch(args)
}

func (a *App) runResumedTagSlash(args []string, resumed config.FlagOverrides, format string) error {
	return a.SessionTag(args, resumed)
}

func (a *App) runResumedStashSlash(args []string, format string) error {
	return a.Stash(args)
}

func (a *App) runResumedAgentsSlash(args []string, overrides config.FlagOverrides, format string) error {
	action := ""
	if meaningful := routeMeaningfulArgs(args); len(meaningful) > 0 {
		action = normalizeAgentsAction(meaningful[0])
	}
	switch action {
	case "", "list", "show", "create", "run", "worktrees", "worktree-remove", "runs", "board", "status", "heartbeat", "stop", "update", "output", "prune", "run-remove":
		return a.AgentsWithOverrides(args, overrides)
	default:
		command := "/agents"
		if action != "" {
			command += " " + action
		}
		return renderUnsupportedResumedSlashCommand(a.Out, command, format)
	}
}

func (a *App) runResumedMarketplaceSlash(args []string, format string) error {
	action := ""
	if meaningful := routeMeaningfulArgs(args); len(meaningful) > 0 {
		action = normalizeMarketplaceAction(meaningful[0])
	}
	switch action {
	case "", "list", "show", "info", "describe", "health", "healthcheck", "lifecycle", "validate", "sources", "source", "marketplaces", "manage-marketplaces", "add-marketplace", "remove-marketplace", "delete-marketplace", "settings", "remote", "browse", "discover", "updates", "install", "install-remote", "update", "enable", "disable", "remove", "uninstall":
		return a.Marketplace(args)
	default:
		command := "/plugins"
		if action != "" {
			command += " " + action
		}
		return renderUnsupportedResumedSlashCommand(a.Out, command, format)
	}
}

func (a *App) runResumedAPISlash(args []string, overrides config.FlagOverrides, format string) error {
	req, err := parseAPIArgs(args)
	if err != nil {
		return err
	}
	if req.Action != "serve" {
		return a.API(args)
	}
	return a.startResumedServerTask("api", []string{"serve", req.Addr}, "api", "Remote API server started", overrides)
}

func (a *App) runResumedAPIKeySlash(args []string, format string) error {
	return a.APIKey(args)
}

func (a *App) runResumedProvidersSlash(args []string, format string) error {
	return a.Providers(args)
}

func (a *App) runResumedOAuthSlash(args []string, format string) error {
	normalized, err := normalizeOAuthJSONArgs(args)
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return a.OAuth([]string{"status"})
	}
	label, supported := resumedOAuthRoute(normalized)
	if supported {
		return a.OAuth(normalized)
	}
	return renderUnsupportedResumedSlashCommand(a.Out, label, format)
}

func (a *App) runResumedProfileSlash(args []string, format string) error {
	return a.Profile(args)
}

func (a *App) runResumedLoginSlash(args []string, format string) error {
	return a.Login(args)
}

func (a *App) runResumedOAuthRefreshSlash(args []string, format string) error {
	return a.OAuthRefresh(args)
}

func (a *App) runResumedLogoutSlash(args []string, format string) error {
	return a.Logout(args)
}

func (a *App) runResumedBudgetSlash(args []string, format string) error {
	return a.Budget(args)
}

func (a *App) runResumedOutputStyleSlash(args []string, format string) error {
	return a.OutputStyle(args)
}

func (a *App) runResumedThemeSlash(args []string, format string) error {
	return a.Theme(args)
}

func (a *App) runResumedLanguageSlash(args []string, format string) error {
	return a.Language(args)
}

func (a *App) runResumedEffortSlash(args []string, command string, format string) error {
	if command == "reasoning" {
		return a.Reasoning(args)
	}
	return a.Effort(args)
}

func (a *App) runResumedFastSlash(args []string, format string) error {
	return a.Fast(args)
}

func (a *App) runResumedVimSlash(args []string, format string) error {
	resumeArgs := args
	if len(routeMeaningfulArgs(args)) == 0 {
		resumeArgs = append(append([]string(nil), args...), "status")
	}
	return a.Vim(resumeArgs)
}

func (a *App) runResumedChromeSlash(args []string, format string) error {
	return a.Chrome(args)
}

func (a *App) runResumedNotificationsSlash(args []string, format string) error {
	return a.Notifications(args)
}

func (a *App) runResumedPrivacySlash(args []string, format string) error {
	return a.PrivacySettings(args)
}

func (a *App) runResumedTelemetrySlash(args []string, format string) error {
	return a.Telemetry(args)
}

func (a *App) runResumedKeybindingsSlash(args []string, format string) error {
	req, err := parseKeybindingsArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show", "path", "init", "validate", "resolve":
		return a.Keybindings(args)
	default:
		return renderUnsupportedResumedSlashCommand(a.Out, resumedSlashCommandLabel("/keybindings", req.Action), format)
	}
}

func (a *App) runResumedTemperatureSlash(args []string, format string) error {
	return a.Temperature(args)
}

func (a *App) runResumedRateLimitSlash(args []string, format string) error {
	return a.RateLimit(args)
}

func (a *App) runResumedPermissionsSlash(args []string, format string) error {
	return a.Permissions(args)
}

func (a *App) runResumedAllowedToolsSlash(args []string, format string) error {
	return a.AllowedTools(args)
}

func resumedSlashCommandLabel(base string, action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return base
	}
	return base + " " + action
}

func slashCommandName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/exit_plan_mode":
		return "exit-plan"
	case "/session", "/sessions":
		return "sessions"
	case "/settings":
		return "config"
	case "/new":
		return "clear"
	case "/exit", "/quit":
		return "exit"
	case "/bug":
		return "feedback"
	case "/checkpoint":
		return "rewind"
	case "/rc", "/remote-control":
		return "bridge"
	case "/app":
		return "desktop"
	case "/terminalsetup":
		return "terminal-setup"
	case "/pr_comments":
		return "pr-comments"
	case "/web-setup":
		return "remote-setup"
	case "/color":
		return "theme"
	case "/caches":
		return "cache"
	case "/thinkback", "/thinkback-play":
		return "think-back"
	case "/parity":
		return "mock-parity"
	case "/branchlock":
		return "branch-lock"
	case "/base-check":
		return "stale-base"
	case "/green":
		return "green-contract"
	case "/g004":
		return "g004-conformance"
	case "/prompt-history":
		return "history"
	case "/continue":
		return "resume"
	case "/reviewremote", "/review-remote":
		return "reviewRemote"
	case "/cwd":
		return "workspace"
	case "/safer-scope":
		return "scope"
	}
	spec, ok := slash.Lookup(name)
	if !ok {
		return ""
	}
	return strings.TrimPrefix(spec.Name, "/")
}

func renderUnsupportedResumedSlashCommand(out io.Writer, command string, format string) error {
	report := buildSlashErrorReport(slashErrorReport{
		Kind:      "unsupported_resumed_slash_command",
		ErrorKind: "unsupported_resumed_slash_command",
		Status:    "error",
		Command:   command,
		Message:   fmt.Sprintf("%s cannot be run through --resume without starting an interactive session", command),
		Hint:      unsupportedResumedSlashHint(),
	})
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func unsupportedResumedSlashHint() string {
	names := slash.ResumeSupportedNames()
	if len(names) == 0 {
		return "Run `codog repl` and use the command there."
	}
	return "Run `codog repl` and use the command there, or use a resume-safe slash command such as " + joinReadable(names) + "."
}

func joinReadable(values []string) string {
	values = append([]string(nil), values...)
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " or " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
	}
}

func directSlashInteractiveOnly(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/approve", "/yes", "/y", "/deny", "/no", "/n", "/attach":
		return true
	default:
		return false
	}
}

func bareApprovalSlashName(command string) string {
	name := strings.ToLower(strings.TrimSpace(command))
	switch name {
	case "approve", "yes", "y", "deny", "no", "n":
		return "/" + name
	default:
		return ""
	}
}

func directSlashResumeSafe(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "/clear", "/new", "/compact", "/conversation", "/resume", "/continue":
		return true
	default:
		return false
	}
}

func renderUnknownSlashCommand(out io.Writer, command string, format string, extraSuggestions []string) error {
	report := unknownSlashCommandReport(command, extraSuggestions)
	err := errors.New(renderUnknownSlashCommandError(report))
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func unknownSlashCommandReport(command string, extraSuggestions []string) slashErrorReport {
	command = strings.TrimSpace(command)
	return buildSlashErrorReport(slashErrorReport{
		Kind:              "unknown_slash_command",
		ErrorKind:         "unknown_slash_command",
		Status:            "error",
		Command:           command,
		Message:           fmt.Sprintf("unknown slash command %q", command),
		Hint:              "Run `codog repl` and use `/help` to list interactive slash commands.",
		Suggestions:       slash.SuggestWithCandidates(command, 3, extraSuggestions),
		CompatibilityNote: unknownSlashCompatibilityNote(command),
	})
}

func unknownSlashCompatibilityNote(command string) string {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(command), "/"))
	if strings.HasPrefix(name, "oh-my-claudecode:") {
		return "Compatibility note: `/oh-my-claudecode:*` is a Claude Code/OMC plugin command. Codog loads compatible Markdown commands from project `.omc/commands` and compatible project hook settings such as `.omc/settings.json`."
	}
	return ""
}

func renderUnknownSlashCommandError(report slashErrorReport) string {
	lines := []string{fmt.Sprintf("%s: %s", report.ErrorKind, report.Message)}
	if len(report.Suggestions) > 0 {
		lines = append(lines, "Did you mean: "+strings.Join(report.Suggestions, ", "))
	}
	if strings.TrimSpace(report.CompatibilityNote) != "" {
		lines = append(lines, report.CompatibilityNote)
	}
	if strings.TrimSpace(report.Hint) != "" {
		lines = append(lines, report.Hint)
	}
	return strings.Join(lines, "\n")
}

func writeUnknownSlashCommand(out io.Writer, command string, extraSuggestions []string) {
	report := unknownSlashCommandReport(command, extraSuggestions)
	fmt.Fprintf(out, "unknown slash command: %s\n", report.Command)
	if len(report.Suggestions) > 0 {
		fmt.Fprintf(out, "Did you mean: %s\n", strings.Join(report.Suggestions, ", "))
	}
	if strings.TrimSpace(report.CompatibilityNote) != "" {
		fmt.Fprintln(out, report.CompatibilityNote)
	}
	if strings.TrimSpace(report.Hint) != "" {
		fmt.Fprintln(out, report.Hint)
	}
}

func renderInteractiveOnlySlash(out io.Writer, command string, format string) error {
	hint := "Run `codog repl` and use the command there."
	if directSlashResumeSafe(command) {
		hint = fmt.Sprintf("Run `codog --resume latest %s` to target a saved session, or run `codog repl` and use the command there.", command)
	}
	return renderInteractiveOnlyWithHint(out, command, fmt.Sprintf("%s is only available in an interactive REPL session", command), hint, format)
}

func renderInteractiveOnlyWithHint(out io.Writer, command string, message string, hint string, format string) error {
	report := buildSlashErrorReport(slashErrorReport{
		Kind:      "interactive_only",
		ErrorKind: "interactive_only",
		Status:    "error",
		Command:   command,
		Message:   message,
		Hint:      hint,
	})
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func buildSlashErrorReport(report slashErrorReport) slashErrorReport {
	if strings.TrimSpace(report.Status) == "" {
		report.Status = "error"
	}
	cliReport := cliErrorReport{
		Kind:      report.ErrorKind,
		ErrorKind: report.ErrorKind,
		Status:    report.Status,
		Command:   report.Command,
		Message:   report.Message,
		Hint:      report.Hint,
	}
	report.Error = buildCLIErrorEnvelope(errors.New(report.ErrorKind+": "+report.Message), cliReport)
	return report
}

func renderGlobalResumeArgumentGuard(out io.Writer, command string, args []string, overrides config.FlagOverrides, format string) (bool, error) {
	if strings.TrimSpace(overrides.Resume) == "" {
		return false, nil
	}
	name := strings.TrimSpace(command)
	if name == "" || strings.HasPrefix(name, "/") {
		return false, nil
	}
	switch strings.ToLower(name) {
	case "prompt", "repl":
		return false, nil
	default:
		err := invalidResumeArgumentError{
			Command: name,
			Args:    routeMeaningfulArgs(args),
			Resume:  overrides.Resume,
		}
		return true, renderCLIError(out, err, format)
	}
}

func renderLocalRouteGuard(out io.Writer, command string, args []string, format string) (bool, error) {
	meaningful := routeMeaningfulArgs(args)
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "model" && len(meaningful) > 1 {
		positionals := modelRoutePositionals(meaningful)
		if len(positionals) > 1 {
			err := unexpectedExtraArgsError{Command: "model", Args: positionals[1:], Usage: modelUsage}
			return true, renderCLIError(out, err, format)
		}
	}
	interactive := false
	slashName := "/" + lower
	hint := fmt.Sprintf("Run `codog repl` and use `%s` there.", slashName)
	switch lower {
	case "session":
		interactive = len(meaningful) > 0 && !isSessionAction(meaningful[0])
	case "clear":
		interactive = len(meaningful) > 0 && !clearArgsAreLocal(meaningful)
	case "fork":
		interactive = len(meaningful) > 0
	case "cost", "usage", "stats":
		interactive = len(meaningful) > 0
	case "memory":
		interactive = len(meaningful) > 0 && strings.EqualFold(meaningful[0], "reset")
	}
	if !interactive {
		return false, nil
	}
	invocation := strings.TrimSpace(strings.Join(append([]string{command}, meaningful...), " "))
	if invocation == "" {
		invocation = command
	}
	message := fmt.Sprintf("%s is only available in an interactive REPL session", invocation)
	return true, renderInteractiveOnlyWithHint(out, invocation, message, hint, format)
}

func modelRoutePositionals(args []string) []string {
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
		case arg == "--target" || arg == "--path" || arg == "--output-format" || arg == "-o":
			index++
		case strings.HasPrefix(arg, "--target="),
			strings.HasPrefix(arg, "--path="),
			strings.HasPrefix(arg, "--output-format="):
		default:
			if !strings.HasPrefix(arg, "-") {
				positionals = append(positionals, arg)
			}
		}
	}
	return positionals
}

func isSessionAction(action string) bool {
	switch normalizeSessionAction(action) {
	case "", "list", "show", "exists", "export", "import", "fork", "switch", "rename", "prune", "delete":
		return true
	default:
		return false
	}
}

func clearArgsAreLocal(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, arg := range args {
		if !strings.EqualFold(strings.TrimSpace(arg), "--confirm") {
			return false
		}
	}
	return true
}

func routeMeaningfulArgs(args []string) []string {
	out := []string{}
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
			out = append(out, arg)
		}
	}
	return out
}

func renderCommandNotFound(out io.Writer, command string, args []string, format string) error {
	report := buildCommandNotFoundReport(command, args)
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func buildCommandNotFoundReport(command string, args []string) commandNotFoundReport {
	command = strings.TrimSpace(command)
	cleanArgs := append([]string(nil), args...)
	message := fmt.Sprintf("unknown command %q", command)
	suggestions := commandSuggestions(command, 4)
	hint := "Run `codog --help` to list commands."
	if len(cleanArgs) > 0 {
		prompt := strings.TrimSpace(strings.Join(append([]string{command}, cleanArgs...), " "))
		hint = fmt.Sprintf("Use `codog prompt %q` to send this as a prompt, or run `codog --help` to list commands.", prompt)
		if len(suggestions) > 0 {
			hint = fmt.Sprintf("Did you mean: %s? %s", strings.Join(suggestions, ", "), hint)
		}
	} else if len(suggestions) > 0 {
		hint = fmt.Sprintf("Did you mean: %s? Run `codog --help` to list commands.", strings.Join(suggestions, ", "))
	}
	report := commandNotFoundReport{
		Kind:      "command_not_found",
		ErrorKind: "command_not_found",
		Status:    "error",
		Command:   command,
		Args:      cleanArgs,
		Message:   message,
		Hint:      hint,
	}
	report.Error = buildCommandNotFoundErrorEnvelope(report)
	return report
}

func buildCommandNotFoundErrorEnvelope(report commandNotFoundReport) cliErrorEnvelope {
	cliReport := cliErrorReport{
		Kind:      report.ErrorKind,
		ErrorKind: report.ErrorKind,
		Status:    report.Status,
		Command:   report.Command,
		Args:      append([]string(nil), report.Args...),
		Message:   report.Message,
		Hint:      report.Hint,
	}
	return buildCLIErrorEnvelope(errors.New(report.ErrorKind+": "+report.Message), cliReport)
}

func requestedOutputFormat(args []string) string {
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "--json":
			return "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index < len(args) {
				return strings.ToLower(strings.TrimSpace(args[index]))
			}
			return ""
		case strings.HasPrefix(arg, "--output-format="):
			return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, "--output-format=")))
		}
	}
	return strings.ToLower(strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT")))
}

func commandSuggestions(command string, limit int) []string {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || limit <= 0 {
		return nil
	}
	type candidate struct {
		name  string
		score int
	}
	candidates := []candidate{}
	for _, name := range builtInCommandNames() {
		lower := strings.ToLower(name)
		score := levenshteinDistance(command, lower)
		if strings.HasPrefix(lower, command) || strings.HasPrefix(command, lower) {
			score--
		}
		if score <= 3 || strings.Contains(lower, command) {
			candidates = append(candidates, candidate{name: name, score: score})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].score < candidates[j].score
	})
	out := make([]string, 0, limit)
	for _, candidate := range candidates {
		if candidate.name == command {
			continue
		}
		out = append(out, candidate.name)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len([]rune(b))
	}
	if b == "" {
		return len([]rune(a))
	}
	ar := []rune(a)
	br := []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		curr[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			curr[j+1] = min(curr[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func parseSimpleOutputFormat(command string, args []string) (string, error) {
	format := "text"
	usage := fmt.Sprintf("codog %s [--json|--output-format text|json]", command)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return "", missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			return "", unknownOptionError{Command: command, Option: arg, Usage: usage}
		}
	}
	switch format {
	case "text", "json":
		return format, nil
	default:
		return "", outputFormatError{Command: command, Value: format, Expected: []string{"text", "json"}}
	}
}

func stripJSONOnlyOutputFormat(command string, args []string) ([]string, string, error) {
	format := "text"
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return nil, "", fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			remaining = append(remaining, arg)
		}
	}
	if err := validateTextOrJSON(format, command); err != nil {
		return nil, "", err
	}
	return remaining, format, nil
}

func parsePlanArgs(args []string) (planRequest, error) {
	const usage = "codog plan [show|enter|set TEXT|exit|clear|open|edit] [--json|--output-format text|json]"
	req := planRequest{Action: "show", Format: "text"}
	textParts := []string{}
	actionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "plan", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "plan", Option: arg, Usage: usage}
		case !actionSet && len(textParts) == 0 && isPlanAction(arg):
			req.Action = normalizePlanAction(arg)
			actionSet = true
		default:
			textParts = append(textParts, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("plan", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	req.Text = strings.TrimSpace(strings.Join(textParts, " "))
	if req.Text != "" && req.Action == "show" {
		req.Action = "enter"
	}
	if (req.Action == "set") && req.Text == "" {
		return req, requiredArgumentError{Command: "plan set", Argument: "TEXT", Usage: usage}
	}
	return req, nil
}

func isPlanAction(value string) bool {
	switch normalizePlanAction(value) {
	case "show", "enter", "set", "exit", "clear", "open":
		return true
	default:
		return false
	}
}

func normalizePlanAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "show", "status", "list":
		return "show"
	case "enter", "start", "on":
		return "enter"
	case "set", "update":
		return "set"
	case "exit", "stop", "off", "done", "accept":
		return "exit"
	case "clear", "reset", "delete":
		return "clear"
	case "open", "edit":
		return "open"
	default:
		return value
	}
}

func parseHistoryArgs(args []string, overrides config.FlagOverrides) (historyRequest, error) {
	parser := historyArgParser{req: historyRequest{Format: "text", Limit: prompthistory.DefaultLimit}}
	if overrides.Resume != "" {
		parser.req.SessionID = overrides.Resume
		if parser.req.SessionID == "true" {
			parser.req.SessionID = "latest"
		}
	}
	if parser.req.SessionID == "" {
		parser.req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			parser.req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, unknownOptionError{Command: "history", Option: arg, Usage: historyUsage}
		}
		if err := parser.consumePositional(arg); err != nil {
			return parser.req, err
		}
	}
	normalizedFormat, err := normalizeOutputFormat("history", parser.req.Format, []string{"text", "json"})
	if err != nil {
		return parser.req, err
	}
	parser.req.Format = normalizedFormat
	if strings.TrimSpace(parser.req.SessionID) == "" {
		parser.req.SessionID = "latest"
	}
	return parser.req, nil
}

const historyUsage = "codog history [SESSION|LIMIT] [--session ID] [--limit N] [--offset N] [--json|--output-format text|json]"

type historyArgParser struct {
	req historyRequest
}

func (p *historyArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format),
		"-o":              p.stringOption(&p.req.Format),
		"--limit":         p.limitOption(),
		"-n":              p.limitOption(),
		"--offset":        p.offsetOption(),
		"--session":       p.stringOption(&p.req.SessionID),
	}
}

func (p *historyArgParser) stringOption(target *string) valueOption {
	return valueOption{missing: historyMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		*target = value
		return nil
	}}
}

func (p *historyArgParser) limitOption() valueOption {
	return valueOption{missing: historyMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		limit, err := parsePositiveIntOption(value, "--limit", historyUsage)
		if err != nil {
			return err
		}
		p.req.Limit = limit
		return nil
	}}
}

func (p *historyArgParser) offsetOption() valueOption {
	return valueOption{missing: historyMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		offset, err := parseNonNegativeIntOption(value, "--offset", historyUsage)
		if err != nil {
			return err
		}
		p.req.Offset, p.req.UseOffset = offset, true
		return nil
	}}
}

func historyMissingValue(flag string) error {
	return missingFlagValueError{Command: "history", Flag: flag, Usage: historyUsage}
}

func (p *historyArgParser) consumePositional(arg string) error {
	limit, err := strconv.Atoi(arg)
	if err == nil {
		if limit <= 0 {
			return invalidFlagValueError{Flag: "LIMIT", Value: arg, Message: "history limit must be positive", Usage: historyUsage}
		}
		p.req.Limit = limit
		return nil
	}
	if p.req.SessionID == "" || session.IsSessionReferenceAlias(p.req.SessionID) {
		p.req.SessionID = arg
		return nil
	}
	return unexpectedExtraArgsError{Command: "history", Args: []string{arg}, Usage: historyUsage}
}

func parseSummaryArgs(args []string, overrides config.FlagOverrides) (summaryRequest, error) {
	const usage = "codog summary [SESSION] [--session ID|--resume ID] [--json|--output-format text|json]"
	req := summaryRequest{Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
		if req.SessionID == "true" {
			req.SessionID = "latest"
		}
	}
	if req.SessionID == "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "summary", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "summary", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "summary", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "summary", Option: arg, Usage: usage}
		default:
			if req.SessionID == "" || session.IsSessionReferenceAlias(req.SessionID) {
				req.SessionID = arg
				continue
			}
			return req, unexpectedExtraArgsError{Command: "summary", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("summary", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func parseRewindArgs(args []string, overrides config.FlagOverrides, defaultSession string) (rewindRequest, error) {
	parser := rewindArgParser{req: rewindRequest{Format: "text", Messages: 2, SessionID: defaultSession}}
	if overrides.Resume != "" {
		parser.req.SessionID = overrides.Resume
		if parser.req.SessionID == "true" {
			parser.req.SessionID = "latest"
		}
	}
	if parser.req.SessionID == "" {
		parser.req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			parser.req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, unknownOptionError{Command: "rewind", Option: arg, Usage: rewindUsage}
		}
		if err := parser.consumePositional(arg); err != nil {
			return parser.req, err
		}
	}
	normalizedFormat, err := normalizeOutputFormat("rewind", parser.req.Format, []string{"text", "json"})
	if err != nil {
		return parser.req, err
	}
	parser.req.Format = normalizedFormat
	return parser.req, nil
}

const rewindUsage = "codog rewind [SESSION|COUNT] [--session ID|--resume ID] [--messages N] [--json|--output-format text|json]"

type rewindArgParser struct {
	req rewindRequest
}

func (p *rewindArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format": p.stringOption(&p.req.Format),
		"-o":              p.stringOption(&p.req.Format),
		"--session":       p.stringOption(&p.req.SessionID),
		"--resume":        p.stringOption(&p.req.SessionID),
		"--messages":      p.messagesOption(),
		"-n":              p.messagesOption(),
	}
}

func (p *rewindArgParser) stringOption(target *string) valueOption {
	return valueOption{missing: rewindMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		*target = value
		return nil
	}}
}

func (p *rewindArgParser) messagesOption() valueOption {
	return valueOption{missing: rewindMissingValue, rejectEmptySeparate: true, rejectOutputFormat: true, set: func(value string) error {
		count, err := parsePositiveIntOption(value, "--messages", rewindUsage)
		if err != nil {
			return err
		}
		p.req.Messages = count
		return nil
	}}
}

func rewindMissingValue(flag string) error {
	return missingFlagValueError{Command: "rewind", Flag: flag, Usage: rewindUsage}
}

func (p *rewindArgParser) consumePositional(arg string) error {
	count, err := strconv.Atoi(arg)
	if err == nil {
		if count <= 0 {
			return invalidFlagValueError{Flag: "COUNT", Value: arg, Message: "rewind message count must be positive", Usage: rewindUsage}
		}
		p.req.Messages = count
		return nil
	}
	if p.req.SessionID == "" || session.IsSessionReferenceAlias(p.req.SessionID) {
		p.req.SessionID = arg
		return nil
	}
	return unexpectedExtraArgsError{Command: "rewind", Args: []string{arg}, Usage: rewindUsage}
}

func parseFilesArgs(args []string) (filesRequest, error) {
	const usage = "codog files [PATH] [--path PATH] [--glob GLOB] [--limit N] [--hidden] [--json|--output-format text|json]"
	req := filesRequest{Format: "text", Limit: 200}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "files", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--path":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "files", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--glob":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "files", Flag: arg, Usage: usage}
			}
			req.Glob = args[index]
		case strings.HasPrefix(arg, "--glob="):
			req.Glob = strings.TrimPrefix(arg, "--glob=")
		case arg == "--limit":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "files", Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(args[index], "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case arg == "--hidden" || arg == "--include-hidden":
			req.IncludeHidden = true
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "files", Option: arg, Usage: usage}
		default:
			if req.Path == "" {
				req.Path = arg
				continue
			}
			return req, unexpectedExtraArgsError{Command: "files", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("files", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func parseSearchArgs(args []string) (searchRequest, error) {
	const usage = "codog search PATTERN [--path PATH] [--glob GLOB] [--ignore-case] [--limit N] [--json|--output-format text|json]"
	req := searchRequest{Format: "text", Limit: 100}
	queryParts := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "search", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--path":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "search", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--glob":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "search", Flag: arg, Usage: usage}
			}
			req.Glob = args[index]
		case strings.HasPrefix(arg, "--glob="):
			req.Glob = strings.TrimPrefix(arg, "--glob=")
		case arg == "--ignore-case" || arg == "-i":
			req.IgnoreCase = true
		case arg == "--limit" || arg == "-n":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "search", Flag: arg, Usage: usage}
			}
			limit, err := parsePositiveIntOption(args[index], "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return req, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "search", Option: arg, Usage: usage}
		default:
			queryParts = append(queryParts, arg)
		}
	}
	req.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	if req.Query == "" {
		return req, requiredArgumentError{Command: "search", Argument: "PATTERN", Usage: usage}
	}
	normalizedFormat, err := normalizeOutputFormat("search", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) Doctor(args []string) error {
	format, err := parseSimpleOutputFormat("doctor", args)
	if err != nil {
		return err
	}
	toolCount := 0
	toolNames := []string{}
	toolPermissions := []doctor.ToolPermission{}
	if a.Tools != nil {
		infos := a.Tools.Infos()
		toolCount = len(infos)
		for _, info := range infos {
			toolNames = append(toolNames, info.Name)
			toolPermissions = append(toolPermissions, doctor.ToolPermission{
				Name:               info.Name,
				RequiredPermission: string(info.Permission),
			})
		}
	}
	sessionCount := -1
	var sessionHygiene *doctor.SessionHygiene
	if a.Sessions != nil {
		report, err := a.auditSessionsWithReport()
		if err == nil {
			sessionCount = report.SessionCount
			sessionHygiene = doctorSessionHygiene(report)
		}
	}
	memoryStatuses := buildMemoryFileStatuses(a.Workspace, a.memoryRulesImportOptions())
	mcpValidation := buildMCPValidation(a.Config.MCPServers)
	hookValidation := buildHookValidation(a.Config.Hooks)
	mcpStatuses := mcp.PreflightAll(context.Background(), a.Config.MCPServers)
	sandboxStatus := sandbox.Detect()
	sandboxStrategy, sandboxOptions, err := sandboxReportRequestOptions(a.Config.Future)
	if err != nil {
		return err
	}
	sandboxRuntime, _, _ := sandbox.ResolveSandboxExecutionStatusFor(sandboxStrategy, a.Workspace, sandboxOptions, sandboxStatus)
	var gitOperation *gitops.Operation
	if operation, err := gitops.InspectOperation(a.Workspace); err == nil {
		gitOperation = operation
	}
	report := doctor.Run(doctor.Options{
		Workspace:             a.Workspace,
		ConfigHome:            a.Config.ConfigHome,
		Model:                 a.Config.Model,
		RuntimeProvider:       a.Config.RuntimeProvider,
		RuntimeProviderSource: a.Config.RuntimeProviderSource,
		BaseURL:               a.Config.BaseURL,
		APIKey:                a.Config.APIKey,
		AuthToken:             a.Config.AuthToken,
		OAuthProfile:          a.Config.OAuthProfile,
		PermissionMode:        a.Config.PermissionMode,
		PermissionModeRaw:     a.Config.PermissionModeRaw,
		PermissionModeSource:  a.Config.PermissionModeSource,
		PermissionModeEnvVar:  a.Config.PermissionModeEnvVar,
		PermissionRules:       localstatus.BuildPermissionRulesStatus(a.Config.PermissionRules, toolNames, tools.ClaudeToolAliases()),
		ConfigLoadError:       a.ConfigLoadError,
		ConfigLoadErrorKind:   a.ConfigLoadErrorKind,
		ToolCount:             toolCount,
		ToolPermissions:       toolPermissions,
		MCPServerStatuses:     mcpStatuses,
		MCPValidation:         mcpValidation,
		HookValidation:        hookValidation,
		SessionCount:          sessionCount,
		SessionHygiene:        sessionHygiene,
		MemoryFiles:           memoryStatuses,
		UserPromptSubmit:      a.Config.Hooks.UserPromptSubmit,
		SessionStart:          a.Config.Hooks.SessionStart,
		PreToolUse:            a.Config.Hooks.PreToolUse,
		PostToolUse:           a.Config.Hooks.PostToolUse,
		PostToolUseFailure:    a.Config.Hooks.PostToolUseFailure,
		PermissionRequest:     a.Config.Hooks.PermissionRequest,
		PermissionDenied:      a.Config.Hooks.PermissionDenied,
		SessionEnd:            a.Config.Hooks.SessionEnd,
		Setup:                 a.Config.Hooks.Setup,
		Stop:                  a.Config.Hooks.Stop,
		StopFailure:           a.Config.Hooks.StopFailure,
		PreCompact:            a.Config.Hooks.PreCompact,
		PostCompact:           a.Config.Hooks.PostCompact,
		Notification:          a.Config.Hooks.Notification,
		SubagentStart:         a.Config.Hooks.SubagentStart,
		SubagentStop:          a.Config.Hooks.SubagentStop,
		WorktreeCreate:        a.Config.Hooks.WorktreeCreate,
		WorktreeRemove:        a.Config.Hooks.WorktreeRemove,
		CwdChanged:            a.Config.Hooks.CwdChanged,
		TaskCreated:           a.Config.Hooks.TaskCreated,
		TaskCompleted:         a.Config.Hooks.TaskCompleted,
		InstructionsLoaded:    a.Config.Hooks.InstructionsLoaded,
		FileChanged:           a.Config.Hooks.FileChanged,
		SandboxDefault:        sandboxStatus.Default,
		SandboxOK:             sandboxStatus.Available,
		SandboxStrategies:     sandboxStatus.Strategies,
		SandboxFallback:       sandboxStatus.FallbackReason,
		SandboxInContainer:    sandboxStatus.Container.InContainer,
		SandboxRuntime:        &sandboxRuntime,
		GitOperation:          gitOperation,
	})
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
	} else {
		doctor.RenderText(a.Out, report)
	}
	if report.HasFailures {
		return errors.New("doctor found failing checks")
	}
	return nil
}

type symbolsReport struct {
	Kind    string             `json:"kind"`
	Total   int                `json:"total"`
	Symbols []codeintel.Symbol `json:"symbols"`
}

type diagnosticsReport struct {
	Kind        string                 `json:"kind"`
	Total       int                    `json:"total"`
	Diagnostics []codeintel.Diagnostic `json:"diagnostics"`
}

type mapReport struct {
	Kind       string               `json:"kind"`
	Total      int                  `json:"total"`
	Depth      int                  `json:"depth"`
	Limit      int                  `json:"limit"`
	FileCount  int                  `json:"file_count"`
	DirCount   int                  `json:"dir_count"`
	Truncated  bool                 `json:"truncated"`
	Extensions []mapSummaryItem     `json:"extensions,omitempty"`
	TopLevel   []mapSummaryItem     `json:"top_level,omitempty"`
	Entries    []codeintel.MapEntry `json:"entries"`
}

type mapSummaryItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type referencesReport struct {
	Kind       string                `json:"kind"`
	Symbol     string                `json:"symbol"`
	Total      int                   `json:"total"`
	References []codeintel.Reference `json:"references"`
}

type definitionReport struct {
	Kind       string           `json:"kind"`
	Symbol     string           `json:"symbol"`
	Found      bool             `json:"found"`
	Definition codeintel.Symbol `json:"definition,omitempty"`
}

type completionReport struct {
	Kind        string                 `json:"kind"`
	Query       string                 `json:"query"`
	Total       int                    `json:"total"`
	Completions []codeintel.Completion `json:"completions"`
}

type formatReport struct {
	Kind   string                 `json:"kind"`
	Write  bool                   `json:"write"`
	Result codeintel.FormatResult `json:"result"`
}

type teleportReport struct {
	Kind       string             `json:"kind"`
	Query      string             `json:"query"`
	Mode       string             `json:"mode"`
	Found      bool               `json:"found"`
	Path       string             `json:"path,omitempty"`
	Content    string             `json:"content,omitempty"`
	Bytes      int                `json:"bytes,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
	Hover      codeintel.Hover    `json:"hover,omitempty"`
	Candidates []codeintel.Symbol `json:"candidates,omitempty"`
}

func (a *App) Symbols(args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("symbols", args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: codog symbols [--json]")
	}
	symbols, err := codeintel.GoSymbols(a.Workspace)
	if err != nil {
		return err
	}
	report := symbolsReport{Kind: "symbols", Total: len(symbols), Symbols: symbols}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSymbols(a.Out, report)
	return nil
}

func (a *App) Diagnostics(ctx context.Context, args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("diagnostics", args)
	if err != nil {
		return err
	}
	diagnostics, err := codeintel.GoDiagnostics(ctx, a.Workspace, rest)
	if err != nil {
		return err
	}
	report := diagnosticsReport{Kind: "diagnostics", Total: len(diagnostics), Diagnostics: diagnostics}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDiagnostics(a.Out, report)
	return nil
}

func (a *App) Map(args []string) error {
	format, rest, depth, limit, err := parseMapArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected map argument %q", rest[0])
	}
	scanLimit := limit
	if scanLimit > 0 {
		scanLimit++
	}
	entries, err := codeintel.CodeMap(a.Workspace, depth, scanLimit)
	if err != nil {
		return err
	}
	truncated := limit > 0 && len(entries) > limit
	if truncated {
		entries = entries[:limit]
	}
	report := buildMapReport(entries, depth, limit, truncated)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderMap(a.Out, report)
	return nil
}

func buildMapReport(entries []codeintel.MapEntry, depth int, limit int, truncated bool) mapReport {
	report := mapReport{
		Kind:      "map",
		Total:     len(entries),
		Depth:     depth,
		Limit:     limit,
		Truncated: truncated,
		Entries:   entries,
	}
	extensions := map[string]int{}
	topLevel := map[string]int{}
	for _, entry := range entries {
		if entry.Type == "dir" {
			report.DirCount++
		} else {
			report.FileCount++
			ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(entry.Path)), ".")
			if ext == "" {
				ext = "[none]"
			}
			extensions[ext]++
		}
		top := firstMapPathSegment(entry.Path)
		if top != "" {
			topLevel[top]++
		}
	}
	report.Extensions = sortedMapSummaryItems(extensions)
	report.TopLevel = sortedMapSummaryItems(topLevel)
	return report
}

func firstMapPathSegment(path string) string {
	path = strings.Trim(strings.TrimSpace(filepath.ToSlash(path)), "/")
	if path == "" {
		return ""
	}
	if before, _, ok := strings.Cut(path, "/"); ok {
		return before
	}
	return path
}

func sortedMapSummaryItems(counts map[string]int) []mapSummaryItem {
	if len(counts) == 0 {
		return nil
	}
	items := make([]mapSummaryItem, 0, len(counts))
	for name, count := range counts {
		items = append(items, mapSummaryItem{Name: name, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func (a *App) References(args []string) error {
	format, rest, limit, err := parseSymbolLimitArgs("references", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog references SYMBOL [--limit N] [--json]")
	}
	symbol := rest[0]
	refs, err := codeintel.References(a.Workspace, symbol, limit)
	if err != nil {
		return err
	}
	report := referencesReport{Kind: "references", Symbol: symbol, Total: len(refs), References: refs}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReferences(a.Out, report)
	return nil
}

func (a *App) Definition(args []string) error {
	format, rest, err := parseCodeIntelOutputArgs("definition", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog definition SYMBOL [--json]")
	}
	symbol := rest[0]
	definition, found, err := codeintel.Definition(a.Workspace, symbol)
	if err != nil {
		return err
	}
	report := definitionReport{Kind: "definition", Symbol: symbol, Found: found, Definition: definition}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDefinition(a.Out, report)
	return nil
}

func (a *App) Hover(args []string) error {
	format, rest, contextLines, err := parseHoverArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog hover SYMBOL [--context N] [--json]")
	}
	hover, err := codeintel.HoverInfo(a.Workspace, rest[0], contextLines)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{"kind": "hover", "hover": hover}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderHover(a.Out, hover)
	return nil
}

func (a *App) Completion(args []string) error {
	if shellCompletionRequested(args) || shellCompletionOutputFlagPresent(args) {
		return renderShellCompletionCommand(a.Out, args)
	}
	format, rest, limit, err := parseSymbolLimitArgs("completion", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog completion PREFIX [--limit N] [--json] or codog completion bash|zsh|fish [--output PATH]")
	}
	query := rest[0]
	completions, err := codeintel.Completions(a.Workspace, query, limit)
	if err != nil {
		return err
	}
	report := completionReport{Kind: "completion", Query: query, Total: len(completions), Completions: completions}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCompletion(a.Out, report)
	return nil
}

type shellCompletionRequest struct {
	Shell  string
	Output string
}

func shellCompletionRequested(args []string) bool {
	req, err := parseShellCompletionArgs(args)
	return err == nil && req.Shell != ""
}

func shellCompletionOutputFlagPresent(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "--output" || strings.HasPrefix(arg, "--output=") {
			return true
		}
	}
	return false
}

func renderShellCompletionCommand(out io.Writer, args []string) error {
	req, err := parseShellCompletionArgs(args)
	if err != nil {
		return err
	}
	if req.Shell == "" {
		return errors.New("usage: codog completion bash|zsh|fish [--output PATH]")
	}
	script, err := shellCompletionScript(req.Shell)
	if err != nil {
		return err
	}
	if req.Output != "" {
		if err := os.WriteFile(req.Output, []byte(script), 0o644); err != nil {
			return err
		}
		return nil
	}
	fmt.Fprint(out, script)
	return nil
}

func parseShellCompletionArgs(args []string) (shellCompletionRequest, error) {
	const usage = "codog completion bash|zsh|fish [--output PATH]"
	req := shellCompletionRequest{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
			continue
		case arg == "--output":
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "completion", Flag: arg, Usage: usage}
			}
			index++
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--json" || arg == "--limit" || strings.HasPrefix(arg, "--limit=") || arg == "--output-format" || arg == "-o" || strings.HasPrefix(arg, "--output-format="):
			return shellCompletionRequest{}, nil
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "completion", Option: arg, Usage: usage}
		default:
			if req.Shell != "" {
				return req, unexpectedExtraArgsError{Command: "completion", Args: []string{arg}, Usage: usage}
			}
			req.Shell = strings.ToLower(arg)
		}
	}
	if req.Shell == "" {
		if req.Output != "" {
			return req, missingArgumentError{Argument: "shell", Example: "codog completion bash --output codog.bash"}
		}
		return req, nil
	}
	if !validCompletionShell(req.Shell) {
		return req, invalidFlagValueError{
			Flag:    "shell",
			Value:   req.Shell,
			Message: "completion shell must be one of bash, zsh, or fish",
			Usage:   usage,
		}
	}
	return req, nil
}

func validCompletionShell(shell string) bool {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "bash", "zsh", "fish":
		return true
	default:
		return false
	}
}

func shellCompletionScript(shell string) (string, error) {
	commands := strings.Join(shellCompletionCommands(), " ")
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "bash":
		return bashCompletionScript(commands), nil
	case "zsh":
		return zshCompletionScript(shellCompletionCommands()), nil
	case "fish":
		return fishCompletionScript(commands), nil
	default:
		return "", invalidFlagValueError{
			Flag:    "shell",
			Value:   shell,
			Message: "completion shell must be one of bash, zsh, or fish",
			Usage:   "codog completion bash|zsh|fish [--output PATH]",
		}
	}
}

func shellCompletionCommands() []string {
	commands := []string{}
	for _, command := range builtInCommandNames() {
		command = strings.TrimSpace(command)
		if command == "" || strings.HasPrefix(command, "/") {
			continue
		}
		commands = append(commands, command)
	}
	return sortedUniqueStrings(commands)
}

func bashCompletionScript(commands string) string {
	return fmt.Sprintf(`# bash completion for codog
_codog_completion() {
  local cur prev
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"

  case "$prev" in
    completion)
      COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") )
      return 0
      ;;
    --setting-sources)
      COMPREPLY=( $(compgen -W "user project local user,project user,project,local" -- "$cur") )
      return 0
      ;;
    --config|--settings|--cwd|-C|--directory|--plugin-dir|--output)
      compopt -o default 2>/dev/null
      return 0
      ;;
  esac

  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=( $(compgen -W %q -- "$cur") )
  fi
}
complete -F _codog_completion codog
`, commands)
}

func zshCompletionScript(commands []string) string {
	quoted := make([]string, 0, len(commands))
	for _, command := range commands {
		quoted = append(quoted, shellSingleQuote(command))
	}
	return fmt.Sprintf(`#compdef codog

_codog() {
  local -a commands
  commands=(%s)

  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi

  if [[ ${words[2]} == completion && CURRENT == 3 ]]; then
    _values 'shell' bash zsh fish
    return
  fi

  if [[ ${words[CURRENT-1]} == --setting-sources ]]; then
    _values 'source' user project local
    return
  fi

  if [[ ${words[CURRENT-1]} == --config || ${words[CURRENT-1]} == --settings || ${words[CURRENT-1]} == --cwd || ${words[CURRENT-1]} == -C || ${words[CURRENT-1]} == --directory || ${words[CURRENT-1]} == --plugin-dir ]]; then
    _files
    return
  fi

  _files
}

_codog "$@"
`, strings.Join(quoted, " "))
}

func fishCompletionScript(commands string) string {
	return fmt.Sprintf(`# fish completion for codog
complete -c codog -f -n '__fish_use_subcommand' -a %q
complete -c codog -f -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c codog -l output -r -d 'Write completion script to a file'
complete -c codog -l agents -r -d 'Define session agents as JSON'
complete -c codog -l plugin-dir -r -d 'Load a plugin directory for this session'
complete -c codog -l setting-sources -r -a 'user project local' -d 'Select configuration sources'
complete -c codog -l ide -d 'Connect to the active local editor bridge'
`, commands)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (a *App) Format(args []string) error {
	format, rest, write, err := parseFormatArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog format PATH [--write] [--json]")
	}
	result, err := codeintel.FormatGoFile(a.Workspace, rest[0], write)
	if err != nil {
		return err
	}
	report := formatReport{Kind: "format", Write: write, Result: result}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderFormat(a.Out, report)
	return nil
}

func (a *App) Teleport(args []string) error {
	format, rest, limit, err := parseSymbolLimitArgs("teleport", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: codog teleport TARGET [--limit N] [--json]")
	}
	report, err := a.teleportReport(rest[0], limit)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTeleport(a.Out, report)
	return nil
}

func (a *App) teleportReport(target string, limit int) (teleportReport, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return teleportReport{}, errors.New("teleport target is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if file, err := (workspaceops.Service{Workspace: a.Workspace}).Read(workspaceops.ReadOptions{Path: target, Limit: 128 * 1024}); err == nil {
		return teleportReport{
			Kind:      "teleport",
			Query:     target,
			Mode:      "file",
			Found:     true,
			Path:      file.Path,
			Content:   file.Content,
			Bytes:     file.Bytes,
			Truncated: file.Truncated,
		}, nil
	}
	symbols, err := codeintel.GoSymbols(a.Workspace)
	if err != nil {
		return teleportReport{}, err
	}
	exact := []codeintel.Symbol{}
	partial := []codeintel.Symbol{}
	lowerTarget := strings.ToLower(target)
	for _, symbol := range symbols {
		switch {
		case symbol.Name == target:
			exact = append(exact, symbol)
		case strings.Contains(strings.ToLower(symbol.Name), lowerTarget):
			partial = append(partial, symbol)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = partial
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Path == candidates[j].Path {
			if candidates[i].Line == candidates[j].Line {
				return candidates[i].Name < candidates[j].Name
			}
			return candidates[i].Line < candidates[j].Line
		}
		return candidates[i].Path < candidates[j].Path
	})
	if len(candidates) == 0 {
		return teleportReport{Kind: "teleport", Query: target, Mode: "symbol", Found: false}, nil
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 1 {
		hover, err := codeintel.HoverInfo(a.Workspace, candidates[0].Name, 2)
		if err != nil {
			return teleportReport{}, err
		}
		return teleportReport{
			Kind:       "teleport",
			Query:      target,
			Mode:       "symbol",
			Found:      hover.Found,
			Path:       hover.Path,
			Hover:      hover,
			Candidates: candidates,
		}, nil
	}
	return teleportReport{Kind: "teleport", Query: target, Mode: "candidates", Found: true, Candidates: candidates}, nil
}

func renderTeleport(out io.Writer, report teleportReport) {
	fmt.Fprintln(out, "Teleport")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Mode             %s\n", report.Mode)
	if !report.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	switch report.Mode {
	case "file":
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
		fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
		if report.Truncated {
			fmt.Fprintln(out, "  Truncated        true")
		}
		fmt.Fprintln(out)
		fmt.Fprint(out, report.Content)
		if !strings.HasSuffix(report.Content, "\n") {
			fmt.Fprintln(out)
		}
	case "symbol":
		if report.Hover.Path != "" {
			fmt.Fprintf(out, "  Location         %s:%d\n", report.Hover.Path, report.Hover.Line)
			fmt.Fprintf(out, "  Kind             %s\n", report.Hover.Kind)
			fmt.Fprintln(out)
			for _, line := range report.Hover.Snippet {
				fmt.Fprintln(out, line)
			}
		}
	default:
		fmt.Fprintf(out, "  Candidates       %d\n", len(report.Candidates))
		fmt.Fprintln(out)
		for _, candidate := range report.Candidates {
			fmt.Fprintf(out, "%s:%d:%s %s\n", candidate.Path, candidate.Line, candidate.Kind, candidate.Name)
		}
	}
}

func renderSymbols(out io.Writer, report symbolsReport) {
	fmt.Fprintln(out, "Symbols")
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, symbol := range report.Symbols {
		fmt.Fprintf(out, "%s:%d:%s %s\n", symbol.Path, symbol.Line, symbol.Kind, symbol.Name)
	}
}

func renderDiagnostics(out io.Writer, report diagnosticsReport) {
	fmt.Fprintln(out, "Diagnostics")
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	if report.Total == 0 {
		fmt.Fprintln(out, "No diagnostics.")
		return
	}
	for _, diagnostic := range report.Diagnostics {
		location := diagnostic.Package
		if diagnostic.Path != "" {
			location = fmt.Sprintf("%s:%d", diagnostic.Path, diagnostic.Line)
			if diagnostic.Column > 0 {
				location = fmt.Sprintf("%s:%d:%d", diagnostic.Path, diagnostic.Line, diagnostic.Column)
			}
		}
		fmt.Fprintf(out, "%s %s\n", location, diagnostic.Message)
	}
}

func renderMap(out io.Writer, report mapReport) {
	fmt.Fprintln(out, "Map")
	fmt.Fprintf(out, "  Entries          %d\n", report.Total)
	fmt.Fprintf(out, "  Files            %d\n", report.FileCount)
	fmt.Fprintf(out, "  Directories      %d\n", report.DirCount)
	fmt.Fprintf(out, "  Depth            %d\n", report.Depth)
	fmt.Fprintf(out, "  Limit            %d\n", report.Limit)
	fmt.Fprintf(out, "  Truncated        %t\n", report.Truncated)
	if len(report.Extensions) > 0 {
		fmt.Fprintf(out, "  Extensions       %s\n", renderMapSummaryInline(report.Extensions))
	}
	if len(report.TopLevel) > 0 {
		fmt.Fprintf(out, "  Top level        %s\n", renderMapSummaryInline(report.TopLevel))
	}
	for _, entry := range report.Entries {
		fmt.Fprintf(out, "%s\t%s\n", entry.Type, entry.Path)
	}
}

func renderMapSummaryInline(items []mapSummaryItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s=%d", item.Name, item.Count))
	}
	return strings.Join(parts, ", ")
}

func renderReferences(out io.Writer, report referencesReport) {
	fmt.Fprintln(out, "References")
	fmt.Fprintf(out, "  Symbol           %s\n", report.Symbol)
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, ref := range report.References {
		fmt.Fprintf(out, "%s:%d:%s\n", ref.Path, ref.Line, ref.Text)
	}
}

func renderDefinition(out io.Writer, report definitionReport) {
	fmt.Fprintln(out, "Definition")
	fmt.Fprintf(out, "  Symbol           %s\n", report.Symbol)
	if !report.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	fmt.Fprintf(out, "  Location         %s:%d\n", report.Definition.Path, report.Definition.Line)
	fmt.Fprintf(out, "  Kind             %s\n", report.Definition.Kind)
}

func renderHover(out io.Writer, hover codeintel.Hover) {
	fmt.Fprintln(out, "Hover")
	fmt.Fprintf(out, "  Symbol           %s\n", hover.Symbol)
	if !hover.Found {
		fmt.Fprintln(out, "  Found            false")
		return
	}
	fmt.Fprintln(out, "  Found            true")
	fmt.Fprintf(out, "  Location         %s:%d\n", hover.Path, hover.Line)
	fmt.Fprintf(out, "  Kind             %s\n", hover.Kind)
	fmt.Fprintln(out)
	for _, line := range hover.Snippet {
		fmt.Fprintln(out, line)
	}
}

func renderCompletion(out io.Writer, report completionReport) {
	fmt.Fprintln(out, "Completion")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Total            %d\n", report.Total)
	for _, completion := range report.Completions {
		if completion.Path != "" {
			fmt.Fprintf(out, "%s:%d:%s %s\n", completion.Path, completion.Line, completion.Kind, completion.Label)
			continue
		}
		if completion.Detail != "" {
			fmt.Fprintf(out, "%s %s %s\n", completion.Kind, completion.Label, completion.Detail)
			continue
		}
		fmt.Fprintf(out, "%s %s\n", completion.Kind, completion.Label)
	}
}

func renderFormat(out io.Writer, report formatReport) {
	fmt.Fprintln(out, "Format")
	fmt.Fprintf(out, "  Path             %s\n", report.Result.Path)
	fmt.Fprintf(out, "  Changed          %t\n", report.Result.Changed)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Result.Bytes)
	fmt.Fprintf(out, "  Written          %t\n", report.Write)
	if !report.Write && report.Result.Content != "" {
		fmt.Fprintln(out)
		fmt.Fprint(out, report.Result.Content)
		if !strings.HasSuffix(report.Result.Content, "\n") {
			fmt.Fprintln(out)
		}
	}
}

func parseCodeIntelOutputArgs(command string, args []string) (string, []string, error) {
	format := "text"
	rest := []string{}
	usage := "codog " + command + " [ARGS...] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return "", nil, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat(command, format, []string{"text", "json"})
	if err != nil {
		return "", nil, err
	}
	return normalizedFormat, rest, nil
}

func parseMapArgs(args []string) (string, []string, int, int, error) {
	const usage = "codog map [PATH] [--depth N] [--limit N] [--json|--output-format text|json]"
	format, rest, err := parseCodeIntelOutputArgs("map", args)
	if err != nil {
		return "", nil, 0, 0, err
	}
	depth := 3
	limit := 200
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--depth":
			index++
			if missingFlagValueAt(rest, index) {
				return "", nil, 0, 0, missingFlagValueError{Command: "map", Flag: arg, Usage: usage}
			}
			parsed, err := parsePositiveIntOption(rest[index], "--depth", usage)
			if err != nil {
				return "", nil, 0, 0, err
			}
			depth = parsed
		case strings.HasPrefix(arg, "--depth="):
			parsed, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--depth="), "--depth", usage)
			if err != nil {
				return "", nil, 0, 0, err
			}
			depth = parsed
		case arg == "--limit":
			index++
			if missingFlagValueAt(rest, index) {
				return "", nil, 0, 0, missingFlagValueError{Command: "map", Flag: arg, Usage: usage}
			}
			parsed, err := parsePositiveIntOption(rest[index], "--limit", usage)
			if err != nil {
				return "", nil, 0, 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--limit="):
			parsed, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return "", nil, 0, 0, err
			}
			limit = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, depth, limit, nil
}

func parseSymbolLimitArgs(command string, args []string) (string, []string, int, error) {
	usage := "codog " + command + " [PATH] [--limit N] [--json|--output-format text|json]"
	format, rest, err := parseCodeIntelOutputArgs(command, args)
	if err != nil {
		return "", nil, 0, err
	}
	limit := 100
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--limit":
			index++
			if missingFlagValueAt(rest, index) {
				return "", nil, 0, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			parsed, err := parsePositiveIntOption(rest[index], "--limit", usage)
			if err != nil {
				return "", nil, 0, err
			}
			limit = parsed
		case strings.HasPrefix(arg, "--limit="):
			parsed, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--limit="), "--limit", usage)
			if err != nil {
				return "", nil, 0, err
			}
			limit = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, limit, nil
}

func parseHoverArgs(args []string) (string, []string, int, error) {
	const usage = "codog hover [PATH] [--context N] [--json|--output-format text|json]"
	format, rest, err := parseCodeIntelOutputArgs("hover", args)
	if err != nil {
		return "", nil, 0, err
	}
	contextLines := 2
	filtered := []string{}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "--context":
			index++
			if missingFlagValueAt(rest, index) {
				return "", nil, 0, missingFlagValueError{Command: "hover", Flag: arg, Usage: usage}
			}
			parsed, err := parsePositiveIntOption(rest[index], "--context", usage)
			if err != nil {
				return "", nil, 0, err
			}
			contextLines = parsed
		case strings.HasPrefix(arg, "--context="):
			parsed, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--context="), "--context", usage)
			if err != nil {
				return "", nil, 0, err
			}
			contextLines = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, contextLines, nil
}

func parseFormatArgs(args []string) (string, []string, bool, error) {
	const usage = "codog format [PATH] [--write[=true|false]] [--json|--output-format text|json]"
	format, rest, err := parseCodeIntelOutputArgs("format", args)
	if err != nil {
		return "", nil, false, err
	}
	write := false
	filtered := []string{}
	for _, arg := range rest {
		switch {
		case arg == "--write":
			write = true
		case strings.HasPrefix(arg, "--write="):
			parsed, err := strconv.ParseBool(strings.TrimPrefix(arg, "--write="))
			if err != nil {
				return "", nil, false, invalidFlagValueError{Flag: "--write", Value: strings.TrimPrefix(arg, "--write="), Message: "format write must be a boolean", Usage: usage}
			}
			write = parsed
		default:
			filtered = append(filtered, arg)
		}
	}
	return format, filtered, write, nil
}

func parsePositiveInt(value string, label string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return parsed, nil
}

func parsePositiveIntOption(value string, option string, usage string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, invalidFlagValueError{
			Flag:    option,
			Value:   value,
			Message: strings.TrimLeft(option, "-") + " must be a positive integer",
			Usage:   usage,
		}
	}
	return parsed, nil
}

func parseNonNegativeIntOption(value string, option string, usage string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, invalidFlagValueError{
			Flag:    option,
			Value:   value,
			Message: strings.TrimLeft(option, "-") + " must be a non-negative integer",
			Usage:   usage,
		}
	}
	return parsed, nil
}

func (a *App) CodeIntel(args []string) error {
	if len(args) == 0 || strings.HasPrefix(strings.TrimSpace(args[0]), "-") {
		return a.Symbols(args)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch action {
	case "symbols":
		return a.Symbols(rest)
	case "diagnostics":
		return a.Diagnostics(context.Background(), rest)
	case "map":
		return a.Map(rest)
	case "references":
		return a.References(rest)
	case "definition":
		return a.Definition(rest)
	case "hover":
		return a.Hover(rest)
	case "teleport":
		return a.Teleport(rest)
	case "completion", "completions":
		return a.Completion(rest)
	case "format", "formatting":
		return a.Format(rest)
	case "lsp":
		return a.CodeIntelLSP(rest)
	case "notebook-read", "notebook":
		req, err := parseCodeIntelNotebookReadArgs(rest)
		if err != nil {
			return err
		}
		path, err := a.resolveCodeIntelNotebookPath(req.NotebookPath)
		if err != nil {
			return err
		}
		result, err := codeintel.ReadNotebook(path, codeintel.NotebookReadOptions{
			CellIndex:      req.CellIndex,
			Limit:          req.Limit,
			IncludeOutputs: req.IncludeOutputs,
		})
		if err != nil {
			return err
		}
		result.Path = displayCodeIntelNotebookPath(a.Workspace, result.Path)
		report := codeIntelNotebookReadReport{
			Kind:   "notebook_read",
			Action: "read",
			Status: "ok",
			Result: result,
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderCodeIntelNotebookRead(a.Out, report)
		return nil
	case "notebook-edit":
		req, err := parseCodeIntelNotebookEditArgs(rest)
		if err != nil {
			return err
		}
		path, err := a.resolveCodeIntelNotebookPath(req.NotebookPath)
		if err != nil {
			return err
		}
		index, err := codeintel.ResolveNotebookEditIndex(path, req.CellIndex, req.CellID, req.Mode)
		if err != nil {
			return err
		}
		result, err := codeintel.EditNotebook(path, codeintel.NotebookEditOptions{
			Index:    index,
			CellType: req.CellType,
			Source:   req.Source,
			Mode:     req.Mode,
		})
		if err != nil {
			return err
		}
		result.Path = displayCodeIntelNotebookPath(a.Workspace, result.Path)
		report := codeIntelNotebookEditReport{
			Kind:   "notebook_edit",
			Action: "edit",
			Status: "ok",
			Result: result,
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderCodeIntelNotebookEdit(a.Out, report)
		return nil
	default:
		return unknownCodeIntelActionError(args[0])
	}
}

var codeIntelActionCandidates = []string{
	"symbols", "diagnostics", "map", "references", "definition", "hover", "teleport",
	"completion", "completions", "format", "formatting", "lsp", "notebook",
	"notebook-read", "notebook-edit",
}

func unknownCodeIntelActionError(action string) error {
	return unknownActionError{
		Command:     "codog code-intel",
		Action:      action,
		Expected:    append([]string(nil), codeIntelActionCandidates...),
		Suggestions: toolnames.Suggestions(action, codeIntelActionCandidates, 4),
		Usage:       "codog code-intel [symbols|diagnostics|map|references|definition|hover|teleport|completion|format|lsp|notebook-read|notebook-edit] [ARGS...]",
	}
}

type codeIntelNotebookReadRequest struct {
	Format         string
	NotebookPath   string
	CellIndex      *int
	Limit          int
	IncludeOutputs bool
}

type codeIntelNotebookReadReport struct {
	Kind   string                       `json:"kind"`
	Action string                       `json:"action"`
	Status string                       `json:"status"`
	Result codeintel.NotebookReadResult `json:"result"`
}

type codeIntelNotebookEditRequest struct {
	Format       string
	NotebookPath string
	Mode         string
	CellIndex    *int
	CellID       string
	CellType     string
	Source       string
	SourceSet    bool
}

type codeIntelNotebookEditReport struct {
	Kind   string                       `json:"kind"`
	Action string                       `json:"action"`
	Status string                       `json:"status"`
	Result codeintel.NotebookEditResult `json:"result"`
}

func parseCodeIntelNotebookReadArgs(args []string) (codeIntelNotebookReadRequest, error) {
	format, rest, err := parseCodeIntelOutputArgs("notebook-read", args)
	if err != nil {
		return codeIntelNotebookReadRequest{}, err
	}
	parser := notebookReadArgParser{req: codeIntelNotebookReadRequest{Format: format, Limit: 100}}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		handled, err := parser.consumeOutputOption(arg)
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		handled, err = consumeValueOption(rest, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		parser.positionals = append(parser.positionals, arg)
	}
	if len(parser.positionals) != 1 {
		return parser.req, errors.New("usage: codog code-intel notebook-read NOTEBOOK [--cell-index N] [--limit N] [--include-outputs] [--json]")
	}
	parser.req.NotebookPath = parser.positionals[0]
	return parser.req, nil
}

type notebookReadArgParser struct {
	req         codeIntelNotebookReadRequest
	positionals []string
}

func (p *notebookReadArgParser) consumeOutputOption(arg string) (bool, error) {
	switch arg {
	case "--include-outputs", "--outputs":
		p.req.IncludeOutputs = true
		return true, nil
	case "--no-outputs":
		p.req.IncludeOutputs = false
		return true, nil
	}
	name, value, inline := strings.Cut(arg, "=")
	if !inline || name != "--include-outputs" && name != "--outputs" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		label := "include outputs"
		if name == "--outputs" {
			label = "outputs"
		}
		return true, fmt.Errorf("notebook-read %s must be a boolean", label)
	}
	p.req.IncludeOutputs = parsed
	return true, nil
}

func (p *notebookReadArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--cell-index": p.cellIndexOption(),
		"--index":      p.cellIndexOption(),
		"--limit":      p.limitOption(),
	}
}

func (p *notebookReadArgParser) cellIndexOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("notebook-read cell index is required") }, set: func(value string) error {
		parsed, err := parseNonNegativeInt(value, "notebook-read cell index")
		if err != nil {
			return err
		}
		p.req.CellIndex = &parsed
		return nil
	}}
}

func (p *notebookReadArgParser) limitOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("notebook-read limit is required") }, set: func(value string) error {
		parsed, err := parsePositiveInt(value, "notebook-read limit")
		if err != nil {
			return err
		}
		p.req.Limit = parsed
		return nil
	}}
}

func parseCodeIntelNotebookEditArgs(args []string) (codeIntelNotebookEditRequest, error) {
	format, rest, err := parseCodeIntelOutputArgs("notebook-edit", args)
	if err != nil {
		return codeIntelNotebookEditRequest{}, err
	}
	parser := notebookEditArgParser{req: codeIntelNotebookEditRequest{Format: format, Mode: "replace"}}
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		handled, err := consumeValueOption(rest, &index, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		parser.positionals = append(parser.positionals, arg)
	}
	return parser.finish()
}

type notebookEditArgParser struct {
	req         codeIntelNotebookEditRequest
	positionals []string
}

func (p *notebookEditArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--mode":       stringValueOption(&p.req.Mode, "notebook-edit mode is required"),
		"--edit-mode":  stringValueOption(&p.req.Mode, "notebook-edit mode is required"),
		"--cell-index": p.cellIndexOption(),
		"--index":      p.cellIndexOption(),
		"--cell-id":    stringValueOption(&p.req.CellID, "notebook-edit cell id is required"),
		"--cell-type":  stringValueOption(&p.req.CellType, "notebook-edit cell type is required"),
		"--type":       stringValueOption(&p.req.CellType, "notebook-edit cell type is required"),
		"--source":     p.sourceOption(),
		"--new-source": p.sourceOption(),
		"--new_source": p.sourceOption(),
	}
}

func (p *notebookEditArgParser) cellIndexOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("notebook-edit cell index is required") }, set: func(value string) error {
		parsed, err := parseNonNegativeInt(value, "notebook-edit cell index")
		if err != nil {
			return err
		}
		p.req.CellIndex = &parsed
		return nil
	}}
}

func (p *notebookEditArgParser) sourceOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("notebook-edit source is required") }, set: func(value string) error {
		p.req.Source, p.req.SourceSet = value, true
		return nil
	}}
}

func (p *notebookEditArgParser) finish() (codeIntelNotebookEditRequest, error) {
	if strings.TrimSpace(p.req.Mode) == "" {
		p.req.Mode = "replace"
	}
	p.req.Mode = strings.ToLower(strings.TrimSpace(p.req.Mode))
	if p.req.CellIndex != nil && strings.TrimSpace(p.req.CellID) != "" {
		return p.req, errors.New("notebook-edit accepts either cell_index or cell_id, not both")
	}
	if len(p.positionals) >= 4 && p.req.CellIndex == nil && p.req.CellID == "" && p.req.CellType == "" && !p.req.SourceSet {
		parsed, err := parseNonNegativeInt(p.positionals[1], "notebook-edit cell index")
		if err != nil {
			return p.req, err
		}
		p.req.NotebookPath = p.positionals[0]
		p.req.CellIndex = &parsed
		p.req.CellType = p.positionals[2]
		p.req.Source = strings.Join(p.positionals[3:], " ")
		p.req.SourceSet = true
		return validateCodeIntelNotebookEditRequest(p.req)
	}
	if len(p.positionals) == 0 {
		return p.req, errors.New("usage: codog code-intel notebook-edit NOTEBOOK [--mode replace|insert|delete] [--cell-index N|--cell-id ID] [--cell-type code|markdown|raw] [--source TEXT] [--json]")
	}
	p.req.NotebookPath = p.positionals[0]
	if len(p.positionals) > 1 && !p.req.SourceSet {
		p.req.Source = strings.Join(p.positionals[1:], " ")
		p.req.SourceSet = true
	}
	return validateCodeIntelNotebookEditRequest(p.req)
}

func validateCodeIntelNotebookEditRequest(req codeIntelNotebookEditRequest) (codeIntelNotebookEditRequest, error) {
	mode, err := codeintel.NormalizeNotebookEditMode(req.Mode)
	if err != nil {
		return req, err
	}
	req.Mode = mode
	if strings.TrimSpace(req.NotebookPath) == "" {
		return req, errors.New("notebook-edit notebook path is required")
	}
	if (req.Mode == "insert" || req.Mode == "replace") && !req.SourceSet {
		return req, errors.New("new_source is required for insert and replace edits")
	}
	return req, nil
}

func parseNonNegativeInt(value string, label string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", label)
	}
	return parsed, nil
}

func (a *App) resolveCodeIntelNotebookPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("notebook path is required")
	}
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if resolvedWorkspace, err := filepath.EvalSymlinks(absWorkspace); err == nil {
		absWorkspace = resolvedWorkspace
	}
	if resolvedPath, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolvedPath
	}
	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("notebook path escapes workspace: %s", path)
	}
	if !strings.HasSuffix(strings.ToLower(absPath), ".ipynb") {
		return "", errors.New("notebook path must point to a .ipynb file")
	}
	return filepath.Clean(absPath), nil
}

func displayCodeIntelNotebookPath(workspace string, path string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return path
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		absWorkspace = workspace
	}
	if resolvedWorkspace, err := filepath.EvalSymlinks(absWorkspace); err == nil {
		absWorkspace = resolvedWorkspace
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	if resolvedPath, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolvedPath
	}
	rel, err := filepath.Rel(absWorkspace, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.ToSlash(rel)
}

func renderCodeIntelNotebookRead(out io.Writer, report codeIntelNotebookReadReport) {
	result := report.Result
	fmt.Fprintln(out, "Notebook Read")
	fmt.Fprintf(out, "  Path             %s\n", result.Path)
	if result.Language != "" {
		fmt.Fprintf(out, "  Language         %s\n", result.Language)
	}
	fmt.Fprintf(out, "  Cell count       %d\n", result.CellCount)
	fmt.Fprintf(out, "  Returned         %d\n", len(result.Cells))
	fmt.Fprintf(out, "  Truncated        %t\n", result.Truncated)
	for _, cell := range result.Cells {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "Cell %d", cell.Index)
		if cell.CellID != "" {
			fmt.Fprintf(out, " (%s)", cell.CellID)
		}
		if cell.CellType != "" {
			fmt.Fprintf(out, " [%s]", cell.CellType)
		}
		fmt.Fprintln(out)
		if cell.OutputCount > 0 {
			fmt.Fprintf(out, "Outputs           %d\n", cell.OutputCount)
		}
		if strings.TrimSpace(cell.Source) != "" {
			fmt.Fprint(out, cell.Source)
			if !strings.HasSuffix(cell.Source, "\n") {
				fmt.Fprintln(out)
			}
		}
	}
}

func renderCodeIntelNotebookEdit(out io.Writer, report codeIntelNotebookEditReport) {
	result := report.Result
	fmt.Fprintln(out, "Notebook Edit")
	fmt.Fprintf(out, "  Path             %s\n", result.Path)
	fmt.Fprintf(out, "  Mode             %s\n", result.Mode)
	fmt.Fprintf(out, "  Index            %d\n", result.Index)
	if result.CellID != "" {
		fmt.Fprintf(out, "  Cell id          %s\n", result.CellID)
	}
	if result.CellType != "" {
		fmt.Fprintf(out, "  Cell type        %s\n", result.CellType)
	}
	if result.Language != "" {
		fmt.Fprintf(out, "  Language         %s\n", result.Language)
	}
	fmt.Fprintf(out, "  Cell count       %d\n", result.CellCount)
	fmt.Fprintf(out, "  Source lines     %d\n", result.SourceLines)
}

func (a *App) CodeIntelLSP(args []string) error {
	format, args, err := parseCodeIntelLSPArgs(args)
	if err != nil {
		return err
	}
	store := codeintel.NewLSPStore(a.Config.ConfigHome, a.Workspace)
	if len(args) == 0 || args[0] == "list" {
		return a.codeIntelLSPList(&store, format)
	}
	payload, err := a.codeIntelLSPPayload(&store, args[0], args[1:])
	if err != nil {
		return err
	}
	return renderCodeIntelLSPPayload(a.Out, format, payload)
}

func (a *App) codeIntelLSPList(store *codeintel.LSPStore, format string) error {
	statuses, err := store.List()
	if err != nil {
		return err
	}
	return renderCodeIntelLSPPayload(a.Out, format, codeIntelLSPListReport{
		Kind:    "lsp_list",
		Action:  "list",
		Status:  "ok",
		Count:   len(statuses),
		Servers: statuses,
		Message: lspListMessage(statuses),
	})
}

func (a *App) codeIntelLSPPayload(store *codeintel.LSPStore, action string, args []string) (any, error) {
	switch action {
	case "actions", "capabilities":
		actions := codeintel.SupportedLSPActions()
		return codeIntelLSPActionsReport{
			Kind:    "lsp_actions",
			Action:  "actions",
			Status:  "ok",
			Count:   len(actions),
			Actions: actions,
			Message: "LSP query actions are resolved locally; start a language server before running `code-intel lsp query`.",
		}, nil
	case "discover":
		candidates := codeintel.DefaultLSPCandidates()
		return codeIntelLSPDiscoverReport{
			Kind:       "lsp_discover",
			Action:     "discover",
			Status:     "ok",
			Count:      len(candidates),
			Candidates: candidates,
			Message:    "Language-server candidates are discovered from common executable names on PATH.",
		}, nil
	case "start":
		if len(args) < 1 {
			return nil, errors.New("usage: codog code-intel lsp start LANGUAGE [COMMAND...]")
		}
		return store.Start(args[0], args[1:])
	case "status":
		if len(args) < 1 {
			return nil, errors.New("usage: codog code-intel lsp status LANGUAGE")
		}
		return store.Status(args[0])
	case "query", "request":
		language, req, err := parseCodeIntelLSPQueryArgs(args)
		if err != nil {
			return nil, err
		}
		return a.lspClientPool().Query(context.Background(), *store, language, req)
	case "stop":
		if len(args) < 1 {
			return nil, errors.New("usage: codog code-intel lsp stop LANGUAGE")
		}
		status, err := store.Stop(args[0])
		if err != nil {
			return nil, err
		}
		if err := a.lspClientPool().Invalidate(args[0]); err != nil {
			return nil, err
		}
		return status, nil
	default:
		return nil, unknownCodeIntelLSPActionError(action)
	}
}

func parseCodeIntelLSPQueryArgs(args []string) (string, codeintel.LSPQueryRequest, error) {
	req := codeintel.LSPQueryRequest{}
	cleanArgs, err := consumeCodeIntelLSPQueryOptions(args, &req)
	if err != nil {
		return "", req, err
	}
	if len(cleanArgs) < 3 {
		return "", req, errors.New("usage: codog code-intel lsp query LANGUAGE ACTION PATH [LINE CHARACTER [NEW_NAME]] [--write|--apply]")
	}
	req.Action = cleanArgs[1]
	req.Path = cleanArgs[2]
	if err := setCodeIntelLSPQueryPosition(&req, cleanArgs); err != nil {
		return "", req, err
	}
	if len(cleanArgs) > 5 {
		req.NewName = cleanArgs[5]
	}
	return cleanArgs[0], req, nil
}

func consumeCodeIntelLSPQueryOptions(args []string, req *codeintel.LSPQueryRequest) ([]string, error) {
	cleanArgs := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--write" || arg == "--apply":
			req.Apply = true
		case arg == "--preview" || arg == "--dry-run":
			req.Apply = false
		case arg == "--code-action-title" || arg == "--action-title":
			index++
			if index >= len(args) {
				return nil, errors.New("lsp query code action title is required")
			}
			req.CodeActionTitle = args[index]
		case strings.HasPrefix(arg, "--code-action-title="):
			req.CodeActionTitle = strings.TrimPrefix(arg, "--code-action-title=")
		case strings.HasPrefix(arg, "--action-title="):
			req.CodeActionTitle = strings.TrimPrefix(arg, "--action-title=")
		default:
			cleanArgs = append(cleanArgs, arg)
		}
	}
	return cleanArgs, nil
}

func setCodeIntelLSPQueryPosition(req *codeintel.LSPQueryRequest, args []string) error {
	if len(args) > 3 {
		line, err := strconv.Atoi(args[3])
		if err != nil {
			return errors.New("lsp query line must be an integer")
		}
		req.Line = line
	}
	if len(args) > 4 {
		character, err := strconv.Atoi(args[4])
		if err != nil {
			return errors.New("lsp query character must be an integer")
		}
		req.Character = character
	}
	return nil
}

var codeIntelLSPActionCandidates = []string{
	"list", "actions", "capabilities", "discover", "start", "status", "query", "request", "stop",
}

func unknownCodeIntelLSPActionError(action string) error {
	return unknownActionError{
		Command:     "codog code-intel lsp",
		Action:      action,
		Expected:    append([]string(nil), codeIntelLSPActionCandidates...),
		Suggestions: toolnames.Suggestions(action, codeIntelLSPActionCandidates, 4),
		Usage:       "codog code-intel lsp [list|actions|discover|start|status|query|stop] [ARGS...]",
	}
}

func parseCodeIntelLSPArgs(args []string) (string, []string, error) {
	format := "json"
	rest := []string{}
	usage := "codog code-intel lsp [list|actions|discover|start|status|query|stop] [ARGS...] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return "", nil, missingFlagValueError{Command: "code-intel lsp", Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			rest = append(rest, arg)
		}
	}
	normalized, err := normalizeOutputFormat("code-intel lsp", format, []string{"text", "json"})
	if err != nil {
		return "", nil, err
	}
	return normalized, rest, nil
}
