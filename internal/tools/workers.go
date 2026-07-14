package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Register image decoders for read_image and notebook image outputs.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/nudges"
	"github.com/Rememorio/codog/internal/provisional"
	"github.com/Rememorio/codog/internal/recovery"
	"github.com/Rememorio/codog/internal/reportconformance"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/roadmap"
	"github.com/Rememorio/codog/internal/taskpacket"
	"github.com/Rememorio/codog/internal/workers"
)

func (RecoveryAttemptTool) Permission() Permission { return PermissionReadOnly }

func (t RecoveryAttemptTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Scenario        string `json:"scenario"`
		FailureSummary  string `json:"failure_summary"`
		FailedStepIndex *int   `json:"failed_step_index"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	scenario, err := recovery.ParseScenario(payload.Scenario)
	if err != nil {
		return "", err
	}
	if payload.FailedStepIndex != nil && *payload.FailedStepIndex < 0 {
		return "", errors.New("failed_step_index must be non-negative")
	}
	report, err := recovery.NewStore(t.ConfigHome).Attempt(scenario, recovery.AttemptOptions{
		FailureSummary:  payload.FailureSummary,
		FailedStepIndex: payload.FailedStepIndex,
	})
	if err != nil {
		return "", err
	}
	return pretty(report), nil
}

type RecoveryStatusTool struct {
	ConfigHome string
}

func (RecoveryStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "recovery_status",
		Description: "Read recovery attempt status and ledger entries for automatic recovery recipes.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"scenario": map[string]any{"type": "string"},
			},
		},
	}
}

func (RecoveryStatusTool) Permission() Permission { return PermissionReadOnly }

func (t RecoveryStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	scenario, hasScenario, err := parseOptionalRecoveryScenario(input)
	if err != nil {
		return "", err
	}
	store := recovery.NewStore(t.ConfigHome)
	if hasScenario {
		status, err := store.Status(scenario)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "recovery_status", "status": status}), nil
	}
	entries, err := store.List()
	if err != nil {
		return "", err
	}
	statuses := []recovery.StatusReport{}
	for _, scenario := range recovery.AllScenarios() {
		status, err := store.Status(scenario)
		if err != nil {
			return "", err
		}
		statuses = append(statuses, status)
	}
	return pretty(map[string]any{"kind": "recovery_ledger", "statuses": statuses, "entries": entries}), nil
}

type WorkerCreateTool struct {
	Workspace    string
	ConfigHome   string
	TrustedRoots []string
}

func (WorkerCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_create",
		Description: "Create a coding worker control record ready for prompt delivery.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"cwd":                             map[string]any{"type": "string"},
				"trusted_roots":                   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"auto_recover_prompt_misdelivery": map[string]any{"type": "boolean"},
			},
			"required": []string{"cwd"},
		},
	}
}

func (WorkerCreateTool) Permission() Permission { return PermissionDanger }

func (t WorkerCreateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		CWD                          string   `json:"cwd"`
		TrustedRoots                 []string `json:"trusted_roots"`
		AutoRecoverPromptMisdelivery *bool    `json:"auto_recover_prompt_misdelivery,omitempty"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	cwd, err := safePath(t.Workspace, payload.CWD, false)
	if err != nil {
		return "", err
	}
	autoRecover := true
	if payload.AutoRecoverPromptMisdelivery != nil {
		autoRecover = *payload.AutoRecoverPromptMisdelivery
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Create(cwd, mergeTrustedRoots(t.TrustedRoots, payload.TrustedRoots), autoRecover)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

func mergeTrustedRoots(configRoots []string, perCallRoots []string) []string {
	out := make([]string, 0, len(configRoots)+len(perCallRoots))
	seen := map[string]bool{}
	for _, root := range append(append([]string(nil), configRoots...), perCallRoots...) {
		root = strings.TrimSpace(root)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

type WorkerListTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_list",
		Description: "List coding worker control records with optional status filters.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"status":      map[string]any{"type": "string"},
				"task_status": map[string]any{"type": "string"},
			},
		},
	}
}

func (WorkerListTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Status     string `json:"status"`
		TaskStatus string `json:"task_status"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	status := strings.TrimSpace(payload.Status)
	taskStatus := strings.TrimSpace(payload.TaskStatus)
	list, err := workerStore(t.ConfigHome, t.Workspace).List()
	if err != nil {
		return "", err
	}
	out := make([]workers.Worker, 0, len(list))
	getter := WorkerGetTool(t)
	for _, worker := range list {
		worker = getter.withTaskStatus(worker)
		if status != "" && !strings.EqualFold(worker.Status, status) {
			continue
		}
		if taskStatus != "" && !strings.EqualFold(worker.TaskStatus, taskStatus) {
			continue
		}
		out = append(out, worker)
	}
	return pretty(map[string]any{
		"kind":        "worker_list",
		"total":       len(out),
		"status":      status,
		"task_status": taskStatus,
		"workers":     out,
	}), nil
}

type WorkerGetTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerGetTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_get", "Fetch the current worker state and event history.")
}

func (WorkerGetTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Get(id)
	if err != nil {
		return "", err
	}
	worker = t.withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerObserveTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerObserveTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_observe",
		Description: "Feed a terminal snapshot into worker state detection.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id":   map[string]any{"type": "string"},
				"screen_text": map[string]any{"type": "string"},
			},
			"required": []string{"worker_id", "screen_text"},
		},
	}
}

func (WorkerObserveTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerObserveTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID   string `json:"worker_id"`
		ScreenText string `json:"screen_text"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Observe(payload.WorkerID, payload.ScreenText)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool(t).withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerResolveTrustTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerResolveTrustTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_resolve_trust", "Resolve a detected worker trust prompt.")
}

func (WorkerResolveTrustTool) Permission() Permission { return PermissionDanger }

func (t WorkerResolveTrustTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).ResolveTrust(id)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool(t).withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerAwaitReadyTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerAwaitReadyTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_await_ready", "Return the current ready-for-prompt verdict for a worker.")
}

