// Package reporting builds low-noise delta reports from persisted state.
package reporting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/roadmap"
)

const (
	SnapshotSchemaVersion = "codog.reporting.snapshot.v1"
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
}

type Report struct {
	Kind                        string                          `json:"kind"`
	Channel                     string                          `json:"channel"`
	ReportID                    string                          `json:"report_id"`
	TriggerID                   string                          `json:"trigger_id,omitempty"`
	SnapshotID                  string                          `json:"snapshot_id"`
	GeneratedAt                 time.Time                       `json:"generated_at"`
	Outcome                     string                          `json:"outcome"`
	Checked                     bool                            `json:"checked"`
	CheckedSurfaces             []string                        `json:"checked_surfaces,omitempty"`
	NoChange                    bool                            `json:"no_change"`
	MixedFreshness              bool                            `json:"mixed_freshness"`
	FreshnessCounts             map[string]int                  `json:"freshness_counts,omitempty"`
	Claims                      []ClaimSummary                  `json:"claims,omitempty"`
	NegativeEvidence            []reportschema.NegativeEvidence `json:"negative_evidence,omitempty"`
	InvalidatesNegativeEvidence []string                        `json:"invalidates_negative_evidence,omitempty"`
	FieldDeltas                 []reportschema.FieldDelta       `json:"field_deltas,omitempty"`
	NewItems                    []ItemSummary                   `json:"new_items,omitempty"`
	ChangedItems                []ItemSummary                   `json:"changed_items,omitempty"`
	UnchangedCount              int                             `json:"unchanged_count"`
	TotalCount                  int                             `json:"total_count"`
	Collapsed                   bool                            `json:"collapsed"`
	FullSnapshotStored          bool                            `json:"full_snapshot_stored"`
	PreviousReportID            string                          `json:"previous_report_id,omitempty"`
	LastMeaningfulReportID      string                          `json:"last_meaningful_report_id,omitempty"`
	LastMeaningfulSnapshotID    string                          `json:"last_meaningful_snapshot_id,omitempty"`
	LastMeaningfulItemIDs       []string                        `json:"last_meaningful_item_ids,omitempty"`
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
		ID:         "claim-" + item.ID + "-status",
		ItemID:     item.ID,
		Kind:       reportschema.ClaimObservedFact,
		Text:       "Pinpoint " + item.ID + " is " + string(item.State),
		Confidence: reportschema.ConfidenceHigh,
		Evidence:   refs,
	}}
	if claim, ok := rootCauseClaim(item); ok {
		claims = append(claims, claim)
	}
	if item.Handoff != nil && len(item.Handoff.SuggestedVerification) > 0 {
		claims = append(claims, ClaimSummary{
			ID:         "claim-" + item.ID + "-verification-recommendation",
			ItemID:     item.ID,
			Kind:       reportschema.ClaimRecommendation,
			Text:       strings.Join(item.Handoff.SuggestedVerification, "; "),
			Confidence: reportschema.ConfidenceMedium,
			Evidence:   refs,
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
		ID:         "claim-" + item.ID + "-root-cause",
		ItemID:     item.ID,
		Kind:       reportschema.ClaimHypothesis,
		Text:       text,
		Confidence: reportschema.ConfidenceMedium,
		Evidence:   cleanStrings(evidence),
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
