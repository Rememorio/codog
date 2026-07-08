package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/sandbox"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OLLAMA_HOST",
		"XAI_API_KEY",
		"XAI_BASE_URL",
		"DASHSCOPE_API_KEY",
		"DASHSCOPE_BASE_URL",
	} {
		_ = os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

func TestRunWarnsWhenAuthMissing(t *testing.T) {
	clearProviderAuthEnv(t)
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	require.False(t, report.HasFailures)
	require.Equal(t, "1.0", report.SchemaVersion)
	require.Contains(t, report.OutputFields, "checks")
	require.Contains(t, report.OutputFields, "check_names")
	require.Equal(t, []string{StatusOK, StatusWarn, StatusFail}, report.StatusValues)
	require.Contains(t, report.CheckNames, "auth")
	require.Contains(t, report.CheckNames, "mcp validation")
	require.Contains(t, report.CheckNames, "hook validation")
	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusWarn, auth.Status)
	require.Contains(t, auth.Summary, "No Anthropic credentials")
	require.Equal(t, "anthropic", auth.Data["selected_provider"])
	require.Equal(t, "ANTHROPIC_API_KEY", auth.Data["required_api_key_env"])
}

func TestRunUsesSelectedProviderForAuthPreflight(t *testing.T) {
	clearProviderAuthEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	report := Run(Options{
		Workspace:       t.TempDir(),
		ConfigHome:      t.TempDir(),
		Model:           "openai/gpt-4.1-mini",
		RuntimeProvider: "openai",
		BaseURL:         "https://api.openai.com/v1",
		PermissionMode:  "workspace-write",
		ToolCount:       6,
		SessionCount:    0,
		SandboxDefault:  "test-sandbox",
		SandboxOK:       true,
	})

	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusWarn, auth.Status)
	require.Contains(t, auth.Summary, "No OpenAI-compatible credentials")
	require.Equal(t, "openai", auth.Data["selected_provider"])
	require.Equal(t, "OPENAI_API_KEY", auth.Data["required_api_key_env"])
	require.Equal(t, true, auth.Data["anthropic_api_key_present"])
	require.Equal(t, false, auth.Data["openai_api_key_present"])
	require.Equal(t, false, auth.Data["selected_provider_api_key_present"])
	require.Contains(t, auth.Hint, "OPENAI_API_KEY")
}

func TestRunReportsProviderSpecificAuthConfigured(t *testing.T) {
	clearProviderAuthEnv(t)
	cases := []struct {
		name        string
		model       string
		provider    string
		apiKey      string
		requiredEnv string
		summary     string
	}{
		{name: "openai", model: "openai/gpt-4.1-mini", provider: "openai", apiKey: "openai-secret", requiredEnv: "OPENAI_API_KEY", summary: "OpenAI-compatible credentials"},
		{name: "xai", model: "grok", provider: "xai", apiKey: "xai-secret", requiredEnv: "XAI_API_KEY", summary: "xAI credentials"},
		{name: "dashscope", model: "qwen-plus", provider: "dashscope", apiKey: "dashscope-secret", requiredEnv: "DASHSCOPE_API_KEY", summary: "DashScope credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Run(Options{
				Workspace:       t.TempDir(),
				ConfigHome:      t.TempDir(),
				Model:           tc.model,
				RuntimeProvider: tc.provider,
				BaseURL:         "https://api.example.test/v1",
				APIKey:          tc.apiKey,
				PermissionMode:  "workspace-write",
				ToolCount:       6,
				SessionCount:    0,
				SandboxDefault:  "test-sandbox",
				SandboxOK:       true,
			})

			auth := findCheck(t, report, "Auth")
			require.Equal(t, StatusOK, auth.Status)
			require.Contains(t, auth.Summary, tc.summary)
			require.Equal(t, tc.provider, auth.Data["selected_provider"])
			require.Equal(t, tc.requiredEnv, auth.Data["required_api_key_env"])
			require.Equal(t, true, auth.Data["selected_provider_api_key_present"])
			require.Equal(t, "api_key", auth.Data["effective_auth_source"])
		})
	}
}

