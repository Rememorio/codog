package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Scenario string

const (
	ScenarioTrustPromptUnresolved   Scenario = "trust_prompt_unresolved"
	ScenarioPromptDeliveredToShell  Scenario = "prompt_delivered_to_shell"
	ScenarioStaleBranch             Scenario = "stale_branch"
	ScenarioCompileRedAfterRefactor Scenario = "compile_red_after_refactor"
	ScenarioMCPHandshakeFailure     Scenario = "mcp_handshake_failure"
	ScenarioPartialPluginStartup    Scenario = "partial_plugin_startup"
	ScenarioProviderFailure         Scenario = "provider_failure"
)

type StepKind string

const (
	StepAcceptTrustPrompt StepKind = "accept_trust_prompt"
	StepRedirectPrompt    StepKind = "redirect_prompt_to_agent"
	StepMergeForward      StepKind = "merge_forward_branch"
	StepCleanBuild        StepKind = "clean_build"
	StepRetryMCPHandshake StepKind = "retry_mcp_handshake"
	StepRestartPlugin     StepKind = "restart_plugin"
	StepRestartWorker     StepKind = "restart_worker"
	StepEscalateToHuman   StepKind = "escalate_to_human"
)

type AttemptState string

const (
	StateQueued    AttemptState = "queued"
	StateRunning   AttemptState = "running"
	StateSucceeded AttemptState = "succeeded"
	StateFailed    AttemptState = "failed"
	StateExhausted AttemptState = "exhausted"
)

type ResultKind string

const (
	ResultRecovered          ResultKind = "recovered"
	ResultPartialRecovery    ResultKind = "partial_recovery"
	ResultEscalationRequired ResultKind = "escalation_required"
)

type EscalationPolicy string

const (
	EscalationAlertHuman     EscalationPolicy = "alert_human"
	EscalationLogAndContinue EscalationPolicy = "log_and_continue"
	EscalationAbort          EscalationPolicy = "abort"
)

type Step struct {
	Kind      StepKind       `json:"kind"`
	TimeoutMS int            `json:"timeout_ms,omitempty"`
	Name      string         `json:"name,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Recipe struct {
	ID               string           `json:"id"`
	Scenario         Scenario         `json:"scenario"`
	Steps            []Step           `json:"steps"`
	MaxAttempts      int              `json:"max_attempts"`
	EscalationPolicy EscalationPolicy `json:"escalation_policy"`
}

type CommandResult struct {
	Step   Step         `json:"step"`
	State  AttemptState `json:"state"`
	Result string       `json:"result"`
}

type Result struct {
	Kind       ResultKind `json:"kind"`
	StepsTaken int        `json:"steps_taken,omitempty"`
	Recovered  []Step     `json:"recovered,omitempty"`
	Remaining  []Step     `json:"remaining,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

type LedgerEntry struct {
	RecipeID           string          `json:"recipe_id"`
	AttemptType        string          `json:"attempt_type"`
	Trigger            Scenario        `json:"trigger"`
	AttemptCount       int             `json:"attempt_count"`
	RetryLimit         int             `json:"retry_limit"`
	AttemptsRemaining  int             `json:"attempts_remaining"`
	State              AttemptState    `json:"state"`
	StartedAt          *time.Time      `json:"started_at,omitempty"`
	FinishedAt         *time.Time      `json:"finished_at,omitempty"`
	CommandResults     []CommandResult `json:"command_results,omitempty"`
	Result             *Result         `json:"result,omitempty"`
	LastFailureSummary string          `json:"last_failure_summary,omitempty"`
	EscalationReason   string          `json:"escalation_reason,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type StatusReport struct {
	Scenario           Scenario     `json:"scenario"`
	Attempted          bool         `json:"attempted"`
	State              AttemptState `json:"state,omitempty"`
	AttemptCount       int          `json:"attempt_count"`
	RetryLimit         int          `json:"retry_limit,omitempty"`
	AttemptsRemaining  int          `json:"attempts_remaining,omitempty"`
	EscalationReason   string       `json:"escalation_reason,omitempty"`
	LastFailureSummary string       `json:"last_failure_summary,omitempty"`
}

type AttemptOptions struct {
	FailureSummary  string
	FailedStepIndex *int
	Now             time.Time
}

type AttemptReport struct {
	Kind   string      `json:"kind"`
	Recipe Recipe      `json:"recipe"`
	Entry  LedgerEntry `json:"entry"`
	Result Result      `json:"result"`
	Events []Event     `json:"events"`
}

type Event struct {
	Type      string       `json:"type"`
	Scenario  Scenario     `json:"scenario"`
	RecipeID  string       `json:"recipe_id"`
	State     AttemptState `json:"state,omitempty"`
	Result    *Result      `json:"result,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

type Store struct {
	ConfigHome string
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func AllScenarios() []Scenario {
	return []Scenario{
		ScenarioTrustPromptUnresolved,
		ScenarioPromptDeliveredToShell,
		ScenarioStaleBranch,
		ScenarioCompileRedAfterRefactor,
		ScenarioMCPHandshakeFailure,
		ScenarioPartialPluginStartup,
		ScenarioProviderFailure,
	}
}

func ParseScenario(value string) (Scenario, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	if value == "prompt_misdelivery" {
		value = string(ScenarioPromptDeliveredToShell)
	}
	if value == "compile_red_cross_crate" {
		value = string(ScenarioCompileRedAfterRefactor)
	}
	for _, scenario := range AllScenarios() {
		if value == string(scenario) {
			return scenario, nil
		}
	}
	return "", fmt.Errorf("unknown recovery scenario %q", value)
}

func ScenarioFromStartupClassification(classification string) (Scenario, bool) {
	switch strings.TrimSpace(strings.ToLower(classification)) {
	case "trust_required":
		return ScenarioTrustPromptUnresolved, true
	case "prompt_misdelivery", "prompt_acceptance_timeout":
		return ScenarioPromptDeliveredToShell, true
	case "transport_dead":
		return ScenarioMCPHandshakeFailure, true
	case "worker_crashed":
		return ScenarioProviderFailure, true
	default:
		return "", false
	}
}

func RecipeFor(scenario Scenario) (Recipe, error) {
	switch scenario {
	case ScenarioTrustPromptUnresolved:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepAcceptTrustPrompt}}, MaxAttempts: 1, EscalationPolicy: EscalationAlertHuman}, nil
	case ScenarioPromptDeliveredToShell:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepRedirectPrompt}}, MaxAttempts: 1, EscalationPolicy: EscalationAlertHuman}, nil
	case ScenarioStaleBranch:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepMergeForward}, {Kind: StepCleanBuild}}, MaxAttempts: 1, EscalationPolicy: EscalationAlertHuman}, nil
	case ScenarioCompileRedAfterRefactor:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepCleanBuild}}, MaxAttempts: 1, EscalationPolicy: EscalationAlertHuman}, nil
	case ScenarioMCPHandshakeFailure:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepRetryMCPHandshake, TimeoutMS: 5000}}, MaxAttempts: 1, EscalationPolicy: EscalationAbort}, nil
	case ScenarioPartialPluginStartup:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepRestartPlugin, Name: "stalled"}, {Kind: StepRetryMCPHandshake, TimeoutMS: 3000}}, MaxAttempts: 1, EscalationPolicy: EscalationLogAndContinue}, nil
	case ScenarioProviderFailure:
		return Recipe{ID: string(scenario), Scenario: scenario, Steps: []Step{{Kind: StepRestartWorker}}, MaxAttempts: 1, EscalationPolicy: EscalationAlertHuman}, nil
	default:
		return Recipe{}, fmt.Errorf("unknown recovery scenario %q", scenario)
	}
}

