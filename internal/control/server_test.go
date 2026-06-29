package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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
