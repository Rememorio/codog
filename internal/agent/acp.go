package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/projectinit"
	"github.com/Rememorio/codog/internal/session"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/trustresolver"
	"github.com/Rememorio/codog/internal/versioninfo"
	"github.com/Rememorio/codog/internal/workerstate"
)

func draftGitStateSummary(state *gitops.PreservedGitState) *draftGitStateReport {
	if state == nil {
		return nil
	}
	return &draftGitStateReport{
		RemoteBase:     state.RemoteBase,
		RemoteBaseSHA:  state.RemoteBaseSHA,
		HeadSHA:        state.HeadSHA,
		BranchName:     state.BranchName,
		PatchBytes:     len(state.Patch),
		UntrackedFiles: len(state.UntrackedFiles),
		FormatPatch:    strings.TrimSpace(state.FormatPatch) != "",
	}
}

func writeDraftCodeBlock(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "No data."
	}
	builder.WriteString("```text\n")
	builder.WriteString(text)
	builder.WriteString("\n```\n")
}

func boundedString(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "\n[truncated]"
}

func boundedGitOutput(workspace string, limit int, args ...string) string {
	out, err := gitops.Run(workspace, args...)
	if err != nil {
		return err.Error()
	}
	if limit <= 0 {
		limit = 12000
	}
	runes := []rune(strings.TrimSpace(out))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "\n[truncated]"
}

func renderVersion(out io.Writer, workspace string, args []string) error {
	format, err := parseSimpleOutputFormat("version", args)
	if err != nil {
		return err
	}
	report := versioninfo.Build(version, workspace)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	versioninfo.RenderText(out, report)
	fmt.Fprintln(out)
	return nil
}

type acpStatusReport struct {
	SchemaVersion string       `json:"schema_version"`
	Kind          string       `json:"kind"`
	Action        string       `json:"action"`
	Status        string       `json:"status"`
	Supported     bool         `json:"supported"`
	Message       string       `json:"message"`
	LaunchCommand *string      `json:"launch_command"`
	Protocol      acpProtocol  `json:"protocol"`
	Contracts     acpContracts `json:"contracts"`
	Aliases       []string     `json:"aliases"`
}

type acpProtocol struct {
	Name              string   `json:"name"`
	JSONRPC           bool     `json:"json_rpc"`
	Daemon            bool     `json:"daemon"`
	Endpoint          *string  `json:"endpoint"`
	ServeStartsDaemon bool     `json:"serve_starts_daemon"`
	Methods           []string `json:"methods"`
}

type acpContracts struct {
	BlockingGates             []string `json:"blocking_gates"`
	StableStatusSurface       string   `json:"stable_status_surface"`
	UnsupportedInvocationKind string   `json:"unsupported_invocation_kind"`
}

type acpUnsupportedReport struct {
	SchemaVersion string   `json:"schema_version"`
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Supported     bool     `json:"supported"`
	Message       string   `json:"message"`
	Invocation    []string `json:"invocation"`
	Hint          string   `json:"hint"`
}

type acpRequest struct {
	Format      string
	Serve       bool
	Unsupported []string
}

var acpJSONRPCMethods = []string{
	"initialize",
	"status",
	"workspace/info",
	"workspace/files",
	"workspace/search",
	"file/read",
	"file/write",
	"file/edit",
	"file/diff",
	"editor/identify",
	"editor/state",
	"editor/open",
	"editor/selection",
	"bridge/faults/list",
	"bridge/faults/record",
	"bridge/faults/clear",
	"diagnostics/go",
	"code/symbols",
	"code/references",
	"code/definition",
	"code/hover",
	"code/completion",
	"code/format",
	"notebook/read",
	"notebook/edit",
	"lsp/actions",
	"lsp/discover",
	"lsp/list",
	"lsp/start",
	"lsp/status",
	"lsp/stop",
	"lsp/query",
	"background/list",
	"background/run",
	"background/get",
	"background/logs",
	"background/board",
	"background/heartbeat",
	"background/stop",
	"background/restart",
	"background/prune",
	"background/supervise",
	"background/watch",
	"agent-runs/list",
	"agent-runs/get",
	"agent-runs/logs",
	"agent-runs/board",
	"agent-runs/heartbeat",
	"agent-runs/stop",
	"agent-runs/prune",
	"mcp/list",
	"mcp/show",
	"mcp/auth",
	"mcp/tools",
	"mcp/call",
	"mcp/resources",
	"mcp/resource-templates",
	"mcp/read",
	"mcp/prompts",
	"mcp/prompt",
	"session/new",
	"session/open",
	"session/list",
	"session/get",
	"session/history",
	"session/append_message",
	"session/append_input",
	"session/rewind",
	"session/fork",
	"session/rename",
	"session/delete",
	"session/prune",
	"prompt",
	"shutdown",
}

var acpSlashAliases = []string{"acp", "--acp", "-acp", "serve", "start", "stdio"}

func renderACPStatus(out io.Writer, args []string) error {
	req, err := parseACPRequest(args)
	if err != nil {
		return err
	}
	if len(req.Unsupported) > 0 {
		report := acpUnsupportedReport{
			SchemaVersion: "1.0",
			Kind:          "unsupported_acp_invocation",
			Action:        "status",
			Status:        "error",
			Supported:     false,
			Message:       "unsupported ACP invocation. Use `codog acp` for status or `codog acp serve` for stdio JSON-RPC.",
			Invocation:    append([]string(nil), args...),
			Hint:          "Start the editor bridge with `codog acp serve`, then send line-delimited JSON-RPC requests on stdin.",
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(out, string(data))
		}
		return fmt.Errorf("unsupported_acp_invocation: unsupported ACP invocation %q", strings.Join(req.Unsupported, " "))
	}
	report := buildACPStatusReport()
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "ACP / Zed")
	fmt.Fprintln(out, "  Status           ok")
	fmt.Fprintln(out, "  Supported        true")
	fmt.Fprintln(out, "  Serve            codog acp serve")
	fmt.Fprintln(out, "  Protocol         stdio JSON-RPC")
	fmt.Fprintln(out, "  Surface          "+strings.Join(acpJSONRPCMethods, ", "))
	fmt.Fprintln(out, "  Message          "+report.Message)
	return nil
}

func buildACPStatusReport() acpStatusReport {
	methods := append([]string(nil), acpJSONRPCMethods...)
	return acpStatusReport{
		SchemaVersion: "1.0",
		Kind:          "acp",
		Action:        "status",
		Status:        "ok",
		Supported:     true,
		Message:       "ACP/Zed editor integration is available over stdio JSON-RPC. Start it with `codog acp serve`, `codog acp start`, or `codog acp stdio`, then use initialize, status, workspace/info, workspace/files, workspace/search, file/read, file/write, file/edit, file/diff, editor/identify, editor/state, editor/open, editor/selection, bridge/faults/list, bridge/faults/record, bridge/faults/clear, diagnostics/go, code/symbols, code/references, code/definition, code/hover, code/completion, code/format, notebook/read, notebook/edit, lsp/actions, lsp/discover, lsp/list, lsp/start, lsp/status, lsp/stop, lsp/query, background/list, background/run, background/get, background/logs, background/board, background/heartbeat, background/stop, background/restart, background/prune, background/supervise, background/watch, agent-runs/list, agent-runs/get, agent-runs/logs, agent-runs/board, agent-runs/heartbeat, agent-runs/stop, agent-runs/prune, mcp/list, mcp/show, mcp/auth, mcp/tools, mcp/call, mcp/resources, mcp/resource-templates, mcp/read, mcp/prompts, mcp/prompt, session/new, session/open, session/list, session/get, session/history, session/append_message, session/append_input, session/rewind, session/fork, session/rename, session/delete, session/prune, prompt, and shutdown requests.",
		LaunchCommand: stringPtr("codog acp serve"),
		Protocol: acpProtocol{
			Name:              "ACP/Zed",
			JSONRPC:           true,
			Daemon:            false,
			Endpoint:          stringPtr("stdio"),
			ServeStartsDaemon: true,
			Methods:           methods,
		},
		Contracts: acpContracts{
			BlockingGates: []string{
				"initialize",
				"session/new",
				"prompt",
				"shutdown",
			},
			StableStatusSurface:       "codog acp --output-format json",
			UnsupportedInvocationKind: "unsupported_acp_invocation",
		},
		Aliases: append([]string(nil), acpSlashAliases...),
	}
}

func parseACPGlobalInvocation(args []string) ([]string, bool, error) {
	if len(args) == 0 {
		return nil, false, nil
	}
	switch {
	case args[0] == "--json":
		if len(args) >= 2 && args[1] == "acp" {
			acpArgs := append([]string{"--json"}, args[2:]...)
			return acpArgs, true, nil
		}
	case args[0] == "--output-format" || args[0] == "-o":
		if len(args) < 2 {
			return nil, false, nil
		}
		if len(args) >= 3 && args[2] == "acp" {
			acpArgs := append([]string{args[0], args[1]}, args[3:]...)
			return acpArgs, true, nil
		}
	case strings.HasPrefix(args[0], "--output-format="):
		if len(args) >= 2 && args[1] == "acp" {
			acpArgs := append([]string{args[0]}, args[2:]...)
			return acpArgs, true, nil
		}
	}
	return nil, false, nil
}

func parseACPRequest(args []string) (acpRequest, error) {
	req := acpRequest{Format: "text"}
	serveSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case isACPServeAlias(arg):
			if serveSeen {
				req.Unsupported = append(req.Unsupported, arg)
			}
			serveSeen = true
			req.Serve = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return acpRequest{}, errors.New("acp output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			req.Unsupported = append(req.Unsupported, arg)
		}
	}
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return acpRequest{}, fmt.Errorf("unknown acp output format %q", req.Format)
	}
}

func acpServeRequested(args []string) bool {
	for _, arg := range args {
		if isACPServeAlias(arg) {
			return true
		}
	}
	return false
}

func acpHelpRequested(args []string) bool {
	for _, arg := range args {
		if isHelpFlag(arg) {
			return true
		}
	}
	return false
}

func isACPServeAlias(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "serve", "start", "stdio":
		return true
	default:
		return false
	}
}

func stringPtr(value string) *string {
	return &value
}

func (a *App) ACP(ctx context.Context, args []string) error {
	req, err := parseACPRequest(args)
	if err != nil {
		return err
	}
	if len(req.Unsupported) != 0 || !req.Serve {
		return renderACPStatus(a.Out, args)
	}
	return a.serveACP(ctx)
}

func acpHeartbeatProvenance(provenance background.EventProvenance, sourceKind string, environment string, channel string, emitter string, confidence string) background.EventProvenance {
	if strings.TrimSpace(sourceKind) != "" {
		provenance.SourceKind = sourceKind
	}
	if strings.TrimSpace(environment) != "" {
		provenance.Environment = environment
	}
	if strings.TrimSpace(channel) != "" {
		provenance.Channel = channel
	}
	if strings.TrimSpace(emitter) != "" {
		provenance.Emitter = emitter
	}
	if strings.TrimSpace(confidence) != "" {
		provenance.Confidence = confidence
	}
	return provenance
}

