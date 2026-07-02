// Package status builds and renders the structured runtime status report used
// by `codog status`.
package status

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/providerdiag"
)

// Options contains the runtime inputs needed to build a status snapshot.
type Options struct {
	Version                     string
	FormatSource                string
	FormatRaw                   string
	FormatOverridden            bool
	ConfigLoadError             string
	ConfigLoadErrorKind         string
	Workspace                   string
	ConfigHome                  string
	Model                       string
	ModelEnvVar                 string
	RuntimeProvider             string
	RuntimeProviderSource       string
	FastMode                    bool
	BaseURL                     string
	PermissionMode              string
	PermissionModeRaw           string
	PermissionModeSource        string
	PermissionModeEnvVar        string
	PermissionRules             config.PermissionRules
	MaxTokens                   int
	MaxTurns                    int
	AutoCompactMessages         int
	APIKey                      string
	AuthToken                   string
	AuthConfigured              bool
	MCPServerCount              int
	UserPromptSubmitHookCount   int
	SessionStartHookCount       int
	SessionEndHookCount         int
	SetupHookCount              int
	PreHookCount                int
	PostHookCount               int
	PostFailureHookCount        int
	PermissionRequestHookCount  int
	PermissionDeniedHookCount   int
	StopHookCount               int
	StopFailureHookCount        int
	PreCompactHookCount         int
	PostCompactHookCount        int
	NotificationHookCount       int
	SubagentStartHookCount      int
	SubagentStopHookCount       int
	WorktreeCreateHookCount     int
	WorktreeRemoveHookCount     int
	CwdChangedHookCount         int
	TaskCreatedHookCount        int
	TaskCompletedHookCount      int
	InstructionsLoadedHookCount int
	FileChangedHookCount        int
	EnabledSkillCount           int
	MCPValidation               MCPValidationStatus
	HookValidation              HookValidationStatus
	PlanActive                  bool
	PlanText                    string
	PlanUpdatedAt               string
	MemoryFiles                 []MemoryFileStatus
	ToolNames                   []string
	AllowedToolSource           string
	AllowedToolEntries          []string
	ToolAliases                 map[string]string
	SessionID                   string
	SessionPath                 string
	SessionMessages             int
	SessionCount                int
	SessionCreatedAtMS          int64
	SessionUpdatedAtMS          int64
	SessionModifiedEpochMillis  int64
	SessionParentSessionID      string
	SessionBranchName           string
	SessionLifecycleKind        string
	SessionLifecycleSignal      string
	GitStatus                   string
	GitError                    string
	GitFreshness                *gitops.BranchFreshness
	LaneBoard                   *background.LaneBoard
	LaneBoardError              string
	SandboxOS                   string
	SandboxDefault              string
	SandboxStrategies           []string
	SandboxAvailable            bool
	Executable                  string
}

// Snapshot is the stable JSON payload returned by `codog status --json`.
type Snapshot struct {
	Kind                string               `json:"kind"`
	Action              string               `json:"action"`
	Status              string               `json:"status"`
	FormatSource        string               `json:"format_source"`
	FormatRaw           string               `json:"format_raw"`
	FormatOverridden    bool                 `json:"format_overridden"`
	ConfigLoadError     string               `json:"config_load_error,omitempty"`
	ConfigLoadErrorKind string               `json:"config_load_error_kind,omitempty"`
	Version             string               `json:"version"`
	Workspace           WorkspaceStatus      `json:"workspace"`
	Config              ConfigStatus         `json:"config"`
	Session             SessionStatus        `json:"session"`
	Plan                PlanStatus           `json:"plan"`
	Tools               ToolsStatus          `json:"tools"`
	AllowedTools        AllowedToolsStatus   `json:"allowed_tools"`
	Git                 GitStatus            `json:"git"`
	LaneBoard           LaneBoardStatus      `json:"lane_board"`
	Sandbox             SandboxStatus        `json:"sandbox"`
	Runtime             RuntimeStatus        `json:"runtime"`
	MCPValidation       MCPValidationStatus  `json:"mcp_validation"`
	HookValidation      HookValidationStatus `json:"hook_validation"`
}

// WorkspaceStatus describes the active workspace and loaded project memory.
type WorkspaceStatus struct {
	Path            string             `json:"path"`
	Name            string             `json:"name"`
	MemoryFileCount int                `json:"memory_file_count"`
	MemoryFiles     []MemoryFileStatus `json:"memory_files,omitempty"`
}

