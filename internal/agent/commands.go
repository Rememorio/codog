package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/insights"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/mcpauthdiag"
	"github.com/Rememorio/codog/internal/mcpserver"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/perfissue"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	localstatus "github.com/Rememorio/codog/internal/status"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/thinkback"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/usage"
)

func parseCommandUninstallArgs(args []string) (commandUninstallRequest, error) {
	req := commandUninstallRequest{Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("commands uninstall output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project" || arg == "--workspace":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--claude":
			req.Target = "claude"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("commands uninstall target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "commands uninstall"); err != nil {
		return req, err
	}
	if len(positionals) == 0 {
		return req, errCommandUninstallMissingName
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog commands uninstall NAME [--project|--user|--claude] [--json]")
	}
	req.Name = positionals[0]
	return req, nil
}

func (a *App) commandTargetRoot(target string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user":
		return filepath.Join(a.Config.ConfigHome, "commands"), "user", nil
	case "project", "workspace":
		return filepath.Join(a.Workspace, ".codog", "commands"), "workspace", nil
	case "claude":
		return filepath.Join(a.Workspace, ".claude", "commands"), "claude", nil
	default:
		return "", "", fmt.Errorf("unknown commands target %q", target)
	}
}

func (a *App) commandUninstallRoots(target string) []string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "user":
		return []string{filepath.Join(a.Config.ConfigHome, "commands")}
	case "project", "workspace":
		return []string{filepath.Join(a.Workspace, ".codog", "commands")}
	case "claude":
		return []string{filepath.Join(a.Workspace, ".claude", "commands")}
	default:
		return []string{
			filepath.Join(a.Workspace, ".codog", "commands"),
			filepath.Join(a.Workspace, ".claude", "commands"),
			filepath.Join(a.Config.ConfigHome, "commands"),
		}
	}
}

func renderCommandLookupError(out io.Writer, action string, subject string, err error, format string) error {
	if errors.Is(err, customcommands.ErrNotFound) {
		return renderCustomCommandNotFound(out, action, subject, format)
	}
	var sourceMissing customcommands.SourceNotFoundError
	if errors.As(err, &sourceMissing) {
		source := strings.TrimSpace(sourceMissing.Source)
		if source == "" {
			source = subject
		}
		return renderCustomCommandNotFound(out, action, source, format)
	}
	return err
}

func renderCustomCommandNotFound(out io.Writer, action string, subject string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "show"
	}
	subject = strings.TrimSpace(subject)
	message := "custom command was not found"
	if subject != "" {
		if action == "install" {
			message = fmt.Sprintf("custom command source %q was not found", subject)
		} else {
			message = fmt.Sprintf("custom command %q was not found", subject)
		}
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "commands",
		Action:    action,
		Status:    "error",
		ErrorKind: "command_not_found",
		Message:   message,
		Hint:      "Run `codog commands list` to see available commands, or `codog commands add <path>` / `codog commands install <path>` to install one.",
	}, format)
}

func renderCommandInstallMissingSource(out io.Writer, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "commands",
		Action:    "install",
		Status:    "error",
		ErrorKind: "missing_argument",
		Argument:  "install_source",
		Message:   "commands install requires a source",
		Hint:      "Usage: codog commands install [--project|--user|--claude] [--name NAME] SOURCE [--json|--output-format text|json].",
	}, format)
}

func renderUnsupportedCommandsAction(out io.Writer, action string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "commands",
		Action:    action,
		Status:    "error",
		ErrorKind: "unsupported_commands_action",
		Message:   fmt.Sprintf("unsupported commands action %q", action),
		Hint:      unknownCommandsActionHint(action),
	}, format)
}

var commandsActionCandidates = []string{
	"list", "ls", "search", "find", "query", "lookup", "audit", "doctor", "check",
	"validate", "source", "sources", "root", "roots", "show", "info", "describe",
	"get", "view", "cat", "run", "render", "exec", "execute", "call", "invoke",
	"install", "add", "uninstall", "remove", "delete", "rm", "del",
}

func unknownCommandsActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, commandsActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog commands %s`? Use `codog commands list` to see available commands.", suggestions[0])
	case 0:
		return "Supported: `codog commands list|ls`, `codog commands search|find QUERY`, `codog commands audit|doctor`, `codog commands sources|roots`, `codog commands show|info|describe NAME`, `codog commands run|render NAME [ARGS...]`, `codog commands add|install SOURCE`, or `codog commands uninstall|remove|rm NAME`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog commands list` to see available commands.", strings.Join(suggestions, ", "))
	}
}

func (a *App) renderCommandsList(format string) error {
	return a.renderCommandsListWithAction(format, "list", "")
}

func (a *App) renderCommandsListWithAction(format string, action string, query string) error {
	all, err := a.runtimeCustomCommands()
	if err != nil {
		return err
	}
	filter := strings.TrimSpace(query)
	if filter != "" {
		all = filterCommands(all, filter)
	}
	if format == "json" {
		summaries := make([]customcommands.Command, len(all))
		copy(summaries, all)
		for i := range summaries {
			summaries[i].Body = ""
		}
		activeCount := 0
		for _, command := range all {
			if command.Active {
				activeCount++
			}
		}
		data, _ := json.MarshalIndent(map[string]any{
			"kind":   "commands",
			"action": action,
			"status": "ok",
			"query":  filter,
			"count":  len(all),
			"summary": map[string]any{
				"total":    len(all),
				"active":   activeCount,
				"shadowed": len(all) - activeCount,
			},
			"commands": summaries,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(a.Out, "No custom commands found.")
		return nil
	}
	for _, command := range all {
		status := "active"
		if !command.Active {
			status = "shadowed"
			if command.ShadowedBy != "" {
				status += " by " + command.ShadowedBy
			}
		}
		fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\t%s\n", command.Name, command.Source, status, command.Preview, command.Path)
	}
	return nil
}

func filterCommands(all []customcommands.Command, filter string) []customcommands.Command {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return all
	}
	out := make([]customcommands.Command, 0, len(all))
	for _, command := range all {
		if strings.Contains(strings.ToLower(command.Name), filter) ||
			strings.Contains(strings.ToLower(command.Source), filter) ||
			strings.Contains(strings.ToLower(command.Preview), filter) ||
			strings.Contains(strings.ToLower(command.Body), filter) {
			out = append(out, command)
		}
	}
	return out
}

type commandAuditReport struct {
	Kind                  string                         `json:"kind"`
	Action                string                         `json:"action"`
	Status                string                         `json:"status"`
	CommandCount          int                            `json:"command_count"`
	ActiveCommandCount    int                            `json:"active_command_count"`
	ShadowedCommandCount  int                            `json:"shadowed_command_count"`
	SourceCount           int                            `json:"source_count"`
	Sources               []customcommands.DiscoveryRoot `json:"sources"`
	FrontmatterErrors     []customcommands.Command       `json:"frontmatter_errors,omitempty"`
	FrontmatterErrorCount int                            `json:"frontmatter_error_count"`
	Message               string                         `json:"message"`
}

func (a *App) commandAudit(args []string) error {
	format, err := parseSimpleOutputFormat("commands audit", args)
	if err != nil {
		return err
	}
	all, err := a.runtimeCustomCommands()
	if err != nil {
		return err
	}
	roots := a.runtimeCustomCommandSources()
	activeCount := 0
	var frontmatterErrors []customcommands.Command
	for _, command := range all {
		if command.Active {
			activeCount++
		}
		if strings.TrimSpace(command.FrontmatterError) != "" {
			command.Body = ""
			frontmatterErrors = append(frontmatterErrors, command)
		}
	}
	report := commandAuditReport{
		Kind:                  "commands",
		Action:                "audit",
		Status:                "ok",
		CommandCount:          len(all),
		ActiveCommandCount:    activeCount,
		ShadowedCommandCount:  len(all) - activeCount,
		SourceCount:           len(roots),
		Sources:               roots,
		FrontmatterErrors:     frontmatterErrors,
		FrontmatterErrorCount: len(frontmatterErrors),
	}
	if len(frontmatterErrors) != 0 {
		report.Status = "degraded"
	}
	report.Message = commandAuditMessage(report)
	return renderCommandAuditReport(a.Out, format, report)
}

func commandAuditMessage(report commandAuditReport) string {
	if report.FrontmatterErrorCount != 0 {
		return fmt.Sprintf("Command audit found %d frontmatter error(s).", report.FrontmatterErrorCount)
	}
	return "Command audit passed."
}

func renderCommandAuditReport(out io.Writer, format string, report commandAuditReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Command Audit")
	fmt.Fprintf(out, "  Status              %s\n", report.Status)
	fmt.Fprintf(out, "  Commands            %d\n", report.CommandCount)
	fmt.Fprintf(out, "  Active              %d\n", report.ActiveCommandCount)
	fmt.Fprintf(out, "  Shadowed            %d\n", report.ShadowedCommandCount)
	fmt.Fprintf(out, "  Sources             %d\n", report.SourceCount)
	fmt.Fprintf(out, "  Frontmatter errors  %d\n", report.FrontmatterErrorCount)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message             %s\n", report.Message)
	}
	return nil
}

func (a *App) commandSources(args []string) error {
	format, err := parseSimpleOutputFormat("commands sources", args)
	if err != nil {
		return err
	}
	roots := a.runtimeCustomCommandSources()
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":       "commands",
			"action":     "sources",
			"status":     "ok",
			"root_count": len(roots),
			"roots":      roots,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Command Sources")
	for _, root := range roots {
		state := "missing"
		if root.Exists {
			state = "present"
		}
		fmt.Fprintf(a.Out, "  %-11s %-8s %s\n", root.Source, state, root.Path)
	}
	return nil
}

func (a *App) Templates(args []string) error {
	action := "list"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = normalizeTemplatesAction(args[0])
		rest = args[1:]
	}
	switch action {
	case "list":
		format, err := parseSimpleOutputFormat("templates", rest)
		if err != nil {
			return err
		}
		return a.renderTemplatesList(format)
	case "search":
		format, remaining, err := parseTemplateOutputArgs("templates search", rest)
		if err != nil {
			return err
		}
		query := strings.TrimSpace(strings.Join(remaining, " "))
		if query == "" {
			return renderMissingActionArgument(a.Out, "templates", "search", "query", "templates search requires a query", "Usage: codog templates search QUERY [--json|--output-format text|json].", format)
		}
		return a.renderTemplatesListWithAction(format, "search", query)
	case "audit":
		return a.templateAudit(rest)
	case "sources":
		return a.templateSources(rest)
	case "show":
		return a.templateShow(rest)
	case "apply":
		return a.templateApply(rest)
	case "install":
		return a.templateInstall(rest)
	case "uninstall":
		return a.templateUninstall(rest)
	default:
		_, format, err := stripJSONOnlyOutputFormat("templates", rest)
		if err != nil {
			return renderCLIError(a.Out, err, requestedOutputFormat(append([]string{"templates", action}, rest...)))
		}
		return renderUnsupportedTemplatesAction(a.Out, action, format)
	}
}

func (a *App) templateShow(args []string) error {
	format, remaining, err := parseTemplateOutputArgs("templates show", args)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return a.renderTemplatesList(format)
	}
	if len(remaining) > 1 {
		return renderCLIError(a.Out, unexpectedExtraArgsError{
			Command: "templates show", Args: append([]string(nil), remaining[1:]...),
			Usage: "codog templates show [NAME] [--json|--output-format text|json]",
		}, format)
	}
	tmpl, err := prompttemplates.Find(a.Config.ConfigHome, a.Workspace, remaining[0])
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(tmpl, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprint(a.Out, tmpl.Body)
	if !strings.HasSuffix(tmpl.Body, "\n") {
		fmt.Fprintln(a.Out)
	}
	return nil
}

