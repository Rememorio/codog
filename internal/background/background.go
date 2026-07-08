package background

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Task struct {
	ID              string           `json:"id"`
	Kind            string           `json:"kind,omitempty"`
	AgentType       string           `json:"agent_type,omitempty"`
	Command         string           `json:"command"`
	Prompt          string           `json:"prompt,omitempty"`
	Description     string           `json:"description,omitempty"`
	TaskPacket      json.RawMessage  `json:"task_packet,omitempty"`
	Workspace       string           `json:"workspace,omitempty"`
	SessionID       string           `json:"session_id,omitempty"`
	RestartPolicy   *RestartPolicy   `json:"restart_policy,omitempty"`
	RestartCount    int              `json:"restart_count,omitempty"`
	PID             int              `json:"pid"`
	Status          string           `json:"status"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     *time.Time       `json:"completed_at,omitempty"`
	ExitCode        *int             `json:"exit_code,omitempty"`
	LogPath         string           `json:"log_path"`
	Error           string           `json:"error,omitempty"`
	RestartedFrom   string           `json:"restarted_from,omitempty"`
	RestartedBy     string           `json:"restarted_by,omitempty"`
	Messages        []TaskMessage    `json:"messages,omitempty"`
	Heartbeat       *LaneHeartbeat   `json:"heartbeat,omitempty"`
	ScopeBinding    ScopeBinding     `json:"scope_binding"`
	TerminalEvents  []TerminalEvent  `json:"terminal_events,omitempty"`
	TerminalOutcome *TerminalOutcome `json:"terminal_outcome,omitempty"`
}

type TaskMessage struct {
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// ScopeBinding declares ownership and watcher responsibility for a lane.
type ScopeBinding struct {
	Owner         string `json:"owner,omitempty"`
	WorkflowScope string `json:"workflow_scope"`
	WatcherAction string `json:"watcher_action"`
	Actionable    bool   `json:"actionable"`
}

// TerminalEvent records one raw terminal notification for audit and dedupe.
type TerminalEvent struct {
	Sequence            int             `json:"sequence"`
	ObservedAt          time.Time       `json:"observed_at"`
	Status              string          `json:"status"`
	ExitCode            *int            `json:"exit_code,omitempty"`
	Error               string          `json:"error,omitempty"`
	Fingerprint         string          `json:"fingerprint"`
	Duplicate           bool            `json:"duplicate,omitempty"`
	DuplicateOf         string          `json:"duplicate_of,omitempty"`
	MateriallyDifferent bool            `json:"materially_different,omitempty"`
	Actionable          bool            `json:"actionable"`
	Provenance          EventProvenance `json:"provenance"`
}

// TerminalOutcome is the single actionable terminal outcome for a lane.
type TerminalOutcome struct {
	Fingerprint         string    `json:"fingerprint"`
	Status              string    `json:"status"`
	ExitCode            *int      `json:"exit_code,omitempty"`
	Error               string    `json:"error,omitempty"`
	ObservedAt          time.Time `json:"observed_at"`
	EventCount          int       `json:"event_count"`
	DuplicateCount      int       `json:"duplicate_count,omitempty"`
	ConflictCount       int       `json:"conflict_count,omitempty"`
	MateriallyDifferent bool      `json:"materially_different,omitempty"`
	Actionable          bool      `json:"actionable"`
}

type LaneFreshness string

const (
	LaneFreshnessHealthy       LaneFreshness = "healthy"
	LaneFreshnessStalled       LaneFreshness = "stalled"
	LaneFreshnessTransportDead LaneFreshness = "transport_dead"
	LaneFreshnessUnknown       LaneFreshness = "unknown"
)

type LaneHeartbeat struct {
	ObservedAt     time.Time       `json:"observed_at"`
	TransportAlive bool            `json:"transport_alive"`
	Status         string          `json:"status,omitempty"`
	Provenance     EventProvenance `json:"provenance"`
}

// EventProvenance labels the source and trust level of a lane event.
type EventProvenance struct {
	SourceKind  string `json:"source_kind"`
	Environment string `json:"environment"`
	Channel     string `json:"channel"`
	Emitter     string `json:"emitter"`
	Confidence  string `json:"confidence"`
}

// LifecycleResolution describes the canonical lifecycle state inferred from
// task status and transport freshness.
type LifecycleResolution struct {
	Status               string `json:"status"`
	Terminal             bool   `json:"terminal"`
	TerminalStateUnknown bool   `json:"terminal_state_unknown,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

type LaneBoardEntry struct {
	TaskID          string              `json:"task_id"`
	Prompt          string              `json:"prompt,omitempty"`
	Command         string              `json:"command,omitempty"`
	Kind            string              `json:"kind,omitempty"`
	SessionID       string              `json:"session_id,omitempty"`
	Status          string              `json:"status"`
	Heartbeat       *LaneHeartbeat      `json:"heartbeat,omitempty"`
	Freshness       LaneFreshness       `json:"freshness"`
	Provenance      EventProvenance     `json:"provenance"`
	ScopeBinding    ScopeBinding        `json:"scope_binding"`
	Lifecycle       LifecycleResolution `json:"lifecycle"`
	TerminalOutcome *TerminalOutcome    `json:"terminal_outcome,omitempty"`
}

type LaneBoard struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Active      []LaneBoardEntry `json:"active"`
	Blocked     []LaneBoardEntry `json:"blocked"`
	Finished    []LaneBoardEntry `json:"finished"`
}

