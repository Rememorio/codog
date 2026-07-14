package background

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestStoreDefaultsAndTaskIDValidation(t *testing.T) {
	configHome := t.TempDir()
	store := NewStore(configHome)
	require.Equal(t, filepath.Join(configHome, "background"), store.Dir)
	require.Equal(t, PruneOptions{OlderThan: 30 * 24 * time.Hour, Keep: 100}, DefaultPruneOptions())

	require.ErrorContains(t, store.save(Task{ID: "../outside", Status: "completed"}), "single path component")
	require.NoFileExists(t, filepath.Join(configHome, "outside.json"))
	_, err := store.Get("../outside")
	require.ErrorContains(t, err, "single path component")
	_, err = store.Get(" ")
	require.ErrorContains(t, err, "task id is required")
}

func TestLateMutationDoesNotRecreateRemovedStore(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "background")}
	require.NoError(t, store.save(Task{ID: "task", Status: "running"}))
	require.NoError(t, os.RemoveAll(store.Dir))

	_, err := store.mutateTask("task", func(task *Task) (bool, error) {
		task.Status = "completed"
		return true, nil
	})
	require.Error(t, err)
	require.NoDirExists(t, store.Dir)
}

func TestListSortsTasksDeterministically(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	started := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.save(Task{ID: "same-b", Status: "completed", StartedAt: started, LogPath: filepath.Join(store.Dir, "same-b.log")}))
	require.NoError(t, store.save(Task{ID: "same-a", Status: "completed", StartedAt: started, LogPath: filepath.Join(store.Dir, "same-a.log")}))
	require.NoError(t, store.save(Task{ID: "old", Status: "completed", StartedAt: started.Add(-time.Hour), LogPath: filepath.Join(store.Dir, "old.log")}))

	tasks, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"same-a", "same-b", "old"}, []string{tasks[0].ID, tasks[1].ID, tasks[2].ID})
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

func TestLogFromReadsFromOffsetWithoutLimit(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	logPath := filepath.Join(store.Dir, "task.log")
	require.NoError(t, os.WriteFile(logPath, []byte("alpha beta gamma"), 0o644))
	require.NoError(t, store.save(Task{ID: "task", Status: "completed", LogPath: logPath}))

	nextOffset, logs, err := store.LogFrom("task", 6)
	require.NoError(t, err)
	require.Equal(t, int64(len("alpha beta gamma")), nextOffset)
	require.Equal(t, "beta gamma", logs)
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
	require.Equal(t, EventProvenance{
		SourceKind:  "live_lane",
		Environment: "local",
		Channel:     "local",
		Emitter:     "codog",
		Confidence:  "medium",
	}, events[0].Provenance)
	require.Equal(t, "log", events[1].Type)
	require.Equal(t, "hello watch", events[1].Data)
	require.Equal(t, int64(len("hello watch")), events[1].Offset)
	require.Equal(t, "live_lane", events[1].Provenance.SourceKind)
}

