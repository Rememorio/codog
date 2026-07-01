package laneevents

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequiredLaneEventsMatchCanonicalContract(t *testing.T) {
	required := RequiredLaneEvents()
	for _, event := range []string{
		LaneStarted,
		LaneReady,
		LanePromptMisdelivery,
		LaneBlocked,
		LaneRed,
		LaneGreen,
		LaneCommitCreated,
		LanePROpened,
		LaneMergeReady,
		LaneFinished,
		LaneFailed,
		BranchStaleAgainstMain,
	} {
		require.Contains(t, required, event)
	}
}

func TestReconcileOrdersEventsAndSuppressesDuplicateTerminals(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Sequence: 3, Type: "completed", LaneID: "lane-1", SessionID: "session-1", TaskID: "task-1", Status: "finished", FinishReason: "stop", TokensOutput: 20, CreatedAt: now.Add(2 * time.Second)},
		{Sequence: 1, Type: "created", LaneID: "lane-1", SessionID: "session-1", CreatedAt: now},
		{Sequence: 2, Type: "completed", LaneID: "lane-1", SessionID: "session-1", TaskID: "task-1", Status: "finished", FinishReason: "stop", TokensOutput: 10, CreatedAt: now.Add(time.Second)},
	}

	projection := Reconcile(events)

	require.Len(t, projection.Events, 3)
	require.Equal(t, int64(1), projection.Events[0].Sequence)
	require.Equal(t, LaneStarted, projection.Events[0].LaneEvent)
	require.Equal(t, ProvenanceLiveLane, projection.Events[0].Provenance.Source)
	require.Equal(t, LaneFinished, projection.Events[1].LaneEvent)
	require.True(t, projection.Events[1].Terminal)
	require.Len(t, projection.DuplicateTerminals, 1)
	require.True(t, projection.DuplicateTerminals[0].MateriallyDiffers)
	require.Len(t, projection.MateriallyDifferentTerminals, 1)
	require.NotNil(t, projection.ActionableTerminal)
	require.Equal(t, int64(2), projection.ActionableTerminal.Sequence)
	require.Equal(t, projection.ActionableTerminal.Fingerprint, projection.DuplicateTerminals[0].DuplicateOf)
}

func TestReconcileUsesLatestDistinctTerminalAsFinalTruth(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	projection := Reconcile([]Event{
		{Sequence: 1, Type: "completed", LaneID: "lane-1", Status: "finished", FinishReason: "stop", CreatedAt: now},
		{Sequence: 2, Type: "completed", LaneID: "lane-1", Status: "failed", FinishReason: "tool_error", CreatedAt: now.Add(time.Second)},
	})

	require.NotNil(t, projection.ActionableTerminal)
	require.Equal(t, LaneFailed, projection.ActionableTerminal.LaneEvent)
	require.Equal(t, int64(2), projection.ActionableTerminal.Sequence)
	require.Empty(t, projection.DuplicateTerminals)
}
