package anttrace

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/stretchr/testify/require"
)

type fakeStreamer struct {
	message anthropic.AssistantMessage
	err     error
}

func (f fakeStreamer) Stream(_ context.Context, _ anthropic.Request, onText func(string)) (anthropic.AssistantMessage, error) {
	onText("trace ")
	onText("ok")
	return f.message, f.err
}

func TestRunSkippedRequest(t *testing.T) {
	report := Run(context.Background(), Options{
		Model:          "claude-test",
		BaseURL:        "http://provider.test/",
		AuthConfigured: true,
		NoRequest:      true,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})

	require.Equal(t, "ant_trace", report.Kind)
	require.Equal(t, "skipped", report.Status)
	require.False(t, report.RequestSent)
	require.Equal(t, "anthropic-compatible", report.Provider)
	require.Equal(t, "http://provider.test", report.BaseURL)
	require.Equal(t, "2026-01-02T03:04:05Z", report.CreatedAt)
	require.Contains(t, report.Messages, "Provider request skipped by --no-request.")
}

func TestRunStreamsAndRenders(t *testing.T) {
	report := Run(context.Background(), Options{
		Model:          "openai/gpt-test",
		AuthConfigured: true,
		Client: fakeStreamer{message: anthropic.AssistantMessage{
			Usage: anthropic.Usage{InputTokens: 11, OutputTokens: 7},
		}},
		RateLimit: anthropic.RateLimitReport{MaxRetries: 3, InitialBackoffMS: 100, MaxBackoffMS: 2000, RetryableStatuses: []int{429}},
	})

	require.Equal(t, "ok", report.Status)
	require.True(t, report.RequestSent)
	require.Equal(t, "openai-compatible", report.Provider)
	require.Equal(t, 2, report.StreamEvents)
	require.Equal(t, "trace ok", report.TextPreview)
	require.Equal(t, 11, report.Usage.InputTokens)
	require.Equal(t, 7, report.Usage.OutputTokens)

	var text bytes.Buffer
	RenderText(&text, report)
	require.Contains(t, text.String(), "Anthropic Trace")
	require.Contains(t, text.String(), "trace ok")
	require.Contains(t, text.String(), "retries=3")

	var jsonOut bytes.Buffer
	require.NoError(t, RenderJSON(&jsonOut, report))
	require.Contains(t, jsonOut.String(), `"kind": "ant_trace"`)
	require.Contains(t, jsonOut.String(), `"stream_events": 2`)

	markdown := RenderMarkdown(report)
	require.Contains(t, markdown, "# Codog Anthropic Trace")
	require.Contains(t, markdown, "Raw Data")
}

func TestRunReportsStreamError(t *testing.T) {
	report := Run(context.Background(), Options{
		Model:  "claude-test",
		Client: fakeStreamer{err: errors.New("provider unavailable")},
	})

	require.Equal(t, "error", report.Status)
	require.True(t, report.RequestSent)
	require.Equal(t, "provider unavailable", report.Error)
	require.Equal(t, "trace ok", report.TextPreview)
}