func (a *App) templateApply(args []string) error {
	req, err := parseTemplateApplyArgs(args)
	if err != nil {
		if errors.Is(err, errTemplateApplyMissingName) {
			return renderMissingActionArgument(a.Out, "templates", "apply", "template_name", "templates apply requires a template name", "Usage: codog templates apply NAME [--var key=value] [--json|--output-format text|json]. Run `codog templates list` to see available templates.", req.Format)
		}
		return err
	}
	tmpl, err := prompttemplates.Find(a.Config.ConfigHome, a.Workspace, req.Name)
	if err != nil {
		return err
	}
	rendered, err := prompttemplates.Render(tmpl, req.Vars)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(map[string]any{"kind": "template_apply", "template": rendered}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprint(a.Out, rendered.Rendered)
	if !strings.HasSuffix(rendered.Rendered, "\n") {
		fmt.Fprintln(a.Out)
	}
	return nil
}

func (a *App) templateInstall(args []string) error {
	req, err := parseTemplateInstallArgs(args)
	if err != nil {
		if errors.Is(err, errTemplateInstallMissingSource) {
			return renderTemplateInstallMissingSource(a.Out, req.Format)
		}
		return err
	}
	targetRoot, targetLabel, err := a.templateTargetRoot(req.Target)
	if err != nil {
		return err
	}
	report, err := prompttemplates.Install(req.Source, targetRoot, req.Name, targetLabel)
	if err != nil {
		return renderTemplateLookupError(a.Out, "install", req.Source, err, req.Format)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Template Installed")
	fmt.Fprintf(a.Out, "  Name             %s\n", report.Name)
	fmt.Fprintf(a.Out, "  Target           %s\n", report.Target)
	fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	return nil
}

func (a *App) templateUninstall(args []string) error {
	req, err := parseTemplateUninstallArgs(args)
	if err != nil {
		if errors.Is(err, errTemplateUninstallMissingName) {
			return renderMissingActionArgument(a.Out, "templates", "uninstall", "template_name", "templates uninstall requires a template name", "Usage: codog templates uninstall NAME [--project|--user] [--json|--output-format text|json]. Run `codog templates list` to see installed templates.", req.Format)
		}
		return err
	}
	report, err := prompttemplates.Uninstall(req.Name, a.templateUninstallRoots(req.Target))
	if err != nil {
		return renderTemplateLookupError(a.Out, "uninstall", req.Name, err, req.Format)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Template Uninstalled")
	fmt.Fprintf(a.Out, "  Name             %s\n", report.Name)
	fmt.Fprintf(a.Out, "  Path             %s\n", report.Path)
	return nil
}

func normalizeTemplatesAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "search", "find", "query", "lookup":
		return "search"
	case "audit", "doctor", "check", "validate":
		return "audit"
	case "source", "sources", "root", "roots":
		return "sources"
	case "show", "info", "describe", "get", "view", "cat":
		return "show"
	case "apply", "render", "run", "exec", "execute", "call", "invoke":
		return "apply"
	case "install", "add":
		return "install"
	case "uninstall", "remove", "delete", "rm", "del":
		return "uninstall"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func (a *App) renderTemplatesList(format string) error {
	return a.renderTemplatesListWithAction(format, "list", "")
}

func (a *App) renderTemplatesListWithAction(format string, action string, query string) error {
	all, err := prompttemplates.Load(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return err
	}
	filter := strings.TrimSpace(query)
	if filter != "" {
		all = filterTemplates(all, filter)
	}
	if format == "json" {
		summaries := make([]prompttemplates.Template, len(all))
		copy(summaries, all)
		for i := range summaries {
			summaries[i].Body = ""
		}
		activeCount := 0
		for _, tmpl := range all {
			if tmpl.Active {
				activeCount++
			}
		}
		data, _ := json.MarshalIndent(map[string]any{
			"kind":   "templates",
			"action": action,
			"status": "ok",
			"query":  filter,
			"count":  len(all),
			"summary": map[string]any{
				"total":    len(all),
				"active":   activeCount,
				"shadowed": len(all) - activeCount,
			},
			"templates": summaries,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(a.Out, "No templates found.")
		return nil
	}
	for _, tmpl := range all {
		fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", tmpl.Name, tmpl.Source, tmpl.Preview, tmpl.Path)
	}
	return nil
}

func filterTemplates(all []prompttemplates.Template, filter string) []prompttemplates.Template {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return all
	}
	out := make([]prompttemplates.Template, 0, len(all))
	for _, tmpl := range all {
		if strings.Contains(strings.ToLower(tmpl.Name), filter) ||
			strings.Contains(strings.ToLower(tmpl.Source), filter) ||
			strings.Contains(strings.ToLower(tmpl.Preview), filter) ||
			strings.Contains(strings.ToLower(tmpl.Body), filter) {
			out = append(out, tmpl)
		}
	}
	return out
}

type templateAuditReport struct {
	Kind                  string                          `json:"kind"`
	Action                string                          `json:"action"`
	Status                string                          `json:"status"`
	TemplateCount         int                             `json:"template_count"`
	ActiveTemplateCount   int                             `json:"active_template_count"`
	ShadowedTemplateCount int                             `json:"shadowed_template_count"`
	SourceCount           int                             `json:"source_count"`
	Sources               []prompttemplates.DiscoveryRoot `json:"sources"`
	Message               string                          `json:"message"`
}

func (a *App) templateAudit(args []string) error {
	format, err := parseSimpleOutputFormat("templates audit", args)
	if err != nil {
		return err
	}
	all, err := prompttemplates.Load(a.Config.ConfigHome, a.Workspace)
	if err != nil {
		return err
	}
	roots := prompttemplates.Sources(a.Config.ConfigHome, a.Workspace)
	activeCount := 0
	for _, tmpl := range all {
		if tmpl.Active {
			activeCount++
		}
	}
	report := templateAuditReport{
		Kind:                  "templates",
		Action:                "audit",
		Status:                "ok",
		TemplateCount:         len(all),
		ActiveTemplateCount:   activeCount,
		ShadowedTemplateCount: len(all) - activeCount,
		SourceCount:           len(roots),
		Sources:               roots,
	}
	report.Message = templateAuditMessage(report)
	return renderTemplateAuditReport(a.Out, format, report)
}

func templateAuditMessage(report templateAuditReport) string {
	return "Template audit passed."
}

func renderTemplateAuditReport(out io.Writer, format string, report templateAuditReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Template Audit")
	fmt.Fprintf(out, "  Status              %s\n", report.Status)
	fmt.Fprintf(out, "  Templates           %d\n", report.TemplateCount)
	fmt.Fprintf(out, "  Active              %d\n", report.ActiveTemplateCount)
	fmt.Fprintf(out, "  Shadowed            %d\n", report.ShadowedTemplateCount)
	fmt.Fprintf(out, "  Sources             %d\n", report.SourceCount)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message             %s\n", report.Message)
	}
	return nil
}

func (a *App) templateSources(args []string) error {
	format, err := parseSimpleOutputFormat("templates sources", args)
	if err != nil {
		return err
	}
	roots := prompttemplates.Sources(a.Config.ConfigHome, a.Workspace)
	if format == "json" {
		data, _ := json.MarshalIndent(map[string]any{
			"kind":       "templates",
			"action":     "sources",
			"status":     "ok",
			"root_count": len(roots),
			"roots":      roots,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Template Sources")
	for _, root := range roots {
		state := "missing"
		if root.Exists {
			state = "present"
		}
		fmt.Fprintf(a.Out, "  %-11s %-8s %s\n", root.Source, state, root.Path)
	}
	return nil
}

type templateApplyRequest struct {
	Name   string
	Vars   map[string]string
	Format string
}

type templateInstallRequest struct {
	Format string
	Target string
	Name   string
	Source string
}

type templateUninstallRequest struct {
	Format string
	Target string
	Name   string
}

var (
	errTemplateApplyMissingName     = errors.New("templates apply missing name")
	errTemplateInstallMissingSource = errors.New("templates install source is required")
	errTemplateUninstallMissingName = errors.New("templates uninstall name is required")
)

func parseTemplateOutputArgs(command string, args []string) (string, []string, error) {
	format := "text"
	remaining := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", nil, fmt.Errorf("%s output format is required", command)
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		default:
			remaining = append(remaining, arg)
		}
	}
	switch format {
	case "text", "json":
		return format, remaining, nil
	default:
		return "", nil, fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func parseTemplateApplyArgs(args []string) (templateApplyRequest, error) {
	req := templateApplyRequest{Format: "text", Vars: map[string]string{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("templates apply output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--var" || arg == "-v":
			index++
			if index >= len(args) {
				return req, errors.New("template variable is required")
			}
			if err := addTemplateVar(req.Vars, args[index]); err != nil {
				return req, err
			}
		case strings.HasPrefix(arg, "--var="):
			if err := addTemplateVar(req.Vars, strings.TrimPrefix(arg, "--var=")); err != nil {
				return req, err
			}
		default:
			if req.Name == "" {
				req.Name = arg
				continue
			}
			if strings.Contains(arg, "=") {
				if err := addTemplateVar(req.Vars, arg); err != nil {
					return req, err
				}
				continue
			}
			return req, fmt.Errorf("unexpected template apply argument %q", arg)
		}
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown templates apply output format %q", req.Format)
	}
	if strings.TrimSpace(req.Name) == "" {
		return req, errTemplateApplyMissingName
	}
	return req, nil
}

func addTemplateVar(vars map[string]string, value string) error {
	key, val, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("template variable must use key=value: %s", value)
	}
	vars[key] = val
	return nil
}

func parseTemplateInstallArgs(args []string) (templateInstallRequest, error) {
	req := templateInstallRequest{Format: "text", Target: "user"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("templates install output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project" || arg == "--workspace":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("templates install target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--name":
			index++
			if index >= len(args) {
				return req, errors.New("templates install name is required")
			}
			req.Name = args[index]
		case strings.HasPrefix(arg, "--name="):
			req.Name = strings.TrimPrefix(arg, "--name=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "templates install"); err != nil {
		return req, err
	}
	if len(positionals) == 0 {
		return req, errTemplateInstallMissingSource
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog templates install [--project|--user] [--name NAME] SOURCE [--json]")
	}
	req.Source = positionals[0]
	return req, nil
}

func parseTemplateUninstallArgs(args []string) (templateUninstallRequest, error) {
	req := templateUninstallRequest{Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("templates uninstall output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--project" || arg == "--workspace":
			req.Target = "project"
		case arg == "--user":
			req.Target = "user"
		case arg == "--target":
			index++
			if index >= len(args) {
				return req, errors.New("templates uninstall target is required")
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		default:
			positionals = append(positionals, arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "templates uninstall"); err != nil {
		return req, err
	}
	if len(positionals) == 0 {
		return req, errTemplateUninstallMissingName
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog templates uninstall NAME [--project|--user] [--json]")
	}
	req.Name = positionals[0]
	return req, nil
}

func (a *App) templateTargetRoot(target string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user":
		return filepath.Join(a.Config.ConfigHome, "templates"), "user", nil
	case "project", "workspace":
		return filepath.Join(a.Workspace, ".codog", "templates"), "workspace", nil
	default:
		return "", "", fmt.Errorf("unknown templates target %q", target)
	}
}

func (a *App) templateUninstallRoots(target string) []string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "user":
		return []string{filepath.Join(a.Config.ConfigHome, "templates")}
	case "project", "workspace":
		return []string{filepath.Join(a.Workspace, ".codog", "templates")}
	default:
		return []string{
			filepath.Join(a.Workspace, ".codog", "templates"),
			filepath.Join(a.Config.ConfigHome, "templates"),
		}
	}
}

func renderTemplateLookupError(out io.Writer, action string, subject string, err error, format string) error {
	if errors.Is(err, prompttemplates.ErrNotFound) {
		return renderTemplateNotFound(out, action, subject, format)
	}
	var sourceMissing prompttemplates.SourceNotFoundError
	if errors.As(err, &sourceMissing) {
		source := strings.TrimSpace(sourceMissing.Source)
		if source == "" {
			source = subject
		}
		return renderTemplateNotFound(out, action, source, format)
	}
	return err
}

func renderTemplateNotFound(out io.Writer, action string, subject string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "show"
	}
	subject = strings.TrimSpace(subject)
	message := "template was not found"
	if subject != "" {
		if action == "install" {
			message = fmt.Sprintf("template source %q was not found", subject)
		} else {
			message = fmt.Sprintf("template %q was not found", subject)
		}
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "templates",
		Action:    action,
		Status:    "error",
		ErrorKind: "template_not_found",
		Message:   message,
		Hint:      "Run `codog templates list` to see available templates, or `codog templates add <path>` / `codog templates install <path>` to install one.",
	}, format)
}

func renderTemplateInstallMissingSource(out io.Writer, format string) error {
	return renderActionError(out, actionErrorReport{
		Kind:      "templates",
		Action:    "install",
		Status:    "error",
		ErrorKind: "missing_argument",
		Argument:  "install_source",
		Message:   "templates install requires a source",
		Hint:      "Usage: codog templates install [--project|--user] [--name NAME] SOURCE [--json|--output-format text|json].",
	}, format)
}

func renderUnsupportedTemplatesAction(out io.Writer, action string, format string) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	return renderActionError(out, actionErrorReport{
		Kind:      "templates",
		Action:    action,
		Status:    "error",
		ErrorKind: "unsupported_templates_action",
		Message:   fmt.Sprintf("unsupported templates action %q", action),
		Hint:      unknownTemplatesActionHint(action),
	}, format)
}

var templatesActionCandidates = []string{
	"list", "ls", "search", "find", "query", "lookup", "audit", "doctor", "check",
	"validate", "source", "sources", "root", "roots", "show", "info", "describe",
	"get", "view", "cat", "apply", "render", "run", "exec", "execute", "call",
	"invoke", "install", "add", "uninstall", "remove", "delete", "rm", "del",
}

func unknownTemplatesActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, templatesActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog templates %s`? Use `codog templates list` to see available templates.", suggestions[0])
	case 0:
		return "Supported: `codog templates list|ls`, `codog templates search|find QUERY`, `codog templates audit|doctor`, `codog templates sources|roots`, `codog templates show|info|describe NAME`, `codog templates apply|render NAME [--var key=value]`, `codog templates add|install SOURCE`, or `codog templates uninstall|remove|rm NAME`."
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog templates list` to see available templates.", strings.Join(suggestions, ", "))
	}
}

func (a *App) MCP(ctx context.Context, args []string) error {
	cleanArgs, format, err := stripJSONOnlyOutputFormat("mcp", args)
	if err != nil {
		return err
	}
	args = cleanArgs
	requestedArgs := append([]string(nil), args...)
	if hasMCPHelpArg(args) {
		renderMCPUsageReport(a.Out, format, buildMCPUsageReport(mcpHelpUnexpected(args)))
		return nil
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args[0] = normalizeMCPAction(args[0])
	}
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return renderMCPUnsupportedAction(a.Out, format, strings.Join(requestedArgs, " "), "list accepts no filter argument; use `codog mcp list`")
		}
		validation := buildMCPValidation(a.Config.MCPServers)
		if len(a.Config.MCPServers) == 0 {
			renderMCPListReport(a.Out, format, buildMCPListReport(nil, validation, "", ""))
			return nil
		}
		statuses := mcp.InspectAll(ctx, a.Config.MCPServers)
		renderMCPListReport(a.Out, format, buildMCPListReport(statuses, validation, "", ""))
		return nil
	}
	switch args[0] {
	case "serve":
		return a.mcpServe(ctx, args[1:])
	case "self", "self-test":
		return a.mcpSelf(ctx, args[1:], format)
	case "show", "info", "describe":
		return a.mcpShow(args[1:], format)
	case "add":
		return a.mcpAdd(args[1:], format)
	case "remove", "delete", "rm":
		return a.mcpRemove(args[1:], format)
	}
	return a.mcpRemote(ctx, args, requestedArgs, format)
}

func (a *App) mcpRemote(ctx context.Context, args, requestedArgs []string, format string) error {
	if !mcpRemoteAction(args[0]) {
		verb := strings.TrimSpace(firstNonEmpty(firstArg(requestedArgs), args[0]))
		return renderMCPUnsupportedAction(a.Out, format, strings.Join(requestedArgs, " "), unknownMCPActionHint(verb))
	}
	if len(a.Config.MCPServers) == 0 {
		return renderMCPRemoteActionError(a.Out, format, mcpRemoteActionErrorReport{
			Kind:            "mcp",
			Action:          args[0],
			OK:              false,
			Status:          "error",
			ErrorKind:       "no_servers_configured",
			RequestedAction: strings.Join(requestedArgs, " "),
			Message:         "No MCP servers are configured.",
			Hint:            "Add one with `codog mcp add NAME COMMAND [ARG...]` or run `codog mcp list`.",
			Usage:           mcpRemoteUsage(args[0]),
		})
	}
	if args[0] == "auth" {
		return a.mcpAuth(ctx, args[1:], requestedArgs, format)
	}
	if len(args) == 1 && mcpAggregateRemoteAction(args[0]) {
		payload := buildMCPAggregateRemoteReport(ctx, canonicalMCPAggregateAction(args[0]), a.Config.MCPServers, a.Config.ConfigHome, a.Config.OAuthProfile)
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	if len(args) < 2 {
		return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport(args[0], strings.Join(requestedArgs, " "), "server"))
	}
	serverName := args[1]
	server, ok := a.Config.MCPServers[serverName]
	if !ok {
		return renderMCPRemoteActionError(a.Out, format, mcpRemoteActionErrorReport{
			Kind:             "mcp",
			Action:           args[0],
			OK:               false,
			Status:           "error",
			ErrorKind:        "server_not_found",
			RequestedAction:  strings.Join(requestedArgs, " "),
			ServerName:       serverName,
			AvailableServers: sortedMCPServerNames(a.Config.MCPServers),
			Message:          fmt.Sprintf("MCP server %q is not configured.", serverName),
			Hint:             "Run `codog mcp list` to see configured servers.",
			Usage:            mcpRemoteUsage(args[0]),
		})
	}
	return a.mcpServerAction(ctx, args, requestedArgs, serverName, server, format)
}

func (a *App) mcpServerAction(ctx context.Context, args, requestedArgs []string, serverName string, server config.MCPServerConfig, format string) error {
	var payload any
	switch args[0] {
	case "tools":
		if len(args) > 2 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[2:]))
		}
		payload = mcp.ListTools(ctx, serverName, server)
	case "call":
		if len(args) < 3 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport(args[0], strings.Join(requestedArgs, " "), "tool"))
		}
		if len(args) < 4 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport(args[0], strings.Join(requestedArgs, " "), "json"))
		}
		if len(args) > 4 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[4:]))
		}
		arguments, err := parseMCPObjectArgument(args[3])
		if err != nil {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteInvalidJSONReport(args[0], strings.Join(requestedArgs, " "), err))
		}
		payload = mcp.CallTool(ctx, serverName, server, args[2], arguments)
	case "resources":
		if len(args) > 2 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[2:]))
		}
		payload = mcp.ListResources(ctx, serverName, server)
	case "resource-templates":
		if len(args) > 2 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[2:]))
		}
		payload = mcp.ListResourceTemplates(ctx, serverName, server)
	case "read":
		if len(args) < 3 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport(args[0], strings.Join(requestedArgs, " "), "uri"))
		}
		if len(args) > 3 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[3:]))
		}
		payload = mcp.ReadResource(ctx, serverName, server, args[2])
	case "prompts":
		if len(args) > 2 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[2:]))
		}
		payload = mcp.ListPrompts(ctx, serverName, server)
	case "prompt":
		if len(args) < 3 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport(args[0], strings.Join(requestedArgs, " "), "prompt"))
		}
		if len(args) > 4 {
			return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteUnexpectedArgumentsReport(args[0], strings.Join(requestedArgs, " "), args[4:]))
		}
		var arguments json.RawMessage
		if len(args) > 3 {
			var err error
			arguments, err = parseMCPObjectArgument(args[3])
			if err != nil {
				return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteInvalidJSONReport(args[0], strings.Join(requestedArgs, " "), err))
			}
		}
		payload = mcp.GetPrompt(ctx, serverName, server, args[2], arguments)
	default:
		return fmt.Errorf("unknown mcp command %q", args[0])
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) mcpAuth(ctx context.Context, args, requestedArgs []string, format string) error {
	req, err := parseMCPAuthArgs(args)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if req.Server == "" {
		return a.mcpAggregateAuth(ctx, req, requestedArgs, format, now)
	}
	server, ok := a.Config.MCPServers[req.Server]
	if !ok {
		return renderMCPRemoteActionError(a.Out, format, mcpRemoteActionErrorReport{
			Kind: "mcp", Action: "auth", OK: false, Status: "error", ErrorKind: "server_not_found",
			RequestedAction: strings.Join(requestedArgs, " "), ServerName: req.Server,
			AvailableServers: sortedMCPServerNames(a.Config.MCPServers),
			Message:          fmt.Sprintf("MCP server %q is not configured.", req.Server),
			Hint:             "Run `codog mcp list` to see configured servers.", Usage: mcpRemoteUsage("auth"),
		})
	}
	result := mcp.InspectAuth(ctx, req.Server, server)
	payload := buildMCPAuthReport(ctx, req, result, a.Config.ConfigHome, a.Config.OAuthProfile, now)
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) mcpAggregateAuth(ctx context.Context, req mcpAuthRequest, requestedArgs []string, format string, now time.Time) error {
	if req.Clear {
		return renderMCPRemoteActionError(a.Out, format, buildMCPRemoteMissingArgumentReport("auth", strings.Join(requestedArgs, " "), "server"))
	}
	payload := buildMCPAggregateAuthReport(ctx, a.Config.MCPServers, a.Config.ConfigHome, a.Config.OAuthProfile, now, req.Refresh)
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func buildMCPAuthReport(ctx context.Context, req mcpAuthRequest, result mcp.AuthStatusResult, configHome, profile string, now time.Time) mcpauthdiag.Report {
	if req.Clear {
		return mcpauthdiag.Clear(ctx, result, configHome, profile, now)
	}
	if req.Refresh {
		return mcpauthdiag.Refresh(ctx, result, configHome, profile, now)
	}
	return mcpauthdiag.Build(result, configHome, profile, now)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func parseMCPObjectArgument(raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("JSON object is required")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	return json.RawMessage(raw), nil
}

func normalizeMCPAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls":
		return "list"
	case "serve":
		return "serve"
	case "self", "self-test", "doctor":
		return "self"
	case "show", "info", "describe", "inspect", "get":
		return "show"
	case "add":
		return "add"
	case "remove", "delete", "rm", "del":
		return "remove"
	case "tools", "tool", "list-tools":
		return "tools"
	case "auth", "oauth":
		return "auth"
	case "call", "invoke", "run":
		return "call"
	case "resources", "resource":
		return "resources"
	case "resource-templates", "resources-templates", "resource-template", "resources-template", "templates", "template":
		return "resource-templates"
	case "read", "read-resource", "get-resource":
		return "read"
	case "prompts", "list-prompts":
		return "prompts"
	case "prompt", "get-prompt", "render-prompt":
		return "prompt"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

var mcpActionCandidates = []string{
	"help", "list", "ls", "serve", "self", "self-test", "doctor", "show", "info",
	"describe", "inspect", "get", "add", "remove", "delete", "rm", "del", "tools",
	"tool", "list-tools", "auth", "oauth", "call", "invoke", "run", "resources",
	"resource", "resource-templates", "resources-templates", "resource-template",
	"resources-template", "templates", "template", "read", "read-resource",
	"get-resource", "prompts", "list-prompts", "prompt", "get-prompt", "render-prompt",
}

func unknownMCPActionHint(action string) string {
	suggestions := toolnames.Suggestions(action, mcpActionCandidates, 4)
	switch len(suggestions) {
	case 1:
		return fmt.Sprintf("Did you mean `codog mcp %s`? Use `codog mcp help` to list supported actions.", suggestions[0])
	case 0:
		return fmt.Sprintf("`%s` is not a supported MCP sub-action; use `codog mcp list`, `codog mcp show SERVER`, `codog mcp add NAME COMMAND [ARG...]`, `codog mcp tools [SERVER]`, `codog mcp call SERVER TOOL JSON`, or `codog mcp help`.", strings.TrimSpace(action))
	default:
		return fmt.Sprintf("Did you mean one of: %s? Use `codog mcp help` to list supported actions.", strings.Join(suggestions, ", "))
	}
}

func mcpRemoteAction(action string) bool {
	switch normalizeMCPAction(action) {
	case "tools", "auth", "call", "resources", "resource-templates", "read", "prompts", "prompt":
		return true
	default:
		return false
	}
}

type mcpAuthRequest struct {
	Server  string
	Refresh bool
	Clear   bool
}

func parseMCPAuthArgs(args []string) (mcpAuthRequest, error) {
	req := mcpAuthRequest{}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--refresh":
			req.Refresh = true
			req.Clear = false
		case arg == "--clear" || arg == "--logout":
			req.Clear = true
			req.Refresh = false
		case arg == "--status":
			req.Refresh = false
			req.Clear = false
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown mcp auth flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 0 {
		first := strings.ToLower(strings.TrimSpace(positionals[0]))
		if first == "refresh" {
			req.Refresh = true
			req.Clear = false
			positionals = positionals[1:]
		} else if first == "clear" || first == "logout" {
			req.Clear = true
			req.Refresh = false
			positionals = positionals[1:]
		} else if first == "status" {
			positionals = positionals[1:]
		}
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog mcp auth [--refresh|refresh|--clear|clear|logout] [SERVER]")
	}
	if len(positionals) == 1 {
		req.Server = positionals[0]
	}
	return req, nil
}

func mcpAggregateRemoteAction(action string) bool {
	switch normalizeMCPAction(action) {
	case "tools", "auth", "resources", "resource-templates", "prompts":
		return true
	default:
		return false
	}
}

func canonicalMCPAggregateAction(action string) string {
	return normalizeMCPAction(action)
}

type mcpAggregateRemoteReport struct {
	Kind              string                           `json:"kind"`
	Action            string                           `json:"action"`
	Status            string                           `json:"status"`
	ServerCount       int                              `json:"server_count"`
	Total             int                              `json:"total"`
	ErrorCount        int                              `json:"error_count"`
	Tools             []mcp.ToolListResult             `json:"tools,omitempty"`
	AuthStatuses      []mcpauthdiag.Report             `json:"auth_statuses,omitempty"`
	Resources         []mcp.ResourceListResult         `json:"resources,omitempty"`
	ResourceTemplates []mcp.ResourceTemplateListResult `json:"resource_templates,omitempty"`
	Prompts           []mcp.PromptListResult           `json:"prompts,omitempty"`
}

func buildMCPAggregateAuthReport(ctx context.Context, servers map[string]config.MCPServerConfig, configHome string, oauthProfile string, now time.Time, refresh bool) mcpAggregateRemoteReport {
	report := mcpAggregateRemoteReport{
		Kind:        "mcp",
		Action:      "auth",
		Status:      "ok",
		ServerCount: len(servers),
	}
	for _, name := range sortedMCPServerNames(servers) {
		server := servers[name]
		result := mcp.InspectAuth(ctx, name, server)
		var status mcpauthdiag.Report
		if refresh {
			status = mcpauthdiag.Refresh(ctx, result, configHome, oauthProfile, now)
		} else {
			status = mcpauthdiag.Build(result, configHome, oauthProfile, now)
		}
		report.AuthStatuses = append(report.AuthStatuses, status)
		if status.Error != "" || status.RefreshError != "" {
			report.ErrorCount++
		}
	}
	if report.ErrorCount > 0 {
		report.Status = "degraded"
	}
	return report
}

func buildMCPAggregateRemoteReport(ctx context.Context, action string, servers map[string]config.MCPServerConfig, configHome string, oauthProfile string) mcpAggregateRemoteReport {
	report := mcpAggregateRemoteReport{
		Kind:        "mcp",
		Action:      action,
		Status:      "ok",
		ServerCount: len(servers),
	}
	for _, name := range sortedMCPServerNames(servers) {
		server := servers[name]
		switch action {
		case "tools":
			result := mcp.ListTools(ctx, name, server)
			report.Tools = append(report.Tools, result)
			report.Total += len(result.Tools)
			if result.Error != "" {
				report.ErrorCount++
			}
		case "auth":
			result := mcpauthdiag.Build(mcp.InspectAuth(ctx, name, server), configHome, oauthProfile, time.Now().UTC())
			report.AuthStatuses = append(report.AuthStatuses, result)
			if result.Error != "" {
				report.ErrorCount++
			}
		case "resources":
			result := mcp.ListResources(ctx, name, server)
			report.Resources = append(report.Resources, result)
			report.Total += countMCPJSONArrayField(result.Resources, "resources")
			if result.Error != "" {
				report.ErrorCount++
			}
		case "resource-templates":
			result := mcp.ListResourceTemplates(ctx, name, server)
			report.ResourceTemplates = append(report.ResourceTemplates, result)
			report.Total += countMCPJSONArrayField(result.Templates, "resourceTemplates")
			if result.Error != "" {
				report.ErrorCount++
			}
		case "prompts":
			result := mcp.ListPrompts(ctx, name, server)
			report.Prompts = append(report.Prompts, result)
			report.Total += countMCPJSONArrayField(result.Prompts, "prompts")
			if result.Error != "" {
				report.ErrorCount++
			}
		}
	}
	if report.ErrorCount > 0 {
		report.Status = "degraded"
	}
	return report
}

func countMCPJSONArrayField(raw json.RawMessage, field string) int {
	if len(raw) == 0 {
		return 0
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0
	}
	var values []json.RawMessage
	if err := json.Unmarshal(payload[field], &values); err != nil {
		return 0
	}
	return len(values)
}

func isMCPShowAction(action string) bool {
	switch normalizeMCPAction(action) {
	case "show":
		return true
	default:
		return false
	}
}

type mcpListReport struct {
	Kind                string                          `json:"kind"`
	Action              string                          `json:"action"`
	Status              string                          `json:"status"`
	WorkingDirectory    string                          `json:"working_directory"`
	ServerCount         int                             `json:"server_count"`
	ConfiguredServers   int                             `json:"configured_servers"`
	TotalConfigured     int                             `json:"total_configured"`
	ValidCount          int                             `json:"valid_count"`
	RequiredCount       int                             `json:"required_count"`
	OptionalCount       int                             `json:"optional_count"`
	InvalidCount        int                             `json:"invalid_count"`
	MCPValidation       localstatus.MCPValidationStatus `json:"mcp_validation"`
	Startup             mcp.StartupReport               `json:"startup"`
	ConfigLoadError     *string                         `json:"config_load_error"`
	ConfigLoadErrorKind string                          `json:"config_load_error_kind,omitempty"`
	Servers             []mcp.ServerStatus              `json:"servers"`
	InvalidServers      []localstatus.ValidationIssue   `json:"invalid_servers,omitempty"`
	NextActions         []string                        `json:"next_actions,omitempty"`
}

type mcpShowReport struct {
	Kind                string                        `json:"kind"`
	Action              string                        `json:"action"`
	Status              string                        `json:"status"`
	ErrorKind           string                        `json:"error_kind,omitempty"`
	WorkingDirectory    string                        `json:"working_directory"`
	Found               bool                          `json:"found"`
	ServerName          string                        `json:"server_name,omitempty"`
	Message             string                        `json:"message,omitempty"`
	Hint                string                        `json:"hint,omitempty"`
	Signature           string                        `json:"signature,omitempty"`
	ConfigHash          string                        `json:"config_hash,omitempty"`
	ConfigLoadError     *string                       `json:"config_load_error"`
	ConfigLoadErrorKind string                        `json:"config_load_error_kind,omitempty"`
	Server              *mcp.ServerDescriptor         `json:"server,omitempty"`
	AvailableServers    []string                      `json:"available_servers,omitempty"`
	TotalConfigured     int                           `json:"total_configured"`
	ValidCount          int                           `json:"valid_count"`
	InvalidCount        int                           `json:"invalid_count"`
	InvalidServers      []localstatus.ValidationIssue `json:"invalid_servers,omitempty"`
}

type mcpUsageReport struct {
	Kind       string        `json:"kind"`
	Action     string        `json:"action"`
	OK         bool          `json:"ok"`
	Status     string        `json:"status"`
	ErrorKind  *string       `json:"error_kind"`
	Hint       *string       `json:"hint"`
	Usage      mcpUsageBlock `json:"usage"`
	Unexpected *string       `json:"unexpected"`
}

type mcpUsageBlock struct {
	SlashCommand string   `json:"slash_command"`
	DirectCLI    string   `json:"direct_cli"`
	Sources      []string `json:"sources"`
}

type mcpUnsupportedActionReport struct {
	Kind            string           `json:"kind"`
	Action          string           `json:"action"`
	OK              bool             `json:"ok"`
	Status          string           `json:"status"`
	ErrorKind       string           `json:"error_kind"`
	Error           cliErrorEnvelope `json:"error"`
	RequestedAction string           `json:"requested_action"`
	Hint            string           `json:"hint"`
	Usage           mcpUsageBlock    `json:"usage"`
}

type mcpRemoteActionErrorReport struct {
	Kind                string        `json:"kind"`
	Action              string        `json:"action"`
	OK                  bool          `json:"ok"`
	Status              string        `json:"status"`
	ErrorKind           string        `json:"error_kind"`
	RequestedAction     string        `json:"requested_action"`
	ServerName          string        `json:"server_name,omitempty"`
	Argument            string        `json:"argument,omitempty"`
	UnexpectedArguments []string      `json:"unexpected_arguments,omitempty"`
	AvailableServers    []string      `json:"available_servers,omitempty"`
	Message             string        `json:"message"`
	Hint                string        `json:"hint"`
	Usage               mcpUsageBlock `json:"usage"`
}

func buildMCPListReport(statuses []mcp.ServerStatus, validation localstatus.MCPValidationStatus, configLoadError string, configLoadErrorKind string) mcpListReport {
	if statuses == nil {
		statuses = []mcp.ServerStatus{}
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
	} else if validation.InvalidCount > 0 {
		status = "degraded"
	}
	startup := mcp.BuildStartupReport(statuses)
	switch startup.Status {
	case "fatal":
		status = "fatal"
	case "degraded":
		if status == "ok" {
			status = "degraded"
		}
	}
	report := mcpListReport{
		Kind:                "mcp",
		Action:              "list",
		Status:              status,
		WorkingDirectory:    currentWorkingDirectory(),
		ServerCount:         len(statuses),
		ConfiguredServers:   validation.ValidCount,
		TotalConfigured:     validation.TotalConfigured,
		ValidCount:          validation.ValidCount,
		RequiredCount:       validation.RequiredCount,
		OptionalCount:       validation.OptionalCount,
		InvalidCount:        validation.InvalidCount,
		MCPValidation:       validation,
		Startup:             startup,
		ConfigLoadError:     loadError,
		ConfigLoadErrorKind: strings.TrimSpace(configLoadErrorKind),
		Servers:             statuses,
		InvalidServers:      append([]localstatus.ValidationIssue(nil), validation.InvalidServers...),
	}
	report.NextActions = mcpListNextActions(report)
	return report
}

func mcpListNextActions(report mcpListReport) []string {
	actions := []string{}
	if report.ConfigLoadError != nil {
		actions = append(actions, "codog doctor --json")
	}
	for _, invalid := range report.InvalidServers {
		if strings.TrimSpace(invalid.Name) == "" {
			continue
		}
		actions = append(actions, "codog mcp show "+shellQuote(invalid.Name)+" --json")
	}
	for _, failure := range report.Startup.FailedRequired {
		if strings.TrimSpace(failure.Name) == "" {
			continue
		}
		actions = append(actions, "codog mcp show "+shellQuote(failure.Name)+" --json")
	}
	for _, failure := range report.Startup.FailedOptional {
		if strings.TrimSpace(failure.Name) == "" {
			continue
		}
		actions = append(actions, "codog mcp show "+shellQuote(failure.Name)+" --json")
	}
	return dedupeStrings(actions)
}

func buildMCPRemoteMissingArgumentReport(action string, requestedAction string, argument string) mcpRemoteActionErrorReport {
	action = strings.TrimSpace(action)
	argument = strings.TrimSpace(argument)
	return mcpRemoteActionErrorReport{
		Kind:            "mcp",
		Action:          action,
		OK:              false,
		Status:          "error",
		ErrorKind:       "missing_argument",
		RequestedAction: strings.TrimSpace(requestedAction),
		Argument:        argument,
		Message:         fmt.Sprintf("mcp %s requires %s.", action, mcpRemoteArgumentLabel(argument)),
		Hint:            "Usage: " + mcpRemoteUsage(action).DirectCLI + ".",
		Usage:           mcpRemoteUsage(action),
	}
}

func buildMCPRemoteInvalidJSONReport(action string, requestedAction string, err error) mcpRemoteActionErrorReport {
	action = strings.TrimSpace(action)
	return mcpRemoteActionErrorReport{
		Kind:            "mcp",
		Action:          action,
		OK:              false,
		Status:          "error",
		ErrorKind:       "invalid_json",
		RequestedAction: strings.TrimSpace(requestedAction),
		Argument:        "json",
		Message:         "mcp " + action + " requires a JSON object argument: " + strings.TrimSpace(err.Error()),
		Hint:            "Pass a JSON object such as `{}` or `{\"key\":\"value\"}`. Usage: " + mcpRemoteUsage(action).DirectCLI + ".",
		Usage:           mcpRemoteUsage(action),
	}
}

func buildMCPRemoteUnexpectedArgumentsReport(action string, requestedAction string, extra []string) mcpRemoteActionErrorReport {
	action = strings.TrimSpace(action)
	unexpected := append([]string(nil), extra...)
	return mcpRemoteActionErrorReport{
		Kind:                "mcp",
		Action:              action,
		OK:                  false,
		Status:              "error",
		ErrorKind:           "unexpected_argument",
		RequestedAction:     strings.TrimSpace(requestedAction),
		Argument:            strings.Join(unexpected, " "),
		UnexpectedArguments: unexpected,
		Message:             fmt.Sprintf("mcp %s received unexpected argument(s): %s", action, strings.Join(unexpected, " ")),
		Hint:                "Usage: " + mcpRemoteUsage(action).DirectCLI + ".",
		Usage:               mcpRemoteUsage(action),
	}
}

func mcpRemoteArgumentLabel(argument string) string {
	switch argument {
	case "server":
		return "a server name"
	case "tool":
		return "a tool name"
	case "json":
		return "tool input JSON"
	case "uri":
		return "a resource URI"
	case "prompt":
		return "a prompt name"
	default:
		return "an argument"
	}
}

func mcpRemoteUsage(action string) mcpUsageBlock {
	action = normalizeMCPAction(action)
	direct := "codog mcp " + action + " SERVER"
	switch action {
	case "tools":
		direct = "codog mcp tools [SERVER]"
	case "auth":
		direct = "codog mcp auth [--refresh|refresh|--clear|clear|logout] [SERVER]"
	case "call":
		direct = "codog mcp call SERVER TOOL JSON"
	case "resources":
		direct = "codog mcp resources [SERVER]"
	case "resource-templates":
		direct = "codog mcp resource-templates [SERVER]"
	case "read":
		direct = "codog mcp read SERVER URI"
	case "prompts":
		direct = "codog mcp prompts [SERVER]"
	case "prompt":
		direct = "codog mcp prompt SERVER NAME [JSON]"
	}
	return mcpUsageBlock{
		SlashCommand: "/" + strings.TrimPrefix(direct, "codog "),
		DirectCLI:    direct,
		Sources:      []string{".codog.json", ".codog.local.json", "user config"},
	}
}

func renderMCPRemoteActionError(out io.Writer, format string, report mcpRemoteActionErrorReport) error {
	err := fmt.Errorf("%s: %s\n%s", report.ErrorKind, report.Message, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	fmt.Fprintln(out, "MCP")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Error            %s\n", report.ErrorKind)
	fmt.Fprintf(out, "  Message          %s\n", report.Message)
	if report.ServerName != "" {
		fmt.Fprintf(out, "  Server           %s\n", report.ServerName)
	}
	if len(report.AvailableServers) > 0 {
		fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.AvailableServers, ", "))
	}
	fmt.Fprintf(out, "  Hint             %s\n", report.Hint)
	fmt.Fprintf(out, "  Usage            %s\n", report.Usage.DirectCLI)
	return &ExitError{Code: 1, Err: err}
}

func buildMCPUnsupportedActionReport(requestedAction string, hint string) mcpUnsupportedActionReport {
	report := mcpUnsupportedActionReport{
		Kind:            "mcp",
		Action:          "error",
		OK:              false,
		Status:          "error",
		ErrorKind:       "unsupported_action",
		RequestedAction: strings.TrimSpace(requestedAction),
		Hint:            strings.TrimSpace(hint),
		Usage:           mcpGeneralUsageBlock(),
	}
	report.Error = buildCLIErrorEnvelope(errors.New(report.ErrorKind+": unsupported mcp action "+report.RequestedAction), cliErrorReport{
		Kind:      report.ErrorKind,
		ErrorKind: report.ErrorKind,
		Status:    report.Status,
		Command:   "mcp",
		Action:    report.Action,
		Value:     report.RequestedAction,
		Message:   "unsupported mcp action " + report.RequestedAction,
		Hint:      report.Hint,
	})
	return report
}

func renderMCPUnsupportedAction(out io.Writer, format string, requestedAction string, hint string) error {
	report := buildMCPUnsupportedActionReport(requestedAction, hint)
	err := fmt.Errorf("%s: unsupported mcp action %q\n%s", report.ErrorKind, report.RequestedAction, report.Hint)
	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return &ExitError{Code: 1, Err: err, Silent: true}
	}
	fmt.Fprintln(out, "MCP")
	fmt.Fprintf(out, "  Error            unsupported action '%s'\n", report.RequestedAction)
	fmt.Fprintf(out, "  Hint             %s\n", report.Hint)
	fmt.Fprintf(out, "  Usage            %s\n", report.Usage.SlashCommand)
	return &ExitError{Code: 1, Err: err}
}

