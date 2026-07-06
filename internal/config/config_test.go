package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/stretchr/testify/require"
)

func TestLoadAppliesManagedPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policyPath, []byte(`{"max_permission_mode":"read-only","denied_tools":["bash"],"permission_rules":{"deny":["write_file"]}}`), 0o644))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","enterprise":{"policy":"`+policyPath+`"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Equal(t, "read-only", cfg.PermissionModeRaw)
	require.Equal(t, "policy", cfg.PermissionModeSource)
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
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","enterprise":{"policy":"`+policyPath+`","policy_public_key":"`+base64.StdEncoding.EncodeToString(publicKey)+`"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "read-only", cfg.PermissionMode)
	require.Contains(t, cfg.PermissionRules.DeniedTools, "bash")
}

func TestLoadEnterpriseConfigCompatibility(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	encodedPublicKey := base64.StdEncoding.EncodeToString(publicKey)
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	writeSignedPolicy(t, policyPath, ManagedPolicy{MaxPermissionMode: "read-only"}, privateKey)
	cases := []struct {
		name      string
		body      string
		policy    string
		publicKey string
	}{
		{
			name:   "legacy future policy",
			body:   `{"future":{"enterprise_policy":"` + policyPath + `","enterprise_policy_public_key":"` + encodedPublicKey + `"}}`,
			policy: policyPath, publicKey: encodedPublicKey,
		},
		{
			name:   "formal enterprise aliases",
			body:   `{"enterprise":{"policy":"` + policyPath + `","publicKey":"` + encodedPublicKey + `"}}`,
			policy: policyPath, publicKey: encodedPublicKey,
		},
		{
			name:   "formal enterprise wins",
			body:   `{"future":{"enterprise_policy":"old-policy","enterprise_policy_public_key":"old-key"},"enterprise":{"policy":"` + policyPath + `","policyPublicKey":"` + encodedPublicKey + `"}}`,
			policy: policyPath, publicKey: encodedPublicKey,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.policy, cfg.Future.EnterprisePolicy)
			require.Equal(t, tc.publicKey, cfg.Future.EnterprisePolicyPublicKey)
		})
	}
}

func TestLoadUpdaterConfigCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name        string
		body        string
		manifestURL string
	}{
		{
			name:        "legacy future manifest URL",
			body:        `{"future":{"updater_manifest_url":"https://updates.example/manifest.json"}}`,
			manifestURL: "https://updates.example/manifest.json",
		},
		{
			name:        "formal updater aliases",
			body:        `{"updater":{"manifestURL":"https://updates.example/manifest.json"}}`,
			manifestURL: "https://updates.example/manifest.json",
		},
		{
			name:        "formal updater wins",
			body:        `{"future":{"updater_manifest_url":"https://old.example/manifest.json"},"updater":{"url":"https://updates.example/manifest.json"}}`,
			manifestURL: "https://updates.example/manifest.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.manifestURL, cfg.Future.UpdaterManifestURL)
		})
	}
}

func TestLoadPreferencesConfigCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name              string
		body              string
		chromeEnabled     bool
		notificationsOn   bool
		ultraReviewEnable bool
	}{
		{
			name:              "legacy future preferences",
			body:              `{"future":{"chrome_default_enabled":true,"notifications_enabled":false,"ultrareview_enabled":false}}`,
			chromeEnabled:     true,
			notificationsOn:   false,
			ultraReviewEnable: false,
		},
		{
			name:              "formal preferences aliases",
			body:              `{"preferences":{"chromeDefaultEnabled":true,"notificationsEnabled":true,"ultraReviewEnabled":false}}`,
			chromeEnabled:     true,
			notificationsOn:   true,
			ultraReviewEnable: false,
		},
		{
			name:              "formal preferences win",
			body:              `{"future":{"chrome_default_enabled":false,"notifications_enabled":false,"ultrareview_enabled":false},"preferences":{"chrome_default_enabled":true,"notifications_enabled":true,"ultrareview_enabled":true}}`,
			chromeEnabled:     true,
			notificationsOn:   true,
			ultraReviewEnable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.NotNil(t, cfg.Future.ChromeDefaultEnabled)
			require.Equal(t, tc.chromeEnabled, *cfg.Future.ChromeDefaultEnabled)
			require.NotNil(t, cfg.Future.NotificationsEnabled)
			require.Equal(t, tc.notificationsOn, *cfg.Future.NotificationsEnabled)
			require.NotNil(t, cfg.Future.UltraReviewEnabled)
			require.Equal(t, tc.ultraReviewEnable, *cfg.Future.UltraReviewEnabled)
		})
	}
}

func TestLoadCompatibilityConfigCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name         string
		body         string
		slackCount   int
		stickerCount int
		extraCount   int
		referralURL  string
		passCount    int
	}{
		{
			name:         "legacy future compatibility counters",
			body:         `{"future":{"slack_app_install_count":3,"sticker_order_count":2,"extra_usage_visit_count":4,"guest_pass_referral_url":"https://example.test/pass","guest_pass_visit_count":5}}`,
			slackCount:   3,
			stickerCount: 2,
			extraCount:   4,
			referralURL:  "https://example.test/pass",
			passCount:    5,
		},
		{
			name:         "formal compatibility aliases",
			body:         `{"compatibility":{"slackAppInstallCount":3,"stickerOrderCount":2,"extraUsageVisitCount":4,"guestPassReferralURL":"https://example.test/pass","guestPassVisitCount":5}}`,
			slackCount:   3,
			stickerCount: 2,
			extraCount:   4,
			referralURL:  "https://example.test/pass",
			passCount:    5,
		},
		{
			name:         "formal compatibility wins with zero",
			body:         `{"future":{"slack_app_install_count":3,"sticker_order_count":2,"extra_usage_visit_count":4,"guest_pass_referral_url":"https://old.example/pass","guest_pass_visit_count":5},"compatibility":{"slack_app_install_count":0,"sticker_order_count":0,"extra_usage_visit_count":0,"guest_pass_referral_url":"","guest_pass_visit_count":0}}`,
			slackCount:   0,
			stickerCount: 0,
			extraCount:   0,
			referralURL:  "",
			passCount:    0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.slackCount, cfg.Future.SlackAppInstallCount)
			require.Equal(t, tc.stickerCount, cfg.Future.StickerOrderCount)
			require.Equal(t, tc.extraCount, cfg.Future.ExtraUsageVisitCount)
			require.Equal(t, tc.referralURL, cfg.Future.GuestPassReferralURL)
			require.Equal(t, tc.passCount, cfg.Future.GuestPassVisitCount)
		})
	}
}

func TestLoadBackgroundConfigCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name      string
		body      string
		statePath string
	}{
		{
			name:      "legacy future background state path",
			body:      `{"future":{"background_state_path":".codog/state.json"}}`,
			statePath: ".codog/state.json",
		},
		{
			name:      "formal background aliases",
			body:      `{"background":{"statePath":".codog/state.json"}}`,
			statePath: ".codog/state.json",
		},
		{
			name:      "formal background wins",
			body:      `{"future":{"background_state_path":".codog/old-state.json"},"background":{"worker_state_path":".codog/state.json"}}`,
			statePath: ".codog/state.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.statePath, cfg.Future.BackgroundStatePath)
		})
	}
}

func TestLoadRejectsInvalidPermissionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"bogus"}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_permission_mode")

	_, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath, PermissionMode: "PROMPT"})
	require.NoError(t, err)
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

func TestLoadPermissionsDefaultModeAliases(t *testing.T) {
	tests := []struct {
		name           string
		defaultMode    string
		permissionMode string
		planMode       bool
		canonical      string
	}{
		{name: "default", defaultMode: "default", permissionMode: "prompt", canonical: "default"},
		{name: "plan", defaultMode: "plan", permissionMode: "read-only", planMode: true, canonical: "plan"},
		{name: "accept edits", defaultMode: "acceptEdits", permissionMode: "workspace-write", canonical: "acceptEdits"},
		{name: "bypass permissions", defaultMode: "bypassPermissions", permissionMode: "allow", canonical: "bypassPermissions"},
		{name: "dont ask", defaultMode: "dontAsk", permissionMode: "read-only", canonical: "dontAsk"},
		{name: "auto", defaultMode: "AUTO", permissionMode: "prompt", canonical: "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			require.NoError(t, os.WriteFile(configPath, []byte(`{"permissions":{"defaultMode":"`+tt.defaultMode+`","allow":["read_file"]}}`), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

			require.NoError(t, err)
			require.Equal(t, tt.permissionMode, cfg.PermissionMode)
			require.Equal(t, tt.defaultMode, cfg.PermissionModeRaw)
			require.Equal(t, "config", cfg.PermissionModeSource)
			require.Equal(t, tt.planMode, cfg.PlanMode)
			require.Equal(t, tt.canonical, cfg.PermissionRules.DefaultMode)
			require.Equal(t, []string{"read_file"}, cfg.PermissionRules.Allow)
		})
	}
}

