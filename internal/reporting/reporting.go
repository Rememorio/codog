// Package reporting builds low-noise delta reports from persisted state.
package reporting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/roadmap"
)

const (
	SnapshotSchemaVersion = reportschema.ReportingSnapshotSchemaV1
	DefaultFreshnessTTL   = time.Hour
)

type Cursor struct {
	Channel                  string            `json:"channel"`
	LastReportID             string            `json:"last_report_id,omitempty"`
	LastSnapshotID           string            `json:"last_snapshot_id,omitempty"`
	LastReportedAt           time.Time         `json:"last_reported_at,omitempty"`
	LastMeaningfulReportID   string            `json:"last_meaningful_report_id,omitempty"`
	LastMeaningfulSnapshotID string            `json:"last_meaningful_snapshot_id,omitempty"`
	LastMeaningfulItemIDs    []string          `json:"last_meaningful_item_ids,omitempty"`
	LastNegativeEvidenceIDs  []string          `json:"last_negative_evidence_ids,omitempty"`
	FieldHashes              map[string]string `json:"field_hashes,omitempty"`
	ItemHashes               map[string]string `json:"item_hashes,omitempty"`
}

type ItemSummary struct {
	ID                  string                       `json:"id"`
	Title               string                       `json:"title"`
	State               roadmap.State                `json:"state"`
	Priority            roadmap.Priority             `json:"priority"`
	Severity            roadmap.Severity             `json:"severity"`
	Impact              roadmap.ImpactClass          `json:"impact"`
	Readiness           roadmap.HandoffReadiness     `json:"readiness,omitempty"`
	UpdatedAt           time.Time                    `json:"updated_at"`
	ObservedAt          time.Time                    `json:"observed_at"`
	AgeSeconds          int64                        `json:"age_seconds"`
	FreshnessTTLSeconds int64                        `json:"freshness_ttl_seconds"`
	Freshness           string                       `json:"freshness"`
	ObservationSource   string                       `json:"observation_source"`
	Fingerprint         string                       `json:"fingerprint"`
	Sensitivity         string                       `json:"sensitivity"`
	EvidenceRefs        []string                     `json:"evidence_refs,omitempty"`
	Claims              []ClaimSummary               `json:"claims,omitempty"`
	Handoff             *roadmap.HandoffPacket       `json:"handoff,omitempty"`
	Implementation      []roadmap.ImplementationLink `json:"implementation,omitempty"`
}

type ClaimSummary struct {
	ID           string   `json:"id"`
	ItemID       string   `json:"item_id"`
	Kind         string   `json:"kind"`
	Text         string   `json:"text"`
	Confidence   string   `json:"confidence"`
	Evidence     []string `json:"evidence,omitempty"`
	PromotedFrom string   `json:"promoted_from,omitempty"`
	Sensitivity  string   `json:"sensitivity"`
}

type Report struct {
	SchemaVersion               string                             `json:"schema_version"`
	SchemaCompatibility         reportschema.CompatibilityGuidance `json:"schema_compatibility"`
	Identity                    ReportIdentity                     `json:"identity"`
	Kind                        string                             `json:"kind"`
	Channel                     string                             `json:"channel"`
	ReportID                    string                             `json:"report_id"`
	TriggerID                   string                             `json:"trigger_id,omitempty"`
	SnapshotID                  string                             `json:"snapshot_id"`
	GeneratedAt                 time.Time                          `json:"generated_at"`
	Outcome                     string                             `json:"outcome"`
	Checked                     bool                               `json:"checked"`
	CheckedSurfaces             []string                           `json:"checked_surfaces,omitempty"`
	NoChange                    bool                               `json:"no_change"`
	MixedFreshness              bool                               `json:"mixed_freshness"`
	FreshnessCounts             map[string]int                     `json:"freshness_counts,omitempty"`
	FieldSensitivity            map[string]string                  `json:"field_sensitivity,omitempty"`
	Claims                      []ClaimSummary                     `json:"claims,omitempty"`
	NegativeEvidence            []reportschema.NegativeEvidence    `json:"negative_evidence,omitempty"`
	InvalidatesNegativeEvidence []string                           `json:"invalidates_negative_evidence,omitempty"`
	FieldDeltas                 []reportschema.FieldDelta          `json:"field_deltas,omitempty"`
	NewItems                    []ItemSummary                      `json:"new_items,omitempty"`
	ChangedItems                []ItemSummary                      `json:"changed_items,omitempty"`
	UnchangedCount              int                                `json:"unchanged_count"`
	TotalCount                  int                                `json:"total_count"`
	Collapsed                   bool                               `json:"collapsed"`
	FullSnapshotStored          bool                               `json:"full_snapshot_stored"`
	PreviousReportID            string                             `json:"previous_report_id,omitempty"`
	LastMeaningfulReportID      string                             `json:"last_meaningful_report_id,omitempty"`
	LastMeaningfulSnapshotID    string                             `json:"last_meaningful_snapshot_id,omitempty"`
	LastMeaningfulItemIDs       []string                           `json:"last_meaningful_item_ids,omitempty"`
}

type ReportIdentity struct {
	ReportID             string `json:"report_id"`
	ContentHash          string `json:"content_hash"`
	CanonicalFingerprint string `json:"canonical_fingerprint"`
}

type ReportProjection struct {
	SchemaVersion   string                     `json:"schema_version"`
	ProjectionID    string                     `json:"projection_id"`
	View            string                     `json:"view"`
	Verbosity       string                     `json:"verbosity"`
	Provenance      ReportProjectionProvenance `json:"provenance"`
	Payload         map[string]any             `json:"payload"`
	CanonicalReport Report                     `json:"canonical_report"`
}

type ReportProjectionProvenance struct {
	PolicyID                string                             `json:"policy_id"`
	CacheKey                string                             `json:"cache_key,omitempty"`
	SourceSchemaVersion     string                             `json:"source_schema_version"`
	SourceReportID          string                             `json:"source_report_id"`
	SourceSnapshotID        string                             `json:"source_snapshot_id"`
	SourceContentHash       string                             `json:"source_content_hash"`
	Consumer                reportschema.ConsumerCapabilities  `json:"consumer"`
	View                    string                             `json:"view"`
	Verbosity               string                             `json:"verbosity"`
	SourceChanged           bool                               `json:"source_changed"`
	RenderingChanged        bool                               `json:"rendering_changed"`
	DuplicateOfProjectionID string                             `json:"duplicate_of_projection_id,omitempty"`
	SupersedesProjectionID  string                             `json:"supersedes_projection_id,omitempty"`
	LatestCompatible        bool                               `json:"latest_compatible"`
	StaleCached             bool                               `json:"stale_cached"`
	Downgraded              bool                               `json:"downgraded"`
	OmittedFieldFamilies    []string                           `json:"omitted_field_families,omitempty"`
	Redactions              []reportschema.RedactionProvenance `json:"redactions,omitempty"`
}

