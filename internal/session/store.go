package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Record struct {
	Type            string             `json:"type"`
	Time            time.Time          `json:"time"`
	Message         *anthropic.Message `json:"message,omitempty"`
	Input           string             `json:"input,omitempty"`
	SessionID       string             `json:"session_id,omitempty"`
	ParentSessionID string             `json:"parent_session_id,omitempty"`
	BranchName      string             `json:"branch_name,omitempty"`
}

type Session struct {
	ID       string
	Messages []anthropic.Message
	Path     string
}

type PromptEntry struct {
	Index     int       `json:"index"`
	Time      time.Time `json:"time"`
	Text      string    `json:"text"`
	SessionID string    `json:"session_id"`
}

type RewindResult struct {
	SessionID         string `json:"session_id"`
	Path              string `json:"path"`
	OriginalMessages  int    `json:"original_messages"`
	RemainingMessages int    `json:"remaining_messages"`
	RemovedMessages   int    `json:"removed_messages"`
}

type ReplaceResult struct {
	SessionID         string `json:"session_id"`
	Path              string `json:"path"`
	OriginalMessages  int    `json:"original_messages"`
	RemainingMessages int    `json:"remaining_messages"`
	RemovedMessages   int    `json:"removed_messages"`
}

type Store struct {
	Dir       string
	LegacyDir string
	Workspace string
}

var ErrNoSessions = errors.New("no saved sessions")

func NewStore(configHome string) *Store {
	return &Store{Dir: filepath.Join(configHome, "sessions")}
}

func NewWorkspaceStore(configHome string, workspace string) *Store {
	canonical := canonicalWorkspace(workspace)
	root := filepath.Join(configHome, "sessions")
	return &Store{
		Dir:       filepath.Join(root, WorkspaceFingerprint(canonical)),
		LegacyDir: root,
		Workspace: canonical,
	}
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
	return writeRecord(file, record)
}

func (s *Store) AppendInput(id string, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path := s.pathFor(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeRecord(file, Record{
		Type:      "input",
		Time:      time.Now().UTC(),
		Input:     input,
		SessionID: id,
	})
}

func (s *Store) Exists(id string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, errors.New("session id is required")
	}
	if id == "latest" {
		_, err := s.LatestID()
		if errors.Is(err, ErrNoSessions) {
			return false, nil
		}
		return err == nil, err
	}
	_, err := os.Stat(s.pathFor(id))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *Store) Delete(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("session id is required")
	}
	if id == "latest" {
		latest, err := s.LatestID()
		if err != nil {
			return err
		}
		id = latest
	}
	path := s.pathFor(id)
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}

func (s *Store) Fork(id string, branchName string) (*Session, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("session id is required")
	}
	exists, err := s.Exists(id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, os.ErrNotExist
	}
	source, err := s.Open(id)
	if err != nil {
		return nil, err
	}
	forkID := newID()
	path := filepath.Join(s.Dir, forkID+".jsonl")
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	branchName = strings.TrimSpace(branchName)
	if err := writeRecord(file, Record{
		Type:            "fork",
		Time:            time.Now().UTC(),
		SessionID:       forkID,
		ParentSessionID: source.ID,
		BranchName:      branchName,
	}); err != nil {
		return nil, err
	}
	for _, msg := range source.Messages {
		next := msg
		if err := writeRecord(file, Record{
			Type:      "message",
			Time:      time.Now().UTC(),
			Message:   &next,
			SessionID: forkID,
		}); err != nil {
			return nil, err
		}
	}
	return &Session{ID: forkID, Messages: append([]anthropic.Message(nil), source.Messages...), Path: path}, nil
}

func (s *Store) List() ([]Session, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var sessions []Session
	for _, dir := range s.sessionDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".jsonl")
			if _, ok := seen[id]; ok {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			messages, err := s.readMessages(path)
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, Session{ID: id, Path: path, Messages: messages})
			seen[id] = struct{}{}
		}
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
		return "", ErrNoSessions
	}
	return sessions[0].ID, nil
}

func (s *Store) PromptHistory(id string) ([]PromptEntry, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("session id is required")
	}
	if id == "latest" {
		latest, err := s.LatestID()
		if err != nil {
			return nil, err
		}
		id = latest
	}
	records, err := s.readRecords(s.pathFor(id))
	if err != nil {
		return nil, err
	}
	var entries []PromptEntry
	for _, record := range records {
		if record.Type == "input" && strings.TrimSpace(record.Input) != "" {
			entries = append(entries, PromptEntry{
				Index:     len(entries) + 1,
				Time:      record.Time,
				Text:      record.Input,
				SessionID: id,
			})
		}
	}
	if len(entries) != 0 {
		return entries, nil
	}
	for _, record := range records {
		if record.Message == nil || record.Message.Role != "user" {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
				continue
			}
			entries = append(entries, PromptEntry{
				Index:     len(entries) + 1,
				Time:      record.Time,
				Text:      block.Text,
				SessionID: id,
			})
			break
		}
	}
	return entries, nil
}

