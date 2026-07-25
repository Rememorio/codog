package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridgeparity"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/configvalidate"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/harness"
	"github.com/Rememorio/codog/internal/mcpserver"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/orchestrationparity"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/planmode"
	"github.com/Rememorio/codog/internal/prompthistory"
	"github.com/Rememorio/codog/internal/releaseparity"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/slash"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/terminalparity"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/undo"
	"github.com/Rememorio/codog/internal/usage"
)

func statusLineCommandEnv(configEnv map[string]string) []string {
	env := os.Environ()
	if len(configEnv) == 0 {
		return env
	}
	for key, value := range configEnv {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env
}

func normalizeStatusLineCommandOutput(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cleanedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (a *App) Context(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("context", args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	report := a.buildContextReport(active)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	contextview.RenderText(a.Out, report)
	return nil
}

type contextVizRequest struct {
	Format string
	Output string
}

type contextVizReport struct {
	Kind    string             `json:"kind"`
	Action  string             `json:"action"`
	Status  string             `json:"status"`
	File    string             `json:"file"`
	Bytes   int                `json:"bytes"`
	Context contextview.Report `json:"context"`
}

func (a *App) ContextViz(args []string, overrides config.FlagOverrides) error {
	req, err := parseContextVizArgs(args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	contextReport := a.buildContextReport(active)
	html := []byte(contextview.RenderHTML(contextReport))
	output := req.Output
	if output == "" {
		output = filepath.Join(".codog", "context-viz.html")
	}
	path := a.resolveOutputPath(output)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, html, 0o644); err != nil {
		return err
	}
	report := contextVizReport{
		Kind:    "ctx_viz",
		Action:  "write",
		Status:  "ok",
		File:    path,
		Bytes:   len(html),
		Context: contextReport,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderContextVizReport(a.Out, report)
	return nil
}

func parseContextVizArgs(args []string) (contextVizRequest, error) {
	const usage = "codog ctx_viz [OUTPUT] [--output PATH] [--json|--output-format text|json]"
	req := contextVizRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "ctx_viz", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--output" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "ctx_viz", Flag: arg, Usage: usage}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "ctx_viz", Option: arg, Usage: usage}
		default:
			if req.Output != "" {
				return req, unexpectedExtraArgsError{Command: "ctx_viz", Args: []string{arg}, Usage: usage}
			}
			req.Output = arg
		}
	}
	normalizedFormat, err := normalizeOutputFormat("ctx_viz", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func renderContextVizReport(out io.Writer, report contextVizReport) {
	fmt.Fprintln(out, "Context Viz")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  File             %s\n", report.File)
	fmt.Fprintf(out, "  Bytes            %d\n", report.Bytes)
	fmt.Fprintf(out, "  Context status   %s\n", report.Context.Status)
}

func (a *App) contextSession(overrides config.FlagOverrides) (*session.Session, error) {
	sessionRef := overrides.Resume
	if sessionRef == "" {
		sessionRef = overrides.SessionID
	}
	if sessionRef == "" || a.Sessions == nil {
		return nil, nil
	}
	if strings.TrimSpace(overrides.Resume) != "" {
		return a.Sessions.OpenExisting(sessionRef)
	}
	return a.Sessions.Open(sessionRef)
}

func (a *App) buildContextReport(active *session.Session) contextview.Report {
	var warnings []string
	memoryReport, err := memory.BuildReport(a.Workspace)
	if err != nil {
		warnings = append(warnings, "memory: "+err.Error())
	}
	focusReport, err := focus.BuildReport(a.Workspace)
	if err != nil {
		warnings = append(warnings, "focus: "+err.Error())
	}
	var tokenEstimate usage.Summary
	if active != nil {
		tokenEstimate = usage.Estimate(active.Messages, a.Config.Model)
	}
	return contextview.Build(contextview.Options{
		Status:       a.statusSnapshot(active),
		Memory:       memoryReport,
		Focus:        focusReport,
		TokenUsage:   tokenEstimate,
		SystemPrompt: a.systemPrompt(),
		Warnings:     warnings,
	})
}

type planRequest struct {
	Action string
	Format string
	Text   string
}

func (a *App) Plan(args []string) error {
	req, err := parsePlanArgs(args)
	if err != nil {
		return err
	}
	var report planmode.Report
	switch req.Action {
	case "show":
		report, err = planmode.Show(a.Workspace)
	case "enter":
		report, err = planmode.Enter(a.Workspace, req.Text)
	case "set":
		report, err = planmode.Set(a.Workspace, req.Text)
	case "exit":
		report, err = planmode.Exit(a.Workspace)
	case "clear":
		report, err = planmode.Clear(a.Workspace)
	case "open":
		report, err = a.openPlan()
	default:
		return fmt.Errorf("unknown plan action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	planmode.RenderText(a.Out, report)
	return nil
}

func (a *App) openPlan() (planmode.Report, error) {
	state, err := planmode.Load(a.Workspace)
	if err != nil {
		return planmode.Report{}, err
	}
	report := planmode.Report{
		Kind:   "plan",
		Action: "open",
		Status: "missing",
		Path:   planmode.Path(a.Workspace),
		State:  state,
	}
	if strings.TrimSpace(state.Plan) == "" {
		report.EditorError = "no plan written yet"
		return report, nil
	}
	editor, err := openPathInEditor(report.Path)
	report.Editor = editor
	if err != nil {
		report.Status = "open_failed"
		report.EditorError = err.Error()
		return report, nil
	}
	report.Status = "opened"
	report.Opened = true
	return report, nil
}

type historyRequest struct {
	SessionID string
	Format    string
	Limit     int
	Offset    int
	UseOffset bool
}

type summaryRequest struct {
	SessionID string
	Format    string
}

func (a *App) History(args []string, overrides config.FlagOverrides) error {
	req, err := parseHistoryArgs(args, overrides)
	if err != nil {
		return err
	}
	sessionID := req.SessionID
	if session.IsSessionReferenceAlias(sessionID) {
		latest, err := a.Sessions.LatestID()
		if errors.Is(err, session.ErrNoSessions) {
			return a.renderPromptHistory(req.Format, "", nil, req)
		}
		if err != nil {
			return err
		}
		sessionID = latest
	}
	entries, err := a.Sessions.PromptHistory(sessionID)
	if err != nil {
		return err
	}
	return a.renderPromptHistory(req.Format, sessionID, entries, req)
}

func (a *App) Summary(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseSummaryArgs(args, overrides)
	if err != nil {
		return err
	}
	sess, err := a.Sessions.Open(req.SessionID)
	if err != nil {
		return err
	}
	report := sessionsummary.Build(sess.ID, sess.Path, a.Config.Model, sess.Messages)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	sessionsummary.RenderText(a.Out, report)
	return nil
}

type rewindRequest struct {
	SessionID string
	Format    string
	Messages  int
}

type rewindReport struct {
	Kind   string               `json:"kind"`
	Action string               `json:"action"`
	Status string               `json:"status"`
	Result session.RewindResult `json:"result"`
}

func (a *App) Rewind(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseRewindArgs(args, overrides, "")
	if err != nil {
		return err
	}
	if req.SessionID == "" {
		req.SessionID = "latest"
	}
	result, err := a.Sessions.Rewind(req.SessionID, req.Messages)
	if err != nil {
		return err
	}
	a.renderRewindReport(req.Format, result)
	return nil
}

func (a *App) renderRewindReport(format string, result session.RewindResult) {
	report := rewindReport{
		Kind:   "rewind",
		Action: "rewind",
		Status: "ok",
		Result: result,
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	fmt.Fprintln(a.Out, "Rewind")
	fmt.Fprintf(a.Out, "  Session          %s\n", result.SessionID)
	fmt.Fprintf(a.Out, "  Removed          %d\n", result.RemovedMessages)
	fmt.Fprintf(a.Out, "  Remaining        %d\n", result.RemainingMessages)
	fmt.Fprintf(a.Out, "  Path             %s\n", result.Path)
}

func (a *App) Undo(args []string) error {
	format, err := parseSimpleOutputFormat("undo", args)
	if err != nil {
		return err
	}
	report, err := undo.RestoreLast(a.Workspace)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Undo")
	fmt.Fprintf(a.Out, "  Tool             %s\n", emptyAsNone(report.Tool))
	fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	if report.Restored {
		fmt.Fprintf(a.Out, "  Restored         true\n")
		fmt.Fprintf(a.Out, "  Bytes            %d\n", report.Bytes)
	}
	if report.Removed {
		fmt.Fprintf(a.Out, "  Removed          true\n")
	}
	fmt.Fprintf(a.Out, "  Remaining        %d\n", report.Remaining)
	return nil
}

func (a *App) renderPromptHistory(format string, sessionID string, entries []session.PromptEntry, req historyRequest) error {
	report := prompthistory.BuildWithOptions(sessionID, entries, prompthistory.BuildOptions{
		Limit:     req.Limit,
		Offset:    req.Offset,
		UseOffset: req.UseOffset,
	})
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	prompthistory.RenderText(a.Out, report)
	return nil
}

func (a *App) renderStatus(format string, active *session.Session, allowedToolSource string, formatSource string, formatRaw string, formatOverridden bool, configPath string) {
	snapshot := a.statusSnapshotWithOptions(active, statusSnapshotOptions{
		AllowedToolSource: allowedToolSource,
		FormatSource:      formatSource,
		FormatRaw:         formatRaw,
		FormatOverridden:  formatOverridden,
		ConfigPath:        configPath,
	})
	if format == "json" {
		data, _ := json.MarshalIndent(snapshot, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	localstatus.RenderText(a.Out, snapshot)
}

func (a *App) statusSnapshot(active *session.Session) localstatus.Snapshot {
	return a.statusSnapshotWithOptions(active, statusSnapshotOptions{})
}

type statusSnapshotOptions struct {
	AllowedToolSource string
	FormatSource      string
	FormatRaw         string
	FormatOverridden  bool
	ConfigPath        string
}

func (a *App) statusSnapshotWithOptions(active *session.Session, opts statusSnapshotOptions) localstatus.Snapshot {
	sessionCount := -1
	sessionNamespacePath := ""
	sessionWorkspace := ""
	sessionWorkspaceFingerprint := ""
	if a.Sessions != nil {
		sessionNamespacePath = a.Sessions.Dir
		sessionWorkspace = a.Sessions.Workspace
		if strings.TrimSpace(sessionWorkspace) != "" {
			sessionWorkspaceFingerprint = session.WorkspaceFingerprint(sessionWorkspace)
		}
		sessions, err := a.Sessions.List()
		if err == nil {
			sessionCount = len(sessions)
		}
	}
	var sessionID, sessionPath string
	var sessionMessages int
	var sessionMetadata session.SessionMetadata
	if active != nil {
		sessionID = active.ID
		sessionPath = active.Path
		sessionMessages = len(active.Messages)
		sessionMetadata = active.Metadata
	}
	var toolNames []string
	if a.Tools != nil {
		for _, def := range a.Tools.Definitions() {
			toolNames = append(toolNames, def.Name)
		}
	}
	memoryStatuses := buildMemoryFileStatuses(a.Workspace, a.memoryRulesImportOptions())
	gitRaw, gitErr := gitops.Status(a.Workspace)
	gitError := ""
	if gitErr != nil {
		gitError = gitErr.Error()
	}
	var gitFreshness *gitops.BranchFreshness
	var gitIdentity *gitops.Identity
	var gitBaseCommit *gitops.BaseCommitCheck
	var gitOperation *gitops.Operation
	if gitErr == nil {
		if freshness, err := gitops.CheckBranchFreshness(a.Workspace, "", "main"); err == nil {
			gitFreshness = &freshness
		}
		if identity, err := gitops.InspectIdentity(a.Workspace); err == nil {
			gitIdentity = &identity
		}
		if check, err := gitops.CheckBaseCommitForWorkspace(a.Workspace, ""); err == nil {
			gitBaseCommit = &check
		}
		if operation, err := gitops.InspectOperation(a.Workspace); err == nil {
			gitOperation = operation
		}
	}
	var laneBoard *background.LaneBoard
	laneBoardError := ""
	if board, err := background.NewStore(a.Config.ConfigHome).LaneBoard(30 * time.Second); err == nil {
		laneBoard = &board
	} else {
		laneBoardError = err.Error()
	}
	sandboxStatus := sandbox.Detect()
	executable := ""
	if path, err := os.Executable(); err == nil {
		executable = path
	}
	planState, _ := planmode.Load(a.Workspace)
	configValidation := buildStatusConfigValidation(configInspectionFallbackPaths(a.Config.ConfigHome, opts.ConfigPath))
	mcpValidation := buildMCPValidation(a.Config.MCPServers)
	runtimeHooks := a.Config.Hooks
	if a.Config.EffectiveDisableAllHooks() {
		runtimeHooks = config.HookConfig{}
	}
	hookValidation := buildHookValidation(runtimeHooks)
	installedPluginCount := 0
	pluginLoadError := ""
	if manifests, err := a.runtimePluginManifests(); err == nil {
		installedPluginCount = len(manifests)
	} else {
		pluginLoadError = err.Error()
	}
	return localstatus.Build(localstatus.Options{
		Version:                     version,
		FormatSource:                opts.FormatSource,
		FormatRaw:                   opts.FormatRaw,
		FormatOverridden:            opts.FormatOverridden,
		ConfigLoadError:             a.ConfigLoadError,
		ConfigLoadErrorKind:         a.ConfigLoadErrorKind,
		Workspace:                   a.Workspace,
		ConfigHome:                  a.Config.ConfigHome,
		Model:                       a.Config.Model,
		ModelEnvVar:                 a.Config.ModelEnvVar,
		RuntimeProvider:             a.Config.RuntimeProvider,
		RuntimeProviderSource:       a.Config.RuntimeProviderSource,
		FastMode:                    fastModeEnabled(a.Config.FastMode),
		BaseURL:                     a.Config.BaseURL,
		PermissionMode:              a.Config.PermissionMode,
		PermissionModeRaw:           a.Config.PermissionModeRaw,
		PermissionModeSource:        a.Config.PermissionModeSource,
		PermissionModeEnvVar:        a.Config.PermissionModeEnvVar,
		PermissionRules:             a.Config.PermissionRules,
		MaxTokens:                   a.Config.MaxTokens,
		MaxTurns:                    a.Config.MaxTurns,
		AutoCompactMessages:         a.Config.AutoCompactMessages,
		APIKey:                      a.Config.APIKey,
		AuthToken:                   a.Config.AuthToken,
		AuthConfigured:              a.Config.APIKey != "" || a.Config.AuthToken != "",
		MCPServerCount:              len(a.Config.MCPServers),
		InstalledPluginCount:        installedPluginCount,
		PluginLoadError:             pluginLoadError,
		TrustedRoots:                append([]string(nil), a.Config.TrustedRoots...),
		UserPromptSubmitHookCount:   len(runtimeHooks.UserPromptSubmit),
		SessionStartHookCount:       len(runtimeHooks.SessionStart),
		SessionEndHookCount:         len(runtimeHooks.SessionEnd),
		SetupHookCount:              len(runtimeHooks.Setup),
		PreHookCount:                len(runtimeHooks.PreToolUse),
		PostHookCount:               len(runtimeHooks.PostToolUse),
		PostFailureHookCount:        len(runtimeHooks.PostToolUseFailure),
		PermissionRequestHookCount:  len(runtimeHooks.PermissionRequest),
		PermissionDeniedHookCount:   len(runtimeHooks.PermissionDenied),
		StopHookCount:               len(runtimeHooks.Stop),
		StopFailureHookCount:        len(runtimeHooks.StopFailure),
		PreCompactHookCount:         len(runtimeHooks.PreCompact),
		PostCompactHookCount:        len(runtimeHooks.PostCompact),
		NotificationHookCount:       len(runtimeHooks.Notification),
		SubagentStartHookCount:      len(runtimeHooks.SubagentStart),
		SubagentStopHookCount:       len(runtimeHooks.SubagentStop),
		WorktreeCreateHookCount:     len(runtimeHooks.WorktreeCreate),
		WorktreeRemoveHookCount:     len(runtimeHooks.WorktreeRemove),
		CwdChangedHookCount:         len(runtimeHooks.CwdChanged),
		TaskCreatedHookCount:        len(runtimeHooks.TaskCreated),
		TaskCompletedHookCount:      len(runtimeHooks.TaskCompleted),
		InstructionsLoadedHookCount: len(runtimeHooks.InstructionsLoaded),
		FileChangedHookCount:        len(runtimeHooks.FileChanged),
		EnabledSkillCount:           len(a.Config.EnabledSkills),
		ConfigValidation:            configValidation,
		MCPValidation:               mcpValidation,
		HookValidation:              hookValidation,
		PlanActive:                  planState.Active,
		PlanText:                    planState.Plan,
		PlanUpdatedAt:               planState.UpdatedAt,
		MemoryFiles:                 memoryStatuses,
		ToolNames:                   toolNames,
		AllowedToolSource:           opts.AllowedToolSource,
		AllowedToolEntries:          append([]string(nil), a.Config.PermissionRules.Allow...),
		ToolAliases:                 tools.ClaudeToolAliases(),
		SessionID:                   sessionID,
		SessionPath:                 sessionPath,
		SessionNamespacePath:        sessionNamespacePath,
		SessionWorkspace:            sessionWorkspace,
		SessionWorkspaceFingerprint: sessionWorkspaceFingerprint,
		SessionMessages:             sessionMessages,
		SessionCount:                sessionCount,
		SessionCreatedAtMS:          timeMillis(sessionMetadata.CreatedAt),
		SessionUpdatedAtMS:          timeMillis(sessionMetadata.UpdatedAt),
		SessionModifiedEpochMillis:  timeMillis(sessionMetadata.ModifiedAt),
		SessionParentSessionID:      sessionMetadata.ParentSessionID,
		SessionBranchName:           sessionMetadata.BranchName,
		GitStatus:                   gitRaw,
		GitError:                    gitError,
		GitFreshness:                gitFreshness,
		GitIdentity:                 gitIdentity,
		GitBaseCommit:               gitBaseCommit,
		GitOperation:                gitOperation,
		LaneBoard:                   laneBoard,
		LaneBoardError:              laneBoardError,
		SandboxOS:                   sandboxStatus.OS,
		SandboxDefault:              sandboxStatus.Default,
		SandboxStrategies:           sandboxStatus.Strategies,
		SandboxAvailable:            sandboxStatus.Available,
		Executable:                  executable,
	})
}

func buildMemoryFileStatuses(workspace string, rulesImport memory.RulesImportOptions) []localstatus.MemoryFileStatus {
	memoryFiles, err := memory.DiscoverWithRulesImport(workspace, rulesImport)
	if err != nil {
		return nil
	}
	memoryMetadata := memory.MetadataFor(workspace, memoryFiles)
	memoryStatuses := make([]localstatus.MemoryFileStatus, 0, len(memoryMetadata))
	for _, file := range memoryMetadata {
		memoryStatuses = append(memoryStatuses, localstatus.MemoryFileStatus{
			Path:           file.Path,
			Name:           file.Name,
			Source:         file.Source,
			Origin:         file.Origin,
			Scope:          file.Scope,
			ScopePath:      file.ScopePath,
			OutsideProject: file.OutsideProject,
			Chars:          file.Chars,
			Lines:          file.Lines,
			Words:          file.Words,
			SizeBytes:      file.SizeBytes,
			ModifiedAt:     file.ModifiedAt,
			AgeSeconds:     file.AgeSeconds,
			Empty:          file.Empty,
			Contributes:    file.Contributes,
			Truncated:      file.Truncated,
		})
	}
	return memoryStatuses
}

func buildMCPValidation(servers map[string]config.MCPServerConfig) localstatus.MCPValidationStatus {
	status := localstatus.MCPValidationStatus{TotalConfigured: len(servers)}
	if len(servers) == 0 {
		return status
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := servers[name]
		if server.Required {
			status.RequiredCount++
		} else {
			status.OptionalCount++
		}
		if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
			status.InvalidServers = append(status.InvalidServers, localstatus.ValidationIssue{
				Name:       name,
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command or url",
				Valid:      false,
			})
			continue
		}
		status.ValidCount++
	}
	status.InvalidCount = len(status.InvalidServers)
	return status
}

func buildStatusConfigValidation(paths []string) localstatus.ConfigValidationStatus {
	report := configvalidate.ValidateFiles(paths)
	return localstatus.ConfigValidationStatus{
		Status:       report.Status,
		FileCount:    report.FileCount,
		PresentCount: report.PresentCount,
		ErrorCount:   report.ErrorCount,
		WarningCount: report.WarningCount,
		Paths:        append([]string(nil), report.Paths...),
	}
}

type hookValidationGroup struct {
	Event    string
	Entries  []config.HookCommand
	Fallback []string
}

func buildHookValidation(cfg config.HookConfig) localstatus.HookValidationStatus {
	status := localstatus.HookValidationStatus{}
	for _, group := range hookValidationGroups(cfg) {
		if len(group.Entries) != 0 {
			for i, hook := range group.Entries {
				if issue, ok := validateHookCommand(group.Event, i, hook); ok {
					status.InvalidHooks = append(status.InvalidHooks, issue)
					continue
				}
				status.ValidCount++
			}
			continue
		}
		for i, command := range group.Fallback {
			hook := config.HookCommand{Type: "command", Command: command}
			if issue, ok := validateHookCommand(group.Event, i, hook); ok {
				status.InvalidHooks = append(status.InvalidHooks, issue)
				continue
			}
			status.ValidCount++
		}
	}
	status.InvalidCount = len(status.InvalidHooks)
	return status
}

func hookValidationGroups(cfg config.HookConfig) []hookValidationGroup {
	return []hookValidationGroup{
		{Event: "pre_tool_use", Entries: cfg.PreToolUseCommands, Fallback: cfg.PreToolUse},
		{Event: "post_tool_use", Entries: cfg.PostToolUseCommands, Fallback: cfg.PostToolUse},
		{Event: "post_tool_use_failure", Entries: cfg.PostToolUseFailureCommands, Fallback: cfg.PostToolUseFailure},
		{Event: "permission_request", Entries: cfg.PermissionRequestCommands, Fallback: cfg.PermissionRequest},
		{Event: "permission_denied", Entries: cfg.PermissionDeniedCommands, Fallback: cfg.PermissionDenied},
		{Event: "user_prompt_submit", Entries: cfg.UserPromptSubmitCommands, Fallback: cfg.UserPromptSubmit},
		{Event: "session_start", Entries: cfg.SessionStartCommands, Fallback: cfg.SessionStart},
		{Event: "session_end", Entries: cfg.SessionEndCommands, Fallback: cfg.SessionEnd},
		{Event: "setup", Entries: cfg.SetupCommands, Fallback: cfg.Setup},
		{Event: "stop", Entries: cfg.StopCommands, Fallback: cfg.Stop},
		{Event: "stop_failure", Entries: cfg.StopFailureCommands, Fallback: cfg.StopFailure},
		{Event: "pre_compact", Entries: cfg.PreCompactCommands, Fallback: cfg.PreCompact},
		{Event: "post_compact", Entries: cfg.PostCompactCommands, Fallback: cfg.PostCompact},
		{Event: "notification", Entries: cfg.NotificationCommands, Fallback: cfg.Notification},
		{Event: "subagent_start", Entries: cfg.SubagentStartCommands, Fallback: cfg.SubagentStart},
		{Event: "subagent_stop", Entries: cfg.SubagentStopCommands, Fallback: cfg.SubagentStop},
		{Event: "worktree_create", Entries: cfg.WorktreeCreateCommands, Fallback: cfg.WorktreeCreate},
		{Event: "worktree_remove", Entries: cfg.WorktreeRemoveCommands, Fallback: cfg.WorktreeRemove},
		{Event: "cwd_changed", Entries: cfg.CwdChangedCommands, Fallback: cfg.CwdChanged},
		{Event: "task_created", Entries: cfg.TaskCreatedCommands, Fallback: cfg.TaskCreated},
		{Event: "task_completed", Entries: cfg.TaskCompletedCommands, Fallback: cfg.TaskCompleted},
		{Event: "instructions_loaded", Entries: cfg.InstructionsLoadedCommands, Fallback: cfg.InstructionsLoaded},
		{Event: "file_changed", Entries: cfg.FileChangedCommands, Fallback: cfg.FileChanged},
	}
}

func validateHookCommand(event string, index int, hook config.HookCommand) (localstatus.ValidationIssue, bool) {
	if strings.TrimSpace(hook.InvalidKind) != "" || strings.TrimSpace(hook.InvalidReason) != "" {
		return hookValidationIssue(
			event,
			index,
			hook,
			firstNonEmpty(strings.TrimSpace(hook.InvalidKind), "invalid_hooks_config"),
			firstNonEmpty(strings.TrimSpace(hook.InvalidField), "entry"),
			firstNonEmpty(strings.TrimSpace(hook.InvalidReason), "invalid hook configuration"),
		), true
	}
	typ := strings.ToLower(strings.TrimSpace(hook.Type))
	if typ == "" {
		typ = "command"
	}
	display := strings.TrimSpace(config.HookCommandDisplay(hook))
	switch typ {
	case "command":
		if strings.TrimSpace(hook.Command) == "" {
			return hookValidationIssue(event, index, hook, "missing_command", "command", "missing command"), true
		}
	case "http":
		if strings.TrimSpace(hook.URL) == "" {
			return hookValidationIssue(event, index, hook, "missing_url", "url", "missing url"), true
		}
	case "prompt", "agent":
		if strings.TrimSpace(hook.Prompt) == "" {
			return hookValidationIssue(event, index, hook, "missing_prompt", "prompt", "missing prompt"), true
		}
	default:
		return hookValidationIssue(event, index, hook, "unsupported_type", "type", "unsupported hook type "+typ), true
	}
	if display == "" {
		return hookValidationIssue(event, index, hook, "missing_display", "command", "missing hook target"), true
	}
	return localstatus.ValidationIssue{}, false
}

func hookValidationIssue(event string, index int, hook config.HookCommand, kind string, field string, reason string) localstatus.ValidationIssue {
	i := index
	return localstatus.ValidationIssue{
		Event:      event,
		Index:      &i,
		HookIndex:  &i,
		Kind:       kind,
		ErrorField: field,
		Reason:     reason,
		Command:    config.HookCommandDisplay(hook),
		Matcher:    strings.TrimSpace(hook.Matcher),
		Valid:      false,
	}
}

type capabilitiesReport struct {
	Kind                    string                     `json:"kind"`
	Action                  string                     `json:"action"`
	Status                  string                     `json:"status"`
	Version                 string                     `json:"version"`
	Workspace               string                     `json:"workspace"`
	Model                   string                     `json:"model"`
	PermissionMode          string                     `json:"permission_mode"`
	CommandCount            int                        `json:"command_count"`
	Commands                []string                   `json:"commands"`
	SlashCommandCount       int                        `json:"slash_command_count"`
	SlashCommands           []capabilitySlash          `json:"slash_commands"`
	ResumeSafeSlashCount    int                        `json:"resume_safe_slash_count"`
	ResumeSafeSlashCommands []string                   `json:"resume_safe_slash_commands"`
	ToolCount               int                        `json:"tool_count"`
	Tools                   []capabilityTool           `json:"tools"`
	ToolAliasCount          int                        `json:"tool_alias_count"`
	ToolAliases             map[string]string          `json:"tool_aliases,omitempty"`
	MCP                     capabilityMCP              `json:"mcp"`
	MockParity              harness.Manifest           `json:"mock_parity"`
	Terminal                terminalparity.Report      `json:"terminal"`
	Bridge                  bridgeparity.Report        `json:"bridge"`
	Orchestration           orchestrationparity.Report `json:"orchestration"`
	Release                 releaseparity.Report       `json:"release"`
	CommandSurface          commandSurfaceReport       `json:"command_surface"`
	Features                []string                   `json:"features"`
	Protocols               []string                   `json:"protocols"`
	OutputFormats           []string                   `json:"output_formats"`
}

type commandSurfaceReport struct {
	Status                         string   `json:"status"`
	CommandCount                   int      `json:"command_count"`
	HelpTopicCount                 int      `json:"help_topic_count"`
	MissingHelpTopicCount          int      `json:"missing_help_topic_count"`
	MissingHelpTopics              []string `json:"missing_help_topics,omitempty"`
	FallbackHelpTopicCount         int      `json:"fallback_help_topic_count"`
	FallbackHelpTopics             []string `json:"fallback_help_topics,omitempty"`
	CompletionCommandCount         int      `json:"completion_command_count"`
	MissingCompletionCommandCount  int      `json:"missing_completion_command_count"`
	MissingCompletionCommands      []string `json:"missing_completion_commands,omitempty"`
	GlobalOutputFormatCommandCount int      `json:"global_output_format_command_count"`
	NoGlobalOutputFormatCount      int      `json:"no_global_output_format_count"`
	NoGlobalOutputFormatCommands   []string `json:"no_global_output_format_commands,omitempty"`
}

type capabilityResolveReport struct {
	Kind        string                    `json:"kind"`
	Action      string                    `json:"action"`
	Status      string                    `json:"status"`
	Query       string                    `json:"query"`
	MatchCount  int                       `json:"match_count"`
	Matches     []capabilityRegistryMatch `json:"matches"`
	Suggestions []string                  `json:"suggestions,omitempty"`
}

type capabilityRegistryMatch struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Canonical       string   `json:"canonical,omitempty"`
	Usage           string   `json:"usage,omitempty"`
	Description     string   `json:"description,omitempty"`
	Permission      string   `json:"permission,omitempty"`
	Aliases         []string `json:"aliases,omitempty"`
	ResumeSupported bool     `json:"resume_supported,omitempty"`
	ExposedOverMCP  bool     `json:"exposed_over_mcp,omitempty"`
	SourceHint      string   `json:"source_hint,omitempty"`
}

type capabilitySlash struct {
	Name            string `json:"name"`
	Usage           string `json:"usage"`
	Description     string `json:"description"`
	ResumeSupported bool   `json:"resume_supported"`
	Hidden          bool   `json:"hidden,omitempty"`
	Disabled        bool   `json:"disabled,omitempty"`
}

type capabilityTool struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Permission     string         `json:"permission"`
	Aliases        []string       `json:"aliases,omitempty"`
	ExposedOverMCP bool           `json:"exposed_over_mcp"`
	InputSchema    map[string]any `json:"input_schema,omitempty"`
}

type referenceParityAuditReport struct {
	Kind     string                 `json:"kind"`
	Action   string                 `json:"action"`
	Status   string                 `json:"status"`
	Source   string                 `json:"source,omitempty"`
	Commands *referenceSurfaceAudit `json:"commands,omitempty"`
	Tools    *referenceSurfaceAudit `json:"tools,omitempty"`
}

type referenceSurfaceAudit struct {
	Kind              string                 `json:"kind"`
	SnapshotPath      string                 `json:"snapshot_path"`
	SourceRoot        string                 `json:"source_root,omitempty"`
	ReferenceCount    int                    `json:"reference_count"`
	CoveredCount      int                    `json:"covered_count"`
	GroupCoveredCount int                    `json:"group_covered_count"`
	UncoveredCount    int                    `json:"uncovered_count"`
	MissingCount      int                    `json:"missing_count"`
	MissingGroups     []referenceAuditGroup  `json:"missing_groups,omitempty"`
	Covered           []referenceAuditMatch  `json:"covered,omitempty"`
	GroupCovered      []referenceAuditMatch  `json:"group_covered,omitempty"`
	Missing           []referenceSnapshotRef `json:"missing,omitempty"`
}

type referenceAuditGroup struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type referenceAuditMatch struct {
	Name       string `json:"name"`
	SourceHint string `json:"source_hint,omitempty"`
	Matched    string `json:"matched"`
}

type referenceSnapshotRef struct {
	Name           string `json:"name"`
	SourceHint     string `json:"source_hint,omitempty"`
	Responsibility string `json:"responsibility,omitempty"`
}

type capabilityMCP struct {
	ConfiguredServerCount  int              `json:"configured_server_count"`
	ConfiguredServers      []string         `json:"configured_servers"`
	LocalResourceCount     int              `json:"local_resource_count"`
	LocalResources         []map[string]any `json:"local_resources"`
	LocalTemplateCount     int              `json:"local_resource_template_count"`
	LocalResourceTemplates []map[string]any `json:"local_resource_templates"`
	LocalPromptCount       int              `json:"local_prompt_count"`
	LocalPrompts           []map[string]any `json:"local_prompts"`
	ExposedToolCount       int              `json:"exposed_tool_count"`
}

func (a *App) Capabilities(args []string) error {
	req, err := parseCapabilitiesArgs(args)
	if err != nil {
		return err
	}
	if req.Action == "audit" {
		report, err := a.referenceParityAuditReport(req)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderReferenceParityAuditText(a.Out, report)
		return nil
	}
	if req.Action == "resolve" {
		report := a.capabilityResolveReport(req.Query)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderCapabilityResolveText(a.Out, report)
		return nil
	}
	report := a.capabilitiesReport()
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCapabilitiesText(a.Out, report)
	return nil
}

type capabilitiesRequest struct {
	Action          string
	Query           string
	Format          string
	CommandSnapshot string
	ToolSnapshot    string
}

func parseCapabilitiesArgs(args []string) (capabilitiesRequest, error) {
	const usage = "codog capabilities [show|list|resolve NAME|audit] [--commands-snapshot PATH] [--tools-snapshot PATH] [--json|--output-format text|json]"
	parser := capabilitiesArgParser{
		req:   capabilitiesRequest{Action: "show", Format: "text"},
		usage: usage,
	}
	for index := 0; index < len(args); index++ {
		if err := parser.consume(args, &index); err != nil {
			return parser.req, err
		}
	}
	return parser.finish()
}

type capabilitiesArgParser struct {
	req         capabilitiesRequest
	positionals []string
	usage       string
}

func (p *capabilitiesArgParser) consume(args []string, index *int) error {
	arg := strings.TrimSpace(args[*index])
	switch {
	case arg == "":
	case arg == "--json":
		p.req.Format = "json"
	case arg == "--output-format" || arg == "-o":
		return p.consumeValue(args, index, arg, "capabilities", func(value string) { p.req.Format = value })
	case strings.HasPrefix(arg, "--output-format="):
		p.req.Format = strings.TrimPrefix(arg, "--output-format=")
	case arg == "--commands-snapshot" || arg == "--command-snapshot":
		return p.consumeValue(args, index, arg, "capabilities audit", func(value string) { p.req.CommandSnapshot = value })
	case strings.HasPrefix(arg, "--commands-snapshot="):
		p.req.CommandSnapshot = strings.TrimPrefix(arg, "--commands-snapshot=")
	case strings.HasPrefix(arg, "--command-snapshot="):
		p.req.CommandSnapshot = strings.TrimPrefix(arg, "--command-snapshot=")
	case arg == "--tools-snapshot" || arg == "--tool-snapshot":
		return p.consumeValue(args, index, arg, "capabilities audit", func(value string) { p.req.ToolSnapshot = value })
	case strings.HasPrefix(arg, "--tools-snapshot="):
		p.req.ToolSnapshot = strings.TrimPrefix(arg, "--tools-snapshot=")
	case strings.HasPrefix(arg, "--tool-snapshot="):
		p.req.ToolSnapshot = strings.TrimPrefix(arg, "--tool-snapshot=")
	case strings.HasPrefix(arg, "-"):
		return unknownOptionError{Command: "capabilities", Option: arg, Usage: p.usage}
	default:
		p.positionals = append(p.positionals, arg)
	}
	return nil
}

func (p *capabilitiesArgParser) consumeValue(args []string, index *int, flag string, command string, apply func(string)) error {
	*index++
	if *index >= len(args) {
		return missingFlagValueError{Command: command, Flag: flag, Usage: p.usage}
	}
	apply(args[*index])
	return nil
}

func (p *capabilitiesArgParser) finish() (capabilitiesRequest, error) {
	if err := validateTextOrJSON(p.req.Format, "capabilities"); err != nil {
		return p.req, err
	}
	if len(p.positionals) == 0 {
		return p.req, nil
	}
	return p.parseAction()
}

func (p *capabilitiesArgParser) parseAction() (capabilitiesRequest, error) {
	action := strings.ToLower(p.positionals[0])
	switch action {
	case "show", "list":
		if len(p.positionals) > 1 {
			return p.req, unexpectedExtraArgsError{Command: "capabilities " + action, Args: p.positionals[1:], Usage: p.usage}
		}
		p.req.Action = "show"
	case "resolve", "lookup", "find":
		if len(p.positionals) < 2 {
			return p.req, requiredArgumentError{Command: "capabilities resolve", Argument: "NAME", Usage: p.usage}
		}
		if len(p.positionals) > 2 {
			return p.req, unexpectedExtraArgsError{Command: "capabilities resolve", Args: p.positionals[2:], Usage: p.usage}
		}
		p.req.Action = "resolve"
		p.req.Query = p.positionals[1]
	case "audit":
		if len(p.positionals) > 1 {
			return p.req, unexpectedExtraArgsError{Command: "capabilities audit", Args: p.positionals[1:], Usage: p.usage}
		}
		p.req.Action = "audit"
	default:
		return p.req, unknownOptionError{Command: "capabilities", Option: p.positionals[0], Usage: p.usage}
	}
	p.req.CommandSnapshot = strings.TrimSpace(p.req.CommandSnapshot)
	p.req.ToolSnapshot = strings.TrimSpace(p.req.ToolSnapshot)
	if p.req.Action == "audit" && p.req.CommandSnapshot == "" && p.req.ToolSnapshot == "" {
		return p.req, requiredArgumentError{Command: "capabilities audit", Argument: "--commands-snapshot or --tools-snapshot", Usage: p.usage}
	}
	return p.req, nil
}

func (a *App) capabilitiesReport() capabilitiesReport {
	commands := builtInCommandNames()
	slashCommands := slashCapabilities()
	resumeSafeSlashCommands := slash.ResumeSupportedNames()
	toolInfos := []tools.ToolInfo{}
	if a.Tools != nil {
		toolInfos = a.Tools.Infos()
	}
	exposed := mcpserver.ExposedTools(a.Tools)
	exposedNames := map[string]bool{}
	for _, tool := range exposed {
		if name, ok := tool["name"].(string); ok && name != "" {
			exposedNames[name] = true
		}
	}
	toolAliases := tools.ClaudeToolAliases()
	aliasesByCanonical := capabilityToolAliasesByCanonical(toolAliases, toolInfos)
	capTools := make([]capabilityTool, 0, len(toolInfos))
	for _, info := range toolInfos {
		capTools = append(capTools, capabilityTool{
			Name:           info.Name,
			Description:    info.Description,
			Permission:     string(info.Permission),
			Aliases:        aliasesByCanonical[info.Name],
			ExposedOverMCP: exposedNames[info.Name],
			InputSchema:    info.InputSchema,
		})
	}
	localResources := mcpserver.LocalResources(a.mcpServerOptions())
	localTemplates := mcpserver.LocalResourceTemplates()
	localPrompts := mcpserver.LocalPrompts()
	pluginManifests, _ := a.runtimePluginManifests()
	return capabilitiesReport{
		Kind:                    "capabilities",
		Action:                  "show",
		Status:                  "ok",
		Version:                 version,
		Workspace:               a.Workspace,
		Model:                   a.Config.Model,
		PermissionMode:          a.Config.PermissionMode,
		CommandCount:            len(commands),
		Commands:                commands,
		SlashCommandCount:       len(slashCommands),
		SlashCommands:           slashCommands,
		ResumeSafeSlashCount:    len(resumeSafeSlashCommands),
		ResumeSafeSlashCommands: resumeSafeSlashCommands,
		ToolCount:               len(capTools),
		Tools:                   capTools,
		ToolAliasCount:          len(toolAliases),
		ToolAliases:             toolAliases,
		MCP: capabilityMCP{
			ConfiguredServerCount:  len(a.Config.MCPServers),
			ConfiguredServers:      sortedMCPServerNames(a.Config.MCPServers),
			LocalResourceCount:     len(localResources),
			LocalResources:         localResources,
			LocalTemplateCount:     len(localTemplates),
			LocalResourceTemplates: localTemplates,
			LocalPromptCount:       len(localPrompts),
			LocalPrompts:           localPrompts,
			ExposedToolCount:       len(exposed),
		},
		MockParity: harness.ScenarioManifest(),
		Terminal:   terminalparity.Build(),
		Bridge: bridgeparity.Build(bridgeparity.Options{
			RemoteAuthToken:   a.Config.Future.RemoteAuthToken,
			EditorBridgeToken: a.Config.Future.EditorBridgeToken,
			RemoteEnabled:     a.Config.Future.RemoteEnabled,
		}),
		Orchestration: orchestrationparity.Build(orchestrationparity.Options{
			ConfigHome:      a.Config.ConfigHome,
			Workspace:       a.Workspace,
			MCPServers:      a.Config.MCPServers,
			PluginManifests: pluginManifests,
		}),
		Release: releaseparity.Build(releaseparity.Options{
			SandboxStrategy:           a.Config.Future.SandboxStrategy,
			SandboxEnabled:            configBoolValue(a.Config.Future.Sandbox.Enabled),
			UpdaterManifestURL:        a.Config.Future.UpdaterManifestURL,
			EnterprisePolicyPath:      a.Config.Future.EnterprisePolicy,
			EnterprisePolicyPublicKey: a.Config.Future.EnterprisePolicyPublicKey,
		}),
		CommandSurface: commandSurface(),
		Features:       codogCapabilityFeatures(),
		Protocols:      codogCapabilityProtocols(),
		OutputFormats:  []string{"text", "json", "stream-json"},
	}
}

func (a *App) referenceParityAuditReport(req capabilitiesRequest) (referenceParityAuditReport, error) {
	capabilities := a.capabilitiesReport()
	report := referenceParityAuditReport{
		Kind:   "capabilities",
		Action: "audit",
		Status: "ok",
	}
	if req.CommandSnapshot != "" {
		audit, err := auditReferenceSurface(req.CommandSnapshot, "commands", commandCapabilityNames(capabilities))
		if err != nil {
			return referenceParityAuditReport{}, err
		}
		report.Commands = &audit
	}
	if req.ToolSnapshot != "" {
		audit, err := auditReferenceSurface(req.ToolSnapshot, "tools", toolCapabilityNames(capabilities))
		if err != nil {
			return referenceParityAuditReport{}, err
		}
		report.Tools = &audit
	}
	if (report.Commands != nil && report.Commands.UncoveredCount > 0) || (report.Tools != nil && report.Tools.UncoveredCount > 0) {
		report.Status = "gap"
	}
	return report, nil
}

func auditReferenceSurface(path string, kind string, available map[string]string) (referenceSurfaceAudit, error) {
	entries, err := readReferenceSnapshot(path)
	if err != nil {
		return referenceSurfaceAudit{}, err
	}
	audit := auditReferenceEntries(entries, kind, available)
	audit.SnapshotPath = path
	return audit, nil
}

func auditReferenceEntries(entries []referenceSnapshotRef, kind string, available map[string]string) referenceSurfaceAudit {
	audit := referenceSurfaceAudit{
		Kind:           kind,
		ReferenceCount: len(entries),
	}
	coveredGroups := map[string]string{}
	sourceFallbacks := referenceSourceFallbackMatches(available)
	for _, entry := range entries {
		if matched, ok := available[normalizeReferenceName(entry.Name)]; ok {
			audit.Covered = append(audit.Covered, referenceAuditMatch{
				Name:       entry.Name,
				SourceHint: entry.SourceHint,
				Matched:    matched,
			})
			group := referenceSourceGroup(entry.SourceHint)
			if group != "" && group != "unknown" {
				coveredGroups[group] = matched
			}
			continue
		}
		if matched, ok := referenceSourceFallbackMatch(entry.SourceHint, sourceFallbacks); ok {
			coveredGroups[referenceSourceGroup(entry.SourceHint)] = matched
			audit.GroupCovered = append(audit.GroupCovered, referenceAuditMatch{
				Name:       entry.Name,
				SourceHint: entry.SourceHint,
				Matched:    matched,
			})
		}
	}
	for _, entry := range entries {
		if _, ok := available[normalizeReferenceName(entry.Name)]; ok {
			continue
		}
		if _, ok := referenceSourceFallbackMatch(entry.SourceHint, sourceFallbacks); ok {
			continue
		}
		if matched, ok := coveredGroups[referenceSourceGroup(entry.SourceHint)]; ok {
			audit.GroupCovered = append(audit.GroupCovered, referenceAuditMatch{
				Name:       entry.Name,
				SourceHint: entry.SourceHint,
				Matched:    matched,
			})
			continue
		}
		audit.Missing = append(audit.Missing, entry)
	}
	audit.CoveredCount = len(audit.Covered)
	audit.GroupCoveredCount = len(audit.GroupCovered)
	audit.UncoveredCount = len(audit.Missing)
	audit.MissingCount = audit.UncoveredCount
	audit.MissingGroups = referenceMissingGroups(audit.Missing)
	return audit
}

func readReferenceSnapshot(path string) ([]referenceSnapshotRef, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read reference snapshot: %w", err)
	}
	var entries []referenceSnapshotRef
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("read reference snapshot: %w", err)
	}
	return entries, nil
}

func commandCapabilityNames(report capabilitiesReport) map[string]string {
	names := map[string]string{}
	add := func(name string) {
		normalized := normalizeReferenceName(name)
		if normalized != "" {
			names[normalized] = name
		}
	}
	for _, command := range report.Commands {
		add(command)
	}
	for _, command := range report.SlashCommands {
		add(command.Name)
		add(strings.TrimPrefix(command.Name, "/"))
	}
	return names
}

func toolCapabilityNames(report capabilitiesReport) map[string]string {
	names := map[string]string{}
	add := func(name string, matched string) {
		normalized := normalizeReferenceName(name)
		if normalized != "" {
			names[normalized] = matched
		}
	}
	for _, tool := range report.Tools {
		add(tool.Name, tool.Name)
		for _, alias := range tool.Aliases {
			add(alias, tool.Name)
		}
	}
	for alias, canonical := range report.ToolAliases {
		add(alias, canonical)
	}
	return names
}

func normalizeReferenceName(name string) string {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
	var builder strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func referenceSourceFallbackMatches(available map[string]string) map[string]string {
	fallbacks := map[string]string{
		"tools/REPLTool":                       "REPLTool",
		"tools/SleepTool":                      "SleepTool",
		"tools/shared/gitOperationTracking.ts": "GitStatusTool",
		"tools/shared/spawnMultiAgent.ts":      "TeamCreateTool",
		"tools/utils.ts":                       "ToolSearchTool",
	}
	out := map[string]string{}
	for source, alias := range fallbacks {
		if matched, ok := available[normalizeReferenceName(alias)]; ok {
			out[source] = matched
		}
	}
	return out
}

func referenceSourceKey(sourceHint string) string {
	return strings.Trim(strings.TrimSpace(sourceHint), "/")
}

func referenceSourceFallbackMatch(sourceHint string, fallbacks map[string]string) (string, bool) {
	if matched, ok := fallbacks[referenceSourceKey(sourceHint)]; ok {
		return matched, true
	}
	matched, ok := fallbacks[referenceSourceGroup(sourceHint)]
	return matched, ok
}

func referenceMissingGroups(entries []referenceSnapshotRef) []referenceAuditGroup {
	counts := map[string]int{}
	for _, entry := range entries {
		source := referenceSourceGroup(entry.SourceHint)
		counts[source]++
	}
	groups := make([]referenceAuditGroup, 0, len(counts))
	for source, count := range counts {
		groups = append(groups, referenceAuditGroup{Source: source, Count: count})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Source < groups[j].Source
	})
	return groups
}

func referenceSourceGroup(sourceHint string) string {
	sourceHint = strings.Trim(strings.TrimSpace(sourceHint), "/")
	if sourceHint == "" {
		return "unknown"
	}
	parts := strings.Split(sourceHint, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func (a *App) capabilityResolveReport(query string) capabilityResolveReport {
	query = strings.TrimSpace(query)
	matches := a.capabilityRegistryMatches(query)
	report := capabilityResolveReport{
		Kind:       "capabilities",
		Action:     "resolve",
		Status:     "ok",
		Query:      query,
		Matches:    matches,
		MatchCount: len(matches),
	}
	if len(matches) == 0 {
		report.Status = "not_found"
		report.Suggestions = a.capabilityRegistrySuggestions(query, 8)
	}
	return report
}

func (a *App) capabilityRegistryMatches(query string) []capabilityRegistryMatch {
	normalized := strings.ToLower(strings.TrimSpace(query))
	normalizedNoSlash := strings.TrimPrefix(normalized, "/")
	if normalized == "" {
		return nil
	}
	matches := []capabilityRegistryMatch{}
	seen := map[string]bool{}
	add := func(match capabilityRegistryMatch) {
		key := match.Kind + "\x00" + match.Name + "\x00" + match.Canonical
		if seen[key] {
			return
		}
		seen[key] = true
		matches = append(matches, match)
	}
	for _, command := range builtInCommandNames() {
		if strings.EqualFold(command, query) {
			match := capabilityRegistryMatch{
				Kind:       "command",
				Name:       command,
				Canonical:  command,
				SourceHint: "built_in_command",
			}
			if spec, ok := commandHelpSpecFor(command); ok {
				match.Usage = spec.Usage
				match.Description = firstHelpParagraph(spec.Text)
				match.Aliases = append([]string(nil), spec.Aliases...)
			}
			add(match)
		}
	}
	for _, spec := range slash.Specs() {
		slashName := strings.ToLower(strings.TrimSpace(spec.Name))
		if slashName == normalized || strings.TrimPrefix(slashName, "/") == normalizedNoSlash {
			add(capabilityRegistryMatch{
				Kind:            "slash",
				Name:            spec.Name,
				Canonical:       spec.Name,
				Usage:           spec.Usage,
				Description:     spec.Description,
				ResumeSupported: spec.ResumeSupported,
				SourceHint:      "slash_command",
			})
		}
	}
	registry := a.activeToolRegistry()
	exposed := capabilityExposedToolNames(registry)
	if registry != nil {
		if info, ok := registry.Info(query); ok {
			kind := "tool"
			canonical := info.Name
			if !strings.EqualFold(query, info.Name) {
				kind = "tool_alias"
			}
			add(capabilityRegistryMatch{
				Kind:           kind,
				Name:           query,
				Canonical:      canonical,
				Description:    info.Description,
				Permission:     string(info.Permission),
				Aliases:        toolDetailAliases(info.Name),
				ExposedOverMCP: exposed[info.Name],
				SourceHint:     "tool_registry",
			})
		}
	}
	for alias, canonical := range tools.ClaudeToolAliases() {
		if !strings.EqualFold(alias, query) {
			continue
		}
		match := capabilityRegistryMatch{
			Kind:       "tool_alias",
			Name:       alias,
			Canonical:  canonical,
			SourceHint: "claude_tool_alias",
		}
		if registry != nil {
			if info, ok := registry.Info(canonical); ok {
				match.Description = info.Description
				match.Permission = string(info.Permission)
				match.Aliases = toolDetailAliases(info.Name)
				match.ExposedOverMCP = exposed[info.Name]
			}
		}
		add(match)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Kind != matches[j].Kind {
			return capabilityMatchKindRank(matches[i].Kind) < capabilityMatchKindRank(matches[j].Kind)
		}
		return matches[i].Name < matches[j].Name
	})
	return matches
}

func capabilityMatchKindRank(kind string) int {
	switch kind {
	case "command":
		return 0
	case "slash":
		return 1
	case "tool":
		return 2
	case "tool_alias":
		return 3
	default:
		return 9
	}
}

func capabilityExposedToolNames(registry *tools.Registry) map[string]bool {
	out := map[string]bool{}
	for _, tool := range mcpserver.ExposedTools(registry) {
		if name, ok := tool["name"].(string); ok && name != "" {
			out[name] = true
		}
	}
	return out
}

func (a *App) capabilityRegistrySuggestions(query string, limit int) []string {
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "/")))
	if query == "" || limit <= 0 {
		return nil
	}
	candidates := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			candidates = append(candidates, value)
		}
	}
	for _, command := range builtInCommandNames() {
		add(command)
	}
	for _, spec := range slash.Specs() {
		add(spec.Name)
	}
	if registry := a.activeToolRegistry(); registry != nil {
		for _, info := range registry.Infos() {
			add(info.Name)
		}
	}
	for alias := range tools.ClaudeToolAliases() {
		add(alias)
	}
	type rankedSuggestion struct {
		score int
		name  string
	}
	seen := map[string]bool{}
	ranked := []rankedSuggestion{}
	for _, candidate := range candidates {
		key := strings.ToLower(candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized := strings.ToLower(strings.TrimPrefix(candidate, "/"))
		score := levenshteinDistance(normalized, query)
		if strings.HasPrefix(normalized, query) {
			score = 0
		} else if strings.Contains(normalized, query) {
			score = min(score, 1)
		}
		if score <= 3 {
			ranked = append(ranked, rankedSuggestion{score: score, name: candidate})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score < ranked[j].score
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, 0, len(ranked))
	for _, candidate := range ranked {
		out = append(out, candidate.name)
	}
	return out
}

func firstHelpParagraph(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	paragraph := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		paragraph = append(paragraph, line)
	}
	return strings.Join(paragraph, " ")
}

func renderCapabilityResolveText(out io.Writer, report capabilityResolveReport) {
	fmt.Fprintln(out, "Capability Resolve")
	fmt.Fprintf(out, "  Query            %s\n", report.Query)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Matches          %d\n", report.MatchCount)
	for _, match := range report.Matches {
		fmt.Fprintf(out, "    - %-10s %s", match.Kind, match.Name)
		if match.Canonical != "" && match.Canonical != match.Name {
			fmt.Fprintf(out, " -> %s", match.Canonical)
		}
		fmt.Fprintln(out)
		if match.Usage != "" {
			fmt.Fprintf(out, "      usage=%s\n", match.Usage)
		}
		if match.Permission != "" {
			fmt.Fprintf(out, "      permission=%s exposed_over_mcp=%t\n", match.Permission, match.ExposedOverMCP)
		}
	}
	if len(report.Suggestions) > 0 {
		fmt.Fprintf(out, "  Suggestions      %s\n", strings.Join(report.Suggestions, ", "))
	}
}

func renderReferenceParityAuditText(out io.Writer, report referenceParityAuditReport) {
	fmt.Fprintln(out, "Capability Snapshot Audit")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Commands != nil {
		renderReferenceSurfaceAuditText(out, *report.Commands)
	}
	if report.Tools != nil {
		renderReferenceSurfaceAuditText(out, *report.Tools)
	}
}

func renderReferenceSurfaceAuditText(out io.Writer, audit referenceSurfaceAudit) {
	fmt.Fprintf(out, "  %s          %d/%d exact, %d group-covered\n", titleCaseASCII(audit.Kind), audit.CoveredCount, audit.ReferenceCount, audit.GroupCoveredCount)
	if audit.UncoveredCount == 0 {
		return
	}
	fmt.Fprintf(out, "    Uncovered      %d\n", audit.UncoveredCount)
	if len(audit.MissingGroups) > 0 {
		fmt.Fprintln(out, "    Uncovered groups")
		limit := min(len(audit.MissingGroups), 5)
		for _, group := range audit.MissingGroups[:limit] {
			fmt.Fprintf(out, "      - %s: %d\n", group.Source, group.Count)
		}
	}
	limit := min(audit.UncoveredCount, 10)
	for _, item := range audit.Missing[:limit] {
		fmt.Fprintf(out, "      - %s", item.Name)
		if item.SourceHint != "" {
			fmt.Fprintf(out, " (%s)", item.SourceHint)
		}
		fmt.Fprintln(out)
	}
	if audit.UncoveredCount > limit {
		fmt.Fprintf(out, "      ... %d more\n", audit.UncoveredCount-limit)
	}
}

func titleCaseASCII(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func renderCapabilitiesText(out io.Writer, report capabilitiesReport) {
	fmt.Fprintln(out, "Codog Capabilities")
	fmt.Fprintf(out, "  Version           %s\n", report.Version)
	fmt.Fprintf(out, "  Commands          %d\n", report.CommandCount)
	fmt.Fprintf(out, "  Slash commands    %d\n", report.SlashCommandCount)
	fmt.Fprintf(out, "  Resume-safe       %d slash commands\n", report.ResumeSafeSlashCount)
	fmt.Fprintf(out, "  Tools             %d\n", report.ToolCount)
	fmt.Fprintf(out, "  Tool aliases      %d\n", report.ToolAliasCount)
	fmt.Fprintf(out, "  MCP servers       %d configured\n", report.MCP.ConfiguredServerCount)
	fmt.Fprintf(out, "  MCP local data    %d resources, %d templates, %d prompts\n", report.MCP.LocalResourceCount, report.MCP.LocalTemplateCount, report.MCP.LocalPromptCount)
	fmt.Fprintf(out, "  Mock parity       %d scenarios, %d categories\n", report.MockParity.ScenarioCount, len(report.MockParity.Categories))
	fmt.Fprintf(out, "  Terminal parity   %s (%d required commands, %d resume-safe)\n", report.Terminal.Status, report.Terminal.RequiredCommandCount, report.Terminal.ResumeSafeSlashCount)
	fmt.Fprintf(out, "  Bridge parity     %s (%d methods, %d routes)\n", report.Bridge.Status, report.Bridge.BridgeMethodCount, report.Bridge.ControlRouteCount)
	fmt.Fprintf(out, "  Orchestration     %s (%d skills, %d plugins, %d MCP servers)\n", report.Orchestration.Status, report.Orchestration.SkillCount, report.Orchestration.PluginCount, report.Orchestration.ConfiguredMCPCount)
	fmt.Fprintf(out, "  Release hardening %s (%s)\n", report.Release.Status, report.Release.Platform)
	fmt.Fprintf(out, "  Command surface   %s (%d help topics, %d completions, %d global JSON)\n", report.CommandSurface.Status, report.CommandSurface.HelpTopicCount, report.CommandSurface.CompletionCommandCount, report.CommandSurface.GlobalOutputFormatCommandCount)
	fmt.Fprintln(out, "  Features")
	for _, feature := range report.Features {
		fmt.Fprintf(out, "    - %s\n", feature)
	}
}

func commandSurface() commandSurfaceReport {
	commands := builtInCommandNames()
	completionSet := stringSet(shellCompletionCommands())
	missingHelp := []string{}
	fallbackHelp := []string{}
	missingCompletion := []string{}
	noGlobalOutput := []string{}
	for _, command := range commands {
		if spec, ok := commandHelpSpecFor(command); !ok {
			missingHelp = append(missingHelp, command)
		} else if spec.SchemaVersion == "codog.help.fallback.v1" {
			fallbackHelp = append(fallbackHelp, command)
		}
		if !completionSet[command] {
			missingCompletion = append(missingCompletion, command)
		}
		if !commandAcceptsGlobalOutputFormat(command) {
			noGlobalOutput = append(noGlobalOutput, command)
		}
	}
	sort.Strings(missingHelp)
	sort.Strings(fallbackHelp)
	sort.Strings(missingCompletion)
	sort.Strings(noGlobalOutput)
	report := commandSurfaceReport{
		Status:                         "ready",
		CommandCount:                   len(commands),
		HelpTopicCount:                 len(commands) - len(missingHelp),
		MissingHelpTopicCount:          len(missingHelp),
		MissingHelpTopics:              missingHelp,
		FallbackHelpTopicCount:         len(fallbackHelp),
		FallbackHelpTopics:             fallbackHelp,
		CompletionCommandCount:         len(commands) - len(missingCompletion),
		MissingCompletionCommandCount:  len(missingCompletion),
		MissingCompletionCommands:      missingCompletion,
		GlobalOutputFormatCommandCount: len(commands) - len(noGlobalOutput),
		NoGlobalOutputFormatCount:      len(noGlobalOutput),
		NoGlobalOutputFormatCommands:   noGlobalOutput,
	}
	if len(missingHelp) > 0 || len(fallbackHelp) > 0 || len(missingCompletion) > 0 {
		report.Status = "gap"
	}
	return report
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[strings.TrimSpace(value)] = true
	}
	return out
}

func capabilityToolAliasesByCanonical(aliases map[string]string, infos []tools.ToolInfo) map[string][]string {
	known := map[string]bool{}
	for _, info := range infos {
		known[info.Name] = true
	}
	out := map[string][]string{}
	for alias, canonical := range aliases {
		if known[canonical] {
			out[canonical] = append(out[canonical], alias)
		}
	}
	for canonical := range out {
		sort.Strings(out[canonical])
	}
	return out
}

func slashCapabilities() []capabilitySlash {
	specs := slash.Specs()
	out := make([]capabilitySlash, 0, len(specs))
	for _, spec := range specs {
		out = append(out, capabilitySlash{
			Name:            spec.Name,
			Usage:           spec.Usage,
			Description:     spec.Description,
			ResumeSupported: spec.ResumeSupported,
			Hidden:          spec.Hidden,
			Disabled:        spec.Disabled,
		})
	}
	return out
}

func codogCapabilityFeatures() []string {
	return sortedUniqueStrings([]string{
		"acp_bridge",
		"anthropic_streaming",
		"api_key_management",
		"ask_user_question_multi_select",
		"ask_user_question_options_alias",
		"ask_user_question_previews",
		"ask_user_question_tabs",
		"approval_tokens",
		"auto_compaction",
		"background_log_offsets",
		"background_output_blocking",
		"bash_output_contract",
		"bash_output_runtime_fields",
		"bash_persisted_output",
		"bash_output_truncation",
		"bash_sandbox_request_status",
		"bash_test_hang_timeout_event",
		"background_tasks",
		"bootstrap_plan",
		"branch_lock_collisions",
		"broad_cwd_guard",
		"bubble_tea_tui",
		"config_layers",
		"config_load_degraded",
		"config_reset",
		"command_surface_audit",
		"cost_token_tracking",
		"deferred_init",
		"doctor_config_load_degraded",
		"doctor_config_validation",
		"doctor_sandbox_runtime_status",
		"dynamic_tool_loading",
		"editor_bridge",
		"g004_conformance",
		"git_workflows",
		"green_contract",
		"hooks",
		"hooks_health",
		"ide_bridge",
		"interface_language",
		"jsonl_sessions",
		"lane_event_projection",
		"lsp",
		"mcp_config_load_degraded",
		"mcp_client",
		"mcp_server",
		"memory_age_scan",
		"metrics",
		"mock_parity_harness",
		"multi_agent",
		"notebooks",
		"oauth",
		"one_shot_prompt",
		"openai_extra_body",
		"openai_compatible_streaming",
		"permission_confirmation",
		"permission_feedback_model_bridge",
		"policy_engine",
		"prefetch_preflight",
		"execution_registry_resolve",
		"plugin_lifecycle",
		"plugin_marketplace",
		"plugins_config_load_degraded",
		"powershell_output_contract",
		"providers_config_load_degraded",
		"prompt_cache_stats",
		"project_memory",
		"notification_preferences",
		"recovery_recipes_ledger",
		"report_schema_projection",
		"remote_control",
		"repl",
		"resume_safe_slash_metadata",
		"sandbox",
		"sandbox_config_defaults",
		"sandbox_runtime_status_report",
		"safer_scope_quick_apply",
		"session_identity_metadata",
		"session_identity_reconciliation",
		"session_resume",
		"skills",
		"slash_commands",
		"sampling_temperature",
		"speech_output",
		"stale_base_guard",
		"stale_branch_guard",
		"status_boot_preflight",
		"status_boot_required_binaries",
		"status_config_load_degraded",
		"status_config_validation",
		"team_watch",
		"telemetry_preferences",
		"tui_first_run_theme_onboarding",
		"tui_live_theme_picker",
		"tui_no_color_theme",
		"tui_permission_picker",
		"tui_permission_feedback",
		"tui_permission_rule_edit",
		"tui_question_picker",
		"tui_structured_tool_activity",
		"tui_tool_activity_in_place",
		"tui_tool_output_expand",
		"task_id_alias_schemas",
		"task_create_prompt_contract",
		"task_get_list_compat_fields",
		"task_lane_board",
		"task_lane_heartbeat",
		"task_metadata_persistence",
		"task_output_runtime_fields",
		"tool_search_mcp_degraded",
		"tool_search_select_query",
		"trust_resolver",
		"typed_task_packets",
		"updater",
		"voice_listen",
		"worker_startup_no_evidence",
		"workspace_switch",
		"workspace_tools",
	})
}

func codogCapabilityProtocols() []string {
	return sortedUniqueStrings([]string{
		"acp_json_rpc_stdio",
		"anthropic_messages",
		"editor_bridge_http",
		"mcp_stdio_client",
		"mcp_stdio_server",
		"openai_chat_completions",
		"remote_control_http",
	})
}

func builtInCommandNames() []string {
	return sortedUniqueStrings([]string{
		"acp",
		"add-dir",
		"advisor",
		"agents",
		"subagent",
		"allowed-tools",
		"android",
		"ant-trace",
		"api",
		"api-key",
		"app",
		"auth",
		"autofix-pr",
		"backfill-sessions",
		"background",
		"bashes",
		"base-check",
		"blame",
		"bookmarks",
		"branch",
		"branch-lock",
		"branchlock",
		"bootstrap-plan",
		"bridge",
		"bridge-kick",
		"break-cache",
		"brief",
		"bug",
		"budget",
		"btw",
		"bughunter",
		"build",
		"cache",
		"caches",
		"capabilities",
		"changelog",
		"checkpoint",
		"chrome",
		"clear",
		"code-intel",
		"color",
		"commands",
		"commit",
		"commit-push-pr",
		"compact",
		"completion",
		"config",
		"continue",
		"context",
		"context-noninteractive",
		"conversation",
		"copy",
		"cost",
		"cron",
		"cwd",
		"ctx_viz",
		"debug-tool-call",
		"deferred-init",
		"definition",
		"desktop",
		"diagnostics",
		"diff",
		"doctor",
		"dump-manifests",
		"effort",
		"enterprise",
		"env",
		"exit",
		"exit-plan",
		"export",
		"extra-usage",
		"extra-usage-core",
		"extra-usage-noninteractive",
		"fast",
		"feedback",
		"files",
		"focus",
		"format",
		"g004",
		"g004-conformance",
		"generateSessionName",
		"git",
		"good-claude",
		"green",
		"green-contract",
		"heapdump",
		"help",
		"history",
		"hooks",
		"hover",
		"ide",
		"import",
		"init",
		"init-verifiers",
		"insights",
		"install",
		"install-github-app",
		"install-slack-app",
		"issue",
		"ios",
		"keybindings",
		"language",
		"listen",
		"lint",
		"log",
		"login",
		"logout",
		"map",
		"marketplace",
		"max-tokens",
		"max-turns",
		"mcp",
		"memory",
		"metrics",
		"mobile",
		"mock-limits",
		"mock-parity",
		"mock-server",
		"model",
		"models",
		"node",
		"notifications",
		"notebook-edit",
		"notebook-read",
		"oauth",
		"oauth-refresh",
		"onboarding",
		"open",
		"output-style",
		"parity",
		"passes",
		"paste",
		"perf-issue",
		"permissions",
		"pin",
		"plan",
		"plugin",
		"plugins",
		"prefetch",
		"pr",
		"pr-comments",
		"pr_comments",
		"privacy-settings",
		"profile",
		"project",
		"prompt",
		"prompt-history",
		"providers",
		"python",
		"rate-limit",
		"rate-limit-options",
		"reasoning",
		"references",
		"release-notes",
		"report-schema",
		"reload-plugins",
		"remote",
		"remote-control",
		"remote-env",
		"remote-setup",
		"rename",
		"repl",
		"reset",
		"reset-limits",
		"resume",
		"review",
		"reviewRemote",
		"rollback",
		"rewind",
		"rc",
		"run",
		"safer-scope",
		"sandbox",
		"sandbox-toggle",
		"scope",
		"search",
		"security-review",
		"self-test",
		"session",
		"server",
		"settings",
		"setup",
		"setup-token",
		"setupGitHubActions",
		"sessions",
		"share",
		"slash",
		"skill",
		"skills",
		"speak",
		"ssh",
		"stash",
		"stale-base",
		"state",
		"stats",
		"status",
		"statusline",
		"startup-report",
		"stickers",
		"summary",
		"symbols",
		"system-prompt",
		"tag",
		"tasks",
		"team",
		"teleport",
		"temperature",
		"telemetry",
		"templates",
		"terminal-setup",
		"terminalSetup",
		"test",
		"theme",
		"think-back",
		"thinkback",
		"thinkback-play",
		"tool-details",
		"todos",
		"tokens",
		"trust",
		"tui",
		"ultraplan",
		"ultrareview",
		"undo",
		"unpin",
		"unfocus",
		"updater",
		"upgrade",
		"usage",
		"validation",
		"version",
		"vim",
		"voice",
		"visualize",
		"web-setup",
		"workspace",
	})
}

func sortedUniqueStrings(values []string) []string {
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
	sort.Strings(out)
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

type commandNotFoundReport struct {
	Kind      string           `json:"kind"`
	ErrorKind string           `json:"error_kind"`
	Error     cliErrorEnvelope `json:"error"`
	Status    string           `json:"status"`
	Command   string           `json:"command"`
	Args      []string         `json:"args,omitempty"`
	Message   string           `json:"message"`
	Hint      string           `json:"hint"`
}

type slashErrorReport struct {
	Kind              string           `json:"kind"`
	ErrorKind         string           `json:"error_kind"`
	Error             cliErrorEnvelope `json:"error"`
	Status            string           `json:"status"`
	Command           string           `json:"command"`
	Message           string           `json:"message"`
	Hint              string           `json:"hint"`
	Suggestions       []string         `json:"suggestions,omitempty"`
	CompatibilityNote string           `json:"compatibility_note,omitempty"`
}

type cliErrorReport struct {
	Type                     string            `json:"type"`
	Kind                     string            `json:"kind"`
	ErrorKind                string            `json:"error_kind"`
	Status                   string            `json:"status"`
	Error                    cliErrorEnvelope  `json:"error"`
	Action                   string            `json:"action,omitempty"`
	Command                  string            `json:"command,omitempty"`
	Args                     []string          `json:"args,omitempty"`
	Option                   string            `json:"option,omitempty"`
	Message                  string            `json:"message"`
	Hint                     string            `json:"hint"`
	Provider                 string            `json:"provider,omitempty"`
	EnvVars                  []string          `json:"env_vars,omitempty"`
	Value                    string            `json:"value,omitempty"`
	Values                   []string          `json:"values,omitempty"`
	Expected                 []string          `json:"expected,omitempty"`
	Argument                 string            `json:"argument,omitempty"`
	ToolName                 string            `json:"tool_name,omitempty"`
	Available                []string          `json:"available,omitempty"`
	ToolAliases              map[string]string `json:"tool_aliases,omitempty"`
	Path                     string            `json:"path,omitempty"`
	SessionSearchPath        string            `json:"session_search_path,omitempty"`
	Workspace                string            `json:"workspace,omitempty"`
	WorkspaceFingerprint     string            `json:"workspace_fingerprint,omitempty"`
	OtherWorkspacePartitions int               `json:"other_workspace_partitions,omitempty"`
	OtherWorkspaceSessions   int               `json:"other_workspace_sessions,omitempty"`
	ExpectedWorkspace        string            `json:"expected_workspace,omitempty"`
	ActualWorkspace          string            `json:"actual_workspace,omitempty"`
}

type cliErrorEnvelope struct {
	Kind      string `json:"kind"`
	Operation string `json:"operation,omitempty"`
	Target    string `json:"target,omitempty"`
	Errno     string `json:"errno,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
}

type actionErrorReport struct {
	Kind      string           `json:"kind"`
	Action    string           `json:"action"`
	Status    string           `json:"status"`
	ErrorKind string           `json:"error_kind"`
	Error     cliErrorEnvelope `json:"error"`
	Argument  string           `json:"argument,omitempty"`
	Message   string           `json:"message"`
	Hint      string           `json:"hint"`
}

type sessionRestoreErrorReport struct {
	Kind                     string           `json:"kind"`
	Action                   string           `json:"action"`
	Status                   string           `json:"status"`
	ErrorKind                string           `json:"error_kind"`
	Error                    cliErrorEnvelope `json:"error"`
	RequestedSession         string           `json:"requested_session,omitempty"`
	Path                     string           `json:"path,omitempty"`
	SessionSearchPath        string           `json:"session_search_path,omitempty"`
	Workspace                string           `json:"workspace,omitempty"`
	WorkspaceFingerprint     string           `json:"workspace_fingerprint,omitempty"`
	OtherWorkspacePartitions int              `json:"other_workspace_partitions,omitempty"`
	OtherWorkspaceSessions   int              `json:"other_workspace_sessions,omitempty"`
	ExpectedWorkspace        string           `json:"expected_workspace,omitempty"`
	ActualWorkspace          string           `json:"actual_workspace,omitempty"`
	Message                  string           `json:"message"`
	Hint                     string           `json:"hint"`
}

func renderSessionRestoreError(out io.Writer, action string, requested string, err error, format string) error {
	if err == nil {
		return nil
	}
	report := buildSessionRestoreErrorReport(action, requested, err)
	exitErr := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "stream-json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: exitErr, Silent: true}
	default:
		return &ExitError{Code: 1, Err: exitErr}
	}
}

func buildSessionRestoreErrorReport(action string, requested string, err error) sessionRestoreErrorReport {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "show"
	}
	requested = strings.TrimSpace(requested)
	report := sessionRestoreErrorReport{
		Kind:             "resume",
		Action:           action,
		Status:           "error",
		ErrorKind:        "session_load_failed",
		RequestedSession: requested,
		Message:          strings.TrimSpace(err.Error()),
		Hint:             "Pass a readable .jsonl or .json session file, or a managed session id from `codog sessions list`.",
	}
	var directoryErr session.PathIsDirectoryError
	var mismatchErr session.WorkspaceMismatchError
	var lookupErr session.LookupError
	switch {
	case errors.As(err, &directoryErr):
		report.ErrorKind = "session_path_is_directory"
		report.Path = directoryErr.Path
		report.Message = fmt.Sprintf("session path is a directory: %s", directoryErr.Path)
		report.Hint = "--resume expects a session id or a .jsonl/.json session file path, not a directory. Run `codog sessions list --json` to list managed sessions."
	case errors.As(err, &mismatchErr):
		report.ErrorKind = "session_workspace_mismatch"
		report.Path = mismatchErr.Path
		report.ExpectedWorkspace = mismatchErr.Expected
		report.ActualWorkspace = mismatchErr.Actual
		report.Message = mismatchErr.Error()
		report.Hint = "Open this session from its original workspace, or use `codog --resume latest` to select the newest compatible session."
	case errors.Is(err, session.ErrNoSessions):
		report.ErrorKind = "no_managed_sessions"
		report.Message = "no managed sessions found"
		report.Hint = "Run `codog prompt <text>` to create a session, or pass an existing .jsonl/.json session path."
		if errors.As(err, &lookupErr) {
			applySessionRestoreLookupError(&report, lookupErr)
		}
	case errors.Is(err, session.ErrSessionNotFound):
		report.ErrorKind = "session_not_found"
		if requested != "" {
			report.Message = fmt.Sprintf("session %q was not found", requested)
		} else if errors.As(err, &lookupErr) && strings.TrimSpace(lookupErr.Reference) != "" {
			report.Message = fmt.Sprintf("session %q was not found", strings.TrimSpace(lookupErr.Reference))
		} else {
			report.Message = "session was not found"
		}
		report.Hint = "Run `codog sessions list` to see saved sessions, or pass an existing .jsonl/.json session path."
		if errors.As(err, &lookupErr) {
			applySessionRestoreLookupError(&report, lookupErr)
		}
	}
	report.Error = buildSessionRestoreErrorEnvelope(err, report)
	return report
}

func buildSessionRestoreErrorEnvelope(err error, report sessionRestoreErrorReport) cliErrorEnvelope {
	cliReport := cliErrorReport{
		Kind:                     report.ErrorKind,
		ErrorKind:                report.ErrorKind,
		Status:                   report.Status,
		Action:                   report.Action,
		Message:                  report.Message,
		Hint:                     report.Hint,
		Path:                     report.Path,
		SessionSearchPath:        report.SessionSearchPath,
		Workspace:                report.Workspace,
		WorkspaceFingerprint:     report.WorkspaceFingerprint,
		OtherWorkspacePartitions: report.OtherWorkspacePartitions,
		OtherWorkspaceSessions:   report.OtherWorkspaceSessions,
		ExpectedWorkspace:        report.ExpectedWorkspace,
		ActualWorkspace:          report.ActualWorkspace,
	}
	if strings.TrimSpace(report.RequestedSession) != "" {
		cliReport.Value = strings.TrimSpace(report.RequestedSession)
	}
	return buildCLIErrorEnvelope(err, cliReport)
}

func applySessionRestoreLookupError(report *sessionRestoreErrorReport, lookup session.LookupError) {
	report.SessionSearchPath = lookup.SearchDir
	report.Workspace = lookup.Workspace
	report.WorkspaceFingerprint = lookup.WorkspaceFingerprint
	report.OtherWorkspacePartitions = lookup.OtherWorkspacePartitions
	report.OtherWorkspaceSessions = lookup.OtherWorkspaceSessions
	if report.Path == "" {
		report.Path = lookup.SearchDir
	}
	report.Hint = sessionLookupHint(report.Hint, lookup)
}

func applyCLIErrorSessionLookup(report *cliErrorReport, err error) {
	var lookup session.LookupError
	if !errors.As(err, &lookup) {
		return
	}
	report.SessionSearchPath = lookup.SearchDir
	report.Workspace = lookup.Workspace
	report.WorkspaceFingerprint = lookup.WorkspaceFingerprint
	report.OtherWorkspacePartitions = lookup.OtherWorkspacePartitions
	report.OtherWorkspaceSessions = lookup.OtherWorkspaceSessions
	if report.Path == "" {
		report.Path = lookup.SearchDir
	}
	report.Hint = sessionLookupHint(report.Hint, lookup)
}

func sessionLookupHint(base string, lookup session.LookupError) string {
	parts := []string{}
	if strings.TrimSpace(base) != "" {
		parts = append(parts, strings.TrimSpace(base))
	}
	if strings.TrimSpace(lookup.SearchDir) != "" {
		parts = append(parts, "Codog searched the current workspace session namespace at "+lookup.SearchDir+".")
	}
	if lookup.OtherWorkspaceSessions > 0 {
		parts = append(parts, fmt.Sprintf("Found %d session(s) in %d other workspace partition(s); those sessions are intentionally isolated from this workspace.", lookup.OtherWorkspaceSessions, lookup.OtherWorkspacePartitions))
	}
	return strings.Join(parts, " ")
}

func sessionNotFoundMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" || message == session.ErrSessionNotFound.Error() {
		return "session was not found"
	}
	if requested, ok := strings.CutPrefix(message, session.ErrSessionNotFound.Error()+":"); ok {
		requested = strings.TrimSpace(requested)
		if requested != "" {
			return fmt.Sprintf("session %q was not found", requested)
		}
	}
	return message
}

func sessionNotFoundMessageFromError(err error, message string) string {
	var lookup session.LookupError
	if errors.As(err, &lookup) && strings.TrimSpace(lookup.Reference) != "" {
		return fmt.Sprintf("session %q was not found", strings.TrimSpace(lookup.Reference))
	}
	return sessionNotFoundMessage(message)
}

type outputFormatError struct {
	Command  string
	Value    string
	Expected []string
}

func (e outputFormatError) Error() string {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		command = "command"
	}
	return fmt.Sprintf("invalid_output_format: unknown %s output format %q", command, e.Value)
}

type missingArgumentError struct {
	Argument string
	Example  string
}

func (e missingArgumentError) Error() string {
	return fmt.Sprintf("missing_argument: %s requires a value", e.Argument)
}

type requiredArgumentError struct {
	Command  string
	Argument string
	Usage    string
}

func (e requiredArgumentError) Error() string {
	argument := strings.TrimSpace(e.Argument)
	if argument == "" {
		argument = "argument"
	}
	return fmt.Sprintf("missing_argument: %s is required", argument)
}

type missingFlagValueError struct {
	Command string
	Flag    string
	Usage   string
}

func (e missingFlagValueError) Error() string {
	flag := strings.TrimSpace(e.Flag)
	if flag == "" {
		flag = "flag"
	}
	return fmt.Sprintf("missing_flag_value: %s requires a value", flag)
}

type invalidFlagValueError struct {
	Flag    string
	Value   string
	Message string
	Hint    string
	Usage   string
}

func (e invalidFlagValueError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		flag := strings.TrimSpace(e.Flag)
		if flag == "" {
			flag = "flag"
		}
		message = flag + " has an invalid value"
	}
	return "invalid_flag_value: " + message
}

type duplicateFlagError struct {
	Flag   string
	Values []string
	Usage  string
}

func (e duplicateFlagError) Error() string {
	flag := strings.TrimSpace(e.Flag)
	if flag == "" {
		flag = "flag"
	}
	return "duplicate_flag: " + flag + " was specified multiple times"
}

type invalidCWDError struct {
	Path string
	Err  error
}

type ideConnectionError struct {
	Kind              string
	ExpectedWorkspace string
	ActualWorkspace   string
	Err               error
}

func (e ideConnectionError) Error() string {
	switch e.Kind {
	case "ide_bridge_state_unavailable":
		if e.Err != nil {
			return fmt.Sprintf("--ide could not read local editor bridge state: %v", e.Err)
		}
		return "--ide could not read local editor bridge state"
	case "ide_workspace_mismatch":
		return fmt.Sprintf("--ide editor workspace %q does not match active workspace %q", e.ActualWorkspace, e.ExpectedWorkspace)
	default:
		return "--ide requires one trusted local editor bridge; identify an editor through `codog bridge serve` first"
	}
}

func (e ideConnectionError) Unwrap() error {
	return e.Err
}

func (e invalidCWDError) Error() string {
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "."
	}
	if e.Err != nil {
		return fmt.Sprintf("invalid_cwd: cannot use cwd %q: %v", path, e.Err)
	}
	return fmt.Sprintf("invalid_cwd: cannot use cwd %q", path)
}

type toolNameError struct {
	Argument  string
	ToolName  string
	Available []string
	Aliases   map[string]string
}

func (e toolNameError) Error() string {
	return fmt.Sprintf("invalid_tool_name: unknown tool name %q for %s", e.ToolName, e.Argument)
}

type missingToolNameError struct {
	Command string
	Usage   string
}

func (e missingToolNameError) Error() string {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		command = "command"
	}
	return fmt.Sprintf("missing_tool_name: %s requires a tool name", command)
}

type unknownOptionError struct {
	Kind    string
	Command string
	Option  string
	Usage   string
}

func (e unknownOptionError) Error() string {
	return fmt.Sprintf("unknown_option: unknown option %q for %s", e.Option, e.Command)
}

type unexpectedExtraArgsError struct {
	Command string
	Args    []string
	Usage   string
}

func (e unexpectedExtraArgsError) Error() string {
	return fmt.Sprintf("unexpected_extra_args: %s got unexpected arguments: %s", e.Command, strings.Join(e.Args, " "))
}

type unknownActionError struct {
	Command     string
	Action      string
	Expected    []string
	Suggestions []string
	Usage       string
}

func (e unknownActionError) Error() string {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		command = "command"
	}
	return fmt.Sprintf("unknown_action: unknown %s command %q", command, strings.TrimSpace(e.Action))
}

type exportFilesystemError struct {
	Operation string
	Path      string
	Err       error
}

func (e exportFilesystemError) Error() string {
	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "write_output"
	}
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "<unknown>"
	}
	if e.Err != nil {
		return fmt.Sprintf("export_%s_failed: failed to %s export output %q: %v", operation, strings.ReplaceAll(operation, "_", " "), path, e.Err)
	}
	return fmt.Sprintf("export_%s_failed: failed to %s export output %q", operation, strings.ReplaceAll(operation, "_", " "), path)
}

func (e exportFilesystemError) Unwrap() error {
	return e.Err
}

type invalidResumeArgumentError struct {
	Command string
	Args    []string
	Resume  string
}

func (e invalidResumeArgumentError) Error() string {
	command := strings.TrimSpace(e.Command)
	if command == "" {
		command = "command"
	}
	return fmt.Sprintf("invalid_resume_argument: %s cannot be used as a trailing --resume command without a leading slash", command)
}

type broadCWDGuardReport struct {
	Kind      string `json:"kind"`
	ErrorKind string `json:"error_kind"`
	Status    string `json:"status"`
	Command   string `json:"command"`
	Workspace string `json:"workspace"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Hint      string `json:"hint"`
}

type debugStartupReport struct {
	Kind              string   `json:"kind"`
	Command           string   `json:"command"`
	Args              []string `json:"args,omitempty"`
	Workspace         string   `json:"workspace"`
	ConfigHome        string   `json:"config_home,omitempty"`
	Model             string   `json:"model,omitempty"`
	BaseURL           string   `json:"base_url,omitempty"`
	RuntimeProvider   string   `json:"runtime_provider,omitempty"`
	OutputFormat      string   `json:"output_format,omitempty"`
	PermissionMode    string   `json:"permission_mode,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	Resume            string   `json:"resume,omitempty"`
	FromPR            string   `json:"from_pr,omitempty"`
	DebugFile         string   `json:"debug_file,omitempty"`
	Debug             bool     `json:"debug"`
	Verbose           bool     `json:"verbose"`
	APIKeyConfigured  bool     `json:"api_key_configured"`
	AuthTokenProvided bool     `json:"auth_token_provided"`
}

func renderDebugStartup(out io.Writer, cfg config.Config, command string, args []string, workspace string, overrides config.FlagOverrides, outputFormat string) error {
	if out == nil && strings.TrimSpace(cfg.DebugFile) == "" {
		return nil
	}
	if !cfg.Debug && !cfg.Verbose && strings.TrimSpace(cfg.DebugFile) == "" {
		return nil
	}
	report := debugStartupReport{
		Kind:              "debug_startup",
		Command:           strings.TrimSpace(command),
		Args:              append([]string(nil), args...),
		Workspace:         workspace,
		ConfigHome:        cfg.ConfigHome,
		Model:             cfg.Model,
		BaseURL:           cfg.BaseURL,
		RuntimeProvider:   cfg.RuntimeProvider,
		OutputFormat:      outputFormat,
		PermissionMode:    cfg.PermissionMode,
		SessionID:         overrides.SessionID,
		Resume:            overrides.Resume,
		FromPR:            overrides.FromPR,
		DebugFile:         cfg.DebugFile,
		Debug:             cfg.Debug,
		Verbose:           cfg.Verbose,
		APIKeyConfigured:  strings.TrimSpace(cfg.APIKey) != "",
		AuthTokenProvided: strings.TrimSpace(cfg.AuthToken) != "",
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("codog debug: %s\n", data)
	if strings.TrimSpace(cfg.DebugFile) != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.DebugFile), 0o755); err != nil {
			return fmt.Errorf("write debug file: %w", err)
		}
		file, err := os.OpenFile(cfg.DebugFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("write debug file: %w", err)
		}
		if _, err := file.WriteString(line); err != nil {
			_ = file.Close()
			return fmt.Errorf("write debug file: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("write debug file: %w", err)
		}
	}
	if out != nil && (cfg.Debug || cfg.Verbose) {
		fmt.Fprint(out, line)
	}
	return nil
}

func renderBroadCWDGuard(out io.Writer, command string, args []string, workspace string, allowed bool, format string) error {
	if allowed || !commandRequiresBroadCWDGuard(command, args) {
		return nil
	}
	reason, normalized, broad := broadWorkspaceReason(workspace, "")
	if !broad {
		return nil
	}
	displayCommand := strings.TrimSpace(command)
	if displayCommand == "" {
		displayCommand = "repl"
	}
	report := broadCWDGuardReport{
		Kind:      "workspace_guard",
		ErrorKind: "broad_cwd",
		Status:    "error",
		Command:   displayCommand,
		Workspace: normalized,
		Reason:    reason,
		Message:   fmt.Sprintf("refusing to run %s from a broad workspace", displayCommand),
		Hint:      "Run Codog from a project directory or pass --allow-broad-cwd when this broad workspace is intentional.",
	}
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func commandRequiresBroadCWDGuard(command string, args []string) bool {
	command = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(command)), "/")
	switch command {
	case "", "repl", "tui", "prompt", "btw", "debug-tool-call":
		return true
	case "run", "test", "build", "lint", "node", "python", "autofix-pr", "review", "reviewremote", "review-remote", "ultrareview", "security-review", "bughunter", "files", "search", "context", "ctx_viz":
		return true
	case "agents":
		return firstMeaningfulArg(args) == "run"
	case "team":
		action := firstMeaningfulArg(args)
		return action == "" || action == "create" || action == "add" || action == "new"
	case "cron":
		action := firstMeaningfulArg(args)
		return action == "create" || action == "add" || action == "new" || action == "run" || action == "run-due"
	case "background", "tasks", "bashes":
		action := firstMeaningfulArg(args)
		return action == "run" || action == "restart"
	default:
		return false
	}
}

func firstMeaningfulArg(args []string) string {
	meaningful := routeMeaningfulArgs(args)
	if len(meaningful) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(meaningful[0]))
}

func broadWorkspaceReason(workspace string, home string) (string, string, bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = filepath.Clean(workspace)
	}
	abs = filepath.Clean(abs)
	if isFilesystemRoot(abs) {
		return "filesystem_root", abs, true
	}
	if strings.TrimSpace(home) == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", abs, false
		}
	}
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		homeAbs = filepath.Clean(home)
	}
	homeAbs = filepath.Clean(homeAbs)
	if samePath(abs, homeAbs) {
		return "home_directory", abs, true
	}
	return "", abs, false
}

func isFilesystemRoot(path string) bool {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	root := volume + string(os.PathSeparator)
	return path == filepath.Clean(root)
}

func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

type promptErrorReport struct {
	Kind      string           `json:"kind"`
	Action    string           `json:"action"`
	ErrorKind string           `json:"error_kind"`
	Error     cliErrorEnvelope `json:"error"`
	Status    string           `json:"status"`
	Argument  string           `json:"argument,omitempty"`
	Message   string           `json:"message"`
	Hint      string           `json:"hint"`
}

func renderMissingPrompt(out io.Writer, format string) error {
	report := buildPromptErrorReport(promptErrorReport{
		Kind:      "prompt",
		Action:    "abort",
		ErrorKind: "missing_prompt",
		Status:    "error",
		Message:   "prompt is empty",
		Hint:      "Provide a prompt with `codog prompt \"...\"`, `codog -p \"...\"`, or pipe text into `codog prompt`.",
	})
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "stream-json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	default:
		return &ExitError{Code: 1, Err: err}
	}
}

