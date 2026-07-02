package hooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hookenv"
	"github.com/stretchr/testify/require"
)

func TestRunPayloadCapturesHookOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	report, err := Runner{Workspace: workspace}.RunPayload(context.Background(), []string{"cat && printf '\\n%s\\n%s\\n%s\\n%s\\n%s\\n' \"$CODOG_HOOK_EVENT\" \"$CODOG_HOOK_TOOL\" \"$CODOG_HOOK_INPUT\" \"$CODOG_HOOK_OUTPUT\" \"$CODOG_HOOK_IS_ERROR\" && echo err >&2"}, Payload{
		Event:   "pre_tool_use",
		Tool:    "read_file",
		Input:   `{"path":"README.md"}`,
		Output:  "done",
		IsError: true,
	})
	require.NoError(t, err)
	require.Equal(t, "hooks", report.Kind)
	require.Len(t, report.Results, 1)
	require.True(t, report.Results[0].Success)
	require.Equal(t, 0, report.Results[0].ExitCode)
	require.Contains(t, report.Results[0].Stdout, `"tool":"read_file"`)
	require.Contains(t, report.Results[0].Stdout, "README.md")
	require.Contains(t, report.Results[0].Stdout, "pre_tool_use")
	require.Contains(t, report.Results[0].Stdout, "read_file")
	require.Contains(t, report.Results[0].Stdout, "done")
	require.Contains(t, report.Results[0].Stdout, "true")
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
	require.Equal(t, 7, report.Results[1].ExitCode)
	require.FileExists(t, path)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "ok\n", string(data))
}

func TestRunPayloadProvidesClaudeEnvFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	configHome := t.TempDir()
	report, err := Runner{Workspace: t.TempDir(), ConfigHome: configHome, SessionID: "session-1"}.RunPayload(context.Background(), []string{"printf 'export CODOG_HOOK_ENV_READY=yes\\n' > \"$CLAUDE_ENV_FILE\""}, Payload{Event: "session_start"})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.True(t, report.Results[0].Success)

	env, err := hookenv.Load(configHome, "session-1")
	require.NoError(t, err)
	require.Contains(t, env, "CODOG_HOOK_ENV_READY=yes")
}

func TestSessionHooksUseClaudeCompatibleStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	transcript := filepath.Join(workspace, "session.jsonl")
	report, err := Runner{Workspace: workspace, SessionID: "session-1"}.RunPayload(context.Background(), []string{"cat && printf '\\n%s\\n%s\\n%s\\n' \"$CLAUDE_SESSION_ID\" \"$CLAUDE_HOOK_EVENT_NAME\" \"$CLAUDE_TRANSCRIPT_PATH\""}, Payload{
		Event: "session_start",
		Input: `{"hook_event_name":"SessionStart","source":"startup","session_id":"session-1","transcript_path":"` + filepath.ToSlash(transcript) + `","cwd":"` + filepath.ToSlash(workspace) + `","permission_mode":"workspace-write","model":"claude-test"}`,
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	stdout := report.Results[0].Stdout
	require.Contains(t, stdout, `"hook_event_name":"SessionStart"`)
	require.Contains(t, stdout, `"event":"session_start"`)
	require.Contains(t, stdout, `"session_id":"session-1"`)
	require.Contains(t, stdout, `"transcript_path":"`+filepath.ToSlash(transcript)+`"`)
	require.Contains(t, stdout, `"cwd":"`+filepath.ToSlash(workspace)+`"`)
	require.Contains(t, stdout, `"permission_mode":"workspace-write"`)
	require.Contains(t, stdout, "\nsession-1\nSessionStart\n"+filepath.ToSlash(transcript)+"\n")

	report, err = Runner{Workspace: workspace, SessionID: "session-1"}.RunPayload(context.Background(), []string{"cat"}, Payload{
		Event:  "session_end",
		Input:  `{"hook_event_name":"SessionEnd","session_id":"session-1","transcript_path":"` + filepath.ToSlash(transcript) + `","cwd":"` + filepath.ToSlash(workspace) + `"}`,
		Reason: "resume",
	})
	require.NoError(t, err)
	require.Contains(t, report.Results[0].Stdout, `"hook_event_name":"SessionEnd"`)
	require.Contains(t, report.Results[0].Stdout, `"event":"session_end"`)
	require.Contains(t, report.Results[0].Stdout, `"reason":"resume"`)
}

func TestSessionHooksMatchClaudeSourceAndReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	startPath := filepath.Join(workspace, "start.json")
	endPath := filepath.Join(workspace, "end.json")
	runner := Runner{
		Workspace:  workspace,
		SessionID:  "session-1",
		ConfigHome: t.TempDir(),
		Config: config.HookConfig{
			SessionStartCommands: []config.HookCommand{{Matcher: "startup", Command: "cat > start.json"}},
			SessionEndCommands:   []config.HookCommand{{Matcher: "resume", Command: "cat > end.json"}},
		},
	}
	require.NoError(t, runner.SessionStart(context.Background(), `{"hook_event_name":"SessionStart","source":"startup","session_id":"session-1","cwd":"`+filepath.ToSlash(workspace)+`"}`))
	data, err := os.ReadFile(startPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"hook_event_name":"SessionStart"`)
	require.Contains(t, string(data), `"source":"startup"`)

	require.NoError(t, runner.SessionEnd(context.Background(), `{"hook_event_name":"SessionEnd","session_id":"session-1","cwd":"`+filepath.ToSlash(workspace)+`"}`, "resume"))
	data, err = os.ReadFile(endPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"hook_event_name":"SessionEnd"`)
	require.Contains(t, string(data), `"reason":"resume"`)
}

