package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/signing"
)

const (
	DefaultBaseURL = "https://api.anthropic.com"
	DefaultModel   = "claude-sonnet-4-5"
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

type PrivacyConfig struct {
	TelemetryEnabled     *bool `json:"telemetry_enabled,omitempty"`
	CrashReportsEnabled  *bool `json:"crash_reports_enabled,omitempty"`
	PromptHistoryEnabled *bool `json:"prompt_history_enabled,omitempty"`
}

type MCPServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
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
	UpdaterManifestURL        string            `json:"updater_manifest_url,omitempty"`
	EditorBridgeSocket        string            `json:"editor_bridge_socket,omitempty"`
	EditorBridgeToken         string            `json:"editor_bridge_token,omitempty"`
	BackgroundStatePath       string            `json:"background_state_path,omitempty"`
	ChromeDefaultEnabled      *bool             `json:"chrome_default_enabled,omitempty"`
	SlackAppInstallCount      int               `json:"slack_app_install_count,omitempty"`
	StickerOrderCount         int               `json:"sticker_order_count,omitempty"`
	ExtraUsageVisitCount      int               `json:"extra_usage_visit_count,omitempty"`
	GuestPassReferralURL      string            `json:"guest_pass_referral_url,omitempty"`
	GuestPassVisitCount       int               `json:"guest_pass_visit_count,omitempty"`
}

type PermissionRules struct {
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
	Ask         []string `json:"ask,omitempty"`
	DeniedTools []string `json:"denied_tools,omitempty"`
}

type ManagedPolicy struct {
	MaxPermissionMode string          `json:"max_permission_mode,omitempty"`
	PermissionRules   PermissionRules `json:"permission_rules,omitempty"`
	DeniedTools       []string        `json:"denied_tools,omitempty"`
	Signature         string          `json:"signature,omitempty"`
}

type Config struct {
	APIKey              string                     `json:"api_key,omitempty"`
	AuthToken           string                     `json:"auth_token,omitempty"`
	BaseURL             string                     `json:"base_url,omitempty"`
	Model               string                     `json:"model,omitempty"`
	AdvisorModel        string                     `json:"advisor_model,omitempty"`
	SystemPrompt        string                     `json:"system_prompt,omitempty"`
	AppendSystemPrompt  string                     `json:"append_system_prompt,omitempty"`
	Theme               string                     `json:"theme,omitempty"`
	EditorMode          string                     `json:"editorMode,omitempty"`
	ReasoningEffort     string                     `json:"reasoning_effort,omitempty"`
	FastMode            *bool                      `json:"fast_mode,omitempty"`
	VoiceEnabled        *bool                      `json:"voice_enabled,omitempty"`
	VoiceCommand        string                     `json:"voice_command,omitempty"`
	SpeechCommand       string                     `json:"speech_command,omitempty"`
	MaxTokens           int                        `json:"max_tokens,omitempty"`
	MaxTurns            int                        `json:"max_turns,omitempty"`
	PermissionMode      string                     `json:"permission_mode,omitempty"`
	PlanMode            bool                       `json:"-"`
	Privacy             PrivacyConfig              `json:"privacy_settings,omitempty"`
	PermissionRules     PermissionRules            `json:"permission_rules,omitempty"`
	ConfigHome          string                     `json:"config_home,omitempty"`
	AutoCompactMessages int                        `json:"auto_compact_messages,omitempty"`
	RateLimit           RateLimitConfig            `json:"rate_limit,omitempty"`
	AdditionalDirs      []string                   `json:"additional_dirs,omitempty"`
	EnabledSkills       []string                   `json:"enabled_skills,omitempty"`
	Hooks               HookConfig                 `json:"hooks,omitempty"`
	MCPServers          map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	Future              FutureConfig               `json:"future,omitempty"`
}

type MutationReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Path   string `json:"path"`
	Key    string `json:"key"`
}

type FlagOverrides struct {
	ConfigPath      string
	SessionID       string
	Resume          string
	Model           string
	BaseURL         string
	SystemPrompt    string
	AppendPrompt    string
	PermissionMode  string
	SkipPermissions bool
	AllowBroadCWD   bool
	AllowedTools    []string
	DisallowedTools []string
	MaxTurns        int
	MaxTokens       int
}

func Load(overrides FlagOverrides) (Config, error) {
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

	for _, path := range configPaths(home, overrides.ConfigPath) {
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
	if err := applyManagedPolicy(&cfg); err != nil {
		return Config{}, err
	}

	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 8
	}
	if cfg.AutoCompactMessages <= 0 {
		cfg.AutoCompactMessages = 40
	}
	if err := validatePermissionMode(&cfg); err != nil {
		return Config{}, err
	}
	cfg.RateLimit = NormalizeRateLimitConfig(cfg.RateLimit)
	return cfg, nil
}

func LoadForInspection(overrides FlagOverrides) (Config, []string, error) {
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
		return Config{}, nil, err
	}
	cfg.ConfigHome = home
	paths := configPaths(home, overrides.ConfigPath)
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
	applyEnv(&cfg)
	applyFlags(&cfg, overrides)
	if err := applyManagedPolicy(&cfg); err != nil {
		return Config{}, paths, err
	}
	if err := validatePermissionMode(&cfg); err != nil {
		return Config{}, paths, err
	}
	cfg.RateLimit = NormalizeRateLimitConfig(cfg.RateLimit)
	return cfg, paths, nil
}

