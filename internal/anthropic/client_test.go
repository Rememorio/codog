package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/stretchr/testify/require"
)

func TestClientStreamsText(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(mockanthropic.Server{
		Text: "hello from mock",
		OnRequest: func(data json.RawMessage) {
			require.NoError(t, json.Unmarshal(data, &requestBody))
		},
	}.Handler())
	defer server.Close()

	client := New(server.URL, "test", "")
	var streamed strings.Builder
	temperature := 0.25
	msg, err := client.Stream(context.Background(), Request{
		Model:       "mock",
		MaxTokens:   64,
		Temperature: &temperature,
		Messages:    []Message{TextMessage("user", "hi")},
	}, func(delta string) {
		streamed.WriteString(delta)
	})
	require.NoError(t, err)
	require.InDelta(t, 0.25, requestBody["temperature"].(float64), 0.0001)
	require.Contains(t, streamed.String(), "hello from mock")
	require.Len(t, msg.Blocks, 1)
	require.Contains(t, msg.Blocks[0].Text, "hello from mock")
	require.Equal(t, 10, msg.Usage.InputTokens)
}

func TestClientStreamsAnthropicAliasAsResolvedModel(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(mockanthropic.Server{
		Text: "hello from alias",
		OnRequest: func(data json.RawMessage) {
			require.NoError(t, json.Unmarshal(data, &requestBody))
		},
	}.Handler())
	defer server.Close()

	client := New(server.URL, "test", "")
	msg, err := client.Stream(context.Background(), Request{
		Model:     "opus",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-7", requestBody["model"])
	require.Contains(t, msg.Blocks[0].Text, "hello from alias")
}

func TestClientStreamsOpenAICompatibleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer openai-key", r.Header.Get("authorization"))
		require.Empty(t, r.Header.Get("x-api-key"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "gpt-4o", body["model"])
		require.Equal(t, true, body["stream"])
		require.InDelta(t, 0.25, body["temperature"].(float64), 0.0001)
		messages := body["messages"].([]any)
		require.Equal(t, "system", messages[0].(map[string]any)["role"])
		require.Equal(t, "Be concise.", messages[0].(map[string]any)["content"])
		streamOptions := body["stream_options"].(map[string]any)
		require.Equal(t, true, streamOptions["include_usage"])

		w.Header().Set("content-type", "text/event-stream")
		writeOpenAISSE(t, w,
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "hello "}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "world"}}}},
			map[string]any{
				"choices": []any{},
				"usage": map[string]any{
					"prompt_tokens":     7,
					"completion_tokens": 2,
					"prompt_tokens_details": map[string]any{
						"cached_tokens": 3,
					},
				},
			},
		)
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "openai-key", "")
	var streamed strings.Builder
	temperature := 0.25
	msg, err := client.Stream(context.Background(), Request{
		Model:       "openai/gpt-4o",
		MaxTokens:   64,
		Temperature: &temperature,
		System:      "Be concise.",
		Messages:    []Message{TextMessage("user", "hi")},
	}, func(delta string) {
		streamed.WriteString(delta)
	})

	require.NoError(t, err)
	require.Equal(t, "hello world", streamed.String())
	require.Len(t, msg.Blocks, 1)
	require.Equal(t, "hello world", msg.Blocks[0].Text)
	require.Equal(t, 4, msg.Usage.InputTokens)
	require.Equal(t, 3, msg.Usage.CacheReadInputTokens)
	require.Equal(t, 2, msg.Usage.OutputTokens)
}

func TestClientStreamsGPTModelThroughOpenAICompatibleRoute(t *testing.T) {
	assertOpenAICompatibleRequestModel(t, "gpt-4.1-mini", "gpt-4.1-mini")
}

func TestClientStreamsLocalModelStripsRoutingPrefix(t *testing.T) {
	assertOpenAICompatibleRequestModel(t, "local/Qwen/Qwen3.6-27B-FP8", "Qwen/Qwen3.6-27B-FP8")
}

func TestClientStreamsDashScopeNamespacedModelStripsRoutingPrefix(t *testing.T) {
	assertOpenAICompatibleRequestModel(t, "qwen/qwen-max", "qwen-max")
	assertOpenAICompatibleRequestModel(t, "kimi/kimi-k2.5", "kimi-k2.5")
}