func TestRunHooksPostsHTTPPayloadWithAllowedHeaders(t *testing.T) {
	t.Setenv("HOOK_TOKEN", "secret-token")
	t.Setenv("HOOK_IGNORED", "ignored")
	var gotAuth string
	var gotIgnored string
	var gotPayload Payload
	var decodeErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotIgnored = r.Header.Get("X-Ignored")
		decodeErr = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	}))
	t.Cleanup(server.Close)

	report, err := Runner{Workspace: t.TempDir()}.RunHooks(context.Background(), []config.HookCommand{{
		Type:           "http",
		URL:            server.URL,
		Headers:        map[string]string{"Authorization": "Bearer $HOOK_TOKEN", "X-Ignored": "$HOOK_IGNORED"},
		AllowedEnvVars: []string{"HOOK_TOKEN"},
	}}, Payload{Event: "post_tool_use", Tool: "bash", Input: `{"command":"git status"}`, Output: "done"})
	require.NoError(t, err)
	require.NoError(t, decodeErr)
	require.Len(t, report.Results, 1)
	require.Equal(t, "http", report.Results[0].Type)
	require.Equal(t, http.StatusAccepted, report.Results[0].StatusCode)
	require.Contains(t, report.Results[0].Stdout, "accepted")
	require.Equal(t, "Bearer secret-token", gotAuth)
	require.Empty(t, gotIgnored)
	require.Equal(t, "post_tool_use", gotPayload.Event)
	require.Equal(t, "bash", gotPayload.Tool)
	require.Equal(t, "done", gotPayload.Output)
}

func TestRunHooksExecutesPromptHookWithRenderedArguments(t *testing.T) {
	var got PromptRequest
	report, err := Runner{
		Workspace: t.TempDir(),
		PromptRunner: func(_ context.Context, req PromptRequest) (string, error) {
			got = req
			return "verified", nil
		},
	}.RunHooks(context.Background(), []config.HookCommand{{
		Type:   "prompt",
		Prompt: "verify $ARGUMENTS",
		Model:  "fast-model",
	}}, Payload{Event: "pre_tool_use", Tool: "write_file", Input: `{"path":"notes.txt"}`})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.Equal(t, "prompt", report.Results[0].Type)
	require.Equal(t, "verified", report.Results[0].Stdout)
	require.Equal(t, "fast-model", got.Model)
	require.Contains(t, got.Prompt, "verify")
	require.Contains(t, got.Prompt, "notes.txt")
	require.Equal(t, "write_file", got.Payload.Tool)
}

