package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/autofixpr"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/mcp"
	"github.com/Rememorio/codog/internal/mcpauthdiag"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/prworkflow"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/todos"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/workerstate"
	"github.com/Rememorio/codog/internal/worktree"
	"github.com/stretchr/testify/require"
)

func TestAPICommandReportsRemoteControlRoutes(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 90,
			},
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.API([]string{"routes", "--addr", ":8799", "--json"}))
	var report apiReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "api", report.Kind)
	require.Equal(t, "routes", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.AuthRequired)
	require.Equal(t, "http://127.0.0.1:8799", report.RemoteURL)
	require.Equal(t, "http://127.0.0.1:8799/health", report.HealthURL)
	require.Equal(t, "http://127.0.0.1:8799/state", report.StateURL)
	require.Equal(t, "http://127.0.0.1:8799/routes", report.RoutesURL)
	require.Equal(t, "http://127.0.0.1:8799/capabilities", report.CapabilitiesURL)
	require.Equal(t, "codog remote serve :8799", report.RemoteCommand)
	require.Equal(t, 90, report.LeaseSeconds)
	require.Equal(t, len(control.RouteSpecs()), report.RouteCount)
	require.NotEmpty(t, report.Routes)
	require.Contains(t, out.String(), `"/file/write"`)
	require.NotContains(t, out.String(), "secret-token")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/api status --addr 127.0.0.1:8800", &session.Session{ID: "active"}))
	require.Contains(t, out.String(), "Remote API")
	require.Contains(t, out.String(), "Remote URL       http://127.0.0.1:8800")
	require.Contains(t, out.String(), "/health")
	require.Contains(t, out.String(), "/state")
	require.Contains(t, out.String(), "/routes")
	require.Contains(t, out.String(), "/capabilities")
	require.Empty(t, errOut.String())
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home": configHome,
		"remote": map[string]any{
			"enabled":       true,
			"auth_token":    "secret-token",
			"lease_seconds": 90,
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "api", "routes", "--addr", "127.0.0.1:8810"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(cliOut), &report))
	require.Equal(t, "api", report.Kind)
	require.Equal(t, "http://127.0.0.1:8810", report.RemoteURL)
	require.True(t, report.AuthRequired)
	require.NotContains(t, cliOut, "secret-token")
}

func TestAPIServeStartsControlListener(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	out := newNotifyBuffer()
	errOut := newNotifyBuffer()
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 45,
			},
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       out,
		Err:       errOut,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.APIContext(ctx, []string{"serve", "127.0.0.1:0", "--json"})
	}()
	t.Cleanup(cancel)

	select {
	case <-out.writes:
	case <-time.After(3 * time.Second):
		require.Fail(t, "api serve did not write a startup report")
	}
	var report apiReport
	require.NoError(t, json.Unmarshal([]byte(out.String()), &report))
	require.Equal(t, "api", report.Kind)
	require.Equal(t, "serve", report.Action)
	require.Equal(t, "serving", report.Status)
	require.True(t, report.Listening)
	require.True(t, report.AuthRequired)
	require.NotContains(t, out.String(), "secret-token")

	require.Eventually(t, func() bool {
		resp, err := http.Get(report.HealthURL)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 25*time.Millisecond)

	resp, err := http.Get(strings.TrimRight(report.RemoteURL, "/") + "/sessions")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(report.RemoteURL, "/")+"/sessions", nil)
	require.NoError(t, err)
	req.Header.Set("authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, errOut.String(), "codog api listening on "+report.RemoteURL)

	cancel()
	require.NoError(t, <-errCh)
}

func TestServerCommandStartsControlListener(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	out := newNotifyBuffer()
	errOut := newNotifyBuffer()
	app := &App{
		Config:   config.Config{ConfigHome: configHome},
		Sessions: session.NewWorkspaceStore(configHome, t.TempDir()),
		Out:      out,
		Err:      errOut,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Server(ctx, []string{
			"--host", "127.0.0.1",
			"--port", "0",
			"--auth-token", "server-secret-token",
			"--workspace", workspace,
			"--idle-timeout", "1200",
			"--max-sessions", "2",
			"--json",
		})
	}()
	t.Cleanup(cancel)

	select {
	case <-out.writes:
	case <-time.After(3 * time.Second):
		require.Fail(t, "server did not write a startup report")
	}
	var report serverReport
	require.NoError(t, json.Unmarshal([]byte(out.String()), &report))
	require.Equal(t, "server", report.Kind)
	require.Equal(t, "serve", report.Action)
	require.Equal(t, "serving", report.Status)
	require.Equal(t, "tcp", report.Network)
	require.Equal(t, workspace, report.Workspace)
	require.Equal(t, "server-secret-token", report.AuthToken)
	require.True(t, report.AuthTokenConfigured)
	require.Equal(t, 1200, report.IdleTimeoutMS)
	require.Equal(t, 2, report.MaxSessions)
	require.True(t, report.MaxSessionsEnforced)
	require.Equal(t, len(control.RouteSpecs()), report.RouteCount)
	require.Contains(t, report.Routes, "/health")
	require.Equal(t, strings.TrimRight(report.HTTPURL, "/")+"/health", report.HealthURL)
	require.Equal(t, strings.TrimRight(report.HTTPURL, "/")+"/routes", report.RoutesURL)
	require.Equal(t, strings.TrimRight(report.HTTPURL, "/")+"/capabilities", report.CapabilitiesURL)

	require.Eventually(t, func() bool {
		resp, err := http.Get(report.HealthURL)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 25*time.Millisecond)

	resp, err := http.Get(strings.TrimRight(report.HTTPURL, "/") + "/state")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(report.HTTPURL, "/")+"/state", nil)
	require.NoError(t, err)
	req.Header.Set("authorization", "Bearer server-secret-token")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	for index := 0; index < 2; index++ {
		req, err = http.NewRequest(http.MethodPost, strings.TrimRight(report.HTTPURL, "/")+"/sessions", nil)
		require.NoError(t, err)
		req.Header.Set("authorization", "Bearer server-secret-token")
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
	req, err = http.NewRequest(http.MethodPost, strings.TrimRight(report.HTTPURL, "/")+"/sessions", nil)
	require.NoError(t, err)
	req.Header.Set("authorization", "Bearer server-secret-token")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, errOut.String(), "codog server listening on "+report.HTTPURL)

	cancel()
	require.NoError(t, <-errCh)
}

func TestServerCommandIdleTimeoutShutsDown(t *testing.T) {
	configHome := t.TempDir()
	out := newNotifyBuffer()
	errOut := newNotifyBuffer()
	app := &App{
		Config:   config.Config{ConfigHome: configHome},
		Sessions: session.NewWorkspaceStore(configHome, t.TempDir()),
		Out:      out,
		Err:      errOut,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Server(ctx, []string{
			"--host", "127.0.0.1",
			"--port", "0",
			"--auth-token", "idle-secret-token",
			"--idle-timeout", "75",
			"--max-sessions", "0",
			"--json",
		})
	}()

	select {
	case <-out.writes:
	case <-time.After(3 * time.Second):
		require.Fail(t, "server did not write a startup report")
	}
	var report serverReport
	require.NoError(t, json.Unmarshal([]byte(out.String()), &report))
	require.Equal(t, 75, report.IdleTimeoutMS)
	require.False(t, report.MaxSessionsEnforced)
	require.Contains(t, errOut.String(), "codog server listening on "+report.HTTPURL)

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		require.Fail(t, "server did not shut down after idle timeout")
	}
}

func TestServerArgsGenerateAuthTokenAndValidateOptions(t *testing.T) {
	req, err := parseServerArgs([]string{"--json"})
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0", req.Host)
	require.Equal(t, "0", req.Port)
	require.Equal(t, 600000, req.IdleTimeoutMS)
	require.Equal(t, 32, req.MaxSessions)
	require.Equal(t, "json", req.Format)

	token, err := generateServerAuthToken()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, "sk-ant-cc-"))
	require.Greater(t, len(token), len("sk-ant-cc-"))

	_, err = parseServerArgs([]string{"--port", "bad"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "port must be an integer")

	_, err = parseServerArgs([]string{"--idle-timeout", "-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-negative integer")
}

func TestServerArgumentParserBoundaries(t *testing.T) {
	req, err := parseServerArgs([]string{
		"--host=127.0.0.1",
		"--port", "8080",
		"--auth-token= secret ",
		"--workspace", "workspace",
		"--idle-timeout=2500",
		"--max-sessions", "0",
		"--output-format=json",
	})
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", req.Host)
	require.Equal(t, "8080", req.Port)
	require.Equal(t, "secret", req.AuthToken)
	require.Equal(t, "workspace", req.Workspace)
	require.Equal(t, 2500, req.IdleTimeoutMS)
	require.Zero(t, req.MaxSessions)
	require.Equal(t, "json", req.Format)

	req, err = parseServerArgs([]string{"--unix", "codog.sock", "-o", "text"})
	require.NoError(t, err)
	require.Equal(t, "codog.sock", req.Unix)

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing host", args: []string{"--host"}},
		{name: "missing port before output", args: []string{"--port", "--json"}},
		{name: "missing auth token", args: []string{"--auth-token"}},
		{name: "missing workspace", args: []string{"--workspace"}},
		{name: "missing idle timeout", args: []string{"--idle-timeout"}},
		{name: "invalid max sessions", args: []string{"--max-sessions=bad"}},
		{name: "unknown option", args: []string{"--bogus"}},
		{name: "unexpected positional", args: []string{"extra"}},
		{name: "empty host", args: []string{"--host="}},
		{name: "empty port", args: []string{"--port="}},
		{name: "unix host conflict", args: []string{"--unix=codog.sock", "--host=localhost"}},
		{name: "unknown format", args: []string{"--output-format=yaml"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServerArgs(test.args)
			require.Error(t, err)
		})
	}
}

