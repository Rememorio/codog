package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bridge"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/hooks"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/workspaceops"
)

type Server struct {
	Sessions    *session.Store
	ConfigHome  string
	Workspace   string
	AuthToken   string
	Hooks       config.HookConfig
	LeaseTTL    time.Duration
	Executable  string
	EditorToken string
	Now         func() time.Time
}

type RouteSpec struct {
	Path        string   `json:"path"`
	Methods     []string `json:"methods"`
	Description string   `json:"description"`
	Public      bool     `json:"public,omitempty"`
	Streaming   bool     `json:"streaming,omitempty"`
}

func RouteSpecs() []RouteSpec {
	return []RouteSpec{
		{Path: "/health", Methods: []string{http.MethodGet}, Description: "Health check.", Public: true},
		{Path: "/state", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Read or update remote client heartbeat, failure, and lease state."},
		{Path: "/sessions", Methods: []string{http.MethodGet, http.MethodPost}, Description: "List sessions or create a new session."},
		{Path: "/sessions/{id}", Methods: []string{http.MethodGet}, Description: "Read one session."},
		{Path: "/sessions/{id}/messages", Methods: []string{http.MethodPost}, Description: "Append a message to a session."},
		{Path: "/sessions/{id}/input", Methods: []string{http.MethodPost}, Description: "Append raw user input to a session."},
		{Path: "/sessions/{id}/rewind", Methods: []string{http.MethodPost}, Description: "Rewind recent session messages."},
		{Path: "/sessions/{id}/prompt", Methods: []string{http.MethodPost}, Description: "Start a background prompt turn for a session."},
		{Path: "/sessions/{id}/background", Methods: []string{http.MethodGet}, Description: "List background tasks for a session."},
		{Path: "/workspace/info", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Read workspace metadata."},
		{Path: "/workspace/files", Methods: []string{http.MethodGet, http.MethodPost}, Description: "List workspace files."},
		{Path: "/workspace/search", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Search workspace files."},
		{Path: "/file/read", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Read a workspace-scoped file."},
		{Path: "/file/write", Methods: []string{http.MethodPost}, Description: "Write a workspace-scoped file."},
		{Path: "/file/edit", Methods: []string{http.MethodPost}, Description: "Edit a workspace-scoped file."},
		{Path: "/file/diff", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Preview a file edit diff."},
		{Path: "/terminal", Methods: []string{http.MethodGet, http.MethodPost}, Description: "List or start terminal tasks."},
		{Path: "/terminal/{id}", Methods: []string{http.MethodGet}, Description: "Read a terminal task."},
		{Path: "/terminal/{id}/restart", Methods: []string{http.MethodPost}, Description: "Restart a terminal task."},
		{Path: "/terminal/{id}/stop", Methods: []string{http.MethodPost}, Description: "Stop a terminal task."},
		{Path: "/terminal/{id}/logs", Methods: []string{http.MethodGet}, Description: "Read terminal task logs."},
		{Path: "/terminal/{id}/watch", Methods: []string{http.MethodGet}, Description: "Stream terminal task events.", Streaming: true},
		{Path: "/background", Methods: []string{http.MethodGet, http.MethodPost}, Description: "List or start background tasks."},
		{Path: "/background/prune", Methods: []string{http.MethodPost}, Description: "Prune old background tasks."},
		{Path: "/background/supervise", Methods: []string{http.MethodPost}, Description: "Run one background supervision pass."},
		{Path: "/background/{id}", Methods: []string{http.MethodGet}, Description: "Read a background task."},
		{Path: "/background/{id}/restart", Methods: []string{http.MethodPost}, Description: "Restart a background task."},
		{Path: "/background/{id}/stop", Methods: []string{http.MethodPost}, Description: "Stop a background task."},
		{Path: "/background/{id}/logs", Methods: []string{http.MethodGet}, Description: "Read background task logs."},
		{Path: "/background/{id}/watch", Methods: []string{http.MethodGet}, Description: "Stream background task events.", Streaming: true},
		{Path: "/hooks/health", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Inspect hook matching without executing hooks."},
		{Path: "/hooks/status", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Alias for hook health inspection."},
		{Path: "/diagnostics/go", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Run Go diagnostics."},
		{Path: "/code/symbols", Methods: []string{http.MethodGet, http.MethodPost}, Description: "List Go symbols."},
		{Path: "/code/references", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Find symbol references."},
		{Path: "/code/definition", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Find a symbol definition."},
		{Path: "/code/hover", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Return hover-style symbol context."},
		{Path: "/code/completion", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Return symbol completions."},
		{Path: "/code/format", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Format a Go file."},
		{Path: "/editor/identify", Methods: []string{http.MethodPost}, Description: "Register or identify an editor bridge client."},
		{Path: "/editor/state", Methods: []string{http.MethodGet}, Description: "Read editor bridge state."},
		{Path: "/editor/open", Methods: []string{http.MethodPost}, Description: "Open a file through the editor bridge."},
		{Path: "/editor/selection", Methods: []string{http.MethodGet, http.MethodPost}, Description: "Read or update editor selection."},
	}
}

type Failure struct {
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable,omitempty"`
	At        time.Time `json:"at,omitempty"`
}

type State struct {
	HeartbeatAt     time.Time  `json:"heartbeat_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Failure         *Failure   `json:"failure,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at,omitempty"`
	LeaseTTLSeconds int        `json:"lease_ttl_seconds,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	LeaseExpired    bool       `json:"lease_expired,omitempty"`
}

type hookHealthRequest struct {
	Event            string   `json:"event"`
	Tool             string   `json:"tool"`
	Input            string   `json:"input,omitempty"`
	Output           string   `json:"output,omitempty"`
	IsError          bool     `json:"is_error,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	NotificationType string   `json:"notification_type,omitempty"`
	Title            string   `json:"title,omitempty"`
	AgentID          string   `json:"agent_id,omitempty"`
	AgentType        string   `json:"agent_type,omitempty"`
	TranscriptPath   string   `json:"agent_transcript_path,omitempty"`
	LastAssistant    string   `json:"last_assistant_message,omitempty"`
	WorktreeID       string   `json:"worktree_id,omitempty"`
	WorktreePath     string   `json:"worktree_path,omitempty"`
	Ref              string   `json:"ref,omitempty"`
	OldCWD           string   `json:"old_cwd,omitempty"`
	NewCWD           string   `json:"new_cwd,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	TaskKind         string   `json:"task_kind,omitempty"`
	TaskStatus       string   `json:"task_status,omitempty"`
	FilePath         string   `json:"file_path,omitempty"`
	Operation        string   `json:"operation,omitempty"`
	MemoryType       string   `json:"memory_type,omitempty"`
	LoadReason       string   `json:"load_reason,omitempty"`
	Globs            []string `json:"globs,omitempty"`
	TriggerFilePath  string   `json:"trigger_file_path,omitempty"`
	ParentFilePath   string   `json:"parent_file_path,omitempty"`
	StopHookActive   bool     `json:"stop_hook_active,omitempty"`
}

type hookHealthReport struct {
	Kind            string               `json:"kind"`
	Action          string               `json:"action"`
	Status          string               `json:"status"`
	Workspace       string               `json:"workspace"`
	Route           string               `json:"route"`
	RoutePresent    bool                 `json:"route_present"`
	AcceptedMethods []string             `json:"accepted_methods"`
	ExecutesHooks   bool                 `json:"executes_hooks"`
	Event           string               `json:"event"`
	MatcherTarget   string               `json:"matcher_target"`
	ConfiguredCount int                  `json:"configured_count"`
	MatchedCount    int                  `json:"matched_count"`
	Matched         []hookCommandSummary `json:"matched"`
	Events          []hookEventHealth    `json:"events"`
}

type hookEventHealth struct {
	Event      string `json:"event"`
	Configured int    `json:"configured"`
}

type hookCommandSummary struct {
	Matcher string `json:"matcher,omitempty"`
	Type    string `json:"type,omitempty"`
	If      string `json:"if,omitempty"`
	Command string `json:"command"`
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/state", s.state)
	mux.HandleFunc("/sessions", s.sessions)
	mux.HandleFunc("/sessions/", s.sessionByID)
	mux.HandleFunc("/workspace/info", s.workspaceInfo)
	mux.HandleFunc("/workspace/files", s.workspaceFiles)
	mux.HandleFunc("/workspace/search", s.workspaceSearch)
	mux.HandleFunc("/file/read", s.fileRead)
	mux.HandleFunc("/file/write", s.fileWrite)
	mux.HandleFunc("/file/edit", s.fileEdit)
	mux.HandleFunc("/file/diff", s.fileDiff)
	mux.HandleFunc("/terminal", s.terminal)
	mux.HandleFunc("/terminal/", s.terminalByID)
	mux.HandleFunc("/background", s.background)
	mux.HandleFunc("/background/prune", s.backgroundPrune)
	mux.HandleFunc("/background/supervise", s.backgroundSupervise)
	mux.HandleFunc("/background/", s.backgroundByID)
	mux.HandleFunc("/hooks/health", s.hooksHealth)
	mux.HandleFunc("/hooks/status", s.hooksHealth)
	mux.HandleFunc("/diagnostics/go", s.goDiagnostics)
	mux.HandleFunc("/code/symbols", s.codeSymbols)
	mux.HandleFunc("/code/references", s.codeReferences)
	mux.HandleFunc("/code/definition", s.codeDefinition)
	mux.HandleFunc("/code/hover", s.codeHover)
	mux.HandleFunc("/code/completion", s.codeCompletion)
	mux.HandleFunc("/code/format", s.codeFormat)
	mux.HandleFunc("/editor/identify", s.editorIdentify)
	mux.HandleFunc("/editor/state", s.editorState)
	mux.HandleFunc("/editor/open", s.editorOpen)
	mux.HandleFunc("/editor/selection", s.editorSelection)
	return s.withAuth(mux)
}

func (s Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

func (s Server) state(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.readState()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.decorateState(state))
	case http.MethodPost:
		var payload struct {
			Heartbeat      bool     `json:"heartbeat"`
			LastError      *string  `json:"last_error"`
			Failure        *Failure `json:"failure"`
			FailureCode    string   `json:"failure_code"`
			FailureMessage string   `json:"failure_message"`
			Retryable      *bool    `json:"retryable"`
			ClearFailure   bool     `json:"clear_failure"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		state, err := s.readState()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		now := s.now()
		if payload.Heartbeat {
			state.HeartbeatAt = now
		}
		if payload.ClearFailure {
			state.LastError = ""
			state.Failure = nil
		}
		if payload.LastError != nil {
			state.LastError = *payload.LastError
			if *payload.LastError == "" {
				state.Failure = nil
			} else if payload.Failure == nil && payload.FailureCode == "" && payload.FailureMessage == "" {
				state.Failure = &Failure{Code: "remote_error", Message: *payload.LastError, At: now}
			}
		}
		if payload.Failure != nil {
			failure := *payload.Failure
			if failure.At.IsZero() {
				failure.At = now
			}
			state.Failure = &failure
			state.LastError = failure.Message
		}
		if payload.FailureCode != "" || payload.FailureMessage != "" {
			retryable := false
			if payload.Retryable != nil {
				retryable = *payload.Retryable
			}
			message := payload.FailureMessage
			if message == "" && payload.LastError != nil {
				message = *payload.LastError
			}
			state.Failure = &Failure{Code: payload.FailureCode, Message: message, Retryable: retryable, At: now}
			state.LastError = message
		}
		state.UpdatedAt = now
		if err := s.writeState(state); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.decorateState(state))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessions, err := s.Sessions.List()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, sessions)
	case http.MethodPost:
		sess, err := s.Sessions.Open("")
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, sess)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) sessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/")
	if rest == "" {
		writeError(w, http.ErrMissingFile, http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeError(w, http.ErrMissingFile, http.StatusBadRequest)
		return
	}
	if len(parts) > 1 && parts[1] == "background" {
		tasks, err := background.NewStore(s.ConfigHome).List()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, background.FilterBySession(tasks, id))
		return
	}
	if len(parts) > 1 && parts[1] == "messages" {
		s.sessionMessages(w, r, id)
		return
	}
	if len(parts) > 1 && parts[1] == "input" {
		s.sessionInput(w, r, id)
		return
	}
	if len(parts) > 1 && parts[1] == "rewind" {
		s.sessionRewind(w, r, id)
		return
	}
	if len(parts) > 1 && parts[1] == "prompt" {
		s.sessionPrompt(w, r, id)
		return
	}
	if len(parts) > 1 {
		writeError(w, http.ErrMissingFile, http.StatusNotFound)
		return
	}
	sess, err := s.Sessions.Open(id)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, sess)
}

func (s Server) sessionMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Message *anthropic.Message `json:"message"`
		Role    string             `json:"role"`
		Text    string             `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	var msg anthropic.Message
	if payload.Message != nil {
		msg = *payload.Message
	} else {
		role := strings.TrimSpace(payload.Role)
		text := strings.TrimSpace(payload.Text)
		if role == "" {
			role = "user"
		}
		if text == "" {
			writeError(w, errors.New("text is required"), http.StatusBadRequest)
			return
		}
		msg = anthropic.TextMessage(role, text)
	}
	if msg.Role == "" || len(msg.Content) == 0 {
		writeError(w, errors.New("message role and content are required"), http.StatusBadRequest)
		return
	}
	if err := s.Sessions.Append(id, msg); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	sess, err := s.Sessions.Open(id)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, sess)
}

func (s Server) sessionInput(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.Input) == "" {
		writeError(w, errors.New("input is required"), http.StatusBadRequest)
		return
	}
	if err := s.Sessions.AppendInput(id, payload.Input); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"session_id": id, "input": payload.Input})
}

func (s Server) sessionRewind(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		RemoveMessages int `json:"remove_messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := s.Sessions.Rewind(id, payload.RemoveMessages)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) sessionPrompt(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Prompt string `json:"prompt"`
		Kind   string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		writeError(w, errors.New("prompt is required"), http.StatusBadRequest)
		return
	}
	executable := strings.TrimSpace(s.Executable)
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = "prompt"
	}
	command := strings.Join([]string{
		shellQuote(executable),
		"--resume",
		shellQuote(id),
		"prompt",
		shellQuote(prompt),
	}, " ")
	task, err := background.NewStore(s.ConfigHome).RunWithOptions(command, s.Workspace, background.RunOptions{
		Kind:      kind,
		SessionID: id,
	})
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, task)
}

func (s Server) workspaceInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	info, err := s.workspaceOps().Info()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

func (s Server) workspaceFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.FilesOptions
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		payload.Path = query.Get("path")
		payload.Pattern = query.Get("pattern")
		payload.Limit = parseOptionalInt(query.Get("limit"))
		payload.IncludeHidden = parseOptionalBool(query.Get("include_hidden"))
	}
	result, err := s.workspaceOps().Files(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) workspaceSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.SearchOptions
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		payload.Query = query.Get("query")
		payload.Path = query.Get("path")
		payload.Glob = query.Get("glob")
		payload.Regex = parseOptionalBool(query.Get("regex"))
		payload.Limit = parseOptionalInt(query.Get("limit"))
		payload.IncludeHidden = parseOptionalBool(query.Get("include_hidden"))
	}
	result, err := s.workspaceOps().Search(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) fileRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.ReadOptions
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		payload.Path = query.Get("path")
		payload.Offset = parseOptionalInt(query.Get("offset"))
		payload.Limit = parseOptionalInt(query.Get("limit"))
	}
	result, err := s.workspaceOps().Read(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) fileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.WriteOptions
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := s.workspaceOps().Write(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) fileEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.EditOptions
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := s.workspaceOps().Edit(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) fileDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload workspaceops.DiffOptions
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		payload.Path = query.Get("path")
		payload.OldString = query.Get("old_string")
		payload.NewString = query.Get("new_string")
	}
	result, err := s.workspaceOps().Diff(payload)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s Server) background(w http.ResponseWriter, r *http.Request) {
	store := background.NewStore(s.ConfigHome)
	switch r.Method {
	case http.MethodGet:
		tasks, err := store.List()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		tasks = background.FilterBySession(tasks, r.URL.Query().Get("session_id"))
		writeJSON(w, tasks)
	case http.MethodPost:
		var payload struct {
			Command       string                    `json:"command"`
			SessionID     string                    `json:"session_id"`
			RestartPolicy *background.RestartPolicy `json:"restart_policy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		task, err := store.RunWithOptions(payload.Command, s.Workspace, background.RunOptions{
			SessionID:     payload.SessionID,
			RestartPolicy: payload.RestartPolicy,
		})
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, task)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) terminal(w http.ResponseWriter, r *http.Request) {
	store := background.NewStore(s.ConfigHome)
	switch r.Method {
	case http.MethodGet:
		tasks, err := store.List()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		tasks = background.FilterByKind(tasks, "terminal")
		tasks = background.FilterBySession(tasks, r.URL.Query().Get("session_id"))
		writeJSON(w, tasks)
	case http.MethodPost:
		var payload struct {
			Command       string                    `json:"command"`
			SessionID     string                    `json:"session_id"`
			RestartPolicy *background.RestartPolicy `json:"restart_policy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		task, err := store.RunWithOptions(payload.Command, s.Workspace, background.RunOptions{
			Kind:          "terminal",
			SessionID:     payload.SessionID,
			RestartPolicy: payload.RestartPolicy,
		})
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, task)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) terminalByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/terminal/"), "/")
	if rest == "" {
		writeError(w, http.ErrMissingFile, http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	store := background.NewStore(s.ConfigHome)
	task, err := store.Status(id)
	if err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	}
	if task.Kind != "terminal" {
		writeError(w, http.ErrMissingFile, http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		writeJSON(w, task)
	case r.Method == http.MethodPost && action == "restart":
		restarted, err := store.Restart(id, s.Workspace)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, restarted)
	case r.Method == http.MethodPost && action == "stop":
		stopped, err := store.Stop(id)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, stopped)
	case r.Method == http.MethodGet && action == "logs":
		limit := int64(64 * 1024)
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				writeError(w, err, http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		logs, err := store.Logs(id, limit)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeText(w, logs)
	case r.Method == http.MethodGet && (action == "stream" || action == "watch"):
		s.streamBackgroundWatch(w, r, store, id)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) backgroundSupervise(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	result, err := background.NewStore(s.ConfigHome).SuperviseOnce(s.now())
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s Server) backgroundPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	options := background.DefaultPruneOptions()
	var payload struct {
		OlderThanDays *int `json:"older_than_days"`
		Keep          *int `json:"keep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if payload.OlderThanDays != nil {
		if *payload.OlderThanDays < 0 {
			writeError(w, errors.New("older_than_days must be non-negative"), http.StatusBadRequest)
			return
		}
		options.OlderThan = time.Duration(*payload.OlderThanDays) * 24 * time.Hour
	}
	if payload.Keep != nil {
		if *payload.Keep < 0 {
			writeError(w, errors.New("keep must be non-negative"), http.StatusBadRequest)
			return
		}
		options.Keep = *payload.Keep
	}
	result, err := background.NewStore(s.ConfigHome).Prune(options)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s Server) backgroundByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/background/"), "/")
	if rest == "" {
		writeError(w, http.ErrMissingFile, http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	store := background.NewStore(s.ConfigHome)
	switch {
	case r.Method == http.MethodGet && action == "":
		task, err := store.Status(id)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, task)
	case r.Method == http.MethodPost && action == "restart":
		task, err := store.Restart(id, s.Workspace)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, task)
	case r.Method == http.MethodPost && action == "stop":
		task, err := store.Stop(id)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, task)
	case r.Method == http.MethodGet && action == "logs":
		limit := int64(64 * 1024)
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				writeError(w, err, http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		logs, err := store.Logs(id, limit)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeText(w, logs)
	case r.Method == http.MethodGet && action == "watch":
		s.streamBackgroundWatch(w, r, store, id)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) streamBackgroundWatch(w http.ResponseWriter, r *http.Request, store background.Store, id string) {
	options, err := parseWatchOptions(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if _, err := store.Get(id); err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "application/x-ndjson")
	w.Header().Set("cache-control", "no-cache")
	encoder := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	err = store.Watch(r.Context(), id, options, func(event background.WatchEvent) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return
	}
}

func parseWatchOptions(r *http.Request) (background.WatchOptions, error) {
	var options background.WatchOptions
	if value := r.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return options, err
		}
		options.Offset = parsed
	}
	if value := r.URL.Query().Get("interval_ms"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return options, err
		}
		if parsed > 0 {
			options.Interval = time.Duration(parsed) * time.Millisecond
		}
	}
	if value := r.URL.Query().Get("events"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return options, err
		}
		options.MaxEvents = parsed
	}
	return options, nil
}

func (s Server) hooksHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req hookHealthRequest
	if r.Method == http.MethodGet {
		req = hookHealthRequestFromQuery(r)
	} else if err := decodeOptionalJSONPayload(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	event, err := normalizeHookHealthEvent(firstNonEmpty(req.Event, "pre_tool_use"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	req.Event = event
	if strings.TrimSpace(req.Input) == "" {
		req.Input = `{}`
	}
	defaultHookHealthTool(&req)
	payload := hookHealthPayload(req)
	matched := hooks.HooksForPayload(s.Hooks, payload)
	writeJSON(w, buildHookHealthReport(s.Hooks, s.Workspace, r.URL.Path, payload, matched))
}

func defaultHookHealthRequest() hookHealthRequest {
	return hookHealthRequest{Event: "pre_tool_use", Input: `{}`}
}

func hookHealthRequestFromQuery(r *http.Request) hookHealthRequest {
	q := r.URL.Query()
	req := defaultHookHealthRequest()
	req.Event = firstNonEmpty(q.Get("event"), q.Get("hook_event"), req.Event)
	req.Tool = firstNonEmpty(q.Get("tool"), q.Get("tool_name"), req.Tool)
	req.Input = firstNonEmpty(q.Get("input"), req.Input)
	req.Output = q.Get("output")
	req.IsError = parseOptionalBool(firstNonEmpty(q.Get("is_error"), q.Get("error")))
	req.Reason = q.Get("reason")
	req.NotificationType = firstNonEmpty(q.Get("notification_type"), q.Get("notification-type"))
	req.Title = q.Get("title")
	req.AgentID = firstNonEmpty(q.Get("agent_id"), q.Get("agent-id"))
	req.AgentType = firstNonEmpty(q.Get("agent_type"), q.Get("agent-type"))
	req.TranscriptPath = firstNonEmpty(q.Get("agent_transcript_path"), q.Get("agent-transcript-path"))
	req.LastAssistant = firstNonEmpty(q.Get("last_assistant_message"), q.Get("last-assistant-message"))
	req.WorktreeID = firstNonEmpty(q.Get("worktree_id"), q.Get("worktree-id"))
	req.WorktreePath = firstNonEmpty(q.Get("worktree_path"), q.Get("worktree-path"))
	req.Ref = q.Get("ref")
	req.OldCWD = firstNonEmpty(q.Get("old_cwd"), q.Get("old-cwd"))
	req.NewCWD = firstNonEmpty(q.Get("new_cwd"), q.Get("new-cwd"))
	req.TaskID = firstNonEmpty(q.Get("task_id"), q.Get("task-id"))
	req.TaskKind = firstNonEmpty(q.Get("task_kind"), q.Get("task-kind"))
	req.TaskStatus = firstNonEmpty(q.Get("task_status"), q.Get("task-status"))
	req.FilePath = firstNonEmpty(q.Get("file_path"), q.Get("file-path"), q.Get("path"))
	req.Operation = q.Get("operation")
	req.MemoryType = firstNonEmpty(q.Get("memory_type"), q.Get("memory-type"))
	req.LoadReason = firstNonEmpty(q.Get("load_reason"), q.Get("load-reason"))
	req.Globs = append([]string(nil), q["glob"]...)
	if len(req.Globs) == 0 {
		req.Globs = append([]string(nil), q["globs"]...)
	}
	req.TriggerFilePath = firstNonEmpty(q.Get("trigger_file_path"), q.Get("trigger-file-path"))
	req.ParentFilePath = firstNonEmpty(q.Get("parent_file_path"), q.Get("parent-file-path"))
	req.StopHookActive = parseOptionalBool(firstNonEmpty(q.Get("stop_hook_active"), q.Get("stop-hook-active")))
	return req
}

func defaultHookHealthTool(req *hookHealthRequest) {
	if strings.TrimSpace(req.Tool) != "" {
		return
	}
	switch req.Event {
	case "user_prompt_submit", "session_start", "stop", "pre_compact", "post_compact", "notification", "subagent_start", "subagent_stop", "file_changed", "instructions_loaded":
		return
	default:
		req.Tool = "bash"
	}
}

func hookHealthPayload(req hookHealthRequest) hooks.Payload {
	payload := hooks.Payload{
		Event:            req.Event,
		Tool:             req.Tool,
		ToolName:         req.Tool,
		ToolInput:        json.RawMessage(req.Input),
		Input:            req.Input,
		Output:           req.Output,
		IsError:          req.IsError,
		Reason:           req.Reason,
		Message:          req.Input,
		Title:            req.Title,
		NotificationType: req.NotificationType,
		AgentID:          req.AgentID,
		AgentType:        req.AgentType,
		TranscriptPath:   req.TranscriptPath,
		LastAssistant:    req.LastAssistant,
		WorktreeID:       req.WorktreeID,
		WorktreePath:     req.WorktreePath,
		Ref:              req.Ref,
		OldCWD:           req.OldCWD,
		NewCWD:           req.NewCWD,
		TaskID:           req.TaskID,
		TaskKind:         req.TaskKind,
		TaskStatus:       req.TaskStatus,
		FilePath:         req.FilePath,
		Operation:        req.Operation,
		MemoryType:       req.MemoryType,
		LoadReason:       req.LoadReason,
		Globs:            append([]string(nil), req.Globs...),
		TriggerFilePath:  req.TriggerFilePath,
		ParentFilePath:   req.ParentFilePath,
		StopHookActive:   req.StopHookActive,
	}
	switch req.Event {
	case "notification":
		payload.NotificationType = firstNonEmpty(req.NotificationType, req.Tool, "generic")
		payload.Tool = payload.NotificationType
	case "subagent_start", "subagent_stop":
		payload.AgentType = firstNonEmpty(req.AgentType, req.Tool, "general")
		payload.Tool = payload.AgentType
	case "worktree_create", "worktree_remove":
		payload.WorktreeID = firstNonEmpty(req.WorktreeID, req.Tool)
		payload.Tool = payload.WorktreeID
	case "cwd_changed":
		payload.NewCWD = firstNonEmpty(req.NewCWD, req.Tool)
		payload.Tool = payload.NewCWD
	case "task_created", "task_completed":
		payload.TaskID = firstNonEmpty(req.TaskID, req.Tool)
		payload.TaskKind = firstNonEmpty(req.TaskKind, req.AgentType, "background")
		payload.Tool = payload.TaskKind
	case "file_changed":
		payload.Operation = firstNonEmpty(req.Operation, req.Tool, "write_file")
		payload.Tool = payload.Operation
		payload.ToolName = payload.Operation
	case "instructions_loaded":
		payload.LoadReason = firstNonEmpty(req.LoadReason, req.Tool, "session_start")
		payload.Tool = payload.LoadReason
		payload.MemoryType = firstNonEmpty(req.MemoryType, "Project")
	}
	return payload
}

func buildHookHealthReport(cfg config.HookConfig, workspace string, route string, payload hooks.Payload, matched []config.HookCommand) hookHealthReport {
	events := make([]hookEventHealth, 0, len(allHookHealthEvents()))
	configured := 0
	for _, event := range allHookHealthEvents() {
		count := hookConfiguredCount(cfg, event)
		configured += count
		events = append(events, hookEventHealth{Event: event, Configured: count})
	}
	return hookHealthReport{
		Kind:            "hooks",
		Action:          "health",
		Status:          "ok",
		Workspace:       workspace,
		Route:           route,
		RoutePresent:    true,
		AcceptedMethods: []string{http.MethodGet, http.MethodPost},
		ExecutesHooks:   false,
		Event:           payload.Event,
		MatcherTarget:   hookMatcherTarget(payload),
		ConfiguredCount: configured,
		MatchedCount:    len(matched),
		Matched:         summarizeHookCommands(matched),
		Events:          events,
	}
}

func hookMatcherTarget(payload hooks.Payload) string {
	switch payload.Event {
	case "notification":
		if strings.TrimSpace(payload.NotificationType) != "" {
			return payload.NotificationType
		}
	case "subagent_start", "subagent_stop":
		if strings.TrimSpace(payload.AgentType) != "" {
			return payload.AgentType
		}
	}
	return payload.Tool
}

func allHookHealthEvents() []string {
	return []string{
		"pre_tool_use",
		"post_tool_use",
		"post_tool_use_failure",
		"permission_request",
		"permission_denied",
		"user_prompt_submit",
		"session_start",
		"session_end",
		"setup",
		"stop",
		"stop_failure",
		"pre_compact",
		"post_compact",
		"notification",
		"subagent_start",
		"subagent_stop",
		"worktree_create",
		"worktree_remove",
		"cwd_changed",
		"task_created",
		"task_completed",
		"instructions_loaded",
		"file_changed",
	}
}

func hookConfiguredCount(cfg config.HookConfig, event string) int {
	switch event {
	case "pre_tool_use":
		return len(hookCommandsForList(cfg.PreToolUseCommands, cfg.PreToolUse))
	case "post_tool_use":
		return len(hookCommandsForList(cfg.PostToolUseCommands, cfg.PostToolUse))
	case "post_tool_use_failure":
		return len(hookCommandsForList(cfg.PostToolUseFailureCommands, cfg.PostToolUseFailure))
	case "permission_request":
		return len(hookCommandsForList(cfg.PermissionRequestCommands, cfg.PermissionRequest))
	case "permission_denied":
		return len(hookCommandsForList(cfg.PermissionDeniedCommands, cfg.PermissionDenied))
	case "user_prompt_submit":
		return len(hookCommandsForList(cfg.UserPromptSubmitCommands, cfg.UserPromptSubmit))
	case "session_start":
		return len(hookCommandsForList(cfg.SessionStartCommands, cfg.SessionStart))
	case "session_end":
		return len(hookCommandsForList(cfg.SessionEndCommands, cfg.SessionEnd))
	case "setup":
		return len(hookCommandsForList(cfg.SetupCommands, cfg.Setup))
	case "stop":
		return len(hookCommandsForList(cfg.StopCommands, cfg.Stop))
	case "stop_failure":
		return len(hookCommandsForList(cfg.StopFailureCommands, cfg.StopFailure))
	case "pre_compact":
		return len(hookCommandsForList(cfg.PreCompactCommands, cfg.PreCompact))
	case "post_compact":
		return len(hookCommandsForList(cfg.PostCompactCommands, cfg.PostCompact))
	case "notification":
		return len(hookCommandsForList(cfg.NotificationCommands, cfg.Notification))
	case "subagent_start":
		return len(hookCommandsForList(cfg.SubagentStartCommands, cfg.SubagentStart))
	case "subagent_stop":
		return len(hookCommandsForList(cfg.SubagentStopCommands, cfg.SubagentStop))
	case "worktree_create":
		return len(hookCommandsForList(cfg.WorktreeCreateCommands, cfg.WorktreeCreate))
	case "worktree_remove":
		return len(hookCommandsForList(cfg.WorktreeRemoveCommands, cfg.WorktreeRemove))
	case "cwd_changed":
		return len(hookCommandsForList(cfg.CwdChangedCommands, cfg.CwdChanged))
	case "task_created":
		return len(hookCommandsForList(cfg.TaskCreatedCommands, cfg.TaskCreated))
	case "task_completed":
		return len(hookCommandsForList(cfg.TaskCompletedCommands, cfg.TaskCompleted))
	case "instructions_loaded":
		return len(hookCommandsForList(cfg.InstructionsLoadedCommands, cfg.InstructionsLoaded))
	case "file_changed":
		return len(hookCommandsForList(cfg.FileChangedCommands, cfg.FileChanged))
	default:
		return 0
	}
}

func hookCommandsForList(commands []config.HookCommand, legacy []string) []hookCommandSummary {
	summaries := summarizeHookCommands(commands)
	if len(summaries) != 0 || len(legacy) == 0 {
		return summaries
	}
	out := make([]hookCommandSummary, 0, len(legacy))
	for _, command := range legacy {
		command = strings.TrimSpace(command)
		if command != "" {
			out = append(out, hookCommandSummary{Command: command})
		}
	}
	return out
}

func summarizeHookCommands(commands []config.HookCommand) []hookCommandSummary {
	out := make([]hookCommandSummary, 0, len(commands))
	for _, command := range commands {
		display := config.HookCommandDisplay(command)
		if display == "" {
			continue
		}
		out = append(out, hookCommandSummary{
			Matcher: strings.TrimSpace(command.Matcher),
			Type:    strings.TrimSpace(command.Type),
			If:      strings.TrimSpace(command.If),
			Command: display,
		})
	}
	return out
}

func normalizeHookHealthEvent(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pre", "pre_tool_use", "pre-tool-use":
		return "pre_tool_use", nil
	case "post", "post_tool_use", "post-tool-use":
		return "post_tool_use", nil
	case "post-failure", "postfailure", "post_tool_use_failure", "post-tool-use-failure":
		return "post_tool_use_failure", nil
	case "permission-request", "permissionrequest", "permission_request":
		return "permission_request", nil
	case "permission-denied", "permissiondenied", "permission_denied":
		return "permission_denied", nil
	case "prompt", "userpromptsubmit", "user_prompt_submit", "user-prompt-submit":
		return "user_prompt_submit", nil
	case "session", "sessionstart", "session_start", "session-start":
		return "session_start", nil
	case "session-end", "sessionend", "session_end":
		return "session_end", nil
	case "setup":
		return "setup", nil
	case "stop":
		return "stop", nil
	case "stop-failure", "stopfailure", "stop_failure":
		return "stop_failure", nil
	case "compact", "precompact", "pre_compact", "pre-compact":
		return "pre_compact", nil
	case "postcompact", "post_compact", "post-compact":
		return "post_compact", nil
	case "notification", "notify":
		return "notification", nil
	case "subagent-start", "subagentstart", "subagent_start":
		return "subagent_start", nil
	case "subagent-stop", "subagentstop", "subagent_stop":
		return "subagent_stop", nil
	case "worktree-create", "worktreecreate", "worktree_create":
		return "worktree_create", nil
	case "worktree-remove", "worktreeremove", "worktree_remove":
		return "worktree_remove", nil
	case "cwd-changed", "cwdchanged", "cwd_changed":
		return "cwd_changed", nil
	case "task-created", "taskcreated", "task_created":
		return "task_created", nil
	case "task-completed", "taskcompleted", "task_completed":
		return "task_completed", nil
	case "instructions-loaded", "instructionsloaded", "instructions_loaded":
		return "instructions_loaded", nil
	case "file-changed", "filechanged", "file_changed":
		return "file_changed", nil
	default:
		return "", fmt.Errorf("unknown hook event %q", value)
	}
}

func (s Server) goDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var patterns []string
	if r.Method == http.MethodGet {
		patterns = r.URL.Query()["pattern"]
	} else {
		var payload struct {
			Patterns []string `json:"patterns"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		patterns = payload.Patterns
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	diagnostics, err := codeintel.GoDiagnostics(context.Background(), workspace, patterns)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, diagnostics)
}

func (s Server) codeSymbols(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Path = r.URL.Query().Get("path")
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	symbols, err := codeintel.GoSymbols(workspace)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(payload.Path) != "" {
		_, rel, err := s.resolveWorkspacePath(payload.Path)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		filtered := symbols[:0]
		for _, symbol := range symbols {
			if filepath.ToSlash(symbol.Path) == rel {
				filtered = append(filtered, symbol)
			}
		}
		symbols = filtered
	}
	writeJSON(w, map[string]any{"kind": "symbols", "total": len(symbols), "symbols": symbols})
}

func (s Server) codeReferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Symbol string `json:"symbol"`
		Limit  int    `json:"limit"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Symbol = r.URL.Query().Get("symbol")
		payload.Limit = parseOptionalInt(r.URL.Query().Get("limit"))
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	refs, err := codeintel.References(workspace, payload.Symbol, payload.Limit)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"kind": "references", "symbol": strings.TrimSpace(payload.Symbol), "total": len(refs), "references": refs})
}

func (s Server) codeDefinition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Symbol string `json:"symbol"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Symbol = r.URL.Query().Get("symbol")
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	definition, found, err := codeintel.Definition(workspace, payload.Symbol)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"kind": "definition", "symbol": strings.TrimSpace(payload.Symbol), "found": found, "definition": definition})
}

func (s Server) codeHover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Symbol       string `json:"symbol"`
		ContextLines int    `json:"context_lines"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Symbol = r.URL.Query().Get("symbol")
		payload.ContextLines = parseOptionalInt(r.URL.Query().Get("context_lines"))
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	hover, err := codeintel.HoverInfo(workspace, payload.Symbol, payload.ContextLines)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"kind": "hover", "symbol": strings.TrimSpace(payload.Symbol), "hover": hover})
}

func (s Server) codeCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Query = r.URL.Query().Get("query")
		payload.Limit = parseOptionalInt(r.URL.Query().Get("limit"))
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	completions, err := codeintel.Completions(workspace, payload.Query, payload.Limit)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"kind": "completion", "query": strings.TrimSpace(payload.Query), "total": len(completions), "completions": completions})
}

