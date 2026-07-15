package runloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/shellstate"
	"github.com/Rememorio/codog/internal/tools"
)

type turnExecution struct {
	runner        Runner
	ctx           context.Context
	hookRunner    hooks.Runner
	toolCtx       context.Context
	system        string
	messages      []anthropic.Message
	messageUsages []MessageUsage
	toolCalls     []ToolCall
	loadedTools   map[string]struct{}
}

const maxConcurrentToolExecutions = 10

type preparedToolCall struct {
	blockID     string
	call        ToolCall
	input       json.RawMessage
	preMessages []string
	execution   tools.AuthorizedExecution
	ready       bool
}

type concurrentToolResult struct {
	output string
	err    error
}

func newTurnExecution(ctx context.Context, runner Runner, previous []anthropic.Message, content []anthropic.ContentBlock, input string) (*turnExecution, error) {
	if runner.Client == nil {
		return nil, errors.New("missing model client")
	}
	if runner.Tools == nil {
		return nil, errors.New("missing tool registry")
	}
	if len(content) == 0 {
		content = []anthropic.ContentBlock{{Type: "text", Text: input}}
	}
	messages := append([]anthropic.Message(nil), previous...)
	messages = append(messages, anthropic.Message{Role: "user", Content: append([]anthropic.ContentBlock(nil), content...)})
	execution := &turnExecution{
		runner:      runner,
		ctx:         ctx,
		hookRunner:  runner.effectiveHookRunner(),
		toolCtx:     tools.ContextWithSessionID(ctx, runner.SessionID),
		system:      runner.effectiveSystemPrompt(),
		messages:    messages,
		loadedTools: loadedToolsFromMessages(previous),
	}
	if err := execution.applyUserPromptHook(input); err != nil {
		return nil, err
	}
	return execution, nil
}

func (r Runner) effectiveSystemPrompt() string {
	if r.System != "" {
		return r.System
	}
	return defaultSystemPrompt
}

func (r Runner) effectiveHookRunner() hooks.Runner {
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
	return hookRunner
}

func (e *turnExecution) applyUserPromptHook(input string) error {
	report, err := e.hookRunner.UserPromptSubmitReport(e.ctx, input)
	if err != nil {
		return err
	}
	if report.Denied {
		return hookDeniedReportError("user_prompt_submit", report)
	}
	e.messages = appendUserPromptHookFeedback(e.messages, hooks.MessagesFromReport(report))
	return nil
}

func (e *turnExecution) run() (TurnResult, error) {
	for turn := 0; turn < e.runner.Config.MaxTurns; turn++ {
		if e.runner.BeforeRequest != nil {
			if err := e.runner.BeforeRequest(e.ctx); err != nil {
				return TurnResult{}, err
			}
		}
		request, err := e.request()
		if err != nil {
			return TurnResult{}, err
		}
		assistant, err := e.stream(request)
		if err != nil {
			return TurnResult{}, e.modelFailure(err)
		}
		e.appendAssistant(assistant)
		if budgetErr, exceeded := e.runner.budgetExceeded(e.messageUsages); exceeded {
			return e.result(turn+1, nil), budgetErr
		}
		blocks := toolUseBlocks(assistant.Blocks)
		if len(blocks) == 0 {
			return e.finish(turn+1, assistant.Blocks)
		}
		e.executeToolBlocks(blocks)
	}
	return e.maxTurnsResult()
}

func (e *turnExecution) request() (anthropic.Request, error) {
	requestMessages, err := e.requestMessages()
	if err != nil {
		return anthropic.Request{}, err
	}
	return anthropic.Request{
		Model:           e.runner.Config.Model,
		MaxTokens:       e.runner.Config.MaxTokens,
		Temperature:     e.runner.Config.Temperature,
		ReasoningEffort: e.runner.Config.ReasoningEffort,
		ExtraBody:       e.runner.Config.ExtraBody,
		System:          e.system,
		Messages:        requestMessages,
		Tools:           e.runner.toolDefinitions(e.loadedTools),
	}, nil
}

func (e *turnExecution) requestMessages() ([]anthropic.Message, error) {
	if !shouldCompactMessages(e.messages, e.runner.Config.AutoCompactMessages) {
		return CompactMessages(e.messages, e.runner.Config.AutoCompactMessages), nil
	}
	payload := CompactHookPayload("auto", "", len(e.messages), e.runner.Config.AutoCompactMessages)
	feedback, err := e.compactionFeedback(payload)
	if err != nil {
		return nil, err
	}
	messages := CompactMessages(e.messages, e.runner.Config.AutoCompactMessages)
	return appendCompactionHookFeedback(messages, feedback), nil
}

