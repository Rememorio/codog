package agentruns

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestStoreTrimsIDsAndListsDeterministically(t *testing.T) {
	store := NewStore(t.TempDir())
	created := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)

	trimmed, err := store.Save(Run{ID: "  run-b  ", TaskID: "  task-b  ", CreatedAt: created})
	require.NoError(t, err)
	require.Equal(t, "run-b", trimmed.ID)
	require.Equal(t, "task-b", trimmed.TaskID)
	_, err = store.Save(Run{ID: "run-a", TaskID: "task-a", CreatedAt: created})
	require.NoError(t, err)
	_, err = store.Save(Run{ID: "old", TaskID: "task-old", CreatedAt: created.Add(-time.Hour)})
	require.NoError(t, err)

	runs, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"run-a", "run-b", "old"}, []string{runs[0].ID, runs[1].ID, runs[2].ID})
	require.FileExists(t, filepath.Join(store.Dir, "run-b.json"))
	require.NoFileExists(t, filepath.Join(store.Dir, "  run-b  .json"))
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
		Provenance: background.EventProvenance{
			SourceKind:  "replay",
			Environment: "test",
			Channel:     "harness",
			Emitter:     "unit-test",
			Confidence:  "high",
		},
	})
	require.NoError(t, err)
	run, err := runStore.Save(Run{ID: "run-" + task.ID, Agent: "reviewer", TaskID: task.ID, CreatedAt: now, UpdatedAt: now})
	require.NoError(t, err)

	status := StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessHealthy, status.Freshness)
	require.NotNil(t, status.Heartbeat)
	require.Equal(t, "codog", status.ScopeBinding.WorkflowScope)
	require.True(t, status.ScopeBinding.Actionable)
	require.Equal(t, "replay", status.Provenance.SourceKind)
	require.Equal(t, "unit-test", status.Provenance.Emitter)
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
	require.NotNil(t, status.TerminalOutcome)
	require.Equal(t, "stopped", status.TerminalOutcome.Status)
	require.Equal(t, "finished", status.Health.State)

	board := BuildBoard(taskStore, []Run{run}, now, 30*time.Second)
	require.Len(t, board.Finished, 1)
	require.Equal(t, "stopped", board.Finished[0].Status)
	require.Equal(t, "live_lane", board.Finished[0].Provenance.SourceKind)
	require.True(t, board.Finished[0].Lifecycle.Terminal)
	require.Equal(t, "canonical_terminal_status", board.Finished[0].Lifecycle.Reason)
	require.NotNil(t, board.Finished[0].TerminalOutcome)
	require.NotEmpty(t, board.Finished[0].TerminalOutcome.Fingerprint)

	orphan := StatusForTaskAt(taskStore, Run{ID: "run-orphan", Agent: "reviewer", TaskID: "missing-task"}, now, 30*time.Second)
	require.Equal(t, background.LaneFreshnessUnknown, orphan.Freshness)
	require.Equal(t, "orphaned", orphan.Health.State)
	require.NotEmpty(t, orphan.Error)
}

func TestStatusForTaskUsesDefaultFreshnessWindow(t *testing.T) {
	configHome := t.TempDir()
	taskStore := background.NewStore(configHome)
	now := time.Now().UTC()
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:      "task-default-window",
		Status:  "running",
		PID:     os.Getpid(),
		LogPath: filepath.Join(taskStore.Dir, "task-default-window.log"),
		Heartbeat: &background.LaneHeartbeat{
			ObservedAt:     now.Add(-time.Minute),
			TransportAlive: true,
			Status:         "running",
		},
	}))

	status := StatusForTask(taskStore, Run{ID: "run-default-window", TaskID: "task-default-window"})
	require.Equal(t, background.LaneFreshnessStalled, status.Freshness)
	require.Equal(t, "stalled", status.Health.State)
}