func TestRunWarnsWhenAnthropicAPIKeyAndBearerAreBothConfigured(t *testing.T) {
	clearProviderAuthEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-real")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-bearer")
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "sk-ant-api03-real",
		AuthToken:      "stale-bearer",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusWarn, auth.Status)
	require.Contains(t, auth.Summary, "both ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN")
	require.Contains(t, auth.Hint, "sk-ant-* API keys")
	require.Equal(t, "api_key_and_bearer", auth.Data["effective_auth_source"])
	require.Equal(t, []string{"x-api-key", "authorization_bearer"}, auth.Data["headers_sent"])
	require.Equal(t, true, auth.Data["both_anthropic_auth_env_vars_present"])
	require.Equal(t, true, auth.Data["selected_provider_both_auth_present"])
	require.Contains(t, strings.Join(auth.Details, "\n"), "Headers sent: x-api-key, authorization_bearer")
}

func TestRunTreatsOllamaRouteAsCredentialOptional(t *testing.T) {
	clearProviderAuthEnv(t)
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:11434")
	report := Run(Options{
		Workspace:             t.TempDir(),
		ConfigHome:            t.TempDir(),
		Model:                 "qwen3:8b",
		RuntimeProvider:       "openai",
		RuntimeProviderSource: "OLLAMA_HOST",
		BaseURL:               "http://127.0.0.1:11434/v1",
		PermissionMode:        "workspace-write",
		ToolCount:             6,
		SessionCount:          0,
		SandboxDefault:        "test-sandbox",
		SandboxOK:             true,
	})

	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusOK, auth.Status)
	require.Contains(t, auth.Summary, "does not require credentials")
	require.Equal(t, "openai", auth.Data["selected_provider"])
	require.Equal(t, "OLLAMA_HOST", auth.Data["runtime_provider_source"])
	require.Equal(t, "", auth.Data["required_api_key_env"])
	require.Equal(t, true, auth.Data["ollama_host_present"])
	require.Equal(t, true, auth.Data["selected_provider_auth_present"])
	require.Equal(t, "OLLAMA_HOST", auth.Data["effective_auth_source"])
}

func TestRunTreatsLocalOpenAIBaseURLAsCredentialOptional(t *testing.T) {
	clearProviderAuthEnv(t)
	report := Run(Options{
		Workspace:             t.TempDir(),
		ConfigHome:            t.TempDir(),
		Model:                 "qwen3:8b",
		RuntimeProvider:       "openai",
		RuntimeProviderSource: "OPENAI_BASE_URL",
		BaseURL:               "http://127.0.0.1:8080/v1",
		PermissionMode:        "workspace-write",
		ToolCount:             6,
		SessionCount:          0,
		SandboxDefault:        "test-sandbox",
		SandboxOK:             true,
	})

	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusOK, auth.Status)
	require.Contains(t, auth.Summary, "does not require credentials")
	require.Equal(t, "openai", auth.Data["selected_provider"])
	require.Equal(t, "OPENAI_BASE_URL", auth.Data["runtime_provider_source"])
	require.Equal(t, "", auth.Data["required_api_key_env"])
	require.Equal(t, true, auth.Data["local_base_url"])
	require.Equal(t, true, auth.Data["selected_provider_auth_present"])
	require.Equal(t, "OPENAI_BASE_URL", auth.Data["effective_auth_source"])
}

func TestRunStillRequiresOpenAIAPIKeyForRemoteBaseURL(t *testing.T) {
	clearProviderAuthEnv(t)
	report := Run(Options{
		Workspace:       t.TempDir(),
		ConfigHome:      t.TempDir(),
		Model:           "openai/gpt-4.1-mini",
		RuntimeProvider: "openai",
		BaseURL:         "https://openrouter.ai/api/v1",
		PermissionMode:  "workspace-write",
		ToolCount:       6,
		SessionCount:    0,
		SandboxDefault:  "test-sandbox",
		SandboxOK:       true,
	})

	auth := findCheck(t, report, "Auth")
	require.Equal(t, StatusWarn, auth.Status)
	require.Contains(t, auth.Summary, "No OpenAI-compatible credentials")
	require.Equal(t, "OPENAI_API_KEY", auth.Data["required_api_key_env"])
	require.Equal(t, false, auth.Data["local_base_url"])
	require.Equal(t, false, auth.Data["selected_provider_auth_present"])
}

