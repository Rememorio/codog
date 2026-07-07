package reportconformance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsCompleteConsumerBundle(t *testing.T) {
	result, err := ValidateJSON([]byte(validBundleJSON()))
	require.NoError(t, err)

	require.True(t, result.Valid)
	require.True(t, result.ParsePassed)
	require.True(t, result.SemanticPassed)
	require.Equal(t, 4, result.RequiredCaseCount)
	require.Equal(t, 4, result.PassedCaseCount)
	require.Equal(t, 0, result.ErrorCount)
	require.NotNil(t, result.LastPassed)
	require.Equal(t, "fixture-consumer", result.LastPassed.Consumer)
	require.Equal(t, "1.2.3", result.LastPassed.Version)
	require.Equal(t, FixtureSetVersion, result.LastPassed.FixtureSet)
	require.Equal(t, "2026-07-07T16:30:00Z", result.LastPassed.PassedAt)
	require.Len(t, result.RequiredCases, 4)
}

func TestValidateSeparatesParseAndSemanticFailures(t *testing.T) {
	var bundle Bundle
	require.NoError(t, json.Unmarshal([]byte(validBundleJSON()), &bundle))
	bundle.FixtureSet = "old-fixture"
	bundle.Cases[0].ProjectionID = "wrong"
	bundle.Cases[0].SemanticChecks.RedactedFieldsHandled = false
	bundle.Cases[3].Parsed = false

	result := Validate(bundle)

	require.False(t, result.Valid)
	require.False(t, result.ParsePassed)
	require.False(t, result.SemanticPassed)
	require.Nil(t, result.LastPassed)
	messages := errorMessages(result)
	require.Contains(t, messages, "/fixture_set [parse]: expected \"reporting-projection-golden-v1\"")
	require.Contains(t, messages, "/cases/public_full_redacts_internal_claims_and_item_detail/projection_id [parse]: expected \"8726177642a65d63\"")
	require.Contains(t, messages, "/cases/public_full_redacts_internal_claims_and_item_detail/semantic_checks/redacted_fields_handled [semantic]: required semantic check not satisfied")
	require.Contains(t, messages, "/cases/public_ops_audit_omits_negative_evidence/parsed [parse]: consumer did not parse fixture projection")
}

func TestValidateRequiresAllConformanceCases(t *testing.T) {
	var bundle Bundle
	require.NoError(t, json.Unmarshal([]byte(validBundleJSON()), &bundle))
	bundle.Cases = bundle.Cases[:2]

	result := Validate(bundle)

	require.False(t, result.Valid)
	require.Equal(t, 2, result.PassedCaseCount)
	messages := errorMessages(result)
	require.Contains(t, messages, "/cases/brief_audience_projection_locks_summary_shape [parse]: required conformance case missing")
	require.Contains(t, messages, "/cases/public_ops_audit_omits_negative_evidence [parse]: required conformance case missing")
}

func TestValidateReportsMalformedEnvelope(t *testing.T) {
	result := Validate(Bundle{
		SchemaVersion: "wrong",
		FixtureSet:    FixtureSetVersion,
		PassedAt:      "not-time",
		Cases: []CaseResult{{
			Name: "public_full_redacts_internal_claims_and_item_detail",
		}},
	})

	require.False(t, result.Valid)
	messages := errorMessages(result)
	require.Contains(t, messages, "/schema_version [parse]: expected \"codog.reporting.consumer_conformance.v1\"")
	require.Contains(t, messages, "/consumer/name [parse]: required string field missing")
	require.Contains(t, messages, "/consumer/version [parse]: required string field missing")
	require.Contains(t, messages, "/passed_at [parse]: must be RFC3339 timestamp")
}

func errorMessages(result Result) []string {
	messages := make([]string, 0, len(result.Errors))
	for _, err := range result.Errors {
		messages = append(messages, err.Path+" ["+err.Kind+"]: "+err.Message)
	}
	return messages
}

func validBundleJSON() string {
	return `{
  "schema_version": "codog.reporting.consumer_conformance.v1",
  "fixture_set": "reporting-projection-golden-v1",
  "consumer": {
    "name": "fixture-consumer",
    "version": "1.2.3"
  },
  "passed_at": "2026-07-07T16:30:00Z",
  "cases": [
    {
      "name": "public_full_redacts_internal_claims_and_item_detail",
      "projection_id": "8726177642a65d63",
      "parsed": true,
      "semantic_checks": {
        "canonical_identity_correlated": true,
        "redacted_fields_handled": true,
        "missing_fields_distinguished": true
      }
    },
    {
      "name": "legacy_claim_delta_projection_downgrades_capabilities",
      "projection_id": "cb803e478e96aa95",
      "parsed": true,
      "semantic_checks": {
        "canonical_identity_correlated": true,
        "downgrade_handled": true,
        "missing_fields_distinguished": true
      }
    },
    {
      "name": "brief_audience_projection_locks_summary_shape",
      "projection_id": "cbaf320e33ad2207",
      "parsed": true,
      "semantic_checks": {
        "canonical_identity_correlated": true
      }
    },
    {
      "name": "public_ops_audit_omits_negative_evidence",
      "projection_id": "001261a0da099142",
      "parsed": true,
      "semantic_checks": {
        "canonical_identity_correlated": true,
        "redacted_fields_handled": true,
        "missing_fields_distinguished": true,
        "no_change_handled": true,
        "freshness_handled": true
      }
    }
  ]
}`
}