func TestWatchWaitsForStoppingTaskTerminalState(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.save(Task{
		ID:        "task",
		Status:    "stopping",
		StartedAt: time.Now().UTC(),
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))
	go func() {
		time.Sleep(25 * time.Millisecond)
		_, _ = store.mutateTask("task", func(task *Task) (bool, error) {
			now := time.Now().UTC()
			task.Status = "stopped"
			task.CompletedAt = &now
			return true, nil
		})
	}()

	var events []WatchEvent
	err := store.Watch(context.Background(), "task", WatchOptions{Interval: 5 * time.Millisecond, MaxEvents: 2}, func(event WatchEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "stopping", events[0].Status)
	require.Equal(t, "stopped", events[1].Status)
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
	require.Eventually(t, func() bool {
		persisted, err := store.Get(task.ID)
		return err == nil && persisted.Status == "stopped"
	}, time.Second, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	persisted, err := store.Get(task.ID)
	require.NoError(t, err)
	require.Equal(t, "stopped", persisted.Status)
	require.Empty(t, persisted.Error)
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

func TestWaitReturnsAfterCompletionBookkeeping(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	task, err := store.Run("printf done", t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	completed, err := store.Wait(ctx, task.ID)

	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.NoFileExists(t, store.completionPath(task.ID))
}

func TestWaitHonorsContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	store := Store{Dir: t.TempDir()}
	task, err := store.Run("sleep 30", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = store.Stop(task.ID)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = store.Wait(ctx, task.ID)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Wait(ctx, task.ID)

	require.ErrorIs(t, err, context.Canceled)
}

func TestRestartTaskReusesCommandAndWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sh")
	}
	store := Store{Dir: t.TempDir()}
	workspace := t.TempDir()
	policy := &RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 3}
	scope := ScopeBinding{Owner: "release-bot", WorkflowScope: "external-git-maintenance", WatcherAction: "observe"}
	task, err := store.RunWithOptions("pwd", workspace, RunOptions{Kind: "terminal", SessionID: "session-1", RestartPolicy: policy, ScopeBinding: scope})
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
	require.Equal(t, "release-bot", restarted.ScopeBinding.Owner)
	require.Equal(t, "external-git-maintenance", restarted.ScopeBinding.WorkflowScope)
	require.Equal(t, "observe", restarted.ScopeBinding.WatcherAction)
	require.False(t, restarted.ScopeBinding.Actionable)
	source, err := store.Get(task.ID)
	require.NoError(t, err)
	require.Equal(t, restarted.ID, source.RestartedBy)

	require.Eventually(t, func() bool {
		logs, err := store.Logs(restarted.ID, 1024)
		return err == nil && strings.Contains(logs, workspace)
	}, 2*time.Second, 50*time.Millisecond)
}

func TestScopeBindingPropagatesToBoardAndWatchEvents(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	logPath := filepath.Join(store.Dir, "task.log")
	now := time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(logPath, []byte("scope event"), 0o644))
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo scope",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: now,
		LogPath:   logPath,
		ScopeBinding: ScopeBinding{
			Owner:         "infra-bot",
			WorkflowScope: "infra-health",
			WatcherAction: "ignore",
		},
	}))

	board, err := store.LaneBoardAt(now, time.Minute)
	require.NoError(t, err)
	require.Len(t, board.Active, 1)
	require.Equal(t, "infra-bot", board.Active[0].ScopeBinding.Owner)
	require.Equal(t, "infra-health", board.Active[0].ScopeBinding.WorkflowScope)
	require.Equal(t, "ignore", board.Active[0].ScopeBinding.WatcherAction)
	require.False(t, board.Active[0].ScopeBinding.Actionable)

	var events []WatchEvent
	err = store.Watch(context.Background(), "task", WatchOptions{MaxEvents: 2}, func(event WatchEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "infra-health", events[0].ScopeBinding.WorkflowScope)
	require.Equal(t, "ignore", events[1].ScopeBinding.WatcherAction)
	require.False(t, events[1].ScopeBinding.Actionable)
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

func TestStatusReconcilesDetachedCompletionRecord(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	startedAt := time.Now().UTC().Add(-time.Minute)
	for _, testCase := range []struct {
		id         string
		status     string
		exitCode   string
		wantStatus string
		wantCode   int
	}{
		{id: "success", status: "running", exitCode: "0\n", wantStatus: "completed", wantCode: 0},
		{id: "failure", status: "exited", exitCode: "7\n", wantStatus: "failed", wantCode: 7},
	} {
		t.Run(testCase.id, func(t *testing.T) {
			require.NoError(t, store.save(Task{
				ID:        testCase.id,
				Command:   "detached command",
				Status:    testCase.status,
				PID:       -1,
				StartedAt: startedAt,
				LogPath:   filepath.Join(store.Dir, testCase.id+".log"),
			}))
			require.NoError(t, os.WriteFile(store.completionPath(testCase.id), []byte(testCase.exitCode), 0o600))

			task, err := store.Status(testCase.id)
			require.NoError(t, err)
			require.Equal(t, testCase.wantStatus, task.Status)
			require.NotNil(t, task.ExitCode)
			require.Equal(t, testCase.wantCode, *task.ExitCode)
			require.NotNil(t, task.CompletedAt)
			if testCase.wantCode == 0 {
				require.Empty(t, task.Error)
			} else {
				require.Equal(t, "exit status 7", task.Error)
			}
			require.NoFileExists(t, store.completionPath(testCase.id))
			require.NotNil(t, task.TerminalOutcome)
			require.Equal(t, testCase.wantStatus, task.TerminalOutcome.Status)
		})
	}
}

func TestListMissingStoreReturnsEmpty(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "missing")}

	tasks, err := store.List()
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.NoDirExists(t, store.Dir)
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

