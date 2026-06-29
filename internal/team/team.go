package team

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
)

type TaskSpec struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
}

type Team struct {
	ID        string     `json:"team_id"`
	Name      string     `json:"name"`
	Tasks     []TaskSpec `json:"tasks"`
	TaskIDs   []string   `json:"task_ids"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Store struct {
	ConfigHome string
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func (s Store) Create(name string, tasks []TaskSpec, taskIDs []string) (Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Team{}, errors.New("team name is required")
	}
	if len(tasks) == 0 {
		return Team{}, errors.New("team tasks are required")
	}
	normalized := make([]TaskSpec, 0, len(tasks))
	for _, task := range tasks {
		task.Prompt = strings.TrimSpace(task.Prompt)
		task.Description = strings.TrimSpace(task.Description)
		task.TaskID = strings.TrimSpace(task.TaskID)
		if task.Prompt == "" {
			return Team{}, errors.New("task prompt is required")
		}
		normalized = append(normalized, task)
	}
	now := time.Now().UTC()
	team := Team{
		ID:        newID(now),
		Name:      name,
		Tasks:     normalized,
		TaskIDs:   append([]string(nil), taskIDs...),
		Status:    "running",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if len(team.TaskIDs) == 0 {
		team.Status = "created"
	}
	if err := s.Save(team); err != nil {
		return Team{}, err
	}
	return team, nil
}

func (s Store) List() ([]Team, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}
	out := []Team{}
	for _, file := range entries {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		team, err := s.read(filepath.Join(s.dir(), file.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, team)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s Store) Get(id string) (Team, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Team{}, errors.New("team_id is required")
	}
	if !safeID(id) {
		return Team{}, fmt.Errorf("invalid team_id %q", id)
	}
	return s.read(s.path(id))
}

func (s Store) MarkDeleted(id string) (Team, error) {
	team, err := s.Get(id)
	if err != nil {
		if os.IsNotExist(err) {
			return Team{}, fmt.Errorf("team not found: %s", id)
		}
		return Team{}, err
	}
	team.Status = "deleted"
	team.UpdatedAt = time.Now().UTC()
	if err := s.Save(team); err != nil {
		return Team{}, err
	}
	return team, nil
}

func (s Store) Save(team Team) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(team, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(team.ID), append(data, '\n'), 0o644)
}

func (s Store) dir() string {
	configHome := strings.TrimSpace(s.ConfigHome)
	if configHome == "" {
		configHome = ".codog"
	}
	return filepath.Join(configHome, "teams")
}

func (s Store) path(id string) string {
	return filepath.Join(s.dir(), id+".json")
}

func (s Store) read(path string) (Team, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Team{}, err
	}
	var team Team
	if err := json.Unmarshal(data, &team); err != nil {
		return Team{}, err
	}
	return team, nil
}

func newID(now time.Time) string {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("team_%d", now.UnixNano())
	}
	return fmt.Sprintf("team_%d_%s", now.Unix(), hex.EncodeToString(bytes[:]))
}

func safeID(id string) bool {
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