func renderEmptyPrompt(out io.Writer, format string) error {
	report := buildPromptErrorReport(promptErrorReport{
		Kind:      "prompt",
		Action:    "abort",
		ErrorKind: "empty_prompt",
		Status:    "error",
		Message:   "empty prompt",
		Hint:      "Provide a prompt with `codog prompt \"...\"`, run a local command such as `codog status`, or start the REPL with no positional argument.",
	})
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "stream-json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	default:
		return &ExitError{Code: 1, Err: err}
	}
}

func renderCompactPromptMissingArgument(out io.Writer, format string) error {
	report := buildPromptErrorReport(promptErrorReport{
		Kind:      "prompt",
		Action:    "abort",
		ErrorKind: "missing_argument",
		Status:    "error",
		Argument:  "prompt or subcommand",
		Message:   "--compact requires a prompt or subcommand",
		Hint:      "Pass a prompt after `--compact`, for example `codog --compact \"summarize this\"`, or run `codog compact --session latest` to compact a saved session.",
	})
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "stream-json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	default:
		return &ExitError{Code: 1, Err: err}
	}
}

func buildPromptErrorReport(report promptErrorReport) promptErrorReport {
	if strings.TrimSpace(report.Status) == "" {
		report.Status = "error"
	}
	cliReport := cliErrorReport{
		Kind:      report.ErrorKind,
		ErrorKind: report.ErrorKind,
		Status:    report.Status,
		Command:   "prompt",
		Action:    report.Action,
		Argument:  report.Argument,
		Message:   report.Message,
		Hint:      report.Hint,
	}
	report.Error = buildCLIErrorEnvelope(errors.New(report.ErrorKind+": "+report.Message), cliReport)
	return report
}

