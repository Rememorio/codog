package provisional

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

const (
	// DefaultWindow is the reconciliation window for unchanged provisional updates.
	DefaultWindow = 5 * time.Minute

	// DecisionNew marks the first provisional status observed for a channel.
	DecisionNew = "new_provisional"
	// DecisionMaterialChange marks an update with a changed status fingerprint.
	DecisionMaterialChange = "material_change"
	// DecisionRepeatedAfterWindow marks an unchanged update exposed after the reconciliation window.
	DecisionRepeatedAfterWindow = "repeated_after_window"
	// DecisionSuppressedDuplicate marks an unchanged update hidden inside the reconciliation window.
	DecisionSuppressedDuplicate = "suppressed_duplicate"
)

// Update describes a provisional or in-flight status acknowledgement to observe.
type Update struct {
	Channel       string
	Owner         string
	Status        string
	ProgressState string
	Blocker       string
	ETA           string
	Message       string
	ObservedAt    time.Time
	Window        time.Duration
}

// Event is the audit record for a raw provisional status observation.
type Event struct {
	EventID       string    `json:"event_id"`
	Channel       string    `json:"channel"`
	Owner         string    `json:"owner,omitempty"`
	Status        string    `json:"status,omitempty"`
	ProgressState string    `json:"progress_state,omitempty"`
	Blocker       string    `json:"blocker,omitempty"`
	ETA           string    `json:"eta,omitempty"`
	Message       string    `json:"message,omitempty"`
	Fingerprint   string    `json:"fingerprint"`
	ObservedAt    time.Time `json:"observed_at"`
	Decision      string    `json:"decision"`
	Exposed       bool      `json:"exposed"`
}

// State stores the latest deduplication state for a channel.
type State struct {
	Kind              string    `json:"kind"`
	Channel           string    `json:"channel"`
	Fingerprint       string    `json:"fingerprint"`
	FirstObservedAt   time.Time `json:"first_observed_at"`
	LastObservedAt    time.Time `json:"last_observed_at"`
	LastExposedAt     time.Time `json:"last_exposed_at"`
	RepeatCount       int       `json:"repeat_count"`
	SuppressedCount   int       `json:"suppressed_count"`
	RawEventCount     int       `json:"raw_event_count"`
	LastDecision      string    `json:"last_decision"`
	LastExposedEvent  Event     `json:"last_exposed_event"`
	LastObservedEvent Event     `json:"last_observed_event"`
}

// Observation reports the deduplication decision for a newly observed update.
type Observation struct {
	Kind                string `json:"kind"`
	Channel             string `json:"channel"`
	Decision            string `json:"decision"`
	Exposed             bool   `json:"exposed"`
	Reason              string `json:"reason"`
	Fingerprint         string `json:"fingerprint"`
	PreviousFingerprint string `json:"previous_fingerprint,omitempty"`
	WindowSeconds       int64  `json:"window_seconds"`
	Event               Event  `json:"event"`
	State               State  `json:"state"`
}

// Store persists provisional status states and append-only audit events.
type Store struct {
	Dir string
}

// NewStore returns a store rooted under configHome.
func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "provisional_status")}
}

// Observe records an update, returns whether it should be exposed, and always writes an audit event.
func (s Store) Observe(update Update) (Observation, error) {
	update = normalizeUpdate(update)
	if update.Channel == "" {
		return Observation{}, errors.New("channel is required")
	}
	if update.Window <= 0 {
		update.Window = DefaultWindow
	}
	fingerprint, err := updateFingerprint(update)
	if err != nil {
		return Observation{}, err
	}
	event := Event{
		Channel:       update.Channel,
		Owner:         update.Owner,
		Status:        update.Status,
		ProgressState: update.ProgressState,
		Blocker:       update.Blocker,
		ETA:           update.ETA,
		Message:       update.Message,
		Fingerprint:   fingerprint,
		ObservedAt:    update.ObservedAt,
	}
	eventID, err := eventHash(event)
	if err != nil {
		return Observation{}, err
	}
	event.EventID = eventID
	previous, err := s.Get(update.Channel)
	hasPrevious := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Observation{}, err
	}
	decision := DecisionNew
	reason := "first provisional status for channel"
	exposed := true
	if hasPrevious {
		switch {
		case previous.Fingerprint != fingerprint:
			decision = DecisionMaterialChange
			reason = "owner, progress state, blocker, eta, or status changed"
		case !previous.LastExposedAt.IsZero() && update.ObservedAt.Sub(previous.LastExposedAt) <= update.Window:
			decision = DecisionSuppressedDuplicate
			reason = "unchanged provisional status inside reconciliation window"
			exposed = false
		default:
			decision = DecisionRepeatedAfterWindow
			reason = "unchanged provisional status outside reconciliation window"
		}
	}
	event.Decision = decision
	event.Exposed = exposed
	state := mergeState(previous, event, exposed, hasPrevious)
	if err := s.saveState(state); err != nil {
		return Observation{}, err
	}
	if err := s.appendAudit(event); err != nil {
		return Observation{}, err
	}
	previousFingerprint := ""
	if hasPrevious {
		previousFingerprint = previous.Fingerprint
	}
	return Observation{
		Kind:                "provisional_status",
		Channel:             update.Channel,
		Decision:            decision,
		Exposed:             exposed,
		Reason:              reason,
		Fingerprint:         fingerprint,
		PreviousFingerprint: previousFingerprint,
		WindowSeconds:       int64(update.Window / time.Second),
		Event:               event,
		State:               state,
	}, nil
}

