package acceptance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/stretchr/testify/require"
)

func TestRealBinaryDirectSlashHelp(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "/help")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Usage:")
	require.Contains(t, result.Combined(), "codog-acceptance")
	require.Contains(t, result.Combined(), "repl")
}

func TestRealBinaryHelpAfterGlobalFlagsShortCircuits(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "--model", "openai/gpt-5.5", "--help")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Stdout, "Usage:")
	require.Contains(t, result.Stdout, "repl")
	require.NotContains(t, result.Combined(), "config_load_failed")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryTUIHelpDescribesInlineDefaults(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	for _, args := range [][]string{{"tui", "--help"}, {"help", "tui"}} {
		result := runCodog(t, bin, workspace, configHome, nil, args...)

		require.Equal(t, 0, result.Code, result.Combined())
		require.Contains(t, result.Stdout, "inline Bubble Tea agent session")
		require.Contains(t, result.Stdout, "terminal scrollback")
		require.Contains(t, result.Stdout, "Enter sends the prompt")
		require.Contains(t, result.Stdout, "codog [flags]")
		require.NotContains(t, result.Stdout, "Ctrl+S")
		require.NotContains(t, result.Combined(), "missing_credentials")
	}
}

func TestRealBinaryTUIHandlesLocalSlashCommandsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	output := runExpectCodog(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52 tui
expect "codog"
send "/help\r"
expect "Common workflows"
send "\033"
expect "ready"
send "/status\r"
expect "Tools"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Common workflows")
	require.Contains(t, output, "Tools")
	require.NotContains(t, output, "\x1b[?1049h")
}

func TestRealBinaryInteractiveStartupPersistsWorkspaceTrustWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	first := runExpectCodogUntrusted(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52
expect "Accessing workspace:"
expect "No, exit"
send "\r"
expect "Let's get started."
expect "Dark mode"
send "\033\[B"
send "\r"
expect "codog"
send "/exit\r"
expect eof
`)

	require.Contains(t, first, "Accessing workspace:")
	require.NotContains(t, first, "\x1b[?1049h")
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	var persisted struct {
		TrustedRoots []string `json:"trustedRoots"`
		Theme        string   `json:"theme"`
	}
	require.NoError(t, json.Unmarshal(data, &persisted))
	trustedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Contains(t, persisted.TrustedRoots, trustedWorkspace)
	require.Equal(t, "dark", persisted.Theme)

	second := runExpectCodogUntrusted(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52
expect {
  "Accessing workspace:" { exit 1 }
  "Let's get started." { exit 1 }
  "codog" {}
  timeout { exit 1 }
}
send "/exit\r"
expect eof
`)
	require.NotContains(t, second, "Accessing workspace:")
	require.NotContains(t, second, "Let's get started.")
}

func TestRealBinaryWorkspaceTrustRejectsProjectHelperBeforeConfigLoad(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	sentinel := filepath.Join(workspace, "project-helper-ran")
	helper := filepath.Join(workspace, "project-helper.sh")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nprintf touched > \""+sentinel+"\"\nprintf 'project-api-key\\n'\n"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755))
	projectSettings, err := json.Marshal(map[string]any{"apiKeyHelper": helper})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "settings.json"), projectSettings, 0o600))

	output := runExpectCodogUntrusted(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52
expect "Accessing workspace:"
expect "Yes, I trust this folder"
send "n"
expect eof
`)

	require.Contains(t, output, "Accessing workspace:")
	_, err = os.Stat(sentinel)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(configHome, "config.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRealBinaryTUIShowsSlashSuggestionsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	output := runExpectCodog(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52 tui
expect "codog"
send "/stat\t"
expect "suggestions"
expect "/status"
expect "Show local workspace"
expect "/statusline"
send "\033"
expect eof
spawn -noecho $env(CODOG_TEST_BIN) --model glm52 tui
expect "codog"
send "/doct\t"
expect "/doctor "
send "\033"
expect eof
`)

	require.Contains(t, output, "suggestions")
	require.Contains(t, output, "/status")
	require.Contains(t, output, "Show local workspace")
	require.Contains(t, output, "/statusline")
	require.Contains(t, output, "/doctor")
}

