package runloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/usage"
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

	supplemental []anthropic.ContentBlock
}

// TurnResult is the complete state produced by one runner invocation.
type TurnResult struct {
	Messages         []anthropic.Message `json:"messages"`
	MessageUsages    []MessageUsage      `json:"message_usages,omitempty"`
	ToolCalls        []ToolCall          `json:"tool_calls,omitempty"`
	StopHookFeedback []string            `json:"stop_hook_feedback,omitempty"`
	Iterations       int                 `json:"iterations"`
}

// MessageUsage links provider usage metadata to the assistant message that
// produced it.
type MessageUsage struct {
	MessageIndex int             `json:"message_index"`
	Usage        anthropic.Usage `json:"usage"`
}

// BudgetExceededError reports that a turn reached the configured spending cap.
type BudgetExceededError struct {
	LimitUSD float64
	CostUSD  float64
}

func (e BudgetExceededError) Error() string {
	return fmt.Sprintf("max_budget_exceeded: estimated cost $%.5f reached max budget $%.5f", e.CostUSD, e.LimitUSD)
}

// Runner coordinates model streaming, tool execution, hooks, and compaction
// for a single user prompt.
type Runner struct {
	Config              config.Config
	Client              ModelClient
	Tools               *tools.Registry
	Prompter            *tools.Prompter
	Hooks               hooks.Runner
	HookPromptRunner    hooks.PromptRunner
	Workspace           string
	SessionID           string
	Out                 io.Writer
	System              string
	OnToolStart         func(ToolCall)
	OnToolUse           func(ToolCall)
	BeforeRequest       func(context.Context) error
	FileChangedFeedback func(context.Context, string) (string, error)
	MaxBudgetUSD        float64
	PriorCostUSD        float64
}

// Run submits input with prior messages, executes tool loops until the model
// stops, and returns the updated conversation state.
func (r Runner) Run(ctx context.Context, previous []anthropic.Message, input string) (TurnResult, error) {
	return r.RunWithUserContent(ctx, previous, []anthropic.ContentBlock{{Type: "text", Text: input}}, input)
}

// RunWithUserContent submits structured user content with prior messages,
// executes tool loops until the model stops, and returns the updated
// conversation state. The plain input is still used for hooks and diagnostics.
func (r Runner) RunWithUserContent(ctx context.Context, previous []anthropic.Message, content []anthropic.ContentBlock, input string) (TurnResult, error) {
	execution, err := newTurnExecution(ctx, r, previous, content, input)
	if err != nil {
		return TurnResult{}, err
	}
	return execution.run()
}

func (r Runner) toolDefinitions(loaded map[string]struct{}) []anthropic.ToolDefinition {
	if r.Config.ToolNamesSet {
		return filterToolDefinitions(r.allToolDefinitions(), r.Config.ToolNames)
	}
	loadedNames := make([]string, 0, len(loaded))
	for name := range loaded {
		loadedNames = append(loadedNames, name)
	}
	if r.Config.PlanMode {
		return r.Tools.DefinitionsForPlanModeWithLoaded(loadedNames)
	}
	return r.Tools.DefinitionsForModel(loadedNames)
}

func (r Runner) allToolDefinitions() []anthropic.ToolDefinition {
	if r.Config.PlanMode {
		return r.Tools.DefinitionsForPlanMode()
	}
	return r.Tools.Definitions()
}

