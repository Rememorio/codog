package reportschema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	SchemaV1                   = "claw.report.v1"
	DefaultProjectionPolicyV1  = "claw.report.projection.v1"
	MockParityReportSchemaV1   = "codog.mock_parity.v1"
	MockParityManifestSchemaV1 = "codog.mock_parity_manifest.v1"

	ClaimObservedFact   = "observed_fact"
	ClaimInference      = "inference"
	ClaimHypothesis     = "hypothesis"
	ClaimRecommendation = "recommendation"

	ConfidenceHigh    = "high"
	ConfidenceMedium  = "medium"
	ConfidenceLow     = "low"
	ConfidenceUnknown = "unknown"

	SensitivityPublic       = "public"
	SensitivityInternal     = "internal"
	SensitivityOperatorOnly = "operator_only"
	SensitivitySecret       = "secret"

	FieldChanged        = "changed"
	FieldUnchanged      = "unchanged"
	FieldCleared        = "cleared"
	FieldCarriedForward = "carried_forward"

	NegativeNotObservedInCheckedScope = "not_observed_in_checked_scope"
	NegativeUnknownNotChecked         = "unknown_not_checked"
)

type Claim struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Text        string   `json:"text"`
	Confidence  string   `json:"confidence"`
	Evidence    []string `json:"evidence,omitempty"`
	Sensitivity string   `json:"sensitivity"`
}

type NegativeEvidence struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	CheckedSurfaces []string `json:"checked_surfaces,omitempty"`
	Query           string   `json:"query"`
	Window          string   `json:"window"`
	Sensitivity     string   `json:"sensitivity"`
}

type FieldDelta struct {
	Field        string  `json:"field"`
	State        string  `json:"state"`
	PreviousHash *string `json:"previous_hash,omitempty"`
	CurrentHash  *string `json:"current_hash,omitempty"`
	Attribution  string  `json:"attribution"`
}

// MessagePart records one transport fragment for a logical report update.
type MessagePart struct {
	ReportID    string `json:"report_id"`
	PartIndex   int    `json:"part_index"`
	PartCount   int    `json:"part_count"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash,omitempty"`
}

// AtomicUpdate binds dogfood status fields and chat fragments to one report id.
type AtomicUpdate struct {
	ReportID        string        `json:"report_id"`
	ActiveSessions  []string      `json:"active_sessions,omitempty"`
	ExactPinpoint   string        `json:"exact_pinpoint,omitempty"`
	ConcreteDelta   string        `json:"concrete_delta,omitempty"`
	Blocker         string        `json:"blocker,omitempty"`
	MessageParts    []MessagePart `json:"message_parts,omitempty"`
	MessageComplete bool          `json:"message_complete"`
}

type Identity struct {
	ReportID    string `json:"report_id"`
	ContentHash string `json:"content_hash"`
}

type CanonicalReport struct {
	SchemaVersion    string             `json:"schema_version"`
	Identity         Identity           `json:"identity"`
	GeneratedAt      string             `json:"generated_at"`
	Producer         string             `json:"producer"`
	Claims           []Claim            `json:"claims,omitempty"`
	NegativeEvidence []NegativeEvidence `json:"negative_evidence,omitempty"`
	FieldDeltas      []FieldDelta       `json:"field_deltas,omitempty"`
	AtomicUpdate     *AtomicUpdate      `json:"atomic_update,omitempty"`
}

type ConsumerCapabilities struct {
	Consumer       string   `json:"consumer"`
	SchemaVersions []string `json:"schema_versions,omitempty"`
	FieldFamilies  []string `json:"field_families,omitempty"`
	MaxSensitivity string   `json:"max_sensitivity"`
}

type RedactionProvenance struct {
	FieldPath    string `json:"field_path"`
	Reason       string `json:"reason"`
	PolicyID     string `json:"policy_id"`
	OriginalHash string `json:"original_hash"`
}

type ProjectionProvenance struct {
	PolicyID             string                `json:"policy_id"`
	SourceSchemaVersion  string                `json:"source_schema_version"`
	SourceReportID       string                `json:"source_report_id"`
	SourceContentHash    string                `json:"source_content_hash"`
	Consumer             string                `json:"consumer"`
	Downgraded           bool                  `json:"downgraded"`
	OmittedFieldFamilies []string              `json:"omitted_field_families,omitempty"`
	Redactions           []RedactionProvenance `json:"redactions,omitempty"`
}

type Projection struct {
	SchemaVersion string               `json:"schema_version"`
	ProjectionID  string               `json:"projection_id"`
	View          string               `json:"view"`
	Provenance    ProjectionProvenance `json:"provenance"`
	Payload       map[string]any       `json:"payload"`
}

type RegistryField struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	FieldFamily string `json:"field_family"`
}

