package reportschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixtureReport(t *testing.T) CanonicalReport {
	t.Helper()
	previousHash := "prev123"
	report, err := Canonicalize(CanonicalReport{
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		Claims: []Claim{
			{
				ID:          "claim-secret",
				Kind:        ClaimObservedFact,
				Text:        "secret token appeared in logs",
				Confidence:  ConfidenceHigh,
				Evidence:    []string{"log:secret"},
				Sensitivity: SensitivitySecret,
			},
			{
				ID:          "claim-hypothesis",
				Kind:        ClaimHypothesis,
				Text:        "transport restart likely caused the retry",
				Confidence:  ConfidenceMedium,
				Evidence:    []string{"event:transport"},
				Sensitivity: SensitivityInternal,
			},
			{
				ID:          "claim-fact",
				Kind:        ClaimObservedFact,
				Text:        "lane finished once",
				Confidence:  ConfidenceHigh,
				Evidence:    []string{"event:lane.finished"},
				Sensitivity: SensitivityPublic,
			},
		},
		NegativeEvidence: []NegativeEvidence{{
			ID:              "neg-blocker",
			Status:          NegativeNotObservedInCheckedScope,
			CheckedSurfaces: []string{"lane_events", "worker_status"},
			Query:           "current blocker",
			Window:          "2026-05-14T00:00:00Z/2026-05-14T00:05:00Z",
			Sensitivity:     SensitivityPublic,
		}},
		FieldDeltas: []FieldDelta{{
			Field:        "blocker",
			State:        FieldCleared,
			PreviousHash: &previousHash,
			Attribution:  "lane.failed reconciled to lane.finished",
		}},
	})
	require.NoError(t, err)
	return report
}

func TestRegistryV1IsSelfDescribing(t *testing.T) {
	registry := RegistryV1()

	require.Equal(t, SchemaV1, registry.SchemaVersion)
	require.Contains(t, fieldIDs(registry), "claims[].kind")
	require.Contains(t, fieldIDs(registry), "negative_evidence[]")
	require.Contains(t, enumValuesByField(t, registry, "claims[].kind"), ClaimHypothesis)
	require.Contains(t, enumValuesByField(t, registry, "field_deltas[].state"), FieldCarriedForward)
	require.Contains(t, enumValuesByField(t, registry, "claims[].sensitivity"), SensitivitySecret)
	require.Contains(t, enumValuesByField(t, registry, "negative_evidence[].sensitivity"), SensitivityInternal)
	require.Contains(t, enumValuesByField(t, registry, "new_items[].sensitivity"), SensitivityOperatorOnly)
	require.Contains(t, enumValuesByField(t, registry, "view"), "delta_brief")
	require.Contains(t, enumValuesByField(t, registry, "verbosity"), "verbose")
	require.Contains(t, fieldIDs(registry), "identity.canonical_fingerprint")
	require.Contains(t, fieldIDs(registry), "field_sensitivity")
	require.False(t, fieldByID(t, registry, "schema_compatibility.policy").Deprecated)
	require.Contains(t, fieldIDs(registry), "atomic_update.message_parts[]")
	require.Contains(t, fieldIDs(registry), "projection.provenance.redactions[]")
	require.Contains(t, fieldIDs(registry), "projection.provenance.redactions[].original_hash")
	require.Contains(t, reportSchemaVersions(registry), SchemaV1)
	require.Contains(t, reportSchemaVersions(registry), ReportingReportSchemaV1)
	require.Contains(t, reportSchemaVersions(registry), ReportingSnapshotSchemaV1)
	require.Contains(t, reportSchemaVersions(registry), MockParityReportSchemaV1)
	require.Contains(t, reportSchemaVersions(registry), MockParityManifestSchemaV1)

	backpressure := registryReportByID(t, registry, "report_backpressure")
	require.Equal(t, ReportingReportSchemaV1, backpressure.SchemaVersion)
	require.Contains(t, backpressure.Fields, "identity")
	require.Contains(t, backpressure.Fields, "schema_compatibility")
	require.Contains(t, backpressure.Fields, "field_deltas")
	require.Contains(t, backpressure.Fields, "field_sensitivity")
	projection := registryReportByID(t, registry, "report_backpressure_projection")
	require.Contains(t, projection.Fields, "canonical_report")
	require.Contains(t, projection.Fields, "view")
	require.Contains(t, projection.Fields, "projection.provenance.redactions[]")

	mockParity := registryReportByID(t, registry, "mock_parity_report")
	require.Equal(t, "codog mock-parity --json", mockParity.Command)
	require.Contains(t, mockParity.Fields, "coverage")
	require.Contains(t, mockParity.Fields, "usage_summary")
}

