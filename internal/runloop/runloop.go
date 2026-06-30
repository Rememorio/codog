package runloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
	Messages      []anthropic.Message `json:"messages"`
	MessageUsages []MessageUsage      `json:"message_usages,omitempty"`
	ToolCalls     []ToolCall          `json:"tool_calls,omitempty"`
	Iterations    int                 `json:"iterations"`
}

type MessageUsage struct {
	MessageIndex int             `json:"message_index"`
	Usage        anthropic.Usage `json:"usage"`
}

type Runner struct {
	Config           config.Config
	Client           ModelClient
	Tools            *tools.Registry
	Prompter         *tools.Prompter
	Hooks            hooks.Runner
	HookPromptRunner hooks.PromptRunner
	Workspace        string
	Out              io.Writer
	System           string
	OnToolUse        func(ToolCall)
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
	if !hasHookConfig(hookRunner.Config) {
		hookRunner.Config = r.Config.Hooks
	}
	if hookRunner.Workspace == "" {
		hookRunner.Workspace = r.Workspace
	}
	if hookRunner.PromptRunner == nil {
		hookRunner.PromptRunner = r.HookPromptRunner
	}
	if err := hookRunner.UserPromptSubmit(ctx, input); err != nil {
		return TurnResult{}, err
	}
	var toolCalls []ToolCall
	var messageUsages []MessageUsage
	for turn := 0; turn < r.Config.MaxTurns; turn++ {
		if shouldCompactMessages(messages, r.Config.AutoCompactMessages) {
			if err := hookRunner.PreCompact(ctx, CompactHookPayload("auto", "", len(messages), r.Config.AutoCompactMessages)); err != nil {
				return TurnResult{}, err
			}
		}
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
		assistantIndex := len(messages)
		messages = append(messages, assistantMsg)
		messageUsages = appendMessageUsage(messageUsages, assistantIndex, assistant.Usage)

		blocks := toolUseBlocks(assistant.Blocks)
		if len(blocks) == 0 {
			if err := hookRunner.Stop(ctx, assistantText(assistant.Blocks), false); err != nil {
				return TurnResult{}, err
			}
			return TurnResult{
				Messages:      messages,
				MessageUsages: messageUsages,
				ToolCalls:     toolCalls,
				Iterations:    turn + 1,
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
				if failureErr := hookRunner.PostToolUseFailure(ctx, block.Name, block.Input, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				}
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
			if call.IsError {
				if failureErr := hookRunner.PostToolUseFailure(ctx, block.Name, block.Input, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				}
			}

			toolCalls = append(toolCalls, call)
			r.emitToolUse(call)
			messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, call.IsError))
		}
	}
	return TurnResult{
		Messages:      messages,
		MessageUsages: messageUsages,
		ToolCalls:     toolCalls,
		Iterations:    r.Config.MaxTurns,
	}, errors.New("conversation exceeded max turns")
}

func hasHookConfig(cfg config.HookConfig) bool {
	return len(cfg.PreToolUse) != 0 ||
		len(cfg.PostToolUse) != 0 ||
		len(cfg.PostToolUseFailure) != 0 ||
		len(cfg.UserPromptSubmit) != 0 ||
		len(cfg.SessionStart) != 0 ||
		len(cfg.Stop) != 0 ||
		len(cfg.PreCompact) != 0 ||
		len(cfg.Notification) != 0 ||
		len(cfg.SubagentStart) != 0 ||
		len(cfg.SubagentStop) != 0 ||
		len(cfg.PreToolUseCommands) != 0 ||
		len(cfg.PostToolUseCommands) != 0 ||
		len(cfg.PostToolUseFailureCommands) != 0 ||
		len(cfg.UserPromptSubmitCommands) != 0 ||
		len(cfg.SessionStartCommands) != 0 ||
		len(cfg.StopCommands) != 0 ||
		len(cfg.PreCompactCommands) != 0 ||
		len(cfg.NotificationCommands) != 0 ||
		len(cfg.SubagentStartCommands) != 0 ||
		len(cfg.SubagentStopCommands) != 0
}

func (r Runner) emitToolUse(call ToolCall) {
	if r.OnToolUse != nil {
		r.OnToolUse(call)
	}
}

func CompactMessages(messages []anthropic.Message, keep int) []anthropic.Message {
	if !shouldCompactMessages(messages, keep) {
		return messages
	}
	omitted := len(messages) - keep
	summary := anthropic.TextMessage("user", fmt.Sprintf("Previous Codog context was auto-compacted. %d older messages were omitted; recent context is retained.", omitted))
	out := make([]anthropic.Message, 0, keep+1)
	out = append(out, summary)
	out = append(out, messages[len(messages)-keep:]...)
	return out
}

func shouldCompactMessages(messages []anthropic.Message, keep int) bool {
	return keep > 0 && len(messages) > keep
}

func CompactHookPayload(source string, sessionID string, messages int, keep int) string {
	data, err := json.Marshal(map[string]any{
		"source":     source,
		"session_id": sessionID,
		"messages":   messages,
		"keep":       keep,
	})
	if err != nil {
		return ""
	}
	return string(data)
}

func MarshalToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func appendMessageUsage(usages []MessageUsage, index int, usage anthropic.Usage) []MessageUsage {
	if usage.InputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == 0 &&
		usage.CacheReadInputTokens == 0 {
		return usages
	}
	return append(usages, MessageUsage{MessageIndex: index, Usage: usage})
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

func assistantText(blocks []anthropic.ContentBlock) string {
	var values []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "")
}