type RegistryReport struct {
	ID            string   `json:"id"`
	SchemaVersion string   `json:"schema_version"`
	Description   string   `json:"description"`
	Producer      string   `json:"producer"`
	Command       string   `json:"command,omitempty"`
	Fields        []string `json:"fields"`
}

type Registry struct {
	SchemaVersion string           `json:"schema_version"`
	Compatibility string           `json:"compatibility"`
	Fields        []RegistryField  `json:"fields"`
	Reports       []RegistryReport `json:"reports,omitempty"`
}

func RegistryV1() Registry {
	return Registry{
		SchemaVersion: SchemaV1,
		Compatibility: "additive fields are compatible; missing required fields are breaking",
		Fields: []RegistryField{
			field("identity.report_id", "stable canonical report identity", true, "identity"),
			field("identity.content_hash", "hash of canonical payload excluding identity", true, "identity"),
			field("claims[].kind", "fact/inference/hypothesis/recommendation label", true, "claims"),
			field("claims[].confidence", "confidence bucket for the claim", true, "claims"),
			field("claims[].evidence", "evidence ids supporting a claim", false, "claims"),
			field("negative_evidence[]", "searched-and-not-found findings with checked scope", false, "negative_evidence"),
			field("field_deltas[]", "field-level changed/unchanged/cleared/carried-forward attribution", false, "field_deltas"),
			field("atomic_update.report_id", "logical update identity shared by all message parts", false, "atomic_update"),
			field("atomic_update.active_sessions[]", "session ids covered by the logical update", false, "atomic_update"),
			field("atomic_update.exact_pinpoint", "single exact roadmap pinpoint for the logical update", false, "atomic_update"),
			field("atomic_update.concrete_delta", "single concrete delta for the logical update", false, "atomic_update"),
			field("atomic_update.blocker", "current blocker for the logical update", false, "atomic_update"),
			field("atomic_update.message_parts[]", "ordered chat transport fragments for reconstructing one logical update", false, "atomic_update"),
			field("projection.provenance.redactions[]", "redaction policy provenance for projected fields", false, "projection"),
		},
		Reports: []RegistryReport{
			report("canonical_report", SchemaV1, "Canonical structured runtime report accepted by report-schema canonicalize/project.", "codog report-schema", "codog report-schema canonicalize", []string{
				"schema_version",
				"identity",
				"generated_at",
				"producer",
				"claims",
				"negative_evidence",
				"field_deltas",
				"atomic_update",
			}),
			report("mock_parity_report", MockParityReportSchemaV1, "Deterministic mock provider parity harness execution report.", "codog mock-parity", "codog mock-parity --json", []string{
				"schema_version",
				"ok",
				"passed",
				"total",
				"scenario_count",
				"request_count",
				"coverage",
				"scenarios",
				"usage_summary",
				"estimated_cost",
			}),
			report("mock_parity_manifest", MockParityManifestSchemaV1, "Deterministic mock provider parity scenario catalog without executing the harness.", "codog mock-parity", "codog mock-parity manifest --json", []string{
				"schema_version",
				"scenario_count",
				"categories",
				"scenarios",
			}),
		},
	}
}

func Canonicalize(report CanonicalReport) (CanonicalReport, error) {
	report.SchemaVersion = SchemaV1
	sort.Slice(report.Claims, func(i, j int) bool { return report.Claims[i].ID < report.Claims[j].ID })
	sort.Slice(report.NegativeEvidence, func(i, j int) bool { return report.NegativeEvidence[i].ID < report.NegativeEvidence[j].ID })
	sort.Slice(report.FieldDeltas, func(i, j int) bool { return report.FieldDeltas[i].Field < report.FieldDeltas[j].Field })
	reportID := strings.TrimSpace(report.Identity.ReportID)
	if reportID == "" && report.AtomicUpdate != nil {
		reportID = strings.TrimSpace(report.AtomicUpdate.ReportID)
	}
	atomicUpdate, err := canonicalizeAtomicUpdate(report.AtomicUpdate, reportID)
	if err != nil {
		return CanonicalReport{}, err
	}
	report.AtomicUpdate = atomicUpdate
	contentHash, err := ContentHash(report)
	if err != nil {
		return CanonicalReport{}, err
	}
	if strings.TrimSpace(report.Identity.ReportID) == "" {
		if reportID != "" {
			report.Identity.ReportID = reportID
		} else {
			report.Identity.ReportID = "report-" + contentHash
		}
	}
	atomicUpdate, err = canonicalizeAtomicUpdate(report.AtomicUpdate, report.Identity.ReportID)
	if err != nil {
		return CanonicalReport{}, err
	}
	report.AtomicUpdate = atomicUpdate
	report.Identity.ContentHash = contentHash
	return report, nil
}

