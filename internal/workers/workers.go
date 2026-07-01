package workers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/laneevents"
)

type TaskReceipt struct {
	Repo              string   `json:"repo"`
	TaskKind          string   `json:"task_kind"`
	SourceSurface     string   `json:"source_surface"`
	ExpectedArtifacts []string `json:"expected_artifacts,omitempty"`
	ObjectivePreview  string   `json:"objective_preview"`
}

type Event = laneevents.Event

const (
	StartupTrustRequired            = "trust_required"
	StartupPromptMisdelivery        = "prompt_misdelivery"
	StartupPromptAcceptanceTimeout  = "prompt_acceptance_timeout"
	StartupTransportDead            = "transport_dead"
	StartupWorkerCrashed            = "worker_crashed"
	StartupUnknown                  = "unknown"
	startupNoEvidenceEventType      = "worker.startup_no_evidence"
	startupPromptAcceptanceAccepted = "accepted"
)

type StartupEvidence struct {
	LastLifecycleState    string     `json:"last_lifecycle_state"`
	LastLifecycleAt       time.Time  `json:"last_lifecycle_at"`
	PaneCommand           string     `json:"pane_command,omitempty"`
	PaneObservedAt        time.Time  `json:"pane_observed_at"`
	CommandStartedAt      time.Time  `json:"command_started_at"`
	PromptSentAt          *time.Time `json:"prompt_sent_at,omitempty"`
	PromptAcceptanceState string     `json:"prompt_acceptance_state"`
	TrustPromptDetected   bool       `json:"trust_prompt_detected"`
	TransportHealthy      *bool      `json:"transport_healthy,omitempty"`
	TransportHealth       string     `json:"transport_health"`
	MCPHealthy            *bool      `json:"mcp_healthy,omitempty"`
	MCPHealth             string     `json:"mcp_health"`
	ElapsedSeconds        int64      `json:"elapsed_seconds"`
}

type StartupNoEvidenceReport struct {
	Kind           string          `json:"kind"`
	Classification string          `json:"classification"`
	Evidence       StartupEvidence `json:"evidence"`
	CreatedAt      time.Time       `json:"created_at"`
}

type TerminalOutcome struct {
	Fingerprint             string    `json:"fingerprint"`
	LaneEvent               string    `json:"lane_event"`
	Type                    string    `json:"type"`
	Status                  string    `json:"status"`
	FinishReason            string    `json:"finish_reason,omitempty"`
	Sequence                int64     `json:"sequence"`
	DuplicateCount          int       `json:"duplicate_count"`
	MaterialDifferenceCount int       `json:"material_difference_count"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type Worker struct {
	ID                           string                   `json:"worker_id"`
	CWD                          string                   `json:"cwd"`
	TrustedRoots                 []string                 `json:"trusted_roots,omitempty"`
	AutoRecoverPromptMisdelivery bool                     `json:"auto_recover_prompt_misdelivery"`
	Status                       string                   `json:"status"`
	ReadyForPrompt               bool                     `json:"ready_for_prompt"`
	TrustResolved                bool                     `json:"trust_resolved"`
	TaskID                       string                   `json:"task_id,omitempty"`
	TaskStatus                   string                   `json:"task_status,omitempty"`
	TaskReceipt                  *TaskReceipt             `json:"task_receipt,omitempty"`
	LastError                    string                   `json:"last_error,omitempty"`
	StartupNoEvidence            *StartupNoEvidenceReport `json:"startup_no_evidence,omitempty"`
	Events                       []Event                  `json:"events,omitempty"`
	Terminal                     *TerminalOutcome         `json:"terminal,omitempty"`
	CreatedAt                    time.Time                `json:"created_at"`
	UpdatedAt                    time.Time                `json:"updated_at"`
}

type ReadySnapshot struct {
	WorkerID       string `json:"worker_id"`
	Status         string `json:"status"`
	ReadyForPrompt bool   `json:"ready_for_prompt"`
	TaskID         string `json:"task_id,omitempty"`
	TaskStatus     string `json:"task_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

type Store struct {
	ConfigHome string
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func (s Store) Create(cwd string, trustedRoots []string, autoRecover bool) (Worker, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return Worker{}, errors.New("cwd is required")
	}
	now := time.Now().UTC()
	worker := Worker{
		ID:                           newID(now),
		CWD:                          cwd,
		TrustedRoots:                 normalizeStrings(trustedRoots),
		AutoRecoverPromptMisdelivery: autoRecover,
		Status:                       "ready_for_prompt",
		ReadyForPrompt:               true,
		TrustResolved:                true,
		CreatedAt:                    now,
		UpdatedAt:                    now,
	}
	appendEvent(&worker, Event{Type: "created", Message: "worker ready for prompt", CreatedAt: now})
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	return worker, nil
}

func (s Store) Get(id string) (Worker, error) {
	if err := validateID(id); err != nil {
		return Worker{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir(), id+".json"))
	if err != nil {
		return Worker{}, err
	}
	var worker Worker
	if err := json.Unmarshal(data, &worker); err != nil {
		return Worker{}, err
	}
	return worker, nil
}

func (s Store) List() ([]Worker, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}
	out := []Worker{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateID(id); err != nil {
			continue
		}
		worker, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, worker)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s Store) Save(worker Worker) error {
	if err := validateID(worker.ID); err != nil {
		return err
	}
	worker.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(worker, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir(), worker.ID+".json"), data, 0o644)
}