func acpScopeBinding(binding background.ScopeBinding, owner string, workflowScope string, watcherAction string) background.ScopeBinding {
	if strings.TrimSpace(owner) != "" {
		binding.Owner = owner
	}
	if strings.TrimSpace(workflowScope) != "" {
		binding.WorkflowScope = workflowScope
	}
	if strings.TrimSpace(watcherAction) != "" {
		binding.WatcherAction = watcherAction
	}
	return binding
}

func acpSessionSummary(workspace string, sess *session.Session) acpserver.SessionSummary {
	if sess == nil {
		return acpserver.SessionSummary{Workspace: workspace}
	}
	return acpserver.SessionSummary{
		SessionID:           sess.ID,
		Workspace:           workspace,
		Path:                sess.Path,
		MessageCount:        len(sess.Messages),
		CreatedAtMS:         timeMillis(sess.Metadata.CreatedAt),
		UpdatedAtMS:         timeMillis(sess.Metadata.UpdatedAt),
		ModifiedEpochMillis: timeMillis(sess.Metadata.ModifiedAt),
		ParentSessionID:     sess.Metadata.ParentSessionID,
		BranchName:          sess.Metadata.BranchName,
		Lifecycle:           acpSessionLifecycle(sess),
	}
}

func acpSessionDetail(workspace string, sess *session.Session) acpserver.SessionDetail {
	summary := acpSessionSummary(workspace, sess)
	var messages any
	if sess != nil {
		messages = sess.Messages
	}
	return acpserver.SessionDetail{
		SessionID:           summary.SessionID,
		Workspace:           summary.Workspace,
		Path:                summary.Path,
		MessageCount:        summary.MessageCount,
		CreatedAtMS:         summary.CreatedAtMS,
		UpdatedAtMS:         summary.UpdatedAtMS,
		ModifiedEpochMillis: summary.ModifiedEpochMillis,
		ParentSessionID:     summary.ParentSessionID,
		BranchName:          summary.BranchName,
		Lifecycle:           summary.Lifecycle,
		Messages:            messages,
	}
}

func acpSessionLifecycle(sess *session.Session) acpserver.SessionLifecycle {
	lifecycle := lifecycleForStoredSession(sess)
	return acpserver.SessionLifecycle{
		Kind:      lifecycle.Kind,
		Signal:    lifecycle.Signal,
		Saved:     lifecycle.Saved,
		Abandoned: lifecycle.Abandoned,
	}
}

func acpLastAssistantText(messages []anthropic.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		var out strings.Builder
		for _, block := range messages[i].Content {
			if block.Type == "text" {
				out.WriteString(block.Text)
			}
		}
		return strings.TrimSpace(out.String())
	}
	return ""
}

func limitACPHistory(entries []session.PromptEntry, limit int) []session.PromptEntry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

func initProject(out io.Writer, workspace string, args []string, setupHook func(projectinit.Report) error) error {
	format, err := parseSimpleOutputFormat("init", args)
	if err != nil {
		return err
	}
	report, err := projectinit.Initialize(workspace)
	if err != nil {
		return err
	}
	if setupHook != nil {
		if err := setupHook(report); err != nil {
			return err
		}
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, projectinit.RenderText(report))
	return nil
}

type memoryRequest struct {
	Action  string
	Format  string
	Editor  string
	Limit   int
	NoOpen  bool
	All     bool
	Confirm bool
	Rest    []string
}

func renderMemoryCommand(out io.Writer, workspace string, rulesImport memory.RulesImportOptions, args []string) error {
	req, err := parseMemoryArgs(args)
	if err != nil {
		return renderMemoryError(out, req.Action, err, req.Format)
	}
	runner := memoryCommandRunner{out: out, workspace: workspace, rulesImport: rulesImport}
	return runner.run(req)
}

type memoryCommandRunner struct {
	out         io.Writer
	workspace   string
	rulesImport memory.RulesImportOptions
}

func (r memoryCommandRunner) run(req memoryRequest) error {
	switch req.Action {
	case "list":
		return r.list(req)
	case "show":
		return r.show(req)
	case "select":
		return r.selectMemory(req)
	case "add":
		return r.add(req)
	case "search", "relevant":
		return r.search(req)
	case "path", "ensure":
		return r.file(req)
	case "edit":
		return r.edit(req)
	case "reset":
		return r.reset(req)
	default:
		return renderMemoryError(r.out, req.Action, fmt.Errorf("unknown memory action %q", req.Action), req.Format)
	}
}

func (r memoryCommandRunner) list(req memoryRequest) error {
	report, err := memory.BuildReportWithRulesImport(r.workspace, r.rulesImport)
	return renderMemoryResult(r.out, req, report, err, memory.RenderReport)
}

func (r memoryCommandRunner) show(req memoryRequest) error {
	report, err := memory.ShowWithRulesImport(r.workspace, strings.Join(req.Rest, " "), r.rulesImport)
	return renderMemoryResult(r.out, req, report, err, memory.RenderShowReport)
}

func (r memoryCommandRunner) selectMemory(req memoryRequest) error {
	report, err := memory.Select(r.workspace, strings.Join(req.Rest, " "))
	return renderMemoryResult(r.out, req, report, err, memory.RenderSelectionReport)
}

func (r memoryCommandRunner) add(req memoryRequest) error {
	if len(req.Rest) == 0 {
		return renderMissingActionArgument(r.out, "memory", "add", "text", "memory add requires text", "Usage: codog memory add TEXT [--json|--output-format text|json].", req.Format)
	}
	report, err := memory.Append(r.workspace, strings.Join(req.Rest, " "))
	return renderMemoryResult(r.out, req, report, err, memory.RenderAppendReport)
}

func (r memoryCommandRunner) search(req memoryRequest) error {
	if len(req.Rest) == 0 {
		return renderMissingActionArgument(r.out, "memory", "search", "query", "memory search requires a query", "Usage: codog memory search QUERY [--limit N] [--json|--output-format text|json].", req.Format)
	}
	report, err := memory.SearchWithRulesImport(r.workspace, strings.Join(req.Rest, " "), req.Limit, r.rulesImport)
	return renderMemoryResult(r.out, req, report, err, memory.RenderSearchReport)
}

func (r memoryCommandRunner) file(req memoryRequest) error {
	var report memory.FileReport
	var err error
	if req.Action == "path" {
		report, err = memory.Path(r.workspace, strings.Join(req.Rest, " "))
	} else {
		report, err = memory.Ensure(r.workspace, strings.Join(req.Rest, " "))
	}
	return renderMemoryResult(r.out, req, report, err, memory.RenderFileReport)
}

func (r memoryCommandRunner) edit(req memoryRequest) error {
	report, err := memory.Edit(r.workspace, strings.Join(req.Rest, " "), req.Editor, !req.NoOpen)
	return renderMemoryResult(r.out, req, report, err, memory.RenderFileReport)
}

func (r memoryCommandRunner) reset(req memoryRequest) error {
	report, err := memory.Reset(r.workspace, memory.ResetOptions{Target: strings.Join(req.Rest, " "), All: req.All, Confirm: req.Confirm})
	return renderMemoryResult(r.out, req, report, err, memory.RenderResetReport)
}

func renderMemoryResult[T any](out io.Writer, req memoryRequest, report T, err error, render func(io.Writer, T)) error {
	if err != nil {
		return renderMemoryError(out, req.Action, err, req.Format)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	render(out, report)
	return nil
}

func renderMemoryError(out io.Writer, action string, err error, format string) error {
	if err == nil {
		return nil
	}
	if !strings.EqualFold(format, "json") {
		return err
	}
	report := buildMemoryErrorReport(action, err)
	return renderActionError(out, report, format)
}

func buildMemoryErrorReport(action string, err error) actionErrorReport {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "list"
	}
	message := strings.TrimSpace(err.Error())
	report := actionErrorReport{
		Kind:      "memory",
		Action:    action,
		Status:    "error",
		ErrorKind: "memory_error",
		Message:   message,
		Hint:      "Run `codog memory list --json` to see loaded instruction files.",
	}
	switch {
	case strings.Contains(message, "no memory files found"):
		report.ErrorKind = "no_memory_files"
		report.Message = "no project memory files were found"
		report.Hint = "Create AGENTS.md, CLAUDE.md, .claude/CLAUDE.md, CLAW.md, .claw/CLAUDE.md, .claw/instructions.md, or .codog/instructions.md, or run `codog memory ensure AGENTS.md`."
	case strings.Contains(message, "memory file path is required"):
		report.ErrorKind = "memory_file_required"
		report.Argument = "path"
		report.Hint = "Pass a memory file name from `codog memory list`, for example `codog memory show AGENTS.md --json`."
	case strings.Contains(message, "memory file not found"):
		report.ErrorKind = "memory_file_not_found"
		report.Argument = "path"
		report.Hint = "Run `codog memory list --json` and retry with one of the listed file names or paths."
	case strings.Contains(message, "memory path escapes workspace"):
		report.ErrorKind = "invalid_memory_path"
		report.Argument = "path"
		report.Hint = "Use a workspace-relative memory path such as AGENTS.md or .codog/instructions.md."
	case strings.Contains(message, "memory search query is required"):
		report.ErrorKind = "missing_argument"
		report.Argument = "query"
		report.Hint = "Usage: codog memory search QUERY [--limit N] [--json|--output-format text|json]."
	case strings.Contains(message, "memory reset confirmation required"):
		report.ErrorKind = "confirmation_required"
		report.Message = "memory reset requires --confirm"
		report.Hint = "Run `codog memory reset PATH --confirm` to clear one memory file, or `codog memory reset --all --confirm` to clear all discovered memory files."
	case strings.Contains(message, "unknown memory action"):
		report.ErrorKind = "unsupported_memory_action"
		report.Hint = unknownMemoryActionHint(action)
	case strings.Contains(message, "unknown memory flag"):
		report.ErrorKind = "unknown_option"
		report.Hint = "Usage: codog memory [list|select|show|search|relevant|add|path|ensure|edit|reset] [ARGS...] [--all] [--confirm] [--limit N] [--editor COMMAND] [--no-open] [--json|--output-format text|json]."
	case strings.Contains(message, "unknown memory output format"):
		report.ErrorKind = "invalid_output_format"
		report.Argument = "output_format"
		report.Hint = "Use --output-format text or --output-format json."
	case strings.Contains(message, "memory limit is required"):
		report.ErrorKind = "missing_argument"
		report.Argument = "limit"
		report.Hint = "Pass a positive integer after --limit."
	case strings.Contains(message, "memory limit must be a positive integer"):
		report.ErrorKind = "invalid_argument"
		report.Argument = "limit"
		report.Hint = "Pass a positive integer after --limit."
	case strings.Contains(message, "memory editor is required"):
		report.ErrorKind = "missing_argument"
		report.Argument = "editor"
		report.Hint = "Pass an editor command after --editor, or omit --editor to use VISUAL or EDITOR."
	case strings.Contains(message, "memory output format is required"):
		report.ErrorKind = "missing_argument"
		report.Argument = "output_format"
		report.Hint = "Pass text or json after --output-format."
	}
	return report
}

