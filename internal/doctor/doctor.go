// Package doctor runs local environment checks for `codog doctor`.
package doctor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/providerdiag"
	"github.com/Rememorio/codog/internal/sandbox"
	localstatus "github.com/Rememorio/codog/internal/status"
)

const (
	// StatusOK means a doctor check passed.
	StatusOK = "ok"
	// StatusWarn means a doctor check found an actionable warning.
	StatusWarn = "warn"
	// StatusFail means a doctor check found a blocking failure.
	StatusFail = "fail"
)

// Options contains the runtime and configuration facts checked by Doctor.
type Options struct {
	Workspace             string
	ConfigHome            string
	Model                 string
	RuntimeProvider       string
	RuntimeProviderSource string
	BaseURL               string
	APIKey                string
	AuthToken             string
	PermissionMode        string
	PermissionModeRaw     string
	PermissionModeSource  string
	PermissionModeEnvVar  string
	PermissionRules       localstatus.PermissionRulesStatus
	ConfigLoadError       string
	ConfigLoadErrorKind   string
	ToolCount             int
	ToolPermissions       []ToolPermission
	MCPServerStatuses     []mcp.ServerStatus
	MCPValidation         localstatus.MCPValidationStatus
	HookValidation        localstatus.HookValidationStatus
	SessionCount          int
	MemoryFiles           []string
	UserPromptSubmit      []string
	SessionStart          []string
	PreToolUse            []string
	PostToolUse           []string
	PostToolUseFailure    []string
	PermissionRequest     []string
	PermissionDenied      []string
	Stop                  []string
	StopFailure           []string
	SessionEnd            []string
	Setup                 []string
	PreCompact            []string
	PostCompact           []string
	Notification          []string
	SubagentStart         []string
	SubagentStop          []string
	WorktreeCreate        []string
	WorktreeRemove        []string
	CwdChanged            []string
	TaskCreated           []string
	TaskCompleted         []string
	InstructionsLoaded    []string
	FileChanged           []string
	SandboxDefault        string
	SandboxOK             bool
	SandboxStrategies     []string
	SandboxFallback       string
	SandboxInContainer    bool
	SandboxRuntime        *sandbox.SandboxExecutionStatus
}

// ToolPermission describes the minimum permission a registered tool needs.
type ToolPermission struct {
	Name               string `json:"name"`
	RequiredPermission string `json:"required_permission"`
}

// Summary counts doctor checks by severity.
type Summary struct {
	Total    int `json:"total"`
	OK       int `json:"ok"`
	Warnings int `json:"warnings"`
	Failures int `json:"failures"`
}

