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
	Identity        *SessionIdentity   `json:"identity,omitempty"`
	Usage           *anthropic.Usage   `json:"usage,omitempty"`
	Input           string             `json:"input,omitempty"`
	SessionID       string             `json:"session_id,omitempty"`
	ParentSessionID string             `json:"parent_session_id,omitempty"`
	BranchName      string             `json:"branch_name,omitempty"`
	MessageIndex    int                `json:"message_index,omitempty"`
	Action          string             `json:"action,omitempty"`
}

type Session struct {
	ID       string
	Messages []anthropic.Message
	Path     string
	Identity SessionIdentity
	Metadata SessionMetadata
}

type SessionMetadata struct {
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	ModifiedAt      time.Time `json:"modified_at,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	BranchName      string    `json:"branch_name,omitempty"`
	PinnedMessages  []int     `json:"pinned_messages,omitempty"`
}

type SessionIdentity struct {
	Title        string                `json:"title,omitempty"`
	Workspace    string                `json:"workspace,omitempty"`
	Worktree     string                `json:"worktree,omitempty"`
	Purpose      string                `json:"purpose,omitempty"`
	Placeholders []IdentityPlaceholder `json:"placeholders,omitempty"`
}

type IdentityPlaceholder struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
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
	IdentityUpdates       int                      `json:"identity_updates"`
	SkippedWithInputs     int                      `json:"skipped_with_inputs"`
	SkippedDisabled       int                      `json:"skipped_disabled"`
	BackfilledSessions    []BackfilledSession      `json:"backfilled_sessions,omitempty"`
	SkippedSessionDetails []BackfillSkippedSession `json:"skipped_session_details,omitempty"`
}

type BackfilledSession struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	Inputs          int    `json:"inputs"`
	IdentityUpdated bool   `json:"identity_updated,omitempty"`
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

// PinResult describes a message pin mutation in a saved session.
type PinResult struct {
	SessionID      string `json:"session_id"`
	Path           string `json:"path"`
	Action         string `json:"action"`
	MessageIndex   int    `json:"message_index"`
	MessageCount   int    `json:"message_count"`
	PinnedMessages []int  `json:"pinned_messages"`
}

type messageRecordInfo struct {
	Time  time.Time
	Usage *anthropic.Usage
}

type Store struct {
	Dir                 string
	LegacyDir           string
	Workspace           string
	PersistenceDisabled bool
}

// ErrNoSessions reports that no saved session files are visible to the store.
var ErrNoSessions = errors.New("no saved sessions")

// ErrSessionNotFound reports that a requested saved session does not exist.
var ErrSessionNotFound = errors.New("session not found")

// ErrAllSessionsEmpty reports that saved sessions exist but none contain messages.
var ErrAllSessionsEmpty = errors.New("all saved sessions are empty")

// DefaultCleanupPeriodDays matches Claude Code's default transcript retention.
const DefaultCleanupPeriodDays = 30

const primarySessionExtension = ".jsonl"
const legacySessionExtension = ".json"

var sessionFileExtensions = []string{primarySessionExtension, legacySessionExtension}
var sessionReferenceAliases = map[string]struct{}{
	"latest": {},
	"last":   {},
	"recent": {},
}

// IsSessionReferenceAlias reports whether reference selects the newest session.
func IsSessionReferenceAlias(reference string) bool {
	_, ok := sessionReferenceAliases[strings.ToLower(strings.TrimSpace(reference))]
	return ok
}

type PathIsDirectoryError struct {
	Path string
}

func (e PathIsDirectoryError) Error() string {
	return fmt.Sprintf("session path is a directory: %s", e.Path)
}

// WorkspaceMismatchError reports an explicitly bound session from another workspace.
type WorkspaceMismatchError struct {
	Expected string
	Actual   string
	Path     string
}

func (e WorkspaceMismatchError) Error() string {
	return fmt.Sprintf("session workspace mismatch: expected %s, found %s", e.Expected, e.Actual)
}

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

// NewWorkspaceStoreWithCleanup opens a workspace store and applies transcript retention.
func NewWorkspaceStoreWithCleanup(configHome string, workspace string, cleanupPeriodDays int) (*Store, error) {
	store := NewWorkspaceStore(configHome, workspace)
	if err := store.ApplyCleanupPeriodDays(cleanupPeriodDays); err != nil {
		return nil, err
	}
	return store, nil
}

// ApplyCleanupPeriodDays removes expired transcripts or disables persistence when days is zero.
func (s *Store) ApplyCleanupPeriodDays(days int) error {
	if days < 0 {
		return errors.New("cleanup period days must be non-negative")
	}
	if days == 0 {
		s.PersistenceDisabled = true
		return s.RemoveAll()
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	return s.RemoveOlderThan(cutoff)
}

// RemoveOlderThan deletes transcript files whose modified time is older than cutoff.
func (s *Store) RemoveOlderThan(cutoff time.Time) error {
	return s.removeSessionFiles(func(info os.FileInfo) bool {
		return info.ModTime().UTC().Before(cutoff)
	})
}

// RemoveAll deletes all transcript files visible to the store.
func (s *Store) RemoveAll() error {
	return s.removeSessionFiles(func(os.FileInfo) bool { return true })
}

func (s *Store) Open(id string) (*Session, error) {
	if s.PersistenceDisabled {
		id = strings.TrimSpace(id)
		if id == "" {
			id = newID()
		}
		if IsSessionReferenceAlias(id) {
			return nil, ErrNoSessions
		}
		if err := validateSessionID(id); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		identity := normalizeSessionIdentity(id, s.Workspace, SessionIdentity{})
		return &Session{ID: id, Identity: identity, Metadata: SessionMetadata{CreatedAt: now, UpdatedAt: now, ModifiedAt: now}}, nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if id == "" {
		id = newID()
	}
	if IsSessionReferenceAlias(id) {
		latest, err := s.LatestSessionExcluding("")
		if err != nil {
			return nil, err
		}
		return latest, nil
	}
	path := s.pathFor(id)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return s.createAtPath(id, path, SessionIdentity{})
	}
	messages, identity, metadata, err := s.readSessionStrict(path, id)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Messages: messages, Path: path, Identity: identity, Metadata: metadata}, nil
}

func (s *Store) OpenExisting(id string) (*Session, error) {
	if s.PersistenceDisabled {
		return nil, ErrNoSessions
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
		latest, err := s.LatestSessionExcluding("")
		if err != nil {
			return nil, err
		}
		return latest, nil
	}
	if looksLikeSessionPath(id) {
		return s.openExistingPath(id)
	}
	path := s.pathFor(id)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, PathIsDirectoryError{Path: path}
	}
	messages, identity, metadata, err := s.readSessionStrict(path, id)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Messages: messages, Path: path, Identity: identity, Metadata: metadata}, nil
}

func (s *Store) openExistingPath(path string) (*Session, error) {
	path = strings.TrimSpace(path)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, path)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, PathIsDirectoryError{Path: path}
	}
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.TrimSpace(id) == "" {
		id = filepath.Base(path)
	}
	messages, identity, metadata, err := s.readSessionStrict(path, id)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Messages: messages, Path: path, Identity: identity, Metadata: metadata}, nil
}

func looksLikeSessionPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) || isSessionFileExtension(filepath.Ext(value))
}

func (s *Store) Create(id string) (*Session, error) {
	return s.CreateWithIdentity(id, SessionIdentity{})
}

func (s *Store) CreateWithIdentity(id string, identity SessionIdentity) (*Session, error) {
	if s.PersistenceDisabled {
		id = strings.TrimSpace(id)
		if id == "" {
			id = newID()
		}
		if IsSessionReferenceAlias(id) {
			return nil, fmt.Errorf("session id cannot be session alias %q", id)
		}
		if err := validateSessionID(id); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		resolved := normalizeSessionIdentity(id, s.Workspace, identity)
		return &Session{ID: id, Identity: resolved, Metadata: SessionMetadata{CreatedAt: now, UpdatedAt: now, ModifiedAt: now}}, nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = newID()
	}
	if IsSessionReferenceAlias(id) {
		return nil, fmt.Errorf("session id cannot be session alias %q", id)
	}
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	if exists, err := s.Exists(id); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("session %q already exists", id)
	}
	path := filepath.Join(s.Dir, id+primarySessionExtension)
	return s.createAtPath(id, path, identity)
}

func (s *Store) createAtPath(id string, path string, identity SessionIdentity) (*Session, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	now := time.Now().UTC()
	if err := writeRecord(file, Record{
		Type:      "session",
		Time:      now,
		SessionID: id,
	}); err != nil {
		return nil, err
	}
	resolved := normalizeSessionIdentity(id, s.Workspace, identity)
	if err := writeRecord(file, Record{
		Type:      "session_identity",
		Time:      now,
		SessionID: id,
		Identity:  &resolved,
	}); err != nil {
		return nil, err
	}
	return &Session{ID: id, Path: path, Identity: resolved, Metadata: SessionMetadata{CreatedAt: now, UpdatedAt: now, ModifiedAt: now}}, nil
}

func (s *Store) Append(id string, msg anthropic.Message) error {
	return s.AppendWithUsage(id, msg, nil)
}

func (s *Store) AppendWithUsage(id string, msg anthropic.Message, usage *anthropic.Usage) error {
	if s.PersistenceDisabled {
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
	if s.PersistenceDisabled {
		return nil
	}
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
	if s.PersistenceDisabled {
		return nil
	}
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
	if s.PersistenceDisabled {
		return false, nil
	}
	if strings.TrimSpace(id) == "" {
		return false, errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
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
	if s.PersistenceDisabled {
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
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

// PinMessage marks the zero-based message index as pinned for the session.
func (s *Store) PinMessage(id string, index int) (PinResult, error) {
	return s.setMessagePin(id, index, true)
}

// UnpinMessage removes a pin from the zero-based message index.
func (s *Store) UnpinMessage(id string, index int) (PinResult, error) {
	return s.setMessagePin(id, index, false)
}

func (s *Store) setMessagePin(id string, index int, pinned bool) (PinResult, error) {
	if index < 0 {
		return PinResult{}, errors.New("message index must be non-negative")
	}
	sess, err := s.OpenExisting(id)
	if err != nil {
		return PinResult{}, err
	}
	if index >= len(sess.Messages) {
		return PinResult{}, fmt.Errorf("message index %d out of range for %d messages", index, len(sess.Messages))
	}
	action := "pin"
	if !pinned {
		action = "unpin"
	}
	record := Record{
		Type:         "pin",
		Time:         time.Now().UTC(),
		SessionID:    sess.ID,
		MessageIndex: index,
		Action:       action,
	}
	file, err := os.OpenFile(sess.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return PinResult{}, err
	}
	defer file.Close()
	if err := writeRecord(file, record); err != nil {
		return PinResult{}, err
	}
	updated, err := s.OpenExisting(sess.ID)
	if err != nil {
		return PinResult{}, err
	}
	return PinResult{
		SessionID:      updated.ID,
		Path:           updated.Path,
		Action:         action,
		MessageIndex:   index,
		MessageCount:   len(updated.Messages),
		PinnedMessages: append([]int(nil), updated.Metadata.PinnedMessages...),
	}, nil
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
	if IsSessionReferenceAlias(oldID) {
		latest, err := s.LatestID()
		if err != nil {
			return RenameResult{}, err
		}
		oldID = latest
	}
	if err := validateSessionID(oldID); err != nil {
		return RenameResult{}, err
	}
	if IsSessionReferenceAlias(newID) {
		return RenameResult{}, fmt.Errorf("new session id cannot be session alias %q", newID)
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
	newPath := filepath.Join(s.Dir, newID+primarySessionExtension)
	records, err := s.readRecords(oldPath)
	if err != nil {
		return RenameResult{}, err
	}
	messageCount := 0
	for index := range records {
		if records[index].SessionID == "" || records[index].SessionID == oldID {
			records[index].SessionID = newID
		}
		if records[index].Identity != nil {
			identity := *records[index].Identity
			if strings.TrimSpace(identity.Title) == "" || identity.Title == oldID {
				identity.Title = newID
			}
			identity = normalizeSessionIdentity(newID, s.Workspace, identity)
			records[index].Identity = &identity
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
	path := filepath.Join(s.Dir, forkID+primarySessionExtension)
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
	purpose := "fork"
	if strings.TrimSpace(branchName) != "" {
		purpose = "fork:" + branchName
	}
	identity := normalizeSessionIdentity(forkID, s.Workspace, SessionIdentity{
		Title:   forkID,
		Purpose: purpose,
	})
	if err := writeRecord(file, Record{
		Type:      "session_identity",
		Time:      time.Now().UTC(),
		SessionID: forkID,
		Identity:  &identity,
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
	for _, index := range source.Metadata.PinnedMessages {
		if err := writeRecord(file, Record{
			Type:         "pin",
			Time:         time.Now().UTC(),
			SessionID:    forkID,
			MessageIndex: index,
			Action:       "pin",
		}); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	return &Session{ID: forkID, Messages: append([]anthropic.Message(nil), source.Messages...), Path: path, Identity: identity, Metadata: SessionMetadata{
		CreatedAt:       now,
		UpdatedAt:       now,
		ModifiedAt:      now,
		ParentSessionID: source.ID,
		BranchName:      branchName,
		PinnedMessages:  append([]int(nil), source.Metadata.PinnedMessages...),
	}}, nil
}

func (s *Store) List() ([]Session, error) {
	if s.PersistenceDisabled {
		return nil, nil
	}
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
		for _, extension := range sessionFileExtensions {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != extension {
					continue
				}
				id := sessionIDFromFileName(entry.Name())
				if _, ok := seen[id]; ok {
					continue
				}
				path := filepath.Join(dir, entry.Name())
				messages, identity, metadata, err := s.readSession(path, id)
				if err != nil {
					return nil, err
				}
				sessions = append(sessions, Session{ID: id, Path: path, Messages: messages, Identity: identity, Metadata: metadata})
				seen[id] = struct{}{}
			}
		}
	}
	sortSessions(sessions)
	return sessions, nil
}

// LatestID returns the newest visible session that contains at least one message.
func (s *Store) LatestID() (string, error) {
	return s.LatestIDExcluding("")
}

// LatestIDExcluding returns the newest non-empty session other than excludeID.
func (s *Store) LatestIDExcluding(excludeID string) (string, error) {
	sessions, err := s.List()
	if err != nil {
		return "", err
	}
	excludeID = strings.TrimSpace(excludeID)
	visible := 0
	for _, sess := range sessions {
		if excludeID != "" && sess.ID == excludeID {
			continue
		}
		visible++
		if len(sess.Messages) > 0 {
			return sess.ID, nil
		}
	}
	if visible == 0 {
		return "", ErrNoSessions
	}
	return "", ErrAllSessionsEmpty
}

// LatestSessionExcluding returns the newest non-empty session, falling back to
// sibling workspace namespaces when the current namespace has no usable match.
func (s *Store) LatestSessionExcluding(excludeID string) (*Session, error) {
	sessions, err := s.List()
	if err != nil {
		return nil, err
	}
	excludeID = strings.TrimSpace(excludeID)
	if latest, ok := latestSessionFrom(sessions, excludeID); ok {
		return &latest, nil
	}
	visible := visibleSessionCount(sessions, excludeID)
	global, err := s.globalWorkspaceSessions()
	if err != nil {
		return nil, err
	}
	if latest, ok := latestSessionFrom(global, excludeID); ok {
		return &latest, nil
	}
	visible += visibleSessionCount(global, excludeID)
	if visible == 0 {
		return nil, ErrNoSessions
	}
	return nil, ErrAllSessionsEmpty
}

// LatestAnyID returns the newest visible session, including empty transcripts.
func (s *Store) LatestAnyID() (string, error) {
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
	if IsSessionReferenceAlias(id) {
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

func (s *Store) Identity(id string) (SessionIdentity, error) {
	if strings.TrimSpace(id) == "" {
		return SessionIdentity{}, errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
		latest, err := s.LatestID()
		if err != nil {
			return SessionIdentity{}, err
		}
		id = latest
	}
	records, err := s.readRecords(s.pathFor(id))
	if err != nil {
		return SessionIdentity{}, err
	}
	return identityFromRecords(id, s.Workspace, records), nil
}

func (s *Store) UpdateIdentity(id string, update SessionIdentity) (SessionIdentity, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SessionIdentity{}, errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
		latest, err := s.LatestID()
		if err != nil {
			return SessionIdentity{}, err
		}
		id = latest
	}
	path := s.pathFor(id)
	records, err := s.readRecords(path)
	if err != nil {
		return SessionIdentity{}, err
	}
	current := identityFromRecords(id, s.Workspace, records)
	next := mergeSessionIdentity(current, update)
	next = normalizeSessionIdentity(id, s.Workspace, next)
	if reflect.DeepEqual(current, next) {
		return current, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return SessionIdentity{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return SessionIdentity{}, err
	}
	defer file.Close()
	if err := writeRecord(file, Record{
		Type:      "session_identity",
		Time:      time.Now().UTC(),
		SessionID: id,
		Identity:  &next,
	}); err != nil {
		return SessionIdentity{}, err
	}
	return next, nil
}

func (s *Store) Usage(id string) ([]UsageEntry, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("session id is required")
	}
	if IsSessionReferenceAlias(id) {
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
		identityRecord, identityUpdated := sessionIdentityBackfillRecord(sess.ID, s.Workspace, records)
		records = append(records, inputs...)
		if identityUpdated {
			records = append(records, identityRecord)
		}
		if err := s.writeRecords(sess.Path, records); err != nil {
			return BackfillReport{}, err
		}
		report.SessionsUpdated++
		report.InputsAdded += len(inputs)
		if identityUpdated {
			report.IdentityUpdates++
		}
		report.BackfilledSessions = append(report.BackfilledSessions, BackfilledSession{ID: sess.ID, Path: sess.Path, Inputs: len(inputs), IdentityUpdated: identityUpdated})
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
	if IsSessionReferenceAlias(id) {
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
	kept := preservedSessionRecords(records)
	seenMessages := 0
	oldToNewMessageIndex := map[int]int{}
	oldMessageIndex := -1
	for _, record := range records {
		if isSessionMetadataRecord(record.Type) {
			continue
		}
		if record.Message != nil {
			oldMessageIndex++
		}
		if record.Type == "pin" {
			continue
		}
		if seenMessages >= remainingMessages {
			break
		}
		if record.Message != nil {
			oldToNewMessageIndex[oldMessageIndex] = seenMessages
			seenMessages++
		}
		kept = append(kept, record)
	}
	kept = append(kept, remappedPinRecords(records, id, oldToNewMessageIndex)...)
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

func sessionIdentityBackfillRecord(id string, workspace string, records []Record) (Record, bool) {
	var identity SessionIdentity
	hadExplicitIdentity := false
	hadExplicitTitle := false
	hadExplicitPurpose := false
	for _, record := range records {
		if record.Type != "session_identity" || record.Identity == nil {
			continue
		}
		hadExplicitIdentity = true
		identity = *record.Identity
		hadExplicitTitle = strings.TrimSpace(identity.Title) != ""
		hadExplicitPurpose = strings.TrimSpace(identity.Purpose) != ""
	}
	prompt := firstPromptTextFromRecords(records)
	if prompt == "" {
		return Record{}, false
	}
	next := normalizeSessionIdentity(id, workspace, identity)
	updated := false
	if shouldEnrichIdentityTitle(next.Title, id, hadExplicitIdentity, hadExplicitTitle) {
		title := sessionIdentityTitleFromText(prompt)
		if strings.TrimSpace(title) != "" && title != next.Title {
			next.Title = title
			updated = true
		}
	}
	if !hadExplicitPurpose && strings.TrimSpace(next.Purpose) == "" {
		purpose := sessionIdentityPurposeFromText(prompt)
		if strings.TrimSpace(purpose) != "" {
			next.Purpose = purpose
			updated = true
		}
	}
	next = normalizeSessionIdentity(id, workspace, next)
	if !updated {
		return Record{}, false
	}
	return Record{
		Type:      "session_identity",
		Time:      time.Now().UTC(),
		SessionID: id,
		Identity:  &next,
	}, true
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
	existingRecords, err := s.readRecords(path)
	if err != nil {
		return ReplaceResult{}, err
	}
	metadata := messageRecordMetadata(existingRecords)
	records := preservedSessionRecords(existingRecords)
	preservedCount := len(records)
	searchFrom := 0
	oldToNewMessageIndex := map[int]int{}
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
			oldToNewMessageIndex[index] = len(records) - preservedCount
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
	records = append(records, remappedPinRecords(existingRecords, sess.ID, oldToNewMessageIndex)...)
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
	return messageRecordMetadata(records)
}

func messageRecordMetadata(records []Record) map[int]messageRecordInfo {
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

func remappedPinRecords(records []Record, sessionID string, oldToNew map[int]int) []Record {
	pinned := pinnedMessagesFromRecords(records, -1)
	if len(pinned) == 0 {
		return nil
	}
	out := []Record{}
	for _, oldIndex := range pinned {
		newIndex, ok := oldToNew[oldIndex]
		if !ok {
			continue
		}
		out = append(out, Record{
			Type:         "pin",
			Time:         time.Now().UTC(),
			SessionID:    sessionID,
			MessageIndex: newIndex,
			Action:       "pin",
		})
	}
	return out
}

func pinnedMessagesFromRecords(records []Record, messageCount int) []int {
	pinned := map[int]bool{}
	for _, record := range records {
		if record.Type != "pin" {
			continue
		}
		if record.MessageIndex < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(record.Action)) {
		case "", "pin":
			pinned[record.MessageIndex] = true
		case "unpin":
			delete(pinned, record.MessageIndex)
		}
	}
	indexes := make([]int, 0, len(pinned))
	for index := range pinned {
		if messageCount >= 0 && index >= messageCount {
			continue
		}
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func preservedSessionRecords(records []Record) []Record {
	preserved := make([]Record, 0, len(records))
	for _, record := range records {
		if isSessionMetadataRecord(record.Type) {
			preserved = append(preserved, record)
		}
	}
	return preserved
}

func isSessionMetadataRecord(recordType string) bool {
	switch recordType {
	case "session", "fork", "session_identity":
		return true
	default:
		return false
	}
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
	for _, dir := range []string{s.Dir, s.LegacyDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, extension := range sessionFileExtensions {
			path := filepath.Join(dir, id+extension)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return filepath.Join(s.Dir, id+primarySessionExtension)
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

func (s *Store) readSession(path string, id string) ([]anthropic.Message, SessionIdentity, SessionMetadata, error) {
	return s.readSessionWithWorkspaceMode(path, id, true)
}

func (s *Store) readSessionStrict(path string, id string) ([]anthropic.Message, SessionIdentity, SessionMetadata, error) {
	return s.readSessionWithWorkspaceMode(path, id, false)
}

func (s *Store) readSessionWithWorkspaceMode(path string, id string, allowWorkspaceMismatch bool) ([]anthropic.Message, SessionIdentity, SessionMetadata, error) {
	records, err := s.readRecords(path)
	if err != nil {
		return nil, SessionIdentity{}, SessionMetadata{}, err
	}
	if !allowWorkspaceMismatch {
		if err := s.validateSessionWorkspace(path, records); err != nil {
			return nil, SessionIdentity{}, SessionMetadata{}, err
		}
	}
	var messages []anthropic.Message
	for _, record := range records {
		if record.Message != nil {
			messages = append(messages, *record.Message)
		}
	}
	return messages, identityFromRecords(id, s.Workspace, records), metadataFromRecords(path, records), nil
}

func (s *Store) validateSessionWorkspace(path string, records []Record) error {
	expected := canonicalWorkspace(s.Workspace)
	if strings.TrimSpace(expected) == "" {
		return nil
	}
	for _, record := range records {
		if record.Type != "session_identity" || record.Identity == nil {
			continue
		}
		actual := canonicalWorkspace(record.Identity.Workspace)
		if strings.TrimSpace(actual) == "" || sameDir(expected, actual) {
			return nil
		}
		return WorkspaceMismatchError{Expected: expected, Actual: actual, Path: path}
	}
	return nil
}

func metadataFromRecords(path string, records []Record) SessionMetadata {
	var metadata SessionMetadata
	messageCount := 0
	for _, record := range records {
		if record.Message != nil {
			messageCount++
		}
		if !record.Time.IsZero() {
			at := record.Time.UTC()
			if metadata.CreatedAt.IsZero() || at.Before(metadata.CreatedAt) {
				metadata.CreatedAt = at
			}
			if metadata.UpdatedAt.IsZero() || at.After(metadata.UpdatedAt) {
				metadata.UpdatedAt = at
			}
		}
		if metadata.ParentSessionID == "" && strings.TrimSpace(record.ParentSessionID) != "" {
			metadata.ParentSessionID = strings.TrimSpace(record.ParentSessionID)
		}
		if metadata.BranchName == "" && strings.TrimSpace(record.BranchName) != "" {
			metadata.BranchName = strings.TrimSpace(record.BranchName)
		}
	}
	metadata.PinnedMessages = pinnedMessagesFromRecords(records, messageCount)
	if info, err := os.Stat(path); err == nil {
		modified := info.ModTime().UTC()
		metadata.ModifiedAt = modified
		if metadata.CreatedAt.IsZero() {
			metadata.CreatedAt = modified
		}
		if metadata.UpdatedAt.IsZero() {
			metadata.UpdatedAt = modified
		}
	}
	return metadata
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

func identityFromRecords(id string, workspace string, records []Record) SessionIdentity {
	var identity SessionIdentity
	hadExplicitIdentity := false
	hadExplicitTitle := false
	hadExplicitPurpose := false
	for _, record := range records {
		if record.Type != "session_identity" || record.Identity == nil {
			continue
		}
		hadExplicitIdentity = true
		identity = *record.Identity
		hadExplicitTitle = strings.TrimSpace(identity.Title) != ""
		hadExplicitPurpose = strings.TrimSpace(identity.Purpose) != ""
	}
	identity = normalizeSessionIdentity(id, workspace, identity)
	prompt := firstPromptTextFromRecords(records)
	if prompt != "" {
		if shouldEnrichIdentityTitle(identity.Title, id, hadExplicitIdentity, hadExplicitTitle) {
			identity.Title = sessionIdentityTitleFromText(prompt)
		}
		if !hadExplicitPurpose && strings.TrimSpace(identity.Purpose) == "" {
			identity.Purpose = sessionIdentityPurposeFromText(prompt)
		}
	}
	identity = normalizeSessionIdentity(id, workspace, identity)
	return identity
}

func mergeSessionIdentity(base SessionIdentity, update SessionIdentity) SessionIdentity {
	if strings.TrimSpace(update.Title) != "" {
		base.Title = strings.TrimSpace(update.Title)
	}
	if strings.TrimSpace(update.Workspace) != "" {
		base.Workspace = strings.TrimSpace(update.Workspace)
	}
	if strings.TrimSpace(update.Worktree) != "" {
		base.Worktree = strings.TrimSpace(update.Worktree)
	}
	if strings.TrimSpace(update.Purpose) != "" {
		base.Purpose = strings.TrimSpace(update.Purpose)
	}
	return base
}

func normalizeSessionIdentity(id string, workspace string, identity SessionIdentity) SessionIdentity {
	identity.Title = strings.TrimSpace(identity.Title)
	if identity.Title == "" {
		identity.Title = strings.TrimSpace(id)
	}
	identity.Workspace = canonicalWorkspace(firstSessionIdentityValue(identity.Workspace, workspace))
	identity.Worktree = canonicalWorkspace(firstSessionIdentityValue(identity.Worktree, identity.Workspace))
	identity.Purpose = strings.TrimSpace(identity.Purpose)
	identity.Placeholders = sessionIdentityPlaceholders(identity)
	return identity
}

func firstSessionIdentityValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPromptTextFromRecords(records []Record) string {
	for _, record := range records {
		if record.Type != "input" {
			continue
		}
		if text := normalizePromptText(record.Input); text != "" {
			return text
		}
	}
	for _, record := range records {
		if record.Message == nil || record.Message.Role != "user" {
			continue
		}
		for _, block := range record.Message.Content {
			if block.Type != "text" {
				continue
			}
			if text := normalizePromptText(block.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

func shouldEnrichIdentityTitle(title string, id string, hadExplicitIdentity bool, hadExplicitTitle bool) bool {
	title = strings.TrimSpace(title)
	id = strings.TrimSpace(id)
	if title == "" {
		return true
	}
	if !hadExplicitIdentity {
		return true
	}
	return !hadExplicitTitle || title == id
}

func sessionIdentityTitleFromText(text string) string {
	text = normalizePromptText(text)
	const maxTitleRunes = 80
	runes := []rune(text)
	if len(runes) <= maxTitleRunes {
		return text
	}
	return string(runes[:maxTitleRunes])
}

func sessionIdentityPurposeFromText(text string) string {
	text = normalizePromptText(text)
	const maxPurposeRunes = 200
	runes := []rune(text)
	if len(runes) <= maxPurposeRunes {
		return text
	}
	return string(runes[:maxPurposeRunes])
}

func normalizePromptText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func sessionIdentityPlaceholders(identity SessionIdentity) []IdentityPlaceholder {
	placeholders := []IdentityPlaceholder{}
	if strings.TrimSpace(identity.Title) == "" {
		placeholders = append(placeholders, IdentityPlaceholder{Field: "title", Reason: "session_id_empty"})
	}
	if strings.TrimSpace(identity.Workspace) == "" {
		placeholders = append(placeholders, IdentityPlaceholder{Field: "workspace", Reason: "workspace_not_configured"})
	}
	if strings.TrimSpace(identity.Worktree) == "" {
		placeholders = append(placeholders, IdentityPlaceholder{Field: "worktree", Reason: "workspace_not_configured"})
	}
	if strings.TrimSpace(identity.Purpose) == "" {
		placeholders = append(placeholders, IdentityPlaceholder{Field: "purpose", Reason: "purpose_not_provided"})
	}
	return placeholders
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

func (s *Store) globalWorkspaceSessions() ([]Session, error) {
	if strings.TrimSpace(s.LegacyDir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.LegacyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.LegacyDir, entry.Name())
		items, err := s.sessionsInDir(dir)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, items...)
	}
	sortSessions(sessions)
	return sessions, nil
}

func (s *Store) sessionsInDir(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sessions := make([]Session, 0, len(entries))
	seen := map[string]struct{}{}
	for _, extension := range sessionFileExtensions {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != extension {
				continue
			}
			id := sessionIDFromFileName(entry.Name())
			if _, ok := seen[id]; ok {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			messages, identity, metadata, err := s.readSession(path, id)
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, Session{ID: id, Path: path, Messages: messages, Identity: identity, Metadata: metadata})
			seen[id] = struct{}{}
		}
	}
	return sessions, nil
}

func latestSessionFrom(sessions []Session, excludeID string) (Session, bool) {
	for _, sess := range sessions {
		if excludeID != "" && sess.ID == excludeID {
			continue
		}
		if len(sess.Messages) > 0 {
			return sess, true
		}
	}
	return Session{}, false
}

func visibleSessionCount(sessions []Session, excludeID string) int {
	count := 0
	for _, sess := range sessions {
		if excludeID != "" && sess.ID == excludeID {
			continue
		}
		count++
	}
	return count
}

func sortSessions(sessions []Session) {
	sort.Slice(sessions, func(i, j int) bool {
		left := sessions[i]
		right := sessions[j]
		if !left.Metadata.UpdatedAt.Equal(right.Metadata.UpdatedAt) {
			return left.Metadata.UpdatedAt.After(right.Metadata.UpdatedAt)
		}
		if !left.Metadata.ModifiedAt.Equal(right.Metadata.ModifiedAt) {
			return left.Metadata.ModifiedAt.After(right.Metadata.ModifiedAt)
		}
		return left.ID > right.ID
	})
}

func isSessionFileName(name string) bool {
	return isSessionFileExtension(filepath.Ext(name))
}

func isSessionFileExtension(extension string) bool {
	for _, candidate := range sessionFileExtensions {
		if strings.EqualFold(extension, candidate) {
			return true
		}
	}
	return false
}

func sessionIDFromFileName(name string) string {
	extension := filepath.Ext(name)
	if isSessionFileExtension(extension) {
		return strings.TrimSuffix(name, extension)
	}
	return strings.TrimSuffix(name, primarySessionExtension)
}

func (s *Store) removeSessionFiles(shouldRemove func(os.FileInfo) bool) error {
	if shouldRemove == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, root := range s.sessionDirs() {
		if strings.TrimSpace(root) == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !isSessionFileName(entry.Name()) {
				return nil
			}
			clean := filepath.Clean(path)
			if _, ok := seen[clean]; ok {
				return nil
			}
			seen[clean] = struct{}{}
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if shouldRemove(info) {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			return nil
		})
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
	}
	return nil
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