func renderActionError(out io.Writer, report actionErrorReport, format string) error {
	report = buildActionErrorReport(report)
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	return &ExitError{Code: 1, Err: err}
}

func buildActionErrorReport(report actionErrorReport) actionErrorReport {
	if strings.TrimSpace(report.Status) == "" {
		report.Status = "error"
	}
	cliReport := cliErrorReport{
		Kind:      report.ErrorKind,
		ErrorKind: report.ErrorKind,
		Status:    report.Status,
		Command:   report.Kind,
		Action:    report.Action,
		Argument:  report.Argument,
		Message:   report.Message,
		Hint:      report.Hint,
	}
	report.Error = buildCLIErrorEnvelope(errors.New(report.ErrorKind+": "+report.Message), cliReport)
	return report
}

func renderMissingActionArgument(out io.Writer, kind string, action string, argument string, message string, hint string, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      strings.TrimSpace(kind),
		Action:    strings.TrimSpace(action),
		Status:    "error",
		ErrorKind: "missing_argument",
		Argument:  strings.TrimSpace(argument),
		Message:   strings.TrimSpace(message),
		Hint:      strings.TrimSpace(hint),
	}, format)
}

func renderCLIError(out io.Writer, err error, format string) error {
	report := buildCLIErrorReport(err)
	exitErr := errors.New(renderCLIErrorText(report))
	var formatErr outputFormatError
	forceJSON := errors.As(err, &formatErr)
	if strings.EqualFold(format, "json") || forceJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: exitErr, Silent: true}
	}
	return &ExitError{Code: 1, Err: exitErr}
}