func TestOpenCommandConnectsAndSubmitsPrompt(t *testing.T) {
	var sawSession bool
	var sawPrompt bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer open-secret", r.Header.Get("authorization"))
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/sessions":
			require.Equal(t, http.MethodPost, r.Method)
			sawSession = true
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.NotEmpty(t, payload["cwd"])
			_, _ = w.Write([]byte(`{"session_id":"remote-session","work_dir":"/remote/work"}`))
		case "/sessions/remote-session/prompt":
			require.Equal(t, http.MethodPost, r.Method)
			sawPrompt = true
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "hello remote", payload["prompt"])
			_, _ = w.Write([]byte(`{"id":"task-1","kind":"prompt","status":"running"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{Out: &out}
	ccURL := "cc://connect?url=" + url.QueryEscape(server.URL) + "&authToken=open-secret"
	require.NoError(t, app.Open(context.Background(), []string{ccURL, "-p", "hello remote", "--json"}))
	require.True(t, sawSession)
	require.True(t, sawPrompt)
	require.NotContains(t, out.String(), "open-secret")
	var report openReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "open", report.Kind)
	require.Equal(t, "connected", report.Status)
	require.Equal(t, server.URL, report.ServerURL)
	require.Equal(t, "remote-session", report.SessionID)
	require.Equal(t, "/remote/work", report.WorkDir)
	require.True(t, report.AuthTokenConfigured)
	require.True(t, report.Print)
	require.True(t, report.PromptSubmitted)
	require.Equal(t, "task-1", report.PromptTask["id"])
}

func TestRunCLIOpenHonorsGlobalOutputFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/sessions", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ID":"cli-session"}`))
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "open", server.URL}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "open"`)
	require.Contains(t, out, `"session_id": "cli-session"`)
}

func TestRunCLIDirectConnectURLRoutesToOpen(t *testing.T) {
	prompts := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer open-secret", r.Header.Get("authorization"))
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/sessions":
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"session_id":"cli-direct","work_dir":"/remote/work"}`))
		case "/sessions/cli-direct/prompt":
			require.Equal(t, http.MethodPost, r.Method)
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			prompts = append(prompts, payload["prompt"])
			_, _ = w.Write([]byte(`{"id":"task-direct","kind":"prompt","status":"running"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	ccURL := "cc://connect?url=" + url.QueryEscape(server.URL) + "&authToken=open-secret"

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", ccURL, "-p", "direct prompt"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "open"`)
	require.Contains(t, out, `"session_id": "cli-direct"`)
	require.Contains(t, out, `"prompt_submitted": true`)
	require.NotContains(t, out, "open-secret")

	out, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "-p", "prefix prompt", ccURL, "--json", "--dangerously-skip-permissions"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "open"`)
	require.Contains(t, out, `"session_id": "cli-direct"`)
	require.Contains(t, out, `"prompt_submitted": true`)
	require.NotContains(t, out, "open-secret")
	require.Equal(t, []string{"direct prompt", "prefix prompt"}, prompts)
}

func TestSSHCommandReportsPlan(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			APIKey:    "secret-api-key",
			AuthToken: "secret-auth-token",
			BaseURL:   "https://api.example.test",
			Model:     "claude-test",
		},
		Out:        &out,
		Executable: "codog",
	}
	require.NoError(t, app.SSH(context.Background(), []string{
		"devbox",
		"/workspace/repo dir",
		"-c",
		"--resume=remote-session",
		"--model", "claude-opus",
		"--permission-mode", "read-only",
		"--plan-mode-required",
		"--dangerously-skip-permissions",
		"--json",
	}))

	var report sshReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ssh", report.Kind)
	require.Equal(t, "connect", report.Action)
	require.Equal(t, "planned", report.Status)
	require.Equal(t, "devbox", report.Host)
	require.Equal(t, "/workspace/repo dir", report.Directory)
	require.False(t, report.Local)
	require.False(t, report.Executed)
	require.Nil(t, report.ExitCode)
	require.Contains(t, report.Message, "Pass --execute")
	require.Equal(t, []string{"--continue", "--resume", "remote-session", "--model", "claude-opus"}, report.ExtraArgs)
	require.Equal(t, "read-only", report.PermissionMode)
	require.True(t, report.PlanModeRequired)
	require.True(t, report.DangerouslySkipPermissions)
	require.True(t, report.RemoteAuthForwarded)
	require.Contains(t, report.RemoteEnvKeys, "ANTHROPIC_API_KEY")
	require.Contains(t, report.RemoteEnvKeys, "ANTHROPIC_AUTH_TOKEN")
	require.Contains(t, report.RemoteEnvKeys, "CODOG_BASE_URL")
	require.Contains(t, report.RemoteShell, "env")
	require.Contains(t, report.RemoteShell, "ANTHROPIC_API_KEY='[redacted]'")
	require.Contains(t, report.RemoteShell, "ANTHROPIC_AUTH_TOKEN='[redacted]'")
	require.Contains(t, report.RemoteShell, "CODOG_BASE_URL='https://api.example.test'")
	require.Equal(t, ".cache/codog/remote/devbox/codog", report.RemoteExecutable)
	require.Equal(t, []string{"ssh", "devbox", "mkdir -p '.cache/codog/remote/devbox' && cat > '.cache/codog/remote/devbox/codog' && chmod 700 '.cache/codog/remote/devbox/codog'"}, report.DeployCommand)
	require.Contains(t, report.RemoteShell, ".cache/codog/remote/devbox/codog --continue --resume remote-session --model claude-opus --permission-mode read-only --plan-mode-required --dangerously-skip-permissions repl")
	require.NotContains(t, report.RemoteShell, "secret-api-key")
	require.NotContains(t, report.RemoteShell, "secret-auth-token")
	require.NotContains(t, out.String(), "secret-api-key")
	require.NotContains(t, out.String(), "secret-auth-token")
	require.Equal(t, []string{"ssh", "devbox", report.RemoteShell}, report.Command)
}

func TestSSHCommandPrintPlanUsesPromptCommand(t *testing.T) {
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			APIKey: "secret-api-key",
			Model:  "claude-test",
		},
		Out:        &out,
		Executable: "codog",
	}
	require.NoError(t, app.SSH(context.Background(), []string{
		"devbox",
		"/workspace/repo",
		"--print",
		"summarize this repo",
		"--permission-mode",
		"read-only",
		"--json",
	}))

	var report sshReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ssh", report.Kind)
	require.Equal(t, "planned", report.Status)
	require.True(t, report.Print)
	require.True(t, report.PromptConfigured)
	require.Contains(t, report.RemoteShell, ".cache/codog/remote/devbox/codog --permission-mode read-only prompt 'summarize this repo'")
	require.NotContains(t, report.RemoteShell, " repl")
	require.NotContains(t, out.String(), "secret-api-key")
}

func TestSSHCommandJSONExecuteRunsLocalChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script child process test is POSIX-specific")
	}
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-child")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'cwd=%s\\n' \"$PWD\"\nprintf 'args=%s\\n' \"$*\"\n"), 0o755))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &errOut,
		In:         strings.NewReader(""),
		Executable: script,
	}
	require.NoError(t, app.SSH(context.Background(), []string{
		"--local",
		"localhost",
		workspace,
		"--resume",
		"latest",
		"--permission-mode",
		"read-only",
		"--json",
		"--execute",
	}))

	var report sshReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "ssh", report.Kind)
	require.Equal(t, "connect", report.Action)
	require.Equal(t, "completed", report.Status)
	require.True(t, report.Local)
	require.True(t, report.Executed)
	require.NotNil(t, report.ExitCode)
	require.Equal(t, 0, *report.ExitCode)
	require.GreaterOrEqual(t, report.DurationMS, int64(0))
	require.Contains(t, report.Stdout, "cwd="+workspace)
	require.Contains(t, report.Stdout, "args=--resume latest --permission-mode read-only repl")
	require.Empty(t, report.Stderr)
	require.Empty(t, report.Error)
	require.Empty(t, errOut.String())
}

func TestSSHCommandJSONExecuteRunsLocalPrintChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script child process test is POSIX-specific")
	}
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-child")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'cwd=%s\\n' \"$PWD\"\nprintf 'args=%s\\n' \"$*\"\n"), 0o755))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &errOut,
		In:         strings.NewReader(""),
		Executable: script,
	}
	require.NoError(t, app.SSH(context.Background(), []string{
		"--local",
		"localhost",
		workspace,
		"--print=explain status",
		"--permission-mode",
		"read-only",
		"--json",
		"--execute",
	}))

	var report sshReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "completed", report.Status)
	require.True(t, report.Local)
	require.True(t, report.Print)
	require.True(t, report.PromptConfigured)
	require.Contains(t, report.Stdout, "cwd="+workspace)
	require.Contains(t, report.Stdout, "args=--permission-mode read-only prompt explain status")
	require.Empty(t, report.Stderr)
	require.Empty(t, report.Error)
	require.Empty(t, errOut.String())
}

func TestSSHCommandDeploysBinaryBeforeRemoteRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	countPath := filepath.Join(t.TempDir(), "ssh.count")
	sshPath := filepath.Join(fakeBin, "ssh")
	script := `#!/bin/sh
count=0
if [ -f "$SSH_COUNT" ]; then
  count=$(cat "$SSH_COUNT")
fi
count=$((count + 1))
printf '%s' "$count" > "$SSH_COUNT"
printf 'call%s:%s\n' "$count" "$*" >> "$SSH_LOG"
bytes=$(wc -c | tr -d ' ')
printf 'stdin%s:%s\n' "$count" "$bytes" >> "$SSH_LOG"
if [ "$count" -eq 2 ]; then
  printf 'run:%s\n' "$*"
fi
`
	require.NoError(t, os.WriteFile(sshPath, []byte(script), 0o755))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_LOG", logPath)
	t.Setenv("SSH_COUNT", countPath)

	localBinary := filepath.Join(t.TempDir(), "codog-local")
	require.NoError(t, os.WriteFile(localBinary, []byte("binary-payload"), 0o755))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:     config.Config{APIKey: "deploy-api-key", Model: "claude-test"},
		Out:        &out,
		Err:        &errOut,
		In:         strings.NewReader(""),
		Executable: localBinary,
	}
	require.NoError(t, app.SSH(context.Background(), []string{"deploy-box", "/repo", "--model=claude-remote", "--permission-mode", "read-only"}))

	logData, err := os.ReadFile(logPath)
	require.NoError(t, err)
	logText := string(logData)
	require.Contains(t, logText, "call1:deploy-box mkdir -p '.cache/codog/remote/deploy-box'")
	require.Contains(t, logText, "cat > '.cache/codog/remote/deploy-box/codog'")
	require.Contains(t, logText, "stdin1:14")
	require.Contains(t, logText, "call2:deploy-box cd '/repo' && env")
	require.Contains(t, logText, "ANTHROPIC_API_KEY='deploy-api-key'")
	require.Contains(t, logText, ".cache/codog/remote/deploy-box/codog --model claude-remote --permission-mode read-only repl")
	require.Contains(t, out.String(), "run:deploy-box cd '/repo' && env")
	require.Empty(t, errOut.String())
}

func TestSSHCommandLocalExecutesChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script child process test is POSIX-specific")
	}
	workspace := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-child")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'cwd=%s\\n' \"$PWD\"\nprintf 'args=%s\\n' \"$*\"\n"), 0o755))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Out:        &out,
		Err:        &errOut,
		In:         strings.NewReader(""),
		Executable: script,
	}
	require.NoError(t, app.SSH(context.Background(), []string{"--local", "localhost", workspace, "--resume", "latest", "--permission-mode", "read-only"}))
	require.Contains(t, out.String(), "cwd="+workspace)
	require.Contains(t, out.String(), "args=--resume latest --permission-mode read-only repl")
	require.Empty(t, errOut.String())
}

func TestSSHCommandAcceptsPrintModeWithoutPrompt(t *testing.T) {
	req, err := parseSSHArgs([]string{"devbox", "-p"})
	require.NoError(t, err)
	require.True(t, req.Print)
	require.Empty(t, req.Prompt)

	req, err = parseSSHArgs([]string{"devbox", "--print"})
	require.NoError(t, err)
	require.True(t, req.Print)
	require.Empty(t, req.Prompt)
}

func TestParseFlagsContinueAliasesResumeLatest(t *testing.T) {
	overrides, command, rest, err := parseFlags([]string{"--continue", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "latest", overrides.Resume)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"-c", "--resume", "session-id", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "session-id", overrides.Resume)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"-r", "short-session", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "short-session", overrides.Resume)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume", "source", "--fork-session", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "source", overrides.Resume)
	require.True(t, overrides.ForkSession)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--continue", "--fork-session", "--session-id", "custom-fork", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "latest", overrides.Resume)
	require.Equal(t, "custom-fork", overrides.SessionID)
	require.True(t, overrides.ForkSession)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--resume", "source", "--resume-session-at", "msg-1", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "source", overrides.Resume)
	require.Equal(t, "msg-1", overrides.ResumeSessionAt)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--model", "sonnet", "--fallback-model", "opus", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "sonnet", overrides.Model)
	require.Equal(t, "opus", overrides.FallbackModel)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--thinking", "disabled", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "disabled", overrides.Thinking)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--from-pr", "42", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "42", overrides.FromPR)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--from-pr", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "true", overrides.FromPR)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--from-pr=https://github.com/acme/widgets/pull/42", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "https://github.com/acme/widgets/pull/42", overrides.FromPR)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--from-pr", "acme/widgets#42", "--fork-session", "--session-id", "forked-pr", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "acme/widgets#42", overrides.FromPR)
	require.Equal(t, "forked-pr", overrides.SessionID)
	require.True(t, overrides.ForkSession)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--add-dir", "../shared", "../common", "prompt", "hello"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"../shared", "../common"}, overrides.AdditionalDirs)
	require.Equal(t, "prompt", command)
	require.Equal(t, []string{"hello"}, rest)

	overrides, command, rest, err = parseFlags([]string{"--add-dir=../shared,../common", "--add-dir", "../more", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, []string{"../shared", "../common", "../more"}, overrides.AdditionalDirs)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--prefill", "review this diff", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "review this diff", overrides.Prefill)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--prefill=review this diff", "tui"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.Equal(t, "review this diff", overrides.Prefill)
	require.Equal(t, "tui", command)
	require.Empty(t, rest)

	overrides, command, rest, err = parseFlags([]string{"--deep-link-origin", "--deep-link-repo", "Rememorio/codog", "--deep-link-last-fetch", "1700000000000", "--prefill", "review this diff", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.DeepLinkOrigin)
	require.Equal(t, "Rememorio/codog", overrides.DeepLinkRepo)
	require.Equal(t, int64(1700000000000), overrides.DeepLinkLastFetchMS)
	require.Equal(t, "review this diff", overrides.Prefill)
	require.Equal(t, "repl", command)
	require.Empty(t, rest)

	overrides, _, _, err = parseFlags([]string{"--deep-link-origin", "--deep-link-last-fetch", "not-a-number", "repl"}, config.FlagOverrides{})
	require.NoError(t, err)
	require.True(t, overrides.DeepLinkOrigin)
	require.Zero(t, overrides.DeepLinkLastFetchMS)

	_, _, _, err = parseFlags([]string{"--fork-session", "repl"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--fork-session requires")

	_, _, _, err = parseFlags([]string{"--resume", "source", "--session-id", "custom", "repl"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--session-id can only be used")

	_, _, _, err = parseFlags([]string{"--resume-session-at", "msg-1", "prompt", "hello"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--resume-session-at requires")

	_, _, _, err = parseFlags([]string{"--from-pr", "42", "--resume", "source", "repl"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be combined")

	_, _, _, err = parseFlags([]string{"--from-pr", "42", "--session-id", "custom", "repl"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--session-id can only be used")

	_, _, _, err = parseFlags([]string{"--resume", "source", "--resume-session-at", "msg-1", "repl"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "prompt mode")

	_, _, _, err = parseFlags([]string{"--prefill", "queued prompt", "prompt", "hello"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "interactive")

	_, _, _, err = parseFlags([]string{"--prefill", "queued prompt", "-p", "hello"}, config.FlagOverrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "interactive")
}

func TestParsePullRequestReference(t *testing.T) {
	cases := []struct {
		input  string
		repo   string
		number int
		url    string
	}{
		{input: "42", number: 42},
		{input: "#42", number: 42},
		{input: "acme/widgets#42", repo: "acme/widgets", number: 42},
		{input: "acme/widgets/pull/42", repo: "acme/widgets", number: 42},
		{input: "github.com/acme/widgets/pull/42", repo: "acme/widgets", number: 42, url: "https://github.com/acme/widgets/pull/42"},
		{input: "https://github.com/acme/widgets/pull/42", repo: "acme/widgets", number: 42, url: "https://github.com/acme/widgets/pull/42"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			ref, err := parsePullRequestReference(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.repo, ref.Repo)
			require.Equal(t, tc.number, ref.Number)
			require.Equal(t, tc.url, ref.URL)
		})
	}

	_, err := parsePullRequestReference("not-a-pr")
	require.Error(t, err)
}

func TestBookmarksCommandLinksPullRequest(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello")))

	var out bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
	}
	require.NoError(t, app.Bookmarks([]string{"add", "fix login", "--session", "source", "--pr", "https://github.com/acme/widgets/pull/42", "--json"}, config.FlagOverrides{}))

	var report bookmarksReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.NotNil(t, report.Bookmark)
	require.Equal(t, "acme/widgets", report.Bookmark.PRRepo)
	require.Equal(t, 42, report.Bookmark.PRNumber)
	require.Equal(t, "https://github.com/acme/widgets/pull/42", report.Bookmark.PRURL)

	listed, err := bookmarks.NewStore(configHome).List(bookmarks.ListOptions{Workspace: workspace})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "source", listed[0].SessionID)
	require.Equal(t, 42, listed[0].PRNumber)
}

func TestBuildDeepLinkBanner(t *testing.T) {
	now := time.UnixMilli(1700864000000)

	banner := buildDeepLinkBanner("/repo/codog", config.FlagOverrides{Prefill: "review this diff"}, now)
	require.Equal(t, "Warning: launched with a pre-filled prompt - review it before pressing Enter.", banner)

	banner = buildDeepLinkBanner("/repo/codog", config.FlagOverrides{
		DeepLinkOrigin:      true,
		DeepLinkRepo:        "Rememorio/codog",
		DeepLinkLastFetchMS: now.Add(-48 * time.Hour).UnixMilli(),
		Prefill:             strings.Repeat("x", 1001),
	}, now)
	require.Contains(t, banner, "external deep link in /repo/codog")
	require.Contains(t, banner, "Resolved Rememorio/codog from local clones; last fetched 2d ago")
	require.NotContains(t, banner, "project instructions may be stale")
	require.Contains(t, banner, "1001 chars")

	banner = buildDeepLinkBanner("/repo/codog", config.FlagOverrides{
		DeepLinkOrigin: true,
		DeepLinkRepo:   "Rememorio/codog",
	}, now)
	require.Contains(t, banner, "last fetched never - project instructions may be stale")
}

func TestOpenSessionForksResumedSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "original prompt")))
	app := &App{Sessions: store, Workspace: t.TempDir()}

	forked, err := app.openSession(config.FlagOverrides{Resume: "source", ForkSession: true})
	require.NoError(t, err)
	require.NotEqual(t, "source", forked.ID)
	require.Len(t, forked.Messages, 1)
	require.Equal(t, "original prompt", forked.Messages[0].Content[0].Text)
	require.Equal(t, "source", forked.Metadata.ParentSessionID)

	source, err := store.OpenExisting("source")
	require.NoError(t, err)
	require.Len(t, source.Messages, 1)

	custom, err := app.openSession(config.FlagOverrides{Resume: "source", ForkSession: true, SessionID: "custom-fork"})
	require.NoError(t, err)
	require.Equal(t, "custom-fork", custom.ID)
	require.Len(t, custom.Messages, 1)
	require.Equal(t, "source", custom.Metadata.ParentSessionID)

	_, err = app.openSession(config.FlagOverrides{ForkSession: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--fork-session requires")
}

func TestOpenSessionResolvesFromPRBookmark(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "from pr")))
	require.NoError(t, store.Append("other", anthropic.TextMessage("user", "other pr")))
	require.NoError(t, store.Append("newest", anthropic.TextMessage("user", "newest pr")))

	bookmarkStore := bookmarks.Store{
		ConfigHome: configHome,
		Now: func() time.Time {
			return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
		},
		NewID: func() (string, error) { return "bm-source", nil },
	}
	_, err := bookmarkStore.Add(bookmarks.Bookmark{
		Name:      "source-pr",
		Workspace: workspace,
		SessionID: "source",
		PRRepo:    "acme/widgets",
		PRNumber:  42,
		PRURL:     "https://github.com/acme/widgets/pull/42",
	})
	require.NoError(t, err)
	bookmarkStore.Now = func() time.Time {
		return time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)
	}
	bookmarkStore.NewID = func() (string, error) { return "bm-other", nil }
	_, err = bookmarkStore.Add(bookmarks.Bookmark{
		Name:      "other-pr",
		Workspace: workspace,
		SessionID: "other",
		PRRepo:    "other/repo",
		PRNumber:  42,
	})
	require.NoError(t, err)
	bookmarkStore.Now = func() time.Time {
		return time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)
	}
	bookmarkStore.NewID = func() (string, error) { return "bm-newest", nil }
	_, err = bookmarkStore.Add(bookmarks.Bookmark{
		Name:      "newest-pr",
		Workspace: workspace,
		SessionID: "newest",
		PRRepo:    "acme/newest",
		PRNumber:  77,
	})
	require.NoError(t, err)

	app := &App{
		Config:    config.Config{ConfigHome: configHome},
		Sessions:  store,
		Workspace: workspace,
	}
	resumed, err := app.openSession(config.FlagOverrides{FromPR: "https://github.com/acme/widgets/pull/42"})
	require.NoError(t, err)
	require.Equal(t, "source", resumed.ID)
	require.Equal(t, "from pr", resumed.Messages[0].Content[0].Text)

	bare, err := app.openSession(config.FlagOverrides{FromPR: "true"})
	require.NoError(t, err)
	require.Equal(t, "newest", bare.ID)

	forked, err := app.openSession(config.FlagOverrides{FromPR: "acme/widgets#42", ForkSession: true, SessionID: "forked-pr"})
	require.NoError(t, err)
	require.Equal(t, "forked-pr", forked.ID)
	require.Equal(t, "source", forked.Metadata.ParentSessionID)

	_, err = app.openSession(config.FlagOverrides{FromPR: "42"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "matches multiple bookmarks")

	_, err = app.openSession(config.FlagOverrides{FromPR: "acme/widgets#999"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no session bookmark linked")

	_, err = app.openSession(config.FlagOverrides{FromPR: "42", Resume: "source"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be combined")
}

func TestOpenSessionResumesAtAssistantMessageID(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.Message{
		ID:   "msg-first",
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type: "text",
			Text: "first answer",
		}},
	}))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "second prompt")))
	require.NoError(t, store.Append("source", anthropic.Message{
		ID:   "msg-second",
		Role: "assistant",
		Content: []anthropic.ContentBlock{{
			Type: "text",
			Text: "second answer",
		}},
	}))
	app := &App{Sessions: store, Workspace: t.TempDir()}

	resumed, err := app.openSession(config.FlagOverrides{Resume: "source", ResumeSessionAt: "msg-first"})
	require.NoError(t, err)
	require.Equal(t, "source", resumed.ID)
	require.Len(t, resumed.Messages, 2)
	require.Equal(t, "msg-first", resumed.Messages[1].ID)

	persisted, err := store.OpenExisting("source")
	require.NoError(t, err)
	require.Len(t, persisted.Messages, 2)
	require.Equal(t, "msg-first", persisted.Messages[1].ID)
}

func TestOpenSessionForkResumesAtAssistantMessageID(t *testing.T) {
	store := session.NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.Message{
		ID:      "msg-first",
		Role:    "assistant",
		Content: []anthropic.ContentBlock{{Type: "text", Text: "first answer"}},
	}))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "second prompt")))
	require.NoError(t, store.Append("source", anthropic.Message{
		ID:      "msg-second",
		Role:    "assistant",
		Content: []anthropic.ContentBlock{{Type: "text", Text: "second answer"}},
	}))
	app := &App{Sessions: store, Workspace: t.TempDir()}

	forked, err := app.openSession(config.FlagOverrides{Resume: "source", ForkSession: true, SessionID: "forked", ResumeSessionAt: "msg-first"})
	require.NoError(t, err)
	require.Equal(t, "forked", forked.ID)
	require.Equal(t, "source", forked.Metadata.ParentSessionID)
	require.Len(t, forked.Messages, 2)
	require.Equal(t, "msg-first", forked.Messages[1].ID)

	source, err := store.OpenExisting("source")
	require.NoError(t, err)
	require.Len(t, source.Messages, 4)

	_, err = app.openSession(config.FlagOverrides{Resume: "source", ResumeSessionAt: "missing"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestOpenTargetParsing(t *testing.T) {
	target, err := parseOpenTarget("cc://127.0.0.1:9876?authToken=secret")
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:9876", target.ServerURL)
	require.Equal(t, "secret", target.AuthToken)

	target, err = parseOpenTarget("https://example.test/base?token=secret&keep=1")
	require.NoError(t, err)
	require.Equal(t, "https://example.test/base?keep=1", target.ServerURL)
	require.Equal(t, "secret", target.AuthToken)

	target, err = parseOpenTarget("cc+unix:///tmp/codog.sock?token=secret")
	require.NoError(t, err)
	require.Equal(t, "unix:/tmp/codog.sock", target.ServerURL)
	require.Equal(t, "/tmp/codog.sock", target.UnixPath)
	require.Equal(t, "secret", target.AuthToken)
}

func TestRunCLIRoutesWebSetupAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "web-setup", "status", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, out, `"kind": "remote_setup"`)
	require.Contains(t, out, `"remote_url": "http://127.0.0.1:8791"`)
}

func TestRunCLIRoutesRemoteControlAlias(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "remote-control", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report ideReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Equal(t, "ide", report.Kind)
	require.Equal(t, "status", report.Action)
	expectedWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	actualWorkspace, err := filepath.EvalSymlinks(report.Workspace)
	require.NoError(t, err)
	require.Equal(t, expectedWorkspace, actualWorkspace)
}

func TestDesktopAndMobileHandoffCommands(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("handoff-session", anthropic.TextMessage("user", "hello handoff")))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Future: config.FutureConfig{
				RemoteEnabled:      true,
				RemoteAuthToken:    "secret-token",
				RemoteLeaseSeconds: 90,
				EditorBridgeSocket: "codog.sock",
				EditorBridgeToken:  "bridge-secret",
			},
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Desktop([]string{"--session", "handoff-session", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "desktop_handoff"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"command": "codog bridge serve"`)
	require.Contains(t, out.String(), `"token_configured": true`)
	var desktopReport desktopHandoffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &desktopReport))
	require.NotEmpty(t, desktopReport.HandoffID)
	require.FileExists(t, desktopReport.ManifestPath)
	require.Contains(t, desktopReport.DeepLink, "codog://handoff/desktop")
	desktopManifestData, err := os.ReadFile(desktopReport.ManifestPath)
	require.NoError(t, err)
	var desktopManifest handoffManifest
	require.NoError(t, json.Unmarshal(desktopManifestData, &desktopManifest))
	require.Equal(t, desktopReport.HandoffID, desktopManifest.ID)
	require.Equal(t, "desktop", desktopManifest.Surface)
	require.Equal(t, "codog bridge serve", desktopManifest.Command)
	out.Reset()

	require.NoError(t, app.Desktop([]string{"status", "--json"}, config.FlagOverrides{}))
	var desktopStatus handoffStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &desktopStatus))
	require.Equal(t, "handoff_status", desktopStatus.Kind)
	require.Equal(t, 1, desktopStatus.Count)
	require.Equal(t, desktopReport.HandoffID, desktopStatus.Manifests[0].ID)
	out.Reset()

	require.NoError(t, app.Desktop([]string{"clear", "--json"}, config.FlagOverrides{}))
	var desktopClear handoffStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &desktopClear))
	require.Equal(t, 1, desktopClear.Removed)
	_, err = os.Stat(desktopReport.ManifestPath)
	require.True(t, os.IsNotExist(err))
	out.Reset()

	require.NoError(t, app.Mobile([]string{"ios", "--addr", ":8799", "--resume", "latest", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "mobile_handoff"`)
	require.Contains(t, out.String(), `"platform": "ios"`)
	require.Contains(t, out.String(), `"session_id": "handoff-session"`)
	require.Contains(t, out.String(), `"remote_url": "http://127.0.0.1:8799"`)
	require.Contains(t, out.String(), `"auth_token_configured": true`)
	require.NotContains(t, out.String(), "secret-token")
	var mobileReport mobileHandoffReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &mobileReport))
	require.NotEmpty(t, mobileReport.HandoffID)
	require.FileExists(t, mobileReport.ManifestPath)
	require.NotNil(t, mobileReport.ExpiresAt)
	require.Contains(t, mobileReport.DeepLink, "codog://handoff/mobile")
	mobileManifestData, err := os.ReadFile(mobileReport.ManifestPath)
	require.NoError(t, err)
	var mobileManifest handoffManifest
	require.NoError(t, json.Unmarshal(mobileManifestData, &mobileManifest))
	require.Equal(t, mobileReport.HandoffID, mobileManifest.ID)
	require.Equal(t, "mobile", mobileManifest.Surface)
	require.Equal(t, "ios", mobileManifest.Platform)
	require.Equal(t, "http://127.0.0.1:8799", mobileManifest.RemoteURL)
	require.NotContains(t, string(mobileManifestData), "secret-token")
	out.Reset()

	require.NoError(t, app.Mobile([]string{"status", "ios", "--json"}, config.FlagOverrides{}))
	var mobileStatus handoffStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &mobileStatus))
	require.Equal(t, "handoff_status", mobileStatus.Kind)
	require.Equal(t, "ios", mobileStatus.Platform)
	require.Equal(t, 1, mobileStatus.Count)
	require.Equal(t, mobileReport.HandoffID, mobileStatus.Manifests[0].ID)
	out.Reset()

	require.NoError(t, app.Mobile([]string{"clear", "ios", "--json"}, config.FlagOverrides{}))
	var mobileClear handoffStatusReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &mobileClear))
	require.Equal(t, 1, mobileClear.Removed)
	_, err = os.Stat(mobileReport.ManifestPath)
	require.True(t, os.IsNotExist(err))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mobile android --addr 127.0.0.1:9999", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Mobile Handoff")
	require.Contains(t, out.String(), "android")
	require.Contains(t, out.String(), "active-session")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/desktop", &session.Session{ID: "active-session"}))
	require.Contains(t, out.String(), "Desktop Handoff")
	require.Contains(t, out.String(), "codog bridge serve")
	require.Empty(t, errOut.String())
}

func TestPromptHistoryPreferenceSkipsInputRecords(t *testing.T) {
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	disabled := false
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
			Privacy:             config.PrivacyConfig{PromptHistoryEnabled: &disabled},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "private prompt", config.FlagOverrides{SessionID: "private-session"}))
	history, err := app.Sessions.PromptHistory("private-session")
	require.NoError(t, err)
	require.Empty(t, history)
	require.Contains(t, out.String(), "done")
}

func TestInstructionsLoadedHookRunsBeforePrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	server := httptest.NewServer(mockanthropic.Server{Text: "done"}.Handler())
	defer server.Close()
	configHome := t.TempDir()
	workspace := t.TempDir()
	instructionsPath := filepath.Join(workspace, "AGENTS.md")
	hookPath := filepath.Join(workspace, "instructions-loaded.json")
	require.NoError(t, os.WriteFile(instructionsPath, []byte("Project instructions.\n"), 0o644))
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:          configHome,
			Model:               "mock",
			BaseURL:             server.URL,
			APIKey:              "test-key",
			MaxTokens:           100,
			MaxTurns:            1,
			AutoCompactMessages: 40,
			PermissionMode:      "workspace-write",
			MCPServers:          map[string]config.MCPServerConfig{},
			Hooks: config.HookConfig{
				InstructionsLoadedCommands: []config.HookCommand{{Matcher: "session_start", Command: "cat > " + shellQuote(hookPath) + "; printf '%s' " + shellQuote(`{"systemMessage":"instructions hook note","hookSpecificOutput":{"additionalContext":"memory hook context"}}`)}},
			},
		},
		Client:    anthropic.New(server.URL, "test-key", ""),
		Tools:     tools.NewRegistry(workspace),
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Prompt(context.Background(), "hello", config.FlagOverrides{SessionID: "instructions-session"}))
	data, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	var payload struct {
		Event      string `json:"event"`
		Tool       string `json:"tool"`
		FilePath   string `json:"file_path"`
		MemoryType string `json:"memory_type"`
		LoadReason string `json:"load_reason"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "instructions_loaded", payload.Event)
	require.Equal(t, "session_start", payload.Tool)
	expectedPath, err := filepath.EvalSymlinks(instructionsPath)
	require.NoError(t, err)
	require.Equal(t, expectedPath, payload.FilePath)
	require.Equal(t, "Project", payload.MemoryType)
	require.Equal(t, "session_start", payload.LoadReason)
	require.Contains(t, out.String(), "done")
	loaded, err := app.Sessions.OpenExisting("instructions-session")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(loaded.Messages), 3)
	require.Contains(t, loaded.Messages[0].Content[0].Text, "InstructionsLoaded hook feedback")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "instructions hook note")
	require.Contains(t, loaded.Messages[0].Content[0].Text, "memory hook context")
	require.Equal(t, "hello", loaded.Messages[1].Content[0].Text)
}

func TestTodosCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Todos([]string{"new", "write", "tests", "--priority", "high", "--json"}))
	require.Contains(t, out.String(), `"kind": "todos"`)
	require.Contains(t, out.String(), `"action": "add"`)
	require.Contains(t, out.String(), `"priority": "high"`)
	require.FileExists(t, todos.Path(workspace))
	out.Reset()

	require.NoError(t, app.Todos([]string{"complete", "todo-1"}))
	require.Contains(t, out.String(), "completed")
	out.Reset()

	require.NoError(t, app.Todos([]string{"reopen", "todo-1", "--json"}))
	require.Contains(t, out.String(), `"action": "pending"`)
	require.Contains(t, out.String(), `"status": "pending"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/todos begin todo-1", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "in_progress")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/todos ls", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Todos")
	require.Contains(t, out.String(), "write tests")
	out.Reset()

	require.NoError(t, app.Todos([]string{"reset", "--json"}))
	require.Contains(t, out.String(), `"action": "clear"`)
	require.Contains(t, out.String(), `"total": 0`)
	require.Empty(t, errOut.String())
}

func TestSecurityReviewCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "install.sh"), []byte("curl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.SecurityReview([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "security_review"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/security-review --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Security Review")
	require.Contains(t, out.String(), "pipe-to-shell")
	require.Empty(t, errOut.String())
}

func TestBughunterCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc risky(v any) { _, _ = v.(string); panic(\"boom\") }\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Bughunter([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "bughunter"`)
	require.Contains(t, out.String(), `"rule": "ignored-return-value"`)
	require.Contains(t, out.String(), `"rule": "panic-in-runtime-path"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/bughunter . --limit 5", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Bughunter")
	require.Contains(t, out.String(), "ignored-return-value")
	require.Empty(t, errOut.String())
	out.Reset()

	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/bughunter", ".", "--limit", "5"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "bughunter"`)
	require.Contains(t, cliOut, `"rule": "ignored-return-value"`)
}

func TestReviewCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "script.sh"), []byte("echo safe\ncurl https://example.test/install.sh | bash\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.Review([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "review"`)
	require.Contains(t, out.String(), `"status": "findings"`)
	require.Contains(t, out.String(), `"rule": "pipe-to-shell"`)
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "ultrareview", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review"`)
	require.Contains(t, cliOut, `"rule": "pipe-to-shell"`)

	require.True(t, app.handleSlash(context.Background(), "/review", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/ultrareview", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
	require.Empty(t, errOut.String())
	out.Reset()

	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	fakeBin := t.TempDir()
	fakeGH := filepath.Join(fakeBin, "gh")
	require.NoError(t, os.WriteFile(fakeGH, []byte(`#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  cat <<'JSON'
{"number":42,"url":"https://github.com/acme/widgets/pull/42","headRepository":{"nameWithOwner":"acme/widgets"}}
JSON
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$4" in
    repos/acme/widgets/issues/42/comments)
      cat <<'JSON'
[{"id":2,"body":"please update the summary","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}]
JSON
      exit 0
      ;;
    repos/acme/widgets/pulls/42/comments)
      cat <<'JSON'
[{"id":1,"body":"inline fix needed","path":"script.sh","line":2,"original_line":2,"diff_hunk":"@@ -1 +1 @@\n-old\n+new","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}]
JSON
      exit 0
      ;;
  esac
fi
echo "unexpected gh invocation: $*" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, app.ReviewRemote(context.Background(), []string{"42", "--repo", "acme/widgets", "--json"}))
	var remote reviewRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &remote))
	require.Equal(t, "review_remote", remote.Kind)
	require.Equal(t, "findings", remote.Status)
	require.Equal(t, "acme/widgets", remote.Repository)
	require.Equal(t, 42, remote.PullRequest)
	require.Equal(t, 2, remote.Remote.Total)
	require.Len(t, remote.Local.SecurityFindings, 1)
	require.Contains(t, remote.Signals, "remote review comments")
	require.Contains(t, remote.Signals, "remote issue comments")
	out.Reset()

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "reviewRemote", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review_remote"`)
	require.Contains(t, cliOut, `"remote_comments"`)
	require.Contains(t, cliOut, `"total": 2`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "latest", "--output-format", "json", "/reviewRemote", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var resumedRemote reviewRemoteReport
	require.NoError(t, json.Unmarshal([]byte(cliOut), &resumedRemote))
	require.Equal(t, "review_remote", resumedRemote.Kind)
	require.Equal(t, "acme/widgets", resumedRemote.Repository)
	require.Equal(t, 42, resumedRemote.PullRequest)
	require.Equal(t, 2, resumedRemote.Remote.Total)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "latest", "--output-format", "json", "/review-remote", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "review_remote"`)
	require.Contains(t, cliOut, `"total": 2`)

	require.True(t, app.handleSlash(context.Background(), "/reviewRemote 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Review")
	require.Contains(t, out.String(), "Remote comments  2")
	require.Contains(t, out.String(), "script.sh:2")
	require.Contains(t, out.String(), "inline fix needed")
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/review-remote 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Remote Review")
	require.Contains(t, out.String(), "PR Comments")
	require.Empty(t, errOut.String())
}

func TestMiscCompatibilityCommands(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.ExitCompatibility([]string{"--json"}))
	var exitReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &exitReport))
	require.Equal(t, "exit", exitReport.Kind)
	require.Equal(t, "ok", exitReport.Status)
	require.False(t, exitReport.ProviderRequestMade)
	out.Reset()

	require.NoError(t, app.GoodClaude([]string{"nice", "--json"}))
	var goodReport simpleCompatibilityReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &goodReport))
	require.Equal(t, "feedback", goodReport.Kind)
	require.Equal(t, "good_claude", goodReport.Action)
	require.True(t, goodReport.WorkspaceWillMutate)
	require.Contains(t, goodReport.NextCommand, "codog feedback")
	require.NotEmpty(t, goodReport.File)
	require.Greater(t, goodReport.Bytes, 0)
	require.FileExists(t, goodReport.File)
	goodData, err := os.ReadFile(goodReport.File)
	require.NoError(t, err)
	require.Contains(t, string(goodData), "Positive feedback from good-claude: nice")
	out.Reset()

}

func TestAutofixPRCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := initGitRepo(t)
	fakeBin := t.TempDir()
	fakeGH := filepath.Join(fakeBin, "gh")
	require.NoError(t, os.WriteFile(fakeGH, []byte(`#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  cat <<'JSON'
{"number":42,"url":"https://github.com/acme/widgets/pull/42","headRepository":{"nameWithOwner":"acme/widgets"}}
JSON
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$4" in
    repos/acme/widgets/issues/42/comments)
      cat <<'JSON'
[{"id":2,"body":"please update the summary","created_at":"2026-01-02T00:00:00Z","html_url":"https://example.test/issue","user":{"login":"alice"}}]
JSON
      exit 0
      ;;
    repos/acme/widgets/pulls/42/comments)
      cat <<'JSON'
[{"id":1,"body":"inline fix needed","path":"script.sh","line":2,"original_line":2,"diff_hunk":"@@ -1 +1 @@\n-old\n+new","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.test/review","user":{"login":"bob"}}]
JSON
      exit 0
      ;;
  esac
fi
echo "unexpected gh invocation: $*" >&2
exit 1
`), 0o755))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: &errOut}
	require.NoError(t, app.AutofixPR(context.Background(), []string{"42", "--repo", "acme/widgets", "--json"}))
	var report autofixpr.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "autofix_pr", report.Kind)
	require.Equal(t, "ready", report.Status)
	require.Equal(t, "acme/widgets", report.Repository)
	require.Equal(t, 42, report.PullRequest)
	require.Equal(t, 2, report.Total)
	require.Equal(t, 2, report.Actionable)
	require.Len(t, report.Items, 2)
	require.Contains(t, report.Prompt, "Fix the GitHub pull request feedback")
	require.Contains(t, report.Prompt, "script.sh:2")
	out.Reset()

	require.NoError(t, app.AutofixPR(context.Background(), []string{"42", "--repo", "acme/widgets", "--write"}))
	require.Contains(t, out.String(), "Autofix PR")
	require.Contains(t, out.String(), "File")
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "autofix", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Autofix PR Task")
	require.Contains(t, string(data), "inline fix needed")
	require.Contains(t, string(data), "please update the summary")
	out.Reset()

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "autofix-pr", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "autofix_pr"`)
	require.Contains(t, cliOut, `"actionable_comments": 2`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "latest", "--output-format", "json", "/autofix-pr", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var resumedAutofix autofixpr.Report
	require.NoError(t, json.Unmarshal([]byte(cliOut), &resumedAutofix))
	require.Equal(t, "autofix_pr", resumedAutofix.Kind)
	require.Equal(t, "ready", resumedAutofix.Status)
	require.Equal(t, "acme/widgets", resumedAutofix.Repository)
	require.Equal(t, 42, resumedAutofix.PullRequest)
	require.Equal(t, 2, resumedAutofix.Actionable)
	require.Contains(t, resumedAutofix.Prompt, "script.sh:2")

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "latest", "--output-format", "json", "/pr-comments", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var resumedPRComments struct {
		Kind           string `json:"kind"`
		Repository     string `json:"repository"`
		Number         int    `json:"number"`
		Total          int    `json:"total"`
		IssueComments  []any  `json:"issue_comments"`
		ReviewComments []any  `json:"review_comments"`
	}
	require.NoError(t, json.Unmarshal([]byte(cliOut), &resumedPRComments))
	require.Equal(t, "pr_comments", resumedPRComments.Kind)
	require.Equal(t, "acme/widgets", resumedPRComments.Repository)
	require.Equal(t, 42, resumedPRComments.Number)
	require.Equal(t, 2, resumedPRComments.Total)
	require.Len(t, resumedPRComments.IssueComments, 1)
	require.Len(t, resumedPRComments.ReviewComments, 1)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--resume", "latest", "--output-format", "json", "/pr_comments", "42", "--repo", "acme/widgets"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "pr_comments"`)
	require.Contains(t, cliOut, `"total": 2`)

	require.True(t, app.handleSlash(context.Background(), "/autofix-pr 42 --repo acme/widgets", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Autofix PR")
	require.Contains(t, out.String(), "Fix items")
	require.Empty(t, errOut.String())
}

func TestFeedbackCommandAndSlashWritesReport(t *testing.T) {
	workspace := initGitRepo(t)
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "feedback context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.Feedback([]string{"bug", "report", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "feedback"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Codog Feedback")
	require.Contains(t, string(data), "bug report")
	require.Contains(t, string(data), "source (1 messages)")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/feedback slash report", sess))
	require.Contains(t, out.String(), "Feedback")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "feedback", "*.md"))
	require.NoError(t, err)
	require.Len(t, files, 2)
}

func TestPullRequestAndIssueDraftCommands(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scratch.txt"), []byte("scratch\n"), 0o644))
	configHome := t.TempDir()
	store := session.NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "draft context")))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
		},
		Sessions:  store,
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.PullRequestDraft([]string{"ship", "readme", "--session", "source", "--json"}, config.FlagOverrides{}))
	require.Contains(t, out.String(), `"kind": "pr"`)
	require.Contains(t, out.String(), `"action": "draft"`)
	require.Contains(t, out.String(), `"session_id": "source"`)
	require.Contains(t, out.String(), `"git_state": {`)
	require.Contains(t, out.String(), `"branch_name":`)
	require.Contains(t, out.String(), `"untracked_files": 1`)
	files, err := filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "pr-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Pull Request Draft")
	require.Contains(t, string(data), "PR: ship readme")
	require.Contains(t, string(data), "README.md")
	require.Contains(t, string(data), "source (1 messages)")
	require.Contains(t, string(data), "## Preserved Git State")
	require.Contains(t, string(data), "+change")
	require.Contains(t, string(data), "scratch.txt")
	out.Reset()

	sess, err := store.Open("source")
	require.NoError(t, err)
	require.True(t, app.handleSlash(context.Background(), "/issue flaky workflow", sess))
	require.Contains(t, out.String(), "Issue Draft")
	require.Contains(t, out.String(), "Git state")
	require.Empty(t, errOut.String())
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "issue-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	data, err = os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "# Issue Draft")
	require.Contains(t, string(data), "Issue: flaky workflow")
	require.Contains(t, string(data), "## Preserved Git State")

	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/pr", "direct", "slash", "--session", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "pr"`)
	require.Contains(t, cliOut, `"session_id": "source"`)
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "pr-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 2)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "/issue", "direct", "issue", "--session", "source"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "issue"`)
	require.Contains(t, cliOut, `"session_id": "source"`)
	files, err = filepath.Glob(filepath.Join(workspace, ".codog", "drafts", "issue-*.md"))
	require.NoError(t, err)
	require.Len(t, files, 2)
}

func TestCommitPushPRDryRunCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\nchange\n"), 0o644))
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "commit-push-pr", "feat: dry run", "--dry-run", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "commit_push_pr"`)
	require.Contains(t, cliOut, `"status": "planned"`)
	require.Contains(t, cliOut, `"ship"`)
	require.Contains(t, cliOut, `"ship_events"`)
	require.Contains(t, cliOut, `"ship.provenance"`)
	require.Contains(t, cliOut, `"merge_method": "pull_request"`)
	require.Contains(t, cliOut, `"pull_request"`)

	stateOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "state", "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, stateOut, `"mode": "ship"`)
	require.Contains(t, stateOut, `"ship"`)
	require.Contains(t, stateOut, `"merge_method": "pull_request"`)

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}
	require.True(t, app.handleSlash(context.Background(), "/commit-push-pr feat: slash dry run --dry-run --no-pr", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Commit Push PR")
	require.Contains(t, out.String(), "Dry run          true")
	require.Contains(t, out.String(), "Ship")
	require.Contains(t, out.String(), "Ship method")
	require.NotContains(t, out.String(), "pull_request")
	require.Empty(t, errOut.String())
}

func commitPushPRStepNames(steps []prworkflow.Step) []string {
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.Name)
	}
	return names
}

func TestInstallGitHubAppCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Workspace: workspace, Out: &out, Err: &errOut}

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow", "claude", "--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "install_github_app"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.False(t, fileExists(filepath.Join(workspace, ".github", "workflows", "claude.yml")))
	out.Reset()

	require.NoError(t, app.InstallGitHubApp([]string{"--workflow=review", "--secret-name", "CLAUDE_KEY"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	path := filepath.Join(workspace, ".github", "workflows", "claude-code-review.yml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "anthropics/claude-code-action@v1")
	require.Contains(t, string(data), "${{ secrets.CLAUDE_KEY }}")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/install-github-app --workflow claude --dry-run", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "GitHub App Setup")
	require.Empty(t, errOut.String())

	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "setupGitHubActions", "--workflow", "all", "--dry-run"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "install_github_app"`)
	require.Contains(t, cliOut, `"dry_run": true`)
	require.Contains(t, cliOut, `"name": "claude"`)
	require.Contains(t, cliOut, `"name": "review"`)
}

func TestInstallSlackAppCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.InstallSlackApp([]string{"--json"}))
	require.Equal(t, slackAppURL, openedURL)
	require.Contains(t, out.String(), `"kind": "install_slack_app"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"install_count": 1`)
	require.Equal(t, 1, app.Config.Future.SlackAppInstallCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"compatibility"`)
	require.Contains(t, string(data), `"slack_app_install_count": 1`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()
	openedURL = ""

	require.NoError(t, app.InstallSlackApp([]string{"status", "--json"}))
	var slackStatus installSlackAppReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &slackStatus))
	require.Equal(t, "install_slack_app", slackStatus.Kind)
	require.Equal(t, "status", slackStatus.Action)
	require.Equal(t, slackAppURL, slackStatus.URL)
	require.False(t, slackStatus.Opened)
	require.Equal(t, 1, slackStatus.InstallCount)
	require.Equal(t, 1, app.Config.Future.SlackAppInstallCount)
	require.Empty(t, openedURL)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"slack_app_install_count": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/install-slack-app --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Slack App Setup")
	require.Contains(t, out.String(), slackAppURL)
	require.Equal(t, 2, app.Config.Future.SlackAppInstallCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"slack_app_install_count": 2`)
	require.Empty(t, errOut.String())
}

func TestStickersCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.Stickers([]string{"--json"}))
	require.Equal(t, stickerOrderURL, openedURL)
	require.Contains(t, out.String(), `"kind": "stickers"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"order_count": 1`)
	require.Equal(t, 1, app.Config.Future.StickerOrderCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"compatibility"`)
	require.Contains(t, string(data), `"sticker_order_count": 1`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()
	openedURL = ""

	require.NoError(t, app.Stickers([]string{"status", "--json"}))
	var stickersStatus stickersReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &stickersStatus))
	require.Equal(t, "stickers", stickersStatus.Kind)
	require.Equal(t, "status", stickersStatus.Action)
	require.Equal(t, stickerOrderURL, stickersStatus.URL)
	require.False(t, stickersStatus.Opened)
	require.Equal(t, 1, stickersStatus.OrderCount)
	require.Equal(t, 1, app.Config.Future.StickerOrderCount)
	require.Empty(t, openedURL)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sticker_order_count": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/stickers --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Sticker Order")
	require.Contains(t, out.String(), stickerOrderURL)
	require.Equal(t, 2, app.Config.Future.StickerOrderCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"sticker_order_count": 2`)
	require.Empty(t, errOut.String())
}

func TestPassesCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	referralURL := "https://example.test/guest-pass"
	require.NoError(t, app.Passes([]string{"set-url", referralURL, "--json"}))
	require.Equal(t, referralURL, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), `"kind": "passes"`)
	require.Contains(t, out.String(), `"referral_url": "`+referralURL+`"`)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"compatibility"`)
	require.Contains(t, string(data), `"guest_pass_referral_url": "`+referralURL+`"`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()

	require.NoError(t, app.Passes([]string{"status", "--json"}))
	var status passesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "passes", status.Kind)
	require.Equal(t, "status", status.Action)
	require.Equal(t, referralURL, status.URL)
	require.Equal(t, "referral", status.URLSource)
	require.True(t, status.ReferralConfigured)
	require.Equal(t, referralURL, status.ReferralURL)
	require.Equal(t, 0, status.VisitCount)
	require.False(t, status.Opened)
	require.Equal(t, 0, app.Config.Future.GuestPassVisitCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"guest_pass_visit_count"`)
	out.Reset()

	require.NoError(t, app.Passes([]string{"--json"}))
	require.Equal(t, referralURL, openedURL)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"url_source": "referral"`)
	require.Contains(t, out.String(), `"referral_configured": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.GuestPassVisitCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"guest_pass_visit_count": 1`)
	out.Reset()
	openedURL = ""

	require.True(t, app.handleSlash(context.Background(), "/passes clear-url", &session.Session{ID: "session"}))
	require.Empty(t, app.Config.Future.GuestPassReferralURL)
	require.Contains(t, out.String(), "Guest Passes")
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"guest_pass_referral_url"`)
	require.Empty(t, errOut.String())
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/passes --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), guestPassDocsURL)
	require.Equal(t, 2, app.Config.Future.GuestPassVisitCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"guest_pass_visit_count": 2`)
}

func TestPassesFetchesEligibilityAndRedemptions(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer fetch-token", r.Header.Get("Authorization"))
		require.Equal(t, "org-123", r.Header.Get("x-organization-uuid"))
		require.Equal(t, "claude_code_guest_pass", r.URL.Query().Get("campaign"))
		seen = append(seen, r.URL.Path)
		switch r.URL.Path {
		case "/api/oauth/organizations/org-123/referral/eligibility":
			fmt.Fprintln(w, `{"eligible":true,"remaining_passes":2,"referrer_reward":{"currency":"USD","amount_minor_units":500},"referral_code_details":{"referral_link":"https://example.test/pass","campaign":"claude_code_guest_pass"}}`)
		case "/api/oauth/organizations/org-123/referral/redemptions":
			fmt.Fprintln(w, `{"limit":3,"redemptions":[{"email":"used@example.test"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:        configHome,
			AuthToken:         "fetch-token",
			ForceLoginOrgUUID: "org-123",
		},
		Workspace: t.TempDir(),
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.Passes([]string{"fetch", "--base-url", server.URL, "--redemptions", "--json"}))
	var report passesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "passes", report.Kind)
	require.Equal(t, "fetch", report.Action)
	require.True(t, report.RequestSent)
	require.Equal(t, "org-123", report.OrganizationUUID)
	require.Equal(t, "claude_code_guest_pass", report.Campaign)
	require.NotNil(t, report.Eligible)
	require.True(t, *report.Eligible)
	require.NotNil(t, report.RemainingPasses)
	require.Equal(t, 2, *report.RemainingPasses)
	require.NotNil(t, report.Limit)
	require.Equal(t, 3, *report.Limit)
	require.NotNil(t, report.Redeemed)
	require.Equal(t, 1, *report.Redeemed)
	require.NotNil(t, report.AvailablePasses)
	require.Equal(t, 2, *report.AvailablePasses)
	require.NotNil(t, report.ReferrerReward)
	require.Equal(t, "USD", report.ReferrerReward.Currency)
	require.Equal(t, 500, report.ReferrerReward.AmountMinorUnits)
	require.Equal(t, "$5", report.ReferrerRewardFormatted)
	require.Equal(t, "https://example.test/pass", report.ReferralURL)
	require.True(t, report.SavedReferralURL)
	require.True(t, report.SavedEligibilityCache)
	require.NotContains(t, out.String(), "fetch-token")
	require.Equal(t, []string{
		"/api/oauth/organizations/org-123/referral/eligibility",
		"/api/oauth/organizations/org-123/referral/redemptions",
	}, seen)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"guest_pass_referral_url": "https://example.test/pass"`)
	require.Contains(t, string(data), `"guest_pass_eligibility_cache"`)
	require.Contains(t, string(data), `"org-123"`)
	require.Contains(t, string(data), `"remaining_passes": 2`)
	require.Contains(t, string(data), `"referrer_reward"`)
	require.Contains(t, string(data), `"amount_minor_units": 500`)
	out.Reset()

	require.NoError(t, app.Passes([]string{"status", "--json"}))
	var cached passesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &cached))
	require.True(t, cached.CacheHit)
	require.NotEmpty(t, cached.CachedAt)
	require.NotNil(t, cached.Eligible)
	require.True(t, *cached.Eligible)
	require.NotNil(t, cached.RemainingPasses)
	require.Equal(t, 2, *cached.RemainingPasses)
	require.NotNil(t, cached.ReferrerReward)
	require.Equal(t, "$5", cached.ReferrerRewardFormatted)
	require.Equal(t, "https://example.test/pass", cached.ReferralURL)
	require.True(t, cached.UpsellReset)
	require.True(t, cached.UpsellVisible)
	require.NotNil(t, cached.LastSeenRemaining)
	require.Equal(t, 2, *cached.LastSeenRemaining)
	out.Reset()

	require.NoError(t, app.Passes([]string{"upsell-seen", "--json"}))
	var upsellSeen passesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &upsellSeen))
	require.True(t, upsellSeen.MarkedUpsellSeen)
	require.Equal(t, 1, upsellSeen.UpsellSeenCount)
	require.True(t, upsellSeen.UpsellVisible)
	out.Reset()

	require.NoError(t, app.Passes([]string{"visit", "--json"}))
	var visited passesReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &visited))
	require.True(t, visited.MarkedVisited)
	require.True(t, visited.HasVisitedPasses)
	require.False(t, visited.UpsellVisible)
	require.NotNil(t, visited.LastSeenRemaining)
	require.Equal(t, 2, *visited.LastSeenRemaining)

	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"has_visited_passes": true`)
	require.Contains(t, string(data), `"passes_upsell_seen_count": 1`)
	require.Contains(t, string(data), `"passes_last_seen_remaining": 2`)
}

