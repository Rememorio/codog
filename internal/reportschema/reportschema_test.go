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
	require.Contains(t, fieldIDs(registry), "projection.provenance.redactions[]")
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
	require.Equal(t, []string{"negative_evidence", "field_deltas"}, projection.Provenance.OmittedFieldFamilies)
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

func redactionPaths(redactions []RedactionProvenance) []string {
	paths := make([]string, 0, len(redactions))
	for _, redaction := range redactions {
		paths = append(paths, redaction.FieldPath)
	}
	return paths
}