func TestRunWarnsOnInvalidProviderEndpointEnv(t *testing.T) {
	clearProviderEndpointEnv(t)
	t.Setenv("OPENAI_BASE_URL", "javascript:alert(1)")
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	check := findCheck(t, report, "Provider endpoints")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "1 provider base URL")
	require.Contains(t, strings.Join(check.Details, "\n"), "OPENAI_BASE_URL")
	require.Equal(t, 1, check.Data["invalid_count"])
	endpoints, ok := check.Data["endpoints"].([]modelrouting.BaseURLDiagnostic)
	require.True(t, ok)
	require.Len(t, endpoints, 1)
	require.Equal(t, "OPENAI_BASE_URL", endpoints[0].Env)
	require.Equal(t, modelrouting.ProviderOpenAI, endpoints[0].Provider)
	require.Equal(t, "unsupported_scheme", endpoints[0].ErrorKind)
	require.False(t, endpoints[0].Valid)
}

func TestRunReportsValidProviderEndpointEnv(t *testing.T) {
	clearProviderEndpointEnv(t)
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:11434/v1")
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "openai/qwen3:8b",
		BaseURL:        "http://127.0.0.1:11434/v1",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	check := findCheck(t, report, "Provider endpoints")
	require.Equal(t, StatusOK, check.Status)
	require.Equal(t, 0, check.Data["invalid_count"])
	endpoints, ok := check.Data["endpoints"].([]modelrouting.BaseURLDiagnostic)
	require.True(t, ok)
	require.Len(t, endpoints, 1)
	require.True(t, endpoints[0].Valid)
	require.True(t, endpoints[0].Local)
}

func TestNewReportSurfacesStableMetadata(t *testing.T) {
	report := NewReport([]Check{
		{Name: "Auth", Status: StatusOK, Summary: "ready"},
		{Name: "MCP validation", Status: StatusWarn, Summary: "invalid"},
		{Name: "Auth", Status: StatusOK, Summary: "duplicate"},
	})

	require.Equal(t, "1.0", report.SchemaVersion)
	require.Equal(t, []string{"auth", "mcp validation"}, report.CheckNames)
	require.Contains(t, report.OutputFields, "schema_version")
	require.Contains(t, report.OutputFields, "status_values")
	require.Equal(t, []string{StatusOK, StatusWarn, StatusFail}, report.StatusValues)
}

func TestRunReportsMemoryMetadata(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		MemoryFiles: []localstatus.MemoryFileStatus{{
			Path:        "/repo/AGENTS.md",
			Name:        "AGENTS.md",
			Scope:       "/repo",
			Chars:       22,
			Lines:       1,
			Words:       3,
			SizeBytes:   22,
			ModifiedAt:  "2026-07-01T12:00:00Z",
			AgeSeconds:  90,
			Contributes: true,
		}},
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	check := findCheck(t, report, "Memory")
	require.Equal(t, StatusOK, check.Status)
	require.Equal(t, 1, check.Data["file_count"])
	require.Equal(t, 3, check.Data["total_words"])
	require.Equal(t, int64(22), check.Data["total_bytes"])
	require.Equal(t, int64(90), check.Data["oldest_age_seconds"])
	require.Contains(t, strings.Join(check.Details, "\n"), "words=3")
	require.Contains(t, strings.Join(check.Details, "\n"), "bytes=22")
	files, ok := check.Data["files"].([]localstatus.MemoryFileStatus)
	require.True(t, ok)
	require.Len(t, files, 1)
	require.Equal(t, "AGENTS.md", files[0].Name)
	require.Equal(t, "2026-07-01T12:00:00Z", files[0].ModifiedAt)
}