func TestPassesActionErrorsPropagate(t *testing.T) {
	blockedParent := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(blockedParent, []byte("file"), 0o644))
	blockedPath := filepath.Join(blockedParent, "config.json")
	app := &App{}
	report := passesReport{}

	require.Error(t, app.executePassesAction(passesRequest{Action: "unknown"}, blockedPath, &report))
	require.Error(t, app.setPassesURL("not-a-url", blockedPath, &report))
	require.Error(t, app.setPassesURL("https://example.com/pass", blockedPath, &report))
	require.Error(t, app.clearPassesURL(blockedPath, &report))
	require.Error(t, app.visitPasses(blockedPath, &report))
	require.Error(t, app.recordPassesUpsell(blockedPath, &report))
	require.Error(t, app.showPasses(passesRequest{Action: "show"}, blockedPath, &report))
}

func TestFormatGuestPassReward(t *testing.T) {
	require.Empty(t, formatGuestPassReward(nil))
	require.Equal(t, "$5", formatGuestPassReward(&config.MoneyInfo{Currency: "USD", AmountMinorUnits: 500}))
	require.Equal(t, "CA$7.50", formatGuestPassReward(&config.MoneyInfo{Currency: "cad", AmountMinorUnits: 750}))
	require.Equal(t, "JPY 1234.56", formatGuestPassReward(&config.MoneyInfo{Currency: "jpy", AmountMinorUnits: 123456}))
}

func TestExtraUsageCommandAndSlash(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: configHome}, Workspace: t.TempDir(), Out: &out, Err: &errOut}
	openedURL := ""
	previousOpen := openExternalURL
	openExternalURL = func(url string) (string, error) {
		openedURL = url
		return "test-open", nil
	}
	t.Cleanup(func() { openExternalURL = previousOpen })

	require.NoError(t, app.ExtraUsage([]string{"--admin", "--json"}))
	require.Equal(t, extraUsageAdminURL, openedURL)
	require.Contains(t, out.String(), `"kind": "extra_usage"`)
	require.Contains(t, out.String(), `"mode": "admin"`)
	require.Contains(t, out.String(), `"opened": true`)
	require.Contains(t, out.String(), `"visit_count": 1`)
	require.Equal(t, 1, app.Config.Future.ExtraUsageVisitCount)
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"compatibility"`)
	require.Contains(t, string(data), `"extra_usage_visit_count": 1`)
	require.NotContains(t, string(data), `"future"`)
	out.Reset()
	openedURL = ""

	require.NoError(t, app.ExtraUsage([]string{"status", "--admin", "--json"}))
	var extraUsageStatus extraUsageReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &extraUsageStatus))
	require.Equal(t, "extra_usage", extraUsageStatus.Kind)
	require.Equal(t, "status", extraUsageStatus.Action)
	require.Equal(t, "admin", extraUsageStatus.Mode)
	require.Equal(t, extraUsageAdminURL, extraUsageStatus.URL)
	require.False(t, extraUsageStatus.Opened)
	require.Equal(t, 1, extraUsageStatus.VisitCount)
	require.Equal(t, 1, app.Config.Future.ExtraUsageVisitCount)
	require.Empty(t, openedURL)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"extra_usage_visit_count": 1`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/extra-usage --personal --no-open", &session.Session{ID: "session"}))
	require.Empty(t, openedURL)
	require.Contains(t, out.String(), "Extra Usage")
	require.Contains(t, out.String(), extraUsagePersonalURL)
	require.Equal(t, 2, app.Config.Future.ExtraUsageVisitCount)
	data, err = os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"extra_usage_visit_count": 2`)
	require.Empty(t, errOut.String())

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(app.Workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })
	openedURL = ""
	cliOut, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "extra-usage-noninteractive", "--admin", "--open", "--path", configPath}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Empty(t, openedURL)
	require.Contains(t, cliOut, `"kind": "extra_usage"`)
	require.Contains(t, cliOut, `"action": "show"`)
	require.Contains(t, cliOut, `"mode": "admin"`)
	require.Contains(t, cliOut, `"opened": false`)
	require.Contains(t, cliOut, `"visit_count":`)

	cliOut, err = captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "extra-usage-core", "--personal", "--no-open", "--path", configPath, "--json"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	require.Contains(t, cliOut, `"kind": "extra_usage"`)
	require.Contains(t, cliOut, `"mode": "personal"`)
	require.Contains(t, cliOut, `"visit_count":`)
}

func TestCompatibilityLinkCommandsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	workspace := t.TempDir()
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	for _, tc := range []struct {
		name   string
		args   []string
		kind   string
		action string
	}{
		{name: "extra usage", args: []string{"extra-usage", "status", "--admin"}, kind: "extra_usage", action: "status"},
		{name: "extra usage core", args: []string{"extra-usage-core", "status", "--personal"}, kind: "extra_usage", action: "status"},
		{name: "extra usage noninteractive", args: []string{"extra-usage-noninteractive", "--admin", "--open"}, kind: "extra_usage", action: "show"},
		{name: "install slack app", args: []string{"install-slack-app", "status"}, kind: "install_slack_app", action: "status"},
		{name: "stickers", args: []string{"stickers", "status"}, kind: "stickers", action: "status"},
		{name: "passes", args: []string{"passes", "status"}, kind: "passes", action: "status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.NoError(t, err)
			var report map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, tc.kind, report["kind"])
			require.Equal(t, tc.action, report["action"])
			require.NotContains(t, out, "Extra Usage")
			require.NotContains(t, out, "Slack App Setup")
			require.NotContains(t, out, "Sticker Order")
			require.NotContains(t, out, "Guest Passes")
		})
	}
}

func TestProjectCommandAndSlash(t *testing.T) {
	workspace := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/project\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Project instructions."), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "initial project")
	var out bytes.Buffer
	app := &App{Config: config.Config{ConfigHome: t.TempDir()}, Workspace: workspace, Out: &out, Err: io.Discard}

	require.NoError(t, app.Project(nil))
	require.Contains(t, out.String(), "Project")
	require.Contains(t, out.String(), "Go module")
	require.Contains(t, out.String(), "Memory files     1")
	out.Reset()

	require.NoError(t, app.Project([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "project"`)
	require.Contains(t, out.String(), `"available": true`)
	require.Contains(t, out.String(), `"go_module":`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/project", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Project")
}

func TestSimpleInfoCommandParseErrorsHonorGlobalJSONFormat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "project", command: "project"},
		{name: "env", command: "env"},
		{name: "version", command: "version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), []string{"--output-format", "json", tc.command, "bogus"}, config.FlagOverrides{})
			})
			requireStructuredCLIError(t, err, []byte(out), "unknown_option", "unknown_option")
			require.Contains(t, out, fmt.Sprintf(`"command": "%s"`, tc.command))
			require.Contains(t, out, `"option": "bogus"`)
		})
	}
}

func TestSimpleInfoCommandsHonorGlobalJSONFormat(t *testing.T) {
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configData, 0o644))

	workspace := t.TempDir()
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	for _, tc := range []struct {
		name   string
		args   []string
		kind   string
		action string
	}{
		{name: "env", args: []string{"env"}, kind: "env"},
		{name: "sandbox-toggle", args: []string{"sandbox-toggle", "status"}, kind: "sandbox_toggle", action: "status"},
		{name: "system-prompt", args: []string{"system-prompt"}, kind: "system-prompt", action: "show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--config", configPath, "--output-format", "json"}, tc.args...)
			out, err := captureStdout(t, func() error {
				return RunCLI(context.Background(), args, config.FlagOverrides{})
			})
			require.NoError(t, err)
			var report map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &report))
			require.Equal(t, tc.kind, report["kind"])
			if tc.action != "" {
				require.Equal(t, tc.action, report["action"])
			}
			require.NotContains(t, out, "Sandbox Toggle")
			require.NotContains(t, out, "Environment")
		})
	}
}

func TestEnvCommandRedactsSensitiveValues(t *testing.T) {
	report := buildEnvReport([]string{
		"ALPHA=visible",
		"CODOG_SECRET_TOKEN=hidden",
		"NO_EQUALS",
	})
	require.Equal(t, 2, report.Total)
	require.Equal(t, 1, report.Redacted)
	require.Equal(t, "ALPHA", report.Variables[0].Name)
	require.Equal(t, "visible", report.Variables[0].Value)
	require.Equal(t, "CODOG_SECRET_TOKEN", report.Variables[1].Name)
	require.Equal(t, "[redacted]", report.Variables[1].Value)

	t.Setenv("CODOG_SECRET_TOKEN", "codog-super-secret-value-123")
	var out bytes.Buffer
	app := &App{Out: &out, Err: io.Discard}
	require.NoError(t, app.Env([]string{"--json"}))
	require.Contains(t, out.String(), `"name": "CODOG_SECRET_TOKEN"`)
	require.Contains(t, out.String(), `"value": "[redacted]"`)
	require.NotContains(t, out.String(), "codog-super-secret-value-123")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/env", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Environment")
}

func TestInitCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	var setupPayloads []struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	setupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Input string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		setupPayloads = append(setupPayloads, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer setupServer.Close()
	app := &App{
		Config: config.Config{
			ConfigHome: t.TempDir(),
			Hooks: config.HookConfig{
				SetupCommands: []config.HookCommand{{Type: "http", URL: setupServer.URL}},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Init(nil))
	require.Contains(t, out.String(), "Init")
	require.Contains(t, out.String(), ".codog/instructions.md")
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.FileExists(t, filepath.Join(workspace, "AGENTS.md"))
	require.FileExists(t, filepath.Join(workspace, "CLAUDE.md"))
	require.Len(t, setupPayloads, 1)
	require.Equal(t, "setup", setupPayloads[0].Event)
	require.Contains(t, setupPayloads[0].Input, `"source":"init"`)
	out.Reset()

	require.NoError(t, app.Init([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "init"`)
	require.Contains(t, out.String(), `"already_initialized": true`)
	require.Contains(t, out.String(), `"deferred": [`)
	require.Contains(t, out.String(), `".codog/sessions/"`)
	require.Contains(t, out.String(), `"AGENTS.md"`)
	require.Contains(t, out.String(), `"CLAUDE.md"`)
	require.Len(t, setupPayloads, 2)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Init")
	require.Len(t, setupPayloads, 3)
}

func TestInitVerifiersCommandAndSlash(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/app\n"), 0o644))
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config:    config.Config{ConfigHome: t.TempDir()},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}

	require.NoError(t, app.InitVerifiers([]string{"--dry-run", "--json"}))
	require.Contains(t, out.String(), `"kind": "init_verifiers"`)
	require.Contains(t, out.String(), `"dry_run": true`)
	require.NoFileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.NoError(t, app.InitVerifiers(nil))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/init-verifiers --target codog --force", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), "Verifier Init")
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "verifier-cli", "SKILL.md"))
	require.Empty(t, errOut.String())
}

func TestStateCommandAndREPLWritesWorkerState(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	customStatePath := filepath.Join(".codog", "custom-worker-state.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			Future: config.FutureConfig{
				BackgroundStatePath: customStatePath,
			},
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		In:        strings.NewReader("/exit\n"),
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.REPL(context.Background(), config.FlagOverrides{SessionID: "session-1"}))
	require.NoFileExists(t, workerstate.Path(workspace))
	require.FileExists(t, filepath.Join(workspace, customStatePath))
	loaded, err := workerstate.LoadPath(filepath.Join(workspace, customStatePath))
	require.NoError(t, err)
	require.Equal(t, "repl", loaded.Mode)
	require.Equal(t, "idle", loaded.Status)
	require.Equal(t, "session-1", loaded.SessionID)

	require.NoError(t, app.State(nil))
	require.Contains(t, out.String(), "State")
	require.Contains(t, out.String(), "Worker")
	out.Reset()

	require.NoError(t, app.State([]string{"--json"}))
	require.Contains(t, out.String(), `"kind": "worker_state"`)
	require.Contains(t, out.String(), `"mode": "repl"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/state", &session.Session{ID: "session-1"}))
	require.Contains(t, out.String(), "State")
}

func TestStateCommandMissingStateReportsActionableErrors(t *testing.T) {
	workspace := t.TempDir()
	var out bytes.Buffer

	err := renderWorkerState(&out, workspace, nil)
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.False(t, exitErr.Silent)
	require.Empty(t, out.String())
	require.Contains(t, err.Error(), "no worker state file found")
	require.Contains(t, err.Error(), "codog repl")
	require.Contains(t, err.Error(), "codog prompt <text>")
	require.Contains(t, err.Error(), "codog state [--json]")

	out.Reset()
	err = renderWorkerState(&out, workspace, []string{"--json"})
	require.Error(t, err)
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.Code)
	require.True(t, exitErr.Silent)
	var report workerStateErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "worker_state", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "error", report.Status)
	require.Equal(t, "missing_worker_state", report.ErrorKind)
	require.Equal(t, workerstate.Path(workspace), report.Path)
	require.Contains(t, report.Message, "no worker state file found")
	require.Contains(t, report.Hint, "codog repl")
	require.Contains(t, report.Commands, "codog prompt <text>")
	require.Contains(t, report.Commands, "codog state [--json]")
}

func TestStateCommandParseErrorsHonorGlobalJSONFormat(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--output-format", "json", "state", "bogus"}, config.FlagOverrides{})
	})
	requireStructuredCLIError(t, err, []byte(out), "unknown_option", "unknown_option")
	require.Contains(t, out, `"command": "state"`)
	require.Contains(t, out, `"option": "bogus"`)
}

func TestHooksCommandAndSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	promptPath := filepath.Join(workspace, "prompt.json")
	sessionPath := filepath.Join(workspace, "session.json")
	prePath := filepath.Join(workspace, "pre.json")
	postPath := filepath.Join(workspace, "post.json")
	postFailurePath := filepath.Join(workspace, "post-failure.json")
	permissionRequestPath := filepath.Join(workspace, "permission-request.json")
	permissionDeniedPath := filepath.Join(workspace, "permission-denied.json")
	sessionEndPath := filepath.Join(workspace, "session-end.json")
	setupPath := filepath.Join(workspace, "setup.json")
	stopPath := filepath.Join(workspace, "stop.json")
	stopFailurePath := filepath.Join(workspace, "stop-failure.json")
	compactPath := filepath.Join(workspace, "compact.json")
	postCompactPath := filepath.Join(workspace, "post-compact.json")
	notificationPath := filepath.Join(workspace, "notification.json")
	subagentStartPath := filepath.Join(workspace, "subagent-start.json")
	subagentStopPath := filepath.Join(workspace, "subagent-stop.json")
	worktreeCreatePath := filepath.Join(workspace, "worktree-create.json")
	worktreeRemovePath := filepath.Join(workspace, "worktree-remove.json")
	cwdChangedPath := filepath.Join(workspace, "cwd-changed.json")
	taskCreatedPath := filepath.Join(workspace, "task-created.json")
	taskCompletedPath := filepath.Join(workspace, "task-completed.json")
	instructionsLoadedPath := filepath.Join(workspace, "instructions-loaded.json")
	fileChangedPath := filepath.Join(workspace, "file-changed.json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				UserPromptSubmit:   []string{"cat > " + shellQuote(promptPath)},
				SessionStart:       []string{"cat > " + shellQuote(sessionPath)},
				PreToolUse:         []string{"cat > " + shellQuote(prePath)},
				PostToolUse:        []string{"cat > " + shellQuote(postPath)},
				PostToolUseFailure: []string{"cat > " + shellQuote(postFailurePath)},
				PermissionRequest:  []string{"cat > " + shellQuote(permissionRequestPath)},
				PermissionDenied:   []string{"cat > " + shellQuote(permissionDeniedPath)},
				SessionEnd:         []string{"cat > " + shellQuote(sessionEndPath)},
				Setup:              []string{"cat > " + shellQuote(setupPath)},
				Stop:               []string{"cat > " + shellQuote(stopPath)},
				StopFailure:        []string{"cat > " + shellQuote(stopFailurePath)},
				PreCompact:         []string{"cat > " + shellQuote(compactPath)},
				PostCompact:        []string{"cat > " + shellQuote(postCompactPath)},
				Notification:       []string{"cat > " + shellQuote(notificationPath)},
				SubagentStart:      []string{"cat > " + shellQuote(subagentStartPath)},
				SubagentStop:       []string{"cat > " + shellQuote(subagentStopPath)},
				WorktreeCreate:     []string{"cat > " + shellQuote(worktreeCreatePath)},
				WorktreeRemove:     []string{"cat > " + shellQuote(worktreeRemovePath)},
				CwdChanged:         []string{"cat > " + shellQuote(cwdChangedPath)},
				TaskCreated:        []string{"cat > " + shellQuote(taskCreatedPath)},
				TaskCompleted:      []string{"cat > " + shellQuote(taskCompletedPath)},
				InstructionsLoaded: []string{"cat > " + shellQuote(instructionsLoadedPath)},
				FileChanged:        []string{"cat > " + shellQuote(fileChangedPath)},
				UserPromptSubmitCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(promptPath)},
				},
				SessionStartCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(sessionPath)},
				},
				PreToolUseCommands: []config.HookCommand{
					{Matcher: "read_*", Command: "cat > " + shellQuote(prePath)},
				},
				PostToolUseCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postPath)},
				},
				PostToolUseFailureCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(postFailurePath)},
				},
				PermissionRequestCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(permissionRequestPath)},
				},
				PermissionDeniedCommands: []config.HookCommand{
					{Matcher: "bash", Command: "cat > " + shellQuote(permissionDeniedPath)},
				},
				SessionEndCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(sessionEndPath)},
				},
				SetupCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(setupPath)},
				},
				StopCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(stopPath)},
				},
				StopFailureCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(stopFailurePath)},
				},
				PreCompactCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(compactPath)},
				},
				PostCompactCommands: []config.HookCommand{
					{Command: "cat > " + shellQuote(postCompactPath)},
				},
				NotificationCommands: []config.HookCommand{
					{Matcher: "background_*", Command: "cat > " + shellQuote(notificationPath)},
				},
				SubagentStartCommands: []config.HookCommand{
					{Matcher: "reviewer", Command: "cat > " + shellQuote(subagentStartPath)},
				},
				SubagentStopCommands: []config.HookCommand{
					{Matcher: "reviewer", Command: "cat > " + shellQuote(subagentStopPath)},
				},
				WorktreeCreateCommands: []config.HookCommand{
					{Matcher: "agent-*", Command: "cat > " + shellQuote(worktreeCreatePath)},
				},
				WorktreeRemoveCommands: []config.HookCommand{
					{Matcher: "agent-*", Command: "cat > " + shellQuote(worktreeRemovePath)},
				},
				CwdChangedCommands: []config.HookCommand{
					{Matcher: "*", Command: "cat > " + shellQuote(cwdChangedPath)},
				},
				TaskCreatedCommands: []config.HookCommand{
					{Matcher: "agent", Command: "cat > " + shellQuote(taskCreatedPath)},
				},
				TaskCompletedCommands: []config.HookCommand{
					{Matcher: "agent", Command: "cat > " + shellQuote(taskCompletedPath)},
				},
				InstructionsLoadedCommands: []config.HookCommand{
					{Matcher: "session_start", Command: "cat > " + shellQuote(instructionsLoadedPath)},
				},
				FileChangedCommands: []config.HookCommand{
					{Matcher: "write_file", Command: "cat > " + shellQuote(fileChangedPath)},
				},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "session"}

	require.NoError(t, app.Hooks(context.Background(), []string{"list", "--json"}))
	require.Contains(t, out.String(), `"user_prompt_submit"`)
	require.Contains(t, out.String(), `"session_start"`)
	require.Contains(t, out.String(), `"pre_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use"`)
	require.Contains(t, out.String(), `"post_tool_use_failure"`)
	require.Contains(t, out.String(), `"permission_request"`)
	require.Contains(t, out.String(), `"permission_denied"`)
	require.Contains(t, out.String(), `"session_end"`)
	require.Contains(t, out.String(), `"setup"`)
	require.Contains(t, out.String(), `"stop"`)
	require.Contains(t, out.String(), `"stop_failure"`)
	require.Contains(t, out.String(), `"pre_compact"`)
	require.Contains(t, out.String(), `"post_compact"`)
	require.Contains(t, out.String(), `"notification"`)
	require.Contains(t, out.String(), `"subagent_start"`)
	require.Contains(t, out.String(), `"subagent_stop"`)
	require.Contains(t, out.String(), `"worktree_create"`)
	require.Contains(t, out.String(), `"worktree_remove"`)
	require.Contains(t, out.String(), `"cwd_changed"`)
	require.Contains(t, out.String(), `"task_created"`)
	require.Contains(t, out.String(), `"task_completed"`)
	require.Contains(t, out.String(), `"instructions_loaded"`)
	require.Contains(t, out.String(), `"file_changed"`)
	var hooksList hooksListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &hooksList))
	require.Contains(t, hooksList.UserPromptSubmitCommands[0].Command, "cat >")
	require.Contains(t, hooksList.SessionStartCommands[0].Command, "cat >")
	require.Equal(t, "read_*", hooksList.PreToolUseCommands[0].Matcher)
	require.Contains(t, hooksList.PreToolUseCommands[0].Command, "cat >")
	require.Equal(t, "bash", hooksList.PostToolUseFailureCommands[0].Matcher)
	require.Equal(t, "bash", hooksList.PermissionRequestCommands[0].Matcher)
	require.Equal(t, "bash", hooksList.PermissionDeniedCommands[0].Matcher)
	require.Contains(t, hooksList.SessionEndCommands[0].Command, "cat >")
	require.Contains(t, hooksList.SetupCommands[0].Command, "cat >")
	require.Contains(t, hooksList.StopCommands[0].Command, "cat >")
	require.Contains(t, hooksList.StopFailureCommands[0].Command, "cat >")
	require.Contains(t, hooksList.PreCompactCommands[0].Command, "cat >")
	require.Contains(t, hooksList.PostCompactCommands[0].Command, "cat >")
	require.Equal(t, "background_*", hooksList.NotificationCommands[0].Matcher)
	require.Equal(t, "reviewer", hooksList.SubagentStartCommands[0].Matcher)
	require.Equal(t, "reviewer", hooksList.SubagentStopCommands[0].Matcher)
	require.Equal(t, "agent-*", hooksList.WorktreeCreateCommands[0].Matcher)
	require.Equal(t, "agent-*", hooksList.WorktreeRemoveCommands[0].Matcher)
	require.Equal(t, "*", hooksList.CwdChangedCommands[0].Matcher)
	require.Equal(t, "agent", hooksList.TaskCreatedCommands[0].Matcher)
	require.Equal(t, "agent", hooksList.TaskCompletedCommands[0].Matcher)
	require.Equal(t, "session_start", hooksList.InstructionsLoadedCommands[0].Matcher)
	require.Equal(t, "write_file", hooksList.FileChangedCommands[0].Matcher)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"health", "pre", "--tool", "read_file", "--json"}))
	var health hooksHealthReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &health))
	require.Equal(t, "health", health.Action)
	require.Equal(t, "pre_tool_use", health.Event)
	require.Equal(t, "read_file", health.MatcherTarget)
	require.Equal(t, 1, health.MatchedCount)
	require.Len(t, health.Matched, 1)
	require.Contains(t, health.Matched[0].Command, prePath)
	require.Greater(t, health.ConfiguredCount, 0)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "user-prompt-submit", "--input", "hello"}))
	data, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"user_prompt_submit"`)
	require.Contains(t, string(data), `"input":"hello"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "session-start", "--input", `{"hook_event_name":"SessionStart","source":"startup","session_id":"session","transcript_path":"/tmp/session.jsonl","cwd":"` + filepath.ToSlash(workspace) + `","permission_mode":"workspace-write"}`}))
	data, err = os.ReadFile(sessionPath)
	require.NoError(t, err)
	var sessionHook struct {
		HookEventName string `json:"hook_event_name"`
		Event         string `json:"event"`
		Input         string `json:"input"`
		SessionID     string `json:"session_id"`
		Transcript    string `json:"transcript_path"`
		CWD           string `json:"cwd"`
		Permission    string `json:"permission_mode"`
	}
	require.NoError(t, json.Unmarshal(data, &sessionHook))
	require.Equal(t, "SessionStart", sessionHook.HookEventName)
	require.Equal(t, "session_start", sessionHook.Event)
	require.Equal(t, "session", sessionHook.SessionID)
	require.Equal(t, "/tmp/session.jsonl", sessionHook.Transcript)
	require.Equal(t, filepath.ToSlash(workspace), sessionHook.CWD)
	require.Equal(t, "workspace-write", sessionHook.Permission)
	require.Contains(t, sessionHook.Input, `"session_id"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "pre", "--tool", "read_file", "--input", `{"path":"README.md"}`}))
	require.Contains(t, out.String(), "Hook Run")
	data, err = os.ReadFile(prePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"pre_tool_use"`)
	require.Contains(t, string(data), `"tool":"read_file"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post --tool=bash --output=done --error", sess))
	data, err = os.ReadFile(postPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post-failure --tool=bash --output=failed --error", sess))
	data, err = os.ReadFile(postFailurePath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"post_tool_use_failure"`)
	require.Contains(t, string(data), `"is_error":true`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run permission-request --tool=bash --input={\"command\":\"git_status\"}", sess))
	data, err = os.ReadFile(permissionRequestPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"permission_request"`)
	require.Contains(t, string(data), `"tool_name":"bash"`)
	require.Contains(t, string(data), `"tool_input":{"command":"git_status"}`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run permission-denied --tool=bash --input={\"command\":\"blocked\"} --reason=deny_rule", sess))
	data, err = os.ReadFile(permissionDeniedPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"permission_denied"`)
	require.Contains(t, string(data), `"reason":"deny_rule"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run session-end --input={\"hook_event_name\":\"SessionEnd\",\"session_id\":\"session\",\"transcript_path\":\"/tmp/session.jsonl\",\"cwd\":\""+filepath.ToSlash(workspace)+"\"} --reason=exit", sess))
	data, err = os.ReadFile(sessionEndPath)
	require.NoError(t, err)
	var sessionEndHook struct {
		HookEventName string `json:"hook_event_name"`
		Event         string `json:"event"`
		Input         string `json:"input"`
		SessionID     string `json:"session_id"`
		Transcript    string `json:"transcript_path"`
		CWD           string `json:"cwd"`
		Reason        string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(data, &sessionEndHook))
	require.Equal(t, "SessionEnd", sessionEndHook.HookEventName)
	require.Equal(t, "session_end", sessionEndHook.Event)
	require.Equal(t, "session", sessionEndHook.SessionID)
	require.Equal(t, "/tmp/session.jsonl", sessionEndHook.Transcript)
	require.Equal(t, filepath.ToSlash(workspace), sessionEndHook.CWD)
	require.Equal(t, "exit", sessionEndHook.Reason)
	require.Contains(t, sessionEndHook.Input, `"session_id"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run setup --input={\"source\":\"init\"}", sess))
	data, err = os.ReadFile(setupPath)
	require.NoError(t, err)
	var setupHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &setupHook))
	require.Equal(t, "setup", setupHook.Event)
	require.Contains(t, setupHook.Input, `"source"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run stop --output=done", sess))
	data, err = os.ReadFile(stopPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"event":"stop"`)
	require.Contains(t, string(data), `"output":"done"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run stop-failure --output=rate_limited --reason=model_error", sess))
	data, err = os.ReadFile(stopFailurePath)
	require.NoError(t, err)
	var stopFailureHook struct {
		Event   string `json:"event"`
		Output  string `json:"output"`
		IsError bool   `json:"is_error"`
		Reason  string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(data, &stopFailureHook))
	require.Equal(t, "stop_failure", stopFailureHook.Event)
	require.Equal(t, "rate_limited", stopFailureHook.Output)
	require.True(t, stopFailureHook.IsError)
	require.Equal(t, "model_error", stopFailureHook.Reason)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run pre-compact --input={\"source\":\"manual\"}", sess))
	data, err = os.ReadFile(compactPath)
	require.NoError(t, err)
	var compactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &compactHook))
	require.Equal(t, "pre_compact", compactHook.Event)
	require.Contains(t, compactHook.Input, `"source"`)
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/hooks run post-compact --input={\"source\":\"manual\"}", sess))
	data, err = os.ReadFile(postCompactPath)
	require.NoError(t, err)
	var postCompactHook struct {
		Event string `json:"event"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &postCompactHook))
	require.Equal(t, "post_compact", postCompactHook.Event)
	require.Contains(t, postCompactHook.Input, `"source"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "notification", "--notification-type", "background_task_started", "--title", "Started", "--input", "task started"}))
	data, err = os.ReadFile(notificationPath)
	require.NoError(t, err)
	var notificationHook struct {
		Event            string `json:"event"`
		Tool             string `json:"tool"`
		Message          string `json:"message"`
		Title            string `json:"title"`
		NotificationType string `json:"notification_type"`
	}
	require.NoError(t, json.Unmarshal(data, &notificationHook))
	require.Equal(t, "notification", notificationHook.Event)
	require.Equal(t, "background_task_started", notificationHook.Tool)
	require.Equal(t, "task started", notificationHook.Message)
	require.Equal(t, "Started", notificationHook.Title)
	require.Equal(t, "background_task_started", notificationHook.NotificationType)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "subagent-start", "--agent-id", "task-1", "--agent-type", "reviewer"}))
	data, err = os.ReadFile(subagentStartPath)
	require.NoError(t, err)
	var subagentStartHook struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	require.NoError(t, json.Unmarshal(data, &subagentStartHook))
	require.Equal(t, "subagent_start", subagentStartHook.Event)
	require.Equal(t, "reviewer", subagentStartHook.Tool)
	require.Equal(t, "task-1", subagentStartHook.AgentID)
	require.Equal(t, "reviewer", subagentStartHook.AgentType)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "subagent-stop", "--agent-id", "task-1", "--agent-type", "reviewer", "--agent-transcript-path", "logs/task-1.log", "--last-assistant-message", "done", "--stop-hook-active"}))
	data, err = os.ReadFile(subagentStopPath)
	require.NoError(t, err)
	var subagentStopHook struct {
		Event          string `json:"event"`
		AgentID        string `json:"agent_id"`
		AgentType      string `json:"agent_type"`
		TranscriptPath string `json:"agent_transcript_path"`
		LastAssistant  string `json:"last_assistant_message"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	require.NoError(t, json.Unmarshal(data, &subagentStopHook))
	require.Equal(t, "subagent_stop", subagentStopHook.Event)
	require.Equal(t, "task-1", subagentStopHook.AgentID)
	require.Equal(t, "reviewer", subagentStopHook.AgentType)
	require.Equal(t, "logs/task-1.log", subagentStopHook.TranscriptPath)
	require.Equal(t, "done", subagentStopHook.LastAssistant)
	require.True(t, subagentStopHook.StopHookActive)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "worktree-create", "--worktree-id", "agent-1", "--worktree-path", filepath.Join(workspace, "wt"), "--ref", "abc123", "--input", `{"source":"agent"}`}))
	data, err = os.ReadFile(worktreeCreatePath)
	require.NoError(t, err)
	var worktreeCreateHook struct {
		Event        string `json:"event"`
		Tool         string `json:"tool"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Ref          string `json:"ref"`
	}
	require.NoError(t, json.Unmarshal(data, &worktreeCreateHook))
	require.Equal(t, "worktree_create", worktreeCreateHook.Event)
	require.Equal(t, "agent-1", worktreeCreateHook.Tool)
	require.Equal(t, "agent-1", worktreeCreateHook.WorktreeID)
	require.Equal(t, filepath.Join(workspace, "wt"), worktreeCreateHook.WorktreePath)
	require.Equal(t, "abc123", worktreeCreateHook.Ref)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "worktree-remove", "--worktree-id", "agent-1", "--worktree-path", filepath.Join(workspace, "wt"), "--ref", "abc123", "--reason", "manual"}))
	data, err = os.ReadFile(worktreeRemovePath)
	require.NoError(t, err)
	var worktreeRemoveHook struct {
		Event        string `json:"event"`
		Reason       string `json:"reason"`
		WorktreeID   string `json:"worktree_id"`
		WorktreePath string `json:"worktree_path"`
		Ref          string `json:"ref"`
	}
	require.NoError(t, json.Unmarshal(data, &worktreeRemoveHook))
	require.Equal(t, "worktree_remove", worktreeRemoveHook.Event)
	require.Equal(t, "manual", worktreeRemoveHook.Reason)
	require.Equal(t, "agent-1", worktreeRemoveHook.WorktreeID)
	require.Equal(t, filepath.Join(workspace, "wt"), worktreeRemoveHook.WorktreePath)
	require.Equal(t, "abc123", worktreeRemoveHook.Ref)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "cwd-changed", "--old-cwd", workspace, "--new-cwd", filepath.Join(workspace, "sub"), "--input", `{"source":"bash"}`}))
	data, err = os.ReadFile(cwdChangedPath)
	require.NoError(t, err)
	var cwdChangedHook struct {
		Event  string `json:"event"`
		Tool   string `json:"tool"`
		OldCWD string `json:"old_cwd"`
		NewCWD string `json:"new_cwd"`
		Input  string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &cwdChangedHook))
	require.Equal(t, "cwd_changed", cwdChangedHook.Event)
	require.Equal(t, filepath.Join(workspace, "sub"), cwdChangedHook.Tool)
	require.Equal(t, workspace, cwdChangedHook.OldCWD)
	require.Equal(t, filepath.Join(workspace, "sub"), cwdChangedHook.NewCWD)
	require.Contains(t, cwdChangedHook.Input, `"source"`)

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "task-created", "--task-id", "task-1", "--task-kind", "agent", "--task-status", "running", "--input", `{"id":"task-1"}`}))
	data, err = os.ReadFile(taskCreatedPath)
	require.NoError(t, err)
	var taskCreatedHook struct {
		Event      string `json:"event"`
		Tool       string `json:"tool"`
		TaskID     string `json:"task_id"`
		TaskKind   string `json:"task_kind"`
		TaskStatus string `json:"task_status"`
	}
	require.NoError(t, json.Unmarshal(data, &taskCreatedHook))
	require.Equal(t, "task_created", taskCreatedHook.Event)
	require.Equal(t, "agent", taskCreatedHook.Tool)
	require.Equal(t, "task-1", taskCreatedHook.TaskID)
	require.Equal(t, "agent", taskCreatedHook.TaskKind)
	require.Equal(t, "running", taskCreatedHook.TaskStatus)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "task-completed", "--task-id", "task-1", "--task-kind", "agent", "--task-status", "stopped", "--reason", "manual"}))
	data, err = os.ReadFile(taskCompletedPath)
	require.NoError(t, err)
	var taskCompletedHook struct {
		Event      string `json:"event"`
		Reason     string `json:"reason"`
		TaskID     string `json:"task_id"`
		TaskKind   string `json:"task_kind"`
		TaskStatus string `json:"task_status"`
	}
	require.NoError(t, json.Unmarshal(data, &taskCompletedHook))
	require.Equal(t, "task_completed", taskCompletedHook.Event)
	require.Equal(t, "manual", taskCompletedHook.Reason)
	require.Equal(t, "task-1", taskCompletedHook.TaskID)
	require.Equal(t, "agent", taskCompletedHook.TaskKind)
	require.Equal(t, "stopped", taskCompletedHook.TaskStatus)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "instructions-loaded", "--path", filepath.Join(workspace, "AGENTS.md"), "--memory-type", "Project", "--load-reason", "session_start", "--glob", "*.md", "--trigger-file-path", filepath.Join(workspace, "main.go")}))
	data, err = os.ReadFile(instructionsLoadedPath)
	require.NoError(t, err)
	var instructionsLoadedHook struct {
		Event           string   `json:"event"`
		Tool            string   `json:"tool"`
		Input           string   `json:"input"`
		FilePath        string   `json:"file_path"`
		MemoryType      string   `json:"memory_type"`
		LoadReason      string   `json:"load_reason"`
		Globs           []string `json:"globs"`
		TriggerFilePath string   `json:"trigger_file_path"`
	}
	require.NoError(t, json.Unmarshal(data, &instructionsLoadedHook))
	require.Equal(t, "instructions_loaded", instructionsLoadedHook.Event)
	require.Equal(t, "session_start", instructionsLoadedHook.Tool)
	require.Equal(t, filepath.Join(workspace, "AGENTS.md"), instructionsLoadedHook.FilePath)
	require.Equal(t, "Project", instructionsLoadedHook.MemoryType)
	require.Equal(t, "session_start", instructionsLoadedHook.LoadReason)
	require.Equal(t, []string{"*.md"}, instructionsLoadedHook.Globs)
	require.Equal(t, filepath.Join(workspace, "main.go"), instructionsLoadedHook.TriggerFilePath)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "file-changed", "--path", "docs/notes.md", "--operation", "write_file", "--input", `{"path":"docs/notes.md"}`}))
	data, err = os.ReadFile(fileChangedPath)
	require.NoError(t, err)
	var fileChangedHook struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		ToolName  string `json:"tool_name"`
		Input     string `json:"input"`
		FilePath  string `json:"file_path"`
		Operation string `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(data, &fileChangedHook))
	require.Equal(t, "file_changed", fileChangedHook.Event)
	require.Equal(t, "write_file", fileChangedHook.Tool)
	require.Equal(t, "write_file", fileChangedHook.ToolName)
	require.Contains(t, fileChangedHook.Input, `"docs/notes.md"`)
	require.Equal(t, "docs/notes.md", fileChangedHook.FilePath)
	require.Equal(t, "write_file", fileChangedHook.Operation)
	require.Empty(t, errOut.String())
}