func parseMemoryArgs(args []string) (memoryRequest, error) {
	req := memoryRequest{Action: "list", Format: "text", Limit: 20}
	if format, ok := scanMemoryOutputFormat(args); ok {
		req.Format = format
	}
	actionSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--no-open":
			req.NoOpen = true
		case arg == "--all":
			req.All = true
		case arg == "--confirm":
			req.Confirm = true
		case arg == "--editor":
			if i+1 >= len(args) {
				return req, errors.New("memory editor is required")
			}
			i++
			req.Editor = args[i]
		case strings.HasPrefix(arg, "--editor="):
			req.Editor = strings.TrimPrefix(arg, "--editor=")
		case arg == "--limit":
			if i+1 >= len(args) {
				return req, errors.New("memory limit is required")
			}
			i++
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit <= 0 {
				return req, fmt.Errorf("memory limit must be a positive integer")
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil || limit <= 0 {
				return req, fmt.Errorf("memory limit must be a positive integer")
			}
			req.Limit = limit
		case arg == "--output-format":
			if i+1 >= len(args) {
				return req, errors.New("memory output format is required")
			}
			i++
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown memory flag %q", arg)
		default:
			if !actionSet {
				action := normalizeMemoryAction(arg)
				switch action {
				case "list", "select", "show", "add", "search", "relevant", "path", "ensure", "edit", "reset":
					req.Action = action
					actionSet = true
					continue
				default:
					req.Action = action
					return req, fmt.Errorf("unknown memory action %q", arg)
				}
			}
			req.Rest = append(req.Rest, arg)
		}
	}
	if req.Format != "text" && req.Format != "json" {
		return req, fmt.Errorf("unknown memory output format %q", req.Format)
	}
	return req, nil
}

func normalizeMemoryAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "select", "choose", "use":
		return "select"
	case "show", "view", "cat", "read":
		return "show"
	case "add", "append":
		return "add"
	case "search", "find":
		return "search"
	case "relevant":
		return "relevant"
	case "path", "file":
		return "path"
	case "ensure", "init", "create", "touch":
		return "ensure"
	case "edit", "open":
		return "edit"
	case "reset", "clear":
		return "reset"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

var memoryActionCandidates = []string{
	"list", "ls", "select", "choose", "use", "show", "view", "cat", "read",
	"add", "append", "search", "find", "relevant", "path", "file", "ensure",
	"init", "create", "touch", "edit", "open", "reset", "clear",
}

func unknownMemoryActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, memoryActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog memory %s`? Use `codog memory list --json` to inspect project memory files.", suggestions[0])
	case 0:
		return "Supported memory actions are list, select, show, search, relevant, add, path, ensure, edit, and reset. Common aliases include view, find, append, file, init, choose, and clear."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog memory list --json` to inspect project memory files.", strings.Join(suggestions, ", "))
	}
}

func scanMemoryOutputFormat(args []string) (string, bool) {
	format := ""
	ok := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			format = "json"
			ok = true
		case arg == "--output-format":
			if i+1 >= len(args) {
				continue
			}
			i++
			format = args[i]
			ok = true
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
			ok = true
		}
	}
	return format, ok
}

func renderWorkerState(out io.Writer, workspace string, args []string) error {
	return renderWorkerStateFromPath(out, workspace, workerstate.Path(workspace), args)
}

func renderWorkerStateFromPath(out io.Writer, workspace string, statePath string, args []string) error {
	format, err := parseSimpleOutputFormat("state", args)
	if err != nil {
		return err
	}
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		statePath = workerstate.Path(workspace)
	}
	state, err := workerstate.LoadPath(statePath)
	if err != nil {
		var missing workerstate.MissingError
		if errors.As(err, &missing) {
			return renderMissingWorkerState(out, missing, format)
		}
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(state, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	workerstate.RenderText(out, state)
	return nil
}

type workerStateErrorReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	ErrorKind string   `json:"error_kind"`
	Path      string   `json:"path"`
	Message   string   `json:"message"`
	Hint      string   `json:"hint"`
	Commands  []string `json:"commands"`
}

func renderMissingWorkerState(out io.Writer, missing workerstate.MissingError, format string) error {
	report := workerStateErrorReport{
		Kind:      "worker_state",
		Action:    "show",
		Status:    "error",
		ErrorKind: "missing_worker_state",
		Path:      missing.Path,
		Message:   fmt.Sprintf("no worker state file found at %s", missing.Path),
		Hint:      "Worker state is written by `codog repl` or `codog prompt <text>`. Run one of those commands, then rerun `codog state [--json]`.",
		Commands: []string{
			"codog repl",
			"codog prompt <text>",
			"codog state [--json]",
		},
	}
	exitErr := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: exitErr, Silent: true}
	}
	return &ExitError{Code: 1, Err: missing}
}

type bootstrapPlanReport struct {
	Kind       string               `json:"kind"`
	Action     string               `json:"action"`
	Status     string               `json:"status"`
	Version    string               `json:"version"`
	Workspace  string               `json:"workspace"`
	PhaseCount int                  `json:"phase_count"`
	Phases     []bootstrapPlanPhase `json:"phases"`
	Message    string               `json:"message,omitempty"`
}

type bootstrapPlanPhase struct {
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Evidence    map[string]any `json:"evidence,omitempty"`
}

func (a *App) BootstrapPlan(args []string) error {
	format, err := parseSimpleOutputFormat("bootstrap-plan", args)
	if err != nil {
		return err
	}
	report := a.buildBootstrapPlanReport(context.Background())
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBootstrapPlanText(a.Out, report)
	return nil
}

func (a *App) buildBootstrapPlanReport(ctx context.Context) bootstrapPlanReport {
	phases := make([]bootstrapPlanPhase, 0, 10)
	addPhase := func(name string, status string, required bool, description string, evidence map[string]any) {
		phases = append(phases, bootstrapPlanPhase{
			Name:        name,
			Status:      status,
			Required:    required,
			Description: description,
			Evidence:    compactEvidence(evidence),
		})
	}

	workspaceStatus := "ready"
	workspaceEvidence := map[string]any{"path": a.Workspace}
	if strings.TrimSpace(a.Workspace) == "" {
		workspaceStatus = "warn"
		workspaceEvidence["error"] = "workspace is empty"
	} else if info, err := os.Stat(a.Workspace); err != nil {
		workspaceStatus = "warn"
		workspaceEvidence["error"] = err.Error()
	} else {
		workspaceEvidence["is_dir"] = info.IsDir()
		if !info.IsDir() {
			workspaceStatus = "warn"
		}
	}
	addPhase("resolve_workspace", workspaceStatus, true, "Resolve the current workspace and path scope before loading local state.", workspaceEvidence)

	configStatus := "ready"
	configEvidence := map[string]any{
		"config_home":      a.Config.ConfigHome,
		"model":            a.Config.Model,
		"permission_mode":  a.Config.PermissionMode,
		"runtime_provider": a.Config.RuntimeProvider,
	}
	if strings.TrimSpace(a.ConfigLoadError) != "" {
		configStatus = "warn"
		configEvidence["config_load_error"] = a.ConfigLoadError
		configEvidence["config_load_error_kind"] = a.ConfigLoadErrorKind
	}
	addPhase("load_config", configStatus, true, "Load layered configuration, provider defaults, permissions, and runtime settings.", configEvidence)

	memoryStatus := "ready"
	memoryEvidence := map[string]any{}
	if files, err := memory.DiscoverWithRulesImport(a.Workspace, a.memoryRulesImportOptions()); err != nil {
		memoryStatus = "warn"
		memoryEvidence["error"] = err.Error()
	} else {
		memoryEvidence["file_count"] = len(files)
	}
	addPhase("load_memory", memoryStatus, false, "Discover project memory files that seed system context.", memoryEvidence)

	runtimeHooks := a.Config.Hooks
	if a.Config.EffectiveDisableAllHooks() {
		runtimeHooks = config.HookConfig{}
	}
	hookValidation := buildHookValidation(runtimeHooks)
	hookStatus := "ready"
	if hookValidation.InvalidCount > 0 {
		hookStatus = "warn"
	}
	addPhase("validate_hooks", hookStatus, false, "Validate configured hooks before any startup or tool events can run.", map[string]any{
		"valid_count":   hookValidation.ValidCount,
		"invalid_count": hookValidation.InvalidCount,
		"disabled":      a.Config.EffectiveDisableAllHooks(),
	})

	mcpValidation := buildMCPValidation(a.Config.MCPServers)
	mcpStatus := "ready"
	if mcpValidation.InvalidCount > 0 {
		mcpStatus = "warn"
	}
	addPhase("validate_mcp", mcpStatus, false, "Validate configured MCP servers before discovery and tool registration.", map[string]any{
		"configured_count": mcpValidation.TotalConfigured,
		"valid_count":      mcpValidation.ValidCount,
		"invalid_count":    mcpValidation.InvalidCount,
		"required_count":   mcpValidation.RequiredCount,
		"optional_count":   mcpValidation.OptionalCount,
	})

	pluginStatus := "ready"
	pluginEvidence := map[string]any{}
	if manifests, err := a.runtimePluginManifests(); err != nil {
		pluginStatus = "warn"
		pluginEvidence["error"] = err.Error()
	} else {
		pluginEvidence["installed_count"] = len(manifests)
	}
	addPhase("load_plugins", pluginStatus, false, "Load plugin manifests that may contribute tools, hooks, skills, and MCP servers.", pluginEvidence)

	toolStatus := "ready"
	toolCount := 0
	if a.Tools == nil {
		toolStatus = "warn"
	} else {
		toolCount = len(a.Tools.Infos())
	}
	addPhase("register_tools", toolStatus, true, "Register built-in and plugin-provided tools with permission metadata.", map[string]any{
		"tool_count": toolCount,
	})

	sessionStatus := "ready"
	sessionEvidence := map[string]any{}
	if a.Sessions == nil {
		sessionStatus = "warn"
		sessionEvidence["error"] = "session store is not configured"
	} else if sessions, err := a.Sessions.List(); err != nil {
		sessionStatus = "warn"
		sessionEvidence["error"] = err.Error()
	} else {
		sessionEvidence["saved_count"] = len(sessions)
	}
	addPhase("open_session_store", sessionStatus, true, "Open the JSONL session store for resume, append, and history operations.", sessionEvidence)

	sessionStartStatus, sessionStartEvidence := a.bootstrapSessionStartHookPhase(ctx, runtimeHooks)
	addPhase("run_session_start_hooks", sessionStartStatus, false, "Run configured session-start hooks when a session is created or resumed.", sessionStartEvidence)

	authConfigured := a.Config.APIKey != "" || a.Config.AuthToken != ""
	dispatchStatus := "ready"
	if !authConfigured {
		dispatchStatus = "warn"
	}
	addPhase("provider_dispatch", dispatchStatus, true, "Dispatch the selected prompt or REPL turn to the configured provider after local startup.", map[string]any{
		"auth_configured":  authConfigured,
		"model":            a.Config.Model,
		"runtime_provider": a.Config.RuntimeProvider,
		"base_url":         a.Config.BaseURL,
	})

	report := bootstrapPlanReport{
		Kind:      "bootstrap_plan",
		Action:    "show",
		Status:    bootstrapPlanStatus(phases),
		Version:   version,
		Workspace: a.Workspace,
		Phases:    phases,
	}
	report.PhaseCount = len(report.Phases)
	if report.Status == "ok" {
		report.Message = "startup plan is ready"
	} else {
		report.Message = "startup plan has warnings"
	}
	return report
}

