package cron

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

type Entry struct {
	ID          string     `json:"cron_id"`
	Schedule    string     `json:"schedule"`
	Prompt      string     `json:"prompt"`
	Description string     `json:"description,omitempty"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	RunCount    int        `json:"run_count"`
}

type Store struct {
	ConfigHome string
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func (s Store) Create(schedule string, prompt string, description string) (Entry, error) {
	schedule = strings.TrimSpace(schedule)
	prompt = strings.TrimSpace(prompt)
	description = strings.TrimSpace(description)
	if schedule == "" {
		return Entry{}, errors.New("schedule is required")
	}
	if prompt == "" {
		return Entry{}, errors.New("prompt is required")
	}
	now := time.Now().UTC()
	entry := Entry{
		ID:          newID(now),
		Schedule:    schedule,
		Prompt:      prompt,
		Description: description,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.save(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s Store) List() ([]Entry, error) {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, file := range entries {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		entry, err := s.read(filepath.Join(s.dir(), file.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s Store) Delete(id string) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, errors.New("cron_id is required")
	}
	if !safeID(id) {
		return Entry{}, fmt.Errorf("invalid cron_id %q", id)
	}
	path := s.path(id)
	entry, err := s.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("cron not found: %s", id)
		}
		return Entry{}, err
	}
	if err := os.Remove(path); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s Store) dir() string {
	configHome := strings.TrimSpace(s.ConfigHome)
	if configHome == "" {
		configHome = ".codog"
	}
	return filepath.Join(configHome, "cron")
}

func (s Store) path(id string) string {
	return filepath.Join(s.dir(), id+".json")
}

func (s Store) read(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s Store) save(entry Entry) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(entry.ID), append(data, '\n'), 0o644)
}

func newID(now time.Time) string {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("cron_%d", now.UnixNano())
	}
	return fmt.Sprintf("cron_%d_%s", now.Unix(), hex.EncodeToString(bytes[:]))
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
