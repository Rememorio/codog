package agentruns

import (
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/background"
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

	touched, err := store.Touch("run-1")
	require.NoError(t, err)
	require.False(t, touched.UpdatedAt.IsZero())

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

func TestStatusForTaskReportsHealth(t *testing.T) {
	configHome := t.TempDir()
	taskStore := background.NewStore(configHome)
	runStore := NewStore(configHome)
	workspace := t.TempDir()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	task, err := taskStore.RunWithOptions("sleep 60", workspace, background.RunOptions{Kind: "agent", AgentType: "reviewer"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = taskStore.Stop(task.ID)
	})
	_, err = taskStore.UpdateHeartbeat(task.ID, background.LaneHeartbeat{
		ObservedAt:     now.Add(-5 * time.Second),
		TransportAlive: true,
		Status:         "working",
	})
	require.NoError(t, err)
	run, err := runStore.Save(Run{ID: "run-" + task.ID, Agent: "reviewer", TaskID: task.ID, CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)

	status := StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessHealthy, status.Freshness)
	require.NotNil(t, status.Heartbeat)
	require.Equal(t, "healthy", status.Health.State)
	require.NotEmpty(t, status.Health.Summary)

	_, err = taskStore.UpdateHeartbeat(task.ID, background.LaneHeartbeat{
		ObservedAt:     now.Add(-2 * time.Minute),
		TransportAlive: true,
		Status:         "working",
	})
	require.NoError(t, err)
	status = StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessStalled, status.Freshness)
	require.Equal(t, "stalled", status.Health.State)
	require.Contains(t, status.Health.RecommendedAction, "logs")

	_, err = taskStore.UpdateHeartbeat(task.ID, background.LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: false,
		Status:         "disconnected",
	})
	require.NoError(t, err)
	status = StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessTransportDead, status.Freshness)
	require.False(t, status.Lifecycle.Terminal)
	require.True(t, status.Lifecycle.TerminalStateUnknown)
	require.Equal(t, "transport_dead", status.Health.State)

	stopped, err := taskStore.Stop(task.ID)
	require.NoError(t, err)
	_, err = taskStore.UpdateHeartbeat(stopped.ID, background.LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: false,
		Status:         "disconnected",
	})
	require.NoError(t, err)
	status = StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, "stopped", status.CurrentStatus)
	require.Equal(t, background.LaneFreshnessTransportDead, status.Freshness)
	require.True(t, status.Lifecycle.Terminal)
	require.False(t, status.Lifecycle.TerminalStateUnknown)
	require.Equal(t, "finished", status.Health.State)

	board := BuildBoard(taskStore, []Run{run}, now, 30*time.Second)
	require.Len(t, board.Finished, 1)
	require.Equal(t, "stopped", board.Finished[0].Status)
	require.True(t, board.Finished[0].Lifecycle.Terminal)
	require.Equal(t, "canonical_terminal_status", board.Finished[0].Lifecycle.Reason)

	orphan := StatusForTaskAt(taskStore, Run{ID: "run-orphan", Agent: "reviewer", TaskID: "missing-task"}, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessUnknown, orphan.Freshness)
	require.Equal(t, "orphaned", orphan.Health.State)
	require.NotEmpty(t, orphan.Error)
}
