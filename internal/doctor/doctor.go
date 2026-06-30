package doctor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/mcp"
)

const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

type Options struct {
	Workspace          string
	ConfigHome         string
	Model              string
	BaseURL            string
	APIKey             string
	AuthToken          string
	PermissionMode     string
	ToolCount          int
	MCPServerStatuses  []mcp.ServerStatus
	SessionCount       int
	MemoryFiles        []string
	UserPromptSubmit   []string
	SessionStart       []string
	PreToolUse         []string
	PostToolUse        []string
	PostToolUseFailure []string
	PermissionRequest  []string
	PermissionDenied   []string
	Stop               []string
	StopFailure        []string
	SessionEnd         []string
	Setup              []string
	PreCompact         []string
	PostCompact        []string
	Notification       []string
	SubagentStart      []string
	SubagentStop       []string
	WorktreeCreate     []string
	WorktreeRemove     []string
	CwdChanged         []string
	TaskCreated        []string
	TaskCompleted      []string
	InstructionsLoaded []string
	FileChanged        []string
	SandboxDefault     string
	SandboxOK          bool
}

type Summary struct {
	Total    int `json:"total"`
	OK       int `json:"ok"`
	Warnings int `json:"warnings"`
	Failures int `json:"failures"`
}

type Check struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

type Report struct {
	Kind        string  `json:"kind"`
	Action      string  `json:"action"`
	Status      string  `json:"status"`
	HasFailures bool    `json:"has_failures"`
	Summary     Summary `json:"summary"`
	Checks      []Check `json:"checks"`
}

func Run(opts Options) Report {
	checks := []Check{
		checkAuth(opts),
		checkBaseURL(opts.BaseURL),
		checkConfigHome(opts.ConfigHome),
		checkWorkspace(opts.Workspace),
		checkMemory(opts.MemoryFiles),
		checkModel(opts.Model),
		checkPermissions(opts.PermissionMode),
		checkTools(opts.ToolCount),
		checkMCP(opts.MCPServerStatuses),
		checkSessions(opts.SessionCount),
		checkHooks(opts),
		checkGit(opts.Workspace),
		checkSandbox(opts),
		checkDeveloperToolchain(),
		checkRuntime(),
	}
	return NewReport(checks)
}

func NewReport(checks []Check) Report {
	summary := Summary{Total: len(checks)}
	for _, check := range checks {
		switch check.Status {
		case StatusOK:
			summary.OK++
		case StatusWarn:
			summary.Warnings++
		case StatusFail:
			summary.Failures++
		}
	}
	status := StatusOK
	if summary.Failures > 0 {
		status = StatusFail
	} else if summary.Warnings > 0 {
		status = StatusWarn
	}
	return Report{
		Kind:        "doctor",
		Action:      "doctor",
		Status:      status,
		HasFailures: summary.Failures > 0,
		Summary:     summary,
		Checks:      checks,
	}
}

func RenderText(w io.Writer, report Report) {
	fmt.Fprintln(w, "Doctor")
	fmt.Fprintf(w, "Summary\n  OK               %d\n  Warnings         %d\n  Failures         %d\n", report.Summary.OK, report.Summary.Warnings, report.Summary.Failures)
	for _, check := range report.Checks {
		fmt.Fprintf(w, "\n%s\n  Status           %s\n  Summary          %s\n", check.Name, check.Status, check.Summary)
		if len(check.Details) != 0 {
			fmt.Fprintln(w, "  Details")
			for _, detail := range check.Details {
				fmt.Fprintf(w, "    - %s\n", detail)
			}
		}
		if strings.TrimSpace(check.Hint) != "" {
			fmt.Fprintf(w, "  Hint             %s\n", check.Hint)
		}
	}
}

