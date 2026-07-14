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
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/pathscope"
	"github.com/Rememorio/codog/internal/saferscope"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/toolnames"
	"github.com/Rememorio/codog/internal/tui"
)

func renderHooksWatchPaths(out io.Writer, report hooksWatchPathsReport) {
	fmt.Fprintln(out, "Hook Watch Paths")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	for _, sess := range report.Sessions {
		fmt.Fprintf(out, "  %s\n", sess.SessionID)
		for _, path := range sess.Paths {
			fmt.Fprintf(out, "    %s\n", path)
		}
	}
	for _, path := range report.Paths {
		fmt.Fprintf(out, "  Path             %s\n", path)
	}
	for _, change := range report.Changes {
		fmt.Fprintf(out, "  %s %s\n", change.Operation, change.Path)
	}
	if len(report.HookReports) > 0 {
		fmt.Fprintf(out, "  Hook runs        %d\n", len(report.HookReports))
	}
}

func findUp(start string, name string) string {
	cursor := filepath.Clean(start)
	for {
		path := filepath.Join(cursor, name)
		if fileExists(path) {
			return path
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return ""
		}
		cursor = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func emptyAsNone(value string) string {
	return emptyAs(value, "none")
}

func emptyAs(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (a *App) Focus(args []string) error {
	format, paths, err := parseFocusArgs("focus", args)
	if err != nil {
		return err
	}
	var report focus.Report
	if len(paths) == 0 {
		report, err = focus.BuildReport(a.Workspace)
	} else {
		report, err = focus.Add(a.Workspace, paths)
	}
	if err != nil {
		return err
	}
	return a.renderFocusReport(format, report)
}

func (a *App) AddDir(args []string) error {
	req, err := parseAddDirArgs(args)
	if err != nil {
		return err
	}
	var report pathscope.Report
	switch req.Action {
	case "list":
		report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "list")
	case "add":
		if _, err = pathscope.Add(a.Workspace, req.Paths); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "add")
		}
	case "remove":
		if _, err = pathscope.Remove(a.Workspace, req.Paths); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "remove")
		}
	case "clear":
		if report, err = pathscope.Clear(a.Workspace); err == nil {
			err = a.refreshBuiltinToolScope()
		}
		if err == nil {
			report, err = pathscope.BuildReport(a.Workspace, a.Config.AdditionalDirs, "clear")
		}
	default:
		err = fmt.Errorf("unknown add-dir action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	pathscope.RenderText(a.Out, report)
	return nil
}

func (a *App) Validation(args []string) error {
	format, paths, err := parseValidationArgs(args)
	if err != nil {
		return err
	}
	report, err := pathscope.Validate(a.Workspace, a.Config.AdditionalDirs, paths)
	if err != nil {
		return err
	}
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	pathscope.RenderValidationText(a.Out, report)
	return nil
}

func parseValidationArgs(args []string) (string, []string, error) {
	const usage = "codog validation [add-dir] [PATH...] [--json|--output-format text|json]"
	format := "text"
	var paths []string
	actionSeen := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return "", nil, missingFlagValueError{Command: "validation", Flag: arg, Usage: usage}
			}
			format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			format = strings.TrimPrefix(arg, "--output-format=")
		case !actionSeen && (arg == "add-dir" || arg == "adddir" || arg == "paths"):
			actionSeen = true
			continue
		default:
			actionSeen = true
			paths = append(paths, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("validation", format, []string{"text", "json"})
	if err != nil {
		return "", nil, err
	}
	return normalizedFormat, paths, nil
}

type addDirRequest struct {
	Format string
	Action string
	Paths  []string
}

func parseAddDirArgs(args []string) (addDirRequest, error) {
	const usage = "codog add-dir [list|add|remove|clear] [PATH...] [--json|--output-format text|json]"
	req := addDirRequest{Format: "text", Action: "list"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if missingFlagValueAt(args, i) {
				return req, missingFlagValueError{Command: "add-dir", Flag: arg, Usage: usage}
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--clear":
			req.Action = "clear"
		case arg == "--remove":
			req.Action = "remove"
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("add-dir", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) == 0 {
		if req.Action == "remove" {
			return req, requiredArgumentError{Command: "add-dir remove", Argument: "PATH", Usage: usage}
		}
		return req, nil
	}
	switch strings.ToLower(positionals[0]) {
	case "list", "show":
		req.Action = "list"
		req.Paths = nil
	case "add":
		req.Action = "add"
		req.Paths = positionals[1:]
	case "remove", "rm", "delete":
		req.Action = "remove"
		req.Paths = positionals[1:]
	case "clear", "reset":
		req.Action = "clear"
		req.Paths = nil
	default:
		if req.Action == "remove" {
			req.Paths = positionals
		} else {
			req.Action = "add"
			req.Paths = positionals
		}
	}
	if (req.Action == "add" || req.Action == "remove") && len(req.Paths) == 0 {
		return req, requiredArgumentError{Command: "add-dir " + req.Action, Argument: "PATH", Usage: usage}
	}
	return req, nil
}

type workspaceRequest struct {
	Action string
	Path   string
	Format string
}

type workspaceReport struct {
	Kind                    string   `json:"kind"`
	Action                  string   `json:"action"`
	Status                  string   `json:"status"`
	Workspace               string   `json:"workspace"`
	PreviousWorkspace       string   `json:"previous_workspace,omitempty"`
	Changed                 bool     `json:"changed"`
	Exists                  bool     `json:"exists"`
	IsDir                   bool     `json:"is_dir"`
	GitWorktree             bool     `json:"git_worktree"`
	ConfigHome              string   `json:"config_home,omitempty"`
	SessionDir              string   `json:"session_dir,omitempty"`
	AdditionalDirs          []string `json:"additional_dirs,omitempty"`
	EffectiveAdditionalDirs []string `json:"effective_additional_dirs,omitempty"`
	Message                 string   `json:"message,omitempty"`
}

type scopeRequest struct {
	Action string
	Choice string
	Target string
	Format string
}

func (a *App) Scope(args []string) error {
	req, err := parseScopeArgs(args)
	if err != nil {
		return err
	}
	var report saferscope.Report
	switch req.Action {
	case "status":
		report, err = saferscope.Status(a.Workspace)
	case "preview":
		report, err = saferscope.Preview(a.Workspace, saferscope.Options{
			Choice:           req.Choice,
			Target:           req.Target,
			RespectGitignore: a.Config.EffectiveRespectGitignore(),
		})
	case "apply":
		report, err = saferscope.Apply(a.Workspace, saferscope.Options{
			Choice:           req.Choice,
			Target:           req.Target,
			RespectGitignore: a.Config.EffectiveRespectGitignore(),
		})
		if err == nil && strings.TrimSpace(report.ActiveWorkspace) != "" && report.ActiveWorkspace != a.Workspace {
			if err = a.switchRuntimeWorkspace(report.ActiveWorkspace); err != nil {
				return err
			}
		}
	case "restore":
		report, err = saferscope.Restore(a.Workspace)
		if err == nil && strings.TrimSpace(report.ActiveWorkspace) != "" && report.ActiveWorkspace != a.Workspace {
			if err = a.switchRuntimeWorkspace(report.ActiveWorkspace); err != nil {
				return err
			}
		}
	default:
		err = fmt.Errorf("unknown scope action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	saferscope.RenderText(a.Out, report)
	return nil
}

func parseScopeArgs(args []string) (scopeRequest, error) {
	const usage = "codog scope [status|preview|apply|restore] [--choice auto|workspace|ignore|create_ignore_file|both] [--target PATH] [--json|--output-format text|json]"
	req := scopeRequest{Action: "preview", Choice: "auto", Format: "text"}
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "scope", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--choice":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "scope", Flag: arg, Usage: usage}
			}
			req.Choice = args[index]
		case strings.HasPrefix(arg, "--choice="):
			req.Choice = strings.TrimPrefix(arg, "--choice=")
		case arg == "--target":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "scope", Flag: arg, Usage: usage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "scope", Option: arg, Usage: usage}
			}
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("scope", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) > 0 {
		switch strings.ToLower(positionals[0]) {
		case "status", "state", "current":
			req.Action = "status"
		case "preview", "plan", "show":
			req.Action = "preview"
		case "apply", "use":
			req.Action = "apply"
		case "restore", "back", "reset":
			req.Action = "restore"
		default:
			return req, unknownActionError{
				Command:     "codog scope",
				Action:      positionals[0],
				Expected:    append([]string(nil), scopeActionCandidates...),
				Suggestions: toolnames.Suggestions(positionals[0], scopeActionCandidates, 4),
				Usage:       usage,
			}
		}
	}
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{Command: "scope " + req.Action, Args: positionals[1:], Usage: usage}
	}
	choice := strings.ToLower(strings.TrimSpace(req.Choice))
	switch choice {
	case "", "auto", "workspace", "switch_workspace", "ignore", "create_ignore_file", "append_ignore_block", "write_ignore_stub", "both", "all":
	default:
		return req, invalidFlagValueError{Flag: "--choice", Value: req.Choice, Message: "scope choice must be auto, workspace, ignore, create_ignore_file, or both", Usage: usage}
	}
	if choice == "" {
		req.Choice = "auto"
	}
	return req, nil
}

var scopeActionCandidates = []string{"status", "state", "current", "preview", "plan", "show", "apply", "use", "restore", "back", "reset"}

func (a *App) WorkspaceCommand(args []string) error {
	req, err := parseWorkspaceArgs(args)
	if err != nil {
		return err
	}
	previous := a.Workspace
	switch req.Action {
	case "status":
	case "set":
		next, err := a.resolveWorkspacePath(req.Path)
		if err != nil {
			return err
		}
		if err := a.switchRuntimeWorkspace(next); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown workspace action %q", req.Action)
	}
	report := a.workspaceReport(req.Action, previous)
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderWorkspaceReport(a.Out, report)
	return nil
}

