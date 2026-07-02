package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/envfile"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/signing"
)

const (
	DefaultBaseURL                  = modelrouting.DefaultAnthropicBaseURL
	DefaultModel                    = "claude-sonnet-4-5"
	sessionDefaultCleanupPeriodDays = 30
	apiKeyHelperTimeout             = 5 * time.Second
)

type HookConfig struct {
	PreToolUse                 []string      `json:"pre_tool_use,omitempty"`
	PostToolUse                []string      `json:"post_tool_use,omitempty"`
	PostToolUseFailure         []string      `json:"post_tool_use_failure,omitempty"`
	PermissionRequest          []string      `json:"permission_request,omitempty"`
	PermissionDenied           []string      `json:"permission_denied,omitempty"`
	UserPromptSubmit           []string      `json:"user_prompt_submit,omitempty"`
	SessionStart               []string      `json:"session_start,omitempty"`
	SessionEnd                 []string      `json:"session_end,omitempty"`
	Setup                      []string      `json:"setup,omitempty"`
	Stop                       []string      `json:"stop,omitempty"`
	StopFailure                []string      `json:"stop_failure,omitempty"`
	PreCompact                 []string      `json:"pre_compact,omitempty"`
	PostCompact                []string      `json:"post_compact,omitempty"`
	Notification               []string      `json:"notification,omitempty"`
	SubagentStart              []string      `json:"subagent_start,omitempty"`
	SubagentStop               []string      `json:"subagent_stop,omitempty"`
	WorktreeCreate             []string      `json:"worktree_create,omitempty"`
	WorktreeRemove             []string      `json:"worktree_remove,omitempty"`
	CwdChanged                 []string      `json:"cwd_changed,omitempty"`
	TaskCreated                []string      `json:"task_created,omitempty"`
	TaskCompleted              []string      `json:"task_completed,omitempty"`
	InstructionsLoaded         []string      `json:"instructions_loaded,omitempty"`
	FileChanged                []string      `json:"file_changed,omitempty"`
	PreToolUseCommands         []HookCommand `json:"-"`
	PostToolUseCommands        []HookCommand `json:"-"`
	PostToolUseFailureCommands []HookCommand `json:"-"`
	PermissionRequestCommands  []HookCommand `json:"-"`
	PermissionDeniedCommands   []HookCommand `json:"-"`
	UserPromptSubmitCommands   []HookCommand `json:"-"`
	SessionStartCommands       []HookCommand `json:"-"`
	SessionEndCommands         []HookCommand `json:"-"`
	SetupCommands              []HookCommand `json:"-"`
	StopCommands               []HookCommand `json:"-"`
	StopFailureCommands        []HookCommand `json:"-"`
	PreCompactCommands         []HookCommand `json:"-"`
	PostCompactCommands        []HookCommand `json:"-"`
	NotificationCommands       []HookCommand `json:"-"`
	SubagentStartCommands      []HookCommand `json:"-"`
	SubagentStopCommands       []HookCommand `json:"-"`
	WorktreeCreateCommands     []HookCommand `json:"-"`
	WorktreeRemoveCommands     []HookCommand `json:"-"`
	CwdChangedCommands         []HookCommand `json:"-"`
	TaskCreatedCommands        []HookCommand `json:"-"`
	TaskCompletedCommands      []HookCommand `json:"-"`
	InstructionsLoadedCommands []HookCommand `json:"-"`
	FileChangedCommands        []HookCommand `json:"-"`
}

type HookCommand struct {
	Matcher        string
	Type           string
	Command        string
	URL            string
	Prompt         string
	Model          string
	If             string
	Shell          string
	TimeoutSeconds float64
	Headers        map[string]string
	AllowedEnvVars []string
	StatusMessage  string
	Once           bool
	Async          bool
	AsyncRewake    bool
}

func (h *HookConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	pre, err := hookCommands(raw, "pre_tool_use", "PreToolUse")
	if err != nil {
		return err
	}
	post, err := hookCommands(raw, "post_tool_use", "PostToolUse")
	if err != nil {
		return err
	}
	postFailure, err := hookCommands(raw, "post_tool_use_failure", "PostToolUseFailure")
	if err != nil {
		return err
	}
	permissionRequest, err := hookCommands(raw, "permission_request", "PermissionRequest")
	if err != nil {
		return err
	}
	permissionDenied, err := hookCommands(raw, "permission_denied", "PermissionDenied")
	if err != nil {
		return err
	}
	userPromptSubmit, err := hookCommands(raw, "user_prompt_submit", "UserPromptSubmit")
	if err != nil {
		return err
	}
	sessionStart, err := hookCommands(raw, "session_start", "SessionStart")
	if err != nil {
		return err
	}
	sessionEnd, err := hookCommands(raw, "session_end", "SessionEnd")
	if err != nil {
		return err
	}
	setup, err := hookCommands(raw, "setup", "Setup")
	if err != nil {
		return err
	}
	stop, err := hookCommands(raw, "stop", "Stop")
	if err != nil {
		return err
	}
	stopFailure, err := hookCommands(raw, "stop_failure", "StopFailure")
	if err != nil {
		return err
	}
	preCompact, err := hookCommands(raw, "pre_compact", "PreCompact")
	if err != nil {
		return err
	}
	postCompact, err := hookCommands(raw, "post_compact", "PostCompact")
	if err != nil {
		return err
	}
	notification, err := hookCommands(raw, "notification", "Notification")
	if err != nil {
		return err
	}
	subagentStart, err := hookCommands(raw, "subagent_start", "SubagentStart")
	if err != nil {
		return err
	}
	subagentStop, err := hookCommands(raw, "subagent_stop", "SubagentStop")
	if err != nil {
		return err
	}
	worktreeCreate, err := hookCommands(raw, "worktree_create", "WorktreeCreate")
	if err != nil {
		return err
	}
	worktreeRemove, err := hookCommands(raw, "worktree_remove", "WorktreeRemove")
	if err != nil {
		return err
	}
	cwdChanged, err := hookCommands(raw, "cwd_changed", "CwdChanged")
	if err != nil {
		return err
	}
	taskCreated, err := hookCommands(raw, "task_created", "TaskCreated")
	if err != nil {
		return err
	}
	taskCompleted, err := hookCommands(raw, "task_completed", "TaskCompleted")
	if err != nil {
		return err
	}
	instructionsLoaded, err := hookCommands(raw, "instructions_loaded", "InstructionsLoaded")
	if err != nil {
		return err
	}
	fileChanged, err := hookCommands(raw, "file_changed", "FileChanged")
	if err != nil {
		return err
	}
	h.PreToolUseCommands = pre
	h.PostToolUseCommands = post
	h.PostToolUseFailureCommands = postFailure
	h.PermissionRequestCommands = permissionRequest
	h.PermissionDeniedCommands = permissionDenied
	h.UserPromptSubmitCommands = userPromptSubmit
	h.SessionStartCommands = sessionStart
	h.SessionEndCommands = sessionEnd
	h.SetupCommands = setup
	h.StopCommands = stop
	h.StopFailureCommands = stopFailure
	h.PreCompactCommands = preCompact
	h.PostCompactCommands = postCompact
	h.NotificationCommands = notification
	h.SubagentStartCommands = subagentStart
	h.SubagentStopCommands = subagentStop
	h.WorktreeCreateCommands = worktreeCreate
	h.WorktreeRemoveCommands = worktreeRemove
	h.CwdChangedCommands = cwdChanged
	h.TaskCreatedCommands = taskCreated
	h.TaskCompletedCommands = taskCompleted
	h.InstructionsLoadedCommands = instructionsLoaded
	h.FileChangedCommands = fileChanged
	h.PreToolUse = hookCommandStrings(pre)
	h.PostToolUse = hookCommandStrings(post)
	h.PostToolUseFailure = hookCommandStrings(postFailure)
	h.PermissionRequest = hookCommandStrings(permissionRequest)
	h.PermissionDenied = hookCommandStrings(permissionDenied)
	h.UserPromptSubmit = hookCommandStrings(userPromptSubmit)
	h.SessionStart = hookCommandStrings(sessionStart)
	h.SessionEnd = hookCommandStrings(sessionEnd)
	h.Setup = hookCommandStrings(setup)
	h.Stop = hookCommandStrings(stop)
	h.StopFailure = hookCommandStrings(stopFailure)
	h.PreCompact = hookCommandStrings(preCompact)
	h.PostCompact = hookCommandStrings(postCompact)
	h.Notification = hookCommandStrings(notification)
	h.SubagentStart = hookCommandStrings(subagentStart)
	h.SubagentStop = hookCommandStrings(subagentStop)
	h.WorktreeCreate = hookCommandStrings(worktreeCreate)
	h.WorktreeRemove = hookCommandStrings(worktreeRemove)
	h.CwdChanged = hookCommandStrings(cwdChanged)
	h.TaskCreated = hookCommandStrings(taskCreated)
	h.TaskCompleted = hookCommandStrings(taskCompleted)
	h.InstructionsLoaded = hookCommandStrings(instructionsLoaded)
	h.FileChanged = hookCommandStrings(fileChanged)
	return nil
}

type RateLimitConfig struct {
	MaxRetries       int `json:"max_retries,omitempty"`
	InitialBackoffMS int `json:"initial_backoff_ms,omitempty"`
	MaxBackoffMS     int `json:"max_backoff_ms,omitempty"`
}

type APITimeoutConfig struct {
	ConnectTimeoutSeconds int `json:"connectTimeout,omitempty"`
	RequestTimeoutSeconds int `json:"requestTimeout,omitempty"`
	MaxRetries            int `json:"maxRetries,omitempty"`
}

type ProviderFallbackConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

type PrivacyConfig struct {
	TelemetryEnabled     *bool `json:"telemetry_enabled,omitempty"`
	CrashReportsEnabled  *bool `json:"crash_reports_enabled,omitempty"`
	PromptHistoryEnabled *bool `json:"prompt_history_enabled,omitempty"`
}

type MCPServerConfig struct {
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               []string          `json:"env,omitempty"`
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	HeadersHelper     string            `json:"headers_helper,omitempty"`
	ToolCallTimeoutMS int               `json:"tool_call_timeout_ms,omitempty"`
	Required          bool              `json:"required,omitempty"`
}