type ReportProjectionCacheEntry struct {
	SchemaVersion     string    `json:"schema_version"`
	CacheKey          string    `json:"cache_key"`
	ProjectionID      string    `json:"projection_id"`
	SourceReportID    string    `json:"source_report_id"`
	SourceContentHash string    `json:"source_content_hash"`
	View              string    `json:"view"`
	Verbosity         string    `json:"verbosity"`
	Consumer          string    `json:"consumer"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Snapshot struct {
	SchemaVersion string        `json:"schema_version"`
	SnapshotID    string        `json:"snapshot_id"`
	Channel       string        `json:"channel"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Items         []ItemSummary `json:"items"`
}

type Store struct {
	Dir     string
	Roadmap roadmap.Store
}

type GenerateOptions struct {
	TriggerID       string
	CheckedSurfaces []string
	CheckedWindow   string
	NegativeQueries []NegativeQuery
	FreshnessTTL    time.Duration
}

// NegativeQuery describes one absence check that should be attached to a no-change report.
type NegativeQuery struct {
	ID              string
	Query           string
	CheckedSurfaces []string
	Window          string
}

func NewStore(configHome string) Store {
	return Store{
		Dir:     filepath.Join(configHome, "reporting"),
		Roadmap: roadmap.NewStore(configHome),
	}
}

func (s Store) Generate(channel string, now time.Time) (Report, error) {
	return s.GenerateWithOptions(channel, now, GenerateOptions{})
}

func (s Store) ProjectReportCached(report Report, capabilities reportschema.ConsumerCapabilities, view string, verbosity string) (ReportProjection, error) {
	projection, err := ProjectReport(report, capabilities, view, verbosity)
	if err != nil {
		return ReportProjection{}, err
	}
	cacheKey, err := projectionCacheKey(report, capabilities, projection.View, projection.Verbosity)
	if err != nil {
		return ReportProjection{}, err
	}
	projection.Provenance.CacheKey = cacheKey
	projection.Provenance.LatestCompatible = true
	projection.Provenance.StaleCached = false

	previous, err := s.getProjectionCache(cacheKey)
	if err != nil && !os.IsNotExist(err) {
		return ReportProjection{}, err
	}
	if err == nil {
		switch {
		case previous.ProjectionID == projection.ProjectionID:
			projection.Provenance.DuplicateOfProjectionID = previous.ProjectionID
		case previous.SourceContentHash != "" && previous.SourceContentHash != projection.Provenance.SourceContentHash:
			projection.Provenance.SourceChanged = true
			projection.Provenance.RenderingChanged = true
			projection.Provenance.SupersedesProjectionID = previous.ProjectionID
		default:
			projection.Provenance.RenderingChanged = true
			projection.Provenance.SupersedesProjectionID = previous.ProjectionID
		}
	}
	entry := ReportProjectionCacheEntry{
		SchemaVersion:     reportschema.ReportingReportSchemaV1,
		CacheKey:          cacheKey,
		ProjectionID:      projection.ProjectionID,
		SourceReportID:    projection.Provenance.SourceReportID,
		SourceContentHash: projection.Provenance.SourceContentHash,
		View:              projection.View,
		Verbosity:         projection.Verbosity,
		Consumer:          projection.Provenance.Consumer.Consumer,
		UpdatedAt:         report.GeneratedAt,
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}
	if err := s.saveProjectionCache(entry); err != nil {
		return ReportProjection{}, err
	}
	return projection, nil
}

func ProjectReport(report Report, capabilities reportschema.ConsumerCapabilities, view string, verbosity string) (ReportProjection, error) {
	capabilities.Consumer = strings.TrimSpace(capabilities.Consumer)
	if capabilities.Consumer == "" {
		capabilities.Consumer = "unknown"
	}
	capabilities.SchemaVersions = cleanStrings(capabilities.SchemaVersions)
	capabilities.FieldFamilies = cleanStrings(capabilities.FieldFamilies)
	capabilities.MaxSensitivity = strings.TrimSpace(capabilities.MaxSensitivity)
	if capabilities.MaxSensitivity == "" {
		capabilities.MaxSensitivity = reportschema.SensitivityInternal
	}
	view = strings.TrimSpace(view)
	if view == "" {
		view = "default"
	}
	verbosity = normalizeVerbosity(verbosity)
	identity := report.Identity
	if identity.ContentHash == "" {
		var err error
		identity, err = reportIdentity(report)
		if err != nil {
			return ReportProjection{}, err
		}
		report.Identity = identity
	}
	fullPayload, err := reportMap(report)
	if err != nil {
		return ReportProjection{}, err
	}
	supportsSchema := supportsReportSchema(capabilities, report.SchemaVersion)
	restrictFamilies := len(capabilities.FieldFamilies) > 0 || !supportsSchema
	payload := fullPayload
	omitted := []string{}
	if isAudienceReportView(view) {
		payload, omitted = audienceReportPayload(report, view, verbosity)
	} else if restrictFamilies {
		payload, omitted = projectedReportPayload(report, capabilities)
	}
	redactions, err := redactProjectionPayload(payload, capabilities)
	if err != nil {
		return ReportProjection{}, err
	}
	sourceHash := identity.ContentHash
	renderingChanged := isAudienceReportView(view) || restrictFamilies
	projection := ReportProjection{
		SchemaVersion: reportschema.ReportingReportSchemaV1,
		View:          view,
		Verbosity:     verbosity,
		Provenance: ReportProjectionProvenance{
			PolicyID:             reportschema.ReportingProjectionPolicyV1,
			SourceSchemaVersion:  report.SchemaVersion,
			SourceReportID:       report.ReportID,
			SourceSnapshotID:     report.SnapshotID,
			SourceContentHash:    sourceHash,
			Consumer:             capabilities,
			View:                 view,
			Verbosity:            verbosity,
			SourceChanged:        false,
			RenderingChanged:     renderingChanged,
			LatestCompatible:     true,
			StaleCached:          false,
			Downgraded:           !supportsSchema || len(omitted) > 0 || isAudienceReportView(view),
			OmittedFieldFamilies: omitted,
			Redactions:           redactions,
		},
		Payload:         payload,
		CanonicalReport: report,
	}
	projectionID, err := stableHash(map[string]any{
		"view":       projection.View,
		"verbosity":  projection.Verbosity,
		"provenance": projection.Provenance,
		"payload":    projection.Payload,
	})
	if err != nil {
		return ReportProjection{}, err
	}
	projection.ProjectionID = projectionID
	return projection, nil
}

