package roadmap

import (
	"strings"
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

func TestFileAppendsEvidenceWithoutChangingIdentity(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)
	longPreview := strings.Repeat("x", MaxEvidencePreviewRunes+12)

	first, err := store.File(Filing{
		Title: "pinpoints need evidence",
		Evidence: []EvidenceAttachment{{
			Role:      EvidenceSymptom,
			Type:      "session",
			Reference: "session-1",
			Preview:   longPreview,
		}},
		Now: now,
	})
	require.NoError(t, err)
	require.Len(t, first.Item.Evidence, 1)
	require.Equal(t, EvidenceSymptom, first.Item.Evidence[0].Role)
	require.Equal(t, "session", first.Item.Evidence[0].Type)
	require.Equal(t, "session-1", first.Item.Evidence[0].Reference)
	require.Len(t, []rune(first.Item.Evidence[0].Preview), MaxEvidencePreviewRunes)
	require.Contains(t, first.Item.Evidence[0].ID, "ev-")

	update, err := store.File(Filing{
		ID: first.ItemID,
		Evidence: []EvidenceAttachment{
			{
				Role:      EvidenceSymptom,
				Type:      "session",
				Reference: "session-1",
				Preview:   "updated bounded preview",
			},
			{
				Role:      EvidenceVerification,
				Type:      "commit",
				Reference: "abc1234",
				Preview:   "go test ./...",
			},
		},
		Now: now.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, first.ItemID, update.ItemID)
	require.Len(t, update.Item.Evidence, 2)
	require.Equal(t, "updated bounded preview", update.Item.Evidence[0].Preview)
	require.Equal(t, EvidenceVerification, update.Item.Evidence[1].Role)
	require.Equal(t, "abc1234", update.Item.Evidence[1].Reference)
	require.Equal(t, []string{first.ItemID}, update.Item.Lineage)
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

	_, err = store.File(Filing{
		Title: "bad evidence",
		Evidence: []EvidenceAttachment{{
			Role:      "guess",
			Reference: "session-1",
		}},
	})
	require.ErrorContains(t, err, "invalid roadmap evidence role")

	_, err = store.File(Filing{
		Title: "missing evidence ref",
		Evidence: []EvidenceAttachment{{
			Role: EvidenceRepro,
		}},
	})
	require.ErrorContains(t, err, "evidence reference is required")

	_, err = store.File(Filing{ID: "../bad", Title: "bad"})
	require.ErrorContains(t, err, "path component")
}