// MemoryFileStatus describes one instruction file loaded into context.
type MemoryFileStatus struct {
	Path           string `json:"path"`
	Name           string `json:"name"`
	Source         string `json:"source,omitempty"`
	Origin         string `json:"origin,omitempty"`
	Scope          string `json:"scope"`
	ScopePath      string `json:"scope_path,omitempty"`
	OutsideProject bool   `json:"outside_project,omitempty"`
	Chars          int    `json:"chars"`
	Contributes    bool   `json:"contributes"`
	Truncated      bool   `json:"truncated,omitempty"`
}

// ConfigStatus summarizes the effective runtime configuration.
type ConfigStatus struct {
	ConfigHome                  string                         `json:"config_home"`
	Model                       string                         `json:"model"`
	ModelEnvVar                 string                         `json:"model_env_var,omitempty"`
	FastMode                    bool                           `json:"fast_mode"`
	BaseURL                     string                         `json:"base_url"`
	RuntimeProvider             string                         `json:"runtime_provider,omitempty"`
	RuntimeProviderSource       string                         `json:"runtime_provider_source,omitempty"`
	ProviderEndpoint            modelrouting.BaseURLDiagnostic `json:"provider_endpoint"`
	PermissionMode              string                         `json:"permission_mode"`
	PermissionModeRaw           string                         `json:"permission_mode_raw"`
	PermissionModeSource        string                         `json:"permission_mode_source"`
	PermissionModeEnvVar        string                         `json:"permission_mode_env_var,omitempty"`
	PermissionRules             PermissionRulesStatus          `json:"permission_rules,omitempty"`
	MaxTokens                   int                            `json:"max_tokens"`
	MaxTurns                    int                            `json:"max_turns"`
	AutoCompactMessages         int                            `json:"auto_compact_messages"`
	Auth                        providerdiag.AuthDiagnostic    `json:"auth"`
	AuthConfigured              bool                           `json:"auth_configured"`
	MCPServerCount              int                            `json:"mcp_server_count"`
	UserPromptSubmitHookCount   int                            `json:"user_prompt_submit_hook_count"`
	SessionStartHookCount       int                            `json:"session_start_hook_count"`
	SessionEndHookCount         int                            `json:"session_end_hook_count"`
	SetupHookCount              int                            `json:"setup_hook_count"`
	PreHookCount                int                            `json:"pre_hook_count"`
	PostHookCount               int                            `json:"post_hook_count"`
	PostFailureHookCount        int                            `json:"post_tool_use_failure_hook_count"`
	PermissionRequestHookCount  int                            `json:"permission_request_hook_count"`
	PermissionDeniedHookCount   int                            `json:"permission_denied_hook_count"`
	StopHookCount               int                            `json:"stop_hook_count"`
	StopFailureHookCount        int                            `json:"stop_failure_hook_count"`
	PreCompactHookCount         int                            `json:"pre_compact_hook_count"`
	PostCompactHookCount        int                            `json:"post_compact_hook_count"`
	NotificationHookCount       int                            `json:"notification_hook_count"`
	SubagentStartHookCount      int                            `json:"subagent_start_hook_count"`
	SubagentStopHookCount       int                            `json:"subagent_stop_hook_count"`
	WorktreeCreateHookCount     int                            `json:"worktree_create_hook_count"`
	WorktreeRemoveHookCount     int                            `json:"worktree_remove_hook_count"`
	CwdChangedHookCount         int                            `json:"cwd_changed_hook_count"`
	TaskCreatedHookCount        int                            `json:"task_created_hook_count"`
	TaskCompletedHookCount      int                            `json:"task_completed_hook_count"`
	InstructionsLoadedHookCount int                            `json:"instructions_loaded_hook_count"`
	FileChangedHookCount        int                            `json:"file_changed_hook_count"`
	EnabledSkillCount           int                            `json:"enabled_skill_count"`
}

// PermissionRulesStatus exposes parsed permission rules for automation audits.
type PermissionRulesStatus struct {
	Allow        []PermissionRuleStatus `json:"allow,omitempty"`
	Deny         []PermissionRuleStatus `json:"deny,omitempty"`
	Ask          []PermissionRuleStatus `json:"ask,omitempty"`
	DeniedTools  []PermissionRuleStatus `json:"denied_tools,omitempty"`
	UnknownCount int                    `json:"unknown_count,omitempty"`
}

// PermissionRuleStatus describes one configured permission rule.
type PermissionRuleStatus struct {
	Raw              string `json:"raw"`
	Tool             string `json:"tool,omitempty"`
	ResolvedToolName string `json:"resolved_tool_name,omitempty"`
	Matcher          string `json:"matcher,omitempty"`
	UnknownTool      bool   `json:"unknown_tool,omitempty"`
}

