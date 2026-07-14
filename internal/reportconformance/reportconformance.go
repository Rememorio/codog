package reportconformance

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/reportschema"
)

const (
	BundleSchemaVersion = reportschema.ReportingConsumerConformanceSchemaV1
	ResultSchemaVersion = reportschema.ReportingConsumerConformanceResultV1
	FixtureSetVersion   = "reporting-projection-golden-v1"
)

type ConsumerIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type SemanticChecks struct {
	CanonicalIdentityCorrelated bool `json:"canonical_identity_correlated"`
	RedactedFieldsHandled       bool `json:"redacted_fields_handled,omitempty"`
	MissingFieldsDistinguished  bool `json:"missing_fields_distinguished,omitempty"`
	DowngradeHandled            bool `json:"downgrade_handled,omitempty"`
	NoChangeHandled             bool `json:"no_change_handled,omitempty"`
	FreshnessHandled            bool `json:"freshness_handled,omitempty"`
}

type CaseResult struct {
	Name           string         `json:"name"`
	ProjectionID   string         `json:"projection_id"`
	Parsed         bool           `json:"parsed"`
	SemanticChecks SemanticChecks `json:"semantic_checks"`
}

type Bundle struct {
	SchemaVersion string           `json:"schema_version"`
	FixtureSet    string           `json:"fixture_set"`
	Consumer      ConsumerIdentity `json:"consumer"`
	PassedAt      string           `json:"passed_at,omitempty"`
	Cases         []CaseResult     `json:"cases"`
}

type RequiredCase struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	ProjectionID           string   `json:"projection_id"`
	View                   string   `json:"view"`
	MaxSensitivity         string   `json:"max_sensitivity"`
	RequiredSemanticChecks []string `json:"required_semantic_checks"`
}

type Error struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type LastPassed struct {
	Consumer   string `json:"consumer"`
	Version    string `json:"version"`
	FixtureSet string `json:"fixture_set"`
	PassedAt   string `json:"passed_at,omitempty"`
}

type Result struct {
	Kind              string           `json:"kind"`
	SchemaVersion     string           `json:"schema_version"`
	FixtureSet        string           `json:"fixture_set"`
	Consumer          ConsumerIdentity `json:"consumer"`
	Valid             bool             `json:"valid"`
	ParsePassed       bool             `json:"parse_passed"`
	SemanticPassed    bool             `json:"semantic_passed"`
	RequiredCaseCount int              `json:"required_case_count"`
	PassedCaseCount   int              `json:"passed_case_count"`
	ErrorCount        int              `json:"error_count"`
	Errors            []Error          `json:"errors,omitempty"`
	LastPassed        *LastPassed      `json:"last_passed,omitempty"`
	RequiredCases     []RequiredCase   `json:"required_cases,omitempty"`
}

func RequiredCases() []RequiredCase {
	return []RequiredCase{
		{
			Name:           "public_full_redacts_internal_claims_and_item_detail",
			Description:    "public full projection handles transformed claims and omitted internal item detail",
			ProjectionID:   "8726177642a65d63",
			View:           "full",
			MaxSensitivity: "public",
			RequiredSemanticChecks: []string{
				"canonical_identity_correlated",
				"redacted_fields_handled",
				"missing_fields_distinguished",
			},
		},
		{
			Name:           "legacy_claim_delta_projection_downgrades_capabilities",
			Description:    "legacy consumer handles field-family downgrade and omitted unsupported families",
			ProjectionID:   "cb803e478e96aa95",
			View:           "legacy",
			MaxSensitivity: "internal",
			RequiredSemanticChecks: []string{
				"canonical_identity_correlated",
				"downgrade_handled",
				"missing_fields_distinguished",
			},
		},
		{
			Name:           "brief_audience_projection_locks_summary_shape",
			Description:    "audience summary projection preserves canonical identity without requiring full detail",
			ProjectionID:   "cbaf320e33ad2207",
			View:           "delta_brief",
			MaxSensitivity: "internal",
			RequiredSemanticChecks: []string{
				"canonical_identity_correlated",
			},
		},
		{
			Name:           "public_ops_audit_omits_negative_evidence",
			Description:    "public audit consumer distinguishes redacted negative evidence from absent data on a no-change cycle",
			ProjectionID:   "001261a0da099142",
			View:           "ops_audit",
			MaxSensitivity: "public",
			RequiredSemanticChecks: []string{
				"canonical_identity_correlated",
				"redacted_fields_handled",
				"missing_fields_distinguished",
				"no_change_handled",
				"freshness_handled",
			},
		},
	}
}

func ValidateJSON(data []byte) (Result, error) {
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Result{}, err
	}
	return Validate(bundle), nil
}