func TestConcurrentUpdatesPreserveAllMessages(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "completed",
		StartedAt: time.Now().UTC(),
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	const updateCount = 64
	errs := make(chan error, updateCount)
	var wg sync.WaitGroup
	for index := range updateCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Update("task", fmt.Sprintf("update-%02d", index))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	task, err := store.Get("task")
	require.NoError(t, err)
	require.Len(t, task.Messages, updateCount)
	seen := make(map[string]bool, updateCount)
	for _, message := range task.Messages {
		seen[message.Message] = true
	}
	for index := range updateCount {
		require.True(t, seen[fmt.Sprintf("update-%02d", index)])
	}
}

func TestCrossProcessUpdatesPreserveAllMessages(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "completed",
		StartedAt: time.Now().UTC(),
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))
	readyDir := t.TempDir()
	barrier := filepath.Join(t.TempDir(), "start")

	const updateCount = 8
	type helperProcess struct {
		cmd    *exec.Cmd
		output *bytes.Buffer
	}
	helpers := make([]helperProcess, 0, updateCount)
	for index := range updateCount {
		message := fmt.Sprintf("process-%02d", index)
		cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundStoreUpdateHelperProcess$")
		cmd.Env = append(os.Environ(),
			"CODOG_BACKGROUND_UPDATE_HELPER=1",
			"CODOG_BACKGROUND_UPDATE_DIR="+store.Dir,
			"CODOG_BACKGROUND_UPDATE_MESSAGE="+message,
			"CODOG_BACKGROUND_UPDATE_READY="+filepath.Join(readyDir, message),
			"CODOG_BACKGROUND_UPDATE_BARRIER="+barrier,
		)
		output := new(bytes.Buffer)
		cmd.Stdout = output
		cmd.Stderr = output
		require.NoError(t, cmd.Start())
		helpers = append(helpers, helperProcess{cmd: cmd, output: output})
	}
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(readyDir)
		return err == nil && len(entries) == updateCount
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(barrier, []byte("start"), 0o600))
	for _, helper := range helpers {
		require.NoError(t, helper.cmd.Wait(), helper.output.String())
	}

	task, err := store.Get("task")
	require.NoError(t, err)
	require.Len(t, task.Messages, updateCount)
	seen := make(map[string]bool, updateCount)
	for _, message := range task.Messages {
		seen[message.Message] = true
	}
	for index := range updateCount {
		require.True(t, seen[fmt.Sprintf("process-%02d", index)])
	}
}

