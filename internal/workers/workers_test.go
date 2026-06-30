package workers

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreManagesWorkerLifecycle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), []string{"repo", "repo", " extra "}, true)
	require.NoError(t, err)
	require.NotEmpty(t, worker.ID)
	require.Equal(t, "ready_for_prompt", worker.Status)
	require.True(t, worker.ReadyForPrompt)
	require.Equal(t, []string{"repo", "extra"}, worker.TrustedRoots)

	worker, err = store.Observe(worker.ID, "trust this folder?")
	require.NoError(t, err)
	require.Equal(t, "trust_prompt", worker.Status)
	require.False(t, worker.ReadyForPrompt)

	worker, err = store.ResolveTrust(worker.ID)
	require.NoError(t, err)
	require.True(t, worker.ReadyForPrompt)

	receipt := &TaskReceipt{Repo: "codog", TaskKind: "test", SourceSurface: "tool", ObjectivePreview: "run tests"}
	worker, err = store.SendPrompt(worker.ID, "run tests", receipt, "task-1")
	require.NoError(t, err)
	require.Equal(t, "running", worker.Status)
	require.Equal(t, "task-1", worker.TaskID)
	require.Equal(t, receipt, worker.TaskReceipt)

	snapshot, err := store.AwaitReady(worker.ID)
	require.NoError(t, err)
	require.False(t, snapshot.ReadyForPrompt)

	worker, err = store.Complete(worker.ID, "stop", 42)
	require.NoError(t, err)
	require.Equal(t, "finished", worker.Status)
	require.NotEmpty(t, worker.Events)

	list, err := store.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, worker.ID, list[0].ID)
}