type WatchEvent struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Offset       int64           `json:"offset,omitempty"`
	Data         string          `json:"data,omitempty"`
	Status       string          `json:"status,omitempty"`
	Error        string          `json:"error,omitempty"`
	Task         *Task           `json:"task,omitempty"`
	Provenance   EventProvenance `json:"provenance"`
	ScopeBinding ScopeBinding    `json:"scope_binding"`
}

type WatchOptions struct {
	Offset    int64
	Interval  time.Duration
	MaxEvents int
}

type RunOptions struct {
	Kind          string
	AgentType     string
	SessionID     string
	RestartedFrom string
	RestartPolicy *RestartPolicy
	RestartCount  int
	Env           []string
	Prompt        string
	Description   string
	TaskPacket    json.RawMessage
	ScopeBinding  ScopeBinding
}

type RestartPolicy struct {
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode,omitempty"`
	MaxAttempts  int    `json:"max_attempts,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"`
}

type PruneOptions struct {
	OlderThan time.Duration
	Keep      int
}

type SuperviseResult struct {
	Restarted []Task          `json:"restarted"`
	Skipped   []SuperviseSkip `json:"skipped,omitempty"`
}

type SuperviseSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type PruneResult struct {
	Removed      []string `json:"removed"`
	RemovedCount int      `json:"removed_count"`
	Kept         int      `json:"kept"`
}

type Store struct {
	Dir string
}

func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "background")}
}

func DefaultPruneOptions() PruneOptions {
	return PruneOptions{OlderThan: 30 * 24 * time.Hour, Keep: 100}
}

