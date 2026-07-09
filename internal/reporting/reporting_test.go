package reporting

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/reportschema"
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
	require.Equal(t, reportschema.ReportingReportSchemaV1, first.SchemaVersion)
	require.Equal(t, reportschema.ReportingCompatibilityV1, first.SchemaCompatibility.Policy)
	require.Equal(t, reportschema.ReportingReportSchemaV1, first.SchemaCompatibility.CurrentVersion)
	require.Equal(t, reportschema.ReportingReportSchemaV1, first.SchemaCompatibility.MinCompatibleVersion)
	require.Contains(t, first.SchemaCompatibility.MinimalStableCore, "report_id")
	require.Contains(t, first.SchemaCompatibility.MinimalStableCore, "outcome")
	require.Contains(t, first.SchemaCompatibility.AdditiveChanges, "new optional top-level fields")
	require.Contains(t, first.SchemaCompatibility.BreakingChanges, "removing or renaming minimal_stable_core fields")
	require.Contains(t, first.SchemaCompatibility.OlderConsumerGuidance, "minimal_stable_core")
	require.Equal(t, first.ReportID, first.Identity.ReportID)
	require.NotEmpty(t, first.Identity.ContentHash)
	require.NotEmpty(t, first.Identity.CanonicalFingerprint)
	contentHash, err := reportContentHash(first)
	require.NoError(t, err)
	require.Equal(t, first.Identity.ContentHash, contentHash)
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
	require.Equal(t, reportschema.SensitivityInternal, first.FieldSensitivity["claims"])
	require.Equal(t, reportschema.SensitivityInternal, first.FieldSensitivity["new_items"])
	require.Equal(t, reportschema.SensitivityInternal, first.NewItems[0].Sensitivity)
	require.Empty(t, first.ChangedItems)
	require.Zero(t, first.UnchangedCount)
	require.Equal(t, 1, first.TotalCount)
	require.False(t, first.Collapsed)
	require.True(t, first.FullSnapshotStored)
	require.NotEmpty(t, first.SnapshotID)
	require.Equal(t, first.ReportID, first.LastMeaningfulReportID)
	require.Equal(t, []string{firstItem.ItemID}, first.LastMeaningfulItemIDs)
	require.NotEmpty(t, first.Claims)
	require.Contains(t, claimKinds(first.Claims), reportschema.ClaimObservedFact)
	statusClaim := claimByID(t, first.Claims, "claim-"+firstItem.ItemID+"-status")
	require.Equal(t, reportschema.SensitivityPublic, statusClaim.Sensitivity)
	firstPriorityDelta := fieldDeltaByField(t, first.FieldDeltas, "pinpoint."+firstItem.ItemID+".priority")
	require.Equal(t, reportschema.FieldChanged, firstPriorityDelta.State)
	require.Nil(t, firstPriorityDelta.PreviousHash)
	require.NotNil(t, firstPriorityDelta.CurrentHash)

	snapshot, err := reportStore.GetSnapshot(first.SnapshotID)
	require.NoError(t, err)
	require.Equal(t, SnapshotSchemaVersion, snapshot.SchemaVersion)
	require.Equal(t, "dogfood", snapshot.Channel)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, firstItem.ItemID, snapshot.Items[0].ID)

	second, err := reportStore.GenerateWithOptions("dogfood", now.Add(2*time.Minute), GenerateOptions{
		TriggerID:       "nudge-cycle-1",
		CheckedSurfaces: []string{"roadmap", "sessions", "logs"},
		CheckedWindow:   "2026-07-07T13:01:00Z/2026-07-07T13:02:00Z",
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
	require.Len(t, second.NegativeEvidence, 2)
	noDelta := negativeEvidenceByQuery(t, second.NegativeEvidence, "no_new_delta")
	require.Equal(t, reportschema.NegativeNotObservedInCheckedScope, noDelta.Status)
	require.Equal(t, reportschema.SensitivityInternal, noDelta.Sensitivity)
	require.Equal(t, []string{"roadmap", "sessions", "logs"}, noDelta.CheckedSurfaces)
	require.Equal(t, "2026-07-07T13:01:00Z/2026-07-07T13:02:00Z", noDelta.Window)
	noBlocker := negativeEvidenceByQuery(t, second.NegativeEvidence, "no_new_blocker")
	require.Equal(t, reportschema.NegativeNotObservedInCheckedScope, noBlocker.Status)
	require.NotEqual(t, noDelta.ID, noBlocker.ID)
	clearedDelta := fieldDeltaByField(t, second.FieldDeltas, "report.delta")
	require.Equal(t, reportschema.FieldCleared, clearedDelta.State)
	require.NotNil(t, clearedDelta.PreviousHash)
	require.Nil(t, clearedDelta.CurrentHash)
	carriedPriority := fieldDeltaByField(t, second.FieldDeltas, "pinpoint."+firstItem.ItemID+".priority")
	require.Equal(t, reportschema.FieldCarriedForward, carriedPriority.State)
	require.NotNil(t, carriedPriority.PreviousHash)
	require.NotNil(t, carriedPriority.CurrentHash)
	require.Equal(t, *carriedPriority.PreviousHash, *carriedPriority.CurrentHash)
	activeSessions := fieldDeltaByField(t, second.FieldDeltas, "report.active_sessions")
	require.Equal(t, reportschema.FieldChanged, activeSessions.State)
	blocker := fieldDeltaByField(t, second.FieldDeltas, "report.blocker")
	require.Equal(t, reportschema.FieldChanged, blocker.State)

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
	require.Empty(t, third.NegativeEvidence)
	require.ElementsMatch(t, []string{noBlocker.ID, noDelta.ID}, third.InvalidatesNegativeEvidence)
	changedPriority := fieldDeltaByField(t, third.FieldDeltas, "pinpoint."+firstItem.ItemID+".priority")
	require.Equal(t, reportschema.FieldChanged, changedPriority.State)
	require.NotNil(t, changedPriority.PreviousHash)
	require.NotNil(t, changedPriority.CurrentHash)
	require.NotEqual(t, *changedPriority.PreviousHash, *changedPriority.CurrentHash)
	require.Equal(t, reportschema.FieldCarriedForward, fieldDeltaByField(t, third.FieldDeltas, "pinpoint."+firstItem.ItemID+".lifecycle_state").State)
	require.Equal(t, reportschema.FieldChanged, fieldDeltaByField(t, third.FieldDeltas, "report.delta").State)
}

func TestGenerateLabelsAndPromotesClaims(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	filed, err := roadmapStore.File(roadmap.Filing{
		Title: "root cause needs confidence",
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceRootCauseHint,
			Type:      "log",
			Reference: "log:startup",
			Preview:   "startup timeout likely caused by missing readiness marker",
		}},
		Handoff: &roadmap.HandoffPacket{
			SuggestedVerification: []string{"go test ./internal/workerstate"},
		},
		Now: now,
	})
	require.NoError(t, err)

	first, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)
	rootClaim := claimByID(t, first.Claims, "claim-"+filed.ItemID+"-root-cause")
	require.Equal(t, reportschema.ClaimHypothesis, rootClaim.Kind)
	require.Equal(t, reportschema.ConfidenceMedium, rootClaim.Confidence)
	require.Equal(t, reportschema.SensitivityInternal, rootClaim.Sensitivity)
	require.Empty(t, rootClaim.PromotedFrom)
	require.Contains(t, rootClaim.Text, "startup timeout")
	require.Len(t, rootClaim.Evidence, 1)
	require.Contains(t, claimKinds(first.Claims), reportschema.ClaimRecommendation)

	updated, err := roadmapStore.File(roadmap.Filing{
		ID: filed.ItemID,
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceVerification,
			Type:      "test",
			Reference: "go-test",
			Preview:   "readiness marker test passes",
		}},
		Now: now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, filed.ItemID, updated.ItemID)

	second, err := reportStore.Generate("dogfood", now.Add(3*time.Minute))
	require.NoError(t, err)
	promoted := claimByID(t, second.Claims, rootClaim.ID)
	require.Equal(t, reportschema.ClaimObservedFact, promoted.Kind)
	require.Equal(t, reportschema.ConfidenceHigh, promoted.Confidence)
	require.Equal(t, reportschema.ClaimHypothesis, promoted.PromotedFrom)
	require.Len(t, promoted.Evidence, 2)
	require.Equal(t, filed.ItemID, promoted.ItemID)
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
	require.Len(t, report.NegativeEvidence, 2)
	noDelta := negativeEvidenceByQuery(t, report.NegativeEvidence, "no_new_delta")
	require.Equal(t, reportschema.NegativeUnknownNotChecked, noDelta.Status)
	require.Empty(t, noDelta.CheckedSurfaces)
	require.Equal(t, "2026-07-07T13:00:00Z/2026-07-07T13:00:00Z", noDelta.Window)
}

