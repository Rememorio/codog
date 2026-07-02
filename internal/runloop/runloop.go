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
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/shellstate"
	"github.com/Rememorio/codog/internal/tools"
)

const defaultSystemPrompt = "You are Codog, a Go-native coding agent CLI. Be concise, inspect before editing, and use tools when they materially help."

// ModelClient streams one Anthropic-compatible request and returns the final
// assistant message.
type ModelClient interface {
	Stream(context.Context, anthropic.Request, func(string)) (anthropic.AssistantMessage, error)
}

// ToolCall records one tool invocation and the result returned to the model.
type ToolCall struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// TurnResult is the complete state produced by one runner invocation.
type TurnResult struct {
	Messages      []anthropic.Message `json:"messages"`
	MessageUsages []MessageUsage      `json:"message_usages,omitempty"`
	ToolCalls     []ToolCall          `json:"tool_calls,omitempty"`
	Iterations    int                 `json:"iterations"`
}

// MessageUsage links provider usage metadata to the assistant message that
// produced it.
type MessageUsage struct {
	MessageIndex int             `json:"message_index"`
	Usage        anthropic.Usage `json:"usage"`
}

// Runner coordinates model streaming, tool execution, hooks, and compaction
// for a single user prompt.
type Runner struct {
	Config           config.Config
	Client           ModelClient
	Tools            *tools.Registry
	Prompter         *tools.Prompter
	Hooks            hooks.Runner
	HookPromptRunner hooks.PromptRunner
	Workspace        string
	SessionID        string
	Out              io.Writer
	System           string
	OnToolUse        func(ToolCall)
}