func buildMCPUsageReport(unexpected string) mcpUsageReport {
	unexpected = strings.TrimSpace(unexpected)
	status := "ok"
	ok := true
	var errorKind *string
	var hint *string
	var unexpectedValue *string
	if unexpected != "" {
		status = "error"
		ok = false
		value := "unknown_mcp_action"
		errorKind = &value
		hintValue := "Use: list|ls, serve, self, show SERVER, add NAME COMMAND [ARG...], add NAME --url URL, remove SERVER, tools [SERVER], auth [--refresh|refresh|--clear|clear|logout] [SERVER], call SERVER TOOL JSON, resources [SERVER], resource-templates [SERVER], read SERVER URI, prompts [SERVER], prompt SERVER NAME [JSON], or help"
		hint = &hintValue
		unexpectedValue = &unexpected
	}
	return mcpUsageReport{
		Kind:       "mcp",
		Action:     "help",
		OK:         ok,
		Status:     status,
		ErrorKind:  errorKind,
		Hint:       hint,
		Usage:      mcpGeneralUsageBlock(),
		Unexpected: unexpectedValue,
	}
}

func mcpGeneralUsageBlock() mcpUsageBlock {
	return mcpUsageBlock{
		SlashCommand: "/mcp [list|ls|serve|self|show SERVER|add NAME COMMAND [ARG...]|add NAME --url URL|remove SERVER|tools [SERVER]|auth [--refresh|refresh|--clear|clear|logout] [SERVER]|call SERVER TOOL JSON|resources [SERVER]|resource-templates [SERVER]|read SERVER URI|prompts [SERVER]|prompt SERVER NAME [JSON]|help]",
		DirectCLI:    "codog mcp [list|ls|serve|self|show SERVER|add NAME COMMAND [ARG...]|add NAME --url URL|remove SERVER|tools [SERVER]|auth [--refresh|refresh|--clear|clear|logout] [SERVER]|call SERVER TOOL JSON|resources [SERVER]|resource-templates [SERVER]|read SERVER URI|prompts [SERVER]|prompt SERVER NAME [JSON]|help]",
		Sources:      []string{".codog.json", ".codog.local.json", "user config"},
	}
}