func TestRunWarnsOnSessionHygieneIssues(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   1,
		SessionHygiene: &SessionHygiene{
			Status:                   StatusWarn,
			SessionCount:             1,
			MessageCount:             2,
			PlaceholderIdentityCount: 1,
			Issues: []SessionHygieneIssue{{
				Kind:       "identity_placeholder",
				Severity:   StatusWarn,
				SessionID:  "draft",
				Field:      "purpose",
				Message:    "session identity still uses a typed placeholder",
				NextAction: "codog sessions repair",
			}},
			NextActions: []string{"codog sessions repair"},
		},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	sessions := findCheck(t, report, "Sessions")
	require.Equal(t, StatusWarn, sessions.Status)
	require.Contains(t, sessions.Summary, "session hygiene")
	require.Contains(t, sessions.Hint, "codog sessions audit")
	require.Contains(t, strings.Join(sessions.Details, "\n"), "Identity placeholders: 1")
	require.Contains(t, strings.Join(sessions.Details, "\n"), "codog sessions repair")
	hygiene, ok := sessions.Data["hygiene"].(*SessionHygiene)
	require.True(t, ok)
	require.Equal(t, 1, hygiene.PlaceholderIdentityCount)
	require.Len(t, hygiene.Issues, 1)
	require.Equal(t, "identity_placeholder", hygiene.Issues[0].Kind)
}

func TestRunFailsInvalidPermissionMode(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "root",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusFail, report.Status)
	require.True(t, report.HasFailures)
	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusFail, permissions.Status)
	require.Contains(t, permissions.Hint, "workspace-write")
}