// Run submits input with prior messages, executes tool loops until the model
// stops, and returns the updated conversation state.
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
	if hookRunner.ConfigHome == "" {
		hookRunner.ConfigHome = r.Config.ConfigHome
	}
	if hookRunner.SessionID == "" {
		hookRunner.SessionID = r.SessionID
	}
	if hookRunner.PromptRunner == nil {
		hookRunner.PromptRunner = r.HookPromptRunner
	}
	toolCtx := tools.ContextWithSessionID(ctx, r.SessionID)
	promptReport, err := hookRunner.UserPromptSubmitReport(ctx, input)
	if err != nil {
		return TurnResult{}, err
	}
	if promptReport.Denied {
		return TurnResult{}, hookDeniedReportError("user_prompt_submit", promptReport)
	}
	messages = appendUserPromptHookFeedback(messages, hooks.MessagesFromReport(promptReport))
	var toolCalls []ToolCall
	var messageUsages []MessageUsage
	for turn := 0; turn < r.Config.MaxTurns; turn++ {
		compactPayload := ""
		compactFeedback := []string{}
		if shouldCompactMessages(messages, r.Config.AutoCompactMessages) {
			compactPayload = CompactHookPayload("auto", "", len(messages), r.Config.AutoCompactMessages)
			report, err := hookRunner.PreCompactReport(ctx, compactPayload)
			if err != nil {
				return TurnResult{}, err
			}
			compactFeedback = append(compactFeedback, hooks.MessagesFromReport(report)...)
			if report.Denied {
				return TurnResult{}, hookDeniedReportError("pre_compact", report)
			}
		}
		requestMessages := CompactMessages(messages, r.Config.AutoCompactMessages)
		if compactPayload != "" {
			report, err := hookRunner.PostCompactReport(ctx, compactPayload)
			if err != nil {
				return TurnResult{}, err
			}
			compactFeedback = append(compactFeedback, hooks.MessagesFromReport(report)...)
			if report.Denied {
				return TurnResult{}, hookDeniedReportError("post_compact", report)
			}
			requestMessages = appendCompactionHookFeedback(requestMessages, compactFeedback)
		}
		req := anthropic.Request{
			Model:           r.Config.Model,
			MaxTokens:       r.Config.MaxTokens,
			Temperature:     r.Config.Temperature,
			ReasoningEffort: r.Config.ReasoningEffort,
			System:          system,
			Messages:        requestMessages,
			Tools:           r.toolDefinitions(),
		}
		assistant, err := r.Client.Stream(ctx, req, func(delta string) {
			if r.Out != nil {
				fmt.Fprint(r.Out, delta)
			}
		})
		if err != nil {
			if hookErr := hookRunner.StopFailure(ctx, err.Error(), "model_error"); hookErr != nil {
				return TurnResult{}, fmt.Errorf("%w; stop failure hook: %v", err, hookErr)
			}
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
			effectiveInput := append(json.RawMessage(nil), block.Input...)
			call := ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: string(effectiveInput),
			}
			_, preToolOutput, err := hookRunner.PreToolUseReport(ctx, block.Name, effectiveInput)
			if err != nil {
				call.Output = preToolUseErrorMessage(err, preToolOutput)
				call.IsError = true
				if failureReport, failureErr := hookRunner.PostToolUseFailureReport(ctx, block.Name, effectiveInput, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				} else {
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(failureReport), call.Output, true)
				}
				toolCalls = append(toolCalls, call)
				r.emitToolUse(call)
				messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, true))
				continue
			}
			if preToolOutput.UpdatedInputProvided {
				effectiveInput = append(json.RawMessage(nil), preToolOutput.UpdatedInput...)
				call.Input = string(effectiveInput)
			}
			if preToolOutput.Denied {
				call.Output = preToolUseDeniedMessage(block.Name, preToolOutput)
				call.IsError = true
				if failureReport, failureErr := hookRunner.PostToolUseFailureReport(ctx, block.Name, effectiveInput, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				} else {
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(failureReport), call.Output, true)
				}
				toolCalls = append(toolCalls, call)
				r.emitToolUse(call)
				messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, true))
				continue
			}

			canonicalTool := tools.CanonicalToolName(block.Name)
			execPrompter := prompterWithPreToolDecision(r.Prompter, preToolOutput.PermissionDecision)
			if r.Config.PlanMode {
				if info, ok := r.Tools.Info(block.Name); ok && !tools.ToolAllowedInPlanMode(info.Name, info.Permission) {
					call.Output = fmt.Sprintf("plan mode blocked tool %s because it requires %s permission", info.Name, info.Permission)
					call.IsError = true
					if failureReport, failureErr := hookRunner.PostToolUseFailureReport(ctx, block.Name, effectiveInput, call.Output); failureErr != nil {
						call.Output = failureErr.Error()
					} else {
						call.Output = mergeHookFeedback(hooks.MessagesFromReport(failureReport), call.Output, true)
					}
					toolCalls = append(toolCalls, call)
					r.emitToolUse(call)
					messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, true))
					continue
				}
				execPrompter = tools.ReadOnlyPrompter(execPrompter, r.Workspace)
			}
			oldCWD := ""
			if canonicalTool == "bash" && r.SessionID != "" {
				if cwd, cwdErr := shellstate.CurrentCWD(r.Config.ConfigHome, r.SessionID, r.Workspace); cwdErr == nil {
					oldCWD = cwd
				}
			}
			output, err := r.Tools.Execute(toolCtx, block.Name, effectiveInput, execPrompter)
			if err != nil {
				call.Output = err.Error()
				call.IsError = true
			} else {
				call.Output = output
			}
			call.Output = mergeHookFeedback(preToolOutput.Messages, call.Output, call.IsError)
			if oldCWD != "" {
				if newCWD, cwdErr := shellstate.CurrentCWD(r.Config.ConfigHome, r.SessionID, r.Workspace); cwdErr == nil && newCWD != oldCWD {
					if cwdReport, hookErr := hookRunner.CwdChangedReport(ctx, oldCWD, newCWD, string(effectiveInput)); hookErr != nil {
						if !call.IsError {
							call.Output = hookErr.Error()
							call.IsError = true
						}
					} else {
						if cwdReport.Denied {
							call.IsError = true
						}
						call.Output = mergeHookFeedback(hooks.MessagesFromReport(cwdReport), call.Output, call.IsError)
					}
				}
			}
			if call.IsError {
				if failureReport, failureErr := hookRunner.PostToolUseFailureReport(ctx, block.Name, effectiveInput, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				} else {
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(failureReport), call.Output, true)
				}
			} else {
				if postReport, hookErr := hookRunner.PostToolUseReport(ctx, block.Name, effectiveInput, call.Output, false); hookErr != nil {
					call.Output = hookErr.Error()
					call.IsError = true
				} else {
					if postReport.Denied {
						call.IsError = true
					}
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(postReport), call.Output, call.IsError)
				}
			}
			fileChangedFailed := false
			if !call.IsError {
				for _, change := range fileChangesForTool(block.Name, effectiveInput) {
					fileReport, hookErr := hookRunner.FileChangedReport(ctx, change.Path, change.Operation, effectiveInput)
					if hookErr != nil {
						call.Output = hookErr.Error()
						call.IsError = true
						fileChangedFailed = true
						break
					}
					if fileReport.Denied {
						call.IsError = true
						fileChangedFailed = true
					}
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(fileReport), call.Output, call.IsError)
					if call.IsError {
						break
					}
				}
			}
			if fileChangedFailed {
				if failureReport, failureErr := hookRunner.PostToolUseFailureReport(ctx, block.Name, effectiveInput, call.Output); failureErr != nil {
					call.Output = failureErr.Error()
				} else {
					call.Output = mergeHookFeedback(hooks.MessagesFromReport(failureReport), call.Output, true)
				}
			}

			toolCalls = append(toolCalls, call)
			r.emitToolUse(call)
			messages = append(messages, anthropic.ToolResultMessage(block.ID, call.Output, call.IsError))
		}
	}
	result := TurnResult{
		Messages:      messages,
		MessageUsages: messageUsages,
		ToolCalls:     toolCalls,
		Iterations:    r.Config.MaxTurns,
	}
	maxTurnsErr := errors.New("conversation exceeded max turns")
	if hookErr := hookRunner.StopFailure(ctx, maxTurnsErr.Error(), "max_turns"); hookErr != nil {
		return result, fmt.Errorf("%w; stop failure hook: %v", maxTurnsErr, hookErr)
	}
	return result, maxTurnsErr
}

