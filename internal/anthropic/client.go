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
}

func New(baseURL, apiKey, authToken string) *Client {
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
	}
}

func (c *Client) Stream(ctx context.Context, req Request, onText func(string)) (AssistantMessage, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return AssistantMessage{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return AssistantMessage{}, err
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

	resp, err := c.http().Do(httpReq)
	if err != nil {
		return AssistantMessage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return AssistantMessage{}, fmt.Errorf("anthropic request failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return parseStream(resp.Body, onText)
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
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
