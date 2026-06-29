package workers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TaskReceipt struct {
	Repo              string   `json:"repo"`
	TaskKind          string   `json:"task_kind"`
	SourceSurface     string   `json:"source_surface"`
	ExpectedArtifacts []string `json:"expected_artifacts,omitempty"`
	ObjectivePreview  string   `json:"objective_preview"`
}

type Event struct {
	Type         string    `json:"type"`
	Message      string    `json:"message,omitempty"`
	FinishReason string    `json:"finish_reason,omitempty"`
	TokensOutput int64     `json:"tokens_output,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Worker struct {
	ID                           string       `json:"worker_id"`
	CWD                          string       `json:"cwd"`
	TrustedRoots                 []string     `json:"trusted_roots,omitempty"`
	AutoRecoverPromptMisdelivery bool         `json:"auto_recover_prompt_misdelivery"`
	Status                       string       `json:"status"`
	ReadyForPrompt               bool         `json:"ready_for_prompt"`
	TrustResolved                bool         `json:"trust_resolved"`
	TaskID                       string       `json:"task_id,omitempty"`
	TaskReceipt                  *TaskReceipt `json:"task_receipt,omitempty"`
	LastError                    string       `json:"last_error,omitempty"`
	Events                       []Event      `json:"events,omitempty"`
	CreatedAt                    time.Time    `json:"created_at"`
	UpdatedAt                    time.Time    `json:"updated_at"`
}

type ReadySnapshot struct {
	WorkerID       string `json:"worker_id"`
	Status         string `json:"status"`
	ReadyForPrompt bool   `json:"ready_for_prompt"`
	LastError      string `json:"last_error,omitempty"`
}

type Store struct {
	ConfigHome string
}

func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

func (s Store) Create(cwd string, trustedRoots []string, autoRecover bool) (Worker, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return Worker{}, errors.New("cwd is required")
	}
	now := time.Now().UTC()
	worker := Worker{
		ID:                           newID(now),
		CWD:                          cwd,
		TrustedRoots:                 normalizeStrings(trustedRoots),
		AutoRecoverPromptMisdelivery: autoRecover,
		Status:                       "ready_for_prompt",
		ReadyForPrompt:               true,
		TrustResolved:                true,
		Events:                       []Event{{Type: "created", Message: "worker ready for prompt", CreatedAt: now}},
		CreatedAt:                    now,
		UpdatedAt:                    now,
	}
	if err := s.Save(worker); err != nil {
		return Worker{}, err
	}
	return worker, nil
}

func (s Store) Get(id string) (Worker, error) {
	if err := validateID(id); err != nil {
		return Worker{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir(), id+".json"))
	if err != nil {
		return Worker{}, err
	}
	var worker Worker
	if err := json.Unmarshal(data, &worker); err != nil {
		return Worker{}, err
	}
	return worker, nil
}

func (s Store) Save(worker Worker) error {
	if err := validateID(worker.ID); err != nil {
		return err
	}
	worker.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(worker, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir(), worker.ID+".json"), data, 0o644)
}

func (s Store) Observe(id string, screenText string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	screenText = strings.TrimSpace(screenText)
	eventType := "observed"
	if strings.Contains(strings.ToLower(screenText), "trust") {
		worker.TrustResolved = false
		worker.ReadyForPrompt = false
		worker.Status = "trust_prompt"
		eventType = "trust_prompt"
	} else if strings.Contains(strings.ToLower(screenText), "ready") || strings.Contains(strings.ToLower(screenText), "prompt") {
		worker.TrustResolved = true
		worker.ReadyForPrompt = true
		worker.Status = "ready_for_prompt"
		eventType = "ready"
	}
	worker.Events = append(worker.Events, Event{Type: eventType, Message: screenText, CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) ResolveTrust(id string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.TrustResolved = true
	worker.ReadyForPrompt = true
	worker.Status = "ready_for_prompt"
	worker.Events = append(worker.Events, Event{Type: "trust_resolved", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) AwaitReady(id string) (ReadySnapshot, error) {
	worker, err := s.Get(id)
	if err != nil {
		return ReadySnapshot{}, err
	}
	return ReadySnapshot{WorkerID: worker.ID, Status: worker.Status, ReadyForPrompt: worker.ReadyForPrompt, LastError: worker.LastError}, nil
}

func (s Store) SendPrompt(id string, prompt string, receipt *TaskReceipt, taskID string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	if !worker.ReadyForPrompt {
		return Worker{}, fmt.Errorf("worker %s is not ready for prompt", id)
	}
	worker.Status = "running"
	worker.ReadyForPrompt = false
	worker.TaskID = strings.TrimSpace(taskID)
	worker.TaskReceipt = receipt
	worker.Events = append(worker.Events, Event{Type: "prompt_sent", Message: strings.TrimSpace(prompt), CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Restart(id string, taskID string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.Status = "running"
	worker.ReadyForPrompt = false
	worker.TaskID = strings.TrimSpace(taskID)
	worker.LastError = ""
	worker.Events = append(worker.Events, Event{Type: "restarted", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Terminate(id string) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	worker.Status = "terminated"
	worker.ReadyForPrompt = false
	worker.Events = append(worker.Events, Event{Type: "terminated", CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) Complete(id string, finishReason string, tokensOutput int64) (Worker, error) {
	worker, err := s.Get(id)
	if err != nil {
		return Worker{}, err
	}
	finishReason = strings.TrimSpace(finishReason)
	if finishReason == "" {
		return Worker{}, errors.New("finish_reason is required")
	}
	worker.Status = "finished"
	if !strings.EqualFold(finishReason, "stop") && !strings.EqualFold(finishReason, "end_turn") && !strings.EqualFold(finishReason, "finished") {
		worker.Status = "failed"
		worker.LastError = finishReason
	}
	worker.ReadyForPrompt = false
	worker.Events = append(worker.Events, Event{Type: "completed", FinishReason: finishReason, TokensOutput: tokensOutput, CreatedAt: time.Now().UTC()})
	return worker, s.Save(worker)
}

func (s Store) dir() string {
	configHome := strings.TrimSpace(s.ConfigHome)
	if configHome == "" {
		configHome = ".codog"
	}
	return filepath.Join(configHome, "workers")
}

func normalizeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func newID(now time.Time) string {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("worker_%d", now.UnixNano())
	}
	return fmt.Sprintf("worker_%d_%s", now.Unix(), hex.EncodeToString(bytes[:]))
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("worker_id is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid worker_id %q", id)
	}
	return nil
}
