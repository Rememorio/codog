package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
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

func TestRouteSpecsDescribeServedRemoteAPI(t *testing.T) {
	routes := RouteSpecs()
	require.NotEmpty(t, routes)

	byPath := map[string]RouteSpec{}
	for _, route := range routes {
		require.NotEmpty(t, route.Path)
		require.NotEmpty(t, route.Methods)
		byPath[route.Path] = route
	}

	require.True(t, byPath["/health"].Public)
	require.Equal(t, []string{http.MethodGet}, byPath["/health"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, byPath["/state"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodDelete}, byPath["/sessions/{id}"].Methods)
	require.Contains(t, byPath, "/sessions/{id}/history")
	require.Contains(t, byPath, "/sessions/{id}/fork")
	require.Contains(t, byPath, "/sessions/{id}/rename")
	require.Equal(t, []string{http.MethodPost}, byPath["/file/write"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, byPath["/notebook/read"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/notebook/edit"].Methods)
	require.Equal(t, []string{http.MethodGet}, byPath["/lsp/actions"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/lsp/start"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, byPath["/lsp/status"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/lsp/query"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, byPath["/mcp/list"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/mcp/call"].Methods)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, byPath["/mcp/read"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/mcp/prompt"].Methods)
	require.Equal(t, []string{http.MethodGet}, byPath["/bridge/faults"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/bridge/faults/record"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/bridge/faults/clear"].Methods)
	require.True(t, byPath["/background/{id}/watch"].Streaming)
	require.Equal(t, []string{http.MethodGet}, byPath["/agents/runs"].Methods)
	require.Equal(t, []string{http.MethodGet}, byPath["/agents/board"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/agents/prune"].Methods)
	require.Equal(t, []string{http.MethodGet}, byPath["/agents/{id}"].Methods)
	require.Equal(t, []string{http.MethodPost}, byPath["/agents/{id}/stop"].Methods)
	require.Contains(t, byPath, "/editor/selection")
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

func TestControlHooksHealth(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "hook-ran")
	server := httptest.NewServer(Server{
		Sessions:  &session.Store{Dir: filepath.Join(root, "sessions")},
		Workspace: root,
		Hooks: config.HookConfig{
			PreToolUseCommands: []config.HookCommand{
				{Matcher: "read_*", Type: "command", Command: "touch " + marker},
				{Matcher: "bash", Type: "command", Command: "echo bash"},
			},
			NotificationCommands: []config.HookCommand{
				{Matcher: "background_*", Type: "command", Command: "touch " + marker},
			},
		},
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/hooks/health?event=pre&tool=read_file")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var getReport hookHealthReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getReport))
	require.Equal(t, "hooks", getReport.Kind)
	require.Equal(t, "health", getReport.Action)
	require.Equal(t, "ok", getReport.Status)
	require.Equal(t, "/hooks/health", getReport.Route)
	require.True(t, getReport.RoutePresent)
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, getReport.AcceptedMethods)
	require.False(t, getReport.ExecutesHooks)
	require.Equal(t, "pre_tool_use", getReport.Event)
	require.Equal(t, "read_file", getReport.MatcherTarget)
	require.Equal(t, 3, getReport.ConfiguredCount)
	require.Equal(t, 1, getReport.MatchedCount)
	require.Len(t, getReport.Matched, 1)
	require.Equal(t, "read_*", getReport.Matched[0].Matcher)
	require.Contains(t, getReport.Matched[0].Command, marker)
	require.NoFileExists(t, marker)

	resp, err = http.Post(server.URL+"/hooks/status", "application/json", bytes.NewBufferString(`{"event":"notification","notification_type":"background_task_started"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var postReport hookHealthReport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&postReport))
	require.Equal(t, "/hooks/status", postReport.Route)
	require.Equal(t, "notification", postReport.Event)
	require.Equal(t, "background_task_started", postReport.MatcherTarget)
	require.Equal(t, 1, postReport.MatchedCount)
	require.Equal(t, "background_*", postReport.Matched[0].Matcher)
	require.False(t, postReport.ExecutesHooks)
	require.NoFileExists(t, marker)

	resp, err = http.Get(server.URL + "/hooks/health?event=unknown")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
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

	resp, err = http.Get(server.URL + "/sessions/session-remote/history?limit=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"session_history"`)
	require.Contains(t, string(body), `"text":"remote prompt"`)

	resp, err = http.Post(server.URL+"/sessions/session-remote/fork", "application/json", bytes.NewBufferString(`{"branch_name":"remote-branch"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"action":"fork"`)
	require.Contains(t, string(body), `"parent_id":"session-remote"`)
	require.Contains(t, string(body), `"branch_name":"remote-branch"`)
	var forkReport struct {
		Session session.Session `json:"session"`
	}
	require.NoError(t, json.Unmarshal(body, &forkReport))
	require.NotEmpty(t, forkReport.Session.ID)
	require.NotEqual(t, "session-remote", forkReport.Session.ID)

	resp, err = http.Post(server.URL+"/sessions/session-remote/rename", "application/json", bytes.NewBufferString(`{"new_id":"renamed-remote"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"action":"rename"`)
	require.Contains(t, string(body), `"old_id":"session-remote"`)
	require.Contains(t, string(body), `"new_id":"renamed-remote"`)

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/sessions/renamed-remote", nil)
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"action":"delete"`)
	require.Contains(t, string(body), `"id":"renamed-remote"`)
	require.NoFileExists(t, filepath.Join(store.Dir, "session-remote.jsonl"))
	require.NoFileExists(t, filepath.Join(store.Dir, "renamed-remote.jsonl"))

	entries, err := store.PromptHistory("session-remote")
	require.NoError(t, err)
	require.Empty(t, entries)
	forked, err := store.Open(forkReport.Session.ID)
	require.NoError(t, err)
	require.Equal(t, "fork:remote-branch", forked.Identity.Purpose)
	require.Len(t, forked.Messages, 1)
	require.Equal(t, "hello remote", forked.Messages[0].Content[0].Text)
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
	}, 10*time.Second, 50*time.Millisecond)
}

func TestControlEditorBridgeEndpoints(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "home")
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	server := httptest.NewServer(Server{
		Sessions:    &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome:  configHome,
		Workspace:   root,
		EditorToken: "secret",
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/editor/identify", "application/json", bytes.NewBufferString(`{"editor":"VS Code","workspace":"`+filepath.ToSlash(root)+`","token":"wrong"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, err = http.Post(server.URL+"/editor/identify", "application/json", bytes.NewBufferString(`{"editor":"VS Code","version":"1.0","workspace":"`+filepath.ToSlash(root)+`","token":"secret"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"editor":"VS Code"`)
	require.Contains(t, string(body), `"trusted":true`)

	resp, err = http.Post(server.URL+"/editor/open", "application/json", bytes.NewBufferString(`{"path":"main.go"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"path":"main.go"`)

	resp, err = http.Post(server.URL+"/editor/selection", "application/json", bytes.NewBufferString(`{"start_line":1,"start_column":1,"end_line":1,"end_column":8}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"path":"main.go"`)
	require.Contains(t, string(body), `"text":"package"`)

	resp, err = http.Get(server.URL + "/editor/selection")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"text":"package"`)

	resp, err = http.Get(server.URL + "/editor/state")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"open_file":{"path":"main.go"`)
	require.Contains(t, string(body), `"selection":{"path":"main.go"`)
	require.FileExists(t, filepath.Join(configHome, "bridge", "editor-state.json"))
}

func TestControlBridgeFaults(t *testing.T) {
	configHome := t.TempDir()
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
		ConfigHome: configHome,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/bridge/faults/record", "application/json", bytes.NewBufferString(`{"action":"latency","args":["250ms"]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"bridge_faults"`)
	require.Contains(t, string(body), `"total":1`)
	require.Contains(t, string(body), `"action":"latency"`)
	require.Contains(t, string(body), "250ms")
	require.FileExists(t, filepath.Join(configHome, "bridge", "faults.json"))

	resp, err = http.Get(server.URL + "/bridge/faults")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"total":1`)
	require.Contains(t, string(body), `"latency"`)

	resp, err = http.Post(server.URL+"/bridge/faults/record", "application/json", bytes.NewBufferString(`{"args":["missing"]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "bridge fault action is required")

	resp, err = http.Post(server.URL+"/bridge/faults/clear", "application/json", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"cleared":true`)
	require.Contains(t, string(body), `"total":0`)
	require.NoFileExists(t, filepath.Join(configHome, "bridge", "faults.json"))
}

func TestControlBridgeFaultsRequireConfigHome(t *testing.T) {
	server := httptest.NewServer(Server{
		Sessions: &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/bridge/faults")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "config home is required")
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

func TestControlAgentRunsLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	root := t.TempDir()
	configHome := filepath.Join(root, "home")
	taskStore := background.NewStore(configHome)
	runStore := agentruns.NewStore(configHome)
	task, err := taskStore.RunWithOptions("printf agent-http", root, background.RunOptions{Kind: "agent", AgentType: "reviewer", SessionID: "session-remote"})
	require.NoError(t, err)
	run, err := runStore.Save(agentruns.Run{
		ID:        "run-" + task.ID,
		Agent:     "reviewer",
		Workspace: root,
		SessionID: "session-remote",
		TaskID:    task.ID,
		CreatedAt: task.StartedAt,
		UpdatedAt: task.StartedAt,
	})
	require.NoError(t, err)
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: configHome,
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/agents/runs?agent=reviewer&session_id=session-remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statuses []agentruns.Status
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&statuses))
	require.Len(t, statuses, 1)
	require.Equal(t, run.ID, statuses[0].Run.ID)

	resp, err = http.Get(server.URL + "/agents/" + run.ID)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var status agentruns.Status
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	require.Equal(t, task.ID, status.Run.TaskID)

	require.Eventually(t, func() bool {
		resp, err := http.Get(server.URL + "/agents/" + run.ID + "/logs?limit=100")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == http.StatusOK && strings.Contains(string(body), "agent-http")
	}, 2*time.Second, 50*time.Millisecond)

	heartbeatBody := bytes.NewBufferString(`{"status":"working","transport_alive":true}`)
	resp, err = http.Post(server.URL+"/agents/"+run.ID+"/heartbeat", "application/json", heartbeatBody)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var action struct {
		Run  agentruns.Run   `json:"run"`
		Task background.Task `json:"task"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&action))
	require.Equal(t, "working", action.Task.Heartbeat.Status)

	resp, err = http.Get(server.URL + "/agents/board?stalled_after_seconds=60")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var board agentruns.Board
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&board))
	require.True(t, controlAgentBoardContains(board, run.ID))

	orphan, err := runStore.Save(agentruns.Run{ID: "run-orphan", Agent: "reviewer", TaskID: "missing-task", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	require.NoError(t, err)
	resp, err = http.Post(server.URL+"/agents/prune", "application/json", bytes.NewBufferString(`{"older_than_days":0,"keep":0}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var pruned background.PruneResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pruned))
	require.Contains(t, pruned.Removed, orphan.ID)

	longTask, err := taskStore.RunWithOptions("sleep 5", root, background.RunOptions{Kind: "agent", AgentType: "reviewer"})
	require.NoError(t, err)
	longRun, err := runStore.Save(agentruns.Run{ID: "run-" + longTask.ID, Agent: "reviewer", TaskID: longTask.ID, CreatedAt: longTask.StartedAt, UpdatedAt: longTask.StartedAt})
	require.NoError(t, err)
	resp, err = http.Post(server.URL+"/agents/"+longRun.ID+"/stop", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&action))
	require.Equal(t, "stopped", action.Task.Status)
}

func controlAgentBoardContains(board agentruns.Board, id string) bool {
	for _, entries := range [][]agentruns.BoardEntry{board.Active, board.Blocked, board.Finished, board.Orphaned} {
		for _, entry := range entries {
			if entry.Run.ID == id {
				return true
			}
		}
	}
	return false
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
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "messy.go"), []byte("package demo\n\nfunc messy(){return}\n"), 0o644))
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

	resp, err = http.Get(server.URL + "/code/completion?query=Build&limit=5")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"completion"`)
	require.Contains(t, string(body), `"label":"BuildWidget"`)

	resp, err = http.Get(server.URL + "/code/format?path=messy.go")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"format"`)
	require.Contains(t, string(body), `"write":false`)
	require.Contains(t, string(body), `"changed":true`)
	require.Contains(t, string(body), `func messy()`)

	resp, err = http.Post(server.URL+"/code/format", "application/json", bytes.NewBufferString(`{"path":"messy.go","write":true}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"write":true`)
	data, err := os.ReadFile(filepath.Join(workspace, "messy.go"))
	require.NoError(t, err)
	require.Contains(t, string(data), "func messy()")

	resp, err = http.Get(server.URL + "/code/symbols?path=../secret.go")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestControlNotebookReadEdit(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	notebook := `{
  "cells": [
    {"cell_type": "markdown", "id": "intro", "metadata": {}, "source": ["# Title\n"]},
    {"cell_type": "code", "id": "calc", "metadata": {}, "source": ["print(1)\n"], "outputs": [{"output_type": "stream", "name": "stdout", "text": ["1\n"]}], "execution_count": 1}
  ],
  "metadata": {"kernelspec": {"language": "python"}},
  "nbformat": 4,
  "nbformat_minor": 5
}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "analysis.ipynb"), []byte(notebook), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("not a notebook"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "outside.ipynb"), []byte(`{"cells":[]}`), 0o644))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  workspace,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/notebook/read?notebook_path=analysis.ipynb&limit=1&include_outputs=true")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"notebook_read"`)
	require.Contains(t, string(body), `"path":"analysis.ipynb"`)
	require.Contains(t, string(body), `"cell_count":2`)
	require.Contains(t, string(body), `"truncated":true`)

	resp, err = http.Get(server.URL + "/notebook/read?notebook_path=analysis.ipynb&cell_index=1&include_outputs=true")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"cell_id":"calc"`)
	require.Contains(t, string(body), `"output_type":"stream"`)

	resp, err = http.Post(server.URL+"/notebook/edit", "application/json", bytes.NewBufferString(`{"notebook_path":"analysis.ipynb","cell_id":"intro","new_source":"# Renamed\n"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"mode":"replace"`)
	require.Contains(t, string(body), `"cell_id":"intro"`)

	resp, err = http.Post(server.URL+"/notebook/edit", "application/json", bytes.NewBufferString(`{"path":"analysis.ipynb","edit_mode":"insert","cell_id":"intro","cell_type":"markdown","source":"inserted note"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"mode":"insert"`)
	require.Contains(t, string(body), `"cell_type":"markdown"`)

	resp, err = http.Post(server.URL+"/notebook/edit", "application/json", bytes.NewBufferString(`{"path":"analysis.ipynb","mode":"delete","cell_id":"calc"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"mode":"delete"`)
	require.Contains(t, string(body), `"cell_count":2`)

	resp, err = http.Post(server.URL+"/notebook/read", "application/json", bytes.NewBufferString(`{"path":"analysis.ipynb","cell_index":0}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "# Renamed")
	require.NotContains(t, string(body), "print(1)")

	data, err := os.ReadFile(filepath.Join(workspace, "analysis.ipynb"))
	require.NoError(t, err)
	require.Contains(t, string(data), "# Renamed")
	require.Contains(t, string(data), "inserted note")
	require.NotContains(t, string(data), "print(1)")

	resp, err = http.Get(server.URL + "/notebook/read?path=notes.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "notebook path must point to a .ipynb file")

	resp, err = http.Get(server.URL + "/notebook/read?path=../outside.ipynb")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "escapes workspace")

	resp, err = http.Post(server.URL+"/notebook/edit", "application/json", bytes.NewBufferString(`{"path":"analysis.ipynb","mode":"replace"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "new_source is required for insert and replace edits")
}

func TestControlMCPEndpoints(t *testing.T) {
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-1")
			writeControlMCP(t, w, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{},
					"prompts":   map[string]any{},
				},
				"serverInfo": map[string]any{"name": "remote", "version": "1.0.0"},
			})
		case "notifications/initialized":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text over HTTP.",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"content": []map[string]any{{"type": "text", "text": "hi remote"}}})
		case "resources/list":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"resources": []map[string]any{{"uri": "codog://note", "name": "note"}}})
		case "resources/templates/list":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"resourceTemplates": []map[string]any{{
				"uriTemplate": "codog://notes/{name}",
				"name":        "note by name",
			}}})
		case "resources/read":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"contents": []map[string]any{{"uri": "codog://note", "text": "note body"}}})
		case "prompts/list":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"prompts": []map[string]any{{
				"name":        "review",
				"description": "Review a topic.",
			}}})
		case "prompts/get":
			require.Equal(t, "session-1", r.Header.Get("Mcp-Session-Id"))
			writeControlMCP(t, w, id, map[string]any{"messages": []map[string]any{{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": "Review hooks",
				},
			}}})
		default:
			writeControlMCPError(t, w, id, "unsupported method")
		}
	}))
	defer mcpServer.Close()

	controlServer := httptest.NewServer(Server{
		Sessions: &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
		MCPServers: map[string]config.MCPServerConfig{
			"remote": {
				URL:     mcpServer.URL + "/mcp?token=secret",
				Headers: map[string]string{"Authorization": "Bearer token"},
			},
		},
	}.Handler())
	defer controlServer.Close()

	resp, err := http.Get(controlServer.URL + "/mcp/list?inspect=false")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_list"`)
	require.Contains(t, string(body), `"remote"`)
	require.Contains(t, string(body), `"transport":{"id":"http"`)
	require.Contains(t, string(body), "token=%5Bredacted%5D")
	require.NotContains(t, string(body), "secret")
	require.NotContains(t, string(body), `"statuses"`)

	resp, err = http.Get(controlServer.URL + "/mcp/show?server=remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_show"`)
	require.Contains(t, string(body), `"status":"ok"`)
	require.Contains(t, string(body), `"protocol_version":"2024-11-05"`)
	require.NotContains(t, string(body), "secret")

	resp, err = http.Get(controlServer.URL + "/mcp/tools?server=remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_tools"`)
	require.Contains(t, string(body), `"name":"echo"`)

	resp, err = http.Post(controlServer.URL+"/mcp/call", "application/json", bytes.NewBufferString(`{"server":"remote","tool":"echo","arguments":{"text":"hi"}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_call"`)
	require.Contains(t, string(body), "hi remote")

	resp, err = http.Get(controlServer.URL + "/mcp/resources?server=remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_resources"`)
	require.Contains(t, string(body), "codog://note")

	resp, err = http.Get(controlServer.URL + "/mcp/resource-templates?server=remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_resource_templates"`)
	require.Contains(t, string(body), "codog://notes/{name}")

	resp, err = http.Get(controlServer.URL + "/mcp/read?server=remote&uri=codog%3A%2F%2Fnote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_read"`)
	require.Contains(t, string(body), "note body")

	resp, err = http.Get(controlServer.URL + "/mcp/prompts?server=remote")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_prompts"`)
	require.Contains(t, string(body), `"name":"review"`)

	resp, err = http.Post(controlServer.URL+"/mcp/prompt", "application/json", bytes.NewBufferString(`{"server":"remote","prompt":"review","arguments":{"topic":"hooks"}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"mcp_prompt"`)
	require.Contains(t, string(body), "Review hooks")

	resp, err = http.Post(controlServer.URL+"/mcp/call", "application/json", bytes.NewBufferString(`{"server":"remote"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "tool is required")

	resp, err = http.Get(controlServer.URL + "/mcp/tools?server=missing")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "missing")
}

func TestControlLSPEndpoints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  workspace,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/lsp/actions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_actions"`)
	require.Contains(t, string(body), `"name":"hover"`)

	resp, err = http.Get(server.URL + "/lsp/discover")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_discover"`)
	require.Contains(t, string(body), `"language":"go"`)

	resp, err = http.Post(server.URL+"/lsp/start", "application/json", bytes.NewBufferString(`{"language":"go","command_args":["sleep","30"]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_start"`)
	require.Contains(t, string(body), `"status":"ok"`)
	require.Contains(t, string(body), `"language":"go"`)
	t.Cleanup(func() {
		_, _ = http.Post(server.URL+"/lsp/stop", "application/json", bytes.NewBufferString(`{"language":"go"}`))
	})

	resp, err = http.Get(server.URL + "/lsp/list")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_list"`)
	require.Contains(t, string(body), `"count":1`)
	require.Contains(t, string(body), `"status":"running"`)

	resp, err = http.Get(server.URL + "/lsp/status?language=go")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_status"`)
	require.Contains(t, string(body), `"language":"go"`)

	resp, err = http.Post(server.URL+"/lsp/query", "application/json", bytes.NewBufferString(`{"language":"go","action":"hover","line":-1}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "line must be non-negative")

	resp, err = http.Post(server.URL+"/lsp/stop", "application/json", bytes.NewBufferString(`{"language":"go"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kind":"lsp_stop"`)
	require.Contains(t, string(body), `"status":"stopped"`)
}

func TestControlLSPRequiresConfigHome(t *testing.T) {
	server := httptest.NewServer(Server{
		Sessions:  &session.Store{Dir: filepath.Join(t.TempDir(), "sessions")},
		Workspace: t.TempDir(),
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/lsp/list")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "config home is required")
}

func writeControlMCP(t *testing.T, w http.ResponseWriter, id any, result map[string]any) {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	_, err = fmt.Fprintln(w, string(data))
	require.NoError(t, err)
}

func writeControlMCPError(t *testing.T, w http.ResponseWriter, id any, message string) {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": message}}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	_, err = fmt.Fprintln(w, string(data))
	require.NoError(t, err)
}

func TestControlWorkspaceAndFileOperations(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "internal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Codog\n\nhello remote\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "internal", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o644))
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  workspace,
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/workspace/info")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"name":"workspace"`)

	resp, err = http.Get(server.URL + "/workspace/files?pattern=*.md")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"path":"README.md"`)

	resp, err = http.Get(server.URL + "/workspace/search?query=remote&glob=*.md")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"text":"hello remote"`)

	resp, err = http.Post(server.URL+"/file/write", "application/json", bytes.NewBufferString(`{"path":"notes.txt","content":"hello world"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"bytes":11`)

	resp, err = http.Post(server.URL+"/file/edit", "application/json", bytes.NewBufferString(`{"path":"notes.txt","old_string":"world","new_string":"codog"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"replacements":1`)

	resp, err = http.Get(server.URL + "/file/read?path=notes.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"content":"hello codog"`)

	resp, err = http.Post(server.URL+"/file/diff", "application/json", bytes.NewBufferString(`{"path":"README.md","old_string":"hello remote","new_string":"hello codog"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `-hello remote`)
	require.Contains(t, string(body), `+hello codog`)

	resp, err = http.Get(server.URL + "/file/read?path=../secret.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