func renderMCPUsageReport(out io.Writer, format string, report mcpUsageReport) {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return
	}
	fmt.Fprintln(out, "MCP")
	fmt.Fprintf(out, "  Usage            %s\n", report.Usage.SlashCommand)
	fmt.Fprintf(out, "  Direct CLI       %s\n", report.Usage.DirectCLI)
	fmt.Fprintf(out, "  Sources          %s\n", strings.Join(report.Usage.Sources, ", "))
	if report.Unexpected != nil {
		fmt.Fprintf(out, "  Unexpected       %s\n", *report.Unexpected)
	}
}

func hasMCPHelpArg(args []string) bool {
	for _, arg := range args {
		if isMCPHelpArg(arg) {
			return true
		}
	}
	return false
}

func mcpHelpUnexpected(args []string) string {
	for index, arg := range args {
		if !isMCPHelpArg(arg) {
			continue
		}
		if index == 0 {
			return ""
		}
		return strings.Join(args[:index], " ")
	}
	return ""
}

func isMCPHelpArg(arg string) bool {
	switch strings.TrimSpace(arg) {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

type mcpShowReportOptions struct {
	Workspace           string
	ServerName          string
	Server              *config.MCPServerConfig
	AvailableServers    []string
	Validation          localstatus.MCPValidationStatus
	ConfigLoadError     string
	ConfigLoadErrorKind string
}

func buildMCPShowReport(options mcpShowReportOptions) mcpShowReport {
	workspace := strings.TrimSpace(options.Workspace)
	if workspace == "" {
		workspace = "."
	}
	status := "ok"
	var loadError *string
	if strings.TrimSpace(options.ConfigLoadError) != "" {
		status = "degraded"
		value := strings.TrimSpace(options.ConfigLoadError)
		loadError = &value
		if strings.TrimSpace(options.ConfigLoadErrorKind) == "" {
			options.ConfigLoadErrorKind = "config_load_failed"
		}
	} else if options.Validation.InvalidCount > 0 {
		status = "degraded"
	}
	report := mcpShowReport{
		Kind:                "mcp",
		Action:              "show",
		Status:              status,
		WorkingDirectory:    workspace,
		Found:               options.Server != nil,
		ServerName:          strings.TrimSpace(options.ServerName),
		ConfigLoadError:     loadError,
		ConfigLoadErrorKind: strings.TrimSpace(options.ConfigLoadErrorKind),
		AvailableServers:    append([]string(nil), options.AvailableServers...),
		TotalConfigured:     options.Validation.TotalConfigured,
		ValidCount:          options.Validation.ValidCount,
		InvalidCount:        options.Validation.InvalidCount,
		InvalidServers:      append([]localstatus.ValidationIssue(nil), options.Validation.InvalidServers...),
	}
	if options.Server != nil {
		server := *options.Server
		descriptor := mcp.DescribeServer(report.ServerName, server)
		report.Server = &descriptor
		report.Signature = mcp.ServerSignature(server)
		report.ConfigHash = mcp.ServerConfigHash(server)
		report.ServerName = ""
		return report
	}
	if report.Status == "ok" {
		report.Status = "error"
	}
	report.ErrorKind = "server_not_found"
	report.Message = fmt.Sprintf("server %q is not configured", strings.TrimSpace(options.ServerName))
	report.Hint = "Run `codog mcp list` to see configured servers."
	return report
}

func renderMCPShowReport(out io.Writer, format string, report mcpShowReport) {
	if format == "text" {
		fmt.Fprintln(out, "MCP")
		fmt.Fprintf(out, "  Working directory %s\n", report.WorkingDirectory)
		fmt.Fprintf(out, "  Status           %s\n", report.Status)
		if report.ConfigLoadError != nil {
			fmt.Fprintf(out, "  Config load      degraded: %s\n", *report.ConfigLoadError)
		}
		if !report.Found {
			fmt.Fprintf(out, "  Result           server `%s` is not configured\n", report.ServerName)
			if len(report.AvailableServers) > 0 {
				fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.AvailableServers, ", "))
			}
			return
		}
		fmt.Fprintf(out, "  Name             %s\n", report.Server.Name)
		fmt.Fprintf(out, "  Transport        %s\n", report.Server.Transport.Label)
		fmt.Fprintf(out, "  Required         %t\n", report.Server.Required)
		if report.Server.Details.URL != "" {
			fmt.Fprintf(out, "  URL              %s\n", report.Server.Details.URL)
		}
		if report.Server.Details.Command != "" {
			fmt.Fprintf(out, "  Command          %s\n", report.Server.Details.Command)
			fmt.Fprintf(out, "  Args count       %d\n", report.Server.Details.ArgsCount)
		}
		if report.Server.Details.ToolCallTimeoutMS > 0 {
			fmt.Fprintf(out, "  Tool timeout     %dms\n", report.Server.Details.ToolCallTimeoutMS)
		}
		if len(report.Server.Details.EnvKeys) > 0 {
			fmt.Fprintf(out, "  Env keys         %s\n", strings.Join(report.Server.Details.EnvKeys, ", "))
		}
		if len(report.Server.Details.HeaderKeys) > 0 {
			fmt.Fprintf(out, "  Header keys      %s\n", strings.Join(report.Server.Details.HeaderKeys, ", "))
		}
		if report.Server.Details.HeadersHelperConfigured {
			fmt.Fprintln(out, "  Headers helper   configured")
		}
		fmt.Fprintf(out, "  Signature        %s\n", report.Signature)
		fmt.Fprintf(out, "  Config hash      %s\n", report.ConfigHash)
		return
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(out, string(data))
}

func renderMCPListReport(out io.Writer, format string, report mcpListReport) {
	if format == "text" {
		fmt.Fprintln(out, "MCP")
		fmt.Fprintf(out, "  Working directory %s\n", report.WorkingDirectory)
		fmt.Fprintf(out, "  Status           %s\n", report.Status)
		fmt.Fprintf(out, "  Configured servers %d\n", report.ConfiguredServers)
		fmt.Fprintf(out, "  Total entries     %d\n", report.TotalConfigured)
		fmt.Fprintf(out, "  Required entries  %d\n", report.RequiredCount)
		fmt.Fprintf(out, "  Optional entries  %d\n", report.OptionalCount)
		fmt.Fprintf(out, "  Invalid entries   %d\n", report.InvalidCount)
		fmt.Fprintf(out, "  Startup gate      %s", report.Startup.Status)
		if report.Startup.RequiredFailedCount > 0 {
			fmt.Fprintf(out, " (%d required failed)", report.Startup.RequiredFailedCount)
		} else if report.Startup.OptionalFailedCount > 0 {
			fmt.Fprintf(out, " (%d optional failed)", report.Startup.OptionalFailedCount)
		}
		fmt.Fprintln(out)
		if report.ConfigLoadError != nil {
			fmt.Fprintf(out, "  Config load      degraded: %s\n", *report.ConfigLoadError)
			fmt.Fprintln(out, "  Hint             Fix the listed config file or run `codog doctor` for details.")
		}
		if len(report.Servers) == 0 {
			fmt.Fprintln(out, "  No valid MCP servers configured.")
		} else {
			fmt.Fprintln(out)
			for _, server := range report.Servers {
				transport := "stdio"
				if strings.TrimSpace(server.URL) != "" {
					transport = "http"
				}
				fmt.Fprintf(out, "  %-16s %-13s %-7s %s\n", server.Name, transport, server.Status, mcpServerStatusSummary(server))
			}
		}
		if len(report.Startup.FailedRequired) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Failed required MCP servers")
			for _, failure := range report.Startup.FailedRequired {
				fmt.Fprintf(out, "    - %s: %s\n", failure.Name, mcpStartupFailureSummary(failure))
			}
		}
		if len(report.Startup.FailedOptional) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Failed optional MCP servers")
			for _, failure := range report.Startup.FailedOptional {
				fmt.Fprintf(out, "    - %s: %s\n", failure.Name, mcpStartupFailureSummary(failure))
			}
		}
		if len(report.InvalidServers) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Invalid MCP servers")
			for _, invalid := range report.InvalidServers {
				fmt.Fprintf(out, "    - %s: %s\n", invalid.Name, invalid.Reason)
			}
		}
		if len(report.NextActions) > 0 {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Next actions")
			for _, action := range report.NextActions {
				fmt.Fprintf(out, "    - %s\n", action)
			}
		}
		return
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(out, string(data))
}