func TestStoppingAgentRunRemainsActive(t *testing.T) {
	configHome := t.TempDir()
	taskStore := background.NewStore(configHome)
	now := time.Now().UTC()
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:        "task-stopping",
		Status:    "stopping",
		StartedAt: now.Add(-time.Minute),
		LogPath:   filepath.Join(configHome, "stopping.log"),
	}))
	run := Run{ID: "run-stopping", Agent: "reviewer", TaskID: "task-stopping", CreatedAt: now}

	status := StatusForTaskAt(taskStore, run, now, 30*time.Second)
	require.Equal(t, "stopping", status.CurrentStatus)
	require.False(t, status.Lifecycle.Terminal)
	require.Equal(t, "heartbeat_unknown", status.Health.State)

	board := BuildBoard(taskStore, []Run{run}, now, 30*time.Second)
	require.Len(t, board.Active, 1)
	require.Empty(t, board.Finished)
}

func TestPruneRemovesOldCompletedAndOrphanedRuns(t *testing.T) {
	configHome := t.TempDir()
	runStore := NewStore(configHome)
	taskStore := background.NewStore(configHome)
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	newer := now.Add(-24 * time.Hour)
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:          "task-old",
		Status:      "completed",
		StartedAt:   old.Add(-time.Minute),
		CompletedAt: &old,
		LogPath:     filepath.Join(taskStore.Dir, "task-old.log"),
	}))
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:          "task-newer",
		Status:      "completed",
		StartedAt:   newer.Add(-time.Minute),
		CompletedAt: &newer,
		LogPath:     filepath.Join(taskStore.Dir, "task-newer.log"),
	}))
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:        "task-running",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: old,
		LogPath:   filepath.Join(taskStore.Dir, "task-running.log"),
	}))
	for _, run := range []Run{
		{ID: "run-old", TaskID: "task-old", CreatedAt: old, UpdatedAt: old},
		{ID: "run-newer", TaskID: "task-newer", CreatedAt: newer, UpdatedAt: newer},
		{ID: "run-running", TaskID: "task-running", CreatedAt: old, UpdatedAt: old},
		{ID: "run-orphan", TaskID: "missing-task", CreatedAt: now, UpdatedAt: now},
	} {
		_, err := runStore.Save(run)
		require.NoError(t, err)
	}

	result, err := Prune(runStore, taskStore, background.PruneOptions{OlderThan: 48 * time.Hour, Keep: 1})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"run-old", "run-orphan"}, result.Removed)
	require.Equal(t, 2, result.RemovedCount)
	require.Equal(t, 2, result.Kept)
	require.NoFileExists(t, filepath.Join(runStore.Dir, "run-old.json"))
	require.NoFileExists(t, filepath.Join(runStore.Dir, "run-orphan.json"))
	require.FileExists(t, filepath.Join(runStore.Dir, "run-newer.json"))
	require.FileExists(t, filepath.Join(runStore.Dir, "run-running.json"))
}

func TestBuildBoardGroupsBlockedAndOrphanedRuns(t *testing.T) {
	configHome := t.TempDir()
	taskStore := background.NewStore(configHome)
	now := time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	require.NoError(t, writeTask(taskStore, background.Task{
		ID:        "task-blocked",
		Status:    "waiting",
		StartedAt: now.Add(-time.Minute),
		LogPath:   filepath.Join(taskStore.Dir, "task-blocked.log"),
		Heartbeat: &background.LaneHeartbeat{
			ObservedAt:     now.Add(-5 * time.Second),
			TransportAlive: true,
			Status:         "waiting",
		},
	}))

	board := BuildBoard(taskStore, []Run{
		{ID: "run-blocked", TaskID: "task-blocked"},
		{ID: "run-orphan", TaskID: "missing-task"},
	}, now, time.Minute)
	require.Len(t, board.Blocked, 1)
	require.Equal(t, "run-blocked", board.Blocked[0].Run.ID)
	require.Equal(t, background.LaneFreshnessHealthy, board.Blocked[0].Freshness)
	require.Len(t, board.Orphaned, 1)
	require.Equal(t, "run-orphan", board.Orphaned[0].Run.ID)
	require.NotEmpty(t, board.Orphaned[0].Error)
}

func writeTask(store background.Store, task background.Task) error {
	if err := os.MkdirAll(store.Dir, 0o755); err != nil {
		return err
	}
	if task.LogPath != "" {
		if err := os.WriteFile(task.LogPath, nil, 0o644); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(store.Dir, task.ID+".json"), append(data, '\n'), 0o644)
}
