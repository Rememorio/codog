package anttrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Streamer interface {
	Stream(context.Context, anthropic.Request, func(string)) (anthropic.AssistantMessage, error)
}

type Options struct {
	Provider       string
	Model          string
	BaseURL        string
	AuthConfigured bool
	RateLimit      anthropic.RateLimitReport
	Message        string
	Timeout        time.Duration
	NoRequest      bool
	Client         Streamer
	CreatedAt      time.Time
}

type Report struct {
	Kind           string                    `json:"kind"`
	Action         string                    `json:"action"`
	Status         string                    `json:"status"`
	Provider       string                    `json:"provider"`
	Model          string                    `json:"model"`
	BaseURL        string                    `json:"base_url"`
	AuthConfigured bool                      `json:"auth_configured"`
	RequestSent    bool                      `json:"request_sent"`
	TimeoutMS      int                       `json:"timeout_ms"`
	ElapsedMS      int64                     `json:"elapsed_ms"`
	StreamEvents   int                       `json:"stream_events"`
	TextPreview    string                    `json:"text_preview,omitempty"`
	Usage          anthropic.Usage           `json:"usage,omitempty"`
	RateLimit      anthropic.RateLimitReport `json:"rate_limit"`
	Error          string                    `json:"error,omitempty"`
	File           string                    `json:"file,omitempty"`
	Bytes          int                       `json:"bytes,omitempty"`
	Messages       []string                  `json:"messages,omitempty"`
	CreatedAt      string                    `json:"created_at,omitempty"`
}

func Run(ctx context.Context, options Options) Report {
	options = normalize(options)
	report := Report{
		Kind:           "ant_trace",
		Action:         "trace",
		Status:         "ok",
		Provider:       options.Provider,
		Model:          options.Model,
		BaseURL:        options.BaseURL,
		AuthConfigured: options.AuthConfigured,
		RequestSent:    false,
		TimeoutMS:      int(options.Timeout / time.Millisecond),
		RateLimit:      options.RateLimit,
		Messages:       []string{"Provider configuration loaded."},
		CreatedAt:      options.CreatedAt.UTC().Format(time.RFC3339),
	}
	if options.NoRequest {
		report.Status = "skipped"
		report.Messages = append(report.Messages, "Provider request skipped by --no-request.")
		return report
	}
	if options.Client == nil {
		report.Status = "error"
		report.Error = "provider client is not configured"
		return report
	}
	if strings.TrimSpace(options.Model) == "" {
		report.Status = "error"
		report.Error = "model is not configured"
		return report
	}
	if !options.AuthConfigured {
		report.Messages = append(report.Messages, "No API key or auth token is configured; the provider may reject the request.")
	}

	requestCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	start := time.Now()
	var chunks []string
	previewLen := 0
	message, err := options.Client.Stream(requestCtx, anthropic.Request{
		Model:     options.Model,
		MaxTokens: 64,
		Messages: []anthropic.Message{
			anthropic.TextMessage("user", options.Message),
		},
	}, func(text string) {
		report.StreamEvents++
		if previewLen < 512 {
			chunks = append(chunks, text)
			previewLen += len(text)
		}
	})
	report.ElapsedMS = time.Since(start).Milliseconds()
	report.RequestSent = true
	report.TextPreview = truncate(strings.TrimSpace(strings.Join(chunks, "")), 512)
	report.Usage = message.Usage
	if err != nil {
		report.Status = "error"
		report.Error = err.Error()
		return report
	}
	report.Messages = append(report.Messages, "Provider streaming request completed.")
	return report
}