func renderCLIErrorWhenStructured(out io.Writer, err error, format string) error {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}
	var formatErr outputFormatError
	if strings.EqualFold(format, "json") || errors.As(err, &formatErr) {
		return renderCLIError(out, err, format)
	}
	return err
}

func buildCLIErrorReport(err error) cliErrorReport {
	message := strings.TrimSpace(err.Error())
	groups := []func(error, string) *cliErrorReport{
		buildCLIErrorReportGroup1,
		buildCLIErrorReportGroup2,
		buildCLIErrorReportGroup3,
		buildCLIErrorReportGroup4,
		buildCLIErrorReportGroup5,
	}
	for _, group := range groups {
		if report := group(err, message); report != nil {
			return *report
		}
	}
	kind := "config_load_failed"
	hint := "Check `codog config paths` and fix the active configuration."
	if rest, ok := strings.CutPrefix(message, "invalid_permission_mode:"); ok {
		kind = "invalid_permission_mode"
		message = strings.TrimSpace(rest)
		hint = "Use one of: read-only, workspace-write, danger-full-access, prompt, allow."
	}
	if rest, ok := strings.CutPrefix(message, "invalid_extra_body:"); ok {
		kind = "invalid_extra_body"
		message = strings.TrimSpace(rest)
		hint = "Set CODOG_EXTRA_BODY to a JSON object, or move provider-specific fields into the config extraBody object."
	}
	return finalizeCLIErrorReport(err, cliErrorReport{
		Kind: kind, ErrorKind: kind, Status: "error", Message: message, Hint: hint,
	})
}

