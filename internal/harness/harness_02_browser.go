package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/outputstyle"
	localstatus "github.com/Rememorio/codog/internal/status"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
)

type chromeHarnessReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Enabled        bool   `json:"enabled"`
	Previous       bool   `json:"previous,omitempty"`
	Configured     bool   `json:"configured"`
	MCPServer      string `json:"mcp_server"`
	InstallURL     string `json:"install_url"`
	PermissionsURL string `json:"permissions_url"`
	ReconnectURL   string `json:"reconnect_url"`
	RecommendedURL string `json:"recommended_url,omitempty"`
	Path           string `json:"path,omitempty"`
	Message        string `json:"message,omitempty"`
}

type notificationsHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Previous   bool   `json:"previous,omitempty"`
	HookCount  int    `json:"hook_count"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

type telemetryHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Previous   bool   `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
	Message    string `json:"message,omitempty"`
}

func decodeChromeHarnessReport(output string) (chromeHarnessReport, error) {
	var report chromeHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return chromeHarnessReport{}, err
	}
	return report, nil
}

func decodeNotificationsHarnessReport(output string) (notificationsHarnessReport, error) {
	var report notificationsHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return notificationsHarnessReport{}, err
	}
	return report, nil
}

func decodeTelemetryHarnessReport(output string) (telemetryHarnessReport, error) {
	var report telemetryHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return telemetryHarnessReport{}, err
	}
	return report, nil
}

func modelRuntimePreferencesScenario() scenario {
	return scenario{
		name: "model_runtime_preferences_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{"config_home": configHome})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			initialReasoningOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "reasoning")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialReasoning, err := decodeEffortHarnessReport(initialReasoningOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialReasoning.Kind != "reasoning" || initialReasoning.Action != "status" || initialReasoning.Effort != "auto" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial reasoning report: %#v", initialReasoning)
			}
			if !slices.Contains(initialReasoning.Available, "high") || !slices.Contains(initialReasoning.Available, "disabled") {
				return localScenarioResult{}, fmt.Errorf("reasoning available list missing expected levels: %#v", initialReasoning.Available)
			}

			setReasoningOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "reasoning", "high", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setReasoning, err := decodeEffortHarnessReport(setReasoningOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setReasoning.Action != "set" || setReasoning.Effort != "high" || setReasoning.Previous != "auto" {
				return localScenarioResult{}, fmt.Errorf("unexpected set reasoning report: %#v", setReasoning)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"reasoning_effort": "high"`) && !strings.Contains(string(configData), `"reasoning_effort":"high"`) {
				return localScenarioResult{}, fmt.Errorf("reasoning config did not persist high effort: %s", string(configData))
			}

			statusReasoningOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "reasoning", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusReasoning, err := decodeEffortHarnessReport(statusReasoningOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusReasoning.Action != "status" || statusReasoning.Effort != "high" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted reasoning report: %#v", statusReasoning)
			}

			reasoningText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "reasoning", "list")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(reasoningText, "Reasoning") || !strings.Contains(reasoningText, "Available") || !strings.Contains(reasoningText, "disabled") {
				return localScenarioResult{}, fmt.Errorf("reasoning text output missing expected values: %s", reasoningText)
			}

			clearReasoningOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "reasoning", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearReasoning, err := decodeEffortHarnessReport(clearReasoningOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearReasoning.Action != "clear" || clearReasoning.Effort != "auto" || clearReasoning.Previous != "high" {
				return localScenarioResult{}, fmt.Errorf("unexpected clear reasoning report: %#v", clearReasoning)
			}

			initialFastOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "fast")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialFast, err := decodeFastHarnessReport(initialFastOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialFast.Kind != "fast" || initialFast.Action != "status" || initialFast.Enabled {
				return localScenarioResult{}, fmt.Errorf("unexpected initial fast report: %#v", initialFast)
			}

			setFastOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "fast", "on", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setFast, err := decodeFastHarnessReport(setFastOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setFast.Action != "set" || !setFast.Enabled {
				return localScenarioResult{}, fmt.Errorf("unexpected set fast report: %#v", setFast)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"fast_mode": true`) && !strings.Contains(string(configData), `"fast_mode":true`) {
				return localScenarioResult{}, fmt.Errorf("fast config did not persist enabled state: %s", string(configData))
			}

			fastText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "fast", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(fastText, "Fast Mode") || !strings.Contains(fastText, "Enabled          true") {
				return localScenarioResult{}, fmt.Errorf("fast text output missing expected values: %s", fastText)
			}

			clearFastOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "fast", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearFast, err := decodeFastHarnessReport(clearFastOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearFast.Action != "clear" || clearFast.Enabled || !clearFast.Previous {
				return localScenarioResult{}, fmt.Errorf("unexpected clear fast report: %#v", clearFast)
			}

			initialTemperatureOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "temperature")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialTemperature, err := decodeTemperatureHarnessReport(initialTemperatureOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialTemperature.Kind != "temperature" || initialTemperature.Action != "status" || initialTemperature.Configured || initialTemperature.Temperature != nil {
				return localScenarioResult{}, fmt.Errorf("unexpected initial temperature report: %#v", initialTemperature)
			}

			setTemperatureOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "temperature", "0.7", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setTemperature, err := decodeTemperatureHarnessReport(setTemperatureOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setTemperature.Action != "set" || !setTemperature.Configured || setTemperature.Temperature == nil || math.Abs(*setTemperature.Temperature-0.7) > 0.0001 {
				return localScenarioResult{}, fmt.Errorf("unexpected set temperature report: %#v", setTemperature)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"temperature": 0.7`) && !strings.Contains(string(configData), `"temperature":0.7`) {
				return localScenarioResult{}, fmt.Errorf("temperature config did not persist 0.7: %s", string(configData))
			}

			temperatureText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "temperature")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(temperatureText, "Temperature") || !strings.Contains(temperatureText, "Value            0.7") {
				return localScenarioResult{}, fmt.Errorf("temperature text output missing expected values: %s", temperatureText)
			}

			clearTemperatureOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "temperature", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearTemperature, err := decodeTemperatureHarnessReport(clearTemperatureOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearTemperature.Action != "clear" || clearTemperature.Configured || clearTemperature.Temperature != nil {
				return localScenarioResult{}, fmt.Errorf("unexpected clear temperature report: %#v", clearTemperature)
			}

			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, clearedKey := range []string{`"reasoning_effort"`, `"fast_mode"`, `"temperature"`} {
				if strings.Contains(string(configData), clearedKey) {
					return localScenarioResult{}, fmt.Errorf("config still contains %s after clear: %s", clearedKey, string(configData))
				}
			}

			report := map[string]any{
				"kind": "model_runtime_preferences",
				"reasoning": map[string]any{
					"initial":        initialReasoning.Effort,
					"set":            setReasoning.Effort,
					"status":         statusReasoning.Effort,
					"cleared":        clearReasoning.Effort,
					"clear_previous": clearReasoning.Previous,
					"path_persisted": setReasoning.Path != "" && strings.HasSuffix(setReasoning.Path, "codog-config.json"),
					"text_rendered":  strings.Contains(reasoningText, "Available"),
				},
				"fast": map[string]any{
					"initial_enabled": initialFast.Enabled,
					"set":             setFast.Enabled,
					"cleared":         clearFast.Enabled,
					"clear_previous":  clearFast.Previous,
					"path_persisted":  setFast.Path != "" && strings.HasSuffix(setFast.Path, "codog-config.json"),
					"text_rendered":   strings.Contains(fastText, "Enabled          true"),
				},
				"temperature": map[string]any{
					"initial_configured": initialTemperature.Configured,
					"set":                *setTemperature.Temperature,
					"cleared_configured": clearTemperature.Configured,
					"path_persisted":     setTemperature.Path != "" && strings.HasSuffix(setTemperature.Path, "codog-config.json"),
					"text_rendered":      strings.Contains(temperatureText, "Value            0.7"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "model runtime preferences harness ok",
				RequestCount: 13,
				MessageCount: 1,
			}, nil
		},
	}
}

type effortHarnessReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Effort    string   `json:"effort"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

type fastHarnessReport struct {
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Enabled  bool   `json:"enabled"`
	Previous bool   `json:"previous,omitempty"`
	Path     string `json:"path,omitempty"`
}

type temperatureHarnessReport struct {
	Kind        string   `json:"kind"`
	Action      string   `json:"action"`
	Status      string   `json:"status"`
	Configured  bool     `json:"configured"`
	Temperature *float64 `json:"temperature,omitempty"`
	Path        string   `json:"path,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func decodeEffortHarnessReport(output string) (effortHarnessReport, error) {
	var report effortHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return effortHarnessReport{}, err
	}
	return report, nil
}

func decodeFastHarnessReport(output string) (fastHarnessReport, error) {
	var report fastHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return fastHarnessReport{}, err
	}
	return report, nil
}

func decodeTemperatureHarnessReport(output string) (temperatureHarnessReport, error) {
	var report temperatureHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return temperatureHarnessReport{}, err
	}
	return report, nil
}

func modelSelectionScenario() scenario {
	return scenario{
		name: "model_selection_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{"config_home": configHome})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "model")
			if err != nil {
				return localScenarioResult{}, err
			}
			initial, err := decodeModelHarnessReport(initialOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initial.Kind != "model" || initial.Action != "show" || strings.TrimSpace(initial.Model) == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial model report: %#v", initial)
			}

			setOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "model", "--path", configPath, "kimi")
			if err != nil {
				return localScenarioResult{}, err
			}
			setReport, err := decodeModelHarnessReport(setOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setReport.Action != "set" || setReport.Model != "kimi" || setReport.Previous != initial.Model || !strings.HasSuffix(setReport.Path, "codog-config.json") {
				return localScenarioResult{}, fmt.Errorf("unexpected set model report: %#v", setReport)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"model": "kimi"`) && !strings.Contains(string(configData), `"model":"kimi"`) {
				return localScenarioResult{}, fmt.Errorf("model config did not persist kimi: %s", string(configData))
			}

			statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "model")
			if err != nil {
				return localScenarioResult{}, err
			}
			status, err := decodeModelHarnessReport(statusOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if status.Action != "show" || status.Model != "kimi" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted model status: %#v", status)
			}

			currentOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "models", "current")
			if err != nil {
				return localScenarioResult{}, err
			}
			current, err := decodeModelDetailHarnessReport(currentOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if current.Kind != "models" || current.Action != "show" || current.RequestedModel != "kimi" || current.ResolvedModel != "kimi-k2.5" || current.Provider != "dashscope" || current.RequiresProviderRequest {
				return localScenarioResult{}, fmt.Errorf("unexpected model current report: %#v", current)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "models", "current")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "Model") || !strings.Contains(textOut, "Requested        kimi") || !strings.Contains(textOut, "Resolved         kimi-k2.5") {
				return localScenarioResult{}, fmt.Errorf("model text output missing expected values: %s", textOut)
			}

			report := map[string]any{
				"kind": "model_selection",
				"model": map[string]any{
					"initial":        initial.Model,
					"set":            setReport.Model,
					"status":         status.Model,
					"previous":       setReport.Previous,
					"path_persisted": setReport.Path != "" && strings.HasSuffix(setReport.Path, "codog-config.json"),
					"text_rendered":  strings.Contains(textOut, "Resolved         kimi-k2.5"),
				},
				"routing": map[string]any{
					"requested":                 current.RequestedModel,
					"resolved":                  current.ResolvedModel,
					"provider":                  current.Provider,
					"wire_protocol":             current.WireProtocol,
					"requires_provider_request": current.RequiresProviderRequest,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "model selection harness ok",
				RequestCount: 5,
				MessageCount: 1,
			}, nil
		},
	}
}

type modelHarnessReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Model          string `json:"model"`
	Previous       string `json:"previous,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	Path           string `json:"path,omitempty"`
}

type modelDetailHarnessReport struct {
	Kind                    string `json:"kind"`
	Action                  string `json:"action"`
	Status                  string `json:"status"`
	RequestedModel          string `json:"requested_model"`
	ResolvedModel           string `json:"resolved_model"`
	Alias                   string `json:"alias,omitempty"`
	Provider                string `json:"provider"`
	WireProtocol            string `json:"wire_protocol"`
	WireModel               string `json:"wire_model"`
	RequiresProviderRequest bool   `json:"requires_provider_request"`
}

func decodeModelHarnessReport(output string) (modelHarnessReport, error) {
	var report modelHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return modelHarnessReport{}, err
	}
	return report, nil
}

func decodeModelDetailHarnessReport(output string) (modelDetailHarnessReport, error) {
	var report modelDetailHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return modelDetailHarnessReport{}, err
	}
	return report, nil
}

func budgetLifecycleScenario() scenario {
	return scenario{
		name: "budget_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{"config_home": configHome})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "budget")
			if err != nil {
				return localScenarioResult{}, err
			}
			initial, err := decodeBudgetHarnessReport(initialOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initial.Kind != "budget" || initial.Action != "show" || initial.MaxTokens != 4096 || initial.MaxTurns != 8 {
				return localScenarioResult{}, fmt.Errorf("unexpected initial budget report: %#v", initial)
			}

			setOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "budget", "use", "--path", configPath, "--max-tokens", "8192", "--max-turns", "12")
			if err != nil {
				return localScenarioResult{}, err
			}
			setReport, err := decodeBudgetHarnessReport(setOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setReport.Action != "set" || setReport.MaxTokens != 8192 || setReport.MaxTurns != 12 || setReport.Previous == nil || setReport.Previous.MaxTokens != 4096 || setReport.Previous.MaxTurns != 8 {
				return localScenarioResult{}, fmt.Errorf("unexpected set budget report: %#v", setReport)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"max_tokens": 8192`) || !strings.Contains(string(configData), `"max_turns": 12`) {
				return localScenarioResult{}, fmt.Errorf("budget config did not persist limits: %s", string(configData))
			}

			statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "budget", "current")
			if err != nil {
				return localScenarioResult{}, err
			}
			status, err := decodeBudgetHarnessReport(statusOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if status.Action != "show" || status.MaxTokens != 8192 || status.MaxTurns != 12 {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted budget status: %#v", status)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "budget")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "Budget") || !strings.Contains(textOut, "Max tokens       8192") || !strings.Contains(textOut, "Max turns        12") {
				return localScenarioResult{}, fmt.Errorf("budget text output missing expected values: %s", textOut)
			}

			resetOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "budget", "off", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			reset, err := decodeBudgetHarnessReport(resetOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if reset.Action != "reset" || reset.MaxTokens != 4096 || reset.MaxTurns != 8 || reset.Previous == nil || reset.Previous.MaxTokens != 8192 || reset.Previous.MaxTurns != 12 {
				return localScenarioResult{}, fmt.Errorf("unexpected reset budget report: %#v", reset)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), `"max_tokens"`) || strings.Contains(string(configData), `"max_turns"`) {
				return localScenarioResult{}, fmt.Errorf("budget config still contains limits after reset: %s", string(configData))
			}

			report := map[string]any{
				"kind": "budget_lifecycle",
				"budget": map[string]any{
					"initial_tokens": initial.MaxTokens,
					"initial_turns":  initial.MaxTurns,
					"set_tokens":     setReport.MaxTokens,
					"set_turns":      setReport.MaxTurns,
					"status_tokens":  status.MaxTokens,
					"status_turns":   status.MaxTurns,
					"reset_tokens":   reset.MaxTokens,
					"reset_turns":    reset.MaxTurns,
					"path_persisted": setReport.Path != "" && strings.HasSuffix(setReport.Path, "codog-config.json"),
					"text_rendered":  strings.Contains(textOut, "Max tokens       8192"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "budget lifecycle harness ok",
				RequestCount: 5,
				MessageCount: 1,
			}, nil
		},
	}
}

type budgetHarnessSnapshot struct {
	MaxTokens int `json:"max_tokens"`
	MaxTurns  int `json:"max_turns"`
}

type budgetHarnessReport struct {
	Kind      string                 `json:"kind"`
	Action    string                 `json:"action"`
	Status    string                 `json:"status"`
	MaxTokens int                    `json:"max_tokens"`
	MaxTurns  int                    `json:"max_turns"`
	Path      string                 `json:"path,omitempty"`
	Target    string                 `json:"target,omitempty"`
	Previous  *budgetHarnessSnapshot `json:"previous,omitempty"`
	Message   string                 `json:"message,omitempty"`
}

func decodeBudgetHarnessReport(output string) (budgetHarnessReport, error) {
	var report budgetHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return budgetHarnessReport{}, err
	}
	return report, nil
}

