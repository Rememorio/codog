package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/codeintel"
	"github.com/Rememorio/codog/internal/session"
)

type Server struct {
	Sessions   *session.Store
	ConfigHome string
	Workspace  string
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/sessions", s.sessions)
	mux.HandleFunc("/sessions/", s.sessionByID)
	mux.HandleFunc("/background", s.background)
	mux.HandleFunc("/background/", s.backgroundByID)
	mux.HandleFunc("/diagnostics/go", s.goDiagnostics)
	return mux
}

func (s Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
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
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
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
	diagnostics, err := codeintel.GoDiagnostics(context.Background(), s.Workspace, patterns)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, diagnostics)
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
