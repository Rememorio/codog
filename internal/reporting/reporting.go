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
	Kind                     string         `json:"kind"`
	Channel                  string         `json:"channel"`
	ReportID                 string         `json:"report_id"`
	TriggerID                string         `json:"trigger_id,omitempty"`
	SnapshotID               string         `json:"snapshot_id"`
	GeneratedAt              time.Time      `json:"generated_at"`
	Outcome                  string         `json:"outcome"`
	Checked                  bool           `json:"checked"`
	CheckedSurfaces          []string       `json:"checked_surfaces,omitempty"`
	NoChange                 bool           `json:"no_change"`
	MixedFreshness           bool           `json:"mixed_freshness"`
	FreshnessCounts          map[string]int `json:"freshness_counts,omitempty"`
	Claims                   []ClaimSummary `json:"claims,omitempty"`
	NewItems                 []ItemSummary  `json:"new_items,omitempty"`
	ChangedItems             []ItemSummary  `json:"changed_items,omitempty"`
	UnchangedCount           int            `json:"unchanged_count"`
	TotalCount               int            `json:"total_count"`
	Collapsed                bool           `json:"collapsed"`
	FullSnapshotStored       bool           `json:"full_snapshot_stored"`
	PreviousReportID         string         `json:"previous_report_id,omitempty"`
	LastMeaningfulReportID   string         `json:"last_meaningful_report_id,omitempty"`
	LastMeaningfulSnapshotID string         `json:"last_meaningful_snapshot_id,omitempty"`
	LastMeaningfulItemIDs    []string       `json:"last_meaningful_item_ids,omitempty"`
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
	FreshnessTTL    time.Duration
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
	report := Report{
		Kind:                     "report_backpressure",
		Channel:                  channel,
		ReportID:                 "report-" + reportID,
		TriggerID:                options.TriggerID,
		SnapshotID:               snapshot.SnapshotID,
		GeneratedAt:              now,
		Outcome:                  outcome,
		Checked:                  true,
		CheckedSurfaces:          options.CheckedSurfaces,
		NoChange:                 noChange,
		MixedFreshness:           len(freshnessCounts) > 1,
		FreshnessCounts:          freshnessCounts,
		Claims:                   claims,
		NewItems:                 newItems,
		ChangedItems:             changedItems,
		UnchangedCount:           unchangedCount,
		TotalCount:               len(summaries),
		Collapsed:                unchangedCount > 0,
		FullSnapshotStored:       true,
		PreviousReportID:         cursor.LastReportID,
		LastMeaningfulReportID:   lastMeaningfulReportID,
		LastMeaningfulSnapshotID: lastMeaningfulSnapshotID,
		LastMeaningfulItemIDs:    lastMeaningfulItemIDs,
	}
	if err := s.saveSnapshot(snapshot); err != nil {
		return Report{}, err
	}
	cursor = Cursor{
		Channel:                  channel,
		LastReportID:             report.ReportID,
		LastSnapshotID:           snapshot.SnapshotID,
		LastReportedAt:           now,
		LastMeaningfulReportID:   lastMeaningfulReportID,
		LastMeaningfulSnapshotID: lastMeaningfulSnapshotID,
		LastMeaningfulItemIDs:    lastMeaningfulItemIDs,
		ItemHashes:               hashes,
	}
	if err := s.saveCursor(cursor); err != nil {
		return Report{}, err
	}
	return report, nil
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