func mcpStartupFailureSummary(failure mcp.StartupFailure) string {
	parts := []string{strings.TrimSpace(failure.Status)}
	if strings.TrimSpace(failure.Phase) != "" {
		parts = append(parts, "phase "+strings.TrimSpace(failure.Phase))
	}
	if strings.TrimSpace(failure.Error) != "" {
		parts = append(parts, strings.TrimSpace(failure.Error))
	}
	return strings.Join(parts, " | ")
}

func mcpServerStatusSummary(server mcp.ServerStatus) string {
	if strings.TrimSpace(server.Error) != "" {
		return "error: " + strings.TrimSpace(server.Error)
	}
	command := strings.TrimSpace(server.Command)
	if command == "" {
		command = strings.TrimSpace(server.URL)
	}
	if command == "" {
		command = "<unknown>"
	}
	parts := []string{command}
	if server.ToolCount == 1 {
		parts = append(parts, "1 tool")
	} else if server.ToolCount > 1 {
		parts = append(parts, fmt.Sprintf("%d tools", server.ToolCount))
	}
	if strings.TrimSpace(server.ProtocolVersion) != "" {
		parts = append(parts, "protocol "+strings.TrimSpace(server.ProtocolVersion))
	}
	return strings.Join(parts, " | ")
}

func currentWorkingDirectory() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

type mcpSelfReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	ToolCount     int      `json:"tool_count"`
	ResourceCount int      `json:"resource_count"`
	PromptCount   int      `json:"prompt_count"`
	Tools         []string `json:"tools"`
	Resources     []string `json:"resources"`
	Prompts       []string `json:"prompts"`
}

func (a *App) mcpSelf(ctx context.Context, args []string, format string) error {
	if len(args) != 0 {
		return errors.New("usage: codog mcp self [--json|--output-format text|json]")
	}
	registry := a.mcpRegistry()
	input := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/list","params":{}}`,
		"",
	}, "\n"))
	var output bytes.Buffer
	if err := mcpserver.Serve(ctx, input, &output, registry, a.mcpServerOptions()); err != nil {
		return err
	}
	responses, err := decodeMCPResponseLines(output.String())
	if err != nil {
		return err
	}
	report := mcpSelfReport{Kind: "mcp", Action: "self", Status: "ok"}
	for _, response := range responses {
		result, _ := response["result"].(map[string]any)
		if result == nil {
			continue
		}
		if values, ok := result["tools"].([]any); ok {
			report.Tools = mcpValueStrings(values, "name")
			report.ToolCount = len(report.Tools)
		}
		if values, ok := result["resources"].([]any); ok {
			report.Resources = mcpValueStrings(values, "uri")
			report.ResourceCount = len(report.Resources)
		}
		if values, ok := result["prompts"].([]any); ok {
			report.Prompts = mcpValueStrings(values, "name")
			report.PromptCount = len(report.Prompts)
		}
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "MCP Self")
	fmt.Fprintf(a.Out, "  Status           %s\n", report.Status)
	fmt.Fprintf(a.Out, "  Tools            %d\n", report.ToolCount)
	fmt.Fprintf(a.Out, "  Resources        %d\n", report.ResourceCount)
	fmt.Fprintf(a.Out, "  Prompts          %d\n", report.PromptCount)
	if len(report.Resources) > 0 {
		fmt.Fprintf(a.Out, "  Resource URIs    %s\n", strings.Join(report.Resources, ", "))
	}
	return nil
}

func decodeMCPResponseLines(output string) ([]map[string]any, error) {
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func mcpValueStrings(values []any, key string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		item, _ := value.(map[string]any)
		text, _ := item[key].(string)
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) mcpServe(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: codog mcp serve")
	}
	return mcpserver.Serve(ctx, mcpReader(a.In, os.Stdin), mcpWriter(a.Out, os.Stdout), a.mcpRegistry(), a.mcpServerOptions())
}

func (a *App) mcpRegistry() *tools.Registry {
	registry := a.Tools
	if registry == nil {
		options := toolRegistryOptionsFromConfig(a.Config, a.Config.AdditionalDirs, nil, nil, a.Executable, a.AgentDefinitions)
		options.PluginDirs = append([]string(nil), a.PluginDirs...)
		registry = tools.NewRegistryWithOptions(a.Workspace, options)
	}
	return registry
}

func (a *App) mcpServerOptions() mcpserver.Options {
	return mcpserver.Options{
		Version:         version,
		Workspace:       a.Workspace,
		PermissionMode:  a.Config.PermissionMode,
		PermissionRules: a.Config.PermissionRules,
	}
}

func mcpReader(value io.Reader, fallback io.Reader) io.Reader {
	if value != nil {
		return value
	}
	return fallback
}

func mcpWriter(value io.Writer, fallback io.Writer) io.Writer {
	if value != nil {
		return value
	}
	return fallback
}

func (a *App) mcpShow(args []string, format string) error {
	if len(args) != 1 {
		return renderActionError(a.Out, actionErrorReport{
			Kind:      "mcp",
			Action:    "show",
			Status:    "error",
			ErrorKind: "missing_argument",
			Message:   "mcp show requires a server name",
			Hint:      "Usage: codog mcp show <server>.",
		}, format)
	}
	name := args[0]
	validation := buildMCPValidation(a.Config.MCPServers)
	server, ok := a.Config.MCPServers[name]
	var serverPtr *config.MCPServerConfig
	if ok {
		serverCopy := server
		serverPtr = &serverCopy
	}
	renderMCPShowReport(a.Out, format, buildMCPShowReport(mcpShowReportOptions{
		Workspace:        a.Workspace,
		ServerName:       name,
		Server:           serverPtr,
		AvailableServers: sortedMCPServerNames(a.Config.MCPServers),
		Validation:       validation,
	}))
	return nil
}

func (a *App) mcpAdd(args []string, format string) error {
	req, err := parseMCPAddArgs(args)
	if err != nil {
		switch {
		case errors.Is(err, errMCPAddMissingName):
			return renderMissingActionArgument(a.Out, "mcp", "add", "server_name", "mcp add requires a server name", "Usage: codog mcp add NAME COMMAND [ARG...] [--env KEY=VALUE] [--tool-call-timeout-ms N] [--required] or codog mcp add NAME --url URL [--header KEY=VALUE] [--headers-helper COMMAND] [--required].", format)
		case errors.Is(err, errMCPAddMissingCommand):
			return renderMissingActionArgument(a.Out, "mcp", "add", "command_or_url", "mcp add requires a command or --url", "Usage: codog mcp add NAME COMMAND [ARG...] [--env KEY=VALUE] [--tool-call-timeout-ms N] [--required] or codog mcp add NAME --url URL [--header KEY=VALUE] [--headers-helper COMMAND] [--required].", format)
		}
		return err
	}
	path := filepath.Join(a.Config.ConfigHome, "config.json")
	server := config.MCPServerConfig{Command: req.Command, Args: req.Args, Env: req.Env, URL: req.URL, Headers: req.Headers, HeadersHelper: req.HeadersHelper, ToolCallTimeoutMS: req.ToolCallTimeoutMS, Required: req.Required}
	report, err := config.SetFileValue(path, "mcp_servers."+req.Name, server)
	if err != nil {
		return err
	}
	if a.Config.MCPServers == nil {
		a.Config.MCPServers = map[string]config.MCPServerConfig{}
	}
	a.Config.MCPServers[req.Name] = server
	if err := a.refreshBuiltinToolScope(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{
		"kind":   "mcp",
		"action": "add",
		"status": "ok",
		"name":   req.Name,
		"path":   report.Path,
		"server": server,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

func (a *App) mcpRemove(args []string, format string) error {
	if len(args) != 1 {
		if len(args) == 0 {
			return renderMissingActionArgument(a.Out, "mcp", "remove", "server_name", "mcp remove requires a server name", "Usage: codog mcp remove SERVER.", format)
		}
		return errors.New("usage: codog mcp remove SERVER")
	}
	name := args[0]
	if err := validateMCPServerName(name); err != nil {
		return err
	}
	path := filepath.Join(a.Config.ConfigHome, "config.json")
	report, err := config.UnsetFileValue(path, "mcp_servers."+name)
	if err != nil {
		return err
	}
	delete(a.Config.MCPServers, name)
	if err := a.refreshBuiltinToolScope(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]any{
		"kind":    "mcp",
		"action":  "remove",
		"status":  "ok",
		"name":    name,
		"path":    report.Path,
		"removed": true,
	}, "", "  ")
	fmt.Fprintln(a.Out, string(data))
	return nil
}

type mcpAddRequest struct {
	Name              string
	Command           string
	Args              []string
	Env               []string
	URL               string
	Headers           map[string]string
	HeadersHelper     string
	ToolCallTimeoutMS int
	Required          bool
}

var (
	errMCPAddMissingName    = errors.New("mcp add missing name")
	errMCPAddMissingCommand = errors.New("mcp add missing command")
)

func parseMCPAddArgs(args []string) (mcpAddRequest, error) {
	var req mcpAddRequest
	positionals, err := parseMCPAddOptions(args, &req)
	if err != nil {
		return req, err
	}
	return finishMCPAddRequest(req, positionals)
}

func parseMCPAddOptions(args []string, req *mcpAddRequest) ([]string, error) {
	options := mcpAddValueOptions(req)
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if arg == "--required" {
			req.Required = true
			continue
		}
		if arg == "--optional" {
			req.Required = false
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return nil, err
		}
		if !handled {
			positionals = append(positionals, arg)
		}
	}
	return positionals, nil
}

func mcpAddValueOptions(req *mcpAddRequest) map[string]valueOption {
	options := map[string]valueOption{
		"--url":            stringValueOption(&req.URL, "mcp add url value is required"),
		"--headers-helper": stringValueOption(&req.HeadersHelper, "mcp add headers helper value is required"),
		"--header": {
			missing: func(string) error { return errors.New("mcp add header value is required") },
			set:     func(value string) error { return setMCPAddHeader(req, value) },
		},
		"--tool-call-timeout-ms": {
			missing: func(string) error { return errors.New("mcp add tool call timeout value is required") },
			set:     func(value string) error { return setMCPAddTimeout(req, value) },
		},
		"--env": {
			missing: func(string) error { return errors.New("mcp add env value is required") },
			set: func(value string) error {
				req.Env = append(req.Env, value)
				return nil
			},
		},
	}
	options["-H"] = options["--header"]
	options["--headersHelper"] = options["--headers-helper"]
	options["--toolCallTimeoutMs"] = options["--tool-call-timeout-ms"]
	options["-e"] = options["--env"]
	return options
}

func setMCPAddHeader(req *mcpAddRequest, value string) error {
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	return addMCPHeader(req.Headers, value)
}

func setMCPAddTimeout(req *mcpAddRequest, value string) error {
	timeout, err := parseMCPToolCallTimeout(value)
	if err != nil {
		return err
	}
	req.ToolCallTimeoutMS = timeout
	return nil
}

func finishMCPAddRequest(req mcpAddRequest, positionals []string) (mcpAddRequest, error) {
	if len(positionals) < 1 {
		return req, errMCPAddMissingName
	}
	req.Name = positionals[0]
	if err := validateMCPServerName(req.Name); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.URL) != "" {
		if err := validateMCPHTTPAdd(req, positionals); err != nil {
			return req, err
		}
		req.URL = strings.TrimSpace(req.URL)
	} else {
		if err := validateMCPStdioAdd(req, positionals); err != nil {
			return req, err
		}
		req.Command = positionals[1]
		req.Args = append([]string(nil), positionals[2:]...)
	}
	req.HeadersHelper = strings.TrimSpace(req.HeadersHelper)
	req.Env = compactMCPEnv(req.Env)
	for _, value := range req.Env {
		if key, _, ok := strings.Cut(value, "="); !ok || strings.TrimSpace(key) == "" {
			return req, fmt.Errorf("mcp env value must use KEY=VALUE: %s", value)
		}
	}
	return req, nil
}

