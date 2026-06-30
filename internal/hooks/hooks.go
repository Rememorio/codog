package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
)

type Runner struct {
	Config       config.HookConfig
	Workspace    string
	Timeout      time.Duration
	PromptRunner PromptRunner
}

type PromptRunner func(context.Context, PromptRequest) (string, error)

type PromptRequest struct {
	Type    string  `json:"type"`
	Prompt  string  `json:"prompt"`
	Model   string  `json:"model,omitempty"`
	Payload Payload `json:"payload"`
}

type Payload struct {
	Event            string          `json:"event"`
	Tool             string          `json:"tool,omitempty"`
	ToolName         string          `json:"tool_name,omitempty"`
	ToolInput        json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	Input            string          `json:"input,omitempty"`
	Output           string          `json:"output,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Message          string          `json:"message,omitempty"`
	Title            string          `json:"title,omitempty"`
	NotificationType string          `json:"notification_type,omitempty"`
	AgentID          string          `json:"agent_id,omitempty"`
	AgentType        string          `json:"agent_type,omitempty"`
	TranscriptPath   string          `json:"agent_transcript_path,omitempty"`
	StopHookActive   bool            `json:"stop_hook_active,omitempty"`
	LastAssistant    string          `json:"last_assistant_message,omitempty"`
}

type CommandResult struct {
	Command    string `json:"command"`
	Type       string `json:"type,omitempty"`
	URL        string `json:"url,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

type RunReport struct {
	Kind    string          `json:"kind"`
	Event   string          `json:"event"`
	Tool    string          `json:"tool,omitempty"`
	Count   int             `json:"count"`
	Results []CommandResult `json:"results"`
}

func (r Runner) PreToolUse(ctx context.Context, tool string, input []byte) error {
	payload := Payload{
		Event: "pre_tool_use",
		Tool:  tool,
		Input: string(input),
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PostToolUse(ctx context.Context, tool string, input []byte, output string, isError bool) error {
	payload := Payload{
		Event:   "post_tool_use",
		Tool:    tool,
		Input:   string(input),
		Output:  output,
		IsError: isError,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PostToolUseFailure(ctx context.Context, tool string, input []byte, output string) error {
	payload := Payload{
		Event:   "post_tool_use_failure",
		Tool:    tool,
		Input:   string(input),
		Output:  output,
		IsError: true,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PermissionRequest(ctx context.Context, tool string, input []byte) error {
	payload := Payload{
		Event:     "permission_request",
		Tool:      tool,
		ToolName:  tool,
		ToolInput: json.RawMessage(input),
		Input:     string(input),
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PermissionDenied(ctx context.Context, tool string, input []byte, reason string) error {
	payload := Payload{
		Event:     "permission_denied",
		Tool:      tool,
		ToolName:  tool,
		ToolInput: json.RawMessage(input),
		Input:     string(input),
		Reason:    reason,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) UserPromptSubmit(ctx context.Context, input string) error {
	payload := Payload{
		Event: "user_prompt_submit",
		Input: input,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) SessionStart(ctx context.Context, input string) error {
	payload := Payload{
		Event: "session_start",
		Input: input,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) SessionEnd(ctx context.Context, input string, reason string) error {
	payload := Payload{
		Event:  "session_end",
		Input:  input,
		Reason: reason,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) Setup(ctx context.Context, input string) error {
	payload := Payload{
		Event: "setup",
		Input: input,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) Stop(ctx context.Context, output string, isError bool) error {
	payload := Payload{
		Event:   "stop",
		Output:  output,
		IsError: isError,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PreCompact(ctx context.Context, input string) error {
	payload := Payload{
		Event: "pre_compact",
		Input: input,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) PostCompact(ctx context.Context, input string) error {
	payload := Payload{
		Event: "post_compact",
		Input: input,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) Notification(ctx context.Context, notificationType string, title string, message string) error {
	notificationType = strings.TrimSpace(notificationType)
	if notificationType == "" {
		notificationType = "generic"
	}
	payload := Payload{
		Event:            "notification",
		Tool:             notificationType,
		Input:            message,
		Message:          message,
		Title:            title,
		NotificationType: notificationType,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) SubagentStart(ctx context.Context, agentID string, agentType string) error {
	payload := Payload{
		Event:     "subagent_start",
		Tool:      agentType,
		AgentID:   agentID,
		AgentType: agentType,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func (r Runner) SubagentStop(ctx context.Context, agentID string, agentType string, transcriptPath string, lastAssistant string, stopHookActive bool) error {
	payload := Payload{
		Event:          "subagent_stop",
		Tool:           agentType,
		AgentID:        agentID,
		AgentType:      agentType,
		TranscriptPath: transcriptPath,
		StopHookActive: stopHookActive,
		LastAssistant:  lastAssistant,
	}
	return r.run(ctx, HooksForPayload(r.Config, payload), payload)
}

func CommandsForEvent(cfg config.HookConfig, event string, tool string) []string {
	payload := Payload{Event: event, Tool: tool}
	hooks := HooksForPayload(cfg, payload)
	commands := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		if hookType(hook) == "command" {
			command := strings.TrimSpace(hook.Command)
			if command != "" {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func HooksForPayload(cfg config.HookConfig, payload Payload) []config.HookCommand {
	switch normalizeEvent(payload.Event) {
	case "pre_tool_use":
		return matchingHooks(cfg.PreToolUseCommands, cfg.PreToolUse, payload)
	case "post_tool_use":
		return matchingHooks(cfg.PostToolUseCommands, cfg.PostToolUse, payload)
	case "post_tool_use_failure":
		return matchingHooks(cfg.PostToolUseFailureCommands, cfg.PostToolUseFailure, payload)
	case "permission_request":
		return matchingHooks(cfg.PermissionRequestCommands, cfg.PermissionRequest, payload)
	case "permission_denied":
		return matchingHooks(cfg.PermissionDeniedCommands, cfg.PermissionDenied, payload)
	case "user_prompt_submit":
		return matchingHooks(cfg.UserPromptSubmitCommands, cfg.UserPromptSubmit, payload)
	case "session_start":
		return matchingHooks(cfg.SessionStartCommands, cfg.SessionStart, payload)
	case "session_end":
		return matchingHooks(cfg.SessionEndCommands, cfg.SessionEnd, payload)
	case "setup":
		return matchingHooks(cfg.SetupCommands, cfg.Setup, payload)
	case "stop":
		return matchingHooks(cfg.StopCommands, cfg.Stop, payload)
	case "pre_compact":
		return matchingHooks(cfg.PreCompactCommands, cfg.PreCompact, payload)
	case "post_compact":
		return matchingHooks(cfg.PostCompactCommands, cfg.PostCompact, payload)
	case "notification":
		return matchingHooks(cfg.NotificationCommands, cfg.Notification, payload)
	case "subagent_start":
		return matchingHooks(cfg.SubagentStartCommands, cfg.SubagentStart, payload)
	case "subagent_stop":
		return matchingHooks(cfg.SubagentStopCommands, cfg.SubagentStop, payload)
	default:
		return nil
	}
}

func (r Runner) run(ctx context.Context, hookList []config.HookCommand, payload Payload) error {
	_, err := r.RunHooks(ctx, hookList, payload)
	return err
}

func (r Runner) RunPayload(ctx context.Context, commands []string, payload Payload) (RunReport, error) {
	hookList := make([]config.HookCommand, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command != "" {
			hookList = append(hookList, config.HookCommand{Type: "command", Command: command})
		}
	}
	return r.RunHooks(ctx, hookList, payload)
}

func (r Runner) RunHooks(ctx context.Context, hookList []config.HookCommand, payload Payload) (RunReport, error) {
	report := RunReport{
		Kind:    "hooks",
		Event:   payload.Event,
		Tool:    payload.Tool,
		Count:   len(hookList),
		Results: []CommandResult{},
	}
	if len(hookList) == 0 {
		return report, nil
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return report, err
	}
	for _, hook := range hookList {
		result, err := r.runOneHook(ctx, hook, payload, data, timeout)
		report.Results = append(report.Results, result)
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func (r Runner) runOneHook(ctx context.Context, hook config.HookCommand, payload Payload, data []byte, defaultTimeout time.Duration) (CommandResult, error) {
	switch hookType(hook) {
	case "command":
		return r.runCommandHook(ctx, hook, payload, data, defaultTimeout)
	case "http":
		return r.runHTTPHook(ctx, hook, data, defaultTimeout)
	case "prompt", "agent":
		return r.runPromptHook(ctx, hook, payload, data, defaultTimeout)
	default:
		result := CommandResult{
			Type:     hookType(hook),
			Command:  config.HookCommandDisplay(hook),
			ExitCode: -1,
			Success:  false,
			Error:    "unknown hook type",
		}
		return result, fmt.Errorf("unknown hook type %q", hookType(hook))
	}
}

func (r Runner) runPromptHook(ctx context.Context, hook config.HookCommand, payload Payload, data []byte, defaultTimeout time.Duration) (CommandResult, error) {
	typ := hookType(hook)
	prompt := strings.ReplaceAll(strings.TrimSpace(hook.Prompt), "$ARGUMENTS", string(data))
	result := CommandResult{
		Command:  config.HookCommandDisplay(hook),
		Type:     typ,
		ExitCode: 0,
		Success:  true,
	}
	if r.PromptRunner == nil {
		result.Success = false
		result.ExitCode = -1
		result.Error = "prompt runner is not configured"
		return result, fmt.Errorf("%s hook requires a prompt runner", typ)
	}
	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout(hook, defaultTimeout))
	defer cancel()
	started := time.Now()
	output, err := r.PromptRunner(hookCtx, PromptRequest{
		Type:    typ,
		Prompt:  prompt,
		Model:   strings.TrimSpace(hook.Model),
		Payload: payload,
	})
	result.DurationMS = time.Since(started).Milliseconds()
	result.Stdout = output
	if hookCtx.Err() == context.DeadlineExceeded {
		result.Success = false
		result.ExitCode = -1
		result.Error = "timeout"
		return result, fmt.Errorf("hook timed out: %s", config.HookCommandDisplay(hook))
	}
	if err != nil {
		result.Success = false
		result.ExitCode = -1
		result.Error = err.Error()
		return result, fmt.Errorf("%s hook failed: %w", typ, err)
	}
	return result, nil
}

func (r Runner) runCommandHook(ctx context.Context, hook config.HookCommand, payload Payload, data []byte, defaultTimeout time.Duration) (CommandResult, error) {
	command := strings.TrimSpace(hook.Command)
	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout(hook, defaultTimeout))
	defer cancel()
	name, args := hookShell(hook, command)
	cmd := exec.CommandContext(hookCtx, name, args...)
	cmd.Dir = r.Workspace
	cmd.Env = append(os.Environ(),
		"CODOG_HOOK_EVENT="+payload.Event,
		"CODOG_HOOK_TOOL="+payload.Tool,
		"CODOG_HOOK_TOOL_NAME="+payload.ToolName,
		"CODOG_HOOK_TOOL_USE_ID="+payload.ToolUseID,
		"CODOG_HOOK_INPUT="+payload.Input,
		"CODOG_HOOK_OUTPUT="+payload.Output,
		"CODOG_HOOK_IS_ERROR="+strconv.FormatBool(payload.IsError),
		"CODOG_HOOK_REASON="+payload.Reason,
		"CODOG_HOOK_MESSAGE="+payload.Message,
		"CODOG_HOOK_TITLE="+payload.Title,
		"CODOG_HOOK_NOTIFICATION_TYPE="+payload.NotificationType,
		"CODOG_HOOK_AGENT_ID="+payload.AgentID,
		"CODOG_HOOK_AGENT_TYPE="+payload.AgentType,
		"CODOG_HOOK_AGENT_TRANSCRIPT_PATH="+payload.TranscriptPath,
		"CODOG_HOOK_STOP_HOOK_ACTIVE="+strconv.FormatBool(payload.StopHookActive),
		"CODOG_HOOK_LAST_ASSISTANT_MESSAGE="+payload.LastAssistant,
	)
	cmd.Stdin = bytes.NewReader(data)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err := cmd.Run()
	duration := time.Since(started).Milliseconds()
	result := CommandResult{
		Command:    command,
		Type:       "command",
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   0,
		DurationMS: duration,
		Success:    true,
	}
	if hookCtx.Err() == context.DeadlineExceeded {
		result.Success = false
		result.ExitCode = -1
		result.Error = "timeout"
		return result, fmt.Errorf("hook timed out: %s", command)
	}
	if err != nil {
		result.Success = false
		result.ExitCode = hookExitCode(err)
		result.Error = err.Error()
		return result, fmt.Errorf("hook failed: %s: %s", command, stderr.String())
	}
	return result, nil
}

func (r Runner) runHTTPHook(ctx context.Context, hook config.HookCommand, data []byte, defaultTimeout time.Duration) (CommandResult, error) {
	url := strings.TrimSpace(hook.URL)
	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout(hook, defaultTimeout))
	defer cancel()
	req, err := http.NewRequestWithContext(hookCtx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		result := CommandResult{Type: "http", URL: url, Command: config.HookCommandDisplay(hook), ExitCode: -1, Success: false, Error: err.Error()}
		return result, err
	}
	req.Header.Set("content-type", "application/json")
	for key, value := range hook.Headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		req.Header.Set(key, expandAllowedEnv(value, hook.AllowedEnvVars))
	}
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	duration := time.Since(started).Milliseconds()
	result := CommandResult{
		Command:    config.HookCommandDisplay(hook),
		Type:       "http",
		URL:        url,
		ExitCode:   0,
		DurationMS: duration,
		Success:    true,
	}
	if hookCtx.Err() == context.DeadlineExceeded {
		result.Success = false
		result.ExitCode = -1
		result.Error = "timeout"
		return result, fmt.Errorf("hook timed out: %s", url)
	}
	if err != nil {
		result.Success = false
		result.ExitCode = -1
		result.Error = err.Error()
		return result, fmt.Errorf("http hook failed: %s: %w", url, err)
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		result.Success = false
		result.ExitCode = -1
		result.Error = readErr.Error()
		return result, readErr
	}
	result.Stdout = string(body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Success = false
		result.ExitCode = resp.StatusCode
		result.Error = resp.Status
		return result, fmt.Errorf("http hook failed: %s: %s", url, resp.Status)
	}
	return result, nil
}

func hookShell(hook config.HookCommand, command string) (string, []string) {
	switch strings.ToLower(strings.TrimSpace(hook.Shell)) {
	case "bash":
		return "bash", []string{"-lc", command}
	case "zsh":
		return "zsh", []string{"-lc", command}
	case "powershell", "pwsh":
		return "pwsh", []string{"-NoLogo", "-NoProfile", "-Command", command}
	default:
		return "sh", []string{"-lc", command}
	}
}

func hookTimeout(hook config.HookCommand, fallback time.Duration) time.Duration {
	if hook.TimeoutSeconds > 0 {
		return time.Duration(hook.TimeoutSeconds * float64(time.Second))
	}
	return fallback
}

func hookType(hook config.HookCommand) string {
	value := strings.ToLower(strings.TrimSpace(hook.Type))
	if value == "" {
		return "command"
	}
	return value
}

func hookExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func matchingHooks(entries []config.HookCommand, fallback []string, payload Payload) []config.HookCommand {
	if len(entries) == 0 {
		return hookCommandsFromStrings(fallback)
	}
	hookList := []config.HookCommand{}
	target := matcherTarget(payload)
	for _, entry := range entries {
		if matcherMatches(entry.Matcher, target) && conditionMatches(entry.If, payload) {
			if strings.TrimSpace(config.HookCommandDisplay(entry)) != "" {
				hookList = append(hookList, entry)
			}
		}
	}
	return hookList
}

func matcherTarget(payload Payload) string {
	if normalizeEvent(payload.Event) == "notification" && strings.TrimSpace(payload.NotificationType) != "" {
		return payload.NotificationType
	}
	if isSubagentEvent(payload.Event) && strings.TrimSpace(payload.AgentType) != "" {
		return payload.AgentType
	}
	return payload.Tool
}

func isSubagentEvent(event string) bool {
	switch normalizeEvent(event) {
	case "subagent_start", "subagent_stop":
		return true
	default:
		return false
	}
}

func hookCommandsFromStrings(values []string) []config.HookCommand {
	out := make([]config.HookCommand, 0, len(values))
	for _, value := range compactStrings(values) {
		out = append(out, config.HookCommand{Type: "command", Command: value})
	}
	return out
}

func matcherMatches(matcher string, tool string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	candidates := toolCandidates(tool)
	for _, part := range strings.Split(matcher, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(part, candidate) {
				return true
			}
			if ok, _ := path.Match(part, candidate); ok {
				return true
			}
		}
		if re, err := regexp.Compile(part); err == nil {
			for _, candidate := range candidates {
				if re.MatchString(candidate) {
					return true
				}
			}
		}
	}
	return false
}

func toolCandidates(tool string) []string {
	tool = strings.TrimSpace(tool)
	normalized := normalizeTool(tool)
	claude := claudeToolName(normalized)
	values := []string{tool, normalized, claude}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func conditionMatches(condition string, payload Payload) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}
	open := strings.Index(condition, "(")
	close := strings.LastIndex(condition, ")")
	if open <= 0 || close <= open {
		return matcherMatches(condition, matcherTarget(payload))
	}
	target := strings.TrimSpace(condition[:open])
	pattern := strings.TrimSpace(condition[open+1 : close])
	if !matcherMatches(target, matcherTarget(payload)) {
		return false
	}
	if pattern == "" || pattern == "*" {
		return true
	}
	for _, value := range payloadMatchValues(payload) {
		if valueMatchesPattern(pattern, value) {
			return true
		}
	}
	return false
}

func payloadMatchValues(payload Payload) []string {
	values := []string{payload.Input, payload.Output, payload.Message, payload.Title, payload.NotificationType, payload.AgentID, payload.AgentType, payload.TranscriptPath, payload.LastAssistant, payload.ToolName, payload.ToolUseID, payload.Reason}
	if len(payload.ToolInput) != 0 {
		values = append(values, string(payload.ToolInput))
	}
	var decoded any
	if err := json.Unmarshal([]byte(payload.Input), &decoded); err == nil {
		collectJSONStrings(decoded, &values)
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func collectJSONStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectJSONStrings(item, out)
		}
	case map[string]any:
		for _, item := range typed {
			collectJSONStrings(item, out)
		}
	}
}

func valueMatchesPattern(pattern string, value string) bool {
	if ok, _ := path.Match(pattern, value); ok {
		return true
	}
	if strings.Contains(value, pattern) {
		return true
	}
	if re, err := regexp.Compile(pattern); err == nil && re.MatchString(value) {
		return true
	}
	return false
}

func expandAllowedEnv(value string, allowed []string) string {
	allow := map[string]struct{}{}
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name != "" {
			allow[name] = struct{}{}
		}
	}
	return os.Expand(value, func(name string) string {
		if _, ok := allow[name]; !ok {
			return ""
		}
		return os.Getenv(name)
	})
}

func normalizeTool(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	tool = strings.ReplaceAll(tool, "-", "_")
	switch tool {
	case "bash":
		return "bash"
	case "read":
		return "read_file"
	case "write":
		return "write_file"
	case "edit":
		return "edit_file"
	case "multiedit", "multi_edit":
		return "multi_edit"
	case "grep":
		return "grep"
	case "glob":
		return "glob"
	default:
		return tool
	}
}

func claudeToolName(tool string) string {
	switch normalizeTool(tool) {
	case "bash":
		return "Bash"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "multi_edit":
		return "MultiEdit"
	case "grep":
		return "Grep"
	case "glob":
		return "Glob"
	default:
		return tool
	}
}

func normalizeEvent(event string) string {
	event = strings.ToLower(strings.TrimSpace(event))
	switch event {
	case "pre", "pretooluse", "pre_tool_use", "pre-tool-use":
		return "pre_tool_use"
	case "post", "posttooluse", "post_tool_use", "post-tool-use":
		return "post_tool_use"
	case "postfailure", "post-failure", "posttoolusefailure", "post_tool_use_failure", "post-tool-use-failure":
		return "post_tool_use_failure"
	case "permissionrequest", "permission_request", "permission-request":
		return "permission_request"
	case "permissiondenied", "permission_denied", "permission-denied":
		return "permission_denied"
	case "userpromptsubmit", "user_prompt_submit", "user-prompt-submit":
		return "user_prompt_submit"
	case "sessionstart", "session_start", "session-start":
		return "session_start"
	case "sessionend", "session_end", "session-end":
		return "session_end"
	case "setup":
		return "setup"
	case "stop":
		return "stop"
	case "precompact", "pre_compact", "pre-compact":
		return "pre_compact"
	case "postcompact", "post_compact", "post-compact":
		return "post_compact"
	case "notification", "notify":
		return "notification"
	case "subagentstart", "subagent_start", "subagent-start":
		return "subagent_start"
	case "subagentstop", "subagent_stop", "subagent-stop":
		return "subagent_stop"
	default:
		return event
	}
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