func FilterBySession(tasks []Task, sessionID string) []Task {
	if sessionID == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.SessionID == sessionID {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func FilterByKind(tasks []Task, kind string) []Task {
	if kind == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Kind == kind {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func (s Store) Run(command string, cwd string) (Task, error) {
	return s.RunWithOptions(command, cwd, RunOptions{})
}

func (s Store) RunWithOptions(command string, cwd string, options RunOptions) (Task, error) {
	return s.run(command, cwd, options)
}

func (s Store) Restart(id string, cwd string) (Task, error) {
	task, err := s.Status(id)
	if err != nil {
		return Task{}, err
	}
	source := task
	if task.Status == "running" {
		stopped, err := s.Stop(id)
		if err != nil {
			return Task{}, err
		}
		source = stopped
	}
	workspace := task.Workspace
	if workspace == "" {
		workspace = cwd
	}
	restarted, err := s.run(task.Command, workspace, RunOptions{
		Kind:          task.Kind,
		AgentType:     task.AgentType,
		SessionID:     task.SessionID,
		RestartedFrom: task.ID,
		RestartPolicy: task.RestartPolicy,
		RestartCount:  task.RestartCount,
		Prompt:        task.Prompt,
		Description:   task.Description,
		TaskPacket:    task.TaskPacket,
		ScopeBinding:  task.ScopeBinding,
	})
	if err != nil {
		return Task{}, err
	}
	source.RestartedBy = restarted.ID
	if err := s.save(source); err != nil {
		return Task{}, err
	}
	return restarted, nil
}

func (s Store) Prune(options PruneOptions) (PruneResult, error) {
	tasks, err := s.List()
	if err != nil {
		return PruneResult{}, err
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return taskRetentionTime(tasks[i]).After(taskRetentionTime(tasks[j]))
	})
	cutoff := time.Time{}
	if options.OlderThan > 0 {
		cutoff = time.Now().UTC().Add(-options.OlderThan)
	}
	seenNonRunning := 0
	result := PruneResult{}
	for _, task := range tasks {
		if task.Status == "running" {
			result.Kept++
			continue
		}
		seenNonRunning++
		if options.Keep > 0 && seenNonRunning <= options.Keep {
			result.Kept++
			continue
		}
		if !cutoff.IsZero() && taskRetentionTime(task).After(cutoff) {
			result.Kept++
			continue
		}
		if err := s.remove(task); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, task.ID)
	}
	result.RemovedCount = len(result.Removed)
	return result, nil
}

func (s Store) SuperviseOnce(now time.Time) (SuperviseResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tasks, err := s.List()
	if err != nil {
		return SuperviseResult{}, err
	}
	result := SuperviseResult{}
	for _, task := range tasks {
		policy, err := normalizeRestartPolicy(task.RestartPolicy)
		if err != nil {
			result.Skipped = append(result.Skipped, SuperviseSkip{ID: task.ID, Reason: "policy"})
			continue
		}
		if policy == nil || !policy.Enabled {
			continue
		}
		if task.Status == "running" {
			continue
		}
		if task.RestartedBy != "" {
			result.Skipped = append(result.Skipped, SuperviseSkip{ID: task.ID, Reason: "restarted"})
			continue
		}
		if !shouldRestart(task, *policy) {
			result.Skipped = append(result.Skipped, SuperviseSkip{ID: task.ID, Reason: "status"})
			continue
		}
		if policy.MaxAttempts > 0 && task.RestartCount >= policy.MaxAttempts {
			result.Skipped = append(result.Skipped, SuperviseSkip{ID: task.ID, Reason: "max_attempts"})
			continue
		}
		if task.CompletedAt != nil && policy.DelaySeconds > 0 {
			next := task.CompletedAt.Add(time.Duration(policy.DelaySeconds) * time.Second)
			if now.Before(next) {
				result.Skipped = append(result.Skipped, SuperviseSkip{ID: task.ID, Reason: "delay"})
				continue
			}
		}
		restarted, err := s.run(task.Command, task.Workspace, RunOptions{
			Kind:          task.Kind,
			AgentType:     task.AgentType,
			SessionID:     task.SessionID,
			RestartedFrom: task.ID,
			RestartPolicy: policy,
			RestartCount:  task.RestartCount + 1,
			Prompt:        task.Prompt,
			Description:   task.Description,
			TaskPacket:    task.TaskPacket,
			ScopeBinding:  task.ScopeBinding,
		})
		if err != nil {
			return result, err
		}
		task.RestartedBy = restarted.ID
		if err := s.save(task); err != nil {
			return result, err
		}
		result.Restarted = append(result.Restarted, restarted)
	}
	return result, nil
}

func (s Store) run(command string, cwd string, options RunOptions) (Task, error) {
	if strings.TrimSpace(command) == "" {
		return Task{}, errors.New("background command is required")
	}
	policy, err := normalizeRestartPolicy(options.RestartPolicy)
	if err != nil {
		return Task{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Task{}, err
	}
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	logPath := filepath.Join(s.Dir, id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Task{}, err
	}
	defer logFile.Close()

	cmd := exec.Command("sh", "-lc", command)
	cmd.Dir = cwd
	if len(options.Env) > 0 {
		cmd.Env = append([]string(nil), options.Env...)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return Task{}, err
	}
	task := Task{
		ID:            id,
		Kind:          options.Kind,
		AgentType:     options.AgentType,
		Command:       command,
		Prompt:        strings.TrimSpace(options.Prompt),
		Description:   strings.TrimSpace(options.Description),
		TaskPacket:    append(json.RawMessage(nil), options.TaskPacket...),
		Workspace:     cwd,
		SessionID:     options.SessionID,
		RestartPolicy: policy,
		RestartCount:  options.RestartCount,
		PID:           cmd.Process.Pid,
		Status:        "running",
		StartedAt:     time.Now().UTC(),
		LogPath:       logPath,
		RestartedFrom: options.RestartedFrom,
		ScopeBinding:  NormalizeScopeBinding(options.ScopeBinding),
	}
	if err := s.save(task); err != nil {
		_ = killBackgroundProcess(cmd.Process.Pid)
		return Task{}, err
	}
	go func() {
		err := cmd.Wait()
		current, getErr := s.Get(task.ID)
		if getErr == nil && current.Status != "running" && current.Status != "exited" {
			return
		}
		if getErr == nil {
			task = current
		}
		now := time.Now().UTC()
		task.CompletedAt = &now
		task.Status = "completed"
		if cmd.ProcessState != nil {
			exitCode := cmd.ProcessState.ExitCode()
			task.ExitCode = &exitCode
		}
		if err != nil {
			task.Status = "failed"
			task.Error = err.Error()
		}
		_ = s.save(task)
	}()
	return task, nil
}

func (s Store) Update(id string, message string) (Task, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return Task{}, errors.New("message is required")
	}
	task, err := s.Status(id)
	if err != nil {
		return Task{}, err
	}
	task.Messages = append(task.Messages, TaskMessage{
		Message:   message,
		CreatedAt: time.Now().UTC(),
	})
	if err := s.save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) UpdateHeartbeat(id string, heartbeat LaneHeartbeat) (Task, error) {
	task, err := s.Status(id)
	if err != nil {
		return Task{}, err
	}
	if heartbeat.ObservedAt.IsZero() {
		heartbeat.ObservedAt = time.Now().UTC()
	} else {
		heartbeat.ObservedAt = heartbeat.ObservedAt.UTC()
	}
	heartbeat.Status = strings.TrimSpace(heartbeat.Status)
	heartbeat.Provenance = NormalizeEventProvenance(heartbeat.Provenance)
	task.Heartbeat = &heartbeat
	if IsTerminalStatus(heartbeat.Status) {
		task.Status = strings.ToLower(strings.TrimSpace(heartbeat.Status))
		observedAt := heartbeat.ObservedAt
		task.CompletedAt = &observedAt
		task.TerminalEvents, task.TerminalOutcome = appendTerminalEvent(task.ID, task.TerminalEvents, TerminalEvent{
			ObservedAt: observedAt,
			Status:     heartbeat.Status,
			ExitCode:   cloneIntPtr(task.ExitCode),
			Error:      task.Error,
			Provenance: heartbeat.Provenance,
		})
	}
	if err := s.save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) LaneBoard(stalledAfter time.Duration) (LaneBoard, error) {
	now := time.Now().UTC()
	return s.LaneBoardAt(now, stalledAfter)
}

func (s Store) LaneBoardAt(now time.Time, stalledAfter time.Duration) (LaneBoard, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if stalledAfter <= 0 {
		stalledAfter = 30 * time.Second
	}
	tasks, err := s.List()
	if err != nil {
		return LaneBoard{}, err
	}
	board := LaneBoard{
		GeneratedAt: now.UTC(),
		Active:      []LaneBoardEntry{},
		Blocked:     []LaneBoardEntry{},
		Finished:    []LaneBoardEntry{},
	}
	for _, task := range tasks {
		entry := laneBoardEntry(task, board.GeneratedAt, stalledAfter)
		switch taskLaneBucket(task.Status) {
		case "active":
			board.Active = append(board.Active, entry)
		case "blocked":
			board.Blocked = append(board.Blocked, entry)
		default:
			board.Finished = append(board.Finished, entry)
		}
	}
	return board, nil
}

func (s Store) List() ([]Task, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Task{}, nil
		}
		return nil, err
	}
	tasks := []Task{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.Status(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].StartedAt.Equal(tasks[j].StartedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	return tasks, nil
}

func laneBoardEntry(task Task, now time.Time, stalledAfter time.Duration) LaneBoardEntry {
	freshness := taskFreshness(task.Heartbeat, now, stalledAfter)
	return LaneBoardEntry{
		TaskID:          task.ID,
		Prompt:          task.Prompt,
		Command:         task.Command,
		Kind:            task.Kind,
		SessionID:       task.SessionID,
		Status:          task.Status,
		Heartbeat:       task.Heartbeat,
		Freshness:       freshness,
		Provenance:      taskProvenance(task),
		ScopeBinding:    NormalizeScopeBinding(task.ScopeBinding),
		Lifecycle:       ResolveLifecycle(task.Status, freshness),
		TerminalOutcome: cloneTerminalOutcome(task.TerminalOutcome),
	}
}

func taskProvenance(task Task) EventProvenance {
	if task.Heartbeat != nil {
		return NormalizeEventProvenance(task.Heartbeat.Provenance)
	}
	return NormalizeEventProvenance(EventProvenance{})
}

func taskFreshness(heartbeat *LaneHeartbeat, now time.Time, stalledAfter time.Duration) LaneFreshness {
	if heartbeat == nil || heartbeat.ObservedAt.IsZero() {
		return LaneFreshnessUnknown
	}
	if !heartbeat.TransportAlive {
		return LaneFreshnessTransportDead
	}
	if stalledAfter <= 0 {
		stalledAfter = 30 * time.Second
	}
	if now.Sub(heartbeat.ObservedAt) > stalledAfter {
		return LaneFreshnessStalled
	}
	return LaneFreshnessHealthy
}

// ResolveLifecycle returns the canonical lifecycle outcome for a task.
func ResolveLifecycle(status string, freshness LaneFreshness) LifecycleResolution {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		normalized = "unknown"
	}
	switch normalized {
	case "completed", "finished", "stopped":
		return LifecycleResolution{
			Status:   normalized,
			Terminal: true,
			Reason:   "canonical_terminal_status",
		}
	case "failed", "error":
		return LifecycleResolution{
			Status:   normalized,
			Terminal: true,
			Reason:   "canonical_terminal_status",
		}
	case "exited":
		return LifecycleResolution{
			Status:               normalized,
			Terminal:             true,
			TerminalStateUnknown: true,
			Reason:               "process_exited_without_status",
		}
	}
	if freshness == LaneFreshnessTransportDead {
		return LifecycleResolution{
			Status:               normalized,
			TerminalStateUnknown: true,
			Reason:               "transport_dead_before_terminal_status",
		}
	}
	switch normalized {
	case "running", "created", "starting", "pending":
		return LifecycleResolution{Status: normalized, Reason: "active_status"}
	case "blocked", "waiting":
		return LifecycleResolution{Status: normalized, Reason: "blocked_status"}
	case "unknown":
		return LifecycleResolution{Status: normalized, Reason: "unknown_status"}
	default:
		return LifecycleResolution{Status: normalized, Reason: "non_terminal_status"}
	}
}

// IsTerminalStatus reports whether a task status is terminal.
func IsTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "finished", "stopped", "failed", "error", "exited":
		return true
	default:
		return false
	}
}