func TestLoadPermissionModeOverridesDefaultModeAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"danger-full-access","permissions":{"defaultMode":"plan"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "danger-full-access", cfg.PermissionMode)
	require.Equal(t, "danger-full-access", cfg.PermissionModeRaw)
	require.Equal(t, "config", cfg.PermissionModeSource)
	require.False(t, cfg.PlanMode)
	require.Equal(t, "plan", cfg.PermissionRules.DefaultMode)
}

func TestLoadClaudeAllowedToolsAliases(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"allowedTools": ["Bash(git *)"],
		"disallowedTools": ["Bash(rm *)"],
		"permissions": {
			"allow": ["Read"],
			"deniedTools": ["Write"]
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, []string{"Read", "Bash(git *)"}, cfg.PermissionRules.Allow)
	require.Equal(t, []string{"Write", "Bash(rm *)"}, cfg.PermissionRules.DeniedTools)
}

func TestLoadAppliesSystemPromptFiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	basePath := filepath.Join(dir, "system.txt")
	appendPath := filepath.Join(dir, "append.txt")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"system_prompt":"config base","append_system_prompt":"config append"}`), 0o644))
	require.NoError(t, os.WriteFile(basePath, []byte("file base\n"), 0o644))
	require.NoError(t, os.WriteFile(appendPath, []byte("file append\n"), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath:       configPath,
		SystemPromptFile: basePath,
		AppendPromptFile: appendPath,
	})

	require.NoError(t, err)
	require.Equal(t, "file base\n", cfg.SystemPrompt)
	require.Equal(t, "config append\n\nfile append", cfg.AppendSystemPrompt)
}

func TestLoadRejectsConflictingSystemPromptFlags(t *testing.T) {
	_, _, err := LoadForInspection(FlagOverrides{
		SystemPrompt:     "base",
		SystemPromptFile: "base.txt",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot use both --system-prompt and --system-prompt-file")

	_, _, err = LoadForInspection(FlagOverrides{
		AppendPrompt:     "extra",
		AppendPromptFile: "extra.txt",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot use both --append-system-prompt and --append-system-prompt-file")
}

func TestLoadReportsMissingSystemPromptFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	_, _, err := LoadForInspection(FlagOverrides{SystemPromptFile: missing})

	require.Error(t, err)
	require.Contains(t, err.Error(), "system prompt file not found")
	require.Contains(t, err.Error(), missing)
}

func TestLoadLaterPermissionDefaultModeClearsPlanMode(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"permissions":{"defaultMode":"plan"}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"permissions":{"defaultMode":"acceptEdits"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "workspace-write", cfg.PermissionMode)
	require.Equal(t, "acceptEdits", cfg.PermissionModeRaw)
	require.Equal(t, "config", cfg.PermissionModeSource)
	require.False(t, cfg.PlanMode)
	require.Equal(t, "acceptEdits", cfg.PermissionRules.DefaultMode)
}

func TestLoadPermissionModeFlagClearsPlanMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permissions":{"defaultMode":"plan"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath, PermissionMode: "workspace-write"})

	require.NoError(t, err)
	require.Equal(t, "workspace-write", cfg.PermissionMode)
	require.Equal(t, "workspace-write", cfg.PermissionModeRaw)
	require.Equal(t, "cli", cfg.PermissionModeSource)
	require.False(t, cfg.PlanMode)
}

func TestLoadPermissionModeEnvProvenance(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permissions":{"defaultMode":"plan"}}`), 0o644))
	t.Setenv("CODOG_PERMISSION_MODE", "PROMPT")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "prompt", cfg.PermissionMode)
	require.Equal(t, "PROMPT", cfg.PermissionModeRaw)
	require.Equal(t, "env", cfg.PermissionModeSource)
	require.Equal(t, "CODOG_PERMISSION_MODE", cfg.PermissionModeEnvVar)
	require.False(t, cfg.PlanMode)
}

func TestLoadRejectsInvalidPermissionsDefaultMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permissions":{"defaultMode":"always"}}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_permission_default_mode")
}

func TestMergeFutureConfigPreservesSandboxDefaults(t *testing.T) {
	enabled := true
	namespace := false
	network := true
	dst := Config{
		Future: FutureConfig{
			SandboxStrategy: "detect",
			RemoteAuthToken: "token",
		},
	}
	merge(&dst, Config{
		Future: FutureConfig{
			Sandbox: SandboxConfig{
				Enabled:               &enabled,
				NamespaceRestrictions: &namespace,
				NetworkIsolation:      &network,
				FilesystemMode:        "allow-list",
				AllowedMounts:         []string{"logs"},
			},
		},
	})

	require.Equal(t, "detect", dst.Future.SandboxStrategy)
	require.Equal(t, "token", dst.Future.RemoteAuthToken)
	require.NotNil(t, dst.Future.Sandbox.Enabled)
	require.True(t, *dst.Future.Sandbox.Enabled)
	require.NotNil(t, dst.Future.Sandbox.NamespaceRestrictions)
	require.False(t, *dst.Future.Sandbox.NamespaceRestrictions)
	require.NotNil(t, dst.Future.Sandbox.NetworkIsolation)
	require.True(t, *dst.Future.Sandbox.NetworkIsolation)
	require.Equal(t, "allow-list", dst.Future.Sandbox.FilesystemMode)
	require.Equal(t, []string{"logs"}, dst.Future.Sandbox.AllowedMounts)
}

func TestLoadRemoteAuthToken(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name         string
		body         string
		enabled      bool
		token        string
		leaseSeconds int
	}{
		{
			name:         "legacy future remote",
			body:         `{"future":{"remote_auth_token":"legacy-token","remote_lease_seconds":30}}`,
			token:        "legacy-token",
			leaseSeconds: 30,
		},
		{
			name:         "formal remote aliases",
			body:         `{"remote":{"enabled":true,"authToken":"secret-token","leaseSeconds":45}}`,
			enabled:      true,
			token:        "secret-token",
			leaseSeconds: 45,
		},
		{
			name:         "formal remote wins",
			body:         `{"future":{"remote_auth_token":"legacy-token","remote_lease_seconds":30},"remote":{"auth_token":"secret-token","lease_seconds":60}}`,
			token:        "secret-token",
			leaseSeconds: 60,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.enabled, cfg.Future.RemoteEnabled)
			require.Equal(t, tc.token, cfg.Future.RemoteAuthToken)
			require.Equal(t, tc.leaseSeconds, cfg.Future.RemoteLeaseSeconds)
		})
	}
}

func TestLoadMarketplaceConfigCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		body    string
		sources []string
		keys    map[string]string
	}{
		{
			name:    "legacy future marketplace",
			body:    `{"future":{"plugin_marketplaces":["https://market.example/index.json"],"plugin_marketplace_public_keys":{"https://market.example/index.json":"legacy-key"}}}`,
			sources: []string{"https://market.example/index.json"},
			keys:    map[string]string{"https://market.example/index.json": "legacy-key"},
		},
		{
			name:    "formal marketplace aliases",
			body:    `{"marketplace":{"sources":["https://market.example/index.json"],"publicKeys":{"https://market.example/index.json":"public-key"}}}`,
			sources: []string{"https://market.example/index.json"},
			keys:    map[string]string{"https://market.example/index.json": "public-key"},
		},
		{
			name:    "formal marketplace wins",
			body:    `{"future":{"plugin_marketplaces":["https://old.example/index.json"],"plugin_marketplace_public_keys":{"https://old.example/index.json":"old-key"}},"marketplace":{"sources":[],"public_keys":{}}}`,
			sources: []string{},
			keys:    map[string]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			if len(tc.sources) == 0 {
				require.Empty(t, cfg.Future.PluginMarketplaces)
			} else {
				require.Equal(t, tc.sources, cfg.Future.PluginMarketplaces)
			}
			if len(tc.keys) == 0 {
				require.Empty(t, cfg.Future.PluginMarketplaceKeys)
			} else {
				require.Equal(t, tc.keys, cfg.Future.PluginMarketplaceKeys)
			}
		})
	}
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

func TestLoadAPITimeoutConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"apiTimeout":{"connectTimeout":11,"requestTimeout":222,"maxRetries":7}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, 11, cfg.APITimeout.ConnectTimeoutSeconds)
	require.Equal(t, 222, cfg.APITimeout.RequestTimeoutSeconds)
	require.Equal(t, 7, cfg.APITimeout.MaxRetries)
}

func TestLoadProviderFallbacksConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"providerFallbacks":{"primary":"claude-primary","fallbacks":["claude-backup","grok-mini"]}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "claude-primary", cfg.ProviderFallbacks.Primary)
	require.Equal(t, []string{"claude-backup", "grok-mini"}, cfg.ProviderFallbacks.Fallbacks)
}

func TestLoadRulesImportConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"rulesImport":["cursor","copilot","cursor"]}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	rules := cfg.EffectiveRulesImport()
	require.Equal(t, "list", rules.Mode)
	require.Equal(t, []string{"cursor", "copilot"}, rules.Frameworks)
	require.True(t, rules.ShouldImport("cursor"))
	require.True(t, rules.ShouldImport("copilot"))
	require.False(t, rules.ShouldImport("windsurf"))

	require.NoError(t, os.WriteFile(configPath, []byte(`{"rulesImport":"none"}`), 0o644))
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "none", cfg.EffectiveRulesImport().Mode)
	require.False(t, cfg.EffectiveRulesImport().ShouldImport("cursor"))
}

func TestLoadRejectsInvalidRulesImportConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"rulesImport":42}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_rules_import")
}

func TestLoadMergesTopLevelEnvByConfigPrecedence(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"env":{"A":"user","B":"user"}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"env":{"B":"project","C":"project"}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.local.json"), []byte(`{"env":{"C":"local","D":"local"}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"A": "user",
		"B": "project",
		"C": "local",
		"D": "local",
	}, cfg.Env)
}

func TestLoadMergesTrustedRootsByConfigPrecedence(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"trustedRoots":["/repo/user","/repo/shared"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"trustedRoots":["/repo/shared","/repo/project"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.local.json"), []byte(`{"trustedRoots":["/repo/local"]}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"/repo/user", "/repo/shared", "/repo/project", "/repo/local"}, cfg.TrustedRoots)
}

func TestLoadMergesHTTPHookPolicyByConfigPrecedence(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{
		"allowedHttpHookUrls":["https://hooks.example.test/*","https://shared.example.test/hook"],
		"httpHookAllowedEnvVars":["HOOK_TOKEN","SHARED_TOKEN"]
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"allowedHttpHookUrls":["https://shared.example.test/hook","https://project.example.test/hook"],
		"httpHookAllowedEnvVars":["PROJECT_TOKEN","HOOK_TOKEN"]
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, cfg.AllowedHTTPHookURLs)
	require.NotNil(t, cfg.HTTPHookAllowedEnvVars)
	require.Equal(t, []string{"https://hooks.example.test/*", "https://shared.example.test/hook", "https://project.example.test/hook"}, *cfg.AllowedHTTPHookURLs)
	require.Equal(t, []string{"HOOK_TOKEN", "SHARED_TOKEN", "PROJECT_TOKEN"}, *cfg.HTTPHookAllowedEnvVars)
}

func TestLoadCleanupPeriodDaysDistinguishesUnsetAndZero(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"cleanupPeriodDays":14}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"cleanupPeriodDays":0}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, cfg.CleanupPeriodDays)
	require.Equal(t, 0, *cfg.CleanupPeriodDays)
	require.Equal(t, 0, cfg.EffectiveCleanupPeriodDays())
}

func TestLoadRespectGitignoreDistinguishesUnsetAndFalse(t *testing.T) {
	cfg, err := Default(FlagOverrides{})
	require.NoError(t, err)
	require.Nil(t, cfg.RespectGitignore)
	require.True(t, cfg.EffectiveRespectGitignore())

	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"respectGitignore":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"respectGitignore":false}`), 0o644))

	cfg, _, err = LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, cfg.RespectGitignore)
	require.False(t, *cfg.RespectGitignore)
	require.False(t, cfg.EffectiveRespectGitignore())
}

func TestLoadDisableAllHooksDistinguishesUnsetAndTrue(t *testing.T) {
	cfg, err := Default(FlagOverrides{})
	require.NoError(t, err)
	require.Nil(t, cfg.DisableAllHooks)
	require.False(t, cfg.EffectiveDisableAllHooks())

	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"disableAllHooks":false}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"disableAllHooks":true}`), 0o644))

	cfg, _, err = LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, cfg.DisableAllHooks)
	require.True(t, *cfg.DisableAllHooks)
	require.True(t, cfg.EffectiveDisableAllHooks())
}

func TestLoadAllowManagedHooksOnlyDistinguishesUnsetAndFalse(t *testing.T) {
	cfg, err := Default(FlagOverrides{})
	require.NoError(t, err)
	require.Nil(t, cfg.AllowManagedHooksOnly)
	require.False(t, cfg.EffectiveAllowManagedHooksOnly())

	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"allowManagedHooksOnly":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"allowManagedHooksOnly":false}`), 0o644))

	cfg, _, err = LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.NotNil(t, cfg.AllowManagedHooksOnly)
	require.False(t, *cfg.AllowManagedHooksOnly)
	require.False(t, cfg.EffectiveAllowManagedHooksOnly())
}

func TestLoadRejectsNegativeCleanupPeriodDays(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"cleanupPeriodDays":-1}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cleanupPeriodDays must be non-negative")
}

func TestLoadRejectsInvalidAPITimeoutConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"apiTimeout":{"requestTimeout":-1}}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_api_timeout")
}

func TestInspectionPathsDoesNotReadExplicitConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bad-config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":42}`), 0o644))

	paths, err := InspectionPaths(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, []string{configPath}, paths)
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

func TestLoadMaxTokenEnvAliases(t *testing.T) {
	unsetEnv(t, "CODOG_MAX_TOKENS", "ANTHROPIC_MAX_TOKENS")
	t.Setenv("ANTHROPIC_MAX_TOKENS", "8192")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, 8192, cfg.MaxTokens)

	t.Setenv("CODOG_MAX_TOKENS", "2048")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, 2048, cfg.MaxTokens)
}

func TestLoadRAGConfigAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"rag_base_url":"http://from-config","rag_timeout_seconds":7,"rag_top_k_max":11}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "http://from-config", cfg.RAGBaseURL)
	require.Equal(t, 7, cfg.RAGTimeoutSeconds)
	require.Equal(t, 11, cfg.RAGTopKMax)

	t.Setenv("CODOG_RAG_BASE_URL", "http://from-env")
	t.Setenv("CODOG_RAG_TIMEOUT_SECONDS", "13")
	t.Setenv("CODOG_RAG_TOP_K_MAX", "17")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "http://from-env", cfg.RAGBaseURL)
	require.Equal(t, 13, cfg.RAGTimeoutSeconds)
	require.Equal(t, 17, cfg.RAGTopKMax)
}

func TestLoadOpenAIEnvironmentForOpenAIModel(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OLLAMA_HOST")
	t.Setenv("CODOG_MODEL", "openai/gpt-4o-mini")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:8080/v1")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4o-mini", cfg.Model)
	require.Equal(t, "openai-secret", cfg.APIKey)
	require.Equal(t, "http://127.0.0.1:8080/v1", cfg.BaseURL)
}

func TestLoadOpenAIEnvironmentForFlagModelOverride(t *testing.T) {
	unsetEnv(t, "CODOG_MODEL", "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_BASE_URL", "OLLAMA_HOST")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_API_KEY", "openai-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath: filepath.Join(t.TempDir(), "missing.json"),
		Model:      "openai/gpt-4.1-mini",
	})
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4.1-mini", cfg.Model)
	require.Empty(t, cfg.ModelEnvVar)
	require.Equal(t, "openai-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, modelrouting.DefaultOpenAIBaseURL, cfg.BaseURL)
}

func TestLoadClaudeCodeOAuthTokenEnvironment(t *testing.T) {
	unsetEnv(t, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CODOG_API_KEY", "CODOG_AUTH_TOKEN")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "claude-oauth-token")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "claude-oauth-token", cfg.AuthToken)

	t.Setenv("CODOG_AUTH_TOKEN", "codog-auth-token")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "codog-auth-token", cfg.AuthToken)
}

func TestLoadOllamaEnvironmentForFlagModelOverride(t *testing.T) {
	unsetEnv(t, "CODOG_MODEL", "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_API_KEY", "OPENAI_BASE_URL")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:11434/")

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath: filepath.Join(t.TempDir(), "missing.json"),
		Model:      "qwen3:8b",
	})
	require.NoError(t, err)
	require.Equal(t, "qwen3:8b", cfg.Model)
	require.Equal(t, "http://127.0.0.1:11434/v1", cfg.BaseURL)
	require.Empty(t, cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, modelrouting.ProviderOpenAI, cfg.RuntimeProvider)
	require.Equal(t, "OLLAMA_HOST", cfg.RuntimeProviderSource)
}

func TestLoadProviderEnvironmentFromDotenv(t *testing.T) {
	unsetEnv(t,
		"CODOG_MODEL",
		"CODOG_BASE_URL",
		"CODOG_API_KEY",
		"CODOG_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OLLAMA_HOST",
	)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(`
CODOG_MODEL=openai/gpt-4o-mini
OPENAI_API_KEY='openai-dotenv-secret'
OPENAI_BASE_URL="http://127.0.0.1:8088/v1"
CODOG_FAST_MODE=true
`), 0o600))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(dir, "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4o-mini", cfg.Model)
	require.Equal(t, "openai-dotenv-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "http://127.0.0.1:8088/v1", cfg.BaseURL)
	require.NotNil(t, cfg.FastMode)
	require.True(t, *cfg.FastMode)
}

func TestLoadProviderEnvironmentPrefersRealEnvOverDotenv(t *testing.T) {
	unsetEnv(t,
		"CODOG_MODEL",
		"CODOG_BASE_URL",
		"CODOG_API_KEY",
		"CODOG_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OLLAMA_HOST",
	)
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(`
CODOG_MODEL=openai/gpt-4o-mini
OPENAI_API_KEY=openai-dotenv-secret
OPENAI_BASE_URL=http://127.0.0.1:8088/v1
`), 0o600))
	t.Setenv("OPENAI_API_KEY", "openai-env-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(dir, "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "openai-env-secret", cfg.APIKey)
	require.Equal(t, "http://127.0.0.1:8088/v1", cfg.BaseURL)
}

