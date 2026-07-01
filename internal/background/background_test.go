package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogsReturnsTail(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	logPath := filepath.Join(store.Dir, "task.log")
	require.NoError(t, os.WriteFile(logPath, []byte("hello codog"), 0o644))
	require.NoError(t, store.save(Task{ID: "task", Status: "completed", LogPath: logPath}))

	logs, err := store.Logs("task", 5)
	require.NoError(t, err)
	require.Equal(t, "codog", logs)
}

func TestLogRangeReturnsBoundedChunkFromOffset(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	logPath := filepath.Join(store.Dir, "task.log")
	require.NoError(t, os.WriteFile(logPath, []byte("alpha beta gamma"), 0o644))
	require.NoError(t, store.save(Task{ID: "task", Status: "completed", LogPath: logPath}))

	nextOffset, logs, err := store.LogRange("task", 6, 4)
	require.NoError(t, err)
	require.Equal(t, int64(10), nextOffset)
	require.Equal(t, "beta", logs)

	nextOffset, logs, err = store.LogRange("task", 10_000, 5)
	require.NoError(t, err)
	require.Equal(t, int64(5), nextOffset)
	require.Equal(t, "alpha", logs)
}

func TestWatchEmitsStatusAndLogEvents(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	logPath := filepath.Join(store.Dir, "task.log")
	require.NoError(t, os.WriteFile(logPath, []byte("hello watch"), 0o644))
	require.NoError(t, store.save(Task{ID: "task", Status: "completed", LogPath: logPath}))

	var events []WatchEvent
	err := store.Watch(context.Background(), "task", WatchOptions{}, func(event WatchEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "status", events[0].Type)
	require.Equal(t, "completed", events[0].Status)
	require.Equal(t, "log", events[1].Type)
	require.Equal(t, "hello watch", events[1].Data)
	require.Equal(t, int64(len("hello watch")), events[1].Offset)
}

func TestStopRunningTask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	store := Store{Dir: t.TempDir()}
	task, err := store.Run("sleep 30", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Stop(task.ID) })

	stopped, err := store.Stop(task.ID)
	require.NoError(t, err)
	require.Equal(t, "stopped", stopped.Status)
	require.NotNil(t, stopped.CompletedAt)
}

func TestRunRecordsExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	task, err := store.Run("exit 7", t.TempDir())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		status, err := store.Status(task.ID)
		return err == nil && status.Status == "failed" && status.ExitCode != nil && *status.ExitCode == 7
	}, 2*time.Second, 50*time.Millisecond)
}

func TestRestartTaskReusesCommandAndWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	workspace := t.TempDir()
	policy := &RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 3}
	task, err := store.RunWithOptions("pwd", workspace, RunOptions{Kind: "terminal", SessionID: "session-1", RestartPolicy: policy})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := store.Status(task.ID)
		return err == nil && status.Status != "running"
	}, 2*time.Second, 50*time.Millisecond)

	restarted, err := store.Restart(task.ID, "")
	require.NoError(t, err)
	require.NotEqual(t, task.ID, restarted.ID)
	require.Equal(t, task.ID, restarted.RestartedFrom)
	require.Equal(t, task.Command, restarted.Command)
	require.Equal(t, "terminal", restarted.Kind)
	require.Equal(t, workspace, restarted.Workspace)
	require.Equal(t, "session-1", restarted.SessionID)
	require.Equal(t, policy, restarted.RestartPolicy)
	source, err := store.Get(task.ID)
	require.NoError(t, err)
	require.Equal(t, restarted.ID, source.RestartedBy)

	require.Eventually(t, func() bool {
		logs, err := store.Logs(restarted.ID, 1024)
		return err == nil && strings.Contains(logs, workspace)
	}, 2*time.Second, 50*time.Millisecond)
}

func TestListRefreshesExitedRunningTask(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	require.NoError(t, store.save(Task{
		ID:        "missing",
		PID:       -1,
		Status:    "running",
		StartedAt: now,
		LogPath:   filepath.Join(store.Dir, "missing.log"),
	}))

	tasks, err := store.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "exited", tasks[0].Status)
}

func TestUpdateAppendsTaskMessage(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "completed",
		StartedAt: now,
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	task, err := store.Update("task", "first update")
	require.NoError(t, err)
	require.Len(t, task.Messages, 1)
	require.Equal(t, "first update", task.Messages[0].Message)

	task, err = store.Get("task")
	require.NoError(t, err)
	require.Len(t, task.Messages, 1)
	require.Equal(t, "first update", task.Messages[0].Message)
}

