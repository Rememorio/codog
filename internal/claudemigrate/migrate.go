package claudemigrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
)

const (
	// DefaultMaxSessions bounds the number of recent Claude Code sessions
	// selected by one migration.
	DefaultMaxSessions = 50
	// DefaultMaxAge bounds session discovery to recently modified transcripts.
	DefaultMaxAge = 30 * 24 * time.Hour
)

// Options controls Claude Code asset discovery and session migration.
type Options struct {
	SourceHome   string
	Workspace    string
	SessionStore *session.Store
	MaxSessions  int
	MaxAge       time.Duration
	Apply        bool
}

// Asset describes one class of Claude Code data visible to Codog.
type Asset struct {
	Kind    string   `json:"kind"`
	Mode    string   `json:"mode"`
	Count   int      `json:"count"`
	Sources []string `json:"sources,omitempty"`
}

// SessionResult describes one discovered Claude Code transcript.
type SessionResult struct {
	Source       string `json:"source"`
	SessionID    string `json:"session_id,omitempty"`
	ModifiedAt   string `json:"modified_at,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Target       string `json:"target,omitempty"`
}

// Report is the stable machine-readable result of a migration inspection or run.
type Report struct {
	Kind               string          `json:"kind"`
	Action             string          `json:"action"`
	Status             string          `json:"status"`
	SourceHome         string          `json:"source_home"`
	Workspace          string          `json:"workspace"`
	Assets             []Asset         `json:"assets"`
	SessionsDiscovered int             `json:"sessions_discovered"`
	SessionsEligible   int             `json:"sessions_eligible"`
	SessionsImported   int             `json:"sessions_imported"`
	SessionsSkipped    int             `json:"sessions_skipped"`
	SessionsFailed     int             `json:"sessions_failed"`
	Sessions           []SessionResult `json:"sessions"`
	Notes              []string        `json:"notes"`
}

type candidate struct {
	path       string
	id         string
	workspace  string
	modifiedAt time.Time
	messages   []importMessage
}

type importMessage struct {
	message anthropic.Message
	usage   *anthropic.Usage
	input   string
}

type rawRecord struct {
	Type        string          `json:"type"`
	CWD         string          `json:"cwd"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	UUID        string          `json:"uuid"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type rawMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   anthropic.Usage `json:"usage"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    json.RawMessage `json:"source"`
}

// DefaultSourceHome returns the Claude Code data directory selected by the
// standard environment variable or the current user's home directory.
func DefaultSourceHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// Run discovers compatible assets and optionally imports eligible sessions.
func Run(opts Options) (Report, error) {
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Kind:       "claude_migration",
		Action:     "status",
		Status:     "ready",
		SourceHome: normalized.SourceHome,
		Workspace:  normalized.Workspace,
		Sessions:   []SessionResult{},
		Notes: []string{
			"Project rules, settings, skills, commands, agents, hooks, and MCP configuration remain loaded from their Claude Code paths.",
			"Session import is local, workspace-scoped, and idempotent; existing Codog session ids are not overwritten.",
		},
	}
	if normalized.Apply {
		report.Action = "run"
	}
	info, err := os.Stat(normalized.SourceHome)
	if errors.Is(err, fs.ErrNotExist) {
		report.Status = "not_found"
		report.Assets = []Asset{}
		return report, nil
	}
	if err != nil {
		return Report{}, fmt.Errorf("inspect Claude Code home: %w", err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("source home is not a directory: %s", normalized.SourceHome)
	}

	report.Assets, err = discoverAssets(normalized.SourceHome, normalized.Workspace)
	if err != nil {
		return Report{}, err
	}
	candidates, failures, err := discoverSessions(normalized)
	if err != nil {
		return Report{}, err
	}
	report.SessionsDiscovered = len(candidates) + len(failures)
	report.SessionsFailed = len(failures)
	report.Sessions = append(report.Sessions, failures...)
	for _, item := range candidates {
		result := SessionResult{
			Source:       item.path,
			SessionID:    item.id,
			ModifiedAt:   item.modifiedAt.UTC().Format(time.RFC3339),
			MessageCount: len(item.messages),
			Status:       "eligible",
		}
		report.SessionsEligible++
		if normalized.SessionStore != nil {
			exists, existsErr := normalized.SessionStore.Exists(item.id)
			if existsErr != nil {
				result.Status = "failed"
				result.Reason = existsErr.Error()
				report.SessionsFailed++
				report.Sessions = append(report.Sessions, result)
				continue
			}
			if exists {
				result.Status = "skipped"
				result.Reason = "session already exists"
				report.SessionsSkipped++
				report.Sessions = append(report.Sessions, result)
				continue
			}
		}
		if normalized.Apply {
			target, importErr := importCandidate(normalized.SessionStore, normalized.Workspace, item)
			if importErr != nil {
				result.Status = "failed"
				result.Reason = importErr.Error()
				report.SessionsFailed++
			} else {
				result.Status = "imported"
				result.Target = target
				report.SessionsImported++
			}
		}
		report.Sessions = append(report.Sessions, result)
	}
	report.Status = migrationStatus(report)
	return report, nil
}

func normalizeOptions(opts Options) (Options, error) {
	var err error
	opts.SourceHome = strings.TrimSpace(opts.SourceHome)
	if opts.SourceHome == "" {
		opts.SourceHome, err = DefaultSourceHome()
		if err != nil {
			return Options{}, err
		}
	}
	opts.SourceHome, err = filepath.Abs(opts.SourceHome)
	if err != nil {
		return Options{}, fmt.Errorf("resolve Claude Code home: %w", err)
	}
	opts.Workspace = strings.TrimSpace(opts.Workspace)
	if opts.Workspace == "" {
		return Options{}, errors.New("workspace is required")
	}
	opts.Workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return Options{}, fmt.Errorf("resolve workspace: %w", err)
	}
	opts.Workspace = canonicalPath(opts.Workspace)
	if opts.MaxSessions < 0 {
		return Options{}, errors.New("max sessions must be non-negative")
	}
	if opts.MaxSessions == 0 {
		opts.MaxSessions = DefaultMaxSessions
	}
	if opts.MaxAge < 0 {
		return Options{}, errors.New("max age must be non-negative")
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}
	if opts.Apply && opts.SessionStore == nil {
		return Options{}, errors.New("session store is required when applying a migration")
	}
	return opts, nil
}

func migrationStatus(report Report) string {
	switch {
	case report.SessionsFailed > 0 && report.SessionsImported > 0:
		return "partial"
	case report.SessionsFailed > 0:
		return "error"
	case report.Action == "run" && report.SessionsImported > 0:
		return "imported"
	case report.SessionsEligible == 0 || report.SessionsSkipped == report.SessionsEligible:
		return "up_to_date"
	default:
		return "ready"
	}
}

func sameWorkspace(left, right string) bool {
	return canonicalPath(left) == canonicalPath(right)
}

func canonicalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

// ParseMaxAge converts a non-negative day count into a discovery duration.
func ParseMaxAge(value string) (time.Duration, error) {
	days, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || days < 0 {
		return 0, fmt.Errorf("max age must be a non-negative day count")
	}
	return time.Duration(days) * 24 * time.Hour, nil
}