func Validate(bundle Bundle) Result {
	errors := validateBundleMetadata(bundle)
	cases, caseErrors := indexBundleCases(bundle.Cases)
	errors = append(errors, caseErrors...)
	required := RequiredCases()
	passedCases, requiredErrors := validateRequiredCases(required, cases)
	errors = append(errors, requiredErrors...)
	sortConformanceErrors(errors)
	parsePassed, semanticPassed := conformancePassStatus(errors)
	valid := len(errors) == 0
	result := Result{
		Kind: "report_consumer_conformance", SchemaVersion: ResultSchemaVersion, FixtureSet: FixtureSetVersion,
		Consumer: bundle.Consumer, Valid: valid, ParsePassed: parsePassed, SemanticPassed: semanticPassed,
		RequiredCaseCount: len(required), PassedCaseCount: passedCases, ErrorCount: len(errors),
		Errors: errors, RequiredCases: required,
	}
	if valid {
		result.LastPassed = &LastPassed{
			Consumer: strings.TrimSpace(bundle.Consumer.Name), Version: strings.TrimSpace(bundle.Consumer.Version),
			FixtureSet: FixtureSetVersion, PassedAt: strings.TrimSpace(bundle.PassedAt),
		}
	}
	return result
}

func validateBundleMetadata(bundle Bundle) []Error {
	errors := []Error{}
	if strings.TrimSpace(bundle.SchemaVersion) != BundleSchemaVersion {
		errors = append(errors, Error{Path: "/schema_version", Kind: "parse", Message: fmt.Sprintf("expected %q", BundleSchemaVersion)})
	}
	if strings.TrimSpace(bundle.FixtureSet) != FixtureSetVersion {
		errors = append(errors, Error{Path: "/fixture_set", Kind: "parse", Message: fmt.Sprintf("expected %q", FixtureSetVersion)})
	}
	if strings.TrimSpace(bundle.Consumer.Name) == "" {
		errors = append(errors, Error{Path: "/consumer/name", Kind: "parse", Message: "required string field missing"})
	}
	if strings.TrimSpace(bundle.Consumer.Version) == "" {
		errors = append(errors, Error{Path: "/consumer/version", Kind: "parse", Message: "required string field missing"})
	}
	if strings.TrimSpace(bundle.PassedAt) != "" {
		if _, err := time.Parse(time.RFC3339, bundle.PassedAt); err != nil {
			errors = append(errors, Error{Path: "/passed_at", Kind: "parse", Message: "must be RFC3339 timestamp"})
		}
	}
	return errors
}

func indexBundleCases(entries []CaseResult) (map[string]CaseResult, []Error) {
	cases := map[string]CaseResult{}
	errors := []Error{}
	for index, candidate := range entries {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			errors = append(errors, Error{Path: fmt.Sprintf("/cases/%d/name", index), Kind: "parse", Message: "required string field missing"})
			continue
		}
		if _, exists := cases[name]; exists {
			errors = append(errors, Error{Path: fmt.Sprintf("/cases/%d/name", index), Kind: "parse", Message: "duplicate case name"})
			continue
		}
		cases[name] = candidate
	}
	return cases, errors
}

func validateRequiredCases(required []RequiredCase, cases map[string]CaseResult) (int, []Error) {
	passedCases := 0
	errors := []Error{}
	for _, req := range required {
		candidate, ok := cases[req.Name]
		if !ok {
			errors = append(errors, Error{Path: "/cases/" + req.Name, Kind: "parse", Message: "required conformance case missing"})
			continue
		}
		casePassed := true
		if strings.TrimSpace(candidate.ProjectionID) != req.ProjectionID {
			errors = append(errors, Error{Path: "/cases/" + req.Name + "/projection_id", Kind: "parse", Message: fmt.Sprintf("expected %q", req.ProjectionID)})
			casePassed = false
		}
		if !candidate.Parsed {
			errors = append(errors, Error{Path: "/cases/" + req.Name + "/parsed", Kind: "parse", Message: "consumer did not parse fixture projection"})
			casePassed = false
		}
		for _, check := range req.RequiredSemanticChecks {
			if !semanticCheck(candidate.SemanticChecks, check) {
				errors = append(errors, Error{Path: "/cases/" + req.Name + "/semantic_checks/" + check, Kind: "semantic", Message: "required semantic check not satisfied"})
				casePassed = false
			}
		}
		if casePassed {
			passedCases++
		}
	}
	return passedCases, errors
}

func sortConformanceErrors(errors []Error) {
	sort.Slice(errors, func(i, j int) bool {
		if errors[i].Path == errors[j].Path {
			return errors[i].Kind < errors[j].Kind
		}
		return errors[i].Path < errors[j].Path
	})
}

func conformancePassStatus(errors []Error) (bool, bool) {
	parsePassed := true
	semanticPassed := true
	for _, err := range errors {
		if err.Kind == "parse" {
			parsePassed = false
		}
		if err.Kind == "semantic" {
			semanticPassed = false
		}
	}
	return parsePassed, semanticPassed
}

func semanticCheck(checks SemanticChecks, name string) bool {
	switch name {
	case "canonical_identity_correlated":
		return checks.CanonicalIdentityCorrelated
	case "redacted_fields_handled":
		return checks.RedactedFieldsHandled
	case "missing_fields_distinguished":
		return checks.MissingFieldsDistinguished
	case "downgrade_handled":
		return checks.DowngradeHandled
	case "no_change_handled":
		return checks.NoChangeHandled
	case "freshness_handled":
		return checks.FreshnessHandled
	default:
		return false
	}
}