func TestRunReportsPermissionModeProvenance(t *testing.T) {
	report := Run(Options{
		Workspace:            t.TempDir(),
		ConfigHome:           t.TempDir(),
		Model:                "claude-test",
		BaseURL:              "https://api.example.test",
		APIKey:               "secret",
		PermissionMode:       "workspace-write",
		PermissionModeRaw:    "acceptEdits",
		PermissionModeSource: "config",
		ToolPermissions: []ToolPermission{
			{Name: "read_file", RequiredPermission: "read-only"},
			{Name: "write_file", RequiredPermission: "workspace-write"},
			{Name: "bash", RequiredPermission: "danger-full-access"},
		},
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.NotEqual(t, StatusFail, report.Status)
	configCheck := findCheck(t, report, "Config")
	require.Equal(t, "workspace-write", configCheck.Data["permission_mode"])
	require.Equal(t, "acceptEdits", configCheck.Data["permission_mode_raw"])
	require.Equal(t, "config", configCheck.Data["permission_mode_source"])
	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusOK, permissions.Status)
	require.Equal(t, "workspace-write", permissions.Data["mode"])
	require.Equal(t, "acceptEdits", permissions.Data["raw"])
	require.Equal(t, "config", permissions.Data["source"])
	require.Equal(t, true, permissions.Data["source_explicit"])
	require.Equal(t, []string{"read_file", "write_file"}, permissions.Data["allowed_tools"])
	require.Equal(t, []string{"bash"}, permissions.Data["gated_tools"])
	require.Equal(t, 2, permissions.Data["allowed_count"])
	require.Equal(t, 1, permissions.Data["gated_count"])
	require.Contains(t, permissions.Data["message"], "resolved from config")
	require.Contains(t, strings.Join(permissions.Details, "\n"), "source: config")
}

func TestRunReportsPromptPermissionModeGatesTools(t *testing.T) {
	report := Run(Options{
		Workspace:            t.TempDir(),
		ConfigHome:           t.TempDir(),
		Model:                "claude-test",
		BaseURL:              "https://api.example.test",
		APIKey:               "secret",
		PermissionMode:       "prompt",
		PermissionModeRaw:    "default",
		PermissionModeSource: "default",
		ToolPermissions: []ToolPermission{
			{Name: "read_file", RequiredPermission: "read-only"},
			{Name: "write_file", RequiredPermission: "workspace-write"},
		},
		ToolCount:      2,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusOK, permissions.Status)
	require.Equal(t, false, permissions.Data["source_explicit"])
	require.Empty(t, permissions.Data["allowed_tools"])
	require.Equal(t, []string{"read_file", "write_file"}, permissions.Data["gated_tools"])
}

func TestRunWarnsForDefaultDangerFullAccess(t *testing.T) {
	report := Run(Options{
		Workspace:            t.TempDir(),
		ConfigHome:           t.TempDir(),
		Model:                "claude-test",
		BaseURL:              "https://api.example.test",
		APIKey:               "secret",
		PermissionMode:       "danger-full-access",
		PermissionModeRaw:    "danger-full-access",
		PermissionModeSource: "default",
		ToolPermissions: []ToolPermission{
			{Name: "read_file", RequiredPermission: "read-only"},
			{Name: "bash", RequiredPermission: "danger-full-access"},
		},
		ToolCount:      2,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusWarn, permissions.Status)
	require.Contains(t, permissions.Summary, "defaults to danger-full-access")
	require.Equal(t, false, permissions.Data["source_explicit"])
	require.Equal(t, true, permissions.Data["default_danger_full_access"])
	require.Equal(t, []string{"bash", "read_file"}, permissions.Data["allowed_tools"])
	require.Contains(t, permissions.Hint, "permission_mode")
}

func TestRunAcceptsExplicitDangerFullAccess(t *testing.T) {
	report := Run(Options{
		Workspace:            t.TempDir(),
		ConfigHome:           t.TempDir(),
		Model:                "claude-test",
		BaseURL:              "https://api.example.test",
		APIKey:               "secret",
		PermissionMode:       "danger-full-access",
		PermissionModeRaw:    "danger-full-access",
		PermissionModeSource: "config",
		ToolPermissions: []ToolPermission{
			{Name: "read_file", RequiredPermission: "read-only"},
			{Name: "bash", RequiredPermission: "danger-full-access"},
		},
		ToolCount:      2,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	permissions := findCheck(t, report, "Permissions")
	require.Equal(t, StatusOK, permissions.Status)
	require.Equal(t, true, permissions.Data["source_explicit"])
	require.Equal(t, false, permissions.Data["default_danger_full_access"])
	require.Equal(t, []string{"bash", "read_file"}, permissions.Data["allowed_tools"])
}

func TestRunWarnsUnknownPermissionRules(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		PermissionRules: localstatus.PermissionRulesStatus{
			Deny: []localstatus.PermissionRuleStatus{
				{Raw: "Bash(rm:*)", Tool: "Bash", ResolvedToolName: "bash", Matcher: "rm"},
				{Raw: "Bsh(echo:*)", Tool: "Bsh", Matcher: "echo", UnknownTool: true},
			},
			UnknownCount: 1,
		},
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	require.False(t, report.HasFailures)
	require.Contains(t, report.CheckNames, "permission rules")
	check := findCheck(t, report, "Permission rules")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "1 permission rule")
	require.Contains(t, strings.Join(check.Details, "\n"), "Bsh(echo:*)")
	require.Contains(t, strings.Join(check.Details, "\n"), "unknown_tool=true")
	require.Equal(t, 1, check.Data["unknown_count"])
}

func TestRunFailsConfigLoadError(t *testing.T) {
	report := Run(Options{
		Workspace:           t.TempDir(),
		ConfigHome:          t.TempDir(),
		Model:               "claude-test",
		BaseURL:             "https://api.example.test",
		APIKey:              "secret",
		PermissionMode:      "workspace-write",
		ConfigLoadError:     "broken.json: unexpected end of JSON input",
		ConfigLoadErrorKind: "config_load_failed",
		ToolCount:           6,
		SessionCount:        0,
		SandboxDefault:      "test-sandbox",
		SandboxOK:           true,
	})

	require.Equal(t, StatusFail, report.Status)
	require.True(t, report.HasFailures)
	configCheck := findCheck(t, report, "Config")
	require.Equal(t, StatusFail, configCheck.Status)
	require.Contains(t, configCheck.Summary, "failed to load")
	require.Equal(t, "broken.json: unexpected end of JSON input", configCheck.Data["load_error"])
	require.Equal(t, "config_load_failed", configCheck.Data["load_error_kind"])
	require.Contains(t, configCheck.Hint, "codog doctor")
}

func TestRenderText(t *testing.T) {
	report := NewReport([]Check{
		{Name: "Auth", Status: StatusOK, Summary: "ready"},
		{Name: "Git", Status: StatusWarn, Summary: "not a worktree", Details: []string{"Inside worktree: false"}, Hint: "Run from a worktree."},
	})

	var out bytes.Buffer
	RenderText(&out, report)

	require.Contains(t, out.String(), "Doctor")
	require.Contains(t, out.String(), "Warnings         1")
	require.Contains(t, out.String(), "Git")
	require.Contains(t, out.String(), "Inside worktree: false")
	require.Contains(t, out.String(), "Run from a worktree.")
}

func TestRunWarnsForMissingHookPath(t *testing.T) {
	workspace := t.TempDir()
	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		PreToolUse:     []string{"./hooks/missing.sh"},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	hooks := findCheck(t, report, "Hooks")
	require.Equal(t, StatusWarn, hooks.Status)
	require.Contains(t, hooks.Summary, "could not be found")
	require.Contains(t, strings.Join(hooks.Details, "\n"), filepath.Join(workspace, "hooks", "missing.sh"))
}

func TestRunAcceptsExistingHookPath(t *testing.T) {
	workspace := t.TempDir()
	hooksDir := filepath.Join(workspace, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		PreToolUse:     []string{"./hooks/pre.sh"},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	hooks := findCheck(t, report, "Hooks")
	require.Equal(t, StatusOK, hooks.Status)
	require.Contains(t, hooks.Summary, "runnable")
}

func TestRunWarnsForStaleBranchFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runTestGit(t, workspace, "init", "-b", "main")
	runTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "chore: base")
	runTestGit(t, workspace, "switch", "-c", "topic")
	runTestGit(t, workspace, "switch", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "fix: main update")
	runTestGit(t, workspace, "switch", "topic")

	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	git := findCheck(t, report, "Git")
	require.Equal(t, StatusWarn, git.Status)
	require.Contains(t, git.Summary, "behind or diverged")
	identity, ok := git.Data["identity"].(gitops.Identity)
	require.True(t, ok)
	require.Equal(t, "topic", identity.HeadRef)
	freshness, ok := git.Data["freshness"].(gitops.BranchFreshness)
	require.True(t, ok)
	require.Equal(t, "stale", freshness.Status)
	baseCommit, ok := git.Data["base_commit"].(gitops.BaseCommitCheck)
	require.True(t, ok)
	require.Equal(t, "no_expected_base", baseCommit.Status)
	require.Contains(t, strings.Join(git.Details, "\n"), "Freshness: stale")
	require.Contains(t, strings.Join(git.Details, "\n"), "Behind: 1")
	require.Contains(t, strings.Join(git.Details, "\n"), "Missing: fix: main update")
}

func TestRunWarnsForPausedGitOperation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runTestGit(t, workspace, "init", "-b", "main")
	runTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "chore: base")

	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
		GitOperation: &gitops.Operation{
			Kind:       "rebase",
			Paused:     true,
			ResumeHint: "git rebase --continue",
			AbortHint:  "git rebase --abort",
		},
	})

	git := findCheck(t, report, "Git")
	require.Equal(t, StatusWarn, git.Status)
	require.Contains(t, git.Summary, "rebase in progress")
	require.Contains(t, git.Hint, "git rebase --continue")
	require.Contains(t, strings.Join(git.Details, "\n"), "Operation: rebase")
	operation, ok := git.Data["operation"].(gitops.Operation)
	require.True(t, ok)
	require.Equal(t, "rebase", operation.Kind)
	require.True(t, operation.Paused)
}

