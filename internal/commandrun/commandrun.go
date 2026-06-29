package commandrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type Options struct {
	Workspace string
	Command   []string
	Timeout   time.Duration
	Kind      string
}

type Result struct {
	Kind           string   `json:"kind"`
	Workspace      string   `json:"workspace"`
	Command        []string `json:"command"`
	ExitCode       int      `json:"exit_code"`
	Stdout         string   `json:"stdout,omitempty"`
	Stderr         string   `json:"stderr,omitempty"`
	TimedOut       bool     `json:"timed_out,omitempty"`
	DurationMillis int64    `json:"duration_millis"`
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Command) == 0 || strings.TrimSpace(opts.Command[0]) == "" {
		return Result{}, errors.New("command is required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Workspace
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := Result{
		Kind:           opts.Kind,
		Workspace:      opts.Workspace,
		Command:        append([]string(nil), opts.Command...),
		Stdout:         stdout.String(),
		Stderr:         stderr.String(),
		DurationMillis: time.Since(start).Milliseconds(),
	}
	if result.Kind == "" {
		result.Kind = "command"
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, err
	}
	return result, nil
}

func RenderText(out io.Writer, result Result) {
	fmt.Fprintln(out, "Command")
	fmt.Fprintf(out, "  Kind             %s\n", result.Kind)
	fmt.Fprintf(out, "  Command          %s\n", strings.Join(result.Command, " "))
	fmt.Fprintf(out, "  Exit code        %d\n", result.ExitCode)
	fmt.Fprintf(out, "  Duration         %dms\n", result.DurationMillis)
	if result.TimedOut {
		fmt.Fprintln(out, "  Timed out        true")
	}
	if strings.TrimSpace(result.Stdout) != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "stdout:")
		fmt.Fprint(out, result.Stdout)
		if !strings.HasSuffix(result.Stdout, "\n") {
			fmt.Fprintln(out)
		}
	}
	if strings.TrimSpace(result.Stderr) != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "stderr:")
		fmt.Fprint(out, result.Stderr)
		if !strings.HasSuffix(result.Stderr, "\n") {
			fmt.Fprintln(out)
		}
	}
}
