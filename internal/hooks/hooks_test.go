package hooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestRunPayloadDisabledSkipsHookExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "marker")
	report, err := Runner{Workspace: workspace, Disabled: true}.RunPayload(context.Background(), []string{"touch " + strconv.Quote(marker)}, Payload{
		Event: "pre_tool_use",
		Tool:  "read_file",
		Input: `{"path":"README.md"}`,
	})

	require.NoError(t, err)
	require.Equal(t, "hooks", report.Kind)
	require.Equal(t, "disabled", report.Status)
	require.True(t, report.Disabled)
	require.Equal(t, 0, report.Count)
	require.Empty(t, report.Results)
	require.NoFileExists(t, marker)
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

func TestCommandHooksExposeClawCompatibleEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{`printf '%s\n%s\n%s\n%s\n%s\n' "$HOOK_EVENT" "$HOOK_TOOL_NAME" "$HOOK_TOOL_INPUT" "$HOOK_TOOL_OUTPUT" "$HOOK_TOOL_IS_ERROR"`}, Payload{
		Event:   "post_tool_use_failure",
		Tool:    "bash",
		Input:   `{"command":"rm -rf /"}`,
		Output:  "blocked",
		IsError: true,
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.Equal(t, "PostToolUseFailure\nbash\n{\"command\":\"rm -rf /\"}\nblocked\n1\n", report.Results[0].Stdout)
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

func TestToolHooksUseClaudeCompatibleStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	runner := Runner{Workspace: t.TempDir()}
	report, err := runner.RunPayload(context.Background(), []string{"cat"}, Payload{
		Event: "pre_tool_use",
		Tool:  "bash",
		Input: `{"command":"git status"}`,
	})
	require.NoError(t, err)
	var pre map[string]any
	require.NoError(t, json.Unmarshal([]byte(report.Results[0].Stdout), &pre))
	require.Equal(t, "PreToolUse", pre["hook_event_name"])
	require.Equal(t, "bash", pre["tool_name"])
	require.Equal(t, `{"command":"git status"}`, pre["tool_input_json"])
	require.Equal(t, "git status", pre["tool_input"].(map[string]any)["command"])
	require.Equal(t, false, pre["tool_result_is_error"])

	report, err = runner.RunPayload(context.Background(), []string{"cat"}, Payload{
		Event:   "post_tool_use_failure",
		Tool:    "bash",
		Input:   `{"command":"rm -rf /"}`,
		Output:  "blocked",
		IsError: true,
	})
	require.NoError(t, err)
	var failure map[string]any
	require.NoError(t, json.Unmarshal([]byte(report.Results[0].Stdout), &failure))
	require.Equal(t, "PostToolUseFailure", failure["hook_event_name"])
	require.Equal(t, "bash", failure["tool_name"])
	require.Equal(t, `{"command":"rm -rf /"}`, failure["tool_input_json"])
	require.Equal(t, "blocked", failure["tool_error"])
	require.Equal(t, true, failure["tool_result_is_error"])

	report, err = runner.RunPayload(context.Background(), []string{"cat"}, Payload{
		Event: "pre_tool_use",
		Tool:  "bash",
		Input: `not-json`,
	})
	require.NoError(t, err)
	var rawInput map[string]any
	require.NoError(t, json.Unmarshal([]byte(report.Results[0].Stdout), &rawInput))
	require.Equal(t, "not-json", rawInput["tool_input_json"])
	require.Equal(t, "not-json", rawInput["tool_input"].(map[string]any)["raw"])
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

	endReport, err := runner.SessionEndReport(context.Background(), `{"hook_event_name":"SessionEnd","session_id":"session-1","cwd":"`+filepath.ToSlash(workspace)+`"}`, "resume")
	require.NoError(t, err)
	require.Equal(t, "session_end", endReport.Event)
	data, err = os.ReadFile(endPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"hook_event_name":"SessionEnd"`)
	require.Contains(t, string(data), `"reason":"resume"`)
}

func TestSessionEndReportParsesHookFeedback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{
		Workspace: t.TempDir(),
		Config: config.HookConfig{
			SessionEndCommands: []config.HookCommand{{
				Matcher: "exit",
				Command: `printf '%s' '{"systemMessage":"session ended","hookSpecificOutput":{"additionalContext":"cleanup complete"}}'`,
			}},
		},
	}.SessionEndReport(context.Background(), `{"hook_event_name":"SessionEnd","reason":"exit","session_id":"session-1"}`, "exit")
	require.NoError(t, err)
	require.Equal(t, []string{"session ended", "cleanup complete"}, MessagesFromReport(report))
}

func TestSessionStartOutputFromReportParsesHookSpecificOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{`printf '%s' '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"repo is warm","initialUserMessage":"continue from hook","watchPaths":["/tmp/a","/tmp/b"]}}'`}, Payload{
		Event: "session_start",
		Input: `{"hook_event_name":"SessionStart","source":"startup","session_id":"session-1"}`,
	})
	require.NoError(t, err)
	output := SessionStartOutputFromReport(report)
	require.Equal(t, []string{"repo is warm"}, output.AdditionalContexts)
	require.Equal(t, []string{"continue from hook"}, output.InitialMessages)
	require.Equal(t, []string{"/tmp/a", "/tmp/b"}, output.WatchPaths)
}

func TestPreToolUseOutputFromReportParsesHookSpecificOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, output, err := Runner{Workspace: t.TempDir()}.PreToolUseReport(context.Background(), "bash", []byte(`{"command":"pwd"}`))
	require.NoError(t, err)
	require.Equal(t, "pre_tool_use", report.Event)
	require.Empty(t, output.PermissionDecision)

	report, err = Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{`printf '%s' '{"systemMessage":"updated","hookSpecificOutput":{"additionalContext":"ctx","permissionDecision":"allow","permissionDecisionReason":"hook ok","updatedInput":{"command":"git status"}}}'`}, Payload{
		Event: "pre_tool_use",
		Tool:  "bash",
		Input: `{"command":"pwd"}`,
	})
	require.NoError(t, err)
	output = PreToolUseOutputFromReport(report)
	require.Equal(t, 1, report.Count)
	require.Equal(t, []string{"updated", "ctx"}, output.Messages)
	require.Equal(t, "allow", output.PermissionDecision)
	require.Equal(t, "hook ok", output.PermissionReason)
	require.True(t, output.UpdatedInputProvided)
	require.JSONEq(t, `{"command":"git status"}`, string(output.UpdatedInput))
	require.Equal(t, []string{"updated", "ctx"}, report.Results[0].Messages)
}

func TestPreToolUseMalformedJSONReportsDiagnostic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{`printf '{not-json\nsecond line'; printf 'stderr warning' >&2; exit 1`}, Payload{
		Event: "pre_tool_use",
		Tool:  "Edit",
		Input: `{"file":"src/lib.rs"}`,
	})
	require.Error(t, err)
	output := PreToolUseOutputFromReport(report)
	require.Len(t, output.Messages, 1)
	rendered := output.Messages[0]
	require.Contains(t, rendered, "hook_invalid_json:")
	require.Contains(t, rendered, "phase=PreToolUse")
	require.Contains(t, rendered, "tool=Edit")
	require.Contains(t, rendered, "command=printf '{not-json")
	require.Contains(t, rendered, "detail=")
	require.Contains(t, rendered, `stdout_preview={not-json\nsecond line`)
	require.Contains(t, rendered, "stderr_preview=stderr warning")
	require.Equal(t, rendered, report.Results[0].Error)
	require.Equal(t, []string{rendered}, report.Results[0].Messages)
}

func TestRunPayloadParsesMessagesForNonPreToolHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{`printf '%s' '{"systemMessage":"post note","reason":"because","hookSpecificOutput":{"additionalContext":"ctx"}}'`}, Payload{
		Event:  "post_tool_use",
		Tool:   "bash",
		Input:  `{"command":"git status"}`,
		Output: "done",
	})
	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.Equal(t, []string{"post note", "because", "ctx"}, report.Results[0].Messages)
}

func TestRunPayloadStopsOnStructuredHookBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "after-block")
	report, err := Runner{Workspace: workspace}.RunPayload(context.Background(), []string{
		`printf '%s' '{"continue":false,"reason":"stop here"}'`,
		"touch " + strconv.Quote(marker),
	}, Payload{
		Event:  "post_tool_use",
		Tool:   "bash",
		Input:  `{"command":"git status"}`,
		Output: "done",
	})
	require.NoError(t, err)
	require.Equal(t, "denied", report.Status)
	require.True(t, report.Denied)
	require.Len(t, report.Results, 1)
	require.True(t, report.Results[0].Denied)
	require.Equal(t, []string{"stop here"}, report.Results[0].Messages)
	require.NoFileExists(t, marker)
}

func TestPostToolUseReturnsErrorOnStructuredHookBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	runner := Runner{
		Workspace: t.TempDir(),
		Config: config.HookConfig{
			PostToolUseCommands: []config.HookCommand{{Matcher: "bash", Command: `printf '%s' '{"decision":"block","reason":"blocked after tool"}'`}},
		},
	}
	err := runner.PostToolUse(context.Background(), "bash", []byte(`{"command":"git status"}`), "done", false)
	require.Error(t, err)
	require.ErrorContains(t, err, "PostToolUse hook denied tool bash")
	require.ErrorContains(t, err, "blocked after tool")
}

func TestRunPayloadTreatsExitCodeTwoAsHookDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	report, err := Runner{Workspace: t.TempDir()}.RunPayload(context.Background(), []string{
		`printf 'deny from exit two'; exit 2`,
	}, Payload{
		Event: "post_tool_use",
		Tool:  "bash",
	})
	require.NoError(t, err)
	require.Equal(t, "denied", report.Status)
	require.True(t, report.Denied)
	require.Len(t, report.Results, 1)
	require.False(t, report.Results[0].Success)
	require.True(t, report.Results[0].Denied)
	require.Equal(t, 2, report.Results[0].ExitCode)
	require.Equal(t, []string{"deny from exit two"}, report.Results[0].Messages)
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

func TestRunHooksBlocksHTTPPayloadOutsideAllowedURLs(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	allowed := []string{"https://allowed.example.test/*"}

	report, err := Runner{Workspace: t.TempDir(), AllowedHTTPHookURLs: &allowed}.RunHooks(context.Background(), []config.HookCommand{{
		Type: "http",
		URL:  server.URL,
	}}, Payload{Event: "post_tool_use", Tool: "bash"})

	require.Error(t, err)
	require.ErrorContains(t, err, "http hook URL is not allowed")
	require.False(t, called)
	require.Len(t, report.Results, 1)
	require.False(t, report.Results[0].Success)
	require.Equal(t, -1, report.Results[0].ExitCode)
	require.Equal(t, server.URL, report.Results[0].URL)
}

func TestRunHooksIntersectsHTTPHeaderEnvWithGlobalAllowlist(t *testing.T) {
	t.Setenv("HOOK_TOKEN", "secret-token")
	t.Setenv("SHARED_TOKEN", "shared-token")
	var gotAllowed string
	var gotShared string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAllowed = r.Header.Get("X-Allowed")
		gotShared = r.Header.Get("X-Shared")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	allowedURLs := []string{server.URL + "/*"}
	allowedEnv := []string{"HOOK_TOKEN"}

	report, err := Runner{Workspace: t.TempDir(), AllowedHTTPHookURLs: &allowedURLs, HTTPHookAllowedEnvVars: &allowedEnv}.RunHooks(context.Background(), []config.HookCommand{{
		Type:           "http",
		URL:            server.URL + "/hook",
		Headers:        map[string]string{"X-Allowed": "$HOOK_TOKEN", "X-Shared": "$SHARED_TOKEN"},
		AllowedEnvVars: []string{"HOOK_TOKEN", "SHARED_TOKEN"},
	}}, Payload{Event: "post_tool_use", Tool: "bash"})

	require.NoError(t, err)
	require.Len(t, report.Results, 1)
	require.True(t, report.Results[0].Success)
	require.Equal(t, "secret-token", gotAllowed)
	require.Empty(t, gotShared)
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