func TestLoadOpenAIEnvironmentForGPTModelOverridesAnthropicEnv(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_BASE_URL", "OLLAMA_HOST")
	t.Setenv("CODOG_MODEL", "gpt-4.1-mini")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_API_KEY", "openai-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "gpt-4.1-mini", cfg.Model)
	require.Equal(t, "openai-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "https://api.openai.com/v1", cfg.BaseURL)
}

func TestLoadAnthropicModelEnvironmentAliases(t *testing.T) {
	unsetEnv(t, "CODOG_MODEL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "CLAUDE_MODEL")
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-4-7")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-7", cfg.Model)
	require.Equal(t, "ANTHROPIC_MODEL", cfg.ModelEnvVar)

	t.Setenv("CODOG_MODEL", "sonnet")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "sonnet", cfg.Model)
	require.Equal(t, "CODOG_MODEL", cfg.ModelEnvVar)

	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json"), Model: "haiku"})

	require.NoError(t, err)
	require.Equal(t, "haiku", cfg.Model)
	require.Empty(t, cfg.ModelEnvVar)
}

func TestLoadAnthropicDefaultAndClaudeModelAliases(t *testing.T) {
	unsetEnv(t, "CODOG_MODEL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "CLAUDE_MODEL")
	t.Setenv("ANTHROPIC_DEFAULT_MODEL", "opus")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "opus", cfg.Model)
	require.Equal(t, "ANTHROPIC_DEFAULT_MODEL", cfg.ModelEnvVar)

	t.Setenv("ANTHROPIC_DEFAULT_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "haiku")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "haiku", cfg.Model)
	require.Equal(t, "CLAUDE_MODEL", cfg.ModelEnvVar)
}

func TestLoadAnthropicSmallFastModelAlias(t *testing.T) {
	unsetEnv(t, "CODOG_ADVISOR_MODEL", "ANTHROPIC_SMALL_FAST_MODEL")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "claude-haiku-4-5")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "claude-haiku-4-5", cfg.AdvisorModel)

	t.Setenv("CODOG_ADVISOR_MODEL", "claude-sonnet-advisor")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-advisor", cfg.AdvisorModel)
}

func TestLoadAnthropicReasoningEffortAlias(t *testing.T) {
	unsetEnv(t, "CODOG_REASONING_EFFORT", "ANTHROPIC_REASONING_EFFORT")
	t.Setenv("ANTHROPIC_REASONING_EFFORT", "high")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "high", cfg.ReasoningEffort)

	t.Setenv("CODOG_REASONING_EFFORT", "low")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})

	require.NoError(t, err)
	require.Equal(t, "low", cfg.ReasoningEffort)
}

func TestLoadClaudeConfigHomeAliases(t *testing.T) {
	codogHome := filepath.Join(t.TempDir(), "codog-home")
	claudeHome := filepath.Join(t.TempDir(), "claude-home")
	claudeDir := filepath.Join(t.TempDir(), "claude-dir")
	unsetEnv(t, "CODOG_CONFIG_HOME", "CLAUDE_CONFIG_HOME", "CLAUDE_CONFIG_DIR")
	t.Setenv("CLAUDE_CONFIG_HOME", claudeHome)

	cfg, paths, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, claudeHome, cfg.ConfigHome)
	require.Equal(t, filepath.Join(claudeHome, "config.json"), paths[0])

	t.Setenv("CLAUDE_CONFIG_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	cfg, paths, err = LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, claudeDir, cfg.ConfigHome)
	require.Equal(t, filepath.Join(claudeDir, "config.json"), paths[0])

	t.Setenv("CODOG_CONFIG_HOME", codogHome)
	cfg, paths, err = LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, codogHome, cfg.ConfigHome)
	require.Equal(t, filepath.Join(codogHome, "config.json"), paths[0])
}

func TestLoadDashScopeEnvironmentForQwenModel(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "DASHSCOPE_BASE_URL")
	t.Setenv("CODOG_MODEL", "qwen-plus")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("DASHSCOPE_API_KEY", "dashscope-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "qwen-plus", cfg.Model)
	require.Equal(t, "dashscope-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", cfg.BaseURL)
}

func TestLoadDashScopeEnvironmentForKimiAlias(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "DASHSCOPE_BASE_URL")
	t.Setenv("CODOG_MODEL", "kimi")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("DASHSCOPE_API_KEY", "dashscope-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "kimi", cfg.Model)
	require.Equal(t, "dashscope-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1", cfg.BaseURL)
}

func TestLoadXAIEnvironmentForGrokAlias(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "XAI_BASE_URL")
	t.Setenv("CODOG_MODEL", "grok")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("XAI_API_KEY", "xai-secret")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "grok", cfg.Model)
	require.Equal(t, "xai-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "https://api.x.ai/v1", cfg.BaseURL)
}

func TestLoadXAIEnvironmentUsesBaseURLOverride(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN")
	t.Setenv("CODOG_MODEL", "xai/grok-3")
	t.Setenv("XAI_API_KEY", "xai-secret")
	t.Setenv("XAI_BASE_URL", "http://127.0.0.1:9090/v1")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "xai/grok-3", cfg.Model)
	require.Equal(t, "xai-secret", cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, "http://127.0.0.1:9090/v1", cfg.BaseURL)
}

func TestLoadLocalModelUsesOllamaHost(t *testing.T) {
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_BASE_URL")
	t.Setenv("CODOG_MODEL", "local/Qwen/Qwen3.6-27B-FP8")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:11434")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "local/Qwen/Qwen3.6-27B-FP8", cfg.Model)
	require.Equal(t, "http://127.0.0.1:11434/v1", cfg.BaseURL)
}

func TestLoadOllamaHostOverridesProviderRoutingForBareTag(t *testing.T) {
	t.Chdir(t.TempDir())
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "XAI_API_KEY", "DASHSCOPE_API_KEY")
	t.Setenv("CODOG_MODEL", "qwen2.5-coder:7b")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:8080/v1")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:11434")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "qwen2.5-coder:7b", cfg.Model)
	require.Equal(t, "http://127.0.0.1:11434/v1", cfg.BaseURL)
	require.Empty(t, cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, modelrouting.ProviderOpenAI, cfg.RuntimeProvider)
	require.Equal(t, "OLLAMA_HOST", cfg.RuntimeProviderSource)
}

func TestLoadOpenAIBaseURLRoutesLocalLookingBareModel(t *testing.T) {
	t.Chdir(t.TempDir())
	unsetEnv(t, "CODOG_BASE_URL", "CODOG_API_KEY", "CODOG_AUTH_TOKEN", "OPENAI_API_KEY", "OLLAMA_HOST", "XAI_API_KEY", "DASHSCOPE_API_KEY")
	t.Setenv("CODOG_MODEL", "llama3.2")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-token")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:8080/v1")

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: filepath.Join(t.TempDir(), "missing.json")})
	require.NoError(t, err)
	require.Equal(t, "llama3.2", cfg.Model)
	require.Equal(t, "http://127.0.0.1:8080/v1", cfg.BaseURL)
	require.Empty(t, cfg.APIKey)
	require.Empty(t, cfg.AuthToken)
	require.Equal(t, modelrouting.ProviderOpenAI, cfg.RuntimeProvider)
	require.Equal(t, "OPENAI_BASE_URL", cfg.RuntimeProviderSource)
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		name := name
		previous, existed := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, previous)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func TestLoadTemperatureConfigEnvAndFlags(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"temperature":0.7}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.7, *cfg.Temperature, 0.0001)

	t.Setenv("CODOG_TEMPERATURE", "0.2")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.2, *cfg.Temperature, 0.0001)

	t.Setenv("CODOG_TEMPERATURE", "")
	t.Setenv("ANTHROPIC_TEMPERATURE", "0.3")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.3, *cfg.Temperature, 0.0001)

	t.Setenv("CODOG_TEMPERATURE", "0.25")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.25, *cfg.Temperature, 0.0001)

	override := 0.1
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath, Temperature: &override})
	require.NoError(t, err)
	require.NotNil(t, cfg.Temperature)
	require.InDelta(t, 0.1, *cfg.Temperature, 0.0001)

	invalid := 1.5
	_, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath, Temperature: &invalid})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_temperature")
}

func TestLoadMCPHeadersHelperAliases(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"mcp_servers": {
			"snake": {"url": "https://snake.example/mcp", "headers_helper": "./snake-helper", "tool_call_timeout_ms": 15000},
			"camel": {"url": "https://camel.example/mcp", "headersHelper": "./camel-helper", "toolCallTimeoutMs": 25000}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "./snake-helper", cfg.MCPServers["snake"].HeadersHelper)
	require.Equal(t, 15000, cfg.MCPServers["snake"].ToolCallTimeoutMS)
	require.Equal(t, "./camel-helper", cfg.MCPServers["camel"].HeadersHelper)
	require.Equal(t, 25000, cfg.MCPServers["camel"].ToolCallTimeoutMS)
}

