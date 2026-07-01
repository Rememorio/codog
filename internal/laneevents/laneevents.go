package laneevents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	LaneStarted            = "lane.started"
	LaneReady              = "lane.ready"
	LanePromptMisdelivery  = "lane.prompt_misdelivery"
	LaneBlocked            = "lane.blocked"
	LaneRed                = "lane.red"
	LaneGreen              = "lane.green"
	LaneCommitCreated      = "lane.commit.created"
	LanePROpened           = "lane.pr.opened"
	LaneMergeReady         = "lane.merge.ready"
	LaneFinished           = "lane.finished"
	LaneFailed             = "lane.failed"
	BranchStaleAgainstMain = "branch.stale_against_main"

	ProvenanceLiveLane    = "live_lane"
	ProvenanceTest        = "test"
	ProvenanceHealthcheck = "healthcheck"
	ProvenanceReplay      = "replay"
	ProvenanceTransport   = "transport"
)

type Provenance struct {
	Source      string  `json:"source"`
	Environment string  `json:"environment"`
	Emitter     string  `json:"emitter"`
	Confidence  float64 `json:"confidence"`
}

type Binding struct {
	Owner         string `json:"owner,omitempty"`
	Scope         string `json:"scope,omitempty"`
	WatcherAction string `json:"watcher_action,omitempty"`
}

type Event struct {
	Sequence          int64          `json:"sequence"`
	Type              string         `json:"type"`
	LaneEvent         string         `json:"lane_event"`
	LaneID            string         `json:"lane_id,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	TaskID            string         `json:"task_id,omitempty"`
	Status            string         `json:"status,omitempty"`
	Message           string         `json:"message,omitempty"`
	Classification    string         `json:"classification,omitempty"`
	Evidence          map[string]any `json:"evidence,omitempty"`
	FinishReason      string         `json:"finish_reason,omitempty"`
	TokensOutput      int64          `json:"tokens_output,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	Provenance        Provenance     `json:"provenance"`
	Binding           Binding        `json:"binding,omitempty"`
	Terminal          bool           `json:"terminal,omitempty"`
	Fingerprint       string         `json:"fingerprint,omitempty"`
	PayloadHash       string         `json:"payload_hash,omitempty"`
	DuplicateOf       string         `json:"duplicate_of,omitempty"`
	MateriallyDiffers bool           `json:"materially_differs,omitempty"`
}

type Projection struct {
	Events                       []Event `json:"events"`
	ActionableTerminal           *Event  `json:"actionable_terminal,omitempty"`
	DuplicateTerminals           []Event `json:"duplicate_terminals,omitempty"`
	MateriallyDifferentTerminals []Event `json:"materially_different_terminals,omitempty"`
}

func RequiredLaneEvents() []string {
	return []string{
		LaneStarted,
		LaneReady,
		LanePromptMisdelivery,
		LaneBlocked,
		LaneRed,
		LaneGreen,
		LaneCommitCreated,
		LanePROpened,
		LaneMergeReady,
		LaneFinished,
		LaneFailed,
		BranchStaleAgainstMain,
	}
}

func Reconcile(events []Event) Projection {
	normalized := make([]Event, 0, len(events))
	for index, event := range events {
		if event.Sequence == 0 {
			event.Sequence = int64(index + 1)
		}
		normalized = append(normalized, Normalize(event))
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Sequence == normalized[j].Sequence {
			return normalized[i].CreatedAt.Before(normalized[j].CreatedAt)
		}
		return normalized[i].Sequence < normalized[j].Sequence
	})

	firstTerminalByFingerprint := map[string]Event{}
	var actionable *Event
	projection := Projection{Events: make([]Event, 0, len(normalized))}
	for _, event := range normalized {
		if event.Terminal {
			if first, ok := firstTerminalByFingerprint[event.Fingerprint]; ok {
				event.DuplicateOf = first.Fingerprint
				if event.PayloadHash != first.PayloadHash {
					event.MateriallyDiffers = true
					projection.MateriallyDifferentTerminals = append(projection.MateriallyDifferentTerminals, event)
				}
				projection.DuplicateTerminals = append(projection.DuplicateTerminals, event)
			} else {
				firstTerminalByFingerprint[event.Fingerprint] = event
				next := event
				actionable = &next
			}
		}
		projection.Events = append(projection.Events, event)
	}
	projection.ActionableTerminal = actionable
	return projection
}

