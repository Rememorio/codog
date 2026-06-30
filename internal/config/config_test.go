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

func TestLoadInterfaceAndPrivacyPreferences(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"theme": "dark",
		"editorMode": "vim",
		"advisor_model": "claude-opus-test",
		"reasoning_effort": "high",
		"fast_mode": true,
		"voice_enabled": true,
		"voice_command": "cat",
		"future": {
			"chrome_default_enabled": true,
			"slack_app_install_count": 3,
			"sticker_order_count": 2,
			"extra_usage_visit_count": 4,
			"guest_pass_referral_url": "https://example.test/pass",
			"guest_pass_visit_count": 5
		},
		"privacy_settings": {
			"telemetry_enabled": true,
			"crash_reports_enabled": false,
			"prompt_history_enabled": false
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "dark", cfg.Theme)
	require.Equal(t, "vim", cfg.EditorMode)
	require.Equal(t, "claude-opus-test", cfg.AdvisorModel)
	require.Equal(t, "high", cfg.ReasoningEffort)
	require.NotNil(t, cfg.FastMode)
	require.True(t, *cfg.FastMode)
	require.NotNil(t, cfg.VoiceEnabled)
	require.True(t, *cfg.VoiceEnabled)
	require.Equal(t, "cat", cfg.VoiceCommand)
	require.NotNil(t, cfg.Future.ChromeDefaultEnabled)
	require.True(t, *cfg.Future.ChromeDefaultEnabled)
	require.Equal(t, 3, cfg.Future.SlackAppInstallCount)
	require.Equal(t, 2, cfg.Future.StickerOrderCount)
	require.Equal(t, 4, cfg.Future.ExtraUsageVisitCount)
	require.Equal(t, "https://example.test/pass", cfg.Future.GuestPassReferralURL)
	require.Equal(t, 5, cfg.Future.GuestPassVisitCount)
	require.NotNil(t, cfg.Privacy.TelemetryEnabled)
	require.True(t, *cfg.Privacy.TelemetryEnabled)
	require.NotNil(t, cfg.Privacy.CrashReportsEnabled)
	require.False(t, *cfg.Privacy.CrashReportsEnabled)
	require.NotNil(t, cfg.Privacy.PromptHistoryEnabled)
	require.False(t, *cfg.Privacy.PromptHistoryEnabled)

	t.Setenv("CODOG_THEME", "light")
	t.Setenv("CODOG_EDITOR_MODE", "default")
	t.Setenv("CODOG_ADVISOR_MODEL", "claude-sonnet-advisor")
	t.Setenv("CODOG_REASONING_EFFORT", "low")
	t.Setenv("CODOG_FAST_MODE", "false")
	t.Setenv("CODOG_VOICE_ENABLED", "false")
	t.Setenv("CODOG_VOICE_COMMAND", "printf")
	t.Setenv("CODOG_PRIVACY_PROMPT_HISTORY_ENABLED", "true")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "light", cfg.Theme)
	require.Equal(t, "default", cfg.EditorMode)
	require.Equal(t, "claude-sonnet-advisor", cfg.AdvisorModel)
	require.Equal(t, "low", cfg.ReasoningEffort)
	require.NotNil(t, cfg.FastMode)
	require.False(t, *cfg.FastMode)
	require.NotNil(t, cfg.VoiceEnabled)
	require.False(t, *cfg.VoiceEnabled)
	require.Equal(t, "printf", cfg.VoiceCommand)
	require.NotNil(t, cfg.Privacy.PromptHistoryEnabled)
	require.True(t, *cfg.Privacy.PromptHistoryEnabled)
}

func TestLoadFutureClickCountersOnly(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"future": {
			"sticker_order_count": 2,
			"extra_usage_visit_count": 4,
			"guest_pass_referral_url": "https://example.test/pass",
			"guest_pass_visit_count": 5
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Future.StickerOrderCount)
	require.Equal(t, 4, cfg.Future.ExtraUsageVisitCount)
	require.Equal(t, "https://example.test/pass", cfg.Future.GuestPassReferralURL)
	require.Equal(t, 5, cfg.Future.GuestPassVisitCount)
}