// Get returns the current deduplication state for a channel.
func (s Store) Get(channel string) (State, error) {
	path, err := s.statePath(channel)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

// List returns all known channel states ordered by most recent observation.
func (s Store) List() ([]State, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []State{}, nil
		}
		return nil, err
	}
	out := []State{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".state.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var state State
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastObservedAt.After(out[j].LastObservedAt) })
	return out, nil
}

func (s Store) saveState(state State) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path, err := s.statePath(state.Channel)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) appendAudit(event Event) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path, err := s.auditPath(event.Channel)
	if err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (s Store) statePath(channel string) (string, error) {
	key, err := channelKey(channel)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, key+".state.json"), nil
}

func (s Store) auditPath(channel string) (string, error) {
	key, err := channelKey(channel)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, key+".audit.jsonl"), nil
}

func normalizeUpdate(update Update) Update {
	update.Channel = strings.TrimSpace(update.Channel)
	update.Owner = strings.TrimSpace(update.Owner)
	update.Status = normalizeStatus(update.Status, update.Message)
	update.ProgressState = strings.TrimSpace(update.ProgressState)
	update.Blocker = strings.TrimSpace(update.Blocker)
	update.ETA = strings.TrimSpace(update.ETA)
	update.Message = strings.TrimSpace(update.Message)
	if update.ObservedAt.IsZero() {
		update.ObservedAt = time.Now().UTC()
	} else {
		update.ObservedAt = update.ObservedAt.UTC()
	}
	return update
}

func normalizeStatus(status string, message string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "" {
		return status
	}
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(normalized, "please wait"):
		return "in_flight"
	case strings.Contains(normalized, "working on it"):
		return "in_flight"
	case strings.Contains(normalized, "adding") && strings.Contains(normalized, "roadmap"):
		return "in_flight"
	case normalized != "":
		return "in_flight"
	default:
		return "provisional"
	}
}

func mergeState(previous State, event Event, exposed bool, hasPrevious bool) State {
	state := previous
	if !hasPrevious {
		state = State{
			Kind:            "provisional_status_state",
			Channel:         event.Channel,
			FirstObservedAt: event.ObservedAt,
		}
	}
	state.Channel = event.Channel
	state.Fingerprint = event.Fingerprint
	state.LastObservedAt = event.ObservedAt
	state.RawEventCount++
	state.LastDecision = event.Decision
	state.LastObservedEvent = event
	if hasPrevious && previous.Fingerprint == event.Fingerprint {
		state.RepeatCount++
	} else {
		state.RepeatCount = 1
		state.SuppressedCount = 0
	}
	if exposed {
		state.LastExposedAt = event.ObservedAt
		state.LastExposedEvent = event
	} else {
		state.SuppressedCount++
	}
	return state
}

func updateFingerprint(update Update) (string, error) {
	return stableHash(map[string]any{
		"channel":        update.Channel,
		"owner":          update.Owner,
		"status":         update.Status,
		"progress_state": update.ProgressState,
		"blocker":        update.Blocker,
		"eta":            update.ETA,
	})
}

func eventHash(event Event) (string, error) {
	return stableHash(map[string]any{
		"channel":        event.Channel,
		"owner":          event.Owner,
		"status":         event.Status,
		"progress_state": event.ProgressState,
		"blocker":        event.Blocker,
		"eta":            event.ETA,
		"message":        event.Message,
		"fingerprint":    event.Fingerprint,
		"observed_at":    event.ObservedAt.Format(time.RFC3339Nano),
	})
}

func channelKey(channel string) (string, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return "", errors.New("channel is required")
	}
	sum := sha256.Sum256([]byte(channel))
	return hex.EncodeToString(sum[:])[:16], nil
}

func stableHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16], nil
}