func TestRealBinaryTUIThemePickerPreviewsAndPersistsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	output := runExpectCodog(t, bin, workspace, configHome, nil, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52
expect "codog"
send "/theme\r"
expect "Dark mode"
send "\033\[B"
expect "Light mode"
send "\r"
expect "Theme"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Dark mode")
	require.Contains(t, output, "Light mode")
	data, err := os.ReadFile(filepath.Join(configHome, "config.json"))
	require.NoError(t, err)
	var persisted struct {
		Theme string `json:"theme"`
	}
	require.NoError(t, json.Unmarshal(data, &persisted))
	require.Equal(t, "light", persisted.Theme)
}

func TestRealBinaryTUISearchesPromptHistoryWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(mockanthropic.Server{Text: "history seed answer"}.Handler())
	defer server.Close()
	extraEnv := []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}

	first := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil, "--model", "claude-sonnet-4-5", "-p", "history recall marker", "--max-turns", "2")
	require.Equal(t, 0, first.Code, first.Combined())
	sessionID := extractSessionID(t, first.Stderr)

	output := runExpectCodog(t, bin, workspace, configHome, extraEnv, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --session `+sessionID+` --model claude-sonnet-4-5 tui
expect "codog"
send "\022"
expect "history"
expect "history recall marker"
send "\r"
expect "history selected"
expect "history recall marker"
send "\033"
expect eof
`)

	require.Contains(t, output, "history recall marker")
	require.Contains(t, output, "history selected")
}

func TestRealBinaryBareResumeOpensSearchablePickerWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(mockanthropic.Server{Text: "picker seed answer"}.Handler())
	defer server.Close()
	extraEnv := []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}

	alpha := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil, "--name", "picker-alpha", "--model", "claude-sonnet-4-5", "-p", "alpha resume prompt")
	require.Equal(t, 0, alpha.Code, alpha.Combined())
	alphaID := extractSessionID(t, alpha.Stderr)
	beta := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil, "--name", "picker-beta", "--model", "claude-sonnet-4-5", "-p", "beta resume prompt")
	require.Equal(t, 0, beta.Code, beta.Combined())

	output := runExpectCodog(t, bin, workspace, configHome, extraEnv, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --resume --model claude-sonnet-4-5
expect "Resume a session"
send "picker-alpha"
expect "Filter: picker-alpha"
expect "picker-alpha"
send "\r"
expect "Session `+alphaID+`"
expect "codog"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Resume a session")
	require.Contains(t, output, "picker-alpha")
	require.Contains(t, output, alphaID)
	require.NotContains(t, output, "\x1b[?1049h")

	nonTTY := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, []byte{}, "--resume")
	require.NotEqual(t, 0, nonTTY.Code, nonTTY.Combined())
	require.Contains(t, nonTTY.Combined(), "requires an interactive terminal")
	require.Contains(t, nonTTY.Combined(), "--resume latest")
}

func TestRealBinaryTUIShowsProviderErrorsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"OPENAI_BASE_URL=" + server.URL + "/v1",
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model glm52 tui
expect "codog"
send "trigger provider error\r"
expect "provider returned an"
expect "empty error body"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "openai-compatible request failed")
	require.Contains(t, output, "provider returned an")
	require.Contains(t, output, "empty error body")
}

func TestRealBinaryTUIEscInterruptsRunningTurnWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model claude-sonnet-4-5 tui
expect "codog"
send "start a long running turn\r"
expect "running"
send "\033"
expect "Interrupted by user."
send "/status\r"
expect "Tools"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Interrupted by user.")
	require.Contains(t, output, "Tools")
}

func TestRealBinaryTUIExitCancelsRunningTurnWithoutQueueing(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model claude-sonnet-4-5 tui
expect "codog"
send "start a turn then exit\r"
expect "running"
send "/exit\r"
expect eof
`)

	require.NotContains(t, output, "Queued prompt 1: /exit")
}

func TestRealBinaryTUIRendersStreamingDeltaBeforeTurnDoneWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		writeAcceptanceStreamEvent(t, w, map[string]any{"type": "message_start"})
		writeAcceptanceStreamEvent(t, w, map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		writeAcceptanceStreamEvent(t, w, map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": "streaming early marker",
			},
		})
		<-r.Context().Done()
	}))
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --model claude-sonnet-4-5 tui
expect "codog"
send "show streaming before done\r"
expect "streaming early marker"
send "\033"
expect "Interrupted by user."
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "streaming early marker")
	require.Contains(t, output, "Interrupted by user.")
}

func TestRealBinaryTUIShowsToolResultsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{ID: "tool-write", Name: "write_file", Input: json.RawMessage(`{"path":"tui-tool.txt","content":"created by tui tool smoke\n"}`)},
				{ID: "tool-bash", Name: "bash", Input: json.RawMessage(`{"command":"printf tui-tool-visible","timeout":1000}`)},
			}},
			{Text: "tui tool final ok"},
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode allow --model claude-sonnet-4-5 tui
expect "codog"
send "exercise visible tui tools\r"
expect "Write(tui-tool.txt)"
expect "Bash(printf tui-tool-visible)"
expect "tui-tool-visible"
expect "tui tool final ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Write(tui-tool.txt)")
	require.Contains(t, output, "Bash(printf tui-tool-visible)")
	require.Contains(t, output, "tui-tool-visible")
	require.Contains(t, output, "tui tool final ok")
	created, err := os.ReadFile(filepath.Join(workspace, "tui-tool.txt"))
	require.NoError(t, err)
	require.Equal(t, "created by tui tool smoke\n", string(created))
}

func TestRealBinaryTUISubmitsCtrlJMultilinePromptWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	server := httptest.NewServer(mockanthropic.Server{
		Text: "multiline prompt ok",
		OnRequest: func(body json.RawMessage) {
			mu.Lock()
			requestBodies = append(requestBodies, string(body))
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --model claude-sonnet-4-5 tui
expect "codog"
send "first line"
send "\012"
send "second line\r"
expect "multiline prompt ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "multiline prompt ok")
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, requestBodies)
	require.Contains(t, requestBodies[0], "first line")
	require.Contains(t, requestBodies[0], "second line")
}

func TestRealBinaryReplSlashHelpAndExit(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, []byte("/help\n/exit\n"), "--model", "openai/gpt-5.5", "repl")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Type /help for commands")
	require.Contains(t, result.Combined(), "/status")
	require.Contains(t, result.Combined(), "/exit")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryReplBareHelpAndQuitAreLocal(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, []byte("help\nquit\n"), "--model", "openai/gpt-5.5", "repl")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "Type /help for commands")
	require.Contains(t, result.Combined(), "/status")
	require.Contains(t, result.Combined(), "/exit")
	require.NotContains(t, result.Combined(), "missing_credentials")
}

func TestRealBinaryConcurrentReplSessionsDoNotCollide(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	const count = 8
	results := make([]commandResult, count)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = runCodog(t, bin, workspace, configHome, []byte("/exit\n"), "--model", "openai/gpt-5.5", "repl")
		}(i)
	}
	wg.Wait()

	for _, result := range results {
		require.Equal(t, 0, result.Code, result.Combined())
		require.NotContains(t, result.Combined(), "file exists")
		require.NotContains(t, result.Combined(), "already exists")
	}
}

func TestRealBinaryCapabilitiesExposeTerminalContract(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	result := runCodog(t, bin, workspace, configHome, nil, "capabilities", "--json")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Stdout, `"terminal"`)
	require.Contains(t, result.Stdout, `"slash_command_count"`)
	require.Contains(t, result.Stdout, `"tui_submit_supported"`)
	require.Contains(t, result.Stdout, `"tui_workspace_trust_prompt"`)
}

func TestRealBinaryOpenAICompatibleErrorIncludesActionableBodyFallback(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	result := runCodogWithExtraEnv(t, bin, workspace, configHome, []string{
		"OPENAI_BASE_URL=" + server.URL + "/v1",
	}, nil, "--model", "glm52", "-p", "hello", "--max-turns", "1")

	require.NotEqual(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Combined(), "openai-compatible request failed: 400 Bad Request")
	require.Contains(t, result.Combined(), "provider returned an empty error body")
	require.Contains(t, result.Combined(), "codog models show MODEL")
}

