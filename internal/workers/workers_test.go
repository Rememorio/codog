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
	require.Len(t, worker.Events, 1)
	require.Equal(t, int64(1), worker.Events[0].Sequence)
	require.Equal(t, "lane.started", worker.Events[0].LaneEvent)
	require.Equal(t, "live_lane", worker.Events[0].Provenance.Source)
	require.Equal(t, "codog-worker", worker.Events[0].Provenance.Emitter)

	worker, err = store.Observe(worker.ID, "trust this folder?")
	require.NoError(t, err)
	require.Equal(t, "trust_prompt", worker.Status)
	require.False(t, worker.ReadyForPrompt)
	require.Equal(t, int64(2), worker.Events[len(worker.Events)-1].Sequence)
	require.Equal(t, "lane.blocked", worker.Events[len(worker.Events)-1].LaneEvent)

	worker, err = store.ResolveTrust(worker.ID)
	require.NoError(t, err)
	require.True(t, worker.ReadyForPrompt)

	receipt := &TaskReceipt{Repo: "codog", TaskKind: "test", SourceSurface: "tool", ObjectivePreview: "run tests"}
	worker, err = store.SendPrompt(worker.ID, "run tests", receipt, "task-1")
	require.NoError(t, err)
	require.Equal(t, "running", worker.Status)
	require.Equal(t, "task-1", worker.TaskID)
	require.Equal(t, receipt, worker.TaskReceipt)
	require.Equal(t, "codog", worker.Events[len(worker.Events)-1].Binding.Owner)
	require.Equal(t, "test", worker.Events[len(worker.Events)-1].Binding.Scope)
	require.Equal(t, "act", worker.Events[len(worker.Events)-1].Binding.WatcherAction)

	snapshot, err := store.AwaitReady(worker.ID)
	require.NoError(t, err)
	require.False(t, snapshot.ReadyForPrompt)

	worker, err = store.Complete(worker.ID, "stop", 42)
	require.NoError(t, err)
	require.Equal(t, "finished", worker.Status)
	require.NotNil(t, worker.Terminal)
	require.Equal(t, "lane.finished", worker.Terminal.LaneEvent)
	require.Equal(t, "stop", worker.Terminal.FinishReason)
	require.Equal(t, 0, worker.Terminal.DuplicateCount)
	require.NotEmpty(t, worker.Events)

	list, err := store.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, worker.ID, list[0].ID)
}

func TestWorkerTerminalEventsAreDedupedWithMaterialDifference(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	worker, err := store.Create(t.TempDir(), nil, false)
	require.NoError(t, err)

	worker, err = store.Complete(worker.ID, "stop", 12)
	require.NoError(t, err)
	require.NotNil(t, worker.Terminal)
	firstFingerprint := worker.Terminal.Fingerprint
	firstSequence := worker.Terminal.Sequence

	worker, err = store.Complete(worker.ID, "stop", 12)
	require.NoError(t, err)
	require.Equal(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Equal(t, firstSequence, worker.Terminal.Sequence)
	require.Equal(t, 1, worker.Terminal.DuplicateCount)
	require.False(t, worker.Events[len(worker.Events)-1].MateriallyDiffers)
	require.Equal(t, firstFingerprint, worker.Events[len(worker.Events)-1].DuplicateOf)

	worker, err = store.Complete(worker.ID, "stop", 13)
	require.NoError(t, err)
	require.Equal(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Equal(t, firstSequence, worker.Terminal.Sequence)
	require.Equal(t, 2, worker.Terminal.DuplicateCount)
	require.Equal(t, 1, worker.Terminal.MaterialDifferenceCount)
	require.True(t, worker.Events[len(worker.Events)-1].MateriallyDiffers)

	worker, err = store.Complete(worker.ID, "tool_error", 13)
	require.NoError(t, err)
	require.Equal(t, "failed", worker.Status)
	require.Equal(t, "lane.failed", worker.Terminal.LaneEvent)
	require.NotEqual(t, firstFingerprint, worker.Terminal.Fingerprint)
	require.Greater(t, worker.Terminal.Sequence, firstSequence)
}