func buildCLIErrorReportGroup1(err error, message string) *cliErrorReport {
	finish := func(report cliErrorReport) *cliErrorReport {
		finished := finalizeCLIErrorReport(err, report)
		return &finished
	}
	var formatErr outputFormatError
	if errors.As(err, &formatErr) {
		expected := append([]string(nil), formatErr.Expected...)
		if len(expected) == 0 {
			expected = []string{"text", "json"}
		}
		return finish(cliErrorReport{
			Kind:      "invalid_output_format",
			ErrorKind: "invalid_output_format",
			Status:    "error",
			Option:    "--output-format",
			Message:   fmt.Sprintf("unknown output format %q", formatErr.Value),
			Hint:      "Use `--output-format json` or `--output-format text`.",
			Value:     formatErr.Value,
			Expected:  expected,
		})
	}
	var jsonSchemaErr promptJSONSchemaValidationError
	if errors.As(err, &jsonSchemaErr) {
		path := strings.TrimSpace(jsonSchemaErr.Path)
		if path == "" {
			path = "$"
		}
		return finish(cliErrorReport{
			Kind:      "json_schema_validation_failed",
			ErrorKind: "json_schema_validation_failed",
			Status:    "error",
			Option:    "--json-schema",
			Message:   jsonSchemaErr.Error(),
			Hint:      "Adjust the prompt or --json-schema so the final assistant response is valid JSON matching the schema.",
			Path:      path,
		})
	}
	var exportErr exportFilesystemError
	if errors.As(err, &exportErr) {
		operation := strings.TrimSpace(exportErr.Operation)
		if operation == "" {
			operation = "write_output"
		}
		path := strings.TrimSpace(exportErr.Path)
		message := fmt.Sprintf("failed to %s export output", strings.ReplaceAll(operation, "_", " "))
		if path != "" {
			message = fmt.Sprintf("%s: %s", message, path)
		}
		hint := "Choose a writable file path inside the workspace."
		switch operation {
		case "resolve_output_path", "validate_output_path":
			hint = "Use a file path inside the workspace, or pass an absolute path intentionally."
			if errors.Is(exportErr.Err, os.ErrNotExist) {
				hint = "Create the parent directory or choose an existing output directory."
			}
		case "write_output":
			hint = "Create the parent directory or choose a writable output file."
		}
		return finish(cliErrorReport{
			Kind:      "export_" + operation + "_failed",
			ErrorKind: "export_" + operation + "_failed",
			Status:    "error",
			Command:   "export",
			Action:    "write",
			Message:   message,
			Hint:      hint,
			Path:      path,
		})
	}
	var directoryErr session.PathIsDirectoryError
	if errors.As(err, &directoryErr) {
		return finish(cliErrorReport{
			Kind:      "session_path_is_directory",
			ErrorKind: "session_path_is_directory",
			Status:    "error",
			Action:    "abort",
			Message:   fmt.Sprintf("session path is a directory: %s", directoryErr.Path),
			Hint:      "Pass a readable .jsonl or .json session file, or a managed session id from `codog sessions list`.",
			Path:      directoryErr.Path,
		})
	}
	var mismatchErr session.WorkspaceMismatchError
	if errors.As(err, &mismatchErr) {
		return finish(cliErrorReport{
			Kind:              "session_workspace_mismatch",
			ErrorKind:         "session_workspace_mismatch",
			Status:            "error",
			Action:            "abort",
			Message:           mismatchErr.Error(),
			Hint:              "Open this session from its original workspace, or use `codog --resume latest` to select the newest compatible session.",
			Path:              mismatchErr.Path,
			ExpectedWorkspace: mismatchErr.Expected,
			ActualWorkspace:   mismatchErr.Actual,
		})
	}
	return nil
}

