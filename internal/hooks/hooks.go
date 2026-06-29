package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	return r.run(ctx, r.Config.PreToolUse, Payload{
		Event: "pre_tool_use",
		Tool:  tool,
		Input: string(input),
	})
}

func (r Runner) PostToolUse(ctx context.Context, tool string, input []byte, output string, isError bool) error {
	return r.run(ctx, r.Config.PostToolUse, Payload{
		Event:   "post_tool_use",
		Tool:    tool,
		Input:   string(input),
		Output:  output,
		IsError: isError,
	})
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
