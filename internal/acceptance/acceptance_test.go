package acceptance

import (
	"bytes"
	"encoding/json"
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

func TestRealBinaryTUIHelpDescribesFullScreenDefaults(t *testing.T) {
	bin := buildCodogBinary(t)
	workspace := t.TempDir()
	configHome := t.TempDir()

	for _, args := range [][]string{{"tui", "--help"}, {"help", "tui"}} {
		result := runCodog(t, bin, workspace, configHome, nil, args...)

		require.Equal(t, 0, result.Code, result.Combined())
		require.Contains(t, result.Stdout, "full-screen Bubble Tea agent session")
		require.Contains(t, result.Stdout, "Enter sends the prompt")
		require.Contains(t, result.Stdout, "codog [flags]")
		require.NotContains(t, result.Stdout, "Ctrl+S")
		require.NotContains(t, result.Combined(), "missing_credentials")
	}
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

func TestAcceptanceHarnessUsesRealBinary(t *testing.T) {
	bin := buildCodogBinary(t)
	require.True(t, strings.Contains(filepath.Base(bin), "codog-acceptance"))
	require.Eventually(t, func() bool {
		info, err := os.Stat(bin)
		return err == nil && !info.IsDir() && info.Size() > 0
	}, time.Second, 10*time.Millisecond)
}