func parseWorkspaceArgs(args []string) (workspaceRequest, error) {
	const usage = "codog workspace [status|set PATH] [--json|--output-format text|json]"
	req := workspaceRequest{Action: "status", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "workspace", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "workspace", Option: arg, Usage: usage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("workspace", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show", "pwd":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "workspace status", Args: rest[1:], Usage: usage}
		}
		req.Action = "status"
	case "set", "cd", "switch":
		if len(rest) != 2 {
			if len(rest) < 2 {
				return req, requiredArgumentError{Command: "workspace set", Argument: "PATH", Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "workspace set", Args: rest[2:], Usage: usage}
		}
		req.Action = "set"
		req.Path = rest[1]
	default:
		if len(rest) != 1 {
			return req, unexpectedExtraArgsError{Command: "workspace", Args: rest[1:], Usage: usage}
		}
		req.Action = "set"
		req.Path = rest[0]
	}
	return req, nil
}

func (a *App) resolveWorkspacePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace path is required")
	}
	if !filepath.IsAbs(path) {
		base := a.Workspace
		if strings.TrimSpace(base) == "" {
			base = "."
		}
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory: %s", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func (a *App) workspaceReport(action string, previous string) workspaceReport {
	workspace := strings.TrimSpace(a.Workspace)
	info, err := os.Stat(workspace)
	exists := err == nil
	isDir := exists && info.IsDir()
	sessionDir := ""
	if a.Sessions != nil {
		sessionDir = a.Sessions.Dir
	}
	effectiveDirs, _ := pathscope.EffectiveDirs(workspace, a.Config.AdditionalDirs)
	report := workspaceReport{
		Kind:                    "workspace",
		Action:                  action,
		Status:                  "ok",
		Workspace:               workspace,
		PreviousWorkspace:       previous,
		Changed:                 previous != "" && filepath.Clean(previous) != filepath.Clean(workspace),
		Exists:                  exists,
		IsDir:                   isDir,
		GitWorktree:             workspaceIsGitWorktree(workspace),
		ConfigHome:              a.Config.ConfigHome,
		SessionDir:              sessionDir,
		AdditionalDirs:          append([]string(nil), a.Config.AdditionalDirs...),
		EffectiveAdditionalDirs: effectiveDirs,
	}
	if report.Changed {
		report.Message = "Workspace updated for the current Codog process."
	} else {
		report.PreviousWorkspace = ""
	}
	return report
}

func (a *App) switchRuntimeWorkspace(next string) error {
	next, err := a.resolveWorkspacePath(next)
	if err != nil {
		return err
	}
	a.Workspace = next
	store, err := session.NewWorkspaceStoreWithCleanup(a.Config.ConfigHome, next, a.Config.EffectiveCleanupPeriodDays())
	if err != nil {
		return err
	}
	a.Sessions = store
	return a.refreshBuiltinToolScope()
}

func workspaceIsGitWorktree(workspace string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	out, err := gitops.Run(workspace, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func renderWorkspaceReport(out io.Writer, report workspaceReport) {
	fmt.Fprintln(out, "Workspace")
	fmt.Fprintf(out, "  Path             %s\n", emptyAsNone(report.Workspace))
	if report.PreviousWorkspace != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.PreviousWorkspace)
	}
	fmt.Fprintf(out, "  Exists           %t\n", report.Exists)
	fmt.Fprintf(out, "  Directory        %t\n", report.IsDir)
	fmt.Fprintf(out, "  Git worktree     %t\n", report.GitWorktree)
	if report.SessionDir != "" {
		fmt.Fprintf(out, "  Session dir      %s\n", report.SessionDir)
	}
	if len(report.EffectiveAdditionalDirs) != 0 {
		fmt.Fprintf(out, "  Additional dirs  %s\n", strings.Join(report.EffectiveAdditionalDirs, ", "))
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func (a *App) refreshBuiltinToolScope() error {
	if a.Tools == nil {
		return nil
	}
	additionalDirs, err := pathscope.EffectiveDirs(a.Workspace, a.Config.AdditionalDirs)
	if err != nil {
		return err
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
		return err
	}
	options := toolRegistryOptionsFromConfig(a.Config, additionalDirs, questionIn, questionOut, executable, a.AgentDefinitions)
	options.PluginDirs = append([]string(nil), a.PluginDirs...)
	a.Tools.UpdateBuiltinScope(a.Workspace, options)
	return nil
}

func (a *App) Unfocus(args []string) error {
	format, paths, err := parseFocusArgs("unfocus", args)
	if err != nil {
		return err
	}
	var report focus.Report
	if len(paths) == 0 || containsFold(paths, "--all") || containsFold(paths, "all") {
		report, err = focus.Clear(a.Workspace)
	} else {
		report, err = focus.Remove(a.Workspace, paths)
	}
	if err != nil {
		return err
	}
	return a.renderFocusReport(format, report)
}

func (a *App) renderFocusReport(format string, report focus.Report) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	focus.RenderText(a.Out, report)
	return nil
}

func parseFocusArgs(command string, args []string) (string, []string, error) {
	usage := "codog " + command + " [PATH...] [--json|--output-format text|json]"
	format := "text"
	var paths []string
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
			paths = append(paths, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat(command, format, []string{"text", "json"})
	if err != nil {
		return "", nil, err
	}
	return normalizedFormat, paths, nil
}

type outputStyleRequest struct {
	Action string
	Name   string
	Format string
}

const outputStyleUsage = "codog output-style [list|ls|search|find|audit|doctor|sources|roots|status|show|view|set|use|clear|off] [NAME] [--output-format text|json]"

func (a *App) OutputStyle(args []string) error {
	req, err := parseOutputStyleArgs(args)
	if err != nil {
		return err
	}
	var report outputstyle.Report
	switch req.Action {
	case "list":
		report, err = outputstyle.List(a.Config.ConfigHome, a.Workspace)
	case "search":
		report, err = outputstyle.Search(a.Config.ConfigHome, a.Workspace, req.Name)
	case "audit":
		report, err = outputstyle.Audit(a.Config.ConfigHome, a.Workspace)
	case "sources":
		sources := outputstyle.Sources(a.Config.ConfigHome, a.Workspace)
		report = outputstyle.Report{
			Kind:        "output_style",
			Action:      "sources",
			Status:      "ok",
			Sources:     sources,
			SourceCount: len(sources),
		}
	case "show":
		report, err = outputstyle.Show(a.Config.ConfigHome, a.Workspace, req.Name)
	case "set":
		report, err = outputstyle.Set(a.Config.ConfigHome, a.Workspace, req.Name)
	case "clear":
		report, err = outputstyle.Clear(a.Workspace)
	default:
		err = fmt.Errorf("unknown output-style command %q", req.Action)
	}
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	outputstyle.RenderText(a.Out, report)
	return nil
}

func parseOutputStyleArgs(args []string) (outputStyleRequest, error) {
	req := outputStyleRequest{Action: "list", Format: "text"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return outputStyleRequest{}, missingFlagValueError{
					Command: "output-style",
					Flag:    arg,
					Usage:   outputStyleUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return outputStyleRequest{}, unknownOptionError{
				Command: "output-style",
				Option:  arg,
				Usage:   outputStyleUsage,
			}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("output-style", req.Format, []string{"text", "json"})
	if err != nil {
		return outputStyleRequest{}, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	rawAction := strings.ToLower(strings.TrimSpace(rest[0]))
	action := normalizeOutputStyleAction(rawAction)
	switch action {
	case "list", "audit", "sources":
		if len(rest) > 1 {
			return outputStyleRequest{}, unexpectedExtraArgsError{
				Command: "output-style " + action,
				Args:    rest[1:],
				Usage:   outputStyleUsage,
			}
		}
		req.Action = action
	case "search":
		if len(rest) < 2 {
			return outputStyleRequest{}, requiredArgumentError{
				Command:  "output-style search",
				Argument: "QUERY",
				Usage:    outputStyleUsage,
			}
		}
		req.Action = "search"
		req.Name = strings.TrimSpace(strings.Join(rest[1:], " "))
	case "show", "set":
		if len(rest) < 2 {
			return outputStyleRequest{}, requiredArgumentError{
				Command:  "output-style " + action,
				Argument: "NAME",
				Usage:    outputStyleUsage,
			}
		}
		if len(rest) > 2 {
			return outputStyleRequest{}, unexpectedExtraArgsError{
				Command: "output-style " + action,
				Args:    rest[2:],
				Usage:   outputStyleUsage,
			}
		}
		req.Action = action
		req.Name = rest[1]
	case "clear":
		if len(rest) > 1 {
			return outputStyleRequest{}, unexpectedExtraArgsError{
				Command: "output-style " + action,
				Args:    rest[1:],
				Usage:   outputStyleUsage,
			}
		}
		req.Action = "clear"
	default:
		if len(rest) > 1 {
			return outputStyleRequest{}, unexpectedExtraArgsError{
				Command: "output-style",
				Args:    rest[1:],
				Usage:   outputStyleUsage,
			}
		}
		req.Action = "set"
		req.Name = rest[0]
	}
	return req, nil
}

func normalizeOutputStyleAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list", "ls", "status", "current":
		return "list"
	case "search", "find", "query", "lookup":
		return "search"
	case "audit", "doctor", "check", "validate":
		return "audit"
	case "source", "sources", "root", "roots":
		return "sources"
	case "show", "info", "describe", "get", "view", "cat":
		return "show"
	case "set", "use", "select", "enable", "on":
		return "set"
	case "clear", "reset", "unset", "disable", "off":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

var availableThemes = append([]string{"default"}, tui.ThemeNames()...)

type themeRequest struct {
	Action string
	Name   string
	Format string
	Target string
	Path   string
}

type themeReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Theme     string   `json:"theme"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

const themeUsage = "codog theme [status|show|list|ls|set|use|clear|off] [NAME] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Theme(args []string) error {
	req, err := parseThemeArgs(args)
	if err != nil {
		return err
	}
	report := themeReport{
		Kind:      "theme",
		Action:    req.Action,
		Status:    "ok",
		Theme:     effectiveTheme(a.Config.Theme),
		Available: append([]string(nil), availableThemes...),
	}
	switch req.Action {
	case "status", "list":
	case "set":
		if err := validateThemeName(req.Name); err != nil {
			return err
		}
		req.Name, _ = tui.NormalizeThemeName(req.Name)
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveTheme(a.Config.Theme)
		if _, err := config.SetFileValue(path, "theme", req.Name); err != nil {
			return err
		}
		a.Config.Theme = req.Name
		report.Action = "set"
		report.Theme = effectiveTheme(a.Config.Theme)
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveTheme(a.Config.Theme)
		if _, err := config.UnsetFileValue(path, "theme"); err != nil {
			return err
		}
		a.Config.Theme = ""
		report.Action = "clear"
		report.Theme = effectiveTheme(a.Config.Theme)
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown theme command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderThemeReport(a.Out, report)
	return nil
}

func parseThemeArgs(args []string) (themeRequest, error) {
	req := themeRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "theme",
					Flag:    arg,
					Usage:   themeUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "theme",
					Flag:    arg,
					Usage:   themeUsage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "theme",
					Flag:    arg,
					Usage:   themeUsage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "theme",
					Option:  arg,
					Usage:   themeUsage,
				}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("theme", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	rawAction := strings.ToLower(strings.TrimSpace(rest[0]))
	action := normalizeThemeAction(rawAction)
	switch action {
	case "status":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "theme " + rawAction,
				Args:    rest[1:],
				Usage:   themeUsage,
			}
		}
		req.Action = "status"
	case "list":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "theme " + rawAction,
				Args:    rest[1:],
				Usage:   themeUsage,
			}
		}
		req.Action = "list"
	case "set":
		if len(rest) < 2 {
			return req, requiredArgumentError{
				Command:  "theme " + action,
				Argument: "NAME",
				Usage:    themeUsage,
			}
		}
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{
				Command: "theme " + action,
				Args:    rest[2:],
				Usage:   themeUsage,
			}
		}
		req.Action = "set"
		req.Name = rest[1]
	case "clear":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "theme " + rawAction,
				Args:    rest[1:],
				Usage:   themeUsage,
			}
		}
		req.Action = "clear"
	default:
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "theme",
				Args:    rest[1:],
				Usage:   themeUsage,
			}
		}
		req.Action = "set"
		req.Name = rest[0]
	}
	return req, nil
}

func normalizeThemeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "status", "show", "info", "current", "get", "view":
		return "status"
	case "list", "ls", "available":
		return "list"
	case "set", "use", "select", "enable", "on":
		return "set"
	case "clear", "reset", "unset", "disable", "off", "default":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func renderThemeReport(out io.Writer, report themeReport) {
	fmt.Fprintln(out, "Theme")
	fmt.Fprintf(out, "  Active           %s\n", report.Theme)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.Available, ", "))
}

func effectiveTheme(theme string) string {
	theme = strings.TrimSpace(theme)
	if theme == "" {
		return "default"
	}
	return theme
}

func effectiveTUITheme(theme string) string {
	if normalized, ok := tui.NormalizeThemeName(theme); ok {
		return normalized
	}
	return "auto"
}

func validateThemeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return requiredArgumentError{
			Command:  "theme set",
			Argument: "NAME",
			Usage:    themeUsage,
		}
	}
	if _, ok := tui.NormalizeThemeName(name); !ok {
		return invalidFlagValueError{
			Flag:    "theme",
			Value:   name,
			Message: "theme must be one of: " + strings.Join(availableThemes, ", "),
			Usage:   themeUsage,
		}
	}
	return nil
}

type languageRequest struct {
	Action   string
	Language string
	Format   string
	Target   string
	Path     string
}

type languageReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	Language   string `json:"language"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

const languageUsage = "codog language [status|show|set|use|clear|off] [LANGUAGE] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Language(args []string) error {
	req, err := parseLanguageArgs(args)
	if err != nil {
		return err
	}
	current := normalizeConfiguredLanguage(a.Config.Language)
	report := languageReport{
		Kind:       "language",
		Action:     req.Action,
		Status:     "ok",
		Configured: current != "",
		Language:   current,
	}
	switch req.Action {
	case "status":
	case "set":
		language, err := normalizeLanguageName(req.Language)
		if err != nil {
			return err
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := current
		if _, err := config.SetFileValue(path, "language", language); err != nil {
			return err
		}
		a.Config.Language = language
		report.Configured = true
		report.Language = language
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := current
		if _, err := config.UnsetFileValue(path, "language"); err != nil {
			return err
		}
		a.Config.Language = ""
		report.Configured = false
		report.Language = ""
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown language command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderLanguageReport(a.Out, report)
	return nil
}

func parseLanguageArgs(args []string) (languageRequest, error) {
	req := languageRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "language",
					Flag:    arg,
					Usage:   languageUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "language",
					Flag:    arg,
					Usage:   languageUsage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "language",
					Flag:    arg,
					Usage:   languageUsage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "language",
					Option:  arg,
					Usage:   languageUsage,
				}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("language", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	rawAction := strings.ToLower(strings.TrimSpace(rest[0]))
	action := normalizeLanguageAction(rawAction)
	switch action {
	case "status":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "language " + rawAction,
				Args:    rest[1:],
				Usage:   languageUsage,
			}
		}
		req.Action = "status"
	case "set":
		if len(rest) < 2 {
			return req, requiredArgumentError{
				Command:  "language " + action,
				Argument: "LANGUAGE",
				Usage:    languageUsage,
			}
		}
		req.Action = "set"
		req.Language = strings.Join(rest[1:], " ")
	case "clear":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: "language " + rawAction,
				Args:    rest[1:],
				Usage:   languageUsage,
			}
		}
		req.Action = "clear"
	default:
		req.Action = "set"
		req.Language = strings.Join(rest, " ")
	}
	return req, nil
}

func normalizeLanguageAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "status", "show", "info", "current", "get", "view":
		return "status"
	case "set", "use", "select", "enable", "on":
		return "set"
	case "clear", "reset", "unset", "disable", "off", "default":
		return "clear"
	default:
		return strings.ToLower(strings.TrimSpace(action))
	}
}