func (s Server) codeFormat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Path  string `json:"path"`
		Write bool   `json:"write"`
	}
	if err := decodeOptionalJSONPayload(r, &payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		payload.Path = r.URL.Query().Get("path")
		payload.Write = false
	}
	workspace, err := s.workspace()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	result, err := codeintel.FormatGoFile(workspace, payload.Path, payload.Write)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"kind": "format", "write": payload.Write, "result": result})
}

func (s Server) editorIdentify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	params, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	identity, err := s.editorBridge().IdentifyEditor(params)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, identity)
}

func (s Server) editorState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	state, err := s.editorBridge().EditorState()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, state)
}

func (s Server) editorOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	params, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	openFile, err := s.editorBridge().OpenEditorFile(params)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, openFile)
}

func (s Server) editorSelection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := s.editorBridge().EditorState()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, state.Selection)
	case http.MethodPost:
		params, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		selection, err := s.editorBridge().SetEditorSelection(params)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, selection)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s Server) editorBridge() bridge.Server {
	return bridge.Server{
		Sessions:   s.Sessions,
		Workspace:  s.Workspace,
		ConfigHome: s.ConfigHome,
		TrustToken: s.EditorToken,
		Executable: s.Executable,
	}
}

func decodeOptionalJSONPayload(r *http.Request, value any) error {
	if r.Method == http.MethodGet {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(value)
}

func parseOptionalInt(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func parseOptionalBool(value string) bool {
	parsed, _ := strconv.ParseBool(value)
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s Server) withAuth(next http.Handler) http.Handler {
	if s.AuthToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if authorized(r, s.AuthToken) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, errors.New("unauthorized"), http.StatusUnauthorized)
	})
}