func (m *MCPServerConfig) UnmarshalJSON(data []byte) error {
	type rawMCPServerConfig struct {
		Command              string            `json:"command,omitempty"`
		Args                 []string          `json:"args,omitempty"`
		Env                  json.RawMessage   `json:"env,omitempty"`
		URL                  string            `json:"url,omitempty"`
		Headers              map[string]string `json:"headers,omitempty"`
		HeadersHelper        string            `json:"headers_helper,omitempty"`
		HeadersHelperCamel   string            `json:"headersHelper,omitempty"`
		ToolCallTimeoutMS    int               `json:"tool_call_timeout_ms,omitempty"`
		ToolCallTimeoutCamel int               `json:"toolCallTimeoutMs,omitempty"`
		Required             bool              `json:"required,omitempty"`
	}
	var raw rawMCPServerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Command = raw.Command
	m.Args = raw.Args
	env, err := parseMCPEnv(raw.Env)
	if err != nil {
		return err
	}
	m.Env = env
	m.URL = raw.URL
	m.Headers = raw.Headers
	m.HeadersHelper = raw.HeadersHelper
	if strings.TrimSpace(raw.HeadersHelperCamel) != "" {
		m.HeadersHelper = raw.HeadersHelperCamel
	}
	m.ToolCallTimeoutMS = raw.ToolCallTimeoutMS
	if raw.ToolCallTimeoutCamel != 0 {
		m.ToolCallTimeoutMS = raw.ToolCallTimeoutCamel
	}
	m.Required = raw.Required
	return nil
}

func parseMCPEnv(data json.RawMessage) ([]string, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err == nil {
		return entries, nil
	}
	var object map[string]string
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("invalid mcp server env: %w", err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	entries = make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+object[key])
	}
	return entries, nil
}

type SandboxConfig struct {
	Enabled               *bool    `json:"enabled,omitempty"`
	NamespaceRestrictions *bool    `json:"namespace_restrictions,omitempty"`
	NetworkIsolation      *bool    `json:"network_isolation,omitempty"`
	FilesystemMode        string   `json:"filesystem_mode,omitempty"`
	AllowedMounts         []string `json:"allowed_mounts,omitempty"`
}

func (s *SandboxConfig) UnmarshalJSON(data []byte) error {
	type plain SandboxConfig
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	readBoolAlias := func(target **bool, keys ...string) error {
		for _, key := range keys {
			value, ok := raw[key]
			if !ok {
				continue
			}
			var parsed bool
			if err := json.Unmarshal(value, &parsed); err != nil {
				return fmt.Errorf("invalid sandbox.%s: %w", key, err)
			}
			*target = &parsed
		}
		return nil
	}
	readStringAlias := func(target *string, keys ...string) error {
		for _, key := range keys {
			value, ok := raw[key]
			if !ok {
				continue
			}
			var parsed string
			if err := json.Unmarshal(value, &parsed); err != nil {
				return fmt.Errorf("invalid sandbox.%s: %w", key, err)
			}
			*target = parsed
		}
		return nil
	}
	readStringArrayAlias := func(target *[]string, keys ...string) error {
		for _, key := range keys {
			value, ok := raw[key]
			if !ok {
				continue
			}
			var parsed []string
			if err := json.Unmarshal(value, &parsed); err != nil {
				return fmt.Errorf("invalid sandbox.%s: %w", key, err)
			}
			*target = parsed
		}
		return nil
	}
	if err := readBoolAlias(&parsed.Enabled, "enabled"); err != nil {
		return err
	}
	if err := readBoolAlias(&parsed.NamespaceRestrictions, "namespaceRestrictions", "namespace_restrictions"); err != nil {
		return err
	}
	if err := readBoolAlias(&parsed.NetworkIsolation, "networkIsolation", "network_isolation", "isolateNetwork", "isolate_network"); err != nil {
		return err
	}
	if err := readStringAlias(&parsed.FilesystemMode, "filesystemMode", "filesystem_mode"); err != nil {
		return err
	}
	if err := readStringArrayAlias(&parsed.AllowedMounts, "allowedMounts", "allowed_mounts"); err != nil {
		return err
	}
	*s = SandboxConfig(parsed)
	return nil
}

type FutureConfig struct {
	RemoteEnabled             bool              `json:"remote_enabled,omitempty"`
	RemoteAuthToken           string            `json:"remote_auth_token,omitempty"`
	RemoteLeaseSeconds        int               `json:"remote_lease_seconds,omitempty"`
	EnterprisePolicy          string            `json:"enterprise_policy,omitempty"`
	EnterprisePolicyPublicKey string            `json:"enterprise_policy_public_key,omitempty"`
	PluginMarketplaces        []string          `json:"plugin_marketplaces,omitempty"`
	PluginMarketplaceKeys     map[string]string `json:"plugin_marketplace_public_keys,omitempty"`
	SandboxStrategy           string            `json:"sandbox_strategy,omitempty"`
	Sandbox                   SandboxConfig     `json:"sandbox,omitempty"`
	UpdaterManifestURL        string            `json:"updater_manifest_url,omitempty"`
	EditorBridgeSocket        string            `json:"editor_bridge_socket,omitempty"`
	EditorBridgeToken         string            `json:"editor_bridge_token,omitempty"`
	BackgroundStatePath       string            `json:"background_state_path,omitempty"`
	ChromeDefaultEnabled      *bool             `json:"chrome_default_enabled,omitempty"`
	NotificationsEnabled      *bool             `json:"notifications_enabled,omitempty"`
	UltraReviewEnabled        *bool             `json:"ultrareview_enabled,omitempty"`
	SlackAppInstallCount      int               `json:"slack_app_install_count,omitempty"`
	StickerOrderCount         int               `json:"sticker_order_count,omitempty"`
	ExtraUsageVisitCount      int               `json:"extra_usage_visit_count,omitempty"`
	GuestPassReferralURL      string            `json:"guest_pass_referral_url,omitempty"`
	GuestPassVisitCount       int               `json:"guest_pass_visit_count,omitempty"`
}

type PermissionRules struct {
	DefaultMode           string   `json:"defaultMode,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	Allow                 []string `json:"allow,omitempty"`
	Deny                  []string `json:"deny,omitempty"`
	Ask                   []string `json:"ask,omitempty"`
	DeniedTools           []string `json:"denied_tools,omitempty"`
}

type ManagedPolicy struct {
	MaxPermissionMode string          `json:"max_permission_mode,omitempty"`
	PermissionRules   PermissionRules `json:"permission_rules,omitempty"`
	DeniedTools       []string        `json:"denied_tools,omitempty"`
	Signature         string          `json:"signature,omitempty"`
}

type StatusLineConfig struct {
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command,omitempty"`
	Padding *float64 `json:"padding,omitempty"`
}

type WorktreeConfig struct {
	SymlinkDirectories []string `json:"symlinkDirectories,omitempty"`
	SparsePaths        []string `json:"sparsePaths,omitempty"`
}

type Config struct {
	APIKey                     string                     `json:"api_key,omitempty"`
	APIKeyHelper               string                     `json:"apiKeyHelper,omitempty"`
	AuthToken                  string                     `json:"auth_token,omitempty"`
	OAuthProfile               string                     `json:"oauth_profile,omitempty"`
	ForceLoginMethod           string                     `json:"forceLoginMethod,omitempty"`
	ForceLoginOrgUUID          string                     `json:"forceLoginOrgUUID,omitempty"`
	BaseURL                    string                     `json:"base_url,omitempty"`
	Model                      string                     `json:"model,omitempty"`
	RuntimeProvider            string                     `json:"-"`
	RuntimeProviderSource      string                     `json:"-"`
	AdvisorModel               string                     `json:"advisor_model,omitempty"`
	SystemPrompt               string                     `json:"system_prompt,omitempty"`
	AppendSystemPrompt         string                     `json:"append_system_prompt,omitempty"`
	Language                   string                     `json:"language,omitempty"`
	Theme                      string                     `json:"theme,omitempty"`
	EditorMode                 string                     `json:"editorMode,omitempty"`
	DefaultShell               string                     `json:"defaultShell,omitempty"`
	ReasoningEffort            string                     `json:"reasoning_effort,omitempty"`
	FastMode                   *bool                      `json:"fast_mode,omitempty"`
	VoiceEnabled               *bool                      `json:"voice_enabled,omitempty"`
	VoiceCommand               string                     `json:"voice_command,omitempty"`
	SpeechCommand              string                     `json:"speech_command,omitempty"`
	MaxTokens                  int                        `json:"max_tokens,omitempty"`
	MaxTurns                   int                        `json:"max_turns,omitempty"`
	Temperature                *float64                   `json:"temperature,omitempty"`
	PermissionMode             string                     `json:"permission_mode,omitempty"`
	PlanMode                   bool                       `json:"-"`
	Privacy                    PrivacyConfig              `json:"privacy_settings,omitempty"`
	PermissionRules            PermissionRules            `json:"permission_rules,omitempty"`
	ConfigHome                 string                     `json:"config_home,omitempty"`
	AutoCompactMessages        int                        `json:"auto_compact_messages,omitempty"`
	CleanupPeriodDays          *int                       `json:"cleanupPeriodDays,omitempty"`
	RespectGitignore           *bool                      `json:"respectGitignore,omitempty"`
	DisableAllHooks            *bool                      `json:"disableAllHooks,omitempty"`
	AllowManagedHooksOnly      *bool                      `json:"allowManagedHooksOnly,omitempty"`
	AllowedHTTPHookURLs        *[]string                  `json:"allowedHttpHookUrls,omitempty"`
	HTTPHookAllowedEnvVars     *[]string                  `json:"httpHookAllowedEnvVars,omitempty"`
	StatusLine                 *StatusLineConfig          `json:"statusLine,omitempty"`
	Worktree                   WorktreeConfig             `json:"worktree,omitempty"`
	EnableAllProjectMCPServers *bool                      `json:"enableAllProjectMcpServers,omitempty"`
	EnabledMCPJSONServers      []string                   `json:"enabledMcpjsonServers,omitempty"`
	DisabledMCPJSONServers     []string                   `json:"disabledMcpjsonServers,omitempty"`
	RateLimit                  RateLimitConfig            `json:"rate_limit,omitempty"`
	APITimeout                 APITimeoutConfig           `json:"apiTimeout,omitempty"`
	ProviderFallbacks          ProviderFallbackConfig     `json:"providerFallbacks,omitempty"`
	Env                        map[string]string          `json:"env,omitempty"`
	TrustedRoots               []string                   `json:"trustedRoots,omitempty"`
	RAGBaseURL                 string                     `json:"rag_base_url,omitempty"`
	RAGTimeoutSeconds          int                        `json:"rag_timeout_seconds,omitempty"`
	RAGTopKMax                 int                        `json:"rag_top_k_max,omitempty"`
	AdditionalDirs             []string                   `json:"additional_dirs,omitempty"`
	EnabledSkills              []string                   `json:"enabled_skills,omitempty"`
	Hooks                      HookConfig                 `json:"hooks,omitempty"`
	MCPServers                 map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	Future                     FutureConfig               `json:"future,omitempty"`
}