// SessionStatus summarizes the active session and saved session ledger.
type SessionStatus struct {
	Active              bool                    `json:"active"`
	ID                  string                  `json:"id,omitempty"`
	Path                string                  `json:"path,omitempty"`
	MessageCount        int                     `json:"message_count"`
	SavedCount          int                     `json:"saved_count"`
	CreatedAtMS         int64                   `json:"created_at_ms,omitempty"`
	UpdatedAtMS         int64                   `json:"updated_at_ms,omitempty"`
	ModifiedEpochMillis int64                   `json:"modified_epoch_millis,omitempty"`
	ParentSessionID     string                  `json:"parent_session_id,omitempty"`
	BranchName          string                  `json:"branch_name,omitempty"`
	Lifecycle           *SessionLifecycleStatus `json:"lifecycle,omitempty"`
}

// SessionLifecycleStatus describes whether a saved session is complete or abandoned.
type SessionLifecycleStatus struct {
	Kind      string `json:"kind"`
	Signal    string `json:"signal"`
	Saved     bool   `json:"saved"`
	Abandoned bool   `json:"abandoned"`
}

// PlanStatus reports whether plan mode is active for the workspace.
type PlanStatus struct {
	Active    bool   `json:"active"`
	Text      string `json:"text,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ToolsStatus lists the registered tool names visible to the model runtime.
type ToolsStatus struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

// AllowedToolsStatus describes the active allowed-tools restriction contract.
type AllowedToolsStatus struct {
	Source     string            `json:"source"`
	Restricted bool              `json:"restricted"`
	Entries    []string          `json:"entries,omitempty"`
	Available  []string          `json:"available"`
	Aliases    map[string]string `json:"aliases"`
}

// GitStatus summarizes local git state for the workspace.
type GitStatus struct {
	Available bool                    `json:"available"`
	Error     string                  `json:"error,omitempty"`
	Branch    string                  `json:"branch,omitempty"`
	Clean     bool                    `json:"clean"`
	Staged    int                     `json:"staged"`
	Unstaged  int                     `json:"unstaged"`
	Untracked int                     `json:"untracked"`
	Conflicts int                     `json:"conflicts"`
	Freshness *gitops.BranchFreshness `json:"freshness,omitempty"`
	Raw       string                  `json:"raw,omitempty"`
}

// LaneBoardStatus summarizes background task lanes.
type LaneBoardStatus struct {
	StatusJSONSupported bool                        `json:"status_json_supported"`
	FreshnessStates     []background.LaneFreshness  `json:"freshness_states"`
	Available           bool                        `json:"available"`
	Error               string                      `json:"error,omitempty"`
	ActiveCount         int                         `json:"active_count"`
	BlockedCount        int                         `json:"blocked_count"`
	FinishedCount       int                         `json:"finished_count"`
	GeneratedAt         string                      `json:"generated_at,omitempty"`
	Active              []background.LaneBoardEntry `json:"active,omitempty"`
	Blocked             []background.LaneBoardEntry `json:"blocked,omitempty"`
	Finished            []background.LaneBoardEntry `json:"finished,omitempty"`
}

// SandboxStatus reports sandbox detection and configured execution strategy.
type SandboxStatus struct {
	OS         string   `json:"os"`
	Default    string   `json:"default,omitempty"`
	Strategies []string `json:"strategies,omitempty"`
	Available  bool     `json:"available"`
}

// RuntimeStatus reports the Codog process and Go runtime details.
type RuntimeStatus struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GoVersion  string `json:"go_version"`
	Executable string `json:"executable,omitempty"`
}

// MCPValidationStatus summarizes static MCP server configuration validation.
type MCPValidationStatus struct {
	TotalConfigured int               `json:"total_configured"`
	ValidCount      int               `json:"valid_count"`
	RequiredCount   int               `json:"required_count"`
	OptionalCount   int               `json:"optional_count"`
	InvalidCount    int               `json:"invalid_count"`
	InvalidServers  []ValidationIssue `json:"invalid_servers,omitempty"`
}

// HookValidationStatus summarizes static hook configuration validation.
type HookValidationStatus struct {
	ValidCount   int               `json:"valid_count"`
	InvalidCount int               `json:"invalid_count"`
	InvalidHooks []ValidationIssue `json:"invalid_hooks,omitempty"`
}

// ValidationIssue describes one invalid MCP server or hook entry.
type ValidationIssue struct {
	Name       string `json:"name,omitempty"`
	Event      string `json:"event,omitempty"`
	Index      *int   `json:"index,omitempty"`
	HookIndex  *int   `json:"hook_index,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ErrorField string `json:"error_field,omitempty"`
	Reason     string `json:"reason"`
	Command    string `json:"command,omitempty"`
	Matcher    string `json:"matcher,omitempty"`
	Valid      bool   `json:"valid"`
}