func TestPruneRemovesOldCompletedTasksAndKeepsRunning(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	oldCompleted := now.Add(-48 * time.Hour)
	recentCompleted := now.Add(-1 * time.Hour)
	oldLog := filepath.Join(store.Dir, "old.log")
	recentLog := filepath.Join(store.Dir, "recent.log")
	runningLog := filepath.Join(store.Dir, "running.log")
	require.NoError(t, os.WriteFile(oldLog, []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(recentLog, []byte("recent"), 0o644))
	require.NoError(t, os.WriteFile(runningLog, []byte("running"), 0o644))
	require.NoError(t, store.save(Task{
		ID:          "old",
		Status:      "completed",
		StartedAt:   oldCompleted.Add(-time.Minute),
		CompletedAt: &oldCompleted,
		LogPath:     oldLog,
	}))
	require.NoError(t, store.save(Task{
		ID:          "recent",
		Status:      "completed",
		StartedAt:   recentCompleted.Add(-time.Minute),
		CompletedAt: &recentCompleted,
		LogPath:     recentLog,
	}))
	require.NoError(t, store.save(Task{
		ID:        "running",
		PID:       os.Getpid(),
		Status:    "running",
		StartedAt: oldCompleted,
		LogPath:   runningLog,
	}))

	result, err := store.Prune(PruneOptions{OlderThan: 24 * time.Hour})
	require.NoError(t, err)
	require.Equal(t, []string{"old"}, result.Removed)
	require.Equal(t, 1, result.RemovedCount)
	require.Equal(t, 2, result.Kept)
	require.NoFileExists(t, oldLog)
	require.NoFileExists(t, filepath.Join(store.Dir, "old.json"))
	require.FileExists(t, recentLog)
	require.FileExists(t, filepath.Join(store.Dir, "recent.json"))
	require.FileExists(t, runningLog)
	require.FileExists(t, filepath.Join(store.Dir, "running.json"))
}

func TestPruneKeepsNewestCompletedTasks(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	newerCompleted := now.Add(-48 * time.Hour)
	olderCompleted := now.Add(-72 * time.Hour)
	for id, completed := range map[string]time.Time{
		"newer": newerCompleted,
		"older": olderCompleted,
	} {
		logPath := filepath.Join(store.Dir, id+".log")
		require.NoError(t, os.WriteFile(logPath, []byte(id), 0o644))
		require.NoError(t, store.save(Task{
			ID:          id,
			Status:      "completed",
			StartedAt:   completed.Add(-time.Minute),
			CompletedAt: &completed,
			LogPath:     logPath,
		}))
	}

	result, err := store.Prune(PruneOptions{OlderThan: 24 * time.Hour, Keep: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"older"}, result.Removed)
	require.FileExists(t, filepath.Join(store.Dir, "newer.json"))
	require.NoFileExists(t, filepath.Join(store.Dir, "older.json"))
}

func TestFilterBySession(t *testing.T) {
	tasks := []Task{
		{ID: "one", SessionID: "session-1"},
		{ID: "two", SessionID: "session-2"},
		{ID: "three"},
	}

	require.Equal(t, tasks, FilterBySession(tasks, ""))
	require.Equal(t, []Task{{ID: "one", SessionID: "session-1"}}, FilterBySession(tasks, "session-1"))
}

func TestFilterByKind(t *testing.T) {
	tasks := []Task{
		{ID: "one", Kind: "terminal"},
		{ID: "two", Kind: "agent"},
		{ID: "three"},
	}

	require.Equal(t, tasks, FilterByKind(tasks, ""))
	require.Equal(t, []Task{{ID: "one", Kind: "terminal"}}, FilterByKind(tasks, "terminal"))
}

func TestSuperviseOnceRestartsFailedTaskWithPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	workspace := t.TempDir()
	completed := time.Now().UTC().Add(-time.Minute)
	logPath := filepath.Join(store.Dir, "failed.log")
	policy := &RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 2}
	require.NoError(t, os.WriteFile(logPath, []byte("failed"), 0o644))
	require.NoError(t, store.save(Task{
		ID:            "failed",
		Command:       "echo supervised",
		Workspace:     workspace,
		SessionID:     "session-1",
		RestartPolicy: policy,
		Status:        "failed",
		StartedAt:     completed.Add(-time.Minute),
		CompletedAt:   &completed,
		LogPath:       logPath,
	}))

	result, err := store.SuperviseOnce(time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, result.Restarted, 1)
	restarted := result.Restarted[0]
	require.Equal(t, "failed", restarted.RestartedFrom)
	require.Equal(t, "session-1", restarted.SessionID)
	require.Equal(t, 1, restarted.RestartCount)
	require.Equal(t, policy, restarted.RestartPolicy)
	source, err := store.Get("failed")
	require.NoError(t, err)
	require.Equal(t, restarted.ID, source.RestartedBy)

	again, err := store.SuperviseOnce(time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, again.Restarted)
	require.Contains(t, again.Skipped, SuperviseSkip{ID: "failed", Reason: "restarted"})
}

func TestSuperviseOnceHonorsDelayAndMaxAttempts(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	for _, task := range []Task{
		{
			ID:            "maxed",
			Command:       "echo maxed",
			Status:        "failed",
			StartedAt:     now.Add(-time.Hour),
			CompletedAt:   &now,
			LogPath:       filepath.Join(store.Dir, "maxed.log"),
			RestartPolicy: &RestartPolicy{Enabled: true, MaxAttempts: 1},
			RestartCount:  1,
		},
		{
			ID:            "delayed",
			Command:       "echo delayed",
			Status:        "failed",
			StartedAt:     now.Add(-time.Hour),
			CompletedAt:   &now,
			LogPath:       filepath.Join(store.Dir, "delayed.log"),
			RestartPolicy: &RestartPolicy{Enabled: true, DelaySeconds: 60},
		},
	} {
		require.NoError(t, os.WriteFile(task.LogPath, []byte(task.ID), 0o644))
		require.NoError(t, store.save(task))
	}

	result, err := store.SuperviseOnce(now)
	require.NoError(t, err)
	require.Empty(t, result.Restarted)
	require.ElementsMatch(t, []SuperviseSkip{
		{ID: "maxed", Reason: "max_attempts"},
		{ID: "delayed", Reason: "delay"},
	}, result.Skipped)
}