func (a *App) bootstrapSessionStartHookPhase(ctx context.Context, runtimeHooks config.HookConfig) (string, map[string]any) {
	hookCount := bootstrapHookCount(runtimeHooks, "session_start")
	evidence := map[string]any{
		"hook_count": hookCount,
	}
	if hookCount == 0 {
		return "skipped", evidence
	}
	input, err := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"source":          "bootstrap_plan",
		"cwd":             a.Workspace,
		"permission_mode": a.Config.PermissionMode,
		"model":           a.Config.Model,
	})
	if err != nil {
		evidence["error"] = err.Error()
		return "warn", evidence
	}
	runner := a.lifecycleHookRunner()
	runner.Config = runtimeHooks
	runner.Disabled = false
	report, err := runner.SessionStartReport(ctx, string(input))
	evidence["hook_status"] = report.Status
	evidence["executed_count"] = len(report.Results)
	evidence["denied"] = report.Denied
	output := hooks.SessionStartOutputFromReport(report)
	evidence["additional_context_count"] = len(output.AdditionalContexts)
	evidence["initial_message_count"] = len(output.InitialMessages)
	evidence["watch_path_count"] = len(output.WatchPaths)
	evidence["report"] = report
	if err != nil {
		evidence["error"] = err.Error()
		return "warn", evidence
	}
	if report.Status != "ok" {
		return "warn", evidence
	}
	return "ready", evidence
}

func bootstrapPlanStatus(phases []bootstrapPlanPhase) string {
	for _, phase := range phases {
		if phase.Status == "warn" || phase.Status == "error" {
			return "warn"
		}
	}
	return "ok"
}

func compactEvidence(evidence map[string]any) map[string]any {
	if len(evidence) == 0 {
		return nil
	}
	out := make(map[string]any, len(evidence))
	for key, value := range evidence {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
		case nil:
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func bootstrapHookCount(cfg config.HookConfig, event string) int {
	for _, group := range hookValidationGroups(cfg) {
		if group.Event != event {
			continue
		}
		if len(group.Entries) != 0 {
			return len(group.Entries)
		}
		return len(group.Fallback)
	}
	return 0
}

func renderBootstrapPlanText(out io.Writer, report bootstrapPlanReport) {
	fmt.Fprintln(out, "Bootstrap Plan")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Version          %s\n", report.Version)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Phases           %d\n", report.PhaseCount)
	for index, phase := range report.Phases {
		fmt.Fprintf(out, "  %2d. %-24s %-7s required=%t\n", index+1, phase.Name, phase.Status, phase.Required)
		if phase.Description != "" {
			fmt.Fprintf(out, "      %s\n", phase.Description)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type deferredInitReport struct {
	Kind                string             `json:"kind"`
	Action              string             `json:"action"`
	Status              string             `json:"status"`
	Version             string             `json:"version"`
	Workspace           string             `json:"workspace"`
	Trusted             bool               `json:"trusted"`
	TrustedRootsCount   int                `json:"trusted_roots_count"`
	TrustReason         string             `json:"trust_reason"`
	PluginInit          bool               `json:"plugin_init"`
	SkillInit           bool               `json:"skill_init"`
	MCPPrefetch         bool               `json:"mcp_prefetch"`
	SessionHooks        bool               `json:"session_hooks"`
	TaskCount           int                `json:"task_count"`
	Tasks               []deferredInitTask `json:"tasks"`
	Executed            bool               `json:"executed"`
	Prefetch            *prefetchReport    `json:"prefetch,omitempty"`
	ConfigLoadError     string             `json:"config_load_error,omitempty"`
	ConfigLoadErrorKind string             `json:"config_load_error_kind,omitempty"`
	Message             string             `json:"message,omitempty"`
}

type deferredInitTask struct {
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Enabled     bool           `json:"enabled"`
	Configured  int            `json:"configured"`
	Description string         `json:"description"`
	Reason      string         `json:"reason,omitempty"`
	Evidence    map[string]any `json:"evidence,omitempty"`
}

func (a *App) DeferredInit(command string, args []string) error {
	req, err := parseDeferredInitArgs(command, args)
	if err != nil {
		return err
	}
	report := a.buildDeferredInitReport(req.Action)
	if req.Run {
		a.executeDeferredInit(&report)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderDeferredInitText(a.Out, report)
	return nil
}

type deferredInitRequest struct {
	Action string
	Format string
	Run    bool
}

func parseDeferredInitArgs(command string, args []string) (deferredInitRequest, error) {
	req := deferredInitRequest{Action: strings.TrimSpace(command), Format: "text"}
	usage := "codog " + command + " [status|run] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
			continue
		case strings.EqualFold(arg, "status") || strings.EqualFold(arg, "check"):
			req.Action = strings.TrimSpace(command)
		case strings.EqualFold(arg, "run"):
			req.Action = "run"
			req.Run = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			return req, unexpectedExtraArgsError{Command: command, Args: []string{arg}, Usage: usage}
		}
	}
	normalized, err := normalizeOutputFormat(command, req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalized
	return req, nil
}

func (a *App) executeDeferredInit(report *deferredInitReport) {
	if report == nil {
		return
	}
	if strings.TrimSpace(report.ConfigLoadError) != "" {
		report.Status = "warn"
		report.Message = "deferred init run skipped because config did not load cleanly"
		return
	}
	if !report.Trusted {
		report.Status = "skipped"
		report.Message = "deferred init run skipped because the workspace is not trusted"
		return
	}
	prefetch := a.buildPrefetchReport("deferred-init")
	report.Executed = true
	report.Prefetch = &prefetch
	if prefetch.Status == "warn" {
		report.Status = "warn"
		report.Message = "deferred init executed with warnings"
		return
	}
	report.Status = "ready"
	report.Message = "deferred init executed"
}

func (a *App) buildDeferredInitReport(action string) deferredInitReport {
	trusted, trustReason := a.deferredInitTrust()
	mcpValidation := buildMCPValidation(a.Config.MCPServers)
	runtimeHooks := a.Config.Hooks
	hooksDisabled := a.Config.EffectiveDisableAllHooks()
	if hooksDisabled {
		runtimeHooks = config.HookConfig{}
	}
	hookValidation := buildHookValidation(runtimeHooks)
	installedPlugins, pluginLoadError := installedPluginCount(a.Workspace)
	sessionHookCount := bootstrapHookCount(runtimeHooks, "session_start")
	notificationHookCount := bootstrapHookCount(runtimeHooks, "notification")
	sessionHooksValid := hookValidation.InvalidCount == 0
	mcpValid := mcpValidation.InvalidCount == 0
	configLoaded := strings.TrimSpace(a.ConfigLoadError) == ""

	pluginInit := trusted && configLoaded && pluginLoadError == ""
	skillInit := trusted && configLoaded
	mcpPrefetch := trusted && configLoaded && mcpValid
	sessionHooks := trusted && configLoaded && !hooksDisabled && sessionHooksValid

	tasks := []deferredInitTask{
		deferredInitTaskFor(
			"plugin_init",
			pluginInit,
			installedPlugins,
			"Load trusted plugin manifests and plugin-provided runtime surfaces after the trust gate.",
			deferredInitReason(trusted, configLoaded, pluginLoadError, installedPlugins > 0),
			map[string]any{"installed_count": installedPlugins, "load_error": pluginLoadError},
		),
		deferredInitTaskFor(
			"skill_init",
			skillInit,
			len(a.Config.EnabledSkills),
			"Prepare trusted local skill discovery for prompt routing and slash invocation.",
			deferredInitReason(trusted, configLoaded, "", true),
			map[string]any{"enabled_skill_count": len(a.Config.EnabledSkills)},
		),
		deferredInitTaskFor(
			"mcp_prefetch",
			mcpPrefetch,
			mcpValidation.TotalConfigured,
			"Prefetch valid MCP server metadata and tool manifests after configuration validation.",
			deferredInitReason(trusted, configLoaded, deferredInitInvalidReason("invalid_mcp", mcpValidation.InvalidCount), mcpValidation.TotalConfigured > 0),
			map[string]any{
				"configured_count": mcpValidation.TotalConfigured,
				"valid_count":      mcpValidation.ValidCount,
				"invalid_count":    mcpValidation.InvalidCount,
			},
		),
		deferredInitTaskFor(
			"session_hooks",
			sessionHooks,
			sessionHookCount,
			"Arm session-start hooks for the first created or resumed session.",
			deferredInitReason(trusted, configLoaded && !hooksDisabled, deferredInitInvalidReason("invalid_hooks", hookValidation.InvalidCount), sessionHookCount > 0),
			map[string]any{"hook_count": sessionHookCount, "hooks_disabled": hooksDisabled, "invalid_count": hookValidation.InvalidCount},
		),
		deferredInitTaskFor(
			"notification_hooks",
			trusted && configLoaded && !hooksDisabled && notificationsEnabled(a.Config.Future.NotificationsEnabled) && hookValidation.InvalidCount == 0,
			notificationHookCount,
			"Arm notification hooks for background, team, cron, and lifecycle events.",
			deferredInitReason(trusted, configLoaded && !hooksDisabled, deferredInitInvalidReason("invalid_hooks", hookValidation.InvalidCount), notificationHookCount > 0 && notificationsEnabled(a.Config.Future.NotificationsEnabled)),
			map[string]any{
				"hook_count":               notificationHookCount,
				"notifications_enabled":    notificationsEnabled(a.Config.Future.NotificationsEnabled),
				"notifications_configured": a.Config.Future.NotificationsEnabled != nil,
				"hooks_disabled":           hooksDisabled,
				"invalid_count":            hookValidation.InvalidCount,
			},
		),
		deferredInitTaskFor(
			"background_supervisor",
			trusted && configLoaded,
			1,
			"Permit background task lane state and supervisor checks after the trust gate.",
			deferredInitReason(trusted, configLoaded, "", true),
			map[string]any{"config_home": a.Config.ConfigHome},
		),
	}

	report := deferredInitReport{
		Kind:                "deferred_init",
		Action:              strings.TrimSpace(action),
		Version:             version,
		Workspace:           a.Workspace,
		Trusted:             trusted,
		TrustedRootsCount:   len(nonEmptyStrings(a.Config.TrustedRoots)),
		TrustReason:         trustReason,
		PluginInit:          pluginInit,
		SkillInit:           skillInit,
		MCPPrefetch:         mcpPrefetch,
		SessionHooks:        sessionHooks,
		Tasks:               tasks,
		ConfigLoadError:     strings.TrimSpace(a.ConfigLoadError),
		ConfigLoadErrorKind: strings.TrimSpace(a.ConfigLoadErrorKind),
	}
	report.TaskCount = len(report.Tasks)
	report.Status = deferredInitStatus(report.Tasks, report.ConfigLoadError)
	report.Message = deferredInitMessage(report)
	return report
}

func (a *App) deferredInitTrust() (bool, string) {
	roots := nonEmptyStrings(a.Config.TrustedRoots)
	if len(roots) == 0 {
		return false, "no_trusted_roots"
	}
	entries := make([]trustresolver.AllowlistEntry, 0, len(roots))
	for _, root := range roots {
		entries = append(entries, trustresolver.AllowlistEntry{Pattern: root})
	}
	worktree := ""
	if strings.TrimSpace(a.Workspace) != "" {
		worktree = filepath.Join(a.Workspace, ".git")
	}
	if trustresolver.New(trustresolver.Config{Allowlisted: entries}).Trusts(a.Workspace, worktree) {
		return true, "workspace_matches_trusted_root"
	}
	return false, "workspace_not_trusted"
}

func installedPluginCount(workspace string) (int, string) {
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return 0, err.Error()
	}
	return len(manifests), ""
}

func deferredInitTaskFor(name string, enabled bool, configured int, description string, reason string, evidence map[string]any) deferredInitTask {
	status := "enabled"
	if !enabled {
		status = "skipped"
	}
	if enabled && configured == 0 {
		status = "idle"
	}
	if strings.HasPrefix(reason, "blocked:") || strings.HasPrefix(reason, "warn:") {
		status = "warn"
	}
	return deferredInitTask{
		Name:        name,
		Status:      status,
		Enabled:     enabled,
		Configured:  configured,
		Description: description,
		Reason:      strings.TrimSpace(reason),
		Evidence:    compactEvidence(evidence),
	}
}

func deferredInitReason(trusted bool, configReady bool, warning string, configured bool) string {
	if !trusted {
		return "workspace_not_trusted"
	}
	if !configReady {
		return "blocked:config_not_ready"
	}
	if strings.TrimSpace(warning) != "" {
		return "warn:" + strings.TrimSpace(warning)
	}
	if !configured {
		return "not_configured"
	}
	return "ready"
}

func deferredInitInvalidReason(kind string, count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", kind, count)
}

func deferredInitStatus(tasks []deferredInitTask, configLoadError string) string {
	if strings.TrimSpace(configLoadError) != "" {
		return "warn"
	}
	enabled := false
	for _, task := range tasks {
		if task.Status == "warn" {
			return "warn"
		}
		if task.Enabled {
			enabled = true
		}
	}
	if enabled {
		return "ready"
	}
	return "skipped"
}

func deferredInitMessage(report deferredInitReport) string {
	switch report.Status {
	case "ready":
		return "trust-gated deferred init is ready"
	case "warn":
		return "deferred init has warnings"
	default:
		return "deferred init is skipped until the workspace is trusted or configured"
	}
}

func renderDeferredInitText(out io.Writer, report deferredInitReport) {
	fmt.Fprintln(out, "Deferred Init")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Trusted          %t (%s)\n", report.Trusted, report.TrustReason)
	fmt.Fprintf(out, "  Executed         %t\n", report.Executed)
	fmt.Fprintf(out, "  Plugin init      %t\n", report.PluginInit)
	fmt.Fprintf(out, "  Skill init       %t\n", report.SkillInit)
	fmt.Fprintf(out, "  MCP prefetch     %t\n", report.MCPPrefetch)
	fmt.Fprintf(out, "  Session hooks    %t\n", report.SessionHooks)
	if report.Prefetch != nil {
		fmt.Fprintf(out, "  Prefetch         %s (%d tasks)\n", report.Prefetch.Status, report.Prefetch.TaskCount)
	}
	if report.ConfigLoadError != "" {
		fmt.Fprintf(out, "  Config load      degraded: %s\n", report.ConfigLoadError)
	}
	fmt.Fprintln(out, "  Tasks")
	for _, task := range report.Tasks {
		fmt.Fprintf(out, "    - %-22s %-7s configured=%d enabled=%t\n", task.Name, task.Status, task.Configured, task.Enabled)
		if task.Reason != "" {
			fmt.Fprintf(out, "      reason=%s\n", task.Reason)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type prefetchReport struct {
	Kind       string         `json:"kind"`
	Action     string         `json:"action"`
	Status     string         `json:"status"`
	Version    string         `json:"version"`
	Workspace  string         `json:"workspace"`
	StartedAt  string         `json:"started_at"`
	DurationMS int64          `json:"duration_ms"`
	TaskCount  int            `json:"task_count"`
	Tasks      []prefetchTask `json:"tasks"`
	Message    string         `json:"message,omitempty"`
}

type prefetchTask struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Started    bool           `json:"started"`
	DurationMS int64          `json:"duration_ms"`
	Detail     string         `json:"detail,omitempty"`
	Error      string         `json:"error,omitempty"`
	Evidence   map[string]any `json:"evidence,omitempty"`
}

type prefetchTaskResult struct {
	Status   string
	Detail   string
	Evidence map[string]any
}

func (a *App) Prefetch(args []string) error {
	action, format, err := parsePrefetchArgs(args)
	if err != nil {
		return err
	}
	report := a.buildPrefetchReport(action)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPrefetchText(a.Out, report)
	return nil
}

func parsePrefetchArgs(args []string) (string, string, error) {
	action := "run"
	format := "text"
	usage := "codog prefetch [run|status] [--json|--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
			continue
		case arg == "run" || arg == "status":
			action = arg
		case arg == "check":
			action = "status"
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", "", missingFlagValueError{Command: "prefetch", Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", unknownOptionError{Command: "prefetch", Option: arg, Usage: usage}
			}
			return "", "", fmt.Errorf("prefetch action must be run or status")
		}
	}
	switch format {
	case "text", "json":
		return action, format, nil
	default:
		return "", "", outputFormatError{Command: "prefetch", Value: format, Expected: []string{"text", "json"}}
	}
}

func (a *App) buildPrefetchReport(action string) prefetchReport {
	started := time.Now().UTC()
	tasks := []prefetchTask{
		a.runPrefetchTask("project_scan", a.prefetchProjectScan),
		a.runPrefetchTask("config_probe", a.prefetchConfigProbe),
		a.runPrefetchTask("memory_scan", a.prefetchMemoryScan),
		a.runPrefetchTask("mcp_prefetch", a.prefetchMCPValidation),
		a.runPrefetchTask("plugin_scan", a.prefetchPluginScan),
		a.runPrefetchTask("session_store", a.prefetchSessionStore),
	}
	report := prefetchReport{
		Kind:       "prefetch",
		Action:     action,
		Version:    version,
		Workspace:  a.Workspace,
		StartedAt:  started.Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(),
		TaskCount:  len(tasks),
		Tasks:      tasks,
	}
	report.Status = prefetchStatus(tasks)
	report.Message = prefetchMessage(report.Status)
	return report
}

func (a *App) runPrefetchTask(name string, fn func() (prefetchTaskResult, error)) prefetchTask {
	started := time.Now()
	result, err := fn()
	status := strings.TrimSpace(result.Status)
	if status == "" {
		status = "ok"
	}
	task := prefetchTask{
		Name:       name,
		Status:     status,
		Started:    true,
		DurationMS: time.Since(started).Milliseconds(),
		Detail:     strings.TrimSpace(result.Detail),
		Evidence:   compactEvidence(result.Evidence),
	}
	if err != nil {
		task.Status = "error"
		task.Error = strings.TrimSpace(err.Error())
	}
	return task
}

func (a *App) prefetchProjectScan() (prefetchTaskResult, error) {
	const maxEntries = 5000
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	var files, dirs int
	var bytesSeen int64
	truncated := false
	err := filepath.WalkDir(workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if files+dirs >= maxEntries {
			truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != workspace && prefetchIgnoredDir(entry.Name()) {
				return filepath.SkipDir
			}
			dirs++
			return nil
		}
		files++
		if info, err := entry.Info(); err == nil {
			bytesSeen += info.Size()
		}
		return nil
	})
	return prefetchTaskResult{
		Detail: "scanned workspace files for startup context",
		Evidence: map[string]any{
			"file_count": files,
			"dir_count":  dirs,
			"bytes":      bytesSeen,
			"truncated":  truncated,
			"limit":      maxEntries,
		},
	}, err
}

func prefetchIgnoredDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", "vendor", "target", "dist", "build", ".cache", ".next":
		return true
	default:
		return false
	}
}