// RecordTerminalEvent appends a terminal notification while preserving only
// one actionable outcome for downstream automation.
func (s Store) RecordTerminalEvent(id string, event TerminalEvent) (Task, error) {
	task, err := s.Status(id)
	if err != nil {
		return Task{}, err
	}
	event.Status = firstNonEmpty(event.Status, task.Status)
	if !IsTerminalStatus(event.Status) {
		return Task{}, errors.New("terminal event status must be terminal")
	}
	if event.ExitCode == nil && task.ExitCode != nil {
		event.ExitCode = cloneIntPtr(task.ExitCode)
	}
	if strings.TrimSpace(event.Error) == "" {
		event.Error = task.Error
	}
	if event.ObservedAt.IsZero() {
		if task.CompletedAt != nil && !task.CompletedAt.IsZero() {
			event.ObservedAt = task.CompletedAt.UTC()
		} else {
			event.ObservedAt = time.Now().UTC()
		}
	}
	task.Status = strings.ToLower(strings.TrimSpace(event.Status))
	task.CompletedAt = &event.ObservedAt
	task.ExitCode = cloneIntPtr(event.ExitCode)
	task.Error = strings.TrimSpace(event.Error)
	task.TerminalEvents, task.TerminalOutcome = appendTerminalEvent(task.ID, task.TerminalEvents, event)
	if err := s.save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// NormalizeEventProvenance fills missing event provenance fields with stable
// defaults and canonicalizes known values.
func NormalizeEventProvenance(provenance EventProvenance) EventProvenance {
	provenance.SourceKind = normalizeProvenanceValue(provenance.SourceKind, "live_lane", map[string]string{
		"live":        "live_lane",
		"live-lane":   "live_lane",
		"test":        "test",
		"health":      "healthcheck",
		"healthcheck": "healthcheck",
		"replay":      "replay",
		"transport":   "transport",
	})
	provenance.Environment = normalizeProvenanceText(provenance.Environment, "local")
	provenance.Channel = normalizeProvenanceText(provenance.Channel, "local")
	provenance.Emitter = normalizeProvenanceText(provenance.Emitter, "codog")
	provenance.Confidence = normalizeProvenanceValue(provenance.Confidence, "medium", map[string]string{
		"low":    "low",
		"medium": "medium",
		"med":    "medium",
		"high":   "high",
	})
	return provenance
}

// NormalizeScopeBinding fills missing lane ownership fields with stable
// defaults and derives whether the current watcher should act.
func NormalizeScopeBinding(binding ScopeBinding) ScopeBinding {
	binding.Owner = strings.TrimSpace(binding.Owner)
	binding.WorkflowScope = normalizeScopeText(binding.WorkflowScope, "codog")
	binding.WatcherAction = normalizeScopeValue(binding.WatcherAction, "act", map[string]string{
		"act":          "act",
		"action":       "act",
		"actionable":   "act",
		"observe":      "observe",
		"observe_only": "observe",
		"observe-only": "observe",
		"readonly":     "observe",
		"read_only":    "observe",
		"ignore":       "ignore",
		"ignored":      "ignore",
	})
	binding.Actionable = binding.WatcherAction == "act"
	return binding
}

func normalizeScopeValue(value string, fallback string, aliases map[string]string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" {
		return fallback
	}
	if canonical, ok := aliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func normalizeScopeText(value string, fallback string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return fallback
	}
	return normalized
}