func TestBackgroundStoreUpdateHelperProcess(t *testing.T) {
	if os.Getenv("CODOG_BACKGROUND_UPDATE_HELPER") != "1" {
		return
	}
	require.NoError(t, os.WriteFile(os.Getenv("CODOG_BACKGROUND_UPDATE_READY"), []byte("ready"), 0o600))
	require.Eventually(t, func() bool {
		_, err := os.Stat(os.Getenv("CODOG_BACKGROUND_UPDATE_BARRIER"))
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	store := Store{Dir: os.Getenv("CODOG_BACKGROUND_UPDATE_DIR")}
	_, err := store.Update("task", os.Getenv("CODOG_BACKGROUND_UPDATE_MESSAGE"))
	require.NoError(t, err)
}

func TestLaneBoardGroupsTasksAndReportsFreshness(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.save(Task{
		ID:        "active",
		Kind:      "agent",
		Prompt:    "active prompt",
		PID:       os.Getpid(),
		Status:    "running",
		StartedAt: now.Add(-time.Minute),
		LogPath:   filepath.Join(store.Dir, "active.log"),
		Heartbeat: &LaneHeartbeat{
			ObservedAt:     now.Add(-10 * time.Second),
			TransportAlive: true,
			Status:         "running",
		},
	}))
	require.NoError(t, store.save(Task{
		ID:        "blocked",
		Status:    "blocked",
		StartedAt: now.Add(-2 * time.Minute),
		LogPath:   filepath.Join(store.Dir, "blocked.log"),
		Heartbeat: &LaneHeartbeat{
			ObservedAt:     now.Add(-2 * time.Minute),
			TransportAlive: true,
			Status:         "waiting",
		},
	}))
	require.NoError(t, store.save(Task{
		ID:        "failed",
		Status:    "failed",
		StartedAt: now.Add(-3 * time.Minute),
		LogPath:   filepath.Join(store.Dir, "failed.log"),
		Heartbeat: &LaneHeartbeat{
			ObservedAt:     now.Add(-5 * time.Second),
			TransportAlive: false,
			Status:         "lost",
		},
	}))
	require.NoError(t, store.save(Task{
		ID:        "completed",
		Status:    "completed",
		StartedAt: now.Add(-4 * time.Minute),
		LogPath:   filepath.Join(store.Dir, "completed.log"),
	}))

	board, err := store.LaneBoardAt(now, 30*time.Second)
	require.NoError(t, err)
	require.Equal(t, now, board.GeneratedAt)
	require.Len(t, board.Active, 1)
	require.Equal(t, "active", board.Active[0].TaskID)
	require.Equal(t, LaneFreshnessHealthy, board.Active[0].Freshness)
	require.Equal(t, "live_lane", board.Active[0].Provenance.SourceKind)
	require.Equal(t, "local", board.Active[0].Provenance.Channel)
	require.Equal(t, "active prompt", board.Active[0].Prompt)
	require.Len(t, board.Blocked, 1)
	require.Equal(t, "blocked", board.Blocked[0].TaskID)
	require.Equal(t, LaneFreshnessStalled, board.Blocked[0].Freshness)
	require.Len(t, board.Finished, 2)
	require.Equal(t, "failed", board.Finished[0].TaskID)
	require.Equal(t, LaneFreshnessTransportDead, board.Finished[0].Freshness)
	require.True(t, board.Finished[0].Lifecycle.Terminal)
	require.False(t, board.Finished[0].Lifecycle.TerminalStateUnknown)
	require.Equal(t, "canonical_terminal_status", board.Finished[0].Lifecycle.Reason)
	require.NotNil(t, board.Finished[0].TerminalOutcome)
	require.Equal(t, "failed", board.Finished[0].TerminalOutcome.Status)
	require.Equal(t, "completed", board.Finished[1].TaskID)
	require.Equal(t, LaneFreshnessUnknown, board.Finished[1].Freshness)
	require.True(t, board.Finished[1].Lifecycle.Terminal)
	require.Equal(t, "completed", board.Finished[1].Lifecycle.Status)
	require.NotEmpty(t, board.Finished[1].TerminalOutcome.Fingerprint)
}

func TestLaneBoardUsesCurrentTimeWrapper(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.save(Task{
		ID:        "active",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		LogPath:   filepath.Join(store.Dir, "active.log"),
	}))

	board, err := store.LaneBoard(time.Minute)
	require.NoError(t, err)
	require.NotZero(t, board.GeneratedAt)
	require.Len(t, board.Active, 1)
	require.Equal(t, "active", board.Active[0].TaskID)
}