func checkAuth(opts Options) Check {
	details := []string{
		fmt.Sprintf("API key configured: %t", opts.APIKey != ""),
		fmt.Sprintf("Auth token configured: %t", opts.AuthToken != ""),
	}
	if opts.APIKey != "" || opts.AuthToken != "" {
		return Check{Name: "Auth", Status: StatusOK, Summary: "Anthropic credentials are configured.", Details: details}
	}
	return Check{
		Name:    "Auth",
		Status:  StatusWarn,
		Summary: "No Anthropic credentials are configured.",
		Details: details,
		Hint:    "Set ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, or save an OAuth token before making provider requests.",
	}
}

func checkBaseURL(raw string) Check {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Check{Name: "Base URL", Status: StatusFail, Summary: "Provider base URL is empty.", Hint: "Set base_url or ANTHROPIC_BASE_URL."}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Check{Name: "Base URL", Status: StatusFail, Summary: "Provider base URL is invalid.", Details: []string{raw}, Hint: "Use an absolute http or https URL."}
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return Check{Name: "Base URL", Status: StatusFail, Summary: "Provider base URL must use http or https.", Details: []string{raw}}
	}
	return Check{Name: "Base URL", Status: StatusOK, Summary: "Provider base URL is valid.", Details: []string{redactURL(raw)}}
}

func checkConfigHome(path string) Check {
	path = strings.TrimSpace(path)
	if path == "" {
		return Check{Name: "Config home", Status: StatusFail, Summary: "Config home is empty."}
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return Check{Name: "Config home", Status: StatusFail, Summary: "Config home exists but is not a directory.", Details: []string{path}}
		}
		return Check{Name: "Config home", Status: StatusOK, Summary: "Config home directory is available.", Details: []string{path}}
	}
	if os.IsNotExist(err) {
		return Check{Name: "Config home", Status: StatusWarn, Summary: "Config home does not exist yet.", Details: []string{path}, Hint: "Codog will create it when it writes config, sessions, tokens, or background state."}
	}
	return Check{Name: "Config home", Status: StatusFail, Summary: "Config home cannot be inspected.", Details: []string{err.Error()}}
}

func checkWorkspace(path string) Check {
	path = strings.TrimSpace(path)
	if path == "" {
		return Check{Name: "Workspace", Status: StatusFail, Summary: "Workspace is empty."}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "Workspace", Status: StatusFail, Summary: "Workspace cannot be inspected.", Details: []string{err.Error()}}
	}
	if !info.IsDir() {
		return Check{Name: "Workspace", Status: StatusFail, Summary: "Workspace is not a directory.", Details: []string{path}}
	}
	return Check{Name: "Workspace", Status: StatusOK, Summary: "Workspace directory is available.", Details: []string{path}}
}

func checkMemory(files []string) Check {
	details := []string{fmt.Sprintf("Loaded files: %d", len(files))}
	for _, path := range files {
		details = append(details, "Loaded: "+path)
	}
	return Check{Name: "Memory", Status: StatusOK, Summary: fmt.Sprintf("%d workspace memory files loaded.", len(files)), Details: details}
}

func checkModel(model string) Check {
	model = strings.TrimSpace(model)
	if model == "" {
		return Check{Name: "Model", Status: StatusFail, Summary: "Model is empty.", Hint: "Set --model, CODOG_MODEL, or model in config."}
	}
	return Check{Name: "Model", Status: StatusOK, Summary: "Model is configured.", Details: []string{model}}
}

func checkPermissions(mode string) Check {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "read-only", "workspace-write", "danger-full-access", "prompt", "allow":
		return Check{Name: "Permissions", Status: StatusOK, Summary: "Permission mode is valid.", Details: []string{mode}}
	case "":
		return Check{Name: "Permissions", Status: StatusFail, Summary: "Permission mode is empty.", Hint: "Use read-only, workspace-write, danger-full-access, prompt, or allow."}
	default:
		return Check{Name: "Permissions", Status: StatusFail, Summary: "Permission mode is invalid.", Details: []string{mode}, Hint: "Use read-only, workspace-write, danger-full-access, prompt, or allow."}
	}
}

