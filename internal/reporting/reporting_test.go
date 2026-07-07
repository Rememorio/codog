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
	require.Equal(t, "new", first.Outcome)
	require.True(t, first.Checked)
	require.False(t, first.NoChange)
	require.Len(t, first.NewItems, 1)
	require.Equal(t, now, first.NewItems[0].ObservedAt)
	require.Equal(t, int64(60), first.NewItems[0].AgeSeconds)
	require.Equal(t, int64(3600), first.NewItems[0].FreshnessTTLSeconds)
	require.Equal(t, "current", first.NewItems[0].Freshness)
	require.Equal(t, "carried_forward", first.NewItems[0].ObservationSource)
	require.False(t, first.MixedFreshness)
	require.Equal(t, map[string]int{"current": 1}, first.FreshnessCounts)
	require.Empty(t, first.ChangedItems)
	require.Zero(t, first.UnchangedCount)
	require.Equal(t, 1, first.TotalCount)
	require.False(t, first.Collapsed)
	require.True(t, first.FullSnapshotStored)
	require.NotEmpty(t, first.SnapshotID)
	require.Equal(t, first.ReportID, first.LastMeaningfulReportID)
	require.Equal(t, []string{firstItem.ItemID}, first.LastMeaningfulItemIDs)

	snapshot, err := reportStore.GetSnapshot(first.SnapshotID)
	require.NoError(t, err)
	require.Equal(t, SnapshotSchemaVersion, snapshot.SchemaVersion)
	require.Equal(t, "dogfood", snapshot.Channel)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, firstItem.ItemID, snapshot.Items[0].ID)

	second, err := reportStore.GenerateWithOptions("dogfood", now.Add(2*time.Minute), GenerateOptions{
		TriggerID:       "nudge-cycle-1",
		CheckedSurfaces: []string{"roadmap", "sessions", "logs"},
	})
	require.NoError(t, err)
	require.Equal(t, "no_change", second.Outcome)
	require.Equal(t, "nudge-cycle-1", second.TriggerID)
	require.True(t, second.Checked)
	require.True(t, second.NoChange)
	require.Equal(t, []string{"roadmap", "sessions", "logs"}, second.CheckedSurfaces)
	require.Empty(t, second.NewItems)
	require.Empty(t, second.ChangedItems)
	require.Equal(t, 1, second.UnchangedCount)
	require.True(t, second.Collapsed)
	require.Equal(t, first.ReportID, second.PreviousReportID)
	require.Equal(t, first.ReportID, second.LastMeaningfulReportID)
	require.Equal(t, first.SnapshotID, second.LastMeaningfulSnapshotID)
	require.Equal(t, []string{firstItem.ItemID}, second.LastMeaningfulItemIDs)

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
	require.Equal(t, "changed", third.Outcome)
	require.False(t, third.NoChange)
	require.Equal(t, firstItem.ItemID, third.ChangedItems[0].ID)
	require.Equal(t, roadmap.PriorityP0, third.ChangedItems[0].Priority)
	require.Zero(t, third.UnchangedCount)
	require.Equal(t, second.ReportID, third.PreviousReportID)
	require.Equal(t, third.ReportID, third.LastMeaningfulReportID)
}

func TestGenerateNoChangeWithoutPriorMeaningfulReport(t *testing.T) {
	reportStore := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	report, err := reportStore.GenerateWithOptions("dogfood", now, GenerateOptions{TriggerID: "nudge-empty"})
	require.NoError(t, err)

	require.Equal(t, "no_change", report.Outcome)
	require.Equal(t, "nudge-empty", report.TriggerID)
	require.True(t, report.Checked)
	require.True(t, report.NoChange)
	require.Empty(t, report.LastMeaningfulReportID)
	require.Empty(t, report.LastMeaningfulItemIDs)
	require.Zero(t, report.TotalCount)
}

func TestGenerateMarksStaleAndMixedFreshness(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	oldItem, err := roadmapStore.File(roadmap.Filing{
		Title: "old carried state",
		Now:   now.Add(-2 * time.Hour),
	})
	require.NoError(t, err)
	newItem, err := roadmapStore.File(roadmap.Filing{
		Title: "fresh carried state",
		Now:   now.Add(-10 * time.Minute),
	})
	require.NoError(t, err)

	report, err := reportStore.GenerateWithOptions("dogfood", now, GenerateOptions{
		FreshnessTTL: 30 * time.Minute,
	})
	require.NoError(t, err)

	require.True(t, report.MixedFreshness)
	require.Equal(t, map[string]int{"current": 1, "stale": 1}, report.FreshnessCounts)
	require.Len(t, report.NewItems, 2)
	byID := map[string]ItemSummary{}
	for _, item := range report.NewItems {
		byID[item.ID] = item
	}
	require.Equal(t, "stale", byID[oldItem.ItemID].Freshness)
	require.Equal(t, int64(7200), byID[oldItem.ItemID].AgeSeconds)
	require.Equal(t, int64(1800), byID[oldItem.ItemID].FreshnessTTLSeconds)
	require.Equal(t, "current", byID[newItem.ItemID].Freshness)
	require.Equal(t, "carried_forward", byID[newItem.ItemID].ObservationSource)

	second, err := reportStore.GenerateWithOptions("dogfood", now.Add(time.Minute), GenerateOptions{
		FreshnessTTL: 30 * time.Minute,
	})
	require.NoError(t, err)
	require.True(t, second.NoChange)
	require.Equal(t, 2, second.UnchangedCount)
	require.True(t, second.MixedFreshness)
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