func TestCommandsForEventFiltersMatchers(t *testing.T) {
	cfg := config.HookConfig{
		PreToolUse: []string{"legacy"},
		PreToolUseCommands: []config.HookCommand{
			{Matcher: "Write", Type: "command", Command: "write-only"},
			{Matcher: "Bash|Glob", Type: "command", Command: "regex"},
			{Matcher: "read_*", Type: "command", Command: "glob"},
			{Type: "command", Command: "all"},
		},
		PostToolUseCommands: []config.HookCommand{
			{Matcher: "Edit,MultiEdit", Type: "command", Command: "edits"},
		},
		PostToolUseFailureCommands: []config.HookCommand{
			{Matcher: "Bash", Type: "command", Command: "failed-bash"},
		},
		PermissionRequestCommands: []config.HookCommand{
			{Matcher: "Bash", Type: "command", Command: "permission-request"},
		},
		PermissionDeniedCommands: []config.HookCommand{
			{Matcher: "Bash", Type: "command", Command: "permission-denied"},
		},
		UserPromptSubmitCommands: []config.HookCommand{
			{Type: "command", Command: "prompt"},
		},
		SessionStartCommands: []config.HookCommand{
			{Type: "command", Command: "session"},
		},
		SessionEndCommands: []config.HookCommand{
			{Type: "command", Command: "session-end"},
		},
		SetupCommands: []config.HookCommand{
			{Type: "command", Command: "setup"},
		},
		StopCommands: []config.HookCommand{
			{Type: "command", Command: "stop"},
		},
		StopFailureCommands: []config.HookCommand{
			{Type: "command", Command: "stop-failure"},
		},
		PreCompactCommands: []config.HookCommand{
			{Type: "command", Command: "compact"},
		},
		PostCompactCommands: []config.HookCommand{
			{Type: "command", Command: "post-compact"},
		},
		NotificationCommands: []config.HookCommand{
			{Matcher: "background_*", Type: "command", Command: "notify"},
		},
		SubagentStartCommands: []config.HookCommand{
			{Matcher: "reviewer", Type: "command", Command: "agent-start"},
		},
		SubagentStopCommands: []config.HookCommand{
			{Matcher: "reviewer", Type: "command", Command: "agent-stop"},
		},
		WorktreeCreateCommands: []config.HookCommand{
			{Matcher: "agent-*", Type: "command", Command: "worktree-create"},
		},
		WorktreeRemoveCommands: []config.HookCommand{
			{Matcher: "agent-*", Type: "command", Command: "worktree-remove"},
		},
		CwdChangedCommands: []config.HookCommand{
			{Matcher: "/repo/*", Type: "command", Command: "cwd-changed"},
		},
		TaskCreatedCommands: []config.HookCommand{
			{Matcher: "agent", Type: "command", Command: "task-created"},
		},
		TaskCompletedCommands: []config.HookCommand{
			{Matcher: "agent", Type: "command", Command: "task-completed"},
		},
		InstructionsLoadedCommands: []config.HookCommand{
			{Matcher: "session_start", Type: "command", Command: "instructions-loaded"},
		},
		FileChangedCommands: []config.HookCommand{
			{Matcher: "Write", Type: "command", Command: "file-changed"},
		},
	}

	require.Equal(t, []string{"write-only", "all"}, CommandsForEvent(cfg, "pre_tool_use", "write_file"))
	require.Equal(t, []string{"glob", "all"}, CommandsForEvent(cfg, "pre_tool_use", "read_file"))
	require.Equal(t, []string{"regex", "all"}, CommandsForEvent(cfg, "pre_tool_use", "bash"))
	require.Equal(t, []string{"edits"}, CommandsForEvent(cfg, "post", "multi_edit"))
	require.Equal(t, []string{"failed-bash"}, CommandsForEvent(cfg, "post-failure", "bash"))
	require.Equal(t, []string{"permission-request"}, CommandsForEvent(cfg, "permission-request", "bash"))
	require.Equal(t, []string{"permission-denied"}, CommandsForEvent(cfg, "permission-denied", "bash"))
	require.Equal(t, []string{"prompt"}, CommandsForEvent(cfg, "user-prompt-submit", ""))
	require.Equal(t, []string{"session"}, CommandsForEvent(cfg, "session-start", ""))
	require.Equal(t, []string{"session-end"}, CommandsForEvent(cfg, "session-end", ""))
	require.Equal(t, []string{"setup"}, CommandsForEvent(cfg, "setup", ""))
	require.Equal(t, []string{"stop"}, CommandsForEvent(cfg, "stop", ""))
	require.Equal(t, []string{"stop-failure"}, CommandsForEvent(cfg, "stop-failure", ""))
	require.Equal(t, []string{"compact"}, CommandsForEvent(cfg, "pre-compact", ""))
	require.Equal(t, []string{"post-compact"}, CommandsForEvent(cfg, "post-compact", ""))
	require.Equal(t, []string{"notify"}, CommandsForEvent(cfg, "notification", "background_task_started"))
	require.Equal(t, []string{"agent-start"}, CommandsForEvent(cfg, "subagent-start", "reviewer"))
	require.Equal(t, []string{"agent-stop"}, CommandsForEvent(cfg, "subagent-stop", "reviewer"))
	require.Equal(t, []string{"worktree-create"}, CommandsForEvent(cfg, "worktree-create", "agent-1"))
	require.Equal(t, []string{"worktree-remove"}, CommandsForEvent(cfg, "worktree-remove", "agent-1"))
	require.Equal(t, []string{"cwd-changed"}, CommandsForEvent(cfg, "cwd-changed", "/repo/subdir"))
	require.Equal(t, []string{"task-created"}, CommandsForEvent(cfg, "task-created", "agent"))
	require.Equal(t, []string{"task-completed"}, CommandsForEvent(cfg, "task-completed", "agent"))
	require.Equal(t, []string{"instructions-loaded"}, CommandsForEvent(cfg, "instructions-loaded", "session_start"))
	require.Equal(t, []string{"file-changed"}, CommandsForEvent(cfg, "file-changed", "write_file"))
	require.Equal(t, []string{"all"}, CommandsForEvent(cfg, "pre_tool_use", "grep"))
}

