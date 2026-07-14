package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseHooksArgsValueOptions(t *testing.T) {
	req, err := parseHooksArgs([]string{
		"run", "instructions-loaded", "--json",
		"--session", "session-1",
		"--tool", "custom",
		"--input", `{"path":"AGENTS.md"}`,
		"--output", "loaded",
		"--notification-type", "status",
		"--title", "Instructions",
		"--agent-id", "agent-1",
		"--agent-type", "reviewer",
		"--agent-transcript-path", "transcript.jsonl",
		"--last-assistant-message", "done",
		"--worktree-id", "worktree-1",
		"--worktree-path", "worktrees/one",
		"--ref", "abc123",
		"--old-cwd", "/old",
		"--new-cwd", "/new",
		"--file-path", "AGENTS.md",
		"--operation", "read_file",
		"--memory-type", "Project",
		"--load-reason", "session_start",
		"--glob", "*.md",
		"--glob", "docs/**",
		"--trigger-file-path", "main.go",
		"--parent-file-path", "README.md",
		"--task-id", "task-1",
		"--task-kind", "agent",
		"--task-status", "running",
		"--reason", "manual",
		"--timeout-ms", "1250",
		"--stop-hook-active", "--error",
	})
	require.NoError(t, err)
	require.Equal(t, "run", req.Action)
	require.Equal(t, "instructions_loaded", req.Event)
	require.Equal(t, "json", req.Format)
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "custom", req.Tool)
	require.Equal(t, `{"path":"AGENTS.md"}`, req.Input)
	require.Equal(t, "loaded", req.Output)
	require.Equal(t, "status", req.NotificationType)
	require.Equal(t, "Instructions", req.Title)
	require.Equal(t, "agent-1", req.AgentID)
	require.Equal(t, "reviewer", req.AgentType)
	require.Equal(t, "transcript.jsonl", req.TranscriptPath)
	require.Equal(t, "done", req.LastAssistant)
	require.Equal(t, "worktree-1", req.WorktreeID)
	require.Equal(t, "worktrees/one", req.WorktreePath)
	require.Equal(t, "abc123", req.Ref)
	require.Equal(t, "/old", req.OldCWD)
	require.Equal(t, "/new", req.NewCWD)
	require.Equal(t, "AGENTS.md", req.FilePath)
	require.Equal(t, "read_file", req.Operation)
	require.Equal(t, "Project", req.MemoryType)
	require.Equal(t, "session_start", req.LoadReason)
	require.Equal(t, []string{"*.md", "docs/**"}, req.Globs)
	require.Equal(t, "main.go", req.TriggerFilePath)
	require.Equal(t, "README.md", req.ParentFilePath)
	require.Equal(t, "task-1", req.TaskID)
	require.Equal(t, "agent", req.TaskKind)
	require.Equal(t, "running", req.TaskStatus)
	require.Equal(t, "manual", req.Reason)
	require.Equal(t, 1250, req.TimeoutMS)
	require.True(t, req.StopHookActive)
	require.True(t, req.IsError)
}

func TestParseHooksArgsInlineOptionsAndDefaults(t *testing.T) {
	req, err := parseHooksArgs([]string{
		"health", "notification",
		"--output-format=json", "--session=session-1", "--tool=background_task",
		"--input={}", "--output=ok", "--title=Started", "--agent-id=agent-1",
		"--agent-type=worker", "--agent-transcript-path=agent.jsonl",
		"--last-assistant-message=done", "--worktree-id=worktree-1",
		"--worktree-path=worktrees/one", "--ref=abc123", "--old-cwd=/old",
		"--new-cwd=/new", "--path=notes.md", "--operation=write_file",
		"--memory-type=Project", "--glob=*.md", "--trigger-file-path=main.go",
		"--parent-file-path=README.md", "--task-id=task-1", "--task-kind=agent",
		"--task-status=done", "--reason=complete", "--timeout-ms=0",
	})
	require.NoError(t, err)
	require.Equal(t, "health", req.Action)
	require.Equal(t, "notification", req.Event)
	require.Equal(t, "background_task", req.NotificationType)
	require.Equal(t, "json", req.Format)
	require.Zero(t, req.TimeoutMS)

	req, err = parseHooksArgs([]string{"run", "subagent-start", "reviewer"})
	require.NoError(t, err)
	require.Empty(t, req.Tool)
	require.Empty(t, req.AgentType)
}

func TestParseHooksArgsFailures(t *testing.T) {
	missingOptions := map[string]string{
		"--output-format": "hooks output format is required",
		"-o":              "hooks output format is required",
		"--session":       "hooks session is required",
		"--tool":          "hooks tool is required",
		"--input":         "hooks input is required",
		"--output":        "hooks output is required",
		"--file-path":     "hooks file path is required",
		"--glob":          "hooks glob is required",
		"--timeout-ms":    "hooks timeout is required",
	}
	for option, message := range missingOptions {
		t.Run(option, func(t *testing.T) {
			_, err := parseHooksArgs([]string{option})
			require.EqualError(t, err, message)
		})
	}

	_, err := parseHooksArgs([]string{"--timeout-ms=-1"})
	require.EqualError(t, err, "hooks timeout must be a non-negative integer")
	_, err = parseHooksArgs([]string{"--output-format=yaml"})
	require.EqualError(t, err, `unknown hooks output format "yaml"`)
	_, err = parseHooksArgs([]string{"watch-paths", "unknown"})
	require.Error(t, err)
	_, err = parseHooksArgs([]string{"unknown"})
	require.Error(t, err)
}

func TestParseHooksArgsWatchPaths(t *testing.T) {
	for _, action := range []string{"check", "scan"} {
		req, err := parseHooksArgs([]string{"watch", action, "session-1"})
		require.NoError(t, err)
		require.Equal(t, "watch-paths", req.Action)
		require.Equal(t, "check", req.WatchAction)
		require.Equal(t, "session-1", req.SessionID)
	}
}
