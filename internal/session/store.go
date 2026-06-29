package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Record struct {
	Type      string             `json:"type"`
	Time      time.Time          `json:"time"`
	Message   *anthropic.Message `json:"message,omitempty"`
	Input     string             `json:"input,omitempty"`
	SessionID string             `json:"session_id,omitempty"`
}

type Session struct {
	ID       string
	Messages []anthropic.Message
	Path     string
}

type Store struct {
	Dir string
}

func NewStore(configHome string) *Store {
	return &Store{Dir: filepath.Join(configHome, "sessions")}
}

func (s *Store) Open(id string) (*Session, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if id == "" {
		id = newID()
	}
	if id == "latest" {
		latest, err := s.LatestID()
		if err != nil {
			return nil, err
		}
		id = latest
	}
	path := s.pathFor(id)
	messages, err := s.readMessages(path)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Messages: messages, Path: path}, nil
}

func (s *Store) Append(id string, msg anthropic.Message) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path := s.pathFor(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	record := Record{
		Type:      "message",
		Time:      time.Now().UTC(),
		Message:   &msg,
		SessionID: id,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (s *Store) List() ([]Session, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		path := filepath.Join(s.Dir, entry.Name())
		messages, err := s.readMessages(path)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, Session{ID: id, Path: path, Messages: messages})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ID > sessions[j].ID
	})
	return sessions, nil
}

func (s *Store) LatestID() (string, error) {
	sessions, err := s.List()
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", errors.New("no saved sessions")
	}
	return sessions[0].ID, nil
}

func (s *Store) pathFor(id string) string {
	return filepath.Join(s.Dir, id+".jsonl")
}

func (s *Store) readMessages(path string) ([]anthropic.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var messages []anthropic.Message
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		if record.Message != nil {
			messages = append(messages, *record.Message)
		}
	}
	return messages, nil
}

func newID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%06d", now.Format("20060102T150405Z"), now.Nanosecond()/1000)
}
