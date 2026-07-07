package reporting

import (
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/roadmap"
	"github.com/stretchr/testify/require"
)

func TestGenerateCollapsesUnchangedItemsAndStoresSnapshot(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	firstItem, err := roadmapStore.File(roadmap.Filing{
		Title:    "report backpressure",
		Priority: roadmap.PriorityP1,
		Severity: roadmap.SeverityHigh,
		Impact:   roadmap.ImpactObservabilityDebt,
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceSymptom,
			Type:      "session",
			Reference: "session-1",
		}},
		Now: now,
	})
	require.NoError(t, err)

	first, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "report_backpressure", first.Kind)
	require.Len(t, first.NewItems, 1)
	require.Empty(t, first.ChangedItems)
	require.Zero(t, first.UnchangedCount)
	require.Equal(t, 1, first.TotalCount)
	require.False(t, first.Collapsed)
	require.True(t, first.FullSnapshotStored)
	require.NotEmpty(t, first.SnapshotID)

	snapshot, err := reportStore.GetSnapshot(first.SnapshotID)
	require.NoError(t, err)
	require.Equal(t, SnapshotSchemaVersion, snapshot.SchemaVersion)
	require.Equal(t, "dogfood", snapshot.Channel)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, firstItem.ItemID, snapshot.Items[0].ID)

	second, err := reportStore.Generate("dogfood", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Empty(t, second.NewItems)
	require.Empty(t, second.ChangedItems)
	require.Equal(t, 1, second.UnchangedCount)
	require.True(t, second.Collapsed)
	require.Equal(t, first.ReportID, second.PreviousReportID)

	updated, err := roadmapStore.File(roadmap.Filing{
		ID:       firstItem.ItemID,
		Priority: roadmap.PriorityP0,
		Severity: roadmap.SeverityCritical,
		Now:      now.Add(3 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, firstItem.ItemID, updated.ItemID)

	third, err := reportStore.Generate("dogfood", now.Add(4*time.Minute))
	require.NoError(t, err)
	require.Empty(t, third.NewItems)
	require.Len(t, third.ChangedItems, 1)
	require.Equal(t, firstItem.ItemID, third.ChangedItems[0].ID)
	require.Equal(t, roadmap.PriorityP0, third.ChangedItems[0].Priority)
	require.Zero(t, third.UnchangedCount)
	require.Equal(t, second.ReportID, third.PreviousReportID)
}

func TestGenerateTracksIndependentChannelCursors(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	_, err := roadmapStore.File(roadmap.Filing{Title: "shared item", Now: now})
	require.NoError(t, err)

	first, err := reportStore.Generate("channel-a", now.Add(time.Minute))
	require.NoError(t, err)
	second, err := reportStore.Generate("channel-b", now.Add(2*time.Minute))
	require.NoError(t, err)

	require.Len(t, first.NewItems, 1)
	require.Len(t, second.NewItems, 1)
	require.NotEqual(t, first.ReportID, second.ReportID)
}

func TestRejectsInvalidReportingInputs(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Generate("", time.Time{})
	require.ErrorContains(t, err, "channel")

	_, err = store.GetSnapshot("../bad")
	require.ErrorContains(t, err, "path component")
}
