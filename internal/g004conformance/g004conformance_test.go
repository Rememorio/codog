package g004conformance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsCompleteBundle(t *testing.T) {
	result, err := ValidateJSON([]byte(validBundleJSON()))
	require.NoError(t, err)

	require.True(t, result.Valid)
	require.Equal(t, 0, result.ErrorCount)
	require.Empty(t, result.Errors)
}

func TestValidateReportsTerminalFingerprintAndSequenceErrors(t *testing.T) {
	var bundle map[string]any
	require.NoError(t, json.Unmarshal([]byte(validBundleJSON()), &bundle))
	events := bundle["laneEvents"].([]any)
	first := events[0].(map[string]any)
	first["event"] = "lane.finished"
	metadata := first["metadata"].(map[string]any)
	delete(metadata, "eventFingerprint")
	second := events[1].(map[string]any)
	second["metadata"].(map[string]any)["seq"] = float64(1)

	result := Validate(bundle)

	require.False(t, result.Valid)
	require.Contains(t, errorMessages(result), "/laneEvents/0/metadata/eventFingerprint: required string field missing")
	require.Contains(t, errorMessages(result), "/laneEvents/1/metadata/seq: sequence must be strictly increasing")
}

func TestValidateReportsReportFindingAndApprovalErrors(t *testing.T) {
	var bundle map[string]any
	require.NoError(t, json.Unmarshal([]byte(validBundleJSON()), &bundle))
	report := bundle["reports"].([]any)[0].(map[string]any)
	report["schemaVersion"] = "wrong"
	report["findings"].([]any)[0].(map[string]any)["confidence"] = "certain"
	token := bundle["approvalTokens"].([]any)[0].(map[string]any)
	token["oneTimeUse"] = false
	delete(token, "replayPreventionNonce")

	result := Validate(bundle)

	require.False(t, result.Valid)
	messages := errorMessages(result)
	require.Contains(t, messages, "/reports/0/schemaVersion: expected 'g004.report.v1', got 'wrong'")
	require.Contains(t, messages, "/reports/0/findings/0/confidence: 'certain' is not one of low, medium, high")
	require.Contains(t, messages, "/approvalTokens/0/oneTimeUse: must be true")
	require.Contains(t, messages, "/approvalTokens/0/replayPreventionNonce: required string field missing")
}

func TestValidateRequiresNonEmptyArrays(t *testing.T) {
	result, err := ValidateJSON([]byte(`{"schemaVersion":"g004.contract.bundle.v1","laneEvents":[]}`))
	require.NoError(t, err)

	require.False(t, result.Valid)
	messages := errorMessages(result)
	require.Contains(t, messages, "/laneEvents: array must not be empty")
	require.Contains(t, messages, "/reports: required array field missing")
	require.Contains(t, messages, "/approvalTokens: required array field missing")
}

func errorMessages(result Result) []string {
	messages := make([]string, 0, len(result.Errors))
	for _, err := range result.Errors {
		messages = append(messages, err.Path+": "+err.Message)
	}
	return messages
}

func validBundleJSON() string {
	return `{
  "schemaVersion": "g004.contract.bundle.v1",
  "laneEvents": [
    {
      "event": "lane.started",
      "status": "running",
      "emittedAt": "2026-05-14T00:00:00Z",
      "metadata": {
        "seq": 1,
        "provenance": "worker-1",
        "emitterIdentity": "codog-worker",
        "environmentLabel": "test"
      }
    },
    {
      "event": "lane.finished",
      "status": "ok",
      "emittedAt": "2026-05-14T00:01:00Z",
      "metadata": {
        "seq": 2,
        "provenance": "worker-1",
        "emitterIdentity": "codog-worker",
        "environmentLabel": "test",
        "eventFingerprint": "lane.finished:worker-1:2"
      }
    }
  ],
  "reports": [
    {
      "schemaVersion": "g004.report.v1",
      "reportId": "report-1",
      "identity": { "contentHash": "hash-1" },
      "projection": { "provenance": "projection-policy" },
      "redaction": { "provenance": "redaction-policy" },
      "consumerCapabilities": ["claims", "fieldDeltas"],
      "findings": [
        { "kind": "fact", "confidence": "high", "statement": "lane finished once" }
      ],
      "fieldDeltas": [
        { "field": "status", "previousHash": "old", "currentHash": "new", "attribution": "worker-1" }
      ]
    }
  ],
  "approvalTokens": [
    {
      "tokenId": "token-1",
      "owner": "operator",
      "scope": "workspace-write",
      "issuedAt": "2026-05-14T00:00:00Z",
      "oneTimeUse": true,
      "replayPreventionNonce": "nonce-1",
      "delegationChain": [
        { "from": "operator", "to": "worker", "action": "grant", "at": "2026-05-14T00:00:01Z" }
      ]
    }
  ]
}`
}