func (WorkerAwaitReadyTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerAwaitReadyTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	snapshot, err := workerStore(t.ConfigHome, t.Workspace).AwaitReady(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(snapshot.TaskID) != "" {
		if task, err := taskStore(t.ConfigHome, t.Workspace).Status(snapshot.TaskID); err == nil {
			snapshot.TaskStatus = task.Status
		}
	}
	return pretty(snapshot), nil
}

type WorkerSendPromptTool struct {
	Workspace  string
	ConfigHome string
	ConfigEnv  map[string]string
	Executable string
}

func (WorkerSendPromptTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_send_prompt",
		Description: "Send a task prompt to a ready worker and run it as a background Codog prompt.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string"},
				"prompt":    map[string]any{"type": "string"},
				"task_receipt": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"repo":               map[string]any{"type": "string"},
						"task_kind":          map[string]any{"type": "string"},
						"source_surface":     map[string]any{"type": "string"},
						"expected_artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"objective_preview":  map[string]any{"type": "string"},
					},
					"required": []string{"repo", "task_kind", "source_surface", "objective_preview"},
				},
			},
			"required": []string{"worker_id"},
		},
	}
}

func (WorkerSendPromptTool) Permission() Permission { return PermissionDanger }

func (t WorkerSendPromptTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID    string               `json:"worker_id"`
		Prompt      string               `json:"prompt"`
		TaskReceipt *workers.TaskReceipt `json:"task_receipt"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" && payload.TaskReceipt != nil {
		prompt = strings.TrimSpace(payload.TaskReceipt.ObjectivePreview)
	}
	if prompt == "" {
		return "", errors.New("prompt or task_receipt.objective_preview is required")
	}
	if err := validateWorkerReceipt(payload.TaskReceipt); err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	snapshot, err := store.AwaitReady(payload.WorkerID)
	if err != nil {
		return "", err
	}
	if !snapshot.ReadyForPrompt {
		return "", fmt.Errorf("worker %s is not ready for prompt", payload.WorkerID)
	}
	executable := strings.TrimSpace(t.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{Kind: "worker", Env: env})
	if err != nil {
		return "", err
	}
	worker, err := store.SendPrompt(payload.WorkerID, prompt, payload.TaskReceipt, task.ID)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerRestartTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerRestartTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_restart", "Restart the background task attached to a worker.")
}

func (WorkerRestartTool) Permission() Permission { return PermissionDanger }

func (t WorkerRestartTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	worker, err := store.Get(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(worker.TaskID) == "" {
		return "", errors.New("worker has no task to restart")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Restart(worker.TaskID, worker.CWD)
	if err != nil {
		return "", err
	}
	worker, err = store.Restart(id, task.ID)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerTerminateTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerTerminateTool) Definition() anthropic.ToolDefinition {
	return workerIDToolDefinition("worker_terminate", "Terminate a worker and stop its attached task when present.")
}

func (WorkerTerminateTool) Permission() Permission { return PermissionDanger }

func (t WorkerTerminateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	id, err := parseWorkerID(input)
	if err != nil {
		return "", err
	}
	store := workerStore(t.ConfigHome, t.Workspace)
	worker, err := store.Get(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(worker.TaskID) != "" {
		_, _ = taskStore(t.ConfigHome, t.Workspace).Stop(worker.TaskID)
	}
	worker, err = store.Terminate(id)
	if err != nil {
		return "", err
	}
	return pretty(worker), nil
}

type WorkerObserveCompletionTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerObserveCompletionTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_observe_completion",
		Description: "Record worker session completion and classify the finish reason.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id":     map[string]any{"type": "string"},
				"finish_reason": map[string]any{"type": "string"},
				"tokens_output": map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"worker_id", "finish_reason", "tokens_output"},
		},
	}
}

func (WorkerObserveCompletionTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerObserveCompletionTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID     string `json:"worker_id"`
		FinishReason string `json:"finish_reason"`
		TokensOutput int64  `json:"tokens_output"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.TokensOutput < 0 {
		return "", errors.New("tokens_output must be non-negative")
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).Complete(payload.WorkerID, payload.FinishReason, payload.TokensOutput)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool(t).withTaskStatus(worker)
	return pretty(worker), nil
}

type WorkerStartupTimeoutTool struct {
	Workspace  string
	ConfigHome string
}

func (WorkerStartupTimeoutTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "worker_startup_timeout",
		Description: "Record a worker startup timeout with evidence and classify the likely failure mode.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id":               map[string]any{"type": "string"},
				"last_lifecycle_state":    map[string]any{"type": "string"},
				"last_lifecycle_at":       map[string]any{"type": "string", "description": "RFC3339 timestamp for the last lifecycle observation."},
				"pane_command":            map[string]any{"type": "string"},
				"pane_observed_at":        map[string]any{"type": "string", "description": "RFC3339 timestamp for the pane observation."},
				"command_started_at":      map[string]any{"type": "string", "description": "RFC3339 timestamp for the worker command start."},
				"prompt_sent_at":          map[string]any{"type": "string", "description": "RFC3339 timestamp for prompt delivery."},
				"prompt_acceptance_state": map[string]any{"type": "string"},
				"trust_prompt_detected":   map[string]any{"type": "boolean"},
				"transport_healthy":       map[string]any{"type": "boolean"},
				"transport_health":        map[string]any{"type": "string"},
				"mcp_healthy":             map[string]any{"type": "boolean"},
				"mcp_health":              map[string]any{"type": "string"},
				"elapsed_seconds":         map[string]any{"type": "integer", "minimum": 0},
			},
			"required": []string{"worker_id"},
		},
	}
}

func (WorkerStartupTimeoutTool) Permission() Permission { return PermissionReadOnly }

func (t WorkerStartupTimeoutTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID              string `json:"worker_id"`
		LastLifecycleState    string `json:"last_lifecycle_state"`
		LastLifecycleAt       string `json:"last_lifecycle_at"`
		PaneCommand           string `json:"pane_command"`
		PaneObservedAt        string `json:"pane_observed_at"`
		CommandStartedAt      string `json:"command_started_at"`
		PromptSentAt          string `json:"prompt_sent_at"`
		PromptAcceptanceState string `json:"prompt_acceptance_state"`
		TrustPromptDetected   bool   `json:"trust_prompt_detected"`
		TransportHealthy      *bool  `json:"transport_healthy"`
		TransportHealth       string `json:"transport_health"`
		MCPHealthy            *bool  `json:"mcp_healthy"`
		MCPHealth             string `json:"mcp_health"`
		ElapsedSeconds        int64  `json:"elapsed_seconds"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if payload.ElapsedSeconds < 0 {
		return "", errors.New("elapsed_seconds must be non-negative")
	}
	evidence := workers.StartupEvidence{
		LastLifecycleState:    payload.LastLifecycleState,
		PaneCommand:           payload.PaneCommand,
		PromptAcceptanceState: payload.PromptAcceptanceState,
		TrustPromptDetected:   payload.TrustPromptDetected,
		TransportHealthy:      payload.TransportHealthy,
		TransportHealth:       payload.TransportHealth,
		MCPHealthy:            payload.MCPHealthy,
		MCPHealth:             payload.MCPHealth,
		ElapsedSeconds:        payload.ElapsedSeconds,
	}
	var err error
	if evidence.LastLifecycleAt, err = parseOptionalWorkerTime(payload.LastLifecycleAt, "last_lifecycle_at"); err != nil {
		return "", err
	}
	if evidence.PaneObservedAt, err = parseOptionalWorkerTime(payload.PaneObservedAt, "pane_observed_at"); err != nil {
		return "", err
	}
	if evidence.CommandStartedAt, err = parseOptionalWorkerTime(payload.CommandStartedAt, "command_started_at"); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.PromptSentAt) != "" {
		promptSentAt, err := parseOptionalWorkerTime(payload.PromptSentAt, "prompt_sent_at")
		if err != nil {
			return "", err
		}
		evidence.PromptSentAt = &promptSentAt
	}
	worker, err := workerStore(t.ConfigHome, t.Workspace).ObserveStartupTimeout(payload.WorkerID, evidence)
	if err != nil {
		return "", err
	}
	worker = WorkerGetTool(t).withTaskStatus(worker)
	return pretty(worker), nil
}

func (t WorkerGetTool) withTaskStatus(worker workers.Worker) workers.Worker {
	if strings.TrimSpace(worker.TaskID) == "" {
		return worker
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(worker.TaskID)
	if err != nil {
		worker.TaskStatus = "unknown"
		if worker.LastError == "" {
			worker.LastError = err.Error()
		}
		return worker
	}
	worker.TaskStatus = task.Status
	return worker
}

func workerIDToolDefinition(name string, description string) anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string"},
			},
			"required": []string{"worker_id"},
		},
	}
}

func parseWorkerID(input json.RawMessage) (string, error) {
	var payload struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.WorkerID) == "" {
		return "", errors.New("worker_id is required")
	}
	return strings.TrimSpace(payload.WorkerID), nil
}

func parseOptionalWorkerTime(value string, field string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	return parsed.UTC(), nil
}

func parseOptionalRecoveryScenario(input json.RawMessage) (recovery.Scenario, bool, error) {
	var payload struct {
		Scenario string `json:"scenario"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", false, err
		}
	}
	if strings.TrimSpace(payload.Scenario) == "" {
		return "", false, nil
	}
	scenario, err := recovery.ParseScenario(payload.Scenario)
	if err != nil {
		return "", false, err
	}
	return scenario, true, nil
}

func validateWorkerReceipt(receipt *workers.TaskReceipt) error {
	if receipt == nil {
		return nil
	}
	required := map[string]string{
		"repo":              receipt.Repo,
		"task_kind":         receipt.TaskKind,
		"source_surface":    receipt.SourceSurface,
		"objective_preview": receipt.ObjectivePreview,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("task_receipt.%s is required", field)
		}
	}
	return nil
}

func workerStore(configHome string, workspace string) workers.Store {
	configHome = strings.TrimSpace(configHome)
	if configHome == "" {
		if workspace == "" {
			workspace = "."
		}
		configHome = filepath.Join(workspace, ".codog")
	}
	return workers.NewStore(configHome)
}

type TaskCreateTool struct {
	Workspace  string
	ConfigHome string
	ConfigEnv  map[string]string
	Executable string
}

func (TaskCreateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_create",
		Description: "Start a background shell task in the workspace and return its task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":     map[string]any{"type": "string"},
				"prompt":      map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"kind":        map[string]any{"type": "string"},
				"session_id":  map[string]any{"type": "string"},
				"scope_binding": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"owner":          map[string]any{"type": "string"},
						"workflow_scope": map[string]any{"type": "string"},
						"watcher_action": map[string]any{"type": "string", "enum": []string{"act", "observe", "ignore"}},
					},
					"additionalProperties": false,
				},
				"owner":          map[string]any{"type": "string"},
				"workflow_scope": map[string]any{"type": "string"},
				"watcher_action": map[string]any{"type": "string", "enum": []string{"act", "observe", "ignore"}},
				"restart_policy": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"enabled":       map[string]any{"type": "boolean"},
						"mode":          map[string]any{"type": "string", "enum": []string{"on-failure", "always"}},
						"max_attempts":  map[string]any{"type": "integer", "minimum": 0},
						"delay_seconds": map[string]any{"type": "integer", "minimum": 0},
					},
				},
			},
			"anyOf":                []map[string]any{{"required": []string{"command"}}, {"required": []string{"prompt"}}},
			"additionalProperties": false,
		},
	}
}

func (TaskCreateTool) Permission() Permission { return PermissionDanger }