func (e *turnExecution) compactionFeedback(payload string) ([]string, error) {
	pre, err := e.hookRunner.PreCompactReport(e.ctx, payload)
	if err != nil {
		return nil, err
	}
	if pre.Denied {
		return nil, hookDeniedReportError("pre_compact", pre)
	}
	feedback := hooks.MessagesFromReport(pre)
	post, err := e.hookRunner.PostCompactReport(e.ctx, payload)
	if err != nil {
		return nil, err
	}
	feedback = append(feedback, hooks.MessagesFromReport(post)...)
	if post.Denied {
		return nil, hookDeniedReportError("post_compact", post)
	}
	return feedback, nil
}

func (e *turnExecution) stream(request anthropic.Request) (anthropic.AssistantMessage, error) {
	return e.runner.Client.Stream(e.ctx, request, func(delta string) {
		if e.runner.Out != nil {
			fmt.Fprint(e.runner.Out, delta)
		}
	})
}

func (e *turnExecution) modelFailure(modelErr error) error {
	report, hookErr := e.hookRunner.StopFailureReport(e.ctx, modelErr.Error(), "model_error")
	if hookErr != nil {
		return fmt.Errorf("%w; stop failure hook: %v", modelErr, hookErr)
	}
	if report.Denied {
		return fmt.Errorf("%w; stop failure hook: %v", modelErr, hookDeniedReportError("stop_failure", report))
	}
	return modelErr
}

func (e *turnExecution) appendAssistant(assistant anthropic.AssistantMessage) {
	index := len(e.messages)
	e.messages = append(e.messages, anthropic.Message{ID: assistant.ID, Role: "assistant", Content: assistant.Blocks})
	e.messageUsages = appendMessageUsage(e.messageUsages, index, assistant.Usage)
}

func (e *turnExecution) finish(iterations int, blocks []anthropic.ContentBlock) (TurnResult, error) {
	report, err := e.hookRunner.StopReport(e.ctx, assistantText(blocks), false)
	if err != nil {
		return TurnResult{}, err
	}
	if report.Denied {
		return TurnResult{}, hookDeniedReportError("stop", report)
	}
	result := e.result(iterations, hooks.MessagesFromReport(report))
	if err := ValidateTurnResult(result); err != nil {
		return result, err
	}
	return result, nil
}

func (e *turnExecution) result(iterations int, stopFeedback []string) TurnResult {
	return TurnResult{
		Messages:         e.messages,
		MessageUsages:    e.messageUsages,
		ToolCalls:        e.toolCalls,
		StopHookFeedback: stopFeedback,
		Iterations:       iterations,
	}
}

func (e *turnExecution) executeToolBlocks(blocks []anthropic.ContentBlock) {
	for index := 0; index < len(blocks); {
		if !e.toolConcurrencySafe(blocks[index]) {
			e.executeToolBlock(blocks[index])
			index++
			continue
		}
		end := index + 1
		for end < len(blocks) && e.toolConcurrencySafe(blocks[end]) {
			end++
		}
		if end-index == 1 {
			e.executeToolBlock(blocks[index])
		} else {
			e.executeConcurrentToolBlocks(blocks[index:end])
		}
		index = end
	}
}

func (e *turnExecution) toolConcurrencySafe(block anthropic.ContentBlock) bool {
	return !hasHookConfig(e.hookRunner.Config) && e.runner.Tools.ConcurrencySafe(block.Name, block.Input)
}

func (e *turnExecution) executeConcurrentToolBlocks(blocks []anthropic.ContentBlock) {
	prepared := make([]preparedToolCall, len(blocks))
	results := make([]concurrentToolResult, len(blocks))
	for index, block := range blocks {
		prepared[index] = e.prepareConcurrentTool(block)
		if prepared[index].ready {
			e.runner.emitToolStart(prepared[index].call)
		}
	}

	ctx, cancel := context.WithCancel(e.toolCtx)
	defer cancel()
	semaphore := make(chan struct{}, maxConcurrentToolExecutions)
	var wait sync.WaitGroup
	var cancelOnce sync.Once
	for index := range prepared {
		if !prepared[index].ready {
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			results[index].output, results[index].err = prepared[index].execution.Execute(ctx, prepared[index].input)
			if results[index].err != nil {
				cancelOnce.Do(cancel)
			}
		}(index)
	}
	wait.Wait()

	for index := range prepared {
		item := &prepared[index]
		if item.ready {
			item.call.Output = results[index].output
			if results[index].err != nil {
				item.call.Output = results[index].err.Error()
				item.call.IsError = true
			} else {
				item.call.Output, item.call.supplemental = e.runner.Tools.ModelResult(item.call.Name, item.call.Output)
			}
			item.call.Output = mergeHookFeedback(item.preMessages, item.call.Output, item.call.IsError)
			e.applyPostToolHook(&item.call, item.input)
		}
		e.recordToolCall(item.blockID, item.call)
	}
}

