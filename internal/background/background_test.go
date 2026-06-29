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

func TestRestartTaskReusesCommandAndWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	workspace := t.TempDir()
	task, err := store.Run("pwd", workspace)
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
	require.Equal(t, workspace, restarted.Workspace)

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
