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
		"kimi/kimi-k2.5":             ProviderDashScope,
		"kimi-k2.5":                  ProviderDashScope,
		"claude-sonnet-4-5":          ProviderAnthropic,
	}

	for model, provider := range tests {
		require.Equal(t, provider, ProviderForModel(model), model)
	}
}

func TestWireModelForBaseURLStripsRoutingPrefixes(t *testing.T) {
	require.Equal(t, "gpt-4o", WireModelForBaseURL("openai/gpt-4o", DefaultOpenAIBaseURL))
	require.Equal(t, "qwen2.5-coder:7b", WireModelForBaseURL("openai/qwen2.5-coder:7b", "http://127.0.0.1:11434/v1"))
	require.Equal(t, "openai/gpt-4.1-mini", WireModelForBaseURL("openai/gpt-4.1-mini", "https://openrouter.ai/api/v1"))
	require.Equal(t, "Qwen/Qwen3.6-27B-FP8", WireModelForBaseURL("local/Qwen/Qwen3.6-27B-FP8", "http://127.0.0.1:8000/v1"))
	require.Equal(t, "qwen-max", WireModelForBaseURL("qwen/qwen-max", DefaultDashScopeBaseURL))
	require.Equal(t, "kimi-k2.5", WireModelForBaseURL("kimi/kimi-k2.5", DefaultDashScopeBaseURL))
	require.Equal(t, "vendor/model", WireModelForBaseURL("vendor/model", "https://gateway.example/v1"))
}