func TestClientStreamsDashScopeAliasThroughOpenAICompatibleRoute(t *testing.T) {
	assertOpenAICompatibleRequestModel(t, "kimi", "kimi-k2.5")
}

func TestOpenAIWireModelPreservesNamespacedModelForCustomGateway(t *testing.T) {
	wire, err := openAIRequestFromAnthropic(Request{
		Model:     "openai/gpt-4.1-mini",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, "https://openrouter.ai/api/v1")
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4.1-mini", wire.Model)
}

func TestOpenAICompatibleReasoningModelStripsTemperature(t *testing.T) {
	temperature := 0.7
	wire, err := openAIRequestFromAnthropic(Request{
		Model:       "o1-mini",
		MaxTokens:   64,
		Temperature: &temperature,
		Messages:    []Message{TextMessage("user", "hi")},
	}, "https://api.openai.com/v1")
	require.NoError(t, err)
	require.Nil(t, wire.Temperature)
	require.Equal(t, 64, wire.MaxTokens)
	require.Zero(t, wire.MaxCompletionTokens)
}

func TestOpenAICompatibleGPT5UsesMaxCompletionTokens(t *testing.T) {
	temperature := 0.7
	wire, err := openAIRequestFromAnthropic(Request{
		Model:       "openai/gpt-5.4",
		MaxTokens:   512,
		Temperature: &temperature,
		Messages:    []Message{TextMessage("user", "hi")},
	}, "https://api.openai.com/v1")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", wire.Model)
	require.Zero(t, wire.MaxTokens)
	require.Equal(t, 512, wire.MaxCompletionTokens)
	data, err := json.Marshal(wire)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.NotContains(t, payload, "max_tokens")
	require.Equal(t, float64(512), payload["max_completion_tokens"])
}

func TestOpenAICompatibleToolResultsIncludeIsErrorForNonKimiModels(t *testing.T) {
	wire, err := openAIRequestFromAnthropic(Request{
		Model:     "gpt-4o",
		MaxTokens: 64,
		Messages: []Message{
			ToolResultMessage("call_1", "failed", true),
			ToolResultMessage("call_2", "ok", false),
		},
	}, "https://api.openai.com/v1")
	require.NoError(t, err)
	require.Len(t, wire.Messages, 2)
	require.NotNil(t, wire.Messages[0].IsError)
	require.True(t, *wire.Messages[0].IsError)
	require.NotNil(t, wire.Messages[1].IsError)
	require.False(t, *wire.Messages[1].IsError)
}

func TestOpenAICompatibleToolResultsOmitIsErrorForKimiModels(t *testing.T) {
	for _, model := range []string{"kimi", "kimi-k2.5", "kimi/kimi-k2.5"} {
		wire, err := openAIRequestFromAnthropic(Request{
			Model:     model,
			MaxTokens: 64,
			Messages:  []Message{ToolResultMessage("call_1", "failed", true)},
		}, "https://dashscope.aliyuncs.com/compatible-mode/v1")
		require.NoError(t, err)
		require.Len(t, wire.Messages, 1)
		require.Nil(t, wire.Messages[0].IsError, model)
	}
}

func TestSanitizeOpenAIToolMessagePairing(t *testing.T) {
	valid := []openAIMessage{
		{
			Role: "assistant",
			ToolCalls: []openAIToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: openAIFunctionCall{
					Name:      "search",
					Arguments: "{}",
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}
	require.Len(t, sanitizeOpenAIToolMessagePairing(valid), 2)

	orphaned := []openAIMessage{
		{Role: "assistant", Content: "hello"},
		{Role: "tool", ToolCallID: "call_2", Content: "orphaned"},
	}
	sanitized := sanitizeOpenAIToolMessagePairing(orphaned)
	require.Len(t, sanitized, 1)
	require.Equal(t, "assistant", sanitized[0].Role)

	mismatched := []openAIMessage{
		{
			Role: "assistant",
			ToolCalls: []openAIToolCall{{
				ID:   "call_3",
				Type: "function",
				Function: openAIFunctionCall{
					Name:      "read_file",
					Arguments: "{}",
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_wrong", Content: "bad"},
	}
	require.Len(t, sanitizeOpenAIToolMessagePairing(mismatched), 1)

	twoResults := []openAIMessage{
		{
			Role: "assistant",
			ToolCalls: []openAIToolCall{
				{ID: "call_a", Type: "function", Function: openAIFunctionCall{Name: "a", Arguments: "{}"}},
				{ID: "call_b", Type: "function", Function: openAIFunctionCall{Name: "b", Arguments: "{}"}},
			},
		},
		{Role: "tool", ToolCallID: "call_a", Content: "a"},
		{Role: "tool", ToolCallID: "call_b", Content: "b"},
	}
	require.Len(t, sanitizeOpenAIToolMessagePairing(twoResults), 3)

	userPreceding := []openAIMessage{
		{Role: "user", Content: "mixed content"},
		{Role: "tool", ToolCallID: "call_mixed", Content: "kept"},
	}
	require.Len(t, sanitizeOpenAIToolMessagePairing(userPreceding), 2)
}

func TestOpenAIRequestDropsAssistantOrphanedToolResults(t *testing.T) {
	wire, err := openAIRequestFromAnthropic(Request{
		Model:     "gpt-4o",
		MaxTokens: 64,
		Messages: []Message{
			TextMessage("assistant", "hello"),
			ToolResultMessage("call_orphan", "orphaned", true),
		},
	}, "https://api.openai.com/v1")
	require.NoError(t, err)
	require.Len(t, wire.Messages, 1)
	require.Equal(t, "assistant", wire.Messages[0].Role)
}

func TestClientStreamsOpenAICompatibleToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		tools := body["tools"].([]any)
		tool := tools[0].(map[string]any)
		require.Equal(t, "function", tool["type"])
		function := tool["function"].(map[string]any)
		require.Equal(t, "read_file", function["name"])
		require.Equal(t, "Read a file.", function["description"])

		w.Header().Set("content-type", "text/event-stream")
		writeOpenAISSE(t, w,
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{
					"index": 0,
					"id":    "call_1",
					"type":  "function",
					"function": map[string]any{
						"name":      "read_file",
						"arguments": `{"pa`,
					},
				},
			}}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{
					"index": 0,
					"function": map[string]any{
						"arguments": `th":"README.md"}`,
					},
				},
			}}}}},
		)
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "openai-key", "")
	msg, err := client.Stream(context.Background(), Request{
		Model:     "openai/gpt-4o",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "read it")},
		Tools: []ToolDefinition{{
			Name:        "read_file",
			Description: "Read a file.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		}},
	}, nil)

	require.NoError(t, err)
	require.Len(t, msg.Blocks, 1)
	require.Equal(t, "tool_use", msg.Blocks[0].Type)
	require.Equal(t, "call_1", msg.Blocks[0].ID)
	require.Equal(t, "read_file", msg.Blocks[0].Name)
	require.JSONEq(t, `{"path":"README.md"}`, string(msg.Blocks[0].Input))
}

