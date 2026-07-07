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
	SchemaV1                    = "claw.report.v1"
	DefaultProjectionPolicyV1   = "claw.report.projection.v1"
	ReportingReportSchemaV1     = "codog.reporting.report.v1"
	ReportingSnapshotSchemaV1   = "codog.reporting.snapshot.v1"
	ReportingCompatibilityV1    = "codog.reporting.compatibility.v1"
	ReportingProjectionPolicyV1 = "codog.reporting.projection.v1"
	MockParityReportSchemaV1    = "codog.mock_parity.v1"
	MockParityManifestSchemaV1  = "codog.mock_parity_manifest.v1"

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

// CompatibilityGuidance describes how consumers should handle report schema evolution.
type CompatibilityGuidance struct {
	Policy                string   `json:"policy"`
	CurrentVersion        string   `json:"current_version"`
	MinCompatibleVersion  string   `json:"min_compatible_version"`
	AdditiveChanges       []string `json:"additive_changes"`
	BreakingChanges       []string `json:"breaking_changes"`
	MinimalStableCore     []string `json:"minimal_stable_core"`
	OlderConsumerGuidance string   `json:"older_consumer_guidance"`
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
	ID                string   `json:"id"`
	Description       string   `json:"description"`
	Required          bool     `json:"required"`
	FieldFamily       string   `json:"field_family"`
	EnumValues        []string `json:"enum_values,omitempty"`
	Deprecated        bool     `json:"deprecated"`
	DeprecationReason string   `json:"deprecation_reason,omitempty"`
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

type RegistryFilter struct {
	ReportIDs      []string
	SchemaVersions []string
	FieldFamilies  []string
}

func RegistryV1() Registry {
	return Registry{
		SchemaVersion: SchemaV1,
		Compatibility: "additive fields are compatible; missing required fields are breaking",
		Fields: []RegistryField{
			field("identity.report_id", "stable canonical report identity", true, "identity"),
			field("identity.content_hash", "hash of canonical payload excluding identity", true, "identity"),
			field("identity.canonical_fingerprint", "fingerprint binding report id, content hash, and snapshot id", false, "identity"),
			field("field_sensitivity", "field path to sensitivity label map used by audience projections", false, "projection"),
			enumField("claims[].kind", "fact/inference/hypothesis/recommendation label", true, "claims", []string{ClaimObservedFact, ClaimInference, ClaimHypothesis, ClaimRecommendation}),
			enumField("claims[].confidence", "confidence bucket for the claim", true, "claims", []string{ConfidenceHigh, ConfidenceMedium, ConfidenceLow, ConfidenceUnknown}),
			field("claims[].evidence", "evidence ids supporting a claim", false, "claims"),
			enumField("claims[].sensitivity", "claim sensitivity label used by projection policies", false, "claims", []string{SensitivityPublic, SensitivityInternal, SensitivityOperatorOnly, SensitivitySecret}),
			field("negative_evidence[]", "searched-and-not-found findings with checked scope", false, "negative_evidence"),
			enumField("negative_evidence[].status", "negative finding status after checking a scope", false, "negative_evidence", []string{NegativeNotObservedInCheckedScope, NegativeUnknownNotChecked}),
			enumField("negative_evidence[].sensitivity", "negative evidence sensitivity label used by projection policies", false, "negative_evidence", []string{SensitivityPublic, SensitivityInternal, SensitivityOperatorOnly, SensitivitySecret}),
			field("field_deltas[]", "field-level changed/unchanged/cleared/carried-forward attribution", false, "field_deltas"),
			enumField("field_deltas[].state", "field-level delta state", false, "field_deltas", []string{FieldChanged, FieldUnchanged, FieldCleared, FieldCarriedForward}),
			enumField("new_items[].sensitivity", "new item sensitivity label used by projection policies", false, "items", []string{SensitivityPublic, SensitivityInternal, SensitivityOperatorOnly, SensitivitySecret}),
			enumField("changed_items[].sensitivity", "changed item sensitivity label used by projection policies", false, "items", []string{SensitivityPublic, SensitivityInternal, SensitivityOperatorOnly, SensitivitySecret}),
			field("atomic_update.report_id", "logical update identity shared by all message parts", false, "atomic_update"),
			field("atomic_update.active_sessions[]", "session ids covered by the logical update", false, "atomic_update"),
			field("atomic_update.exact_pinpoint", "single exact roadmap pinpoint for the logical update", false, "atomic_update"),
			field("atomic_update.concrete_delta", "single concrete delta for the logical update", false, "atomic_update"),
			field("atomic_update.blocker", "current blocker for the logical update", false, "atomic_update"),
			field("atomic_update.message_parts[]", "ordered chat transport fragments for reconstructing one logical update", false, "atomic_update"),
			field("projection.provenance.redactions[]", "redaction policy provenance for projected fields", false, "projection"),
			field("projection.provenance.redactions[].reason", "why a projected field was transformed or omitted", false, "projection"),
			field("projection.provenance.redactions[].original_hash", "stable hash of the original canonical value before redaction", false, "projection"),
			field("projection.provenance.identity_inputs", "canonical source, policy, capability, view, and verbosity inputs used to derive projection identity", false, "projection"),
			field("projection.provenance.identity_input_hash", "stable hash of the projection identity inputs", false, "projection"),
			field("projection.provenance.payload_hash", "stable hash of the rendered projection payload", false, "projection"),
			field("projection.provenance.canonical_equivalence_hash", "stable hash for comparing canonically equivalent projections across transports", false, "projection"),
			field("projection.provenance.omitted_field_families[]", "field families intentionally omitted by consumer projection", false, "projection"),
			field("projection.provenance.cache_key", "stable key for the consumer/view/verbosity compatible projection cache", false, "projection"),
			field("projection.provenance.latest_compatible", "whether the emitted projection is the latest compatible view for its cache key", false, "projection"),
			field("projection.provenance.stale_cached", "whether the emitted projection is a stale cached view", false, "projection"),
			field("projection.provenance.supersedes_projection_id", "previous projection id superseded by this projection when the source changes", false, "projection"),
			field("schema_version", "structured payload schema version", true, "identity"),
			field("projection_id", "stable projection id derived from source, view, verbosity, and payload", false, "projection"),
			enumField("view", "audience-specific projection view", false, "projection", []string{"default", "full", "delta_brief", "ops_audit", "human_readable", "roadmap_sync"}),
			enumField("verbosity", "projection detail level", false, "projection", []string{"brief", "normal", "verbose"}),
			field("canonical_report", "full-fidelity source report embedded with a projection", false, "projection"),
			field("schema_compatibility.policy", "schema evolution policy id for structured report payloads", false, "compatibility"),
			field("schema_compatibility.minimal_stable_core[]", "fields preserved for degraded older parsers", false, "compatibility"),
			field("kind", "structured report kind discriminator", true, "identity"),
			field("channel", "logical report channel", true, "identity"),
			field("report_id", "stable report id for a generated dogfood report", true, "identity"),
			field("snapshot_id", "stored full snapshot id for audit and resume", true, "identity"),
			enumField("outcome", "delta report outcome", true, "summary", []string{"new", "changed", "no_change"}),
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
			report("report_backpressure", ReportingReportSchemaV1, "Delta-first dogfood report with schema compatibility guidance and field-level attribution.", "codog report_backpressure", "codog tool report_backpressure", []string{
				"schema_version",
				"schema_compatibility",
				"identity",
				"kind",
				"channel",
				"report_id",
				"snapshot_id",
				"generated_at",
				"outcome",
				"checked",
				"no_change",
				"field_sensitivity",
				"claims",
				"negative_evidence",
				"field_deltas",
				"new_items",
				"changed_items",
				"last_meaningful_report_id",
			}),
			report("report_backpressure_projection", ReportingReportSchemaV1, "Audience-specific projection envelope for report_backpressure.", "codog report_backpressure", "codog tool report_backpressure", []string{
				"schema_version",
				"projection_id",
				"view",
				"verbosity",
				"projection.provenance.omitted_field_families[]",
				"projection.provenance.redactions[]",
				"projection.provenance.identity_inputs",
				"projection.provenance.identity_input_hash",
				"projection.provenance.payload_hash",
				"projection.provenance.canonical_equivalence_hash",
				"canonical_report",
			}),
			report("report_backpressure_snapshot", ReportingSnapshotSchemaV1, "Stored full dogfood report snapshot for audit and resume.", "codog report_backpressure", "codog tool report_backpressure snapshot", []string{
				"schema_version",
				"snapshot_id",
				"channel",
				"generated_at",
				"items",
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

func FilterRegistry(registry Registry, filter RegistryFilter) Registry {
	filter.ReportIDs = canonicalStringSet(filter.ReportIDs)
	filter.SchemaVersions = canonicalStringSet(filter.SchemaVersions)
	filter.FieldFamilies = canonicalStringSet(filter.FieldFamilies)
	selectedReports := make([]RegistryReport, 0, len(registry.Reports))
	fieldIDs := map[string]bool{}
	for _, report := range registry.Reports {
		if !matchesRegistryFilter(report.ID, filter.ReportIDs) || !matchesRegistryFilter(report.SchemaVersion, filter.SchemaVersions) {
			continue
		}
		selectedReports = append(selectedReports, report)
		for _, fieldID := range report.Fields {
			fieldIDs[fieldID] = true
		}
	}
	if len(filter.ReportIDs) == 0 && len(filter.SchemaVersions) == 0 {
		for _, field := range registry.Fields {
			fieldIDs[field.ID] = true
		}
	}
	fields := make([]RegistryField, 0, len(registry.Fields))
	for _, field := range registry.Fields {
		if !fieldIDs[field.ID] && !matchesRegistryFieldPrefix(field.ID, fieldIDs) {
			continue
		}
		if !matchesRegistryFilter(field.FieldFamily, filter.FieldFamilies) {
			continue
		}
		fields = append(fields, field)
	}
	registry.Fields = fields
	registry.Reports = selectedReports
	return registry
}

func matchesRegistryFilter(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesRegistryFieldPrefix(fieldID string, reportFields map[string]bool) bool {
	for reportField := range reportFields {
		if strings.HasPrefix(fieldID, reportField+".") || strings.HasPrefix(fieldID, reportField+"[]") {
			return true
		}
	}
	return false
}

func field(id string, description string, required bool, family string) RegistryField {
	return RegistryField{ID: id, Description: description, Required: required, FieldFamily: family}
}

func enumField(id string, description string, required bool, family string, values []string) RegistryField {
	field := field(id, description, required, family)
	field.EnumValues = append([]string(nil), values...)
	return field
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