func validateMCPHTTPAdd(req mcpAddRequest, positionals []string) error {
	if req.ToolCallTimeoutMS > 0 {
		return errors.New("mcp add --tool-call-timeout-ms applies only to stdio servers")
	}
	if len(positionals) > 1 {
		return errors.New("mcp add with --url does not accept COMMAND arguments")
	}
	return validateMCPAddURL(req.URL)
}

func validateMCPStdioAdd(req mcpAddRequest, positionals []string) error {
	if strings.TrimSpace(req.HeadersHelper) != "" {
		return errors.New("mcp add --headers-helper requires --url")
	}
	if len(positionals) < 2 {
		return errMCPAddMissingCommand
	}
	return nil
}

func parseMCPToolCallTimeout(value string) (int, error) {
	timeout, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("mcp tool call timeout must be a positive integer in milliseconds: %s", value)
	}
	return timeout, nil
}

func validateMCPAddURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("mcp url must use http or https")
	}
	if parsed.Host == "" {
		return errors.New("mcp url host is required")
	}
	return nil
}

func addMCPHeader(headers map[string]string, value string) error {
	key, headerValue, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("mcp header value must use KEY=VALUE: %s", value)
	}
	headers[strings.TrimSpace(key)] = headerValue
	return nil
}

func compactMCPEnv(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validateMCPServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mcp server name is required")
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid MCP server name %q", name)
	}
	return nil
}

type simpleCompatibilityReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Command             string   `json:"command"`
	Workspace           string   `json:"workspace,omitempty"`
	Message             string   `json:"message"`
	Args                []string `json:"args,omitempty"`
	ProviderRequestMade bool     `json:"provider_request_made"`
	WorkspaceWillMutate bool     `json:"workspace_will_mutate"`
	NextCommand         string   `json:"next_command,omitempty"`
	File                string   `json:"file,omitempty"`
	Bytes               int      `json:"bytes,omitempty"`
	SessionID           string   `json:"session_id,omitempty"`
	SessionMessages     int      `json:"session_messages,omitempty"`
	PluginID            string   `json:"plugin_id,omitempty"`
	PluginRoot          string   `json:"plugin_root,omitempty"`
	ManifestFile        string   `json:"manifest_file,omitempty"`
	CommandFile         string   `json:"command_file,omitempty"`
	DryRun              bool     `json:"dry_run,omitempty"`
	Created             bool     `json:"created,omitempty"`
	Force               bool     `json:"force,omitempty"`
}

func (a *App) ExitCompatibility(args []string) error {
	clean, format, err := stripJSONOnlyOutputFormat("exit", args)
	if err != nil {
		return err
	}
	report := simpleCompatibilityReport{
		Kind:                "exit",
		Action:              "exit",
		Status:              "ok",
		Command:             "exit",
		Workspace:           a.Workspace,
		Message:             "Exit requested. In the REPL, `/exit` closes the interactive session; as a CLI command this reports success and exits with status 0.",
		Args:                clean,
		ProviderRequestMade: false,
		WorkspaceWillMutate: false,
	}
	return renderSimpleCompatibility(a.Out, report, format)
}

func (a *App) GoodClaude(args []string) error {
	clean, format, err := stripJSONOnlyOutputFormat("good-claude", args)
	if err != nil {
		return err
	}
	message := strings.TrimSpace(strings.Join(clean, " "))
	if message == "" {
		message = "Good Claude"
	}
	feedback, err := a.writeFeedback(feedbackRequest{
		Message: "Positive feedback from good-claude: " + message,
	})
	if err != nil {
		return err
	}
	report := simpleCompatibilityReport{
		Kind:                "feedback",
		Action:              "good_claude",
		Status:              "ok",
		Command:             "good-claude",
		Workspace:           a.Workspace,
		Message:             "Positive feedback was written to a local feedback draft.",
		Args:                clean,
		ProviderRequestMade: false,
		WorkspaceWillMutate: true,
		NextCommand:         "codog feedback " + shellQuote(message),
		File:                feedback.File,
		Bytes:               feedback.Bytes,
		SessionID:           feedback.SessionID,
		SessionMessages:     feedback.SessionMessages,
	}
	return renderSimpleCompatibility(a.Out, report, format)
}

func renderSimpleCompatibility(out io.Writer, report simpleCompatibilityReport, format string) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, "Compatibility")
	fmt.Fprintf(out, "  Command          %s\n", report.Command)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Message          %s\n", report.Message)
	if report.NextCommand != "" {
		fmt.Fprintf(out, "  Next             %s\n", report.NextCommand)
	}
	if report.File != "" {
		fmt.Fprintf(out, "  File             %s\n", report.File)
	}
	if report.PluginID != "" {
		fmt.Fprintf(out, "  Plugin           %s\n", report.PluginID)
	}
	if report.ManifestFile != "" {
		fmt.Fprintf(out, "  Manifest         %s\n", report.ManifestFile)
	}
	if report.CommandFile != "" {
		fmt.Fprintf(out, "  Command file     %s\n", report.CommandFile)
	}
	if report.DryRun {
		fmt.Fprintln(out, "  Dry run          true")
	}
	if report.Created {
		fmt.Fprintln(out, "  Created          true")
	}
	return nil
}

type usageOverviewReport struct {
	Kind                     string                `json:"kind"`
	Action                   string                `json:"action"`
	Status                   string                `json:"status"`
	SessionID                string                `json:"session_id,omitempty"`
	Model                    string                `json:"model"`
	MessageCount             int                   `json:"message_count"`
	InputTokens              int                   `json:"input_tokens"`
	OutputTokens             int                   `json:"output_tokens"`
	CacheCreationInputTokens int                   `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                   `json:"cache_read_input_tokens,omitempty"`
	TotalTokens              int                   `json:"total_tokens"`
	EstimatedUSD             float64               `json:"estimated_usd"`
	CostUSD                  *float64              `json:"cost_usd,omitempty"`
	Source                   string                `json:"source"`
	MaxOutputTokens          int                   `json:"max_output_tokens,omitempty"`
	ContextWindowTokens      int                   `json:"context_window_tokens,omitempty"`
	ContextRemainingTokens   int                   `json:"context_remaining_tokens,omitempty"`
	ContextUsedRatio         float64               `json:"context_used_ratio,omitempty"`
	Summary                  usage.Summary         `json:"summary"`
	Roles                    []usage.RoleUsage     `json:"roles,omitempty"`
	Blocks                   []usage.BlockUsage    `json:"blocks,omitempty"`
	ToolUse                  *usage.ToolUseSummary `json:"tool_use,omitempty"`
	Message                  string                `json:"message,omitempty"`
}

func (a *App) ShowCost(overrides config.FlagOverrides) error {
	return a.UsageOverview("cost", []string{"--output-format", "json"}, overrides)
}

func (a *App) UsageOverview(command string, args []string, overrides config.FlagOverrides) error {
	kind := normalizeUsageOverviewKind(command)
	format, err := parseSimpleOutputFormat(kind, args)
	if err != nil {
		return err
	}
	report, err := a.buildUsageOverviewReport(kind, overrides)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderUsageOverview(a.Out, report)
	return nil
}

func normalizeUsageOverviewKind(command string) string {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "tokens":
		return "tokens"
	case "stats":
		return "stats"
	default:
		return "cost"
	}
}

func (a *App) buildUsageOverviewReport(kind string, overrides config.FlagOverrides) (usageOverviewReport, error) {
	sess, err := a.openSession(overrides)
	if err != nil {
		return usageOverviewReport{}, err
	}
	actual, _ := a.sessionUsageValues(sess.ID)
	usageReport := usage.BuildReportWithUsage(sess.ID, a.Config.Model, sess.Messages, actual)
	summary := usageReport.Summary
	report := usageOverviewReport{
		Kind:                     normalizeUsageOverviewKind(kind),
		Action:                   "show",
		Status:                   "ok",
		SessionID:                sess.ID,
		Model:                    a.Config.Model,
		MessageCount:             len(sess.Messages),
		InputTokens:              summary.InputTokens,
		OutputTokens:             summary.OutputTokens,
		CacheCreationInputTokens: summary.CacheCreationInputTokens,
		CacheReadInputTokens:     summary.CacheReadInputTokens,
		TotalTokens:              summary.TotalTokens,
		EstimatedUSD:             summary.EstimatedUSD,
		Source:                   summary.Source,
		Summary:                  summary,
	}
	if report.Kind == "cost" {
		costUSD := summary.EstimatedUSD
		report.CostUSD = &costUSD
	}
	if limit, ok := modelrouting.TokenLimitForModel(a.Config.Model); ok {
		report.MaxOutputTokens = limit.MaxOutputTokens
		report.ContextWindowTokens = limit.ContextWindowTokens
		if limit.ContextWindowTokens > summary.TotalTokens {
			report.ContextRemainingTokens = limit.ContextWindowTokens - summary.TotalTokens
		}
		report.ContextUsedRatio = roundedRatio(summary.TotalTokens, limit.ContextWindowTokens)
	}
	if report.Kind == "stats" {
		report.Roles = append([]usage.RoleUsage(nil), usageReport.Roles...)
		report.Blocks = append([]usage.BlockUsage(nil), usageReport.Blocks...)
		toolUse := usageReport.ToolUse
		report.ToolUse = &toolUse
	}
	return report, nil
}

func renderUsageOverview(out io.Writer, report usageOverviewReport) {
	fmt.Fprintln(out, usageOverviewTitle(report.Kind))
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	fmt.Fprintf(out, "  Model            %s\n", emptyAsNone(report.Model))
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Total tokens     %d\n", report.TotalTokens)
	fmt.Fprintf(out, "  Input tokens     %d\n", report.InputTokens)
	fmt.Fprintf(out, "  Output tokens    %d\n", report.OutputTokens)
	if report.CacheCreationInputTokens != 0 || report.CacheReadInputTokens != 0 {
		fmt.Fprintf(out, "  Cache tokens     create=%d read=%d\n", report.CacheCreationInputTokens, report.CacheReadInputTokens)
	}
	if report.ContextWindowTokens > 0 {
		fmt.Fprintf(out, "  Context window   %d\n", report.ContextWindowTokens)
		fmt.Fprintf(out, "  Context used     %.2f%%\n", report.ContextUsedRatio*100)
		fmt.Fprintf(out, "  Context left     %d\n", report.ContextRemainingTokens)
	}
	if report.Kind == "cost" {
		costUSD := report.EstimatedUSD
		if report.CostUSD != nil {
			costUSD = *report.CostUSD
		}
		fmt.Fprintf(out, "  Cost USD         %.5f\n", costUSD)
	} else {
		fmt.Fprintf(out, "  Estimated USD    %.5f\n", report.EstimatedUSD)
	}
	fmt.Fprintf(out, "  Source           %s\n", emptyAsNone(report.Source))
	if report.Kind == "stats" && report.ToolUse != nil {
		fmt.Fprintf(out, "  Tool use         calls=%d results=%d errors=%d\n", report.ToolUse.ToolUses, report.ToolUse.ToolResults, report.ToolUse.Errors)
	}
}

func usageOverviewTitle(kind string) string {
	switch normalizeUsageOverviewKind(kind) {
	case "tokens":
		return "Tokens"
	case "stats":
		return "Stats"
	default:
		return "Cost"
	}
}

func (a *App) Usage(args []string, overrides config.FlagOverrides) error {
	format, err := parseSimpleOutputFormat("usage", args)
	if err != nil {
		return err
	}
	sess, err := a.openSession(overrides)
	if err != nil {
		return err
	}
	actual, _ := a.sessionUsageValues(sess.ID)
	report := usage.BuildReportWithUsage(sess.ID, a.Config.Model, sess.Messages, actual)
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	usage.RenderText(a.Out, report)
	return nil
}

type cacheReport struct {
	Kind                     string  `json:"kind"`
	Action                   string  `json:"action"`
	Status                   string  `json:"status"`
	SessionID                string  `json:"session_id,omitempty"`
	Model                    string  `json:"model,omitempty"`
	MessageCount             int     `json:"message_count"`
	UsageRecords             int     `json:"usage_records"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheTotalInputTokens    int     `json:"cache_total_input_tokens"`
	TotalTokens              int     `json:"total_tokens"`
	CacheHitRatio            float64 `json:"cache_hit_ratio"`
	Source                   string  `json:"source"`
	Message                  string  `json:"message,omitempty"`
}

func (a *App) Cache(args []string, overrides config.FlagOverrides) error {
	format, parsedOverrides, err := parseCacheArgs(args, overrides)
	if err != nil {
		return err
	}
	report, err := a.buildCacheReport(parsedOverrides)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCacheReport(a.Out, report)
	return nil
}

func parseCacheArgs(args []string, overrides config.FlagOverrides) (string, config.FlagOverrides, error) {
	format := "text"
	const usage = "codog cache [--session ID|--resume ID] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return "", overrides, missingFlagValueError{Command: "cache", Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return "", overrides, missingFlagValueError{Command: "cache", Flag: arg, Usage: usage}
			}
			overrides.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			overrides.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return "", overrides, missingFlagValueError{Command: "cache", Flag: arg, Usage: usage}
			}
			overrides.Resume = args[index]
		case strings.HasPrefix(arg, "--resume="):
			overrides.Resume = strings.TrimPrefix(arg, "--resume=")
		default:
			if strings.HasPrefix(arg, "-") {
				return "", overrides, unknownOptionError{Command: "cache", Option: arg, Usage: usage}
			}
			return "", overrides, unexpectedExtraArgsError{Command: "cache", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("cache", format, []string{"text", "json"})
	if err != nil {
		return "", overrides, err
	}
	return normalizedFormat, overrides, nil
}