func (a *App) prefetchConfigProbe() (prefetchTaskResult, error) {
	status := "ok"
	detail := "loaded effective config and runtime preferences"
	evidence := map[string]any{
		"config_home":     a.Config.ConfigHome,
		"model":           a.Config.Model,
		"permission_mode": a.Config.PermissionMode,
		"mcp_servers":     len(a.Config.MCPServers),
		"enabled_skills":  len(a.Config.EnabledSkills),
		"hooks_disabled":  a.Config.EffectiveDisableAllHooks(),
	}
	if strings.TrimSpace(a.ConfigLoadError) != "" {
		status = "warn"
		detail = "config load failed; using defaults for remaining prefetch checks"
		evidence["config_load_error"] = strings.TrimSpace(a.ConfigLoadError)
		evidence["config_load_error_kind"] = strings.TrimSpace(a.ConfigLoadErrorKind)
	}
	return prefetchTaskResult{
		Status:   status,
		Detail:   detail,
		Evidence: evidence,
	}, nil
}

func (a *App) prefetchMemoryScan() (prefetchTaskResult, error) {
	files, err := memory.DiscoverWithRulesImport(a.Workspace, a.memoryRulesImportOptions())
	if err != nil {
		return prefetchTaskResult{}, err
	}
	chars := 0
	words := 0
	bytesSeen := int64(0)
	empty := 0
	truncated := 0
	oldestAge := int64(0)
	for _, summary := range memory.SummariesAt(files, time.Now()) {
		chars += summary.Chars
		words += summary.Words
		bytesSeen += summary.SizeBytes
		if summary.Empty {
			empty++
		}
		if summary.Truncated {
			truncated++
		}
		if summary.AgeSeconds > oldestAge {
			oldestAge = summary.AgeSeconds
		}
	}
	return prefetchTaskResult{
		Detail: "loaded project memory candidates",
		Evidence: map[string]any{
			"instruction_files":  len(files),
			"chars":              chars,
			"words":              words,
			"bytes":              bytesSeen,
			"empty_files":        empty,
			"truncated_files":    truncated,
			"oldest_age_seconds": oldestAge,
		},
	}, nil
}

func (a *App) prefetchMCPValidation() (prefetchTaskResult, error) {
	validation := buildMCPValidation(a.Config.MCPServers)
	status := "ok"
	detail := "validated configured MCP server launch metadata"
	if validation.InvalidCount > 0 {
		status = "warn"
		detail = "some MCP server definitions are invalid"
	}
	return prefetchTaskResult{
		Status: status,
		Detail: detail,
		Evidence: map[string]any{
			"configured_count": validation.TotalConfigured,
			"valid_count":      validation.ValidCount,
			"invalid_count":    validation.InvalidCount,
			"required_count":   validation.RequiredCount,
			"optional_count":   validation.OptionalCount,
		},
	}, nil
}

func (a *App) prefetchPluginScan() (prefetchTaskResult, error) {
	manifests, err := a.runtimePluginManifests()
	if err != nil {
		return prefetchTaskResult{}, err
	}
	enabled := 0
	toolsCount := 0
	commandsCount := 0
	for _, manifest := range manifests {
		if manifest.Enabled {
			enabled++
		}
		toolsCount += len(manifest.Tools)
		commandsCount += len(manifest.Commands)
	}
	return prefetchTaskResult{
		Detail: "loaded local plugin manifests",
		Evidence: map[string]any{
			"installed_count": len(manifests),
			"enabled_count":   enabled,
			"tool_count":      toolsCount,
			"command_count":   commandsCount,
		},
	}, nil
}