func (r Runner) toolDefinitions() []anthropic.ToolDefinition {
	if r.Config.PlanMode {
		return r.Tools.DefinitionsForPlanMode()
	}
	return r.Tools.Definitions()
}

func hasHookConfig(cfg config.HookConfig) bool {
	return len(cfg.PreToolUse) != 0 ||
		len(cfg.PostToolUse) != 0 ||
		len(cfg.PostToolUseFailure) != 0 ||
		len(cfg.PermissionRequest) != 0 ||
		len(cfg.PermissionDenied) != 0 ||
		len(cfg.UserPromptSubmit) != 0 ||
		len(cfg.SessionStart) != 0 ||
		len(cfg.SessionEnd) != 0 ||
		len(cfg.Setup) != 0 ||
		len(cfg.Stop) != 0 ||
		len(cfg.StopFailure) != 0 ||
		len(cfg.PreCompact) != 0 ||
		len(cfg.PostCompact) != 0 ||
		len(cfg.Notification) != 0 ||
		len(cfg.SubagentStart) != 0 ||
		len(cfg.SubagentStop) != 0 ||
		len(cfg.WorktreeCreate) != 0 ||
		len(cfg.WorktreeRemove) != 0 ||
		len(cfg.CwdChanged) != 0 ||
		len(cfg.TaskCreated) != 0 ||
		len(cfg.TaskCompleted) != 0 ||
		len(cfg.InstructionsLoaded) != 0 ||
		len(cfg.FileChanged) != 0 ||
		len(cfg.PreToolUseCommands) != 0 ||
		len(cfg.PostToolUseCommands) != 0 ||
		len(cfg.PostToolUseFailureCommands) != 0 ||
		len(cfg.PermissionRequestCommands) != 0 ||
		len(cfg.PermissionDeniedCommands) != 0 ||
		len(cfg.UserPromptSubmitCommands) != 0 ||
		len(cfg.SessionStartCommands) != 0 ||
		len(cfg.SessionEndCommands) != 0 ||
		len(cfg.SetupCommands) != 0 ||
		len(cfg.StopCommands) != 0 ||
		len(cfg.StopFailureCommands) != 0 ||
		len(cfg.PreCompactCommands) != 0 ||
		len(cfg.PostCompactCommands) != 0 ||
		len(cfg.NotificationCommands) != 0 ||
		len(cfg.SubagentStartCommands) != 0 ||
		len(cfg.SubagentStopCommands) != 0 ||
		len(cfg.WorktreeCreateCommands) != 0 ||
		len(cfg.WorktreeRemoveCommands) != 0 ||
		len(cfg.CwdChangedCommands) != 0 ||
		len(cfg.TaskCreatedCommands) != 0 ||
		len(cfg.TaskCompletedCommands) != 0 ||
		len(cfg.InstructionsLoadedCommands) != 0 ||
		len(cfg.FileChangedCommands) != 0
}

func prompterWithPreToolDecision(base *tools.Prompter, decision string) *tools.Prompter {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision == "" {
		return base
	}
	next := &tools.Prompter{}
	if base != nil {
		*next = *base
	}
	switch decision {
	case "allow":
		next.Mode = tools.PermissionAllow
	case "ask":
		next.Mode = tools.PermissionPrompt
	}
	return next
}

func preToolUseDeniedMessage(toolName string, output hooks.PreToolUseOutput) string {
	reason := strings.TrimSpace(output.PermissionReason)
	if reason == "" && len(output.Messages) > 0 {
		reason = strings.Join(output.Messages, "\n")
	}
	if reason == "" {
		return fmt.Sprintf("pre_tool_use hook denied tool %s", toolName)
	}
	return fmt.Sprintf("pre_tool_use hook denied tool %s: %s", toolName, reason)
}

