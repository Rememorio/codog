package runloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/tools"
)

const defaultSystemPrompt = "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."

type ModelClient interface {
	Stream(context.Context, anthropic.Request, func(string)) (anthropic.AssistantMessage, error)
}

type ToolCall struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

type TurnResult struct {
	Messages   []anthropic.Message `json:"messages"`
	ToolCalls  []ToolCall          `json:"tool_calls,omitempty"`
	Iterations int                 `json:"iterations"`
}

type Runner struct {
	Config    config.Config
	Client    ModelClient
	Tools     *tools.Registry
	Prompter  *tools.Prompter
	Hooks     hooks.Runner
	Workspace string
	Out       io.Writer
	System    string
	OnToolUse func(ToolCall)
}

func (r Runner) Run(ctx context.Context, previous []anthropic.Message, input string) (TurnResult, error) {
	if r.Client == nil {
		return TurnResult{}, errors.New("missing model client")
	}
	if r.Tools == nil {
		return TurnResult{}, errors.New("missing tool registry")
	}

	messages := append([]anthropic.Message(nil), previous...)
	messages = append(messages, anthropic.TextMessage("user", input))

	system := r.System
	if system == "" {
		system = defaultSystemPrompt
	}
	hookRunner := r.Hooks
	if len(hookRunner.Config.PreToolUse) == 0 && len(hookRunner.Config.PostToolUse) == 0 {
		hookRunner.Config = r.Config.Hooks
	}
	if hookRunner.Workspace == "" {
		hookRunner.Workspace = r.Workspace
	}
	var toolCalls []ToolCall
	for turn := 0; turn < r.Config.MaxTurns; turn++ {
		requestMessages := CompactMessages(messages, r.Config.AutoCompactMessages)
		req := anthropic.Request{
			Model:     r.Config.Model,
			MaxTokens: r.Config.MaxTokens,
			System:    system,
			Messages:  requestMessages,
			Tools:     r.Tools.Definitions(),
		}
		assistant, err := r.Client.Stream(ctx, req, func(delta string) {
			if r.Out != nil {
				fmt.Fprint(r.Out, delta)
			}
		})
		if err != nil {
			return TurnResult{}, err
		}
		assistantMsg := anthropic.Message{Role: "assistant", Content: assistant.Blocks}
		messages = append(messages, assistantMsg)

		blocks := toolUseBlocks(assistant.Blocks)
		if len(blocks) == 0 {
			return TurnResult{
				Messages:   messages,
				ToolCalls:  toolCalls,
				Iterations: turn + 1,
			}, nil
		}

		for _, block := range blocks {
			call := ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: string(block.Input),
			}
			if err := hookRunner.PreToolUse(ctx, block.Name, block.Input); err != nil {
				call.Output = err.Error()
				call.IsError = true
				toolCalls = append(toolCalls, call)
				r.emitToolUse(call)
				messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, true))
				continue
			}

			output, err := r.Tools.Execute(ctx, block.Name, block.Input, r.Prompter)
			if err != nil {
				call.Output = err.Error()
				call.IsError = true
			} else {
				call.Output = output
			}
			if hookErr := hookRunner.PostToolUse(ctx, block.Name, block.Input, call.Output, call.IsError); hookErr != nil && !call.IsError {
				call.Output = hookErr.Error()
				call.IsError = true
			}

			toolCalls = append(toolCalls, call)
			r.emitToolUse(call)
			messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, call.IsError))
		}
	}
	return TurnResult{
		Messages:   messages,
		ToolCalls:  toolCalls,
		Iterations: r.Config.MaxTurns,
	}, errors.New("conversation exceeded max turns")
}

func (r Runner) emitToolUse(call ToolCall) {
	if r.OnToolUse != nil {
		r.OnToolUse(call)
	}
}

func CompactMessages(messages []anthropic.Message, keep int) []anthropic.Message {
	if keep <= 0 || len(messages) <= keep {
		return messages
	}
	omitted := len(messages) - keep
	summary := anthropic.TextMessage("user", fmt.Sprintf("Previous Codog context was auto-compacted. %d older messages were omitted; recent context is retained.", omitted))
	out := make([]anthropic.Message, 0, keep+1)
	out = append(out, summary)
	out = append(out, messages[len(messages)-keep:]...)
	return out
}

func MarshalToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func toolUseBlocks(blocks []anthropic.ContentBlock) []anthropic.ContentBlock {
	var result []anthropic.ContentBlock
	for _, block := range blocks {
		if block.Type == "tool_use" {
			result = append(result, block)
		}
	}
	return result
}