type nestedMCPConfig struct {
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	var aliases struct {
		PermissionModeCamel string                     `json:"permissionMode,omitempty"`
		PermissionRules     PermissionRules            `json:"permissions,omitempty"`
		MCPServers          map[string]MCPServerConfig `json:"mcpServers,omitempty"`
		MCP                 nestedMCPConfig            `json:"mcp,omitempty"`
		Sandbox             SandboxConfig              `json:"sandbox,omitempty"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}
	if parsed.PermissionMode == "" {
		parsed.PermissionMode = aliases.PermissionModeCamel
	}
	if permissionRulesSet(aliases.PermissionRules) {
		mergePermissionRules(&parsed.PermissionRules, aliases.PermissionRules)
	}
	if parsed.PermissionMode == "" && parsed.PermissionRules.DefaultMode != "" {
		mode, planMode, _, ok := mapClaudePermissionDefaultMode(parsed.PermissionRules.DefaultMode)
		if ok {
			parsed.PermissionMode = mode
			parsed.PlanMode = planMode
		}
	}
	if len(aliases.MCP.Servers) != 0 || len(aliases.MCPServers) != 0 {
		if parsed.MCPServers == nil {
			parsed.MCPServers = map[string]MCPServerConfig{}
		}
		for name, server := range aliases.MCP.Servers {
			if _, exists := parsed.MCPServers[name]; !exists {
				parsed.MCPServers[name] = server
			}
		}
		for name, server := range aliases.MCPServers {
			parsed.MCPServers[name] = server
		}
	}
	if sandboxConfigSet(aliases.Sandbox) {
		mergeSandboxConfig(&parsed.Future.Sandbox, aliases.Sandbox)
	}
	*c = Config(parsed)
	return nil
}

type MutationReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Path   string `json:"path"`
	Key    string `json:"key"`
}

type FlagOverrides struct {
	ConfigPath                     string
	SessionID                      string
	Resume                         string
	Model                          string
	BaseURL                        string
	SystemPrompt                   string
	AppendPrompt                   string
	PermissionMode                 string
	SkipPermissions                bool
	AllowBroadCWD                  bool
	AllowedTools                   []string
	DisallowedTools                []string
	OutputFormatSource             string
	OutputFormatRaw                string
	OutputFormatOverridden         bool
	OutputFormatSubcommandExplicit bool
	MaxTurns                       int
	MaxTokens                      int
	Temperature                    *float64
}

func Load(overrides FlagOverrides) (Config, error) {
	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, err
	}

	for _, path := range configPaths(cfg.ConfigHome, overrides.ConfigPath) {
		if path == "" {
			continue
		}
		next, err := readConfigFile(path)
		if err != nil {
			return Config{}, err
		}
		merge(&cfg, next)
	}

	applyEnv(&cfg)
	applyFlags(&cfg, overrides)
	if err := applyAPIKeyHelper(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyManagedPolicy(&cfg); err != nil {
		return Config{}, err
	}
	if err := finalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadForInspection(overrides FlagOverrides) (Config, []string, error) {
	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, nil, err
	}
	paths := configPaths(cfg.ConfigHome, overrides.ConfigPath)
	for _, path := range paths {
		if path == "" {
			continue
		}
		next, err := readConfigFile(path)
		if err != nil {
			return Config{}, paths, err
		}
		merge(&cfg, next)
	}
	if overrides.ConfigPath == "" {
		if err := applyProjectMCPJSON(&cfg, ".mcp.json"); err != nil {
			return Config{}, paths, err
		}
	}
	applyEnv(&cfg)
	applyFlags(&cfg, overrides)
	if err := applyAPIKeyHelper(&cfg); err != nil {
		return Config{}, paths, err
	}
	if err := applyManagedPolicy(&cfg); err != nil {
		return Config{}, paths, err
	}
	if err := finalizeConfig(&cfg); err != nil {
		return Config{}, paths, err
	}
	return cfg, paths, nil
}

// InspectionPaths returns the config files that inspection commands should use
// without reading or unmarshalling them.
func InspectionPaths(overrides FlagOverrides) ([]string, error) {
	cfg, err := defaultConfig()
	if err != nil {
		return nil, err
	}
	return configPaths(cfg.ConfigHome, overrides.ConfigPath), nil
}

func Default(overrides FlagOverrides) (Config, error) {
	cfg, err := defaultConfig()
	if err != nil {
		return Config{}, err
	}
	applyEnv(&cfg)
	applyFlags(&cfg, overrides)
	if err := applyAPIKeyHelper(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyManagedPolicy(&cfg); err != nil {
		return Config{}, err
	}
	if err := finalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// EffectiveCleanupPeriodDays returns the configured transcript retention window.
func (c Config) EffectiveCleanupPeriodDays() int {
	if c.CleanupPeriodDays == nil {
		return sessionDefaultCleanupPeriodDays
	}
	return *c.CleanupPeriodDays
}

// EffectiveRespectGitignore reports whether file enumeration should honor .gitignore.
func (c Config) EffectiveRespectGitignore() bool {
	if c.RespectGitignore == nil {
		return true
	}
	return *c.RespectGitignore
}

// EffectiveDisableAllHooks reports whether hook execution is globally disabled.
func (c Config) EffectiveDisableAllHooks() bool {
	return c.DisableAllHooks != nil && *c.DisableAllHooks
}

// EffectiveAllowManagedHooksOnly reports whether unmanaged hooks are ignored.
func (c Config) EffectiveAllowManagedHooksOnly() bool {
	return c.AllowManagedHooksOnly != nil && *c.AllowManagedHooksOnly
}

func defaultConfig() (Config, error) {
	cfg := Config{
		BaseURL:             DefaultBaseURL,
		Model:               DefaultModel,
		MaxTokens:           4096,
		MaxTurns:            8,
		PermissionMode:      "workspace-write",
		AutoCompactMessages: 40,
		RateLimit:           DefaultRateLimitConfig(),
		MCPServers:          map[string]MCPServerConfig{},
	}
	home, err := defaultConfigHome()
	if err != nil {
		return Config{}, err
	}
	cfg.ConfigHome = home
	return cfg, nil
}

func finalizeConfig(cfg *Config) error {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 8
	}
	if cfg.AutoCompactMessages <= 0 {
		cfg.AutoCompactMessages = 40
	}
	if cfg.CleanupPeriodDays != nil && *cfg.CleanupPeriodDays < 0 {
		return errors.New("cleanupPeriodDays must be non-negative")
	}
	if err := validateTemperature(cfg); err != nil {
		return err
	}
	if err := validatePermissionMode(cfg); err != nil {
		return err
	}
	if err := validatePermissionRulesDefaultMode(cfg); err != nil {
		return err
	}
	if err := validateForceLoginMethod(cfg); err != nil {
		return err
	}
	if err := validateDefaultShell(cfg); err != nil {
		return err
	}
	if err := validateStatusLineConfig(cfg); err != nil {
		return err
	}
	if err := validateSandboxConfig(cfg.Future.Sandbox); err != nil {
		return err
	}
	if err := validateAPITimeoutConfig(cfg.APITimeout); err != nil {
		return err
	}
	cfg.RateLimit = NormalizeRateLimitConfig(cfg.RateLimit)
	return nil
}

func configPaths(home, explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return []string{
		filepath.Join(home, "config.json"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json"),
		filepath.Join(".omc", "settings.json"),
		filepath.Join(".omc", "settings.local.json"),
		filepath.Join(".omc", "config.json"),
		filepath.Join(".claw", "settings.json"),
		filepath.Join(".claw", "settings.local.json"),
		filepath.Join(".claw", "config.json"),
		".codog.json",
		".codog.local.json",
	}
}

func readConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, &FileError{Path: path, Err: err}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, &FileError{Path: path, Err: err}
	}
	return cfg, nil
}

func applyProjectMCPJSON(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &FileError{Path: path, Err: err}
	}
	var parsed struct {
		MCPServersSnake         map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
		MCPServersCamel         map[string]MCPServerConfig `json:"mcpServers,omitempty"`
		MCP                     nestedMCPConfig            `json:"mcp,omitempty"`
		EnableAllProjectServers *bool                      `json:"enableAllProjectMcpServers,omitempty"`
		EnabledMCPJSONServers   []string                   `json:"enabledMcpjsonServers,omitempty"`
		DisabledMCPJSONServers  []string                   `json:"disabledMcpjsonServers,omitempty"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return &FileError{Path: path, Err: err}
	}
	servers := map[string]MCPServerConfig{}
	for name, server := range parsed.MCP.Servers {
		servers[name] = server
	}
	for name, server := range parsed.MCPServersSnake {
		servers[name] = server
	}
	for name, server := range parsed.MCPServersCamel {
		servers[name] = server
	}
	if len(servers) == 0 {
		return nil
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]MCPServerConfig{}
	}
	effective := *cfg
	if effective.EnableAllProjectMCPServers == nil && parsed.EnableAllProjectServers != nil {
		enabled := *parsed.EnableAllProjectServers
		effective.EnableAllProjectMCPServers = &enabled
	}
	effective.EnabledMCPJSONServers = mergeStringLists(effective.EnabledMCPJSONServers, parsed.EnabledMCPJSONServers)
	effective.DisabledMCPJSONServers = mergeStringLists(effective.DisabledMCPJSONServers, parsed.DisabledMCPJSONServers)
	for _, name := range sortedMCPServerNames(servers) {
		if !projectMCPServerEnabled(effective, name) {
			continue
		}
		if _, exists := cfg.MCPServers[name]; exists {
			continue
		}
		cfg.MCPServers[name] = servers[name]
	}
	return nil
}

func projectMCPServerEnabled(cfg Config, name string) bool {
	if stringListContains(cfg.DisabledMCPJSONServers, name) {
		return false
	}
	if cfg.EnableAllProjectMCPServers != nil && *cfg.EnableAllProjectMCPServers {
		return true
	}
	return stringListContains(cfg.EnabledMCPJSONServers, name)
}