func TestRunReportsGitIdentityData(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runTestGit(t, workspace, "init", "-b", "main")
	runTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "chore: base")

	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	git := findCheck(t, report, "Git")
	require.Equal(t, StatusOK, git.Status)
	identity, ok := git.Data["identity"].(gitops.Identity)
	require.True(t, ok)
	require.Equal(t, "main", identity.HeadRef)
	require.NotEmpty(t, identity.HeadSHA)
	require.NotEmpty(t, identity.HeadShortSHA)
	freshness, ok := git.Data["freshness"].(gitops.BranchFreshness)
	require.True(t, ok)
	require.True(t, freshness.Fresh)
	baseCommit, ok := git.Data["base_commit"].(gitops.BaseCommitCheck)
	require.True(t, ok)
	require.Equal(t, "no_expected_base", baseCommit.Status)
	require.Contains(t, strings.Join(git.Details, "\n"), "Head: "+identity.HeadShortSHA)
}

func TestRunWarnsForDivergedBaseCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runTestGit(t, workspace, "init", "-b", "main")
	runTestGit(t, workspace, "config", "user.email", "codog@example.test")
	runTestGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runTestGit(t, workspace, "add", ".")
	runTestGit(t, workspace, "commit", "-m", "chore: base")
	baseSHA := strings.TrimSpace(runTestGitOutput(t, workspace, "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog-base"), []byte(baseSHA+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "next.txt"), []byte("next\n"), 0o644))
	runTestGit(t, workspace, "add", "next.txt")
	runTestGit(t, workspace, "commit", "-m", "feat: next")

	report := Run(Options{
		Workspace:      workspace,
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	git := findCheck(t, report, "Git")
	require.Equal(t, StatusWarn, git.Status)
	require.Contains(t, git.Summary, "expected base commit")
	identity, ok := git.Data["identity"].(gitops.Identity)
	require.True(t, ok)
	require.Equal(t, "main", identity.HeadRef)
	baseCommit, ok := git.Data["base_commit"].(gitops.BaseCommitCheck)
	require.True(t, ok)
	require.Equal(t, "diverged", baseCommit.Status)
	require.False(t, baseCommit.Matches)
	require.Equal(t, baseSHA, baseCommit.Expected)
	require.Contains(t, strings.Join(git.Details, "\n"), "Base commit: diverged")
	require.Contains(t, strings.Join(git.Details, "\n"), "Expected base: "+baseSHA)
	require.Contains(t, git.Hint, "codog stale-base --json")
	require.Contains(t, git.Data, "base_commit")
}

func TestRunWarnsForUnavailableMCPServer(t *testing.T) {
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		MCPServerStatuses: []mcp.ServerStatus{
			{Name: "ready", Status: "ok", ToolCount: 2, ResolvedPath: "echo"},
			{Name: "missing", Status: "command_not_found", Error: "missing command"},
		},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	check := findCheck(t, report, "MCP")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "1 MCP server")
	require.Contains(t, strings.Join(check.Details, "\n"), "missing: command_not_found")
}

