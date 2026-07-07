package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/roadmap"
	"github.com/stretchr/testify/require"
)

type projectionGoldenFixtureSet struct {
	SchemaVersion string                   `json:"schema_version"`
	FixtureSet    string                   `json:"fixture_set"`
	Validates     []string                 `json:"validates"`
	Sources       []projectionGoldenSource `json:"sources"`
	Cases         []projectionGoldenCase   `json:"cases"`
}

type projectionGoldenSource struct {
	Name    string                       `json:"name"`
	Channel string                       `json:"channel"`
	Filings []projectionGoldenFiling     `json:"filings"`
	Reports []projectionGoldenReportStep `json:"reports"`
}

type projectionGoldenFiling struct {
	ID                   string   `json:"id,omitempty"`
	Title                string   `json:"title"`
	Priority             string   `json:"priority,omitempty"`
	Severity             string   `json:"severity,omitempty"`
	Impact               string   `json:"impact,omitempty"`
	RootCausePreview     string   `json:"root_cause_preview,omitempty"`
	VerificationCommands []string `json:"verification_commands,omitempty"`
	Now                  string   `json:"now"`
}

type projectionGoldenReportStep struct {
	Name            string   `json:"name"`
	Now             string   `json:"now"`
	TriggerID       string   `json:"trigger_id,omitempty"`
	CheckedSurfaces []string `json:"checked_surfaces,omitempty"`
	CheckedWindow   string   `json:"checked_window,omitempty"`
}

type projectionGoldenCase struct {
	Name                             string                            `json:"name"`
	Source                           string                            `json:"source"`
	Report                           string                            `json:"report"`
	Consumer                         reportschema.ConsumerCapabilities `json:"consumer"`
	View                             string                            `json:"view"`
	Verbosity                        string                            `json:"verbosity"`
	ExpectedProjectionID             string                            `json:"expected_projection_id"`
	ExpectedIdentityInputHash        string                            `json:"expected_identity_input_hash"`
	ExpectedPayloadHash              string                            `json:"expected_payload_hash"`
	ExpectedCanonicalEquivalenceHash string                            `json:"expected_canonical_equivalence_hash"`
	ExpectedRedactionPaths           []string                          `json:"expected_redaction_paths,omitempty"`
	ExpectedOmittedFieldFamilies     []string                          `json:"expected_omitted_field_families,omitempty"`
}

func TestProjectionGoldenFixtures(t *testing.T) {
	fixture := loadProjectionGoldenFixtureSet(t)

	require.Equal(t, "codog.reporting.projection_fixtures.v1", fixture.SchemaVersion)
	require.Equal(t, "reporting-projection-golden-v1", fixture.FixtureSet)
	require.Contains(t, fixture.Validates, "deterministic_projection_identity")
	require.Contains(t, fixture.Validates, "redaction_regression_lock")
	require.Contains(t, fixture.Validates, "capability_downgrade_regression_lock")
	require.NotEmpty(t, fixture.Cases)

	reports := buildProjectionGoldenReports(t, fixture)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			sourceReports, ok := reports[tc.Source]
			require.Truef(t, ok, "missing source %q", tc.Source)
			report, ok := sourceReports[tc.Report]
			require.Truef(t, ok, "missing report %q for source %q", tc.Report, tc.Source)

			projection, err := ProjectReport(report, tc.Consumer, tc.View, tc.Verbosity)
			require.NoError(t, err)
			canonicalHash, err := ProjectionCanonicalHash(projection)
			require.NoError(t, err)

			if tc.ExpectedProjectionID == "" ||
				tc.ExpectedIdentityInputHash == "" ||
				tc.ExpectedPayloadHash == "" ||
				tc.ExpectedCanonicalEquivalenceHash == "" {
				t.Fatalf("golden fixture %q missing expected hashes: projection_id=%q identity_input_hash=%q payload_hash=%q canonical_equivalence_hash=%q",
					tc.Name,
					projection.ProjectionID,
					projection.Provenance.IdentityInputHash,
					projection.Provenance.PayloadHash,
					projection.Provenance.CanonicalEquivalenceHash)
			}
			require.Equal(t, tc.ExpectedProjectionID, projection.ProjectionID)
			require.Equal(t, tc.ExpectedIdentityInputHash, projection.Provenance.IdentityInputHash)
			require.Equal(t, tc.ExpectedPayloadHash, projection.Provenance.PayloadHash)
			require.Equal(t, tc.ExpectedCanonicalEquivalenceHash, projection.Provenance.CanonicalEquivalenceHash)
			require.Equal(t, tc.ExpectedCanonicalEquivalenceHash, canonicalHash)
			require.Equal(t, normalizeProjectionGoldenConsumer(tc.Consumer), projection.Provenance.IdentityInputs.Consumer)
			require.Equal(t, tc.View, projection.Provenance.IdentityInputs.View)
			require.Equal(t, normalizeVerbosity(tc.Verbosity), projection.Provenance.IdentityInputs.Verbosity)
			require.Equal(t, reportschema.ReportingProjectionPolicyV1, projection.Provenance.IdentityInputs.ProjectionPolicyID)
			require.Equal(t, report.Identity.ContentHash, projection.Provenance.IdentityInputs.SourceContentHash)
			require.ElementsMatch(t, tc.ExpectedRedactionPaths, redactionPaths(projection.Provenance.Redactions))
			require.ElementsMatch(t, tc.ExpectedOmittedFieldFamilies, projection.Provenance.OmittedFieldFamilies)
		})
	}
}