func (a *App) prefetchSessionStore() (prefetchTaskResult, error) {
	if a.Sessions == nil {
		return prefetchTaskResult{Status: "warn", Detail: "session store is not configured"}, nil
	}
	sessions, err := a.Sessions.List()
	if err != nil {
		return prefetchTaskResult{}, err
	}
	return prefetchTaskResult{
		Detail: "opened session store and listed saved transcripts",
		Evidence: map[string]any{
			"session_count": len(sessions),
		},
	}, nil
}

func prefetchStatus(tasks []prefetchTask) string {
	status := "ok"
	for _, task := range tasks {
		switch task.Status {
		case "error":
			return "warn"
		case "warn":
			status = "warn"
		}
	}
	return status
}

func prefetchMessage(status string) string {
	if status == "ok" {
		return "local startup prefetch completed"
	}
	return "local startup prefetch completed with warnings"
}

func renderPrefetchText(out io.Writer, report prefetchReport) {
	fmt.Fprintln(out, "Prefetch")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	fmt.Fprintf(out, "  Tasks            %d\n", report.TaskCount)
	for _, task := range report.Tasks {
		fmt.Fprintf(out, "    - %-18s %-5s %dms\n", task.Name, task.Status, task.DurationMS)
		if task.Detail != "" {
			fmt.Fprintf(out, "      %s\n", task.Detail)
		}
		if task.Error != "" {
			fmt.Fprintf(out, "      error=%s\n", task.Error)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) Status(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("status", args)
	if err != nil {
		return err
	}
	formatSource, formatRaw, formatOverridden := statusOutputFormatProvenance(args, overrides)
	var active *session.Session
	sessionRef := overrides.Resume
	if sessionRef == "" {
		sessionRef = overrides.SessionID
	}
	if sessionRef != "" && a.Sessions != nil {
		if strings.TrimSpace(overrides.Resume) != "" {
			active, err = a.Sessions.OpenExisting(sessionRef)
		} else {
			active, err = a.Sessions.Open(sessionRef)
		}
		if err != nil {
			return renderSessionRestoreError(a.Out, "status", sessionRef, err, format)
		}
	}
	allowedToolSource := ""
	if len(overrides.AllowedTools) != 0 {
		allowedToolSource = "flag"
	}
	a.renderStatus(format, active, allowedToolSource, formatSource, formatRaw, formatOverridden, overrides.ConfigPath)
	return nil
}

func statusOutputFormatProvenance(args []string, overrides config.FlagOverrides) (source string, raw string, overridden bool) {
	envValue := strings.TrimSpace(os.Getenv("CODOG_OUTPUT_FORMAT"))
	if !overrides.OutputFormatSubcommandExplicit && strings.TrimSpace(overrides.OutputFormatSource) != "" {
		return overrides.OutputFormatSource, overrides.OutputFormatRaw, overrides.OutputFormatOverridden
	}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "--json":
			return "flag", "json", envValue != "" || overrides.OutputFormatSource == "env"
		case arg == "--output-format" || arg == "-o":
			index++
			if index < len(args) {
				return "flag", strings.TrimSpace(args[index]), envValue != "" || overrides.OutputFormatSource == "env"
			}
			return "flag", "", envValue != "" || overrides.OutputFormatSource == "env"
		case strings.HasPrefix(arg, "--output-format="):
			return "flag", strings.TrimSpace(strings.TrimPrefix(arg, "--output-format=")), envValue != "" || overrides.OutputFormatSource == "env"
		}
	}
	if strings.TrimSpace(overrides.OutputFormatSource) != "" {
		return overrides.OutputFormatSource, overrides.OutputFormatRaw, overrides.OutputFormatOverridden
	}
	if envValue != "" {
		return "env", envValue, false
	}
	return "default", "", false
}

type statuslineReport struct {
	Kind                  string                   `json:"kind"`
	Line                  string                   `json:"line"`
	Status                string                   `json:"status"`
	Source                string                   `json:"source"`
	Workspace             string                   `json:"workspace"`
	Model                 string                   `json:"model"`
	FastMode              bool                     `json:"fast_mode"`
	PermissionMode        string                   `json:"permission_mode"`
	SessionActive         bool                     `json:"session_active"`
	SessionID             string                   `json:"session_id,omitempty"`
	SessionMessages       int                      `json:"session_messages,omitempty"`
	SessionCount          int                      `json:"session_count"`
	GitAvailable          bool                     `json:"git_available"`
	GitBranch             string                   `json:"git_branch,omitempty"`
	GitClean              bool                     `json:"git_clean"`
	GitDirty              bool                     `json:"git_dirty"`
	GitConflicts          int                      `json:"git_conflicts,omitempty"`
	PlanActive            bool                     `json:"plan_active"`
	ClaudeStatuslineInput *claudeStatuslineContext `json:"claude_statusline_input,omitempty"`
}

const statusLineCommandTimeout = 5 * time.Second

func (a *App) Statusline(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("statusline", args)
	if err != nil {
		return err
	}
	active, err := a.contextSession(overrides)
	if err != nil {
		return err
	}
	report := buildStatuslineReport(a.statusSnapshot(active))
	claudeInput, ok, err := readClaudeStatuslineInput(a.In)
	if err != nil {
		return err
	}
	if ok {
		report = report.withClaudeStatuslineInput(claudeInput)
	}
	if line, ok := a.runConfiguredStatusLine(report, claudeInput, ok); ok {
		report.Line = line
		report.Source = "statusLine_command"
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, report.Line)
	return nil
}

type claudeStatuslineInput struct {
	SessionID       string                        `json:"session_id"`
	SessionName     string                        `json:"session_name"`
	TranscriptPath  string                        `json:"transcript_path"`
	CWD             string                        `json:"cwd"`
	PermissionMode  string                        `json:"permission_mode"`
	Model           claudeStatuslineModel         `json:"model"`
	Workspace       claudeStatuslineWorkspace     `json:"workspace"`
	Version         string                        `json:"version"`
	OutputStyle     claudeStatuslineOutputStyle   `json:"output_style"`
	Cost            claudeStatuslineCost          `json:"cost"`
	ContextWindow   claudeStatuslineContextWindow `json:"context_window"`
	RateLimits      claudeStatuslineRateLimits    `json:"rate_limits"`
	Vim             claudeStatuslineVim           `json:"vim"`
	Agent           claudeStatuslineAgent         `json:"agent"`
	Worktree        claudeStatuslineWorktree      `json:"worktree"`
	Remote          map[string]any                `json:"remote"`
	Exceeds200K     bool                          `json:"exceeds_200k_tokens"`
	AdditionalInput map[string]json.RawMessage    `json:"-"`
}

type claudeStatuslineModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type claudeStatuslineWorkspace struct {
	CurrentDir string   `json:"current_dir"`
	ProjectDir string   `json:"project_dir"`
	AddedDirs  []string `json:"added_dirs"`
}

type claudeStatuslineOutputStyle struct {
	Name string `json:"name"`
}

type claudeStatuslineCost struct {
	TotalCostUSD       *float64 `json:"total_cost_usd,omitempty"`
	TotalDurationMS    int64    `json:"total_duration_ms,omitempty"`
	TotalAPIDurationMS int64    `json:"total_api_duration_ms,omitempty"`
	TotalLinesAdded    int      `json:"total_lines_added,omitempty"`
	TotalLinesRemoved  int      `json:"total_lines_removed,omitempty"`
}

type claudeStatuslineContextWindow struct {
	TotalInputTokens    int                           `json:"total_input_tokens,omitempty"`
	TotalOutputTokens   int                           `json:"total_output_tokens,omitempty"`
	ContextWindowSize   int                           `json:"context_window_size,omitempty"`
	CurrentUsage        *claudeStatuslineCurrentUsage `json:"current_usage,omitempty"`
	UsedPercentage      *float64                      `json:"used_percentage,omitempty"`
	RemainingPercentage *float64                      `json:"remaining_percentage,omitempty"`
}

type claudeStatuslineCurrentUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type claudeStatuslineRateLimits struct {
	FiveHour *claudeStatuslineRateLimit `json:"five_hour,omitempty"`
	SevenDay *claudeStatuslineRateLimit `json:"seven_day,omitempty"`
}

type claudeStatuslineRateLimit struct {
	UsedPercentage float64 `json:"used_percentage,omitempty"`
	ResetsAt       int64   `json:"resets_at,omitempty"`
}

type claudeStatuslineVim struct {
	Mode string `json:"mode"`
}

type claudeStatuslineAgent struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type claudeStatuslineWorktree struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	OriginalCWD    string `json:"original_cwd"`
	OriginalBranch string `json:"original_branch"`
}

type claudeStatuslineContext struct {
	SessionID             string                     `json:"session_id,omitempty"`
	SessionName           string                     `json:"session_name,omitempty"`
	TranscriptPath        string                     `json:"transcript_path,omitempty"`
	CWD                   string                     `json:"cwd,omitempty"`
	PermissionMode        string                     `json:"permission_mode,omitempty"`
	ModelID               string                     `json:"model_id,omitempty"`
	ModelDisplayName      string                     `json:"model_display_name,omitempty"`
	WorkspaceCurrentDir   string                     `json:"workspace_current_dir,omitempty"`
	WorkspaceProjectDir   string                     `json:"workspace_project_dir,omitempty"`
	WorkspaceAddedDirs    []string                   `json:"workspace_added_dirs,omitempty"`
	Version               string                     `json:"version,omitempty"`
	OutputStyle           string                     `json:"output_style,omitempty"`
	TotalCostUSD          *float64                   `json:"total_cost_usd,omitempty"`
	TotalInputTokens      int                        `json:"total_input_tokens,omitempty"`
	TotalOutputTokens     int                        `json:"total_output_tokens,omitempty"`
	ContextWindowSize     int                        `json:"context_window_size,omitempty"`
	ContextUsedPercentage *float64                   `json:"context_used_percentage,omitempty"`
	ContextRemainingPct   *float64                   `json:"context_remaining_percentage,omitempty"`
	RateLimits            claudeStatuslineRateLimits `json:"rate_limits,omitempty"`
	VimMode               string                     `json:"vim_mode,omitempty"`
	AgentName             string                     `json:"agent_name,omitempty"`
	AgentType             string                     `json:"agent_type,omitempty"`
	WorktreeName          string                     `json:"worktree_name,omitempty"`
	WorktreePath          string                     `json:"worktree_path,omitempty"`
	WorktreeBranch        string                     `json:"worktree_branch,omitempty"`
	Exceeds200K           bool                       `json:"exceeds_200k_tokens,omitempty"`
}

type terminalSetupRequest struct {
	Action string
	Format string
	Shell  string
	Target string
	Path   string
	Force  bool
}

type setupRequest struct {
	Action         string
	Format         string
	TerminalAction string
	Shell          string
	Target         string
	Path           string
	Force          bool
}

type setupCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type setupReport struct {
	Kind       string                `json:"kind"`
	Action     string                `json:"action"`
	Status     string                `json:"status"`
	Workspace  string                `json:"workspace"`
	ConfigHome string                `json:"config_home,omitempty"`
	ConfigPath string                `json:"config_path,omitempty"`
	Checks     []setupCheck          `json:"checks"`
	Project    *projectinit.Report   `json:"project,omitempty"`
	Terminal   *terminalsetup.Report `json:"terminal,omitempty"`
	Messages   []string              `json:"messages,omitempty"`
}