func TestRealBinaryRunLoopExecutesWorkspaceTools(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app.txt"), []byte("alpha Needle\n"), 0o644))

	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{ID: "tool-read", Name: "read_file", Input: json.RawMessage(`{"path":"src/app.txt"}`)},
				{ID: "tool-write", Name: "write_file", Input: json.RawMessage(`{"path":"created.txt","content":"created by real binary tool smoke\n"}`)},
				{ID: "tool-edit", Name: "edit_file", Input: json.RawMessage(`{"path":"src/app.txt","old_string":"alpha","new_string":"beta"}`)},
				{ID: "tool-grep", Name: "grep", Input: json.RawMessage(`{"pattern":"Needle","path":"."}`)},
				{ID: "tool-glob", Name: "glob", Input: json.RawMessage(`{"pattern":"src/*.txt","limit":5}`)},
				{ID: "tool-bash", Name: "bash", Input: json.RawMessage(`{"command":"printf real-bash-smoke"}`)},
			}},
			{Text: "real binary tool smoke ok"},
		},
		OnRequest: func(json.RawMessage) {
			mu.Lock()
			requests++
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()

	result := runCodogWithExtraEnv(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, nil, "--permission-mode", "allow", "--model", "claude-sonnet-4-5", "-p", "exercise workspace tools", "--max-turns", "4")

	require.Equal(t, 0, result.Code, result.Combined())
	require.Contains(t, result.Stdout, "real binary tool smoke ok")
	appData, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
	require.NoError(t, err)
	require.Equal(t, "beta Needle\n", string(appData))
	created, err := os.ReadFile(filepath.Join(workspace, "created.txt"))
	require.NoError(t, err)
	require.Equal(t, "created by real binary tool smoke\n", string(created))
	mu.Lock()
	require.GreaterOrEqual(t, requests, 2)
	mu.Unlock()
}

func TestRealBinaryPermissionPromptApproveAndDeny(t *testing.T) {
	bin := buildCodogBinary(t)
	for _, tc := range []struct {
		name           string
		answer         string
		command        string
		finalText      string
		expectFile     bool
		expectStderr   string
		expectNoStderr string
	}{
		{
			name:         "approve",
			answer:       "y\n",
			command:      "printf approved > permission.txt",
			finalText:    "permission approved smoke ok",
			expectFile:   true,
			expectStderr: "Allow? [y/N/a=always for session]",
		},
		{
			name:           "deny",
			answer:         "n\n",
			command:        "printf denied > permission.txt",
			finalText:      "permission denied smoke ok",
			expectFile:     false,
			expectStderr:   "Allow? [y/N/a=always for session]",
			expectNoStderr: "approved",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			configHome := t.TempDir()
			server := httptest.NewServer(mockanthropic.Server{
				Turns: []mockanthropic.Turn{
					{ToolUses: []mockanthropic.ToolUse{{
						ID:    "tool-bash",
						Name:  "bash",
						Input: json.RawMessage(`{"command":` + strconv.Quote(tc.command) + `,"timeout":1000}`),
					}}},
					{Text: tc.finalText},
				},
			}.Handler())
			defer server.Close()

			result := runCodogWithExtraEnv(t, bin, workspace, configHome, []string{
				"ANTHROPIC_API_KEY=acceptance-anthropic-key",
				"ANTHROPIC_BASE_URL=" + server.URL,
			}, []byte(tc.answer), "--permission-mode", "workspace-write", "--model", "claude-sonnet-4-5", "-p", "permission prompt smoke", "--max-turns", "4")

			require.Equal(t, 0, result.Code, result.Combined())
			require.Contains(t, result.Stdout, tc.finalText)
			require.Contains(t, result.Stderr, tc.expectStderr)
			if tc.expectNoStderr != "" {
				require.NotContains(t, result.Stderr, tc.expectNoStderr)
			}
			_, err := os.Stat(filepath.Join(workspace, "permission.txt"))
			if tc.expectFile {
				require.NoError(t, err)
			} else {
				require.True(t, os.IsNotExist(err), "denied command should not create file: %v", err)
			}
		})
	}
}