func normalizeProvenanceValue(value string, fallback string, aliases map[string]string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" {
		return fallback
	}
	if canonical, ok := aliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func normalizeProvenanceText(value string, fallback string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return fallback
	}
	return normalized
}

func appendTerminalEvent(taskID string, events []TerminalEvent, event TerminalEvent) ([]TerminalEvent, *TerminalOutcome) {
	event.Status = strings.ToLower(strings.TrimSpace(event.Status))
	event.Error = strings.TrimSpace(event.Error)
	event.ObservedAt = normalizeTerminalObservedAt(event.ObservedAt)
	event.Provenance = NormalizeEventProvenance(event.Provenance)
	event.ExitCode = cloneIntPtr(event.ExitCode)
	event.Fingerprint = terminalFingerprint(taskID, event)
	event.Sequence = len(events) + 1
	if event.Sequence == 1 {
		event.Actionable = true
	} else {
		event.Actionable = false
	}
	for _, existing := range events {
		if existing.Fingerprint == event.Fingerprint {
			event.Duplicate = true
			event.DuplicateOf = existing.Fingerprint
			event.MateriallyDifferent = terminalEventMateriallyDifferent(existing, event)
			event.Actionable = false
			break
		}
	}
	next := append(append([]TerminalEvent(nil), events...), event)
	return next, buildTerminalOutcome(next)
}