func (s *Store) Rewind(id string, removeMessages int) (RewindResult, error) {
	if strings.TrimSpace(id) == "" {
		return RewindResult{}, errors.New("session id is required")
	}
	if removeMessages <= 0 {
		return RewindResult{}, errors.New("rewind message count must be positive")
	}
	if id == "latest" {
		latest, err := s.LatestID()
		if err != nil {
			return RewindResult{}, err
		}
		id = latest
	}
	path := s.pathFor(id)
	records, err := s.readRecords(path)
	if err != nil {
		return RewindResult{}, err
	}
	totalMessages := 0
	for _, record := range records {
		if record.Message != nil {
			totalMessages++
		}
	}
	if totalMessages == 0 {
		return RewindResult{}, errors.New("session has no messages to rewind")
	}
	remainingMessages := totalMessages - removeMessages
	if remainingMessages < 0 {
		remainingMessages = 0
	}
	var kept []Record
	seenMessages := 0
	for _, record := range records {
		if seenMessages >= remainingMessages {
			break
		}
		if record.Message != nil {
			seenMessages++
		}
		kept = append(kept, record)
	}
	if err := s.writeRecords(path, kept); err != nil {
		return RewindResult{}, err
	}
	return RewindResult{
		SessionID:         id,
		Path:              path,
		OriginalMessages:  totalMessages,
		RemainingMessages: remainingMessages,
		RemovedMessages:   totalMessages - remainingMessages,
	}, nil
}

func (s *Store) ReplaceMessages(sess *Session, messages []anthropic.Message) (ReplaceResult, error) {
	if sess == nil {
		return ReplaceResult{}, errors.New("session is required")
	}
	if strings.TrimSpace(sess.ID) == "" {
		return ReplaceResult{}, errors.New("session id is required")
	}
	path := sess.Path
	if path == "" {
		path = s.pathFor(sess.ID)
	}
	records := make([]Record, 0, len(messages))
	for _, msg := range messages {
		next := msg
		records = append(records, Record{
			Type:      "message",
			Time:      time.Now().UTC(),
			Message:   &next,
			SessionID: sess.ID,
		})
	}
	if err := s.writeRecords(path, records); err != nil {
		return ReplaceResult{}, err
	}
	original := len(sess.Messages)
	sess.Messages = append([]anthropic.Message(nil), messages...)
	return ReplaceResult{
		SessionID:         sess.ID,
		Path:              path,
		OriginalMessages:  original,
		RemainingMessages: len(messages),
		RemovedMessages:   original - len(messages),
	}, nil
}

func (s *Store) pathFor(id string) string {
	path := filepath.Join(s.Dir, id+".jsonl")
	if s.LegacyDir == "" || sameDir(s.Dir, s.LegacyDir) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	legacy := filepath.Join(s.LegacyDir, id+".jsonl")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return path
}

func (s *Store) readMessages(path string) ([]anthropic.Message, error) {
	records, err := s.readRecords(path)
	if err != nil {
		return nil, err
	}
	var messages []anthropic.Message
	for _, record := range records {
		if record.Message != nil {
			messages = append(messages, *record.Message)
		}
	}
	return messages, nil
}

func (s *Store) readRecords(path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var records []Record
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) writeRecords(path string, records []Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rewind-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	for _, record := range records {
		if err := writeRecord(tmp, record); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeRecord(file *os.File, record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func newID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%06d", now.Format("20060102T150405Z"), now.Nanosecond()/1000)
}

func canonicalWorkspace(workspace string) string {
	if workspace == "" {
		return ""
	}
	abs, err := filepath.Abs(workspace)
	if err == nil {
		workspace = abs
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return filepath.Clean(workspace)
	}
	return canonical
}

func WorkspaceFingerprint(workspace string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(filepath.Clean(workspace)))
	return fmt.Sprintf("%016x", hash.Sum64())
}

func (s *Store) sessionDirs() []string {
	dirs := []string{s.Dir}
	if s.LegacyDir != "" && !sameDir(s.Dir, s.LegacyDir) {
		dirs = append(dirs, s.LegacyDir)
	}
	return dirs
}

func sameDir(left string, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err == nil {
		left = leftAbs
	}
	rightAbs, err := filepath.Abs(right)
	if err == nil {
		right = rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