func preToolUseErrorMessage(err error, output hooks.PreToolUseOutput) string {
	if len(output.Messages) > 0 {
		return strings.Join(output.Messages, "\n")
	}
	return err.Error()
}

func mergeHookFeedback(messages []string, output string, isError bool) string {
	messages = compactHookFeedbackMessages(messages)
	if len(messages) == 0 {
		return output
	}
	sections := []string{}
	if strings.TrimSpace(output) != "" {
		sections = append(sections, output)
	}
	label := "Hook feedback"
	if isError {
		label = "Hook feedback (error)"
	}
	sections = append(sections, label+":\n"+strings.Join(messages, "\n"))
	return strings.Join(sections, "\n\n")
}

func appendCompactionHookFeedback(messages []anthropic.Message, feedback []string) []anthropic.Message {
	feedback = compactHookFeedbackMessages(feedback)
	if len(feedback) == 0 {
		return messages
	}
	if len(messages) == 0 || len(messages[0].Content) == 0 || messages[0].Content[0].Type != "text" {
		return append([]anthropic.Message{anthropic.TextMessage("user", "Compaction hook feedback:\n"+strings.Join(feedback, "\n"))}, messages...)
	}
	out := append([]anthropic.Message(nil), messages...)
	out[0].Content = append([]anthropic.ContentBlock(nil), messages[0].Content...)
	text := strings.TrimRight(out[0].Content[0].Text, "\n")
	out[0].Content[0].Text = text + "\n\nCompaction hook feedback:\n" + strings.Join(feedback, "\n")
	return out
}

func appendUserPromptHookFeedback(messages []anthropic.Message, feedback []string) []anthropic.Message {
	feedback = compactHookFeedbackMessages(feedback)
	if len(feedback) == 0 {
		return messages
	}
	return append(messages, anthropic.TextMessage("user", "UserPromptSubmit hook feedback:\n\n"+strings.Join(feedback, "\n")))
}

func hookDeniedReportError(event string, report hooks.RunReport) error {
	messages := hooks.MessagesFromReport(report)
	if len(messages) > 0 {
		return fmt.Errorf("%s hook denied: %s", event, strings.Join(messages, "\n"))
	}
	return fmt.Errorf("%s hook denied", event)
}

func compactHookFeedbackMessages(messages []string) []string {
	out := make([]string, 0, len(messages))
	seen := map[string]struct{}{}
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		if _, ok := seen[message]; ok {
			continue
		}
		seen[message] = struct{}{}
		out = append(out, message)
	}
	return out
}

type fileChange struct {
	Path      string
	Operation string
}

func fileChangesForTool(name string, input json.RawMessage) []fileChange {
	operation := tools.CanonicalToolName(name)
	switch operation {
	case "write_file", "edit_file", "multi_edit":
		var payload struct {
			Path     string `json:"path"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(input, &payload); err != nil {
			return nil
		}
		path := firstNonEmptyString(payload.Path, payload.FilePath)
		if path == "" {
			return nil
		}
		return []fileChange{{Path: path, Operation: operation}}
	case "notebook_edit":
		var payload struct {
			NotebookPath string `json:"notebook_path"`
		}
		if err := json.Unmarshal(input, &payload); err != nil {
			return nil
		}
		path := strings.TrimSpace(payload.NotebookPath)
		if path == "" {
			return nil
		}
		return []fileChange{{Path: path, Operation: operation}}
	default:
		return nil
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (r Runner) emitToolUse(call ToolCall) {
	if r.OnToolUse != nil {
		r.OnToolUse(call)
	}
}

// CompactMessages replaces older messages with a synthetic summary while
// preserving the most recent keep messages verbatim.
func CompactMessages(messages []anthropic.Message, keep int) []anthropic.Message {
	if !shouldCompactMessages(messages, keep) {
		return messages
	}
	omitted := messages[:len(messages)-keep]
	summaryText := sessionsummary.BuildCompactionSummary(omitted, keep).Summary
	summary := anthropic.TextMessage("user", summaryText)
	out := make([]anthropic.Message, 0, keep+1)
	out = append(out, summary)
	out = append(out, messages[len(messages)-keep:]...)
	return out
}

func shouldCompactMessages(messages []anthropic.Message, keep int) bool {
	return keep > 0 && len(messages) > keep
}

// CompactHookPayload returns the JSON payload sent to pre-compaction hooks.
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

// MarshalToolInput renders a raw tool input object for logging and callbacks.
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
