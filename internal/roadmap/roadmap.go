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
	"strconv"
	"strings"
	"time"
)

// State is the lifecycle state for a roadmap pinpoint.
type State string

// Priority ranks roadmap pinpoints for implementation queues.
type Priority string

// Severity describes user or operator impact independently from queue order.
type Severity string

// ImpactClass identifies the kind of gap a pinpoint represents.
type ImpactClass string

// HandoffReadiness describes whether a pinpoint can become implementation work.
type HandoffReadiness string

const (
	StateFiled        State = "filed"
	StateAcknowledged State = "acknowledged"
	StateInProgress   State = "in_progress"
	StateBlocked      State = "blocked"
	StateDone         State = "done"
	StateSuperseded   State = "superseded"

	PriorityP0 Priority = "p0"
	PriorityP1 Priority = "p1"
	PriorityP2 Priority = "p2"
	PriorityP3 Priority = "p3"

	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"

	ImpactUserFacingBreakage ImpactClass = "user_facing_breakage"
	ImpactOperatorFriction   ImpactClass = "operator_friction"
	ImpactObservabilityDebt  ImpactClass = "observability_debt"
	ImpactLongTailHardening  ImpactClass = "long_tail_hardening"

	ReadinessImplementationReady HandoffReadiness = "implementation_ready"
	ReadinessNeedsRepro          HandoffReadiness = "needs_repro"
	ReadinessNeedsTriage         HandoffReadiness = "needs_triage"

	EvidenceRepro         EvidenceRole = "repro"
	EvidenceSymptom       EvidenceRole = "symptom"
	EvidenceRootCauseHint EvidenceRole = "root_cause_hint"
	EvidenceVerification  EvidenceRole = "verification"

	MaxEvidencePreviewRunes = 240
)

// PriorityReason records why a pinpoint received its current priority.
type PriorityReason struct {
	BlastRadius        string `json:"blast_radius,omitempty"`
	Reproducibility    string `json:"reproducibility,omitempty"`
	AutomationBreakage string `json:"automation_breakage,omitempty"`
	MergeRisk          string `json:"merge_risk,omitempty"`
	Rationale          string `json:"rationale,omitempty"`
}

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

// HandoffPacket is the executable context for starting implementation work.
type HandoffPacket struct {
	PinpointID            string            `json:"pinpoint_id"`
	Objective             string            `json:"objective"`
	SuspectedScope        []string          `json:"suspected_scope,omitempty"`
	EvidenceRefs          []string          `json:"evidence_refs,omitempty"`
	Priority              Priority          `json:"priority"`
	Severity              Severity          `json:"severity"`
	Impact                ImpactClass       `json:"impact"`
	SuggestedVerification []string          `json:"suggested_verification,omitempty"`
	Readiness             HandoffReadiness  `json:"readiness"`
	GeneratedAt           time.Time         `json:"generated_at"`
	Metadata              map[string]string `json:"metadata,omitempty"`
}

// ImplementationLink ties a pinpoint to spawned execution infrastructure.
type ImplementationLink struct {
	ID           string    `json:"id"`
	LaneID       string    `json:"lane_id,omitempty"`
	TaskID       string    `json:"task_id,omitempty"`
	WorktreeID   string    `json:"worktree_id,omitempty"`
	WorktreePath string    `json:"worktree_path,omitempty"`
	PRURL        string    `json:"pr_url,omitempty"`
	PRNumber     int       `json:"pr_number,omitempty"`
	Status       string    `json:"status,omitempty"`
	AddedAt      time.Time `json:"added_at"`
}