func renderLanguageReport(out io.Writer, report languageReport) {
	fmt.Fprintln(out, "Language")
	if report.Configured {
		fmt.Fprintf(out, "  Active           %s\n", report.Language)
	} else {
		fmt.Fprintln(out, "  Active           default")
	}
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func normalizeConfiguredLanguage(language string) string {
	language, err := normalizeLanguageName(language)
	if err != nil {
		return ""
	}
	return language
}

func normalizeLanguageName(language string) (string, error) {
	language = strings.TrimSpace(language)
	if language == "" {
		return "", requiredArgumentError{
			Command:  "language set",
			Argument: "LANGUAGE",
			Usage:    languageUsage,
		}
	}
	if strings.ContainsAny(language, "\r\n") {
		return "", invalidFlagValueError{
			Flag:    "language",
			Value:   language,
			Message: "language must be a single line",
			Usage:   languageUsage,
		}
	}
	if len(language) > 80 {
		return "", invalidFlagValueError{
			Flag:    "language",
			Value:   language,
			Message: "language must be 80 characters or fewer",
			Usage:   languageUsage,
		}
	}
	return language, nil
}

var availableEfforts = []string{"auto", "low", "medium", "high", "disabled"}

const (
	effortUsage    = "codog effort [status|list|set|clear] [auto|low|medium|high|disabled] [--target user|project|local] [--path PATH] [--output-format text|json]"
	reasoningUsage = "codog reasoning [status|list|set|clear] [auto|low|medium|high|disabled] [--target user|project|local] [--path PATH] [--output-format text|json]"
)

type effortRequest struct {
	Action string
	Level  string
	Format string
	Target string
	Path   string
}

type effortReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Effort    string   `json:"effort"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

func (a *App) Effort(args []string) error {
	return a.reasoningEffort(args, "effort", "Effort")
}

func (a *App) Reasoning(args []string) error {
	return a.reasoningEffort(args, "reasoning", "Reasoning")
}

func (a *App) reasoningEffort(args []string, kind string, title string) error {
	req, err := parseEffortArgs(args, kind)
	if err != nil {
		return err
	}
	report := effortReport{
		Kind:      kind,
		Action:    req.Action,
		Status:    "ok",
		Effort:    effectiveEffort(a.Config.ReasoningEffort),
		Available: append([]string(nil), availableEfforts...),
	}
	switch req.Action {
	case "status", "list":
	case "set":
		if err := validateEffort(req.Level, kind); err != nil {
			return err
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveEffort(a.Config.ReasoningEffort)
		if _, err := config.SetFileValue(path, "reasoning_effort", req.Level); err != nil {
			return err
		}
		a.Config.ReasoningEffort = req.Level
		report.Action = "set"
		report.Effort = effectiveEffort(a.Config.ReasoningEffort)
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		previous := effectiveEffort(a.Config.ReasoningEffort)
		if _, err := config.UnsetFileValue(path, "reasoning_effort"); err != nil {
			return err
		}
		a.Config.ReasoningEffort = ""
		report.Action = "clear"
		report.Effort = effectiveEffort(a.Config.ReasoningEffort)
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown effort command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderEffortReport(a.Out, title, report)
	return nil
}

func parseEffortArgs(args []string, command string) (effortRequest, error) {
	req := effortRequest{Action: "status", Format: "text", Target: "user"}
	usage := effortCommandUsage(command)
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: command,
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: command,
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: command,
					Flag:    arg,
					Usage:   usage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: command,
					Option:  arg,
					Usage:   usage,
				}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat(command, req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: command + " " + strings.ToLower(rest[0]),
				Args:    rest[1:],
				Usage:   usage,
			}
		}
		req.Action = "status"
	case "list":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: command + " list",
				Args:    rest[1:],
				Usage:   usage,
			}
		}
		req.Action = "list"
	case "set":
		if len(rest) < 2 {
			return req, requiredArgumentError{
				Command:  command + " set",
				Argument: "LEVEL",
				Usage:    usage,
			}
		}
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{
				Command: command + " set",
				Args:    rest[2:],
				Usage:   usage,
			}
		}
		req.Action = "set"
		req.Level = strings.ToLower(rest[1])
	case "clear", "reset":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: command + " " + strings.ToLower(rest[0]),
				Args:    rest[1:],
				Usage:   usage,
			}
		}
		req.Action = "clear"
	default:
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{
				Command: command,
				Args:    rest[1:],
				Usage:   usage,
			}
		}
		req.Action = "set"
		req.Level = strings.ToLower(rest[0])
	}
	return req, nil
}

func effortCommandUsage(command string) string {
	if command == "reasoning" {
		return reasoningUsage
	}
	return effortUsage
}

func renderEffortReport(out io.Writer, title string, report effortReport) {
	fmt.Fprintln(out, title)
	fmt.Fprintf(out, "  Active           %s\n", report.Effort)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	fmt.Fprintf(out, "  Available        %s\n", strings.Join(report.Available, ", "))
}

func effectiveEffort(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return "auto"
	}
	return level
}

func validateEffort(level string, command string) error {
	level = effectiveEffort(level)
	for _, allowed := range availableEfforts {
		if level == allowed {
			return nil
		}
	}
	return invalidFlagValueError{
		Flag:    command,
		Value:   level,
		Message: command + " level must be one of auto, low, medium, high, or disabled",
		Usage:   effortCommandUsage(command),
	}
}

type fastRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type fastReport struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Enabled  bool   `json:"enabled"`
	Previous bool   `json:"previous,omitempty"`
	Path     string `json:"path,omitempty"`
}

const fastUsage = "codog fast [status|on|off|toggle|clear] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Fast(args []string) error {
	req, err := parseFastArgs(args)
	if err != nil {
		return err
	}
	previous := fastModeEnabled(a.Config.FastMode)
	report := fastReport{
		Kind:    "fast",
		Action:  req.Action,
		Status:  "ok",
		Enabled: previous,
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "fast_mode", next); err != nil {
			return err
		}
		a.Config.FastMode = &next
		report.Action = "set"
		report.Enabled = next
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "fast_mode"); err != nil {
			return err
		}
		a.Config.FastMode = nil
		report.Action = "clear"
		report.Enabled = false
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown fast command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderFastReport(a.Out, report)
	return nil
}

func parseFastArgs(args []string) (fastRequest, error) {
	req := fastRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "fast", Flag: arg, Usage: fastUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "fast", Flag: arg, Usage: fastUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "fast", Flag: arg, Usage: fastUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "fast", Option: arg, Usage: fastUsage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("fast", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "fast " + strings.ToLower(rest[0]), Args: rest[1:], Usage: fastUsage}
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, unexpectedExtraArgsError{Command: "fast", Args: []string{rest[0]}, Usage: fastUsage}
	}
	return req, nil
}

func renderFastReport(out io.Writer, report fastReport) {
	fmt.Fprintln(out, "Fast Mode")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func fastModeEnabled(value *bool) bool {
	return value != nil && *value
}

type voiceRequest struct {
	Action    string
	Format    string
	Target    string
	Path      string
	Command   string
	Input     string
	TimeoutMS int
}

type voiceReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	Enabled           bool   `json:"enabled"`
	CommandConfigured bool   `json:"command_configured"`
	CommandAvailable  bool   `json:"command_available"`
	Command           string `json:"command,omitempty"`
	Path              string `json:"path,omitempty"`
	Transcript        string `json:"transcript,omitempty"`
	Stdout            string `json:"stdout,omitempty"`
	Stderr            string `json:"stderr,omitempty"`
	StdoutTruncated   bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated   bool   `json:"stderr_truncated,omitempty"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	DurationMS        int64  `json:"duration_ms,omitempty"`
	TimedOut          bool   `json:"timed_out,omitempty"`
	Message           string `json:"message,omitempty"`
}

