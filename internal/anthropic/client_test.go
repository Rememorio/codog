package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/stretchr/testify/require"
)

func TestClientStreamsText(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "hello from mock"}.Handler())
	defer server.Close()

	client := New(server.URL, "test", "")
	var streamed strings.Builder
	msg, err := client.Stream(context.Background(), Request{
		Model:     "mock",
		MaxTokens: 64,
		Messages:  []Message{TextMessage("user", "hi")},
	}, func(delta string) {
		streamed.WriteString(delta)
	})
	require.NoError(t, err)
	require.Contains(t, streamed.String(), "hello from mock")
	require.Len(t, msg.Blocks, 1)
	require.Contains(t, msg.Blocks[0].Text, "hello from mock")
	require.Equal(t, 10, msg.Usage.InputTokens)
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