func loadProjectionGoldenFixtureSet(t *testing.T) projectionGoldenFixtureSet {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "projection_golden_fixtures.json"))
	require.NoError(t, err)
	var fixture projectionGoldenFixtureSet
	require.NoError(t, json.Unmarshal(data, &fixture))
	return fixture
}

func buildProjectionGoldenReports(t *testing.T, fixture projectionGoldenFixtureSet) map[string]map[string]Report {
	t.Helper()
	out := map[string]map[string]Report{}
	for _, source := range fixture.Sources {
		configHome := t.TempDir()
		roadmapStore := roadmap.NewStore(configHome)
		reportStore := NewStore(configHome)
		for _, filing := range source.Filings {
			_, err := roadmapStore.File(roadmap.Filing{
				ID:       filing.ID,
				Title:    filing.Title,
				Priority: roadmap.Priority(filing.Priority),
				Severity: roadmap.Severity(filing.Severity),
				Impact:   roadmap.ImpactClass(filing.Impact),
				Evidence: projectionGoldenEvidence(filing),
				Handoff:  projectionGoldenHandoff(filing),
				Now:      mustParseProjectionGoldenTime(t, filing.Now),
			})
			require.NoError(t, err)
		}
		reportsByName := map[string]Report{}
		for _, step := range source.Reports {
			report, err := reportStore.GenerateWithOptions(source.Channel, mustParseProjectionGoldenTime(t, step.Now), GenerateOptions{
				TriggerID:       step.TriggerID,
				CheckedSurfaces: step.CheckedSurfaces,
				CheckedWindow:   step.CheckedWindow,
			})
			require.NoError(t, err)
			reportsByName[step.Name] = report
		}
		out[source.Name] = reportsByName
	}
	return out
}

func normalizeProjectionGoldenConsumer(capabilities reportschema.ConsumerCapabilities) reportschema.ConsumerCapabilities {
	capabilities.Consumer = strings.TrimSpace(capabilities.Consumer)
	capabilities.SchemaVersions = cleanStrings(capabilities.SchemaVersions)
	capabilities.FieldFamilies = cleanStrings(capabilities.FieldFamilies)
	if capabilities.MaxSensitivity == "" {
		capabilities.MaxSensitivity = reportschema.SensitivityInternal
	}
	return capabilities
}

func projectionGoldenEvidence(filing projectionGoldenFiling) []roadmap.EvidenceAttachment {
	if filing.RootCausePreview == "" {
		return nil
	}
	return []roadmap.EvidenceAttachment{{
		Role:      roadmap.EvidenceRootCauseHint,
		Type:      "log",
		Reference: "log:" + filing.ID,
		Preview:   filing.RootCausePreview,
	}}
}

func projectionGoldenHandoff(filing projectionGoldenFiling) *roadmap.HandoffPacket {
	if len(filing.VerificationCommands) == 0 {
		return nil
	}
	return &roadmap.HandoffPacket{SuggestedVerification: append([]string(nil), filing.VerificationCommands...)}
}

func mustParseProjectionGoldenTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}