func TestRunReportsConfigValidationChecks(t *testing.T) {
	hookIndex := 0
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		MCPValidation: localstatus.MCPValidationStatus{
			TotalConfigured: 2,
			ValidCount:      1,
			InvalidCount:    1,
			InvalidServers: []localstatus.ValidationIssue{{
				Name:       "missing",
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command",
			}},
		},
		HookValidation: localstatus.HookValidationStatus{
			ValidCount:   1,
			InvalidCount: 1,
			InvalidHooks: []localstatus.ValidationIssue{{
				Event:      "pre_tool_use",
				Index:      &hookIndex,
				HookIndex:  &hookIndex,
				Kind:       "missing_command",
				ErrorField: "command",
				Reason:     "missing command",
			}},
		},
		SandboxDefault: "test-sandbox",
		SandboxOK:      true,
	})

	require.Equal(t, StatusWarn, report.Status)
	mcpValidation := findCheck(t, report, "MCP validation")
	require.Equal(t, StatusWarn, mcpValidation.Status)
	require.Contains(t, mcpValidation.Summary, "1 MCP server entry is invalid")
	require.Equal(t, 2, mcpValidation.Data["total_configured"])
	require.Equal(t, 1, mcpValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(mcpValidation.Details, "\n"), "Invalid server: missing")
	require.Contains(t, mcpValidation.Hint, "mcp_validation.invalid_servers")

	hookValidation := findCheck(t, report, "Hook validation")
	require.Equal(t, StatusWarn, hookValidation.Status)
	require.Contains(t, hookValidation.Summary, "1 hook entry is invalid")
	require.Equal(t, 1, hookValidation.Data["invalid_count"])
	require.Contains(t, strings.Join(hookValidation.Details, "\n"), "Invalid hook: pre_tool_use")
	require.Contains(t, hookValidation.Hint, "hook_validation.invalid_hooks")
}