func TestRecordTerminalEventNormalizesAndDeduplicates(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Date(2026, 7, 5, 11, 0, 0, 0, time.UTC)
	exitCode := 9
	require.NoError(t, store.save(Task{
		ID:          "task",
		Status:      "running",
		PID:         os.Getpid(),
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: &now,
		ExitCode:    &exitCode,
		Error:       "boom",
		LogPath:     filepath.Join(store.Dir, "task.log"),
	}))

	first, err := store.RecordTerminalEvent("task", TerminalEvent{
		Status: " failed ",
		Provenance: EventProvenance{
			SourceKind: "replay",
			Emitter:    "agent",
			Confidence: "high",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "failed", first.Status)
	require.Len(t, first.TerminalEvents, 1)
	require.True(t, first.TerminalEvents[0].Actionable)
	require.Equal(t, exitCode, *first.TerminalEvents[0].ExitCode)
	require.Equal(t, "boom", first.TerminalEvents[0].Error)
	require.Equal(t, now, first.TerminalEvents[0].ObservedAt)
	require.NotNil(t, first.TerminalOutcome)
	require.Equal(t, 1, first.TerminalOutcome.EventCount)

	duplicate, err := store.RecordTerminalEvent("task", TerminalEvent{Status: "failed"})
	require.NoError(t, err)
	require.Len(t, duplicate.TerminalEvents, 2)
	require.True(t, duplicate.TerminalEvents[1].Duplicate)
	require.False(t, duplicate.TerminalEvents[1].Actionable)
	require.Equal(t, 2, duplicate.TerminalOutcome.EventCount)
	require.Equal(t, 1, duplicate.TerminalOutcome.DuplicateCount)

	_, err = store.RecordTerminalEvent("task", TerminalEvent{Status: "running"})
	require.ErrorContains(t, err, "terminal event status must be terminal")
}

func TestConcurrentTerminalEventsPreserveEveryNotification(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	const eventCount = 48
	errs := make(chan error, eventCount)
	var wg sync.WaitGroup
	for index := range eventCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.RecordTerminalEvent("task", TerminalEvent{
				Status:     "completed",
				ObservedAt: time.Now().UTC(),
				Provenance: EventProvenance{Emitter: fmt.Sprintf("worker-%02d", index)},
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	task, err := store.Get("task")
	require.NoError(t, err)
	require.Len(t, task.TerminalEvents, eventCount)
	require.NotNil(t, task.TerminalOutcome)
	require.Equal(t, eventCount, task.TerminalOutcome.EventCount)
}

func TestResolveLifecycleDistinguishesTransportDeathFromTerminalStatus(t *testing.T) {
	require.True(t, IsActiveStatus("stopping"))
	require.False(t, IsActiveStatus("stopped"))

	running := ResolveLifecycle("running", LaneFreshnessTransportDead)
	require.False(t, running.Terminal)
	require.True(t, running.TerminalStateUnknown)
	require.Equal(t, "transport_dead_before_terminal_status", running.Reason)

	completed := ResolveLifecycle("completed", LaneFreshnessTransportDead)
	require.True(t, completed.Terminal)
	require.False(t, completed.TerminalStateUnknown)
	require.Equal(t, "canonical_terminal_status", completed.Reason)

	exited := ResolveLifecycle("exited", LaneFreshnessHealthy)
	require.True(t, exited.Terminal)
	require.True(t, exited.TerminalStateUnknown)
	require.Equal(t, "process_exited_without_status", exited.Reason)
}

func TestUpdateHeartbeatNormalizesEventProvenance(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: now,
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	task, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: true,
		Status:         "working",
		Provenance: EventProvenance{
			SourceKind:  "health",
			Environment: "dogfood",
			Channel:     "bridge",
			Emitter:     "clawhip",
			Confidence:  "high",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, task.Heartbeat)
	require.Equal(t, EventProvenance{
		SourceKind:  "healthcheck",
		Environment: "dogfood",
		Channel:     "bridge",
		Emitter:     "clawhip",
		Confidence:  "high",
	}, task.Heartbeat.Provenance)

	board, err := store.LaneBoardAt(now, time.Minute)
	require.NoError(t, err)
	require.Len(t, board.Active, 1)
	require.Equal(t, "healthcheck", board.Active[0].Provenance.SourceKind)
	require.Equal(t, "clawhip", board.Active[0].Provenance.Emitter)
}

func TestTerminalEventsSuppressDuplicatesAndSurfaceConflicts(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.save(Task{
		ID:        "task",
		Command:   "echo hi",
		Status:    "running",
		PID:       os.Getpid(),
		StartedAt: now,
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	first, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: true,
		Status:         "completed",
		Provenance:     EventProvenance{SourceKind: "live", Emitter: "worker-a"},
	})
	require.NoError(t, err)
	require.Equal(t, "completed", first.Status)
	require.Len(t, first.TerminalEvents, 1)
	require.True(t, first.TerminalEvents[0].Actionable)
	require.NotNil(t, first.TerminalOutcome)
	require.Equal(t, 1, first.TerminalOutcome.EventCount)
	require.True(t, first.TerminalOutcome.Actionable)

	duplicate, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now.Add(time.Second),
		TransportAlive: true,
		Status:         "completed",
		Provenance:     EventProvenance{SourceKind: "live", Emitter: "worker-a"},
	})
	require.NoError(t, err)
	require.Len(t, duplicate.TerminalEvents, 2)
	require.True(t, duplicate.TerminalEvents[1].Duplicate)
	require.False(t, duplicate.TerminalEvents[1].Actionable)
	require.Equal(t, first.TerminalOutcome.Fingerprint, duplicate.TerminalEvents[1].DuplicateOf)
	require.Equal(t, 2, duplicate.TerminalOutcome.EventCount)
	require.Equal(t, 1, duplicate.TerminalOutcome.DuplicateCount)
	require.False(t, duplicate.TerminalOutcome.MateriallyDifferent)

	changedDuplicate, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now.Add(2 * time.Second),
		TransportAlive: true,
		Status:         "completed",
		Provenance:     EventProvenance{SourceKind: "live", Emitter: "worker-b"},
	})
	require.NoError(t, err)
	require.True(t, changedDuplicate.TerminalEvents[2].Duplicate)
	require.True(t, changedDuplicate.TerminalEvents[2].MateriallyDifferent)
	require.Equal(t, 2, changedDuplicate.TerminalOutcome.DuplicateCount)
	require.True(t, changedDuplicate.TerminalOutcome.MateriallyDifferent)

	conflict, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now.Add(3 * time.Second),
		TransportAlive: true,
		Status:         "failed",
		Provenance:     EventProvenance{SourceKind: "live", Emitter: "worker-a"},
	})
	require.NoError(t, err)
	require.Len(t, conflict.TerminalEvents, 4)
	require.False(t, conflict.TerminalEvents[3].Actionable)
	require.Equal(t, "completed", conflict.TerminalOutcome.Status)
	require.Equal(t, 1, conflict.TerminalOutcome.ConflictCount)
}

