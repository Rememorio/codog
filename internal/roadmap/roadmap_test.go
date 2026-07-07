package roadmap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFileAssignsStableIDAndUpdatesLifecycle(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	first, err := store.File(Filing{
		Title:       "lane reports need stable ids",
		Description: "new reports are currently prose only",
		Now:         now,
	})
	require.NoError(t, err)
	require.True(t, first.Created)
	require.Equal(t, "new_roadmap_filing", first.Action)
	require.NotEmpty(t, first.ItemID)
	require.Equal(t, StateFiled, first.State)
	require.Equal(t, now, first.Item.LastStateChangeAt)

	update, err := store.File(Filing{
		ID:       first.ItemID,
		Title:    "lane reports need stable ids after edits",
		State:    StateInProgress,
		Related:  []string{"rp-related"},
		ReportID: "report-1",
		Now:      now.Add(time.Hour),
	})
	require.NoError(t, err)
	require.False(t, update.Created)
	require.Equal(t, "roadmap_update", update.Action)
	require.Equal(t, first.ItemID, update.ItemID)
	require.Equal(t, "lane reports need stable ids after edits", update.Item.Title)
	require.Equal(t, StateInProgress, update.State)
	require.Equal(t, now.Add(time.Hour), update.Item.LastStateChangeAt)
	require.Equal(t, []string{"rp-related"}, update.Item.Related)
	require.Equal(t, "report-1", update.Item.ReportID)
	require.Equal(t, []string{first.ItemID}, update.Item.Lineage)

	again, err := store.File(Filing{
		ID:    first.ItemID,
		State: StateInProgress,
		Now:   now.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, now.Add(time.Hour), again.Item.LastStateChangeAt)

	items, err := store.List()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, first.ItemID, items[0].ID)
}

func TestFileMarksSupersededLineage(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC)

	item, err := store.File(Filing{Title: "old issue", Now: now})
	require.NoError(t, err)
	superseded, err := store.File(Filing{
		ID:           item.ItemID,
		SupersededBy: "rp-new",
		Now:          now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, StateSuperseded, superseded.State)
	require.Equal(t, "rp-new", superseded.Item.SupersededBy)
	require.Equal(t, now.Add(time.Minute), superseded.Item.LastStateChangeAt)
}

func TestRejectsInvalidRoadmapInput(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.File(Filing{State: StateDone})
	require.ErrorContains(t, err, "title or id")

	_, err = store.File(Filing{Title: "bad state", State: "fresh"})
	require.ErrorContains(t, err, "invalid roadmap lifecycle state")

	_, err = store.File(Filing{ID: "../bad", Title: "bad"})
	require.ErrorContains(t, err, "path component")
}