func loadToolSearchMatches(loaded map[string]struct{}, output string) {
	var result struct {
		MatchNames []string `json:"match_names"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return
	}
	for _, name := range result.MatchNames {
		if canonical := tools.CanonicalToolName(name); canonical != "" {
			loaded[canonical] = struct{}{}
		}
	}
}

func loadedToolsFromMessages(messages []anthropic.Message) map[string]struct{} {
	loaded := map[string]struct{}{}
	searchIDs := map[string]struct{}{}
	for _, message := range messages {
		for _, block := range message.Content {
			switch {
			case block.Type == "tool_use" && tools.CanonicalToolName(block.Name) == "tool_search":
				if block.ID != "" {
					searchIDs[block.ID] = struct{}{}
				}
			case block.Type == "tool_result":
				if _, ok := searchIDs[block.ToolUseID]; ok && !block.IsError {
					loadToolSearchMatches(loaded, block.Content)
				}
			}
		}
	}
	return loaded
}

func (r Runner) toolSelectionAllows(name string) bool {
	if !r.Config.ToolNamesSet {
		return true
	}
	canonical := tools.CanonicalToolName(name)
	for _, selected := range r.Config.ToolNames {
		if toolSelectionNameMatches(selected, canonical, name) {
			return true
		}
	}
	return false
}

func filterToolDefinitions(definitions []anthropic.ToolDefinition, selected []string) []anthropic.ToolDefinition {
	if len(selected) == 0 {
		return nil
	}
	out := make([]anthropic.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if toolSelectionAllowsName(selected, definition.Name) {
			out = append(out, definition)
		}
	}
	return out
}

func toolSelectionAllowsName(selected []string, name string) bool {
	canonical := tools.CanonicalToolName(name)
	for _, candidate := range selected {
		if toolSelectionNameMatches(candidate, canonical, name) {
			return true
		}
	}
	return false
}

func toolSelectionNameMatches(selected string, canonical string, original string) bool {
	selected = toolSelectionBaseName(selected)
	if selected == "" {
		return false
	}
	if strings.EqualFold(selected, "default") {
		return true
	}
	selectedCanonical := tools.CanonicalToolName(selected)
	if strings.Contains(selected, "*") {
		return toolSelectionPatternMatches(selected, original) ||
			toolSelectionPatternMatches(selected, canonical)
	}
	return strings.EqualFold(selected, original) ||
		strings.EqualFold(selected, canonical) ||
		strings.EqualFold(selectedCanonical, canonical)
}

func toolSelectionBaseName(selected string) string {
	selected = strings.TrimSpace(selected)
	if before, _, ok := strings.Cut(selected, "("); ok {
		return strings.TrimSpace(before)
	}
	return selected
}

func toolSelectionPatternMatches(pattern string, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || value == "" {
		return false
	}
	if pattern == "*" || pattern == value {
		return true
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		next := strings.Index(value[position:], part)
		if next < 0 {
			return false
		}
		if index == 0 && !strings.HasPrefix(pattern, "*") && next != 0 {
			return false
		}
		position += next + len(part)
	}
	if !strings.HasSuffix(pattern, "*") && len(parts) > 0 {
		last := parts[len(parts)-1]
		if last != "" && !strings.HasSuffix(value, last) {
			return false
		}
	}
	return true
}

func hasHookConfig(cfg config.HookConfig) bool {
	stringHooks := [][]string{
		cfg.PreToolUse, cfg.PostToolUse, cfg.PostToolUseFailure,
		cfg.PermissionRequest, cfg.PermissionDenied, cfg.UserPromptSubmit,
		cfg.SessionStart, cfg.SessionEnd, cfg.Setup, cfg.Stop, cfg.StopFailure,
		cfg.PreCompact, cfg.PostCompact, cfg.Notification,
		cfg.SubagentStart, cfg.SubagentStop,
		cfg.WorktreeCreate, cfg.WorktreeRemove, cfg.CwdChanged,
		cfg.TaskCreated, cfg.TaskCompleted, cfg.InstructionsLoaded, cfg.FileChanged,
	}
	commandHooks := [][]config.HookCommand{
		cfg.PreToolUseCommands, cfg.PostToolUseCommands, cfg.PostToolUseFailureCommands,
		cfg.PermissionRequestCommands, cfg.PermissionDeniedCommands, cfg.UserPromptSubmitCommands,
		cfg.SessionStartCommands, cfg.SessionEndCommands, cfg.SetupCommands,
		cfg.StopCommands, cfg.StopFailureCommands,
		cfg.PreCompactCommands, cfg.PostCompactCommands, cfg.NotificationCommands,
		cfg.SubagentStartCommands, cfg.SubagentStopCommands,
		cfg.WorktreeCreateCommands, cfg.WorktreeRemoveCommands, cfg.CwdChangedCommands,
		cfg.TaskCreatedCommands, cfg.TaskCompletedCommands,
		cfg.InstructionsLoadedCommands, cfg.FileChangedCommands,
	}
	return hasAnyHookEntries(stringHooks) || hasAnyHookEntries(commandHooks)
}

func hasAnyHookEntries[T any](groups [][]T) bool {
	for _, group := range groups {
		if len(group) != 0 {
			return true
		}
	}
	return false
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

func (r Runner) emitToolStart(call ToolCall) {
	if r.OnToolStart != nil {
		r.OnToolStart(call)
	}
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

func (r Runner) budgetExceeded(messageUsages []MessageUsage) (BudgetExceededError, bool) {
	if r.MaxBudgetUSD <= 0 {
		return BudgetExceededError{}, false
	}
	actual := make([]anthropic.Usage, 0, len(messageUsages))
	for _, messageUsage := range messageUsages {
		actual = append(actual, messageUsage.Usage)
	}
	summary, ok := usage.ActualSummary(actual, r.Config.Model)
	if !ok {
		return BudgetExceededError{}, false
	}
	cost := r.PriorCostUSD + summary.EstimatedUSD
	if cost < r.MaxBudgetUSD {
		return BudgetExceededError{}, false
	}
	return BudgetExceededError{LimitUSD: r.MaxBudgetUSD, CostUSD: cost}, true
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
