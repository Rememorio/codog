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
		StopCommands: []config.HookCommand{
			{Type: "command", Command: "stop"},
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
	require.Equal(t, []string{"stop"}, CommandsForEvent(cfg, "stop", ""))
	require.Equal(t, []string{"compact"}, CommandsForEvent(cfg, "pre-compact", ""))
	require.Equal(t, []string{"post-compact"}, CommandsForEvent(cfg, "post-compact", ""))
	require.Equal(t, []string{"notify"}, CommandsForEvent(cfg, "notification", "background_task_started"))
	require.Equal(t, []string{"agent-start"}, CommandsForEvent(cfg, "subagent-start", "reviewer"))
	require.Equal(t, []string{"agent-stop"}, CommandsForEvent(cfg, "subagent-stop", "reviewer"))
	require.Equal(t, []string{"all"}, CommandsForEvent(cfg, "pre_tool_use", "grep"))
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
