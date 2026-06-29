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
	PreToolUse  []string `json:"pre_tool_use,omitempty"`
	PostToolUse []string `json:"post_tool_use,omitempty"`
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
	MaxTokens           int                        `json:"max_tokens,omitempty"`
	MaxTurns            int                        `json:"max_turns,omitempty"`
	PermissionMode      string                     `json:"permission_mode,omitempty"`
	PermissionRules     PermissionRules            `json:"permission_rules,omitempty"`
	ConfigHome          string                     `json:"config_home,omitempty"`
	AutoCompactMessages int                        `json:"auto_compact_messages,omitempty"`
	EnabledSkills       []string                   `json:"enabled_skills,omitempty"`
	Hooks               HookConfig                 `json:"hooks,omitempty"`
	MCPServers          map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	Future              FutureConfig               `json:"future,omitempty"`
}

type FlagOverrides struct {
	ConfigPath     string
	SessionID      string
	Resume         string
	Model          string
	BaseURL        string
	PermissionMode string
	MaxTurns       int
	MaxTokens      int
}

func Load(overrides FlagOverrides) (Config, error) {
	cfg := Config{
		BaseURL:             DefaultBaseURL,
		Model:               DefaultModel,
		MaxTokens:           4096,
		MaxTurns:            8,
		PermissionMode:      "workspace-write",
		AutoCompactMessages: 40,
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
	return cfg, paths, nil
}

func configPaths(home, explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	return []string{
		filepath.Join(home, "config.json"),
		".codog.json",
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
	if src.MaxTokens != 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if src.MaxTurns != 0 {
		dst.MaxTurns = src.MaxTurns
	}
	if src.PermissionMode != "" {
		dst.PermissionMode = src.PermissionMode
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
	if len(src.EnabledSkills) != 0 {
		dst.EnabledSkills = append([]string(nil), src.EnabledSkills...)
	}
	if len(src.Hooks.PreToolUse) != 0 {
		dst.Hooks.PreToolUse = append([]string(nil), src.Hooks.PreToolUse...)
	}
	if len(src.Hooks.PostToolUse) != 0 {
		dst.Hooks.PostToolUse = append([]string(nil), src.Hooks.PostToolUse...)
	}
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

func mergePermissionRules(dst *PermissionRules, src PermissionRules) {
	dst.Allow = append(dst.Allow, src.Allow...)
	dst.Deny = append(dst.Deny, src.Deny...)
	dst.Ask = append(dst.Ask, src.Ask...)
	dst.DeniedTools = append(dst.DeniedTools, src.DeniedTools...)
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
	if value := os.Getenv("CODOG_PERMISSION_MODE"); value != "" {
		cfg.PermissionMode = value
	}
	if value := os.Getenv("CODOG_CONFIG_HOME"); value != "" {
		cfg.ConfigHome = expandHome(value)
	}
	if value := os.Getenv("CODOG_MAX_TURNS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.MaxTurns = parsed
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
	if overrides.PermissionMode != "" {
		cfg.PermissionMode = overrides.PermissionMode
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