func TestLoadSkipPermissionsFlagOverridesPermissionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"read-only"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath, PermissionMode: "workspace-write", SkipPermissions: true})
	require.NoError(t, err)
	require.Equal(t, "allow", cfg.PermissionMode)
}

func TestLoadPermissionRuleFlagOverrides(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_rules":{"allow":["read_file"],"denied_tools":["bash"]}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath:      configPath,
		AllowedTools:    []string{"grep", "glob"},
		DisallowedTools: []string{"write_file"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"read_file", "grep", "glob"}, cfg.PermissionRules.Allow)
	require.Equal(t, []string{"bash", "write_file"}, cfg.PermissionRules.DeniedTools)
}

func TestLoadSystemPromptConfigEnvAndFlags(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"system_prompt":"config base","append_system_prompt":"config extra"}`), 0o644))
	t.Setenv("CODOG_SYSTEM_PROMPT", "env base")
	t.Setenv("CODOG_APPEND_SYSTEM_PROMPT", "env extra")

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath:   configPath,
		SystemPrompt: "flag base",
		AppendPrompt: "flag extra",
	})
	require.NoError(t, err)
	require.Equal(t, "flag base", cfg.SystemPrompt)
	require.Equal(t, "config extra\n\nenv extra\n\nflag extra", cfg.AppendSystemPrompt)
}

func TestLoadHooksSupportsSimpleAndDocumentedFormats(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"hooks": {
			"UserPromptSubmit": ["echo prompt-submit"],
			"SessionStart": ["echo session-start"],
			"pre_tool_use": ["echo simple-pre"],
			"PostToolUse": [
				{"matcher": "Write", "hooks": [{"type": "command", "command": "echo documented-post"}]},
				{"matcher": "Bash", "hooks": [{"type": "http", "url": "https://example.test/hook", "if": "Bash(git *)", "headers": {"Authorization": "Bearer $HOOK_TOKEN"}, "allowedEnvVars": ["HOOK_TOKEN"], "timeout": 1.5}]},
				{"command": "echo direct-post"}
			],
			"PostToolUseFailure": ["echo post-failure"],
			"PermissionRequest": [{"matcher": "Bash", "command": "echo permission-request"}],
			"PermissionDenied": [{"matcher": "Bash", "command": "echo permission-denied"}],
			"Stop": [{"hooks": [{"type": "command", "command": "echo stop"}]}],
			"PreCompact": ["echo pre-compact"],
			"PostCompact": ["echo post-compact"],
			"Notification": [{"matcher": "background_*", "command": "echo notify"}],
			"SubagentStart": [{"matcher": "reviewer", "command": "echo agent-start"}],
			"SubagentStop": [{"matcher": "reviewer", "command": "echo agent-stop"}]
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, []string{"echo prompt-submit"}, cfg.Hooks.UserPromptSubmit)
	require.Equal(t, []string{"echo session-start"}, cfg.Hooks.SessionStart)
	require.Equal(t, []string{"echo simple-pre"}, cfg.Hooks.PreToolUse)
	require.Equal(t, []string{"echo documented-post", "http POST https://example.test/hook", "echo direct-post"}, cfg.Hooks.PostToolUse)
	require.Equal(t, []string{"echo post-failure"}, cfg.Hooks.PostToolUseFailure)
	require.Equal(t, []string{"echo permission-request"}, cfg.Hooks.PermissionRequest)
	require.Equal(t, []string{"echo permission-denied"}, cfg.Hooks.PermissionDenied)
	require.Equal(t, []string{"echo stop"}, cfg.Hooks.Stop)
	require.Equal(t, []string{"echo pre-compact"}, cfg.Hooks.PreCompact)
	require.Equal(t, []string{"echo post-compact"}, cfg.Hooks.PostCompact)
	require.Equal(t, []string{"echo notify"}, cfg.Hooks.Notification)
	require.Equal(t, []string{"echo agent-start"}, cfg.Hooks.SubagentStart)
	require.Equal(t, []string{"echo agent-stop"}, cfg.Hooks.SubagentStop)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo prompt-submit"}}, cfg.Hooks.UserPromptSubmitCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo session-start"}}, cfg.Hooks.SessionStartCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo simple-pre"}}, cfg.Hooks.PreToolUseCommands)
	require.Equal(t, []HookCommand{
		{Matcher: "Write", Type: "command", Command: "echo documented-post"},
		{Matcher: "Bash", Type: "http", URL: "https://example.test/hook", If: "Bash(git *)", TimeoutSeconds: 1.5, Headers: map[string]string{"Authorization": "Bearer $HOOK_TOKEN"}, AllowedEnvVars: []string{"HOOK_TOKEN"}},
		{Type: "command", Command: "echo direct-post"},
	}, cfg.Hooks.PostToolUseCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo post-failure"}}, cfg.Hooks.PostToolUseFailureCommands)
	require.Equal(t, []HookCommand{{Matcher: "Bash", Type: "command", Command: "echo permission-request"}}, cfg.Hooks.PermissionRequestCommands)
	require.Equal(t, []HookCommand{{Matcher: "Bash", Type: "command", Command: "echo permission-denied"}}, cfg.Hooks.PermissionDeniedCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo stop"}}, cfg.Hooks.StopCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo pre-compact"}}, cfg.Hooks.PreCompactCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo post-compact"}}, cfg.Hooks.PostCompactCommands)
	require.Equal(t, []HookCommand{{Matcher: "background_*", Type: "command", Command: "echo notify"}}, cfg.Hooks.NotificationCommands)
	require.Equal(t, []HookCommand{{Matcher: "reviewer", Type: "command", Command: "echo agent-start"}}, cfg.Hooks.SubagentStartCommands)
	require.Equal(t, []HookCommand{{Matcher: "reviewer", Type: "command", Command: "echo agent-stop"}}, cfg.Hooks.SubagentStopCommands)
}

func TestLoadMergesHooksAcrossConfigLayers(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	require.NoError(t, os.Chdir(workspace))
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.MkdirAll(configHome, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{
		"hooks": {
			"user_prompt_submit": ["echo user-prompt"],
			"session_start": ["echo user-session"],
			"pre_tool_use": ["echo user-pre"],
			"post_tool_use": ["echo user-post"],
			"post_tool_use_failure": ["echo user-post-failure"],
			"permission_request": ["echo user-permission-request"],
			"permission_denied": ["echo user-permission-denied"],
			"stop": ["echo user-stop"],
			"pre_compact": ["echo user-compact"],
			"post_compact": ["echo user-post-compact"],
			"notification": ["echo user-notification"],
			"subagent_start": ["echo user-subagent-start"],
			"subagent_stop": ["echo user-subagent-stop"]
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{
		"hooks": {
			"UserPromptSubmit": [{"command": "echo project-prompt"}],
			"SessionStart": [{"command": "echo project-session"}],
			"PreToolUse": [
				{"matcher": "Write", "command": "echo project-pre"}
			],
			"PostToolUseFailure": [{"command": "echo project-post-failure"}],
			"PermissionRequest": [{"matcher": "Bash", "command": "echo project-permission-request"}],
			"PermissionDenied": [{"matcher": "Bash", "command": "echo project-permission-denied"}],
			"Stop": [{"command": "echo project-stop"}],
			"PreCompact": [{"command": "echo project-compact"}],
			"PostCompact": [{"command": "echo project-post-compact"}],
			"Notification": [{"matcher": "background_task_started", "command": "echo project-notification"}],
			"SubagentStart": [{"matcher": "reviewer", "command": "echo project-subagent-start"}],
			"SubagentStop": [{"matcher": "reviewer", "command": "echo project-subagent-stop"}]
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.local.json"), []byte(`{
		"hooks": {
			"user_prompt_submit": ["echo user-prompt", "echo local-prompt"],
			"session_start": ["echo user-session", "echo local-session"],
			"pre_tool_use": ["echo user-pre", "echo local-pre"],
			"post_tool_use_failure": ["echo user-post-failure", "echo local-post-failure"],
			"permission_request": ["echo user-permission-request", "echo local-permission-request"],
			"permission_denied": ["echo user-permission-denied", "echo local-permission-denied"],
			"stop": ["echo user-stop", "echo local-stop"],
			"pre_compact": ["echo user-compact", "echo local-compact"],
			"post_compact": ["echo user-post-compact", "echo local-post-compact"],
			"notification": ["echo user-notification", "echo local-notification"],
			"subagent_start": ["echo user-subagent-start", "echo local-subagent-start"],
			"subagent_stop": ["echo user-subagent-stop", "echo local-subagent-stop"]
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"echo user-prompt", "echo project-prompt", "echo local-prompt"}, cfg.Hooks.UserPromptSubmit)
	require.Equal(t, []string{"echo user-session", "echo project-session", "echo local-session"}, cfg.Hooks.SessionStart)
	require.Equal(t, []string{"echo user-pre", "echo project-pre", "echo local-pre"}, cfg.Hooks.PreToolUse)
	require.Equal(t, []string{"echo user-post"}, cfg.Hooks.PostToolUse)
	require.Equal(t, []string{"echo user-post-failure", "echo project-post-failure", "echo local-post-failure"}, cfg.Hooks.PostToolUseFailure)
	require.Equal(t, []string{"echo user-permission-request", "echo project-permission-request", "echo local-permission-request"}, cfg.Hooks.PermissionRequest)
	require.Equal(t, []string{"echo user-permission-denied", "echo project-permission-denied", "echo local-permission-denied"}, cfg.Hooks.PermissionDenied)
	require.Equal(t, []string{"echo user-stop", "echo project-stop", "echo local-stop"}, cfg.Hooks.Stop)
	require.Equal(t, []string{"echo user-compact", "echo project-compact", "echo local-compact"}, cfg.Hooks.PreCompact)
	require.Equal(t, []string{"echo user-post-compact", "echo project-post-compact", "echo local-post-compact"}, cfg.Hooks.PostCompact)
	require.Equal(t, []string{"echo user-notification", "echo project-notification", "echo local-notification"}, cfg.Hooks.Notification)
	require.Equal(t, []string{"echo user-subagent-start", "echo project-subagent-start", "echo local-subagent-start"}, cfg.Hooks.SubagentStart)
	require.Equal(t, []string{"echo user-subagent-stop", "echo project-subagent-stop", "echo local-subagent-stop"}, cfg.Hooks.SubagentStop)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-prompt"},
		{Type: "command", Command: "echo project-prompt"},
		{Type: "command", Command: "echo local-prompt"},
	}, cfg.Hooks.UserPromptSubmitCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-session"},
		{Type: "command", Command: "echo project-session"},
		{Type: "command", Command: "echo local-session"},
	}, cfg.Hooks.SessionStartCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-pre"},
		{Matcher: "Write", Type: "command", Command: "echo project-pre"},
		{Type: "command", Command: "echo local-pre"},
	}, cfg.Hooks.PreToolUseCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-post-failure"},
		{Type: "command", Command: "echo project-post-failure"},
		{Type: "command", Command: "echo local-post-failure"},
	}, cfg.Hooks.PostToolUseFailureCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-permission-request"},
		{Matcher: "Bash", Type: "command", Command: "echo project-permission-request"},
		{Type: "command", Command: "echo local-permission-request"},
	}, cfg.Hooks.PermissionRequestCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-permission-denied"},
		{Matcher: "Bash", Type: "command", Command: "echo project-permission-denied"},
		{Type: "command", Command: "echo local-permission-denied"},
	}, cfg.Hooks.PermissionDeniedCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-stop"},
		{Type: "command", Command: "echo project-stop"},
		{Type: "command", Command: "echo local-stop"},
	}, cfg.Hooks.StopCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-compact"},
		{Type: "command", Command: "echo project-compact"},
		{Type: "command", Command: "echo local-compact"},
	}, cfg.Hooks.PreCompactCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-post-compact"},
		{Type: "command", Command: "echo project-post-compact"},
		{Type: "command", Command: "echo local-post-compact"},
	}, cfg.Hooks.PostCompactCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-notification"},
		{Matcher: "background_task_started", Type: "command", Command: "echo project-notification"},
		{Type: "command", Command: "echo local-notification"},
	}, cfg.Hooks.NotificationCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-subagent-start"},
		{Matcher: "reviewer", Type: "command", Command: "echo project-subagent-start"},
		{Type: "command", Command: "echo local-subagent-start"},
	}, cfg.Hooks.SubagentStartCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-subagent-stop"},
		{Matcher: "reviewer", Type: "command", Command: "echo project-subagent-stop"},
		{Type: "command", Command: "echo local-subagent-stop"},
	}, cfg.Hooks.SubagentStopCommands)
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
