package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/session"
)

type Server struct {
	Sessions   *session.Store
	ConfigHome string
	Workspace  string
	AuthToken  string
	Now        func() time.Time
}

type State struct {
	HeartbeatAt time.Time `json:"heartbeat_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/state", s.state)
	mux.HandleFunc("/sessions", s.sessions)
	mux.HandleFunc("/sessions/", s.sessionByID)
	mux.HandleFunc("/background", s.background)
	mux.HandleFunc("/background/", s.backgroundByID)
	mux.HandleFunc("/diagnostics/go", s.goDiagnostics)
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
		writeJSON(w, state)
	case http.MethodPost:
		var payload struct {
			Heartbeat bool   `json:"heartbeat"`
			LastError string `json:"last_error"`
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
		state.LastError = payload.LastError
		state.UpdatedAt = now
		if err := s.writeState(state); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, state)
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
	id := strings.TrimPrefix(r.URL.Path, "/sessions/")
	if id == "" {
		writeError(w, http.ErrMissingFile, http.StatusBadRequest)
		return
	}
	sess, err := s.Sessions.Open(id)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, sess)
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
		writeJSON(w, tasks)
	case http.MethodPost:
		var payload struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		task, err := store.Run(payload.Command, s.Workspace)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, task)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
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
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
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
	diagnostics, err := codeintel.GoDiagnostics(context.Background(), s.Workspace, patterns)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, diagnostics)
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
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Server) statePath() string {
	return filepath.Join(s.ConfigHome, "remote", "state.json")
}

func (s Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
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
