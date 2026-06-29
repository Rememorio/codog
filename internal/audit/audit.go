package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultLimit = 50

type Event struct {
	Type               string    `json:"type"`
	Time               time.Time `json:"time"`
	SessionID          string    `json:"session_id,omitempty"`
	Workspace          string    `json:"workspace,omitempty"`
	ToolName           string    `json:"tool_name,omitempty"`
	Input              string    `json:"input,omitempty"`
	Output             string    `json:"output,omitempty"`
	IsError            bool      `json:"is_error,omitempty"`
	PermissionMode     string    `json:"permission_mode,omitempty"`
	RequiredPermission string    `json:"required_permission,omitempty"`
	Allowed            *bool     `json:"allowed,omitempty"`
	Reason             string    `json:"reason,omitempty"`
}

type Store struct {
	Path string
}

func NewStore(configHome string) *Store {
	return &Store{Path: filepath.Join(configHome, "audit", "events.jsonl")}
}

func (s *Store) Append(event Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (s *Store) List(limit int) ([]Event, error) {
	file, err := os.Open(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Time.After(events[j].Time)
	})
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func Clip(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "... [truncated]"
}

func Bool(value bool) *bool {
	return &value
}