// Build converts runtime options into a status snapshot.
func Build(opts Options) Snapshot {
	git := parseGitStatus(opts.GitStatus, opts.GitError)
	laneBoard := buildLaneBoardStatus(opts.LaneBoard, opts.LaneBoardError)
	endpoint := buildProviderEndpointStatus(opts)
	auth := buildProviderAuthStatus(opts)
	if opts.GitFreshness != nil {
		freshness := *opts.GitFreshness
		git.Freshness = &freshness
	}
	status := "ok"
	if !git.Available {
		status = "degraded"
	} else if strings.TrimSpace(opts.ConfigLoadError) != "" {
		status = "degraded"
	} else if !endpoint.Valid {
		status = "degraded"
	} else if opts.MCPValidation.InvalidCount > 0 || opts.HookValidation.InvalidCount > 0 {
		status = "degraded"
	} else if strings.TrimSpace(auth.Warning) != "" {
		status = "warn"
	} else if git.Freshness != nil {
		if !git.Freshness.Fresh {
			status = "warn"
		}
	}
	return Snapshot{
		Kind:                "status",
		Action:              "show",
		Status:              status,
		FormatSource:        defaultString(strings.TrimSpace(opts.FormatSource), "default"),
		FormatRaw:           strings.TrimSpace(opts.FormatRaw),
		FormatOverridden:    opts.FormatOverridden,
		ConfigLoadError:     strings.TrimSpace(opts.ConfigLoadError),
		ConfigLoadErrorKind: strings.TrimSpace(opts.ConfigLoadErrorKind),
		Version:             opts.Version,
		Workspace: WorkspaceStatus{
			Path:            opts.Workspace,
			Name:            filepath.Base(opts.Workspace),
			MemoryFileCount: len(opts.MemoryFiles),
			MemoryFiles:     append([]MemoryFileStatus(nil), opts.MemoryFiles...),
		},
		Config: ConfigStatus{
			ConfigHome:                  opts.ConfigHome,
			Model:                       opts.Model,
			ModelEnvVar:                 strings.TrimSpace(opts.ModelEnvVar),
			RuntimeProvider:             endpoint.Provider,
			RuntimeProviderSource:       strings.TrimSpace(opts.RuntimeProviderSource),
			FastMode:                    opts.FastMode,
			BaseURL:                     opts.BaseURL,
			ProviderEndpoint:            endpoint,
			PermissionMode:              opts.PermissionMode,
			PermissionModeRaw:           defaultString(opts.PermissionModeRaw, opts.PermissionMode),
			PermissionModeSource:        defaultString(opts.PermissionModeSource, "unknown"),
			PermissionModeEnvVar:        strings.TrimSpace(opts.PermissionModeEnvVar),
			PermissionRules:             BuildPermissionRulesStatus(opts.PermissionRules, opts.ToolNames, opts.ToolAliases),
			MaxTokens:                   opts.MaxTokens,
			MaxTurns:                    opts.MaxTurns,
			AutoCompactMessages:         opts.AutoCompactMessages,
			Auth:                        auth,
			AuthConfigured:              opts.AuthConfigured,
			MCPServerCount:              opts.MCPServerCount,
			UserPromptSubmitHookCount:   opts.UserPromptSubmitHookCount,
			SessionStartHookCount:       opts.SessionStartHookCount,
			SessionEndHookCount:         opts.SessionEndHookCount,
			SetupHookCount:              opts.SetupHookCount,
			PreHookCount:                opts.PreHookCount,
			PostHookCount:               opts.PostHookCount,
			PostFailureHookCount:        opts.PostFailureHookCount,
			PermissionRequestHookCount:  opts.PermissionRequestHookCount,
			PermissionDeniedHookCount:   opts.PermissionDeniedHookCount,
			StopHookCount:               opts.StopHookCount,
			StopFailureHookCount:        opts.StopFailureHookCount,
			PreCompactHookCount:         opts.PreCompactHookCount,
			PostCompactHookCount:        opts.PostCompactHookCount,
			NotificationHookCount:       opts.NotificationHookCount,
			SubagentStartHookCount:      opts.SubagentStartHookCount,
			SubagentStopHookCount:       opts.SubagentStopHookCount,
			WorktreeCreateHookCount:     opts.WorktreeCreateHookCount,
			WorktreeRemoveHookCount:     opts.WorktreeRemoveHookCount,
			CwdChangedHookCount:         opts.CwdChangedHookCount,
			TaskCreatedHookCount:        opts.TaskCreatedHookCount,
			TaskCompletedHookCount:      opts.TaskCompletedHookCount,
			InstructionsLoadedHookCount: opts.InstructionsLoadedHookCount,
			FileChangedHookCount:        opts.FileChangedHookCount,
			EnabledSkillCount:           opts.EnabledSkillCount,
		},
		Session: SessionStatus{
			Active:              opts.SessionID != "",
			ID:                  opts.SessionID,
			Path:                opts.SessionPath,
			MessageCount:        opts.SessionMessages,
			SavedCount:          opts.SessionCount,
			CreatedAtMS:         opts.SessionCreatedAtMS,
			UpdatedAtMS:         opts.SessionUpdatedAtMS,
			ModifiedEpochMillis: opts.SessionModifiedEpochMillis,
			ParentSessionID:     strings.TrimSpace(opts.SessionParentSessionID),
			BranchName:          strings.TrimSpace(opts.SessionBranchName),
			Lifecycle:           buildSessionLifecycleStatus(opts),
		},
		Plan: PlanStatus{
			Active:    opts.PlanActive,
			Text:      strings.TrimSpace(opts.PlanText),
			UpdatedAt: opts.PlanUpdatedAt,
		},
		Tools: ToolsStatus{
			Count: len(opts.ToolNames),
			Names: append([]string(nil), opts.ToolNames...),
		},
		AllowedTools: buildAllowedToolsStatus(opts),
		Git:          git,
		LaneBoard:    laneBoard,
		Sandbox: SandboxStatus{
			OS:         opts.SandboxOS,
			Default:    opts.SandboxDefault,
			Strategies: append([]string(nil), opts.SandboxStrategies...),
			Available:  opts.SandboxAvailable,
		},
		Runtime: RuntimeStatus{
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			GoVersion:  runtime.Version(),
			Executable: opts.Executable,
		},
		MCPValidation:  opts.MCPValidation,
		HookValidation: opts.HookValidation,
	}
}