type onboardingRequest struct {
	Format string
	Path   string
}

func (a *App) Onboarding(args []string) error {
	req, err := parseOnboardingArgs(args)
	if err != nil {
		return err
	}
	workspace := a.Workspace
	if strings.TrimSpace(req.Path) != "" {
		workspace = a.resolveOutputPath(req.Path)
	}
	report, err := onboarding.Analyze(onboarding.Options{Workspace: workspace})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return onboarding.RenderJSON(a.Out, report)
	}
	onboarding.RenderText(a.Out, report)
	return nil
}

func parseOnboardingArgs(args []string) (onboardingRequest, error) {
	const usage = "codog onboarding [--path PATH|--workspace PATH] [--json|--output-format text|json]"
	req := onboardingRequest{Format: "text"}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "onboarding", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--path" || arg == "--workspace":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "onboarding", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "--workspace="):
			req.Path = strings.TrimPrefix(arg, "--workspace=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "onboarding", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "onboarding", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("onboarding", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) Setup(ctx context.Context, args []string) error {
	req, err := parseSetupArgs(args)
	if err != nil {
		return err
	}
	report, err := a.buildSetupReport(ctx, req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSetupReport(a.Out, report)
	return nil
}

func parseSetupArgs(args []string) (setupRequest, error) {
	const usage = "codog setup [status|init|terminal [ACTION]|all] [--shell SHELL] [--target shell|vscode|cursor|windsurf|zed|alacritty|apple-terminal] [--path PATH] [--force] [--json|--output-format text|json]"
	req := setupRequest{Action: "status", Format: "text", TerminalAction: "status"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "setup", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--shell":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "setup", Flag: arg, Usage: usage}
			}
			req.Shell = args[index]
		case strings.HasPrefix(arg, "--shell="):
			req.Shell = strings.TrimPrefix(arg, "--shell=")
		case arg == "--target":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "setup", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "setup", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--force":
			req.Force = true
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "setup", Option: arg, Usage: usage}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("setup", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "check", "checks", "diagnose", "doctor":
		req.Action = "status"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "setup status", Args: rest[1:], Usage: usage}
		}
	case "init", "project":
		req.Action = "init"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "setup init", Args: rest[1:], Usage: usage}
		}
	case "terminal", "shell":
		req.Action = "terminal"
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{Command: "setup terminal", Args: rest[2:], Usage: usage}
		}
		if len(rest) == 2 {
			req.TerminalAction = strings.ToLower(rest[1])
		}
	case "all", "run":
		req.Action = "all"
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "setup all", Args: rest[1:], Usage: usage}
		}
	default:
		return req, unexpectedExtraArgsError{Command: "setup", Args: []string{rest[0]}, Usage: usage}
	}
	return req, nil
}

func (a *App) buildSetupReport(ctx context.Context, req setupRequest) (setupReport, error) {
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err == nil {
		workspace = absWorkspace
	}
	report := setupReport{
		Kind:       "setup",
		Action:     req.Action,
		Status:     "ok",
		Workspace:  workspace,
		ConfigHome: strings.TrimSpace(a.Config.ConfigHome),
		ConfigPath: setupConfigPath(a.Config.ConfigHome),
	}
	if req.Action == "init" || req.Action == "all" {
		project, err := projectinit.Initialize(workspace)
		if err != nil {
			return setupReport{}, err
		}
		if err := a.runSetupHook(ctx, "setup", project.Status); err != nil {
			return setupReport{}, err
		}
		report.Project = &project
	}
	if req.Action == "status" || req.Action == "terminal" || req.Action == "all" {
		terminalAction := req.TerminalAction
		if req.Action != "terminal" {
			terminalAction = "status"
		}
		terminal, err := terminalsetup.Run(terminalsetup.Options{
			Action: terminalAction,
			Shell:  req.Shell,
			Target: req.Target,
			Path:   req.Path,
			Force:  req.Force,
		})
		if err != nil {
			return setupReport{}, err
		}
		report.Terminal = &terminal
	}
	report.Checks = a.setupChecks(report.Terminal)
	report.Status = setupStatus(report.Checks)
	report.Messages = setupMessages(report)
	return report, nil
}

func setupConfigPath(configHome string) string {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		return ""
	}
	return filepath.Join(configHome, "config.json")
}

func (a *App) setupChecks(terminal *terminalsetup.Report) []setupCheck {
	checks := []setupCheck{}
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	if info, err := os.Stat(workspace); err != nil {
		checks = append(checks, setupCheck{Name: "Workspace", Status: "fail", Message: err.Error()})
	} else if !info.IsDir() {
		checks = append(checks, setupCheck{Name: "Workspace", Status: "fail", Message: "workspace is not a directory"})
	} else {
		checks = append(checks, setupCheck{Name: "Workspace", Status: "ok", Message: workspace})
	}

	configHome := strings.TrimSpace(a.Config.ConfigHome)
	switch {
	case configHome == "":
		checks = append(checks, setupCheck{Name: "Config home", Status: "warn", Message: "config home is not configured"})
	default:
		if info, err := os.Stat(configHome); err == nil && info.IsDir() {
			checks = append(checks, setupCheck{Name: "Config home", Status: "ok", Message: configHome})
		} else if err != nil && os.IsNotExist(err) {
			checks = append(checks, setupCheck{Name: "Config home", Status: "warn", Message: "config home will be created when Codog writes preferences"})
		} else if err != nil {
			checks = append(checks, setupCheck{Name: "Config home", Status: "fail", Message: err.Error()})
		} else {
			checks = append(checks, setupCheck{Name: "Config home", Status: "fail", Message: "config home path is not a directory"})
		}
	}

	if strings.TrimSpace(a.Config.APIKey) != "" || strings.TrimSpace(a.Config.AuthToken) != "" {
		checks = append(checks, setupCheck{Name: "Provider credentials", Status: "ok", Message: "provider credentials are configured"})
	} else {
		checks = append(checks, setupCheck{Name: "Provider credentials", Status: "warn", Message: "set ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, or a provider-specific API key before running provider requests"})
	}
	if strings.TrimSpace(a.Config.Model) != "" {
		checks = append(checks, setupCheck{Name: "Model", Status: "ok", Message: a.Config.Model})
	} else {
		checks = append(checks, setupCheck{Name: "Model", Status: "warn", Message: "model is not configured"})
	}

	instructionsPath := filepath.Join(workspace, ".codog", "instructions.md")
	configPath := filepath.Join(workspace, ".codog.json")
	agentsPath := filepath.Join(workspace, "AGENTS.md")
	claudePath := filepath.Join(workspace, "CLAUDE.md")
	instructionsOK := fileExists(instructionsPath)
	projectConfigOK := fileExists(configPath)
	agentsOK := fileExists(agentsPath)
	claudeOK := fileExists(claudePath)
	switch {
	case instructionsOK && projectConfigOK && agentsOK && claudeOK:
		checks = append(checks, setupCheck{Name: "Project memory", Status: "ok", Message: ".codog/instructions.md, .codog.json, AGENTS.md, and CLAUDE.md are present"})
	case instructionsOK || projectConfigOK || agentsOK || claudeOK:
		checks = append(checks, setupCheck{Name: "Project memory", Status: "warn", Message: "project setup is partial; run `codog setup init`"})
	default:
		checks = append(checks, setupCheck{Name: "Project memory", Status: "warn", Message: "run `codog setup init` to create project guidance and shared defaults"})
	}

	if terminal != nil {
		if terminal.Installed {
			checks = append(checks, setupCheck{Name: "Terminal integration", Status: "ok", Message: terminal.Message})
		} else {
			checks = append(checks, setupCheck{Name: "Terminal integration", Status: "warn", Message: "run `codog terminal-setup install` to install shell helpers"})
		}
	}
	return checks
}

func setupStatus(checks []setupCheck) string {
	status := "ok"
	for _, check := range checks {
		switch check.Status {
		case "fail", "error":
			return "error"
		case "warn":
			if status == "ok" {
				status = "warn"
			}
		}
	}
	return status
}

func setupMessages(report setupReport) []string {
	messages := []string{}
	for _, check := range report.Checks {
		if check.Status == "warn" || check.Status == "fail" || check.Status == "error" {
			messages = append(messages, check.Name+": "+check.Message)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "Codog local setup looks ready.")
	}
	return messages
}

func renderSetupReport(out io.Writer, report setupReport) {
	fmt.Fprintln(out, "Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Workspace        %s\n", report.Workspace)
	if report.ConfigHome != "" {
		fmt.Fprintf(out, "  Config home      %s\n", report.ConfigHome)
	}
	if report.ConfigPath != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.ConfigPath)
	}
	fmt.Fprintln(out, "  Checks")
	for _, check := range report.Checks {
		fmt.Fprintf(out, "    %-22s %-5s %s\n", check.Name, check.Status, check.Message)
	}
	if report.Project != nil {
		fmt.Fprintln(out, "  Project init")
		for _, artifact := range report.Project.Artifacts {
			fmt.Fprintf(out, "    %-22s %s\n", artifact.Name, artifact.Status)
		}
	}
	if report.Terminal != nil {
		fmt.Fprintln(out, "  Terminal")
		fmt.Fprintf(out, "    Shell                 %s\n", report.Terminal.Shell)
		if report.Terminal.Path != "" {
			fmt.Fprintf(out, "    Path                  %s\n", report.Terminal.Path)
		}
		fmt.Fprintf(out, "    Installed             %t\n", report.Terminal.Installed)
		if report.Terminal.Changed {
			fmt.Fprintf(out, "    Changed               %t\n", report.Terminal.Changed)
		}
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Message          %s\n", message)
	}
}

func (a *App) TerminalSetup(args []string) error {
	req, err := parseTerminalSetupArgs(args)
	if err != nil {
		return err
	}
	report, err := terminalsetup.Run(terminalsetup.Options{
		Action: req.Action,
		Shell:  req.Shell,
		Target: req.Target,
		Path:   req.Path,
		Force:  req.Force,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTerminalSetupReport(a.Out, report)
	return nil
}

func parseTerminalSetupArgs(args []string) (terminalSetupRequest, error) {
	const usage = "codog terminal-setup [status|install|uninstall|snippet] [--shell SHELL] [--target shell|vscode|cursor|windsurf|zed|alacritty|apple-terminal] [--path PATH] [--force] [--json|--output-format text|json]"
	req := terminalSetupRequest{Action: "status", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "terminal-setup", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--shell":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "terminal-setup", Flag: arg, Usage: usage}
			}
			req.Shell = args[index]
		case strings.HasPrefix(arg, "--shell="):
			req.Shell = strings.TrimPrefix(arg, "--shell=")
		case arg == "--target":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "terminal-setup", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "terminal-setup", Flag: arg, Usage: usage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--force":
			req.Force = true
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "terminal-setup", Option: arg, Usage: usage}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("terminal-setup", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "terminal-setup", Args: rest[1:], Usage: usage}
	}
	if len(rest) == 1 {
		req.Action = strings.ToLower(rest[0])
	}
	return req, nil
}