func checkTools(count int) Check {
	if count <= 0 {
		return Check{Name: "Tools", Status: StatusFail, Summary: "No tools are registered."}
	}
	return Check{Name: "Tools", Status: StatusOK, Summary: "Tool registry is populated.", Details: []string{fmt.Sprintf("Registered tools: %d", count)}}
}

func checkMCP(statuses []mcp.ServerStatus) Check {
	if len(statuses) == 0 {
		return Check{Name: "MCP", Status: StatusOK, Summary: "No MCP servers are configured.", Details: []string{"Configured servers: 0"}}
	}
	details := make([]string, 0, len(statuses)+1)
	details = append(details, fmt.Sprintf("Configured servers: %d", len(statuses)))
	failures := 0
	for _, status := range statuses {
		state := strings.TrimSpace(status.Status)
		if state == "" {
			state = "unknown"
		}
		detail := fmt.Sprintf("%s: %s", status.Name, state)
		if status.ToolCount != 0 {
			detail += fmt.Sprintf(" tools=%d", status.ToolCount)
		}
		if status.ResolvedPath != "" {
			detail += " path=" + status.ResolvedPath
		}
		if status.Error != "" {
			detail += " error=" + status.Error
		}
		details = append(details, detail)
		if state != StatusOK {
			failures++
		}
	}
	if failures != 0 {
		return Check{
			Name:    "MCP",
			Status:  StatusWarn,
			Summary: fmt.Sprintf("%d MCP server(s) are unavailable.", failures),
			Details: details,
			Hint:    "Fix missing MCP commands or server startup errors before relying on MCP tools.",
		}
	}
	return Check{Name: "MCP", Status: StatusOK, Summary: "All configured MCP servers responded.", Details: details}
}

func checkSessions(count int) Check {
	if count < 0 {
		return Check{Name: "Sessions", Status: StatusWarn, Summary: "Session store could not be listed."}
	}
	return Check{Name: "Sessions", Status: StatusOK, Summary: "Session store is readable.", Details: []string{fmt.Sprintf("Saved sessions: %d", count)}}
}