func (e *turnExecution) prepareConcurrentTool(block anthropic.ContentBlock) preparedToolCall {
	input := append(json.RawMessage(nil), block.Input...)
	prepared := preparedToolCall{
		blockID: block.ID,
		call:    ToolCall{ID: block.ID, Name: block.Name, Input: string(input)},
		input:   input,
	}
	if !e.runner.toolSelectionAllows(block.Name) {
		prepared.call.Output = fmt.Sprintf("tool %s is not available because it was not included by --tools", block.Name)
		prepared.call.IsError = true
		return prepared
	}
	preOutput, ready := e.applyPreToolHook(&prepared.call, &prepared.input)
	if !ready {
		return prepared
	}
	prompter, ready := e.executionPrompter(&prepared.call, prepared.input, preOutput.PermissionDecision)
	if !ready {
		return prepared
	}
	execution, err := e.runner.Tools.AuthorizeExecution(prepared.call.Name, prepared.input, prompter)
	if err != nil {
		prepared.call.Output = err.Error()
		prepared.call.IsError = true
		e.applyFailureHook(&prepared.call, prepared.input)
		return prepared
	}
	prepared.preMessages = preOutput.Messages
	prepared.execution = execution
	prepared.ready = true
	return prepared
}

func (e *turnExecution) executeToolBlock(block anthropic.ContentBlock) {
	input := append(json.RawMessage(nil), block.Input...)
	call := ToolCall{ID: block.ID, Name: block.Name, Input: string(input)}
	if !e.runner.toolSelectionAllows(block.Name) {
		call.Output = fmt.Sprintf("tool %s is not available because it was not included by --tools", block.Name)
		call.IsError = true
		e.recordToolCall(block.ID, call)
		return
	}
	preOutput, ready := e.applyPreToolHook(&call, &input)
	if !ready {
		e.recordToolCall(block.ID, call)
		return
	}
	prompter, ready := e.executionPrompter(&call, input, preOutput.PermissionDecision)
	if !ready {
		e.recordToolCall(block.ID, call)
		return
	}
	e.executePreparedTool(&call, input, preOutput.Messages, prompter)
	e.recordToolCall(block.ID, call)
}

func (e *turnExecution) applyPreToolHook(call *ToolCall, input *json.RawMessage) (hooks.PreToolUseOutput, bool) {
	_, output, err := e.hookRunner.PreToolUseReport(e.ctx, call.Name, *input)
	if err != nil {
		call.Output = preToolUseErrorMessage(err, output)
		call.IsError = true
		e.applyFailureHook(call, *input)
		return output, false
	}
	if output.UpdatedInputProvided {
		*input = append(json.RawMessage(nil), output.UpdatedInput...)
		call.Input = string(*input)
	}
	if output.Denied {
		call.Output = preToolUseDeniedMessage(call.Name, output)
		call.IsError = true
		e.applyFailureHook(call, *input)
		return output, false
	}
	return output, true
}

func (e *turnExecution) executionPrompter(call *ToolCall, input json.RawMessage, decision string) (*tools.Prompter, bool) {
	prompter := prompterWithPreToolDecision(e.runner.Prompter, decision)
	if !e.runner.Config.PlanMode {
		return prompter, true
	}
	if info, ok := e.runner.Tools.Info(call.Name); ok && !tools.ToolAllowedInPlanMode(info.Name, info.Permission) {
		call.Output = fmt.Sprintf("plan mode blocked tool %s because it requires %s permission", info.Name, info.Permission)
		call.IsError = true
		e.applyFailureHook(call, input)
		return nil, false
	}
	return tools.ReadOnlyPrompter(prompter, e.runner.Workspace), true
}

func (e *turnExecution) executePreparedTool(call *ToolCall, input json.RawMessage, preMessages []string, prompter *tools.Prompter) {
	canonical := tools.CanonicalToolName(call.Name)
	oldCWD := e.currentBashCWD(canonical)
	e.runner.emitToolStart(*call)
	output, err := e.runner.Tools.Execute(e.toolCtx, call.Name, input, prompter)
	if err != nil {
		call.Output = err.Error()
		call.IsError = true
	} else {
		call.Output, call.supplemental = e.runner.Tools.ModelResult(call.Name, output)
		e.loadToolSearchResult(canonical, output)
	}
	call.Output = mergeHookFeedback(preMessages, call.Output, call.IsError)
	e.applyCWDChangedHook(call, input, oldCWD)
	e.applyPostToolHook(call, input)
	if !call.IsError && e.applyFileChangedHooks(call, input) {
		e.applyFailureHook(call, input)
	}
}

