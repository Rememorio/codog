package agentruns

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreSavesListsGetsAndRemovesRuns(t *testing.T) {
	store := NewStore(t.TempDir())
	created := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)

	run, err := store.Save(Run{
		ID:        "run-1",
		Agent:     "reviewer",
		Prompt:    "check auth",
		Workspace: "workspace",
		SessionID: "session-1",
		TaskID:    "task-1",
		CreatedAt: created,
		UpdatedAt: created,
	})
	require.NoError(t, err)
	require.Equal(t, "run-1", run.ID)

	got, err := store.Get("run-1")
	require.NoError(t, err)
	require.Equal(t, "reviewer", got.Agent)
	require.Equal(t, "check auth", got.Prompt)

	runs, err := store.List()
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "run-1", runs[0].ID)

	require.NoError(t, store.Remove("run-1"))
	runs, err = store.List()
	require.NoError(t, err)
	require.Empty(t, runs)
}

func TestStoreRejectsInvalidRuns(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Save(Run{ID: "../bad", TaskID: "task-1"})
	require.ErrorContains(t, err, "single path component")

	_, err = store.Save(Run{ID: "run-1"})
	require.ErrorContains(t, err, "task id is required")
}