func TestSessionStartHookOutputUpdatesSessionContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	watchPath := filepath.Join(workspace, "watched.md")
	hookOutput := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"repo is already indexed","initialUserMessage":"continue from hook","watchPaths":["` + filepath.ToSlash(watchPath) + `"]}}`
	app := &App{
		Config: config.Config{
			ConfigHome:     configHome,
			Model:          "claude-test",
			PermissionMode: "workspace-write",
			Hooks: config.HookConfig{
				SessionStartCommands: []config.HookCommand{{Matcher: "startup", Command: "printf '%s' " + shellQuote(hookOutput)}},
			},
		},
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Workspace: workspace,
		Out:       io.Discard,
		Err:       io.Discard,
	}
	sess, err := app.Sessions.Open("hook-session")
	require.NoError(t, err)

	require.NoError(t, app.runSessionStartHook(context.Background(), sess, "startup"))
	require.Len(t, sess.Messages, 2)
	require.Equal(t, "user", sess.Messages[0].Role)
	require.Contains(t, sess.Messages[0].Content[0].Text, "SessionStart hook additional context")
	require.Contains(t, sess.Messages[0].Content[0].Text, "repo is already indexed")
	require.Equal(t, "continue from hook", sess.Messages[1].Content[0].Text)

	reloaded, err := app.Sessions.OpenExisting("hook-session")
	require.NoError(t, err)
	require.Len(t, reloaded.Messages, 2)
	require.Contains(t, reloaded.Messages[0].Content[0].Text, "repo is already indexed")
	watchData, err := os.ReadFile(filepath.Join(configHome, "hooks", "watch-paths", "hook-session.json"))
	require.NoError(t, err)
	require.Contains(t, string(watchData), filepath.ToSlash(watchPath))
}

func TestSessionEndHookFeedbackIsVisible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Model:      "claude-test",
			Hooks: config.HookConfig{
				SessionEndCommands: []config.HookCommand{{
					Matcher: "exit",
					Command: `printf '%s' '{"systemMessage":"session end note","hookSpecificOutput":{"additionalContext":"session end context"}}'`,
				}},
			},
		},
		Workspace: workspace,
		Err:       &errOut,
	}
	sess := &session.Session{ID: "end-session", Path: filepath.Join(workspace, "end-session.jsonl")}

	require.NoError(t, app.runSessionEndHook(context.Background(), sess, "exit"))
	require.Contains(t, errOut.String(), "session end hook feedback:")
	require.Contains(t, errOut.String(), "session end note")
	require.Contains(t, errOut.String(), "session end context")
}

func TestTaskHookFeedbackIsVisible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				TaskCreatedCommands: []config.HookCommand{{
					Matcher: "agent",
					Command: `printf '%s' '{"systemMessage":"task created note","hookSpecificOutput":{"additionalContext":"task created context"}}'`,
				}},
				TaskCompletedCommands: []config.HookCommand{{
					Matcher: "agent",
					Command: `printf '%s' '{"systemMessage":"task completed note","hookSpecificOutput":{"additionalContext":"task completed context"}}'`,
				}},
			},
		},
		Workspace: workspace,
		Err:       &errOut,
	}
	task := background.Task{ID: "task-1", Kind: "agent", Status: "running", Command: "echo ok"}

	app.runTaskCreatedHook(context.Background(), task)
	require.Contains(t, errOut.String(), "task created hook feedback:")
	require.Contains(t, errOut.String(), "task created note")
	require.Contains(t, errOut.String(), "task created context")
	errOut.Reset()

	task.Status = "completed"
	app.runTaskCompletedHook(context.Background(), task, "manual")
	require.Contains(t, errOut.String(), "task completed hook feedback:")
	require.Contains(t, errOut.String(), "task completed note")
	require.Contains(t, errOut.String(), "task completed context")
}

func TestSubagentHookFeedbackIsVisible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				SubagentStartCommands: []config.HookCommand{{
					Matcher: "reviewer",
					Command: `printf '%s' '{"systemMessage":"subagent start note","hookSpecificOutput":{"additionalContext":"subagent start context"}}'`,
				}},
				SubagentStopCommands: []config.HookCommand{{
					Matcher: "reviewer",
					Command: `printf '%s' '{"systemMessage":"subagent stop note","hookSpecificOutput":{"additionalContext":"subagent stop context"}}'`,
				}},
			},
		},
		Workspace: workspace,
		Err:       &errOut,
	}

	app.runSubagentStartHook(context.Background(), "agent-1", "reviewer")
	require.Contains(t, errOut.String(), "subagent start hook feedback:")
	require.Contains(t, errOut.String(), "subagent start note")
	require.Contains(t, errOut.String(), "subagent start context")
	errOut.Reset()

	app.runSubagentStopHook(context.Background(), "agent-1", "reviewer", "transcript.jsonl", "done", false)
	require.Contains(t, errOut.String(), "subagent stop hook feedback:")
	require.Contains(t, errOut.String(), "subagent stop note")
	require.Contains(t, errOut.String(), "subagent stop context")
}

func TestWorktreeHookFeedbackIsVisible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			Hooks: config.HookConfig{
				WorktreeCreateCommands: []config.HookCommand{{
					Matcher: "agent-*",
					Command: `printf '%s' '{"systemMessage":"worktree create note","hookSpecificOutput":{"additionalContext":"worktree create context"}}'`,
				}},
				WorktreeRemoveCommands: []config.HookCommand{{
					Matcher: "agent-*",
					Command: `printf '%s' '{"systemMessage":"worktree remove note","hookSpecificOutput":{"additionalContext":"worktree remove context"}}'`,
				}},
			},
		},
		Workspace: workspace,
		Err:       &errOut,
	}
	allocation := worktree.Allocation{ID: "agent-1", Path: filepath.Join(workspace, ".codog", "worktrees", "agent-1"), Ref: "main"}

	require.NoError(t, app.runWorktreeCreateHook(context.Background(), allocation, "agent"))
	require.Contains(t, errOut.String(), "worktree create hook feedback:")
	require.Contains(t, errOut.String(), "worktree create note")
	require.Contains(t, errOut.String(), "worktree create context")
	errOut.Reset()

	require.NoError(t, app.runWorktreeRemoveHook(context.Background(), allocation, "manual"))
	require.Contains(t, errOut.String(), "worktree remove hook feedback:")
	require.Contains(t, errOut.String(), "worktree remove note")
	require.Contains(t, errOut.String(), "worktree remove context")
}

func TestHooksDisabledSkipsRunAndReportsStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "disabled-hook.json")
	disabled := true
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			DisableAllHooks: &disabled,
			Hooks: config.HookConfig{
				UserPromptSubmit: []string{"cat > " + shellQuote(marker)},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}

	require.NoError(t, app.Hooks(context.Background(), []string{"list", "--json"}))
	require.Contains(t, out.String(), `"status": "disabled"`)
	require.Contains(t, out.String(), `"disabled": true`)
	require.Contains(t, out.String(), `"user_prompt_submit"`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"health", "user-prompt-submit", "--json"}))
	require.Contains(t, out.String(), `"status": "disabled"`)
	require.Contains(t, out.String(), `"matched_count": 0`)
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"run", "user-prompt-submit", "--input", "hello", "--json"}))
	require.Contains(t, out.String(), `"status": "disabled"`)
	require.Contains(t, out.String(), `"count": 0`)
	require.NoFileExists(t, marker)
}

func TestHooksWatchPathsCheckTriggersFileChangedHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	watched := filepath.Join(workspace, "watched.md")
	require.NoError(t, os.WriteFile(watched, []byte("first\n"), 0o644))
	hookPayloadPath := filepath.Join(t.TempDir(), "file-changed.json")
	var out bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome: configHome,
			Hooks: config.HookConfig{
				FileChangedCommands: []config.HookCommand{{Matcher: "changed", Command: "cat > " + shellQuote(hookPayloadPath)}},
			},
		},
		Workspace: workspace,
		Out:       &out,
		Err:       io.Discard,
	}
	require.NoError(t, app.persistSessionStartWatchPaths("watch-session", []string{watched}))

	require.NoError(t, app.Hooks(context.Background(), []string{"watch-paths", "list", "watch-session", "--json"}))
	require.Contains(t, out.String(), `"session_id": "watch-session"`)
	require.Contains(t, out.String(), filepath.ToSlash(watched))
	out.Reset()

	require.NoError(t, app.Hooks(context.Background(), []string{"watch-paths", "check", "watch-session", "--json"}))
	require.Contains(t, out.String(), `"status": "initialized"`)
	require.NoFileExists(t, hookPayloadPath)
	out.Reset()

	require.NoError(t, os.WriteFile(watched, []byte("second\n"), 0o644))
	require.NoError(t, app.Hooks(context.Background(), []string{"watch-paths", "check", "watch-session", "--json"}))
	require.Contains(t, out.String(), `"status": "changed"`)
	require.Contains(t, out.String(), `"operation": "changed"`)
	data, err := os.ReadFile(hookPayloadPath)
	require.NoError(t, err)
	var payload struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		ToolName  string `json:"tool_name"`
		Input     string `json:"input"`
		FilePath  string `json:"file_path"`
		Operation string `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "file_changed", payload.Event)
	require.Equal(t, "changed", payload.Tool)
	require.Equal(t, "changed", payload.ToolName)
	require.Equal(t, "watched.md", payload.FilePath)
	require.Equal(t, "changed", payload.Operation)
	require.Contains(t, payload.Input, `"source":"watch_paths"`)
}

