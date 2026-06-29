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
	if len(commands) == 0 {
		return nil
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
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
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		cancel()
		if hookCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("hook timed out: %s", command)
		}
		if err != nil {
			return fmt.Errorf("hook failed: %s: %s", command, stderr.String())
		}
	}
	return nil
}
