package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LifecycleDefaultTimeout is the per-command timeout used for plugin lifecycle hooks.
const LifecycleDefaultTimeout = 30 * time.Second

// LifecycleRunResult summarizes an explicit plugin lifecycle phase execution.
type LifecycleRunResult struct {
	PluginID     string                   `json:"plugin_id"`
	Phase        string                   `json:"phase"`
	Status       string                   `json:"status"`
	CommandCount int                      `json:"command_count"`
	Commands     []LifecycleCommandResult `json:"commands"`
	Message      string                   `json:"message,omitempty"`
}

// LifecycleCommandResult records one lifecycle command invocation.
type LifecycleCommandResult struct {
	Index           int    `json:"index"`
	Command         string `json:"command"`
	WorkingDir      string `json:"working_dir,omitempty"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	DurationMS      int64  `json:"duration_ms"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	Error           string `json:"error,omitempty"`
}

// LifecycleCommands returns the manifest commands configured for a lifecycle phase.
func LifecycleCommands(manifest Manifest, phase string) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "init", "start", "startup":
		return append([]string(nil), manifest.Lifecycle.Init...), true
	case "shutdown", "stop", "teardown":
		return append([]string(nil), manifest.Lifecycle.Shutdown...), true
	default:
		return nil, false
	}
}

// RunLifecycle executes one lifecycle phase for a plugin manifest.
func RunLifecycle(ctx context.Context, manifest Manifest, phase string, timeout time.Duration) LifecycleRunResult {
	normalizedPhase := normalizeLifecyclePhase(phase)
	result := LifecycleRunResult{
		PluginID: manifest.ID,
		Phase:    normalizedPhase,
		Status:   "ok",
	}
	if !manifest.Enabled {
		result.Status = "skipped"
		result.Message = "plugin is disabled"
		return result
	}
	commands, ok := LifecycleCommands(manifest, normalizedPhase)
	if !ok {
		result.Status = "failed"
		result.Message = fmt.Sprintf("unsupported lifecycle phase %q", phase)
		return result
	}
	result.CommandCount = len(commands)
	if len(commands) == 0 {
		result.Status = "skipped"
		result.Message = "no lifecycle commands are configured for this phase"
		return result
	}
	if timeout <= 0 {
		timeout = LifecycleDefaultTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for index, command := range commands {
		commandResult := runLifecycleCommand(ctx, manifest.Root, normalizedPhase, index, command, timeout)
		result.Commands = append(result.Commands, commandResult)
		if commandResult.Status == "failed" {
			result.Status = "failed"
		}
	}
	if result.Status == "failed" {
		result.Message = "one or more lifecycle commands failed"
	}
	return result
}

func normalizeLifecyclePhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "start", "startup":
		return "init"
	case "stop", "teardown":
		return "shutdown"
	default:
		return strings.ToLower(strings.TrimSpace(phase))
	}
}

func runLifecycleCommand(ctx context.Context, root string, phase string, index int, command string, timeout time.Duration) LifecycleCommandResult {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := lifecycleShellCommand(runCtx, command)
	cmd.Dir = root
	stdout := &lifecycleBoundedBuffer{Limit: 256 * 1024}
	stderr := &lifecycleBoundedBuffer{Limit: 64 * 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	result := LifecycleCommandResult{
		Index:           index,
		Command:         command,
		WorkingDir:      root,
		Status:          "ok",
		ExitCode:        0,
		DurationMS:      time.Since(start).Milliseconds(),
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated,
		StderrTruncated: stderr.Truncated,
	}
	if err == nil {
		return result
	}
	result.Status = "failed"
	result.ExitCode = -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.Error = fmt.Sprintf("plugin lifecycle %s command timed out after %s", phase, timeout)
		return result
	}
	result.Error = err.Error()
	return result
}

func lifecycleShellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

type lifecycleBoundedBuffer struct {
	bytes.Buffer
	Limit     int
	Truncated bool
}

func (b *lifecycleBoundedBuffer) Write(data []byte) (int, error) {
	accepted := len(data)
	if b.Limit <= 0 {
		return accepted, nil
	}
	remaining := b.Limit - b.Buffer.Len()
	if remaining <= 0 {
		b.Truncated = b.Truncated || len(data) > 0
		return accepted, nil
	}
	if len(data) > remaining {
		b.Truncated = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return accepted, nil
}
