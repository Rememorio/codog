package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicVersion = "2023-06-01"

type Client struct {
	HTTP      *http.Client
	BaseURL   string
	APIKey    string
	AuthToken string
	RateLimit RateLimitOptions
	Sleep     func(context.Context, time.Duration) error
}

type RateLimitOptions struct {
	MaxRetries     int           `json:"max_retries"`
	InitialBackoff time.Duration `json:"initial_backoff"`
	MaxBackoff     time.Duration `json:"max_backoff"`
}

type RateLimitReport struct {
	MaxRetries        int   `json:"max_retries"`
	InitialBackoffMS  int   `json:"initial_backoff_ms"`
	MaxBackoffMS      int   `json:"max_backoff_ms"`
	RetryableStatuses []int `json:"retryable_statuses"`
}

func New(baseURL, apiKey, authToken string) *Client {
	return NewWithRateLimit(baseURL, apiKey, authToken, DefaultRateLimitOptions())
}

func NewWithRateLimit(baseURL, apiKey, authToken string, rateLimit RateLimitOptions) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &Client{
		HTTP: &http.Client{
			Timeout: 10 * time.Minute,
		},
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    apiKey,
		AuthToken: authToken,
		RateLimit: normalizeRateLimit(rateLimit),
	}
}

func DefaultRateLimitOptions() RateLimitOptions {
	return RateLimitOptions{
		MaxRetries:     2,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
	}
}

func (o RateLimitOptions) Report() RateLimitReport {
	o = normalizeRateLimit(o)
	return RateLimitReport{
		MaxRetries:        o.MaxRetries,
		InitialBackoffMS:  int(o.InitialBackoff / time.Millisecond),
		MaxBackoffMS:      int(o.MaxBackoff / time.Millisecond),
		RetryableStatuses: []int{429, 500, 502, 503, 504},
	}
}

func (c *Client) Stream(ctx context.Context, req Request, onText func(string)) (AssistantMessage, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return AssistantMessage{}, err
	}

	options := normalizeRateLimit(c.RateLimit)
	var lastErr error
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		httpReq, err := c.newRequest(ctx, body)
		if err != nil {
			return AssistantMessage{}, err
		}
		resp, err := c.http().Do(httpReq)
		if err != nil {
			lastErr = err
			if attempt < options.MaxRetries {
				if sleepErr := c.sleep(ctx, backoffDelay(options, attempt, 0)); sleepErr != nil {
					return AssistantMessage{}, sleepErr
				}
				continue
			}
			return AssistantMessage{}, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			return parseStream(resp.Body, onText)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		retryAfter := retryAfterDelay(resp.Header.Get("retry-after"), time.Now())
		statusErr := fmt.Errorf("anthropic request failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
		_ = resp.Body.Close()
		lastErr = statusErr
		if attempt < options.MaxRetries && retryableStatus(resp.StatusCode) {
			if sleepErr := c.sleep(ctx, backoffDelay(options, attempt, retryAfter)); sleepErr != nil {
				return AssistantMessage{}, sleepErr
			}
			continue
		}
		return AssistantMessage{}, statusErr
	}
	return AssistantMessage{}, lastErr
}

func (c *Client) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}
	if c.AuthToken != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.AuthToken)
	}
	return httpReq, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeRateLimit(options RateLimitOptions) RateLimitOptions {
	defaults := DefaultRateLimitOptions()
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	}
	if options.MaxRetries == 0 {
		options.MaxRetries = defaults.MaxRetries
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = defaults.InitialBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = defaults.MaxBackoff
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = options.InitialBackoff
	}
	return options
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func backoffDelay(options RateLimitOptions, attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > options.MaxBackoff {
			return options.MaxBackoff
		}
		return retryAfter
	}
	delay := options.InitialBackoff
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= options.MaxBackoff {
			return options.MaxBackoff
		}
	}
	return delay
}

func retryAfterDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		return seconds
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

type streamEnvelope struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Usage Usage `json:"usage"`
}

type blockBuilder struct {
	block     ContentBlock
	inputJSON strings.Builder
}

func parseStream(r io.Reader, onText func(string)) (AssistantMessage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var dataLines []string
	builders := map[int]*blockBuilder{}
	var usage Usage
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consumeEvent(dataLines, builders, &usage, onText); err != nil {
				return AssistantMessage{}, err
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) > 0 {
		if err := consumeEvent(dataLines, builders, &usage, onText); err != nil {
			return AssistantMessage{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, err
	}

	blocks := make([]ContentBlock, 0, len(builders))
	for index := 0; index < len(builders); index++ {
		builder, ok := builders[index]
		if !ok {
			continue
		}
		if builder.block.Type == "tool_use" {
			raw := strings.TrimSpace(builder.inputJSON.String())
			if raw == "" {
				raw = "{}"
			}
			builder.block.Input = json.RawMessage(raw)
		}
		blocks = append(blocks, builder.block)
	}
	return AssistantMessage{Blocks: blocks, Usage: usage}, nil
}

func consumeEvent(lines []string, builders map[int]*blockBuilder, usage *Usage, onText func(string)) error {
	if len(lines) == 0 {
		return nil
	}
	payload := strings.Join(lines, "\n")
	if payload == "[DONE]" {
		return nil
	}
	var event streamEnvelope
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return err
	}
	switch event.Type {
	case "content_block_start":
		builder := &blockBuilder{}
		switch event.ContentBlock.Type {
		case "text":
			builder.block = ContentBlock{Type: "text", Text: event.ContentBlock.Text}
		case "tool_use":
			builder.block = ContentBlock{
				Type:  "tool_use",
				ID:    event.ContentBlock.ID,
				Name:  event.ContentBlock.Name,
				Input: event.ContentBlock.Input,
			}
			if len(event.ContentBlock.Input) != 0 && string(event.ContentBlock.Input) != "null" {
				builder.inputJSON.Write(event.ContentBlock.Input)
			}
		default:
			builder.block = ContentBlock{Type: event.ContentBlock.Type}
		}
		builders[event.Index] = builder
	case "content_block_delta":
		builder := builders[event.Index]
		if builder == nil {
			return errors.New("received content delta before content start")
		}
		switch event.Delta.Type {
		case "text_delta":
			builder.block.Text += event.Delta.Text
			if onText != nil {
				onText(event.Delta.Text)
			}
		case "input_json_delta":
			builder.inputJSON.WriteString(event.Delta.PartialJSON)
		}
	case "message_delta":
		*usage = event.Usage
	case "message_stop", "content_block_stop", "message_start", "ping":
		return nil
	case "error":
		return fmt.Errorf("anthropic stream error: %s", payload)
	}
	return nil
}