func renderTerminalSetupReport(out io.Writer, report terminalsetup.Report) {
	fmt.Fprintln(out, "Terminal Setup")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Target != "" {
		fmt.Fprintf(out, "  Target           %s\n", report.Target)
	}
	if report.Shell != "" {
		fmt.Fprintf(out, "  Shell            %s\n", report.Shell)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Path             %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Installed        %t\n", report.Installed)
	if report.Action == "install" || report.Action == "uninstall" {
		fmt.Fprintf(out, "  Changed          %t\n", report.Changed)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
	if report.Action == "snippet" && report.Snippet != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, report.Snippet)
	}
}

func buildStatuslineReport(snapshot localstatus.Snapshot) statuslineReport {
	workspace := snapshot.Workspace.Name
	if strings.TrimSpace(workspace) == "" {
		workspace = filepath.Base(snapshot.Workspace.Path)
	}
	if workspace == "." || workspace == string(filepath.Separator) {
		workspace = "workspace"
	}
	gitLabel := "no-git"
	gitDirty := false
	if snapshot.Git.Available {
		gitLabel = emptyAs(snapshot.Git.Branch, "detached")
		switch {
		case snapshot.Git.Conflicts > 0:
			gitLabel += "!"
			gitDirty = true
		case !snapshot.Git.Clean:
			gitLabel += "*"
			gitDirty = true
		}
	}
	sessionLabel := fmt.Sprintf("sessions=%d", snapshot.Session.SavedCount)
	if snapshot.Session.Active {
		sessionLabel = fmt.Sprintf("session=%s(%d)", snapshot.Session.ID, snapshot.Session.MessageCount)
	}
	planLabel := "plan=off"
	if snapshot.Plan.Active {
		planLabel = "plan=on"
	}
	fastLabel := "fast=off"
	if snapshot.Config.FastMode {
		fastLabel = "fast=on"
	}
	line := strings.Join([]string{
		"codog",
		workspace,
		gitLabel,
		emptyAs(snapshot.Config.Model, "model=unset"),
		fastLabel,
		emptyAs(snapshot.Config.PermissionMode, "permission=unset"),
		sessionLabel,
		planLabel,
	}, " ")
	return statuslineReport{
		Kind:            "statusline",
		Line:            line,
		Status:          snapshot.Status,
		Source:          "codog",
		Workspace:       workspace,
		Model:           snapshot.Config.Model,
		FastMode:        snapshot.Config.FastMode,
		PermissionMode:  snapshot.Config.PermissionMode,
		SessionActive:   snapshot.Session.Active,
		SessionID:       snapshot.Session.ID,
		SessionMessages: snapshot.Session.MessageCount,
		SessionCount:    snapshot.Session.SavedCount,
		GitAvailable:    snapshot.Git.Available,
		GitBranch:       snapshot.Git.Branch,
		GitClean:        snapshot.Git.Clean,
		GitDirty:        gitDirty,
		GitConflicts:    snapshot.Git.Conflicts,
		PlanActive:      snapshot.Plan.Active,
	}
}

func readClaudeStatuslineInput(in io.Reader) (claudeStatuslineInput, bool, error) {
	data, nonTerminal, err := readPromptInputState(in)
	if err != nil {
		return claudeStatuslineInput{}, false, err
	}
	if !nonTerminal || strings.TrimSpace(data) == "" {
		return claudeStatuslineInput{}, false, nil
	}
	input, ok := parseClaudeStatuslineInput([]byte(data))
	return input, ok, nil
}

func parseClaudeStatuslineInput(data []byte) (claudeStatuslineInput, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) == 0 {
		return claudeStatuslineInput{}, false
	}
	if !looksLikeClaudeStatuslineInput(raw) {
		return claudeStatuslineInput{}, false
	}
	var input claudeStatuslineInput
	if err := json.Unmarshal(data, &input); err != nil {
		return claudeStatuslineInput{}, false
	}
	input.AdditionalInput = raw
	return input, true
}

func looksLikeClaudeStatuslineInput(raw map[string]json.RawMessage) bool {
	if _, ok := raw["transcript_path"]; ok {
		return true
	}
	if _, ok := raw["context_window"]; ok {
		return true
	}
	if _, ok := raw["rate_limits"]; ok {
		return true
	}
	if _, ok := raw["output_style"]; ok {
		return true
	}
	if _, ok := raw["workspace"]; ok {
		if _, hasSession := raw["session_id"]; hasSession {
			return true
		}
		if _, hasCWD := raw["cwd"]; hasCWD {
			return true
		}
	}
	return false
}

func (report statuslineReport) withClaudeStatuslineInput(input claudeStatuslineInput) statuslineReport {
	ctx := claudeStatuslineContext{
		SessionID:             strings.TrimSpace(input.SessionID),
		SessionName:           strings.TrimSpace(input.SessionName),
		TranscriptPath:        strings.TrimSpace(input.TranscriptPath),
		CWD:                   strings.TrimSpace(input.CWD),
		PermissionMode:        strings.TrimSpace(input.PermissionMode),
		ModelID:               strings.TrimSpace(input.Model.ID),
		ModelDisplayName:      strings.TrimSpace(input.Model.DisplayName),
		WorkspaceCurrentDir:   strings.TrimSpace(input.Workspace.CurrentDir),
		WorkspaceProjectDir:   strings.TrimSpace(input.Workspace.ProjectDir),
		WorkspaceAddedDirs:    cleanedStrings(input.Workspace.AddedDirs),
		Version:               strings.TrimSpace(input.Version),
		OutputStyle:           strings.TrimSpace(input.OutputStyle.Name),
		TotalCostUSD:          input.Cost.TotalCostUSD,
		TotalInputTokens:      input.ContextWindow.TotalInputTokens,
		TotalOutputTokens:     input.ContextWindow.TotalOutputTokens,
		ContextWindowSize:     input.ContextWindow.ContextWindowSize,
		ContextUsedPercentage: input.ContextWindow.UsedPercentage,
		ContextRemainingPct:   input.ContextWindow.RemainingPercentage,
		RateLimits:            input.RateLimits,
		VimMode:               strings.TrimSpace(input.Vim.Mode),
		AgentName:             strings.TrimSpace(input.Agent.Name),
		AgentType:             strings.TrimSpace(input.Agent.Type),
		WorktreeName:          strings.TrimSpace(input.Worktree.Name),
		WorktreePath:          strings.TrimSpace(input.Worktree.Path),
		WorktreeBranch:        strings.TrimSpace(input.Worktree.Branch),
		Exceeds200K:           input.Exceeds200K,
	}
	report.Source = "claude_statusline_stdin"
	report.ClaudeStatuslineInput = &ctx
	report.SessionActive = true
	if ctx.SessionID != "" {
		report.SessionID = ctx.SessionID
	}
	if ctx.PermissionMode != "" {
		report.PermissionMode = ctx.PermissionMode
	}
	if model := firstNonEmpty(ctx.ModelDisplayName, ctx.ModelID); model != "" {
		report.Model = model
	}
	if workspace := statuslineWorkspaceFromClaude(ctx); workspace != "" {
		report.Workspace = workspace
	}
	report.Line = renderClaudeStatuslineLine(report, ctx)
	return report
}

func statuslineWorkspaceFromClaude(ctx claudeStatuslineContext) string {
	path := firstNonEmpty(ctx.WorkspaceCurrentDir, ctx.CWD, ctx.WorkspaceProjectDir)
	if strings.TrimSpace(path) == "" {
		return ""
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return path
	}
	return base
}

func renderClaudeStatuslineLine(report statuslineReport, ctx claudeStatuslineContext) string {
	gitLabel := "no-git"
	if report.GitAvailable {
		gitLabel = emptyAs(report.GitBranch, "detached")
		switch {
		case report.GitConflicts > 0:
			gitLabel += "!"
		case report.GitDirty:
			gitLabel += "*"
		}
	}
	sessionLabel := "session=active"
	if sessionName := firstNonEmpty(ctx.SessionName, ctx.SessionID); sessionName != "" {
		sessionLabel = "session=" + sessionName
	}
	contextLabel := ""
	switch {
	case ctx.ContextRemainingPct != nil:
		contextLabel = fmt.Sprintf("context=%.0f%%-left", *ctx.ContextRemainingPct)
	case ctx.ContextUsedPercentage != nil:
		contextLabel = fmt.Sprintf("context=%.0f%%-used", *ctx.ContextUsedPercentage)
	case ctx.ContextWindowSize > 0 && (ctx.TotalInputTokens > 0 || ctx.TotalOutputTokens > 0):
		used := float64(ctx.TotalInputTokens+ctx.TotalOutputTokens) / float64(ctx.ContextWindowSize) * 100
		contextLabel = fmt.Sprintf("context=%.0f%%-used", used)
	}
	costLabel := ""
	if ctx.TotalCostUSD != nil {
		costLabel = fmt.Sprintf("cost=$%.4f", *ctx.TotalCostUSD)
	}
	agentLabel := ""
	if agent := firstNonEmpty(ctx.AgentName, ctx.AgentType); agent != "" {
		agentLabel = "agent=" + agent
	}
	worktreeLabel := ""
	if worktree := firstNonEmpty(ctx.WorktreeName, ctx.WorktreeBranch); worktree != "" {
		worktreeLabel = "worktree=" + worktree
	}
	parts := []string{
		"codog",
		emptyAs(report.Workspace, "workspace"),
		gitLabel,
		emptyAs(report.Model, "model=unset"),
		emptyAs(report.PermissionMode, "permission=unset"),
		sessionLabel,
		contextLabel,
		costLabel,
		agentLabel,
		worktreeLabel,
	}
	return strings.Join(nonEmptyStrings(parts), " ")
}

func (a *App) runConfiguredStatusLine(report statuslineReport, input claudeStatuslineInput, hasInput bool) (string, bool) {
	statusLine := a.Config.StatusLine
	if statusLine == nil || a.Config.EffectiveDisableAllHooks() {
		return "", false
	}
	if strings.TrimSpace(statusLine.Type) != "command" || strings.TrimSpace(statusLine.Command) == "" {
		return "", false
	}
	payload, err := a.statusLineCommandInput(report, input, hasInput)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusLineCommandTimeout)
	defer cancel()
	cmd := statusLineShellCommand(ctx, statusLine.Command)
	if workspace := strings.TrimSpace(a.Workspace); workspace != "" {
		cmd.Dir = workspace
	}
	cmd.Env = statusLineCommandEnv(a.Config.Env)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", false
	}
	output := normalizeStatusLineCommandOutput(stdout.String())
	if output == "" {
		return "", false
	}
	return output, true
}

func (a *App) statusLineCommandInput(report statuslineReport, input claudeStatuslineInput, hasInput bool) ([]byte, error) {
	if hasInput {
		return json.Marshal(input)
	}
	workspace := strings.TrimSpace(a.Workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	input = claudeStatuslineInput{
		SessionID:      strings.TrimSpace(report.SessionID),
		CWD:            workspace,
		PermissionMode: strings.TrimSpace(report.PermissionMode),
		Model: claudeStatuslineModel{
			ID:          strings.TrimSpace(report.Model),
			DisplayName: strings.TrimSpace(report.Model),
		},
		Workspace: claudeStatuslineWorkspace{
			CurrentDir: workspace,
			ProjectDir: workspace,
			AddedDirs:  cleanedStrings(a.Config.AdditionalDirs),
		},
		Version: version,
	}
	return json.Marshal(input)
}

func statusLineShellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
