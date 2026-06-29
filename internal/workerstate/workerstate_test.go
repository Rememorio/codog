package workerstate

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSaveLoadAndRenderState(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 6, 29, 1, 2, 3, 0, time.UTC)
	state := New(Options{
		WorkerID:       "worker-1",
		Version:        "test-version",
		Mode:           "repl",
		Status:         "idle",
		Workspace:      workspace,
		SessionID:      "session-1",
		SessionPath:    "session.jsonl",
		Model:          "claude-test",
		PermissionMode: "workspace-write",
		PID:            1234,
		Now:            now,
	})

	require.NoError(t, Save(workspace, state))
	require.FileExists(t, Path(workspace))

	loaded, err := Load(workspace)
	require.NoError(t, err)
	require.Equal(t, "worker_state", loaded.Kind)
	require.Equal(t, "worker-1", loaded.WorkerID)
	require.Equal(t, "session-1", loaded.SessionID)
	require.Equal(t, now, loaded.UpdatedAt)

	var out bytes.Buffer
	RenderText(&out, loaded)
	require.Contains(t, out.String(), "State")
	require.Contains(t, out.String(), "Worker           worker-1")
	require.Contains(t, out.String(), "Status           idle")
	require.Contains(t, out.String(), "Session          session-1")
}

func TestLoadMissingStateReturnsActionableError(t *testing.T) {
	_, err := Load(t.TempDir())

	require.Error(t, err)
	var missing MissingError
	require.True(t, errors.As(err, &missing))
	require.Contains(t, err.Error(), "codog prompt <text>")
	require.Contains(t, err.Error(), "codog state")
}

func TestNewDefaults(t *testing.T) {
	state := New(Options{Mode: "prompt", PID: 99})

	require.Equal(t, "worker_state", state.Kind)
	require.Equal(t, "codog-prompt-99", state.WorkerID)
	require.Equal(t, "idle", state.Status)
	require.Equal(t, 99, state.PID)
	require.False(t, state.UpdatedAt.IsZero())
}