func sortedMCPServerNames(servers map[string]MCPServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

type FileError struct {
	Path string
	Err  error
}

func (e *FileError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Path) == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

func (e *FileError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsFileError(err error) bool {
	var fileErr *FileError
	return errors.As(err, &fileErr)
}

func SetFileValue(path string, key string, value any) (MutationReport, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return MutationReport{}, fmt.Errorf("config key is required")
	}
	data, err := readConfigMap(path)
	if err != nil {
		return MutationReport{}, err
	}
	setNestedValue(data, strings.Split(key, "."), value)
	if err := writeConfigMap(path, data); err != nil {
		return MutationReport{}, err
	}
	return MutationReport{Kind: "config", Action: "set", Status: "ok", Path: path, Key: key}, nil
}

func UnsetFileValue(path string, key string) (MutationReport, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return MutationReport{}, fmt.Errorf("config key is required")
	}
	data, err := readConfigMap(path)
	if err != nil {
		return MutationReport{}, err
	}
	unsetNestedValue(data, strings.Split(key, "."))
	if err := writeConfigMap(path, data); err != nil {
		return MutationReport{}, err
	}
	return MutationReport{Kind: "config", Action: "unset", Status: "ok", Path: path, Key: key}, nil
}

func ResetFile(path string) (MutationReport, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return MutationReport{}, fmt.Errorf("config path is required")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return MutationReport{}, err
	}
	return MutationReport{Kind: "config", Action: "reset", Status: "ok", Path: path, Key: "*"}, nil
}

func ParseConfigValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err == nil {
		return value
	}
	return raw
}

func readConfigMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = map[string]any{}
	}
	return object, nil
}

func writeConfigMap(path string, object map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func setNestedValue(object map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return
	}
	if len(parts) == 1 {
		object[key] = value
		return
	}
	next, _ := object[key].(map[string]any)
	if next == nil {
		next = map[string]any{}
		object[key] = next
	}
	setNestedValue(next, parts[1:], value)
}

func unsetNestedValue(object map[string]any, parts []string) {
	if len(parts) == 0 {
		return
	}
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return
	}
	if len(parts) == 1 {
		delete(object, key)
		return
	}
	next, ok := object[key].(map[string]any)
	if !ok {
		return
	}
	unsetNestedValue(next, parts[1:])
	if len(next) == 0 {
		delete(object, key)
	}
}

func hookCommands(raw map[string]json.RawMessage, keys ...string) ([]HookCommand, error) {
	for _, key := range keys {
		data, ok := raw[key]
		if !ok || len(data) == 0 || string(data) == "null" {
			continue
		}
		return parseHookCommandList(data)
	}
	return nil, nil
}

func parseHookCommandList(data json.RawMessage) ([]HookCommand, error) {
	return parseHookCommandListWithMatcher(data, "")
}

func parseHookCommandListWithMatcher(data json.RawMessage, inheritedMatcher string) ([]HookCommand, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	commands := []HookCommand{}
	for _, entry := range entries {
		next, err := parseHookEntry(entry, inheritedMatcher)
		if err != nil {
			return nil, err
		}
		commands = append(commands, next...)
	}
	return compactHookCommands(commands), nil
}

func parseHookEntry(data json.RawMessage, inheritedMatcher string) ([]HookCommand, error) {
	var command string
	if err := json.Unmarshal(data, &command); err == nil {
		return []HookCommand{{Matcher: inheritedMatcher, Type: "command", Command: command}}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	hook := HookCommand{Matcher: inheritedMatcher, Type: "command"}
	matcher := inheritedMatcher
	if rawMatcher, ok := object["matcher"]; ok {
		var parsed string
		if err := json.Unmarshal(rawMatcher, &parsed); err == nil {
			matcher = parsed
		}
	}
	hook.Matcher = matcher
	if rawType, ok := object["type"]; ok {
		if value, ok := parseJSONString(rawType); ok {
			hook.Type = strings.ToLower(strings.TrimSpace(value))
		}
	}
	if hook.Type == "" {
		hook.Type = "command"
	}
	if rawIf, ok := object["if"]; ok {
		hook.If, _ = parseJSONString(rawIf)
	}
	if rawShell, ok := object["shell"]; ok {
		hook.Shell, _ = parseJSONString(rawShell)
	}
	if rawStatus, ok := object["statusMessage"]; ok {
		hook.StatusMessage, _ = parseJSONString(rawStatus)
	}
	if rawTimeout, ok := object["timeout"]; ok {
		hook.TimeoutSeconds, _ = parseJSONFloat(rawTimeout)
	}
	if rawOnce, ok := object["once"]; ok {
		hook.Once, _ = parseJSONBool(rawOnce)
	}
	if rawAsync, ok := object["async"]; ok {
		hook.Async, _ = parseJSONBool(rawAsync)
	}
	if rawAsyncRewake, ok := object["asyncRewake"]; ok {
		hook.AsyncRewake, _ = parseJSONBool(rawAsyncRewake)
	}
	if rawHeaders, ok := object["headers"]; ok {
		hook.Headers = parseJSONStringMap(rawHeaders)
	}
	if rawAllowed, ok := object["allowedEnvVars"]; ok {
		hook.AllowedEnvVars = parseJSONStringSlice(rawAllowed)
	}
	if rawCommand, ok := object["command"]; ok {
		if err := json.Unmarshal(rawCommand, &command); err == nil {
			hook.Type = "command"
			hook.Command = command
			return []HookCommand{hook}, nil
		}
	}
	if rawURL, ok := object["url"]; ok {
		hook.URL, _ = parseJSONString(rawURL)
	}
	if rawPrompt, ok := object["prompt"]; ok {
		hook.Prompt, _ = parseJSONString(rawPrompt)
	}
	if rawModel, ok := object["model"]; ok {
		hook.Model, _ = parseJSONString(rawModel)
	}
	if rawHooks, ok := object["hooks"]; ok {
		return parseHookCommandListWithMatcher(rawHooks, matcher)
	}
	return []HookCommand{hook}, nil
}

func hookCommandStrings(values []HookCommand) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		command := HookCommandDisplay(value)
		if command != "" {
			out = append(out, command)
		}
	}
	return out
}