func TestLoadMCPServersCamelAliasAndEnvObject(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"mcpServers": {
			"demo": {"command": "demo-mcp", "args": ["--stdio"], "env": {"TOKEN": "secret", "A": "b"}}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "demo-mcp", cfg.MCPServers["demo"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["demo"].Args)
	require.Equal(t, []string{"A=b", "TOKEN=secret"}, cfg.MCPServers["demo"].Env)
}

func TestLoadNestedMCPServersAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"mcp": {
			"servers": {
				"demo": {"command": "demo-mcp", "args": ["--stdio"], "env": {"TOKEN": "secret"}}
			}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "demo-mcp", cfg.MCPServers["demo"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["demo"].Args)
	require.Equal(t, []string{"TOKEN=secret"}, cfg.MCPServers["demo"].Env)
}

func TestLoadMCPConfigFlagMergesFileAndInlineJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	flagPath := filepath.Join(dir, "mcp.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"mcpServers": {
			"base": {"command": "base-mcp"},
			"override": {"command": "base-override"}
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(flagPath, []byte(`{
		"mcp": {
			"servers": {
				"file": {"command": "file-mcp", "args": ["--stdio"]},
				"override": {"command": "file-override"}
			}
		}
	}`), 0o644))
	inline := `{"mcpServers":{"inline":{"url":"https://inline.example/mcp","headers":{"Authorization":"Bearer test"}}}}`

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath: configPath,
		MCPConfigs: []string{flagPath, inline},
	})

	require.NoError(t, err)
	require.Equal(t, "base-mcp", cfg.MCPServers["base"].Command)
	require.Equal(t, "file-mcp", cfg.MCPServers["file"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["file"].Args)
	require.Equal(t, "file-override", cfg.MCPServers["override"].Command)
	require.Equal(t, "https://inline.example/mcp", cfg.MCPServers["inline"].URL)
	require.Equal(t, "Bearer test", cfg.MCPServers["inline"].Headers["Authorization"])
}

func TestLoadStrictMCPConfigFlagIgnoresConfiguredServers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"mcpServers": {
			"base": {"command": "base-mcp"}
		}
	}`), 0o644))
	inline := `{"mcpServers":{"dynamic":{"command":"dynamic-mcp"}}}`

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath:      configPath,
		MCPConfigs:      []string{inline},
		StrictMCPConfig: true,
	})

	require.NoError(t, err)
	require.NotContains(t, cfg.MCPServers, "base")
	require.Equal(t, "dynamic-mcp", cfg.MCPServers["dynamic"].Command)
}

func TestLoadMCPConfigFlagRejectsInvalidSources(t *testing.T) {
	_, _, err := LoadForInspection(FlagOverrides{MCPConfigs: []string{`{"mcpServers":`}})
	require.Error(t, err)
	require.True(t, IsFileError(err))
	require.Contains(t, err.Error(), "--mcp-config")

	_, _, err = LoadForInspection(FlagOverrides{MCPConfigs: []string{filepath.Join(t.TempDir(), "missing.json")}})
	require.Error(t, err)
	require.True(t, IsFileError(err))
}

func TestLoadSettingsFlagMergesFileAndInlineJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	settingsPath := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"model": "config-model",
		"max_tokens": 100,
		"mcpServers": {
			"base": {"command": "base-mcp"}
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(settingsPath, []byte(`{
		"model": "settings-model",
		"append_system_prompt": "from settings",
		"mcpServers": {
			"settings": {"command": "settings-mcp"}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{
		ConfigPath: configPath,
		Settings:   settingsPath,
		MaxTokens:  200,
	})

	require.NoError(t, err)
	require.Equal(t, "settings-model", cfg.Model)
	require.Equal(t, 200, cfg.MaxTokens)
	require.Equal(t, "from settings", cfg.AppendSystemPrompt)
	require.Equal(t, "base-mcp", cfg.MCPServers["base"].Command)
	require.Equal(t, "settings-mcp", cfg.MCPServers["settings"].Command)

	cfg, _, err = LoadForInspection(FlagOverrides{
		ConfigPath: configPath,
		Settings:   `{"model":"inline-model","mcpServers":{"inline":{"url":"https://inline.example/mcp"}}}`,
	})

	require.NoError(t, err)
	require.Equal(t, "inline-model", cfg.Model)
	require.Equal(t, "https://inline.example/mcp", cfg.MCPServers["inline"].URL)
}

func TestLoadSettingsFlagRejectsInvalidSources(t *testing.T) {
	_, _, err := LoadForInspection(FlagOverrides{Settings: `{"model":`})
	require.Error(t, err)
	require.True(t, IsFileError(err))
	require.Contains(t, err.Error(), "--settings")

	_, _, err = LoadForInspection(FlagOverrides{Settings: filepath.Join(t.TempDir(), "missing.json")})
	require.Error(t, err)
	require.True(t, IsFileError(err))
}

func TestLoadProjectMCPJSONRespectsTrustSettings(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"enableAllProjectMcpServers": true,
		"disabledMcpjsonServers": ["blocked"],
		"mcp_servers": {
			"shared": {"command": "settings-mcp"}
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
		"mcpServers": {
			"project": {"command": "project-mcp", "env": {"TOKEN": "secret"}},
			"blocked": {"command": "blocked-mcp"},
			"shared": {"command": "project-override"}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "project-mcp", cfg.MCPServers["project"].Command)
	require.Equal(t, []string{"TOKEN=secret"}, cfg.MCPServers["project"].Env)
	require.NotContains(t, cfg.MCPServers, "blocked")
	require.Equal(t, "settings-mcp", cfg.MCPServers["shared"].Command)
}

func TestLoadProjectMCPJSONRequiresExplicitEnablement(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"enabledMcpjsonServers": ["allowed"]
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
		"mcpServers": {
			"allowed": {"command": "allowed-mcp"},
			"ignored": {"command": "ignored-mcp"}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "allowed-mcp", cfg.MCPServers["allowed"].Command)
	require.NotContains(t, cfg.MCPServers, "ignored")
}

func TestLoadProjectMCPJSONAcceptsNestedMCPServers(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"enabledMcpjsonServers": ["allowed"]
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
		"mcp": {
			"servers": {
				"allowed": {"command": "allowed-mcp", "args": ["--stdio"]},
				"ignored": {"command": "ignored-mcp"}
			}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "allowed-mcp", cfg.MCPServers["allowed"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["allowed"].Args)
	require.NotContains(t, cfg.MCPServers, "ignored")
}

func TestLoadProjectMCPJSONUsesLegacyTrustFields(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
		"enableAllProjectMcpServers": true,
		"disabledMcpjsonServers": ["blocked"],
		"mcpServers": {
			"project": {"command": "project-mcp"},
			"blocked": {"command": "blocked-mcp"}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "project-mcp", cfg.MCPServers["project"].Command)
	require.NotContains(t, cfg.MCPServers, "blocked")
}

func TestLoadProjectMCPJSONDoesNotOverrideExplicitSettingsTrust(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"enableAllProjectMcpServers": false
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
		"enableAllProjectMcpServers": true,
		"enabledMcpjsonServers": ["allowed"],
		"mcpServers": {
			"allowed": {"command": "allowed-mcp"},
			"ignored": {"command": "ignored-mcp"}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "allowed-mcp", cfg.MCPServers["allowed"].Command)
	require.NotContains(t, cfg.MCPServers, "ignored")
}

func TestLoadForceLoginSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"forceLoginMethod":"CONSOLE","forceLoginOrgUUID":"org-123"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "console", cfg.ForceLoginMethod)
	require.Equal(t, "org-123", cfg.ForceLoginOrgUUID)
}

func TestLoadRejectsInvalidForceLoginMethod(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"forceLoginMethod":"password"}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_force_login_method")
}

func TestLoadAPIKeyHelper(t *testing.T) {
	unsetCredentialEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"apiKeyHelper":"echo helper-secret"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "echo helper-secret", cfg.APIKeyHelper)
	require.Equal(t, "helper-secret", cfg.APIKey)
}

func TestLoadAPIKeyHelperDoesNotOverrideConfiguredCredential(t *testing.T) {
	unsetCredentialEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"api_key":"config-secret","apiKeyHelper":"echo helper-secret"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "config-secret", cfg.APIKey)
}

func TestLoadAPIKeyHelperDoesNotOverrideEnvCredential(t *testing.T) {
	unsetEnv(t, "ANTHROPIC_AUTH_TOKEN")
	unsetProviderRoutingEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "env-secret")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"apiKeyHelper":"echo helper-secret"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "env-secret", cfg.APIKey)
}

func TestLoadAPIKeyHelperReportsFailure(t *testing.T) {
	unsetCredentialEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"apiKeyHelper":"exit 7"}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.Error(t, err)
	require.Contains(t, err.Error(), "apiKeyHelper failed")
}

func TestLoadStatusLineSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"statusLine":{"type":"command","command":"echo ready","padding":2}}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.NotNil(t, cfg.StatusLine)
	require.Equal(t, "command", cfg.StatusLine.Type)
	require.Equal(t, "echo ready", cfg.StatusLine.Command)
	require.NotNil(t, cfg.StatusLine.Padding)
	require.Equal(t, 2.0, *cfg.StatusLine.Padding)
}

func TestLoadRejectsInvalidStatusLineSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"statusLine":{"type":"inline","command":"echo ready"}}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_status_line")
}

func TestLoadWorktreeSettings(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{"worktree":{"symlinkDirectories":["node_modules"],"sparsePaths":["cmd"]}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{"worktree":{"symlinkDirectories":[".cache"],"sparsePaths":["internal","cmd"]}}`), 0o644))
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { _ = os.Chdir(previous) })

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, []string{"node_modules", ".cache"}, cfg.Worktree.SymlinkDirectories)
	require.Equal(t, []string{"cmd", "internal"}, cfg.Worktree.SparsePaths)
}

func TestLoadDefaultShellSetting(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"defaultShell":"PowerShell"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.NoError(t, err)
	require.Equal(t, "powershell", cfg.DefaultShell)
}

func TestLoadDefaultShellFromEnv(t *testing.T) {
	t.Setenv("CODOG_DEFAULT_SHELL", "bash")

	cfg, err := Default(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, "bash", cfg.DefaultShell)
}

func TestLoadRejectsInvalidDefaultShell(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"defaultShell":"zsh"}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_default_shell")
}

func unsetCredentialEnv(t *testing.T) {
	unsetEnv(t, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CODOG_API_KEY", "CODOG_AUTH_TOKEN")
	unsetProviderRoutingEnv(t)
}

func unsetProviderRoutingEnv(t *testing.T) {
	unsetEnv(t, "CODOG_MODEL", "OPENAI_API_KEY", "XAI_API_KEY", "DASHSCOPE_API_KEY")
}

func TestLoadInterfaceAndPrivacyPreferences(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"theme": "dark",
		"language": "Japanese",
		"editorMode": "vim",
		"advisor_model": "claude-opus-test",
		"oauth_profile": "default",
		"reasoning_effort": "high",
		"fast_mode": true,
		"voice_enabled": true,
		"voice_command": "cat",
		"speech_command": "say",
		"future": {
			"chrome_default_enabled": true,
			"notifications_enabled": true,
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
	require.Equal(t, "Japanese", cfg.Language)
	require.Equal(t, "vim", cfg.EditorMode)
	require.Equal(t, "claude-opus-test", cfg.AdvisorModel)
	require.Equal(t, "default", cfg.OAuthProfile)
	require.Equal(t, "high", cfg.ReasoningEffort)
	require.NotNil(t, cfg.FastMode)
	require.True(t, *cfg.FastMode)
	require.NotNil(t, cfg.VoiceEnabled)
	require.True(t, *cfg.VoiceEnabled)
	require.Equal(t, "cat", cfg.VoiceCommand)
	require.Equal(t, "say", cfg.SpeechCommand)
	require.NotNil(t, cfg.Future.ChromeDefaultEnabled)
	require.True(t, *cfg.Future.ChromeDefaultEnabled)
	require.NotNil(t, cfg.Future.NotificationsEnabled)
	require.True(t, *cfg.Future.NotificationsEnabled)
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
	t.Setenv("CODOG_LANGUAGE", "French")
	t.Setenv("CODOG_EDITOR_MODE", "default")
	t.Setenv("CODOG_ADVISOR_MODEL", "claude-sonnet-advisor")
	t.Setenv("CODOG_OAUTH_PROFILE", "work")
	t.Setenv("CODOG_REASONING_EFFORT", "low")
	t.Setenv("CODOG_FAST_MODE", "false")
	t.Setenv("CODOG_VOICE_ENABLED", "false")
	t.Setenv("CODOG_VOICE_COMMAND", "printf")
	t.Setenv("CODOG_SPEECH_COMMAND", "cat")
	t.Setenv("CODOG_NOTIFICATIONS_ENABLED", "false")
	t.Setenv("CODOG_PRIVACY_PROMPT_HISTORY_ENABLED", "true")
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "light", cfg.Theme)
	require.Equal(t, "French", cfg.Language)
	require.Equal(t, "default", cfg.EditorMode)
	require.Equal(t, "claude-sonnet-advisor", cfg.AdvisorModel)
	require.Equal(t, "work", cfg.OAuthProfile)
	require.Equal(t, "low", cfg.ReasoningEffort)
	require.NotNil(t, cfg.FastMode)
	require.False(t, *cfg.FastMode)
	require.NotNil(t, cfg.VoiceEnabled)
	require.False(t, *cfg.VoiceEnabled)
	require.Equal(t, "printf", cfg.VoiceCommand)
	require.Equal(t, "cat", cfg.SpeechCommand)
	require.NotNil(t, cfg.Future.NotificationsEnabled)
	require.False(t, *cfg.Future.NotificationsEnabled)
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

func TestLoadSubagentModelAlias(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"subagentModel":"claude-haiku-subagent"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "claude-haiku-subagent", cfg.AdvisorModel)

	require.NoError(t, os.WriteFile(configPath, []byte(`{"advisor_model":"claude-opus-advisor","subagentModel":"claude-haiku-subagent"}`), 0o644))
	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "claude-opus-advisor", cfg.AdvisorModel)
}

func TestLoadSkipPermissionsFlagOverridesPermissionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"permission_mode":"read-only"}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath, PermissionMode: "workspace-write", SkipPermissions: true})
	require.NoError(t, err)
	require.Equal(t, "allow", cfg.PermissionMode)
	require.Equal(t, "--skip-permissions", cfg.PermissionModeRaw)
	require.Equal(t, "cli", cfg.PermissionModeSource)
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
			"SessionEnd": [{"command": "echo session-end"}],
			"Setup": [{"command": "echo setup"}],
			"Stop": [{"hooks": [{"type": "command", "command": "echo stop"}]}],
			"StopFailure": ["echo stop-failure"],
			"PreCompact": ["echo pre-compact"],
			"PostCompact": ["echo post-compact"],
			"Notification": [{"matcher": "background_*", "command": "echo notify"}],
			"SubagentStart": [{"matcher": "reviewer", "command": "echo agent-start"}],
			"SubagentStop": [{"matcher": "reviewer", "command": "echo agent-stop"}],
			"WorktreeCreate": [{"matcher": "agent-*", "command": "echo worktree-create"}],
			"WorktreeRemove": [{"matcher": "agent-*", "command": "echo worktree-remove"}],
			"CwdChanged": [{"matcher": "*", "command": "echo cwd-changed"}],
			"TaskCreated": [{"matcher": "agent", "command": "echo task-created"}],
			"TaskCompleted": [{"matcher": "agent", "command": "echo task-completed"}],
			"InstructionsLoaded": [{"matcher": "session_start", "command": "echo instructions-loaded"}],
			"FileChanged": [{"matcher": "Write", "command": "echo file-changed"}]
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
	require.Equal(t, []string{"echo session-end"}, cfg.Hooks.SessionEnd)
	require.Equal(t, []string{"echo setup"}, cfg.Hooks.Setup)
	require.Equal(t, []string{"echo stop"}, cfg.Hooks.Stop)
	require.Equal(t, []string{"echo stop-failure"}, cfg.Hooks.StopFailure)
	require.Equal(t, []string{"echo pre-compact"}, cfg.Hooks.PreCompact)
	require.Equal(t, []string{"echo post-compact"}, cfg.Hooks.PostCompact)
	require.Equal(t, []string{"echo notify"}, cfg.Hooks.Notification)
	require.Equal(t, []string{"echo agent-start"}, cfg.Hooks.SubagentStart)
	require.Equal(t, []string{"echo agent-stop"}, cfg.Hooks.SubagentStop)
	require.Equal(t, []string{"echo worktree-create"}, cfg.Hooks.WorktreeCreate)
	require.Equal(t, []string{"echo worktree-remove"}, cfg.Hooks.WorktreeRemove)
	require.Equal(t, []string{"echo cwd-changed"}, cfg.Hooks.CwdChanged)
	require.Equal(t, []string{"echo task-created"}, cfg.Hooks.TaskCreated)
	require.Equal(t, []string{"echo task-completed"}, cfg.Hooks.TaskCompleted)
	require.Equal(t, []string{"echo instructions-loaded"}, cfg.Hooks.InstructionsLoaded)
	require.Equal(t, []string{"echo file-changed"}, cfg.Hooks.FileChanged)
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
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo session-end"}}, cfg.Hooks.SessionEndCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo setup"}}, cfg.Hooks.SetupCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo stop"}}, cfg.Hooks.StopCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo stop-failure"}}, cfg.Hooks.StopFailureCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo pre-compact"}}, cfg.Hooks.PreCompactCommands)
	require.Equal(t, []HookCommand{{Type: "command", Command: "echo post-compact"}}, cfg.Hooks.PostCompactCommands)
	require.Equal(t, []HookCommand{{Matcher: "background_*", Type: "command", Command: "echo notify"}}, cfg.Hooks.NotificationCommands)
	require.Equal(t, []HookCommand{{Matcher: "reviewer", Type: "command", Command: "echo agent-start"}}, cfg.Hooks.SubagentStartCommands)
	require.Equal(t, []HookCommand{{Matcher: "reviewer", Type: "command", Command: "echo agent-stop"}}, cfg.Hooks.SubagentStopCommands)
	require.Equal(t, []HookCommand{{Matcher: "agent-*", Type: "command", Command: "echo worktree-create"}}, cfg.Hooks.WorktreeCreateCommands)
	require.Equal(t, []HookCommand{{Matcher: "agent-*", Type: "command", Command: "echo worktree-remove"}}, cfg.Hooks.WorktreeRemoveCommands)
	require.Equal(t, []HookCommand{{Matcher: "*", Type: "command", Command: "echo cwd-changed"}}, cfg.Hooks.CwdChangedCommands)
	require.Equal(t, []HookCommand{{Matcher: "agent", Type: "command", Command: "echo task-created"}}, cfg.Hooks.TaskCreatedCommands)
	require.Equal(t, []HookCommand{{Matcher: "agent", Type: "command", Command: "echo task-completed"}}, cfg.Hooks.TaskCompletedCommands)
	require.Equal(t, []HookCommand{{Matcher: "session_start", Type: "command", Command: "echo instructions-loaded"}}, cfg.Hooks.InstructionsLoadedCommands)
	require.Equal(t, []HookCommand{{Matcher: "Write", Type: "command", Command: "echo file-changed"}}, cfg.Hooks.FileChangedCommands)
}

func TestLoadHooksKeepsValidSiblingsBesideInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"hooks": {
			"PreToolUse": [
				"echo valid-pre",
				42,
				{"matcher": 42, "command": "echo bad-matcher"},
				{"matcher": "Read", "hooks": {"not": "an-array"}},
				{"matcher": "Write", "hooks": [{"type": "command"}]}
			],
			"PostToolUse": "not-an-array"
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, []string{"echo valid-pre"}, cfg.Hooks.PreToolUse)
	require.Empty(t, cfg.Hooks.PostToolUse)
	require.Len(t, cfg.Hooks.PreToolUseCommands, 5)
	require.Equal(t, HookCommand{Type: "command", Command: "echo valid-pre"}, cfg.Hooks.PreToolUseCommands[0])
	require.Equal(t, "invalid_hooks_config", cfg.Hooks.PreToolUseCommands[1].InvalidKind)
	require.Equal(t, "entry", cfg.Hooks.PreToolUseCommands[1].InvalidField)
	require.Contains(t, cfg.Hooks.PreToolUseCommands[1].InvalidReason, "cannot unmarshal number")
	require.Equal(t, "invalid_hooks_config", cfg.Hooks.PreToolUseCommands[2].InvalidKind)
	require.Equal(t, "entry", cfg.Hooks.PreToolUseCommands[2].InvalidField)
	require.Contains(t, cfg.Hooks.PreToolUseCommands[2].InvalidReason, "matcher must be a string")
	require.Equal(t, "Read", cfg.Hooks.PreToolUseCommands[3].Matcher)
	require.Equal(t, "hooks", cfg.Hooks.PreToolUseCommands[3].InvalidField)
	require.Contains(t, cfg.Hooks.PreToolUseCommands[3].InvalidReason, "hook event must be an array")
	require.Equal(t, HookCommand{Matcher: "Write", Type: "command"}, cfg.Hooks.PreToolUseCommands[4])
	require.Len(t, cfg.Hooks.PostToolUseCommands, 1)
	require.Equal(t, "hooks", cfg.Hooks.PostToolUseCommands[0].InvalidField)
	require.Contains(t, cfg.Hooks.PostToolUseCommands[0].InvalidReason, "hook event must be an array")
}

func TestLoadProjectCompatibleHookSettings(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	require.NoError(t, os.Chdir(workspace))
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".omc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".omc", "settings.json"), []byte(`{
		"hooks": {
			"SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "echo omc-start"}]}],
			"SessionEnd": [{"matcher": "resume", "hooks": [{"type": "command", "command": "echo omc-end"}]}]
		}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{
		"hooks": {
			"SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "echo codog-start"}]}]
		}
	}`), 0o644))

	cfg, paths, err := LoadForInspection(FlagOverrides{})
	require.NoError(t, err)
	require.Contains(t, paths, filepath.Join(".omc", "settings.json"))
	require.Equal(t, []HookCommand{
		{Matcher: "startup", Type: "command", Command: "echo omc-start"},
		{Matcher: "startup", Type: "command", Command: "echo codog-start"},
	}, cfg.Hooks.SessionStartCommands)
	require.Equal(t, []HookCommand{{Matcher: "resume", Type: "command", Command: "echo omc-end"}}, cfg.Hooks.SessionEndCommands)
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
			"session_end": ["echo user-session-end"],
			"setup": ["echo user-setup"],
			"stop": ["echo user-stop"],
			"stop_failure": ["echo user-stop-failure"],
			"pre_compact": ["echo user-compact"],
			"post_compact": ["echo user-post-compact"],
			"notification": ["echo user-notification"],
			"subagent_start": ["echo user-subagent-start"],
			"subagent_stop": ["echo user-subagent-stop"],
			"worktree_create": ["echo user-worktree-create"],
			"worktree_remove": ["echo user-worktree-remove"],
			"cwd_changed": ["echo user-cwd-changed"],
			"task_created": ["echo user-task-created"],
			"task_completed": ["echo user-task-completed"],
			"instructions_loaded": ["echo user-instructions-loaded"],
			"file_changed": ["echo user-file-changed"]
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
			"SessionEnd": [{"command": "echo project-session-end"}],
			"Setup": [{"command": "echo project-setup"}],
			"Stop": [{"command": "echo project-stop"}],
			"StopFailure": [{"command": "echo project-stop-failure"}],
			"PreCompact": [{"command": "echo project-compact"}],
			"PostCompact": [{"command": "echo project-post-compact"}],
			"Notification": [{"matcher": "background_task_started", "command": "echo project-notification"}],
			"SubagentStart": [{"matcher": "reviewer", "command": "echo project-subagent-start"}],
			"SubagentStop": [{"matcher": "reviewer", "command": "echo project-subagent-stop"}],
			"WorktreeCreate": [{"matcher": "agent-*", "command": "echo project-worktree-create"}],
			"WorktreeRemove": [{"matcher": "agent-*", "command": "echo project-worktree-remove"}],
			"CwdChanged": [{"matcher": "*", "command": "echo project-cwd-changed"}],
			"TaskCreated": [{"matcher": "agent", "command": "echo project-task-created"}],
			"TaskCompleted": [{"matcher": "agent", "command": "echo project-task-completed"}],
			"InstructionsLoaded": [{"matcher": "session_start", "command": "echo project-instructions-loaded"}],
			"FileChanged": [{"matcher": "Write", "command": "echo project-file-changed"}]
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
			"session_end": ["echo user-session-end", "echo local-session-end"],
			"setup": ["echo user-setup", "echo local-setup"],
			"stop": ["echo user-stop", "echo local-stop"],
			"stop_failure": ["echo user-stop-failure", "echo local-stop-failure"],
			"pre_compact": ["echo user-compact", "echo local-compact"],
			"post_compact": ["echo user-post-compact", "echo local-post-compact"],
			"notification": ["echo user-notification", "echo local-notification"],
			"subagent_start": ["echo user-subagent-start", "echo local-subagent-start"],
			"subagent_stop": ["echo user-subagent-stop", "echo local-subagent-stop"],
			"worktree_create": ["echo user-worktree-create", "echo local-worktree-create"],
			"worktree_remove": ["echo user-worktree-remove", "echo local-worktree-remove"],
			"cwd_changed": ["echo user-cwd-changed", "echo local-cwd-changed"],
			"task_created": ["echo user-task-created", "echo local-task-created"],
			"task_completed": ["echo user-task-completed", "echo local-task-completed"],
			"instructions_loaded": ["echo user-instructions-loaded", "echo local-instructions-loaded"],
			"file_changed": ["echo user-file-changed", "echo local-file-changed"]
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
	require.Equal(t, []string{"echo user-session-end", "echo project-session-end", "echo local-session-end"}, cfg.Hooks.SessionEnd)
	require.Equal(t, []string{"echo user-setup", "echo project-setup", "echo local-setup"}, cfg.Hooks.Setup)
	require.Equal(t, []string{"echo user-stop", "echo project-stop", "echo local-stop"}, cfg.Hooks.Stop)
	require.Equal(t, []string{"echo user-stop-failure", "echo project-stop-failure", "echo local-stop-failure"}, cfg.Hooks.StopFailure)
	require.Equal(t, []string{"echo user-compact", "echo project-compact", "echo local-compact"}, cfg.Hooks.PreCompact)
	require.Equal(t, []string{"echo user-post-compact", "echo project-post-compact", "echo local-post-compact"}, cfg.Hooks.PostCompact)
	require.Equal(t, []string{"echo user-notification", "echo project-notification", "echo local-notification"}, cfg.Hooks.Notification)
	require.Equal(t, []string{"echo user-subagent-start", "echo project-subagent-start", "echo local-subagent-start"}, cfg.Hooks.SubagentStart)
	require.Equal(t, []string{"echo user-subagent-stop", "echo project-subagent-stop", "echo local-subagent-stop"}, cfg.Hooks.SubagentStop)
	require.Equal(t, []string{"echo user-worktree-create", "echo project-worktree-create", "echo local-worktree-create"}, cfg.Hooks.WorktreeCreate)
	require.Equal(t, []string{"echo user-worktree-remove", "echo project-worktree-remove", "echo local-worktree-remove"}, cfg.Hooks.WorktreeRemove)
	require.Equal(t, []string{"echo user-cwd-changed", "echo project-cwd-changed", "echo local-cwd-changed"}, cfg.Hooks.CwdChanged)
	require.Equal(t, []string{"echo user-task-created", "echo project-task-created", "echo local-task-created"}, cfg.Hooks.TaskCreated)
	require.Equal(t, []string{"echo user-task-completed", "echo project-task-completed", "echo local-task-completed"}, cfg.Hooks.TaskCompleted)
	require.Equal(t, []string{"echo user-instructions-loaded", "echo project-instructions-loaded", "echo local-instructions-loaded"}, cfg.Hooks.InstructionsLoaded)
	require.Equal(t, []string{"echo user-file-changed", "echo project-file-changed", "echo local-file-changed"}, cfg.Hooks.FileChanged)
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
		{Type: "command", Command: "echo user-session-end"},
		{Type: "command", Command: "echo project-session-end"},
		{Type: "command", Command: "echo local-session-end"},
	}, cfg.Hooks.SessionEndCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-setup"},
		{Type: "command", Command: "echo project-setup"},
		{Type: "command", Command: "echo local-setup"},
	}, cfg.Hooks.SetupCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-stop"},
		{Type: "command", Command: "echo project-stop"},
		{Type: "command", Command: "echo local-stop"},
	}, cfg.Hooks.StopCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-stop-failure"},
		{Type: "command", Command: "echo project-stop-failure"},
		{Type: "command", Command: "echo local-stop-failure"},
	}, cfg.Hooks.StopFailureCommands)
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
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-worktree-create"},
		{Matcher: "agent-*", Type: "command", Command: "echo project-worktree-create"},
		{Type: "command", Command: "echo local-worktree-create"},
	}, cfg.Hooks.WorktreeCreateCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-worktree-remove"},
		{Matcher: "agent-*", Type: "command", Command: "echo project-worktree-remove"},
		{Type: "command", Command: "echo local-worktree-remove"},
	}, cfg.Hooks.WorktreeRemoveCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-cwd-changed"},
		{Matcher: "*", Type: "command", Command: "echo project-cwd-changed"},
		{Type: "command", Command: "echo local-cwd-changed"},
	}, cfg.Hooks.CwdChangedCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-task-created"},
		{Matcher: "agent", Type: "command", Command: "echo project-task-created"},
		{Type: "command", Command: "echo local-task-created"},
	}, cfg.Hooks.TaskCreatedCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-task-completed"},
		{Matcher: "agent", Type: "command", Command: "echo project-task-completed"},
		{Type: "command", Command: "echo local-task-completed"},
	}, cfg.Hooks.TaskCompletedCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-instructions-loaded"},
		{Matcher: "session_start", Type: "command", Command: "echo project-instructions-loaded"},
		{Type: "command", Command: "echo local-instructions-loaded"},
	}, cfg.Hooks.InstructionsLoadedCommands)
	require.Equal(t, []HookCommand{
		{Type: "command", Command: "echo user-file-changed"},
		{Matcher: "Write", Type: "command", Command: "echo project-file-changed"},
		{Type: "command", Command: "echo local-file-changed"},
	}, cfg.Hooks.FileChangedCommands)
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

	cfg, _, err = LoadForInspection(FlagOverrides{ConfigPath: configPath, AdditionalDirs: []string{"three", "two"}})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two", "three"}, cfg.AdditionalDirs)
}

func TestLoadPermissionsAdditionalDirectoriesAlias(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	previous, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(previous)) })
	t.Setenv("CODOG_CONFIG_HOME", configHome)
	require.NoError(t, os.Chdir(workspace))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{
		"permissions": {"additionalDirectories": ["../shared", "../common"]}
	}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), []byte(`{
		"permissions": {"additionalDirectories": ["../common", "../project"]}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{})

	require.NoError(t, err)
	require.Equal(t, []string{"../shared", "../common", "../project"}, cfg.AdditionalDirs)
	require.Equal(t, []string{"../shared", "../common", "../project"}, cfg.PermissionRules.AdditionalDirectories)
}

func TestLoadEditorBridgeToken(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		body   string
		socket string
		token  string
	}{
		{
			name:   "legacy future bridge",
			body:   `{"future":{"editor_bridge_socket":"legacy.sock","editor_bridge_token":"legacy-token"}}`,
			socket: "legacy.sock",
			token:  "legacy-token",
		},
		{
			name:   "formal editor bridge aliases",
			body:   `{"editor_bridge":{"path":"codog.sock","authToken":"bridge-token"}}`,
			socket: "codog.sock",
			token:  "bridge-token",
		},
		{
			name:   "formal editor bridge wins",
			body:   `{"future":{"editor_bridge_socket":"legacy.sock","editor_bridge_token":"legacy-token"},"editor_bridge":{"socket":"formal.sock","token":"formal-token"}}`,
			socket: "formal.sock",
			token:  "formal-token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.socket, cfg.Future.EditorBridgeSocket)
			require.Equal(t, tc.token, cfg.Future.EditorBridgeToken)
		})
	}
}