func ensureTerminalOutcome(task Task) Task {
	normalizedEvents := make([]TerminalEvent, 0, len(task.TerminalEvents))
	for _, event := range task.TerminalEvents {
		if !IsTerminalStatus(event.Status) {
			continue
		}
		normalizedEvents, _ = appendTerminalEvent(task.ID, normalizedEvents, event)
	}
	task.TerminalEvents = normalizedEvents
	if IsTerminalStatus(task.Status) {
		event := TerminalEvent{
			Status:     task.Status,
			ExitCode:   cloneIntPtr(task.ExitCode),
			Error:      task.Error,
			Provenance: taskProvenance(task),
		}
		if task.CompletedAt != nil {
			event.ObservedAt = *task.CompletedAt
		}
		fingerprint := terminalFingerprint(task.ID, event)
		if !hasTerminalFingerprint(task.TerminalEvents, fingerprint) {
			task.TerminalEvents, task.TerminalOutcome = appendTerminalEvent(task.ID, task.TerminalEvents, event)
			return task
		}
	}
	task.TerminalOutcome = buildTerminalOutcome(task.TerminalEvents)
	return task
}

func buildTerminalOutcome(events []TerminalEvent) *TerminalOutcome {
	if len(events) == 0 {
		return nil
	}
	var canonical *TerminalEvent
	duplicateCount := 0
	conflictCount := 0
	materiallyDifferent := false
	for i := range events {
		event := events[i]
		if event.Duplicate {
			duplicateCount++
		}
		if event.MateriallyDifferent {
			materiallyDifferent = true
		}
		if canonical == nil && event.Actionable {
			canonical = &event
			continue
		}
		if canonical != nil && event.Fingerprint != canonical.Fingerprint {
			conflictCount++
			if event.Sequence > canonical.Sequence {
				materiallyDifferent = true
			}
		}
	}
	if canonical == nil {
		canonical = &events[0]
	}
	return &TerminalOutcome{
		Fingerprint:         canonical.Fingerprint,
		Status:              canonical.Status,
		ExitCode:            cloneIntPtr(canonical.ExitCode),
		Error:               canonical.Error,
		ObservedAt:          canonical.ObservedAt,
		EventCount:          len(events),
		DuplicateCount:      duplicateCount,
		ConflictCount:       conflictCount,
		MateriallyDifferent: materiallyDifferent,
		Actionable:          true,
	}
}