func buildCLIErrorReportGroup2(err error, message string) *cliErrorReport {
	finish := func(report cliErrorReport) *cliErrorReport {
		finished := finalizeCLIErrorReport(err, report)
		return &finished
	}
	if errors.Is(err, session.ErrNoSessions) {
		report := cliErrorReport{
			Kind:      "no_managed_sessions",
			ErrorKind: "no_managed_sessions",
			Status:    "error",
			Action:    "abort",
			Message:   "no saved sessions found",
			Hint:      "Run `codog prompt <text>` to create a session, or pass an existing .jsonl/.json session path.",
		}
		applyCLIErrorSessionLookup(&report, err)
		return finish(report)
	}
	if errors.Is(err, session.ErrSessionNotFound) {
		report := cliErrorReport{
			Kind:      "session_not_found",
			ErrorKind: "session_not_found",
			Status:    "error",
			Action:    "abort",
			Message:   sessionNotFoundMessageFromError(err, message),
			Hint:      "Run `codog sessions list` to see saved sessions, or pass an existing .jsonl/.json session path.",
		}
		applyCLIErrorSessionLookup(&report, err)
		return finish(report)
	}
	if errors.Is(err, oauth.ErrNoToken) {
		return finish(cliErrorReport{
			Kind:      "oauth_token_missing",
			ErrorKind: "oauth_token_missing",
			Status:    "error",
			Message:   "no oauth token is saved",
			Hint:      "Run `codog oauth token save ACCESS_TOKEN` or complete `codog login` before using token-dependent OAuth commands.",
		})
	}
	if errors.Is(err, undo.ErrNoUndo) {
		return finish(cliErrorReport{
			Kind:      "no_undo_records",
			ErrorKind: "no_undo_records",
			Status:    "error",
			Message:   "no undo records are available",
			Hint:      "Run an editing command that records undo state before using `codog undo`.",
		})
	}
	var outputStyleNotFound outputstyle.NotFoundError
	if errors.As(err, &outputStyleNotFound) {
		return finish(cliErrorReport{
			Kind:      "output_style_not_found",
			ErrorKind: "output_style_not_found",
			Status:    "error",
			Value:     outputStyleNotFound.Name,
			Message:   fmt.Sprintf("output style %q was not found", outputStyleNotFound.Name),
			Hint:      "Run `codog output-style list` to see available output styles.",
		})
	}
	var credentialsErr anthropic.MissingCredentialsError
	if errors.As(err, &credentialsErr) {
		provider := strings.TrimSpace(credentialsErr.Provider)
		if provider == "" {
			provider = "provider"
		}
		envVars := append([]string(nil), credentialsErr.EnvVars...)
		if len(envVars) == 0 {
			envVars = []string{"provider credentials"}
		}
		hint := strings.TrimSpace(credentialsErr.Hint)
		if hint == "" {
			hint = fmt.Sprintf("Set %s before using %s models.", strings.Join(envVars, " or "), provider)
		}
		return finish(cliErrorReport{
			Kind:      "missing_credentials",
			ErrorKind: "missing_credentials",
			Status:    "error",
			Message:   fmt.Sprintf("%s credentials are not configured.", provider),
			Hint:      hint,
			Provider:  provider,
			EnvVars:   envVars,
		})
	}
	var missingErr missingArgumentError
	if errors.As(err, &missingErr) {
		argument := strings.TrimSpace(missingErr.Argument)
		if argument == "" {
			argument = "argument"
		}
		example := strings.TrimSpace(missingErr.Example)
		if example == "" {
			example = argument + " read_file,grep"
		}
		return finish(cliErrorReport{
			Kind:      "missing_argument",
			ErrorKind: "missing_argument",
			Status:    "error",
			Message:   fmt.Sprintf("%s requires a value", argument),
			Hint:      fmt.Sprintf("Provide a comma-separated tool list, for example `%s`.", example),
			Argument:  argument,
		})
	}
	return nil
}

func buildCLIErrorReportGroup3(err error, message string) *cliErrorReport {
	finish := func(report cliErrorReport) *cliErrorReport {
		finished := finalizeCLIErrorReport(err, report)
		return &finished
	}
	var requiredArgErr requiredArgumentError
	if errors.As(err, &requiredArgErr) {
		command := strings.TrimSpace(requiredArgErr.Command)
		argument := strings.TrimSpace(requiredArgErr.Argument)
		if argument == "" {
			argument = "argument"
		}
		usage := strings.TrimSpace(requiredArgErr.Usage)
		hint := fmt.Sprintf("Provide %s.", argument)
		if usage != "" {
			hint = "Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      "missing_argument",
			ErrorKind: "missing_argument",
			Status:    "error",
			Command:   command,
			Argument:  argument,
			Message:   fmt.Sprintf("%s is required", argument),
			Hint:      hint,
		})
	}
	var missingFlagErr missingFlagValueError
	if errors.As(err, &missingFlagErr) {
		command := strings.TrimSpace(missingFlagErr.Command)
		flag := strings.TrimSpace(missingFlagErr.Flag)
		if flag == "" {
			flag = "flag"
		}
		usage := strings.TrimSpace(missingFlagErr.Usage)
		hint := fmt.Sprintf("Provide a value for %s.", flag)
		if usage != "" {
			hint = "Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      "missing_flag_value",
			ErrorKind: "missing_flag_value",
			Status:    "error",
			Command:   command,
			Option:    flag,
			Message:   fmt.Sprintf("%s requires a value", flag),
			Hint:      hint,
		})
	}
	if rest, ok := strings.CutPrefix(message, "missing_flag_value:"); ok {
		message = strings.TrimSpace(rest)
		if message == "" {
			message = "flag requires a value"
		}
		return finish(cliErrorReport{
			Kind:      "missing_flag_value",
			ErrorKind: "missing_flag_value",
			Status:    "error",
			Message:   message,
			Hint:      "Provide the required flag value.",
		})
	}
	if rest, ok := strings.CutPrefix(message, "missing_manifests:"); ok {
		message = strings.TrimSpace(rest)
		if message == "" {
			message = "manifest discovery directory is missing"
		}
		return finish(cliErrorReport{
			Kind:      "missing_manifests",
			ErrorKind: "missing_manifests",
			Status:    "error",
			Command:   "dump-manifests",
			Message:   message,
			Hint:      "Pass an existing workspace directory with --manifests-dir, or omit the flag to inspect the current workspace.",
		})
	}
	var duplicateFlagErr duplicateFlagError
	if errors.As(err, &duplicateFlagErr) {
		flag := strings.TrimSpace(duplicateFlagErr.Flag)
		if flag == "" {
			flag = "flag"
		}
		usage := strings.TrimSpace(duplicateFlagErr.Usage)
		hint := "Remove the duplicate flag or keep a single effective value."
		if usage != "" {
			hint = "Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      "duplicate_flag",
			ErrorKind: "duplicate_flag",
			Status:    "error",
			Option:    flag,
			Value:     strings.Join(duplicateFlagErr.Values, ","),
			Values:    append([]string(nil), duplicateFlagErr.Values...),
			Message:   fmt.Sprintf("%s was specified multiple times", flag),
			Hint:      hint,
		})
	}
	var invalidFlagErr invalidFlagValueError
	if errors.As(err, &invalidFlagErr) {
		flag := strings.TrimSpace(invalidFlagErr.Flag)
		value := strings.TrimSpace(invalidFlagErr.Value)
		message := strings.TrimSpace(invalidFlagErr.Message)
		if message == "" {
			if flag == "" {
				flag = "flag"
			}
			message = flag + " has an invalid value"
		}
		usage := strings.TrimSpace(invalidFlagErr.Usage)
		hint := "Use a supported flag value."
		if strings.TrimSpace(invalidFlagErr.Hint) != "" {
			hint = strings.TrimSpace(invalidFlagErr.Hint)
		} else if usage != "" {
			hint = "Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      "invalid_flag_value",
			ErrorKind: "invalid_flag_value",
			Status:    "error",
			Option:    flag,
			Value:     value,
			Message:   message,
			Hint:      hint,
		})
	}
	return nil
}