func (s Store) GenerateWithOptions(channel string, now time.Time, options GenerateOptions) (Report, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return Report{}, errors.New("reporting channel is required")
	}
	options.TriggerID = strings.TrimSpace(options.TriggerID)
	options.CheckedSurfaces = cleanStrings(options.CheckedSurfaces)
	options.CheckedWindow = strings.TrimSpace(options.CheckedWindow)
	options.NegativeQueries = cleanNegativeQueries(options.NegativeQueries)
	if options.FreshnessTTL <= 0 {
		options.FreshnessTTL = DefaultFreshnessTTL
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	items, err := s.Roadmap.List()
	if err != nil {
		return Report{}, err
	}
	summaries := make([]ItemSummary, 0, len(items))
	claims := []ClaimSummary{}
	hashes := make(map[string]string, len(items))
	for _, item := range items {
		summary, err := summarizeItem(item, now, options.FreshnessTTL)
		if err != nil {
			return Report{}, err
		}
		summaries = append(summaries, summary)
		claims = append(claims, summary.Claims...)
		hashes[item.ID] = summary.Fingerprint
	}
	cursor, err := s.GetCursor(channel)
	if err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	if cursor.ItemHashes == nil {
		cursor.ItemHashes = map[string]string{}
	}
	if cursor.FieldHashes == nil {
		cursor.FieldHashes = map[string]string{}
	}
	newItems := []ItemSummary{}
	changedItems := []ItemSummary{}
	unchangedCount := 0
	freshnessCounts := map[string]int{}
	for _, summary := range summaries {
		freshnessCounts[summary.Freshness]++
		previous, ok := cursor.ItemHashes[summary.ID]
		switch {
		case !ok:
			newItems = append(newItems, summary)
		case previous != summary.Fingerprint:
			changedItems = append(changedItems, summary)
		default:
			unchangedCount++
		}
	}
	snapshot := Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Channel:       channel,
		GeneratedAt:   now,
		Items:         summaries,
	}
	snapshotID, err := stableHash(snapshot)
	if err != nil {
		return Report{}, err
	}
	snapshot.SnapshotID = "snapshot-" + snapshotID
	reportID, err := stableHash(map[string]any{
		"channel":      channel,
		"trigger_id":   options.TriggerID,
		"snapshot_id":  snapshot.SnapshotID,
		"generated_at": now.Format(time.RFC3339Nano),
	})
	if err != nil {
		return Report{}, err
	}
	meaningfulItemIDs := meaningfulIDs(newItems, changedItems)
	noChange := len(newItems) == 0 && len(changedItems) == 0
	outcome := "changed"
	if noChange {
		outcome = "no_change"
	} else if len(newItems) > 0 && len(changedItems) == 0 {
		outcome = "new"
	}
	lastMeaningfulReportID := cursor.LastMeaningfulReportID
	lastMeaningfulSnapshotID := cursor.LastMeaningfulSnapshotID
	lastMeaningfulItemIDs := append([]string(nil), cursor.LastMeaningfulItemIDs...)
	if !noChange {
		lastMeaningfulReportID = "report-" + reportID
		lastMeaningfulSnapshotID = snapshot.SnapshotID
		lastMeaningfulItemIDs = meaningfulItemIDs
	}
	negativeEvidence := []reportschema.NegativeEvidence{}
	if noChange {
		negativeEvidence, err = buildNegativeEvidence(channel, now, cursor, options)
		if err != nil {
			return Report{}, err
		}
	}
	invalidatesNegativeEvidence := []string{}
	if !noChange && len(cursor.LastNegativeEvidenceIDs) > 0 {
		invalidatesNegativeEvidence = append(invalidatesNegativeEvidence, cursor.LastNegativeEvidenceIDs...)
		sort.Strings(invalidatesNegativeEvidence)
	}
	fieldDeltas, fieldHashes, err := buildFieldDeltas(summaries, meaningfulItemIDs, cursor, options, negativeEvidence)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion:               reportschema.ReportingReportSchemaV1,
		SchemaCompatibility:         reportSchemaCompatibility(),
		Kind:                        "report_backpressure",
		Channel:                     channel,
		ReportID:                    "report-" + reportID,
		TriggerID:                   options.TriggerID,
		SnapshotID:                  snapshot.SnapshotID,
		GeneratedAt:                 now,
		Outcome:                     outcome,
		Checked:                     true,
		CheckedSurfaces:             options.CheckedSurfaces,
		NoChange:                    noChange,
		MixedFreshness:              len(freshnessCounts) > 1,
		FreshnessCounts:             freshnessCounts,
		FieldSensitivity:            reportFieldSensitivity(),
		Claims:                      claims,
		NegativeEvidence:            negativeEvidence,
		InvalidatesNegativeEvidence: invalidatesNegativeEvidence,
		FieldDeltas:                 fieldDeltas,
		NewItems:                    newItems,
		ChangedItems:                changedItems,
		UnchangedCount:              unchangedCount,
		TotalCount:                  len(summaries),
		Collapsed:                   unchangedCount > 0,
		FullSnapshotStored:          true,
		PreviousReportID:            cursor.LastReportID,
		LastMeaningfulReportID:      lastMeaningfulReportID,
		LastMeaningfulSnapshotID:    lastMeaningfulSnapshotID,
		LastMeaningfulItemIDs:       lastMeaningfulItemIDs,
	}
	identity, err := reportIdentity(report)
	if err != nil {
		return Report{}, err
	}
	report.Identity = identity
	if err := s.saveSnapshot(snapshot); err != nil {
		return Report{}, err
	}
	lastNegativeEvidenceIDs := []string{}
	if noChange {
		lastNegativeEvidenceIDs = negativeEvidenceIDs(negativeEvidence)
	}
	cursor = Cursor{
		Channel:                  channel,
		LastReportID:             report.ReportID,
		LastSnapshotID:           snapshot.SnapshotID,
		LastReportedAt:           now,
		LastMeaningfulReportID:   lastMeaningfulReportID,
		LastMeaningfulSnapshotID: lastMeaningfulSnapshotID,
		LastMeaningfulItemIDs:    lastMeaningfulItemIDs,
		LastNegativeEvidenceIDs:  lastNegativeEvidenceIDs,
		FieldHashes:              fieldHashes,
		ItemHashes:               hashes,
	}
	if err := s.saveCursor(cursor); err != nil {
		return Report{}, err
	}
	return report, nil
}