func TestRealBinaryPermissionPromptApproveAndDenyWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	for _, tc := range []struct {
		name       string
		answer     string
		command    string
		finalText  string
		expectFile bool
	}{
		{
			name:       "approve",
			answer:     "y",
			command:    "printf approved-tty > permission.txt",
			finalText:  "permission tty approved ok",
			expectFile: true,
		},
		{
			name:       "deny",
			answer:     "n",
			command:    "printf denied-tty > permission.txt",
			finalText:  "permission tty denied ok",
			expectFile: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			configHome := t.TempDir()
			server := httptest.NewServer(mockanthropic.Server{
				Turns: []mockanthropic.Turn{
					{ToolUses: []mockanthropic.ToolUse{{
						ID:    "tool-bash",
						Name:  "bash",
						Input: json.RawMessage(`{"command":` + strconv.Quote(tc.command) + `,"timeout":1000}`),
					}}},
					{Text: tc.finalText},
				},
			}.Handler())
			defer server.Close()

			output := runExpectCodog(t, bin, workspace, configHome, []string{
				"ANTHROPIC_API_KEY=acceptance-anthropic-key",
				"ANTHROPIC_BASE_URL=" + server.URL,
			}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode workspace-write --model claude-sonnet-4-5 -p "permission tty smoke" --max-turns 4
expect -exact {Allow? [y/N/a=always for session]}
send "`+tc.answer+`\r"
expect "`+tc.finalText+`"
expect eof
`)

			require.Contains(t, output, "Allow?")
			require.Contains(t, output, tc.finalText)
			_, err := os.Stat(filepath.Join(workspace, "permission.txt"))
			if tc.expectFile {
				require.NoError(t, err)
			} else {
				require.True(t, os.IsNotExist(err), "denied command should not create file: %v", err)
			}
		})
	}
}

func TestRealBinaryTUIPermissionEventsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-bash",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"printf permission-tui > permission.txt","timeout":1000}`),
			}}},
			{Text: "permission tui approved ok"},
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode workspace-write --model claude-sonnet-4-5 tui
expect "codog"
send "permission tui smoke\r"
expect "Permission"
expect "Bash(printf permission-tui > permission.txt)"
expect "Allow bash to use danger-full-access?"
expect "don't ask again"
send "\033\[B"
send "\033\[A\r"
expect "bash approved"
expect "Exit code 0"
expect "permission tui approved ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Permission")
	require.Contains(t, output, "Bash(printf permission-tui > permission.txt)")
	require.Contains(t, output, "Allow bash to use danger-full-access?")
	require.Contains(t, output, "don't ask again")
	require.Contains(t, output, "bash approved")
	require.Contains(t, output, "Exit code 0")
	require.Contains(t, output, "permission tui approved ok")
	created, err := os.ReadFile(filepath.Join(workspace, "permission.txt"))
	require.NoError(t, err)
	require.Equal(t, "permission-tui", string(created))
}

func TestRealBinaryTUIPermissionFeedbackReachesModelWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-feedback",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"printf permission-feedback-visible","timeout":1000}`),
			}}},
			{Text: "permission feedback tui ok"},
		},
		OnRequest: func(body json.RawMessage) {
			mu.Lock()
			requestBodies = append(requestBodies, string(body))
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode workspace-write --model claude-sonnet-4-5 tui
expect "codog"
send "permission feedback smoke\r"
expect "Allow bash to use danger-full-access?"
send "\t"
expect "Add next-step guidance"
send "run focused tests next\r"
expect "permission feedback tui ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Add next-step guidance")
	require.Contains(t, output, "permission feedback tui ok")
	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	require.GreaterOrEqual(t, len(bodies), 2)
	require.Contains(t, bodies[len(bodies)-1], "permission_feedback")
	require.Contains(t, bodies[len(bodies)-1], "run focused tests next")
}

func TestRealBinaryTUIAskUserQuestionWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-question",
				Name:  "ask_user_question",
				Input: json.RawMessage(`{"question":"Pick a TUI lane","choices":["alpha","beta"],"default":"alpha"}`),
			}}},
			{Text: "question tui answered ok"},
		},
		OnRequest: func(body json.RawMessage) {
			mu.Lock()
			requestBodies = append(requestBodies, string(body))
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode allow --model claude-sonnet-4-5 tui
expect "codog"
send "question tui smoke\r"
expect "Pick a TUI lane"
expect "2. beta"
expect "Type something"
send "\033\[B\r"
expect "question tui answered ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Pick a TUI lane")
	require.Contains(t, output, "2. beta")
	require.Contains(t, output, "Type something")
	require.Contains(t, output, "question tui answered ok")
	mu.Lock()
	joinedRequests := strings.Join(requestBodies, "\n")
	mu.Unlock()
	require.Contains(t, joinedRequests, `\"answer\": \"beta\"`)
}

func TestRealBinaryTUIModernQuestionsWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:   "tool-modern-questions",
				Name: "ask_user_question",
				Input: json.RawMessage(`{"questions":[` +
					`{"question":"Pick a lane?","header":"Lane","options":[{"label":"Alpha","description":"Stable"},{"label":"Beta","description":"Fast"}]},` +
					`{"question":"Enable features?","header":"Features","options":[{"label":"Cache","description":"Reuse results"},{"label":"Trace","description":"Record spans"}],"multiSelect":true}` +
					`]}`),
			}}},
			{Text: "modern questions answered ok"},
		},
		OnRequest: func(body json.RawMessage) {
			mu.Lock()
			requestBodies = append(requestBodies, string(body))
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 30
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode allow --model claude-sonnet-4-5 tui
expect "codog"
send "modern questions smoke\r"
expect "Pick a lane?"
expect "Stable"
send "\033\[B\r"
expect "Enable features?"
expect "Select one or more"
send " "
send "\033\[B"
send " "
send "\r"
expect "Review answers"
expect "Features: Cache, Trace"
send "\r"
expect "modern questions answered ok"
send "/exit\r"
expect eof
`)

	require.Contains(t, output, "Pick a lane?")
	require.Contains(t, output, "Review answers")
	require.Contains(t, output, "modern questions answered ok")
	mu.Lock()
	joinedRequests := strings.Join(requestBodies, "\n")
	mu.Unlock()
	require.Contains(t, joinedRequests, `\"Pick a lane?\": \"Beta\"`)
	require.Contains(t, joinedRequests, `\"Enable features?\": \"Cache, Trace\"`)
}

func TestRealBinaryTUIQueuesPromptWhileBusyWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		requestCount++
		current := requestCount
		mu.Unlock()
		if current == 1 {
			time.Sleep(250 * time.Millisecond)
			writeAcceptanceTextStream(t, w, "first queued done")
			return
		}
		if current == 2 {
			writeAcceptanceTextStream(t, w, "second queued done")
			return
		}
		writeAcceptanceTextStream(t, w, "third queued done")
	}))
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode allow --model claude-sonnet-4-5 tui
expect "codog"
send "first queued prompt\r"
expect "running"
send "second queued prompt\r"
expect "Queued prompt 1"
send "third queued prompt\r"
expect "Queued prompt 2"
expect "first queued done"
expect "second queued done"
expect "third queued done"
send "\003"
expect "press ctrl+c again to exit"
send "\003"
expect {
  eof {}
  timeout { exit 1 }
}
`)

	require.Contains(t, output, "Queued prompt 1")
	require.Contains(t, output, "Queued prompt 2")
	require.Contains(t, output, "first queued done")
	require.Contains(t, output, "second queued done")
	require.Contains(t, output, "third queued done")
	mu.Lock()
	joinedRequests := strings.Join(requestBodies, "\n")
	count := requestCount
	mu.Unlock()
	require.Equal(t, 3, count)
	require.Contains(t, joinedRequests, "first queued prompt")
	require.Contains(t, joinedRequests, "second queued prompt")
	require.Contains(t, joinedRequests, "third queued prompt")
}

func TestRealBinaryTUICyclesModeBeforeTurnWithTTY(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-write",
				Name:  "write_file",
				Input: json.RawMessage(`{"path":"mode-cycle.txt","content":"mode cycle ok\n"}`),
			}}},
			{Text: "mode cycle final ok"},
		},
	}.Handler())
	defer server.Close()

	output := runExpectCodog(t, bin, workspace, configHome, []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}, `