func RenderText(out io.Writer, report Report) {
	fmt.Fprintln(out, "Anthropic Trace")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Provider         %s\n", report.Provider)
	fmt.Fprintf(out, "  Model            %s\n", report.Model)
	fmt.Fprintf(out, "  Base URL         %s\n", report.BaseURL)
	fmt.Fprintf(out, "  Auth configured  %t\n", report.AuthConfigured)
	fmt.Fprintf(out, "  Request sent     %t\n", report.RequestSent)
	fmt.Fprintf(out, "  Timeout          %dms\n", report.TimeoutMS)
	fmt.Fprintf(out, "  Elapsed          %dms\n", report.ElapsedMS)
	fmt.Fprintf(out, "  Stream events    %d\n", report.StreamEvents)
	if report.TextPreview != "" {
		fmt.Fprintf(out, "  Text preview     %s\n", report.TextPreview)
	}
	if report.Usage.InputTokens != 0 || report.Usage.OutputTokens != 0 || report.Usage.CacheCreationInputTokens != 0 || report.Usage.CacheReadInputTokens != 0 {
		fmt.Fprintf(out, "  Usage            input=%d output=%d cache_create=%d cache_read=%d\n", report.Usage.InputTokens, report.Usage.OutputTokens, report.Usage.CacheCreationInputTokens, report.Usage.CacheReadInputTokens)
	}
	fmt.Fprintf(out, "  Rate limit       retries=%d initial=%dms max=%dms statuses=%v\n", report.RateLimit.MaxRetries, report.RateLimit.InitialBackoffMS, report.RateLimit.MaxBackoffMS, report.RateLimit.RetryableStatuses)
	if report.File != "" {
		fmt.Fprintf(out, "  File             %s\n", report.File)
	}
	if report.Error != "" {
		fmt.Fprintf(out, "  Error            %s\n", report.Error)
	}
	for _, message := range report.Messages {
		fmt.Fprintf(out, "  Message          %s\n", message)
	}
}

func RenderJSON(out io.Writer, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func RenderMarkdown(report Report) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Codog Anthropic Trace")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Status: `%s`\n", report.Status)
	fmt.Fprintf(&b, "- Provider: `%s`\n", report.Provider)
	fmt.Fprintf(&b, "- Model: `%s`\n", report.Model)
	fmt.Fprintf(&b, "- Base URL: `%s`\n", report.BaseURL)
	fmt.Fprintf(&b, "- Auth configured: `%t`\n", report.AuthConfigured)
	fmt.Fprintf(&b, "- Request sent: `%t`\n", report.RequestSent)
	fmt.Fprintf(&b, "- Timeout: `%dms`\n", report.TimeoutMS)
	fmt.Fprintf(&b, "- Elapsed: `%dms`\n", report.ElapsedMS)
	fmt.Fprintf(&b, "- Stream events: `%d`\n", report.StreamEvents)
	if report.Error != "" {
		fmt.Fprintf(&b, "- Error: `%s`\n", report.Error)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Rate Limit")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Max retries: `%d`\n", report.RateLimit.MaxRetries)
	fmt.Fprintf(&b, "- Initial backoff: `%dms`\n", report.RateLimit.InitialBackoffMS)
	fmt.Fprintf(&b, "- Max backoff: `%dms`\n", report.RateLimit.MaxBackoffMS)
	fmt.Fprintf(&b, "- Retryable statuses: `%v`\n", report.RateLimit.RetryableStatuses)
	if report.TextPreview != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Text Preview")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "```")
		fmt.Fprintln(&b, report.TextPreview)
		fmt.Fprintln(&b, "```")
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Raw Data")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "```json")
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(&b, string(data))
	fmt.Fprintln(&b, "```")
	return b.String()
}

func normalize(options Options) Options {
	options.Provider = strings.TrimSpace(options.Provider)
	if options.Provider == "" {
		options.Provider = inferProvider(options.Model)
	}
	options.Model = strings.TrimSpace(options.Model)
	options.BaseURL = strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if options.BaseURL == "" {
		options.BaseURL = "https://api.anthropic.com"
	}
	options.Message = strings.TrimSpace(options.Message)
	if options.Message == "" {
		options.Message = "Reply with a short provider trace acknowledgement."
	}
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	if options.CreatedAt.IsZero() {
		options.CreatedAt = time.Now().UTC()
	}
	return options
}

func inferProvider(model string) string {
	if strings.HasPrefix(strings.TrimSpace(model), "openai/") {
		return "openai-compatible"
	}
	return "anthropic-compatible"
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