func (t TaskCreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command       string                    `json:"command"`
		Prompt        string                    `json:"prompt"`
		Description   string                    `json:"description"`
		Kind          string                    `json:"kind"`
		SessionID     string                    `json:"session_id"`
		Restart       *background.RestartPolicy `json:"restart_policy"`
		ScopeBinding  background.ScopeBinding   `json:"scope_binding"`
		Owner         string                    `json:"owner"`
		WorkflowScope string                    `json:"workflow_scope"`
		WatcherAction string                    `json:"watcher_action"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	command := strings.TrimSpace(payload.Command)
	prompt := strings.TrimSpace(payload.Prompt)
	if command == "" && prompt == "" {
		return "", errors.New("command or prompt is required")
	}
	if command != "" && prompt != "" {
		return "", errors.New("command and prompt cannot both be provided")
	}
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	if prompt != "" {
		executable := strings.TrimSpace(t.Executable)
		if executable == "" {
			executable, err = os.Executable()
			if err != nil {
				return "", err
			}
		}
		taskPrompt := prompt
		if description := strings.TrimSpace(payload.Description); description != "" {
			taskPrompt = "Task: " + description + "\n\n" + taskPrompt
		}
		kind := strings.TrimSpace(payload.Kind)
		if kind == "" {
			kind = "task"
		}
		task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(buildTeamTaskCommand(executable, taskPrompt), cwd, background.RunOptions{
			Kind:          kind,
			SessionID:     payload.SessionID,
			RestartPolicy: payload.Restart,
			Env:           env,
			Prompt:        prompt,
			Description:   strings.TrimSpace(payload.Description),
			ScopeBinding:  toolScopeBinding(payload.ScopeBinding, payload.Owner, payload.WorkflowScope, payload.WatcherAction),
		})
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{
			"task_id":     task.ID,
			"status":      task.Status,
			"prompt":      prompt,
			"description": strings.TrimSpace(payload.Description),
			"created_at":  task.StartedAt,
			"task":        task,
		}), nil
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(command, cwd, background.RunOptions{
		Kind:          payload.Kind,
		SessionID:     payload.SessionID,
		RestartPolicy: payload.Restart,
		Env:           env,
		ScopeBinding:  toolScopeBinding(payload.ScopeBinding, payload.Owner, payload.WorkflowScope, payload.WatcherAction),
	})
	if err != nil {
		return "", err
	}
	return pretty(taskCompatibilityFields(task)), nil
}

type RunTaskPacketTool struct {
	Workspace  string
	ConfigHome string
	ConfigEnv  map[string]string
	Executable string
}

func (RunTaskPacketTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "run_task_packet",
		Description: "Create a background task from a structured task packet.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"objective":           map[string]any{"type": "string"},
				"scope":               map[string]any{"type": "string"},
				"scope_path":          map[string]any{"type": "string"},
				"repo":                map[string]any{"type": "string"},
				"worktree":            map[string]any{"type": "string"},
				"branch_policy":       map[string]any{"type": "string"},
				"acceptance_tests":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"acceptance_criteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"resources": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"kind":  map[string]any{"type": "string"},
							"value": map[string]any{"type": "string"},
						},
						"required": []string{"kind", "value"},
					},
				},
				"model":              map[string]any{"type": "string"},
				"provider":           map[string]any{"type": "string"},
				"permission_profile": map[string]any{"type": "string"},
				"commit_policy":      map[string]any{"type": "string"},
				"reporting_contract": map[string]any{"type": "string"},
				"reporting_targets":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"escalation_policy":  map[string]any{"type": "string"},
				"recovery_policy":    map[string]any{"type": "string"},
				"verification_plan":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{
				"objective",
				"scope",
				"repo",
				"branch_policy",
				"commit_policy",
			},
		},
	}
}

func (RunTaskPacketTool) Permission() Permission { return PermissionDanger }

func (t RunTaskPacketTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	packet, err := taskpacket.Parse(input)
	if err != nil {
		return "", err
	}
	if err := taskpacket.Validate(packet); err != nil {
		return "", err
	}
	resolvedScope, err := taskpacket.ResolveScope(t.Workspace, packet)
	if err != nil {
		return "", err
	}
	executable := strings.TrimSpace(t.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	prompt := renderTaskPacketPrompt(packet)
	taskPacketData, err := json.Marshal(packet)
	if err != nil {
		return "", err
	}
	env, err := toolEnvironment(ctx, t.ConfigHome, t.ConfigEnv)
	if err != nil {
		return "", err
	}
	cwd, err := toolCWD(ctx, t.ConfigHome, t.Workspace)
	if err != nil {
		return "", err
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).RunWithOptions(buildTeamTaskCommand(executable, prompt), cwd, background.RunOptions{
		Kind:        "task_packet",
		Env:         env,
		Prompt:      prompt,
		Description: packet.Objective,
		TaskPacket:  taskPacketData,
	})
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{
		"task_id":        task.ID,
		"status":         task.Status,
		"prompt":         prompt,
		"description":    packet.Objective,
		"task_packet":    packet,
		"resolved_scope": resolvedScope,
		"created_at":     task.StartedAt,
		"task":           task,
	}), nil
}

func renderTaskPacketPrompt(packet taskpacket.Packet) string {
	var builder strings.Builder
	builder.WriteString("Execute this structured task packet.\n\n")
	builder.WriteString("Objective:\n")
	builder.WriteString(strings.TrimSpace(packet.Objective))
	builder.WriteString("\n\nScope:\n")
	builder.WriteString(string(packet.Scope))
	if strings.TrimSpace(packet.ScopePath) != "" {
		builder.WriteString(" ")
		builder.WriteString(strings.TrimSpace(packet.ScopePath))
	}
	builder.WriteString("\n\nRepository:\n")
	builder.WriteString(strings.TrimSpace(packet.Repo))
	if strings.TrimSpace(packet.Worktree) != "" {
		builder.WriteString("\n\nWorktree:\n")
		builder.WriteString(strings.TrimSpace(packet.Worktree))
	}
	builder.WriteString("\n\nBranch policy:\n")
	builder.WriteString(strings.TrimSpace(packet.BranchPolicy))
	if len(packet.AcceptanceTests) > 0 {
		builder.WriteString("\n\nAcceptance tests:\n")
		for _, test := range packet.AcceptanceTests {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(test))
			builder.WriteString("\n")
		}
	}
	if len(packet.AcceptanceCriteria) > 0 {
		builder.WriteString("\n\nAcceptance criteria:\n")
		for _, criterion := range packet.AcceptanceCriteria {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(criterion))
			builder.WriteString("\n")
		}
	}
	if len(packet.Resources) > 0 {
		builder.WriteString("\n\nResources:\n")
		for _, resource := range packet.Resources {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(resource.Kind))
			builder.WriteString(": ")
			builder.WriteString(strings.TrimSpace(resource.Value))
			builder.WriteString("\n")
		}
	}
	builder.WriteString("\nCommit policy:\n")
	builder.WriteString(strings.TrimSpace(packet.CommitPolicy))
	if strings.TrimSpace(packet.ReportingContract) != "" {
		builder.WriteString("\n\nReporting contract:\n")
		builder.WriteString(strings.TrimSpace(packet.ReportingContract))
	}
	if len(packet.ReportingTargets) > 0 {
		builder.WriteString("\n\nReporting targets:\n")
		for _, target := range packet.ReportingTargets {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(target))
			builder.WriteString("\n")
		}
	}
	if strings.TrimSpace(packet.EscalationPolicy) != "" {
		builder.WriteString("\n\nEscalation policy:\n")
		builder.WriteString(strings.TrimSpace(packet.EscalationPolicy))
	}
	if strings.TrimSpace(packet.RecoveryPolicy) != "" {
		builder.WriteString("\n\nRecovery policy:\n")
		builder.WriteString(strings.TrimSpace(packet.RecoveryPolicy))
	}
	if len(packet.VerificationPlan) > 0 {
		builder.WriteString("\n\nVerification plan:\n")
		for _, step := range packet.VerificationPlan {
			builder.WriteString("- ")
			builder.WriteString(strings.TrimSpace(step))
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

type TaskListTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskListTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_list",
		Description: "List background tasks, optionally filtered by session or kind.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
				"kind":       map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (TaskListTool) Permission() Permission { return PermissionReadOnly }

func (t TaskListTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}
	if len(input) != 0 {
		if err := json.Unmarshal(input, &payload); err != nil {
			return "", err
		}
	}
	tasks, err := taskStore(t.ConfigHome, t.Workspace).List()
	if err != nil {
		return "", err
	}
	tasks = background.FilterBySession(tasks, payload.SessionID)
	tasks = background.FilterByKind(tasks, payload.Kind)
	views := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		views = append(views, taskCompatibilityFields(task))
	}
	return pretty(map[string]any{"tasks": views, "total": len(views), "count": len(views)}), nil
}

func taskCompatibilityFields(task background.Task) map[string]any {
	data, err := json.Marshal(task)
	if err != nil {
		return map[string]any{
			"task_id":    task.ID,
			"created_at": task.StartedAt,
			"updated_at": taskUpdatedAt(task),
			"task":       task,
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		fields = map[string]any{}
	}
	fields["task_id"] = task.ID
	fields["created_at"] = task.StartedAt
	fields["updated_at"] = taskUpdatedAt(task)
	fields["task"] = task
	return fields
}

func taskUpdatedAt(task background.Task) time.Time {
	updated := task.StartedAt
	if task.CompletedAt != nil && task.CompletedAt.After(updated) {
		updated = *task.CompletedAt
	}
	for _, message := range task.Messages {
		if message.CreatedAt.After(updated) {
			updated = message.CreatedAt
		}
	}
	return updated
}

func taskIDRequirement(extra ...string) []map[string]any {
	fields := append([]string{"id", "task_id", "taskId"}, extra...)
	options := make([]map[string]any, 0, len(fields))
	for _, field := range fields {
		options = append(options, map[string]any{"required": []string{field}})
	}
	return options
}

type TaskStatusTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_status",
		Description: "Get background task metadata by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "string"},
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
			},
			"anyOf":                taskIDRequirement(),
			"additionalProperties": false,
		},
	}
}

func (TaskStatusTool) Permission() Permission { return PermissionReadOnly }

func (t TaskStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		ID          string `json:"id"`
		TaskID      string `json:"task_id"`
		TaskIDAlias string `json:"taskId"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.ID, payload.TaskID, payload.TaskIDAlias)
	if id == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(id)
	if err != nil {
		return "", err
	}
	return pretty(taskCompatibilityFields(task)), nil
}

type TaskGetTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskGetTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_get",
		Description: "Get background task metadata and stored task messages by task id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
			},
			"anyOf":                taskIDRequirement(),
			"additionalProperties": false,
		},
	}
}

func (TaskGetTool) Permission() Permission { return PermissionReadOnly }

func (t TaskGetTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID      string `json:"task_id"`
		TaskIDAlias string `json:"taskId"`
		ID          string `json:"id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.TaskID, payload.TaskIDAlias, payload.ID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Status(id)
	if err != nil {
		return "", err
	}
	return pretty(taskCompatibilityFields(task)), nil
}

type TaskUpdateTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskUpdateTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_update",
		Description: "Append a message update to a background task registry entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"taskId":  map[string]any{"type": "string"},
				"id":      map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
			"required":             []string{"message"},
			"anyOf":                taskIDRequirement(),
			"additionalProperties": false,
		},
	}
}

func (TaskUpdateTool) Permission() Permission { return PermissionDanger }

func (t TaskUpdateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID      string `json:"task_id"`
		TaskIDAlias string `json:"taskId"`
		ID          string `json:"id"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.TaskID, payload.TaskIDAlias, payload.ID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		return "", errors.New("task update message is required")
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).Update(id, message)
	if err != nil {
		return "", err
	}
	last := ""
	if len(task.Messages) > 0 {
		last = task.Messages[len(task.Messages)-1].Message
	}
	return pretty(map[string]any{
		"task_id":       task.ID,
		"taskId":        task.ID,
		"id":            task.ID,
		"status":        task.Status,
		"message_count": len(task.Messages),
		"last_message":  last,
	}), nil
}

type TaskHeartbeatTool struct {
	Workspace  string
	ConfigHome string
}

func (TaskHeartbeatTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "task_heartbeat",
		Description: "Record a heartbeat for a background task and return updated task metadata.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":              map[string]any{"type": "string"},
				"task_id":         map[string]any{"type": "string"},
				"taskId":          map[string]any{"type": "string"},
				"status":          map[string]any{"type": "string"},
				"transport_alive": map[string]any{"type": "boolean"},
				"observed_at":     map[string]any{"type": "string", "format": "date-time"},
				"provenance": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source_kind": map[string]any{"type": "string"},
						"environment": map[string]any{"type": "string"},
						"channel":     map[string]any{"type": "string"},
						"emitter":     map[string]any{"type": "string"},
						"confidence":  map[string]any{"type": "string"},
					},
					"additionalProperties": false,
				},
				"source_kind": map[string]any{"type": "string"},
				"environment": map[string]any{"type": "string"},
				"channel":     map[string]any{"type": "string"},
				"emitter":     map[string]any{"type": "string"},
				"confidence":  map[string]any{"type": "string"},
			},
			"anyOf":                taskIDRequirement(),
			"additionalProperties": false,
		},
	}
}

func (TaskHeartbeatTool) Permission() Permission { return PermissionDanger }

