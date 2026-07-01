package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
)

type Record struct {
	Type            string             `json:"type"`
	Time            time.Time          `json:"time"`
	Message         *anthropic.Message `json:"message,omitempty"`
	Usage           *anthropic.Usage   `json:"usage,omitempty"`
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

type UsageEntry struct {
	MessageIndex int             `json:"message_index"`
	Time         time.Time       `json:"time"`
	SessionID    string          `json:"session_id"`
	Usage        anthropic.Usage `json:"usage"`
}

type BackfillReport struct {
	Kind                  string                   `json:"kind"`
	Action                string                   `json:"action"`
	Status                string                   `json:"status"`
	SessionsScanned       int                      `json:"sessions_scanned"`
	SessionsUpdated       int                      `json:"sessions_updated"`
	InputsAdded           int                      `json:"inputs_added"`
	SkippedWithInputs     int                      `json:"skipped_with_inputs"`
	SkippedDisabled       int                      `json:"skipped_disabled"`
	BackfilledSessions    []BackfilledSession      `json:"backfilled_sessions,omitempty"`
	SkippedSessionDetails []BackfillSkippedSession `json:"skipped_session_details,omitempty"`
}

type BackfilledSession struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Inputs int    `json:"inputs"`
}

type BackfillSkippedSession struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
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