func TestProjectReportNegotiatesConsumerCapabilities(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	_, err := roadmapStore.File(roadmap.Filing{
		Title:    "projectable report",
		Priority: roadmap.PriorityP1,
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceRootCauseHint,
			Type:      "log",
			Reference: "log:projection",
			Preview:   "projection keeps canonical context",
		}},
		Now: now,
	})
	require.NoError(t, err)
	report, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)

	projection, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "legacy-claw",
		SchemaVersions: []string{"legacy.report.v1"},
		FieldFamilies:  []string{"claims", "field_deltas"},
	}, "legacy", "normal")
	require.NoError(t, err)

	require.Equal(t, reportschema.ReportingReportSchemaV1, projection.SchemaVersion)
	require.NotEmpty(t, projection.ProjectionID)
	require.Equal(t, "legacy", projection.View)
	require.True(t, projection.Provenance.Downgraded)
	require.Equal(t, reportschema.ReportingProjectionPolicyV1, projection.Provenance.PolicyID)
	require.Equal(t, "legacy-claw", projection.Provenance.Consumer.Consumer)
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "negative_evidence")
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "items")
	require.Equal(t, report.ReportID, projection.Provenance.SourceReportID)
	require.Equal(t, report.SnapshotID, projection.Provenance.SourceSnapshotID)
	require.Equal(t, report.Identity.ContentHash, projection.Provenance.SourceContentHash)
	require.False(t, projection.Provenance.SourceChanged)
	require.True(t, projection.Provenance.RenderingChanged)
	require.Equal(t, report.ReportID, projection.Payload["report_id"])
	require.Equal(t, report.Identity, projection.Payload["identity"])
	require.Contains(t, projection.Payload, "claims")
	require.Contains(t, projection.Payload, "field_deltas")
	require.NotContains(t, projection.Payload, "new_items")
	require.NotContains(t, projection.Payload, "negative_evidence")
	require.Len(t, projection.CanonicalReport.NewItems, 1)

	full, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "current-claw",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}, "full", "normal")
	require.NoError(t, err)
	require.False(t, full.Provenance.Downgraded)
	require.False(t, full.Provenance.RenderingChanged)
	require.Empty(t, full.Provenance.OmittedFieldFamilies)
	require.Contains(t, full.Payload, "new_items")
}