func compactHookCommands(values []HookCommand) []HookCommand {
	out := make([]HookCommand, 0, len(values))
	for _, value := range values {
		value.Matcher = strings.TrimSpace(value.Matcher)
		value.Type = strings.ToLower(strings.TrimSpace(value.Type))
		if value.Type == "" {
			value.Type = "command"
		}
		value.Command = strings.TrimSpace(value.Command)
		value.URL = strings.TrimSpace(value.URL)
		value.Prompt = strings.TrimSpace(value.Prompt)
		value.Model = strings.TrimSpace(value.Model)
		value.If = strings.TrimSpace(value.If)
		value.Shell = strings.TrimSpace(value.Shell)
		value.StatusMessage = strings.TrimSpace(value.StatusMessage)
		if HookCommandDisplay(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func HookCommandDisplay(value HookCommand) string {
	switch strings.ToLower(strings.TrimSpace(value.Type)) {
	case "", "command":
		return strings.TrimSpace(value.Command)
	case "http":
		url := strings.TrimSpace(value.URL)
		if url == "" {
			return ""
		}
		return "http POST " + url
	case "prompt":
		prompt := strings.TrimSpace(value.Prompt)
		if prompt == "" {
			return ""
		}
		return "prompt " + prompt
	case "agent":
		prompt := strings.TrimSpace(value.Prompt)
		if prompt == "" {
			return ""
		}
		return "agent " + prompt
	default:
		if command := strings.TrimSpace(value.Command); command != "" {
			return command
		}
		if url := strings.TrimSpace(value.URL); url != "" {
			return value.Type + " " + url
		}
		return ""
	}
}

func parseJSONString(data json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return "", false
	}
	return value, true
}

func parseJSONBool(data json.RawMessage) (bool, bool) {
	var value bool
	if err := json.Unmarshal(data, &value); err != nil {
		return false, false
	}
	return value, true
}

func parseJSONFloat(data json.RawMessage) (float64, bool) {
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, false
	}
	return value, true
}

func parseJSONStringMap(data json.RawMessage) map[string]string {
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil
	}
	out := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func parseJSONStringSlice(data json.RawMessage) []string {
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func merge(dst *Config, src Config) {
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.APIKeyHelper != "" {
		dst.APIKeyHelper = src.APIKeyHelper
	}
	if src.AuthToken != "" {
		dst.AuthToken = src.AuthToken
	}
	if src.OAuthProfile != "" {
		dst.OAuthProfile = src.OAuthProfile
	}
	if src.ForceLoginMethod != "" {
		dst.ForceLoginMethod = src.ForceLoginMethod
	}
	if src.ForceLoginOrgUUID != "" {
		dst.ForceLoginOrgUUID = src.ForceLoginOrgUUID
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.SystemPrompt != "" {
		dst.SystemPrompt = src.SystemPrompt
	}
	if src.AppendSystemPrompt != "" {
		dst.AppendSystemPrompt = joinPromptAppend(dst.AppendSystemPrompt, src.AppendSystemPrompt)
	}
	if src.Language != "" {
		dst.Language = src.Language
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.EditorMode != "" {
		dst.EditorMode = src.EditorMode
	}
	if src.DefaultShell != "" {
		dst.DefaultShell = src.DefaultShell
	}
	if src.AdvisorModel != "" {
		dst.AdvisorModel = src.AdvisorModel
	}
	if src.ReasoningEffort != "" {
		dst.ReasoningEffort = src.ReasoningEffort
	}
	if src.FastMode != nil {
		value := *src.FastMode
		dst.FastMode = &value
	}
	if src.VoiceEnabled != nil {
		value := *src.VoiceEnabled
		dst.VoiceEnabled = &value
	}
	if src.VoiceCommand != "" {
		dst.VoiceCommand = src.VoiceCommand
	}
	if src.SpeechCommand != "" {
		dst.SpeechCommand = src.SpeechCommand
	}
	if src.MaxTokens != 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if src.MaxTurns != 0 {
		dst.MaxTurns = src.MaxTurns
	}
	if src.Temperature != nil {
		value := *src.Temperature
		dst.Temperature = &value
	}
	if src.PermissionMode != "" {
		dst.PermissionMode = src.PermissionMode
		dst.PlanMode = src.PlanMode
	}
	if privacyConfigSet(src.Privacy) {
		mergePrivacyConfig(&dst.Privacy, src.Privacy)
	}
	if permissionRulesSet(src.PermissionRules) {
		mergePermissionRules(&dst.PermissionRules, src.PermissionRules)
		if src.PermissionMode == "" && src.PermissionRules.DefaultMode != "" {
			if mode, planMode, _, ok := mapClaudePermissionDefaultMode(src.PermissionRules.DefaultMode); ok {
				dst.PermissionMode = mode
				dst.PlanMode = planMode
			}
		}
		if len(src.PermissionRules.AdditionalDirectories) != 0 {
			dst.AdditionalDirs = mergeStringLists(dst.AdditionalDirs, src.PermissionRules.AdditionalDirectories)
		}
	}
	if src.ConfigHome != "" {
		dst.ConfigHome = expandHome(src.ConfigHome)
	}
	if src.AutoCompactMessages != 0 {
		dst.AutoCompactMessages = src.AutoCompactMessages
	}
	if src.CleanupPeriodDays != nil {
		days := *src.CleanupPeriodDays
		dst.CleanupPeriodDays = &days
	}
	if src.RespectGitignore != nil {
		respect := *src.RespectGitignore
		dst.RespectGitignore = &respect
	}
	if src.DisableAllHooks != nil {
		disabled := *src.DisableAllHooks
		dst.DisableAllHooks = &disabled
	}
	if src.AllowManagedHooksOnly != nil {
		managedOnly := *src.AllowManagedHooksOnly
		dst.AllowManagedHooksOnly = &managedOnly
	}
	if src.AllowedHTTPHookURLs != nil {
		allowed := mergeStringLists(stringListPointerValue(dst.AllowedHTTPHookURLs), *src.AllowedHTTPHookURLs)
		dst.AllowedHTTPHookURLs = &allowed
	}
	if src.HTTPHookAllowedEnvVars != nil {
		allowed := mergeStringLists(stringListPointerValue(dst.HTTPHookAllowedEnvVars), *src.HTTPHookAllowedEnvVars)
		dst.HTTPHookAllowedEnvVars = &allowed
	}
	if src.StatusLine != nil {
		dst.StatusLine = cloneStatusLineConfig(src.StatusLine)
	}
	if len(src.Worktree.SymlinkDirectories) != 0 {
		dst.Worktree.SymlinkDirectories = mergeStringLists(dst.Worktree.SymlinkDirectories, src.Worktree.SymlinkDirectories)
	}
	if len(src.Worktree.SparsePaths) != 0 {
		dst.Worktree.SparsePaths = mergeStringLists(dst.Worktree.SparsePaths, src.Worktree.SparsePaths)
	}
	if src.EnableAllProjectMCPServers != nil {
		enabled := *src.EnableAllProjectMCPServers
		dst.EnableAllProjectMCPServers = &enabled
	}
	if src.EnabledMCPJSONServers != nil {
		dst.EnabledMCPJSONServers = mergeStringLists(dst.EnabledMCPJSONServers, src.EnabledMCPJSONServers)
	}
	if src.DisabledMCPJSONServers != nil {
		dst.DisabledMCPJSONServers = mergeStringLists(dst.DisabledMCPJSONServers, src.DisabledMCPJSONServers)
	}
	if rateLimitConfigSet(src.RateLimit) {
		mergeRateLimitConfig(&dst.RateLimit, src.RateLimit)
	}
	if apiTimeoutConfigSet(src.APITimeout) {
		mergeAPITimeoutConfig(&dst.APITimeout, src.APITimeout)
	}
	if providerFallbackConfigSet(src.ProviderFallbacks) {
		mergeProviderFallbackConfig(&dst.ProviderFallbacks, src.ProviderFallbacks)
	}
	if len(src.Env) != 0 {
		if dst.Env == nil {
			dst.Env = map[string]string{}
		}
		for key, value := range src.Env {
			if strings.TrimSpace(key) == "" {
				continue
			}
			dst.Env[key] = value
		}
	}
	if src.TrustedRoots != nil {
		dst.TrustedRoots = mergeStringLists(dst.TrustedRoots, src.TrustedRoots)
	}
	if src.RAGBaseURL != "" {
		dst.RAGBaseURL = src.RAGBaseURL
	}
	if src.RAGTimeoutSeconds != 0 {
		dst.RAGTimeoutSeconds = src.RAGTimeoutSeconds
	}
	if src.RAGTopKMax != 0 {
		dst.RAGTopKMax = src.RAGTopKMax
	}
	if len(src.AdditionalDirs) != 0 {
		dst.AdditionalDirs = append([]string(nil), src.AdditionalDirs...)
	}
	if len(src.EnabledSkills) != 0 {
		dst.EnabledSkills = append([]string(nil), src.EnabledSkills...)
	}
	mergeHookConfig(&dst.Hooks, src.Hooks)
	if len(src.MCPServers) != 0 {
		if dst.MCPServers == nil {
			dst.MCPServers = map[string]MCPServerConfig{}
		}
		for name, server := range src.MCPServers {
			dst.MCPServers[name] = server
		}
	}
	if futureConfigSet(src.Future) {
		mergeFutureConfig(&dst.Future, src.Future)
	}
}

func futureConfigSet(cfg FutureConfig) bool {
	return cfg.RemoteEnabled ||
		cfg.RemoteAuthToken != "" ||
		cfg.RemoteLeaseSeconds != 0 ||
		cfg.EnterprisePolicy != "" ||
		cfg.EnterprisePolicyPublicKey != "" ||
		len(cfg.PluginMarketplaces) != 0 ||
		len(cfg.PluginMarketplaceKeys) != 0 ||
		cfg.SandboxStrategy != "" ||
		sandboxConfigSet(cfg.Sandbox) ||
		cfg.UpdaterManifestURL != "" ||
		cfg.EditorBridgeSocket != "" ||
		cfg.EditorBridgeToken != "" ||
		cfg.BackgroundStatePath != "" ||
		cfg.ChromeDefaultEnabled != nil ||
		cfg.NotificationsEnabled != nil ||
		cfg.UltraReviewEnabled != nil ||
		cfg.SlackAppInstallCount != 0 ||
		cfg.StickerOrderCount != 0 ||
		cfg.ExtraUsageVisitCount != 0 ||
		cfg.GuestPassReferralURL != "" ||
		cfg.GuestPassVisitCount != 0
}

func sandboxConfigSet(cfg SandboxConfig) bool {
	return cfg.Enabled != nil ||
		cfg.NamespaceRestrictions != nil ||
		cfg.NetworkIsolation != nil ||
		cfg.FilesystemMode != "" ||
		cfg.AllowedMounts != nil
}

func mergeFutureConfig(dst *FutureConfig, src FutureConfig) {
	if src.RemoteEnabled {
		dst.RemoteEnabled = src.RemoteEnabled
	}
	if src.RemoteAuthToken != "" {
		dst.RemoteAuthToken = src.RemoteAuthToken
	}
	if src.RemoteLeaseSeconds != 0 {
		dst.RemoteLeaseSeconds = src.RemoteLeaseSeconds
	}
	if src.EnterprisePolicy != "" {
		dst.EnterprisePolicy = src.EnterprisePolicy
	}
	if src.EnterprisePolicyPublicKey != "" {
		dst.EnterprisePolicyPublicKey = src.EnterprisePolicyPublicKey
	}
	if len(src.PluginMarketplaces) != 0 {
		dst.PluginMarketplaces = append([]string(nil), src.PluginMarketplaces...)
	}
	if len(src.PluginMarketplaceKeys) != 0 {
		dst.PluginMarketplaceKeys = cloneStringMap(src.PluginMarketplaceKeys)
	}
	if src.SandboxStrategy != "" {
		dst.SandboxStrategy = src.SandboxStrategy
	}
	if sandboxConfigSet(src.Sandbox) {
		mergeSandboxConfig(&dst.Sandbox, src.Sandbox)
	}
	if src.UpdaterManifestURL != "" {
		dst.UpdaterManifestURL = src.UpdaterManifestURL
	}
	if src.EditorBridgeSocket != "" {
		dst.EditorBridgeSocket = src.EditorBridgeSocket
	}
	if src.EditorBridgeToken != "" {
		dst.EditorBridgeToken = src.EditorBridgeToken
	}
	if src.BackgroundStatePath != "" {
		dst.BackgroundStatePath = src.BackgroundStatePath
	}
	if src.ChromeDefaultEnabled != nil {
		dst.ChromeDefaultEnabled = src.ChromeDefaultEnabled
	}
	if src.NotificationsEnabled != nil {
		dst.NotificationsEnabled = src.NotificationsEnabled
	}
	if src.UltraReviewEnabled != nil {
		dst.UltraReviewEnabled = src.UltraReviewEnabled
	}
	if src.SlackAppInstallCount != 0 {
		dst.SlackAppInstallCount = src.SlackAppInstallCount
	}
	if src.StickerOrderCount != 0 {
		dst.StickerOrderCount = src.StickerOrderCount
	}
	if src.ExtraUsageVisitCount != 0 {
		dst.ExtraUsageVisitCount = src.ExtraUsageVisitCount
	}
	if src.GuestPassReferralURL != "" {
		dst.GuestPassReferralURL = src.GuestPassReferralURL
	}
	if src.GuestPassVisitCount != 0 {
		dst.GuestPassVisitCount = src.GuestPassVisitCount
	}
}

func mergeSandboxConfig(dst *SandboxConfig, src SandboxConfig) {
	if src.Enabled != nil {
		value := *src.Enabled
		dst.Enabled = &value
	}
	if src.NamespaceRestrictions != nil {
		value := *src.NamespaceRestrictions
		dst.NamespaceRestrictions = &value
	}
	if src.NetworkIsolation != nil {
		value := *src.NetworkIsolation
		dst.NetworkIsolation = &value
	}
	if src.FilesystemMode != "" {
		dst.FilesystemMode = src.FilesystemMode
	}
	if src.AllowedMounts != nil {
		dst.AllowedMounts = append([]string(nil), src.AllowedMounts...)
	}
}

func permissionRulesSet(rules PermissionRules) bool {
	return rules.DefaultMode != "" ||
		len(rules.AdditionalDirectories) != 0 ||
		len(rules.Allow) != 0 ||
		len(rules.Deny) != 0 ||
		len(rules.Ask) != 0 ||
		len(rules.DeniedTools) != 0
}

func privacyConfigSet(cfg PrivacyConfig) bool {
	return cfg.TelemetryEnabled != nil ||
		cfg.CrashReportsEnabled != nil ||
		cfg.PromptHistoryEnabled != nil
}

func mergePrivacyConfig(dst *PrivacyConfig, src PrivacyConfig) {
	if src.TelemetryEnabled != nil {
		dst.TelemetryEnabled = src.TelemetryEnabled
	}
	if src.CrashReportsEnabled != nil {
		dst.CrashReportsEnabled = src.CrashReportsEnabled
	}
	if src.PromptHistoryEnabled != nil {
		dst.PromptHistoryEnabled = src.PromptHistoryEnabled
	}
}

func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		MaxRetries:       2,
		InitialBackoffMS: 500,
		MaxBackoffMS:     5000,
	}
}

func NormalizeRateLimitConfig(cfg RateLimitConfig) RateLimitConfig {
	defaults := DefaultRateLimitConfig()
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaults.MaxRetries
	}
	if cfg.InitialBackoffMS <= 0 {
		cfg.InitialBackoffMS = defaults.InitialBackoffMS
	}
	if cfg.MaxBackoffMS <= 0 {
		cfg.MaxBackoffMS = defaults.MaxBackoffMS
	}
	if cfg.MaxBackoffMS < cfg.InitialBackoffMS {
		cfg.MaxBackoffMS = cfg.InitialBackoffMS
	}
	return cfg
}

func rateLimitConfigSet(cfg RateLimitConfig) bool {
	return cfg.MaxRetries != 0 || cfg.InitialBackoffMS != 0 || cfg.MaxBackoffMS != 0
}

func apiTimeoutConfigSet(cfg APITimeoutConfig) bool {
	return cfg.ConnectTimeoutSeconds != 0 || cfg.RequestTimeoutSeconds != 0 || cfg.MaxRetries != 0
}

func providerFallbackConfigSet(cfg ProviderFallbackConfig) bool {
	return cfg.Primary != "" || cfg.Fallbacks != nil
}

func mergeRateLimitConfig(dst *RateLimitConfig, src RateLimitConfig) {
	if src.MaxRetries != 0 {
		dst.MaxRetries = src.MaxRetries
	}
	if src.InitialBackoffMS != 0 {
		dst.InitialBackoffMS = src.InitialBackoffMS
	}
	if src.MaxBackoffMS != 0 {
		dst.MaxBackoffMS = src.MaxBackoffMS
	}
}

func mergeAPITimeoutConfig(dst *APITimeoutConfig, src APITimeoutConfig) {
	if src.ConnectTimeoutSeconds != 0 {
		dst.ConnectTimeoutSeconds = src.ConnectTimeoutSeconds
	}
	if src.RequestTimeoutSeconds != 0 {
		dst.RequestTimeoutSeconds = src.RequestTimeoutSeconds
	}
	if src.MaxRetries != 0 {
		dst.MaxRetries = src.MaxRetries
	}
}

func mergeProviderFallbackConfig(dst *ProviderFallbackConfig, src ProviderFallbackConfig) {
	if src.Primary != "" {
		dst.Primary = src.Primary
	}
	if src.Fallbacks != nil {
		dst.Fallbacks = append([]string(nil), src.Fallbacks...)
	}
}

func mergeStringLists(base []string, overlay []string) []string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make([]string, 0, len(base)+len(overlay))
	seen := map[string]bool{}
	for _, value := range append(append([]string(nil), base...), overlay...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func stringListPointerValue(values *[]string) []string {
	if values == nil {
		return nil
	}
	return *values
}

func stringListContains(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}

func mergeHookConfig(dst *HookConfig, src HookConfig) {
	if len(src.PreToolUseCommands) != 0 {
		dst.PreToolUseCommands = mergeHookCommands(dst.PreToolUseCommands, src.PreToolUseCommands)
	} else if len(src.PreToolUse) != 0 {
		dst.PreToolUseCommands = mergeHookCommands(dst.PreToolUseCommands, hookCommandsFromStrings(src.PreToolUse))
	}
	if len(src.PostToolUseCommands) != 0 {
		dst.PostToolUseCommands = mergeHookCommands(dst.PostToolUseCommands, src.PostToolUseCommands)
	} else if len(src.PostToolUse) != 0 {
		dst.PostToolUseCommands = mergeHookCommands(dst.PostToolUseCommands, hookCommandsFromStrings(src.PostToolUse))
	}
	if len(src.PostToolUseFailureCommands) != 0 {
		dst.PostToolUseFailureCommands = mergeHookCommands(dst.PostToolUseFailureCommands, src.PostToolUseFailureCommands)
	} else if len(src.PostToolUseFailure) != 0 {
		dst.PostToolUseFailureCommands = mergeHookCommands(dst.PostToolUseFailureCommands, hookCommandsFromStrings(src.PostToolUseFailure))
	}
	if len(src.PermissionRequestCommands) != 0 {
		dst.PermissionRequestCommands = mergeHookCommands(dst.PermissionRequestCommands, src.PermissionRequestCommands)
	} else if len(src.PermissionRequest) != 0 {
		dst.PermissionRequestCommands = mergeHookCommands(dst.PermissionRequestCommands, hookCommandsFromStrings(src.PermissionRequest))
	}
	if len(src.PermissionDeniedCommands) != 0 {
		dst.PermissionDeniedCommands = mergeHookCommands(dst.PermissionDeniedCommands, src.PermissionDeniedCommands)
	} else if len(src.PermissionDenied) != 0 {
		dst.PermissionDeniedCommands = mergeHookCommands(dst.PermissionDeniedCommands, hookCommandsFromStrings(src.PermissionDenied))
	}
	if len(src.UserPromptSubmitCommands) != 0 {
		dst.UserPromptSubmitCommands = mergeHookCommands(dst.UserPromptSubmitCommands, src.UserPromptSubmitCommands)
	} else if len(src.UserPromptSubmit) != 0 {
		dst.UserPromptSubmitCommands = mergeHookCommands(dst.UserPromptSubmitCommands, hookCommandsFromStrings(src.UserPromptSubmit))
	}
	if len(src.SessionStartCommands) != 0 {
		dst.SessionStartCommands = mergeHookCommands(dst.SessionStartCommands, src.SessionStartCommands)
	} else if len(src.SessionStart) != 0 {
		dst.SessionStartCommands = mergeHookCommands(dst.SessionStartCommands, hookCommandsFromStrings(src.SessionStart))
	}
	if len(src.SessionEndCommands) != 0 {
		dst.SessionEndCommands = mergeHookCommands(dst.SessionEndCommands, src.SessionEndCommands)
	} else if len(src.SessionEnd) != 0 {
		dst.SessionEndCommands = mergeHookCommands(dst.SessionEndCommands, hookCommandsFromStrings(src.SessionEnd))
	}
	if len(src.SetupCommands) != 0 {
		dst.SetupCommands = mergeHookCommands(dst.SetupCommands, src.SetupCommands)
	} else if len(src.Setup) != 0 {
		dst.SetupCommands = mergeHookCommands(dst.SetupCommands, hookCommandsFromStrings(src.Setup))
	}
	if len(src.StopCommands) != 0 {
		dst.StopCommands = mergeHookCommands(dst.StopCommands, src.StopCommands)
	} else if len(src.Stop) != 0 {
		dst.StopCommands = mergeHookCommands(dst.StopCommands, hookCommandsFromStrings(src.Stop))
	}
	if len(src.StopFailureCommands) != 0 {
		dst.StopFailureCommands = mergeHookCommands(dst.StopFailureCommands, src.StopFailureCommands)
	} else if len(src.StopFailure) != 0 {
		dst.StopFailureCommands = mergeHookCommands(dst.StopFailureCommands, hookCommandsFromStrings(src.StopFailure))
	}
	if len(src.PreCompactCommands) != 0 {
		dst.PreCompactCommands = mergeHookCommands(dst.PreCompactCommands, src.PreCompactCommands)
	} else if len(src.PreCompact) != 0 {
		dst.PreCompactCommands = mergeHookCommands(dst.PreCompactCommands, hookCommandsFromStrings(src.PreCompact))
	}
	if len(src.PostCompactCommands) != 0 {
		dst.PostCompactCommands = mergeHookCommands(dst.PostCompactCommands, src.PostCompactCommands)
	} else if len(src.PostCompact) != 0 {
		dst.PostCompactCommands = mergeHookCommands(dst.PostCompactCommands, hookCommandsFromStrings(src.PostCompact))
	}
	if len(src.NotificationCommands) != 0 {
		dst.NotificationCommands = mergeHookCommands(dst.NotificationCommands, src.NotificationCommands)
	} else if len(src.Notification) != 0 {
		dst.NotificationCommands = mergeHookCommands(dst.NotificationCommands, hookCommandsFromStrings(src.Notification))
	}
	if len(src.SubagentStartCommands) != 0 {
		dst.SubagentStartCommands = mergeHookCommands(dst.SubagentStartCommands, src.SubagentStartCommands)
	} else if len(src.SubagentStart) != 0 {
		dst.SubagentStartCommands = mergeHookCommands(dst.SubagentStartCommands, hookCommandsFromStrings(src.SubagentStart))
	}
	if len(src.SubagentStopCommands) != 0 {
		dst.SubagentStopCommands = mergeHookCommands(dst.SubagentStopCommands, src.SubagentStopCommands)
	} else if len(src.SubagentStop) != 0 {
		dst.SubagentStopCommands = mergeHookCommands(dst.SubagentStopCommands, hookCommandsFromStrings(src.SubagentStop))
	}
	if len(src.WorktreeCreateCommands) != 0 {
		dst.WorktreeCreateCommands = mergeHookCommands(dst.WorktreeCreateCommands, src.WorktreeCreateCommands)
	} else if len(src.WorktreeCreate) != 0 {
		dst.WorktreeCreateCommands = mergeHookCommands(dst.WorktreeCreateCommands, hookCommandsFromStrings(src.WorktreeCreate))
	}
	if len(src.WorktreeRemoveCommands) != 0 {
		dst.WorktreeRemoveCommands = mergeHookCommands(dst.WorktreeRemoveCommands, src.WorktreeRemoveCommands)
	} else if len(src.WorktreeRemove) != 0 {
		dst.WorktreeRemoveCommands = mergeHookCommands(dst.WorktreeRemoveCommands, hookCommandsFromStrings(src.WorktreeRemove))
	}
	if len(src.CwdChangedCommands) != 0 {
		dst.CwdChangedCommands = mergeHookCommands(dst.CwdChangedCommands, src.CwdChangedCommands)
	} else if len(src.CwdChanged) != 0 {
		dst.CwdChangedCommands = mergeHookCommands(dst.CwdChangedCommands, hookCommandsFromStrings(src.CwdChanged))
	}
	if len(src.TaskCreatedCommands) != 0 {
		dst.TaskCreatedCommands = mergeHookCommands(dst.TaskCreatedCommands, src.TaskCreatedCommands)
	} else if len(src.TaskCreated) != 0 {
		dst.TaskCreatedCommands = mergeHookCommands(dst.TaskCreatedCommands, hookCommandsFromStrings(src.TaskCreated))
	}
	if len(src.TaskCompletedCommands) != 0 {
		dst.TaskCompletedCommands = mergeHookCommands(dst.TaskCompletedCommands, src.TaskCompletedCommands)
	} else if len(src.TaskCompleted) != 0 {
		dst.TaskCompletedCommands = mergeHookCommands(dst.TaskCompletedCommands, hookCommandsFromStrings(src.TaskCompleted))
	}
	if len(src.InstructionsLoadedCommands) != 0 {
		dst.InstructionsLoadedCommands = mergeHookCommands(dst.InstructionsLoadedCommands, src.InstructionsLoadedCommands)
	} else if len(src.InstructionsLoaded) != 0 {
		dst.InstructionsLoadedCommands = mergeHookCommands(dst.InstructionsLoadedCommands, hookCommandsFromStrings(src.InstructionsLoaded))
	}
	if len(src.FileChangedCommands) != 0 {
		dst.FileChangedCommands = mergeHookCommands(dst.FileChangedCommands, src.FileChangedCommands)
	} else if len(src.FileChanged) != 0 {
		dst.FileChangedCommands = mergeHookCommands(dst.FileChangedCommands, hookCommandsFromStrings(src.FileChanged))
	}
	dst.PreToolUse = hookCommandStrings(dst.PreToolUseCommands)
	dst.PostToolUse = hookCommandStrings(dst.PostToolUseCommands)
	dst.PostToolUseFailure = hookCommandStrings(dst.PostToolUseFailureCommands)
	dst.PermissionRequest = hookCommandStrings(dst.PermissionRequestCommands)
	dst.PermissionDenied = hookCommandStrings(dst.PermissionDeniedCommands)
	dst.UserPromptSubmit = hookCommandStrings(dst.UserPromptSubmitCommands)
	dst.SessionStart = hookCommandStrings(dst.SessionStartCommands)
	dst.SessionEnd = hookCommandStrings(dst.SessionEndCommands)
	dst.Setup = hookCommandStrings(dst.SetupCommands)
	dst.Stop = hookCommandStrings(dst.StopCommands)
	dst.StopFailure = hookCommandStrings(dst.StopFailureCommands)
	dst.PreCompact = hookCommandStrings(dst.PreCompactCommands)
	dst.PostCompact = hookCommandStrings(dst.PostCompactCommands)
	dst.Notification = hookCommandStrings(dst.NotificationCommands)
	dst.SubagentStart = hookCommandStrings(dst.SubagentStartCommands)
	dst.SubagentStop = hookCommandStrings(dst.SubagentStopCommands)
	dst.WorktreeCreate = hookCommandStrings(dst.WorktreeCreateCommands)
	dst.WorktreeRemove = hookCommandStrings(dst.WorktreeRemoveCommands)
	dst.CwdChanged = hookCommandStrings(dst.CwdChangedCommands)
	dst.TaskCreated = hookCommandStrings(dst.TaskCreatedCommands)
	dst.TaskCompleted = hookCommandStrings(dst.TaskCompletedCommands)
	dst.InstructionsLoaded = hookCommandStrings(dst.InstructionsLoadedCommands)
	dst.FileChanged = hookCommandStrings(dst.FileChangedCommands)
}

func MergeHookConfig(dst *HookConfig, src HookConfig) {
	mergeHookConfig(dst, src)
}

func hookCommandsFromStrings(values []string) []HookCommand {
	commands := make([]HookCommand, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			commands = append(commands, HookCommand{Type: "command", Command: value})
		}
	}
	return commands
}

func mergeHookCommands(dst []HookCommand, src []HookCommand) []HookCommand {
	out := compactHookCommands(dst)
	seen := map[string]struct{}{}
	for _, command := range out {
		seen[hookCommandKey(command)] = struct{}{}
	}
	for _, command := range compactHookCommands(src) {
		key := hookCommandKey(command)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, command)
	}
	return out
}