set timeout 20
spawn -noecho $env(CODOG_TEST_BIN) --permission-mode read-only --model claude-sonnet-4-5 tui
expect {
  "codog" {}
  timeout { exit 1 }
}
send "\033\[Z"
expect {
  "default" {}
  timeout { exit 1 }
}
send "\033\[Z"
expect {
  "accept edits" {}
  timeout { exit 1 }
}
send "mode cycle smoke\r"
expect {
  "mode cycle final ok" {}
  timeout { exit 1 }
}
send "\003"
expect "press ctrl+c again to exit"
send "\003"
expect {
  eof {}
  timeout { exit 1 }
}
`)

	require.Contains(t, output, "accept edits")
	require.Contains(t, output, "mode cycle final ok")
	created, err := os.ReadFile(filepath.Join(workspace, "mode-cycle.txt"))
	require.NoError(t, err)
	require.Equal(t, "mode cycle ok\n", string(created))
}

func TestRealBinaryPromptResumeSendsPriorSessionHistory(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()
	var mu sync.Mutex
	requestBodies := []string{}
	server := httptest.NewServer(mockanthropic.Server{
		Turns: []mockanthropic.Turn{
			{Text: "first answer marker"},
			{Text: "second answer marker"},
		},
		OnRequest: func(body json.RawMessage) {
			mu.Lock()
			requestBodies = append(requestBodies, string(body))
			mu.Unlock()
		},
	}.Handler())
	defer server.Close()
	extraEnv := []string{
		"ANTHROPIC_API_KEY=acceptance-anthropic-key",
		"ANTHROPIC_BASE_URL=" + server.URL,
	}

	first := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil, "--model", "claude-sonnet-4-5", "-p", "first prompt marker", "--max-turns", "2")
	require.Equal(t, 0, first.Code, first.Combined())
	require.Contains(t, first.Stdout, "first answer marker")
	sessionID := extractSessionID(t, first.Stderr)

	second := runCodogWithExtraEnv(t, bin, workspace, configHome, extraEnv, nil, "--resume", sessionID, "--model", "claude-sonnet-4-5", "-p", "second prompt marker", "--max-turns", "2")
	require.Equal(t, 0, second.Code, second.Combined())
	require.Contains(t, second.Stdout, "second answer marker")

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(requestBodies), 2)
	resumedRequest := requestBodies[len(requestBodies)-1]
	require.Contains(t, resumedRequest, "first prompt marker")
	require.Contains(t, resumedRequest, "first answer marker")
	require.Contains(t, resumedRequest, "second prompt marker")
}

func TestRealBinaryBackgroundTasksPersistTerminalStateAfterLauncherExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background command smoke uses POSIX sh")
	}
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	for _, testCase := range []struct {
		name       string
		command    string
		wantStatus string
		wantCode   int
		wantLog    string
	}{
		{name: "success", command: "sleep 0.05; printf detached-success", wantStatus: "completed", wantCode: 0, wantLog: "detached-success"},
		{name: "failure", command: "sleep 0.05; printf detached-failure; exit 7", wantStatus: "failed", wantCode: 7, wantLog: "detached-failure"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := runCodog(t, bin, workspace, configHome, nil, "background", "run", "--output-format", "json", testCase.command)
			require.Equal(t, 0, started.Code, started.Combined())
			var startReport struct {
				TaskID string `json:"task_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(started.Stdout), &startReport), started.Combined())
			require.NotEmpty(t, startReport.TaskID)

			var statusReport struct {
				Task *struct {
					Status   string `json:"status"`
					ExitCode *int   `json:"exit_code"`
				} `json:"task"`
			}
			require.Eventually(t, func() bool {
				status := runCodog(t, bin, workspace, configHome, nil, "background", "status", startReport.TaskID, "--output-format", "json")
				if status.Code != 0 || json.Unmarshal([]byte(status.Stdout), &statusReport) != nil || statusReport.Task == nil || statusReport.Task.ExitCode == nil {
					return false
				}
				return statusReport.Task.Status == testCase.wantStatus && *statusReport.Task.ExitCode == testCase.wantCode
			}, 3*time.Second, 25*time.Millisecond)

			logs := runCodog(t, bin, workspace, configHome, nil, "background", "logs", startReport.TaskID, "--output-format", "json")
			require.Equal(t, 0, logs.Code, logs.Combined())
			require.Contains(t, logs.Stdout, testCase.wantLog)
		})
	}
}