func (e *turnExecution) loadToolSearchResult(canonical string, output string) {
	if canonical == "tool_search" {
		loadToolSearchMatches(e.loadedTools, output)
	}
}

func (e *turnExecution) currentBashCWD(canonical string) string {
	if canonical != "bash" || e.runner.SessionID == "" {
		return ""
	}
	cwd, err := shellstate.CurrentCWD(e.runner.Config.ConfigHome, e.runner.SessionID, e.runner.Workspace)
	if err != nil {
		return ""
	}
	return cwd
}

func (e *turnExecution) applyCWDChangedHook(call *ToolCall, input json.RawMessage, oldCWD string) {
	if oldCWD == "" {
		return
	}
	newCWD, err := shellstate.CurrentCWD(e.runner.Config.ConfigHome, e.runner.SessionID, e.runner.Workspace)
	if err != nil || newCWD == oldCWD {
		return
	}
	report, err := e.hookRunner.CwdChangedReport(e.ctx, oldCWD, newCWD, string(input))
	if err != nil {
		if !call.IsError {
			call.Output = err.Error()
			call.IsError = true
		}
		return
	}
	if report.Denied {
		call.IsError = true
	}
	call.Output = mergeHookFeedback(hooks.MessagesFromReport(report), call.Output, call.IsError)
}

func (e *turnExecution) applyPostToolHook(call *ToolCall, input json.RawMessage) {
	if call.IsError {
		e.applyFailureHook(call, input)
		return
	}
	report, err := e.hookRunner.PostToolUseReport(e.ctx, call.Name, input, call.Output, false)
	if err != nil {
		call.Output = err.Error()
		call.IsError = true
		return
	}
	if report.Denied {
		call.IsError = true
	}
	call.Output = mergeHookFeedback(hooks.MessagesFromReport(report), call.Output, call.IsError)
}

func (e *turnExecution) applyFailureHook(call *ToolCall, input json.RawMessage) {
	report, err := e.hookRunner.PostToolUseFailureReport(e.ctx, call.Name, input, call.Output)
	if err != nil {
		call.Output = err.Error()
		return
	}
	call.Output = mergeHookFeedback(hooks.MessagesFromReport(report), call.Output, true)
}

func (e *turnExecution) applyFileChangedHooks(call *ToolCall, input json.RawMessage) bool {
	for _, change := range fileChangesForTool(call.Name, input) {
		report, err := e.hookRunner.FileChangedReport(e.ctx, change.Path, change.Operation, input)
		if err != nil {
			call.Output = err.Error()
			call.IsError = true
			return true
		}
		if report.Denied {
			call.IsError = true
		}
		call.Output = mergeHookFeedback(hooks.MessagesFromReport(report), call.Output, call.IsError)
		if call.IsError {
			return true
		}
		e.applyFileChangedFeedback(call, change.Path)
	}
	return false
}

func (e *turnExecution) applyFileChangedFeedback(call *ToolCall, path string) {
	if e.runner.FileChangedFeedback == nil {
		return
	}
	feedback, err := e.runner.FileChangedFeedback(e.ctx, path)
	if err != nil {
		feedback = fmt.Sprintf("Language-server diagnostics unavailable for %s: %v", path, err)
	}
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return
	}
	call.Output = strings.TrimRight(call.Output, "\n") + "\n\n" + feedback
}

func (e *turnExecution) recordToolCall(blockID string, call ToolCall) {
	if call.IsError {
		call.supplemental = nil
	}
	e.toolCalls = append(e.toolCalls, call)
	e.runner.emitToolUse(call)
	e.messages = append(e.messages, anthropic.ToolResultMessageWithSupplemental(blockID, call.Output, call.IsError, call.supplemental))
}

func (e *turnExecution) maxTurnsResult() (TurnResult, error) {
	maxTurnsErr := errors.New("conversation exceeded max turns")
	report, hookErr := e.hookRunner.StopFailureReport(e.ctx, maxTurnsErr.Error(), "max_turns")
	result := e.result(e.runner.Config.MaxTurns, hooks.MessagesFromReport(report))
	if hookErr != nil {
		return result, fmt.Errorf("%w; stop failure hook: %v", maxTurnsErr, hookErr)
	}
	if report.Denied {
		return result, fmt.Errorf("%w; stop failure hook: %v", maxTurnsErr, hookDeniedReportError("stop_failure", report))
	}
	return result, maxTurnsErr
}