func TestFilterRegistryForReportVersionAndCapabilities(t *testing.T) {
	filtered := FilterRegistry(RegistryV1(), RegistryFilter{
		ReportIDs:      []string{"report_backpressure"},
		SchemaVersions: []string{ReportingReportSchemaV1},
		FieldFamilies:  []string{"field_deltas"},
	})

	require.Len(t, filtered.Reports, 1)
	require.Equal(t, "report_backpressure", filtered.Reports[0].ID)
	require.Equal(t, []string{"field_deltas[]", "field_deltas[].state"}, fieldIDs(filtered))
	require.Contains(t, enumValuesByField(t, filtered, "field_deltas[].state"), FieldChanged)
}

func TestCanonicalizeSortsAndHashesReport(t *testing.T) {
	report := fixtureReport(t)

	require.Equal(t, SchemaV1, report.SchemaVersion)
	require.Contains(t, report.Identity.ReportID, "report-")
	require.Len(t, report.Identity.ContentHash, 16)
	require.Equal(t, "claim-fact", report.Claims[0].ID)
	require.Equal(t, ClaimHypothesis, report.Claims[1].Kind)
	require.Equal(t, ConfidenceMedium, report.Claims[1].Confidence)
	require.Equal(t, NegativeNotObservedInCheckedScope, report.NegativeEvidence[0].Status)
	require.Equal(t, FieldCleared, report.FieldDeltas[0].State)
}

func TestCanonicalizeBindsAtomicUpdateAndOrdersMessageParts(t *testing.T) {
	report, err := Canonicalize(CanonicalReport{
		Identity:    Identity{ReportID: "report-atomic-1"},
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		AtomicUpdate: &AtomicUpdate{
			ActiveSessions: []string{"session-b", "session-a", "session-a"},
			ExactPinpoint:  "4.13 message atomicity",
			ConcreteDelta:  "split report transport added",
			Blocker:        "none",
			MessageParts: []MessagePart{
				{PartIndex: 1, PartCount: 3, Content: "delta; "},
				{PartIndex: 2, PartCount: 3, Content: "blocker."},
				{PartIndex: 0, PartCount: 3, Content: "pinpoint; "},
			},
		},
	})
	require.NoError(t, err)

	require.Equal(t, "report-atomic-1", report.AtomicUpdate.ReportID)
	require.Equal(t, []string{"session-a", "session-b"}, report.AtomicUpdate.ActiveSessions)
	require.True(t, report.AtomicUpdate.MessageComplete)
	require.Equal(t, []int{0, 1, 2}, messagePartIndexes(report.AtomicUpdate.MessageParts))
	for _, part := range report.AtomicUpdate.MessageParts {
		require.Equal(t, "report-atomic-1", part.ReportID)
		require.NotEmpty(t, part.ContentHash)
	}

	message, complete, err := ReconstructAtomicMessage(*report.AtomicUpdate)
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, "pinpoint; delta; blocker.", message)
}