const voiceUsage = "codog voice [status|on|off|toggle|set-command|clear-command|clear|test|listen] [--command COMMAND] [--input TEXT] [--timeout-ms N] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Voice(args []string) error {
	req, err := parseVoiceArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !boolPtrEnabled(a.Config.VoiceEnabled)
		}
		if next && !externalCommandAvailable(a.Config.VoiceCommand) {
			return requiredArgumentError{Command: "voice on", Argument: "COMMAND", Usage: voiceUsage}
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "voice_enabled", next); err != nil {
			return err
		}
		a.Config.VoiceEnabled = &next
		req.Path = path
	case "set-command":
		command := strings.TrimSpace(req.Command)
		if command == "" {
			return requiredArgumentError{Command: "voice set-command", Argument: "COMMAND", Usage: voiceUsage}
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "voice_command", command); err != nil {
			return err
		}
		a.Config.VoiceCommand = command
		req.Path = path
	case "clear-command":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "voice_command"); err != nil {
			return err
		}
		disabled := false
		if _, err := config.SetFileValue(path, "voice_enabled", disabled); err != nil {
			return err
		}
		a.Config.VoiceCommand = ""
		a.Config.VoiceEnabled = &disabled
		req.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		for _, key := range []string{"voice_enabled", "voice_command"} {
			if _, err := config.UnsetFileValue(path, key); err != nil {
				return err
			}
		}
		a.Config.VoiceEnabled = nil
		a.Config.VoiceCommand = ""
		req.Path = path
	case "test", "transcribe", "listen":
		report, runErr := a.runVoiceCommand(req)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			if runErr != nil {
				return &ExitError{Code: 1, Err: runErr, Silent: true}
			}
		} else {
			renderVoiceReport(a.Out, report)
		}
		return runErr
	default:
		return fmt.Errorf("unknown voice command %q", req.Action)
	}
	report := a.voiceStatusReport(req.Action, req.Path)
	if !report.CommandConfigured {
		report.Message = "Voice mode needs an external STT command before it can be enabled."
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderVoiceReport(a.Out, report)
	return nil
}

func parseVoiceArgs(args []string) (voiceRequest, error) {
	req := voiceRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--command":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			req.Command = args[index]
		case strings.HasPrefix(arg, "--command="):
			req.Command = strings.TrimPrefix(arg, "--command=")
		case arg == "--input" || arg == "--stdin":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			req.Input = args[index]
		case strings.HasPrefix(arg, "--input="):
			req.Input = strings.TrimPrefix(arg, "--input=")
		case strings.HasPrefix(arg, "--stdin="):
			req.Input = strings.TrimPrefix(arg, "--stdin=")
		case arg == "--timeout-ms":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "voice", Flag: arg, Usage: voiceUsage}
			}
			timeout, err := parseNonNegativeIntOption(args[index], "--timeout-ms", voiceUsage)
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		case strings.HasPrefix(arg, "--timeout-ms="):
			timeout, err := parseNonNegativeIntOption(strings.TrimPrefix(arg, "--timeout-ms="), "--timeout-ms", voiceUsage)
			if err != nil {
				return req, err
			}
			req.TimeoutMS = timeout
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "voice", Option: arg, Usage: voiceUsage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("voice", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "set-command", "command":
		req.Action = "set-command"
		if req.Command == "" && len(rest) > 1 {
			req.Command = strings.Join(rest[1:], " ")
		}
	case "test", "check":
		req.Action = "test"
	case "transcribe", "listen":
		req.Action = strings.ToLower(rest[0])
	case "clear-command":
		req.Action = "clear-command"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, unexpectedExtraArgsError{Command: "voice", Args: []string{rest[0]}, Usage: voiceUsage}
	}
	if req.Action != "set-command" && len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "voice " + strings.ToLower(rest[0]), Args: rest[1:], Usage: voiceUsage}
	}
	if req.Action == "set-command" && strings.TrimSpace(req.Command) == "" {
		return req, requiredArgumentError{Command: "voice set-command", Argument: "COMMAND", Usage: voiceUsage}
	}
	return req, nil
}

func (a *App) voiceStatusReport(action string, path string) voiceReport {
	return voiceReport{
		Kind:              "voice",
		Action:            action,
		Status:            "ok",
		Enabled:           boolPtrEnabled(a.Config.VoiceEnabled),
		CommandConfigured: strings.TrimSpace(a.Config.VoiceCommand) != "",
		CommandAvailable:  externalCommandAvailable(a.Config.VoiceCommand),
		Command:           strings.TrimSpace(a.Config.VoiceCommand),
		Path:              path,
	}
}