func hookCommandKey(command HookCommand) string {
	data, err := json.Marshal(command)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(command.Matcher)) + "\x00" + HookCommandDisplay(command)
	}
	return string(data)
}

func mergePermissionRules(dst *PermissionRules, src PermissionRules) {
	if src.DefaultMode != "" {
		dst.DefaultMode = src.DefaultMode
	}
	if len(src.AdditionalDirectories) != 0 {
		dst.AdditionalDirectories = mergeStringLists(dst.AdditionalDirectories, src.AdditionalDirectories)
	}
	dst.Allow = append(dst.Allow, src.Allow...)
	dst.Deny = append(dst.Deny, src.Deny...)
	dst.Ask = append(dst.Ask, src.Ask...)
	dst.DeniedTools = append(dst.DeniedTools, src.DeniedTools...)
}

func cloneStatusLineConfig(src *StatusLineConfig) *StatusLineConfig {
	if src == nil {
		return nil
	}
	clone := *src
	if src.Padding != nil {
		padding := *src.Padding
		clone.Padding = &padding
	}
	return &clone
}

func joinPromptAppend(existing string, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "\n\n" + next
}

func applyEnv(cfg *Config) {
	dotenv := envfile.Current()
	lookup := func(name string) string {
		value, _ := envfile.Lookup(name, dotenv)
		return value
	}
	genericBaseURLSet := strings.TrimSpace(lookup("CODOG_BASE_URL")) != ""
	genericAPIKeySet := strings.TrimSpace(lookup("CODOG_API_KEY")) != ""
	genericAuthTokenSet := strings.TrimSpace(lookup("CODOG_AUTH_TOKEN")) != ""
	genericCredentialSet := genericAPIKeySet || genericAuthTokenSet

	if value := lookup("ANTHROPIC_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	if value := lookup("ANTHROPIC_AUTH_TOKEN"); value != "" {
		cfg.AuthToken = value
	}
	if value := lookup("ANTHROPIC_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := lookup("CODOG_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := lookup("CODOG_MODEL"); value != "" {
		cfg.Model = value
	}
	if value := lookup("CODOG_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	if value := lookup("CODOG_AUTH_TOKEN"); value != "" {
		cfg.AuthToken = value
	}
	applyRoutedProviderEnv(cfg, genericBaseURLSet, genericCredentialSet, lookup)
	if value := lookup("CODOG_ADVISOR_MODEL"); value != "" {
		cfg.AdvisorModel = value
	}
	if value := lookup("CODOG_SYSTEM_PROMPT"); value != "" {
		cfg.SystemPrompt = value
	}
	if value := lookup("CODOG_APPEND_SYSTEM_PROMPT"); value != "" {
		cfg.AppendSystemPrompt = joinPromptAppend(cfg.AppendSystemPrompt, value)
	}
	if value := lookup("CODOG_LANGUAGE"); value != "" {
		cfg.Language = value
	}
	if value := lookup("CODOG_THEME"); value != "" {
		cfg.Theme = value
	}
	if value := lookup("CODOG_EDITOR_MODE"); value != "" {
		cfg.EditorMode = value
	}
	if value := lookup("CODOG_DEFAULT_SHELL"); value != "" {
		cfg.DefaultShell = value
	}
	if value := lookup("CODOG_REASONING_EFFORT"); value != "" {
		cfg.ReasoningEffort = value
	}
	if value := lookup("CODOG_OAUTH_PROFILE"); value != "" {
		cfg.OAuthProfile = value
	}
	if value := lookup("CODOG_TEMPERATURE"); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			cfg.Temperature = &parsed
		}
	}
	if value, ok := parseBoolEnv("CODOG_FAST_MODE", lookup); ok {
		cfg.FastMode = &value
	}
	if value, ok := parseBoolEnv("CODOG_VOICE_ENABLED", lookup); ok {
		cfg.VoiceEnabled = &value
	}
	if value := lookup("CODOG_VOICE_COMMAND"); value != "" {
		cfg.VoiceCommand = value
	}
	if value := lookup("CODOG_SPEECH_COMMAND"); value != "" {
		cfg.SpeechCommand = value
	}
	if value := lookup("CODOG_PERMISSION_MODE"); value != "" {
		cfg.PermissionMode = value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_TELEMETRY_ENABLED", lookup); ok {
		cfg.Privacy.TelemetryEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_CRASH_REPORTS_ENABLED", lookup); ok {
		cfg.Privacy.CrashReportsEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_PROMPT_HISTORY_ENABLED", lookup); ok {
		cfg.Privacy.PromptHistoryEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_CHROME_DEFAULT_ENABLED", lookup); ok {
		cfg.Future.ChromeDefaultEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_NOTIFICATIONS_ENABLED", lookup); ok {
		cfg.Future.NotificationsEnabled = &value
	}
	if value := lookup("CODOG_CONFIG_HOME"); value != "" {
		cfg.ConfigHome = expandHome(value)
	}
	if value := lookup("CODOG_MAX_TURNS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxTurns = parsed
		}
	}
	if value := lookup("CODOG_RATE_LIMIT_MAX_RETRIES"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.MaxRetries = parsed
		}
	}
	if value := lookup("CODOG_RATE_LIMIT_INITIAL_BACKOFF_MS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.InitialBackoffMS = parsed
		}
	}
	if value := lookup("CODOG_RATE_LIMIT_MAX_BACKOFF_MS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.MaxBackoffMS = parsed
		}
	}
	if value := lookup("CODOG_RAG_BASE_URL"); value != "" {
		cfg.RAGBaseURL = value
	}
	if value := lookup("CODOG_RAG_TIMEOUT_SECONDS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RAGTimeoutSeconds = parsed
		}
	}
	if value := lookup("CODOG_RAG_TOP_K_MAX"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RAGTopKMax = parsed
		}
	}
	if value := lookup("CODOG_ADDITIONAL_DIRS"); value != "" {
		cfg.AdditionalDirs = splitPathList(value)
	}
}

