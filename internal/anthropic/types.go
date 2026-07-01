package anthropic

import "encoding/json"

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Request struct {
	Model       string           `json:"model"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature *float64         `json:"temperature,omitempty"`
	System      string           `json:"system,omitempty"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Stream      bool             `json:"stream"`
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type AssistantMessage struct {
	Blocks []ContentBlock
	Usage  Usage
}

func TextMessage(role, text string) Message {
	return Message{
		Role: role,
		Content: []ContentBlock{{
			Type: "text",
			Text: text,
		}},
	}
}

func ToolResultMessage(id, content string, isError bool) Message {
	return Message{
		Role: "user",
		Content: []ContentBlock{{
			Type:      "tool_result",
			ToolUseID: id,
			Content:   content,
			IsError:   isError,
		}},
	}
}
