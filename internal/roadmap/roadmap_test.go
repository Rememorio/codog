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

func TestFileTracksPrioritySeverityAndListOrder(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	defaulted, err := store.File(Filing{
		Title: "default priority",
		Now:   now,
	})
	require.NoError(t, err)
	require.Equal(t, PriorityP2, defaulted.Item.Priority)
	require.Equal(t, SeverityMedium, defaulted.Item.Severity)
	require.Equal(t, ImpactLongTailHardening, defaulted.Item.Impact)
	require.NotNil(t, defaulted.Item.PriorityUpdatedAt)

	urgent, err := store.File(Filing{
		Title:    "user breakage",
		Priority: PriorityP1,
		Severity: SeverityHigh,
		Impact:   ImpactUserFacingBreakage,
		PriorityReason: PriorityReason{
			BlastRadius:        "users cannot complete prompts",
			Reproducibility:    "reproducible",
			AutomationBreakage: "blocks dogfood loop",
			MergeRisk:          "low",
			Rationale:          "active user-facing regression",
		},
		Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, PriorityP1, urgent.Item.Priority)
	require.Equal(t, "active user-facing regression", urgent.Item.PriorityReason.Rationale)

	updated, err := store.File(Filing{
		ID:       urgent.ItemID,
		Priority: PriorityP0,
		Severity: SeverityCritical,
		Impact:   ImpactUserFacingBreakage,
		PriorityReason: PriorityReason{
			BlastRadius:        "all interactive sessions",
			Reproducibility:    "always",
			AutomationBreakage: "prevents merge queue",
			MergeRisk:          "high until fixed",
		},
		Now: now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, urgent.ItemID, updated.ItemID)
	require.Equal(t, PriorityP0, updated.Item.Priority)
	require.Equal(t, SeverityCritical, updated.Item.Severity)
	require.Equal(t, "prevents merge queue", updated.Item.PriorityReason.AutomationBreakage)
	require.Equal(t, now.Add(2*time.Minute), *updated.Item.PriorityUpdatedAt)
	require.Equal(t, []string{urgent.ItemID}, updated.Item.Lineage)

	items, err := store.List()
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, updated.ItemID, items[0].ID)
	require.Equal(t, defaulted.ItemID, items[1].ID)
}

func TestFileBuildsHandoffAndTracksImplementationLinks(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	filed, err := store.File(Filing{
		Title:       "handoff needs executable context",
		Description: "pinpoint should become an implementation lane",
		Priority:    PriorityP1,
		Severity:    SeverityHigh,
		Impact:      ImpactOperatorFriction,
		Evidence: []EvidenceAttachment{
			{Role: EvidenceSymptom, Type: "session", Reference: "session-1", Preview: "missing packet"},
			{Role: EvidenceVerification, Type: "test", Reference: "go-test", Preview: "go test ./internal/roadmap"},
		},
		Now: now,
	})
	require.NoError(t, err)
	require.NotNil(t, filed.Item.Handoff)
	require.Equal(t, filed.ItemID, filed.Item.Handoff.PinpointID)
	require.Equal(t, PriorityP1, filed.Item.Handoff.Priority)
	require.Equal(t, SeverityHigh, filed.Item.Handoff.Severity)
	require.Equal(t, ImpactOperatorFriction, filed.Item.Handoff.Impact)
	require.Equal(t, ReadinessImplementationReady, filed.Item.Handoff.Readiness)
	require.Equal(t, []string{"workspace"}, filed.Item.Handoff.SuspectedScope)
	require.Len(t, filed.Item.Handoff.EvidenceRefs, 2)
	require.Equal(t, []string{"go test ./internal/roadmap"}, filed.Item.Handoff.SuggestedVerification)

	updated, err := store.File(Filing{
		ID: filed.ItemID,
		Handoff: &HandoffPacket{
			Objective:             "Implement the handoff packet",
			SuspectedScope:        []string{"internal/roadmap", "internal/tools"},
			SuggestedVerification: []string{"go test ./internal/roadmap ./internal/tools"},
			Readiness:             ReadinessImplementationReady,
			Metadata:              map[string]string{"owner": "queue"},
		},
		Implementation: []ImplementationLink{{
			LaneID:       "lane-1",
			TaskID:       "task-1",
			WorktreeID:   "wt-1",
			WorktreePath: "worktrees/wt-1",
			PRURL:        "https://github.com/Rememorio/codog/pull/1",
			PRNumber:     1,
			Status:       "running",
		}},
		ExecutionResults: []ExecutionResult{{
			LaneID:       "lane-1",
			Status:       "running",
			Summary:      "implementation started",
			EvidenceRefs: []string{filed.Item.Evidence[0].ID},
		}},
		Now: now.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, filed.ItemID, updated.ItemID)
	require.Equal(t, StateInProgress, updated.State)
	require.Equal(t, "Implement the handoff packet", updated.Item.Handoff.Objective)
	require.Equal(t, []string{"internal/roadmap", "internal/tools"}, updated.Item.Handoff.SuspectedScope)
	require.Equal(t, []string{"go test ./internal/roadmap ./internal/tools"}, updated.Item.Handoff.SuggestedVerification)
	require.Equal(t, map[string]string{"owner": "queue"}, updated.Item.Handoff.Metadata)
	require.Len(t, updated.Item.Implementation, 1)
	require.Equal(t, "lane-1", updated.Item.Implementation[0].LaneID)
	require.Contains(t, updated.Item.Implementation[0].ID, "impl-")
	require.Len(t, updated.Item.ExecutionResults, 1)
	require.Contains(t, updated.Item.ExecutionResults[0].ID, "exec-")

	completed, err := store.File(Filing{
		ID: filed.ItemID,
		ExecutionResults: []ExecutionResult{{
			LinkID:  updated.Item.Implementation[0].ID,
			LaneID:  "lane-1",
			Status:  "passed",
			Summary: "verification passed",
		}},
		Now: now.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, StateDone, completed.State)
	require.Len(t, completed.Item.ExecutionResults, 2)
	require.Equal(t, []string{filed.ItemID}, completed.Item.Lineage)
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

	_, err = store.File(Filing{Title: "bad priority", Priority: "p9"})
	require.ErrorContains(t, err, "invalid roadmap priority")

	_, err = store.File(Filing{Title: "bad severity", Severity: "urgent"})
	require.ErrorContains(t, err, "invalid roadmap severity")

	_, err = store.File(Filing{Title: "bad impact", Impact: "unknown"})
	require.ErrorContains(t, err, "invalid roadmap impact")

	_, err = store.File(Filing{
		Title: "bad readiness",
		Handoff: &HandoffPacket{
			Readiness: "maybe",
		},
	})
	require.ErrorContains(t, err, "invalid roadmap handoff readiness")

	_, err = store.File(Filing{
		Title: "bad pr",
		Implementation: []ImplementationLink{{
			PRNumber: -1,
		}},
	})
	require.ErrorContains(t, err, "pr_number")

	_, err = store.File(Filing{
		Title: "bad result",
		ExecutionResults: []ExecutionResult{{
			Summary: "missing status",
		}},
	})
	require.ErrorContains(t, err, "execution result status")

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
