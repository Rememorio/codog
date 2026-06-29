package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunPayloadCapturesHookOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	report, err := Runner{Workspace: workspace}.RunPayload(context.Background(), []string{"cat && echo err >&2"}, Payload{
		Event: "pre_tool_use",
		Tool:  "read_file",
		Input: `{"path":"README.md"}`,
	})
	require.NoError(t, err)
	require.Equal(t, "hooks", report.Kind)
	require.Len(t, report.Results, 1)
	require.True(t, report.Results[0].Success)
	require.Contains(t, report.Results[0].Stdout, `"tool":"read_file"`)
	require.Contains(t, report.Results[0].Stdout, "README.md")
	require.Contains(t, report.Results[0].Stderr, "err")
}

func TestRunPayloadReturnsPartialFailureReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	path := filepath.Join(workspace, "ran.txt")
	report, err := Runner{Workspace: workspace}.RunPayload(context.Background(), []string{"echo ok > ran.txt", "exit 7"}, Payload{Event: "post_tool_use"})
	require.Error(t, err)
	require.Len(t, report.Results, 2)
	require.True(t, report.Results[0].Success)
	require.False(t, report.Results[1].Success)
	require.FileExists(t, path)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "ok\n", string(data))
}

func TestCommandsForEventFiltersMatchers(t *testing.T) {
	cfg := config.HookConfig{
		PreToolUse: []string{"legacy"},
		PreToolUseCommands: []config.HookCommand{
			{Matcher: "Write", Command: "write-only"},
			{Matcher: "Bash|Glob", Command: "regex"},
			{Matcher: "read_*", Command: "glob"},
			{Command: "all"},
		},
		PostToolUseCommands: []config.HookCommand{
			{Matcher: "Edit,MultiEdit", Command: "edits"},
		},
	}

	require.Equal(t, []string{"write-only", "all"}, CommandsForEvent(cfg, "pre_tool_use", "write_file"))
	require.Equal(t, []string{"glob", "all"}, CommandsForEvent(cfg, "pre_tool_use", "read_file"))
	require.Equal(t, []string{"regex", "all"}, CommandsForEvent(cfg, "pre_tool_use", "bash"))
	require.Equal(t, []string{"edits"}, CommandsForEvent(cfg, "post", "multi_edit"))
	require.Equal(t, []string{"all"}, CommandsForEvent(cfg, "pre_tool_use", "grep"))
}