func hasTerminalFingerprint(events []TerminalEvent, fingerprint string) bool {
	for _, event := range events {
		if event.Fingerprint == fingerprint {
			return true
		}
	}
	return false
}

func terminalFingerprint(taskID string, event TerminalEvent) string {
	exitCode := ""
	if event.ExitCode != nil {
		exitCode = strconv.Itoa(*event.ExitCode)
	}
	status := strings.ToLower(strings.TrimSpace(event.Status))
	sum := sha256.Sum256([]byte(strings.Join([]string{taskID, status, exitCode}, "\x00")))
	return "terminal:" + hex.EncodeToString(sum[:12])
}

func terminalEventMateriallyDifferent(a TerminalEvent, b TerminalEvent) bool {
	return strings.TrimSpace(a.Error) != strings.TrimSpace(b.Error) ||
		a.Provenance.SourceKind != b.Provenance.SourceKind ||
		a.Provenance.Emitter != b.Provenance.Emitter
}

func cloneTerminalOutcome(outcome *TerminalOutcome) *TerminalOutcome {
	if outcome == nil {
		return nil
	}
	cloned := *outcome
	cloned.ExitCode = cloneIntPtr(outcome.ExitCode)
	return &cloned
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func normalizeTerminalObservedAt(observedAt time.Time) time.Time {
	if observedAt.IsZero() {
		return time.Now().UTC()
	}
	return observedAt.UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func taskLaneBucket(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "created", "starting", "pending":
		return "active"
	case "blocked", "waiting":
		return "blocked"
	default:
		return "finished"
	}
}

func (s Store) Get(id string) (Task, error) {
	id, err := validateTaskID(id)
	if err != nil {
		return Task{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) Status(id string) (Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	task.ScopeBinding = NormalizeScopeBinding(task.ScopeBinding)
	normalizeTaskHeartbeat(&task)
	if task.Status == "running" && !processRunning(task.PID) {
		now := time.Now().UTC()
		task.Status = "exited"
		task.CompletedAt = &now
		_ = s.save(task)
	}
	return task, nil
}

func normalizeTaskHeartbeat(task *Task) {
	if task != nil && task.Heartbeat != nil {
		task.Heartbeat.Provenance = NormalizeEventProvenance(task.Heartbeat.Provenance)
	}
}

func (s Store) Stop(id string) (Task, error) {
	task, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if task.Status != "running" {
		return task, nil
	}
	if err := killBackgroundProcess(task.PID); err != nil && processRunning(task.PID) {
		return Task{}, err
	}
	waitForBackgroundProcessExit(task.PID, 500*time.Millisecond)
	now := time.Now().UTC()
	task.Status = "stopped"
	task.CompletedAt = &now
	if err := s.save(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func waitForBackgroundProcessExit(pid int, timeout time.Duration) {
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for processRunning(pid) {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s Store) Logs(id string, limitBytes int64) (string, error) {
	task, err := s.Get(id)
	if err != nil {
		return "", err
	}
	file, err := os.Open(task.LogPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if limitBytes <= 0 || limitBytes > info.Size() {
		limitBytes = info.Size()
	}
	start := info.Size() - limitBytes
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s Store) LogFrom(id string, offset int64) (int64, string, error) {
	return s.LogRange(id, offset, 0)
}

func (s Store) LogRange(id string, offset int64, limitBytes int64) (int64, string, error) {
	task, err := s.Get(id)
	if err != nil {
		return offset, "", err
	}
	return s.readLogRange(task.LogPath, offset, limitBytes)
}

func (s Store) Watch(ctx context.Context, id string, options WatchOptions, emit func(WatchEvent) error) error {
	if emit == nil {
		return errors.New("watch emit callback is required")
	}
	if options.Interval <= 0 {
		options.Interval = 500 * time.Millisecond
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	task, err := s.Status(id)
	if err != nil {
		return err
	}
	events := 0
	if err := emit(WatchEvent{Type: "status", ID: id, Status: task.Status, Error: task.Error, Task: &task, Provenance: taskProvenance(task), ScopeBinding: NormalizeScopeBinding(task.ScopeBinding)}); err != nil {
		return err
	}
	events++
	if options.MaxEvents > 0 && events >= options.MaxEvents {
		return nil
	}
	lastStatus := task.Status
	for {
		nextOffset, data, err := s.readLogFrom(task.LogPath, offset)
		if err != nil {
			return err
		}
		if data != "" {
			offset = nextOffset
			if err := emit(WatchEvent{Type: "log", ID: id, Offset: offset, Data: data, Provenance: taskProvenance(task), ScopeBinding: NormalizeScopeBinding(task.ScopeBinding)}); err != nil {
				return err
			}
			events++
			if options.MaxEvents > 0 && events >= options.MaxEvents {
				return nil
			}
		}
		task, err = s.Status(id)
		if err != nil {
			return err
		}
		if task.Status != lastStatus {
			if err := emit(WatchEvent{Type: "status", ID: id, Status: task.Status, Error: task.Error, Task: &task, Provenance: taskProvenance(task), ScopeBinding: NormalizeScopeBinding(task.ScopeBinding)}); err != nil {
				return err
			}
			events++
			lastStatus = task.Status
			if options.MaxEvents > 0 && events >= options.MaxEvents {
				return nil
			}
		}
		if task.Status != "running" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(options.Interval):
		}
	}
}

func (s Store) readLogFrom(path string, offset int64) (int64, string, error) {
	return s.readLogRange(path, offset, 0)
}

func (s Store) readLogRange(path string, offset int64, limitBytes int64) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return offset, "", nil
		}
		return offset, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, "", err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > info.Size() {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, "", err
	}
	remaining := info.Size() - offset
	if limitBytes <= 0 || limitBytes > remaining {
		limitBytes = remaining
	}
	data, err := io.ReadAll(io.LimitReader(file, limitBytes))
	if err != nil {
		return offset, "", err
	}
	return offset + int64(len(data)), string(data), nil
}

func (s Store) save(task Task) error {
	id, err := validateTaskID(task.ID)
	if err != nil {
		return err
	}
	task.ID = id
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	task.ScopeBinding = NormalizeScopeBinding(task.ScopeBinding)
	task = ensureTerminalOutcome(task)
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+task.ID+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := filepath.Join(s.Dir, task.ID+".json")
	if err := os.Rename(tmpPath, target); err != nil {
		if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if renameErr := os.Rename(tmpPath, target); renameErr != nil {
			return renameErr
		}
	}
	keepTemp = false
	return nil
}

func validateTaskID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("background task id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", errors.New("background task id must be a single path component")
	}
	return id, nil
}

func (s Store) remove(task Task) error {
	if task.Status == "running" {
		return errors.New("cannot prune a running background task")
	}
	if task.LogPath != "" && isPathInsideDir(task.LogPath, s.Dir) {
		if err := os.Remove(task.LogPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(filepath.Join(s.Dir, task.ID+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func taskRetentionTime(task Task) time.Time {
	if task.CompletedAt != nil && !task.CompletedAt.IsZero() {
		return *task.CompletedAt
	}
	return task.StartedAt
}

func isPathInsideDir(path string, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func normalizeRestartPolicy(policy *RestartPolicy) (*RestartPolicy, error) {
	if policy == nil {
		return nil, nil
	}
	next := *policy
	if next.Mode == "" {
		next.Mode = "on-failure"
	}
	if next.Mode != "on-failure" && next.Mode != "always" {
		return nil, errors.New("restart mode must be on-failure or always")
	}
	if next.MaxAttempts < 0 {
		return nil, errors.New("restart max attempts must be non-negative")
	}
	if next.DelaySeconds < 0 {
		return nil, errors.New("restart delay must be non-negative")
	}
	return &next, nil
}

func shouldRestart(task Task, policy RestartPolicy) bool {
	switch policy.Mode {
	case "", "on-failure":
		return task.Status == "failed" || task.Status == "exited"
	case "always":
		return task.Status != "stopped"
	default:
		return false
	}
}