func buildProviderEndpointStatus(opts Options) modelrouting.BaseURLDiagnostic {
	provider := strings.TrimSpace(opts.RuntimeProvider)
	if provider == "" {
		provider = modelrouting.ProviderForModel(opts.Model)
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = defaultProviderBaseURL(provider)
	}
	envName := activeBaseURLEnv(provider, opts.RuntimeProviderSource)
	source := "configured"
	switch {
	case strings.EqualFold(strings.TrimSpace(opts.RuntimeProviderSource), "OLLAMA_HOST"):
		source = "env"
	case envName != "" && strings.TrimSpace(os.Getenv(envName)) != "":
		source = "env"
	case strings.TrimSpace(os.Getenv("CODOG_BASE_URL")) != "":
		envName = "CODOG_BASE_URL"
		source = "env"
	case sameBaseURL(baseURL, defaultProviderBaseURL(provider)):
		source = "default"
	}
	return modelrouting.DiagnoseBaseURL(provider, envName, source, baseURL)
}

func buildProviderAuthStatus(opts Options) providerdiag.AuthDiagnostic {
	return providerdiag.AnalyzeAuth(providerdiag.AuthOptions{
		Model:                 opts.Model,
		RuntimeProvider:       opts.RuntimeProvider,
		RuntimeProviderSource: opts.RuntimeProviderSource,
		BaseURL:               opts.BaseURL,
		APIKey:                opts.APIKey,
		AuthToken:             opts.AuthToken,
	})
}

func activeBaseURLEnv(provider string, runtimeProviderSource string) string {
	if strings.EqualFold(strings.TrimSpace(runtimeProviderSource), "OLLAMA_HOST") {
		return "OLLAMA_HOST"
	}
	switch provider {
	case modelrouting.ProviderOpenAI:
		return "OPENAI_BASE_URL"
	case modelrouting.ProviderXAI:
		return "XAI_BASE_URL"
	case modelrouting.ProviderDashScope:
		return "DASHSCOPE_BASE_URL"
	default:
		return "ANTHROPIC_BASE_URL"
	}
}

