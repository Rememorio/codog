package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/config"
)

type Runner struct {
	Config    config.HookConfig
	Workspace string
	Timeout   time.Duration
}

type Payload struct {
	Event   string `json:"event"`
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

type CommandResult struct {
	Command    string `json:"command"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
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
	return r.run(ctx, CommandsForEvent(r.Config, "pre_tool_use", tool), Payload{
		Event: "pre_tool_use",
		Tool:  tool,
		Input: string(input),
	})
}

func (r Runner) PostToolUse(ctx context.Context, tool string, input []byte, output string, isError bool) error {
	return r.run(ctx, CommandsForEvent(r.Config, "post_tool_use", tool), Payload{
		Event:   "post_tool_use",
		Tool:    tool,
		Input:   string(input),
		Output:  output,
		IsError: isError,
	})
}

func CommandsForEvent(cfg config.HookConfig, event string, tool string) []string {
	switch normalizeEvent(event) {
	case "pre_tool_use":
		return matchingCommands(cfg.PreToolUseCommands, cfg.PreToolUse, tool)
	case "post_tool_use":
		return matchingCommands(cfg.PostToolUseCommands, cfg.PostToolUse, tool)
	default:
		return nil
	}
}

func (r Runner) run(ctx context.Context, commands []string, payload Payload) error {
	_, err := r.RunPayload(ctx, commands, payload)
	return err
}

func (r Runner) RunPayload(ctx context.Context, commands []string, payload Payload) (RunReport, error) {
	report := RunReport{
		Kind:    "hooks",
		Event:   payload.Event,
		Tool:    payload.Tool,
		Count:   len(commands),
		Results: []CommandResult{},
	}
	if len(commands) == 0 {
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
	for _, command := range commands {
		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(hookCtx, "sh", "-lc", command)
		cmd.Dir = r.Workspace
		cmd.Env = append(os.Environ(),
			"CODOG_HOOK_EVENT="+payload.Event,
			"CODOG_HOOK_TOOL="+payload.Tool,
		)
		cmd.Stdin = bytes.NewReader(data)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		started := time.Now()
		err := cmd.Run()
		duration := time.Since(started).Milliseconds()
		cancel()
		result := CommandResult{
			Command:    command,
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			DurationMS: duration,
			Success:    true,
		}
		if hookCtx.Err() == context.DeadlineExceeded {
			result.Success = false
			result.Error = "timeout"
			report.Results = append(report.Results, result)
			return report, fmt.Errorf("hook timed out: %s", command)
		}
		if err != nil {
			result.Success = false
			result.Error = err.Error()
			report.Results = append(report.Results, result)
			return report, fmt.Errorf("hook failed: %s: %s", command, stderr.String())
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func matchingCommands(entries []config.HookCommand, fallback []string, tool string) []string {
	if len(entries) == 0 {
		return compactStrings(fallback)
	}
	commands := []string{}
	for _, entry := range entries {
		if matcherMatches(entry.Matcher, tool) {
			command := strings.TrimSpace(entry.Command)
			if command != "" {
				commands = append(commands, command)
			}
		}
	}
	return commands
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
	case "pre", "pretooluse", "pre_tool_use":
		return "pre_tool_use"
	case "post", "posttooluse", "post_tool_use":
		return "post_tool_use"
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