func reportSchemaCompatibility() reportschema.CompatibilityGuidance {
	return reportschema.CompatibilityGuidance{
		Policy:               reportschema.ReportingCompatibilityV1,
		CurrentVersion:       reportschema.ReportingReportSchemaV1,
		MinCompatibleVersion: reportschema.ReportingReportSchemaV1,
		AdditiveChanges: []string{
			"new optional top-level fields",
			"new optional item summary fields",
			"new field_deltas entries for additional fields",
			"new negative_evidence query ids",
		},
		BreakingChanges: []string{
			"removing or renaming minimal_stable_core fields",
			"changing enum meanings for outcome or field_deltas.state",
			"changing report_id or snapshot_id identity semantics",
		},
		MinimalStableCore: []string{
			"schema_version",
			"identity",
			"kind",
			"channel",
			"report_id",
			"snapshot_id",
			"generated_at",
			"outcome",
			"checked",
			"no_change",
			"total_count",
			"unchanged_count",
		},
		OlderConsumerGuidance: "Consumers that do not support this version should parse only minimal_stable_core, ignore unknown fields, and fetch the snapshot for full audit context.",
	}
}

func reportFieldSensitivity() map[string]string {
	return map[string]string{
		"identity":             reportschema.SensitivityPublic,
		"schema_version":       reportschema.SensitivityPublic,
		"kind":                 reportschema.SensitivityPublic,
		"channel":              reportschema.SensitivityInternal,
		"report_id":            reportschema.SensitivityPublic,
		"snapshot_id":          reportschema.SensitivityInternal,
		"outcome":              reportschema.SensitivityPublic,
		"checked":              reportschema.SensitivityPublic,
		"no_change":            reportschema.SensitivityPublic,
		"claims":               reportschema.SensitivityInternal,
		"negative_evidence":    reportschema.SensitivityInternal,
		"field_deltas":         reportschema.SensitivityInternal,
		"new_items":            reportschema.SensitivityInternal,
		"changed_items":        reportschema.SensitivityInternal,
		"freshness_counts":     reportschema.SensitivityInternal,
		"schema_compatibility": reportschema.SensitivityPublic,
	}
}

func reportIdentity(report Report) (ReportIdentity, error) {
	contentHash, err := reportContentHash(report)
	if err != nil {
		return ReportIdentity{}, err
	}
	fingerprint, err := stableHash(map[string]any{
		"report_id":    report.ReportID,
		"content_hash": contentHash,
		"snapshot_id":  report.SnapshotID,
	})
	if err != nil {
		return ReportIdentity{}, err
	}
	return ReportIdentity{
		ReportID:             report.ReportID,
		ContentHash:          contentHash,
		CanonicalFingerprint: fingerprint,
	}, nil
}

func reportContentHash(report Report) (string, error) {
	hashable := report
	hashable.Identity = ReportIdentity{}
	return stableHash(hashable)
}

func projectedReportPayload(report Report, capabilities reportschema.ConsumerCapabilities) (map[string]any, []string) {
	payload := map[string]any{
		"schema_version":  report.SchemaVersion,
		"identity":        report.Identity,
		"kind":            report.Kind,
		"channel":         report.Channel,
		"report_id":       report.ReportID,
		"snapshot_id":     report.SnapshotID,
		"generated_at":    report.GeneratedAt,
		"outcome":         report.Outcome,
		"checked":         report.Checked,
		"no_change":       report.NoChange,
		"total_count":     report.TotalCount,
		"unchanged_count": report.UnchangedCount,
	}
	families := []string{"compatibility", "claims", "negative_evidence", "field_deltas", "items", "freshness"}
	omitted := []string{}
	for _, family := range families {
		if supportsReportFamily(capabilities, family) {
			addReportFamily(payload, report, family)
			continue
		}
		omitted = append(omitted, family)
	}
	return payload, omitted
}

func audienceReportPayload(report Report, view string, verbosity string) (map[string]any, []string) {
	payload := reportCorePayload(report)
	payload["projection_view"] = view
	payload["projection_verbosity"] = verbosity
	switch view {
	case "delta_brief":
		payload["summary"] = reportSummary(report)
		payload["top_items"] = projectedItems(report, verbosity)
		payload["field_deltas"] = report.FieldDeltas
		return payload, []string{"compatibility", "negative_evidence", "freshness"}
	case "ops_audit":
		payload["schema_compatibility"] = report.SchemaCompatibility
		payload["checked_surfaces"] = report.CheckedSurfaces
		payload["negative_evidence"] = report.NegativeEvidence
		payload["invalidates_negative_evidence"] = report.InvalidatesNegativeEvidence
		payload["field_deltas"] = report.FieldDeltas
		payload["freshness_counts"] = report.FreshnessCounts
		payload["items"] = projectedItems(report, verbosity)
		return payload, []string{}
	case "human_readable":
		payload["summary_text"] = humanReportSummary(report)
		payload["highlights"] = reportHighlights(report, verbosity)
		payload["next_actions"] = reportNextActions(report)
		return payload, []string{"field_deltas", "negative_evidence", "freshness"}
	case "roadmap_sync":
		payload["roadmap_items"] = roadmapSyncItems(report, verbosity)
		payload["field_deltas"] = report.FieldDeltas
		payload["last_meaningful_report_id"] = report.LastMeaningfulReportID
		payload["last_meaningful_snapshot_id"] = report.LastMeaningfulSnapshotID
		payload["last_meaningful_item_ids"] = report.LastMeaningfulItemIDs
		return payload, []string{"claims", "negative_evidence", "freshness"}
	default:
		return payload, []string{"compatibility", "claims", "negative_evidence", "field_deltas", "items", "freshness"}
	}
}

func reportCorePayload(report Report) map[string]any {
	return map[string]any{
		"schema_version":  report.SchemaVersion,
		"identity":        report.Identity,
		"kind":            report.Kind,
		"channel":         report.Channel,
		"report_id":       report.ReportID,
		"snapshot_id":     report.SnapshotID,
		"generated_at":    report.GeneratedAt,
		"outcome":         report.Outcome,
		"checked":         report.Checked,
		"no_change":       report.NoChange,
		"total_count":     report.TotalCount,
		"unchanged_count": report.UnchangedCount,
	}
}