func configPaths(home, explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return []string{
		filepath.Join(home, "config.json"),
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
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
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

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func merge(dst *Config, src Config) {
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.AuthToken != "" {
		dst.AuthToken = src.AuthToken
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
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.EditorMode != "" {
		dst.EditorMode = src.EditorMode
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
	if src.PermissionMode != "" {
		dst.PermissionMode = src.PermissionMode
	}
	if privacyConfigSet(src.Privacy) {
		mergePrivacyConfig(&dst.Privacy, src.Privacy)
	}
	if permissionRulesSet(src.PermissionRules) {
		mergePermissionRules(&dst.PermissionRules, src.PermissionRules)
	}
	if src.ConfigHome != "" {
		dst.ConfigHome = expandHome(src.ConfigHome)
	}
	if src.AutoCompactMessages != 0 {
		dst.AutoCompactMessages = src.AutoCompactMessages
	}
	if rateLimitConfigSet(src.RateLimit) {
		mergeRateLimitConfig(&dst.RateLimit, src.RateLimit)
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
		dst.Future = src.Future
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
		cfg.UpdaterManifestURL != "" ||
		cfg.EditorBridgeSocket != "" ||
		cfg.EditorBridgeToken != "" ||
		cfg.BackgroundStatePath != "" ||
		cfg.ChromeDefaultEnabled != nil ||
		cfg.SlackAppInstallCount != 0 ||
		cfg.StickerOrderCount != 0 ||
		cfg.ExtraUsageVisitCount != 0 ||
		cfg.GuestPassReferralURL != "" ||
		cfg.GuestPassVisitCount != 0
}

func permissionRulesSet(rules PermissionRules) bool {
	return len(rules.Allow) != 0 ||
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
	dst.Allow = append(dst.Allow, src.Allow...)
	dst.Deny = append(dst.Deny, src.Deny...)
	dst.Ask = append(dst.Ask, src.Ask...)
	dst.DeniedTools = append(dst.DeniedTools, src.DeniedTools...)
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
	if value := os.Getenv("ANTHROPIC_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	if value := os.Getenv("ANTHROPIC_AUTH_TOKEN"); value != "" {
		cfg.AuthToken = value
	}
	if value := os.Getenv("ANTHROPIC_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := os.Getenv("CODOG_BASE_URL"); value != "" {
		cfg.BaseURL = value
	}
	if value := os.Getenv("CODOG_MODEL"); value != "" {
		cfg.Model = value
	}
	if value := os.Getenv("CODOG_API_KEY"); value != "" {
		cfg.APIKey = value
	}
	if value := os.Getenv("CODOG_AUTH_TOKEN"); value != "" {
		cfg.AuthToken = value
	}
	if strings.HasPrefix(strings.TrimSpace(cfg.Model), "openai/") {
		if value := os.Getenv("OPENAI_API_KEY"); value != "" && cfg.APIKey == "" {
			cfg.APIKey = value
		}
		if value := os.Getenv("OPENAI_BASE_URL"); value != "" {
			cfg.BaseURL = value
		}
	}
	if value := os.Getenv("CODOG_ADVISOR_MODEL"); value != "" {
		cfg.AdvisorModel = value
	}
	if value := os.Getenv("CODOG_SYSTEM_PROMPT"); value != "" {
		cfg.SystemPrompt = value
	}
	if value := os.Getenv("CODOG_APPEND_SYSTEM_PROMPT"); value != "" {
		cfg.AppendSystemPrompt = joinPromptAppend(cfg.AppendSystemPrompt, value)
	}
	if value := os.Getenv("CODOG_THEME"); value != "" {
		cfg.Theme = value
	}
	if value := os.Getenv("CODOG_EDITOR_MODE"); value != "" {
		cfg.EditorMode = value
	}
	if value := os.Getenv("CODOG_REASONING_EFFORT"); value != "" {
		cfg.ReasoningEffort = value
	}
	if value, ok := parseBoolEnv("CODOG_FAST_MODE"); ok {
		cfg.FastMode = &value
	}
	if value, ok := parseBoolEnv("CODOG_VOICE_ENABLED"); ok {
		cfg.VoiceEnabled = &value
	}
	if value := os.Getenv("CODOG_VOICE_COMMAND"); value != "" {
		cfg.VoiceCommand = value
	}
	if value := os.Getenv("CODOG_SPEECH_COMMAND"); value != "" {
		cfg.SpeechCommand = value
	}
	if value := os.Getenv("CODOG_PERMISSION_MODE"); value != "" {
		cfg.PermissionMode = value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_TELEMETRY_ENABLED"); ok {
		cfg.Privacy.TelemetryEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_CRASH_REPORTS_ENABLED"); ok {
		cfg.Privacy.CrashReportsEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_PRIVACY_PROMPT_HISTORY_ENABLED"); ok {
		cfg.Privacy.PromptHistoryEnabled = &value
	}
	if value, ok := parseBoolEnv("CODOG_CHROME_DEFAULT_ENABLED"); ok {
		cfg.Future.ChromeDefaultEnabled = &value
	}
	if value := os.Getenv("CODOG_CONFIG_HOME"); value != "" {
		cfg.ConfigHome = expandHome(value)
	}
	if value := os.Getenv("CODOG_MAX_TURNS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxTurns = parsed
		}
	}
	if value := os.Getenv("CODOG_RATE_LIMIT_MAX_RETRIES"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.MaxRetries = parsed
		}
	}
	if value := os.Getenv("CODOG_RATE_LIMIT_INITIAL_BACKOFF_MS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.InitialBackoffMS = parsed
		}
	}
	if value := os.Getenv("CODOG_RATE_LIMIT_MAX_BACKOFF_MS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.RateLimit.MaxBackoffMS = parsed
		}
	}
	if value := os.Getenv("CODOG_ADDITIONAL_DIRS"); value != "" {
		cfg.AdditionalDirs = splitPathList(value)
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
	}
	if overrides.SkipPermissions {
		cfg.PermissionMode = "allow"
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

func parseBoolEnv(name string) (bool, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}