func applyRoutedProviderEnv(cfg *Config, genericBaseURLSet bool, genericCredentialSet bool, lookup func(string) string) {
	if ollamaHost := strings.TrimSpace(lookup("OLLAMA_HOST")); ollamaHost != "" && !genericBaseURLSet {
		cfg.RuntimeProvider = modelrouting.ProviderOpenAI
		cfg.RuntimeProviderSource = "OLLAMA_HOST"
		if !genericCredentialSet {
			cfg.AuthToken = ""
			cfg.APIKey = ""
		}
		cfg.BaseURL = strings.TrimRight(ollamaHost, "/") + "/v1"
		return
	}
	if openAIBaseURL := strings.TrimSpace(lookup("OPENAI_BASE_URL")); openAIBaseURL != "" &&
		modelrouting.ProviderForModel(cfg.Model) == modelrouting.ProviderAnthropic &&
		modelrouting.LooksLikeLocalOpenAICompatibleModel(cfg.Model) &&
		!genericBaseURLSet {
		cfg.RuntimeProvider = modelrouting.ProviderOpenAI
		cfg.RuntimeProviderSource = "OPENAI_BASE_URL"
		if !genericCredentialSet {
			cfg.AuthToken = ""
			cfg.APIKey = strings.TrimSpace(lookup("OPENAI_API_KEY"))
		}
		cfg.BaseURL = openAIBaseURL
		return
	}

	switch modelrouting.ProviderForModel(cfg.Model) {
	case modelrouting.ProviderOpenAI:
		if !genericCredentialSet {
			cfg.AuthToken = ""
			cfg.APIKey = strings.TrimSpace(lookup("OPENAI_API_KEY"))
		}
		if !genericBaseURLSet {
			switch {
			case strings.TrimSpace(lookup("OLLAMA_HOST")) != "":
				cfg.BaseURL = strings.TrimRight(strings.TrimSpace(lookup("OLLAMA_HOST")), "/") + "/v1"
			case strings.TrimSpace(lookup("OPENAI_BASE_URL")) != "":
				cfg.BaseURL = strings.TrimSpace(lookup("OPENAI_BASE_URL"))
			default:
				cfg.BaseURL = modelrouting.DefaultOpenAIBaseURL
			}
		}
	case modelrouting.ProviderXAI:
		if !genericCredentialSet {
			cfg.AuthToken = ""
			cfg.APIKey = strings.TrimSpace(lookup("XAI_API_KEY"))
		}
		if !genericBaseURLSet {
			if value := strings.TrimSpace(lookup("XAI_BASE_URL")); value != "" {
				cfg.BaseURL = value
			} else {
				cfg.BaseURL = modelrouting.DefaultXAIBaseURL
			}
		}
	case modelrouting.ProviderDashScope:
		if !genericCredentialSet {
			cfg.AuthToken = ""
			cfg.APIKey = strings.TrimSpace(lookup("DASHSCOPE_API_KEY"))
		}
		if !genericBaseURLSet {
			if value := strings.TrimSpace(lookup("DASHSCOPE_BASE_URL")); value != "" {
				cfg.BaseURL = value
			} else {
				cfg.BaseURL = modelrouting.DefaultDashScopeBaseURL
			}
		}
	}
}