func reportSummary(report Report) map[string]any {
	return map[string]any{
		"outcome":       report.Outcome,
		"new_count":     len(report.NewItems),
		"changed_count": len(report.ChangedItems),
		"total_count":   report.TotalCount,
		"no_change":     report.NoChange,
		"collapsed":     report.Collapsed,
	}
}

func projectedItems(report Report, verbosity string) []map[string]any {
	items := append([]ItemSummary(nil), report.NewItems...)
	items = append(items, report.ChangedItems...)
	limit := len(items)
	switch verbosity {
	case "brief":
		if limit > 3 {
			limit = 3
		}
	case "normal":
		if limit > 10 {
			limit = 10
		}
	}
	projected := make([]map[string]any, 0, limit)
	for _, item := range items[:limit] {
		value := map[string]any{
			"id":       item.ID,
			"title":    item.Title,
			"state":    item.State,
			"priority": item.Priority,
			"severity": item.Severity,
			"impact":   item.Impact,
		}
		if verbosity != "brief" {
			value["freshness"] = item.Freshness
			value["evidence_refs"] = item.EvidenceRefs
		}
		if verbosity == "verbose" {
			value["claims"] = item.Claims
			value["handoff"] = item.Handoff
			value["implementation"] = item.Implementation
		}
		projected = append(projected, value)
	}
	return projected
}

func humanReportSummary(report Report) string {
	switch report.Outcome {
	case "no_change":
		return "No new report delta was observed."
	case "new":
		return "New roadmap report items were observed."
	default:
		return "Roadmap report items changed."
	}
}

func reportHighlights(report Report, verbosity string) []string {
	items := append([]ItemSummary(nil), report.NewItems...)
	items = append(items, report.ChangedItems...)
	limit := len(items)
	if verbosity == "brief" && limit > 3 {
		limit = 3
	}
	highlights := make([]string, 0, limit)
	for _, item := range items[:limit] {
		highlights = append(highlights, string(item.Priority)+" "+item.Title)
	}
	if len(highlights) == 0 && report.NoChange {
		highlights = append(highlights, "No changed roadmap items")
	}
	return highlights
}

func reportNextActions(report Report) []string {
	actions := []string{}
	for _, item := range append(append([]ItemSummary(nil), report.NewItems...), report.ChangedItems...) {
		if item.Handoff != nil {
			actions = append(actions, item.Handoff.SuggestedVerification...)
		}
	}
	return cleanStrings(actions)
}

func roadmapSyncItems(report Report, verbosity string) []map[string]any {
	items := append([]ItemSummary(nil), report.NewItems...)
	items = append(items, report.ChangedItems...)
	projected := make([]map[string]any, 0, len(items))
	for _, item := range items {
		value := map[string]any{
			"id":          item.ID,
			"title":       item.Title,
			"state":       item.State,
			"priority":    item.Priority,
			"severity":    item.Severity,
			"impact":      item.Impact,
			"updated_at":  item.UpdatedAt,
			"readiness":   item.Readiness,
			"fingerprint": item.Fingerprint,
		}
		if verbosity != "brief" {
			value["evidence_refs"] = item.EvidenceRefs
			value["handoff"] = item.Handoff
		}
		projected = append(projected, value)
	}
	return projected
}

func isAudienceReportView(view string) bool {
	switch view {
	case "delta_brief", "ops_audit", "human_readable", "roadmap_sync":
		return true
	default:
		return false
	}
}

func normalizeVerbosity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "brief", "normal", "verbose":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "normal"
	}
}

func addReportFamily(payload map[string]any, report Report, family string) {
	switch family {
	case "compatibility":
		payload["schema_compatibility"] = report.SchemaCompatibility
	case "claims":
		payload["claims"] = report.Claims
	case "negative_evidence":
		payload["negative_evidence"] = report.NegativeEvidence
		payload["invalidates_negative_evidence"] = report.InvalidatesNegativeEvidence
	case "field_deltas":
		payload["field_deltas"] = report.FieldDeltas
	case "items":
		payload["new_items"] = report.NewItems
		payload["changed_items"] = report.ChangedItems
		payload["last_meaningful_report_id"] = report.LastMeaningfulReportID
		payload["last_meaningful_snapshot_id"] = report.LastMeaningfulSnapshotID
		payload["last_meaningful_item_ids"] = report.LastMeaningfulItemIDs
	case "freshness":
		payload["mixed_freshness"] = report.MixedFreshness
		payload["freshness_counts"] = report.FreshnessCounts
	}
}

func redactProjectionPayload(payload map[string]any, capabilities reportschema.ConsumerCapabilities) ([]reportschema.RedactionProvenance, error) {
	maxRank, err := reportingSensitivityRank(capabilities.MaxSensitivity)
	if err != nil {
		return nil, err
	}
	redactions := []reportschema.RedactionProvenance{}
	if raw, ok := payload["claims"]; ok {
		claims, changed, err := redactProjectedClaims(raw, maxRank, &redactions)
		if err != nil {
			return nil, err
		}
		if changed {
			payload["claims"] = claims
		}
	}
	if raw, ok := payload["negative_evidence"]; ok && maxRank < mustSensitivityRank(reportschema.SensitivityInternal) {
		hash, err := stableHash(raw)
		if err != nil {
			return nil, err
		}
		delete(payload, "negative_evidence")
		redactions = append(redactions, reportschema.RedactionProvenance{
			FieldPath:    "negative_evidence",
			Reason:       "omitted: sensitivity exceeds consumer policy",
			PolicyID:     reportschema.ReportingProjectionPolicyV1,
			OriginalHash: hash,
		})
	}
	if raw, ok := payload["top_items"]; ok {
		if err := redactProjectedItems(raw, "top_items", maxRank, &redactions); err != nil {
			return nil, err
		}
	}
	if raw, ok := payload["roadmap_items"]; ok {
		if err := redactProjectedItems(raw, "roadmap_items", maxRank, &redactions); err != nil {
			return nil, err
		}
	}
	if raw, ok := payload["items"]; ok {
		if err := redactProjectedItems(raw, "items", maxRank, &redactions); err != nil {
			return nil, err
		}
	}
	if raw, ok := payload["new_items"]; ok {
		if err := redactProjectedItems(raw, "new_items", maxRank, &redactions); err != nil {
			return nil, err
		}
	}
	if raw, ok := payload["changed_items"]; ok {
		if err := redactProjectedItems(raw, "changed_items", maxRank, &redactions); err != nil {
			return nil, err
		}
	}
	sort.Slice(redactions, func(i, j int) bool { return redactions[i].FieldPath < redactions[j].FieldPath })
	return redactions, nil
}

