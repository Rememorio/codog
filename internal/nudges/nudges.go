// Package nudges tracks recurring external nudges and acknowledgements.
package nudges

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
)

// State describes how the current delivery relates to prior nudge state.
type State string

const (
	StateNew            State = "new_nudge"
	StateRetry          State = "retry_nudge"
	StateStaleDuplicate State = "stale_duplicate"
)

// Record is the persisted state for one nudge cycle.
type Record struct {
	NudgeID          string     `json:"nudge_id"`
	CycleID          string     `json:"cycle_id"`
	Fingerprint      string     `json:"fingerprint"`
	FirstDeliveredAt time.Time  `json:"first_delivered_at"`
	LastDeliveredAt  time.Time  `json:"last_delivered_at"`
	DeliveryCount    int        `json:"delivery_count"`
	LastPrompt       string     `json:"last_prompt,omitempty"`
	Acknowledged     bool       `json:"acknowledged"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at,omitempty"`
	ResponseID       string     `json:"response_id,omitempty"`
	LastState        State      `json:"last_state"`
}

// Observation is returned after recording or acknowledging a delivery.
type Observation struct {
	Kind                string    `json:"kind"`
	NudgeID             string    `json:"nudge_id"`
	CycleID             string    `json:"cycle_id"`
	Fingerprint         string    `json:"fingerprint"`
	State               State     `json:"state"`
	DeliveryCount       int       `json:"delivery_count"`
	DeliveredAt         time.Time `json:"delivered_at"`
	AlreadyAcknowledged bool      `json:"already_acknowledged"`
	Acknowledged        bool      `json:"acknowledged"`
	Stale               bool      `json:"stale"`
	ResponseID          string    `json:"response_id,omitempty"`
	Record              Record    `json:"record"`
}

// Delivery is one incoming nudge delivery.
type Delivery struct {
	NudgeID     string
	CycleID     string
	Prompt      string
	DeliveredAt time.Time
	ResponseID  string
}

// Store persists nudge cycle records under the Codog config home.
type Store struct {
	Dir string
}

// NewStore returns a nudge store rooted at configHome.
func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "nudges")}
}

// Observe records a delivery and classifies it as new, retry, or stale.
func (s Store) Observe(delivery Delivery) (Observation, error) {
	delivery = normalizeDelivery(delivery)
	if delivery.NudgeID == "" {
		return Observation{}, errors.New("nudge_id is required")
	}
	if delivery.CycleID == "" {
		return Observation{}, errors.New("cycle_id is required")
	}
	record, err := s.Get(delivery.NudgeID, delivery.CycleID)
	if err != nil && !os.IsNotExist(err) {
		return Observation{}, err
	}
	state := StateNew
	if err == nil {
		if record.Acknowledged {
			state = StateStaleDuplicate
		} else {
			state = StateRetry
		}
	} else {
		record = Record{
			NudgeID:          delivery.NudgeID,
			CycleID:          delivery.CycleID,
			Fingerprint:      fingerprint(delivery.NudgeID, delivery.CycleID),
			FirstDeliveredAt: delivery.DeliveredAt,
		}
	}
	record.LastDeliveredAt = delivery.DeliveredAt
	record.DeliveryCount++
	record.LastPrompt = strings.TrimSpace(delivery.Prompt)
	record.LastState = state
	if delivery.ResponseID != "" {
		record.ResponseID = delivery.ResponseID
	}
	if err := s.save(record); err != nil {
		return Observation{}, err
	}
	return observationFromRecord(record, delivery.DeliveredAt, state), nil
}

// Acknowledge records that Codog handled a nudge cycle.
func (s Store) Acknowledge(delivery Delivery) (Observation, error) {
	observation, err := s.Observe(delivery)
	if err != nil {
		return Observation{}, err
	}
	record := observation.Record
	now := delivery.DeliveredAt
	record.Acknowledged = true
	record.AcknowledgedAt = &now
	if strings.TrimSpace(delivery.ResponseID) != "" {
		record.ResponseID = strings.TrimSpace(delivery.ResponseID)
	}
	if err := s.save(record); err != nil {
		return Observation{}, err
	}
	observation = observationFromRecord(record, delivery.DeliveredAt, observation.State)
	observation.Acknowledged = true
	observation.AlreadyAcknowledged = observation.State == StateStaleDuplicate
	observation.Stale = observation.State == StateStaleDuplicate
	return observation, nil
}

// Get loads one nudge cycle record.
func (s Store) Get(nudgeID string, cycleID string) (Record, error) {
	path, err := s.path(nudgeID, cycleID)
	if err != nil {
		return Record{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// List returns all records, newest delivery first.
func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Record{}, nil
		}
		return nil, err
	}
	records := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].LastDeliveredAt.After(records[j].LastDeliveredAt)
	})
	return records, nil
}

func (s Store) save(record Record) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path, err := s.path(record.NudgeID, record.CycleID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) path(nudgeID string, cycleID string) (string, error) {
	nudgeID = strings.TrimSpace(nudgeID)
	cycleID = strings.TrimSpace(cycleID)
	if nudgeID == "" || cycleID == "" {
		return "", errors.New("nudge_id and cycle_id are required")
	}
	if strings.ContainsAny(nudgeID, `/\`) || strings.ContainsAny(cycleID, `/\`) || nudgeID == "." || cycleID == "." || nudgeID == ".." || cycleID == ".." {
		return "", errors.New("nudge_id and cycle_id must be path components")
	}
	return filepath.Join(s.Dir, nudgeID+"--"+cycleID+".json"), nil
}

func normalizeDelivery(delivery Delivery) Delivery {
	delivery.NudgeID = strings.TrimSpace(delivery.NudgeID)
	delivery.CycleID = strings.TrimSpace(delivery.CycleID)
	delivery.ResponseID = strings.TrimSpace(delivery.ResponseID)
	if delivery.DeliveredAt.IsZero() {
		delivery.DeliveredAt = time.Now().UTC()
	} else {
		delivery.DeliveredAt = delivery.DeliveredAt.UTC()
	}
	return delivery
}

func observationFromRecord(record Record, deliveredAt time.Time, state State) Observation {
	return Observation{
		Kind:                "nudge",
		NudgeID:             record.NudgeID,
		CycleID:             record.CycleID,
		Fingerprint:         record.Fingerprint,
		State:               state,
		DeliveryCount:       record.DeliveryCount,
		DeliveredAt:         deliveredAt,
		AlreadyAcknowledged: record.Acknowledged,
		Acknowledged:        record.Acknowledged,
		Stale:               state == StateStaleDuplicate,
		ResponseID:          record.ResponseID,
		Record:              record,
	}
}

func fingerprint(nudgeID string, cycleID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{nudgeID, cycleID}, "\x00")))
	return "nudge:" + hex.EncodeToString(sum[:12])
}