func TestUpdateHeartbeatPersistsTaskHeartbeat(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.save(Task{
		ID:        "task",
		Status:    "running",
		StartedAt: now.Add(-time.Minute),
		LogPath:   filepath.Join(store.Dir, "task.log"),
	}))

	task, err := store.UpdateHeartbeat("task", LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: true,
		Status:         " running ",
	})
	require.NoError(t, err)
	require.NotNil(t, task.Heartbeat)
	require.Equal(t, now, task.Heartbeat.ObservedAt)
	require.True(t, task.Heartbeat.TransportAlive)
	require.Equal(t, "running", task.Heartbeat.Status)

	persisted, err := store.Get("task")
	require.NoError(t, err)
	require.NotNil(t, persisted.Heartbeat)
	require.Equal(t, "running", persisted.Heartbeat.Status)
}

func TestPruneRemovesOldCompletedTasksAndKeepsRunning(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	now := time.Now().UTC()
	oldCompleted := now.Add(-48 * time.Hour)
	recentCompleted := now.Add(-1 * time.Hour)
	oldLog := filepath.Join(store.Dir, "old.log")
	recentLog := filepath.Join(store.Dir, "recent.log")
	runningLog := filepath.Join(store.Dir, "running.log")
	stoppingLog := filepath.Join(store.Dir, "stopping.log")
	require.NoError(t, os.WriteFile(oldLog, []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(recentLog, []byte("recent"), 0o644))
	require.NoError(t, os.WriteFile(runningLog, []byte("running"), 0o644))
	require.NoError(t, os.WriteFile(stoppingLog, []byte("stopping"), 0o644))
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
	require.NoError(t, store.save(Task{
		ID:        "stopping",
		PID:       os.Getpid(),
		Status:    "stopping",
		StartedAt: oldCompleted,
		LogPath:   stoppingLog,
	}))

	result, err := store.Prune(PruneOptions{OlderThan: 24 * time.Hour})
	require.NoError(t, err)
	require.Equal(t, []string{"old"}, result.Removed)
	require.Equal(t, 1, result.RemovedCount)
	require.Equal(t, 3, result.Kept)
	require.NoFileExists(t, oldLog)
	require.NoFileExists(t, filepath.Join(store.Dir, "old.json"))
	require.FileExists(t, recentLog)
	require.FileExists(t, filepath.Join(store.Dir, "recent.json"))
	require.FileExists(t, runningLog)
	require.FileExists(t, filepath.Join(store.Dir, "running.json"))
	require.FileExists(t, stoppingLog)
	require.FileExists(t, filepath.Join(store.Dir, "stopping.json"))
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
		ScopeBinding:  ScopeBinding{Owner: "reviewer", WorkflowScope: "claw-code-dogfood", WatcherAction: "act"},
	}))

	result, err := store.SuperviseOnce(time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, result.Restarted, 1)
	restarted := result.Restarted[0]
	require.Equal(t, "failed", restarted.RestartedFrom)
	require.Equal(t, "session-1", restarted.SessionID)
	require.Equal(t, 1, restarted.RestartCount)
	require.Equal(t, policy, restarted.RestartPolicy)
	require.Equal(t, "reviewer", restarted.ScopeBinding.Owner)
	require.Equal(t, "claw-code-dogfood", restarted.ScopeBinding.WorkflowScope)
	require.True(t, restarted.ScopeBinding.Actionable)
	source, err := store.Get("failed")
	require.NoError(t, err)
	require.Equal(t, restarted.ID, source.RestartedBy)

	again, err := store.SuperviseOnce(time.Now().UTC())
	require.NoError(t, err)
	require.Empty(t, again.Restarted)
	require.Contains(t, again.Skipped, SuperviseSkip{ID: "failed", Reason: "restarted"})
}

func TestConcurrentSupervisionStartsSingleRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	store := Store{Dir: t.TempDir()}
	workspace := t.TempDir()
	completed := time.Now().UTC().Add(-time.Minute)
	policy := &RestartPolicy{Enabled: true, Mode: "on-failure", MaxAttempts: 2}
	require.NoError(t, store.save(Task{
		ID:            "failed",
		Command:       "sleep 30",
		Workspace:     workspace,
		RestartPolicy: policy,
		Status:        "failed",
		StartedAt:     completed.Add(-time.Minute),
		CompletedAt:   &completed,
		LogPath:       filepath.Join(store.Dir, "failed.log"),
	}))

	const supervisorCount = 24
	results := make(chan SuperviseResult, supervisorCount)
	errs := make(chan error, supervisorCount)
	var wg sync.WaitGroup
	for range supervisorCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.SuperviseOnce(time.Now().UTC())
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	restarted := []Task{}
	for result := range results {
		restarted = append(restarted, result.Restarted...)
	}
	require.Len(t, restarted, 1)
	t.Cleanup(func() { _, _ = store.Stop(restarted[0].ID) })
	source, err := store.Get("failed")
	require.NoError(t, err)
	require.Equal(t, restarted[0].ID, source.RestartedBy)

	tasks, err := store.List()
	require.NoError(t, err)
	children := FilterByKind(tasks, restarted[0].Kind)
	childCount := 0
	for _, task := range children {
		if task.RestartedFrom == "failed" {
			childCount++
		}
	}
	require.Equal(t, 1, childCount)
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
		{
			ID:            "stopping",
			Command:       "echo stopping",
			Status:        "stopping",
			StartedAt:     now.Add(-time.Hour),
			LogPath:       filepath.Join(store.Dir, "stopping.log"),
			RestartPolicy: &RestartPolicy{Enabled: true, Mode: "always"},
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