func authCredentialsScenario() scenario {
	return scenario{
		name: "auth_credentials_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			const apiSecret = "sk-ant-codog-harness-secret"
			const authSecret = "oauth-codog-harness-token-secret"
			authEnv := []string{
				"ANTHROPIC_API_KEY=",
				"CODOG_API_KEY=",
				"OPENAI_API_KEY=",
				"CLAUDE_CODE_OAUTH_TOKEN=",
				"ANTHROPIC_AUTH_TOKEN=",
				"CODOG_AUTH_TOKEN=",
			}
			runAuthCodog := func(args ...string) (string, error) {
				return runHarnessCodogWithEnv(ctx, workspace, authEnv, args...)
			}
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{"config_home": configHome})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			initialAPIKeyOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "api-key")
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(initialAPIKeyOut, apiSecret) || strings.Contains(initialAPIKeyOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("initial api-key output leaked a harness secret")
			}
			initialAPIKey, err := decodeAPIKeyHarnessReport(initialAPIKeyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialAPIKey.Kind != "api_key" || initialAPIKey.Action != "status" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial api-key report: %#v", initialAPIKey)
			}

			setAPIKeyOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "api-key", "set", apiSecret, "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(setAPIKeyOut, apiSecret) || strings.Contains(setAPIKeyOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("api-key set output leaked a harness secret")
			}
			setAPIKey, err := decodeAPIKeyHarnessReport(setAPIKeyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setAPIKey.Action != "set" || !setAPIKey.Configured || setAPIKey.Source != "config" || setAPIKey.RedactedValue == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected set api-key report: %#v", setAPIKey)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), apiSecret) {
				return localScenarioResult{}, fmt.Errorf("api key was not persisted in config file")
			}

			authAPIKeyOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "auth")
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(authAPIKeyOut, apiSecret) || strings.Contains(authAPIKeyOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("auth api-key output leaked a harness secret")
			}
			authAPIKey, err := decodeAuthHarnessReport(authAPIKeyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if authAPIKey.Kind != "auth" || authAPIKey.Action != "status" || !authAPIKey.Ready || authAPIKey.AuthMethod != "api_key" || !authAPIKey.APIKeyConfigured || authAPIKey.APIKeySource != "config" {
				return localScenarioResult{}, fmt.Errorf("unexpected auth api-key report: %#v", authAPIKey)
			}

			apiKeyText, err := runAuthCodog("--config", configPath, "api-key", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(apiKeyText, apiSecret) || strings.Contains(apiKeyText, authSecret) {
				return localScenarioResult{}, fmt.Errorf("api-key text output leaked a harness secret")
			}
			if !strings.Contains(apiKeyText, "API Key") || !strings.Contains(apiKeyText, "Configured       true") || !strings.Contains(apiKeyText, "Source           config") {
				return localScenarioResult{}, fmt.Errorf("api-key text output missing expected values: %s", apiKeyText)
			}

			clearAPIKeyOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "api-key", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(clearAPIKeyOut, apiSecret) || strings.Contains(clearAPIKeyOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("api-key clear output leaked a harness secret")
			}
			clearAPIKey, err := decodeAPIKeyHarnessReport(clearAPIKeyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearAPIKey.Action != "clear" {
				return localScenarioResult{}, fmt.Errorf("unexpected clear api-key report: %#v", clearAPIKey)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), apiSecret) || strings.Contains(string(configData), `"api_key"`) {
				return localScenarioResult{}, fmt.Errorf("api key was not cleared from config file: %s", string(configData))
			}

			setupTokenOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "setup-token", "--token", authSecret, "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(setupTokenOut, apiSecret) || strings.Contains(setupTokenOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("setup-token output leaked a harness secret")
			}
			setupToken, err := decodeSetupTokenHarnessReport(setupTokenOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setupToken.Kind != "setup_token" || setupToken.Action != "save" || !setupToken.Configured || setupToken.RedactedValue == "" || !slices.Contains(setupToken.EnvVars, "CODOG_AUTH_TOKEN") {
				return localScenarioResult{}, fmt.Errorf("unexpected setup-token report: %#v", setupToken)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), authSecret) {
				return localScenarioResult{}, fmt.Errorf("auth token was not persisted in config file")
			}

			authTokenOut, err := runAuthCodog("--config", configPath, "--output-format", "json", "auth")
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(authTokenOut, apiSecret) || strings.Contains(authTokenOut, authSecret) {
				return localScenarioResult{}, fmt.Errorf("auth token output leaked a harness secret")
			}
			authToken, err := decodeAuthHarnessReport(authTokenOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if authToken.AuthMethod != "auth_token" || !authToken.Ready || !authToken.AuthTokenConfigured {
				return localScenarioResult{}, fmt.Errorf("unexpected auth token report: %#v", authToken)
			}

			authText, err := runAuthCodog("--config", configPath, "auth", "--text")
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(authText, apiSecret) || strings.Contains(authText, authSecret) {
				return localScenarioResult{}, fmt.Errorf("auth text output leaked a harness secret")
			}
			if !strings.Contains(authText, "Auth") || !strings.Contains(authText, "Ready            true") || !strings.Contains(authText, "Method           auth_token") {
				return localScenarioResult{}, fmt.Errorf("auth text output missing expected values: %s", authText)
			}

			report := map[string]any{
				"kind": "auth_credentials",
				"api_key": map[string]any{
					"initial_configured": initialAPIKey.Configured,
					"set_configured":     setAPIKey.Configured,
					"source":             setAPIKey.Source,
					"redacted":           setAPIKey.RedactedValue != "",
					"cleared_action":     clearAPIKey.Action,
					"path_persisted":     setAPIKey.Path != "" && strings.HasSuffix(setAPIKey.Path, "codog-config.json"),
					"text_rendered":      strings.Contains(apiKeyText, "Configured       true"),
				},
				"auth": map[string]any{
					"api_key_method":  authAPIKey.AuthMethod,
					"token_method":    authToken.AuthMethod,
					"token_ready":     authToken.Ready,
					"text_rendered":   strings.Contains(authText, "Method           auth_token"),
					"secret_redacted": !strings.Contains(setAPIKeyOut+authAPIKeyOut+apiKeyText+clearAPIKeyOut+setupTokenOut+authTokenOut+authText, apiSecret) && !strings.Contains(setAPIKeyOut+authAPIKeyOut+apiKeyText+clearAPIKeyOut+setupTokenOut+authTokenOut+authText, authSecret),
				},
				"setup_token": map[string]any{
					"configured":     setupToken.Configured,
					"redacted":       setupToken.RedactedValue != "",
					"env_vars":       len(setupToken.EnvVars),
					"path_persisted": setupToken.Path != "" && strings.HasSuffix(setupToken.Path, "codog-config.json"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "auth credentials harness ok",
				RequestCount: 8,
				MessageCount: 1,
			}, nil
		},
	}
}

type apiKeyHarnessReport struct {
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	Configured    bool   `json:"configured"`
	RedactedValue string `json:"redacted_value,omitempty"`
	Source        string `json:"source,omitempty"`
	Path          string `json:"path,omitempty"`
	Message       string `json:"message,omitempty"`
}

type authHarnessReport struct {
	Kind                string `json:"kind"`
	Action              string `json:"action"`
	Status              string `json:"status"`
	Ready               bool   `json:"ready"`
	AuthMethod          string `json:"auth_method"`
	APIKeyConfigured    bool   `json:"api_key_configured"`
	APIKeySource        string `json:"api_key_source,omitempty"`
	AuthTokenConfigured bool   `json:"auth_token_configured"`
	OAuthProfile        string `json:"oauth_profile,omitempty"`
	Message             string `json:"message,omitempty"`
}

type setupTokenHarnessReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Configured    bool     `json:"configured"`
	RedactedValue string   `json:"redacted_value,omitempty"`
	Path          string   `json:"path,omitempty"`
	EnvVars       []string `json:"env_vars,omitempty"`
	Message       string   `json:"message,omitempty"`
}

func decodeAPIKeyHarnessReport(output string) (apiKeyHarnessReport, error) {
	var report apiKeyHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return apiKeyHarnessReport{}, err
	}
	return report, nil
}

func decodeAuthHarnessReport(output string) (authHarnessReport, error) {
	var report authHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return authHarnessReport{}, err
	}
	return report, nil
}

func decodeSetupTokenHarnessReport(output string) (setupTokenHarnessReport, error) {
	var report setupTokenHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return setupTokenHarnessReport{}, err
	}
	return report, nil
}

