package providerdiag

import (
	"testing"

	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/stretchr/testify/require"
)

func TestAnalyzeAuthReportsAnthropicAPIKeyAndBearer(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-real")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-bearer")
	diag := AnalyzeAuth(AuthOptions{
		Model:     "claude-test",
		BaseURL:   modelrouting.DefaultAnthropicBaseURL,
		APIKey:    "sk-ant-api03-real",
		AuthToken: "stale-bearer",
	})

	require.Equal(t, modelrouting.ProviderAnthropic, diag.SelectedProvider)
	require.True(t, diag.SelectedProviderAPIKeyPresent)
	require.True(t, diag.SelectedProviderAuthTokenPresent)
	require.True(t, diag.SelectedProviderAuthPresent)
	require.True(t, diag.SelectedProviderBothAuthPresent)
	require.True(t, diag.BothAnthropicAuthEnvVarsPresent)
	require.Equal(t, "api_key_and_bearer", diag.EffectiveAuthSource)
	require.Equal(t, []string{"x-api-key", "authorization_bearer"}, diag.HeadersSent)
	require.Contains(t, diag.Warning, "both ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN")
	require.Contains(t, diag.Hint, "sk-ant-* API keys")
}

func TestAnalyzeAuthReportsProviderSpecificAPIKey(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	diag := AnalyzeAuth(AuthOptions{
		Model:           "openai/gpt-4.1-mini",
		BaseURL:         modelrouting.DefaultOpenAIBaseURL,
		APIKey:          "sk-test",
		RuntimeProvider: modelrouting.ProviderOpenAI,
	})

	require.Equal(t, modelrouting.ProviderOpenAI, diag.SelectedProvider)
	require.Equal(t, "OPENAI_API_KEY", diag.RequiredAPIKeyEnv)
	require.Equal(t, "api_key", diag.EffectiveAuthSource)
	require.Equal(t, []string{"authorization_bearer"}, diag.HeadersSent)
	require.Empty(t, diag.Warning)
	require.False(t, diag.BothAnthropicAuthEnvVarsPresent)
}

func TestAnalyzeAuthTreatsLocalOpenAIRouteAsOptional(t *testing.T) {
	clearAuthEnv(t)
	diag := AnalyzeAuth(AuthOptions{
		Model:                 "qwen3:8b",
		RuntimeProvider:       modelrouting.ProviderOpenAI,
		RuntimeProviderSource: "OPENAI_BASE_URL",
		BaseURL:               "http://127.0.0.1:11434/v1",
	})

	require.True(t, diag.AuthOptional)
	require.True(t, diag.SelectedProviderAuthPresent)
	require.Equal(t, "OPENAI_BASE_URL", diag.EffectiveAuthSource)
	require.Empty(t, diag.RequiredAPIKeyEnv)
	require.Empty(t, diag.HeadersSent)
}

func clearAuthEnv(t *testing.T) {
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