func (s Store) Observe(id string, screenText string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	screenText = strings.TrimSpace(screenText)
	eventType := "observed"
	if strings.Contains(strings.ToLower(screenText), "trust") {
		worker.TrustResolved = false
		worker.ReadyForPrompt = false
		worker.Status = "trust_prompt"
		eventType = "trust_prompt"
	} else if strings.Contains(strings.ToLower(screenText), "ready") || strings.Contains(strings.ToLower(screenText), "prompt") {
		worker.TrustResolved = true
		worker.ReadyForPrompt = true
		worker.Status = "ready_for_prompt"
		eventType = "ready"
	}
	appendEvent(&worker, Event{Type: eventType, Message: screenText, CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) ResolveTrust(id string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.TrustResolved = true
	worker.ReadyForPrompt = true
	worker.Status = "ready_for_prompt"
	appendEvent(&worker, Event{Type: "trust_resolved", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) AwaitReady(id string) (ReadySnapshot, error) {
	worker, err := s.Get(id)
	if err != nil {
		return ReadySnapshot{}, err
	}
	return ReadySnapshot{WorkerID: worker.ID, Status: worker.Status, ReadyForPrompt: worker.ReadyForPrompt, TaskID: worker.TaskID, TaskStatus: worker.TaskStatus, LastError: worker.LastError}, nil
}

func (s Store) SendPrompt(id string, prompt string, receipt *TaskReceipt, taskID string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	if !worker.ReadyForPrompt {
		return Worker{}, fmt.Errorf("worker %s is not ready for prompt", id)
	}
	worker.Status = "running"
	worker.ReadyForPrompt = false
	worker.TaskID = strings.TrimSpace(taskID)
	if worker.TaskID != "" {
		worker.TaskStatus = "running"
	}
	worker.TaskReceipt = receipt
	appendEvent(&worker, Event{Type: "prompt_sent", Message: strings.TrimSpace(prompt), CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Restart(id string, taskID string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.Status = "running"
	worker.ReadyForPrompt = false
	worker.TaskID = strings.TrimSpace(taskID)
	if worker.TaskID != "" {
		worker.TaskStatus = "running"
	}
	worker.LastError = ""
	appendEvent(&worker, Event{Type: "restarted", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Terminate(id string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.Status = "terminated"
	worker.ReadyForPrompt = false
	if worker.TaskID != "" {
		worker.TaskStatus = "stopped"
	}
	appendEvent(&worker, Event{Type: "terminated", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Complete(id string, finishReason string, tokensOutput int64) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	finishReason = strings.TrimSpace(finishReason)
	if finishReason == "" {
		return Worker{}, errors.New("finish_reason is required")
	}
	worker.Status = "finished"
	if !strings.EqualFold(finishReason, "stop") && !strings.EqualFold(finishReason, "end_turn") && !strings.EqualFold(finishReason, "finished") {
		worker.Status = "failed"
		worker.LastError = finishReason
	}
	worker.ReadyForPrompt = false
	appendEvent(&worker, Event{Type: "completed", FinishReason: finishReason, TokensOutput: tokensOutput, CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) ObserveStartupTimeout(id string, evidence StartupEvidence) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	now := time.Now().UTC()
	evidence = normalizeStartupEvidence(worker, evidence, now)
	classification := ClassifyStartupNoEvidence(evidence)
	report := &StartupNoEvidenceReport{
		Kind:           startupNoEvidenceEventType,
		Classification: classification,
		Evidence:       evidence,
		CreatedAt:      now,
	}

	worker.Status = "failed"
	worker.ReadyForPrompt = false
	if worker.TaskID != "" {
		worker.TaskStatus = "failed"
	}
	worker.LastError = "startup_no_evidence: " + classification
	worker.StartupNoEvidence = report
	appendEvent(&worker, Event{
		Type:           startupNoEvidenceEventType,
		Message:        "startup timeout classified as " + classification,
		Classification: classification,
		Evidence:       startupEvidenceMap(evidence),
		CreatedAt:      now,
		Provenance: laneevents.Provenance{
			Source:      laneevents.ProvenanceHealthcheck,
			Environment: "local",
			Emitter:     "codog-worker",
			Confidence:  1,
		},
	})
	return worker, s.Save(worker)
}

func ClassifyStartupNoEvidence(evidence StartupEvidence) string {
	state := normalizeState(evidence.LastLifecycleState)
	acceptance := normalizeState(evidence.PromptAcceptanceState)
	if boolPointerValue(evidence.TransportHealthy) == "false" || healthLooksDead(evidence.TransportHealth) {
		return StartupTransportDead
	}
	if evidence.TrustPromptDetected || strings.Contains(state, "trust") || strings.Contains(acceptance, "trust") {
		return StartupTrustRequired
	}
	if evidence.PromptSentAt != nil && state == "running" && !acceptanceLooksAccepted(acceptance) {
		return StartupPromptAcceptanceTimeout
	}
	if strings.Contains(acceptance, "misdeliver") || strings.Contains(acceptance, "wrong_pane") || strings.Contains(acceptance, "wrong-pane") || strings.Contains(acceptance, "shell") {
		return StartupPromptMisdelivery
	}
	if evidence.PromptSentAt != nil && !acceptanceLooksAccepted(acceptance) && (state == "ready_for_prompt" || evidence.ElapsedSeconds > 30) {
		return StartupPromptMisdelivery
	}
	if workerLooksCrashed(state) || (boolPointerValue(evidence.TransportHealthy) != "false" && (boolPointerValue(evidence.MCPHealthy) == "false" || healthLooksDead(evidence.MCPHealth))) {
		return StartupWorkerCrashed
	}
	if evidence.PromptSentAt != nil && !acceptanceLooksAccepted(acceptance) && (strings.Contains(acceptance, "pending") || strings.Contains(acceptance, "timeout") || strings.Contains(acceptance, "not_accepted")) {
		return StartupPromptAcceptanceTimeout
	}
	return StartupUnknown
}

func normalizeStartupEvidence(worker Worker, evidence StartupEvidence, now time.Time) StartupEvidence {
	evidence.LastLifecycleState = strings.TrimSpace(evidence.LastLifecycleState)
	if evidence.LastLifecycleState == "" {
		evidence.LastLifecycleState = worker.Status
	}
	if evidence.LastLifecycleAt.IsZero() {
		evidence.LastLifecycleAt = worker.UpdatedAt
	}
	if evidence.LastLifecycleAt.IsZero() {
		evidence.LastLifecycleAt = worker.CreatedAt
	}
	evidence.PaneCommand = strings.TrimSpace(evidence.PaneCommand)
	if evidence.PaneObservedAt.IsZero() {
		evidence.PaneObservedAt = now
	}
	if evidence.CommandStartedAt.IsZero() {
		evidence.CommandStartedAt = worker.CreatedAt
	}
	if evidence.PromptSentAt == nil {
		evidence.PromptSentAt = lastPromptSentAt(worker.Events)
	}
	evidence.PromptAcceptanceState = strings.TrimSpace(evidence.PromptAcceptanceState)
	if evidence.PromptAcceptanceState == "" {
		evidence.PromptAcceptanceState = inferPromptAcceptanceState(worker, evidence)
	}
	if !evidence.TrustPromptDetected {
		evidence.TrustPromptDetected = trustPromptDetected(worker)
	}
	evidence.TransportHealth = normalizeHealthSummary("transport", evidence.TransportHealth, evidence.TransportHealthy)
	evidence.MCPHealth = normalizeHealthSummary("mcp", evidence.MCPHealth, evidence.MCPHealthy)
	if evidence.ElapsedSeconds <= 0 && !evidence.CommandStartedAt.IsZero() {
		evidence.ElapsedSeconds = int64(evidence.PaneObservedAt.Sub(evidence.CommandStartedAt).Seconds())
		if evidence.ElapsedSeconds < 0 {
			evidence.ElapsedSeconds = 0
		}
	}
	return evidence
}

func lastPromptSentAt(events []Event) *time.Time {
	for index := len(events) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(events[index].Type), "prompt_sent") {
			sentAt := events[index].CreatedAt
			if sentAt.IsZero() {
				return nil
			}
			return &sentAt
		}
	}
	return nil
}

func inferPromptAcceptanceState(worker Worker, evidence StartupEvidence) string {
	if worker.ReadyForPrompt && evidence.PromptSentAt == nil {
		return "not_sent"
	}
	if strings.EqualFold(worker.Status, "running") && evidence.PromptSentAt != nil {
		return "pending"
	}
	if worker.ReadyForPrompt && evidence.PromptSentAt != nil {
		return "not_accepted"
	}
	return "unknown"
}

func trustPromptDetected(worker Worker) bool {
	if strings.Contains(strings.ToLower(worker.Status), "trust") {
		return true
	}
	for _, event := range worker.Events {
		if strings.Contains(strings.ToLower(event.Type), "trust") || strings.Contains(strings.ToLower(event.Message), "trust") {
			return true
		}
	}
	return false
}

func normalizeHealthSummary(name string, value string, healthy *bool) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if healthy == nil {
		return name + ":not_checked"
	}
	if *healthy {
		return name + ":healthy"
	}
	return name + ":unhealthy"
}

func startupEvidenceMap(evidence StartupEvidence) map[string]any {
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func normalizeState(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func boolPointerValue(value *bool) string {
	if value == nil {
		return "unknown"
	}
	if *value {
		return "true"
	}
	return "false"
}

func healthLooksDead(value string) bool {
	value = normalizeState(value)
	return strings.Contains(value, "dead") ||
		strings.Contains(value, "down") ||
		strings.Contains(value, "lost") ||
		strings.Contains(value, "disconnect") ||
		strings.Contains(value, "unreachable") ||
		strings.Contains(value, "failed") ||
		strings.Contains(value, "unhealthy")
}

func acceptanceLooksAccepted(value string) bool {
	value = normalizeState(value)
	return value == startupPromptAcceptanceAccepted || value == "true" || value == "ok" || value == "observed"
}

func workerLooksCrashed(state string) bool {
	return strings.Contains(state, "crash") ||
		strings.Contains(state, "panic") ||
		strings.Contains(state, "exited") ||
		strings.Contains(state, "terminated") ||
		strings.Contains(state, "failed")
}

func appendEvent(worker *Worker, event Event) {
	if worker == nil {
		return
	}
	event.Sequence = nextSequence(worker.Events)
	event.LaneID = worker.ID
	event.TaskID = worker.TaskID
	event.Status = worker.Status
	event.Provenance = laneevents.NormalizeProvenance(event.Provenance)
	if event.Provenance.Emitter == "codog" {
		event.Provenance.Emitter = "codog-worker"
	}
	if worker.TaskReceipt != nil {
		event.Binding.Scope = firstNonEmpty(worker.TaskReceipt.TaskKind, worker.TaskReceipt.SourceSurface)
		event.Binding.Owner = strings.TrimSpace(worker.TaskReceipt.Repo)
		event.Binding.WatcherAction = "act"
	} else {
		event.Binding.Scope = "worker"
		event.Binding.WatcherAction = "observe"
	}
	projected := laneevents.Reconcile(append(worker.Events, event))
	worker.Events = projected.Events
	worker.Terminal = terminalOutcome(projected)
}

func terminalOutcome(projected laneevents.Projection) *TerminalOutcome {
	if projected.ActionableTerminal == nil {
		return nil
	}
	event := *projected.ActionableTerminal
	return &TerminalOutcome{
		Fingerprint:             event.Fingerprint,
		LaneEvent:               event.LaneEvent,
		Type:                    event.Type,
		Status:                  event.Status,
		FinishReason:            event.FinishReason,
		Sequence:                event.Sequence,
		DuplicateCount:          len(projected.DuplicateTerminals),
		MaterialDifferenceCount: len(projected.MateriallyDifferentTerminals),
		UpdatedAt:               event.CreatedAt,
	}
}

func nextSequence(events []Event) int64 {
	var maxSequence int64
	for _, event := range events {
		if event.Sequence > maxSequence {
			maxSequence = event.Sequence
		}
	}
	return maxSequence + 1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s Store) dir() string {
	configHome := strings.TrimSpace(s.ConfigHome)
	if configHome == "" {
		configHome = ".codog"
	}
	return filepath.Join(configHome, "workers")
}

func normalizeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func newID(now time.Time) string {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("worker_%d", now.UnixNano())
	}
	return fmt.Sprintf("worker_%d_%s", now.Unix(), hex.EncodeToString(bytes[:]))
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("worker_id is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid worker_id %q", id)
	}
	return nil
}