func (a *App) buildCacheReport(overrides config.FlagOverrides) (cacheReport, error) {
	sess, err := a.openSession(overrides)
	if err != nil {
		return cacheReport{}, err
	}
	entries, err := a.Sessions.Usage(sess.ID)
	if err != nil {
		return cacheReport{}, err
	}
	usages := make([]anthropic.Usage, 0, len(entries))
	for _, entry := range entries {
		usages = append(usages, entry.Usage)
	}
	summary, ok := usage.ActualSummary(usages, a.Config.Model)
	source := "actual"
	if !ok {
		summary = usage.Estimate(sess.Messages, a.Config.Model)
		source = "none"
	}
	cacheTotal := summary.CacheCreationInputTokens + summary.CacheReadInputTokens
	ratio := 0.0
	denominator := summary.InputTokens + cacheTotal
	if denominator > 0 {
		ratio = float64(summary.CacheReadInputTokens) / float64(denominator)
	}
	report := cacheReport{
		Kind:                     "cache",
		Action:                   "show",
		Status:                   "ok",
		SessionID:                sess.ID,
		Model:                    a.Config.Model,
		MessageCount:             len(sess.Messages),
		UsageRecords:             len(entries),
		InputTokens:              summary.InputTokens,
		OutputTokens:             summary.OutputTokens,
		CacheCreationInputTokens: summary.CacheCreationInputTokens,
		CacheReadInputTokens:     summary.CacheReadInputTokens,
		CacheTotalInputTokens:    cacheTotal,
		TotalTokens:              summary.TotalTokens,
		CacheHitRatio:            math.Round(ratio*10000) / 10000,
		Source:                   source,
	}
	if source == "none" {
		report.Message = "No provider cache usage records were found for this session."
	} else if cacheTotal == 0 {
		report.Message = "Provider usage is recorded, but no prompt cache tokens were reported."
	}
	return report, nil
}

func renderCacheReport(out io.Writer, report cacheReport) {
	fmt.Fprintln(out, "Prompt Cache")
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	fmt.Fprintf(out, "  Model            %s\n", emptyAsNone(report.Model))
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Usage records    %d\n", report.UsageRecords)
	fmt.Fprintf(out, "  Cache created    %d\n", report.CacheCreationInputTokens)
	fmt.Fprintf(out, "  Cache read       %d\n", report.CacheReadInputTokens)
	fmt.Fprintf(out, "  Cache total      %d\n", report.CacheTotalInputTokens)
	fmt.Fprintf(out, "  Input tokens     %d\n", report.InputTokens)
	fmt.Fprintf(out, "  Output tokens    %d\n", report.OutputTokens)
	fmt.Fprintf(out, "  Total tokens     %d\n", report.TotalTokens)
	fmt.Fprintf(out, "  Hit ratio        %.2f%%\n", report.CacheHitRatio*100)
	fmt.Fprintf(out, "  Source           %s\n", report.Source)
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type breakCacheRequest struct {
	Format    string
	SessionID string
	Message   string
}

type breakCacheReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	SessionID      string `json:"session_id"`
	MessageCount   int    `json:"message_count"`
	Path           string `json:"path"`
	Nonce          string `json:"nonce"`
	Marker         string `json:"marker"`
	CreatedSession bool   `json:"created_session"`
}