func defaultProviderBaseURL(provider string) string {
	switch provider {
	case modelrouting.ProviderOpenAI:
		return modelrouting.DefaultOpenAIBaseURL
	case modelrouting.ProviderXAI:
		return modelrouting.DefaultXAIBaseURL
	case modelrouting.ProviderDashScope:
		return modelrouting.DefaultDashScopeBaseURL
	default:
		return config.DefaultBaseURL
	}
}

func sameBaseURL(left string, right string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(left), "/"), strings.TrimRight(strings.TrimSpace(right), "/"))
}

func buildSessionLifecycleStatus(opts Options) *SessionLifecycleStatus {
	if strings.TrimSpace(opts.SessionID) == "" {
		return nil
	}
	kind := strings.TrimSpace(opts.SessionLifecycleKind)
	signal := strings.TrimSpace(opts.SessionLifecycleSignal)
	if kind == "" {
		kind = "saved_only"
		signal = "saved only"
		if opts.SessionMessages == 0 {
			kind = "empty"
			signal = "empty saved session"
		}
	}
	if signal == "" {
		signal = strings.ReplaceAll(kind, "_", " ")
	}
	return &SessionLifecycleStatus{
		Kind:   kind,
		Signal: signal,
		Saved:  true,
	}
}

func buildAllowedToolsStatus(opts Options) AllowedToolsStatus {
	entries := append([]string(nil), opts.AllowedToolEntries...)
	available := append([]string(nil), opts.ToolNames...)
	aliases := map[string]string{}
	for alias, canonical := range opts.ToolAliases {
		aliases[alias] = canonical
	}
	source := strings.TrimSpace(opts.AllowedToolSource)
	if source == "" {
		source = "default"
		if len(entries) != 0 {
			source = "configured"
		}
	}
	return AllowedToolsStatus{
		Source:     source,
		Restricted: len(entries) != 0,
		Entries:    entries,
		Available:  available,
		Aliases:    aliases,
	}
}

// BuildPermissionRulesStatus parses configured permission rules against the
// active tool registry so status and doctor use the same audit semantics.
func BuildPermissionRulesStatus(rules config.PermissionRules, toolNames []string, toolAliases map[string]string) PermissionRulesStatus {
	opts := Options{
		PermissionRules: rules,
		ToolNames:       append([]string(nil), toolNames...),
		ToolAliases:     copyStringMap(toolAliases),
	}
	return buildPermissionRulesStatus(opts)
}

func buildPermissionRulesStatus(opts Options) PermissionRulesStatus {
	report := PermissionRulesStatus{
		Allow:       buildPermissionRuleEntries(opts.PermissionRules.Allow, opts),
		Deny:        buildPermissionRuleEntries(opts.PermissionRules.Deny, opts),
		Ask:         buildPermissionRuleEntries(opts.PermissionRules.Ask, opts),
		DeniedTools: buildPermissionRuleEntries(opts.PermissionRules.DeniedTools, opts),
	}
	report.UnknownCount = permissionRuleUnknownCount(report.Allow) +
		permissionRuleUnknownCount(report.Deny) +
		permissionRuleUnknownCount(report.Ask) +
		permissionRuleUnknownCount(report.DeniedTools)
	return report
}

func copyStringMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func buildPermissionRuleEntries(rules []string, opts Options) []PermissionRuleStatus {
	if len(rules) == 0 {
		return nil
	}
	available := map[string]string{}
	for _, name := range opts.ToolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		available[name] = name
		available[strings.ToLower(name)] = name
	}
	aliases := map[string]string{}
	for alias, canonical := range opts.ToolAliases {
		alias = strings.TrimSpace(alias)
		canonical = strings.TrimSpace(canonical)
		if alias == "" || canonical == "" {
			continue
		}
		aliases[alias] = canonical
		aliases[strings.ToLower(alias)] = canonical
	}
	out := make([]PermissionRuleStatus, 0, len(rules))
	for _, raw := range rules {
		entry := buildPermissionRuleEntry(raw, available, aliases)
		if strings.TrimSpace(entry.Raw) != "" {
			out = append(out, entry)
		}
	}
	return out
}