func buildCLIErrorReportGroup4(err error, message string) *cliErrorReport {
	finish := func(report cliErrorReport) *cliErrorReport {
		finished := finalizeCLIErrorReport(err, report)
		return &finished
	}
	var cwdErr invalidCWDError
	if errors.As(err, &cwdErr) {
		path := strings.TrimSpace(cwdErr.Path)
		if path == "" {
			path = "."
		}
		return finish(cliErrorReport{
			Kind:      "invalid_cwd",
			ErrorKind: "invalid_cwd",
			Status:    "error",
			Message:   fmt.Sprintf("cannot use cwd %q", path),
			Hint:      "Pass an existing directory with --cwd, -C, or --directory.",
			Path:      path,
		})
	}
	var ideErr ideConnectionError
	if errors.As(err, &ideErr) {
		kind := strings.TrimSpace(ideErr.Kind)
		if kind == "" {
			kind = "ide_bridge_unavailable"
		}
		hint := "Start the local editor bridge and identify a trusted editor before using --ide."
		if kind == "ide_workspace_mismatch" {
			hint = "Identify the editor again from the active workspace, or run Codog from the editor workspace."
		}
		return finish(cliErrorReport{
			Kind:              kind,
			ErrorKind:         kind,
			Status:            "error",
			Command:           "ide",
			Action:            "connect",
			Message:           ideErr.Error(),
			Hint:              hint,
			ExpectedWorkspace: strings.TrimSpace(ideErr.ExpectedWorkspace),
			ActualWorkspace:   strings.TrimSpace(ideErr.ActualWorkspace),
		})
	}
	var toolErr toolNameError
	if errors.As(err, &toolErr) {
		argument := strings.TrimSpace(toolErr.Argument)
		if argument == "" {
			argument = "--allowed-tools"
		}
		toolName := strings.TrimSpace(toolErr.ToolName)
		return finish(cliErrorReport{
			Kind:        "invalid_tool_name",
			ErrorKind:   "invalid_tool_name",
			Status:      "error",
			Message:     fmt.Sprintf("unknown tool name %q for %s", toolName, argument),
			Hint:        "Use canonical snake_case tool names or supported aliases; MCP tools may use mcp__server__tool or mcp__server__*.",
			Argument:    argument,
			ToolName:    toolName,
			Available:   append([]string(nil), toolErr.Available...),
			ToolAliases: copyStringMap(toolErr.Aliases),
		})
	}
	var missingToolErr missingToolNameError
	if errors.As(err, &missingToolErr) {
		command := strings.TrimSpace(missingToolErr.Command)
		if command == "" {
			command = "command"
		}
		usage := strings.TrimSpace(missingToolErr.Usage)
		if usage == "" {
			usage = "codog " + command + " TOOL"
		}
		return finish(cliErrorReport{
			Kind:      "missing_tool_name",
			ErrorKind: "missing_tool_name",
			Status:    "error",
			Command:   command,
			Message:   command + " requires a tool name",
			Hint:      "Usage: " + usage,
		})
	}
	var optionErr unknownOptionError
	if errors.As(err, &optionErr) {
		kind := strings.TrimSpace(optionErr.Kind)
		if kind == "" {
			kind = "unknown_option"
		}
		command := strings.TrimSpace(optionErr.Command)
		option := strings.TrimSpace(optionErr.Option)
		usage := strings.TrimSpace(optionErr.Usage)
		hint := fmt.Sprintf("Remove %s or use a supported option.", option)
		if usage != "" {
			hint = "Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      kind,
			ErrorKind: kind,
			Status:    "error",
			Command:   command,
			Option:    option,
			Message:   fmt.Sprintf("unknown option %q for %s", option, command),
			Hint:      hint,
		})
	}
	var extraArgsErr unexpectedExtraArgsError
	if errors.As(err, &extraArgsErr) {
		command := strings.TrimSpace(extraArgsErr.Command)
		args := append([]string(nil), extraArgsErr.Args...)
		usage := strings.TrimSpace(extraArgsErr.Usage)
		if usage == "" {
			usage = "codog " + command
		}
		return finish(cliErrorReport{
			Kind:      "unexpected_extra_args",
			ErrorKind: "unexpected_extra_args",
			Status:    "error",
			Command:   command,
			Args:      args,
			Message:   fmt.Sprintf("%s does not accept extra arguments: %s", command, strings.Join(args, " ")),
			Hint:      "Usage: " + usage,
		})
	}
	return nil
}

func buildCLIErrorReportGroup5(err error, message string) *cliErrorReport {
	finish := func(report cliErrorReport) *cliErrorReport {
		finished := finalizeCLIErrorReport(err, report)
		return &finished
	}
	var actionErr unknownActionError
	if errors.As(err, &actionErr) {
		command := strings.TrimSpace(actionErr.Command)
		if command == "" {
			command = "command"
		}
		action := strings.TrimSpace(actionErr.Action)
		expected := append([]string(nil), actionErr.Expected...)
		suggestions := append([]string(nil), actionErr.Suggestions...)
		usage := strings.TrimSpace(actionErr.Usage)
		hint := "Use a supported command."
		switch len(suggestions) {
		case 1:
			hint = fmt.Sprintf("Did you mean `%s %s`?", command, suggestions[0])
		case 0:
			if len(expected) > 0 {
				hint = "Use one of: " + strings.Join(expected, ", ") + "."
			}
		default:
			hint = "Did you mean one of: " + strings.Join(suggestions, ", ") + "?"
		}
		if usage != "" {
			hint += " Usage: " + usage
		}
		return finish(cliErrorReport{
			Kind:      "unknown_action",
			ErrorKind: "unknown_action",
			Status:    "error",
			Command:   command,
			Action:    action,
			Value:     action,
			Expected:  expected,
			Message:   fmt.Sprintf("unknown %s command %q", command, action),
			Hint:      hint,
		})
	}
	var resumeArgErr invalidResumeArgumentError
	if errors.As(err, &resumeArgErr) {
		command := strings.TrimSpace(resumeArgErr.Command)
		args := append([]string(nil), resumeArgErr.Args...)
		resume := strings.TrimSpace(resumeArgErr.Resume)
		if resume == "" {
			resume = "latest"
		}
		slashCommand := "/" + strings.TrimPrefix(command, "/")
		return finish(cliErrorReport{
			Kind:      "invalid_resume_argument",
			ErrorKind: "invalid_resume_argument",
			Status:    "error",
			Command:   command,
			Args:      args,
			Message:   fmt.Sprintf("%s cannot be used after --resume without a leading slash", command),
			Hint:      fmt.Sprintf("Use `codog --resume %s %s` for a resume slash command, or `codog --resume %s prompt \"...\"` to continue the session with a prompt. Local commands that support sessions accept command-specific `--session` or `--resume` flags after the command.", shellQuote(resume), slashCommand, shellQuote(resume)),
		})
	}
	return nil
}
func finalizeCLIErrorReport(err error, report cliErrorReport) cliErrorReport {
	report.Type = "error"
	if strings.TrimSpace(report.Status) == "" {
		report.Status = "error"
	}
	if strings.TrimSpace(report.Kind) == "" {
		report.Kind = "unknown"
	}
	if strings.TrimSpace(report.ErrorKind) == "" {
		report.ErrorKind = report.Kind
	}
	report.Error = buildCLIErrorEnvelope(err, report)
	return report
}

func buildCLIErrorEnvelope(err error, report cliErrorReport) cliErrorEnvelope {
	kind := classifyCLIErrorEnvelopeKind(err, report)
	operation := cliErrorOperation(err, report, kind)
	target := cliErrorTarget(err, report)
	errno := cliErrorErrno(err)
	hint := strings.TrimSpace(report.Hint)
	return cliErrorEnvelope{
		Kind:      kind,
		Operation: operation,
		Target:    target,
		Errno:     errno,
		Detail:    cliErrorDetail(err, report),
		Hint:      hint,
		Retryable: cliErrorRetryable(err, kind, errno),
	}
}

func renderCLIErrorText(report cliErrorReport) string {
	parts := []string{
		fmt.Sprintf("%s: %s", report.ErrorKind, report.Message),
		fmt.Sprintf("kind=%s operation=%s retryable=%t", report.Error.Kind, emptyAsCLIError(report.Error.Operation, "unknown"), report.Error.Retryable),
	}
	if target := strings.TrimSpace(report.Error.Target); target != "" {
		parts = append(parts, "target="+target)
	}
	if errno := strings.TrimSpace(report.Error.Errno); errno != "" {
		parts = append(parts, "errno="+errno)
	}
	if detail := strings.TrimSpace(report.Error.Detail); detail != "" && detail != strings.TrimSpace(report.Message) {
		parts = append(parts, "detail="+detail)
	}
	if hint := strings.TrimSpace(report.Error.Hint); hint != "" {
		parts = append(parts, "hint="+hint)
	}
	if report.Error.Kind == "usage" {
		parts = append(parts, "Run `codog --help` for usage.")
	}
	return strings.Join(parts, "\n")
}

func classifyCLIErrorEnvelopeKind(err error, report cliErrorReport) string {
	errorKind := strings.ToLower(strings.TrimSpace(report.ErrorKind))
	switch errorKind {
	case "invalid_cwd", "missing_manifests":
		return "filesystem"
	case "missing_credentials", "oauth_token_missing":
		return "auth"
	case "session_path_is_directory", "session_workspace_mismatch", "no_managed_sessions", "session_not_found":
		return "session"
	case "json_schema_validation_failed":
		return "parse"
	case "command_not_found", "interactive_only", "unknown_slash_command", "unsupported_resumed_slash_command", "missing_argument", "missing_prompt", "empty_prompt", "missing_flag_value", "duplicate_flag", "invalid_flag_value", "invalid_output_format", "invalid_resume_argument", "invalid_tool_name", "missing_tool_name", "unknown_option", "unexpected_extra_args":
		return "usage"
	case "invalid_permission_mode":
		return "policy"
	}
	if strings.HasPrefix(errorKind, "ide_") {
		return "ide"
	}
	if strings.HasPrefix(errorKind, "unsupported_") ||
		strings.HasPrefix(errorKind, "unknown_") ||
		strings.HasSuffix(errorKind, "_not_found") ||
		strings.Contains(errorKind, "_missing_") {
		return "usage"
	}
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return "filesystem"
		}
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "credential"), strings.Contains(message, "api key"), strings.Contains(message, "auth token"), strings.Contains(message, "oauth token"):
			return "auth"
		case strings.Contains(message, "session"):
			return "session"
		case strings.Contains(message, "mcp"):
			return "mcp"
		case strings.Contains(message, "json") || strings.Contains(message, "parse"):
			return "parse"
		case strings.Contains(message, "permission") || strings.Contains(message, "policy") || strings.Contains(message, "sandbox"):
			return "policy"
		case strings.Contains(message, "github") || strings.Contains(message, "pull request") || strings.Contains(message, "deliver"):
			return "delivery"
		}
	}
	if strings.Contains(errorKind, "mcp") {
		return "mcp"
	}
	if strings.Contains(errorKind, "github") || strings.Contains(errorKind, "pr_") || strings.Contains(errorKind, "delivery") {
		return "delivery"
	}
	if errorKind == "config_load_failed" {
		return "runtime"
	}
	return "unknown"
}

func cliErrorOperation(err error, report cliErrorReport, kind string) string {
	var exportErr exportFilesystemError
	if errors.As(err, &exportErr) && strings.TrimSpace(exportErr.Operation) != "" {
		return strings.TrimSpace(exportErr.Operation)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && strings.TrimSpace(pathErr.Op) != "" {
		return strings.TrimSpace(pathErr.Op)
	}
	switch kind {
	case "auth":
		if strings.EqualFold(report.ErrorKind, "oauth_token_missing") {
			return "resolve_oauth_token"
		}
		return "resolve_anthropic_auth"
	case "filesystem":
		if report.ErrorKind == "invalid_cwd" {
			return "resolve_cwd"
		}
		return firstNonEmpty(report.Command, "filesystem")
	case "session":
		return "resolve_session_id"
	case "parse":
		return firstNonEmpty(report.Command, "parse")
	case "mcp":
		return firstNonEmpty(report.Command, "mcp")
	case "delivery":
		return firstNonEmpty(report.Command, "deliver")
	case "policy":
		return firstNonEmpty(report.Command, report.Option, "evaluate_policy")
	case "ide":
		return "connect_editor_bridge"
	case "usage":
		return "parse_args"
	case "runtime":
		return firstNonEmpty(report.Command, report.Action, "run")
	default:
		return firstNonEmpty(report.Command, report.Action, report.ErrorKind, "unknown")
	}
}

func cliErrorTarget(err error, report cliErrorReport) string {
	var exportErr exportFilesystemError
	if errors.As(err, &exportErr) && strings.TrimSpace(exportErr.Path) != "" {
		return strings.TrimSpace(exportErr.Path)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && strings.TrimSpace(pathErr.Path) != "" {
		return strings.TrimSpace(pathErr.Path)
	}
	if report.ErrorKind == "session_not_found" {
		if strings.TrimSpace(report.Value) != "" {
			return strings.TrimSpace(report.Value)
		}
		if target := sessionErrorTarget(report.Message); target != "" {
			return target
		}
		if err != nil {
			return sessionErrorTarget(err.Error())
		}
	}
	if target := firstNonEmpty(report.SessionSearchPath, report.Path, report.Option, report.Argument, report.Value, report.ToolName, report.Command); target != "" {
		return target
	}
	if len(report.EnvVars) > 0 {
		return report.EnvVars[0]
	}
	if strings.TrimSpace(report.Provider) != "" {
		return strings.TrimSpace(report.Provider)
	}
	return ""
}

func sessionErrorTarget(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if _, after, ok := strings.Cut(message, "\""); ok {
		if value, _, ok := strings.Cut(after, "\""); ok {
			return strings.TrimSpace(value)
		}
	}
	if _, after, ok := strings.Cut(message, ":"); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func cliErrorErrno(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, os.ErrPermission):
		return "EACCES"
	case errors.Is(err, os.ErrExist):
		return "EEXIST"
	case errors.Is(err, os.ErrClosed):
		return "EBADF"
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return strings.ToUpper(strings.ReplaceAll(pathErr.Err.Error(), " ", "_"))
	}
	return ""
}

func cliErrorDetail(err error, report cliErrorReport) string {
	if err == nil {
		return strings.TrimSpace(report.Message)
	}
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		return strings.TrimSpace(report.Message)
	}
	return detail
}

func cliErrorRetryable(err error, kind string, errno string) bool {
	switch kind {
	case "filesystem":
		return errno == "ENOENT"
	case "mcp", "delivery", "ide":
		return true
	case "runtime":
		return errors.Is(err, os.ErrClosed)
	default:
		return false
	}
}

