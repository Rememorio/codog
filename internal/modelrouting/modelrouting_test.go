package modelrouting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderForModelRoutesOpenAICompatiblePrefixes(t *testing.T) {
	tests := map[string]string{
		"openai/gpt-4o":              ProviderOpenAI,
		"gpt-4.1-mini":               ProviderOpenAI,
		"local/Qwen/Qwen3.6-27B-FP8": ProviderOpenAI,
		"qwen/qwen-max":              ProviderDashScope,
		"qwen-plus":                  ProviderDashScope,
		"kimi":                       ProviderDashScope,
		"kimi/kimi-k2.5":             ProviderDashScope,
		"kimi-k2.5":                  ProviderDashScope,
		"opus":                       ProviderAnthropic,
		"claude-sonnet-4-5":          ProviderAnthropic,
	}

	for model, provider := range tests {
		require.Equal(t, provider, ProviderForModel(model), model)
	}
}

func TestResolveAliasAndTokenLimits(t *testing.T) {
	require.Equal(t, "claude-opus-4-7", ResolveAlias("OPUS"))
	require.Equal(t, "claude-sonnet-4-6", ResolveAlias("sonnet"))
	require.Equal(t, "claude-haiku-4-5-20251213", ResolveAlias("haiku"))
	require.Equal(t, "kimi-k2.5", ResolveAlias("kimi"))
	require.Equal(t, "custom-model", ResolveAlias(" custom-model "))

	limit, ok := TokenLimitForModel("kimi")
	require.True(t, ok)
	require.Equal(t, 16384, limit.MaxOutputTokens)
	require.Equal(t, 256000, limit.ContextWindowTokens)

	limit, ok = TokenLimitForModel("openai/gpt-4.1-mini")
	require.True(t, ok)
	require.Equal(t, 32768, limit.MaxOutputTokens)
	require.Equal(t, 1047576, limit.ContextWindowTokens)

	limit, ok = TokenLimitForModel("claude-sonnet-4-5")
	require.True(t, ok)
	require.Equal(t, 64000, limit.MaxOutputTokens)
	require.Equal(t, 200000, limit.ContextWindowTokens)
}

func TestWireModelForBaseURLStripsRoutingPrefixes(t *testing.T) {
	require.Equal(t, "gpt-4o", WireModelForBaseURL("openai/gpt-4o", DefaultOpenAIBaseURL))
	require.Equal(t, "kimi-k2.5", WireModelForBaseURL("kimi", DefaultDashScopeBaseURL))
	require.Equal(t, "qwen2.5-coder:7b", WireModelForBaseURL("openai/qwen2.5-coder:7b", "http://127.0.0.1:11434/v1"))
	require.Equal(t, "openai/gpt-4.1-mini", WireModelForBaseURL("openai/gpt-4.1-mini", "https://openrouter.ai/api/v1"))
	require.Equal(t, "Qwen/Qwen3.6-27B-FP8", WireModelForBaseURL("local/Qwen/Qwen3.6-27B-FP8", "http://127.0.0.1:8000/v1"))
	require.Equal(t, "qwen-max", WireModelForBaseURL("qwen/qwen-max", DefaultDashScopeBaseURL))
	require.Equal(t, "kimi-k2.5", WireModelForBaseURL("kimi/kimi-k2.5", DefaultDashScopeBaseURL))
	require.Equal(t, "vendor/model", WireModelForBaseURL("vendor/model", "https://gateway.example/v1"))
}

func TestModelRejectsIsErrorFieldOnlyForKimiFamily(t *testing.T) {
	require.True(t, ModelRejectsIsErrorField("kimi"))
	require.True(t, ModelRejectsIsErrorField("kimi-k2.5"))
	require.True(t, ModelRejectsIsErrorField("kimi-k1.5"))
	require.True(t, ModelRejectsIsErrorField("dashscope/kimi-k2.5"))
	require.True(t, ModelRejectsIsErrorField("moonshot/kimi-moonshot"))
	require.True(t, ModelRejectsIsErrorField("KIMI-K2.5"))

	require.False(t, ModelRejectsIsErrorField("gpt-4o"))
	require.False(t, ModelRejectsIsErrorField("qwen/qwen-plus"))
	require.False(t, ModelRejectsIsErrorField("claude-sonnet-4-6"))
}
