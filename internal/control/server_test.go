package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestControlHealth(t *testing.T) {
	server := httptest.NewServer(Server{Sessions: &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")}}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestControlAuth(t *testing.T) {
	server := httptest.NewServer(Server{
		Sessions:  &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
		AuthToken: "secret-token",
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(server.URL + "/sessions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	require.NoError(t, err)
	req.Header.Set("authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestControlState(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		LeaseTTL:   30 * time.Second,
		Now:        func() time.Time { return now },
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/state", "application/json", bytes.NewBufferString(`{"heartbeat":true,"failure_code":"transport_lost","failure_message":"lost connection","retryable":true}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"last_error":"lost connection"`)
	require.Contains(t, string(body), `"failure":{"code":"transport_lost","message":"lost connection","retryable":true`)
	require.Contains(t, string(body), `"heartbeat_at":"2026-06-29T12:00:00Z"`)
	require.Contains(t, string(body), `"lease_ttl_seconds":30`)
	require.Contains(t, string(body), `"lease_expires_at":"2026-06-29T12:00:30Z"`)
	require.NotContains(t, string(body), `"lease_expired":true`)

	resp, err = http.Get(server.URL + "/state")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"last_error":"lost connection"`)

	now = now.Add(31 * time.Second)
	resp, err = http.Get(server.URL + "/state")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"lease_expired":true`)
}

func TestControlStateKeepsLastErrorCompatibility(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Now:        func() time.Time { return now },
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/state", "application/json", bytes.NewBufferString(`{"last_error":"legacy error"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"last_error":"legacy error"`)
	require.Contains(t, string(body), `"failure":{"code":"remote_error","message":"legacy error"`)

	resp, err = http.Post(server.URL+"/state", "application/json", bytes.NewBufferString(`{"clear_failure":true}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"failure"`)
	require.NotContains(t, string(body), `"last_error"`)
}

func TestControlSessionMutationEndpoints(t *testing.T) {
	root := t.TempDir()
	store := &session.Store{Dir: filepath.Join(root, "sessions")}
	server := httptest.NewServer(Server{
		Sessions:   store,
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/sessions/session-remote/input", "application/json", bytes.NewBufferString(`{"input":"remote prompt"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Post(server.URL+"/sessions/session-remote/messages", "application/json", bytes.NewBufferString(`{"role":"user","text":"hello remote"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "hello remote")

	resp, err = http.Post(server.URL+"/sessions/session-remote/messages", "application/json", bytes.NewBufferString(`{"message":{"role":"assistant","content":[{"type":"text","text":"remote answer"}]}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(server.URL + "/sessions/session-remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "hello remote")
	require.Contains(t, string(body), "remote answer")

	resp, err = http.Post(server.URL+"/sessions/session-remote/rewind", "application/json", bytes.NewBufferString(`{"remove_messages":1}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"removed_messages":1`)

	entries, err := store.PromptHistory("session-remote")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "remote prompt", entries[0].Text)
}

func TestControlSessionPromptStartsBackgroundRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	root := t.TempDir()
	script := filepath.Join(t.TempDir(), "codog-shim")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'remote-prompt:%s\\n' \"$*\"\n"), 0o755))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  root,
		Executable: script,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/sessions/session-remote/prompt", "application/json", bytes.NewBufferString(`{"prompt":"summarize remote state"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var task background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	require.NotEmpty(t, task.ID)
	require.Equal(t, "prompt", task.Kind)
	require.Equal(t, "session-remote", task.SessionID)
	require.Eventually(t, func() bool {
		logs, err := background.NewStore(filepath.Join(root, "home")).Logs(task.ID, 4096)
		return err == nil && strings.Contains(logs, "remote-prompt:--resume session-remote prompt summarize remote state")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestControlBackgroundLifecycle(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/background", "application/json", bytes.NewBufferString(`{"command":"echo remote","session_id":"session-remote","restart_policy":{"enabled":true,"mode":"on-failure","max_attempts":2}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var task background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	require.NotEmpty(t, task.ID)
	require.Equal(t, "session-remote", task.SessionID)
	require.NotNil(t, task.RestartPolicy)
	require.Equal(t, 2, task.RestartPolicy.MaxAttempts)

	require.Eventually(t, func() bool {
		resp, err := http.Get(server.URL + "/background/" + task.ID + "/logs?limit=100")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return bytes.Contains(body, []byte("remote"))
	}, 2*time.Second, 50*time.Millisecond)

	resp, err = http.Get(server.URL + "/background/" + task.ID)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = http.Get(server.URL + "/background?session_id=session-remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	var sessionTasks []background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessionTasks))
	require.Len(t, sessionTasks, 1)
	require.Equal(t, task.ID, sessionTasks[0].ID)

	resp, err = http.Get(server.URL + "/sessions/session-remote/background")
	require.NoError(t, err)
	defer resp.Body.Close()
	sessionTasks = nil
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessionTasks))
	require.Len(t, sessionTasks, 1)
	require.Equal(t, task.ID, sessionTasks[0].ID)

	resp, err = http.Post(server.URL+"/background/"+task.ID+"/restart", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var restarted background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&restarted))
	require.NotEmpty(t, restarted.ID)
	require.NotEqual(t, task.ID, restarted.ID)
	require.Equal(t, task.ID, restarted.RestartedFrom)
	require.Equal(t, "session-remote", restarted.SessionID)

	require.Eventually(t, func() bool {
		resp, err := http.Get(server.URL + "/background/" + restarted.ID + "/logs?limit=100")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return bytes.Contains(body, []byte("remote"))
	}, 2*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		resp, err := http.Get(server.URL + "/background/" + restarted.ID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var task background.Task
		if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
			return false
		}
		return task.Status != "running"
	}, 2*time.Second, 50*time.Millisecond)

	resp, err = http.Post(server.URL+"/background/prune", "application/json", bytes.NewBufferString(`{"older_than_days":0,"keep":0}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var pruned background.PruneResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pruned))
	require.Contains(t, pruned.Removed, task.ID)
	require.Contains(t, pruned.Removed, restarted.ID)
}

func TestControlBackgroundSupervise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	root := t.TempDir()
	configHome := filepath.Join(root, "home")
	store := background.NewStore(configHome)
	now := time.Now().UTC()
	failed := background.Task{
		ID:            "failed",
		Command:       "echo supervised-http",
		Workspace:     root,
		SessionID:     "session-remote",
		RestartPolicy: &background.RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 2},
		Status:        "failed",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   &now,
		LogPath:       filepath.Join(store.Dir, "failed.log"),
	}
	require.NoError(t, os.MkdirAll(store.Dir, 0o755))
	require.NoError(t, os.WriteFile(failed.LogPath, []byte("failed"), 0o644))
	data, err := json.Marshal(failed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.Dir, "failed.json"), data, 0o644))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: configHome,
		Workspace:  root,
		Now:        func() time.Time { return now },
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/background/supervise", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result background.SuperviseResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Len(t, result.Restarted, 1)
	require.Equal(t, "failed", result.Restarted[0].RestartedFrom)
	require.Equal(t, "session-remote", result.Restarted[0].SessionID)
	source, err := store.Get("failed")
	require.NoError(t, err)
	require.Equal(t, result.Restarted[0].ID, source.RestartedBy)
}

func TestControlBackgroundWatchStreamsEvents(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "home")
	store := background.NewStore(configHome)
	task, err := store.Run("echo watch-remote", root)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		logs, err := store.Logs(task.ID, 100)
		return err == nil && strings.Contains(logs, "watch-remote")
	}, 2*time.Second, 50*time.Millisecond)
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: configHome,
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/background/" + task.ID + "/watch?interval_ms=10")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("content-type"), "application/x-ndjson")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"type":"status"`)
	require.Contains(t, string(body), `"type":"log"`)
	require.Contains(t, string(body), "watch-remote")
}

func TestControlTerminalLifecycleStreamsEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	root := t.TempDir()
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/terminal", "application/json", bytes.NewBufferString(`{"command":"echo terminal-remote","session_id":"session-terminal"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var task background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	require.NotEmpty(t, task.ID)
	require.Equal(t, "terminal", task.Kind)
	require.Equal(t, "session-terminal", task.SessionID)

	require.Eventually(t, func() bool {
		resp, err := http.Get(server.URL + "/terminal/" + task.ID + "/stream?interval_ms=10")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == http.StatusOK &&
			strings.Contains(resp.Header.Get("content-type"), "application/x-ndjson") &&
			strings.Contains(string(body), `"type":"status"`) &&
			strings.Contains(string(body), `"type":"log"`) &&
			strings.Contains(string(body), "terminal-remote")
	}, 2*time.Second, 50*time.Millisecond)

	resp, err = http.Get(server.URL + "/terminal?session_id=session-terminal")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tasks []background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tasks))
	require.Len(t, tasks, 1)
	require.Equal(t, task.ID, tasks[0].ID)
	require.Equal(t, "terminal", tasks[0].Kind)

	resp, err = http.Get(server.URL + "/terminal/" + task.ID)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var current background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&current))
	require.Equal(t, "terminal", current.Kind)

	resp, err = http.Get(server.URL + "/terminal/" + task.ID + "/logs?limit=100")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "terminal-remote")

	resp, err = http.Post(server.URL+"/terminal/"+task.ID+"/restart", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var restarted background.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&restarted))
	require.Equal(t, "terminal", restarted.Kind)
	require.Equal(t, task.ID, restarted.RestartedFrom)
}

func TestControlGoDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/remote\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package remote\n\nfunc Broken() { Missing() }\n"), 0o644))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
		ConfigHome: filepath.Join(t.TempDir(), "home"),
		Workspace:  workspace,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/diagnostics/go?pattern=./...")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "undefined")
	require.Contains(t, string(body), "main.go")
}

func TestControlCodeIntelligence(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	source := strings.Join([]string{
		"package demo",
		"",
		"type Widget struct{}",
		"",
		"func BuildWidget() Widget {",
		"    return Widget{}",
		"}",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "demo.go"), []byte(source), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.go"), []byte("package secret\n\ntype Secret struct{}\n"), 0o644))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  workspace,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/code/symbols?path=demo.go")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"symbols"`)
	require.Contains(t, string(body), `"name":"BuildWidget"`)
	require.Contains(t, string(body), `"name":"Widget"`)

	resp, err = http.Post(server.URL+"/code/references", "application/json", bytes.NewBufferString(`{"symbol":"Widget","limit":5}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"references"`)
	require.Contains(t, string(body), `"symbol":"Widget"`)
	require.Contains(t, string(body), `"text":"type Widget struct{}"`)

	resp, err = http.Get(server.URL + "/code/definition?symbol=BuildWidget")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"definition"`)
	require.Contains(t, string(body), `"found":true`)
	require.Contains(t, string(body), `"name":"BuildWidget"`)

	resp, err = http.Post(server.URL+"/code/hover", "application/json", bytes.NewBufferString(`{"symbol":"Widget","context_lines":1}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"hover"`)
	require.Contains(t, string(body), `"found":true`)
	require.Contains(t, string(body), `type Widget struct{}`)

	resp, err = http.Get(server.URL + "/code/symbols?path=../secret.go")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
