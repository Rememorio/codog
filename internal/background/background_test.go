package background

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
