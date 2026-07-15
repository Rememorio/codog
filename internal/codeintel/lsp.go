package codeintel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/background"
)

// LSPCandidate describes a language-server command Codog can discover.
type LSPCandidate struct {
	Language    string   `json:"language"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Installed   bool     `json:"installed"`
	Path        string   `json:"path,omitempty"`
	Description string   `json:"description,omitempty"`
}

// LSPServer records a configured language server. New records use lazy mode:
// Start verifies the protocol handshake, while long-lived Codog runtimes open
// and reuse the process on the first query.
type LSPServer struct {
	Language  string    `json:"language"`
	Command   string    `json:"command"`
	TaskID    string    `json:"task_id"`
	Mode      string    `json:"mode,omitempty"`
	Workspace string    `json:"workspace"`
	StartedAt time.Time `json:"started_at"`
}

// LSPServerStatus combines persisted server metadata with task state.
type LSPServerStatus struct {
	LSPServer
	Task background.Task `json:"task"`
}

// LSPStore persists language-server metadata under the Codog config home.
type LSPStore struct {
	ConfigHome string
	Workspace  string
}

// NewLSPStore returns a store for workspace language-server metadata.
func NewLSPStore(configHome, workspace string) LSPStore {
	return LSPStore{ConfigHome: configHome, Workspace: workspace}
}

// DefaultLSPCandidates returns common language-server commands and install state.
func DefaultLSPCandidates() []LSPCandidate {
	candidates := []LSPCandidate{
		{Language: "go", Command: "gopls", Description: "Go language server"},
		{Language: "python", Command: "pyright-langserver", Args: []string{"--stdio"}, Description: "Python language server"},
		{Language: "typescript", Command: "typescript-language-server", Args: []string{"--stdio"}, Description: "TypeScript language server"},
		{Language: "javascript", Command: "typescript-language-server", Args: []string{"--stdio"}, Description: "JavaScript language server"},
		{Language: "rust", Command: "rust-analyzer", Description: "Rust language server"},
	}
	for i := range candidates {
		if path, err := exec.LookPath(candidates[i].Command); err == nil {
			candidates[i].Installed = true
			candidates[i].Path = path
		}
	}
	return candidates
}

// Start verifies and records a language server for lazy session reuse.
func (s LSPStore) Start(language string, commandArgs []string) (LSPServerStatus, error) {
	language, err := normalizeLanguage(language)
	if err != nil {
		return LSPServerStatus{}, err
	}
	if existing, err := s.Status(language); err == nil && (existing.Task.Status == "ready" || background.IsActiveStatus(existing.Task.Status)) {
		return LSPServerStatus{}, fmt.Errorf("lsp server %q is already configured", language)
	}
	command, err := lspCommand(language, commandArgs)
	if err != nil {
		return LSPServerStatus{}, err
	}
	workspace := s.Workspace
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return LSPServerStatus{}, err
		}
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := startLSPProcess(probeCtx, workspace, command)
	if err != nil {
		return LSPServerStatus{}, fmt.Errorf("initialize lsp server %q: %w", language, err)
	}
	if err := process.close(true); err != nil {
		return LSPServerStatus{}, fmt.Errorf("close lsp server probe %q: %w", language, err)
	}
	startedAt := time.Now().UTC()
	server := LSPServer{
		Language:  language,
		Command:   command,
		Mode:      "lazy",
		Workspace: workspace,
		StartedAt: startedAt,
	}
	if err := s.save(server); err != nil {
		return LSPServerStatus{}, err
	}
	return LSPServerStatus{LSPServer: server, Task: readyLSPTask(server)}, nil
}

// List returns all persisted language-server statuses.
func (s LSPStore) List() ([]LSPServerStatus, error) {
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		if os.IsNotExist(err) {
			return []LSPServerStatus{}, nil
		}
		return nil, err
	}
	statuses := []LSPServerStatus{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		language := strings.TrimSuffix(entry.Name(), ".json")
		status, err := s.Status(language)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Language < statuses[j].Language
	})
	return statuses, nil
}

// Status returns persisted metadata and task state for a language server.
func (s LSPStore) Status(language string) (LSPServerStatus, error) {
	language, err := normalizeLanguage(language)
	if err != nil {
		return LSPServerStatus{}, err
	}
	server, err := s.load(language)
	if err != nil {
		return LSPServerStatus{}, err
	}
	if server.TaskID == "" {
		return LSPServerStatus{LSPServer: server, Task: readyLSPTask(server)}, nil
	}
	task, err := background.NewStore(s.ConfigHome).Status(server.TaskID)
	if err != nil {
		return LSPServerStatus{}, err
	}
	return LSPServerStatus{LSPServer: server, Task: task}, nil
}

// Stop removes a language-server registration or stops a legacy background
// record created by an older Codog release.
func (s LSPStore) Stop(language string) (LSPServerStatus, error) {
	status, err := s.Status(language)
	if err != nil {
		return LSPServerStatus{}, err
	}
	if status.TaskID == "" {
		completedAt := time.Now().UTC()
		status.Task.Status = "stopped"
		status.Task.CompletedAt = &completedAt
		if err := os.Remove(s.path(status.Language)); err != nil && !os.IsNotExist(err) {
			return LSPServerStatus{}, err
		}
		return status, nil
	}
	task, err := background.NewStore(s.ConfigHome).Stop(status.TaskID)
	if err != nil {
		return LSPServerStatus{}, err
	}
	status.Task = task
	return status, nil
}

func readyLSPTask(server LSPServer) background.Task {
	return background.Task{
		Kind:      "lsp",
		Command:   server.Command,
		Workspace: server.Workspace,
		Status:    "ready",
		StartedAt: server.StartedAt,
	}
}

func (s LSPStore) save(server LSPServer) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(server.Language), append(data, '\n'), 0o644)
}

func (s LSPStore) load(language string) (LSPServer, error) {
	data, err := os.ReadFile(s.path(language))
	if err != nil {
		return LSPServer{}, err
	}
	var server LSPServer
	if err := json.Unmarshal(data, &server); err != nil {
		return LSPServer{}, err
	}
	return server, nil
}

func (s LSPStore) loadForQuery(language string) (LSPServer, error) {
	server, err := s.load(language)
	if err != nil || server.TaskID == "" {
		return server, err
	}
	legacyStore := background.NewStore(s.ConfigHome)
	if task, statusErr := legacyStore.Status(server.TaskID); statusErr == nil && background.IsActiveStatus(task.Status) {
		_, _ = legacyStore.Stop(server.TaskID)
	}
	server.TaskID = ""
	server.Mode = "lazy"
	if err := s.save(server); err != nil {
		return LSPServer{}, err
	}
	return server, nil
}

func (s LSPStore) dir() string {
	return filepath.Join(s.ConfigHome, "code-intel", "lsp")
}

func (s LSPStore) path(language string) string {
	return filepath.Join(s.dir(), language+".json")
}

func lspCommand(language string, commandArgs []string) (string, error) {
	if len(commandArgs) == 0 {
		for _, candidate := range DefaultLSPCandidates() {
			if candidate.Language == language {
				if !candidate.Installed {
					return "", fmt.Errorf("default lsp command %q was not found; pass COMMAND explicitly", candidate.Command)
				}
				return shellCommand(append([]string{candidate.Command}, candidate.Args...)), nil
			}
		}
		return "", fmt.Errorf("no default lsp command for language %q", language)
	}
	return shellCommand(commandArgs), nil
}

func normalizeLanguage(language string) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		return "", errors.New("language is required")
	}
	for _, r := range language {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return "", errors.New("language must be a single safe name")
	}
	return language, nil
}

func shellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