func outputStyleLifecycleScenario() scenario {
	return scenario{
		name: "output_style_lifecycle_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			userStyleDir := filepath.Join(configHome, "output-styles")
			workspaceStyleDir := filepath.Join(workspace, ".codog", "output-styles")
			if err := os.MkdirAll(userStyleDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.MkdirAll(workspaceStyleDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(userStyleDir, "calm.md"), []byte("Use calm user prose.\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspaceStyleDir, "calm.md"), []byte("Use calm workspace prose.\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			list, err := outputstyle.List(configHome, workspace)
			if err != nil {
				return localScenarioResult{}, err
			}
			if list.Kind != "output_style" || list.Action != "list" || list.Active != "" {
				return localScenarioResult{}, fmt.Errorf("unexpected output style list report: %#v", list)
			}
			if !styleSummaryPresent(list.Styles, "concise", "builtin") ||
				!styleSummaryPresent(list.Styles, "calm", "workspace") ||
				!styleSummaryPresent(list.Styles, "calm", "user") {
				return localScenarioResult{}, fmt.Errorf("output style list missed expected styles: %#v", list.Styles)
			}

			set, err := outputstyle.Set(configHome, workspace, "calm")
			if err != nil {
				return localScenarioResult{}, err
			}
			if set.Active != "calm" || set.Style == nil || set.Style.Source != "workspace" {
				return localScenarioResult{}, fmt.Errorf("unexpected output style set report: %#v", set)
			}
			if _, err := os.Stat(outputstyle.StatePath(workspace)); err != nil {
				return localScenarioResult{}, fmt.Errorf("output style state not persisted: %w", err)
			}
			prompt := outputstyle.RenderPrompt(configHome, workspace)
			if !strings.Contains(prompt, `<output_style name="calm" source="workspace">`) ||
				!strings.Contains(prompt, "Use calm workspace prose.") {
				return localScenarioResult{}, fmt.Errorf("unexpected output style prompt: %s", prompt)
			}

			show, err := outputstyle.Show(configHome, workspace, "calm")
			if err != nil {
				return localScenarioResult{}, err
			}
			if show.Style == nil || show.Style.Source != "workspace" || !strings.Contains(show.Style.Body, "workspace prose") {
				return localScenarioResult{}, fmt.Errorf("unexpected output style show report: %#v", show)
			}

			cleared, err := outputstyle.Clear(workspace)
			if err != nil {
				return localScenarioResult{}, err
			}
			if cleared.Action != "clear" || outputstyle.RenderPrompt(configHome, workspace) != "" {
				return localScenarioResult{}, fmt.Errorf("unexpected output style clear report: %#v", cleared)
			}
			if _, err := os.Stat(outputstyle.StatePath(workspace)); !os.IsNotExist(err) {
				return localScenarioResult{}, fmt.Errorf("output style state still exists after clear: %v", err)
			}

			report := map[string]any{
				"kind": "output_style_lifecycle",
				"list": map[string]any{
					"styles":             len(list.Styles),
					"builtin_concise":    styleSummaryPresent(list.Styles, "concise", "builtin"),
					"workspace_override": styleSummaryPresent(list.Styles, "calm", "workspace"),
					"user_style":         styleSummaryPresent(list.Styles, "calm", "user"),
				},
				"set": map[string]any{
					"active": set.Active,
					"source": set.Style.Source,
				},
				"prompt": map[string]any{
					"injected": strings.Contains(prompt, `<output_style name="calm" source="workspace">`),
				},
				"show": map[string]any{
					"source": show.Style.Source,
					"name":   show.Style.Name,
				},
				"clear": map[string]any{
					"status": cleared.Status,
					"empty":  outputstyle.RenderPrompt(configHome, workspace) == "",
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "output style lifecycle harness ok",
				RequestCount: 4,
				MessageCount: 1,
			}, nil
		},
	}
}

func diagnosticsStatusScenario() scenario {
	return scenario{
		name: "diagnostics_status_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			profilePath := filepath.Join(workspace, ".zshrc")
			if err := os.WriteFile(profilePath, []byte("# shell profile\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := runHarnessGit(workspace, "init", "-q", "-b", "main"); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"config", "user.email", "codog@example.test"},
				{"config", "user.name", "Codog Test"},
			} {
				if err := runHarnessGit(workspace, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("diagnostics parity\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "README.md"},
				{"commit", "-q", "-m", "chore: diagnostics parity"},
			} {
				if err := runHarnessGit(workspace, args...); err != nil {
					return localScenarioResult{}, err
				}
			}

			permissionRules := config.PermissionRules{
				DefaultMode: "workspace-write",
				Allow:       []string{"read_file"},
				Deny:        []string{"bash"},
			}
			toolNames := []string{"bash", "read_file", "write_file"}
			ruleStatus := localstatus.BuildPermissionRulesStatus(permissionRules, toolNames, nil)

			statusReport := localstatus.Build(localstatus.Options{
				Version:               "dev",
				FormatSource:          "default",
				Workspace:             workspace,
				ConfigHome:            configHome,
				Model:                 "claude-test",
				RuntimeProvider:       "anthropic",
				RuntimeProviderSource: "config",
				PermissionMode:        "workspace-write",
				PermissionModeRaw:     "workspace-write",
				PermissionModeSource:  "config",
				PermissionRules:       permissionRules,
				APIKey:                "test-key",
				AuthConfigured:        true,
				ToolNames:             toolNames,
				AllowedToolSource:     "default",
				AllowedToolEntries:    []string{"read_file", "write_file"},
				SetupHookCount:        1,
				MemoryFiles: []localstatus.MemoryFileStatus{{
					Path:  "AGENTS.md",
					Name:  "AGENTS.md",
					Scope: "workspace",
				}},
				GitStatus: "## main",
				GitIdentity: &gitops.Identity{
					HeadSHA:      "1234567890abcdef1234567890abcdef12345678",
					HeadShortSHA: "1234567890ab",
					HeadRef:      "main",
					GitDir:       filepath.Join(workspace, ".git"),
				},
				GitBaseCommit: &gitops.BaseCommitCheck{
					Status:   "matches",
					Matches:  true,
					Source:   &gitops.BaseCommitSource{Kind: "codog_file", Value: "1234567890abcdef1234567890abcdef12345678", Path: filepath.Join(workspace, ".codog-base")},
					Expected: "1234567890abcdef1234567890abcdef12345678",
					Actual:   "1234567890abcdef1234567890abcdef12345678",
				},
				SandboxOS:        "darwin",
				SandboxDefault:   "seatbelt",
				SandboxAvailable: true,
			})
			if statusReport.Kind != "status" || statusReport.Action != "show" {
				return localScenarioResult{}, fmt.Errorf("unexpected status report identity: %#v", statusReport)
			}
			if statusReport.Workspace.MemoryFileCount != 1 || statusReport.Config.PermissionRules.UnknownCount != 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected status workspace/config summary: %#v", statusReport)
			}
			if !statusReport.BootPreflight.Repo.WorkspaceExists || statusReport.Runtime.OS == "" {
				return localScenarioResult{}, fmt.Errorf("status report missing boot or runtime diagnostics")
			}

			doctorReport := doctor.Run(doctor.Options{
				Workspace:             workspace,
				ConfigHome:            configHome,
				Model:                 "claude-test",
				RuntimeProvider:       "anthropic",
				RuntimeProviderSource: "config",
				APIKey:                "test-key",
				PermissionMode:        "workspace-write",
				PermissionModeRaw:     "workspace-write",
				PermissionModeSource:  "config",
				PermissionRules:       ruleStatus,
				ToolCount:             len(toolNames),
				ToolPermissions: []doctor.ToolPermission{
					{Name: "read_file", RequiredPermission: "read-only"},
					{Name: "write_file", RequiredPermission: "workspace-write"},
				},
				SessionCount:   1,
				MemoryFiles:    statusReport.Workspace.MemoryFiles,
				Setup:          []string{"printf setup-ok"},
				SandboxDefault: "seatbelt",
				SandboxOK:      true,
			})
			if doctorReport.Kind != "doctor" || doctorReport.Action != "doctor" {
				return localScenarioResult{}, fmt.Errorf("unexpected doctor report identity: %#v", doctorReport)
			}
			for _, expected := range []string{"auth", "config home", "workspace", "permissions", "tools", "hooks", "sandbox"} {
				if !slices.Contains(doctorReport.CheckNames, expected) {
					return localScenarioResult{}, fmt.Errorf("doctor report missing %q check: %#v", expected, doctorReport.CheckNames)
				}
			}
			if doctorReport.Summary.Total != len(doctorReport.Checks) || doctorReport.Summary.Total == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected doctor check summary: %#v", doctorReport.Summary)
			}
			var gitCheck doctor.Check
			for _, check := range doctorReport.Checks {
				if check.Name == "Git" {
					gitCheck = check
					break
				}
			}
			gitIdentity, _ := gitCheck.Data["identity"].(gitops.Identity)
			gitFreshness, _ := gitCheck.Data["freshness"].(gitops.BranchFreshness)
			gitBaseCommit, _ := gitCheck.Data["base_commit"].(gitops.BaseCommitCheck)
			if gitIdentity.HeadSHA == "" || gitFreshness.Status == "" || gitBaseCommit.Status == "" {
				return localScenarioResult{}, fmt.Errorf("doctor git check missing structured data: %#v", gitCheck.Data)
			}

			terminalStatus, err := terminalsetup.Run(terminalsetup.Options{
				Action: "status",
				Shell:  "zsh",
				Path:   profilePath,
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if terminalStatus.Kind != "terminal_setup" || terminalStatus.Action != "status" || terminalStatus.Installed {
				return localScenarioResult{}, fmt.Errorf("unexpected terminal setup status report: %#v", terminalStatus)
			}
			terminalSnippet, err := terminalsetup.Run(terminalsetup.Options{
				Action: "snippet",
				Shell:  "zsh",
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if terminalSnippet.Action != "snippet" || !strings.Contains(terminalSnippet.Snippet, "CODOG_SHELL_INTEGRATION") {
				return localScenarioResult{}, fmt.Errorf("unexpected terminal setup snippet report: %#v", terminalSnippet)
			}
			terminalKeybindingsPath := filepath.Join(workspace, "keybindings.json")
			terminalTargetInstall, err := terminalsetup.Run(terminalsetup.Options{
				Action: "install",
				Target: "vscode",
				Path:   terminalKeybindingsPath,
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if terminalTargetInstall.Target != "vscode" || !terminalTargetInstall.Installed || !terminalTargetInstall.Changed {
				return localScenarioResult{}, fmt.Errorf("unexpected terminal target install report: %#v", terminalTargetInstall)
			}
			keybindingsData, err := os.ReadFile(terminalKeybindingsPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(keybindingsData), `"key": "shift+enter"`) || !strings.Contains(string(keybindingsData), "workbench.action.terminal.sendSequence") {
				return localScenarioResult{}, fmt.Errorf("terminal target keybinding was not installed: %s", string(keybindingsData))
			}

			report := map[string]any{
				"kind": "diagnostics_status",
				"status": map[string]any{
					"kind":                statusReport.Kind,
					"action":              statusReport.Action,
					"workspace_name":      statusReport.Workspace.Name,
					"memory_file_count":   statusReport.Workspace.MemoryFileCount,
					"permission_unknowns": statusReport.Config.PermissionRules.UnknownCount,
					"boot_workspace":      statusReport.BootPreflight.Repo.WorkspaceExists,
					"git_head_sha":        statusReport.Git.HeadSHA,
					"git_head_ref":        statusReport.Git.HeadRef,
					"git_detached":        statusReport.Git.IsDetached,
					"base_commit_status":  statusReport.Git.BaseCommit.Status,
					"base_commit_matches": statusReport.Git.BaseCommit.Matches,
					"boot_head_ref":       statusReport.BootPreflight.Repo.Identity.HeadRef,
					"boot_base_commit":    statusReport.BootPreflight.Repo.BaseCommit.Status,
				},
				"doctor": map[string]any{
					"kind":            doctorReport.Kind,
					"status":          doctorReport.Status,
					"checks":          doctorReport.Summary.Total,
					"has_auth":        slices.Contains(doctorReport.CheckNames, "auth"),
					"has_hooks":       slices.Contains(doctorReport.CheckNames, "hooks"),
					"has_sandbox":     slices.Contains(doctorReport.CheckNames, "sandbox"),
					"git_head_ref":    gitIdentity.HeadRef,
					"git_freshness":   gitFreshness.Status,
					"git_base_commit": gitBaseCommit.Status,
				},
				"terminal_setup": map[string]any{
					"status_action":  terminalStatus.Action,
					"installed":      terminalStatus.Installed,
					"snippet_action": terminalSnippet.Action,
					"snippet":        strings.Contains(terminalSnippet.Snippet, "codog_statusline"),
					"target":         terminalTargetInstall.Target,
					"target_install": terminalTargetInstall.Installed,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "diagnostics status harness ok",
				RequestCount: 3,
				MessageCount: 1,
			}, nil
		},
	}
}

func statuslineCLIScenario() scenario {
	return scenario{
		name: "statusline_cli_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configBody := map[string]any{
				"config_home":     configHome,
				"model":           "claude-statusline",
				"permission_mode": "read-only",
				"fast_mode":       true,
			}
			configData, err := json.Marshal(configBody)
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			jsonOutput, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "statusline")
			if err != nil {
				return localScenarioResult{}, err
			}
			var statusline struct {
				Kind           string `json:"kind"`
				Line           string `json:"line"`
				Status         string `json:"status"`
				Source         string `json:"source"`
				Workspace      string `json:"workspace"`
				Model          string `json:"model"`
				FastMode       bool   `json:"fast_mode"`
				PermissionMode string `json:"permission_mode"`
				SessionActive  bool   `json:"session_active"`
				SessionCount   int    `json:"session_count"`
				GitAvailable   bool   `json:"git_available"`
				GitDirty       bool   `json:"git_dirty"`
				PlanActive     bool   `json:"plan_active"`
			}
			if err := json.Unmarshal([]byte(jsonOutput), &statusline); err != nil {
				return localScenarioResult{}, err
			}
			workspaceName := filepath.Base(workspace)
			if statusline.Kind != "statusline" || statusline.Source != "codog" || statusline.Workspace != workspaceName {
				return localScenarioResult{}, fmt.Errorf("unexpected statusline identity: %#v", statusline)
			}
			if statusline.Model != "claude-statusline" || !statusline.FastMode || statusline.PermissionMode != "read-only" {
				return localScenarioResult{}, fmt.Errorf("unexpected statusline config fields: %#v", statusline)
			}
			if statusline.SessionActive || statusline.SessionCount != 0 || statusline.GitAvailable || statusline.GitDirty || statusline.PlanActive {
				return localScenarioResult{}, fmt.Errorf("unexpected statusline runtime fields: %#v", statusline)
			}
			for _, expected := range []string{"codog", workspaceName, "no-git", "claude-statusline", "fast=on", "read-only", "sessions=0", "plan=off"} {
				if !strings.Contains(statusline.Line, expected) {
					return localScenarioResult{}, fmt.Errorf("statusline line missing %q: %s", expected, statusline.Line)
				}
			}

			textOutput, err := runHarnessCodog(ctx, workspace, "--config", configPath, "statusline")
			if err != nil {
				return localScenarioResult{}, err
			}
			textOutput = strings.TrimSpace(textOutput)
			if textOutput != statusline.Line {
				return localScenarioResult{}, fmt.Errorf("statusline text mismatch: json line %q text %q", statusline.Line, textOutput)
			}

			report := map[string]any{
				"kind": "statusline_cli",
				"statusline": map[string]any{
					"status":          statusline.Status,
					"source":          statusline.Source,
					"workspace":       statusline.Workspace,
					"model":           statusline.Model,
					"fast_mode":       statusline.FastMode,
					"permission_mode": statusline.PermissionMode,
					"session_count":   statusline.SessionCount,
					"git_available":   statusline.GitAvailable,
					"text_matches":    textOutput == statusline.Line,
					"line":            statusline.Line,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "statusline cli harness ok",
				RequestCount: 2,
				MessageCount: 1,
			}, nil
		},
	}
}

func styleSummaryPresent(styles []outputstyle.StyleSummary, name string, source string) bool {
	for _, style := range styles {
		if style.Name == name && style.Source == source {
			return true
		}
	}
	return false
}

func tuiPromptCompletionScenario() scenario {
	return scenario{
		name: "tui_prompt_completion_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			multiple := tui.PreviewWithCandidates("/m", []string{"/memory list", "/model claude-test"}, 96, 24, true, false)
			for _, expected := range []string{"Codog TUI", "Enter send", "/memory list", "/model claude-test"} {
				if !strings.Contains(multiple.View, expected) {
					return localScenarioResult{}, fmt.Errorf("TUI preview missing %s", expected)
				}
			}
			if len(multiple.Matches) != 2 {
				return localScenarioResult{}, fmt.Errorf("expected 2 TUI completion matches, got %d", len(multiple.Matches))
			}
			automatic := tui.PreviewWithCandidates("/", []string{"/memory list", "/model claude-test", "/status"}, 96, 24, false, false)
			if len(automatic.Matches) != 3 || !strings.Contains(automatic.View, "suggestions") {
				return localScenarioResult{}, fmt.Errorf("expected automatic TUI slash menu, got %#v", automatic.Matches)
			}
			if !strings.Contains(automatic.View, "? for shortcuts") || !strings.Contains(automatic.View, "Ctrl+T tasks") {
				return localScenarioResult{}, fmt.Errorf("expected TUI footer hints, got %s", automatic.View)
			}
			bashMode := tui.PreviewWithCandidates("!echo @not-a-file-ref", []string{"/status"}, 96, 24, false, false)
			if bashMode.Mode != "bash" || len(bashMode.Matches) != 0 || !strings.Contains(bashMode.View, "! for bash mode") {
				return localScenarioResult{}, fmt.Errorf("expected TUI bash mode without prompt completions, got %#v", bashMode)
			}
			bashPath := tui.PreviewWithFileCandidates("!cat internal/t", []string{"internal/tui/tui.go", "internal/agent/agent.go"}, 96, 24, true)
			if bashPath.Value != "!cat internal/tui/tui.go " || strings.Contains(bashPath.Value, "@") {
				return localScenarioResult{}, fmt.Errorf("expected bash path completion without @ prefix, got value=%q view=%s", bashPath.Value, bashPath.View)
			}
			bashHistory := tui.PreviewWithBashHistory("!go te", []string{"!go test ./internal/tui"}, nil, 96, 24, false)
			if bashHistory.InlineHint != "!go test ./internal/tui" || !strings.Contains(bashHistory.View, "ghost: !go test ./internal/tui") {
				return localScenarioResult{}, fmt.Errorf("expected bash history ghost completion, got %#v", bashHistory)
			}
			bashHistoryCompleted := tui.PreviewWithBashHistory("!go te", []string{"!go test ./internal/tui"}, nil, 96, 24, true)
			if bashHistoryCompleted.Value != "!go test ./internal/tui " {
				return localScenarioResult{}, fmt.Errorf("expected bash history completion with trailing space, got value=%q", bashHistoryCompleted.Value)
			}
			bashRun := tui.PreviewWithBashMode("!printf codog", 96, 24)
			if bashRun.Prompt != "/run printf codog" || !strings.Contains(bashRun.View, "bash ok: /run printf codog") {
				return localScenarioResult{}, fmt.Errorf("expected bash mode to route through /run, got %#v", bashRun)
			}
			escapePending := tui.PreviewWithEscape("draft prompt", 1, 96, 24)
			if escapePending.Quit || escapePending.Value != "draft prompt" || !strings.Contains(escapePending.View, "Esc again to clear") {
				return localScenarioResult{}, fmt.Errorf("expected first escape to preserve composer, got %#v", escapePending)
			}
			escapeClear := tui.PreviewWithEscape("draft prompt", 2, 96, 24)
			if escapeClear.Quit || escapeClear.Value != "" || !strings.Contains(escapeClear.View, "input cleared") {
				return localScenarioResult{}, fmt.Errorf("expected double escape to clear composer without exiting, got %#v", escapeClear)
			}
			escapeIdle := tui.PreviewWithEscape("", 2, 96, 24)
			if escapeIdle.Quit {
				return localScenarioResult{}, fmt.Errorf("expected escape not to exit from empty composer, got %#v", escapeIdle)
			}
			commandArgs := tui.PreviewWithCandidates("/model ", []string{"/model claude-test"}, 96, 24, false, false)
			if !strings.Contains(commandArgs.CommandHint, "arguments: [name]") || !strings.Contains(commandArgs.View, "command args") {
				return localScenarioResult{}, fmt.Errorf("expected slash command argument hint, got hint=%q view=%s", commandArgs.CommandHint, commandArgs.View)
			}
			midInputCommand := tui.PreviewWithCandidates("please /sta", []string{"/status"}, 96, 24, false, false)
			if midInputCommand.InlineHint != "/status" || !strings.Contains(midInputCommand.View, "ghost: /status") {
				return localScenarioResult{}, fmt.Errorf("expected mid-input slash command hint, got hint=%q view=%s", midInputCommand.InlineHint, midInputCommand.View)
			}
			midInputCompleted := tui.PreviewWithCandidates("please /sta", []string{"/status"}, 96, 24, true, false)
			if midInputCompleted.Value != "please /status " {
				return localScenarioResult{}, fmt.Errorf("expected mid-input slash command completion, got value=%q", midInputCompleted.Value)
			}
			queued := tui.PreviewWithQueued("", []string{"review auth flow", "!printf codog"}, 96, 24)
			if !strings.Contains(queued.View, "queued prompts: 2") || !strings.Contains(queued.View, "2 queued") {
				return localScenarioResult{}, fmt.Errorf("expected queued prompt preview, got %s", queued.View)
			}
			if !strings.Contains(queued.View, "bash: printf codog") {
				return localScenarioResult{}, fmt.Errorf("expected queued bash preview, got %s", queued.View)
			}
			stash := tui.PreviewWithStash("draft prompt", []string{"notes.txt"}, 96, 24)
			if !stash.HasStash || stash.Value != "" || !strings.Contains(stash.View, "stashed prompt") {
				return localScenarioResult{}, fmt.Errorf("expected prompt stash preview, got %#v", stash)
			}
			transcript := tui.PreviewWithTranscript([]tui.Entry{
				{Role: "tool", Text: "stdout\nstderr"},
			}, 96, 24)
			if !transcript.Transcript || !strings.Contains(transcript.View, "001/001 tool") || !strings.Contains(transcript.View, "2 lines") {
				return localScenarioResult{}, fmt.Errorf("expected expanded transcript preview, got %#v", transcript)
			}
			todosPreview := tui.PreviewWithTodos("", []tui.TodoItem{
				{ID: "todo-1", Content: "write focused parity test", Status: "in_progress", Priority: "high"},
				{ID: "todo-2", Content: "run validation", Status: "pending", Priority: "medium"},
			}, 96, 24)
			if !todosPreview.TodosOpen || !strings.Contains(todosPreview.View, "tasks: 2 total") || !strings.Contains(todosPreview.View, "write focused parity test") {
				return localScenarioResult{}, fmt.Errorf("expected todos preview, got %#v", todosPreview)
			}
			undoPreview := tui.PreviewWithUndo("", "draft", 96, 24)
			if undoPreview.Value != "" || !strings.Contains(undoPreview.View, "undo") {
				return localScenarioResult{}, fmt.Errorf("expected undo preview, got %#v", undoPreview)
			}
			attachments := tui.PreviewWithAttachments("describe", []string{"notes.txt", "pixel.png"}, 96, 24)
			if !strings.Contains(attachments.View, "attachments: 2") || !strings.Contains(attachments.View, "notes.txt") || len(attachments.Attachments) != 2 {
				return localScenarioResult{}, fmt.Errorf("expected attachment preview, got %s", attachments.View)
			}
			attachmentRemoval := tui.PreviewWithAttachmentRemoval("describe", []string{"notes.txt", "pixel.png"}, 96, 24)
			if len(attachmentRemoval.Attachments) != 1 || attachmentRemoval.Attachments[0] != "notes.txt" || !strings.Contains(attachmentRemoval.View, "attachment removed") {
				return localScenarioResult{}, fmt.Errorf("expected attachment removal preview, got attachments=%#v view=%s", attachmentRemoval.Attachments, attachmentRemoval.View)
			}
			attachmentNavigation := tui.PreviewWithAttachmentNavigation("describe", []string{"notes.txt", "pixel.png", "report.pdf"}, []string{"right", "delete"}, 96, 24)
			if !attachmentNavigation.AttachmentsOpen || len(attachmentNavigation.Attachments) != 2 || attachmentNavigation.Attachments[1] != "report.pdf" || !strings.Contains(attachmentNavigation.View, "attachments 2/2") {
				return localScenarioResult{}, fmt.Errorf("expected attachment navigation preview, got attachments=%#v view=%s", attachmentNavigation.Attachments, attachmentNavigation.View)
			}
			paste := tui.PreviewWithPaste("prefix ", "clipboard text", 96, 24)
			if paste.Value != "prefix clipboard text" || !strings.Contains(paste.View, "pasted 1 line") {
				return localScenarioResult{}, fmt.Errorf("expected paste preview, got value=%q view=%s", paste.Value, paste.View)
			}
			pasteImage := tui.PreviewWithPasteAttachment("", "clipboard.png", 96, 24)
			if len(pasteImage.Attachments) != 1 || !strings.Contains(pasteImage.View, "clipboard image attached") {
				return localScenarioResult{}, fmt.Errorf("expected paste image preview, got attachments=%#v view=%s", pasteImage.Attachments, pasteImage.View)
			}
			fileRef := tui.PreviewWithFileCandidates("review @internal/t", []string{"internal/tui/tui.go", "internal/agent/agent.go"}, 96, 24, true)
			if fileRef.Value != "review @internal/tui/tui.go " {
				return localScenarioResult{}, fmt.Errorf("expected file reference completion, got value=%q view=%s", fileRef.Value, fileRef.View)
			}
			previewFile := filepath.Join(workspace, "src", "quick-open.go")
			if err := os.MkdirAll(filepath.Dir(previewFile), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(previewFile, []byte("package quick\n\nfunc PreviewTarget() {}\nconst NeedleValue = \"workspace-search\"\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			quickOpenPreview := tui.PreviewWithQuickOpen("inspect", []string{previewFile, filepath.Join(workspace, "src", "other.go")}, "quick", 96, 24, false)
			if !quickOpenPreview.QuickOpen || !strings.Contains(quickOpenPreview.View, "preview") || !strings.Contains(quickOpenPreview.View, "package quick") {
				return localScenarioResult{}, fmt.Errorf("expected quick open preview, got view=%s", quickOpenPreview.View)
			}
			quickOpen := tui.PreviewWithQuickOpen("inspect", []string{"internal/tui/tui.go", "internal/agent/agent.go"}, "tui", 96, 24, true)
			if quickOpen.QuickOpen || quickOpen.Value != "inspect @internal/tui/tui.go " {
				return localScenarioResult{}, fmt.Errorf("expected quick open file reference, got value=%q view=%s", quickOpen.Value, quickOpen.View)
			}
			globalSearchPreview := tui.PreviewWithGlobalSearch("inspect", []string{previewFile}, "NeedleValue", 96, 24, false)
			if !globalSearchPreview.GlobalSearch || !strings.Contains(globalSearchPreview.View, "global search: NeedleValue") || !strings.Contains(globalSearchPreview.View, "NeedleValue") || !strings.Contains(globalSearchPreview.View, "preview") {
				return localScenarioResult{}, fmt.Errorf("expected global search preview, got view=%s", globalSearchPreview.View)
			}
			globalSearch := tui.PreviewWithGlobalSearch("inspect", []string{previewFile}, "NeedleValue", 96, 24, true)
			if globalSearch.GlobalSearch || !strings.Contains(globalSearch.Value, "@"+previewFile+"#L4 ") {
				return localScenarioResult{}, fmt.Errorf("expected global search line reference, got value=%q view=%s", globalSearch.Value, globalSearch.View)
			}
			diffDialog := tui.PreviewWithDiffDialog([]tui.DiffSource{
				{
					Name:     "Uncommitted changes",
					Subtitle: "git diff HEAD",
					Files: []tui.DiffFile{
						{Path: "src/app.go", Status: "modified", Summary: "+2 -1", Diff: "@@ src/app.go\n-old\n+new"},
						{Path: "src/app_test.go", Status: "added", Summary: "+8", Diff: "+func TestApp() {}"},
					},
				},
				{
					Name:     "Turn 2",
					Subtitle: "write tests",
					Files: []tui.DiffFile{
						{Path: "src/app_test.go", Status: "modified", Summary: "+4", Diff: "+require.NoError(t, err)"},
					},
				},
			}, []string{"down", "enter"}, 96, 24)
			if !diffDialog.DiffDialog || diffDialog.Mode != "diff detail" || !strings.Contains(diffDialog.View, "ADDED src/app_test.go") {
				return localScenarioResult{}, fmt.Errorf("expected diff dialog detail preview, got mode=%q view=%s", diffDialog.Mode, diffDialog.View)
			}
			modelPicker := tui.PreviewWithModelPicker("inspect", []string{"sonnet", "opus"}, "sonnet", 96, 24, false)
			if !modelPicker.ModelPicker || !strings.Contains(modelPicker.View, "model picker") || !strings.Contains(modelPicker.View, "sonnet  current") {
				return localScenarioResult{}, fmt.Errorf("expected model picker preview, got view=%s", modelPicker.View)
			}
			fastToggle := tui.PreviewWithRuntimeToggle("", "alt+o", tui.RuntimeControlResult{
				Title:  "Fast Mode",
				Status: "fast on",
				Lines:  []string{"Fast mode: on", "Previous: off"},
			}, 96, 24)
			if !strings.Contains(fastToggle.View, "Fast Mode") || !strings.Contains(fastToggle.View, "Fast mode: on") {
				return localScenarioResult{}, fmt.Errorf("expected fast toggle preview, got view=%s", fastToggle.View)
			}
			if !strings.Contains(fastToggle.View, "fast: on") {
				return localScenarioResult{}, fmt.Errorf("expected fast runtime badge, got view=%s", fastToggle.View)
			}
			thinkingToggle := tui.PreviewWithRuntimeToggle("", "alt+t", tui.RuntimeControlResult{
				Title:  "Thinking",
				Status: "thinking medium",
				Lines:  []string{"Reasoning: medium", "Previous: low"},
			}, 96, 24)
			if !strings.Contains(thinkingToggle.View, "Thinking") || !strings.Contains(thinkingToggle.View, "Reasoning: medium") {
				return localScenarioResult{}, fmt.Errorf("expected thinking toggle preview, got view=%s", thinkingToggle.View)
			}
			if !strings.Contains(thinkingToggle.View, "thinking: medium") {
				return localScenarioResult{}, fmt.Errorf("expected thinking runtime badge, got view=%s", thinkingToggle.View)
			}
			vimNormal := tui.PreviewWithVimMode("abc", []string{"esc"}, 96, 24)
			if vimNormal.Value != "abc" || vimNormal.Mode != "vim normal" || !strings.Contains(vimNormal.View, "vim: normal") {
				return localScenarioResult{}, fmt.Errorf("expected vim normal preview, got %#v", vimNormal)
			}
			vimEdited := tui.PreviewWithVimMode("abc", []string{"esc", "0", "x", "A", "!"}, 96, 24)
			if vimEdited.Value != "bc!" || !strings.Contains(vimEdited.View, "vim: insert") {
				return localScenarioResult{}, fmt.Errorf("expected vim edit preview, got %#v", vimEdited)
			}
			vimWordEdited := tui.PreviewWithVimMode("one two three", []string{"esc", "0", "w", "x", "A", "!"}, 96, 24)
			if vimWordEdited.Value != "one wo three!" {
				return localScenarioResult{}, fmt.Errorf("expected vim word edit preview, got %#v", vimWordEdited)
			}
			vimOperatorEdited := tui.PreviewWithVimMode("abc", []string{"esc", "c", "c", "n"}, 96, 24)
			if vimOperatorEdited.Value != "n" {
				return localScenarioResult{}, fmt.Errorf("expected vim operator edit preview, got %#v", vimOperatorEdited)
			}
			customQuickOpen := tui.PreviewWithKeybindings("inspect", map[string][]string{"quick open files": {"alt+q"}}, []string{"internal/tui/tui.go"}, "alt+q", 96, 24)
			if !customQuickOpen.QuickOpen || !strings.Contains(customQuickOpen.View, "quick open") {
				return localScenarioResult{}, fmt.Errorf("expected custom keybinding quick open preview, got %#v", customQuickOpen)
			}
			customEditor := tui.PreviewWithKeybindings("draft", map[string][]string{"edit composer in $EDITOR": {"ctrl+e"}}, nil, "ctrl+e", 96, 24)
			if customEditor.Value != "edited: draft" {
				return localScenarioResult{}, fmt.Errorf("expected custom keybinding editor preview, got %#v", customEditor)
			}
			customEditorChord := tui.PreviewWithKeybindings("draft", map[string][]string{"edit composer in $EDITOR": {"ctrl+x ctrl+e"}}, nil, "ctrl+x ctrl+e", 96, 24)
			if customEditorChord.Value != "edited: draft" {
				return localScenarioResult{}, fmt.Errorf("expected custom keybinding editor chord preview, got %#v", customEditorChord)
			}
			customModal := tui.PreviewWithContextKeybindings("model-picker", map[string]map[string][]string{
				"tui-modal": {
					"move modal selection down":      {"alt+j"},
					"jump modal selection to bottom": {"alt+e"},
				},
			}, []string{"alt+j", "alt+e", "enter"}, 96, 24)
			if customModal.ModelPicker || !strings.Contains(customModal.View, "Model: gamma") {
				return localScenarioResult{}, fmt.Errorf("expected custom modal keybinding preview, got view=%s", customModal.View)
			}
			customAttachments := tui.PreviewWithContextKeybindings("attachments", map[string]map[string][]string{
				"tui-attachments": {
					"select next attachment":     {"alt+j"},
					"remove selected attachment": {"alt+x"},
				},
			}, []string{"alt+j", "alt+x"}, 96, 24)
			if !customAttachments.AttachmentsOpen || len(customAttachments.Attachments) != 2 || customAttachments.Attachments[1] != "three.txt" {
				return localScenarioResult{}, fmt.Errorf("expected custom attachment keybinding preview, got %#v", customAttachments)
			}
			customDiff := tui.PreviewWithContextKeybindings("diff", map[string]map[string][]string{
				"tui-diff": {
					"select next changed file": {"alt+j"},
					"view selected file diff":  {"alt+o"},
				},
			}, []string{"alt+j", "alt+o"}, 96, 24)
			if !customDiff.DiffDialog || customDiff.Mode != "diff detail" || !strings.Contains(customDiff.View, "ADDED main_test.go") {
				return localScenarioResult{}, fmt.Errorf("expected custom diff keybinding preview, got mode=%q view=%s", customDiff.Mode, customDiff.View)
			}
			undoControl := tui.PreviewWithRuntimeControl("", "ctrl+x ctrl+u", tui.RuntimeControlResult{
				Title:  "Undo",
				Status: "restored",
				Lines:  []string{"Path: notes.txt", "Restored: true"},
			}, 96, 24)
			if !strings.Contains(undoControl.View, "Undo") || !strings.Contains(undoControl.View, "Path: notes.txt") {
				return localScenarioResult{}, fmt.Errorf("expected undo control preview, got view=%s", undoControl.View)
			}
			exportControl := tui.PreviewWithRuntimeControl("", "ctrl+x ctrl+s", tui.RuntimeControlResult{
				Title:  "Conversation Exported",
				Status: "exported",
				Lines:  []string{"Session: session-1", "File: .codog/exports/session-1.md"},
			}, 96, 24)
			if !strings.Contains(exportControl.View, "Conversation Exported") || !strings.Contains(exportControl.View, ".codog/exports/session-1.md") {
				return localScenarioResult{}, fmt.Errorf("expected export control preview, got view=%s", exportControl.View)
			}
			copyControl := tui.PreviewWithRuntimeControl("", "ctrl+x ctrl+y", tui.RuntimeControlResult{
				Title:  "Conversation Copied",
				Status: "copied",
				Lines:  []string{"Session: session-1", "Clipboard: pbcopy"},
			}, 96, 24)
			if !strings.Contains(copyControl.View, "Conversation Copied") || !strings.Contains(copyControl.View, "Clipboard: pbcopy") {
				return localScenarioResult{}, fmt.Errorf("expected copy control preview, got view=%s", copyControl.View)
			}
			messageActions := tui.PreviewWithMessageActions([]tui.Entry{{Role: "assistant", Text: "Use message actions"}}, 96, 24, -1)
			if !messageActions.MessageMenu || !strings.Contains(messageActions.View, "message actions") || !strings.Contains(messageActions.View, "copy to composer") || !strings.Contains(messageActions.View, "copy to clipboard") {
				return localScenarioResult{}, fmt.Errorf("expected message actions preview, got view=%s", messageActions.View)
			}
			messageCopy := tui.PreviewWithMessageActions([]tui.Entry{{Role: "assistant", Text: "copy me"}}, 96, 24, 0)
			if messageCopy.MessageMenu || messageCopy.Value != "copy me" {
				return localScenarioResult{}, fmt.Errorf("expected message copied into composer, got value=%q view=%s", messageCopy.Value, messageCopy.View)
			}
			messageClipboardCopy := tui.PreviewWithMessageActions([]tui.Entry{{Role: "assistant", Text: "copy me"}}, 96, 24, 1)
			if messageClipboardCopy.MessageMenu || !strings.Contains(messageClipboardCopy.View, "Message Copied") {
				return localScenarioResult{}, fmt.Errorf("expected message copied to clipboard preview, got view=%s", messageClipboardCopy.View)
			}
			messageTargetCopy := tui.PreviewWithMessageActionTarget([]tui.Entry{{Role: "assistant", Text: "first target"}, {Role: "assistant", Text: "second target"}}, 96, 24, 0, -1)
			if messageTargetCopy.MessageMenu || messageTargetCopy.Value != "first target" {
				return localScenarioResult{}, fmt.Errorf("expected selected earlier message copied, got value=%q view=%s", messageTargetCopy.Value, messageTargetCopy.View)
			}
			messageRestore := tui.PreviewWithMessageActions([]tui.Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 4)
			if messageRestore.MessageMenu || !strings.Contains(messageRestore.View, "Conversation Restored") {
				return localScenarioResult{}, fmt.Errorf("expected message restore preview, got view=%s", messageRestore.View)
			}
			messageFork := tui.PreviewWithMessageActions([]tui.Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 5)
			if messageFork.MessageMenu || !strings.Contains(messageFork.View, "Conversation Forked") {
				return localScenarioResult{}, fmt.Errorf("expected message fork preview, got view=%s", messageFork.View)
			}
			messageSummarize := tui.PreviewWithMessageActions([]tui.Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 6)
			if messageSummarize.MessageMenu || !strings.Contains(messageSummarize.View, "Conversation Summarized") {
				return localScenarioResult{}, fmt.Errorf("expected message summarize preview, got view=%s", messageSummarize.View)
			}
			messageSummarizeUpTo := tui.PreviewWithMessageActions([]tui.Entry{{Role: "user", Text: "first"}, {Role: "assistant", Text: "answer"}}, 96, 24, 7)
			if messageSummarizeUpTo.MessageMenu || !strings.Contains(messageSummarizeUpTo.View, "Earlier Conversation Summarized") {
				return localScenarioResult{}, fmt.Errorf("expected message summarize up-to preview, got view=%s", messageSummarizeUpTo.View)
			}

			submitted := tui.PreviewWithCandidates("/mo", []string{"/model claude-test"}, 96, 24, true, true)
			if !submitted.Submitted {
				return localScenarioResult{}, fmt.Errorf("TUI prompt was not submitted")
			}
			if submitted.Prompt != "/model claude-test" {
				return localScenarioResult{}, fmt.Errorf("unexpected submitted prompt %q", submitted.Prompt)
			}
			if submitted.Value != "/model claude-test " {
				return localScenarioResult{}, fmt.Errorf("unexpected completed prompt value %q", submitted.Value)
			}

			report := map[string]any{
				"kind":                           "tui_prompt_completion",
				"matches":                        multiple.Matches,
				"automatic":                      automatic.Matches,
				"footer_hints":                   strings.Contains(automatic.View, "? for shortcuts") && strings.Contains(automatic.View, "Ctrl+T tasks"),
				"bash_mode":                      bashMode.Mode == "bash" && len(bashMode.Matches) == 0,
				"bash_path_completion":           bashPath.Value == "!cat internal/tui/tui.go ",
				"bash_mode_run":                  bashRun.Prompt == "/run printf codog",
				"escape_clear":                   !escapeClear.Quit && escapeClear.Value == "" && strings.Contains(escapeClear.View, "input cleared"),
				"escape_no_exit":                 !escapeIdle.Quit,
				"command_args":                   strings.Contains(commandArgs.CommandHint, "arguments: [name]"),
				"mid_input_command":              midInputCommand.InlineHint == "/status",
				"mid_input_command_completion":   midInputCompleted.Value == "please /status ",
				"queued_preview":                 strings.Contains(queued.View, "queued prompts: 2"),
				"queued_bash_preview":            strings.Contains(queued.View, "bash: printf codog"),
				"stash_preview":                  stash.HasStash,
				"transcript_preview":             transcript.Transcript,
				"todos_preview":                  todosPreview.TodosOpen && strings.Contains(todosPreview.View, "write focused parity test"),
				"undo_preview":                   undoPreview.Value == "" && strings.Contains(undoPreview.View, "undo"),
				"attachment_preview":             strings.Contains(attachments.View, "attachments: 2"),
				"attachment_navigation":          attachmentNavigation.AttachmentsOpen && len(attachmentNavigation.Attachments) == 2,
				"paste_preview":                  strings.Contains(paste.View, "pasted 1 line"),
				"paste_image_preview":            strings.Contains(pasteImage.View, "clipboard image attached"),
				"file_ref_completion":            strings.Contains(fileRef.Value, "@internal/tui/tui.go"),
				"quick_open_preview":             strings.Contains(quickOpenPreview.View, "package quick"),
				"quick_open":                     strings.Contains(quickOpen.Value, "@internal/tui/tui.go"),
				"global_search_preview":          strings.Contains(globalSearchPreview.View, "NeedleValue"),
				"global_search":                  strings.Contains(globalSearch.Value, "#L4"),
				"diff_dialog":                    diffDialog.DiffDialog && strings.Contains(diffDialog.View, "src/app_test.go"),
				"model_picker":                   modelPicker.ModelPicker,
				"runtime_fast_toggle":            strings.Contains(fastToggle.View, "Fast mode: on"),
				"runtime_fast_badge":             strings.Contains(fastToggle.View, "fast: on"),
				"runtime_thinking":               strings.Contains(thinkingToggle.View, "Reasoning: medium"),
				"runtime_thinking_badge":         strings.Contains(thinkingToggle.View, "thinking: medium"),
				"vim_normal_mode":                vimNormal.Mode == "vim normal",
				"vim_normal_edit":                vimEdited.Value == "bc!",
				"vim_word_edit":                  vimWordEdited.Value == "one wo three!",
				"vim_operator_edit":              vimOperatorEdited.Value == "n",
				"custom_keybinding_quick_open":   customQuickOpen.QuickOpen,
				"custom_keybinding_editor":       customEditor.Value == "edited: draft",
				"custom_keybinding_editor_chord": customEditorChord.Value == "edited: draft",
				"custom_keybinding_modal":        !customModal.ModelPicker && strings.Contains(customModal.View, "Model: gamma"),
				"custom_keybinding_attachments":  len(customAttachments.Attachments) == 2 && customAttachments.Attachments[1] == "three.txt",
				"custom_keybinding_diff":         customDiff.DiffDialog && customDiff.Mode == "diff detail",
				"message_actions":                messageActions.MessageMenu,
				"message_action_copy":            messageCopy.Value == "copy me",
				"message_action_target":          messageTargetCopy.Value == "first target",
				"message_action_restore":         strings.Contains(messageRestore.View, "Conversation Restored"),
				"message_action_fork":            strings.Contains(messageFork.View, "Conversation Forked"),
				"message_action_summary":         strings.Contains(messageSummarize.View, "Conversation Summarized"),
				"message_action_summary_up_to":   strings.Contains(messageSummarizeUpTo.View, "Earlier Conversation Summarized"),
				"attachments":                    attachments.Attachments,
				"submitted":                      submitted.Submitted,
				"submitted_prompt":               submitted.Prompt,
				"view_contains":                  []string{"Codog TUI", "Enter send"},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "tui prompt completion harness ok",
				RequestCount: 1,
				MessageCount: 1,
			}, nil
		},
	}
}

func askUserQuestionScenario() scenario {
	return scenario{
		name: "ask_user_question_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			questionIn := strings.NewReader("2\n\n")
			var questionOut bytes.Buffer
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				QuestionIn:  questionIn,
				QuestionOut: &questionOut,
			})
			choiceOut, err := registry.Execute(ctx, "AskUserQuestionTool", json.RawMessage(`{
				"question":"Pick a parity lane",
				"choices":["alpha","beta"],
				"default":"alpha"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var choice struct {
				Question string `json:"question"`
				Answer   string `json:"answer"`
			}
			if err := json.Unmarshal([]byte(choiceOut), &choice); err != nil {
				return localScenarioResult{}, err
			}
			if choice.Question != "Pick a parity lane" || choice.Answer != "beta" {
				return localScenarioResult{}, fmt.Errorf("unexpected choice answer: %s", choiceOut)
			}

			defaultOut, err := registry.Execute(ctx, "ask_user_question", json.RawMessage(`{
				"question":"Continue with default?",
				"options":["yes","no"],
				"default":"yes"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var defaultAnswer struct {
				Question string `json:"question"`
				Answer   string `json:"answer"`
			}
			if err := json.Unmarshal([]byte(defaultOut), &defaultAnswer); err != nil {
				return localScenarioResult{}, err
			}
			if defaultAnswer.Question != "Continue with default?" || defaultAnswer.Answer != "yes" {
				return localScenarioResult{}, fmt.Errorf("unexpected default answer: %s", defaultOut)
			}
			rendered := questionOut.String()
			for _, expected := range []string{"Pick a parity lane", "1. alpha", "2. beta", "Default: alpha", "Continue with default?", "1. yes"} {
				if !strings.Contains(rendered, expected) {
					return localScenarioResult{}, fmt.Errorf("question prompt missing %q: %s", expected, rendered)
				}
			}
			return localScenarioResult{
				Output:       strings.Join([]string{choiceOut, defaultOut, rendered, "ask user question harness ok"}, "\n"),
				FinalMessage: "ask user question harness ok",
				ToolUses:     []string{"ask_user_question", "ask_user_question"},
				ToolCalls:    2,
			}, nil
		},
	}
}

func runtimeOutputToolsScenario() scenario {
	return scenario{
		name: "runtime_output_tools_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			imagePath := filepath.Join(workspace, "diagram.png")
			notesPath := filepath.Join(workspace, "notes.txt")
			if err := os.WriteFile(imagePath, []byte("png"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(notesPath, []byte("notes"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistry(workspace)
			briefOut, err := registry.Execute(ctx, "BriefTool", json.RawMessage(`{
				"message":"Brief parity message",
				"status":"proactive",
				"attachments":["diagram.png"]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var brief struct {
				Message     string `json:"message"`
				Status      string `json:"status"`
				Attachments []struct {
					Path    string `json:"path"`
					Size    int64  `json:"size"`
					IsImage bool   `json:"is_image"`
				} `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(briefOut), &brief); err != nil {
				return localScenarioResult{}, err
			}
			if brief.Message != "Brief parity message" || brief.Status != "proactive" || len(brief.Attachments) != 1 || !brief.Attachments[0].IsImage || brief.Attachments[0].Size != 3 {
				return localScenarioResult{}, fmt.Errorf("unexpected brief output: %s", briefOut)
			}

			messageOut, err := registry.Execute(ctx, "send_user_message", json.RawMessage(`{
				"message":"User-facing parity message",
				"status":"normal",
				"attachments":["notes.txt"]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var message struct {
				Message     string `json:"message"`
				Status      string `json:"status"`
				Attachments []struct {
					Size    int64 `json:"size"`
					IsImage bool  `json:"is_image"`
				} `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(messageOut), &message); err != nil {
				return localScenarioResult{}, err
			}
			if message.Message != "User-facing parity message" || message.Status != "normal" || len(message.Attachments) != 1 || message.Attachments[0].IsImage || message.Attachments[0].Size != 5 {
				return localScenarioResult{}, fmt.Errorf("unexpected send_user_message output: %s", messageOut)
			}

			structuredOut, err := registry.Execute(ctx, "StructuredOutputTool", json.RawMessage(`{
				"ok":true,
				"items":["brief","structured"],
				"score":2
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var structured struct {
				Data             string         `json:"data"`
				StructuredOutput map[string]any `json:"structured_output"`
			}
			if err := json.Unmarshal([]byte(structuredOut), &structured); err != nil {
				return localScenarioResult{}, err
			}
			if structured.Data == "" || structured.StructuredOutput["ok"] != true || structured.StructuredOutput["score"] != float64(2) {
				return localScenarioResult{}, fmt.Errorf("unexpected structured output: %s", structuredOut)
			}

			sleepOut, err := registry.Execute(ctx, "SleepTool", json.RawMessage(`{"duration_ms":0}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var slept struct {
				DurationMS int    `json:"duration_ms"`
				Message    string `json:"message"`
			}
			if err := json.Unmarshal([]byte(sleepOut), &slept); err != nil {
				return localScenarioResult{}, err
			}
			if slept.DurationMS != 0 || slept.Message != "Slept for 0ms" {
				return localScenarioResult{}, fmt.Errorf("unexpected sleep output: %s", sleepOut)
			}
			return localScenarioResult{
				Output:       strings.Join([]string{briefOut, messageOut, structuredOut, sleepOut, "runtime output tools harness ok"}, "\n"),
				FinalMessage: "runtime output tools harness ok",
				ToolUses:     []string{"brief", "send_user_message", "structured_output", "sleep"},
				ToolCalls:    4,
			}, nil
		},
	}
}

func replRuntimeScenario() scenario {
	return scenario{
		name: "repl_runtime_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigEnv: map[string]string{"CODOG_REPL_PARITY": "ready"},
			})
			shellOut, err := registry.Execute(ctx, "REPLTool", json.RawMessage(`{
				"language":"sh",
				"code":"printf '%s:%s' \"$PWD\" \"$CODOG_REPL_PARITY\"",
				"timeout_ms":1000
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var shellResult struct {
				Language   string `json:"language"`
				Stdout     string `json:"stdout"`
				Stderr     string `json:"stderr"`
				ExitCode   int    `json:"exit_code"`
				TimedOut   bool   `json:"timed_out"`
				DurationMS int64  `json:"duration_ms"`
			}
			if err := json.Unmarshal([]byte(shellOut), &shellResult); err != nil {
				return localScenarioResult{}, err
			}
			if shellResult.Language != "sh" || !strings.HasSuffix(shellResult.Stdout, filepath.Base(workspace)+":ready") || shellResult.Stderr != "" || shellResult.ExitCode != 0 || shellResult.TimedOut {
				return localScenarioResult{}, fmt.Errorf("unexpected repl shell output: %s", shellOut)
			}
			if shellResult.DurationMS < 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected repl duration: %s", shellOut)
			}

			timeoutOut, err := registry.Execute(ctx, "repl", json.RawMessage(`{
				"language":"sh",
				"code":"sleep 1",
				"timeout_ms":20
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var timeoutResult struct {
				Language string `json:"language"`
				ExitCode int    `json:"exit_code"`
				TimedOut bool   `json:"timed_out"`
			}
			if err := json.Unmarshal([]byte(timeoutOut), &timeoutResult); err != nil {
				return localScenarioResult{}, err
			}
			if timeoutResult.Language != "sh" || timeoutResult.ExitCode != -1 || !timeoutResult.TimedOut {
				return localScenarioResult{}, fmt.Errorf("unexpected repl timeout output: %s", timeoutOut)
			}
			return localScenarioResult{
				Output:       strings.Join([]string{shellOut, timeoutOut, "repl runtime harness ok"}, "\n"),
				FinalMessage: "repl runtime harness ok",
				ToolUses:     []string{"repl", "repl"},
				ToolCalls:    2,
			}, nil
		},
	}
}