func redactProjectedClaims(raw any, maxRank int, redactions *[]reportschema.RedactionProvenance) (any, bool, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return raw, false, err
	}
	var claims []ClaimSummary
	if err := json.Unmarshal(data, &claims); err != nil {
		return raw, false, nil
	}
	changed := false
	out := make([]ClaimSummary, 0, len(claims))
	for i, claim := range claims {
		rank, err := reportingSensitivityRank(claim.Sensitivity)
		if err != nil {
			return raw, false, err
		}
		if rank <= maxRank {
			out = append(out, claim)
			continue
		}
		hash, err := stableHash(claim)
		if err != nil {
			return raw, false, err
		}
		changed = true
		if claim.Sensitivity == reportschema.SensitivitySecret {
			*redactions = append(*redactions, reportschema.RedactionProvenance{
				FieldPath:    fmt.Sprintf("claims[%d]", i),
				Reason:       "omitted: sensitivity exceeds consumer policy",
				PolicyID:     reportschema.ReportingProjectionPolicyV1,
				OriginalHash: hash,
			})
			continue
		}
		claim.Text = "<redacted>"
		claim.Evidence = nil
		out = append(out, claim)
		*redactions = append(*redactions, reportschema.RedactionProvenance{
			FieldPath:    fmt.Sprintf("claims[%d].text", i),
			Reason:       "transformed: sensitivity exceeds consumer policy",
			PolicyID:     reportschema.ReportingProjectionPolicyV1,
			OriginalHash: hash,
		})
	}
	return out, changed, nil
}

func redactProjectedItems(raw any, path string, maxRank int, redactions *[]reportschema.RedactionProvenance) error {
	if maxRank >= mustSensitivityRank(reportschema.SensitivityInternal) {
		return nil
	}
	items := []map[string]any{}
	switch values := raw.(type) {
	case []map[string]any:
		items = values
	case []any:
		for _, value := range values {
			item, ok := value.(map[string]any)
			if ok {
				items = append(items, item)
			}
		}
	default:
		return nil
	}
	for i, item := range items {
		for _, field := range []string{"evidence_refs", "handoff", "claims", "implementation"} {
			value, ok := item[field]
			if !ok {
				continue
			}
			hash, err := stableHash(value)
			if err != nil {
				return err
			}
			delete(item, field)
			*redactions = append(*redactions, reportschema.RedactionProvenance{
				FieldPath:    fmt.Sprintf("%s[%d].%s", path, i, field),
				Reason:       "omitted: sensitivity exceeds consumer policy",
				PolicyID:     reportschema.ReportingProjectionPolicyV1,
				OriginalHash: hash,
			})
		}
	}
	return nil
}

func reportingSensitivityRank(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return 2, nil
	case reportschema.SensitivityPublic:
		return 1, nil
	case reportschema.SensitivityInternal:
		return 2, nil
	case reportschema.SensitivityOperatorOnly:
		return 3, nil
	case reportschema.SensitivitySecret:
		return 4, nil
	default:
		return 0, fmt.Errorf("unknown sensitivity %q", value)
	}
}

func mustSensitivityRank(value string) int {
	rank, err := reportingSensitivityRank(value)
	if err != nil {
		panic(err)
	}
	return rank
}