func (s Store) Attempt(scenario Scenario, opts AttemptOptions) (AttemptReport, error) {
	recipe, err := RecipeFor(scenario)
	if err != nil {
		return AttemptReport{}, err
	}
	ledger, err := s.load()
	if err != nil {
		return AttemptReport{}, err
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	entry := ledger[string(scenario)]
	if entry.RecipeID == "" {
		entry = LedgerEntry{
			RecipeID:          recipe.ID,
			AttemptType:       "automatic",
			Trigger:           scenario,
			RetryLimit:        recipe.MaxAttempts,
			AttemptsRemaining: recipe.MaxAttempts,
			State:             StateQueued,
			UpdatedAt:         now,
		}
	}
	if entry.AttemptCount >= recipe.MaxAttempts {
		result := Result{
			Kind:   ResultEscalationRequired,
			Reason: fmt.Sprintf("max recovery attempts (%d) exceeded for %s", recipe.MaxAttempts, scenario),
		}
		entry.AttemptsRemaining = 0
		entry.State = StateExhausted
		entry.FinishedAt = &now
		entry.Result = &result
		entry.LastFailureSummary = result.Reason
		entry.EscalationReason = result.Reason
		entry.UpdatedAt = now
		ledger[string(scenario)] = entry
		if err := s.save(ledger); err != nil {
			return AttemptReport{}, err
		}
		return AttemptReport{
			Kind:   "recovery_attempt",
			Recipe: recipe,
			Entry:  entry,
			Result: result,
			Events: []Event{
				{Type: "recovery.attempted", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: now},
				{Type: "recovery.escalated", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: now},
			},
		}, nil
	}

	entry.AttemptCount++
	entry.AttemptsRemaining = max(0, recipe.MaxAttempts-entry.AttemptCount)
	entry.State = StateRunning
	entry.StartedAt = &now
	entry.FinishedAt = nil
	entry.CommandResults = nil
	entry.Result = nil
	entry.LastFailureSummary = strings.TrimSpace(opts.FailureSummary)
	entry.EscalationReason = ""
	entry.UpdatedAt = now

	executed := []Step{}
	commandResults := []CommandResult{}
	failed := false
	for index, step := range recipe.Steps {
		if opts.FailedStepIndex != nil && *opts.FailedStepIndex == index {
			summary := firstNonEmpty(opts.FailureSummary, fmt.Sprintf("step %d failed for %s", index, scenario))
			commandResults = append(commandResults, CommandResult{Step: step, State: StateFailed, Result: summary})
			failed = true
			break
		}
		executed = append(executed, step)
		commandResults = append(commandResults, CommandResult{Step: step, State: StateSucceeded, Result: fmt.Sprintf("step %d succeeded for %s", index, scenario)})
	}

	result := Result{Kind: ResultRecovered, StepsTaken: len(recipe.Steps)}
	if failed {
		remaining := append([]Step(nil), recipe.Steps[len(executed):]...)
		if len(executed) == 0 {
			result = Result{Kind: ResultEscalationRequired, Reason: firstNonEmpty(opts.FailureSummary, fmt.Sprintf("recovery failed at first step for %s", scenario))}
		} else {
			result = Result{Kind: ResultPartialRecovery, Recovered: executed, Remaining: remaining}
		}
	}

	finishedAt := now.Add(time.Nanosecond)
	entry.FinishedAt = &finishedAt
	entry.CommandResults = commandResults
	entry.Result = &result
	switch result.Kind {
	case ResultRecovered:
		entry.State = StateSucceeded
	case ResultPartialRecovery:
		entry.State = StateFailed
		entry.LastFailureSummary = firstNonEmpty(entry.LastFailureSummary, fmt.Sprintf("%d step(s) remaining after partial recovery", len(result.Remaining)))
	case ResultEscalationRequired:
		entry.State = StateExhausted
		entry.LastFailureSummary = firstNonEmpty(entry.LastFailureSummary, result.Reason)
		entry.EscalationReason = result.Reason
	}
	entry.UpdatedAt = finishedAt
	ledger[string(scenario)] = entry
	if err := s.save(ledger); err != nil {
		return AttemptReport{}, err
	}
	events := []Event{{Type: "recovery.attempted", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: finishedAt}}
	switch result.Kind {
	case ResultRecovered:
		events = append(events, Event{Type: "recovery.succeeded", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: finishedAt})
	case ResultPartialRecovery:
		events = append(events, Event{Type: "recovery.failed", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: finishedAt})
	case ResultEscalationRequired:
		events = append(events, Event{Type: "recovery.escalated", Scenario: scenario, RecipeID: recipe.ID, State: entry.State, Result: &result, CreatedAt: finishedAt})
	}
	return AttemptReport{Kind: "recovery_attempt", Recipe: recipe, Entry: entry, Result: result, Events: events}, nil
}

func (s Store) Status(scenario Scenario) (StatusReport, error) {
	if _, err := RecipeFor(scenario); err != nil {
		return StatusReport{}, err
	}
	ledger, err := s.load()
	if err != nil {
		return StatusReport{}, err
	}
	entry, ok := ledger[string(scenario)]
	if !ok {
		recipe, _ := RecipeFor(scenario)
		return StatusReport{Scenario: scenario, RetryLimit: recipe.MaxAttempts, AttemptsRemaining: recipe.MaxAttempts}, nil
	}
	return statusFromEntry(entry), nil
}

func (s Store) List() ([]LedgerEntry, error) {
	ledger, err := s.load()
	if err != nil {
		return nil, err
	}
	entries := make([]LedgerEntry, 0, len(ledger))
	for _, entry := range ledger {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].RecipeID < entries[j].RecipeID
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
	return entries, nil
}

func statusFromEntry(entry LedgerEntry) StatusReport {
	return StatusReport{
		Scenario:           entry.Trigger,
		Attempted:          entry.AttemptCount > 0,
		State:              entry.State,
		AttemptCount:       entry.AttemptCount,
		RetryLimit:         entry.RetryLimit,
		AttemptsRemaining:  entry.AttemptsRemaining,
		EscalationReason:   entry.EscalationReason,
		LastFailureSummary: entry.LastFailureSummary,
	}
}

func (s Store) load() (map[string]LedgerEntry, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]LedgerEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ledger map[string]LedgerEntry
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, err
	}
	if ledger == nil {
		ledger = map[string]LedgerEntry{}
	}
	return ledger, nil
}

func (s Store) save(ledger map[string]LedgerEntry) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o644)
}

func (s Store) path() string {
	return filepath.Join(s.dir(), "ledger.json")
}

func (s Store) dir() string {
	configHome := strings.TrimSpace(s.ConfigHome)
	if configHome == "" {
		configHome = ".codog"
	}
	return filepath.Join(configHome, "recovery")
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
