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
	"sort"
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
	if isOpenAICompatibleModel(req.Model) {
		return c.streamOpenAICompatible(ctx, req, onText)
	}
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

func isOpenAICompatibleModel(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), "openai/")
}

func stripOpenAIModelPrefix(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "openai/")
}

type openAIRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Tools         []openAITool    `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions map[string]bool `json:"stream_options,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func (c *Client) streamOpenAICompatible(ctx context.Context, req Request, onText func(string)) (AssistantMessage, error) {
	wireReq, err := openAIRequestFromAnthropic(req)
	if err != nil {
		return AssistantMessage{}, err
	}
	body, err := json.Marshal(wireReq)
	if err != nil {
		return AssistantMessage{}, err
	}
	options := normalizeRateLimit(c.RateLimit)
	var lastErr error
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		httpReq, err := c.newOpenAIRequest(ctx, body)
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
			return parseOpenAIStream(resp.Body, onText)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		retryAfter := retryAfterDelay(resp.Header.Get("retry-after"), time.Now())
		statusErr := fmt.Errorf("openai-compatible request failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
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

func openAIRequestFromAnthropic(req Request) (openAIRequest, error) {
	messages := make([]openAIMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: strings.TrimSpace(req.System)})
	}
	for _, msg := range req.Messages {
		converted, err := openAIMessagesFromAnthropic(msg)
		if err != nil {
			return openAIRequest{}, err
		}
		messages = append(messages, converted...)
	}
	tools := make([]openAITool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	wire := openAIRequest{
		Model:         stripOpenAIModelPrefix(req.Model),
		Messages:      messages,
		Tools:         tools,
		Stream:        true,
		StreamOptions: map[string]bool{"include_usage": true},
		MaxTokens:     req.MaxTokens,
	}
	return wire, nil
}

func openAIMessagesFromAnthropic(msg Message) ([]openAIMessage, error) {
	role := strings.TrimSpace(msg.Role)
	if role == "" {
		return nil, errors.New("message role is required")
	}
	if role == "user" {
		var out []openAIMessage
		var text strings.Builder
		flushText := func() {
			if strings.TrimSpace(text.String()) != "" {
				out = append(out, openAIMessage{Role: "user", Content: text.String()})
				text.Reset()
			}
		}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			case "tool_result":
				flushText()
				out = append(out, openAIMessage{Role: "tool", ToolCallID: block.ToolUseID, Content: block.Content})
			}
		}
		flushText()
		return out, nil
	}
	if role == "assistant" {
		var text strings.Builder
		var toolCalls []openAIToolCall
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			case "tool_use":
				args := block.Input
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				toolCalls = append(toolCalls, openAIToolCall{
					ID:   block.ID,
					Type: "function",
					Function: openAIFunctionCall{
						Name:      block.Name,
						Arguments: string(args),
					},
				})
			}
		}
		return []openAIMessage{{Role: "assistant", Content: text.String(), ToolCalls: toolCalls}}, nil
	}
	return []openAIMessage{{Role: role, Content: contentText(msg.Content)}}, nil
}

func contentText(blocks []ContentBlock) string {
	var text strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n")
		}
		text.WriteString(block.Text)
	}
	return text.String()
}

func (c *Client) newOpenAIRequest(ctx context.Context, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIChatCompletionsURL(c.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	token := strings.TrimSpace(c.AuthToken)
	if token == "" {
		token = strings.TrimSpace(c.APIKey)
	}
	if token != "" {
		httpReq.Header.Set("authorization", "Bearer "+token)
	}
	return httpReq, nil
}

func openAIChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
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

type openAIStreamEnvelope struct {
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			Reasoning        string                `json:"reasoning"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type openAIToolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func parseOpenAIStream(r io.Reader, onText func(string)) (AssistantMessage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var text strings.Builder
	toolCalls := map[int]*openAIToolCallBuilder{}
	var usage Usage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var event openAIStreamEnvelope
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return AssistantMessage{}, err
		}
		if event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			usage = usageFromOpenAI(event.Usage)
		}
		for _, choice := range event.Choices {
			deltaText := choice.Delta.Content
			if deltaText == "" {
				deltaText = choice.Delta.ReasoningContent
			}
			if deltaText == "" {
				deltaText = choice.Delta.Reasoning
			}
			if deltaText != "" {
				text.WriteString(deltaText)
				if onText != nil {
					onText(deltaText)
				}
			}
			for _, toolDelta := range choice.Delta.ToolCalls {
				builder := toolCalls[toolDelta.Index]
				if builder == nil {
					builder = &openAIToolCallBuilder{}
					toolCalls[toolDelta.Index] = builder
				}
				if toolDelta.ID != "" {
					builder.id = toolDelta.ID
				}
				if toolDelta.Function.Name != "" {
					builder.name = toolDelta.Function.Name
				}
				if toolDelta.Function.Arguments != "" {
					builder.arguments.WriteString(toolDelta.Function.Arguments)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return AssistantMessage{}, err
	}
	blocks := []ContentBlock{}
	if text.Len() > 0 {
		blocks = append(blocks, ContentBlock{Type: "text", Text: text.String()})
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		builder := toolCalls[index]
		args := strings.TrimSpace(builder.arguments.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return AssistantMessage{}, fmt.Errorf("openai-compatible tool call %d arguments are not valid JSON", index)
		}
		blocks = append(blocks, ContentBlock{
			Type:  "tool_use",
			ID:    builder.id,
			Name:  builder.name,
			Input: json.RawMessage(args),
		})
	}
	return AssistantMessage{Blocks: blocks, Usage: usage}, nil
}

func usageFromOpenAI(usage openAIUsage) Usage {
	cached := usage.PromptTokensDetails.CachedTokens
	input := usage.PromptTokens
	if cached > 0 && cached <= input {
		input -= cached
	}
	return Usage{
		InputTokens:          input,
		OutputTokens:         usage.CompletionTokens,
		CacheReadInputTokens: cached,
	}
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