func TestCanonicalizeKeepsAtomicFieldsStableForPartialBursts(t *testing.T) {
	report, err := Canonicalize(CanonicalReport{
		Identity:    Identity{ReportID: "report-partial-1"},
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		AtomicUpdate: &AtomicUpdate{
			ExactPinpoint: "4.13",
			ConcreteDelta: "first and last fragment arrived",
			Blocker:       "middle fragment missing",
			MessageParts: []MessagePart{
				{PartIndex: 2, PartCount: 3, Content: "tail"},
				{PartIndex: 0, PartCount: 3, Content: "head"},
			},
		},
	})
	require.NoError(t, err)

	require.False(t, report.AtomicUpdate.MessageComplete)
	require.Equal(t, "4.13", report.AtomicUpdate.ExactPinpoint)
	require.Equal(t, "first and last fragment arrived", report.AtomicUpdate.ConcreteDelta)
	require.Equal(t, "middle fragment missing", report.AtomicUpdate.Blocker)

	message, complete, err := ReconstructAtomicMessage(*report.AtomicUpdate)
	require.NoError(t, err)
	require.False(t, complete)
	require.Equal(t, "headtail", message)
}

func TestCanonicalizeRejectsMismatchedAtomicReportID(t *testing.T) {
	_, err := Canonicalize(CanonicalReport{
		Identity: Identity{ReportID: "report-parent"},
		AtomicUpdate: &AtomicUpdate{
			ReportID: "report-other",
		},
	})
	require.ErrorContains(t, err, "does not match report identity")

	_, err = Canonicalize(CanonicalReport{
		Identity: Identity{ReportID: "report-parent"},
		AtomicUpdate: &AtomicUpdate{
			MessageParts: []MessagePart{{
				ReportID:  "report-other",
				PartIndex: 0,
				PartCount: 1,
				Content:   "body",
			}},
		},
	})
	require.ErrorContains(t, err, "does not match report identity")
}

func TestAtomicReportIDDoesNotPerturbContentHash(t *testing.T) {
	left, err := Canonicalize(CanonicalReport{
		Identity:    Identity{ReportID: "report-left"},
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		AtomicUpdate: &AtomicUpdate{
			MessageParts: []MessagePart{{PartIndex: 0, PartCount: 1, Content: "same"}},
		},
	})
	require.NoError(t, err)
	right, err := Canonicalize(CanonicalReport{
		Identity:    Identity{ReportID: "report-right"},
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		AtomicUpdate: &AtomicUpdate{
			MessageParts: []MessagePart{{PartIndex: 0, PartCount: 1, Content: "same"}},
		},
	})
	require.NoError(t, err)

	require.Equal(t, left.Identity.ContentHash, right.Identity.ContentHash)
}

func TestProjectIsDeterministicAndRecordsRedactionProvenance(t *testing.T) {
	report := fixtureReport(t)
	capabilities := ConsumerCapabilities{
		Consumer:       "clawhip",
		SchemaVersions: []string{SchemaV1},
		FieldFamilies:  []string{"claims", "negative_evidence", "field_deltas"},
		MaxSensitivity: SensitivityPublic,
	}

	first, err := Project(report, capabilities, "delta_brief")
	require.NoError(t, err)
	second, err := Project(report, capabilities, "delta_brief")
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Equal(t, report.Identity.ReportID, first.Provenance.SourceReportID)
	require.Equal(t, report.Identity.ContentHash, first.Provenance.SourceContentHash)
	require.True(t, first.Provenance.Downgraded)
	require.Len(t, first.Provenance.Redactions, 2)
	require.Contains(t, redactionPaths(first.Provenance.Redactions), "claims[1].text")
	require.Contains(t, redactionPaths(first.Provenance.Redactions), "claims[2]")
}

func TestProjectOmitsUnsupportedFamilies(t *testing.T) {
	report := fixtureReport(t)
	capabilities := ConsumerCapabilities{
		Consumer:       "legacy",
		SchemaVersions: []string{SchemaV1},
		FieldFamilies:  []string{"claims"},
		MaxSensitivity: SensitivityInternal,
	}

	projection, err := Project(report, capabilities, "legacy_view")
	require.NoError(t, err)

	require.True(t, projection.Provenance.Downgraded)
	require.Equal(t, []string{"negative_evidence", "field_deltas", "atomic_update"}, projection.Provenance.OmittedFieldFamilies)
	require.Contains(t, projection.Payload, "claims")
	require.NotContains(t, projection.Payload, "negative_evidence")
	require.NotContains(t, projection.Payload, "field_deltas")
}