func checkHooks(opts Options) Check {
	userPromptSubmit := compactHookCommands(opts.UserPromptSubmit)
	sessionStart := compactHookCommands(opts.SessionStart)
	pre := compactHookCommands(opts.PreToolUse)
	post := compactHookCommands(opts.PostToolUse)
	postFailure := compactHookCommands(opts.PostToolUseFailure)
	permissionRequest := compactHookCommands(opts.PermissionRequest)
	permissionDenied := compactHookCommands(opts.PermissionDenied)
	sessionEnd := compactHookCommands(opts.SessionEnd)
	setup := compactHookCommands(opts.Setup)
	stop := compactHookCommands(opts.Stop)
	stopFailure := compactHookCommands(opts.StopFailure)
	preCompact := compactHookCommands(opts.PreCompact)
	postCompact := compactHookCommands(opts.PostCompact)
	notification := compactHookCommands(opts.Notification)
	subagentStart := compactHookCommands(opts.SubagentStart)
	subagentStop := compactHookCommands(opts.SubagentStop)
	worktreeCreate := compactHookCommands(opts.WorktreeCreate)
	worktreeRemove := compactHookCommands(opts.WorktreeRemove)
	cwdChanged := compactHookCommands(opts.CwdChanged)
	taskCreated := compactHookCommands(opts.TaskCreated)
	taskCompleted := compactHookCommands(opts.TaskCompleted)
	instructionsLoaded := compactHookCommands(opts.InstructionsLoaded)
	fileChanged := compactHookCommands(opts.FileChanged)
	total := len(userPromptSubmit) + len(sessionStart) + len(sessionEnd) + len(setup) + len(pre) + len(post) + len(postFailure) + len(permissionRequest) + len(permissionDenied) + len(stop) + len(stopFailure) + len(preCompact) + len(postCompact) + len(notification) + len(subagentStart) + len(subagentStop) + len(worktreeCreate) + len(worktreeRemove) + len(cwdChanged) + len(taskCreated) + len(taskCompleted) + len(instructionsLoaded) + len(fileChanged)
	details := []string{
		fmt.Sprintf("UserPromptSubmit hooks: %d", len(userPromptSubmit)),
		fmt.Sprintf("SessionStart hooks: %d", len(sessionStart)),
		fmt.Sprintf("SessionEnd hooks: %d", len(sessionEnd)),
		fmt.Sprintf("Setup hooks: %d", len(setup)),
		fmt.Sprintf("PreToolUse hooks: %d", len(pre)),
		fmt.Sprintf("PostToolUse hooks: %d", len(post)),
		fmt.Sprintf("PostToolUseFailure hooks: %d", len(postFailure)),
		fmt.Sprintf("PermissionRequest hooks: %d", len(permissionRequest)),
		fmt.Sprintf("PermissionDenied hooks: %d", len(permissionDenied)),
		fmt.Sprintf("Stop hooks: %d", len(stop)),
		fmt.Sprintf("StopFailure hooks: %d", len(stopFailure)),
		fmt.Sprintf("PreCompact hooks: %d", len(preCompact)),
		fmt.Sprintf("PostCompact hooks: %d", len(postCompact)),
		fmt.Sprintf("Notification hooks: %d", len(notification)),
		fmt.Sprintf("SubagentStart hooks: %d", len(subagentStart)),
		fmt.Sprintf("SubagentStop hooks: %d", len(subagentStop)),
		fmt.Sprintf("WorktreeCreate hooks: %d", len(worktreeCreate)),
		fmt.Sprintf("WorktreeRemove hooks: %d", len(worktreeRemove)),
		fmt.Sprintf("CwdChanged hooks: %d", len(cwdChanged)),
		fmt.Sprintf("TaskCreated hooks: %d", len(taskCreated)),
		fmt.Sprintf("TaskCompleted hooks: %d", len(taskCompleted)),
		fmt.Sprintf("InstructionsLoaded hooks: %d", len(instructionsLoaded)),
		fmt.Sprintf("FileChanged hooks: %d", len(fileChanged)),
	}
	if total == 0 {
		return Check{Name: "Hooks", Status: StatusOK, Summary: "No hooks are configured.", Details: details}
	}
	if _, err := exec.LookPath("sh"); err != nil {
		return Check{Name: "Hooks", Status: StatusWarn, Summary: "Hooks are configured but sh is not available on PATH.", Details: details, Hint: "Install a POSIX-compatible shell or remove configured hooks."}
	}
	issues := hookPathIssues(opts.Workspace, "UserPromptSubmit", userPromptSubmit)
	issues = append(issues, hookPathIssues(opts.Workspace, "SessionStart", sessionStart)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "SessionEnd", sessionEnd)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "Setup", setup)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PreToolUse", pre)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PostToolUse", post)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PostToolUseFailure", postFailure)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PermissionRequest", permissionRequest)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PermissionDenied", permissionDenied)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "Stop", stop)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "StopFailure", stopFailure)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PreCompact", preCompact)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "PostCompact", postCompact)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "Notification", notification)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "SubagentStart", subagentStart)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "SubagentStop", subagentStop)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "WorktreeCreate", worktreeCreate)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "WorktreeRemove", worktreeRemove)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "CwdChanged", cwdChanged)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "TaskCreated", taskCreated)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "TaskCompleted", taskCompleted)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "InstructionsLoaded", instructionsLoaded)...)
	issues = append(issues, hookPathIssues(opts.Workspace, "FileChanged", fileChanged)...)
	if len(issues) != 0 {
		details = append(details, issues...)
		return Check{Name: "Hooks", Status: StatusWarn, Summary: "Some hook command paths could not be found.", Details: details, Hint: "Fix missing hook script paths or use a command available on PATH."}
	}
	return Check{Name: "Hooks", Status: StatusOK, Summary: "Hook configuration is runnable.", Details: details}
}

