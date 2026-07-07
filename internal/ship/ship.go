// Package ship defines delivery provenance records and lane-event projections.
package ship

import (
	"fmt"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/laneevents"
)

const (
	EventPrepared        = "ship.prepared"
	EventCommitsSelected = "ship.commits_selected"
	EventMerged          = "ship.merged"
	EventPushedMain      = "ship.pushed_main"
	EventProvenance      = "ship.provenance"
)

// Provenance captures the auditable delivery path for shipped work.
type Provenance struct {
	SourceBranch string `json:"source_branch,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	BaseCommit   string `json:"base_commit,omitempty"`
	FirstCommit  string `json:"first_commit,omitempty"`
	LastCommit   string `json:"last_commit,omitempty"`
	CommitRange  string `json:"commit_range,omitempty"`
	CommitCount  int    `json:"commit_count"`
	MergeMethod  string `json:"merge_method,omitempty"`
	Actor        string `json:"actor,omitempty"`
	PRNumber     int    `json:"pr_number,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	Remote       string `json:"remote,omitempty"`
	TargetBranch string `json:"target_branch,omitempty"`
}

// Classification separates intended deliverables from incidental riders in a
// delivery push.
type Classification struct {
	Intentional int `json:"intentional"`
	Riders      int `json:"riders"`
}

// Event is a compact machine-readable ship event.
type Event struct {
	Event          string         `json:"event"`
	Status         string         `json:"status"`
	At             time.Time      `json:"at"`
	Provenance     Provenance     `json:"provenance"`
	Classification Classification `json:"classification"`
}

// Report is the stable ship provenance payload embedded in command and state
// reports.
type Report struct {
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	Provenance     Provenance     `json:"provenance"`
	Classification Classification `json:"classification"`
	Events         []Event        `json:"events"`
	Summary        string         `json:"summary"`
}

// NewReport builds the standard ship provenance event set.
func NewReport(status string, provenance Provenance, classification Classification, at time.Time) Report {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "planned"
	}
	provenance.CommitRange = normalizeRange(provenance)
	events := []Event{
		newEvent(EventPrepared, status, provenance, classification, at),
		newEvent(EventCommitsSelected, status, provenance, classification, at),
		newEvent(EventMerged, status, provenance, classification, at),
		newEvent(EventPushedMain, status, provenance, classification, at),
		newEvent(EventProvenance, status, provenance, classification, at),
	}
	return Report{
		Kind:           "ship_provenance",
		Status:         status,
		Provenance:     provenance,
		Classification: classification,
		Events:         events,
		Summary:        Summary(provenance, classification),
	}
}

// LaneEvents projects ship events onto the common lane event envelope.
func LaneEvents(report Report) []laneevents.Event {
	out := make([]laneevents.Event, 0, len(report.Events))
	for index, event := range report.Events {
		out = append(out, laneevents.Normalize(laneevents.Event{
			Sequence:  int64(index + 1),
			Type:      event.Event,
			LaneEvent: event.Event,
			Status:    event.Status,
			Message:   report.Summary,
			CreatedAt: event.At,
			Provenance: laneevents.Provenance{
				Source:      laneevents.ProvenanceLiveLane,
				Environment: "local",
				Emitter:     "codog",
				Confidence:  1,
			},
			Evidence: map[string]any{
				"ship":           event.Provenance,
				"classification": event.Classification,
			},
		}))
	}
	return out
}

// Summary renders a short human-readable provenance summary.
func Summary(provenance Provenance, classification Classification) string {
	method := strings.TrimSpace(provenance.MergeMethod)
	if method == "" {
		method = "unknown"
	}
	if classification.Intentional == 0 && provenance.CommitCount > 0 {
		classification.Intentional = provenance.CommitCount
	}
	return fmt.Sprintf("%d intentional commit(s), %d rider(s), via %s", classification.Intentional, classification.Riders, method)
}

func newEvent(name string, status string, provenance Provenance, classification Classification, at time.Time) Event {
	return Event{Event: name, Status: status, At: at, Provenance: provenance, Classification: classification}
}

func normalizeRange(provenance Provenance) string {
	if strings.TrimSpace(provenance.CommitRange) != "" {
		return provenance.CommitRange
	}
	first := strings.TrimSpace(provenance.FirstCommit)
	last := strings.TrimSpace(provenance.LastCommit)
	if first == "" && last == "" {
		return ""
	}
	if first == "" {
		first = last
	}
	if last == "" {
		last = first
	}
	if first == last {
		return first
	}
	return first + ".." + last
}