// ExecutionResult records later implementation progress back on the pinpoint.
type ExecutionResult struct {
	ID           string    `json:"id"`
	LinkID       string    `json:"link_id,omitempty"`
	LaneID       string    `json:"lane_id,omitempty"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary,omitempty"`
	EvidenceRefs []string  `json:"evidence_refs,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
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
	Priority          Priority             `json:"priority"`
	Severity          Severity             `json:"severity"`
	Impact            ImpactClass          `json:"impact"`
	PriorityReason    PriorityReason       `json:"priority_reason,omitempty"`
	PriorityUpdatedAt *time.Time           `json:"priority_updated_at,omitempty"`
	Handoff           *HandoffPacket       `json:"handoff,omitempty"`
	Implementation    []ImplementationLink `json:"implementation,omitempty"`
	ExecutionResults  []ExecutionResult    `json:"execution_results,omitempty"`
}

// Filing is a create or update request for a roadmap pinpoint.
type Filing struct {
	ID               string
	Title            string
	Description      string
	State            State
	Supersedes       []string
	SupersededBy     string
	Related          []string
	ReportID         string
	Evidence         []EvidenceAttachment
	Priority         Priority
	Severity         Severity
	Impact           ImpactClass
	PriorityReason   PriorityReason
	Handoff          *HandoffPacket
	Implementation   []ImplementationLink
	ExecutionResults []ExecutionResult
	Now              time.Time
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
	if err := validatePriorityFields(filing); err != nil {
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
		applyPriority(&item, filing)
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
		applyPriority(&item, filing)
	}
	ensurePriorityDefaults(&item, filing.Now)
	item.Supersedes = mergeStrings(item.Supersedes, filing.Supersedes)
	item.Related = mergeStrings(item.Related, filing.Related)
	evidence, err := normalizeEvidence(filing.Evidence, filing.Now)
	if err != nil {
		return Result{}, err
	}
	item.Evidence = mergeEvidence(item.Evidence, evidence)
	handoff, err := normalizeHandoff(filing.Handoff, filing.Now)
	if err != nil {
		return Result{}, err
	}
	if handoff != nil {
		item.Handoff = handoff
	}
	implementation, err := normalizeImplementationLinks(filing.Implementation, filing.Now)
	if err != nil {
		return Result{}, err
	}
	item.Implementation = mergeImplementationLinks(item.Implementation, implementation)
	results, err := normalizeExecutionResults(filing.ExecutionResults, filing.Now)
	if err != nil {
		return Result{}, err
	}
	item.ExecutionResults = mergeExecutionResults(item.ExecutionResults, results)
	applyExecutionResultState(&item, results, filing.Now)
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
	ensureHandoff(&item, filing.Now)
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
	ensurePriorityDefaults(&item, fallbackTime(item.UpdatedAt))
	ensureHandoff(&item, fallbackTime(item.UpdatedAt))
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
		ensurePriorityDefaults(&item, fallbackTime(item.UpdatedAt))
		ensureHandoff(&item, fallbackTime(item.UpdatedAt))
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := priorityRank(items[i].Priority)
		rightRank := priorityRank(items[j].Priority)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
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
	filing.Priority = Priority(strings.TrimSpace(string(filing.Priority)))
	filing.Severity = Severity(strings.TrimSpace(string(filing.Severity)))
	filing.Impact = ImpactClass(strings.TrimSpace(string(filing.Impact)))
	filing.PriorityReason = normalizePriorityReason(filing.PriorityReason)
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

func validatePriorityFields(filing Filing) error {
	if filing.Priority != "" {
		if err := validatePriority(filing.Priority); err != nil {
			return err
		}
	}
	if filing.Severity != "" {
		if err := validateSeverity(filing.Severity); err != nil {
			return err
		}
	}
	if filing.Impact != "" {
		if err := validateImpact(filing.Impact); err != nil {
			return err
		}
	}
	return nil
}

func validatePriority(priority Priority) error {
	switch priority {
	case PriorityP0, PriorityP1, PriorityP2, PriorityP3:
		return nil
	default:
		return errors.New("invalid roadmap priority")
	}
}

func validateSeverity(severity Severity) error {
	switch severity {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return nil
	default:
		return errors.New("invalid roadmap severity")
	}
}

func validateImpact(impact ImpactClass) error {
	switch impact {
	case ImpactUserFacingBreakage, ImpactOperatorFriction, ImpactObservabilityDebt, ImpactLongTailHardening:
		return nil
	default:
		return errors.New("invalid roadmap impact")
	}
}

func applyPriority(item *Item, filing Filing) {
	changed := false
	if filing.Priority != "" && item.Priority != filing.Priority {
		item.Priority = filing.Priority
		changed = true
	}
	if filing.Severity != "" && item.Severity != filing.Severity {
		item.Severity = filing.Severity
		changed = true
	}
	if filing.Impact != "" && item.Impact != filing.Impact {
		item.Impact = filing.Impact
		changed = true
	}
	if !isZeroPriorityReason(filing.PriorityReason) {
		item.PriorityReason = filing.PriorityReason
		changed = true
	}
	if changed {
		updatedAt := filing.Now
		item.PriorityUpdatedAt = &updatedAt
	}
}

func ensurePriorityDefaults(item *Item, now time.Time) {
	changed := false
	if item.Priority == "" {
		item.Priority = PriorityP2
		changed = true
	}
	if item.Severity == "" {
		item.Severity = SeverityMedium
		changed = true
	}
	if item.Impact == "" {
		item.Impact = ImpactLongTailHardening
		changed = true
	}
	if changed && item.PriorityUpdatedAt == nil {
		updatedAt := now
		item.PriorityUpdatedAt = &updatedAt
	}
}

func normalizePriorityReason(reason PriorityReason) PriorityReason {
	return PriorityReason{
		BlastRadius:        strings.TrimSpace(reason.BlastRadius),
		Reproducibility:    strings.TrimSpace(reason.Reproducibility),
		AutomationBreakage: strings.TrimSpace(reason.AutomationBreakage),
		MergeRisk:          strings.TrimSpace(reason.MergeRisk),
		Rationale:          strings.TrimSpace(reason.Rationale),
	}
}

func isZeroPriorityReason(reason PriorityReason) bool {
	return reason.BlastRadius == "" &&
		reason.Reproducibility == "" &&
		reason.AutomationBreakage == "" &&
		reason.MergeRisk == "" &&
		reason.Rationale == ""
}

func priorityRank(priority Priority) int {
	switch priority {
	case PriorityP0:
		return 0
	case PriorityP1:
		return 1
	case PriorityP2, "":
		return 2
	case PriorityP3:
		return 3
	default:
		return 4
	}
}

func fallbackTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}

func normalizeHandoff(packet *HandoffPacket, now time.Time) (*HandoffPacket, error) {
	if packet == nil {
		return nil, nil
	}
	normalized := *packet
	normalized.PinpointID = strings.TrimSpace(normalized.PinpointID)
	normalized.Objective = strings.TrimSpace(normalized.Objective)
	normalized.SuspectedScope = cleanStrings(normalized.SuspectedScope)
	normalized.EvidenceRefs = cleanStrings(normalized.EvidenceRefs)
	normalized.SuggestedVerification = cleanStrings(normalized.SuggestedVerification)
	normalized.Readiness = HandoffReadiness(strings.TrimSpace(string(normalized.Readiness)))
	if normalized.Readiness != "" {
		if err := validateReadiness(normalized.Readiness); err != nil {
			return nil, err
		}
	}
	if normalized.GeneratedAt.IsZero() {
		normalized.GeneratedAt = now
	} else {
		normalized.GeneratedAt = normalized.GeneratedAt.UTC()
	}
	normalized.Metadata = cleanStringMap(normalized.Metadata)
	return &normalized, nil
}

func validateReadiness(readiness HandoffReadiness) error {
	switch readiness {
	case ReadinessImplementationReady, ReadinessNeedsRepro, ReadinessNeedsTriage:
		return nil
	default:
		return errors.New("invalid roadmap handoff readiness")
	}
}

func ensureHandoff(item *Item, now time.Time) {
	if item.Handoff == nil {
		item.Handoff = &HandoffPacket{}
	}
	packet := item.Handoff
	packet.PinpointID = item.ID
	if packet.Objective == "" {
		packet.Objective = handoffObjective(*item)
	}
	if len(packet.SuspectedScope) == 0 {
		packet.SuspectedScope = []string{"workspace"}
	}
	packet.EvidenceRefs = evidenceRefs(item.Evidence)
	packet.Priority = item.Priority
	packet.Severity = item.Severity
	packet.Impact = item.Impact
	if len(packet.SuggestedVerification) == 0 {
		packet.SuggestedVerification = defaultVerificationPlan(*item)
	}
	if packet.Readiness == "" {
		packet.Readiness = inferReadiness(*item)
	}
	if packet.GeneratedAt.IsZero() {
		packet.GeneratedAt = now
	}
}

func handoffObjective(item Item) string {
	description := strings.TrimSpace(item.Description)
	if description == "" {
		return item.Title
	}
	return item.Title + ": " + description
}

func evidenceRefs(evidence []EvidenceAttachment) []string {
	refs := make([]string, 0, len(evidence))
	for _, value := range evidence {
		if value.ID != "" {
			refs = append(refs, value.ID)
			continue
		}
		if value.Reference != "" {
			refs = append(refs, value.Reference)
		}
	}
	return cleanStrings(refs)
}

func defaultVerificationPlan(item Item) []string {
	for _, evidence := range item.Evidence {
		if evidence.Role == EvidenceVerification && evidence.Preview != "" {
			return []string{evidence.Preview}
		}
	}
	return []string{"Run the focused tests or checks that exercise the referenced evidence."}
}

func inferReadiness(item Item) HandoffReadiness {
	if len(item.Evidence) == 0 {
		return ReadinessNeedsTriage
	}
	for _, evidence := range item.Evidence {
		if evidence.Role == EvidenceRepro || evidence.Role == EvidenceVerification {
			return ReadinessImplementationReady
		}
	}
	return ReadinessNeedsRepro
}

func normalizeImplementationLinks(values []ImplementationLink, now time.Time) ([]ImplementationLink, error) {
	out := make([]ImplementationLink, 0, len(values))
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.LaneID = strings.TrimSpace(value.LaneID)
		value.TaskID = strings.TrimSpace(value.TaskID)
		value.WorktreeID = strings.TrimSpace(value.WorktreeID)
		value.WorktreePath = strings.TrimSpace(value.WorktreePath)
		value.PRURL = strings.TrimSpace(value.PRURL)
		value.Status = strings.TrimSpace(value.Status)
		if value.ID == "" && value.LaneID == "" && value.TaskID == "" && value.WorktreeID == "" && value.WorktreePath == "" && value.PRURL == "" && value.PRNumber == 0 {
			continue
		}
		if value.PRNumber < 0 {
			return nil, errors.New("roadmap implementation pr_number must be non-negative")
		}
		if value.AddedAt.IsZero() {
			value.AddedAt = now
		} else {
			value.AddedAt = value.AddedAt.UTC()
		}
		if value.ID == "" {
			value.ID = implementationLinkID(value)
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

func implementationLinkID(value ImplementationLink) string {
	key := strings.Join([]string{value.LaneID, value.TaskID, value.WorktreeID, value.WorktreePath, value.PRURL, strconv.Itoa(value.PRNumber)}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return "impl-" + hex.EncodeToString(sum[:6])
}

func normalizeExecutionResults(values []ExecutionResult, now time.Time) ([]ExecutionResult, error) {
	out := make([]ExecutionResult, 0, len(values))
	for _, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.LinkID = strings.TrimSpace(value.LinkID)
		value.LaneID = strings.TrimSpace(value.LaneID)
		value.Status = strings.TrimSpace(value.Status)
		value.Summary = strings.TrimSpace(value.Summary)
		value.EvidenceRefs = cleanStrings(value.EvidenceRefs)
		if value.ID == "" && value.LinkID == "" && value.LaneID == "" && value.Status == "" && value.Summary == "" && len(value.EvidenceRefs) == 0 {
			continue
		}
		if value.Status == "" {
			return nil, errors.New("roadmap execution result status is required")
		}
		if value.RecordedAt.IsZero() {
			value.RecordedAt = now
		} else {
			value.RecordedAt = value.RecordedAt.UTC()
		}
		if value.ID == "" {
			value.ID = executionResultID(value)
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].RecordedAt.Before(out[j].RecordedAt)
	})
	return out, nil
}

func executionResultID(value ExecutionResult) string {
	key := strings.Join([]string{value.LinkID, value.LaneID, value.Status, value.Summary, strings.Join(value.EvidenceRefs, "\x00")}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return "exec-" + hex.EncodeToString(sum[:6])
}

func applyExecutionResultState(item *Item, results []ExecutionResult, now time.Time) {
	if item.State == StateSuperseded || len(results) == 0 {
		return
	}
	for _, result := range results {
		switch strings.ToLower(result.Status) {
		case "done", "passed", "completed", "merged":
			item.State = StateDone
			item.LastStateChangeAt = now
		case "blocked", "failed":
			item.State = StateBlocked
			item.LastStateChangeAt = now
		case "started", "running", "in_progress":
			if item.State != StateDone {
				item.State = StateInProgress
				item.LastStateChangeAt = now
			}
		}
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

func cleanStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
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

func mergeImplementationLinks(existing []ImplementationLink, next []ImplementationLink) []ImplementationLink {
	if len(next) == 0 {
		return existing
	}
	merged := append([]ImplementationLink(nil), existing...)
	byID := make(map[string]int, len(merged)+len(next))
	for i, value := range merged {
		byID[value.ID] = i
	}
	for _, value := range next {
		if index, ok := byID[value.ID]; ok {
			if value.Status != "" {
				merged[index].Status = value.Status
			}
			if value.PRURL != "" {
				merged[index].PRURL = value.PRURL
			}
			if value.PRNumber != 0 {
				merged[index].PRNumber = value.PRNumber
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

func mergeExecutionResults(existing []ExecutionResult, next []ExecutionResult) []ExecutionResult {
	if len(next) == 0 {
		return existing
	}
	merged := append([]ExecutionResult(nil), existing...)
	byID := make(map[string]int, len(merged)+len(next))
	for i, value := range merged {
		byID[value.ID] = i
	}
	for _, value := range next {
		if index, ok := byID[value.ID]; ok {
			merged[index] = value
			continue
		}
		byID[value.ID] = len(merged)
		merged = append(merged, value)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].RecordedAt.Equal(merged[j].RecordedAt) {
			return merged[i].ID < merged[j].ID
		}
		return merged[i].RecordedAt.Before(merged[j].RecordedAt)
	})
	return merged
}