func applyFlags(cfg *Config, overrides FlagOverrides) {
	if overrides.Model != "" {
		cfg.Model = overrides.Model
	}
	if overrides.BaseURL != "" {
		cfg.BaseURL = overrides.BaseURL
	}
	if overrides.SystemPrompt != "" {
		cfg.SystemPrompt = overrides.SystemPrompt
	}
	if overrides.AppendPrompt != "" {
		cfg.AppendSystemPrompt = joinPromptAppend(cfg.AppendSystemPrompt, overrides.AppendPrompt)
	}
	if overrides.PermissionMode != "" {
		cfg.PermissionMode = overrides.PermissionMode
		cfg.PlanMode = false
	}
	if overrides.SkipPermissions {
		cfg.PermissionMode = "allow"
		cfg.PlanMode = false
	}
	if len(overrides.AllowedTools) > 0 {
		cfg.PermissionRules.Allow = append(cfg.PermissionRules.Allow, overrides.AllowedTools...)
	}
	if len(overrides.DisallowedTools) > 0 {
		cfg.PermissionRules.DeniedTools = append(cfg.PermissionRules.DeniedTools, overrides.DisallowedTools...)
	}
	if overrides.MaxTurns != 0 {
		cfg.MaxTurns = overrides.MaxTurns
	}
	if overrides.MaxTokens != 0 {
		cfg.MaxTokens = overrides.MaxTokens
	}
	if overrides.Temperature != nil {
		value := *overrides.Temperature
		cfg.Temperature = &value
	}
}

func applyAPIKeyHelper(cfg *Config) error {
	command := strings.TrimSpace(cfg.APIKeyHelper)
	if command == "" || strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.AuthToken) != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyHelperTimeout)
	defer cancel()
	cmd := shellCommandContext(ctx, command)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("apiKeyHelper timed out after %s", apiKeyHelperTimeout)
		}
		return fmt.Errorf("apiKeyHelper failed: %w", err)
	}
	key := firstNonEmptyLine(stdout.String())
	if key == "" {
		return errors.New("apiKeyHelper returned no API key")
	}
	cfg.APIKey = key
	return nil
}

func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func firstNonEmptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func applyManagedPolicy(cfg *Config) error {
	if cfg.Future.EnterprisePolicy == "" {
		return nil
	}
	policy, err := LoadManagedPolicyFile(cfg.Future.EnterprisePolicy)
	if err != nil {
		return err
	}
	if cfg.Future.EnterprisePolicyPublicKey != "" {
		if err := VerifyManagedPolicy(policy, cfg.Future.EnterprisePolicyPublicKey); err != nil {
			return err
		}
	}
	if policy.MaxPermissionMode != "" && permissionRank(policy.MaxPermissionMode) < permissionRank(cfg.PermissionMode) {
		cfg.PermissionMode = policy.MaxPermissionMode
	}
	mergePermissionRules(&cfg.PermissionRules, policy.PermissionRules)
	cfg.PermissionRules.DeniedTools = append(cfg.PermissionRules.DeniedTools, policy.DeniedTools...)
	return nil
}

func LoadManagedPolicyFile(path string) (ManagedPolicy, error) {
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return ManagedPolicy{}, err
	}
	var policy ManagedPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return ManagedPolicy{}, err
	}
	return policy, nil
}

func VerifyManagedPolicyFile(path, publicKey string) (ManagedPolicy, error) {
	policy, err := LoadManagedPolicyFile(path)
	if err != nil {
		return ManagedPolicy{}, err
	}
	if err := VerifyManagedPolicy(policy, publicKey); err != nil {
		return ManagedPolicy{}, err
	}
	return policy, nil
}

func VerifyManagedPolicy(policy ManagedPolicy, publicKey string) error {
	if policy.Signature == "" {
		return fmt.Errorf("managed policy signature is required")
	}
	payload, err := ManagedPolicyPayload(policy)
	if err != nil {
		return err
	}
	if err := signing.VerifyEd25519(publicKey, policy.Signature, payload); err != nil {
		if strings.Contains(err.Error(), "signature verification failed") {
			return fmt.Errorf("managed policy %w", err)
		}
		return err
	}
	return nil
}

func ManagedPolicyPayload(policy ManagedPolicy) ([]byte, error) {
	policy.Signature = ""
	return json.Marshal(policy)
}

func permissionRank(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "read-only":
		return 1
	case "workspace-write":
		return 2
	case "prompt":
		return 3
	case "danger-full-access":
		return 4
	case "allow":
		return 5
	default:
		return 0
	}
}

func validatePermissionMode(cfg *Config) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.PermissionMode))
	switch mode {
	case "read-only", "workspace-write", "danger-full-access", "prompt", "allow":
		cfg.PermissionMode = mode
		return nil
	default:
		return fmt.Errorf("invalid_permission_mode: unknown permission mode %q", cfg.PermissionMode)
	}
}

func validatePermissionRulesDefaultMode(cfg *Config) error {
	if strings.TrimSpace(cfg.PermissionRules.DefaultMode) == "" {
		return nil
	}
	_, _, canonical, ok := mapClaudePermissionDefaultMode(cfg.PermissionRules.DefaultMode)
	if !ok {
		return fmt.Errorf("invalid_permission_default_mode: unknown permissions.defaultMode %q", cfg.PermissionRules.DefaultMode)
	}
	cfg.PermissionRules.DefaultMode = canonical
	return nil
}

func mapClaudePermissionDefaultMode(mode string) (string, bool, string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "default":
		return "prompt", false, "default", true
	case "plan":
		return "read-only", true, "plan", true
	case "acceptedits":
		return "workspace-write", false, "acceptEdits", true
	case "bypasspermissions":
		return "allow", false, "bypassPermissions", true
	case "dontask":
		return "read-only", false, "dontAsk", true
	case "auto":
		return "prompt", false, "auto", true
	default:
		return "", false, "", false
	}
}

func validateForceLoginMethod(cfg *Config) error {
	method := strings.ToLower(strings.TrimSpace(cfg.ForceLoginMethod))
	switch method {
	case "":
		cfg.ForceLoginMethod = ""
		return nil
	case "claudeai", "console":
		cfg.ForceLoginMethod = method
		return nil
	default:
		return fmt.Errorf("invalid_force_login_method: unknown forceLoginMethod %q", cfg.ForceLoginMethod)
	}
}

func validateDefaultShell(cfg *Config) error {
	shell := strings.ToLower(strings.TrimSpace(cfg.DefaultShell))
	switch shell {
	case "":
		cfg.DefaultShell = ""
		return nil
	case "bash", "powershell":
		cfg.DefaultShell = shell
		return nil
	default:
		return fmt.Errorf("invalid_default_shell: defaultShell must be %q or %q", "bash", "powershell")
	}
}

func validateStatusLineConfig(cfg *Config) error {
	if cfg.StatusLine == nil {
		return nil
	}
	typ := strings.TrimSpace(cfg.StatusLine.Type)
	if typ != "command" {
		return fmt.Errorf("invalid_status_line: statusLine.type must be %q", "command")
	}
	command := strings.TrimSpace(cfg.StatusLine.Command)
	if command == "" {
		return fmt.Errorf("invalid_status_line: statusLine.command is required")
	}
	cfg.StatusLine.Type = typ
	cfg.StatusLine.Command = command
	if cfg.StatusLine.Padding != nil && *cfg.StatusLine.Padding < 0 {
		return fmt.Errorf("invalid_status_line: statusLine.padding must be non-negative")
	}
	return nil
}

func validateTemperature(cfg *Config) error {
	if cfg.Temperature == nil {
		return nil
	}
	value := *cfg.Temperature
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("invalid_temperature: temperature must be between 0 and 1")
	}
	return nil
}

func validateSandboxConfig(cfg SandboxConfig) error {
	mode := strings.TrimSpace(cfg.FilesystemMode)
	if mode == "" {
		return nil
	}
	switch mode {
	case "off", "workspace-only", "allow-list":
		return nil
	default:
		return fmt.Errorf("invalid_sandbox_config: unsupported filesystem mode %q", cfg.FilesystemMode)
	}
}

func validateAPITimeoutConfig(cfg APITimeoutConfig) error {
	if cfg.ConnectTimeoutSeconds < 0 {
		return fmt.Errorf("invalid_api_timeout: connectTimeout must be non-negative")
	}
	if cfg.RequestTimeoutSeconds < 0 {
		return fmt.Errorf("invalid_api_timeout: requestTimeout must be non-negative")
	}
	if cfg.MaxRetries < 0 {
		return fmt.Errorf("invalid_api_timeout: maxRetries must be non-negative")
	}
	return nil
}

func defaultConfigHome() (string, error) {
	if value := os.Getenv("CODOG_CONFIG_HOME"); value != "" {
		return expandHome(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codog"), nil
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func splitPathList(value string) []string {
	parts := filepath.SplitList(value)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBoolEnv(name string, lookup func(string) string) (bool, bool) {
	value := strings.TrimSpace(lookup(name))
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}