func (a *App) runVoiceCommand(req voiceRequest) (voiceReport, error) {
	report := a.voiceStatusReport(req.Action, req.Path)
	if !report.CommandConfigured {
		err := errors.New("voice command is not configured; run `codog voice set-command COMMAND`")
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	if !report.CommandAvailable {
		err := fmt.Errorf("voice command is not executable: %s", report.Command)
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	if (req.Action == "transcribe" || req.Action == "listen") && !report.Enabled {
		err := errors.New("voice mode is disabled; run `codog voice on` before listening")
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	args := externalCommandArgs(report.Command)
	if len(args) == 0 {
		err := errors.New("voice command is empty")
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if strings.TrimSpace(a.Workspace) != "" {
		cmd.Dir = a.Workspace
	}
	if req.Input != "" {
		cmd.Stdin = strings.NewReader(req.Input)
	}
	stdout := &boundedTextBuffer{Limit: 256 * 1024}
	stderr := &boundedTextBuffer{Limit: 64 * 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	report.DurationMS = time.Since(start).Milliseconds()
	report.Stdout = stdout.String()
	report.Stderr = stderr.String()
	report.StdoutTruncated = stdout.Truncated
	report.StderrTruncated = stderr.Truncated
	transcript := strings.TrimSpace(report.Stdout)
	if transcript != "" {
		report.Transcript = transcript
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report.TimedOut = true
			err = fmt.Errorf("voice command timed out after %s", timeout)
		}
		report.Status = "error"
		report.Message = err.Error()
		report.ExitCode = &exitCode
		return report, err
	}
	report.ExitCode = &exitCode
	return report, nil
}

type boundedTextBuffer struct {
	bytes.Buffer
	Limit     int
	Truncated bool
}

func (b *boundedTextBuffer) Write(data []byte) (int, error) {
	accepted := len(data)
	if b.Limit <= 0 {
		return accepted, nil
	}
	remaining := b.Limit - b.Buffer.Len()
	if remaining <= 0 {
		b.Truncated = b.Truncated || len(data) > 0
		return accepted, nil
	}
	if len(data) > remaining {
		b.Truncated = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return accepted, nil
}

func renderVoiceReport(out io.Writer, report voiceReport) {
	fmt.Fprintln(out, "Voice")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Command          %t\n", report.CommandConfigured)
	fmt.Fprintf(out, "  Available        %t\n", report.CommandAvailable)
	if report.Command != "" {
		fmt.Fprintf(out, "  Command value    %s\n", report.Command)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Transcript != "" {
		fmt.Fprintf(out, "  Transcript       %s\n", report.Transcript)
	}
	if report.ExitCode != nil {
		fmt.Fprintf(out, "  Exit code        %d\n", *report.ExitCode)
	}
	if report.DurationMS > 0 {
		fmt.Fprintf(out, "  Duration         %dms\n", report.DurationMS)
	}
	if report.Stderr != "" {
		fmt.Fprintf(out, "  Stderr           %s\n", report.Stderr)
	}
	if report.TimedOut {
		fmt.Fprintln(out, "  Timed out        true")
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type speakRequest struct {
	Action    string
	Format    string
	Target    string
	Path      string
	Command   string
	Input     string
	SessionID string
	Nth       int
	TimeoutMS int
}

type speakReport struct {
	Kind              string `json:"kind"`
	Action            string `json:"action"`
	Status            string `json:"status"`
	CommandConfigured bool   `json:"command_configured"`
	CommandAvailable  bool   `json:"command_available"`
	Command           string `json:"command,omitempty"`
	Path              string `json:"path,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	Nth               int    `json:"nth,omitempty"`
	InputBytes        int    `json:"input_bytes,omitempty"`
	TextPreview       string `json:"text_preview,omitempty"`
	Stdout            string `json:"stdout,omitempty"`
	Stderr            string `json:"stderr,omitempty"`
	StdoutTruncated   bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated   bool   `json:"stderr_truncated,omitempty"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	DurationMS        int64  `json:"duration_ms,omitempty"`
	TimedOut          bool   `json:"timed_out,omitempty"`
	Message           string `json:"message,omitempty"`
}

func (a *App) Speak(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parseSpeakArgs(args, overrides)
	if err != nil {
		return err
	}
	switch req.Action {
	case "status":
	case "set-command":
		command := strings.TrimSpace(req.Command)
		if command == "" {
			return errors.New("speech command is required")
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "speech_command", command); err != nil {
			return err
		}
		a.Config.SpeechCommand = command
		req.Path = path
	case "clear-command", "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "speech_command"); err != nil {
			return err
		}
		a.Config.SpeechCommand = ""
		req.Path = path
	case "test", "speak":
		report, runErr := a.runSpeechCommand(ctx, req)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			renderSpeakReport(a.Out, report)
		}
		return runErr
	default:
		return fmt.Errorf("unknown speak command %q", req.Action)
	}
	report := a.speechStatusReport(req.Action, req.Path)
	if !report.CommandConfigured {
		report.Message = "Speech output needs an external TTS command before it can run."
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderSpeakReport(a.Out, report)
	return nil
}

func parseSpeakArgs(args []string, overrides config.FlagOverrides) (speakRequest, error) {
	req := speakRequest{Action: "speak", Format: "text", Target: "user", SessionID: "latest", Nth: 1}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	rest, err := parseSpeakOptions(args, &req)
	if err != nil {
		return req, err
	}
	if err := validateTextOrJSON(req.Format, "speak"); err != nil {
		return req, err
	}
	return applySpeakAction(req, rest)
}

func parseSpeakOptions(args []string, req *speakRequest) ([]string, error) {
	options := speakValueOptions(req)
	var rest []string
	for index := 0; index < len(args); index++ {
		if args[index] == "--json" {
			req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return nil, err
		}
		if !handled {
			rest = append(rest, args[index])
		}
	}
	return rest, nil
}

func speakValueOptions(req *speakRequest) map[string]valueOption {
	options := map[string]valueOption{
		"--output-format": stringValueOption(&req.Format, "speak output format is required"),
		"--target":        stringValueOption(&req.Target, "speak target is required"),
		"--path":          stringValueOption(&req.Path, "speak config path is required"),
		"--command":       stringValueOption(&req.Command, "speech command is required"),
		"--input":         stringValueOption(&req.Input, "speak input is required"),
		"--session":       stringValueOption(&req.SessionID, "speak session is required"),
		"--resume":        stringValueOption(&req.SessionID, "speak resume session is required"),
		"--nth": {
			missing: func(string) error { return errors.New("speak response index is required") },
			set:     func(value string) error { return setSpeakNth(req, value) },
		},
		"--timeout-ms": {
			missing: func(string) error { return errors.New("speak timeout is required") },
			set:     func(value string) error { return setSpeakTimeout(req, value) },
		},
	}
	options["-o"] = options["--output-format"]
	options["--text"] = options["--input"]
	options["--stdin"] = options["--input"]
	return options
}

func setSpeakNth(req *speakRequest, raw string) error {
	nth, err := strconv.Atoi(raw)
	if err != nil || nth < 1 {
		return errors.New("speak response index must be greater than zero")
	}
	req.Nth = nth
	return nil
}

func setSpeakTimeout(req *speakRequest, raw string) error {
	timeout, err := strconv.Atoi(raw)
	if err != nil || timeout < 0 {
		return errors.New("speak timeout must be a non-negative integer")
	}
	req.TimeoutMS = timeout
	return nil
}

func applySpeakAction(req speakRequest, rest []string) (speakRequest, error) {
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
		return rejectSpeakExtras(req, rest[1:])
	case "set-command", "command":
		req.Action = "set-command"
		if req.Command == "" && len(rest) > 1 {
			req.Command = strings.Join(rest[1:], " ")
		}
	case "clear-command":
		req.Action = "clear-command"
		return rejectSpeakExtras(req, rest[1:])
	case "clear", "reset", "unset":
		req.Action = "clear"
		return rejectSpeakExtras(req, rest[1:])
	case "test", "run":
		req.Action = "test"
		if req.Input == "" && len(rest) > 1 {
			req.Input = strings.Join(rest[1:], " ")
		}
	case "speak":
		req.Action = "speak"
		if req.Input == "" && len(rest) > 1 {
			req.Input = strings.Join(rest[1:], " ")
		}
	case "last", "latest":
		req.Action = "speak"
		return rejectSpeakExtras(req, rest[1:])
	default:
		if req.Input != "" {
			return req, fmt.Errorf("unexpected speak argument %q", rest[0])
		}
		req.Action = "speak"
		req.Input = strings.Join(rest, " ")
	}
	return req, nil
}

func rejectSpeakExtras(req speakRequest, extras []string) (speakRequest, error) {
	if len(extras) > 0 {
		return req, fmt.Errorf("unexpected speak argument %q", extras[0])
	}
	return req, nil
}

func (a *App) speechStatusReport(action string, path string) speakReport {
	command := strings.TrimSpace(a.Config.SpeechCommand)
	return speakReport{
		Kind:              "speak",
		Action:            action,
		Status:            "ok",
		CommandConfigured: command != "",
		CommandAvailable:  externalCommandAvailable(command),
		Command:           command,
		Path:              path,
	}
}

func (a *App) runSpeechCommand(ctx context.Context, req speakRequest) (speakReport, error) {
	report := a.speechStatusReport(req.Action, req.Path)
	if !report.CommandConfigured {
		err := errors.New("speech command is not configured; run `codog speak set-command COMMAND`")
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	if !report.CommandAvailable {
		err := fmt.Errorf("speech command is not executable: %s", report.Command)
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	text := req.Input
	if req.Action == "test" && strings.TrimSpace(text) == "" {
		text = "speech check"
	}
	if strings.TrimSpace(text) == "" {
		if a.Sessions == nil {
			err := errors.New("session store is unavailable")
			report.Status = "error"
			report.Message = err.Error()
			return report, err
		}
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			report.Status = "error"
			report.Message = err.Error()
			return report, err
		}
		report.SessionID = sess.ID
		report.Nth = req.Nth
		text = renderNthAssistantMessage(sess, req.Nth)
		if strings.TrimSpace(text) == "" {
			err := fmt.Errorf("assistant response %d not found", req.Nth)
			report.Status = "error"
			report.Message = err.Error()
			return report, err
		}
	}
	report.InputBytes = len([]byte(text))
	report.TextPreview = speakPreview(text)
	args := externalCommandArgs(report.Command)
	if len(args) == 0 {
		err := errors.New("speech command is empty")
		report.Status = "error"
		report.Message = err.Error()
		return report, err
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	if strings.TrimSpace(a.Workspace) != "" {
		cmd.Dir = a.Workspace
	}
	cmd.Stdin = strings.NewReader(text)
	stdout := &boundedTextBuffer{Limit: 256 * 1024}
	stderr := &boundedTextBuffer{Limit: 64 * 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	report.DurationMS = time.Since(start).Milliseconds()
	report.Stdout = stdout.String()
	report.Stderr = stderr.String()
	report.StdoutTruncated = stdout.Truncated
	report.StderrTruncated = stderr.Truncated
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			report.TimedOut = true
			err = fmt.Errorf("speech command timed out after %s", timeout)
		}
		report.Status = "error"
		report.Message = err.Error()
		report.ExitCode = &exitCode
		return report, err
	}
	report.ExitCode = &exitCode
	return report, nil
}

func speakPreview(text string) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= 120 {
		return text
	}
	return string(runes[:120]) + "..."
}

func renderSpeakReport(out io.Writer, report speakReport) {
	fmt.Fprintln(out, "Speak")
	fmt.Fprintf(out, "  Command          %t\n", report.CommandConfigured)
	fmt.Fprintf(out, "  Available        %t\n", report.CommandAvailable)
	if report.Command != "" {
		fmt.Fprintf(out, "  Command value    %s\n", report.Command)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.SessionID != "" {
		fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	}
	if report.Nth > 0 {
		fmt.Fprintf(out, "  Response index   %d\n", report.Nth)
	}
	if report.TextPreview != "" {
		fmt.Fprintf(out, "  Text             %s\n", report.TextPreview)
	}
	if report.InputBytes > 0 {
		fmt.Fprintf(out, "  Input bytes      %d\n", report.InputBytes)
	}
	if report.Stdout != "" {
		fmt.Fprintf(out, "  Stdout           %s\n", report.Stdout)
	}
	if report.ExitCode != nil {
		fmt.Fprintf(out, "  Exit code        %d\n", *report.ExitCode)
	}
	if report.DurationMS > 0 {
		fmt.Fprintf(out, "  Duration         %dms\n", report.DurationMS)
	}
	if report.Stderr != "" {
		fmt.Fprintf(out, "  Stderr           %s\n", report.Stderr)
	}
	if report.TimedOut {
		fmt.Fprintln(out, "  Timed out        true")
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

const (
	chromeExtensionURL   = "https://claude.ai/chrome"
	chromePermissionsURL = "https://clau.de/chrome/permissions"
	chromeReconnectURL   = "https://clau.de/chrome/reconnect"
	chromeMCPServerName  = "claude-in-chrome"
)

type chromeRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type chromeReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Enabled        bool   `json:"enabled"`
	Previous       bool   `json:"previous,omitempty"`
	Configured     bool   `json:"configured"`
	MCPServer      string `json:"mcp_server"`
	InstallURL     string `json:"install_url"`
	PermissionsURL string `json:"permissions_url"`
	ReconnectURL   string `json:"reconnect_url"`
	RecommendedURL string `json:"recommended_url,omitempty"`
	Path           string `json:"path,omitempty"`
	Message        string `json:"message,omitempty"`
}

const chromeUsage = "codog chrome [status|on|off|toggle|clear|install|permissions|reconnect] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Chrome(args []string) error {
	req, err := parseChromeArgs(args)
	if err != nil {
		return err
	}
	previous := boolPtrEnabled(a.Config.Future.ChromeDefaultEnabled)
	report := chromeReport{
		Kind:           "chrome",
		Action:         req.Action,
		Status:         "ok",
		Enabled:        previous,
		Configured:     a.Config.Future.ChromeDefaultEnabled != nil,
		MCPServer:      chromeMCPServerName,
		InstallURL:     chromeExtensionURL,
		PermissionsURL: chromePermissionsURL,
		ReconnectURL:   chromeReconnectURL,
		RecommendedURL: chromeRecommendedURL(req.Action),
		Message:        chromeActionMessage(req.Action),
	}
	switch req.Action {
	case "status", "install", "permissions", "reconnect":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if err := setPreferenceBool(path, "preferences.chrome_default_enabled", legacyChromeDefaultEnabledKey, next); err != nil {
			return err
		}
		a.Config.Future.ChromeDefaultEnabled = &next
		report.Action = "set"
		report.Enabled = next
		report.Previous = previous
		report.Configured = true
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if err := unsetPreferenceBool(path, "preferences.chrome_default_enabled", legacyChromeDefaultEnabledKey); err != nil {
			return err
		}
		a.Config.Future.ChromeDefaultEnabled = nil
		report.Enabled = false
		report.Previous = previous
		report.Configured = false
		report.Path = path
	default:
		return fmt.Errorf("unknown chrome command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderChromeReport(a.Out, report)
	return nil
}

func parseChromeArgs(args []string) (chromeRequest, error) {
	req := chromeRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "chrome", Flag: arg, Usage: chromeUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "chrome", Flag: arg, Usage: chromeUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "chrome", Flag: arg, Usage: chromeUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "chrome", Option: arg, Usage: chromeUsage}
		default:
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("chrome", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	case "install", "extension":
		req.Action = "install"
	case "permissions", "manage-permissions":
		req.Action = "permissions"
	case "reconnect", "connect":
		req.Action = "reconnect"
	default:
		return req, unexpectedExtraArgsError{Command: "chrome", Args: []string{rest[0]}, Usage: chromeUsage}
	}
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "chrome " + strings.ToLower(rest[0]), Args: rest[1:], Usage: chromeUsage}
	}
	return req, nil
}

func chromeRecommendedURL(action string) string {
	switch action {
	case "install":
		return chromeExtensionURL
	case "permissions":
		return chromePermissionsURL
	case "reconnect":
		return chromeReconnectURL
	default:
		return ""
	}
}

func chromeActionMessage(action string) string {
	switch action {
	case "install":
		return "Open the install URL in Chrome, then reconnect the extension."
	case "permissions":
		return "Open the permissions URL to manage site-level browser access."
	case "reconnect":
		return "Open the reconnect URL after installing or updating the Chrome extension."
	default:
		return "Chrome integration uses the claude-in-chrome MCP server when the extension is connected."
	}
}

func renderChromeReport(out io.Writer, report chromeReport) {
	fmt.Fprintln(out, "Chrome")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	fmt.Fprintf(out, "  MCP server       %s\n", report.MCPServer)
	fmt.Fprintf(out, "  Install URL      %s\n", report.InstallURL)
	fmt.Fprintf(out, "  Permissions URL  %s\n", report.PermissionsURL)
	fmt.Fprintf(out, "  Reconnect URL    %s\n", report.ReconnectURL)
	if report.RecommendedURL != "" {
		fmt.Fprintf(out, "  Recommended URL  %s\n", report.RecommendedURL)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

type notificationsRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type notificationsReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Previous   bool   `json:"previous,omitempty"`
	HookCount  int    `json:"hook_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

const notificationsUsage = "codog notifications [status|on|off|toggle|clear] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Notifications(args []string) error {
	req, err := parseNotificationsArgs(args)
	if err != nil {
		return err
	}
	previous := notificationsEnabled(a.Config.Future.NotificationsEnabled)
	report := a.notificationsReport(req.Action, req.Path)
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if err := setPreferenceBool(path, "preferences.notifications_enabled", legacyNotificationsEnabledKey, next); err != nil {
			return err
		}
		a.Config.Future.NotificationsEnabled = &next
		report = a.notificationsReport("set", path)
		report.Previous = previous
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if err := unsetPreferenceBool(path, "preferences.notifications_enabled", legacyNotificationsEnabledKey); err != nil {
			return err
		}
		a.Config.Future.NotificationsEnabled = nil
		report = a.notificationsReport("clear", path)
		report.Previous = previous
	default:
		return fmt.Errorf("unknown notifications command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderNotificationsReport(a.Out, report)
	return nil
}

func parseNotificationsArgs(args []string) (notificationsRequest, error) {
	req := notificationsRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "notifications",
					Flag:    arg,
					Usage:   notificationsUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "notifications",
					Flag:    arg,
					Usage:   notificationsUsage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "notifications",
					Flag:    arg,
					Usage:   notificationsUsage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "notifications",
					Option:  arg,
					Usage:   notificationsUsage,
				}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("notifications", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true":
		req.Action = "on"
	case "off", "disable", "disabled", "false":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, unexpectedExtraArgsError{
			Command: "notifications",
			Args:    []string{rest[0]},
			Usage:   notificationsUsage,
		}
	}
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{
			Command: "notifications " + strings.ToLower(rest[0]),
			Args:    rest[1:],
			Usage:   notificationsUsage,
		}
	}
	return req, nil
}

func (a *App) notificationsReport(action string, path string) notificationsReport {
	enabled := notificationsEnabled(a.Config.Future.NotificationsEnabled)
	report := notificationsReport{
		Kind:       "notifications",
		Action:     action,
		Status:     "ok",
		Enabled:    enabled,
		Configured: a.Config.Future.NotificationsEnabled != nil,
		HookCount:  len(hookCommandsForList(a.Config.Hooks.NotificationCommands, a.Config.Hooks.Notification)),
		Path:       path,
	}
	if !enabled {
		report.Message = "Notification hooks are disabled."
	} else if report.HookCount == 0 {
		report.Message = "Notifications are enabled, but no notification hooks are configured."
	} else {
		report.Message = "Notifications are enabled and notification hooks are configured."
	}
	return report
}

func notificationsEnabled(value *bool) bool {
	return enabledByDefault(value)
}

func enabledByDefault(value *bool) bool {
	return value == nil || *value
}

func renderNotificationsReport(out io.Writer, report notificationsReport) {
	fmt.Fprintln(out, "Notifications")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	fmt.Fprintf(out, "  Hooks            %d\n", report.HookCount)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

func boolPtrEnabled(value *bool) bool {
	return value != nil && *value
}

func externalCommandAvailable(command string) bool {
	fields := externalCommandArgs(command)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	if filepath.IsAbs(name) {
		info, err := os.Stat(name)
		return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func externalCommandArgs(command string) []string {
	return strings.Fields(command)
}

type vimRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type vimReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	EditorMode string `json:"editor_mode"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

const vimUsage = "codog vim [status|on|off|toggle|clear] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Vim(args []string) error {
	req, err := parseVimArgs(args)
	if err != nil {
		return err
	}
	previous := effectiveEditorMode(a.Config.EditorMode)
	report := vimReport{
		Kind:       "vim",
		Action:     req.Action,
		Status:     "ok",
		Enabled:    editorModeIsVim(previous),
		EditorMode: previous,
	}
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		nextEnabled := req.Action == "on"
		if req.Action == "toggle" {
			nextEnabled = !editorModeIsVim(previous)
		}
		nextMode := "default"
		if nextEnabled {
			nextMode = "vim"
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "editorMode", nextMode); err != nil {
			return err
		}
		a.Config.EditorMode = nextMode
		report.Action = "set"
		report.Enabled = nextEnabled
		report.EditorMode = nextMode
		report.Previous = previous
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "editorMode"); err != nil {
			return err
		}
		a.Config.EditorMode = ""
		report.Action = "clear"
		report.Enabled = false
		report.EditorMode = effectiveEditorMode(a.Config.EditorMode)
		report.Previous = previous
		report.Path = path
	default:
		return fmt.Errorf("unknown vim command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderVimReport(a.Out, report)
	return nil
}

func parseVimArgs(args []string) (vimRequest, error) {
	req := vimRequest{Action: "toggle", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{
					Command: "vim",
					Flag:    arg,
					Usage:   vimUsage,
				}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "vim",
					Flag:    arg,
					Usage:   vimUsage,
				}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{
					Command: "vim",
					Flag:    arg,
					Usage:   vimUsage,
				}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{
					Command: "vim",
					Option:  arg,
					Usage:   vimUsage,
				}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("vim", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	if len(rest) > 1 && strings.ToLower(rest[0]) != "set" {
		return req, unexpectedExtraArgsError{
			Command: "vim " + strings.ToLower(rest[0]),
			Args:    rest[1:],
			Usage:   vimUsage,
		}
	}
	action := strings.ToLower(rest[0])
	if action == "set" {
		if len(rest) < 2 {
			return req, requiredArgumentError{
				Command:  "vim set",
				Argument: "MODE",
				Usage:    vimUsage,
			}
		}
		if len(rest) > 2 {
			return req, unexpectedExtraArgsError{
				Command: "vim set",
				Args:    rest[2:],
				Usage:   vimUsage,
			}
		}
		action = strings.ToLower(rest[1])
	}
	switch action {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "vim":
		req.Action = "on"
	case "off", "disable", "disabled", "default", "emacs":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset":
		req.Action = "clear"
	default:
		return req, invalidFlagValueError{
			Flag:    "mode",
			Value:   action,
			Message: "vim mode must be one of on, off, toggle, status, or clear",
			Usage:   vimUsage,
		}
	}
	return req, nil
}

func renderVimReport(out io.Writer, report vimReport) {
	fmt.Fprintln(out, "Vim")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Editor mode      %s\n", report.EditorMode)
	if report.Previous != "" {
		fmt.Fprintf(out, "  Previous         %s\n", report.Previous)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func effectiveEditorMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "default"
	}
	return mode
}

func editorModeIsVim(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), "vim")
}

type privacyRequest struct {
	Action string
	Key    string
	Value  bool
	Format string
	Target string
	Path   string
}

type privacyReport struct {
	Kind     string          `json:"kind"`
	Action   string          `json:"action"`
	Status   string          `json:"status"`
	Settings map[string]bool `json:"settings"`
	Key      string          `json:"key,omitempty"`
	Value    *bool           `json:"value,omitempty"`
	Path     string          `json:"path,omitempty"`
}

func (a *App) PrivacySettings(args []string) error {
	req, err := parsePrivacyArgs(args)
	if err != nil {
		return err
	}
	report := privacyReport{
		Kind:     "privacy_settings",
		Action:   req.Action,
		Status:   "ok",
		Settings: effectivePrivacySettings(a.Config.Privacy),
	}
	switch req.Action {
	case "show":
	case "set":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "privacy_settings."+req.Key, req.Value); err != nil {
			return err
		}
		a.setPrivacyValue(req.Key, &req.Value)
		report.Settings = effectivePrivacySettings(a.Config.Privacy)
		report.Key = req.Key
		report.Value = &req.Value
		report.Path = path
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "privacy_settings."+req.Key); err != nil {
			return err
		}
		a.setPrivacyValue(req.Key, nil)
		report.Settings = effectivePrivacySettings(a.Config.Privacy)
		report.Key = req.Key
		report.Path = path
	default:
		return fmt.Errorf("unknown privacy-settings command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPrivacyReport(a.Out, report)
	return nil
}

type telemetryRequest struct {
	Action string
	Format string
	Target string
	Path   string
}

type telemetryReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Previous   bool   `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

const telemetryUsage = "codog telemetry [status|on|off|toggle|clear] [--target user|project|local] [--path PATH] [--output-format text|json]"

func (a *App) Telemetry(args []string) error {
	req, err := parseTelemetryArgs(args)
	if err != nil {
		return err
	}
	previous := privacyBool(a.Config.Privacy.TelemetryEnabled, false)
	report := a.telemetryReport(req.Action, req.Path)
	switch req.Action {
	case "status":
	case "on", "off", "toggle":
		next := req.Action == "on"
		if req.Action == "toggle" {
			next = !previous
		}
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.SetFileValue(path, "privacy_settings.telemetry_enabled", next); err != nil {
			return err
		}
		a.Config.Privacy.TelemetryEnabled = &next
		report = a.telemetryReport("set", path)
		report.Previous = previous
	case "clear":
		path, err := a.preferenceConfigPath(req.Target, req.Path)
		if err != nil {
			return err
		}
		if _, err := config.UnsetFileValue(path, "privacy_settings.telemetry_enabled"); err != nil {
			return err
		}
		a.Config.Privacy.TelemetryEnabled = nil
		report = a.telemetryReport("clear", path)
		report.Previous = previous
	default:
		return fmt.Errorf("unknown telemetry command %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTelemetryReport(a.Out, report)
	return nil
}

func parseTelemetryArgs(args []string) (telemetryRequest, error) {
	req := telemetryRequest{Action: "status", Format: "text", Target: "user"}
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "telemetry", Flag: arg, Usage: telemetryUsage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--target":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "telemetry", Flag: arg, Usage: telemetryUsage}
			}
			req.Target = args[index]
		case strings.HasPrefix(arg, "--target="):
			req.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--path":
			index++
			if index >= len(args) || isOutputFormatFlag(args[index]) {
				return req, missingFlagValueError{Command: "telemetry", Flag: arg, Usage: telemetryUsage}
			}
			req.Path = args[index]
		case strings.HasPrefix(arg, "--path="):
			req.Path = strings.TrimPrefix(arg, "--path=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "telemetry", Option: arg, Usage: telemetryUsage}
			}
			rest = append(rest, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("telemetry", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(rest) == 0 {
		return req, nil
	}
	switch strings.ToLower(rest[0]) {
	case "status", "show":
		req.Action = "status"
	case "on", "enable", "enabled", "true", "yes":
		req.Action = "on"
	case "off", "disable", "disabled", "false", "no":
		req.Action = "off"
	case "toggle":
		req.Action = "toggle"
	case "clear", "reset", "unset", "default":
		req.Action = "clear"
	default:
		return req, unexpectedExtraArgsError{Command: "telemetry", Args: []string{rest[0]}, Usage: telemetryUsage}
	}
	if len(rest) > 1 {
		return req, unexpectedExtraArgsError{Command: "telemetry " + strings.ToLower(rest[0]), Args: rest[1:], Usage: telemetryUsage}
	}
	return req, nil
}

func (a *App) telemetryReport(action string, path string) telemetryReport {
	enabled := privacyBool(a.Config.Privacy.TelemetryEnabled, false)
	report := telemetryReport{
		Kind:       "telemetry",
		Action:     action,
		Status:     "ok",
		Enabled:    enabled,
		Configured: a.Config.Privacy.TelemetryEnabled != nil,
		Path:       path,
	}
	if enabled {
		report.Message = "Telemetry is enabled."
	} else if report.Configured {
		report.Message = "Telemetry is disabled."
	} else {
		report.Message = "Telemetry is unset and defaults to disabled."
	}
	return report
}

func renderTelemetryReport(out io.Writer, report telemetryReport) {
	fmt.Fprintln(out, "Telemetry")
	fmt.Fprintf(out, "  Enabled          %t\n", report.Enabled)
	fmt.Fprintf(out, "  Configured       %t\n", report.Configured)
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
	if report.Message != "" {
		fmt.Fprintf(out, "  Message          %s\n", report.Message)
	}
}

const privacyUsage = "codog privacy-settings [show|set|enable|disable|clear] [KEY] [on|off] [--target user|project|local] [--path PATH] [--output-format text|json]"

func parsePrivacyArgs(args []string) (privacyRequest, error) {
	req := privacyRequest{Action: "show", Format: "text", Target: "user"}
	rest, err := parsePrivacyOptions(args, &req)
	if err != nil {
		return req, err
	}
	req.Format, err = normalizeOutputFormat("privacy-settings", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	return applyPrivacyAction(req, rest)
}

func parsePrivacyOptions(args []string, req *privacyRequest) ([]string, error) {
	options := map[string]valueOption{}
	addPrivacyOption(options, "--output-format", &req.Format, false)
	options["-o"] = options["--output-format"]
	addPrivacyOption(options, "--target", &req.Target, true)
	addPrivacyOption(options, "--path", &req.Path, true)
	var rest []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			req.Format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return nil, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return nil, unknownOptionError{Command: "privacy-settings", Option: arg, Usage: privacyUsage}
		}
		rest = append(rest, arg)
	}
	return rest, nil
}

func addPrivacyOption(options map[string]valueOption, name string, target *string, rejectOutput bool) {
	options[name] = valueOption{
		missing: func(flag string) error {
			return missingFlagValueError{Command: "privacy-settings", Flag: flag, Usage: privacyUsage}
		},
		rejectOutputFormat: rejectOutput,
		set: func(value string) error {
			*target = value
			return nil
		},
	}
}

func applyPrivacyAction(req privacyRequest, rest []string) (privacyRequest, error) {
	if len(rest) == 0 {
		return req, nil
	}
	action := strings.ToLower(rest[0])
	switch action {
	case "show", "status", "list":
		if len(rest) > 1 {
			return req, unexpectedExtraArgsError{Command: "privacy-settings " + action, Args: rest[1:], Usage: privacyUsage}
		}
		req.Action = "show"
		return req, nil
	case "set":
		return applyPrivacySet(req, rest)
	case "enable", "enabled", "on":
		return applyPrivacyToggle(req, action, rest, true)
	case "disable", "disabled", "off":
		return applyPrivacyToggle(req, action, rest, false)
	case "clear", "reset", "unset":
		return applyPrivacyClear(req, action, rest)
	default:
		return applyImplicitPrivacySet(req, rest)
	}
}

func applyPrivacySet(req privacyRequest, rest []string) (privacyRequest, error) {
	if len(rest) < 2 {
		return req, requiredArgumentError{Command: "privacy-settings set", Argument: "KEY", Usage: privacyUsage}
	}
	if len(rest) < 3 {
		return req, requiredArgumentError{Command: "privacy-settings set", Argument: "VALUE", Usage: privacyUsage}
	}
	if len(rest) > 3 {
		return req, unexpectedExtraArgsError{Command: "privacy-settings set", Args: rest[3:], Usage: privacyUsage}
	}
	return setPrivacyRequest(req, rest[1], rest[2])
}

func applyPrivacyToggle(req privacyRequest, action string, rest []string, value bool) (privacyRequest, error) {
	key, err := privacyActionKey(action, rest)
	if err != nil {
		return req, err
	}
	req.Action, req.Key, req.Value = "set", key, value
	return req, nil
}

func applyPrivacyClear(req privacyRequest, action string, rest []string) (privacyRequest, error) {
	key, err := privacyActionKey(action, rest)
	if err != nil {
		return req, err
	}
	req.Action, req.Key = "clear", key
	return req, nil
}

func privacyActionKey(action string, rest []string) (string, error) {
	command := "privacy-settings " + action
	if len(rest) < 2 {
		return "", requiredArgumentError{Command: command, Argument: "KEY", Usage: privacyUsage}
	}
	if len(rest) > 2 {
		return "", unexpectedExtraArgsError{Command: command, Args: rest[2:], Usage: privacyUsage}
	}
	return canonicalPrivacyKey(rest[1], privacyUsage)
}

func applyImplicitPrivacySet(req privacyRequest, rest []string) (privacyRequest, error) {
	if len(rest) != 2 {
		return req, unexpectedExtraArgsError{Command: "privacy-settings", Args: []string{rest[0]}, Usage: privacyUsage}
	}
	return setPrivacyRequest(req, rest[0], rest[1])
}

func setPrivacyRequest(req privacyRequest, rawKey, rawValue string) (privacyRequest, error) {
	key, err := canonicalPrivacyKey(rawKey, privacyUsage)
	if err != nil {
		return req, err
	}
	value, err := parseOnOff(rawValue, privacyUsage)
	if err != nil {
		return req, err
	}
	req.Action, req.Key, req.Value = "set", key, value
	return req, nil
}

func renderPrivacyReport(out io.Writer, report privacyReport) {
	fmt.Fprintln(out, "Privacy Settings")
	for _, key := range []string{"telemetry_enabled", "crash_reports_enabled", "prompt_history_enabled"} {
		label := privacyLabel(key)
		state := "disabled"
		if report.Settings[key] {
			state = "enabled"
		}
		fmt.Fprintf(out, "  %-16s %s\n", label, state)
	}
	if report.Key != "" {
		fmt.Fprintf(out, "  Updated          %s\n", report.Key)
	}
	if report.Path != "" {
		fmt.Fprintf(out, "  Config path      %s\n", report.Path)
	}
}

func effectivePrivacySettings(cfg config.PrivacyConfig) map[string]bool {
	return map[string]bool{
		"telemetry_enabled":      privacyBool(cfg.TelemetryEnabled, false),
		"crash_reports_enabled":  privacyBool(cfg.CrashReportsEnabled, false),
		"prompt_history_enabled": privacyBool(cfg.PromptHistoryEnabled, true),
	}
}

func privacyBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func (a *App) promptHistoryEnabled() bool {
	return privacyBool(a.Config.Privacy.PromptHistoryEnabled, true)
}

func (a *App) setPrivacyValue(key string, value *bool) {
	switch key {
	case "telemetry_enabled":
		a.Config.Privacy.TelemetryEnabled = value
	case "crash_reports_enabled":
		a.Config.Privacy.CrashReportsEnabled = value
	case "prompt_history_enabled":
		a.Config.Privacy.PromptHistoryEnabled = value
	}
}

func canonicalPrivacyKey(raw string, usage string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "telemetry", "analytics", "telemetry-enabled":
		return "telemetry_enabled", nil
	case "crash", "crash-report", "crash-reports", "crash-reports-enabled":
		return "crash_reports_enabled", nil
	case "history", "prompt-history", "prompt-history-enabled":
		return "prompt_history_enabled", nil
	default:
		return "", invalidFlagValueError{
			Flag:    "key",
			Value:   raw,
			Message: "privacy setting must be telemetry, crash-reports, or prompt-history",
			Usage:   usage,
		}
	}
}

func privacyLabel(key string) string {
	switch key {
	case "telemetry_enabled":
		return "Telemetry"
	case "crash_reports_enabled":
		return "Crash reports"
	case "prompt_history_enabled":
		return "Prompt history"
	default:
		return key
	}
}

func parseOnOff(raw string, usage string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "on", "enable", "enabled", "yes", "y":
		return true, nil
	case "off", "disable", "disabled", "no", "n":
		return false, nil
	default:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, invalidFlagValueError{
				Flag:    "value",
				Value:   raw,
				Message: "value must be on or off",
				Usage:   usage,
			}
		}
		return parsed, nil
	}
}

func validateTextOrJSON(format, command string) error {
	switch format {
	case "text", "json":
		return nil
	default:
		return fmt.Errorf("unknown %s output format %q", command, format)
	}
}

func (a *App) preferenceConfigPath(target, path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return a.resolveOutputPath(path), nil
	}
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "user", "global":
		if strings.TrimSpace(a.Config.ConfigHome) == "" {
			return "", errors.New("config home is unavailable")
		}
		return filepath.Join(a.Config.ConfigHome, "config.json"), nil
	case "project", "workspace":
		return a.resolveOutputPath(".codog.json"), nil
	case "local":
		return a.resolveOutputPath(".codog.local.json"), nil
	default:
		return "", invalidFlagValueError{
			Flag:    "--target",
			Value:   target,
			Message: "target must be one of user, project, or local",
		}
	}
}

const (
	legacyBackgroundStatePathKey         = "future.background_state_path"
	legacyChromeDefaultEnabledKey        = "future.chrome_default_enabled"
	legacyEditorBridgeSocketKey          = "future.editor_bridge_socket"
	legacyEditorBridgeTokenKey           = "future.editor_bridge_token"
	legacyEnterprisePolicyKey            = "future.enterprise_policy"
	legacyEnterprisePolicyPublicKeyKey   = "future.enterprise_policy_public_key"
	legacyExtraUsageVisitCountKey        = "future.extra_usage_visit_count"
	legacyGuestPassReferralURLKey        = "future.guest_pass_referral_url"
	legacyGuestPassEligibilityCacheKey   = "future.guest_pass_eligibility_cache"
	legacyGuestPassVisitCountKey         = "future.guest_pass_visit_count"
	legacyHasVisitedPassesKey            = "future.has_visited_passes"
	legacyPassesLastSeenRemainingKey     = "future.passes_last_seen_remaining"
	legacyPassesUpsellSeenCountKey       = "future.passes_upsell_seen_count"
	legacyNotificationsEnabledKey        = "future.notifications_enabled"
	legacyPluginMarketplacePublicKeysKey = "future.plugin_marketplace_public_keys"
	legacyPluginMarketplacesKey          = "future.plugin_marketplaces"
	legacyRemoteAuthTokenKey             = "future.remote_auth_token"
	legacyRemoteEnabledKey               = "future.remote_enabled"
	legacyRemoteLeaseSecondsKey          = "future.remote_lease_seconds"
	legacySandboxConfigKey               = "future.sandbox"
	legacySandboxStrategyKey             = "future.sandbox_strategy"
	legacySlackAppInstallCountKey        = "future.slack_app_install_count"
	legacyStickerOrderCountKey           = "future.sticker_order_count"
	legacyUltraReviewEnabledKey          = "future.ultrareview_enabled"
	legacyUpdaterManifestURLKey          = "future.updater_manifest_url"
)

var (
	backgroundResetKeys    = []string{"background", legacyBackgroundStatePathKey}
	compatibilityResetKeys = []string{"compatibility", legacySlackAppInstallCountKey, legacyStickerOrderCountKey, legacyExtraUsageVisitCountKey, legacyGuestPassReferralURLKey, legacyGuestPassEligibilityCacheKey, legacyGuestPassVisitCountKey, legacyHasVisitedPassesKey, legacyPassesUpsellSeenCountKey, legacyPassesLastSeenRemainingKey}
	editorBridgeResetKeys  = []string{"editor_bridge", legacyEditorBridgeSocketKey, legacyEditorBridgeTokenKey}
	enterpriseResetKeys    = []string{"enterprise", legacyEnterprisePolicyKey, legacyEnterprisePolicyPublicKeyKey}
	marketplaceResetKeys   = []string{"marketplace", legacyPluginMarketplacesKey, legacyPluginMarketplacePublicKeysKey}
	preferencesResetKeys   = []string{"preferences", legacyChromeDefaultEnabledKey, legacyNotificationsEnabledKey, legacyUltraReviewEnabledKey}
	remoteResetKeys        = []string{"remote", legacyRemoteEnabledKey, legacyRemoteAuthTokenKey, legacyRemoteLeaseSecondsKey}
	sandboxResetKeys       = []string{"sandbox", legacySandboxStrategyKey, legacySandboxConfigKey}
	updaterResetKeys       = []string{"updater", legacyUpdaterManifestURLKey}
)

func unsetConfigKeys(path string, keys []string) error {
	for _, key := range keys {
		if _, err := config.UnsetFileValue(path, key); err != nil {
			return err
		}
	}
	return nil
}

func setPreferenceBool(path, key, legacyKey string, value bool) error {
	if _, err := config.SetFileValue(path, key, value); err != nil {
		return err
	}
	if legacyKey != "" {
		if _, err := config.UnsetFileValue(path, legacyKey); err != nil {
			return err
		}
	}
	return nil
}

func unsetPreferenceBool(path, key, legacyKey string) error {
	if _, err := config.UnsetFileValue(path, key); err != nil {
		return err
	}
	if legacyKey != "" {
		if _, err := config.UnsetFileValue(path, legacyKey); err != nil {
			return err
		}
	}
	return nil
}

func setCompatibilityValue(path, key, legacyKey string, value any) error {
	if _, err := config.SetFileValue(path, key, value); err != nil {
		return err
	}
	if legacyKey != "" {
		if _, err := config.UnsetFileValue(path, legacyKey); err != nil {
			return err
		}
	}
	return nil
}

func unsetCompatibilityValue(path, key, legacyKey string) error {
	if _, err := config.UnsetFileValue(path, key); err != nil {
		return err
	}
	if legacyKey != "" {
		if _, err := config.UnsetFileValue(path, legacyKey); err != nil {
			return err
		}
	}
	return nil
}

type keybindingEntry struct {
	Key           string `json:"key"`
	NormalizedKey string `json:"normalized_key,omitempty"`
	Action        string `json:"action"`
	Mode          string `json:"mode,omitempty"`
	Description   string `json:"description,omitempty"`
}

type keybindingSection struct {
	Name     string            `json:"name"`
	Entries  []keybindingEntry `json:"entries"`
	Disabled bool              `json:"disabled,omitempty"`
}

type keybindingsRequest struct {
	Action  string
	Format  string
	Force   bool
	Path    string
	Args    []string
	Context string
	Key     string
}

type keybindingReport struct {
	Kind              string                      `json:"kind"`
	Action            string                      `json:"action"`
	Status            string                      `json:"status"`
	EditorMode        string                      `json:"editor_mode"`
	VimMode           bool                        `json:"vim_mode"`
	KeybindingsPath   string                      `json:"keybindings_path,omitempty"`
	KeybindingsExists bool                        `json:"keybindings_exists"`
	UserBindings      *keybindingValidationReport `json:"user_bindings,omitempty"`
	Sections          []keybindingSection         `json:"sections,omitempty"`
}

type keybindingsFileReport struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Path        string `json:"path"`
	Created     bool   `json:"created"`
	Exists      bool   `json:"exists"`
	Opened      bool   `json:"opened,omitempty"`
	Editor      string `json:"editor,omitempty"`
	EditorError string `json:"editor_error,omitempty"`
}

type keybindingValidationReport struct {
	Kind         string              `json:"kind"`
	Action       string              `json:"action"`
	Status       string              `json:"status"`
	Path         string              `json:"path"`
	Exists       bool                `json:"exists"`
	Valid        bool                `json:"valid"`
	ContextCount int                 `json:"context_count"`
	BindingCount int                 `json:"binding_count"`
	Errors       []string            `json:"errors,omitempty"`
	Sections     []keybindingSection `json:"sections,omitempty"`
}

type keybindingResolveReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Context       string   `json:"context"`
	Key           string   `json:"key"`
	NormalizedKey string   `json:"normalized_key"`
	Found         bool     `json:"found"`
	Source        string   `json:"source,omitempty"`
	BindingAction string   `json:"binding_action,omitempty"`
	Section       string   `json:"section,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func (a *App) Keybindings(args []string) error {
	req, err := parseKeybindingsArgs(args)
	if err != nil {
		return err
	}
	switch req.Action {
	case "show":
		report := a.keybindingReport()
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderKeybindings(a.Out, report)
		return nil
	case "path":
		path, err := a.keybindingsPath()
		if err != nil {
			return err
		}
		report := keybindingsFileReport{
			Kind:   "keybindings",
			Action: "path",
			Status: "ok",
			Path:   path,
			Exists: fileExists(path),
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, path)
		return nil
	case "init":
		report, err := a.initKeybindings(req.Force)
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderKeybindingsFileReport(a.Out, report)
		return nil
	case "open":
		report, err := a.openKeybindings()
		if err != nil {
			return err
		}
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		renderKeybindingsFileReport(a.Out, report)
		return nil
	case "validate":
		report := a.validateKeybindings(req.Path)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			renderKeybindingsValidation(a.Out, report)
		}
		if !report.Valid {
			return errors.New("invalid keybindings")
		}
		return nil
	case "resolve":
		report := a.resolveKeybinding(req.Context, req.Key, req.Path)
		if req.Format == "json" {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			renderKeybindingResolve(a.Out, report)
		}
		if report.Status == "invalid" {
			return errors.New("invalid keybindings")
		}
		return nil
	default:
		return fmt.Errorf("unknown keybindings command %q", req.Action)
	}
}
