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
	PreToolUse          []string      `json:"pre_tool_use,omitempty"`
	PostToolUse         []string      `json:"post_tool_use,omitempty"`
	PreToolUseCommands  []HookCommand `json:"-"`
	PostToolUseCommands []HookCommand `json:"-"`
}

type HookCommand struct {
	Matcher string
	Command string
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
	h.PreToolUseCommands = pre
	h.PostToolUseCommands = post
	h.PreToolUse = hookCommandStrings(pre)
	h.PostToolUse = hookCommandStrings(post)
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
	SystemPrompt        string                     `json:"system_prompt,omitempty"`
	AppendSystemPrompt  string                     `json:"append_system_prompt,omitempty"`
	Theme               string                     `json:"theme,omitempty"`
	EditorMode          string                     `json:"editorMode,omitempty"`
	ReasoningEffort     string                     `json:"reasoning_effort,omitempty"`
	FastMode            *bool                      `json:"fast_mode,omitempty"`
	MaxTokens           int                        `json:"max_tokens,omitempty"`
	MaxTurns            int                        `json:"max_turns,omitempty"`
	PermissionMode      string                     `json:"permission_mode,omitempty"`
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
		return []HookCommand{{Matcher: inheritedMatcher, Command: command}}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	matcher := inheritedMatcher
	if rawMatcher, ok := object["matcher"]; ok {
		var parsed string
		if err := json.Unmarshal(rawMatcher, &parsed); err == nil {
			matcher = parsed
		}
	}
	if rawCommand, ok := object["command"]; ok {
		if err := json.Unmarshal(rawCommand, &command); err == nil {
			return []HookCommand{{Matcher: matcher, Command: command}}, nil
		}
	}
	if rawHooks, ok := object["hooks"]; ok {
		return parseHookCommandListWithMatcher(rawHooks, matcher)
	}
	return nil, nil
}

func hookCommandStrings(values []HookCommand) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		command := strings.TrimSpace(value.Command)
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
		value.Command = strings.TrimSpace(value.Command)
		if value.Command != "" {
			out = append(out, value)
		}
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
	if src.ReasoningEffort != "" {
		dst.ReasoningEffort = src.ReasoningEffort
	}
	if src.FastMode != nil {
		value := *src.FastMode
		dst.FastMode = &value
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
		cfg.BackgroundStatePath != ""
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
	dst.PreToolUse = hookCommandStrings(dst.PreToolUseCommands)
	dst.PostToolUse = hookCommandStrings(dst.PostToolUseCommands)
}

func hookCommandsFromStrings(values []string) []HookCommand {
	commands := make([]HookCommand, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			commands = append(commands, HookCommand{Command: value})
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
	return strings.ToLower(strings.TrimSpace(command.Matcher)) + "\x00" + strings.TrimSpace(command.Command)
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
	switch mode {
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