func ContentHash(report CanonicalReport) (string, error) {
	hashable := report
	hashable.Identity.ReportID = ""
	hashable.Identity.ContentHash = ""
	if hashable.AtomicUpdate != nil {
		atomicUpdate := *hashable.AtomicUpdate
		atomicUpdate.ReportID = ""
		atomicUpdate.MessageParts = append([]MessagePart(nil), atomicUpdate.MessageParts...)
		for i := range atomicUpdate.MessageParts {
			atomicUpdate.MessageParts[i].ReportID = ""
		}
		hashable.AtomicUpdate = &atomicUpdate
	}
	return StableJSONHash(hashable)
}

// ReconstructAtomicMessage joins message parts in canonical order.
func ReconstructAtomicMessage(update AtomicUpdate) (string, bool, error) {
	canonical, err := canonicalizeAtomicUpdate(&update, strings.TrimSpace(update.ReportID))
	if err != nil {
		return "", false, err
	}
	if canonical == nil {
		return "", false, nil
	}
	var builder strings.Builder
	for _, part := range canonical.MessageParts {
		builder.WriteString(part.Content)
	}
	return builder.String(), canonical.MessageComplete, nil
}

func Project(report CanonicalReport, capabilities ConsumerCapabilities, view string) (Projection, error) {
	canonical, err := Canonicalize(report)
	if err != nil {
		return Projection{}, err
	}
	if strings.TrimSpace(capabilities.Consumer) == "" {
		capabilities.Consumer = "unknown"
	}
	if strings.TrimSpace(capabilities.MaxSensitivity) == "" {
		capabilities.MaxSensitivity = SensitivityPublic
	}
	if _, err := sensitivityRank(capabilities.MaxSensitivity); err != nil {
		return Projection{}, err
	}
	if strings.TrimSpace(view) == "" {
		view = "default"
	}

	omitted := []string{}
	redactions := []RedactionProvenance{}
	payload := map[string]any{
		"identity":     canonical.Identity,
		"generated_at": canonical.GeneratedAt,
		"producer":     canonical.Producer,
	}
	if supportsFamily(capabilities, "claims") {
		claims := make([]any, 0, len(canonical.Claims))
		for i, claim := range canonical.Claims {
			projected, ok, err := redactClaim(i, claim, capabilities, &redactions)
			if err != nil {
				return Projection{}, err
			}
			if ok {
				claims = append(claims, projected)
			}
		}
		payload["claims"] = claims
	} else {
		omitted = append(omitted, "claims")
	}
	if supportsFamily(capabilities, "negative_evidence") {
		negativeEvidence := canonical.NegativeEvidence
		if negativeEvidence == nil {
			negativeEvidence = []NegativeEvidence{}
		}
		payload["negative_evidence"] = negativeEvidence
	} else {
		omitted = append(omitted, "negative_evidence")
	}
	if supportsFamily(capabilities, "field_deltas") {
		fieldDeltas := canonical.FieldDeltas
		if fieldDeltas == nil {
			fieldDeltas = []FieldDelta{}
		}
		payload["field_deltas"] = fieldDeltas
	} else {
		omitted = append(omitted, "field_deltas")
	}
	if supportsFamily(capabilities, "atomic_update") {
		if canonical.AtomicUpdate != nil {
			payload["atomic_update"] = canonical.AtomicUpdate
		}
	} else {
		omitted = append(omitted, "atomic_update")
	}

	provenance := ProjectionProvenance{
		PolicyID:             DefaultProjectionPolicyV1,
		SourceSchemaVersion:  canonical.SchemaVersion,
		SourceReportID:       canonical.Identity.ReportID,
		SourceContentHash:    canonical.Identity.ContentHash,
		Consumer:             capabilities.Consumer,
		Downgraded:           !supportsSchema(capabilities, SchemaV1) || len(omitted) > 0 || len(redactions) > 0,
		OmittedFieldFamilies: omitted,
		Redactions:           redactions,
	}
	projection := Projection{
		SchemaVersion: SchemaV1,
		View:          view,
		Provenance:    provenance,
		Payload:       payload,
	}
	projectionID, err := StableJSONHash(map[string]any{
		"view":       projection.View,
		"provenance": projection.Provenance,
		"payload":    projection.Payload,
	})
	if err != nil {
		return Projection{}, err
	}
	projection.ProjectionID = projectionID
	return projection, nil
}

func StableJSONHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return "", err
	}
	data, err = json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}

func field(id string, description string, required bool, family string) RegistryField {
	return RegistryField{ID: id, Description: description, Required: required, FieldFamily: family}
}

func report(id string, schemaVersion string, description string, producer string, command string, fields []string) RegistryReport {
	return RegistryReport{
		ID:            id,
		SchemaVersion: schemaVersion,
		Description:   description,
		Producer:      producer,
		Command:       command,
		Fields:        append([]string(nil), fields...),
	}
}

