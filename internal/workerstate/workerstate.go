package workerstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const FileName = "worker-state.json"

type Options struct {
	WorkerID       string
	Version        string
	Mode           string
	Status         string
	Workspace      string
	SessionID      string
	SessionPath    string
	Model          string
	PermissionMode string
	PID            int
	LastError      string
	Now            time.Time
}

type State struct {
	Kind           string    `json:"kind"`
	WorkerID       string    `json:"worker_id"`
	Version        string    `json:"version"`
	Mode           string    `json:"mode"`
	Status         string    `json:"status"`
	Workspace      string    `json:"workspace"`
	SessionID      string    `json:"session_id,omitempty"`
	SessionPath    string    `json:"session_path,omitempty"`
	Model          string    `json:"model,omitempty"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	PID            int       `json:"pid"`
	LastError      string    `json:"last_error,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func New(opts Options) State {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "unknown"
	}
	status := strings.TrimSpace(opts.Status)
	if status == "" {
		status = "idle"
	}
	workerID := strings.TrimSpace(opts.WorkerID)
	if workerID == "" {
		workerID = "codog-" + mode + "-" + strconv.Itoa(pid)
	}
	return State{
		Kind:           "worker_state",
		WorkerID:       workerID,
		Version:        opts.Version,
		Mode:           mode,
		Status:         status,
		Workspace:      opts.Workspace,
		SessionID:      opts.SessionID,
		SessionPath:    opts.SessionPath,
		Model:          opts.Model,
		PermissionMode: opts.PermissionMode,
		PID:            pid,
		LastError:      opts.LastError,
		UpdatedAt:      now,
	}
}

func Path(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, ".codog", FileName)
}

func Save(workspace string, state State) error {
	path := Path(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".worker-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func Load(workspace string) (State, error) {
	path := Path(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, MissingError{Path: path}
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Kind != "worker_state" {
		return State{}, errors.New("worker state kind is invalid")
	}
	return state, nil
}

func RenderText(w io.Writer, state State) {
	fmt.Fprintln(w, "State")
	fmt.Fprintf(w, "  Worker           %s\n", state.WorkerID)
	fmt.Fprintf(w, "  Status           %s\n", state.Status)
	fmt.Fprintf(w, "  Mode             %s\n", state.Mode)
	fmt.Fprintf(w, "  Session          %s\n", emptyAsNone(state.SessionID))
	fmt.Fprintf(w, "  Model            %s\n", emptyAsNone(state.Model))
	fmt.Fprintf(w, "  Permission       %s\n", emptyAsNone(state.PermissionMode))
	fmt.Fprintf(w, "  Workspace        %s\n", state.Workspace)
	fmt.Fprintf(w, "  Updated          %s\n", state.UpdatedAt.Format(time.RFC3339))
	if state.LastError != "" {
		fmt.Fprintf(w, "  Last error       %s\n", state.LastError)
	}
}

type MissingError struct {
	Path string
}

func (e MissingError) Error() string {
	return fmt.Sprintf("no worker state file found at %s\n  Hint: worker state is written by `codog repl` or `codog prompt <text>`.\n  Run:   codog repl\n  Or:    codog prompt <text>\n  Then rerun: codog state [--json]", e.Path)
}

func emptyAsNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