func (t TaskHeartbeatTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		TaskID         string                     `json:"task_id"`
		TaskIDAlias    string                     `json:"taskId"`
		ID             string                     `json:"id"`
		Status         string                     `json:"status"`
		TransportAlive *bool                      `json:"transport_alive"`
		ObservedAt     *time.Time                 `json:"observed_at"`
		Provenance     background.EventProvenance `json:"provenance"`
		SourceKind     string                     `json:"source_kind"`
		Environment    string                     `json:"environment"`
		Channel        string                     `json:"channel"`
		Emitter        string                     `json:"emitter"`
		Confidence     string                     `json:"confidence"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	id := firstNonEmpty(payload.TaskID, payload.TaskIDAlias, payload.ID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("task_id is required")
	}
	transportAlive := true
	if payload.TransportAlive != nil {
		transportAlive = *payload.TransportAlive
	}
	observedAt := time.Time{}
	if payload.ObservedAt != nil {
		observedAt = *payload.ObservedAt
	}
	task, err := taskStore(t.ConfigHome, t.Workspace).UpdateHeartbeat(id, background.LaneHeartbeat{
		ObservedAt:     observedAt,
		TransportAlive: transportAlive,
		Status:         payload.Status,
		Provenance:     toolHeartbeatProvenance(payload.Provenance, payload.SourceKind, payload.Environment, payload.Channel, payload.Emitter, payload.Confidence),
	})
	if err != nil {
		return "", err
	}
	return pretty(taskCompatibilityFields(task)), nil
}

func toolHeartbeatProvenance(provenance background.EventProvenance, sourceKind string, environment string, channel string, emitter string, confidence string) background.EventProvenance {
	if strings.TrimSpace(sourceKind) != "" {
		provenance.SourceKind = sourceKind
	}
	if strings.TrimSpace(environment) != "" {
		provenance.Environment = environment
	}
	if strings.TrimSpace(channel) != "" {
		provenance.Channel = channel
	}
	if strings.TrimSpace(emitter) != "" {
		provenance.Emitter = emitter
	}
	if strings.TrimSpace(confidence) != "" {
		provenance.Confidence = confidence
	}
	return provenance
}

func toolScopeBinding(binding background.ScopeBinding, owner string, workflowScope string, watcherAction string) background.ScopeBinding {
	if strings.TrimSpace(owner) != "" {
		binding.Owner = owner
	}
	if strings.TrimSpace(workflowScope) != "" {
		binding.WorkflowScope = workflowScope
	}
	if strings.TrimSpace(watcherAction) != "" {
		binding.WatcherAction = watcherAction
	}
	return binding
}

type NudgeTool struct {
	ConfigHome string
}

var nudgeActionNames = []string{"observe", "ack", "acknowledge", "status", "list"}

func (NudgeTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "nudge",
		Description: "Record, acknowledge, or inspect a recurring nudge cycle.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": append([]string(nil), nudgeActionNames...)},
				"nudge_id":     map[string]any{"type": "string"},
				"cycle_id":     map[string]any{"type": "string"},
				"prompt":       map[string]any{"type": "string"},
				"delivered_at": map[string]any{"type": "string", "format": "date-time"},
				"response_id":  map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (NudgeTool) Permission() Permission { return PermissionReadOnly }

func (t NudgeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action      string `json:"action"`
		NudgeID     string `json:"nudge_id"`
		CycleID     string `json:"cycle_id"`
		Prompt      string `json:"prompt"`
		DeliveredAt string `json:"delivered_at"`
		ResponseID  string `json:"response_id"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = "observe"
	}
	store := nudges.NewStore(t.ConfigHome)
	switch action {
	case "list":
		records, err := store.List()
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "nudge_list", "records": records, "count": len(records)}), nil
	case "status":
		record, err := store.Get(payload.NudgeID, payload.CycleID)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "nudge_status", "record": record}), nil
	}
	delivery, err := nudgeDeliveryFromPayload(payload.NudgeID, payload.CycleID, payload.Prompt, payload.DeliveredAt, payload.ResponseID)
	if err != nil {
		return "", err
	}
	switch action {
	case "observe":
		observation, err := store.Observe(delivery)
		if err != nil {
			return "", err
		}
		return pretty(observation), nil
	case "ack", "acknowledge":
		observation, err := store.Acknowledge(delivery)
		if err != nil {
			return "", err
		}
		return pretty(observation), nil
	default:
		return "", unknownToolActionError("nudge", payload.Action, nudgeActionNames)
	}
}

func nudgeDeliveryFromPayload(nudgeID string, cycleID string, prompt string, deliveredAt string, responseID string) (nudges.Delivery, error) {
	delivery := nudges.Delivery{
		NudgeID:    nudgeID,
		CycleID:    cycleID,
		Prompt:     prompt,
		ResponseID: responseID,
	}
	if strings.TrimSpace(deliveredAt) != "" {
		parsed, err := time.Parse(time.RFC3339, deliveredAt)
		if err != nil {
			return nudges.Delivery{}, err
		}
		delivery.DeliveredAt = parsed
	}
	return delivery, nil
}

type ProvisionalStatusTool struct {
	ConfigHome string
}

var provisionalStatusActionNames = []string{"observe", "get", "status", "list"}

func (ProvisionalStatusTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "provisional_status",
		Description: "Deduplicate provisional in-flight acknowledgements while preserving raw repeats for audit.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":          map[string]any{"type": "string", "enum": append([]string(nil), provisionalStatusActionNames...)},
				"channel":         map[string]any{"type": "string"},
				"owner":           map[string]any{"type": "string"},
				"status":          map[string]any{"type": "string"},
				"progress_state":  map[string]any{"type": "string"},
				"blocker":         map[string]any{"type": "string"},
				"eta":             map[string]any{"type": "string"},
				"message":         map[string]any{"type": "string"},
				"observed_at":     map[string]any{"type": "string", "format": "date-time"},
				"window_seconds":  map[string]any{"type": "integer", "minimum": 1},
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1},
				"timeout_policy":  map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}
}

func (ProvisionalStatusTool) Permission() Permission { return PermissionReadOnly }

func (t ProvisionalStatusTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action         string `json:"action"`
		Channel        string `json:"channel"`
		Owner          string `json:"owner"`
		Status         string `json:"status"`
		ProgressState  string `json:"progress_state"`
		Blocker        string `json:"blocker"`
		ETA            string `json:"eta"`
		Message        string `json:"message"`
		ObservedAt     string `json:"observed_at"`
		WindowSeconds  int    `json:"window_seconds"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		TimeoutPolicy  string `json:"timeout_policy"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = "observe"
	}
	store := provisional.NewStore(t.ConfigHome)
	switch action {
	case "list":
		states, err := store.List()
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "provisional_status_list", "states": states, "count": len(states)}), nil
	case "get", "status":
		state, err := store.Get(payload.Channel)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "provisional_status_state", "state": state}), nil
	case "observe":
		update, err := provisionalUpdateFromPayload(payload.Channel, payload.Owner, payload.Status, payload.ProgressState, payload.Blocker, payload.ETA, payload.Message, payload.ObservedAt, payload.WindowSeconds, payload.TimeoutSeconds, payload.TimeoutPolicy)
		if err != nil {
			return "", err
		}
		observation, err := store.Observe(update)
		if err != nil {
			return "", err
		}
		return pretty(observation), nil
	default:
		return "", unknownToolActionError("provisional_status", payload.Action, provisionalStatusActionNames)
	}
}