func authorized(r *http.Request, token string) bool {
	if value := r.Header.Get("authorization"); value != "" {
		scheme, credential, ok := strings.Cut(value, " ")
		if ok && strings.EqualFold(scheme, "Bearer") && credential == token {
			return true
		}
	}
	return r.Header.Get("x-codog-token") == token
}

func (s Server) readState() (State, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s Server) writeState(state State) error {
	path := s.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state.persisted(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Server) decorateState(state State) State {
	ttl := s.LeaseTTL
	if ttl <= 0 {
		return state
	}
	state.LeaseTTLSeconds = int(ttl.Seconds())
	if !state.HeartbeatAt.IsZero() {
		expires := state.HeartbeatAt.Add(ttl)
		state.LeaseExpiresAt = &expires
		state.LeaseExpired = s.now().After(expires)
	}
	return state
}

func (state State) persisted() State {
	state.LeaseTTLSeconds = 0
	state.LeaseExpiresAt = nil
	state.LeaseExpired = false
	return state
}

func (s Server) statePath() string {
	return filepath.Join(s.ConfigHome, "remote", "state.json")
}

func (s Server) workspace() (string, error) {
	return s.workspaceOps().WorkspacePath()
}

func (s Server) resolveWorkspacePath(requested string) (string, string, error) {
	return s.workspaceOps().Resolve(requested, false)
}

func (s Server) workspaceOps() workspaceops.Service {
	return workspaceops.Service{Workspace: s.Workspace}
}

func (s Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!*?[]{}()<>|&;") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeText(w http.ResponseWriter, value string) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(value))
}

func writeError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}