func buildPermissionRuleEntry(raw string, available map[string]string, aliases map[string]string) PermissionRuleStatus {
	entry := PermissionRuleStatus{Raw: strings.TrimSpace(raw)}
	tool, matcher := parsePermissionRuleStatus(entry.Raw)
	entry.Tool = tool
	entry.Matcher = matcher
	if tool == "" || tool == "*" {
		entry.ResolvedToolName = tool
		return entry
	}
	if strings.HasPrefix(strings.ToLower(tool), "mcp__") && strings.Contains(tool, "__") {
		entry.ResolvedToolName = tool
		return entry
	}
	if resolved := resolvePermissionRuleTool(tool, available, aliases); resolved != "" {
		entry.ResolvedToolName = resolved
		return entry
	}
	entry.UnknownTool = true
	return entry
}

func resolvePermissionRuleTool(tool string, available map[string]string, aliases map[string]string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return ""
	}
	if resolved := available[tool]; resolved != "" {
		return resolved
	}
	if resolved := aliases[tool]; resolved != "" {
		return resolved
	}
	lower := strings.ToLower(tool)
	if resolved := available[lower]; resolved != "" {
		return resolved
	}
	return aliases[lower]
}

func parsePermissionRuleStatus(rule string) (string, string) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return "", ""
	}
	if open := strings.Index(rule, "("); open > 0 && strings.HasSuffix(rule, ")") {
		return strings.TrimSpace(rule[:open]), normalizePermissionRuleMatcher(rule[open+1 : len(rule)-1])
	}
	if tool, matcher, ok := strings.Cut(rule, ":"); ok {
		return strings.TrimSpace(tool), normalizePermissionRuleMatcher(matcher)
	}
	return rule, ""
}

func normalizePermissionRuleMatcher(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "*")
	value = strings.TrimSuffix(value, ":")
	return strings.TrimSpace(value)
}