func TestHooksForPayloadFiltersInstructionsLoadedMatchersAndConditions(t *testing.T) {
	cfg := config.HookConfig{InstructionsLoadedCommands: []config.HookCommand{
		{Matcher: "session_start", Type: "command", If: "session_start(AGENTS.md)", Command: "agents"},
		{Matcher: "compact", Type: "command", Command: "compact"},
	}}

	matched := HooksForPayload(cfg, Payload{
		Event:      "instructions_loaded",
		Tool:       "session_start",
		FilePath:   "/repo/AGENTS.md",
		MemoryType: "Project",
		LoadReason: "session_start",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "agents", matched[0].Command)

	matched = HooksForPayload(cfg, Payload{
		Event:      "instructions_loaded",
		Tool:       "session_start",
		FilePath:   "/repo/README.md",
		MemoryType: "Project",
		LoadReason: "session_start",
	})
	require.Empty(t, matched)

	matched = HooksForPayload(cfg, Payload{
		Event:      "instructions_loaded",
		Tool:       "compact",
		FilePath:   "/repo/CLAUDE.md",
		MemoryType: "Project",
		LoadReason: "compact",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "compact", matched[0].Command)
}

func TestHooksForPayloadFiltersFileChangedMatchersAndConditions(t *testing.T) {
	cfg := config.HookConfig{FileChangedCommands: []config.HookCommand{
		{Matcher: "Write", Type: "command", If: "Write(docs/notes.md)", Command: "write-notes"},
		{Matcher: "NotebookEdit", Type: "command", Command: "notebook"},
	}}

	matched := HooksForPayload(cfg, Payload{
		Event:     "file_changed",
		Tool:      "write_file",
		Operation: "write_file",
		FilePath:  "docs/notes.md",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "write-notes", matched[0].Command)

	matched = HooksForPayload(cfg, Payload{
		Event:     "file_changed",
		Tool:      "write_file",
		Operation: "write_file",
		FilePath:  "docs/README.md",
	})
	require.Empty(t, matched)

	matched = HooksForPayload(cfg, Payload{
		Event:     "file_changed",
		Tool:      "notebook_edit",
		Operation: "notebook_edit",
		FilePath:  "analysis.ipynb",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "notebook", matched[0].Command)
}

func TestHooksForPayloadFiltersIfConditions(t *testing.T) {
	cfg := config.HookConfig{PreToolUseCommands: []config.HookCommand{
		{Matcher: "Bash", Type: "command", If: "Bash(git *)", Command: "git-hook"},
		{Matcher: "Bash", Type: "http", If: "Bash(npm *)", URL: "https://example.test/hook"},
	}}

	matched := HooksForPayload(cfg, Payload{Event: "pre_tool_use", Tool: "bash", Input: `{"command":"git status"}`})
	require.Len(t, matched, 1)
	require.Equal(t, "git-hook", matched[0].Command)

	matched = HooksForPayload(cfg, Payload{Event: "pre_tool_use", Tool: "bash", Input: `{"command":"npm test"}`})
	require.Len(t, matched, 1)
	require.Equal(t, "http", matched[0].Type)
}

func TestHooksForPayloadFiltersSubagentMatchersAndConditions(t *testing.T) {
	cfg := config.HookConfig{SubagentStopCommands: []config.HookCommand{
		{Matcher: "reviewer", Type: "command", If: "reviewer(*done*)", Command: "reviewer-stop"},
		{Matcher: "writer", Type: "command", Command: "writer-stop"},
	}}

	matched := HooksForPayload(cfg, Payload{
		Event:         "subagent_stop",
		AgentID:       "task-1",
		AgentType:     "reviewer",
		LastAssistant: "review done",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "reviewer-stop", matched[0].Command)

	matched = HooksForPayload(cfg, Payload{
		Event:         "subagent_stop",
		AgentID:       "task-1",
		AgentType:     "reviewer",
		LastAssistant: "still working",
	})
	require.Empty(t, matched)
}

func TestHooksForPayloadFiltersNotificationMatchersAndConditions(t *testing.T) {
	cfg := config.HookConfig{NotificationCommands: []config.HookCommand{
		{Matcher: "background_*", Type: "command", If: "background_task_started(*started*)", Command: "started"},
		{Matcher: "auth_*", Type: "command", Command: "auth"},
	}}

	matched := HooksForPayload(cfg, Payload{
		Event:            "notification",
		Message:          "background task started",
		Title:            "Background task started",
		NotificationType: "background_task_started",
	})
	require.Len(t, matched, 1)
	require.Equal(t, "started", matched[0].Command)

	matched = HooksForPayload(cfg, Payload{
		Event:            "notification",
		Message:          "background task stopped",
		NotificationType: "background_task_stopped",
	})
	require.Empty(t, matched)
}
