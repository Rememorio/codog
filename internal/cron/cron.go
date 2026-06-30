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
	"strconv"
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

func (s Store) Due(now time.Time) ([]Entry, error) {
	entries, err := s.List()
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, entry := range entries {
		if IsDue(entry, now) {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s Store) MarkRun(id string, now time.Time) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, errors.New("cron_id is required")
	}
	if !safeID(id) {
		return Entry{}, fmt.Errorf("invalid cron_id %q", id)
	}
	entry, err := s.read(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("cron not found: %s", id)
		}
		return Entry{}, err
	}
	runAt := now.UTC()
	entry.LastRunAt = &runAt
	entry.RunCount++
	entry.UpdatedAt = runAt
	if err := s.save(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func IsDue(entry Entry, now time.Time) bool {
	if !entry.Enabled {
		return false
	}
	now = now.UTC().Truncate(time.Minute)
	if entry.LastRunAt != nil && !entry.LastRunAt.UTC().Truncate(time.Minute).Before(now) {
		return false
	}
	return scheduleDue(entry.Schedule, now, entry.LastRunAt)
}

func scheduleDue(schedule string, now time.Time, lastRun *time.Time) bool {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return false
	}
	lower := strings.ToLower(schedule)
	switch lower {
	case "@hourly":
		return now.Minute() == 0
	case "@daily", "@midnight":
		return now.Hour() == 0 && now.Minute() == 0
	case "@weekly":
		return now.Weekday() == time.Sunday && now.Hour() == 0 && now.Minute() == 0
	case "@monthly":
		return now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0
	case "@yearly", "@annually":
		return now.Month() == time.January && now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0
	}
	if strings.HasPrefix(lower, "@every ") {
		interval, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(lower, "@every ")))
		if err != nil || interval <= 0 {
			return false
		}
		if lastRun == nil {
			return true
		}
		return !now.Before(lastRun.UTC().Add(interval))
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return false
	}
	return matchCronField(now.Minute(), fields[0], 0, 59, false) &&
		matchCronField(now.Hour(), fields[1], 0, 23, false) &&
		matchCronField(now.Day(), fields[2], 1, 31, false) &&
		matchCronField(int(now.Month()), fields[3], 1, 12, false) &&
		matchCronField(int(now.Weekday()), fields[4], 0, 7, true)
}

func matchCronField(value int, expr string, min int, max int, weekday bool) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	if weekday && value == 7 {
		value = 0
	}
	allowed := map[int]bool{}
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		if err := expandCronPart(part, min, max, weekday, allowed); err != nil {
			return false
		}
	}
	if weekday && value == 0 && allowed[7] {
		return true
	}
	return allowed[value]
}

func expandCronPart(part string, min int, max int, weekday bool, allowed map[int]bool) error {
	step := 1
	if base, rawStep, ok := strings.Cut(part, "/"); ok {
		part = base
		parsed, err := strconv.Atoi(rawStep)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid cron step")
		}
		step = parsed
	}
	start, end, err := cronPartRange(part, min, max)
	if err != nil {
		return err
	}
	add := func(v int) {
		if weekday && v == 7 {
			allowed[0] = true
		}
		allowed[v] = true
	}
	if start <= end {
		for v := start; v <= end; v += step {
			add(v)
		}
		return nil
	}
	for v := start; v <= max; v += step {
		add(v)
	}
	for v := min; v <= end; v += step {
		add(v)
	}
	return nil
}

func cronPartRange(part string, min int, max int) (int, int, error) {
	part = strings.TrimSpace(part)
	if part == "*" {
		return min, max, nil
	}
	if left, right, ok := strings.Cut(part, "-"); ok {
		start, err := parseCronNumber(left, min, max)
		if err != nil {
			return 0, 0, err
		}
		end, err := parseCronNumber(right, min, max)
		if err != nil {
			return 0, 0, err
		}
		return start, end, nil
	}
	value, err := parseCronNumber(part, min, max)
	if err != nil {
		return 0, 0, err
	}
	return value, value, nil
}

func parseCronNumber(value string, min int, max int) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("cron value %d out of range", parsed)
	}
	return parsed, nil
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