func TestProjectReportBuildsAudienceViews(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	filed, err := roadmapStore.File(roadmap.Filing{
		Title:    "audience projection",
		Priority: roadmap.PriorityP0,
		Severity: roadmap.SeverityCritical,
		Impact:   roadmap.ImpactObservabilityDebt,
		Handoff: &roadmap.HandoffPacket{
			SuggestedVerification: []string{"go test ./internal/reporting"},
		},
		Now: now,
	})
	require.NoError(t, err)
	report, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)

	brief, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "clawhip",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}, "delta_brief", "brief")
	require.NoError(t, err)
	require.True(t, brief.Provenance.Downgraded)
	require.Equal(t, "delta_brief", brief.View)
	require.Equal(t, "brief", brief.Verbosity)
	require.Equal(t, "delta_brief", brief.Provenance.View)
	require.Equal(t, "brief", brief.Provenance.Verbosity)
	require.Equal(t, report.ReportID, brief.Provenance.SourceReportID)
	require.Equal(t, report.Identity.ContentHash, brief.Provenance.SourceContentHash)
	require.False(t, brief.Provenance.SourceChanged)
	require.True(t, brief.Provenance.RenderingChanged)
	require.Contains(t, brief.Payload, "summary")
	require.Equal(t, report.Identity, brief.Payload["identity"])
	require.Contains(t, brief.Payload, "top_items")
	require.NotContains(t, brief.Payload, "negative_evidence")
	require.Len(t, brief.CanonicalReport.NewItems, 1)

	human, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "human-operator",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}, "human_readable", "verbose")
	require.NoError(t, err)
	require.Equal(t, "human_readable", human.View)
	require.Equal(t, brief.Provenance.SourceContentHash, human.Provenance.SourceContentHash)
	require.NotEqual(t, brief.ProjectionID, human.ProjectionID)
	require.Contains(t, human.Payload["summary_text"], "New roadmap")
	require.Contains(t, human.Payload, "highlights")
	require.Contains(t, human.Payload, "next_actions")

	sync, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "roadmap-sync",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}, "roadmap_sync", "normal")
	require.NoError(t, err)
	require.Contains(t, sync.Payload, "roadmap_items")
	require.Equal(t, report.ReportID, sync.Provenance.SourceReportID)
	require.Equal(t, brief.Provenance.SourceContentHash, sync.Provenance.SourceContentHash)
	items, ok := sync.Payload["roadmap_items"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Equal(t, filed.ItemID, items[0]["id"])

	briefAgain, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "clawhip",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}, "delta_brief", "brief")
	require.NoError(t, err)
	require.Equal(t, brief.ProjectionID, briefAgain.ProjectionID)
	require.Equal(t, brief.Provenance.SourceContentHash, briefAgain.Provenance.SourceContentHash)
}