func emptyAsCLIError(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func normalizeDirectSlashInvocation(out io.Writer, command string, args []string, format string, extraSuggestions []string) (string, []string, error) {
	name := strings.TrimSpace(command)
	if !strings.HasPrefix(name, "/") {
		return command, args, nil
	}
	if directSlashInteractiveOnly(name) {
		return "", nil, renderInteractiveOnlySlash(out, name, format)
	}
	mapped := slashCommandName(name)
	if mapped == "" {
		return "", nil, renderUnknownSlashCommand(out, name, format, extraSuggestions)
	}
	return mapped, injectGlobalOutputFormat(mapped, args, format), nil
}

func (a *App) runDirectCustomSlash(ctx context.Context, command string, args []string, overrides config.FlagOverrides, format string) (bool, error) {
	custom, err := a.findRuntimeCustomCommand(command)
	if err != nil {
		if errors.Is(err, customcommands.ErrNotFound) {
			return false, nil
		}
		return true, err
	}
	rendered := customcommands.RenderWithSession(custom, strings.Join(args, " "), directCustomSlashSessionID(overrides))
	if strings.TrimSpace(rendered.Rendered) == "" {
		return true, fmt.Errorf("custom command %s rendered an empty prompt", command)
	}
	return true, a.promptWithOutputOptions(ctx, rendered.Rendered, overrides, format, false, turnOptions{AllowedTools: custom.AllowedTools})
}

func directCustomSlashSessionID(overrides config.FlagOverrides) string {
	return firstNonEmpty(strings.TrimSpace(overrides.Resume), strings.TrimSpace(overrides.SessionID))
}

// RunResumedSlash executes a supported slash command against a resumed session.
func (a *App) RunResumedSlash(ctx context.Context, command string, args []string, overrides config.FlagOverrides, format string) error {
	name := strings.ToLower(strings.TrimSpace(command))
	if !strings.HasPrefix(name, "/") {
		return fmt.Errorf("resume slash command must start with /: %q", command)
	}
	mapped := slashCommandName(name)
	if mapped == "" {
		if handled, err := a.runDirectCustomSlash(ctx, command, args, overrides, format); handled {
			return err
		}
		return renderUnknownSlashCommand(a.Out, command, format, a.customSlashCompletionCandidates())
	}
	name = slashSwitchName(mapped)
	resumed := overrides
	if strings.TrimSpace(resumed.Resume) == "" {
		resumed.Resume = "latest"
	}
	handlers := []resumedSlashHandler{
		a.runResumedSlashBasics,
		a.runResumedSlashAccount,
		a.runResumedSlashPreferences,
		a.runResumedSlashWorkspace,
		a.runResumedSlashExtensions,
		a.runResumedSlashReview,
		a.runResumedSlashCodeIntel,
		a.runResumedSlashProduct,
		a.runResumedSlashRemote,
		a.runResumedSlashOperations,
		a.runResumedSlashDevelopment,
		a.runResumedSlashGit,
		a.runResumedSlashSessions,
		a.runResumedSlashUsage,
	}
	for _, handle := range handlers {
		if err := handle(ctx, name, args, resumed, format); !errors.Is(err, errResumedSlashNotHandled) {
			return err
		}
	}
	return renderUnsupportedResumedSlashCommand(a.Out, command, format)
}

var errResumedSlashNotHandled = errors.New("resumed slash command not handled")

type resumedSlashHandler func(context.Context, string, []string, config.FlagOverrides, string) error

func (a *App) runResumedSlashBasics(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/help":
		return renderHelpCommand(a.Out, resumeSlashArgs("help", args, format))
	case "/version":
		return renderVersion(a.Out, a.Workspace, resumeSlashArgs("version", args, format))
	case "/config":
		return a.ConfigCommand(resumeSlashArgs("config", args, format))
	case "/api":
		return a.runResumedAPISlash(resumeSlashArgs("api", args, format), resumed, format)
	case "/api-key":
		return a.runResumedAPIKeySlash(resumeSlashArgs("api-key", args, format), format)
	case "/providers":
		return a.runResumedProvidersSlash(resumeSlashArgs("providers", args, format), format)
	case "/oauth":
		return a.runResumedOAuthSlash(args, format)
	case "/login":
		return a.runResumedLoginSlash(resumeSlashArgs("login", args, format), format)
	case "/oauth-refresh":
		return a.runResumedOAuthRefreshSlash(resumeSlashArgs("oauth-refresh", args, format), format)
	case "/logout":
		return a.runResumedLogoutSlash(resumeSlashArgs("logout", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashAccount(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/profile":
		return a.runResumedProfileSlash(resumeSlashArgs("profile", args, format), format)
	case "/advisor":
		return a.runResumedAdvisorSlash(resumeSlashArgs("advisor", args, format), format)
	case "/budget":
		return a.runResumedBudgetSlash(resumeSlashArgs("budget", args, format), format)
	case "/output-style":
		return a.runResumedOutputStyleSlash(resumeSlashArgs("output-style", args, format), format)
	case "/theme":
		return a.runResumedThemeSlash(resumeSlashArgs("theme", args, format), format)
	case "/language":
		return a.runResumedLanguageSlash(resumeSlashArgs("language", args, format), format)
	case "/effort":
		return a.runResumedEffortSlash(resumeSlashArgs("effort", args, format), "effort", format)
	case "/reasoning":
		return a.runResumedEffortSlash(resumeSlashArgs("reasoning", args, format), "reasoning", format)
	case "/fast":
		return a.runResumedFastSlash(resumeSlashArgs("fast", args, format), format)
	case "/voice":
		return a.runResumedVoiceSlash(resumeSlashArgs("voice", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashPreferences(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/listen":
		return a.runResumedVoiceSlash(resumeSlashArgs("voice", append([]string{"listen"}, args...), format), format)
	case "/speak":
		return a.runResumedSpeakSlash(ctx, resumeSlashArgs("speak", args, format), resumed, format)
	case "/vim":
		return a.runResumedVimSlash(resumeSlashArgs("vim", args, format), format)
	case "/chrome":
		return a.runResumedChromeSlash(resumeSlashArgs("chrome", args, format), format)
	case "/notifications":
		return a.runResumedNotificationsSlash(resumeSlashArgs("notifications", args, format), format)
	case "/privacy-settings":
		return a.runResumedPrivacySlash(resumeSlashArgs("privacy-settings", args, format), format)
	case "/telemetry":
		return a.runResumedTelemetrySlash(resumeSlashArgs("telemetry", args, format), format)
	case "/keybindings":
		return a.runResumedKeybindingsSlash(resumeSlashArgs("keybindings", args, format), format)
	case "/max-tokens":
		return a.ResumedMaxTokens(resumeSlashArgs("max-tokens", args, format))
	case "/max-turns":
		return a.ResumedMaxTurns(resumeSlashArgs("max-turns", args, format))
	case "/temperature":
		return a.runResumedTemperatureSlash(resumeSlashArgs("temperature", args, format), format)
	case "/rate-limit":
		return a.runResumedRateLimitSlash(resumeSlashArgs("rate-limit", args, format), format)
	case "/rate-limit-options":
		return a.RateLimitOptions(resumeSlashArgs("rate-limit-options", args, format))
	case "/reset-limits":
		return a.ResetLimits(resumeSlashArgs("reset-limits", args, format))
	case "/permissions":
		return a.runResumedPermissionsSlash(resumeSlashArgs("permissions", args, format), format)
	case "/allowed-tools":
		return a.runResumedAllowedToolsSlash(resumeSlashArgs("allowed-tools", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashWorkspace(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/init":
		return a.Init(resumeSlashArgs("init", args, format))
	case "/init-verifiers":
		return a.InitVerifiers(resumeSlashArgs("init-verifiers", args, format))
	case "/memory":
		return a.Memory(resumeSlashArgs("memory", args, format))
	case "/project":
		return a.Project(resumeSlashArgs("project", args, format))
	case "/env":
		return a.Env(resumeSlashArgs("env", args, format))
	case "/state":
		return a.State(resumeSlashArgs("state", args, format))
	case "/onboarding":
		return a.Onboarding(resumeSlashArgs("onboarding", args, format))
	case "/setup":
		return a.runResumedSetupSlash(ctx, resumeSlashArgs("setup", args, format), format)
	case "/doctor":
		return a.Doctor(resumeSlashArgs("doctor", args, format))
	case "/system-prompt":
		return a.SystemPromptCommand(resumeSlashArgs("system-prompt", args, format))
	case "/tool-details":
		return a.ToolDetails(resumeSlashArgs("tool-details", args, format))
	case "/debug-tool-call":
		return a.runResumedDebugToolCallSlash(ctx, resumeSlashArgs("debug-tool-call", args, format), resumed, format)
	case "/model":
		return a.ResumedModel(resumeSlashArgs("model", args, format))
	case "/models":
		return a.Models(resumeSlashArgs("models", args, format))
	case "/status":
		return a.Status(resumeSlashArgs("status", args, format), resumed)
	case "/statusline":
		return a.Statusline(resumeSlashArgs("statusline", args, format), resumed)
	case "/sandbox":
		return a.Sandbox()
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashExtensions(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/sandbox-toggle":
		return a.runResumedSandboxToggleSlash(resumeSlashArgs("sandbox-toggle", args, format), format)
	case "/mcp":
		return a.runResumedMCPSlash(ctx, resumeSlashArgs("mcp", args, format), resumed, format)
	case "/capabilities":
		return a.Capabilities(resumeSlashArgs("capabilities", args, format))
	case "/prefetch":
		return a.Prefetch(resumeSlashArgs("prefetch", args, format))
	case "/acp":
		return a.runResumedACPSlash(ctx, resumeSlashArgs("acp", args, format), resumed, format)
	case "/skill":
		return a.runResumedSkillsSlash("/skill", resumeSlashArgs("skills", args, format), format)
	case "/skills":
		return a.runResumedSkillsSlash("/skills", resumeSlashArgs("skills", args, format), format)
	case "/commands":
		return a.Commands(resumeSlashArgs("commands", args, format))
	case "/templates":
		return a.Templates(resumeSlashArgs("templates", args, format))
	case "/todos":
		return a.Todos(resumeSlashArgs("todos", args, format))
	case "/hooks":
		return a.runResumedHooksSlash(ctx, resumeSlashArgs("hooks", args, format), format)
	case "/agents":
		return a.runResumedAgentsSlash(resumeSlashArgs("agents", args, format), resumed, format)
	case "/subagent":
		return a.Subagent(resumeSlashArgs("subagent", args, format), resumed)
	case "/plugin", "/plugins", "/marketplace":
		return a.runResumedMarketplaceSlash(resumeSlashArgs("plugins", args, format), format)
	case "/reload-plugins":
		return a.ReloadPlugins(resumeSlashArgs("reload-plugins", args, format))
	case "/background", "/tasks", "/bashes":
		return a.runResumedBackgroundSlash(args, resumed, format)
	case "/cron":
		return a.runResumedCronSlash(resumeSlashArgs("cron", args, format), format)
	case "/team":
		return a.runResumedTeamSlash(resumeSlashArgs("team", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashReview(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/terminal-setup":
		return a.runResumedTerminalSetupSlash(resumeSlashArgs("terminal-setup", args, format), format)
	case "/files":
		return a.Files(resumeSlashArgs("files", args, format))
	case "/scope":
		return a.Scope(resumeSlashArgs("scope", args, format))
	case "/search":
		return a.Search(ctx, resumeSlashArgs("search", args, format))
	case "/security-review":
		return a.SecurityReview(resumeSlashArgs("security-review", args, format))
	case "/bughunter":
		return a.Bughunter(resumeSlashArgs("bughunter", args, format))
	case "/feedback":
		return a.Feedback(resumeSlashArgs("feedback", args, format), resumed)
	case "/pr":
		return a.PullRequestDraft(resumeSlashArgs("pr", args, format), resumed)
	case "/issue":
		return a.IssueDraft(resumeSlashArgs("issue", args, format), resumed)
	case "/autofix-pr":
		return a.AutofixPR(ctx, resumeSlashArgs("autofix-pr", args, format))
	case "/pr-comments":
		return a.PRComments(ctx, resumeSlashArgs("pr-comments", args, format))
	case "/brief":
		return a.Brief(resumeSlashArgs("brief", args, format))
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashCodeIntel(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/btw":
		return a.BTW(ctx, resumeSlashArgs("btw", args, format), resumed, nil)
	case "/install-github-app":
		return a.InstallGitHubApp(resumeSlashArgs("install-github-app", args, format))
	case "/upgrade":
		return a.Upgrade(ctx, resumeSlashArgs("upgrade", args, format))
	case "/install":
		return a.Install(ctx, resumeSlashArgs("install", args, format))
	case "/review", "/ultrareview":
		return a.Review(resumeSlashArgs("review", args, format))
	case "/reviewremote":
		return a.ReviewRemote(ctx, resumeSlashArgs("reviewRemote", args, format))
	case "/symbols":
		return a.Symbols(resumeSlashArgs("symbols", args, format))
	case "/diagnostics":
		return a.Diagnostics(ctx, resumeSlashArgs("diagnostics", args, format))
	case "/map":
		return a.Map(resumeSlashArgs("map", args, format))
	case "/references":
		return a.References(resumeSlashArgs("references", args, format))
	case "/definition":
		return a.Definition(resumeSlashArgs("definition", args, format))
	case "/hover":
		return a.Hover(resumeSlashArgs("hover", args, format))
	case "/teleport":
		return a.Teleport(resumeSlashArgs("teleport", args, format))
	case "/completion":
		return a.Completion(resumeSlashArgs("completion", args, format))
	case "/format":
		return a.runResumedFormatSlash(resumeSlashArgs("format", args, format), format)
	case "/code-intel":
		return a.runResumedCodeIntelSlash(ctx, args, format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashProduct(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/notebook-read":
		return a.CodeIntel(resumeSlashArgs("notebook-read", append([]string{"notebook-read"}, args...), format))
	case "/notebook-edit":
		return a.CodeIntel(resumeSlashArgs("notebook-edit", append([]string{"notebook-edit"}, args...), format))
	case "/metrics":
		return a.Metrics(resumeSlashArgs("metrics", args, format), resumed)
	case "/insights":
		return a.Insights(resumeSlashArgs("insights", args, format))
	case "/perf-issue":
		return a.runResumedPerfIssueSlash(resumeSlashArgs("perf-issue", args, format), format)
	case "/think-back":
		return a.runResumedThinkBackSlash(name, resumeSlashArgs("think-back", args, format), format)
	case "/desktop":
		return a.Desktop(resumeSlashArgs("desktop", args, format), resumed)
	case "/mobile":
		return a.Mobile(resumeSlashArgs("mobile", args, format), resumed)
	case "/ios":
		return a.Mobile(resumeSlashArgs("mobile", append([]string{"ios"}, args...), format), resumed)
	case "/android":
		return a.Mobile(resumeSlashArgs("mobile", append([]string{"android"}, args...), format), resumed)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashRemote(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/remote-env":
		return a.runResumedRemoteEnvSlash(resumeSlashArgs("remote-env", args, format), format)
	case "/remote":
		return a.runResumedRemoteSlash(resumeSlashArgs("remote", args, format), resumed, format)
	case "/remote-setup":
		return a.runResumedRemoteSetupSlash(resumeSlashArgs("remote-setup", args, format), resumed, format)
	case "/bridge":
		return a.runResumedBridgeSlash(name, resumeSlashArgs("bridge", args, format), resumed, format)
	case "/ide":
		return a.runResumedIDESlash(resumeSlashArgs("ide", args, format), format)
	case "/bridge-kick":
		return a.runResumedBridgeKickSlash(resumeSlashArgs("bridge-kick", args, format), format)
	case "/workspace":
		return a.runResumedWorkspaceSlash(resumeSlashArgs("workspace", args, format), format)
	case "/focus":
		return a.runResumedFocusSlash(resumeSlashArgs("focus", args, format), format)
	case "/unfocus":
		return a.runResumedUnfocusSlash(resumeSlashArgs("unfocus", args, format), format)
	case "/add-dir":
		return a.runResumedAddDirSlash(resumeSlashArgs("add-dir", args, format), format)
	case "/validation":
		return a.Validation(resumeSlashArgs("validation", args, format))
	case "/ant-trace":
		return a.runResumedAntTraceSlash(ctx, resumeSlashArgs("ant-trace", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashOperations(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/mock-limits":
		return a.runResumedMockLimitsSlash(resumeSlashArgs("mock-limits", args, format), resumed, format)
	case "/mock-parity", "/self-test":
		defaultFormat := "text"
		if name == "/self-test" {
			defaultFormat = "json"
		}
		return runMockParityCommand(ctx, a.Out, args, format, defaultFormat)
	case "/extra-usage":
		return a.runResumedExtraUsageSlash(resumeSlashArgs("extra-usage", args, format), format)
	case "/install-slack-app":
		return a.runResumedInstallSlackAppSlash(resumeSlashArgs("install-slack-app", args, format), format)
	case "/stickers":
		return a.runResumedStickersSlash(resumeSlashArgs("stickers", args, format), format)
	case "/passes":
		return a.runResumedPassesSlash(resumeSlashArgs("passes", args, format), format)
	case "/heapdump":
		return a.runResumedHeapDumpSlash(resumeSlashArgs("heapdump", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashDevelopment(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/diff":
		return a.Diff(resumeSlashArgs("diff", args, format))
	case "/commit":
		return a.GitCommit(resumeSlashArgs("commit", args, format), format)
	case "/commit-push-pr":
		return a.CommitPushPR(ctx, resumeSlashArgs("commit-push-pr", args, format))
	case "/git":
		return a.Git(resumeSlashArgs("git", args, format))
	case "/run":
		return a.RunCommand(ctx, resumeSlashArgs("run", args, format))
	case "/node", "/python":
		return a.LanguageCommand(ctx, strings.TrimPrefix(name, "/"), resumeSlashArgs(strings.TrimPrefix(name, "/"), args, format))
	case "/test":
		return a.ProjectCommand(ctx, "test", resumeSlashArgs("test", args, format))
	case "/build":
		return a.ProjectCommand(ctx, "build", resumeSlashArgs("build", args, format))
	case "/lint":
		return a.ProjectCommand(ctx, "lint", resumeSlashArgs("lint", args, format))
	case "/log":
		return a.GitLog(resumeSlashArgs("log", args, format))
	case "/blame":
		return a.GitBlame(resumeSlashArgs("blame", args, format))
	case "/changelog":
		return a.Changelog(resumeSlashArgs("changelog", args, format))
	case "/release-notes":
		return a.ReleaseNotes(resumeSlashArgs("release-notes", args, format))
	case "/reset":
		return a.runResumedResetSlash(resumeSlashArgs("reset", args, format), format)
	case "/undo":
		return a.Undo(resumeSlashArgs("undo", args, format))
	case "/plan", "/ultraplan":
		return a.runResumedPlanSlash(resumeSlashArgs("plan", args, format), format)
	case "/exit-plan":
		return a.runResumedPlanSlash(resumeSlashArgs("plan", append([]string{"exit"}, args...), format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashGit(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/branch":
		return a.runResumedBranchSlash(resumeSlashArgs("branch", args, format), format)
	case "/branch-lock":
		return a.BranchLock(resumeSlashArgs("branch-lock", args, format))
	case "/stale-base":
		return a.StaleBase(resumeSlashArgs("stale-base", args, format))
	case "/green-contract":
		return a.GreenContract(resumeSlashArgs("green-contract", args, format))
	case "/g004-conformance":
		return a.G004Conformance(resumeSlashArgs("g004-conformance", args, format))
	case "/report-schema":
		return a.ReportSchema(resumeSlashArgs("report-schema", args, format))
	case "/trust":
		return a.Trust(resumeSlashArgs("trust", args, format))
	case "/tag":
		return a.runResumedTagSlash(resumeSlashArgs("tag", args, format), resumed, format)
	case "/stash":
		return a.runResumedStashSlash(resumeSlashArgs("stash", args, format), format)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashSessions(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/clear":
		return a.ClearResumedSession(resumeSlashArgs("clear", args, format), resumed)
	case "/compact":
		return a.Compact(resumeSlashArgs("compact", args, format), resumed)
	case "/conversation":
		return a.Conversation(resumeSlashArgs("conversation", args, format), resumed)
	case "/sessions":
		return a.runResumedSessionSlash(resumeSlashArgs("sessions", args, format), resumed)
	case "/resume":
		return a.ResumeCommand(resumeSlashArgs("resume", args, format))
	case "/summary":
		return a.Summary(resumeSlashArgs("summary", args, format), resumed)
	case "/history":
		return a.History(resumeSlashArgs("history", args, format), resumed)
	case "/backfill-sessions":
		return a.BackfillSessions(resumeSlashArgs("backfill-sessions", args, format))
	case "/import":
		return a.ClaudeImport(resumeSlashArgs("import", args, format))
	case "/visualize":
		return a.Visualize(resumeSlashArgs("visualize", args, format))
	case "/generatesessionname", "/generate-session-name":
		return a.GenerateSessionName(resumeSlashArgs("generateSessionName", args, format), resumed)
	case "/rewind":
		return a.Rewind(resumeSlashArgs("rewind", args, format), resumed)
	case "/context":
		return a.Context(resumeSlashArgs("context", args, format), resumed)
	case "/ctx_viz":
		return a.ContextViz(resumeSlashArgs("ctx_viz", args, format), resumed)
	case "/export":
		return a.ExportWithOverrides(resumeSlashArgs("export", args, format), resumed)
	case "/share":
		return a.Share(resumeSlashJSONArgs(args, format), resumed)
	case "/copy":
		return a.Copy(ctx, resumeSlashJSONArgs(args, format), resumed)
	case "/paste":
		return a.Paste(ctx, resumeSlashJSONArgs(args, format), resumed)
	default:
		return errResumedSlashNotHandled
	}
}

func (a *App) runResumedSlashUsage(ctx context.Context, name string, args []string, resumed config.FlagOverrides, format string) error {
	switch name {
	case "/pin":
		return a.Pin(resumeSlashArgs("pin", args, format), resumed)
	case "/unpin":
		return a.Unpin(resumeSlashArgs("unpin", args, format), resumed)
	case "/cost", "/tokens", "/stats":
		return a.UsageOverview(strings.TrimPrefix(name, "/"), resumeSlashArgs(strings.TrimPrefix(name, "/"), args, format), resumed)
	case "/usage":
		return a.Usage(resumeSlashArgs("usage", args, format), resumed)
	case "/cache":
		return a.Cache(resumeSlashArgs("cache", args, format), resumed)
	case "/break-cache":
		return a.BreakCache(resumeSlashArgs("break-cache", args, format), resumed)
	case "/bookmarks":
		return a.Bookmarks(resumeSlashArgs("bookmarks", args, format), resumed)
	case "/rename":
		return a.Rename(resumeSlashArgs("rename", args, format), resumed)
	default:
		return errResumedSlashNotHandled
	}
}