func provisionalUpdateFromPayload(channel string, owner string, status string, progressState string, blocker string, eta string, message string, observedAt string, windowSeconds int, timeoutSeconds int, timeoutPolicy string) (provisional.Update, error) {
	update := provisional.Update{
		Channel:       channel,
		Owner:         owner,
		Status:        status,
		ProgressState: progressState,
		Blocker:       blocker,
		ETA:           eta,
		Message:       message,
		TimeoutPolicy: timeoutPolicy,
	}
	if strings.TrimSpace(observedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, observedAt)
		if err != nil {
			return provisional.Update{}, err
		}
		update.ObservedAt = parsed
	}
	if windowSeconds > 0 {
		update.Window = time.Duration(windowSeconds) * time.Second
	}
	if timeoutSeconds > 0 {
		update.Timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return update, nil
}

type RoadmapPinpointTool struct {
	ConfigHome string
}

var roadmapPinpointActionNames = []string{"file", "update", "get", "list", "handoff"}

func (RoadmapPinpointTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "roadmap_pinpoint",
		Description: "Create, update, inspect, or list machine-readable roadmap pinpoints.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":        map[string]any{"type": "string", "enum": append([]string(nil), roadmapPinpointActionNames...)},
				"id":            map[string]any{"type": "string"},
				"title":         map[string]any{"type": "string"},
				"description":   map[string]any{"type": "string"},
				"state":         map[string]any{"type": "string", "enum": []string{"filed", "acknowledged", "in_progress", "blocked", "done", "superseded"}},
				"supersedes":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"superseded_by": map[string]any{"type": "string"},
				"related":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"report_id":     map[string]any{"type": "string"},
				"priority":      map[string]any{"type": "string", "enum": []string{"p0", "p1", "p2", "p3"}},
				"severity":      map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
				"impact":        map[string]any{"type": "string", "enum": []string{"user_facing_breakage", "operator_friction", "observability_debt", "long_tail_hardening"}},
				"priority_reason": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"blast_radius":        map[string]any{"type": "string"},
						"reproducibility":     map[string]any{"type": "string"},
						"automation_breakage": map[string]any{"type": "string"},
						"merge_risk":          map[string]any{"type": "string"},
						"rationale":           map[string]any{"type": "string"},
					},
				},
				"handoff": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"objective":              map[string]any{"type": "string"},
						"suspected_scope":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"evidence_refs":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"suggested_verification": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"readiness":              map[string]any{"type": "string", "enum": []string{"implementation_ready", "needs_repro", "needs_triage"}},
						"metadata":               map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
					},
				},
				"implementation": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":            map[string]any{"type": "string"},
							"lane_id":       map[string]any{"type": "string"},
							"task_id":       map[string]any{"type": "string"},
							"worktree_id":   map[string]any{"type": "string"},
							"worktree_path": map[string]any{"type": "string"},
							"pr_url":        map[string]any{"type": "string"},
							"pr_number":     map[string]any{"type": "integer", "minimum": 0},
							"status":        map[string]any{"type": "string"},
							"added_at":      map[string]any{"type": "string", "format": "date-time"},
						},
					},
				},
				"execution_results": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":            map[string]any{"type": "string"},
							"link_id":       map[string]any{"type": "string"},
							"lane_id":       map[string]any{"type": "string"},
							"status":        map[string]any{"type": "string"},
							"summary":       map[string]any{"type": "string"},
							"evidence_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"recorded_at":   map[string]any{"type": "string", "format": "date-time"},
						},
						"required": []string{"status"},
					},
				},
				"evidence": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"id":        map[string]any{"type": "string"},
							"role":      map[string]any{"type": "string", "enum": []string{"repro", "symptom", "root_cause_hint", "verification"}},
							"type":      map[string]any{"type": "string"},
							"reference": map[string]any{"type": "string"},
							"preview":   map[string]any{"type": "string"},
							"added_at":  map[string]any{"type": "string", "format": "date-time"},
						},
						"required": []string{"role", "reference"},
					},
				},
				"now": map[string]any{"type": "string", "format": "date-time"},
			},
			"additionalProperties": false,
		},
	}
}

func (RoadmapPinpointTool) Permission() Permission { return PermissionReadOnly }

