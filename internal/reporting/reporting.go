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

	"github.com/Rememorio/codog/internal/roadmap"
)

const SnapshotSchemaVersion = "codog.reporting.snapshot.v1"

type Cursor struct {
	Channel        string            `json:"channel"`
	LastReportID   string            `json:"last_report_id,omitempty"`
	LastSnapshotID string            `json:"last_snapshot_id,omitempty"`
	LastReportedAt time.Time         `json:"last_reported_at,omitempty"`
	ItemHashes     map[string]string `json:"item_hashes,omitempty"`
}

type ItemSummary struct {
	ID             string                       `json:"id"`
	Title          string                       `json:"title"`
	State          roadmap.State                `json:"state"`
	Priority       roadmap.Priority             `json:"priority"`
	Severity       roadmap.Severity             `json:"severity"`
	Impact         roadmap.ImpactClass          `json:"impact"`
	Readiness      roadmap.HandoffReadiness     `json:"readiness,omitempty"`
	UpdatedAt      time.Time                    `json:"updated_at"`
	Fingerprint    string                       `json:"fingerprint"`
	EvidenceRefs   []string                     `json:"evidence_refs,omitempty"`
	Handoff        *roadmap.HandoffPacket       `json:"handoff,omitempty"`
	Implementation []roadmap.ImplementationLink `json:"implementation,omitempty"`
}

type Report struct {
	Kind               string        `json:"kind"`
	Channel            string        `json:"channel"`
	ReportID           string        `json:"report_id"`
	SnapshotID         string        `json:"snapshot_id"`
	GeneratedAt        time.Time     `json:"generated_at"`
	NewItems           []ItemSummary `json:"new_items,omitempty"`
	ChangedItems       []ItemSummary `json:"changed_items,omitempty"`
	UnchangedCount     int           `json:"unchanged_count"`
	TotalCount         int           `json:"total_count"`
	Collapsed          bool          `json:"collapsed"`
	FullSnapshotStored bool          `json:"full_snapshot_stored"`
	PreviousReportID   string        `json:"previous_report_id,omitempty"`
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

func NewStore(configHome string) Store {
	return Store{
		Dir:     filepath.Join(configHome, "reporting"),
		Roadmap: roadmap.NewStore(configHome),
	}
}

func (s Store) Generate(channel string, now time.Time) (Report, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return Report{}, errors.New("reporting channel is required")
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
	hashes := make(map[string]string, len(items))
	for _, item := range items {
		summary, err := summarizeItem(item)
		if err != nil {
			return Report{}, err
		}
		summaries = append(summaries, summary)
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
	for _, summary := range summaries {
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
		"snapshot_id":  snapshot.SnapshotID,
		"generated_at": now.Format(time.RFC3339Nano),
	})
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Kind:               "report_backpressure",
		Channel:            channel,
		ReportID:           "report-" + reportID,
		SnapshotID:         snapshot.SnapshotID,
		GeneratedAt:        now,
		NewItems:           newItems,
		ChangedItems:       changedItems,
		UnchangedCount:     unchangedCount,
		TotalCount:         len(summaries),
		Collapsed:          unchangedCount > 0,
		FullSnapshotStored: true,
		PreviousReportID:   cursor.LastReportID,
	}
	if err := s.saveSnapshot(snapshot); err != nil {
		return Report{}, err
	}
	cursor = Cursor{
		Channel:        channel,
		LastReportID:   report.ReportID,
		LastSnapshotID: snapshot.SnapshotID,
		LastReportedAt: now,
		ItemHashes:     hashes,
	}
	if err := s.saveCursor(cursor); err != nil {
		return Report{}, err
	}
	return report, nil
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

func summarizeItem(item roadmap.Item) (ItemSummary, error) {
	summary := ItemSummary{
		ID:             item.ID,
		Title:          item.Title,
		State:          item.State,
		Priority:       item.Priority,
		Severity:       item.Severity,
		Impact:         item.Impact,
		UpdatedAt:      item.UpdatedAt,
		EvidenceRefs:   evidenceRefs(item.Evidence),
		Handoff:        item.Handoff,
		Implementation: append([]roadmap.ImplementationLink(nil), item.Implementation...),
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
		"handoff":        summary.Handoff,
		"implementation": summary.Implementation,
	})
	if err != nil {
		return ItemSummary{}, err
	}
	summary.Fingerprint = hash
	return summary, nil
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