func canonicalizeAtomicUpdate(update *AtomicUpdate, reportID string) (*AtomicUpdate, error) {
	if update == nil {
		return nil, nil
	}
	canonical := *update
	canonical.ReportID = strings.TrimSpace(canonical.ReportID)
	reportID = strings.TrimSpace(reportID)
	switch {
	case canonical.ReportID == "":
		canonical.ReportID = reportID
	case reportID == "":
		reportID = canonical.ReportID
	case canonical.ReportID != reportID:
		return nil, fmt.Errorf("atomic_update report_id %q does not match report identity %q", canonical.ReportID, reportID)
	}
	canonical.ActiveSessions = canonicalStringSet(canonical.ActiveSessions)
	canonical.MessageParts = append([]MessagePart(nil), canonical.MessageParts...)
	seen := map[int]struct{}{}
	expectedCount := 0
	for i := range canonical.MessageParts {
		part := &canonical.MessageParts[i]
		part.ReportID = strings.TrimSpace(part.ReportID)
		switch {
		case part.ReportID == "":
			part.ReportID = reportID
		case reportID == "":
			reportID = part.ReportID
			canonical.ReportID = reportID
		case part.ReportID != reportID:
			return nil, fmt.Errorf("message part report_id %q does not match report identity %q", part.ReportID, reportID)
		}
		if part.PartCount <= 0 {
			return nil, fmt.Errorf("message part %d has invalid part_count %d", part.PartIndex, part.PartCount)
		}
		if part.PartIndex < 0 || part.PartIndex >= part.PartCount {
			return nil, fmt.Errorf("message part index %d outside part_count %d", part.PartIndex, part.PartCount)
		}
		if expectedCount == 0 {
			expectedCount = part.PartCount
		} else if part.PartCount != expectedCount {
			return nil, fmt.Errorf("message part %d has part_count %d, expected %d", part.PartIndex, part.PartCount, expectedCount)
		}
		if _, ok := seen[part.PartIndex]; ok {
			return nil, fmt.Errorf("duplicate message part index %d", part.PartIndex)
		}
		seen[part.PartIndex] = struct{}{}
		if strings.TrimSpace(part.ContentHash) == "" {
			hash, err := StableJSONHash(part.Content)
			if err != nil {
				return nil, err
			}
			part.ContentHash = hash
		}
	}
	sort.Slice(canonical.MessageParts, func(i, j int) bool {
		return canonical.MessageParts[i].PartIndex < canonical.MessageParts[j].PartIndex
	})
	canonical.MessageComplete = expectedCount > 0 && len(seen) == expectedCount
	return &canonical, nil
}

func canonicalStringSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func supportsFamily(capabilities ConsumerCapabilities, family string) bool {
	if len(capabilities.FieldFamilies) == 0 {
		return true
	}
	for _, value := range capabilities.FieldFamilies {
		if value == family {
			return true
		}
	}
	return false
}

func supportsSchema(capabilities ConsumerCapabilities, schema string) bool {
	for _, value := range capabilities.SchemaVersions {
		if value == schema {
			return true
		}
	}
	return false
}

func redactClaim(index int, claim Claim, capabilities ConsumerCapabilities, redactions *[]RedactionProvenance) (Claim, bool, error) {
	claimRank, err := sensitivityRank(claim.Sensitivity)
	if err != nil {
		return Claim{}, false, err
	}
	maxRank, err := sensitivityRank(capabilities.MaxSensitivity)
	if err != nil {
		return Claim{}, false, err
	}
	if claimRank <= maxRank {
		return claim, true, nil
	}
	originalHash, err := StableJSONHash(claim)
	if err != nil {
		return Claim{}, false, err
	}
	if claim.Sensitivity == SensitivitySecret {
		*redactions = append(*redactions, RedactionProvenance{
			FieldPath:    fmt.Sprintf("claims[%d]", index),
			Reason:       "omitted: sensitivity exceeds consumer policy",
			PolicyID:     DefaultProjectionPolicyV1,
			OriginalHash: originalHash,
		})
		return Claim{}, false, nil
	}
	redacted := claim
	redacted.Text = "<redacted>"
	redacted.Evidence = nil
	*redactions = append(*redactions, RedactionProvenance{
		FieldPath:    fmt.Sprintf("claims[%d].text", index),
		Reason:       "transformed: sensitivity exceeds consumer policy",
		PolicyID:     DefaultProjectionPolicyV1,
		OriginalHash: originalHash,
	})
	return redacted, true, nil
}

func sensitivityRank(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SensitivityPublic:
		return 1, nil
	case SensitivityInternal:
		return 2, nil
	case SensitivityOperatorOnly:
		return 3, nil
	case SensitivitySecret:
		return 4, nil
	default:
		return 0, fmt.Errorf("unknown sensitivity %q", value)
	}
}