func TestReportingSensitivityRankUsesInternalDefaultAndSuggestions(t *testing.T) {
	rank, err := reportingSensitivityRank("")
	require.NoError(t, err)
	require.Equal(t, 2, rank)

	_, err = reportingSensitivityRank("operatr_only")
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown sensitivity "operatr_only"`)
	require.Contains(t, err.Error(), `did you mean "operator_only"`)
}

func TestProjectReportRedactsBySensitivityPolicy(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	filed, err := roadmapStore.File(roadmap.Filing{
		Title:    "redacted projection",
		Priority: roadmap.PriorityP1,
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceRootCauseHint,
			Type:      "log",
			Reference: "log:redaction",
			Preview:   "operator trace includes internal queue names",
		}},
		Handoff: &roadmap.HandoffPacket{
			SuggestedVerification: []string{"go test ./internal/reporting"},
		},
		Now: now,
	})
	require.NoError(t, err)
	report, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)

	projection, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "public-viewer",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
		MaxSensitivity: reportschema.SensitivityPublic,
	}, "full", "verbose")
	require.NoError(t, err)

	require.Equal(t, report.ReportID, projection.Provenance.SourceReportID)
	require.Equal(t, report.SnapshotID, projection.Provenance.SourceSnapshotID)
	require.Equal(t, report.Identity.ContentHash, projection.Provenance.SourceContentHash)
	require.NotEmpty(t, projection.Provenance.Redactions)
	require.True(t, hasRedactionPath(projection.Provenance.Redactions, func(path string) bool {
		return strings.HasPrefix(path, "claims[") && strings.HasSuffix(path, "].text")
	}))
	require.Contains(t, redactionPaths(projection.Provenance.Redactions), "new_items[0].claims")
	require.Contains(t, redactionPaths(projection.Provenance.Redactions), "new_items[0].handoff")

	claims, ok := projection.Payload["claims"].([]ClaimSummary)
	require.True(t, ok)
	require.Len(t, claims, len(report.Claims))
	publicClaimSeen := false
	redactedClaimSeen := false
	for _, claim := range claims {
		if claim.Sensitivity == reportschema.SensitivityPublic && strings.Contains(claim.Text, filed.ItemID) {
			publicClaimSeen = true
		}
		if claim.Sensitivity == reportschema.SensitivityInternal && claim.Text == "<redacted>" && len(claim.Evidence) == 0 {
			redactedClaimSeen = true
		}
	}
	require.True(t, publicClaimSeen)
	require.True(t, redactedClaimSeen)

	items, ok := projection.Payload["new_items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, filed.ItemID, item["id"])
	require.NotContains(t, item, "claims")
	require.NotContains(t, item, "handoff")
	require.NotContains(t, item, "evidence_refs")
	require.NotContains(t, item, "implementation")
	require.Contains(t, claimByID(t, projection.CanonicalReport.Claims, "claim-"+filed.ItemID+"-root-cause").Text, "operator trace")
	require.NotNil(t, projection.CanonicalReport.NewItems[0].Handoff)
}

func TestProjectReportRecordsDeterministicIdentityInputs(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	_, err := roadmapStore.File(roadmap.Filing{
		Title:    "deterministic projection",
		Priority: roadmap.PriorityP1,
		Evidence: []roadmap.EvidenceAttachment{{
			Role:      roadmap.EvidenceRootCauseHint,
			Type:      "log",
			Reference: "log:deterministic",
			Preview:   "internal render detail",
		}},
		Now: now,
	})
	require.NoError(t, err)
	report, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)
	capabilities := reportschema.ConsumerCapabilities{
		Consumer:       "public-viewer",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
		MaxSensitivity: reportschema.SensitivityPublic,
	}

	first, err := ProjectReport(report, capabilities, "full", "verbose")
	require.NoError(t, err)
	second, err := ProjectReport(report, capabilities, "full", "verbose")
	require.NoError(t, err)

	require.Equal(t, first.ProjectionID, second.ProjectionID)
	require.Equal(t, first.Provenance.IdentityInputHash, second.Provenance.IdentityInputHash)
	require.Equal(t, first.Provenance.PayloadHash, second.Provenance.PayloadHash)
	require.Equal(t, first.Provenance.CanonicalEquivalenceHash, second.Provenance.CanonicalEquivalenceHash)
	require.Equal(t, first.ProjectionID, first.Provenance.CanonicalEquivalenceHash)
	require.Equal(t, reportschema.ReportingReportSchemaV1, first.Provenance.IdentityInputs.ProjectionSchemaVersion)
	require.Equal(t, report.SchemaVersion, first.Provenance.IdentityInputs.SourceSchemaVersion)
	require.Equal(t, report.Identity.ContentHash, first.Provenance.IdentityInputs.SourceContentHash)
	require.Equal(t, reportschema.ReportingProjectionPolicyV1, first.Provenance.IdentityInputs.ProjectionPolicyID)
	require.Equal(t, "public-viewer", first.Provenance.IdentityInputs.Consumer.Consumer)
	require.Equal(t, reportschema.SensitivityPublic, first.Provenance.IdentityInputs.Consumer.MaxSensitivity)
	require.Equal(t, "full", first.Provenance.IdentityInputs.View)
	require.Equal(t, "verbose", first.Provenance.IdentityInputs.Verbosity)
	require.True(t, first.Provenance.RenderingChanged)

	canonicalHash, err := ProjectionCanonicalHash(first)
	require.NoError(t, err)
	require.Equal(t, first.Provenance.CanonicalEquivalenceHash, canonicalHash)
	data, err := json.MarshalIndent(first, "", "  ")
	require.NoError(t, err)
	var roundTripped ReportProjection
	require.NoError(t, json.Unmarshal(data, &roundTripped))
	roundTripHash, err := ProjectionCanonicalHash(roundTripped)
	require.NoError(t, err)
	require.Equal(t, canonicalHash, roundTripHash)

	internal, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "public-viewer",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
		MaxSensitivity: reportschema.SensitivityInternal,
	}, "full", "verbose")
	require.NoError(t, err)
	require.Equal(t, first.Provenance.SourceContentHash, internal.Provenance.SourceContentHash)
	require.NotEqual(t, first.Provenance.IdentityInputHash, internal.Provenance.IdentityInputHash)
	require.NotEqual(t, first.Provenance.PayloadHash, internal.Provenance.PayloadHash)
	require.NotEqual(t, first.ProjectionID, internal.ProjectionID)
}

func TestProjectReportOmitNegativeEvidenceForPublicPolicy(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	_, err := roadmapStore.File(roadmap.Filing{
		Title: "negative evidence projection",
		Now:   now,
	})
	require.NoError(t, err)
	_, err = reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)
	report, err := reportStore.GenerateWithOptions("dogfood", now.Add(2*time.Minute), GenerateOptions{
		CheckedSurfaces: []string{"roadmap", "sessions"},
	})
	require.NoError(t, err)
	require.Len(t, report.NegativeEvidence, 2)

	projection, err := ProjectReport(report, reportschema.ConsumerCapabilities{
		Consumer:       "public-audit",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
		MaxSensitivity: reportschema.SensitivityPublic,
	}, "ops_audit", "normal")
	require.NoError(t, err)

	require.NotContains(t, projection.Payload, "negative_evidence")
	require.Contains(t, redactionPaths(projection.Provenance.Redactions), "negative_evidence")
	require.Len(t, projection.CanonicalReport.NegativeEvidence, 2)
	require.Equal(t, reportschema.SensitivityInternal, projection.CanonicalReport.NegativeEvidence[0].Sensitivity)
}

func TestProjectReportCacheMarksDuplicatesAndSupersededViews(t *testing.T) {
	configHome := t.TempDir()
	roadmapStore := roadmap.NewStore(configHome)
	reportStore := NewStore(configHome)
	now := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	filed, err := roadmapStore.File(roadmap.Filing{
		Title:    "cached projection",
		Priority: roadmap.PriorityP1,
		Now:      now,
	})
	require.NoError(t, err)
	report, err := reportStore.Generate("dogfood", now.Add(time.Minute))
	require.NoError(t, err)
	capabilities := reportschema.ConsumerCapabilities{
		Consumer:       "clawhip",
		SchemaVersions: []string{reportschema.ReportingReportSchemaV1},
	}

	first, err := reportStore.ProjectReportCached(report, capabilities, "delta_brief", "brief")
	require.NoError(t, err)
	require.NotEmpty(t, first.Provenance.CacheKey)
	require.True(t, first.Provenance.LatestCompatible)
	require.False(t, first.Provenance.StaleCached)
	require.False(t, first.Provenance.SourceChanged)
	require.Empty(t, first.Provenance.SupersedesProjectionID)

	duplicate, err := reportStore.ProjectReportCached(report, capabilities, "delta_brief", "brief")
	require.NoError(t, err)
	require.Equal(t, first.ProjectionID, duplicate.ProjectionID)
	require.Equal(t, first.ProjectionID, duplicate.Provenance.DuplicateOfProjectionID)
	require.Equal(t, first.Provenance.CacheKey, duplicate.Provenance.CacheKey)
	require.False(t, duplicate.Provenance.SourceChanged)

	_, err = roadmapStore.File(roadmap.Filing{
		ID:       filed.ItemID,
		Priority: roadmap.PriorityP0,
		Now:      now.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	changed, err := reportStore.Generate("dogfood", now.Add(3*time.Minute))
	require.NoError(t, err)
	second, err := reportStore.ProjectReportCached(changed, capabilities, "delta_brief", "brief")
	require.NoError(t, err)

	require.NotEqual(t, first.Provenance.SourceContentHash, second.Provenance.SourceContentHash)
	require.NotEqual(t, first.ProjectionID, second.ProjectionID)
	require.True(t, second.Provenance.SourceChanged)
	require.True(t, second.Provenance.RenderingChanged)
	require.True(t, second.Provenance.LatestCompatible)
	require.False(t, second.Provenance.StaleCached)
	require.Equal(t, first.ProjectionID, second.Provenance.SupersedesProjectionID)
	require.Empty(t, second.Provenance.DuplicateOfProjectionID)
}

func claimByID(t *testing.T, claims []ClaimSummary, id string) ClaimSummary {
	t.Helper()
	for _, claim := range claims {
		if claim.ID == id {
			return claim
		}
	}
	t.Fatalf("missing claim %q in %#v", id, claims)
	return ClaimSummary{}
}

func claimKinds(claims []ClaimSummary) []string {
	kinds := make([]string, 0, len(claims))
	for _, claim := range claims {
		kinds = append(kinds, claim.Kind)
	}
	return kinds
}

func negativeEvidenceByQuery(t *testing.T, values []reportschema.NegativeEvidence, query string) reportschema.NegativeEvidence {
	t.Helper()
	for _, value := range values {
		if value.Query == query {
			return value
		}
	}
	t.Fatalf("missing negative evidence query %q in %#v", query, values)
	return reportschema.NegativeEvidence{}
}

func fieldDeltaByField(t *testing.T, values []reportschema.FieldDelta, field string) reportschema.FieldDelta {
	t.Helper()
	for _, value := range values {
		if value.Field == field {
			return value
		}
	}
	t.Fatalf("missing field delta %q in %#v", field, values)
	return reportschema.FieldDelta{}
}

func redactionPaths(values []reportschema.RedactionProvenance) []string {
	paths := make([]string, 0, len(values))
	for _, value := range values {
		paths = append(paths, value.FieldPath)
	}
	return paths
}

func hasRedactionPath(values []reportschema.RedactionProvenance, matches func(string) bool) bool {
	for _, value := range values {
		if matches(value.FieldPath) {
			return true
		}
	}
	return false
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