func (a *App) BreakCache(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseBreakCacheArgs(args, overrides)
	if err != nil {
		return err
	}
	sess, created, err := a.breakCacheSession(req.SessionID)
	if err != nil {
		return err
	}
	nonce, err := newCacheBreakerNonce()
	if err != nil {
		return err
	}
	marker := strings.TrimSpace(req.Message)
	if marker == "" {
		marker = "Codog cache breaker nonce: " + nonce
	}
	if !strings.Contains(marker, nonce) {
		marker = strings.TrimSpace(marker + "\n\nCodog cache breaker nonce: " + nonce)
	}
	if err := a.Sessions.Append(sess.ID, anthropic.TextMessage("user", marker)); err != nil {
		return err
	}
	report := breakCacheReport{
		Kind:           "break_cache",
		Action:         "append_marker",
		Status:         "ok",
		SessionID:      sess.ID,
		MessageCount:   len(sess.Messages) + 1,
		Path:           sess.Path,
		Nonce:          nonce,
		Marker:         marker,
		CreatedSession: created,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBreakCacheReport(a.Out, report)
	return nil
}

func parseBreakCacheArgs(args []string, overrides config.FlagOverrides) (breakCacheRequest, error) {
	req := breakCacheRequest{Format: "text"}
	const usage = "codog break-cache [MESSAGE] [--session ID|--resume ID] [--message TEXT] [--output-format text|json]"
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
			if index >= len(args) {
				return req, missingFlagValueError{Command: "break-cache", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "break-cache", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "break-cache", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--message":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "break-cache", Flag: arg, Usage: usage}
			}
			req.Message = args[index]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "break-cache", Option: arg, Usage: usage}
		default:
			if req.Message == "" {
				req.Message = strings.Join(args[index:], " ")
				index = len(args)
				continue
			}
			return req, unexpectedExtraArgsError{Command: "break-cache", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("break-cache", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func (a *App) breakCacheSession(id string) (*session.Session, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "latest"
	}
	if session.IsSessionReferenceAlias(id) {
		latest, err := a.Sessions.LatestID()
		if errors.Is(err, session.ErrNoSessions) {
			created, createErr := a.Sessions.CreateWithIdentity("", session.SessionIdentity{
				Workspace: a.Workspace,
				Worktree:  a.Workspace,
				Purpose:   "break-cache",
			})
			return created, true, createErr
		}
		if err != nil {
			return nil, false, err
		}
		id = latest
	}
	exists, err := a.Sessions.Exists(id)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		created, createErr := a.Sessions.CreateWithIdentity(id, session.SessionIdentity{
			Workspace: a.Workspace,
			Worktree:  a.Workspace,
			Purpose:   "break-cache",
		})
		return created, true, createErr
	}
	opened, err := a.Sessions.Open(id)
	return opened, false, err
}

func newCacheBreakerNonce() (string, error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func renderBreakCacheReport(out io.Writer, report breakCacheReport) {
	fmt.Fprintln(out, "Break Cache")
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Messages         %d\n", report.MessageCount)
	fmt.Fprintf(out, "  Created session  %t\n", report.CreatedSession)
	fmt.Fprintf(out, "  Nonce            %s\n", report.Nonce)
	fmt.Fprintf(out, "  Path             %s\n", report.Path)
}

type metricsRequest struct {
	Format    string
	Limit     int
	Overrides config.FlagOverrides
}

type metricsReport struct {
	Kind             string                 `json:"kind"`
	Action           string                 `json:"action"`
	Status           string                 `json:"status"`
	Workspace        string                 `json:"workspace,omitempty"`
	Model            string                 `json:"model,omitempty"`
	Session          *metricsSessionReport  `json:"session,omitempty"`
	WorkspaceMetrics metricsWorkspaceReport `json:"workspace_metrics"`
	TopTools         []insights.ToolCount   `json:"top_tools,omitempty"`
	RecentSessions   []metricsRecentSession `json:"recent_sessions,omitempty"`
	Message          string                 `json:"message,omitempty"`
}

type metricsSessionReport struct {
	ID                       string  `json:"id"`
	MessageCount             int     `json:"message_count"`
	UsageRecords             int     `json:"usage_records"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	TotalTokens              int     `json:"total_tokens"`
	EstimatedUSD             float64 `json:"estimated_usd"`
	TokenSource              string  `json:"token_source"`
	ToolUses                 int     `json:"tool_uses"`
	ToolResults              int     `json:"tool_results"`
	ToolErrors               int     `json:"tool_errors"`
	CacheHitRatio            float64 `json:"cache_hit_ratio"`
}

type metricsWorkspaceReport struct {
	SessionCount             int     `json:"session_count"`
	MessageCount             int     `json:"message_count"`
	UserMessages             int     `json:"user_messages"`
	AssistantMessages        int     `json:"assistant_messages"`
	PromptCount              int     `json:"prompt_count"`
	ToolUses                 int     `json:"tool_uses"`
	UsageRecords             int     `json:"usage_records"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	TotalTokens              int     `json:"total_tokens"`
	EstimatedUSD             float64 `json:"estimated_usd"`
	TokenSource              string  `json:"token_source,omitempty"`
	AverageTokensPerSession  float64 `json:"average_tokens_per_session"`
	AverageTokensPerPrompt   float64 `json:"average_tokens_per_prompt"`
}

type metricsRecentSession struct {
	ID           string `json:"id"`
	Messages     int    `json:"messages"`
	Prompts      int    `json:"prompts"`
	ToolUses     int    `json:"tool_uses"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

func (a *App) Metrics(args []string, overrides config.FlagOverrides) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseMetricsArgs(args, overrides)
	if err != nil {
		return err
	}
	report, err := a.buildMetricsReport(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderMetricsReport(a.Out, report)
	return nil
}

func parseMetricsArgs(args []string, overrides config.FlagOverrides) (metricsRequest, error) {
	req := metricsRequest{Format: "text", Limit: 5, Overrides: overrides}
	const usage = "codog metrics [--session ID|--resume ID] [--limit N] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "metrics", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "metrics", Flag: arg, Usage: usage}
			}
			req.Overrides.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.Overrides.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "metrics", Flag: arg, Usage: usage}
			}
			req.Overrides.Resume = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.Overrides.Resume = strings.TrimPrefix(arg, "--resume=")
		case arg == "--limit":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "metrics", Flag: arg, Usage: usage}
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
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "metrics", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "metrics", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("metrics", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) buildMetricsReport(req metricsRequest) (metricsReport, error) {
	insight, err := insights.Build(a.Sessions, insights.Options{Limit: req.Limit})
	if err != nil {
		return metricsReport{}, err
	}
	allUsages, usageRecords, err := a.workspaceUsageValues()
	if err != nil {
		return metricsReport{}, err
	}
	workspace := metricsWorkspaceFromInsights(insight, allUsages, usageRecords, a.Config.Model)
	report := metricsReport{
		Kind:             "metrics",
		Action:           "show",
		Status:           "ok",
		Workspace:        a.Workspace,
		Model:            a.Config.Model,
		WorkspaceMetrics: workspace,
		TopTools:         append([]insights.ToolCount(nil), insight.TopTools...),
		RecentSessions:   metricsRecentSessions(insight.RecentSessions),
	}
	sess, err := a.metricsSession(req.Overrides)
	if err != nil {
		return metricsReport{}, err
	}
	if sess == nil {
		report.Message = "No saved sessions were found. Run a prompt or REPL turn to collect session metrics."
		return report, nil
	}
	sessionReport, err := a.metricsForSession(sess)
	if err != nil {
		return metricsReport{}, err
	}
	report.Session = &sessionReport
	return report, nil
}

func (a *App) workspaceUsageValues() ([]anthropic.Usage, int, error) {
	sessions, err := a.Sessions.List()
	if err != nil {
		return nil, 0, err
	}
	usages := []anthropic.Usage{}
	records := 0
	for _, sess := range sessions {
		entries, err := a.Sessions.Usage(sess.ID)
		if err != nil {
			return nil, 0, err
		}
		records += len(entries)
		for _, entry := range entries {
			usages = append(usages, entry.Usage)
		}
	}
	return usages, records, nil
}

func metricsWorkspaceFromInsights(report insights.Report, actual []anthropic.Usage, usageRecords int, model string) metricsWorkspaceReport {
	out := metricsWorkspaceReport{
		SessionCount:      report.Sessions,
		MessageCount:      report.Messages,
		UserMessages:      report.UserMessages,
		AssistantMessages: report.AssistantMessages,
		PromptCount:       report.Prompts,
		ToolUses:          report.ToolUses,
		UsageRecords:      usageRecords,
		InputTokens:       report.Usage.Input,
		OutputTokens:      report.Usage.Output,
	}
	if summary, ok := usage.ActualSummary(actual, model); ok {
		out.InputTokens = summary.InputTokens
		out.OutputTokens = summary.OutputTokens
		out.CacheCreationInputTokens = summary.CacheCreationInputTokens
		out.CacheReadInputTokens = summary.CacheReadInputTokens
		out.TotalTokens = summary.TotalTokens
		out.EstimatedUSD = summary.EstimatedUSD
		out.TokenSource = summary.Source
	} else {
		out.CacheCreationInputTokens = report.Usage.CacheCreation
		out.CacheReadInputTokens = report.Usage.CacheRead
		out.TotalTokens = report.Usage.Input + report.Usage.Output + report.Usage.CacheCreation + report.Usage.CacheRead
		if out.TotalTokens > 0 {
			out.TokenSource = "actual"
		}
	}
	if report.Sessions > 0 {
		out.AverageTokensPerSession = roundedRatio(out.TotalTokens, report.Sessions)
	}
	if report.Prompts > 0 {
		out.AverageTokensPerPrompt = roundedRatio(out.TotalTokens, report.Prompts)
	}
	return out
}

func metricsRecentSessions(sessions []insights.SessionSummary) []metricsRecentSession {
	out := make([]metricsRecentSession, 0, len(sessions))
	for _, sess := range sessions {
		total := sess.Usage.Input + sess.Usage.Output + sess.Usage.CacheCreation + sess.Usage.CacheRead
		out = append(out, metricsRecentSession{
			ID:           sess.ID,
			Messages:     sess.Messages,
			Prompts:      sess.Prompts,
			ToolUses:     sess.ToolUses,
			InputTokens:  sess.Usage.Input,
			OutputTokens: sess.Usage.Output,
			TotalTokens:  total,
		})
	}
	return out
}

func (a *App) metricsSession(overrides config.FlagOverrides) (*session.Session, error) {
	if strings.TrimSpace(overrides.SessionID) != "" || strings.TrimSpace(overrides.Resume) != "" {
		return a.openSession(overrides)
	}
	latest, err := a.Sessions.LatestID()
	if err != nil {
		if errors.Is(err, session.ErrNoSessions) {
			return nil, nil
		}
		return nil, err
	}
	return a.openSession(config.FlagOverrides{SessionID: latest})
}

func (a *App) metricsForSession(sess *session.Session) (metricsSessionReport, error) {
	actual, _ := a.sessionUsageValues(sess.ID)
	usageReport := usage.BuildReportWithUsage(sess.ID, a.Config.Model, sess.Messages, actual)
	entries, err := a.Sessions.Usage(sess.ID)
	if err != nil {
		return metricsSessionReport{}, err
	}
	cacheTotal := usageReport.Summary.CacheCreationInputTokens + usageReport.Summary.CacheReadInputTokens
	cacheDenominator := usageReport.Summary.InputTokens + cacheTotal
	cacheHitRatio := 0.0
	if cacheDenominator > 0 {
		cacheHitRatio = roundedRatio(usageReport.Summary.CacheReadInputTokens, cacheDenominator)
	}
	return metricsSessionReport{
		ID:                       sess.ID,
		MessageCount:             len(sess.Messages),
		UsageRecords:             len(entries),
		InputTokens:              usageReport.Summary.InputTokens,
		OutputTokens:             usageReport.Summary.OutputTokens,
		CacheCreationInputTokens: usageReport.Summary.CacheCreationInputTokens,
		CacheReadInputTokens:     usageReport.Summary.CacheReadInputTokens,
		TotalTokens:              usageReport.Summary.TotalTokens,
		EstimatedUSD:             usageReport.Summary.EstimatedUSD,
		TokenSource:              usageReport.Summary.Source,
		ToolUses:                 usageReport.ToolUse.ToolUses,
		ToolResults:              usageReport.ToolUse.ToolResults,
		ToolErrors:               usageReport.ToolUse.Errors,
		CacheHitRatio:            cacheHitRatio,
	}, nil
}

func roundedRatio(numerator int, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return math.Round((float64(numerator)/float64(denominator))*10000) / 10000
}

func renderMetricsReport(out io.Writer, report metricsReport) {
	fmt.Fprintln(out, "Metrics")
	fmt.Fprintf(out, "  Workspace        %s\n", emptyAsNone(report.Workspace))
	fmt.Fprintf(out, "  Model            %s\n", emptyAsNone(report.Model))
	fmt.Fprintf(out, "  Sessions         %d\n", report.WorkspaceMetrics.SessionCount)
	fmt.Fprintf(out, "  Messages         %d user=%d assistant=%d\n", report.WorkspaceMetrics.MessageCount, report.WorkspaceMetrics.UserMessages, report.WorkspaceMetrics.AssistantMessages)
	fmt.Fprintf(out, "  Prompts          %d\n", report.WorkspaceMetrics.PromptCount)
	fmt.Fprintf(out, "  Tool uses        %d\n", report.WorkspaceMetrics.ToolUses)
	fmt.Fprintf(out, "  Usage records    %d\n", report.WorkspaceMetrics.UsageRecords)
	fmt.Fprintf(out, "  Tokens           input=%d output=%d cache_create=%d cache_read=%d total=%d\n",
		report.WorkspaceMetrics.InputTokens,
		report.WorkspaceMetrics.OutputTokens,
		report.WorkspaceMetrics.CacheCreationInputTokens,
		report.WorkspaceMetrics.CacheReadInputTokens,
		report.WorkspaceMetrics.TotalTokens,
	)
	fmt.Fprintf(out, "  Estimated USD    %.5f\n", report.WorkspaceMetrics.EstimatedUSD)
	if report.WorkspaceMetrics.AverageTokensPerSession != 0 || report.WorkspaceMetrics.AverageTokensPerPrompt != 0 {
		fmt.Fprintf(out, "  Averages         tokens/session=%.2f tokens/prompt=%.2f\n", report.WorkspaceMetrics.AverageTokensPerSession, report.WorkspaceMetrics.AverageTokensPerPrompt)
	}
	if report.Session != nil {
		fmt.Fprintln(out, "Current session")
		fmt.Fprintf(out, "  ID               %s\n", report.Session.ID)
		fmt.Fprintf(out, "  Messages         %d\n", report.Session.MessageCount)
		fmt.Fprintf(out, "  Usage records    %d\n", report.Session.UsageRecords)
		fmt.Fprintf(out, "  Tokens           input=%d output=%d cache_create=%d cache_read=%d total=%d\n",
			report.Session.InputTokens,
			report.Session.OutputTokens,
			report.Session.CacheCreationInputTokens,
			report.Session.CacheReadInputTokens,
			report.Session.TotalTokens,
		)
		fmt.Fprintf(out, "  Source           %s\n", emptyAsNone(report.Session.TokenSource))
		fmt.Fprintf(out, "  Cache hit ratio  %.2f%%\n", report.Session.CacheHitRatio*100)
		fmt.Fprintf(out, "  Tool use         calls=%d results=%d errors=%d\n", report.Session.ToolUses, report.Session.ToolResults, report.Session.ToolErrors)
	}
	if len(report.TopTools) != 0 {
		fmt.Fprintln(out, "Top tools")
		for _, tool := range report.TopTools {
			fmt.Fprintf(out, "  %s             %d\n", tool.Name, tool.Count)
		}
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type insightsRequest struct {
	Format string
	Limit  int
}

type perfIssueRequest struct {
	Format         string
	Limit          int
	Output         string
	Write          bool
	TokenThreshold int
	ToolThreshold  int
}

func (a *App) Insights(args []string) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseInsightsArgs(args)
	if err != nil {
		return err
	}
	report, err := insights.Build(a.Sessions, insights.Options{Limit: req.Limit})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return insights.RenderJSON(a.Out, report)
	}
	insights.RenderText(a.Out, report)
	return nil
}

func parseInsightsArgs(args []string) (insightsRequest, error) {
	req := insightsRequest{Format: "text", Limit: 5}
	const usage = "codog insights [--limit N] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "insights", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "insights", Flag: arg, Usage: usage}
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
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "insights", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "insights", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("insights", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) PerfIssue(args []string) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parsePerfIssueArgs(args)
	if err != nil {
		return err
	}
	summary, err := insights.Build(a.Sessions, insights.Options{Limit: req.Limit})
	if err != nil {
		return err
	}
	report := perfissue.Build(summary, perfissue.Options{
		Workspace:      a.Workspace,
		Limit:          req.Limit,
		TokenThreshold: req.TokenThreshold,
		ToolThreshold:  req.ToolThreshold,
	})
	if req.Write || strings.TrimSpace(req.Output) != "" {
		path := a.perfIssueOutputPath(req.Output, time.Now().UTC())
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := session.ValidateExportOutputPath(path); err != nil {
			return err
		}
		data := []byte(perfissue.RenderMarkdown(report))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		report.File = path
		report.Bytes = len(data)
	}
	if req.Format == "json" {
		return perfissue.RenderJSON(a.Out, report)
	}
	perfissue.RenderText(a.Out, report)
	return nil
}

func parsePerfIssueArgs(args []string) (perfIssueRequest, error) {
	req := perfIssueRequest{Format: "text", Limit: 5}
	const usage = "codog perf-issue [--limit N] [--token-threshold N] [--tool-threshold N] [--output PATH] [--write] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "perf-issue", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "perf-issue", Flag: arg, Usage: usage}
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
		case arg == "--token-threshold":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "perf-issue", Flag: arg, Usage: usage}
			}
			value, err := parsePositiveIntOption(args[index], "--token-threshold", usage)
			if err != nil {
				return req, err
			}
			req.TokenThreshold = value
		case strings.HasPrefix(arg, "--token-threshold="):
			value, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--token-threshold="), "--token-threshold", usage)
			if err != nil {
				return req, err
			}
			req.TokenThreshold = value
		case arg == "--tool-threshold":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "perf-issue", Flag: arg, Usage: usage}
			}
			value, err := parsePositiveIntOption(args[index], "--tool-threshold", usage)
			if err != nil {
				return req, err
			}
			req.ToolThreshold = value
		case strings.HasPrefix(arg, "--tool-threshold="):
			value, err := parsePositiveIntOption(strings.TrimPrefix(arg, "--tool-threshold="), "--tool-threshold", usage)
			if err != nil {
				return req, err
			}
			req.ToolThreshold = value
		case arg == "--output":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "perf-issue", Flag: arg, Usage: usage}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--write":
			req.Write = true
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "perf-issue", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "perf-issue", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("perf-issue", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func (a *App) perfIssueOutputPath(output string, createdAt time.Time) string {
	filename := fmt.Sprintf("perf-issue-%s-%d.md", createdAt.Format("20060102T150405Z"), createdAt.UnixNano())
	if strings.TrimSpace(output) == "" {
		return filepath.Join(a.Workspace, ".codog", "perf", filename)
	}
	path := a.resolveOutputPath(output)
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path
	}
	return filepath.Join(path, filename)
}

type thinkBackRequest struct {
	Format string
	Year   int
	Limit  int
	Output string
}

func (a *App) ThinkBack(args []string) error {
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	req, err := parseThinkBackArgs(args)
	if err != nil {
		return err
	}
	report, err := thinkback.Write(a.Sessions, thinkback.Options{
		Workspace: a.Workspace,
		Year:      req.Year,
		Limit:     req.Limit,
		Output:    req.Output,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return thinkback.RenderJSON(a.Out, report)
	}
	thinkback.RenderText(a.Out, report)
	return nil
}

func parseThinkBackArgs(args []string) (thinkBackRequest, error) {
	req := thinkBackRequest{Format: "text", Limit: 8}
	const usage = "codog think-back [--year YYYY] [--limit N] [--output PATH] [--output-format text|json]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "think-back", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--year":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "think-back", Flag: arg, Usage: usage}
			}
			year, err := parseThinkBackYear(args[index], usage)
			if err != nil {
				return req, err
			}
			req.Year = year
		case strings.HasPrefix(arg, "--year="):
			year, err := parseThinkBackYear(strings.TrimPrefix(arg, "--year="), usage)
			if err != nil {
				return req, err
			}
			req.Year = year
		case arg == "--limit":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "think-back", Flag: arg, Usage: usage}
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
		case arg == "--output":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "think-back", Flag: arg, Usage: usage}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "think-back", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "think-back", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("think-back", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	return req, nil
}

func parseThinkBackYear(value string, usage string) (int, error) {
	year, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || year < 2000 || year > 9999 {
		return 0, invalidFlagValueError{
			Flag:    "--year",
			Value:   value,
			Message: "think-back year must be a four digit year",
			Usage:   usage,
		}
	}
	return year, nil
}

func (a *App) sessionUsageValues(sessionID string) ([]anthropic.Usage, error) {
	entries, err := a.Sessions.Usage(sessionID)
	if err != nil {
		return nil, err
	}
	values := make([]anthropic.Usage, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.Usage)
	}
	return values, nil
}

func (a *App) sessionActualCostUSD(sessionID string, model string) (float64, bool) {
	actual, err := a.sessionUsageValues(sessionID)
	if err != nil {
		return 0, false
	}
	summary, ok := usage.ActualSummary(actual, model)
	if !ok {
		return 0, false
	}
	return summary.EstimatedUSD, true
}

type compactRequest struct {
	Format  string
	Session string
	Keep    int
}

const compactUsage = "codog compact [--session ID|--resume ID] [--keep N] [--output-format text|json]"

func (a *App) Compact(args []string, overrides config.FlagOverrides) error {
	req, err := parseCompactArgs(args, overrides, a.Config.AutoCompactMessages)
	if err != nil {
		return err
	}
	sess, err := a.Sessions.Open(req.Session)
	if err != nil {
		return err
	}
	compactPayload := runloop.CompactHookPayload("manual", sess.ID, len(sess.Messages), req.Keep)
	if err := a.lifecycleHookRunner().PreCompact(context.Background(), compactPayload); err != nil {
		return err
	}
	compacted := compactMessagesPreservingPins(sess.Messages, req.Keep, sess.Metadata.PinnedMessages)
	var result session.ReplaceResult
	if reflect.DeepEqual(compacted, sess.Messages) {
		result = session.ReplaceResult{
			SessionID:         sess.ID,
			Path:              sess.Path,
			OriginalMessages:  len(sess.Messages),
			RemainingMessages: len(sess.Messages),
			RemovedMessages:   0,
		}
	} else {
		result, err = a.Sessions.ReplaceMessages(sess, compacted)
		if err != nil {
			return err
		}
	}
	if err := a.lifecycleHookRunner().PostCompact(context.Background(), compactPayload); err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, "Session Compacted")
	fmt.Fprintf(a.Out, "  Session          %s\n", result.SessionID)
	fmt.Fprintf(a.Out, "  Original         %d\n", result.OriginalMessages)
	fmt.Fprintf(a.Out, "  Remaining        %d\n", result.RemainingMessages)
	fmt.Fprintf(a.Out, "  Removed          %d\n", result.RemovedMessages)
	return nil
}

func compactMessagesPreservingPins(messages []anthropic.Message, keep int, pinned []int) []anthropic.Message {
	compacted := runloop.CompactMessages(messages, keep)
	if len(compacted) == len(messages) || keep <= 0 {
		return compacted
	}
	keepFrom := len(messages) - keep
	pinnedBeforeRecent := []anthropic.Message{}
	for _, index := range pinned {
		if index >= 0 && index < keepFrom {
			pinnedBeforeRecent = append(pinnedBeforeRecent, messages[index])
		}
	}
	if len(pinnedBeforeRecent) == 0 {
		return compacted
	}
	out := make([]anthropic.Message, 0, len(compacted)+len(pinnedBeforeRecent))
	out = append(out, compacted[0])
	out = append(out, pinnedBeforeRecent...)
	out = append(out, compacted[1:]...)
	return out
}

func parseCompactArgs(args []string, overrides config.FlagOverrides, defaultKeep int) (compactRequest, error) {
	req := compactRequest{Format: "text", Keep: defaultKeep}
	req.Session = overrides.Resume
	if req.Session == "" {
		req.Session = overrides.SessionID
	}
	if req.Session == "" {
		req.Session = "latest"
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			if i+1 >= len(args) {
				return req, missingFlagValueError{
					Command: "compact",
					Flag:    arg,
					Usage:   compactUsage,
				}
			}
			i++
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--keep":
			if i+1 >= len(args) || isOutputFormatFlag(args[i+1]) {
				return req, missingFlagValueError{
					Command: "compact",
					Flag:    arg,
					Usage:   compactUsage,
				}
			}
			i++
			value, err := parseCompactKeep(args[i], arg)
			if err != nil {
				return req, err
			}
			req.Keep = value
		case strings.HasPrefix(arg, "--keep="):
			value, err := parseCompactKeep(strings.TrimPrefix(arg, "--keep="), "--keep")
			if err != nil {
				return req, err
			}
			req.Keep = value
		case arg == "--session":
			if i+1 >= len(args) || isOutputFormatFlag(args[i+1]) {
				return req, missingFlagValueError{
					Command: "compact",
					Flag:    arg,
					Usage:   compactUsage,
				}
			}
			i++
			req.Session = args[i]
		case strings.HasPrefix(arg, "--session="):
			req.Session = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			if i+1 >= len(args) || isOutputFormatFlag(args[i+1]) {
				return req, missingFlagValueError{
					Command: "compact",
					Flag:    arg,
					Usage:   compactUsage,
				}
			}
			i++
			req.Session = args[i]
		case strings.HasPrefix(arg, "--resume="):
			req.Session = strings.TrimPrefix(arg, "--resume=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "compact",
					Option:  arg,
					Usage:   compactUsage,
				}
			}
			return req, unexpectedExtraArgsError{
				Command: "compact",
				Args:    []string{arg},
				Usage:   compactUsage,
			}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("compact", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if req.Keep <= 0 {
		req.Keep = 40
	}
	return req, nil
}