func TestProjectUsesEmptyArraysForSupportedEmptyFamilies(t *testing.T) {
	report, err := Canonicalize(CanonicalReport{
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
	})
	require.NoError(t, err)

	projection, err := Project(report, ConsumerCapabilities{
		Consumer:       "viewer",
		SchemaVersions: []string{SchemaV1},
		MaxSensitivity: SensitivityPublic,
	}, "default")
	require.NoError(t, err)
	data, err := json.Marshal(projection.Payload)
	require.NoError(t, err)

	require.Contains(t, string(data), `"claims":[]`)
	require.Contains(t, string(data), `"negative_evidence":[]`)
	require.Contains(t, string(data), `"field_deltas":[]`)
	require.NotContains(t, string(data), `"negative_evidence":null`)
	require.NotContains(t, string(data), `"field_deltas":null`)
}

func TestProjectIncludesAtomicUpdateWhenSupported(t *testing.T) {
	report, err := Canonicalize(CanonicalReport{
		Identity:    Identity{ReportID: "report-atomic-1"},
		GeneratedAt: "2026-05-14T00:00:00Z",
		Producer:    "worker-1",
		AtomicUpdate: &AtomicUpdate{
			ExactPinpoint: "4.13",
			MessageParts:  []MessagePart{{PartIndex: 0, PartCount: 1, Content: "one"}},
		},
	})
	require.NoError(t, err)

	projection, err := Project(report, ConsumerCapabilities{
		Consumer:       "discord-bot",
		SchemaVersions: []string{SchemaV1},
		FieldFamilies:  []string{"atomic_update"},
		MaxSensitivity: SensitivityPublic,
	}, "dogfood")
	require.NoError(t, err)

	require.Contains(t, projection.Payload, "atomic_update")
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "claims")
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "negative_evidence")
	require.Contains(t, projection.Provenance.OmittedFieldFamilies, "field_deltas")
}

func TestStableJSONHashIgnoresMapKeyOrder(t *testing.T) {
	var left any
	var right any
	require.NoError(t, json.Unmarshal([]byte(`{"b":2,"a":{"z":1,"y":0}}`), &left))
	require.NoError(t, json.Unmarshal([]byte(`{"a":{"y":0,"z":1},"b":2}`), &right))

	leftHash, err := StableJSONHash(left)
	require.NoError(t, err)
	rightHash, err := StableJSONHash(right)
	require.NoError(t, err)

	require.Equal(t, leftHash, rightHash)
}

func fieldIDs(registry Registry) []string {
	ids := make([]string, 0, len(registry.Fields))
	for _, field := range registry.Fields {
		ids = append(ids, field.ID)
	}
	return ids
}

func reportSchemaVersions(registry Registry) []string {
	versions := make([]string, 0, len(registry.Reports))
	for _, report := range registry.Reports {
		versions = append(versions, report.SchemaVersion)
	}
	return versions
}

func registryReportByID(t *testing.T, registry Registry, id string) RegistryReport {
	t.Helper()
	for _, report := range registry.Reports {
		if report.ID == id {
			return report
		}
	}
	t.Fatalf("missing registry report %q in %#v", id, registry.Reports)
	return RegistryReport{}
}

func fieldByID(t *testing.T, registry Registry, id string) RegistryField {
	t.Helper()
	for _, field := range registry.Fields {
		if field.ID == id {
			return field
		}
	}
	t.Fatalf("missing registry field %q in %#v", id, registry.Fields)
	return RegistryField{}
}

func enumValuesByField(t *testing.T, registry Registry, id string) []string {
	t.Helper()
	return fieldByID(t, registry, id).EnumValues
}

func redactionPaths(redactions []RedactionProvenance) []string {
	paths := make([]string, 0, len(redactions))
	for _, redaction := range redactions {
		paths = append(paths, redaction.FieldPath)
	}
	return paths
}

func messagePartIndexes(parts []MessagePart) []int {
	indexes := make([]int, 0, len(parts))
	for _, part := range parts {
		indexes = append(indexes, part.PartIndex)
	}
	return indexes
}
