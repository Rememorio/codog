package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAppliesManagedPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, []byte(`{"max_permission_mode":"read-only","denied_tools":["bash"],"permission_rules":{"deny":["write_file"]}}`), 0o644))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","future":{"enterprise_policy":"`+policyPath+`"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Contains(t, cfg.PermissionRules.DeniedTools, "bash")
	require.Contains(t, cfg.PermissionRules.Deny, "write_file")
}

func TestLoadVerifiesSignedManagedPolicy(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	policy := ManagedPolicy{
		MaxPermissionMode: "read-only",
		DeniedTools:       []string{"bash"},
	}
	writeSignedPolicy(t, policyPath, policy, privateKey)
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","future":{"enterprise_policy":"`+policyPath+`","enterprise_policy_public_key":"`+base64.StdEncoding.EncodeToString(publicKey)+`"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Contains(t, cfg.PermissionRules.DeniedTools, "bash")
}

func TestLoadRejectsTamperedSignedManagedPolicy(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	policy := ManagedPolicy{MaxPermissionMode: "read-only"}
	signed := writeSignedPolicy(t, policyPath, policy, privateKey)
	signed.MaxPermissionMode = "danger-full-access"
	data, err := json.Marshal(signed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, data, 0o644))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","future":{"enterprise_policy":"`+policyPath+`","enterprise_policy_public_key":"`+base64.StdEncoding.EncodeToString(publicKey)+`"}}`), 0o644))

	_, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "managed policy signature verification failed")
}

func TestMergeAppendsPermissionRules(t *testing.T) {
	dst := Config{
		PermissionRules: PermissionRules{
			Allow:       []string{"read_file"},
			DeniedTools: []string{"bash"},
		},
	}
	merge(&dst, Config{
		PermissionRules: PermissionRules{
			Deny:        []string{"write_file"},
			Ask:         []string{"edit_file"},
			DeniedTools: []string{"plugin_tool"},
		},
	})

	require.Equal(t, []string{"read_file"}, dst.PermissionRules.Allow)
	require.Equal(t, []string{"write_file"}, dst.PermissionRules.Deny)
	require.Equal(t, []string{"edit_file"}, dst.PermissionRules.Ask)
	require.Equal(t, []string{"bash", "plugin_tool"}, dst.PermissionRules.DeniedTools)
}

func TestLoadRemoteAuthToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"future":{"remote_auth_token":"secret-token","remote_lease_seconds":30}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "secret-token", cfg.Future.RemoteAuthToken)
	require.Equal(t, 30, cfg.Future.RemoteLeaseSeconds)
}

func TestLoadRateLimitConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"rate_limit":{"max_retries":4,"initial_backoff_ms":250,"max_backoff_ms":2000}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, 4, cfg.RateLimit.MaxRetries)
	require.Equal(t, 250, cfg.RateLimit.InitialBackoffMS)
	require.Equal(t, 2000, cfg.RateLimit.MaxBackoffMS)
}

func TestLoadRateLimitEnvOverrides(t *testing.T) {
	t.Setenv("CODOG_RATE_LIMIT_MAX_RETRIES", "5")
	t.Setenv("CODOG_RATE_LIMIT_INITIAL_BACKOFF_MS", "100")
	t.Setenv("CODOG_RATE_LIMIT_MAX_BACKOFF_MS", "300")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, 5, cfg.RateLimit.MaxRetries)
	require.Equal(t, 100, cfg.RateLimit.InitialBackoffMS)
	require.Equal(t, 300, cfg.RateLimit.MaxBackoffMS)
}

func TestLoadHooksSupportsSimpleAndDocumentedFormats(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"hooks": {
			"pre_tool_use": ["echo simple-pre"],
			"PostToolUse": [
				{"matcher": "Write", "hooks": [{"type": "command", "command": "echo documented-post"}]},
				{"command": "echo direct-post"}
			]
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, []string{"echo simple-pre"}, cfg.Hooks.PreToolUse)
	require.Equal(t, []string{"echo documented-post", "echo direct-post"}, cfg.Hooks.PostToolUse)
	require.Equal(t, []HookCommand{{Command: "echo simple-pre"}}, cfg.Hooks.PreToolUseCommands)
	require.Equal(t, []HookCommand{
		{Matcher: "Write", Command: "echo documented-post"},
		{Command: "echo direct-post"},
	}, cfg.Hooks.PostToolUseCommands)
}

func TestLoadAdditionalDirsConfigAndEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"additional_dirs":["../shared","/tmp/example"]}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, []string{"../shared", "/tmp/example"}, cfg.AdditionalDirs)

	t.Setenv("CODOG_ADDITIONAL_DIRS", "one"+string(os.PathListSeparator)+"two")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, cfg.AdditionalDirs)
}

func TestLoadEditorBridgeToken(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"future":{"editor_bridge_token":"bridge-token"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "bridge-token", cfg.Future.EditorBridgeToken)
}

func TestLoadProjectLocalOverridesSharedConfig(t *testing.T) {
	workspace := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"model":"shared-model","permission_mode":"read-only"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.local.json"), []byte(`{"model":"local-model"}`), 0o644))

	cfg, paths, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "local-model", cfg.Model)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Contains(t, paths, ".codog.json")
	require.Contains(t, paths, ".codog.local.json")
}

func TestSetAndUnsetFileValue(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"old","future":{"sandbox_strategy":"detect"}}`), 0o644))

	report, err := SetFileValue(configPath, "model", "new-model")
	require.NoError(t, err)
	require.Equal(t, "set", report.Action)
	require.Equal(t, "model", report.Key)
	report, err = SetFileValue(configPath, "rate_limit.max_retries", float64(4))
	require.NoError(t, err)
	require.Equal(t, "rate_limit.max_retries", report.Key)
	report, err = UnsetFileValue(configPath, "future.sandbox_strategy")
	require.NoError(t, err)
	require.Equal(t, "unset", report.Action)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, "new-model", raw["model"])
	require.Equal(t, float64(4), raw["rate_limit"].(map[string]any)["max_retries"])
	require.NotContains(t, raw, "future")
}

func TestParseConfigValue(t *testing.T) {
	require.Equal(t, "claude-sonnet", ParseConfigValue("claude-sonnet"))
	require.Equal(t, true, ParseConfigValue("true"))
	require.Equal(t, float64(42), ParseConfigValue("42"))
	require.Equal(t, []any{"read_file"}, ParseConfigValue(`["read_file"]`))
}

func writeSignedPolicy(t *testing.T, path string, policy ManagedPolicy, privateKey ed25519.PrivateKey) ManagedPolicy {
	t.Helper()
	payload, err := ManagedPolicyPayload(policy)
	require.NoError(t, err)
	policy.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return policy
}