func assertOpenAICompatibleRequestModel(t *testing.T, model string, expectedWireModel string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, expectedWireModel, body["model"])
		w.Header().Set("content-type", "text/event-stream")
		writeOpenAISSE(t, w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}}})
	}))
	defer server.Close()

	client := New(server.URL+"/v1", "provider-key", "")
	msg, err := client.Stream(context.Background(), Request{
		Model:     model,
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, nil)

	require.NoError(t, err)
	require.Len(t, msg.Blocks, 1)
	require.Equal(t, "ok", msg.Blocks[0].Text)
}

func TestClientRetriesRateLimitedRequests(t *testing.T) {
	attempts := 0
	success := mockanthropic.Server{Text: "retry success"}.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("retry-after", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		success.ServeHTTP(w, r)
	}))
	defer server.Close()

	var delays []time.Duration
	client := NewWithRateLimit(server.URL, "test", "", RateLimitOptions{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	})
	client.Sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	msg, err := client.Stream(context.Background(), Request{
		Model:     "mock",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, nil)

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
	require.Len(t, delays, 2)
	require.Equal(t, 20*time.Millisecond, delays[0])
	require.Contains(t, msg.Blocks[0].Text, "retry success")
}

func writeOpenAISSE(t *testing.T, w http.ResponseWriter, payloads ...any) {
	t.Helper()
	for _, payload := range payloads {
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
		require.NoError(t, err)
	}
	_, err := fmt.Fprint(w, "data: [DONE]\n\n")
	require.NoError(t, err)
}