func Normalize(event Event) Event {
	event.Type = strings.TrimSpace(event.Type)
	event.LaneEvent = strings.TrimSpace(event.LaneEvent)
	if event.LaneEvent == "" {
		event.LaneEvent = CanonicalLaneEvent(event.Type, event.Status, event.FinishReason)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	event.Provenance = NormalizeProvenance(event.Provenance)
	event.Binding.WatcherAction = strings.TrimSpace(event.Binding.WatcherAction)
	event.Terminal = IsTerminal(event)
	event.Fingerprint = EventFingerprint(event)
	event.PayloadHash = PayloadHash(event)
	return event
}

func NormalizeProvenance(provenance Provenance) Provenance {
	provenance.Source = strings.TrimSpace(provenance.Source)
	if provenance.Source == "" {
		provenance.Source = ProvenanceLiveLane
	}
	provenance.Environment = strings.TrimSpace(provenance.Environment)
	if provenance.Environment == "" {
		provenance.Environment = "local"
	}
	provenance.Emitter = strings.TrimSpace(provenance.Emitter)
	if provenance.Emitter == "" {
		provenance.Emitter = "codog"
	}
	if provenance.Confidence <= 0 {
		provenance.Confidence = 1
	}
	return provenance
}

func CanonicalLaneEvent(eventType string, status string, finishReason string) string {
	eventType = strings.TrimSpace(strings.ToLower(eventType))
	status = strings.TrimSpace(strings.ToLower(status))
	finishReason = strings.TrimSpace(strings.ToLower(finishReason))
	switch eventType {
	case "created", "prompt_sent", "restarted":
		return LaneStarted
	case "ready", "trust_resolved":
		return LaneReady
	case "trust_prompt", "blocked", "worker.startup_no_evidence", "startup_no_evidence":
		return LaneBlocked
	case "prompt_misdelivery":
		return LanePromptMisdelivery
	case "green":
		return LaneGreen
	case "red":
		return LaneRed
	case "commit_created":
		return LaneCommitCreated
	case "pr_opened":
		return LanePROpened
	case "merge_ready":
		return LaneMergeReady
	case "stale_against_main":
		return BranchStaleAgainstMain
	case "terminated":
		return LaneFailed
	case "completed":
		if status == "failed" || finishReason == "failed" || finishReason == "error" {
			return LaneFailed
		}
		return LaneFinished
	default:
		if status == "failed" {
			return LaneFailed
		}
		if status == "finished" || status == "completed" {
			return LaneFinished
		}
		return LaneStarted
	}
}

func IsTerminal(event Event) bool {
	switch strings.TrimSpace(event.LaneEvent) {
	case LaneFinished, LaneFailed, LaneRed:
		return true
	default:
		return false
	}
}

func EventFingerprint(event Event) string {
	parts := []string{
		"terminal",
		strings.TrimSpace(event.LaneID),
		strings.TrimSpace(event.SessionID),
		strings.TrimSpace(event.TaskID),
		strings.TrimSpace(event.LaneEvent),
		strings.TrimSpace(event.Status),
		strings.TrimSpace(event.FinishReason),
	}
	if !IsTerminal(event) {
		parts[0] = "event"
		parts = append(parts, strings.TrimSpace(event.Type), strings.TrimSpace(event.Message))
	}
	return shortHash(strings.Join(parts, "\x00"))
}

func PayloadHash(event Event) string {
	payload := map[string]any{
		"type":           strings.TrimSpace(event.Type),
		"lane_event":     strings.TrimSpace(event.LaneEvent),
		"status":         strings.TrimSpace(event.Status),
		"message":        strings.TrimSpace(event.Message),
		"classification": strings.TrimSpace(event.Classification),
		"evidence":       event.Evidence,
		"finish_reason":  strings.TrimSpace(event.FinishReason),
		"tokens_output":  event.TokensOutput,
	}
	data, _ := json.Marshal(payload)
	return shortHash(string(data))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