func TestPluginHooksLoadedByRunCLI(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]string{"config_home": configHome})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"demo","name":"demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "hooks", "hooks.json"), []byte(`{"user_prompt_submit":["${CLAUDE_PLUGIN_ROOT}/prompt"],"pre_tool_use":[{"matcher":"bash","command":"${CLAUDE_PLUGIN_DATA}/pre"}]}`), 0o644))

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "hooks", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report hooksListReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	actualWorkspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	pluginRootSlash := filepath.ToSlash(filepath.Join(actualWorkspace, ".codog", "plugins", "demo"))
	pluginDataSlash := filepath.ToSlash(filepath.Join(actualWorkspace, ".codog", "plugin-data", "demo"))
	require.Contains(t, report.UserPromptSubmit, pluginRootSlash+"/prompt")
	require.Len(t, report.PreToolUseCommands, 1)
	require.Equal(t, "bash", report.PreToolUseCommands[0].Matcher)
	require.Equal(t, pluginDataSlash+"/pre", report.PreToolUseCommands[0].Command)
}

func TestAllowManagedHooksOnlyKeepsPluginHooksAndDropsLocalHooks(t *testing.T) {
	workspace := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(map[string]any{
		"config_home":           configHome,
		"allowManagedHooksOnly": true,
		"hooks":                 map[string]any{"user_prompt_submit": []string{"echo local-hook"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o644))

	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"id":"demo","name":"demo"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "hooks", "hooks.json"), []byte(`{"user_prompt_submit":["echo managed-hook"]}`), 0o644))

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(oldWD)) })

	out, err := captureStdout(t, func() error {
		return RunCLI(context.Background(), []string{"--config", configPath, "--output-format", "json", "hooks", "list"}, config.FlagOverrides{})
	})
	require.NoError(t, err)
	var report hooksListReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.True(t, report.ManagedOnly)
	require.NotContains(t, report.UserPromptSubmit, "echo local-hook")
	require.Contains(t, report.UserPromptSubmit, "echo managed-hook")
	require.Len(t, report.UserPromptSubmitCommands, 1)
	require.Equal(t, "echo managed-hook", report.UserPromptSubmitCommands[0].Command)
}

func TestPluginMCPServersMergeIntoRuntimeConfig(t *testing.T) {
	workspace := t.TempDir()
	pluginRoot := filepath.Join(workspace, ".codog", "plugins", "demo")
	require.NoError(t, os.MkdirAll(pluginRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{
		"id":"demo",
		"name":"demo",
		"mcp_servers":{"local":{"command":"plugin-mcp","args":["--stdio"],"env":["A=B"]}}
	}`), 0o644))

	cfg := config.Config{
		MCPServers: map[string]config.MCPServerConfig{
			"user": {Command: "user-mcp"},
		},
	}
	require.NoError(t, applyPluginMCPServers(&cfg, workspace))
	require.Equal(t, "user-mcp", cfg.MCPServers["user"].Command)
	require.Equal(t, "plugin-mcp", cfg.MCPServers["plugin:demo:local"].Command)
	require.Equal(t, []string{"--stdio"}, cfg.MCPServers["plugin:demo:local"].Args)
	require.Equal(t, []string{
		"CLAUDE_PLUGIN_ROOT=" + filepath.ToSlash(pluginRoot),
		"CLAUDE_PLUGIN_DATA=" + filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo")),
		"A=B",
	}, cfg.MCPServers["plugin:demo:local"].Env)
}

func TestPermissionHooksFromPrompter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	workspace := t.TempDir()
	configHome := t.TempDir()
	requestPath := filepath.Join(workspace, "permission-request.json")
	deniedPath := filepath.Join(workspace, "permission-denied.json")
	var errOut bytes.Buffer
	app := &App{
		Config: config.Config{
			ConfigHome:      configHome,
			PermissionMode:  "prompt",
			PermissionRules: config.PermissionRules{},
			Hooks: config.HookConfig{
				PermissionRequestCommands: []config.HookCommand{{
					Matcher: "bash",
					Command: "cat > " + shellQuote(requestPath) + `; printf '%s' '{"systemMessage":"request note","hookSpecificOutput":{"additionalContext":"request context"}}'`,
				}},
				PermissionDeniedCommands: []config.HookCommand{{
					Matcher: "bash",
					Command: "cat > " + shellQuote(deniedPath) + `; printf '%s' '{"systemMessage":"denied note","hookSpecificOutput":{"additionalContext":"denied context"}}'`,
				}},
			},
		},
		Workspace: workspace,
		In:        strings.NewReader("n\n"),
		Err:       &errOut,
	}
	prompter := app.prompterWithAllowedTools("session-1", nil)

	err := prompter.Authorize("bash", tools.PermissionDanger, []byte(`{"command":"echo ok"}`))
	require.Error(t, err)

	requestPayload, err := os.ReadFile(requestPath)
	require.NoError(t, err)
	require.Contains(t, string(requestPayload), `"event":"permission_request"`)
	require.Contains(t, string(requestPayload), `"tool_name":"bash"`)
	require.Contains(t, string(requestPayload), `"tool_input":{"command":"echo ok"}`)
	deniedPayload, err := os.ReadFile(deniedPath)
	require.NoError(t, err)
	require.Contains(t, string(deniedPayload), `"event":"permission_denied"`)
	require.Contains(t, string(deniedPayload), `"reason":"user_denied"`)
	require.Contains(t, errOut.String(), "Allow? [y/N/a=always for session]")
	require.Contains(t, errOut.String(), "permission request hook feedback:")
	require.Contains(t, errOut.String(), "request note")
	require.Contains(t, errOut.String(), "request context")
	require.Contains(t, errOut.String(), "permission denied hook feedback:")
	require.Contains(t, errOut.String(), "denied note")
	require.Contains(t, errOut.String(), "denied context")
}

func TestMCPCommandToolsCallAndResources(t *testing.T) {
	server := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	configHome := t.TempDir()
	_, err := oauth.SaveToken(configHome, oauth.Token{AccessToken: "oauth-access-token"})
	require.NoError(t, err)
	var out bytes.Buffer
	app := &App{
		Config: config.Config{ConfigHome: configHome, MCPServers: map[string]config.MCPServerConfig{"test": server}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	require.Contains(t, out.String(), "MCP")
	require.Contains(t, out.String(), "Working directory")
	require.Contains(t, out.String(), "Configured servers 1")
	require.Contains(t, out.String(), "Total entries     1")
	require.Contains(t, out.String(), "Required entries  1")
	require.Contains(t, out.String(), "Optional entries  0")
	require.Contains(t, out.String(), "Invalid entries   0")
	require.Contains(t, out.String(), "test")
	require.Contains(t, out.String(), "stdio")
	require.Contains(t, out.String(), "ok")
	require.Contains(t, out.String(), "1 tool")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"--output-format", "json", "list"}))
	var listReport mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "mcp", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, "ok", listReport.Status)
	require.NotEmpty(t, listReport.WorkingDirectory)
	require.Equal(t, 1, listReport.ServerCount)
	require.Equal(t, 1, listReport.RequiredCount)
	require.Equal(t, 0, listReport.OptionalCount)
	require.Equal(t, "ok", listReport.Startup.Status)
	require.Equal(t, 1, listReport.Startup.RequiredOKCount)
	require.Equal(t, 0, listReport.Startup.RequiredFailedCount)
	require.Equal(t, 1, listReport.MCPValidation.RequiredCount)
	require.Equal(t, 0, listReport.MCPValidation.OptionalCount)
	require.Equal(t, "test", listReport.Servers[0].Name)
	require.Equal(t, "ok", listReport.Servers[0].Status)
	require.True(t, listReport.Servers[0].Required)
	require.Equal(t, mcp.ServerSignature(server), listReport.Servers[0].Signature)
	require.Equal(t, mcp.ServerConfigHash(server), listReport.Servers[0].ConfigHash)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"list", "extra", "--json"}), "unsupported_action")
	var unsupported mcpUnsupportedActionReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &unsupported))
	require.Equal(t, "mcp", unsupported.Kind)
	require.Equal(t, "error", unsupported.Action)
	require.False(t, unsupported.OK)
	require.Equal(t, "unsupported_action", unsupported.ErrorKind)
	require.Equal(t, "list extra", unsupported.RequestedAction)
	require.Contains(t, unsupported.Hint, "codog mcp list")
	require.Contains(t, unsupported.Usage.DirectCLI, "add NAME COMMAND [ARG...]")
	require.Contains(t, unsupported.Usage.DirectCLI, "add NAME --url URL")
	require.Contains(t, unsupported.Usage.DirectCLI, "remove SERVER")
	require.Contains(t, unsupported.Usage.DirectCLI, "call SERVER TOOL JSON")
	require.Contains(t, unsupported.Usage.DirectCLI, "read SERVER URI")
	require.Contains(t, unsupported.Usage.DirectCLI, "prompt SERVER NAME [JSON]")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"tools", "test"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	require.Contains(t, out.String(), `"input_schema"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"tools"}))
	var aggregateTools mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregateTools))
	require.Equal(t, "mcp", aggregateTools.Kind)
	require.Equal(t, "tools", aggregateTools.Action)
	require.Equal(t, "ok", aggregateTools.Status)
	require.Equal(t, 1, aggregateTools.ServerCount)
	require.Equal(t, 1, aggregateTools.Total)
	require.Len(t, aggregateTools.Tools, 1)
	require.Equal(t, "test", aggregateTools.Tools[0].Server)
	require.Equal(t, "echo", aggregateTools.Tools[0].Tools[0].Name)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"auth", "test"}))
	require.Contains(t, out.String(), `"status": "ok"`)
	require.Contains(t, out.String(), `"tool_count": 1`)
	require.Contains(t, out.String(), `"oauth_status"`)
	require.Contains(t, out.String(), `"next_actions"`)
	require.Contains(t, out.String(), `"codog mcp tools test"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"auth"}))
	var aggregateAuth mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregateAuth))
	require.Equal(t, "auth", aggregateAuth.Action)
	require.Equal(t, "ok", aggregateAuth.Status)
	require.Equal(t, 1, aggregateAuth.ServerCount)
	require.Len(t, aggregateAuth.AuthStatuses, 1)
	require.Equal(t, "test", aggregateAuth.AuthStatuses[0].Server)
	require.Equal(t, "ok", aggregateAuth.AuthStatuses[0].Status)
	require.NotNil(t, aggregateAuth.AuthStatuses[0].OAuthStatus)
	require.True(t, aggregateAuth.AuthStatuses[0].OAuthStatus.TokenPresent)
	require.Contains(t, actionCommandsFromMCPReport(aggregateAuth.AuthStatuses[0].NextActions), "codog mcp tools test")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp tools test", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"call", "test", "echo", `{"text":"hi"}`}))
	require.Contains(t, out.String(), `"text": "hi"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resources", "test"}))
	require.Contains(t, out.String(), "codog://note")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resources"}))
	var aggregateResources mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregateResources))
	require.Equal(t, "resources", aggregateResources.Action)
	require.Equal(t, "ok", aggregateResources.Status)
	require.Equal(t, 1, aggregateResources.Total)
	require.Len(t, aggregateResources.Resources, 1)
	require.Contains(t, string(aggregateResources.Resources[0].Resources), "codog://note")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resource-templates", "test"}))
	require.Contains(t, out.String(), "codog://notes/{name}")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"resource-templates"}))
	var aggregateTemplates mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregateTemplates))
	require.Equal(t, "resource-templates", aggregateTemplates.Action)
	require.Equal(t, "ok", aggregateTemplates.Status)
	require.Equal(t, 1, aggregateTemplates.Total)
	require.Len(t, aggregateTemplates.ResourceTemplates, 1)
	require.Contains(t, string(aggregateTemplates.ResourceTemplates[0].Templates), "codog://notes/{name}")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"read", "test", "codog://note"}))
	require.Contains(t, out.String(), "note body")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompts", "test"}))
	require.Contains(t, out.String(), `"name": "review"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompts"}))
	var aggregatePrompts mcpAggregateRemoteReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &aggregatePrompts))
	require.Equal(t, "prompts", aggregatePrompts.Action)
	require.Equal(t, "ok", aggregatePrompts.Status)
	require.Equal(t, 1, aggregatePrompts.Total)
	require.Len(t, aggregatePrompts.Prompts, 1)
	var promptsPayload struct {
		Prompts []struct {
			Name string `json:"name"`
		} `json:"prompts"`
	}
	require.NoError(t, json.Unmarshal(aggregatePrompts.Prompts[0].Prompts, &promptsPayload))
	require.Len(t, promptsPayload.Prompts, 1)
	require.Equal(t, "review", promptsPayload.Prompts[0].Name)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"prompt", "test", "review", `{"topic":"hooks"}`}))
	require.Contains(t, out.String(), "Review hooks")
}

func TestMCPListReportsOptionalFailuresAndNextActions(t *testing.T) {
	required := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	optionalMissing := config.MCPServerConfig{
		Command:  "codog-definitely-missing-mcp-server",
		Required: false,
	}
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{
			"optional-missing": optionalMissing,
			"required-ready":   required,
		}},
		Out: &out,
		Err: io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"list"}))
	text := out.String()
	require.Contains(t, text, "Startup gate      degraded (1 optional failed)")
	require.Contains(t, text, "Failed optional MCP servers")
	require.Contains(t, text, "optional-missing")
	require.Contains(t, text, "Next actions")
	require.Contains(t, text, "codog mcp show 'optional-missing' --json")
	require.NotContains(t, text, "Failed required MCP servers")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"list", "--json"}))
	var report mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, "degraded", report.Status)
	require.Equal(t, "degraded", report.Startup.Status)
	require.Equal(t, 1, report.Startup.OptionalFailedCount)
	require.Empty(t, report.Startup.FailedRequired)
	require.Len(t, report.Startup.FailedOptional, 1)
	require.Equal(t, "optional-missing", report.Startup.FailedOptional[0].Name)
	require.Contains(t, report.NextActions, "codog mcp show 'optional-missing' --json")
}

func actionCommandsFromMCPReport(actions []mcpauthdiag.NextAction) []string {
	commands := make([]string, 0, len(actions))
	for _, action := range actions {
		commands = append(commands, action.Command)
	}
	return commands
}

func TestMCPCommandActionAliases(t *testing.T) {
	server := config.MCPServerConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestAgentMCPHelperProcess"},
		Env:      []string{"CODOG_AGENT_MCP_HELPER=1"},
		Required: true,
	}
	var out bytes.Buffer
	app := &App{
		Config: config.Config{MCPServers: map[string]config.MCPServerConfig{"test": server}},
		Out:    &out,
		Err:    io.Discard,
	}

	require.NoError(t, app.MCP(context.Background(), []string{"ls", "--json"}))
	var listReport mcpListReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &listReport))
	require.Equal(t, "mcp", listReport.Kind)
	require.Equal(t, "list", listReport.Action)
	require.Equal(t, "test", listReport.Servers[0].Name)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"inspect", "test", "--json"}))
	var showReport mcpShowReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &showReport))
	require.Equal(t, "mcp", showReport.Kind)
	require.Equal(t, "show", showReport.Action)
	require.True(t, showReport.Found)
	require.NotNil(t, showReport.Server)
	require.Equal(t, "test", showReport.Server.Name)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"tool", "test"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"invoke", "test", "echo", `{"text":"hi"}`}))
	require.Contains(t, out.String(), `"text": "hi"`)
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"template", "test"}))
	require.Contains(t, out.String(), "codog://notes/{name}")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"get-resource", "test", "codog://note"}))
	require.Contains(t, out.String(), "note body")
	out.Reset()

	require.NoError(t, app.MCP(context.Background(), []string{"get-prompt", "test", "review", `{"topic":"aliases"}`}))
	require.Contains(t, out.String(), "Review hooks")
	out.Reset()

	require.True(t, app.handleSlash(context.Background(), "/mcp tool test", &session.Session{ID: "session"}))
	require.Contains(t, out.String(), `"name": "echo"`)
	out.Reset()

	require.ErrorContains(t, app.MCP(context.Background(), []string{"tool", "missing", "--json"}), "server_not_found")
	var errorReport mcpRemoteActionErrorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &errorReport))
	require.Equal(t, "tools", errorReport.Action)
	require.Equal(t, "tool missing", errorReport.RequestedAction)
	require.Equal(t, "codog mcp tools [SERVER]", errorReport.Usage.DirectCLI)
}