// Check describes one doctor check result.
type Check struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Details []string       `json:"details,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Report is the stable JSON payload returned by `codog doctor --json`.
type Report struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	SchemaVersion string   `json:"schema_version"`
	HasFailures   bool     `json:"has_failures"`
	Summary       Summary  `json:"summary"`
	Checks        []Check  `json:"checks"`
	OutputFields  []string `json:"output_fields"`
	CheckNames    []string `json:"check_names"`
	StatusValues  []string `json:"status_values"`
}

// Run evaluates local Codog configuration, workspace, hooks, MCP, sandbox, and
// developer-toolchain health.
func Run(opts Options) Report {
	checks := []Check{
		checkAuth(opts),
		checkBaseURL(opts.BaseURL),
		checkProviderEndpoints(),
		checkConfigLoad(opts),
		checkConfigHome(opts.ConfigHome),
		checkWorkspace(opts.Workspace),
		checkMemory(opts.MemoryFiles),
		checkModel(opts.Model),
		checkPermissions(opts.PermissionMode, opts.PermissionModeRaw, opts.PermissionModeSource, opts.PermissionModeEnvVar, opts.ToolPermissions),
		checkPermissionRules(opts.PermissionRules),
		checkTools(opts.ToolCount),
		checkMCPValidation(opts.MCPValidation),
		checkMCP(opts.MCPServerStatuses),
		checkSessions(opts.SessionCount),
		checkHooks(opts),
		checkHookValidation(opts.HookValidation),
		checkGit(opts.Workspace),
		checkSandbox(opts),
		checkDeveloperToolchain(),
		checkRuntime(),
	}
	return NewReport(checks)
}

// NewReport combines checks and derives the top-level doctor status.
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
		Kind:          "doctor",
		Action:        "doctor",
		Status:        status,
		SchemaVersion: "1.0",
		HasFailures:   summary.Failures > 0,
		Summary:       summary,
		Checks:        checks,
		OutputFields:  doctorOutputFields(),
		CheckNames:    doctorCheckNames(checks),
		StatusValues:  []string{StatusOK, StatusWarn, StatusFail},
	}
}

func doctorOutputFields() []string {
	return []string{
		"kind",
		"action",
		"status",
		"schema_version",
		"has_failures",
		"summary",
		"checks",
		"output_fields",
		"check_names",
		"status_values",
	}
}

func doctorCheckNames(checks []Check) []string {
	names := make([]string, 0, len(checks))
	seen := map[string]struct{}{}
	for _, check := range checks {
		name := strings.ToLower(strings.TrimSpace(check.Name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// RenderText writes a human-readable doctor report.
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

func checkConfigLoad(opts Options) Check {
	if strings.TrimSpace(opts.ConfigLoadError) == "" {
		return Check{
			Name:    "Config",
			Status:  StatusOK,
			Summary: "Runtime config loaded successfully.",
			Details: []string{
				"Config load error: none",
				"Model: " + emptyDoctorValue(opts.Model),
				"Permission mode: " + emptyDoctorValue(opts.PermissionMode),
				"Permission mode source: " + emptyDoctorValue(opts.PermissionModeSource),
				fmt.Sprintf("MCP servers: %d", opts.MCPValidation.TotalConfigured),
				fmt.Sprintf("Invalid MCP servers: %d", opts.MCPValidation.InvalidCount),
				fmt.Sprintf("Invalid hooks: %d", opts.HookValidation.InvalidCount),
			},
			Data: map[string]any{
				"load_error":              nil,
				"load_error_kind":         "",
				"model":                   opts.Model,
				"permission_mode":         opts.PermissionMode,
				"permission_mode_raw":     defaultDoctorValue(opts.PermissionModeRaw, opts.PermissionMode),
				"permission_mode_source":  defaultDoctorValue(opts.PermissionModeSource, "unknown"),
				"permission_mode_env_var": strings.TrimSpace(opts.PermissionModeEnvVar),
				"mcp_servers":             opts.MCPValidation.TotalConfigured,
				"mcp_invalid_servers":     opts.MCPValidation.InvalidCount,
				"hook_invalid_entries":    opts.HookValidation.InvalidCount,
			},
		}
	}
	kind := strings.TrimSpace(opts.ConfigLoadErrorKind)
	if kind == "" {
		kind = "config_load_failed"
	}
	return Check{
		Name:    "Config",
		Status:  StatusFail,
		Summary: "Runtime config failed to load.",
		Details: []string{
			"Load error kind: " + kind,
			"Load error: " + strings.TrimSpace(opts.ConfigLoadError),
		},
		Hint: "Fix the JSON syntax error in the listed config file, then rerun `codog doctor`.",
		Data: map[string]any{
			"load_error":      strings.TrimSpace(opts.ConfigLoadError),
			"load_error_kind": kind,
		},
	}
}

func checkAuth(opts Options) Check {
	auth := providerdiag.AnalyzeAuth(providerdiag.AuthOptions{
		Model:                 opts.Model,
		RuntimeProvider:       opts.RuntimeProvider,
		RuntimeProviderSource: opts.RuntimeProviderSource,
		BaseURL:               opts.BaseURL,
		APIKey:                opts.APIKey,
		AuthToken:             opts.AuthToken,
	})
	details := []string{
		"Selected provider: " + auth.SelectedProvider,
		fmt.Sprintf("API key configured: %t", auth.SelectedProviderAPIKeyPresent),
		fmt.Sprintf("Auth token configured: %t", auth.SelectedProviderAuthTokenPresent),
		"Effective auth source: " + auth.EffectiveAuthSource,
	}
	if auth.RequiredAPIKeyEnv != "" {
		details = append(details, "Required API key env: "+auth.RequiredAPIKeyEnv)
	}
	if len(auth.RequiredAuthEnvs) != 0 {
		details = append(details, "Accepted auth envs: "+strings.Join(auth.RequiredAuthEnvs, ", "))
	}
	if auth.RuntimeProviderSource != "" {
		details = append(details, "Provider source: "+auth.RuntimeProviderSource)
	}
	if len(auth.HeadersSent) != 0 {
		details = append(details, "Headers sent: "+strings.Join(auth.HeadersSent, ", "))
	}
	data := auth.Data()
	if auth.SelectedProviderAuthPresent {
		if auth.Warning != "" {
			return Check{Name: "Auth", Status: StatusWarn, Summary: auth.Warning + ".", Details: details, Hint: auth.Hint, Data: data}
		}
		summary := providerDisplayName(auth.SelectedProvider) + " credentials are configured."
		if auth.AuthOptional {
			summary = providerDisplayName(auth.SelectedProvider) + " does not require credentials for the selected local route."
		}
		return Check{Name: "Auth", Status: StatusOK, Summary: summary, Details: details, Data: data}
	}
	return Check{
		Name:    "Auth",
		Status:  StatusWarn,
		Summary: "No " + providerDisplayName(auth.SelectedProvider) + " credentials are configured.",
		Details: details,
		Hint:    providerAuthHint(auth.SelectedProvider, auth.RequiredAPIKeyEnv, auth.RequiredAuthEnvs),
		Data:    data,
	}
}

func providerAuthHint(provider string, requiredKeyEnv string, requiredAuthEnvs []string) string {
	if provider == modelrouting.ProviderOpenAI && requiredKeyEnv == "" {
		return "Local OpenAI-compatible routes such as OLLAMA_HOST or a loopback/private OPENAI_BASE_URL can run without an API key."
	}
	if len(requiredAuthEnvs) > 1 {
		return "Set " + strings.Join(requiredAuthEnvs, ", ") + ", or save an OAuth token before making provider requests."
	}
	if requiredKeyEnv != "" {
		return "Set " + requiredKeyEnv + " before making provider requests."
	}
	return "Configure credentials for the selected provider before making provider requests."
}

func providerDisplayName(provider string) string {
	switch provider {
	case modelrouting.ProviderOpenAI:
		return "OpenAI-compatible"
	case modelrouting.ProviderXAI:
		return "xAI"
	case modelrouting.ProviderDashScope:
		return "DashScope"
	default:
		return "Anthropic"
	}
}

func checkBaseURL(raw string) Check {
	raw = strings.TrimSpace(raw)
	diag := modelrouting.DiagnoseBaseURL("", "", "active", raw)
	if diag.ErrorKind == "empty_base_url" {
		return Check{Name: "Base URL", Status: StatusFail, Summary: "Provider base URL is empty.", Hint: "Set base_url or ANTHROPIC_BASE_URL.", Data: map[string]any{"endpoint": diag}}
	}
	if !diag.Valid {
		return Check{Name: "Base URL", Status: StatusFail, Summary: "Provider base URL is invalid.", Details: []string{diag.URL}, Hint: "Use an absolute http or https URL.", Data: map[string]any{"endpoint": diag}}
	}
	return Check{Name: "Base URL", Status: StatusOK, Summary: "Provider base URL is valid.", Details: []string{diag.URL}, Data: map[string]any{"endpoint": diag}}
}

func checkProviderEndpoints() Check {
	diagnostics := configuredProviderEndpointDiagnostics()
	if len(diagnostics) == 0 {
		return Check{
			Name:    "Provider endpoints",
			Status:  StatusOK,
			Summary: "No provider base URL environment overrides are set.",
			Data: map[string]any{
				"endpoints": []modelrouting.BaseURLDiagnostic{},
			},
		}
	}
	invalid := 0
	details := make([]string, 0, len(diagnostics))
	for _, diag := range diagnostics {
		status := "valid"
		if !diag.Valid {
			invalid++
			status = diag.ErrorKind
		}
		details = append(details, fmt.Sprintf("%s (%s): %s", diag.Env, diag.Provider, status))
	}
	if invalid != 0 {
		return Check{
			Name:    "Provider endpoints",
			Status:  StatusWarn,
			Summary: fmt.Sprintf("%d provider base URL environment override(s) are invalid.", invalid),
			Details: details,
			Hint:    "Use absolute http or https URLs for provider base URL environment variables.",
			Data: map[string]any{
				"invalid_count": invalid,
				"endpoints":     diagnostics,
			},
		}
	}
	return Check{
		Name:    "Provider endpoints",
		Status:  StatusOK,
		Summary: "Provider base URL environment overrides are valid.",
		Details: details,
		Data: map[string]any{
			"invalid_count": 0,
			"endpoints":     diagnostics,
		},
	}
}

func configuredProviderEndpointDiagnostics() []modelrouting.BaseURLDiagnostic {
	defs := []struct {
		provider string
		env      string
	}{
		{provider: modelrouting.ProviderAnthropic, env: "ANTHROPIC_BASE_URL"},
		{provider: modelrouting.ProviderOpenAI, env: "OPENAI_BASE_URL"},
		{provider: modelrouting.ProviderXAI, env: "XAI_BASE_URL"},
		{provider: modelrouting.ProviderDashScope, env: "DASHSCOPE_BASE_URL"},
	}
	out := make([]modelrouting.BaseURLDiagnostic, 0, len(defs))
	for _, def := range defs {
		value, ok := os.LookupEnv(def.env)
		if !ok {
			continue
		}
		out = append(out, modelrouting.DiagnoseBaseURL(def.provider, def.env, "env", value))
	}
	return out
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

func checkPermissions(mode, raw, source, envVar string, toolPermissions []ToolPermission) Check {
	mode = strings.TrimSpace(mode)
	raw = defaultDoctorValue(raw, mode)
	source = defaultDoctorValue(source, "unknown")
	envVar = strings.TrimSpace(envVar)
	allowedTools, gatedTools := permissionToolLists(mode, toolPermissions)
	sourceExplicit := source != "default" && source != "unknown"
	message := permissionModeMessage(mode, source, raw, len(allowedTools), len(gatedTools))
	details := []string{
		"mode: " + emptyDoctorValue(mode),
		"raw: " + emptyDoctorValue(raw),
		"source: " + emptyDoctorValue(source),
		fmt.Sprintf("source explicit: %t", sourceExplicit),
		message,
	}
	if envVar != "" {
		details = append(details, "env var: "+envVar)
	}
	data := map[string]any{
		"mode":            mode,
		"raw":             raw,
		"source":          source,
		"source_explicit": sourceExplicit,
		"env_var":         envVar,
		"message":         message,
		"allowed_tools":   allowedTools,
		"gated_tools":     gatedTools,
		"allowed_count":   len(allowedTools),
		"gated_count":     len(gatedTools),
	}
	switch mode {
	case "read-only", "workspace-write", "danger-full-access", "prompt", "allow":
		return Check{Name: "Permissions", Status: StatusOK, Summary: "Permission mode is valid.", Details: details, Data: data}
	case "":
		return Check{Name: "Permissions", Status: StatusFail, Summary: "Permission mode is empty.", Details: details, Hint: "Use read-only, workspace-write, danger-full-access, prompt, or allow.", Data: data}
	default:
		return Check{Name: "Permissions", Status: StatusFail, Summary: "Permission mode is invalid.", Details: details, Hint: "Use read-only, workspace-write, danger-full-access, prompt, or allow.", Data: data}
	}
}

func permissionToolLists(mode string, toolPermissions []ToolPermission) ([]string, []string) {
	allowed := []string{}
	gated := []string{}
	mode = strings.TrimSpace(mode)
	for _, tool := range toolPermissions {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		required := strings.TrimSpace(tool.RequiredPermission)
		if permissionModeAllows(mode, required) {
			allowed = append(allowed, name)
		} else {
			gated = append(gated, name)
		}
	}
	sort.Strings(allowed)
	sort.Strings(gated)
	return allowed, gated
}

func permissionModeAllows(mode, required string) bool {
	if mode == "allow" {
		return true
	}
	if mode == "prompt" || mode == "" {
		return false
	}
	requiredRank := permissionModeRank(required)
	if requiredRank == 0 {
		return false
	}
	return permissionModeRank(mode) >= requiredRank
}

func permissionModeRank(mode string) int {
	switch strings.TrimSpace(mode) {
	case "read-only":
		return 1
	case "workspace-write":
		return 2
	case "danger-full-access":
		return 3
	case "allow":
		return 4
	default:
		return 0
	}
}

func permissionModeMessage(mode, source, raw string, allowedCount, gatedCount int) string {
	return fmt.Sprintf("Permission mode %s resolved from %s raw value %q; %d tools are allowed by mode and %d require confirmation or a rule override.", emptyDoctorValue(mode), emptyDoctorValue(source), raw, allowedCount, gatedCount)
}

func checkPermissionRules(rules localstatus.PermissionRulesStatus) Check {
	total := len(rules.Allow) + len(rules.Deny) + len(rules.Ask) + len(rules.DeniedTools)
	details := []string{
		fmt.Sprintf("Total rules: %d", total),
		fmt.Sprintf("Unknown tools: %d", rules.UnknownCount),
	}
	details = append(details, permissionRuleDetails("allow", rules.Allow)...)
	details = append(details, permissionRuleDetails("deny", rules.Deny)...)
	details = append(details, permissionRuleDetails("ask", rules.Ask)...)
	details = append(details, permissionRuleDetails("denied_tools", rules.DeniedTools)...)
	data := map[string]any{
		"allow":         rules.Allow,
		"deny":          rules.Deny,
		"ask":           rules.Ask,
		"denied_tools":  rules.DeniedTools,
		"unknown_count": rules.UnknownCount,
	}
	if rules.UnknownCount > 0 {
		return Check{
			Name:    "Permission rules",
			Status:  StatusWarn,
			Summary: fmt.Sprintf("%d permission rule(s) reference unknown tools.", rules.UnknownCount),
			Details: details,
			Hint:    "Fix unknown permission rule tools or inspect `codog status --json` config.permission_rules for resolved_tool_name details.",
			Data:    data,
		}
	}
	if total == 0 {
		return Check{
			Name:    "Permission rules",
			Status:  StatusOK,
			Summary: "No explicit permission rules are configured.",
			Details: details,
			Data:    data,
		}
	}
	return Check{
		Name:    "Permission rules",
		Status:  StatusOK,
		Summary: fmt.Sprintf("%d permission rule(s) resolved successfully.", total),
		Details: details,
		Data:    data,
	}
}

func permissionRuleDetails(kind string, entries []localstatus.PermissionRuleStatus) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		raw := strings.TrimSpace(entry.Raw)
		if raw == "" {
			continue
		}
		resolved := strings.TrimSpace(entry.ResolvedToolName)
		if resolved == "" {
			resolved = "<unknown>"
		}
		detail := fmt.Sprintf("%s: %s -> %s", kind, raw, resolved)
		if matcher := strings.TrimSpace(entry.Matcher); matcher != "" {
			detail += " matcher=" + matcher
		}
		if entry.UnknownTool {
			detail += " unknown_tool=true"
		}
		out = append(out, detail)
	}
	return out
}

func checkTools(count int) Check {
	if count <= 0 {
		return Check{Name: "Tools", Status: StatusFail, Summary: "No tools are registered."}
	}
	return Check{Name: "Tools", Status: StatusOK, Summary: "Tool registry is populated.", Details: []string{fmt.Sprintf("Registered tools: %d", count)}}
}

func checkMCPValidation(summary localstatus.MCPValidationStatus) Check {
	details := []string{
		fmt.Sprintf("Total entries: %d", summary.TotalConfigured),
		fmt.Sprintf("Valid entries: %d", summary.ValidCount),
		fmt.Sprintf("Invalid entries: %d", summary.InvalidCount),
	}
	for _, issue := range summary.InvalidServers {
		name := strings.TrimSpace(issue.Name)
		if name == "" {
			name = "<unnamed>"
		}
		details = append(details, fmt.Sprintf("Invalid server: %s (%s)", name, issue.Reason))
	}
	data := map[string]any{
		"total_configured": summary.TotalConfigured,
		"valid_count":      summary.ValidCount,
		"invalid_count":    summary.InvalidCount,
		"invalid_servers":  summary.InvalidServers,
	}
	if summary.InvalidCount > 0 {
		return Check{
			Name:    "MCP validation",
			Status:  StatusWarn,
			Summary: invalidEntriesSummary(summary.InvalidCount, "MCP server", summary.ValidCount),
			Details: details,
			Hint:    "Inspect `codog status --json` mcp_validation.invalid_servers and fix each rejected mcp_servers entry.",
			Data:    data,
		}
	}
	return Check{
		Name:    "MCP validation",
		Status:  StatusOK,
		Summary: fmt.Sprintf("%d MCP server entries validated.", summary.ValidCount),
		Details: details,
		Data:    data,
	}
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

func checkHookValidation(summary localstatus.HookValidationStatus) Check {
	details := []string{
		fmt.Sprintf("Valid entries: %d", summary.ValidCount),
		fmt.Sprintf("Invalid entries: %d", summary.InvalidCount),
	}
	for _, issue := range summary.InvalidHooks {
		event := strings.TrimSpace(issue.Event)
		if event == "" {
			event = "<unknown>"
		}
		details = append(details, fmt.Sprintf("Invalid hook: %s (%s)", event, issue.Reason))
	}
	data := map[string]any{
		"valid_count":   summary.ValidCount,
		"invalid_count": summary.InvalidCount,
		"invalid_hooks": summary.InvalidHooks,
	}
	if summary.InvalidCount > 0 {
		return Check{
			Name:    "Hook validation",
			Status:  StatusWarn,
			Summary: invalidEntriesSummary(summary.InvalidCount, "hook", summary.ValidCount),
			Details: details,
			Hint:    "Inspect `codog status --json` hook_validation.invalid_hooks and fix each rejected hooks entry.",
			Data:    data,
		}
	}
	return Check{
		Name:    "Hook validation",
		Status:  StatusOK,
		Summary: fmt.Sprintf("%d hook entries validated.", summary.ValidCount),
		Details: details,
		Data:    data,
	}
}

func invalidEntriesSummary(invalid int, subject string, valid int) string {
	invalidNoun := subject + " entries are"
	if invalid == 1 {
		invalidNoun = subject + " entry is"
	}
	validNoun := "entries"
	if valid == 1 {
		validNoun = "entry"
	}
	return fmt.Sprintf("%d %s invalid; %d valid %s remain loaded.", invalid, invalidNoun, valid, validNoun)
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
	branch := ""
	if rawBranch, err := runGit(workspace, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = strings.TrimSpace(rawBranch)
		details = append(details, "Branch: "+branch)
	}
	if freshness, err := gitops.CheckBranchFreshness(workspace, branch, "main"); err == nil {
		details = append(details,
			"Base: "+freshness.Base,
			fmt.Sprintf("Ahead: %d", freshness.Ahead),
			fmt.Sprintf("Behind: %d", freshness.Behind),
			"Freshness: "+freshness.Status,
		)
		for _, subject := range freshness.MissingFixes {
			details = append(details, "Missing: "+subject)
		}
		if !freshness.Fresh {
			return Check{
				Name:    "Git",
				Status:  StatusWarn,
				Summary: "Current branch is behind or diverged from base.",
				Details: details,
				Hint:    "Review `codog branch freshness` and update the branch before risky edits or PR work.",
			}
		}
	}
	return Check{Name: "Git", Status: StatusOK, Summary: "git worktree is available.", Details: details}
}

func checkSandbox(opts Options) Check {
	if opts.SandboxRuntime != nil {
		return checkSandboxRuntime(*opts.SandboxRuntime)
	}
	details := []string{}
	if opts.SandboxDefault != "" {
		details = append(details, "Default: "+opts.SandboxDefault)
	}
	if len(opts.SandboxStrategies) > 0 {
		details = append(details, "Strategies: "+strings.Join(opts.SandboxStrategies, ", "))
	}
	details = append(details, fmt.Sprintf("In container: %t", opts.SandboxInContainer))
	if opts.SandboxFallback != "" {
		details = append(details, "Fallback: "+opts.SandboxFallback)
	}
	if opts.SandboxDefault == "" {
		if opts.SandboxOK {
			return Check{Name: "Sandbox", Status: StatusOK, Summary: "Sandbox support is available.", Details: details}
		}
		return Check{Name: "Sandbox", Status: StatusWarn, Summary: "No platform sandbox strategy was detected.", Details: details, Hint: "Set future.sandbox_strategy to a supported strategy when isolation is required."}
	}
	status := StatusOK
	summary := "Sandbox strategy is available."
	if !opts.SandboxOK {
		status = StatusWarn
		summary = "Configured platform sandbox strategy is not available."
	}
	return Check{Name: "Sandbox", Status: status, Summary: summary, Details: details}
}

func checkSandboxRuntime(status sandbox.SandboxExecutionStatus) Check {
	details := []string{
		fmt.Sprintf("Enabled: %t", status.Enabled),
		fmt.Sprintf("Active: %t", status.Active),
		fmt.Sprintf("Supported: %t", status.Supported),
		"Strategy: " + emptyDoctorValue(status.Strategy),
		fmt.Sprintf("Namespace supported: %t", status.NamespaceSupported),
		fmt.Sprintf("Namespace active: %t", status.NamespaceActive),
		fmt.Sprintf("Network supported: %t", status.NetworkSupported),
		fmt.Sprintf("Network active: %t", status.NetworkActive),
		"Filesystem mode: " + emptyDoctorValue(status.FilesystemMode),
		fmt.Sprintf("Filesystem active: %t", status.FilesystemActive),
		"Allowed mounts: " + joinedDoctorValues(status.AllowedMounts),
		fmt.Sprintf("In container: %t", status.InContainer),
	}
	if status.FallbackReason != "" {
		details = append(details, "Fallback: "+status.FallbackReason)
	}
	for _, gap := range status.CapabilityGaps {
		details = append(details, fmt.Sprintf("Capability gap: %s: %s", gap.Capability, gap.Reason))
	}
	if len(status.ContainerMarkers) != 0 {
		details = append(details, "Container markers: "+strings.Join(status.ContainerMarkers, ", "))
	}
	check := Check{
		Name:    "Sandbox",
		Status:  StatusOK,
		Summary: "Sandbox is not requested for this session.",
		Details: details,
		Data: map[string]any{
			"enabled":             status.Enabled,
			"active":              status.Active,
			"supported":           status.Supported,
			"strategy":            status.Strategy,
			"namespace_supported": status.NamespaceSupported,
			"namespace_active":    status.NamespaceActive,
			"network_supported":   status.NetworkSupported,
			"network_active":      status.NetworkActive,
			"filesystem_mode":     status.FilesystemMode,
			"filesystem_active":   status.FilesystemActive,
			"allowed_mounts":      jsonDoctorStringSlice(status.AllowedMounts),
			"in_container":        status.InContainer,
			"container_markers":   jsonDoctorStringSlice(status.ContainerMarkers),
			"fallback_reason":     status.FallbackReason,
			"capability_gaps":     status.CapabilityGaps,
		},
	}
	switch {
	case !status.Enabled:
		return check
	case status.Active:
		check.Summary = "Sandbox protections are active."
		return check
	case status.Supported || status.FilesystemActive:
		check.Status = StatusWarn
		check.Summary = "Sandbox was requested but is running in a degraded state."
		check.Hint = "Review `codog sandbox` for the active components and fallback reason."
		return check
	default:
		check.Status = StatusWarn
		check.Summary = "Sandbox was requested but is not currently active."
		check.Hint = "Install or enable a supported sandbox strategy, or disable sandbox when isolation is not required."
		return check
	}
}

func emptyDoctorValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<none>"
	}
	return value
}

func defaultDoctorValue(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func joinedDoctorValues(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

func jsonDoctorStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
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