func hookPathIssues(workspace string, event string, commands []string) []string {
	issues := []string{}
	for _, command := range commands {
		path, ok := hookCommandPath(workspace, command)
		if !ok {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				issues = append(issues, fmt.Sprintf("%s missing path: %s", event, path))
				continue
			}
			issues = append(issues, fmt.Sprintf("%s cannot inspect path %s: %s", event, path, err))
		}
	}
	return issues
}

func hookCommandPath(workspace string, command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "|&;<>()$`*?[]{}!\"'\\\n\r") {
		return "", false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 || !strings.Contains(fields[0], "/") {
		return "", false
	}
	path := fields[0]
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return filepath.Clean(path), true
}

func compactHookCommands(commands []string) []string {
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command != "" {
			out = append(out, command)
		}
	}
	return out
}

func checkGit(workspace string) Check {
	if _, err := exec.LookPath("git"); err != nil {
		return Check{Name: "Git", Status: StatusWarn, Summary: "git is not available on PATH.", Hint: "Install git to enable diff, commit, workspace, and worktree features."}
	}
	inside, err := runGit(workspace, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return Check{Name: "Git", Status: StatusWarn, Summary: "Workspace is not inside a git worktree.", Hint: "Run codog from a git worktree to enable diff, commit, and agent worktree features."}
	}
	details := []string{"Inside worktree: true"}
	if branch, err := runGit(workspace, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		details = append(details, "Branch: "+strings.TrimSpace(branch))
	}
	return Check{Name: "Git", Status: StatusOK, Summary: "git worktree is available.", Details: details}
}

func checkSandbox(opts Options) Check {
	if opts.SandboxDefault == "" {
		if opts.SandboxOK {
			return Check{Name: "Sandbox", Status: StatusOK, Summary: "Sandbox support is available."}
		}
		return Check{Name: "Sandbox", Status: StatusWarn, Summary: "No platform sandbox strategy was detected.", Hint: "Set future.sandbox_strategy to a supported strategy when isolation is required."}
	}
	status := StatusOK
	summary := "Sandbox strategy is available."
	if !opts.SandboxOK {
		status = StatusWarn
		summary = "Configured platform sandbox strategy is not available."
	}
	return Check{Name: "Sandbox", Status: status, Summary: summary, Details: []string{"Default: " + opts.SandboxDefault}}
}

func checkDeveloperToolchain() Check {
	path, err := exec.LookPath("go")
	if err != nil {
		return Check{Name: "Go toolchain", Status: StatusWarn, Summary: "go is not available on PATH.", Hint: "Install Go to build Codog from source or use Go code diagnostics."}
	}
	version, err := runCommand("", "go", "version")
	details := []string{"Path: " + path}
	if err == nil {
		details = append(details, strings.TrimSpace(version))
	}
	return Check{Name: "Go toolchain", Status: StatusOK, Summary: "Go toolchain is available.", Details: details}
}

func checkRuntime() Check {
	details := []string{
		"OS: " + runtime.GOOS,
		"Arch: " + runtime.GOARCH,
		"Go runtime: " + runtime.Version(),
	}
	if exe, err := os.Executable(); err == nil {
		details = append(details, "Executable: "+exe)
	}
	return Check{Name: "Runtime", Status: StatusOK, Summary: "Codog runtime metadata is available.", Details: details}
}

func runGit(workspace string, args ...string) (string, error) {
	return runCommand(workspace, "git", args...)
}

func runCommand(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() != 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return stdout.String(), nil
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.UserPassword("[redacted]", "[redacted]")
	return parsed.String()
}
