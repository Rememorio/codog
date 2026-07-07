// Package roadmap stores machine-readable roadmap pinpoints.
package roadmap

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

// State is the lifecycle state for a roadmap pinpoint.
type State string

const (
	StateFiled        State = "filed"
	StateAcknowledged State = "acknowledged"
	StateInProgress   State = "in_progress"
	StateBlocked      State = "blocked"
	StateDone         State = "done"
	StateSuperseded   State = "superseded"

	EvidenceRepro         EvidenceRole = "repro"
	EvidenceSymptom       EvidenceRole = "symptom"
	EvidenceRootCauseHint EvidenceRole = "root_cause_hint"
	EvidenceVerification  EvidenceRole = "verification"

	MaxEvidencePreviewRunes = 240
)

// EvidenceRole classifies how an attachment supports a roadmap pinpoint.
type EvidenceRole string

// EvidenceAttachment is a bounded preview plus canonical machine reference.
type EvidenceAttachment struct {
	ID        string       `json:"id"`
	Role      EvidenceRole `json:"role"`
	Type      string       `json:"type"`
	Reference string       `json:"reference"`
	Preview   string       `json:"preview,omitempty"`
	AddedAt   time.Time    `json:"added_at"`
}

// Item is one tracked roadmap pinpoint.
type Item struct {
	ID                string               `json:"id"`
	Title             string               `json:"title"`
	Description       string               `json:"description,omitempty"`
	State             State                `json:"state"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
	LastStateChangeAt time.Time            `json:"last_state_change_at"`
	Supersedes        []string             `json:"supersedes,omitempty"`
	SupersededBy      string               `json:"superseded_by,omitempty"`
	Related           []string             `json:"related,omitempty"`
	Lineage           []string             `json:"lineage,omitempty"`
	ReportID          string               `json:"report_id,omitempty"`
	Evidence          []EvidenceAttachment `json:"evidence,omitempty"`
}

// Filing is a create or update request for a roadmap pinpoint.
type Filing struct {
	ID           string
	Title        string
	Description  string
	State        State
	Supersedes   []string
	SupersededBy string
	Related      []string
	ReportID     string
	Evidence     []EvidenceAttachment
	Now          time.Time
}

// Result describes whether a filing created or updated an item.
type Result struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	ItemID  string `json:"item_id"`
	State   State  `json:"state"`
	Item    Item   `json:"item"`
	Created bool   `json:"created"`
}

// Store persists roadmap pinpoints under the Codog config home.
type Store struct {
	Dir string
}

// NewStore returns a roadmap store rooted at configHome.
func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "roadmap-pinpoints")}
}

// File creates a new pinpoint or updates an existing one.
func (s Store) File(filing Filing) (Result, error) {
	filing = normalizeFiling(filing)
	if filing.Title == "" && filing.ID == "" {
		return Result{}, errors.New("title or id is required")
	}
	if filing.State == "" {
		filing.State = StateFiled
	}
	if err := validateState(filing.State); err != nil {
		return Result{}, err
	}
	id := filing.ID
	if id == "" {
		id = stableID(filing.Title, filing.Description)
	}
	item, err := s.Get(id)
	created := false
	if err != nil {
		if !os.IsNotExist(err) {
			return Result{}, err
		}
		created = true
		item = Item{
			ID:                id,
			Title:             filing.Title,
			Description:       filing.Description,
			State:             filing.State,
			CreatedAt:         filing.Now,
			UpdatedAt:         filing.Now,
			LastStateChangeAt: filing.Now,
			Lineage:           []string{id},
		}
	} else {
		if filing.Title != "" {
			item.Title = filing.Title
		}
		if filing.Description != "" {
			item.Description = filing.Description
		}
		item.UpdatedAt = filing.Now
		if filing.State != "" && item.State != filing.State {
			item.State = filing.State
			item.LastStateChangeAt = filing.Now
		}
	}
	item.Supersedes = mergeStrings(item.Supersedes, filing.Supersedes)
	item.Related = mergeStrings(item.Related, filing.Related)
	evidence, err := normalizeEvidence(filing.Evidence, filing.Now)
	if err != nil {
		return Result{}, err
	}
	item.Evidence = mergeEvidence(item.Evidence, evidence)
	if filing.SupersededBy != "" {
		item.SupersededBy = filing.SupersededBy
		if item.State != StateSuperseded {
			item.State = StateSuperseded
			item.LastStateChangeAt = filing.Now
		}
	}
	if filing.ReportID != "" {
		item.ReportID = filing.ReportID
	}
	if item.Lineage == nil {
		item.Lineage = []string{item.ID}
	}
	if err := s.save(item); err != nil {
		return Result{}, err
	}
	action := "roadmap_update"
	if created {
		action = "new_roadmap_filing"
	}
	return Result{Kind: "roadmap_pinpoint", Action: action, ItemID: item.ID, State: item.State, Item: item, Created: created}, nil
}

// Get loads one item by id.
func (s Store) Get(id string) (Item, error) {
	path, err := s.path(id)
	if err != nil {
		return Item{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Item{}, err
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, err
	}
	return item, nil
}

// List returns all items, newest update first.
func (s Store) List() ([]Item, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Item{}, nil
		}
		return nil, err
	}
	items := []Item{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s Store) save(item Item) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path, err := s.path(item.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) path(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("roadmap id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", errors.New("roadmap id must be a path component")
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

func normalizeFiling(filing Filing) Filing {
	filing.ID = strings.TrimSpace(filing.ID)
	filing.Title = strings.TrimSpace(filing.Title)
	filing.Description = strings.TrimSpace(filing.Description)
	filing.SupersededBy = strings.TrimSpace(filing.SupersededBy)
	filing.ReportID = strings.TrimSpace(filing.ReportID)
	if filing.Now.IsZero() {
		filing.Now = time.Now().UTC()
	} else {
		filing.Now = filing.Now.UTC()
	}
	filing.Supersedes = cleanStrings(filing.Supersedes)
	filing.Related = cleanStrings(filing.Related)
	return filing
}

func validateState(state State) error {
	switch state {
	case StateFiled, StateAcknowledged, StateInProgress, StateBlocked, StateDone, StateSuperseded:
		return nil
	default:
		return errors.New("invalid roadmap lifecycle state")
	}
}

func normalizeEvidence(values []EvidenceAttachment, now time.Time) ([]EvidenceAttachment, error) {
	out := make([]EvidenceAttachment, 0, len(values))
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Role = EvidenceRole(strings.TrimSpace(string(value.Role)))
		value.Type = strings.TrimSpace(value.Type)
		value.Reference = strings.TrimSpace(value.Reference)
		value.Preview = boundedPreview(value.Preview)
		if value.Role == "" && value.Type == "" && value.Reference == "" && value.Preview == "" {
			continue
		}
		if err := validateEvidenceRole(value.Role); err != nil {
			return nil, err
		}
		if value.Type == "" {
			value.Type = "reference"
		}
		if value.Reference == "" {
			return nil, errors.New("evidence reference is required")
		}
		if value.AddedAt.IsZero() {
			value.AddedAt = now
		} else {
			value.AddedAt = value.AddedAt.UTC()
		}
		if value.ID == "" {
			value.ID = evidenceID(value)
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AddedAt.Equal(out[j].AddedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].AddedAt.Before(out[j].AddedAt)
	})
	return out, nil
}

func validateEvidenceRole(role EvidenceRole) error {
	switch role {
	case EvidenceRepro, EvidenceSymptom, EvidenceRootCauseHint, EvidenceVerification:
		return nil
	default:
		return errors.New("invalid roadmap evidence role")
	}
}

func evidenceID(value EvidenceAttachment) string {
	key := strings.Join([]string{string(value.Role), value.Type, value.Reference}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return "ev-" + hex.EncodeToString(sum[:6])
}

func boundedPreview(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= MaxEvidencePreviewRunes {
		return value
	}
	return string(runes[:MaxEvidencePreviewRunes])
}

func stableID(title string, description string) string {
	key := strings.ToLower(strings.TrimSpace(title)) + "\x00" + strings.ToLower(strings.TrimSpace(description))
	sum := sha256.Sum256([]byte(key))
	return "rp-" + hex.EncodeToString(sum[:6])
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

func mergeStrings(existing []string, next []string) []string {
	return cleanStrings(append(append([]string(nil), existing...), next...))
}

func mergeEvidence(existing []EvidenceAttachment, next []EvidenceAttachment) []EvidenceAttachment {
	if len(next) == 0 {
		return existing
	}
	merged := append([]EvidenceAttachment(nil), existing...)
	byID := make(map[string]int, len(merged)+len(next))
	for i, value := range merged {
		byID[value.ID] = i
	}
	for _, value := range next {
		if index, ok := byID[value.ID]; ok {
			if value.Preview != "" {
				merged[index].Preview = value.Preview
			}
			if value.AddedAt.Before(merged[index].AddedAt) {
				merged[index].AddedAt = value.AddedAt
			}
			continue
		}
		byID[value.ID] = len(merged)
		merged = append(merged, value)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].AddedAt.Equal(merged[j].AddedAt) {
			return merged[i].ID < merged[j].ID
		}
		return merged[i].AddedAt.Before(merged[j].AddedAt)
	})
	return merged
}