func permissionRuleUnknownCount(entries []PermissionRuleStatus) int {
	count := 0
	for _, entry := range entries {
		if entry.UnknownTool {
			count++
		}
	}
	return count
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func RenderText(w io.Writer, snapshot Snapshot) {
	fmt.Fprintln(w, "Status")
	fmt.Fprintf(w, "  Version          %s\n", snapshot.Version)
	fmt.Fprintf(w, "  Status           %s\n", snapshot.Status)
	if snapshot.ConfigLoadError != "" {
		fmt.Fprintf(w, "  Config load      degraded: %s\n", snapshot.ConfigLoadError)
	}
	fmt.Fprintf(w, "  Workspace        %s\n", snapshot.Workspace.Path)
	fmt.Fprintf(w, "  Memory files     %d\n", snapshot.Workspace.MemoryFileCount)
	fmt.Fprintf(w, "  Model            %s\n", snapshot.Config.Model)
	fmt.Fprintf(w, "  Fast mode        %t\n", snapshot.Config.FastMode)
	fmt.Fprintf(w, "  Permission       %s\n", snapshot.Config.PermissionMode)
	if snapshot.Plan.Active {
		fmt.Fprintln(w, "  Plan             active")
	} else {
		fmt.Fprintln(w, "  Plan             inactive")
	}
	fmt.Fprintf(w, "  Auth configured  %t\n", snapshot.Config.AuthConfigured)
	if snapshot.Session.Active {
		fmt.Fprintf(w, "  Session          %s (%d messages)\n", snapshot.Session.ID, snapshot.Session.MessageCount)
		if snapshot.Session.Lifecycle != nil && snapshot.Session.Lifecycle.Signal != "" {
			fmt.Fprintf(w, "  Session state    %s\n", snapshot.Session.Lifecycle.Signal)
		}
		if snapshot.Session.ParentSessionID != "" {
			fmt.Fprintf(w, "  Session parent   %s\n", snapshot.Session.ParentSessionID)
		}
		if snapshot.Session.BranchName != "" {
			fmt.Fprintf(w, "  Session branch   %s\n", snapshot.Session.BranchName)
		}
	} else {
		fmt.Fprintf(w, "  Session          none (%d saved)\n", snapshot.Session.SavedCount)
	}
	if snapshot.Git.Available {
		fmt.Fprintf(w, "  Git              branch=%s clean=%t staged=%d unstaged=%d untracked=%d conflicts=%d\n",
			snapshot.Git.Branch,
			snapshot.Git.Clean,
			snapshot.Git.Staged,
			snapshot.Git.Unstaged,
			snapshot.Git.Untracked,
			snapshot.Git.Conflicts,
		)
		if snapshot.Git.Freshness != nil {
			freshness := snapshot.Git.Freshness
			upstream := ""
			if freshness.HasUpstream {
				upstream = " upstream=" + freshness.Upstream
			}
			fmt.Fprintf(w, "  Git freshness    status=%s base=%s%s ahead=%d behind=%d\n",
				freshness.Status,
				freshness.Base,
				upstream,
				freshness.Ahead,
				freshness.Behind,
			)
		}
	} else {
		fmt.Fprintf(w, "  Git              unavailable: %s\n", snapshot.Git.Error)
	}
	if snapshot.MCPValidation.TotalConfigured > 0 || snapshot.MCPValidation.InvalidCount > 0 {
		fmt.Fprintf(w, "  MCP validation   valid=%d invalid=%d required=%d optional=%d\n",
			snapshot.MCPValidation.ValidCount,
			snapshot.MCPValidation.InvalidCount,
			snapshot.MCPValidation.RequiredCount,
			snapshot.MCPValidation.OptionalCount,
		)
	}
	if snapshot.HookValidation.ValidCount > 0 || snapshot.HookValidation.InvalidCount > 0 {
		fmt.Fprintf(w, "  Hook validation  valid=%d invalid=%d\n", snapshot.HookValidation.ValidCount, snapshot.HookValidation.InvalidCount)
	}
	fmt.Fprintf(w, "  Sandbox          available=%t default=%s\n", snapshot.Sandbox.Available, snapshot.Sandbox.Default)
	if snapshot.LaneBoard.Available {
		fmt.Fprintf(w, "  Task lanes       active=%d blocked=%d finished=%d\n",
			snapshot.LaneBoard.ActiveCount,
			snapshot.LaneBoard.BlockedCount,
			snapshot.LaneBoard.FinishedCount,
		)
	} else if snapshot.LaneBoard.Error != "" {
		fmt.Fprintf(w, "  Task lanes       unavailable: %s\n", snapshot.LaneBoard.Error)
	}
	fmt.Fprintf(w, "  Tools            %d\n", snapshot.Tools.Count)
}

func buildLaneBoardStatus(board *background.LaneBoard, errText string) LaneBoardStatus {
	status := LaneBoardStatus{
		StatusJSONSupported: true,
		FreshnessStates: []background.LaneFreshness{
			background.LaneFreshnessHealthy,
			background.LaneFreshnessStalled,
			background.LaneFreshnessTransportDead,
			background.LaneFreshnessUnknown,
		},
	}
	if strings.TrimSpace(errText) != "" {
		status.Error = strings.TrimSpace(errText)
		return status
	}
	if board == nil {
		return status
	}
	status.Available = true
	status.Active = append([]background.LaneBoardEntry(nil), board.Active...)
	status.Blocked = append([]background.LaneBoardEntry(nil), board.Blocked...)
	status.Finished = append([]background.LaneBoardEntry(nil), board.Finished...)
	status.ActiveCount = len(status.Active)
	status.BlockedCount = len(status.Blocked)
	status.FinishedCount = len(status.Finished)
	if !board.GeneratedAt.IsZero() {
		status.GeneratedAt = board.GeneratedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return status
}

func parseGitStatus(raw string, errText string) GitStatus {
	raw = strings.TrimSpace(raw)
	if strings.TrimSpace(errText) != "" {
		return GitStatus{Available: false, Error: strings.TrimSpace(errText), Raw: raw}
	}
	if raw == "" {
		return GitStatus{Available: true, Clean: true}
	}
	status := GitStatus{Available: true, Raw: raw}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "## ") {
			status.Branch = parseBranch(line)
			continue
		}
		if len(line) < 2 {
			continue
		}
		index := line[0]
		worktree := line[1]
		if index == '?' && worktree == '?' {
			status.Untracked++
			continue
		}
		if isConflict(index, worktree) {
			status.Conflicts++
			continue
		}
		if index != ' ' {
			status.Staged++
		}
		if worktree != ' ' {
			status.Unstaged++
		}
	}
	status.Clean = status.Staged == 0 && status.Unstaged == 0 && status.Untracked == 0 && status.Conflicts == 0
	return status
}

func parseBranch(line string) string {
	branch := strings.TrimSpace(strings.TrimPrefix(line, "## "))
	if branch == "" {
		return ""
	}
	if strings.HasPrefix(branch, "No commits yet on ") {
		return strings.TrimPrefix(branch, "No commits yet on ")
	}
	if before, _, ok := strings.Cut(branch, "..."); ok {
		return before
	}
	if before, _, ok := strings.Cut(branch, " "); ok {
		return before
	}
	return branch
}

func isConflict(index byte, worktree byte) bool {
	return index == 'U' || worktree == 'U' ||
		(index == 'A' && worktree == 'A') ||
		(index == 'D' && worktree == 'D')
}