func (t RoadmapPinpointTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action         string                   `json:"action"`
		ID             string                   `json:"id"`
		Title          string                   `json:"title"`
		Description    string                   `json:"description"`
		State          string                   `json:"state"`
		Supersedes     []string                 `json:"supersedes"`
		SupersededBy   string                   `json:"superseded_by"`
		Related        []string                 `json:"related"`
		ReportID       string                   `json:"report_id"`
		Evidence       []roadmapEvidencePayload `json:"evidence"`
		Priority       string                   `json:"priority"`
		Severity       string                   `json:"severity"`
		Impact         string                   `json:"impact"`
		PriorityReason struct {
			BlastRadius        string `json:"blast_radius"`
			Reproducibility    string `json:"reproducibility"`
			AutomationBreakage string `json:"automation_breakage"`
			MergeRisk          string `json:"merge_risk"`
			Rationale          string `json:"rationale"`
		} `json:"priority_reason"`
		Handoff          *roadmap.HandoffPacket       `json:"handoff"`
		Implementation   []roadmap.ImplementationLink `json:"implementation"`
		ExecutionResults []roadmap.ExecutionResult    `json:"execution_results"`
		Now              string                       `json:"now"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = "file"
	}
	store := roadmap.NewStore(t.ConfigHome)
	switch action {
	case "get", "handoff":
		item, err := store.Get(payload.ID)
		if err != nil {
			return "", err
		}
		if action == "handoff" {
			return pretty(map[string]any{"kind": "roadmap_pinpoint_handoff", "item_id": item.ID, "handoff": item.Handoff, "implementation": item.Implementation, "execution_results": item.ExecutionResults}), nil
		}
		return pretty(map[string]any{"kind": "roadmap_pinpoint_status", "item": item}), nil
	case "list":
		items, err := store.List()
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"kind": "roadmap_pinpoint_list", "items": items, "count": len(items)}), nil
	case "file", "update":
		now, err := parseOptionalRFC3339(payload.Now)
		if err != nil {
			return "", err
		}
		evidence, err := roadmapEvidenceFromPayload(payload.Evidence, now)
		if err != nil {
			return "", err
		}
		result, err := store.File(roadmap.Filing{
			ID:           payload.ID,
			Title:        payload.Title,
			Description:  payload.Description,
			State:        roadmap.State(payload.State),
			Supersedes:   payload.Supersedes,
			SupersededBy: payload.SupersededBy,
			Related:      payload.Related,
			ReportID:     payload.ReportID,
			Evidence:     evidence,
			Priority:     roadmap.Priority(payload.Priority),
			Severity:     roadmap.Severity(payload.Severity),
			Impact:       roadmap.ImpactClass(payload.Impact),
			PriorityReason: roadmap.PriorityReason{
				BlastRadius:        payload.PriorityReason.BlastRadius,
				Reproducibility:    payload.PriorityReason.Reproducibility,
				AutomationBreakage: payload.PriorityReason.AutomationBreakage,
				MergeRisk:          payload.PriorityReason.MergeRisk,
				Rationale:          payload.PriorityReason.Rationale,
			},
			Handoff:          payload.Handoff,
			Implementation:   payload.Implementation,
			ExecutionResults: payload.ExecutionResults,
			Now:              now,
		})
		if err != nil {
			return "", err
		}
		return pretty(result), nil
	default:
		return "", unknownToolActionError("roadmap", payload.Action, roadmapPinpointActionNames)
	}
}

type roadmapEvidencePayload struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Type      string `json:"type"`
	Reference string `json:"reference"`
	Preview   string `json:"preview"`
	AddedAt   string `json:"added_at"`
}

func roadmapEvidenceFromPayload(values []roadmapEvidencePayload, defaultTime time.Time) ([]roadmap.EvidenceAttachment, error) {
	evidence := make([]roadmap.EvidenceAttachment, 0, len(values))
	for _, value := range values {
		attachment := roadmap.EvidenceAttachment{
			ID:        value.ID,
			Role:      roadmap.EvidenceRole(value.Role),
			Type:      value.Type,
			Reference: value.Reference,
			Preview:   value.Preview,
		}
		if strings.TrimSpace(value.AddedAt) != "" {
			addedAt, err := time.Parse(time.RFC3339, value.AddedAt)
			if err != nil {
				return nil, err
			}
			attachment.AddedAt = addedAt
		} else {
			attachment.AddedAt = defaultTime
		}
		evidence = append(evidence, attachment)
	}
	return evidence, nil
}

type ReportBackpressureTool struct {
	ConfigHome string
}

type ReportSchemaTool struct{}

type flexibleStrings []string

func (s *flexibleStrings) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = flexibleStrings{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*s = flexibleStrings(multiple)
	return nil
}

func (s flexibleStrings) Values() []string {
	values := make([]string, 0, len(s))
	for _, value := range s {
		value = strings.TrimSpace(value)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

var reportSchemaActionNames = []string{"registry", "conformance", "conformance_fixtures"}

func (ReportSchemaTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "report_schema",
		Description: "Fetch structured report schemas or validate reporting consumer conformance bundles.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": append([]string(nil), reportSchemaActionNames...)},
				"input":  map[string]any{"type": "string"},
				"report": map[string]any{
					"oneOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
				},
				"schema_version": map[string]any{
					"oneOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
				},
				"field_family": map[string]any{
					"oneOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
				},
			},
			"additionalProperties": false,
		},
	}
}

func (ReportSchemaTool) Permission() Permission { return PermissionReadOnly }

func (ReportSchemaTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Action        string          `json:"action"`
		Input         string          `json:"input"`
		Report        flexibleStrings `json:"report"`
		SchemaVersion flexibleStrings `json:"schema_version"`
		FieldFamily   flexibleStrings `json:"field_family"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action == "" {
		action = "registry"
	}
	switch action {
	case "registry":
		registry := reportschema.FilterRegistry(reportschema.RegistryV1(), reportschema.RegistryFilter{
			ReportIDs:      payload.Report.Values(),
			SchemaVersions: payload.SchemaVersion.Values(),
			FieldFamilies:  payload.FieldFamily.Values(),
		})
		return pretty(map[string]any{"kind": "report_schema", "action": "registry", "status": "ok", "registry": registry}), nil
	case "conformance":
		if strings.TrimSpace(payload.Input) == "" {
			return "", errors.New("report_schema conformance input is required")
		}
		result, err := reportconformance.ValidateJSON([]byte(payload.Input))
		if err != nil {
			return "", err
		}
		status := "ok"
		if !result.Valid {
			status = "invalid"
		}
		return pretty(map[string]any{"kind": "report_schema", "action": "conformance", "status": status, "conformance": result}), nil
	case "conformance_fixtures":
		return pretty(map[string]any{"kind": "report_schema", "action": "conformance_fixtures", "status": "ok", "fixture_set": reportconformance.FixtureSetVersion, "cases": reportconformance.RequiredCases()}), nil
	default:
		return "", unknownToolActionError("report_schema", payload.Action, reportSchemaActionNames)
	}
}

var reportBackpressureActionNames = []string{"generate", "snapshot"}

func (ReportBackpressureTool) Definition() anthropic.ToolDefinition {
	return anthropic.ToolDefinition{
		Name:        "report_backpressure",
		Description: "Generate delta-first dogfood reports with per-channel cursors and stored full snapshots.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": append([]string(nil), reportBackpressureActionNames...)},
				"channel":    map[string]any{"type": "string"},
				"trigger_id": map[string]any{"type": "string"},
				"checked_surfaces": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"checked_window": map[string]any{"type": "string"},
				"negative_queries": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":    map[string]any{"type": "string"},
							"query": map[string]any{"type": "string"},
							"checked_surfaces": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
							"window": map[string]any{"type": "string"},
						},
						"additionalProperties": false,
					},
				},
				"freshness_ttl_seconds": map[string]any{"type": "integer", "minimum": 1},
				"snapshot_id":           map[string]any{"type": "string"},
				"now":                   map[string]any{"type": "string", "format": "date-time"},
				"consumer":              map[string]any{"type": "string"},
				"schema_versions": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"field_families": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"projection_view": map[string]any{"type": "string"},
				"projection_verbosity": map[string]any{
					"type": "string",
					"enum": []string{"brief", "normal", "verbose"},
				},
				"max_sensitivity": map[string]any{
					"type": "string",
					"enum": []string{"public", "internal", "operator_only", "secret"},
				},
			},
			"additionalProperties": false,
		},
	}
}