func reportMap(report Report) (map[string]any, error) {
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func supportsReportSchema(capabilities reportschema.ConsumerCapabilities, schema string) bool {
	if len(capabilities.SchemaVersions) == 0 {
		return true
	}
	for _, value := range capabilities.SchemaVersions {
		if value == schema {
			return true
		}
	}
	return false
}

func supportsReportFamily(capabilities reportschema.ConsumerCapabilities, family string) bool {
	if len(capabilities.FieldFamilies) == 0 {
		return false
	}
	for _, value := range capabilities.FieldFamilies {
		if value == family {
			return true
		}
	}
	return false
}

func projectionCacheKey(report Report, capabilities reportschema.ConsumerCapabilities, view string, verbosity string) (string, error) {
	capabilities.Consumer = strings.TrimSpace(capabilities.Consumer)
	capabilities.SchemaVersions = cleanStrings(capabilities.SchemaVersions)
	capabilities.FieldFamilies = cleanStrings(capabilities.FieldFamilies)
	capabilities.MaxSensitivity = strings.TrimSpace(capabilities.MaxSensitivity)
	if capabilities.MaxSensitivity == "" {
		capabilities.MaxSensitivity = reportschema.SensitivityInternal
	}
	hash, err := stableHash(map[string]any{
		"channel":         report.Channel,
		"consumer":        capabilities.Consumer,
		"schema_versions": capabilities.SchemaVersions,
		"field_families":  capabilities.FieldFamilies,
		"max_sensitivity": capabilities.MaxSensitivity,
		"view":            view,
		"verbosity":       verbosity,
	})
	if err != nil {
		return "", err
	}
	return "projection-" + hash, nil
}

func buildNegativeEvidence(channel string, now time.Time, cursor Cursor, options GenerateOptions) ([]reportschema.NegativeEvidence, error) {
	queries := options.NegativeQueries
	if len(queries) == 0 {
		queries = []NegativeQuery{
			{ID: "no_new_delta", Query: "no_new_delta"},
			{ID: "no_new_blocker", Query: "no_new_blocker"},
		}
	}
	out := make([]reportschema.NegativeEvidence, 0, len(queries))
	defaultWindow := checkedWindow(cursor.LastReportedAt, now)
	if options.CheckedWindow != "" {
		defaultWindow = options.CheckedWindow
	}
	for _, query := range queries {
		surfaces := cleanStrings(query.CheckedSurfaces)
		if len(surfaces) == 0 {
			surfaces = append([]string(nil), options.CheckedSurfaces...)
		}
		window := strings.TrimSpace(query.Window)
		if window == "" {
			window = defaultWindow
		}
		queryText := strings.TrimSpace(query.Query)
		if queryText == "" {
			queryText = strings.TrimSpace(query.ID)
		}
		status := reportschema.NegativeUnknownNotChecked
		if len(surfaces) > 0 {
			status = reportschema.NegativeNotObservedInCheckedScope
		}
		id, err := stableHash(map[string]any{
			"channel":          channel,
			"query":            queryText,
			"status":           status,
			"checked_surfaces": surfaces,
			"window":           window,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, reportschema.NegativeEvidence{
			ID:              "neg-" + id,
			Status:          status,
			CheckedSurfaces: surfaces,
			Query:           queryText,
			Window:          window,
			Sensitivity:     reportschema.SensitivityInternal,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func buildFieldDeltas(summaries []ItemSummary, meaningfulItemIDs []string, cursor Cursor, options GenerateOptions, negativeEvidence []reportschema.NegativeEvidence) ([]reportschema.FieldDelta, map[string]string, error) {
	current := map[string]string{}
	attribution := map[string]string{}
	carriedForward := map[string]bool{}
	set := func(field string, value any, source string, carried bool) error {
		hash, err := stableHash(value)
		if err != nil {
			return err
		}
		current[field] = hash
		attribution[field] = source
		carriedForward[field] = carried
		return nil
	}
	if len(meaningfulItemIDs) > 0 {
		if err := set("report.delta", meaningfulItemIDs, "report_backpressure", false); err != nil {
			return nil, nil, err
		}
		if err := set("report.pinpoint", meaningfulItemIDs, "report_backpressure", false); err != nil {
			return nil, nil, err
		}
	}
	if hasString(options.CheckedSurfaces, "sessions") {
		if err := set("report.active_sessions", []string{}, "checked_surfaces:sessions", false); err != nil {
			return nil, nil, err
		}
	}
	if hasNegativeQuery(negativeEvidence, "no_new_blocker") {
		if err := set("report.blocker", "", "negative_evidence:no_new_blocker", false); err != nil {
			return nil, nil, err
		}
	}
	for _, summary := range summaries {
		source := "roadmap_pinpoint:" + summary.ID
		carry := summary.ObservationSource == "carried_forward"
		if err := set("pinpoint."+summary.ID+".lifecycle_state", summary.State, source, carry); err != nil {
			return nil, nil, err
		}
		if err := set("pinpoint."+summary.ID+".priority", summary.Priority, source, carry); err != nil {
			return nil, nil, err
		}
		if err := set("pinpoint."+summary.ID+".freshness", summary.Freshness, source, carry); err != nil {
			return nil, nil, err
		}
	}

	fields := map[string]bool{}
	for field := range cursor.FieldHashes {
		fields[field] = true
	}
	for field := range current {
		fields[field] = true
	}
	fieldNames := make([]string, 0, len(fields))
	for field := range fields {
		fieldNames = append(fieldNames, field)
	}
	sort.Strings(fieldNames)

	deltas := make([]reportschema.FieldDelta, 0, len(fieldNames))
	for _, field := range fieldNames {
		previousHash, hadPrevious := cursor.FieldHashes[field]
		currentHash, hasCurrent := current[field]
		previous := stringPtrIf(hadPrevious, previousHash)
		currentPtr := stringPtrIf(hasCurrent, currentHash)
		deltas = append(deltas, reportschema.FieldDelta{
			Field:        field,
			State:        fieldDeltaState(hadPrevious, previousHash, hasCurrent, currentHash, carriedForward[field]),
			PreviousHash: previous,
			CurrentHash:  currentPtr,
			Attribution:  fieldAttribution(field, attribution),
		})
	}
	return deltas, current, nil
}

func fieldDeltaState(hadPrevious bool, previousHash string, hasCurrent bool, currentHash string, carriedForward bool) string {
	switch {
	case !hadPrevious && !hasCurrent:
		return reportschema.FieldUnchanged
	case hadPrevious && !hasCurrent:
		return reportschema.FieldCleared
	case !hadPrevious:
		return reportschema.FieldChanged
	case previousHash != currentHash:
		return reportschema.FieldChanged
	case carriedForward:
		return reportschema.FieldCarriedForward
	default:
		return reportschema.FieldUnchanged
	}
}

func fieldAttribution(field string, attribution map[string]string) string {
	if value := attribution[field]; value != "" {
		return value
	}
	return "previous_report"
}

func stringPtrIf(ok bool, value string) *string {
	if !ok {
		return nil
	}
	result := value
	return &result
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func hasNegativeQuery(values []reportschema.NegativeEvidence, query string) bool {
	for _, value := range values {
		if value.Query == query {
			return true
		}
	}
	return false
}

func checkedWindow(previous time.Time, now time.Time) string {
	end := now.UTC().Format(time.RFC3339)
	if previous.IsZero() {
		return end + "/" + end
	}
	return previous.UTC().Format(time.RFC3339) + "/" + end
}

func cleanNegativeQueries(values []NegativeQuery) []NegativeQuery {
	out := []NegativeQuery{}
	seen := map[string]bool{}
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Query = strings.TrimSpace(value.Query)
		value.CheckedSurfaces = cleanStrings(value.CheckedSurfaces)
		value.Window = strings.TrimSpace(value.Window)
		key := value.ID + "\x00" + value.Query + "\x00" + strings.Join(value.CheckedSurfaces, "\x00") + "\x00" + value.Window
		if (value.ID == "" && value.Query == "") || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func negativeEvidenceIDs(values []reportschema.NegativeEvidence) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if value.ID != "" {
			ids = append(ids, value.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func meaningfulIDs(newItems []ItemSummary, changedItems []ItemSummary) []string {
	ids := make([]string, 0, len(newItems)+len(changedItems))
	for _, item := range newItems {
		ids = append(ids, item.ID)
	}
	for _, item := range changedItems {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func (s Store) GetCursor(channel string) (Cursor, error) {
	path, err := s.cursorPath(channel)
	if err != nil {
		return Cursor{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Cursor{}, err
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return Cursor{}, err
	}
	return cursor, nil
}

func (s Store) GetSnapshot(snapshotID string) (Snapshot, error) {
	path, err := s.snapshotPath(snapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s Store) getProjectionCache(cacheKey string) (ReportProjectionCacheEntry, error) {
	path, err := s.projectionCachePath(cacheKey)
	if err != nil {
		return ReportProjectionCacheEntry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReportProjectionCacheEntry{}, err
	}
	var entry ReportProjectionCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return ReportProjectionCacheEntry{}, err
	}
	return entry, nil
}

func summarizeItem(item roadmap.Item, now time.Time, freshnessTTL time.Duration) (ItemSummary, error) {
	observedAt := item.UpdatedAt
	if observedAt.IsZero() {
		observedAt = item.CreatedAt
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	observedAt = observedAt.UTC()
	age := now.Sub(observedAt)
	if age < 0 {
		age = 0
	}
	freshness := "current"
	if age > freshnessTTL {
		freshness = "stale"
	}
	observationSource := "fresh"
	if age > 0 {
		observationSource = "carried_forward"
	}
	summary := ItemSummary{
		ID:                  item.ID,
		Title:               item.Title,
		State:               item.State,
		Priority:            item.Priority,
		Severity:            item.Severity,
		Impact:              item.Impact,
		UpdatedAt:           item.UpdatedAt,
		ObservedAt:          observedAt,
		AgeSeconds:          int64(age.Seconds()),
		FreshnessTTLSeconds: int64(freshnessTTL.Seconds()),
		Freshness:           freshness,
		ObservationSource:   observationSource,
		EvidenceRefs:        evidenceRefs(item.Evidence),
		Sensitivity:         reportschema.SensitivityInternal,
		Claims:              claimsForItem(item),
		Handoff:             item.Handoff,
		Implementation:      append([]roadmap.ImplementationLink(nil), item.Implementation...),
	}
	if item.Handoff != nil {
		summary.Readiness = item.Handoff.Readiness
	}
	hash, err := stableHash(map[string]any{
		"id":             summary.ID,
		"title":          summary.Title,
		"state":          summary.State,
		"priority":       summary.Priority,
		"severity":       summary.Severity,
		"impact":         summary.Impact,
		"evidence_refs":  summary.EvidenceRefs,
		"claims":         summary.Claims,
		"handoff":        summary.Handoff,
		"implementation": summary.Implementation,
	})
	if err != nil {
		return ItemSummary{}, err
	}
	summary.Fingerprint = hash
	return summary, nil
}

func claimsForItem(item roadmap.Item) []ClaimSummary {
	refs := evidenceRefs(item.Evidence)
	claims := []ClaimSummary{{
		ID:          "claim-" + item.ID + "-status",
		ItemID:      item.ID,
		Kind:        reportschema.ClaimObservedFact,
		Text:        "Pinpoint " + item.ID + " is " + string(item.State),
		Confidence:  reportschema.ConfidenceHigh,
		Evidence:    refs,
		Sensitivity: reportschema.SensitivityPublic,
	}}
	if claim, ok := rootCauseClaim(item); ok {
		claims = append(claims, claim)
	}
	if item.Handoff != nil && len(item.Handoff.SuggestedVerification) > 0 {
		claims = append(claims, ClaimSummary{
			ID:          "claim-" + item.ID + "-verification-recommendation",
			ItemID:      item.ID,
			Kind:        reportschema.ClaimRecommendation,
			Text:        strings.Join(item.Handoff.SuggestedVerification, "; "),
			Confidence:  reportschema.ConfidenceMedium,
			Evidence:    refs,
			Sensitivity: reportschema.SensitivityInternal,
		})
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
	return claims
}

func rootCauseClaim(item roadmap.Item) (ClaimSummary, bool) {
	var hint roadmap.EvidenceAttachment
	foundHint := false
	confirmingRefs := []string{}
	for _, evidence := range item.Evidence {
		switch evidence.Role {
		case roadmap.EvidenceRootCauseHint:
			if !foundHint {
				hint = evidence
				foundHint = true
			}
		case roadmap.EvidenceRepro, roadmap.EvidenceVerification:
			if evidence.ID != "" {
				confirmingRefs = append(confirmingRefs, evidence.ID)
			}
		}
	}
	if !foundHint {
		return ClaimSummary{}, false
	}
	evidence := []string{}
	if hint.ID != "" {
		evidence = append(evidence, hint.ID)
	}
	evidence = append(evidence, confirmingRefs...)
	sort.Strings(evidence)
	text := strings.TrimSpace(hint.Preview)
	if text == "" {
		text = "Root cause hint: " + hint.Reference
	}
	claim := ClaimSummary{
		ID:          "claim-" + item.ID + "-root-cause",
		ItemID:      item.ID,
		Kind:        reportschema.ClaimHypothesis,
		Text:        text,
		Confidence:  reportschema.ConfidenceMedium,
		Evidence:    cleanStrings(evidence),
		Sensitivity: reportschema.SensitivityInternal,
	}
	if len(confirmingRefs) > 0 {
		claim.Kind = reportschema.ClaimObservedFact
		claim.Confidence = reportschema.ConfidenceHigh
		claim.PromotedFrom = reportschema.ClaimHypothesis
	}
	return claim, true
}

func evidenceRefs(evidence []roadmap.EvidenceAttachment) []string {
	refs := make([]string, 0, len(evidence))
	for _, value := range evidence {
		if value.ID != "" {
			refs = append(refs, value.ID)
		}
	}
	sort.Strings(refs)
	return refs
}

func (s Store) saveCursor(cursor Cursor) error {
	if err := os.MkdirAll(filepath.Join(s.Dir, "cursors"), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cursor, "", "  ")
	if err != nil {
		return err
	}
	path, err := s.cursorPath(cursor.Channel)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) saveSnapshot(snapshot Snapshot) error {
	if err := os.MkdirAll(filepath.Join(s.Dir, "snapshots"), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path, err := s.snapshotPath(snapshot.SnapshotID)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) saveProjectionCache(entry ReportProjectionCacheEntry) error {
	if err := os.MkdirAll(filepath.Join(s.Dir, "projection-cache"), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	path, err := s.projectionCachePath(entry.CacheKey)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) cursorPath(channel string) (string, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return "", errors.New("reporting channel is required")
	}
	return filepath.Join(s.Dir, "cursors", safeID(channel)+".json"), nil
}

func (s Store) snapshotPath(snapshotID string) (string, error) {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return "", errors.New("snapshot_id is required")
	}
	if strings.ContainsAny(snapshotID, `/\`) || snapshotID == "." || snapshotID == ".." {
		return "", errors.New("snapshot_id must be a path component")
	}
	return filepath.Join(s.Dir, "snapshots", snapshotID+".json"), nil
}

func (s Store) projectionCachePath(cacheKey string) (string, error) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return "", errors.New("projection cache key is required")
	}
	if strings.ContainsAny(cacheKey, `/\`) || cacheKey == "." || cacheKey == ".." {
		return "", errors.New("projection cache key must be a path component")
	}
	return filepath.Join(s.Dir, "projection-cache", cacheKey+".json"), nil
}

func safeID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func cleanStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func stableHash(value any) (string, error) {
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
