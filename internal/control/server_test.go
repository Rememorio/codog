package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestControlBackgroundLifecycle(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(Server{
		Sessions:   &session.Store{Dir: filepath.Join(root, "sessions")},
		ConfigHome: filepath.Join(root, "home"),
		Workspace:  root,
	}.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/background", "application/json", bytes.NewBufferString(`{"command":"echo remote"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var task struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	require.NotEmpty(t, task.ID)

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