type commandResult struct {
	Code   int
	Stdout string
	Stderr string
}

func (r commandResult) Combined() string {
	return r.Stdout + r.Stderr
}

func runCodog(t *testing.T, bin string, workspace string, configHome string, stdin []byte, args ...string) commandResult {
	t.Helper()
	return runCodogWithExtraEnv(t, bin, workspace, configHome, nil, stdin, args...)
}

func runCodogWithExtraEnv(t *testing.T, bin string, workspace string, configHome string, extraEnv []string, stdin []byte, args ...string) commandResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workspace
	cmd.Env = append(acceptanceEnv(configHome), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errorAs(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err)
		}
	}
	return commandResult{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

func runExpectCodog(t *testing.T, bin string, workspace string, configHome string, extraEnv []string, script string) string {
	t.Helper()
	_, err := config.SetFileValue(filepath.Join(configHome, "config.json"), "trustedRoots", []string{workspace})
	require.NoError(t, err)
	_, err = config.SetFileValue(filepath.Join(configHome, "config.json"), "theme", "dark")
	require.NoError(t, err)
	return runExpectCodogUntrusted(t, bin, workspace, configHome, extraEnv, script)
}

func runExpectCodogUntrusted(t *testing.T, bin string, workspace string, configHome string, extraEnv []string, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("expect-based TTY acceptance is not supported on windows")
	}
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("expect is required for TTY acceptance")
	}
	cmd := exec.Command("expect", "-c", script)
	cmd.Dir = workspace
	cmd.Env = append([]string{}, acceptanceEnv(configHome)...)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Env = append(cmd.Env, "CODOG_TEST_BIN="+bin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Run(), out.String())
	return out.String()
}

func writeAcceptanceTextStream(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("content-type", "text/event-stream")
	writeAcceptanceStreamEvent(t, w, map[string]any{"type": "message_start"})
	writeAcceptanceStreamEvent(t, w, map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
	for _, token := range strings.Split(text, " ") {
		writeAcceptanceStreamEvent(t, w, map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": token + " ",
			},
		})
	}
	writeAcceptanceStreamEvent(t, w, map[string]any{"type": "content_block_stop", "index": 0})
	writeAcceptanceStreamEvent(t, w, map[string]any{
		"type":  "message_delta",
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	writeAcceptanceStreamEvent(t, w, map[string]any{"type": "message_stop"})
}

func acceptanceEnv(configHome string) []string {
	env := os.Environ()
	env = append(env,
		"CODOG_CONFIG_HOME="+configHome,
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_AUTH_TOKEN=",
		"OPENAI_API_KEY=acceptance-openai-key",
		"XAI_API_KEY=",
		"DASHSCOPE_API_KEY=",
		"CODOG_DISABLE_UPDATE_CHECK=1",
	)
	return env
}

func buildCodogBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "codog-acceptance")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/codog")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	require.NoError(t, cmd.Run(), out.String())
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.False(t, info.IsDir())
	return root
}

func errorAs(err error, target any) bool {
	type unwrapper interface {
		Unwrap() error
	}
	for err != nil {
		switch t := target.(type) {
		case **exec.ExitError:
			if v, ok := err.(*exec.ExitError); ok {
				*t = v
				return true
			}
		}
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func extractSessionID(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "session: "); ok {
			value = strings.TrimSpace(value)
			require.NotEmpty(t, value)
			return value
		}
	}
	t.Fatalf("session id not found in stderr: %s", stderr)
	return ""
}

func writeAcceptanceStreamEvent(t *testing.T, w http.ResponseWriter, payload map[string]any) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = w.Write([]byte("event: " + payload["type"].(string) + "\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("data: " + string(data) + "\n\n"))
	require.NoError(t, err)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestAcceptanceHarnessUsesRealBinary(t *testing.T) {
	bin := buildCodogBinary(t)
	require.True(t, strings.Contains(filepath.Base(bin), "codog-acceptance"))
	require.Eventually(t, func() bool {
		info, err := os.Stat(bin)
		return err == nil && !info.IsDir() && info.Size() > 0
	}, time.Second, 10*time.Millisecond)
}
