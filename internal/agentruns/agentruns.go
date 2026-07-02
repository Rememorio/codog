package agentruns

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/background"
)

// Run records the relationship between a named agent invocation and the
// background task that executes it.
type Run struct {
	ID           string    `json:"id"`
	Agent        string    `json:"agent"`
	Prompt       string    `json:"prompt,omitempty"`
	Workspace    string    `json:"workspace,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	TaskID       string    `json:"task_id"`
	WorktreeID   string    `json:"worktree_id,omitempty"`
	WorktreePath string    `json:"worktree_path,omitempty"`
	WorktreeRef  string    `json:"worktree_ref,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Store persists agent run records under the Codog config home.
type Store struct {
	Dir string
}

// Status combines an agent run record with the current state of its background
// task, when that task can still be found.
type Status struct {
	Run           Run              `json:"run"`
	Task          *background.Task `json:"task,omitempty"`
	CurrentStatus string           `json:"current_status"`
	Error         string           `json:"error,omitempty"`
}

// BoardEntry is the lane-board view for a single agent run.
type BoardEntry struct {
	Run       Run                       `json:"run"`
	Task      *background.Task          `json:"task,omitempty"`
	Status    string                    `json:"status"`
	Freshness background.LaneFreshness  `json:"freshness"`
	Heartbeat *background.LaneHeartbeat `json:"heartbeat,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

// Board groups agent runs by current execution state for operator views.
type Board struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Active      []BoardEntry `json:"active"`
	Blocked     []BoardEntry `json:"blocked"`
	Finished    []BoardEntry `json:"finished"`
	Orphaned    []BoardEntry `json:"orphaned,omitempty"`
}

// NewStore returns the agent run store rooted at configHome.
func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "agent-runs")}
}

// Save writes a complete agent run record, filling timestamps when they are
// missing.
func (s Store) Save(run Run) (Run, error) {
	if strings.TrimSpace(run.ID) == "" {
		return Run{}, errors.New("agent run id is required")
	}
	if strings.TrimSpace(run.TaskID) == "" {
		return Run{}, errors.New("agent run task id is required")
	}
	if err := validateID(run.ID); err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Run{}, err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return Run{}, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, run.ID+".json"), append(data, '\n'), 0o644); err != nil {
		return Run{}, err
	}
	return run, nil
}

// List returns all saved agent runs, newest first.
func (s Store) List() ([]Run, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Run{}, nil
		}
		return nil, err
	}
	runs := []Run{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		run, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	return runs, nil
}

// Get loads a single agent run by id.
func (s Store) Get(id string) (Run, error) {
	if err := validateID(id); err != nil {
		return Run{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, err
	}
	return run, nil
}

// Touch refreshes a run's UpdatedAt timestamp and saves it.
func (s Store) Touch(id string) (Run, error) {
	run, err := s.Get(id)
	if err != nil {
		return Run{}, err
	}
	run.UpdatedAt = time.Now().UTC()
	return s.Save(run)
}

// Remove deletes a saved agent run record.
func (s Store) Remove(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.Dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// StatusForTask resolves an agent run against the background task store.
func StatusForTask(store background.Store, run Run) Status {
	status := Status{Run: run, CurrentStatus: "unknown"}
	task, err := store.Status(run.TaskID)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Task = &task
	status.CurrentStatus = firstNonEmpty(task.Status, "unknown")
	return status
}

// BuildBoard groups agent runs into active, blocked, finished, and orphaned
// lanes using their background task state.
func BuildBoard(store background.Store, runs []Run, now time.Time, stalledAfter time.Duration) Board {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if stalledAfter <= 0 {
		stalledAfter = 30 * time.Second
	}
	board := Board{
		GeneratedAt: now.UTC(),
		Active:      []BoardEntry{},
		Blocked:     []BoardEntry{},
		Finished:    []BoardEntry{},
		Orphaned:    []BoardEntry{},
	}
	for _, run := range runs {
		entry := BoardEntry{Run: run, Status: "unknown", Freshness: background.LaneFreshnessUnknown}
		task, err := store.Status(run.TaskID)
		if err != nil {
			entry.Error = err.Error()
			board.Orphaned = append(board.Orphaned, entry)
			continue
		}
		entry.Task = &task
		entry.Status = firstNonEmpty(task.Status, "unknown")
		entry.Heartbeat = task.Heartbeat
		entry.Freshness = freshness(task.Heartbeat, board.GeneratedAt, stalledAfter)
		switch laneBucket(task.Status) {
		case "active":
			board.Active = append(board.Active, entry)
		case "blocked":
			board.Blocked = append(board.Blocked, entry)
		default:
			board.Finished = append(board.Finished, entry)
		}
	}
	return board
}

// Prune removes stale completed or orphaned agent run records while preserving
// running tasks and the newest retained completed runs.
func Prune(runStore Store, taskStore background.Store, options background.PruneOptions) (background.PruneResult, error) {
	runs, err := runStore.List()
	if err != nil {
		return background.PruneResult{}, err
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return retentionTime(runs[i]).After(retentionTime(runs[j]))
	})
	cutoff := time.Time{}
	if options.OlderThan > 0 {
		cutoff = time.Now().UTC().Add(-options.OlderThan)
	}
	seenNonRunning := 0
	result := background.PruneResult{}
	for _, run := range runs {
		task, taskErr := taskStore.Status(run.TaskID)
		if taskErr == nil && strings.EqualFold(task.Status, "running") {
			result.Kept++
			continue
		}
		if taskErr == nil {
			seenNonRunning++
			if options.Keep > 0 && seenNonRunning <= options.Keep {
				result.Kept++
				continue
			}
			if !cutoff.IsZero() && retentionTime(run).After(cutoff) {
				result.Kept++
				continue
			}
		}
		if err := runStore.Remove(run.ID); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, run.ID)
	}
	result.RemovedCount = len(result.Removed)
	return result, nil
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("agent run id is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return errors.New("agent run id must be a single path component")
	}
	return nil
}

func freshness(heartbeat *background.LaneHeartbeat, now time.Time, stalledAfter time.Duration) background.LaneFreshness {
	if heartbeat == nil || heartbeat.ObservedAt.IsZero() {
		return background.LaneFreshnessUnknown
	}
	if !heartbeat.TransportAlive {
		return background.LaneFreshnessTransportDead
	}
	if stalledAfter <= 0 {
		stalledAfter = 30 * time.Second
	}
	if now.Sub(heartbeat.ObservedAt) > stalledAfter {
		return background.LaneFreshnessStalled
	}
	return background.LaneFreshnessHealthy
}

func laneBucket(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "created", "starting", "pending":
		return "active"
	case "blocked", "waiting":
		return "blocked"
	default:
		return "finished"
	}
}

func retentionTime(run Run) time.Time {
	if !run.UpdatedAt.IsZero() {
		return run.UpdatedAt
	}
	return run.CreatedAt
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