type RenameResult struct {
	OldID        string `json:"old_id"`
	NewID        string `json:"new_id"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	MessageCount int    `json:"message_count"`
}

type messageRecordInfo struct {
	Time  time.Time
	Usage *anthropic.Usage
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

func (s *Store) Create(id string) (*Session, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = newID()
	}
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	if exists, err := s.Exists(id); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("session %q already exists", id)
	}
	path := filepath.Join(s.Dir, id+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := writeRecord(file, Record{
		Type:      "session",
		Time:      time.Now().UTC(),
		SessionID: id,
	}); err != nil {
		return nil, err
	}
	return &Session{ID: id, Path: path}, nil
}

func (s *Store) Append(id string, msg anthropic.Message) error {
	return s.AppendWithUsage(id, msg, nil)
}

func (s *Store) AppendWithUsage(id string, msg anthropic.Message, usage *anthropic.Usage) error {
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
		Usage:     usage,
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

func (s *Store) AppendPromptHistoryDisabled(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("session id is required")
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
		Type:      "prompt_history",
		Time:      time.Now().UTC(),
		Input:     "disabled",
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

func (s *Store) Rename(oldID string, newID string) (RenameResult, error) {
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" {
		return RenameResult{}, errors.New("session id is required")
	}
	if newID == "" {
		return RenameResult{}, errors.New("new session id is required")
	}
	if oldID == "latest" {
		latest, err := s.LatestID()
		if err != nil {
			return RenameResult{}, err
		}
		oldID = latest
	}
	if err := validateSessionID(oldID); err != nil {
		return RenameResult{}, err
	}
	if newID == "latest" {
		return RenameResult{}, errors.New(`new session id cannot be "latest"`)
	}
	if err := validateSessionID(newID); err != nil {
		return RenameResult{}, err
	}
	if oldID == newID {
		return RenameResult{}, errors.New("new session id must differ from current id")
	}
	exists, err := s.Exists(oldID)
	if err != nil {
		return RenameResult{}, err
	}
	if !exists {
		return RenameResult{}, os.ErrNotExist
	}
	if exists, err := s.Exists(newID); err != nil {
		return RenameResult{}, err
	} else if exists {
		return RenameResult{}, fmt.Errorf("session %q already exists", newID)
	}
	oldPath := s.pathFor(oldID)
	newPath := filepath.Join(s.Dir, newID+".jsonl")
	records, err := s.readRecords(oldPath)
	if err != nil {
		return RenameResult{}, err
	}
	messageCount := 0
	for index := range records {
		if records[index].SessionID == "" || records[index].SessionID == oldID {
			records[index].SessionID = newID
		}
		if records[index].Message != nil {
			messageCount++
		}
	}
	if err := s.writeRecords(newPath, records); err != nil {
		return RenameResult{}, err
	}
	if err := os.Remove(oldPath); err != nil {
		return RenameResult{}, err
	}
	return RenameResult{
		OldID:        oldID,
		NewID:        newID,
		OldPath:      oldPath,
		NewPath:      newPath,
		MessageCount: messageCount,
	}, nil
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
		if record.Type == "prompt_history" && strings.EqualFold(strings.TrimSpace(record.Input), "disabled") {
			return nil, nil
		}
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

func (s *Store) Usage(id string) ([]UsageEntry, error) {
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
	entries := []UsageEntry{}
	messageIndex := -1
	for _, record := range records {
		if record.Message == nil {
			continue
		}
		messageIndex++
		if record.Usage == nil || usageEmpty(*record.Usage) {
			continue
		}
		entries = append(entries, UsageEntry{
			MessageIndex: messageIndex,
			Time:         record.Time,
			SessionID:    id,
			Usage:        *record.Usage,
		})
	}
	return entries, nil
}

func (s *Store) BackfillPromptHistory() (BackfillReport, error) {
	sessions, err := s.List()
	if err != nil {
		return BackfillReport{}, err
	}
	report := BackfillReport{
		Kind:            "backfill_sessions",
		Action:          "prompt_history",
		Status:          "ok",
		SessionsScanned: len(sessions),
	}
	for _, sess := range sessions {
		records, err := s.readRecords(sess.Path)
		if err != nil {
			return BackfillReport{}, err
		}
		hasInput := false
		disabled := false
		for _, record := range records {
			switch record.Type {
			case "input":
				if strings.TrimSpace(record.Input) != "" {
					hasInput = true
				}
			case "prompt_history":
				if strings.EqualFold(strings.TrimSpace(record.Input), "disabled") {
					disabled = true
				}
			}
		}
		if hasInput {
			report.SkippedWithInputs++
			report.SkippedSessionDetails = append(report.SkippedSessionDetails, BackfillSkippedSession{ID: sess.ID, Reason: "existing_inputs"})
			continue
		}
		if disabled {
			report.SkippedDisabled++
			report.SkippedSessionDetails = append(report.SkippedSessionDetails, BackfillSkippedSession{ID: sess.ID, Reason: "prompt_history_disabled"})
			continue
		}
		inputs := promptInputsFromRecords(sess.ID, records)
		if len(inputs) == 0 {
			continue
		}
		records = append(records, inputs...)
		if err := s.writeRecords(sess.Path, records); err != nil {
			return BackfillReport{}, err
		}
		report.SessionsUpdated++
		report.InputsAdded += len(inputs)
		report.BackfilledSessions = append(report.BackfilledSessions, BackfilledSession{ID: sess.ID, Path: sess.Path, Inputs: len(inputs)})
	}
	return report, nil
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

func usageEmpty(usage anthropic.Usage) bool {
	return usage.InputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == 0 &&
		usage.CacheReadInputTokens == 0
}

func promptInputsFromRecords(sessionID string, records []Record) []Record {
	inputs := []Record{}
	for _, record := range records {
		if record.Message == nil || record.Message.Role != "user" {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type != "text" || strings.TrimSpace(block.Text) == "" {
				continue
			}
			when := record.Time
			if when.IsZero() {
				when = time.Now().UTC()
			}
			inputs = append(inputs, Record{
				Type:      "input",
				Time:      when,
				Input:     strings.TrimSpace(block.Text),
				SessionID: sessionID,
			})
			break
		}
	}
	return inputs
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
	metadata := s.messageRecordMetadata(path)
	records := make([]Record, 0, len(messages))
	searchFrom := 0
	for _, msg := range messages {
		next := msg
		record := Record{
			Type:      "message",
			Time:      time.Now().UTC(),
			Message:   &next,
			SessionID: sess.ID,
		}
		if index := findMessageIndex(sess.Messages, msg, searchFrom); index >= 0 {
			searchFrom = index + 1
			if info, ok := metadata[index]; ok {
				if !info.Time.IsZero() {
					record.Time = info.Time
				}
				if info.Usage != nil && !usageEmpty(*info.Usage) {
					usage := *info.Usage
					record.Usage = &usage
				}
			}
		}
		records = append(records, record)
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

func (s *Store) messageRecordMetadata(path string) map[int]messageRecordInfo {
	records, err := s.readRecords(path)
	if err != nil {
		return nil
	}
	metadata := map[int]messageRecordInfo{}
	messageIndex := -1
	for _, record := range records {
		if record.Message == nil {
			continue
		}
		messageIndex++
		var usage *anthropic.Usage
		if record.Usage != nil && !usageEmpty(*record.Usage) {
			next := *record.Usage
			usage = &next
		}
		metadata[messageIndex] = messageRecordInfo{Time: record.Time, Usage: usage}
	}
	return metadata
}

func findMessageIndex(messages []anthropic.Message, target anthropic.Message, start int) int {
	if start < 0 {
		start = 0
	}
	for index := start; index < len(messages); index++ {
		if reflect.DeepEqual(messages[index], target) {
			return index
		}
	}
	return -1
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

func validateSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("session id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") || filepath.Base(id) != id {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
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