func TestRunReportsSandboxFallbackDetails(t *testing.T) {
	report := Run(Options{
		Workspace:       t.TempDir(),
		ConfigHome:      t.TempDir(),
		Model:           "claude-test",
		BaseURL:         "https://api.example.test",
		APIKey:          "secret",
		PermissionMode:  "workspace-write",
		ToolCount:       6,
		SessionCount:    0,
		SandboxFallback: "bwrap: command not found",
	})

	check := findCheck(t, report, "Sandbox")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, strings.Join(check.Details, "\n"), "Fallback: bwrap: command not found")
	require.Contains(t, strings.Join(check.Details, "\n"), "In container: false")
}

func TestRunReportsSandboxRuntimeStatus(t *testing.T) {
	status := sandbox.SandboxExecutionStatus{
		Enabled:            true,
		Active:             false,
		Supported:          false,
		NamespaceSupported: false,
		NamespaceActive:    false,
		NetworkSupported:   false,
		NetworkActive:      false,
		FilesystemMode:     "workspace-only",
		FilesystemActive:   false,
		AllowedMounts:      []string{},
		InContainer:        true,
		ContainerMarkers:   []string{"/.dockerenv"},
		FallbackReason:     "sandbox strategy unavailable",
		CapabilityGaps: []sandbox.CapabilityGap{{
			Capability: "strategy",
			Requested:  true,
			Supported:  false,
			Active:     false,
			Reason:     "sandbox strategy unavailable",
		}},
	}
	report := Run(Options{
		Workspace:      t.TempDir(),
		ConfigHome:     t.TempDir(),
		Model:          "claude-test",
		BaseURL:        "https://api.example.test",
		APIKey:         "secret",
		PermissionMode: "workspace-write",
		ToolCount:      6,
		SessionCount:   0,
		SandboxRuntime: &status,
	})

	check := findCheck(t, report, "Sandbox")
	require.Equal(t, StatusWarn, check.Status)
	require.Contains(t, check.Summary, "not currently active")
	require.Contains(t, strings.Join(check.Details, "\n"), "Enabled: true")
	require.Contains(t, strings.Join(check.Details, "\n"), "Filesystem mode: workspace-only")
	require.Contains(t, strings.Join(check.Details, "\n"), "Fallback: sandbox strategy unavailable")
	require.Contains(t, strings.Join(check.Details, "\n"), "Capability gap: strategy: sandbox strategy unavailable")
	require.NotNil(t, check.Data)
	require.Equal(t, true, check.Data["enabled"])
	require.Equal(t, false, check.Data["active"])
	require.Equal(t, "workspace-only", check.Data["filesystem_mode"])
	require.Equal(t, []string{}, check.Data["allowed_mounts"])
	require.Equal(t, status.CapabilityGaps, check.Data["capability_gaps"])
	require.Contains(t, check.Hint, "supported sandbox strategy")
}

func runTestGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}

func runTestGitOutput(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
	return string(data)
}

func findCheck(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q in %#v", name, report.Checks)
	return Check{}
}

func clearProviderAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"XAI_API_KEY",
		"DASHSCOPE_API_KEY",
		"OLLAMA_HOST",
	} {
		t.Setenv(name, "")
	}
}

func clearProviderEndpointEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ANTHROPIC_BASE_URL",
		"OPENAI_BASE_URL",
		"XAI_BASE_URL",
		"DASHSCOPE_BASE_URL",
	} {
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