func TestLoadSandboxConfigAliases(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"sandbox": {
			"strategy": "sandbox-exec"
		},
		"future": {
			"sandbox": {
				"enabled": true,
				"namespaceRestrictions": false,
				"networkIsolation": true,
				"filesystemMode": "allow-list",
				"allowedMounts": ["logs", "tmp/cache"]
			}
		}
	}`), 0o644))

	cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.NoError(t, err)
	require.Equal(t, "sandbox-exec", cfg.Future.SandboxStrategy)
	require.NotNil(t, cfg.Future.Sandbox.Enabled)
	require.True(t, *cfg.Future.Sandbox.Enabled)
	require.NotNil(t, cfg.Future.Sandbox.NamespaceRestrictions)
	require.False(t, *cfg.Future.Sandbox.NamespaceRestrictions)
	require.NotNil(t, cfg.Future.Sandbox.NetworkIsolation)
	require.True(t, *cfg.Future.Sandbox.NetworkIsolation)
	require.Equal(t, "allow-list", cfg.Future.Sandbox.FilesystemMode)
	require.Equal(t, []string{"logs", "tmp/cache"}, cfg.Future.Sandbox.AllowedMounts)
}

func TestLoadSandboxStrategyCompatibility(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		body     string
		expected string
	}{
		{name: "legacy future strategy", body: `{"future":{"sandbox_strategy":"detect"}}`, expected: "detect"},
		{name: "nested future sandbox strategy", body: `{"future":{"sandbox":{"strategy":"bwrap"}}}`, expected: "bwrap"},
		{
			name: "top level sandbox strategy wins",
			body: `{
				"future": {"sandbox_strategy": "detect"},
				"sandbox": {"strategy": "restricted-token"}
			}`,
			expected: "restricted-token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.body), 0o644))

			cfg, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
			require.NoError(t, err)
			require.Equal(t, tc.expected, cfg.Future.SandboxStrategy)
		})
	}
}

func TestLoadRejectsInvalidSandboxFilesystemMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"future":{"sandbox":{"filesystemMode":"invalid"}}}`), 0o644))

	_, _, err := LoadForInspection(FlagOverrides{ConfigPath: configPath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_sandbox_config")
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
	require.NoError(t, os.WriteFile(configPath, []byte(`{"model":"old","sandbox":{"strategy":"detect"}}`), 0o644))

	report, err := SetFileValue(configPath, "model", "new-model")
	require.NoError(t, err)
	require.Equal(t, "set", report.Action)
	require.Equal(t, "model", report.Key)
	report, err = SetFileValue(configPath, "rate_limit.max_retries", float64(4))
	require.NoError(t, err)
	require.Equal(t, "rate_limit.max_retries", report.Key)
	report, err = UnsetFileValue(configPath, "sandbox.strategy")
	require.NoError(t, err)
	require.Equal(t, "unset", report.Action)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, "new-model", raw["model"])
	require.Equal(t, float64(4), raw["rate_limit"].(map[string]any)["max_retries"])
	require.NotContains(t, raw, "sandbox")

	report, err = ResetFile(configPath)
	require.NoError(t, err)
	require.Equal(t, "reset", report.Action)
	require.Equal(t, "*", report.Key)
	require.NoFileExists(t, configPath)

	report, err = ResetFile(configPath)
	require.NoError(t, err)
	require.Equal(t, "reset", report.Action)
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
