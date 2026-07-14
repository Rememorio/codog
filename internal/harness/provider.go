package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/policyengine"
	"github.com/Rememorio/codog/internal/providerdiag"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/updater"
)

func providerRoutingScenario() scenario {
	type routingCase struct {
		Name           string `json:"name"`
		Model          string `json:"model"`
		Provider       string `json:"provider"`
		ProviderSource string `json:"provider_source,omitempty"`
		BaseURL        string `json:"base_url"`
		WireModel      string `json:"wire_model"`
		AuthSource     string `json:"auth_source"`
		AuthOptional   bool   `json:"auth_optional,omitempty"`
	}

	return scenario{
		name: "provider_routing_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			cleanup, err := isolateProviderEnv(map[string]string{
				"ANTHROPIC_API_KEY": "anthropic-secret",
				"OPENAI_API_KEY":    "openai-secret",
				"DASHSCOPE_API_KEY": "dashscope-secret",
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			defer cleanup()

			missingConfig := filepath.Join(workspace, "missing.json")
			cases := []routingCase{}
			for _, item := range []struct {
				name     string
				env      map[string]string
				override config.FlagOverrides
				want     routingCase
			}{
				{
					name: "openai-prefixed-model",
					override: config.FlagOverrides{
						ConfigPath: missingConfig,
						Model:      "openai/gpt-4.1-mini",
					},
					want: routingCase{
						Name:       "openai-prefixed-model",
						Model:      "openai/gpt-4.1-mini",
						Provider:   modelrouting.ProviderOpenAI,
						BaseURL:    modelrouting.DefaultOpenAIBaseURL,
						WireModel:  "gpt-4.1-mini",
						AuthSource: "api_key",
					},
				},
				{
					name: "ollama-host-bare-local-model",
					env: map[string]string{
						"OLLAMA_HOST": "http://127.0.0.1:11434",
					},
					override: config.FlagOverrides{
						ConfigPath: missingConfig,
						Model:      "qwen2.5-coder:7b",
					},
					want: routingCase{
						Name:           "ollama-host-bare-local-model",
						Model:          "qwen2.5-coder:7b",
						Provider:       modelrouting.ProviderOpenAI,
						ProviderSource: "OLLAMA_HOST",
						BaseURL:        "http://127.0.0.1:11434/v1",
						WireModel:      "qwen2.5-coder:7b",
						AuthSource:     "OLLAMA_HOST",
						AuthOptional:   true,
					},
				},
				{
					name: "openai-compatible-custom-base-url",
					env: map[string]string{
						"OPENAI_BASE_URL": "http://127.0.0.1:8080/v1",
					},
					override: config.FlagOverrides{
						ConfigPath: missingConfig,
						Model:      "llama3.2",
					},
					want: routingCase{
						Name:           "openai-compatible-custom-base-url",
						Model:          "llama3.2",
						Provider:       modelrouting.ProviderOpenAI,
						ProviderSource: "OPENAI_BASE_URL",
						BaseURL:        "http://127.0.0.1:8080/v1",
						WireModel:      "llama3.2",
						AuthSource:     "OPENAI_BASE_URL",
						AuthOptional:   true,
					},
				},
				{
					name: "dashscope-kimi-alias",
					override: config.FlagOverrides{
						ConfigPath: missingConfig,
						Model:      "kimi",
					},
					want: routingCase{
						Name:       "dashscope-kimi-alias",
						Model:      "kimi",
						Provider:   modelrouting.ProviderDashScope,
						BaseURL:    modelrouting.DefaultDashScopeBaseURL,
						WireModel:  "kimi-k2.5",
						AuthSource: "api_key",
					},
				},
			} {
				restore, err := applyProviderCaseEnv(item.env)
				if err != nil {
					return localScenarioResult{}, err
				}
				cfg, _, loadErr := config.LoadForInspection(item.override)
				restore()
				if loadErr != nil {
					return localScenarioResult{}, loadErr
				}
				provider := cfg.RuntimeProvider
				if provider == "" {
					provider = modelrouting.ProviderForModel(cfg.Model)
				}
				got := routingCase{
					Name:           item.name,
					Model:          cfg.Model,
					Provider:       provider,
					ProviderSource: cfg.RuntimeProviderSource,
					BaseURL:        cfg.BaseURL,
					WireModel:      modelrouting.WireModelForBaseURL(cfg.Model, cfg.BaseURL),
				}
				auth := providerdiag.AnalyzeAuth(providerdiag.AuthOptions{
					Model:                 cfg.Model,
					RuntimeProvider:       provider,
					RuntimeProviderSource: cfg.RuntimeProviderSource,
					BaseURL:               cfg.BaseURL,
					APIKey:                cfg.APIKey,
					AuthToken:             cfg.AuthToken,
				})
				got.AuthSource = auth.EffectiveAuthSource
				got.AuthOptional = auth.AuthOptional
				if got != item.want {
					return localScenarioResult{}, fmt.Errorf("unexpected provider routing for %s: got %#v want %#v", item.name, got, item.want)
				}
				cases = append(cases, got)
			}

			data, err := json.MarshalIndent(map[string]any{
				"kind":  "provider_routing",
				"cases": cases,
			}, "", "  ")
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "provider routing harness ok",
				RequestCount: len(cases),
				MessageCount: 1,
			}, nil
		},
	}
}

func isolateProviderEnv(values map[string]string) (func(), error) {
	names := []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_MODEL",
		"CLAUDE_MODEL",
		"CODOG_API_KEY",
		"CODOG_AUTH_TOKEN",
		"CODOG_BASE_URL",
		"CODOG_MODEL",
		"DASHSCOPE_API_KEY",
		"DASHSCOPE_BASE_URL",
		"OLLAMA_HOST",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"XAI_API_KEY",
		"XAI_BASE_URL",
	}
	previous := map[string]string{}
	existed := map[string]bool{}
	for _, name := range names {
		previous[name], existed[name] = os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			restoreEnv(previous, existed)
			return nil, err
		}
	}
	for name, value := range values {
		if err := os.Setenv(name, value); err != nil {
			restoreEnv(previous, existed)
			return nil, err
		}
	}
	return func() { restoreEnv(previous, existed) }, nil
}

func applyProviderCaseEnv(values map[string]string) (func(), error) {
	previous := map[string]string{}
	existed := map[string]bool{}
	for name, value := range values {
		previous[name], existed[name] = os.LookupEnv(name)
		if err := os.Setenv(name, value); err != nil {
			restoreEnv(previous, existed)
			return nil, err
		}
	}
	return func() { restoreEnv(previous, existed) }, nil
}

func restoreEnv(previous map[string]string, existed map[string]bool) {
	for name, value := range previous {
		if existed[name] {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	}
}

func sessionResumeJSONLRoundtripScenario() scenario {
	return scenario{
		name:           "session_resume_jsonl_roundtrip",
		turns:          []mockanthropic.Turn{{Text: "resume harness ok"}},
		prompt:         resumeJSONLPrompt,
		setup:          setupResumeJSONLSession,
		loadPrevious:   loadResumeJSONLMessages,
		verify:         verifyResumeJSONLResult,
		verifyRequests: verifyResumeJSONLRequests,
	}
}

const (
	resumeJSONLSessionID = "resume-jsonl"
	resumeJSONLPrompt    = "continue from stored session"
)

func resumeJSONLStore(workspace string) *session.Store {
	return session.NewWorkspaceStore(filepath.Join(workspace, "config-home"), workspace)
}

func setupResumeJSONLSession(workspace string) error {
	store := resumeJSONLStore(workspace)
	if _, err := store.CreateWithIdentity(resumeJSONLSessionID, session.SessionIdentity{Title: "Stored resume context", Purpose: "prompt"}); err != nil {
		return err
	}
	if err := store.AppendInput(resumeJSONLSessionID, "stored prompt"); err != nil {
		return err
	}
	if err := store.Append(resumeJSONLSessionID, anthropic.TextMessage("user", "stored prompt")); err != nil {
		return err
	}
	return store.Append(resumeJSONLSessionID, anthropic.TextMessage("assistant", "stored answer"))
}

func loadResumeJSONLMessages(workspace string) ([]anthropic.Message, error) {
	sess, err := resumeJSONLStore(workspace).OpenExisting(resumeJSONLSessionID)
	if err != nil {
		return nil, err
	}
	if len(sess.Messages) != 2 {
		return nil, fmt.Errorf("expected 2 stored messages before resume, got %d", len(sess.Messages))
	}
	return sess.Messages, nil
}

func verifyResumeJSONLResult(workspace string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "resume harness ok") {
		return fmt.Errorf("missing resume final response")
	}
	if len(result.Messages) != 4 {
		return fmt.Errorf("expected 4 messages after resumed turn, got %d", len(result.Messages))
	}
	store := resumeJSONLStore(workspace)
	if err := store.AppendInput(resumeJSONLSessionID, resumeJSONLPrompt); err != nil {
		return err
	}
	for _, msg := range result.Messages[2:] {
		if err := store.Append(resumeJSONLSessionID, msg); err != nil {
			return err
		}
	}
	reopened, err := store.OpenExisting(resumeJSONLSessionID)
	if err != nil {
		return err
	}
	return verifyReopenedResumeJSONL(reopened)
}

