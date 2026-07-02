package agentruns

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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

type Store struct {
	Dir string
}

func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "agent-runs")}
}

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

func (s Store) Remove(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.Dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