func verifyReopenedResumeJSONL(reopened *session.Session) error {
	if len(reopened.Messages) != 4 {
		return fmt.Errorf("expected 4 persisted messages after resume, got %d", len(reopened.Messages))
	}
	if strings.TrimSpace(reopened.Messages[0].Content[0].Text) != "stored prompt" ||
		strings.TrimSpace(reopened.Messages[1].Content[0].Text) != "stored answer" ||
		strings.TrimSpace(reopened.Messages[2].Content[0].Text) != resumeJSONLPrompt ||
		strings.TrimSpace(reopened.Messages[3].Content[0].Text) != "resume harness ok" {
		return fmt.Errorf("unexpected persisted resume messages: %#v", reopened.Messages)
	}
	if strings.TrimSpace(reopened.Identity.Workspace) == "" || strings.TrimSpace(reopened.Identity.Worktree) == "" || len(reopened.Identity.Placeholders) != 0 {
		return fmt.Errorf("unexpected reopened identity after resume: %#v", reopened.Identity)
	}
	return nil
}

func verifyResumeJSONLRequests(requests []anthropic.Request) error {
	if len(requests) != 1 {
		return fmt.Errorf("expected 1 resume request, got %d", len(requests))
	}
	if len(requests[0].Messages) != 3 {
		return fmt.Errorf("expected resumed request with 3 messages, got %d", len(requests[0].Messages))
	}
	messages := requests[0].Messages
	if messages[0].Content[0].Text != "stored prompt" || messages[1].Content[0].Text != "stored answer" || messages[2].Content[0].Text != resumeJSONLPrompt {
		return fmt.Errorf("unexpected resumed request messages: %#v", messages)
	}
	return nil
}

func resumeSlashCommandScenario() scenario {
	return scenario{
		name:     "resume_slash_command_roundtrip",
		runLocal: resumeSlashCommandScenarioRunLocal,
	}
}

func sessionExportPathSafetyScenario() scenario {
	return scenario{
		name:     "session_export_path_safety_roundtrip",
		runLocal: sessionExportPathSafetyScenarioRunLocal,
	}
}

func requireResumeSlashCLIReport(output string, wantSessionID string) error {
	var report struct {
		Kind             string   `json:"kind"`
		ErrorKind        string   `json:"error_kind"`
		Status           string   `json:"status"`
		SessionID        string   `json:"session_id"`
		RequestedSession string   `json:"requested_session"`
		ContinueCommands []string `json:"continue_commands"`
	}
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return err
	}
	if report.ErrorKind != "" {
		return fmt.Errorf("unexpected error_kind %q in %s", report.ErrorKind, output)
	}
	if report.Kind != "resume" || report.Status != "ok" {
		return fmt.Errorf("unexpected resume report: %s", output)
	}
	if report.SessionID != wantSessionID || report.RequestedSession != wantSessionID {
		return fmt.Errorf("unexpected session id/requested session: %#v", report)
	}
	if len(report.ContinueCommands) == 0 {
		return fmt.Errorf("missing continue commands in %s", output)
	}
	return nil
}

func runHarnessCodog(ctx context.Context, workspace string, args ...string) (string, error) {
	return runHarnessCodogWithEnv(ctx, workspace, nil, args...)
}

func runHarnessCodogWithEnv(ctx context.Context, workspace string, extraEnv []string, args ...string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	commandArgs := append([]string{"run", ".", "--cwd", workspace}, args...)
	cmd := exec.CommandContext(ctx, "go", commandArgs...)
	cmd.Dir = root
	if len(extraEnv) != 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go %s failed: %w: %s", strings.Join(commandArgs, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}

func promptDirectoryAttachmentScenario() scenario {
	return scenario{
		name:     "prompt_directory_attachment_roundtrip",
		runLocal: promptDirectoryAttachmentScenarioRunLocal,
	}
}

func bashOutputTruncationScenario() scenario {
	return scenario{
		name:       "bash_output_truncation_roundtrip",
		permission: tools.PermissionAllow,
		configHome: true,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-1",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"yes x | head -c 20000","timeout_ms":1000}`),
			}}},
			{Text: "bash truncation harness ok"},
		},
		prompt: "run large bash output",
		verify: bashOutputTruncationScenarioVerify,
	}
}

func bashBackgroundOutputScenario() scenario {
	return scenario{
		name:     "bash_background_output_roundtrip",
		runLocal: bashBackgroundOutputScenarioRunLocal,
	}
}

func bashKillScenario() scenario {
	return scenario{
		name:     "bash_kill_roundtrip",
		runLocal: bashKillScenarioRunLocal,
	}
}

func powerShellStdoutScenario() scenario {
	return scenario{
		name:            "powershell_stdout_roundtrip",
		permission:      tools.PermissionAllow,
		registryOptions: powerShellStdoutScenarioRegistryOptions,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-1",
				Name:  "powershell",
				Input: json.RawMessage(`{"command":"Write-Output harness-powershell","timeout":1000}`),
			}}},
			{Text: "powershell harness ok"},
		},
		prompt: "run powershell",
		verify: powerShellStdoutScenarioVerify,
	}
}

func permissionScopeDenialScenario() scenario {
	return scenario{
		name:     "permission_scope_denial_roundtrip",
		runLocal: permissionScopeDenialScenarioRunLocal,
	}
}

func harnessShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func sandboxBypassStatusScenario() scenario {
	return scenario{
		name:     "sandbox_bypass_status_roundtrip",
		runLocal: sandboxBypassStatusScenarioRunLocal,
	}
}

func policyUpdateSandboxScenario() scenario {
	return scenario{
		name:     "policy_update_sandbox_roundtrip",
		runLocal: policyUpdateSandboxScenarioRunLocal,
	}
}

func policyApprovalScenario() scenario {
	return scenario{
		name:     "policy_approval_roundtrip",
		runLocal: policyApprovalScenarioRunLocal,
	}
}

func notebookReadEditScenario() scenario {
	return scenario{
		name:     "notebook_read_edit_roundtrip",
		runLocal: notebookReadEditScenarioRunLocal,
	}
}

func webAccessScenario() scenario {
	return scenario{
		name:     "web_access_roundtrip",
		runLocal: webAccessScenarioRunLocal,
	}
}

func webAccessLimitsScenario() scenario {
	return scenario{
		name:     "web_access_limits_roundtrip",
		runLocal: webAccessLimitsScenarioRunLocal,
	}
}

func setenvForScenario(key string, value string) func() {
	previous, hadPrevious := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if hadPrevious {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	}
}

func gitWorkspaceScenario() scenario {
	return scenario{
		name:     "git_workspace_roundtrip",
		runLocal: gitWorkspaceScenarioRunLocal,
	}
}

func resumeSlashCommandScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	configPath := filepath.Join(workspace, "config.json")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return localScenarioResult{}, err
	}

	store := session.NewWorkspaceStore(configHome, workspace)
	for id, text := range map[string]string{
		"active": "active session prompt",
		"other":  "other session prompt",
	} {
		if err := store.Append(id, anthropic.TextMessage("user", text)); err != nil {
			return localScenarioResult{}, err
		}
	}

	directOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "/resume", "other")
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := requireResumeSlashCLIReport(directOut, "other"); err != nil {
		return localScenarioResult{}, fmt.Errorf("direct /resume: %w", err)
	}

	resumedOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--resume", "active", "--output-format", "json", "/resume", "other")
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := requireResumeSlashCLIReport(resumedOut, "other"); err != nil {
		return localScenarioResult{}, fmt.Errorf("resumed /resume: %w", err)
	}

	return localScenarioResult{
		Output:       strings.Join([]string{directOut, resumedOut}, "\n"),
		FinalMessage: "resume slash command harness ok",
		RequestCount: 2,
		MessageCount: 2,
	}, nil
}

func sessionExportPathSafetyScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	parent := filepath.Dir(workspace)
	configHome := filepath.Join(workspace, "config-home")
	configPath := filepath.Join(workspace, "config.json")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	configData, err := json.Marshal(map[string]string{"config_home": configHome})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return localScenarioResult{}, err
	}
	store := session.NewWorkspaceStore(configHome, workspace)
	if err := store.Append("export-safe", anthropic.TextMessage("user", "export safety prompt")); err != nil {
		return localScenarioResult{}, err
	}
	exportOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "export", "--session", "export-safe", "--output", "notes.md")
	if err != nil {
		return localScenarioResult{}, err
	}
	exportPath := filepath.Join(workspace, "notes.md")
	data, err := os.ReadFile(exportPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !strings.Contains(string(data), "export safety prompt") {
		return localScenarioResult{}, fmt.Errorf("exported markdown missing session content")
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "notes.md.txt")); !os.IsNotExist(statErr) {
		return localScenarioResult{}, fmt.Errorf("export unexpectedly wrote notes.md.txt")
	}
	var exportReport struct {
		File   string `json:"file"`
		Format string `json:"format"`
	}
	if err := json.Unmarshal([]byte(exportOut), &exportReport); err != nil {
		return localScenarioResult{}, err
	}
	if filepath.Base(exportReport.File) != "notes.md" || exportReport.Format != "markdown" {
		return localScenarioResult{}, fmt.Errorf("unexpected export report: %s", exportOut)
	}
	escapedPath := filepath.Join(parent, "escaped.md")
	_, traversalErr := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "export", "--session", "export-safe", "--output", "../escaped.md")
	if traversalErr == nil {
		return localScenarioResult{}, fmt.Errorf("expected export traversal to fail")
	}
	if _, statErr := os.Stat(escapedPath); !os.IsNotExist(statErr) {
		return localScenarioResult{}, fmt.Errorf("export traversal wrote %s", escapedPath)
	}
	return localScenarioResult{
		Output:       exportOut,
		FinalMessage: "session export path safety harness ok",
	}, nil
}

func promptDirectoryAttachmentScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	captured := make(chan json.RawMessage, 1)
	server := httptest.NewServer(mockanthropic.Server{
		Text: "directory attachment harness ok",
		OnRequest: func(raw json.RawMessage) {
			select {
			case captured <- append(json.RawMessage(nil), raw...):
			default:
			}
		},
	}.Handler())
	defer server.Close()

	configHome := filepath.Join(workspace, "config-home")
	docsDir := filepath.Join(workspace, "docs", "nested")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Harness Docs\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(docsDir, "guide.txt"), []byte("nested guide\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		return localScenarioResult{}, err
	}
	configPath := filepath.Join(workspace, "codog-config.json")
	configData, err := json.Marshal(map[string]any{
		"config_home":     configHome,
		"base_url":        server.URL,
		"api_key":         "test-key",
		"model":           "mock",
		"max_turns":       1,
		"permission_mode": "read-only",
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		return localScenarioResult{}, err
	}

	var promptReport struct {
		Response string `json:"response"`
	}
	_, err = decodeHarnessOutput(&promptReport, func() (string, error) {
		return runHarnessCodog(ctx, workspace, "--config", configPath, "prompt", "Describe directory attachment", "--attach", "docs", "--output-format", "json")
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if promptReport.Response != "directory attachment harness ok" {
		return localScenarioResult{}, fmt.Errorf("unexpected directory attachment response: %q", promptReport.Response)
	}

	var raw json.RawMessage
	select {
	case raw = <-captured:
	default:
		return localScenarioResult{}, fmt.Errorf("expected provider request for directory attachment")
	}
	var body struct {
		Messages []struct {
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Title string `json:"title"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return localScenarioResult{}, err
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected directory attachment content blocks: %s", string(raw))
	}
	attachment := body.Messages[0].Content[1]
	for _, expected := range []string{
		`<attachment_directory path="docs" files=2`,
		`<file path="README.md"`,
		"# Harness Docs",
		`<file path="nested/guide.txt"`,
		"nested guide",
		"<skipped>",
		"binary.bin",
	} {
		if !strings.Contains(attachment.Text, expected) {
			return localScenarioResult{}, fmt.Errorf("directory attachment missing %s: %s", expected, attachment.Text)
		}
	}
	report := map[string]any{
		"kind": "prompt_directory_attachment",
		"attachment": map[string]any{
			"title":          attachment.Title,
			"type":           attachment.Type,
			"files":          2,
			"skipped_binary": strings.Contains(attachment.Text, "binary.bin"),
			"nested":         strings.Contains(attachment.Text, `nested/guide.txt`),
			"text_rendered":  strings.Contains(attachment.Text, `<attachment_directory path="docs"`),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "directory attachment harness ok",
		RequestCount: 1,
		MessageCount: 1,
	}, nil
}

func bashOutputTruncationScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "bash truncation harness ok") {
		return fmt.Errorf("missing bash truncation final response")
	}
	if err := expectToolCalls(result, 1, false); err != nil {
		return err
	}
	var payload struct {
		Stdout              string `json:"stdout"`
		PersistedOutputPath string `json:"persistedOutputPath"`
		PersistedOutputSize int64  `json:"persistedOutputSize"`
	}
	if err := json.Unmarshal([]byte(result.ToolCalls[0].Output), &payload); err != nil {
		return err
	}
	if len(payload.Stdout) >= 20000 {
		return fmt.Errorf("stdout was not truncated")
	}
	if !strings.Contains(payload.Stdout, "[output truncated - exceeded 16384 bytes]") {
		return fmt.Errorf("missing truncation marker in stdout")
	}
	if payload.PersistedOutputPath == "" || payload.PersistedOutputSize <= 20000 {
		return fmt.Errorf("missing persisted full output path/size: path=%q size=%d", payload.PersistedOutputPath, payload.PersistedOutputSize)
	}
	var persisted struct {
		Kind            string   `json:"kind"`
		Stdout          string   `json:"stdout"`
		TruncatedFields []string `json:"truncated_fields"`
	}
	data, err := os.ReadFile(payload.PersistedOutputPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	if persisted.Kind != "bash_output" || len(persisted.Stdout) != 20000 || strings.Join(persisted.TruncatedFields, ",") != "stdout" {
		return fmt.Errorf("unexpected persisted bash output metadata: kind=%q stdout=%d fields=%v", persisted.Kind, len(persisted.Stdout), persisted.TruncatedFields)
	}
	return nil
}

func bashBackgroundOutputScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	var started struct {
		Background                   bool   `json:"background"`
		BackgroundTaskID             string `json:"backgroundTaskId"`
		BackgroundedByUser           bool   `json:"backgroundedByUser"`
		AssistantAutoBackgrounded    bool   `json:"assistantAutoBackgrounded"`
		NoOutputExpected             bool   `json:"noOutputExpected"`
		RawOutputPath                any    `json:"rawOutputPath"`
		ReturnCodeInterpretation     any    `json:"returnCodeInterpretation"`
		SandboxPermissionsDowngraded bool   `json:"sandboxPermissionsDowngraded"`
	}
	startOut, err := decodeHarnessOutput(&started, func() (string, error) {
		return registry.Execute(ctx, "Bash", json.RawMessage(`{
				"command":"printf background-harness",
				"run_in_background":true
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if !started.Background || started.BackgroundTaskID == "" || started.BackgroundedByUser || started.AssistantAutoBackgrounded {
		return localScenarioResult{}, fmt.Errorf("unexpected background bash start payload: %s", startOut)
	}
	if !started.NoOutputExpected || started.RawOutputPath != nil || started.ReturnCodeInterpretation != nil || started.SandboxPermissionsDowngraded {
		return localScenarioResult{}, fmt.Errorf("unexpected background bash contract fields: %s", startOut)
	}

	var output struct {
		Output           string `json:"output"`
		Stdout           string `json:"stdout"`
		BackgroundTaskID string `json:"backgroundTaskId"`
		Offset           int64  `json:"offset"`
		NextOffset       int64  `json:"nextOffset"`
		BytesRead        int    `json:"bytesRead"`
		TimedOut         bool   `json:"timedOut"`
		TimeoutMS        int    `json:"timeoutMs"`
		RawOutputPath    string `json:"rawOutputPath"`
		NoOutputExpected bool   `json:"noOutputExpected"`
	}
	outputOut, err := decodeHarnessOutput(&output, func() (string, error) {
		return registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64,
				"block":true,
				"timeout_ms":2000
			}`, started.BackgroundTaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if output.BackgroundTaskID != started.BackgroundTaskID || output.Stdout != "background-harness" || output.Output != output.Stdout {
		return localScenarioResult{}, fmt.Errorf("unexpected bash output payload: %s", outputOut)
	}
	if output.Offset != 0 || output.NextOffset <= 0 || output.BytesRead != len("background-harness") {
		return localScenarioResult{}, fmt.Errorf("unexpected bash output offsets: %s", outputOut)
	}
	if output.TimedOut || output.TimeoutMS != 2000 || output.NoOutputExpected {
		return localScenarioResult{}, fmt.Errorf("unexpected bash output wait flags: %s", outputOut)
	}
	if output.RawOutputPath == "" {
		return localScenarioResult{}, fmt.Errorf("missing bash output raw path: %s", outputOut)
	}
	if _, err := os.Stat(output.RawOutputPath); err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       strings.Join([]string{startOut, outputOut, "bash background output harness ok"}, "\n"),
		FinalMessage: "bash background output harness ok",
		ToolUses:     []string{"bash", "bash_output"},
		ToolCalls:    2,
	}, nil
}

func bashKillScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	var started struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		Background       bool   `json:"background"`
	}
	startOut, err := decodeHarnessOutput(&started, func() (string, error) {
		return registry.Execute(ctx, "Bash", json.RawMessage(`{
				"command":"printf kill-ready; sleep 10",
				"run_in_background":true
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if !started.Background || started.BackgroundTaskID == "" {
		return localScenarioResult{}, fmt.Errorf("unexpected kill bash start payload: %s", startOut)
	}
	killedTask := false
	defer func() {
		if !killedTask {
			_, _ = registry.Execute(ctx, "KillBash", json.RawMessage(fmt.Sprintf(`{"bash_id":%q}`, started.BackgroundTaskID)), nil)
		}
	}()

	var beforeKill struct {
		Stdout      string `json:"stdout"`
		Interrupted bool   `json:"interrupted"`
		TimedOut    bool   `json:"timedOut"`
	}
	beforeKillOut, err := decodeHarnessOutput(&beforeKill, func() (string, error) {
		return registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64,
				"block":true,
				"timeout_ms":2000
			}`, started.BackgroundTaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if beforeKill.Stdout != "kill-ready" || beforeKill.Interrupted || beforeKill.TimedOut {
		return localScenarioResult{}, fmt.Errorf("unexpected pre-kill bash output: %s", beforeKillOut)
	}

	var killed struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		Status           string `json:"status"`
		Interrupted      bool   `json:"interrupted"`
		NoOutputExpected bool   `json:"noOutputExpected"`
	}
	killOut, err := decodeHarnessOutput(&killed, func() (string, error) {
		return registry.Execute(ctx, "KillBash", json.RawMessage(fmt.Sprintf(`{"bash_id":%q}`, started.BackgroundTaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if killed.BackgroundTaskID != started.BackgroundTaskID || killed.Status != "stopped" || !killed.Interrupted || !killed.NoOutputExpected {
		return localScenarioResult{}, fmt.Errorf("unexpected kill bash payload: %s", killOut)
	}
	killedTask = true

	var afterKill struct {
		Stdout           string `json:"stdout"`
		Status           string `json:"status"`
		Interrupted      bool   `json:"interrupted"`
		BackgroundTaskID string `json:"backgroundTaskId"`
		RawOutputPath    string `json:"rawOutputPath"`
	}
	afterKillOut, err := decodeHarnessOutput(&afterKill, func() (string, error) {
		return registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64
			}`, started.BackgroundTaskID)), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if afterKill.BackgroundTaskID != started.BackgroundTaskID || afterKill.Status != "stopped" || !afterKill.Interrupted || afterKill.Stdout != "kill-ready" {
		return localScenarioResult{}, fmt.Errorf("unexpected post-kill bash output: %s", afterKillOut)
	}
	if afterKill.RawOutputPath == "" {
		return localScenarioResult{}, fmt.Errorf("missing post-kill raw output path: %s", afterKillOut)
	}
	if _, err := os.Stat(afterKill.RawOutputPath); err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       strings.Join([]string{startOut, beforeKillOut, killOut, afterKillOut, "bash kill harness ok"}, "\n"),
		FinalMessage: "bash kill harness ok",
		ToolUses:     []string{"bash", "bash_output", "kill_bash", "bash_output"},
		ToolCalls:    4,
	}, nil
}

func powerShellStdoutScenarioRegistryOptions(_ string, configHome string) tools.RegistryOptions {
	return tools.RegistryOptions{
		ConfigHome: configHome,
		PowerShell: "echo",
	}
}

func powerShellStdoutScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "powershell harness ok") {
		return fmt.Errorf("missing powershell final response")
	}
	if err := expectToolCalls(result, 1, false); err != nil {
		return err
	}
	var payload struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(result.ToolCalls[0].Output), &payload); err != nil {
		return err
	}
	if !strings.Contains(payload.Stdout, "harness-powershell") {
		return fmt.Errorf("missing powershell stdout in tool output: %q", payload.Stdout)
	}
	return nil
}

func permissionScopeDenialScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	outside := filepath.Join(filepath.Dir(workspace), filepath.Base(workspace)+"-outside")
	defer func() { _ = os.RemoveAll(outside) }()
	if err := os.MkdirAll(outside, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret\n"), 0o600); err != nil {
		return localScenarioResult{}, err
	}

	registry := tools.NewRegistry(workspace)
	prompter := &tools.Prompter{Mode: tools.PermissionReadOnly, Workspace: workspace}
	command := "cat " + harnessShellQuote(secretPath)
	var permissionCheck struct {
		Kind               string `json:"kind"`
		RequestedTool      string `json:"requested_tool"`
		TargetTool         string `json:"target_tool"`
		CanonicalTool      string `json:"canonical_tool"`
		KnownTool          bool   `json:"known_tool"`
		RequiredPermission string `json:"required_permission"`
		Allowed            bool   `json:"allowed"`
		WouldPrompt        bool   `json:"would_prompt"`
		Reason             string `json:"reason"`
		Message            string `json:"message"`
		Decision           struct {
			ToolName string `json:"tool_name"`
			Allowed  bool   `json:"allowed"`
			Reason   string `json:"reason"`
			Message  string `json:"message"`
		} `json:"decision"`
	}
	permissionOut, err := decodeHarnessOutput(&permissionCheck, func() (string, error) {
		return registry.Execute(ctx, "permission_check", json.RawMessage(`{
				"target_tool": "BashTool",
				"input": {"command": `+strconv.Quote(command)+`}
			}`), prompter)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if permissionCheck.Kind != "permission_check" ||
		permissionCheck.RequestedTool != "BashTool" ||
		permissionCheck.TargetTool != "bash" ||
		permissionCheck.CanonicalTool != "bash" ||
		!permissionCheck.KnownTool ||
		permissionCheck.RequiredPermission != string(tools.PermissionDanger) ||
		permissionCheck.Allowed ||
		permissionCheck.WouldPrompt ||
		permissionCheck.Reason != "bash_validation" ||
		!strings.Contains(permissionCheck.Message, "path resolves outside workspace scope") ||
		permissionCheck.Decision.ToolName != "bash" ||
		permissionCheck.Decision.Allowed ||
		permissionCheck.Decision.Reason != "bash_validation" ||
		!strings.Contains(permissionCheck.Decision.Message, "path resolves outside workspace scope") {
		return localScenarioResult{}, fmt.Errorf("unexpected permission check output: %s", permissionOut)
	}

	decisions := []tools.PermissionDecision{}
	prompter.OnDecision = func(decision tools.PermissionDecision) {
		decisions = append(decisions, decision)
	}
	bashOut, bashErr := registry.Execute(ctx, "bash", json.RawMessage(`{"command": `+strconv.Quote(command)+`}`), prompter)
	if bashErr == nil {
		return localScenarioResult{}, fmt.Errorf("expected scoped bash denial, got output: %s", bashOut)
	}
	if !harnessContainsAll(bashErr.Error(), "permission denied for tool bash by tool validation", "path resolves outside workspace scope") {
		return localScenarioResult{}, fmt.Errorf("unexpected bash denial error: %w", bashErr)
	}
	if len(decisions) != 1 ||
		decisions[0].ToolName != "bash" ||
		decisions[0].Allowed ||
		decisions[0].Reason != "bash_validation" ||
		!strings.Contains(decisions[0].Message, "path resolves outside workspace scope") {
		return localScenarioResult{}, fmt.Errorf("unexpected bash permission decisions: %#v", decisions)
	}

	readOut, readErr := registry.Execute(ctx, "read_file", json.RawMessage(`{"path": `+strconv.Quote(secretPath)+`}`), nil)
	if readErr == nil {
		return localScenarioResult{}, fmt.Errorf("expected scoped read_file denial, got output: %s", readOut)
	}
	if !strings.Contains(readErr.Error(), "path escapes workspace scope") {
		return localScenarioResult{}, fmt.Errorf("unexpected read_file scope error: %w", readErr)
	}

	report := map[string]any{
		"kind": "permission_scope_denial",
		"bash": map[string]any{
			"allowed":          permissionCheck.Allowed,
			"reason":           permissionCheck.Reason,
			"message":          permissionCheck.Message,
			"decision_count":   len(decisions),
			"runtime_denial":   bashErr.Error(),
			"canonical_tool":   permissionCheck.CanonicalTool,
			"requested_tool":   permissionCheck.RequestedTool,
			"would_prompt":     permissionCheck.WouldPrompt,
			"decision_allowed": decisions[0].Allowed,
			"decision_reason":  decisions[0].Reason,
		},
		"file": map[string]any{
			"denied": true,
			"error":  readErr.Error(),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:         string(data),
		FinalMessage:   "permission scope denial harness ok",
		RequestCount:   3,
		MessageCount:   1,
		ToolCalls:      3,
		ToolUses:       []string{"permission_check", "bash", "read_file"},
		ToolErrorCount: 2,
	}, nil
}

func sandboxBypassStatusScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	var payload struct {
		Stdout                    string `json:"stdout"`
		DangerouslyDisableSandbox bool   `json:"dangerouslyDisableSandbox"`
		Sandbox                   string `json:"sandbox,omitempty"`
		SandboxStatus             struct {
			Enabled             bool     `json:"enabled"`
			Active              bool     `json:"active"`
			Supported           bool     `json:"supported"`
			ConfiguredStrategy  string   `json:"configured_strategy"`
			ResolutionStatus    string   `json:"resolution_status"`
			ResolutionAvailable bool     `json:"resolution_available"`
			FilesystemMode      string   `json:"filesystem_mode"`
			FilesystemActive    bool     `json:"filesystem_active"`
			AllowedMounts       []string `json:"allowed_mounts"`
			Requested           struct {
				Enabled               bool     `json:"enabled"`
				NamespaceRestrictions bool     `json:"namespace_restrictions"`
				NetworkIsolation      bool     `json:"network_isolation"`
				FilesystemMode        string   `json:"filesystem_mode"`
				AllowedMounts         []string `json:"allowed_mounts"`
			} `json:"requested"`
		} `json:"sandboxStatus"`
	}
	out, err := decodeHarnessOutput(&payload, func() (string, error) {
		return tools.BashTool{
			Workspace:       workspace,
			ConfigHome:      configHome,
			SandboxStrategy: "detect",
		}.Execute(ctx, json.RawMessage(`{
				"command":"printf sandbox-bypass-ok",
				"timeout_ms":1000,
				"dangerouslyDisableSandbox":true,
				"namespaceRestrictions":true,
				"isolateNetwork":true,
				"filesystemMode":"allow-list",
				"allowedMounts":["logs"]
			}`))
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if payload.Stdout != "sandbox-bypass-ok" {
		return localScenarioResult{}, fmt.Errorf("unexpected bash stdout %q", payload.Stdout)
	}
	if !payload.DangerouslyDisableSandbox {
		return localScenarioResult{}, fmt.Errorf("sandbox bypass flag was not preserved")
	}
	if payload.Sandbox != "" {
		return localScenarioResult{}, fmt.Errorf("sandbox command should not be active when bypassed: %q", payload.Sandbox)
	}
	status := payload.SandboxStatus
	if status.Enabled || status.Active || !status.Supported || status.ResolutionStatus != "disabled" || status.ConfiguredStrategy != "off" {
		return localScenarioResult{}, fmt.Errorf("unexpected sandbox status: %#v", status)
	}
	if status.Requested.Enabled ||
		!status.Requested.NamespaceRestrictions ||
		!status.Requested.NetworkIsolation ||
		status.Requested.FilesystemMode != "allow-list" ||
		status.FilesystemMode != "allow-list" ||
		status.FilesystemActive {
		return localScenarioResult{}, fmt.Errorf("unexpected sandbox request/status: %#v", status)
	}
	expectedMount := filepath.Join(workspace, "logs")
	if !slices.Contains(status.AllowedMounts, expectedMount) {
		return localScenarioResult{}, fmt.Errorf("sandbox allowed mounts missing %q: %v", expectedMount, status.AllowedMounts)
	}
	if !slices.Contains(status.Requested.AllowedMounts, "logs") {
		return localScenarioResult{}, fmt.Errorf("sandbox requested mounts missing logs: %v", status.Requested.AllowedMounts)
	}

	return localScenarioResult{
		Output:       out,
		FinalMessage: "sandbox bypass status harness ok",
		ToolCalls:    1,
		ToolUses:     []string{"bash"},
		RequestCount: 1,
	}, nil
}

func policyUpdateSandboxScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	policyEval := policyengine.DefaultEngine().Evaluate(policyengine.LaneContext{
		LaneID:              "lane-policy",
		BranchBehind:        2,
		VerificationBlocked: true,
	})
	if len(policyEval.Actions) != 1 || policyEval.Actions[0].Kind != policyengine.ActionMergeForward {
		return localScenarioResult{}, fmt.Errorf("unexpected policy actions: %#v", policyEval.Actions)
	}
	if len(policyEval.Events) != 1 || policyEval.Events[0].RuleID != "stale-branch-merge-forward" {
		return localScenarioResult{}, fmt.Errorf("unexpected policy events: %#v", policyEval.Events)
	}

	auditStore := audit.NewStore(configHome)
	if err := auditStore.Append(audit.Event{
		Type:           "policy_decision",
		SessionID:      "session-policy",
		Workspace:      "workspace",
		ToolName:       "branch_freshness",
		Allowed:        audit.Bool(false),
		Reason:         policyEval.Actions[0].Reason,
		PermissionMode: "managed",
	}); err != nil {
		return localScenarioResult{}, err
	}
	auditEvents, err := auditStore.List(10)
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(auditEvents) != 1 || auditEvents[0].Type != "policy_decision" || auditEvents[0].Allowed == nil || *auditEvents[0].Allowed {
		return localScenarioResult{}, fmt.Errorf("unexpected audit events: %#v", auditEvents)
	}

	network := true
	logsDir := filepath.Join(workspace, "logs")
	detected := sandbox.Status{
		OS:                 "linux",
		Default:            "bwrap",
		Available:          true,
		Strategies:         []string{"bwrap", "unshare"},
		NamespaceSupported: true,
		NetworkSupported:   true,
		StrategyStatuses: []sandbox.StrategyStatus{
			{Name: "bwrap", Available: true},
			{Name: "unshare", Available: true},
		},
	}
	sandboxStatus, effective, err := sandbox.ResolveSandboxExecutionStatusFor("detect", workspace, sandbox.SandboxRequestOptions{
		NetworkIsolation: &network,
		FilesystemMode:   sandbox.FilesystemIsolationAllowList,
		AllowedMounts:    []string{logsDir},
	}, detected)
	if err != nil {
		return localScenarioResult{}, err
	}
	if effective != "bwrap" || !sandboxStatus.Active || !sandboxStatus.NetworkActive || !sandboxStatus.FilesystemActive || sandboxStatus.ResolutionStatus != "enabled" {
		return localScenarioResult{}, fmt.Errorf("unexpected sandbox status: %#v effective=%q", sandboxStatus, effective)
	}
	if !slices.Contains(sandboxStatus.AllowedMounts, logsDir) {
		return localScenarioResult{}, fmt.Errorf("sandbox allowed mounts missing logs dir: %v", sandboxStatus.AllowedMounts)
	}
	sandboxName, sandboxArgs, err := sandbox.BuildShellCommandWithStatus(effective, workspace, "printf policy-sandbox", sandboxStatus)
	if err != nil {
		return localScenarioResult{}, err
	}
	if sandboxName != "bwrap" || !slices.Contains(sandboxArgs, "--unshare-net") || !slices.Contains(sandboxArgs, logsDir) {
		return localScenarioResult{}, fmt.Errorf("unexpected sandbox command: %s %v", sandboxName, sandboxArgs)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	artifactPayload := []byte("#!/bin/sh\nprintf codog-updated\n")
	artifactSHA := sha256.Sum256(artifactPayload)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			manifest := updater.Manifest{
				Version: "0.2.0",
				Downloads: map[string]string{
					"test": serverURL + "/codog-test",
				},
				Checksums: map[string]string{
					"test": "sha256:" + hex.EncodeToString(artifactSHA[:]),
				},
			}
			payload, err := json.Marshal(manifest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
			if err := json.NewEncoder(w).Encode(manifest); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/codog-test":
			_, _ = w.Write(artifactPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	defer server.Close()
	publicKeyValue := base64.StdEncoding.EncodeToString(publicKey)
	check, err := updater.CheckSigned(ctx, "0.1.0", server.URL+"/manifest.json", publicKeyValue)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !check.UpdateAvailable || !check.SignatureValid || check.LatestVersion != "0.2.0" {
		return localScenarioResult{}, fmt.Errorf("unexpected signed update check: %#v", check)
	}
	download, err := updater.DownloadSigned(ctx, server.URL+"/manifest.json", "test", filepath.Join(workspace, "downloads"), publicKeyValue)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !download.Verified || download.SHA256 != hex.EncodeToString(artifactSHA[:]) {
		return localScenarioResult{}, fmt.Errorf("unexpected signed download: %#v", download)
	}
	target := filepath.Join(workspace, "bin", "codog")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(target, []byte("old-codog"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	install, err := updater.Install(download.Path, target)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !install.Installed || install.BackupPath == "" {
		return localScenarioResult{}, fmt.Errorf("unexpected install result: %#v", install)
	}
	rollback, err := updater.Rollback(target)
	if err != nil {
		return localScenarioResult{}, err
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !rollback.RolledBack || string(restored) != "old-codog" {
		return localScenarioResult{}, fmt.Errorf("unexpected rollback result: %#v restored=%q", rollback, restored)
	}

	report := map[string]any{
		"kind": "policy_update_sandbox",
		"policy": map[string]any{
			"actions": []string{string(policyEval.Actions[0].Kind)},
			"rule":    policyEval.Events[0].RuleID,
		},
		"audit": map[string]any{
			"events":  len(auditEvents),
			"allowed": *auditEvents[0].Allowed,
		},
		"sandbox": map[string]any{
			"strategy":          sandboxStatus.Strategy,
			"active":            sandboxStatus.Active,
			"network_active":    sandboxStatus.NetworkActive,
			"filesystem_active": sandboxStatus.FilesystemActive,
			"command":           sandboxName,
		},
		"updater": map[string]any{
			"latest_version":    check.LatestVersion,
			"signature_valid":   check.SignatureValid,
			"download_verified": download.Verified,
			"installed":         install.Installed,
			"rolled_back":       rollback.RolledBack,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "policy update sandbox harness ok",
		RequestCount: 5,
		MessageCount: 1,
	}, nil
}

func policyApprovalScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

	var staleEval struct {
		Kind    string `json:"kind"`
		Actions []struct {
			Kind             string   `json:"kind"`
			RecoveryScenario string   `json:"recovery_scenario"`
			Commands         []string `json:"commands"`
		} `json:"actions"`
		Events []struct {
			RuleID string `json:"rule_id"`
			Kind   string `json:"kind"`
			Action string `json:"action"`
		} `json:"events"`
	}
	staleOut, err := decodeHarnessOutput(&staleEval, func() (string, error) {
		return registry.Execute(ctx, "PolicyEvaluateTool", json.RawMessage(`{
				"lane_id": "lane-policy",
				"green_level": 3,
				"green_contract_satisfied": true,
				"review_status": "approved",
				"diff_scope": "scoped",
				"branch_status": "stale",
				"branch_behind": 2,
				"verification_blocked": true,
				"completed": true
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if staleEval.Kind != "policy_evaluation" || len(staleEval.Actions) != 3 || staleEval.Actions[0].Kind != "merge_forward" || staleEval.Actions[0].RecoveryScenario != "stale_branch" || !slices.Contains(staleEval.Actions[0].Commands, "branch_freshness") || staleEval.Actions[1].Kind != "closeout_lane" || staleEval.Actions[2].Kind != "cleanup_session" {
		return localScenarioResult{}, fmt.Errorf("unexpected stale policy evaluation: %s", staleOut)
	}
	if len(staleEval.Events) != 3 || staleEval.Events[0].RuleID != "stale-branch-merge-forward" || staleEval.Events[1].RuleID != "lane-completed-closeout" {
		return localScenarioResult{}, fmt.Errorf("unexpected stale policy events: %s", staleOut)
	}

	var escalateEval struct {
		Actions []struct {
			Kind string `json:"kind"`
		} `json:"actions"`
		Events []struct {
			Kind   string `json:"kind"`
			Action string `json:"action"`
		} `json:"events"`
	}
	escalateOut, err := decodeHarnessOutput(&escalateEval, func() (string, error) {
		return registry.Execute(ctx, "policy_evaluate", json.RawMessage(`{
				"lane_id": "lane-startup",
				"blocker": "startup",
				"retry_count": 1,
				"retry_limit": 1
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(escalateEval.Actions) != 1 || escalateEval.Actions[0].Kind != "escalate" || len(escalateEval.Events) != 1 || escalateEval.Events[0].Kind != "escalate" {
		return localScenarioResult{}, fmt.Errorf("unexpected escalation policy evaluation: %s", escalateOut)
	}

	var blocked struct {
		BlockedHandoff struct {
			Kind             string `json:"kind"`
			Status           string `json:"status"`
			Reason           string `json:"reason"`
			PolicySource     string `json:"policy_source"`
			ActorScope       string `json:"actor_scope"`
			TechnicalFailure bool   `json:"technical_failure"`
			Fallback         []struct {
				Kind string `json:"kind"`
			} `json:"fallback"`
		} `json:"blocked_handoff"`
	}
	blockedOut, err := decodeHarnessOutput(&blocked, func() (string, error) {
		return registry.Execute(ctx, "policy_evaluate", json.RawMessage(`{
				"lane_id": "lane-main",
				"requested_action": "git push origin main",
				"repository": "owner/repo",
				"branch": "main",
				"actor": "release-bot",
				"actor_scope": "automation",
				"policy_source": "AGENTS.md"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if blocked.BlockedHandoff.Kind != "policy_blocked_handoff" || blocked.BlockedHandoff.Status != "blocked_by_policy" || blocked.BlockedHandoff.Reason != "main_push_forbidden" || blocked.BlockedHandoff.PolicySource != "AGENTS.md" || blocked.BlockedHandoff.ActorScope != "automation" || blocked.BlockedHandoff.TechnicalFailure || len(blocked.BlockedHandoff.Fallback) != 2 || blocked.BlockedHandoff.Fallback[0].Kind != "create_branch" || blocked.BlockedHandoff.Fallback[1].Kind != "open_pr" {
		return localScenarioResult{}, fmt.Errorf("unexpected policy-blocked handoff: %s", blockedOut)
	}

	scope := `"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"}`
	var pending struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
		Grant  struct {
			Token  string `json:"token"`
			Status string `json:"status"`
		} `json:"grant"`
	}
	pendingOut, err := decodeHarnessOutput(&pending, func() (string, error) {
		return registry.Execute(ctx, "ApprovalTokenTool", json.RawMessage(`{
				"action": "pending",
				"token": "tok-main",
				`+scope+`,
				"approving_actor": "owner",
				"requesting_actor": "release-lead",
				"approved_executor": "release-bot"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if pending.Kind != "approval_token" || pending.Action != "pending" || pending.Status != "ok" || pending.Grant.Token != "tok-main" || pending.Grant.Status != "approval_pending" {
		return localScenarioResult{}, fmt.Errorf("unexpected approval pending output: %s", pendingOut)
	}

	var pendingVerify struct {
		Status    string `json:"status"`
		ErrorKind string `json:"error_kind"`
	}
	pendingVerifyOut, err := decodeHarnessOutput(&pendingVerify, func() (string, error) {
		return registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "verify",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if pendingVerify.Status != "denied" || pendingVerify.ErrorKind != "approval_pending" {
		return localScenarioResult{}, fmt.Errorf("unexpected pending verify output: %s", pendingVerifyOut)
	}

	var grant struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		Status string `json:"status"`
		Grant  struct {
			Token                 string `json:"token"`
			ReplayPreventionNonce string `json:"replay_prevention_nonce"`
			Status                string `json:"status"`
			ApprovingActor        string `json:"approving_actor"`
			ApprovedExecutor      string `json:"approved_executor"`
			MaxUses               int    `json:"max_uses"`
		} `json:"grant"`
	}
	grantOut, err := decodeHarnessOutput(&grant, func() (string, error) {
		return registry.Execute(ctx, "ApprovalTokenTool", json.RawMessage(`{
				"action": "approve",
				"token": "tok-main",
				`+scope+`,
				"approving_actor": "owner",
				"requesting_actor": "release-lead",
				"approved_executor": "release-bot",
				"max_uses": 1,
				"delegation_chain": [{"actor":"owner","session_id":"session-owner","reason":"owner approval"},{"actor":"orchestrator","session_id":"session-orchestrator","reason":"relay"}]
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if grant.Kind != "approval_token" || grant.Action != "approve" || grant.Status != "ok" || grant.Grant.Token != "tok-main" || grant.Grant.ReplayPreventionNonce == "" || grant.Grant.Status != "approval_granted" || grant.Grant.ApprovingActor != "owner" || grant.Grant.ApprovedExecutor != "release-bot" || grant.Grant.MaxUses != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected approval approve output: %s", grantOut)
	}

	var verify struct {
		Status string `json:"status"`
		Audit  struct {
			Kind                  string `json:"kind"`
			Token                 string `json:"token"`
			ReplayPreventionNonce string `json:"replay_prevention_nonce"`
			Scope                 struct {
				Commit string `json:"commit"`
			} `json:"scope"`
			RequestingActor    string `json:"requesting_actor"`
			ExecutingActor     string `json:"executing_actor"`
			ExecutionMode      string `json:"execution_mode"`
			Status             string `json:"status"`
			DelegatedExecution bool   `json:"delegated_execution"`
			DelegationChain    []struct {
				Actor string `json:"actor"`
			} `json:"delegation_chain"`
		} `json:"audit"`
	}
	verifyOut, err := decodeHarnessOutput(&verify, func() (string, error) {
		return registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "verify",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if verify.Status != "ok" || verify.Audit.Kind != "approval_token_audit" || verify.Audit.Token != "tok-main" || verify.Audit.ReplayPreventionNonce != grant.Grant.ReplayPreventionNonce || verify.Audit.Scope.Commit != "abc123" || verify.Audit.RequestingActor != "release-lead" || verify.Audit.ExecutingActor != "release-bot" || verify.Audit.ExecutionMode != "delegated_execution" || verify.Audit.Status != "approval_granted" || !verify.Audit.DelegatedExecution || len(verify.Audit.DelegationChain) != 4 || verify.Audit.DelegationChain[1].Actor != "orchestrator" || verify.Audit.DelegationChain[2].Actor != "release-lead" || verify.Audit.DelegationChain[3].Actor != "release-bot" {
		return localScenarioResult{}, fmt.Errorf("unexpected approval verify output: %s", verifyOut)
	}

	var consume struct {
		Status string `json:"status"`
		Audit  struct {
			Status string `json:"status"`
			Uses   int    `json:"uses"`
		} `json:"audit"`
	}
	consumeOut, err := decodeHarnessOutput(&consume, func() (string, error) {
		return registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "consume",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if consume.Status != "ok" || consume.Audit.Status != "approval_consumed" || consume.Audit.Uses != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected approval consume output: %s", consumeOut)
	}

	var replay struct {
		Status    string `json:"status"`
		ErrorKind string `json:"error_kind"`
	}
	replayOut, err := decodeHarnessOutput(&replay, func() (string, error) {
		return registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "consume",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if replay.Status != "denied" || replay.ErrorKind != "approval_already_consumed" {
		return localScenarioResult{}, fmt.Errorf("unexpected approval replay output: %s", replayOut)
	}

	var list struct {
		Status string `json:"status"`
		Ledger struct {
			Kind   string `json:"kind"`
			Grants []struct {
				Token                 string `json:"token"`
				ReplayPreventionNonce string `json:"replay_prevention_nonce"`
				Status                string `json:"status"`
				State                 string `json:"state"`
				Usable                bool   `json:"usable"`
				Uses                  int    `json:"uses"`
				RemainingUses         int    `json:"remaining_uses"`
				LastAuditErrorKind    string `json:"last_audit_error_kind"`
			} `json:"grants"`
		} `json:"ledger"`
	}
	listOut, err := decodeHarnessOutput(&list, func() (string, error) {
		return registry.Execute(ctx, "approval_token", json.RawMessage(`{"action":"list"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if list.Status != "ok" || list.Ledger.Kind != "approval_token_ledger" || len(list.Ledger.Grants) != 1 || list.Ledger.Grants[0].Token != "tok-main" || list.Ledger.Grants[0].ReplayPreventionNonce != grant.Grant.ReplayPreventionNonce || list.Ledger.Grants[0].Status != "approval_consumed" || list.Ledger.Grants[0].State != "consumed" || list.Ledger.Grants[0].Usable || list.Ledger.Grants[0].Uses != 1 || list.Ledger.Grants[0].RemainingUses != 0 || list.Ledger.Grants[0].LastAuditErrorKind != "approval_already_consumed" {
		return localScenarioResult{}, fmt.Errorf("unexpected approval token ledger output: %s", listOut)
	}

	report := map[string]any{
		"kind": "policy_approval",
		"policy": map[string]any{
			"stale_actions":     []string{staleEval.Actions[0].Kind, staleEval.Actions[1].Kind, staleEval.Actions[2].Kind},
			"escalation_action": escalateEval.Actions[0].Kind,
			"blocked_status":    blocked.BlockedHandoff.Status,
			"blocked_reason":    blocked.BlockedHandoff.Reason,
			"fallback":          []string{blocked.BlockedHandoff.Fallback[0].Kind, blocked.BlockedHandoff.Fallback[1].Kind},
		},
		"approval": map[string]any{
			"token":                grant.Grant.Token,
			"replay_nonce":         verify.Audit.ReplayPreventionNonce,
			"ledger_replay_nonce":  list.Ledger.Grants[0].ReplayPreventionNonce,
			"scope_commit":         verify.Audit.Scope.Commit,
			"requesting_actor":     verify.Audit.RequestingActor,
			"executing_actor":      verify.Audit.ExecutingActor,
			"execution_mode":       verify.Audit.ExecutionMode,
			"pending":              pending.Grant.Status,
			"pending_verify_error": pendingVerify.ErrorKind,
			"verified":             verify.Audit.Status,
			"delegated":            verify.Audit.DelegatedExecution,
			"consumed":             consume.Audit.Status,
			"replay_error":         replay.ErrorKind,
			"ledger_status":        list.Ledger.Grants[0].Status,
			"ledger_state":         list.Ledger.Grants[0].State,
			"ledger_usable":        list.Ledger.Grants[0].Usable,
			"remaining_uses":       list.Ledger.Grants[0].RemainingUses,
			"last_audit_error":     list.Ledger.Grants[0].LastAuditErrorKind,
			"delegation_hop_count": len(verify.Audit.DelegationChain),
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "policy approval harness ok",
		RequestCount: 10,
		MessageCount: 1,
		ToolCalls:    10,
		ToolUses: []string{
			"policy_evaluate",
			"policy_evaluate",
			"policy_evaluate",
			"approval_token",
			"approval_token",
			"approval_token",
			"approval_token",
			"approval_token",
			"approval_token",
			"approval_token",
		},
	}, nil
}

func notebookReadEditScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	notebookPath := filepath.Join(workspace, "analysis.ipynb")
	initial := `{
  "cells": [
    {"cell_type":"markdown","id":"intro","source":["# Title\n","notes"],"metadata":{}},
    {"cell_type":"code","id":"calc","execution_count":1,"source":["print('hi')\n"],"metadata":{},"outputs":[{"output_type":"stream","name":"stdout","text":["hi\n"]}]}
  ],
  "metadata": {"kernelspec":{"language":"python"}},
  "nbformat": 4,
  "nbformat_minor": 5
}`
	if err := os.WriteFile(notebookPath, []byte(initial), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	readOut, err := tools.NotebookReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"notebook_path":"analysis.ipynb","cell_index":1,"include_outputs":true}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "notebook_read"`, `"path": "analysis.ipynb"`, `"cell_count": 2`, `"cell_id": "calc"`, `"output_count": 1`, `"outputs": [`} {
		if !strings.Contains(readOut, expected) {
			return localScenarioResult{}, fmt.Errorf("notebook read output missing %s", expected)
		}
	}

	replaceOut, err := tools.NotebookEditTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"notebook_path":"analysis.ipynb","cell_id":"intro","cell_type":"markdown","new_source":"# Renamed\n"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(replaceOut, `"kind": "notebook_edit"`, `"mode": "replace"`, `"cell_id": "intro"`) {
		return localScenarioResult{}, fmt.Errorf("unexpected notebook replace output: %s", replaceOut)
	}

	insertOut, err := tools.NotebookEditTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"notebook_path":"analysis.ipynb","cell_id":"intro","edit_mode":"insert","cell_type":"code","new_source":"print(2)\n"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(insertOut, `"mode": "insert"`, `"cell_type": "code"`, `"cell_count": 3`) {
		return localScenarioResult{}, fmt.Errorf("unexpected notebook insert output: %s", insertOut)
	}

	finalRead, err := tools.NotebookReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"notebook_path":"analysis.ipynb","limit":3,"include_outputs":true}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	data, err := os.ReadFile(notebookPath)
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{"# Renamed", "print(2)", `"id": "cell-3"`, `"kernelspec"`} {
		if !strings.Contains(string(data), expected) {
			return localScenarioResult{}, fmt.Errorf("persisted notebook missing %s", expected)
		}
	}
	if strings.Contains(string(data), "# Title") {
		return localScenarioResult{}, fmt.Errorf("persisted notebook still contains replaced title")
	}
	if !harnessContainsAll(finalRead, `"cell_count": 3`, "# Renamed", "print(2)") {
		return localScenarioResult{}, fmt.Errorf("final notebook read missing edited cells: %s", finalRead)
	}

	return localScenarioResult{
		Output:       strings.Join([]string{readOut, replaceOut, insertOut, finalRead}, "\n"),
		FinalMessage: "notebook read edit harness ok",
		ToolCalls:    4,
		ToolUses:     []string{"notebook_read", "notebook_edit", "notebook_edit", "notebook_read"},
		RequestCount: 4,
	}, nil
}

func webAccessScenarioRunLocal(ctx context.Context, _ string) (localScenarioResult, error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><head><title>Codog Web Parity</title><script>ignored()</script></head><body><main><h1>Fetch Summary</h1><p>Codog fetches local HTML text for grounded answers.</p></main></body></html>`)
		case "/search":
			if got := r.URL.Query().Get("q"); got != "codog web parity" {
				http.Error(w, "unexpected query "+got, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `
<html><body>
  <a class="result__a" href="https://example.com/codog">Codog docs</a>
  <div class="result__snippet">Go implementation notes for Codog web parity.</div>
  <a class="result__a" href="https://blocked.example/skip">Blocked result</a>
  <div class="result__snippet">This result should be filtered.</div>
</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetchInput := json.RawMessage(fmt.Sprintf(`{"url":%q,"prompt":"Return the title","timeout_ms":5000}`, server.URL+"/page"))
	fetchOut, err := tools.WebFetchTool{}.Execute(ctx, fetchInput)
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"code": 200`, `"codeText": "OK"`, `"title": "Codog Web Parity"`, `"summary": "Title: Codog Web Parity"`, `"durationMs":`} {
		if !strings.Contains(fetchOut, expected) {
			return localScenarioResult{}, fmt.Errorf("web fetch output missing %s", expected)
		}
	}

	restoreSearchEnv := setenvForScenario("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/search")
	defer restoreSearchEnv()
	searchOut, err := tools.WebSearchTool{}.Execute(ctx, json.RawMessage(`{"query":"codog web parity","max_results":1,"allowed_domains":["example.com"],"timeout_ms":5000}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"query": "codog web parity"`, `"tool_use_id": "web_search_1"`, `"title": "Codog docs"`, `"url": "https://example.com/codog"`, `"hits": [`, `"durationSeconds":`} {
		if !strings.Contains(searchOut, expected) {
			return localScenarioResult{}, fmt.Errorf("web search output missing %s", expected)
		}
	}
	if strings.Contains(searchOut, "Blocked result") || strings.Contains(searchOut, "blocked.example") {
		return localScenarioResult{}, fmt.Errorf("web search output included filtered result: %s", searchOut)
	}

	return localScenarioResult{
		Output:       strings.Join([]string{fetchOut, searchOut}, "\n"),
		FinalMessage: "web access harness ok",
		ToolCalls:    2,
		ToolUses:     []string{"web_fetch", "web_search"},
		RequestCount: 2,
	}, nil
}

func webAccessLimitsScenarioRunLocal(ctx context.Context, _ string) (localScenarioResult, error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, strings.Repeat("bounded fetch content ", 8))
		case "/search":
			if got := r.URL.Query().Get("q"); got != "codog blocked parity" {
				http.Error(w, "unexpected query "+got, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `
<html><body>
  <a class="result__a" href="https://example.com/blocked">Filtered docs</a>
  <div class="result__snippet">This result should be removed by the blocked domain filter.</div>
</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetchInput := json.RawMessage(fmt.Sprintf(`{"url":%q,"prompt":"Summarize briefly","max_bytes":32,"timeout_ms":5000}`, server.URL+"/large"))
	fetchOut, err := tools.WebFetchTool{}.Execute(ctx, fetchInput)
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"code": 200`, `"bytes": 32`, `"truncated": true`, `"result": "bounded fetch content bounded fe"`} {
		if !strings.Contains(fetchOut, expected) {
			return localScenarioResult{}, fmt.Errorf("bounded web fetch output missing %s", expected)
		}
	}

	restoreSearchEnv := setenvForScenario("CODOG_WEB_SEARCH_BASE_URL", server.URL+"/search")
	defer restoreSearchEnv()
	searchOut, err := tools.WebSearchTool{}.Execute(ctx, json.RawMessage(`{"query":"codog blocked parity","blocked_domains":["example.com"],"timeout_ms":5000}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"query": "codog blocked parity"`, `"tool_use_id": "web_search_1"`, `"hits": []`, `"content": []`, "No web search results matched"} {
		if !strings.Contains(searchOut, expected) {
			return localScenarioResult{}, fmt.Errorf("filtered web search output missing %s", expected)
		}
	}
	if strings.Contains(searchOut, "Filtered docs") || strings.Contains(searchOut, "example.com/blocked") {
		return localScenarioResult{}, fmt.Errorf("filtered web search output included blocked result: %s", searchOut)
	}

	return localScenarioResult{
		Output:       strings.Join([]string{fetchOut, searchOut}, "\n"),
		FinalMessage: "web access limits harness ok",
		ToolCalls:    2,
		ToolUses:     []string{"web_fetch", "web_search"},
		RequestCount: 2,
	}, nil
}

func gitWorkspaceScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
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
	notesPath := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("alpha\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"add", "notes.txt"},
		{"commit", "-q", "-m", "initial notes"},
	} {
		if err := runHarnessGit(workspace, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	if err := os.WriteFile(notesPath, []byte("alpha\nbeta\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}

	statusOut, err := tools.GitStatusTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(statusOut, `"output"`, "notes.txt") {
		return localScenarioResult{}, fmt.Errorf("unexpected git status output: %s", statusOut)
	}
	diffOut, err := tools.GitDiffTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"path":"notes.txt"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !strings.Contains(diffOut, "+beta") {
		return localScenarioResult{}, fmt.Errorf("git diff output missing edit: %s", diffOut)
	}
	logOut, err := tools.GitLogTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"count":1,"oneline":true}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !strings.Contains(logOut, "initial notes") {
		return localScenarioResult{}, fmt.Errorf("git log output missing commit subject: %s", logOut)
	}
	showOut, err := tools.GitShowTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"commit":"HEAD","format":"metadata"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !strings.Contains(showOut, "initial notes") {
		return localScenarioResult{}, fmt.Errorf("git show output missing metadata: %s", showOut)
	}
	blameOut, err := tools.GitBlameTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"path":"notes.txt","start_line":1,"end_line":1}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !harnessContainsAll(blameOut, "alpha", "Codog Test") {
		return localScenarioResult{}, fmt.Errorf("git blame output missing line attribution: %s", blameOut)
	}

	for _, args := range [][]string{
		{"restore", "notes.txt"},
		{"switch", "-q", "-c", "topic"},
		{"switch", "-q", "main"},
	} {
		if err := runHarnessGit(workspace, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	for _, args := range [][]string{
		{"add", "fix.txt"},
		{"commit", "-q", "-m", "fix: main update"},
	} {
		if err := runHarnessGit(workspace, args...); err != nil {
			return localScenarioResult{}, err
		}
	}
	freshnessOut, err := tools.BranchFreshnessTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"branch":"topic","base":"main"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "branch_freshness"`, `"status": "stale"`, `"verification_blocked": true`, `"lane_event": "branch.stale_against_main"`, `"recovery_scenario": "stale_branch"`} {
		if !strings.Contains(freshnessOut, expected) {
			return localScenarioResult{}, fmt.Errorf("branch freshness output missing %s", expected)
		}
	}

	return localScenarioResult{
		Output:       strings.Join([]string{statusOut, diffOut, logOut, showOut, blameOut, freshnessOut}, "\n"),
		FinalMessage: "git workspace harness ok",
		ToolCalls:    6,
		ToolUses:     []string{"git_status", "git_diff", "git_log", "git_show", "git_blame", "branch_freshness"},
		RequestCount: 6,
	}, nil
}
