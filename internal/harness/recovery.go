package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
)

func recoveryLifecycleScenario() scenario {
	return scenario{
		name:     "recovery_lifecycle_roundtrip",
		runLocal: recoveryLifecycleScenarioRunLocal,
	}
}

func agentMarkdownDefinitionScenario() scenario {
	return scenario{
		name:     "agent_markdown_definition_roundtrip",
		runLocal: agentMarkdownDefinitionScenarioRunLocal,
	}
}

func nudgeAckDedupeScenario() scenario {
	return scenario{
		name:     "nudge_ack_dedupe_roundtrip",
		runLocal: nudgeAckDedupeScenarioRunLocal,
	}
}

func provisionalStatusEscalationScenario() scenario {
	return scenario{
		name:     "provisional_status_escalation_roundtrip",
		runLocal: provisionalStatusEscalationScenarioRunLocal,
	}
}

func roadmapPinpointLifecycleScenario() scenario {
	return scenario{
		name:     "roadmap_pinpoint_lifecycle_roundtrip",
		runLocal: roadmapPinpointLifecycleScenarioRunLocal,
	}
}

func reportAtomicUpdateScenario() scenario {
	return scenario{
		name:     "report_atomic_update_roundtrip",
		runLocal: reportAtomicUpdateScenarioRunLocal,
	}
}

func reportBackpressureScenario() scenario {
	return scenario{
		name:     "report_backpressure_roundtrip",
		runLocal: reportBackpressureScenarioRunLocal,
	}
}

func claimKind(claims []any, idFragment string) string {
	for _, value := range claims {
		claim, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, _ := claim["id"].(string)
		if strings.Contains(id, idFragment) {
			kind, _ := claim["kind"].(string)
			return kind
		}
	}
	return ""
}

func claimPromotedFrom(claims []any, idFragment string) string {
	for _, value := range claims {
		claim, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, _ := claim["id"].(string)
		if strings.Contains(id, idFragment) {
			promotedFrom, _ := claim["promoted_from"].(string)
			return promotedFrom
		}
	}
	return ""
}

func negativeEvidenceField(values []any, query string, field string) string {
	for _, value := range values {
		evidence, ok := value.(map[string]any)
		if !ok {
			continue
		}
		evidenceQuery, _ := evidence["query"].(string)
		if evidenceQuery == query {
			result, _ := evidence[field].(string)
			return result
		}
	}
	return ""
}

func fieldDeltaState(values []any, field string) string {
	for _, value := range values {
		delta, ok := value.(map[string]any)
		if !ok {
			continue
		}
		deltaField, _ := delta["field"].(string)
		if deltaField == field {
			state, _ := delta["state"].(string)
			return state
		}
	}
	return ""
}

func fieldDeltaStateSuffix(values []any, suffix string) string {
	for _, value := range values {
		delta, ok := value.(map[string]any)
		if !ok {
			continue
		}
		deltaField, _ := delta["field"].(string)
		if strings.HasSuffix(deltaField, suffix) {
			state, _ := delta["state"].(string)
			return state
		}
	}
	return ""
}

func backgroundAgentRunScenario() scenario {
	return scenario{
		name:     "background_agent_run_roundtrip",
		runLocal: backgroundAgentRunScenarioRunLocal,
	}
}

func sshPrintPlanScenario() scenario {
	return scenario{
		name:     "ssh_print_plan_roundtrip",
		runLocal: sshPrintPlanScenarioRunLocal,
	}
}

func remoteTriggerScenario() scenario {
	var receivedMethod string
	var receivedPath string
	var receivedHeader string
	var receivedBody string
	return scenario{
		name:       "remote_trigger_roundtrip",
		permission: tools.PermissionAllow,
		prompt:     "trigger remote webhook",
		prepare: func(_ string) ([]mockanthropic.Turn, func(), error) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedMethod = r.Method
				receivedPath = r.URL.Path
				receivedHeader = r.Header.Get("x-harness")
				data, _ := io.ReadAll(r.Body)
				receivedBody = string(data)
				w.Header().Set("x-harness-result", "ok")
				fmt.Fprint(w, "abcdef")
			}))
			input, err := json.Marshal(map[string]any{
				"url":       server.URL + "/hook",
				"method":    "POST",
				"headers":   map[string]string{"x-harness": "token"},
				"body":      "payload",
				"max_bytes": 4,
			})
			if err != nil {
				server.Close()
				return nil, nil, err
			}
			return []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "remote_trigger",
					Input: input,
				}}},
				{Text: "remote trigger harness ok"},
			}, server.Close, nil
		},
		verify: func(_ string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "remote trigger harness ok") {
				return fmt.Errorf("missing remote trigger final response")
			}
			if err := expectToolCalls(result, 1, false); err != nil {
				return err
			}
			if receivedMethod != http.MethodPost || receivedPath != "/hook" || receivedHeader != "token" || receivedBody != "payload" {
				return fmt.Errorf("unexpected remote trigger request method=%q path=%q header=%q body=%q", receivedMethod, receivedPath, receivedHeader, receivedBody)
			}
			toolOutput := result.ToolCalls[0].Output
			for _, expected := range []string{`"status_code": 200`, `"body": "abcd"`, `"truncated": true`, `"X-Harness-Result": [`} {
				if !strings.Contains(toolOutput, expected) {
					return fmt.Errorf("remote trigger output missing %s: %s", expected, toolOutput)
				}
			}
			return nil
		},
	}
}

func remoteAPIListenerScenario() scenario {
	return scenario{
		name:     "remote_api_listener_roundtrip",
		runLocal: remoteAPIListenerScenarioRunLocal,
	}
}

func remoteBridgeWorkspaceScenario() scenario {
	return scenario{
		name:     "remote_bridge_workspace_roundtrip",
		runLocal: remoteBridgeWorkspaceScenarioRunLocal,
	}
}

func mcpLifecycleScenario() scenario {
	return scenario{
		name:     "mcp_lifecycle_roundtrip",
		runLocal: mcpLifecycleScenarioRunLocal,
	}
}

func mcpToolHookScenario() scenario {
	state := &mcpToolHookState{toolName: tools.NewMCPToolName("workflow", "echo")}
	return scenario{
		name:       "mcp_tool_hook_roundtrip",
		permission: tools.PermissionWorkspace,
		hooks: config.HookConfig{
			PreToolUseCommands: []config.HookCommand{{
				Matcher: state.toolName,
				Command: `printf '%s' '{"systemMessage":"mcp pre hook","hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"mcp hook ok","updatedInput":{"text":"hooked mcp input"}}}'`,
			}},
			PostToolUseCommands: []config.HookCommand{{
				Matcher: state.toolName,
				Command: `printf '%s' '{"systemMessage":"mcp post hook"}'`,
			}},
		},
		prepare:           state.prepare,
		configureRegistry: state.configureRegistry,
		prompt:            "call hooked MCP tool",
		verify:            state.verify,
	}
}

type mcpToolHookState struct {
	toolName           string
	serverURL          string
	seenMethods        []string
	seenHookedArgument bool
}

func (s *mcpToolHookState) prepare(_ string) ([]mockanthropic.Turn, func(), error) {
	server := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	s.serverURL = server.URL + "/mcp"
	turns := []mockanthropic.Turn{
		{ToolUses: []mockanthropic.ToolUse{{ID: "tool-1", Name: s.toolName, Input: json.RawMessage(`{"text":"original mcp input"}`)}}},
		{Text: "mcp tool hook harness ok"},
	}
	return turns, server.Close, nil
}

func (s *mcpToolHookState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	method, _ := request["method"].(string)
	s.seenMethods = append(s.seenMethods, method)
	s.handleRequest(w, method, request)
}

func (s *mcpToolHookState) handleRequest(w http.ResponseWriter, method string, request map[string]any) {
	id := request["id"]
	switch method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "mcp-tool-hook-session")
		writeMCPHarnessResponse(w, id, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mcp-tool-hook", "version": "1.0.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/call":
		s.handleToolCall(w, id, request)
	default:
		writeMCPHarnessError(w, id, "unsupported method: "+method)
	}
}

func (s *mcpToolHookState) handleToolCall(w http.ResponseWriter, id any, request map[string]any) {
	params, _ := request["params"].(map[string]any)
	if params["name"] != "echo" {
		writeMCPHarnessError(w, id, fmt.Sprintf("unexpected tool name %v", params["name"]))
		return
	}
	args, _ := params["arguments"].(map[string]any)
	text, _ := args["text"].(string)
	if text != "hooked mcp input" {
		writeMCPHarnessError(w, id, "pre hook did not update MCP input")
		return
	}
	s.seenHookedArgument = true
	writeMCPHarnessResponse(w, id, map[string]any{"content": []map[string]any{{"type": "text", "text": "mcp tool hook saw hooked mcp input"}}})
}

func (s *mcpToolHookState) configureRegistry(registry *tools.Registry) error {
	if strings.TrimSpace(s.serverURL) == "" {
		return errors.New("MCP server URL was not prepared")
	}
	registry.Register(tools.MCPTool{
		Name: s.toolName, ServerName: "workflow", RemoteName: "echo",
		Description: "Echo text through the MCP hook harness.",
		Schema:      map[string]any{"type": "object", "additionalProperties": true},
		Server:      config.MCPServerConfig{URL: s.serverURL},
	})
	return nil
}

func (s *mcpToolHookState) verify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "mcp tool hook harness ok") {
		return fmt.Errorf("missing MCP tool hook final response")
	}
	if err := expectToolCalls(result, 1, false); err != nil {
		return err
	}
	call := result.ToolCalls[0]
	if call.Name != s.toolName {
		return fmt.Errorf("unexpected MCP tool name %q", call.Name)
	}
	if call.Input != `{"text":"hooked mcp input"}` {
		return fmt.Errorf("MCP tool input was not updated by hook: %s", call.Input)
	}
	if !harnessContainsAll(call.Output, "mcp tool hook saw hooked mcp input", "Hook feedback:\nmcp post hook") {
		return fmt.Errorf("MCP tool output missing result or post-hook feedback: %s", call.Output)
	}
	for _, method := range []string{"initialize", "notifications/initialized", "tools/call"} {
		if !slices.Contains(s.seenMethods, method) {
			return fmt.Errorf("MCP hook server did not receive %s; methods=%v", method, s.seenMethods)
		}
	}
	if !s.seenHookedArgument {
		return fmt.Errorf("MCP hook server did not receive the updated argument")
	}
	return nil
}

func mcpAuthOAuthRefreshScenario() scenario {
	return scenario{
		name:     "mcp_auth_oauth_refresh_roundtrip",
		runLocal: mcpAuthOAuthRefreshScenarioRunLocal,
	}
}

func mcpAuthRecoveryScenario() scenario {
	return scenario{
		name:     "mcp_auth_recovery_roundtrip",
		runLocal: mcpAuthRecoveryScenarioRunLocal,
	}
}

func writeJSONBody(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeMCPHarnessResponse(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeMCPHarnessError(w http.ResponseWriter, id any, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": message,
		},
	})
}

func getControlBody(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func postControlBody(url string, payload string) (string, error) {
	resp, err := http.Post(url, "application/json", strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func recoveryLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

	var recipeReport struct {
		Kind   string `json:"kind"`
		Recipe struct {
			ID               string `json:"id"`
			Scenario         string `json:"scenario"`
			MaxAttempts      int    `json:"max_attempts"`
			EscalationPolicy string `json:"escalation_policy"`
			Steps            []struct {
				Kind string `json:"kind"`
			} `json:"steps"`
		} `json:"recipe"`
	}
	recipeOut, err := decodeHarnessOutput(&recipeReport, func() (string, error) {
		return registry.Execute(ctx, "RecoveryRecipeTool", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(recipeReport.Recipe.Steps) != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected recovery recipe output: %s", recipeOut)
	}
	if err := verifyHarnessChecks("recovery recipe output", recipeOut,
		recipeReport.Kind == "recovery_recipe",
		recipeReport.Recipe.ID == "stale_branch",
		recipeReport.Recipe.MaxAttempts == 1,
		recipeReport.Recipe.Steps[0].Kind == "merge_forward_branch",
	); err != nil {
		return localScenarioResult{}, err
	}

	var initialStatus struct {
		Kind   string `json:"kind"`
		Status struct {
			Scenario          string `json:"scenario"`
			Attempted         bool   `json:"attempted"`
			AttemptsRemaining int    `json:"attempts_remaining"`
		} `json:"status"`
	}
	statusOut, err := decodeHarnessOutput(&initialStatus, func() (string, error) {
		return registry.Execute(ctx, "recovery_status", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := verifyHarnessChecks("initial recovery status output", statusOut,
		initialStatus.Kind == "recovery_status",
		initialStatus.Status.Scenario == "stale_branch",
		!initialStatus.Status.Attempted,
		initialStatus.Status.AttemptsRemaining == 1,
	); err != nil {
		return localScenarioResult{}, err
	}

	var firstAttempt struct {
		Kind   string `json:"kind"`
		Result struct {
			Kind       string `json:"kind"`
			StepsTaken int    `json:"steps_taken"`
		} `json:"result"`
		Entry struct {
			State        string `json:"state"`
			AttemptCount int    `json:"attempt_count"`
		} `json:"entry"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	firstAttemptOut, err := decodeHarnessOutput(&firstAttempt, func() (string, error) {
		return registry.Execute(ctx, "recovery_attempt", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(firstAttempt.Events) == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected first recovery attempt output: %s", firstAttemptOut)
	}
	if err := verifyHarnessChecks("first recovery attempt output", firstAttemptOut,
		firstAttempt.Kind == "recovery_attempt",
		firstAttempt.Result.Kind == "recovered",
		firstAttempt.Result.StepsTaken == 2,
		firstAttempt.Entry.State == "succeeded",
		firstAttempt.Entry.AttemptCount == 1,
		firstAttempt.Events[len(firstAttempt.Events)-1].Type == "recovery.succeeded",
	); err != nil {
		return localScenarioResult{}, err
	}

	var secondAttempt struct {
		Result struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"result"`
		Entry struct {
			State            string `json:"state"`
			EscalationReason string `json:"escalation_reason"`
		} `json:"entry"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	secondAttemptOut, err := decodeHarnessOutput(&secondAttempt, func() (string, error) {
		return registry.Execute(ctx, "RecoveryAttemptTool", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(secondAttempt.Events) == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected second recovery attempt output: %s", secondAttemptOut)
	}
	if err := verifyHarnessChecks("second recovery attempt output", secondAttemptOut,
		secondAttempt.Result.Kind == "escalation_required",
		secondAttempt.Entry.State == "exhausted",
		strings.Contains(secondAttempt.Entry.EscalationReason, "max recovery attempts"),
		secondAttempt.Events[len(secondAttempt.Events)-1].Type == "recovery.escalated",
	); err != nil {
		return localScenarioResult{}, err
	}

	var partialAttempt struct {
		Result struct {
			Kind      string `json:"kind"`
			Recovered []struct {
				Kind string `json:"kind"`
			} `json:"recovered"`
			Remaining []struct {
				Kind string `json:"kind"`
			} `json:"remaining"`
		} `json:"result"`
		Entry struct {
			State              string `json:"state"`
			LastFailureSummary string `json:"last_failure_summary"`
		} `json:"entry"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	partialAttemptOut, err := decodeHarnessOutput(&partialAttempt, func() (string, error) {
		return registry.Execute(ctx, "recovery_attempt", json.RawMessage(`{
				"scenario": "partial_plugin_startup",
				"failure_summary": "mcp still unhealthy",
				"failed_step_index": 1
			}`), nil)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(partialAttempt.Result.Recovered) != 1 || len(partialAttempt.Result.Remaining) != 1 || len(partialAttempt.Events) == 0 {
		return localScenarioResult{}, fmt.Errorf("unexpected partial recovery attempt output: %s", partialAttemptOut)
	}
	if err := verifyHarnessChecks("partial recovery attempt output", partialAttemptOut,
		partialAttempt.Result.Kind == "partial_recovery",
		partialAttempt.Entry.State == "failed",
		partialAttempt.Entry.LastFailureSummary == "mcp still unhealthy",
		partialAttempt.Result.Recovered[0].Kind == "restart_plugin",
		partialAttempt.Result.Remaining[0].Kind == "retry_mcp_handshake",
		partialAttempt.Events[len(partialAttempt.Events)-1].Type == "recovery.failed",
	); err != nil {
		return localScenarioResult{}, err
	}

	var ledger struct {
		Kind     string `json:"kind"`
		Statuses []struct {
			Scenario           string `json:"scenario"`
			Attempted          bool   `json:"attempted"`
			State              string `json:"state"`
			AttemptCount       int    `json:"attempt_count"`
			EscalationReason   string `json:"escalation_reason"`
			LastFailureSummary string `json:"last_failure_summary"`
		} `json:"statuses"`
		Entries []struct {
			Trigger      string `json:"trigger"`
			State        string `json:"state"`
			AttemptCount int    `json:"attempt_count"`
		} `json:"entries"`
	}
	ledgerOut, err := decodeHarnessOutput(&ledger, func() (string, error) { return registry.Execute(ctx, "RecoveryStatusTool", json.RawMessage(`{}`), nil) })
	if err != nil {
		return localScenarioResult{}, err
	}
	if ledger.Kind != "recovery_ledger" || len(ledger.Entries) != 2 {
		return localScenarioResult{}, fmt.Errorf("unexpected recovery ledger output: %s", ledgerOut)
	}
	var staleStatus struct {
		Scenario           string `json:"scenario"`
		Attempted          bool   `json:"attempted"`
		State              string `json:"state"`
		AttemptCount       int    `json:"attempt_count"`
		EscalationReason   string `json:"escalation_reason"`
		LastFailureSummary string `json:"last_failure_summary"`
	}
	var partialStatus struct {
		Scenario           string `json:"scenario"`
		Attempted          bool   `json:"attempted"`
		State              string `json:"state"`
		AttemptCount       int    `json:"attempt_count"`
		EscalationReason   string `json:"escalation_reason"`
		LastFailureSummary string `json:"last_failure_summary"`
	}
	for _, status := range ledger.Statuses {
		switch status.Scenario {
		case "stale_branch":
			staleStatus = status
		case "partial_plugin_startup":
			partialStatus = status
		}
	}
	if err := verifyHarnessChecks("stale branch ledger status", fmt.Sprintf("%#v", staleStatus),
		staleStatus.Attempted,
		staleStatus.State == "exhausted",
		staleStatus.AttemptCount == 1,
		strings.Contains(staleStatus.EscalationReason, "max recovery attempts"),
	); err != nil {
		return localScenarioResult{}, err
	}
	if err := verifyHarnessChecks("partial plugin ledger status", fmt.Sprintf("%#v", partialStatus),
		partialStatus.Attempted,
		partialStatus.State == "failed",
		partialStatus.AttemptCount == 1,
		partialStatus.LastFailureSummary == "mcp still unhealthy",
	); err != nil {
		return localScenarioResult{}, err
	}

	report := map[string]any{
		"kind": "recovery_lifecycle",
		"recovery": map[string]any{
			"recipe":               recipeReport.Recipe.ID,
			"steps":                len(recipeReport.Recipe.Steps),
			"first_result":         firstAttempt.Result.Kind,
			"second_result":        secondAttempt.Result.Kind,
			"partial_result":       partialAttempt.Result.Kind,
			"stale_state":          staleStatus.State,
			"partial_state":        partialStatus.State,
			"ledger_entries":       len(ledger.Entries),
			"escalation_recorded":  staleStatus.EscalationReason != "",
			"failure_summary_seen": partialStatus.LastFailureSummary,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "recovery lifecycle harness ok",
		RequestCount: 6,
		MessageCount: 1,
		ToolCalls:    6,
		ToolUses: []string{
			"recovery_recipe",
			"recovery_status",
			"recovery_attempt",
			"recovery_attempt",
			"recovery_attempt",
			"recovery_status",
		},
	}, nil
}

func agentMarkdownDefinitionScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	configPath := filepath.Join(workspace, "config.json")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"config_home":%q}`, configHome)), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	agentsDir := filepath.Join(workspace, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(`---
description: Claude-style review agent
model: openai/gpt-4.1-mini
tools:
  - read_file
  - grep
---
# Reviewer
Review changes and report verification.
`), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	listOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "agents", "list")
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"kind": "agents"`, `"accepted_formats"`, `".md"`, `"name": "reviewer"`, `"source": "claude"`, `"format": "markdown"`} {
		if !strings.Contains(listOut, expected) {
			return localScenarioResult{}, fmt.Errorf("agent markdown list output missing %s", expected)
		}
	}
	showOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "agents", "show", "reviewer")
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"action": "show"`, `"model": "openai/gpt-4.1-mini"`, `"tools": [`, `"read_file"`, "Review changes and report verification"} {
		if !strings.Contains(showOut, expected) {
			return localScenarioResult{}, fmt.Errorf("agent markdown show output missing %s", expected)
		}
	}
	helpOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "agents", "help")
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"action": "help"`, `".claude/agents"`, `".markdown"`} {
		if !strings.Contains(helpOut, expected) {
			return localScenarioResult{}, fmt.Errorf("agent markdown help output missing %s", expected)
		}
	}
	return localScenarioResult{
		Output:       strings.Join([]string{listOut, showOut, helpOut}, "\n"),
		FinalMessage: "agent markdown definition harness ok",
	}, nil
}

func nudgeAckDedupeScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	call := func(input string) (map[string]any, error) {
		var report map[string]any
		_, err := decodeHarnessOutput(&report, func() (string, error) { return registry.Execute(ctx, "NudgeTool", json.RawMessage(input), nil) })
		if err != nil {
			return nil, err
		}
		return report, nil
	}

	first, err := call(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","prompt":"check status","delivered_at":"2026-07-07T12:00:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	retry, err := call(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","prompt":"check status","delivered_at":"2026-07-07T12:01:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	ack, err := call(`{"action":"ack","nudge_id":"dogfood","cycle_id":"cycle-1","response_id":"response-1","delivered_at":"2026-07-07T12:02:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	stale, err := call(`{"action":"observe","nudge_id":"dogfood","cycle_id":"cycle-1","delivered_at":"2026-07-07T12:03:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	list, err := call(`{"action":"list"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	if first["state"] != "new_nudge" || retry["state"] != "retry_nudge" || ack["acknowledged"] != true || stale["state"] != "stale_duplicate" || stale["already_acknowledged"] != true {
		return localScenarioResult{}, fmt.Errorf("unexpected nudge states: first=%v retry=%v ack=%v stale=%v", first, retry, ack, stale)
	}
	report := map[string]any{
		"kind":                 "nudge_ack_dedupe",
		"first_state":          first["state"],
		"retry_state":          retry["state"],
		"acknowledged":         ack["acknowledged"],
		"stale_state":          stale["state"],
		"already_acknowledged": stale["already_acknowledged"],
		"delivery_count":       stale["delivery_count"],
		"record_count":         list["count"],
		"fingerprint":          first["fingerprint"],
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "nudge ack dedupe harness ok",
		RequestCount: 5,
		MessageCount: 1,
		ToolCalls:    5,
		ToolUses:     []string{"nudge", "nudge", "nudge", "nudge", "nudge"},
	}, nil
}

func provisionalStatusEscalationScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	call := func(input string) (map[string]any, error) {
		var report map[string]any
		_, err := decodeHarnessOutput(&report, func() (string, error) {
			return registry.Execute(ctx, "ProvisionalStatusTool", json.RawMessage(input), nil)
		})
		if err != nil {
			return nil, err
		}
		return report, nil
	}

	first, err := call(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"working on it","observed_at":"2026-07-07T17:00:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	fresh, err := call(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"please wait","observed_at":"2026-07-07T17:01:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	stale, err := call(`{"action":"observe","channel":"dogfood","owner":"worker-1","progress_state":"implementing","message":"working on it","observed_at":"2026-07-07T17:03:00Z","window_seconds":300,"timeout_seconds":120,"timeout_policy":"dogfood-fast-ttl"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	escalation, _ := stale["escalation"].(map[string]any)
	policy, _ := escalation["policy"].(map[string]any)
	if first["decision"] != "new_provisional" || fresh["decision"] != "suppressed_duplicate" || fresh["exposed"] != false || stale["decision"] != "stale_provisional" || stale["stale"] != true || escalation["kind"] != "provisional_status_stale" || escalation["signal"] != "blocker" || policy["id"] != "dogfood-fast-ttl" {
		return localScenarioResult{}, fmt.Errorf("unexpected provisional status escalation: first=%#v fresh=%#v stale=%#v", first, fresh, stale)
	}
	report := map[string]any{
		"kind":              "provisional_status_escalation",
		"first_decision":    first["decision"],
		"fresh_decision":    fresh["decision"],
		"fresh_exposed":     fresh["exposed"],
		"stale_decision":    stale["decision"],
		"stale_signal":      escalation["signal"],
		"stale_kind":        escalation["kind"],
		"stale_for_seconds": escalation["stale_for_seconds"],
		"policy_id":         policy["id"],
		"deadline_at":       policy["deadline_at"],
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "provisional status escalation harness ok",
		RequestCount: 3,
		MessageCount: 1,
		ToolCalls:    3,
		ToolUses:     []string{"provisional_status", "provisional_status", "provisional_status"},
	}, nil
}

func roadmapPinpointLifecycleScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	call := func(input string) (map[string]any, error) {
		var report map[string]any
		_, err := decodeHarnessOutput(&report, func() (string, error) {
			return registry.Execute(ctx, "RoadmapPinpointTool", json.RawMessage(input), nil)
		})
		if err != nil {
			return nil, err
		}
		return report, nil
	}

	filed, err := call(`{"action":"file","title":"stable pinpoint ids","description":"dogfood reports need ids","priority":"p1","severity":"high","impact":"observability_debt","priority_reason":{"blast_radius":"dogfood reports","reproducibility":"always","automation_breakage":"prevents queue ranking","merge_risk":"low","rationale":"fresh structured reporting gap"},"evidence":[{"role":"symptom","type":"session","reference":"session-dogfood-1","preview":"pinpoint was only prose in the report"}],"now":"2026-07-07T13:00:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	itemID, _ := filed["item_id"].(string)
	if itemID == "" || filed["action"] != "new_roadmap_filing" {
		return localScenarioResult{}, fmt.Errorf("unexpected roadmap filing: %#v", filed)
	}
	updated, err := call(`{"action":"update","id":"` + itemID + `","title":"stable pinpoint ids after edit","state":"in_progress","report_id":"report-1","priority":"p0","severity":"critical","impact":"operator_friction","priority_reason":{"blast_radius":"implementation queue","reproducibility":"always","automation_breakage":"blocks queue ranking","merge_risk":"medium"},"handoff":{"objective":"Implement stable pinpoint ids","suspected_scope":["internal/roadmap","internal/tools"],"suggested_verification":["go test ./internal/roadmap ./internal/tools"],"readiness":"implementation_ready"},"implementation":[{"lane_id":"lane-roadmap-1","task_id":"task-roadmap-1","worktree_id":"wt-roadmap-1","worktree_path":"worktrees/wt-roadmap-1","pr_url":"https://github.com/Rememorio/codog/pull/1","pr_number":1,"status":"running"}],"execution_results":[{"lane_id":"lane-roadmap-1","status":"running","summary":"implementation started"}],"evidence":[{"role":"verification","type":"commit","reference":"commit-1","preview":"roadmap pinpoint lifecycle test covers the update"}],"now":"2026-07-07T14:00:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	handoff, err := call(`{"action":"handoff","id":"` + itemID + `"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	closed, err := call(`{"action":"update","id":"` + itemID + `","execution_results":[{"lane_id":"lane-roadmap-1","status":"passed","summary":"verification passed"}],"now":"2026-07-07T15:00:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	list, err := call(`{"action":"list"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	updatedItem, _ := updated["item"].(map[string]any)
	evidence, _ := updatedItem["evidence"].([]any)
	handoffPacket, _ := handoff["handoff"].(map[string]any)
	implementation, _ := handoff["implementation"].([]any)
	if updated["item_id"] != itemID || updated["state"] != "in_progress" || closed["item_id"] != itemID || closed["state"] != "done" || len(evidence) != 2 || handoffPacket["readiness"] != "implementation_ready" || len(implementation) != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected roadmap lifecycle: filed=%#v updated=%#v handoff=%#v closed=%#v", filed, updated, handoff, closed)
	}
	report := map[string]any{
		"kind":           "roadmap_pinpoint_lifecycle",
		"item_id":        itemID,
		"first_action":   filed["action"],
		"update_action":  updated["action"],
		"update_state":   updated["state"],
		"closed_state":   closed["state"],
		"record_count":   list["count"],
		"evidence_count": len(evidence),
		"priority":       updatedItem["priority"],
		"severity":       updatedItem["severity"],
		"impact":         updatedItem["impact"],
		"readiness":      handoffPacket["readiness"],
		"implementation": len(implementation),
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "roadmap pinpoint lifecycle harness ok",
		RequestCount: 5,
		MessageCount: 1,
		ToolCalls:    5,
		ToolUses:     []string{"roadmap_pinpoint", "roadmap_pinpoint", "roadmap_pinpoint", "roadmap_pinpoint", "roadmap_pinpoint"},
	}, nil
}

func reportAtomicUpdateScenarioRunLocal(context.Context, string) (localScenarioResult, error) {
	complete, err := reportschema.Canonicalize(reportschema.CanonicalReport{
		Identity:    reportschema.Identity{ReportID: "report-atomic-harness"},
		GeneratedAt: "2026-07-07T16:00:00Z",
		Producer:    "codog mock-parity",
		AtomicUpdate: &reportschema.AtomicUpdate{
			ActiveSessions: []string{"session-b", "session-a", "session-a"},
			ExactPinpoint:  "4.13 report atomicity",
			ConcreteDelta:  "canonical message parts added",
			Blocker:        "none",
			MessageParts: []reportschema.MessagePart{
				{PartIndex: 2, PartCount: 3, Content: "blocker=none"},
				{PartIndex: 0, PartCount: 3, Content: "pinpoint=4.13;"},
				{PartIndex: 1, PartCount: 3, Content: "delta=canonical;"},
			},
		},
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	message, messageComplete, err := reportschema.ReconstructAtomicMessage(*complete.AtomicUpdate)
	if err != nil {
		return localScenarioResult{}, err
	}
	if !messageComplete || message != "pinpoint=4.13;delta=canonical;blocker=none" {
		return localScenarioResult{}, fmt.Errorf("unexpected reconstructed atomic message: complete=%t message=%q", messageComplete, message)
	}
	partial, err := reportschema.Canonicalize(reportschema.CanonicalReport{
		Identity:    reportschema.Identity{ReportID: "report-atomic-partial"},
		GeneratedAt: "2026-07-07T16:01:00Z",
		Producer:    "codog mock-parity",
		AtomicUpdate: &reportschema.AtomicUpdate{
			ExactPinpoint: "4.13 report atomicity",
			ConcreteDelta: "first and last message part arrived",
			Blocker:       "middle fragment missing",
			MessageParts: []reportschema.MessagePart{
				{PartIndex: 2, PartCount: 3, Content: "tail"},
				{PartIndex: 0, PartCount: 3, Content: "head"},
			},
		},
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	partialMessage, partialComplete, err := reportschema.ReconstructAtomicMessage(*partial.AtomicUpdate)
	if err != nil {
		return localScenarioResult{}, err
	}
	if partialComplete || partial.AtomicUpdate.ExactPinpoint != complete.AtomicUpdate.ExactPinpoint || partialMessage != "headtail" {
		return localScenarioResult{}, fmt.Errorf("unexpected partial atomic report: complete=%t report=%#v message=%q", partialComplete, partial.AtomicUpdate, partialMessage)
	}
	report := map[string]any{
		"kind":             "report_atomic_update",
		"report_id":        complete.Identity.ReportID,
		"message_complete": complete.AtomicUpdate.MessageComplete,
		"part_indexes":     []int{complete.AtomicUpdate.MessageParts[0].PartIndex, complete.AtomicUpdate.MessageParts[1].PartIndex, complete.AtomicUpdate.MessageParts[2].PartIndex},
		"reconstructed":    message,
		"partial_complete": partial.AtomicUpdate.MessageComplete,
		"partial_message":  partialMessage,
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "report atomic update harness ok",
		RequestCount: 2,
		MessageCount: 2,
		ToolCalls:    0,
	}, nil
}

func reportBackpressureScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
	call := func(tool string, input string) (map[string]any, error) {
		var report map[string]any
		_, err := decodeHarnessOutput(&report, func() (string, error) { return registry.Execute(ctx, tool, json.RawMessage(input), nil) })
		if err != nil {
			return nil, err
		}
		return report, nil
	}

	filed, err := call("RoadmapPinpointTool", `{"action":"file","title":"backpressure report","description":"collapse unchanged backlog","priority":"p1","severity":"high","impact":"observability_debt","evidence":[{"role":"root_cause_hint","type":"log","reference":"log:dogfood","preview":"repeated backlog likely comes from missing cursor collapse"}],"now":"2026-07-07T16:00:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	itemID, _ := filed["item_id"].(string)
	first, err := call("ReportBackpressureTool", `{"action":"generate","channel":"dogfood","now":"2026-07-07T16:01:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	firstSchemaVersion, _ := first["schema_version"].(string)
	firstCompatibility, _ := first["schema_compatibility"].(map[string]any)
	compatibilityPolicy, _ := firstCompatibility["policy"].(string)
	stableCore, _ := firstCompatibility["minimal_stable_core"].([]any)
	second, err := call("ReportBackpressureTool", `{"action":"generate","channel":"dogfood","trigger_id":"nudge-cycle-1","checked_surfaces":["roadmap","sessions"],"checked_window":"2026-07-07T16:01:00Z/2026-07-07T16:02:00Z","freshness_ttl_seconds":30,"now":"2026-07-07T16:02:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	firstProjection, err := call("ReportBackpressureTool", `{"action":"generate","channel":"dogfood","consumer":"clawhip","schema_versions":["codog.reporting.report.v1"],"projection_view":"delta_brief","projection_verbosity":"brief","now":"2026-07-07T16:02:30Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	_, err = call("RoadmapPinpointTool", `{"action":"update","id":"`+itemID+`","priority":"p0","severity":"critical","evidence":[{"role":"verification","type":"test","reference":"go-test","preview":"cursor collapse test passes"}],"now":"2026-07-07T16:03:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	third, err := call("ReportBackpressureTool", `{"action":"generate","channel":"dogfood","now":"2026-07-07T16:04:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	snapshotID, _ := first["snapshot_id"].(string)
	snapshot, err := call("ReportBackpressureTool", `{"action":"snapshot","snapshot_id":"`+snapshotID+`"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	projected, err := call("ReportBackpressureTool", `{"action":"generate","channel":"dogfood","consumer":"clawhip","schema_versions":["codog.reporting.report.v1"],"projection_view":"delta_brief","projection_verbosity":"brief","now":"2026-07-07T16:05:00Z"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	schema, err := call("ReportSchemaTool", `{"action":"registry","report":"report_backpressure","schema_version":"codog.reporting.report.v1","field_family":"field_deltas"}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	firstNew, _ := first["new_items"].([]any)
	thirdChanged, _ := third["changed_items"].([]any)
	firstClaims, _ := first["claims"].([]any)
	thirdClaims, _ := third["claims"].([]any)
	secondUnchanged, _ := second["unchanged_count"].(float64)
	secondCollapsed, _ := second["collapsed"].(bool)
	secondNoChange, _ := second["no_change"].(bool)
	secondOutcome, _ := second["outcome"].(string)
	secondTrigger, _ := second["trigger_id"].(string)
	lastMeaningful, _ := second["last_meaningful_report_id"].(string)
	freshnessCounts, _ := second["freshness_counts"].(map[string]any)
	staleCount, _ := freshnessCounts["stale"].(float64)
	secondNegative, _ := second["negative_evidence"].([]any)
	negativeStatus := negativeEvidenceField(secondNegative, "no_new_delta", "status")
	negativeWindow := negativeEvidenceField(secondNegative, "no_new_delta", "window")
	secondFieldDeltas, _ := second["field_deltas"].([]any)
	secondDeltaState := fieldDeltaState(secondFieldDeltas, "report.delta")
	firstRootKind := claimKind(firstClaims, "root-cause")
	thirdRootKind := claimKind(thirdClaims, "root-cause")
	thirdPromotedFrom := claimPromotedFrom(thirdClaims, "root-cause")
	invalidates, _ := third["invalidates_negative_evidence"].([]any)
	thirdFieldDeltas, _ := third["field_deltas"].([]any)
	thirdPriorityState := fieldDeltaStateSuffix(thirdFieldDeltas, ".priority")
	snapshotBody, _ := snapshot["snapshot"].(map[string]any)
	provenance, _ := projected["provenance"].(map[string]any)
	firstProjectionID, _ := firstProjection["projection_id"].(string)
	projectedDowngraded, _ := provenance["downgraded"].(bool)
	projectedSourceHash, _ := provenance["source_content_hash"].(string)
	projectedRenderingChanged, _ := provenance["rendering_changed"].(bool)
	projectedSourceChanged, _ := provenance["source_changed"].(bool)
	projectedLatest, _ := provenance["latest_compatible"].(bool)
	projectedStale, _ := provenance["stale_cached"].(bool)
	projectedSupersedes, _ := provenance["supersedes_projection_id"].(string)
	omittedFamilies, _ := provenance["omitted_field_families"].([]any)
	projectedPayload, _ := projected["payload"].(map[string]any)
	projectedCanonical, _ := projected["canonical_report"].(map[string]any)
	canonicalIdentity, _ := projectedCanonical["identity"].(map[string]any)
	canonicalHash, _ := canonicalIdentity["content_hash"].(string)
	projectedView, _ := projected["view"].(string)
	projectedVerbosity, _ := projected["verbosity"].(string)
	projectedSummary, _ := projectedPayload["summary"].(map[string]any)
	projectedTopItems, _ := projectedPayload["top_items"].([]any)
	schemaRegistry, _ := schema["registry"].(map[string]any)
	schemaFields, _ := schemaRegistry["fields"].([]any)
	if err := verifyHarnessChecks("backpressure report", fmt.Sprintf("checks=%#v", map[string]any{
		"item_id": itemID, "first_projection_id": firstProjectionID, "projected_downgraded": projectedDowngraded,
		"rendering_changed": projectedRenderingChanged, "source_changed": projectedSourceChanged, "latest": projectedLatest,
		"stale": projectedStale, "supersedes": projectedSupersedes, "source_hash": projectedSourceHash,
		"canonical_hash": canonicalHash, "projected_view": projectedView, "projected_verbosity": projectedVerbosity,
		"omitted": len(omittedFamilies), "has_field_deltas": projectedPayload["field_deltas"] != nil,
		"has_new_items": projectedPayload["new_items"] != nil, "summary_outcome": projectedSummary["outcome"],
		"top_items": len(projectedTopItems), "schema_fields": len(schemaFields), "third_changed": len(thirdChanged),
		"snapshot_schema": snapshotBody["schema_version"],
	}),
		itemID != "",
		firstSchemaVersion == "codog.reporting.report.v1",
		compatibilityPolicy == "codog.reporting.compatibility.v1",
		len(stableCore) > 0,
		firstProjectionID != "",
		len(firstNew) == 1,
		secondUnchanged == 1,
		secondCollapsed,
		secondNoChange,
		secondOutcome == "no_change",
		secondTrigger == "nudge-cycle-1",
		lastMeaningful != "",
		staleCount == 1,
		len(secondNegative) == 2,
		negativeStatus == "not_observed_in_checked_scope",
		negativeWindow == "2026-07-07T16:01:00Z/2026-07-07T16:02:00Z",
		len(secondFieldDeltas) > 0,
		secondDeltaState == "cleared",
		len(invalidates) == 2,
		thirdPriorityState == "changed",
		projectedDowngraded,
		projectedRenderingChanged,
		projectedSourceChanged,
		projectedLatest,
		!projectedStale,
		projectedSupersedes == firstProjectionID,
		projectedSourceHash != "",
		projectedSourceHash == canonicalHash,
		projectedView == "delta_brief",
		projectedVerbosity == "brief",
		len(omittedFamilies) > 0,
		projectedPayload["field_deltas"] != nil,
		projectedPayload["new_items"] == nil,
		projectedSummary["outcome"] != nil,
		len(projectedTopItems) == 0,
		projectedCanonical["schema_compatibility"] != nil,
		len(schemaFields) == 2,
		firstRootKind == "hypothesis",
		thirdRootKind == "observed_fact",
		thirdPromotedFrom == "hypothesis",
		len(thirdChanged) == 1,
		snapshotBody["schema_version"] == "codog.reporting.snapshot.v1",
	); err != nil {
		return localScenarioResult{}, err
	}
	output := map[string]any{
		"kind":             "report_backpressure_roundtrip",
		"schema_version":   firstSchemaVersion,
		"compatibility":    compatibilityPolicy,
		"stable_core":      len(stableCore),
		"item_id":          itemID,
		"first_new":        len(firstNew),
		"second_outcome":   secondOutcome,
		"second_no_change": secondNoChange,
		"second_trigger":   secondTrigger,
		"second_unchanged": int(secondUnchanged),
		"second_collapsed": secondCollapsed,
		"last_meaningful":  lastMeaningful,
		"stale_count":      int(staleCount),
		"negative_count":   len(secondNegative),
		"negative_status":  negativeStatus,
		"negative_window":  negativeWindow,
		"field_delta":      secondDeltaState,
		"invalidates":      len(invalidates),
		"priority_delta":   thirdPriorityState,
		"projected":        projectedDowngraded,
		"projected_view":   projectedView,
		"projected_level":  projectedVerbosity,
		"projected_omits":  len(omittedFamilies),
		"source_anchor":    projectedSourceHash,
		"source_changed":   projectedSourceChanged,
		"latest":           projectedLatest,
		"supersedes":       projectedSupersedes,
		"schema_fields":    len(schemaFields),
		"first_root_claim": firstRootKind,
		"third_root_claim": thirdRootKind,
		"promoted_from":    thirdPromotedFrom,
		"third_changed":    len(thirdChanged),
		"snapshot_id":      snapshotID,
	}
	data, err := json.Marshal(output)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "report backpressure harness ok",
		RequestCount: 9,
		MessageCount: 1,
		ToolCalls:    9,
		ToolUses:     []string{"roadmap_pinpoint", "report_backpressure", "report_backpressure", "report_backpressure", "roadmap_pinpoint", "report_backpressure", "report_backpressure", "report_backpressure", "report_schema"},
	}, nil
}

func backgroundAgentRunScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	taskStore := background.NewStore(configHome)
	runStore := agentruns.NewStore(configHome)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	sessionID := "session-bg"

	task, err := taskStore.RunWithOptions("printf codog-bg-ready; sleep 5", workspace, background.RunOptions{
		Kind:        "agent",
		AgentType:   "reviewer",
		SessionID:   sessionID,
		Prompt:      "review branch",
		Description: "Parity reviewer",
		ScopeBinding: background.ScopeBinding{
			Owner:         "reviewer",
			WorkflowScope: "claw-code-dogfood",
			WatcherAction: "act",
		},
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	defer func() { _, _ = taskStore.Stop(task.ID) }()

	if _, err := waitForBackgroundLogs(ctx, taskStore, task.ID, "codog-bg-ready", 2*time.Second); err != nil {
		return localScenarioResult{}, err
	}
	updatedTask, err := taskStore.UpdateHeartbeat(task.ID, background.LaneHeartbeat{
		ObservedAt:     now.Add(-5 * time.Second),
		TransportAlive: true,
		Status:         "working",
		Provenance: background.EventProvenance{
			SourceKind:  "health",
			Environment: "mock-parity",
			Channel:     "harness",
			Emitter:     "codog-harness",
			Confidence:  "high",
		},
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if updatedTask.Heartbeat == nil {
		return localScenarioResult{}, fmt.Errorf("background heartbeat was not persisted")
	}
	if err := verifyHarnessChecks("background heartbeat", fmt.Sprintf("%#v", updatedTask.Heartbeat),
		updatedTask.Heartbeat.Status == "working",
	); err != nil {
		return localScenarioResult{}, err
	}

	run, err := runStore.Save(agentruns.Run{
		ID:        "run-" + task.ID,
		Agent:     "reviewer",
		Prompt:    "review branch",
		Workspace: workspace,
		SessionID: sessionID,
		TaskID:    task.ID,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	status := agentruns.StatusForTaskAt(taskStore, run, now, 30*time.Second)
	if err := verifyHarnessChecks("agent run status", fmt.Sprintf("%#v", status),
		status.CurrentStatus == "running",
		status.Freshness == background.LaneFreshnessHealthy,
		status.Health.State == "healthy",
		!status.Lifecycle.Terminal,
		status.Lifecycle.Reason == "active_status",
		status.Provenance.SourceKind == "healthcheck",
		status.Provenance.Emitter == "codog-harness",
		status.ScopeBinding.Owner == "reviewer",
		status.ScopeBinding.WorkflowScope == "claw-code-dogfood",
		status.ScopeBinding.Actionable,
	); err != nil {
		return localScenarioResult{}, err
	}
	agentBoard := agentruns.BuildBoard(taskStore, []agentruns.Run{run}, now, 30*time.Second)
	if len(agentBoard.Active) != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected agent lane board: %#v", agentBoard)
	}
	if err := verifyHarnessChecks("agent lane board", fmt.Sprintf("%#v", agentBoard),
		agentBoard.Active[0].Run.ID == run.ID,
		agentBoard.Active[0].Freshness == background.LaneFreshnessHealthy,
	); err != nil {
		return localScenarioResult{}, err
	}
	taskBoard, err := taskStore.LaneBoardAt(now, 30*time.Second)
	if err != nil {
		return localScenarioResult{}, err
	}
	if len(taskBoard.Active) != 1 {
		return localScenarioResult{}, fmt.Errorf("unexpected background lane board: %#v", taskBoard)
	}
	if err := verifyHarnessChecks("background lane board", fmt.Sprintf("%#v", taskBoard),
		taskBoard.Active[0].TaskID == task.ID,
		taskBoard.Active[0].Freshness == background.LaneFreshnessHealthy,
	); err != nil {
		return localScenarioResult{}, err
	}

	events, err := watchBackgroundTaskEvents(ctx, taskStore, task.ID)
	if err != nil {
		return localScenarioResult{}, err
	}

	stopped, err := taskStore.Stop(task.ID)
	if err != nil {
		return localScenarioResult{}, err
	}
	if stopped.Status != "stopped" {
		return localScenarioResult{}, fmt.Errorf("expected stopped task, got %q", stopped.Status)
	}
	if _, err := taskStore.UpdateHeartbeat(task.ID, background.LaneHeartbeat{
		ObservedAt:     now,
		TransportAlive: false,
		Status:         "stopped",
		Provenance: background.EventProvenance{
			SourceKind:  "transport",
			Environment: "mock-parity",
			Channel:     "harness",
			Emitter:     "codog-harness",
			Confidence:  "high",
		},
	}); err != nil {
		return localScenarioResult{}, err
	}
	stoppedStatus := agentruns.StatusForTaskAt(taskStore, run, now, 30*time.Second)
	if err := verifyHarnessChecks("stopped lifecycle", fmt.Sprintf("%#v", stoppedStatus),
		stoppedStatus.Lifecycle.Terminal,
		!stoppedStatus.Lifecycle.TerminalStateUnknown,
		stoppedStatus.Health.State == "finished",
	); err != nil {
		return localScenarioResult{}, err
	}
	if stoppedStatus.TerminalOutcome == nil {
		return localScenarioResult{}, fmt.Errorf("missing terminal dedupe outcome: %#v", stoppedStatus.TerminalOutcome)
	}
	if err := verifyHarnessChecks("terminal dedupe outcome", fmt.Sprintf("%#v", stoppedStatus.TerminalOutcome),
		stoppedStatus.TerminalOutcome.DuplicateCount >= 1,
	); err != nil {
		return localScenarioResult{}, err
	}
	restarted, err := taskStore.Restart(task.ID, workspace)
	if err != nil {
		return localScenarioResult{}, err
	}
	defer func() { _, _ = taskStore.Stop(restarted.ID) }()
	if err := verifyHarnessChecks("restarted task", fmt.Sprintf("%#v", restarted),
		restarted.RestartedFrom == task.ID,
		restarted.Kind == "agent",
		restarted.AgentType == "reviewer",
		restarted.SessionID == sessionID,
	); err != nil {
		return localScenarioResult{}, err
	}
	source, err := taskStore.Get(task.ID)
	if err != nil {
		return localScenarioResult{}, err
	}
	if source.RestartedBy != restarted.ID {
		return localScenarioResult{}, fmt.Errorf("source task missing restarted_by link")
	}

	supervisor, err := runBackgroundSupervisorPhase(ctx, taskStore, workspace, sessionID, now)
	if err != nil {
		return localScenarioResult{}, err
	}

	report := map[string]any{
		"kind": "background_agent_run",
		"task": map[string]any{
			"kind":                 task.Kind,
			"agent_type":           task.AgentType,
			"session_id":           task.SessionID,
			"watch_events":         []string{events[0].Type, events[1].Type},
			"stopped":              stopped.Status,
			"stopped_terminal":     stoppedStatus.Lifecycle.Terminal,
			"terminal_fingerprint": stoppedStatus.TerminalOutcome.Fingerprint,
			"terminal_duplicates":  stoppedStatus.TerminalOutcome.DuplicateCount,
			"restarted":            restarted.RestartedFrom == task.ID,
			"lane":                 "active",
			"lane_freshness":       string(taskBoard.Active[0].Freshness),
			"source_kind":          taskBoard.Active[0].Provenance.SourceKind,
			"workflow_scope":       taskBoard.Active[0].ScopeBinding.WorkflowScope,
		},
		"agent_run": map[string]any{
			"agent":            run.Agent,
			"status":           status.CurrentStatus,
			"freshness":        string(status.Freshness),
			"health":           status.Health.State,
			"lifecycle_reason": status.Lifecycle.Reason,
			"emitter":          status.Provenance.Emitter,
			"owner":            status.ScopeBinding.Owner,
			"actionable":       status.ScopeBinding.Actionable,
			"active_lanes":     len(agentBoard.Active),
		},
		"supervisor": map[string]any{
			"failed_status":    supervisor.failedStatus,
			"failed_exit_code": supervisor.failedExitCode,
			"restarted":        supervisor.restarted,
			"restart_count":    supervisor.restartCount,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "background agent run harness ok",
		RequestCount: 7,
		MessageCount: 1,
	}, nil
}

type backgroundSupervisorSummary struct {
	failedStatus   string
	failedExitCode int
	restarted      int
	restartCount   int
}

func runBackgroundSupervisorPhase(ctx context.Context, store background.Store, workspace, sessionID string, now time.Time) (backgroundSupervisorSummary, error) {
	failing, err := store.RunWithOptions("printf codog-bg-fail; exit 7", workspace, background.RunOptions{
		Kind:      "agent",
		AgentType: "fixer",
		SessionID: sessionID,
		Prompt:    "fix failure",
		RestartPolicy: &background.RestartPolicy{
			Enabled:     true,
			Mode:        "on-failure",
			MaxAttempts: 1,
		},
	})
	if err != nil {
		return backgroundSupervisorSummary{}, err
	}
	failed, err := waitForBackgroundTask(ctx, store, failing.ID, 2*time.Second, func(task background.Task) bool {
		return task.Status == "failed" && task.ExitCode != nil && *task.ExitCode == 7
	})
	if err != nil {
		return backgroundSupervisorSummary{}, err
	}
	if failed.RestartPolicy == nil || !failed.RestartPolicy.Enabled {
		return backgroundSupervisorSummary{}, fmt.Errorf("failing task lost restart policy")
	}
	supervised, err := store.SuperviseOnce(now.Add(time.Minute))
	if err != nil {
		return backgroundSupervisorSummary{}, err
	}
	if len(supervised.Restarted) != 1 {
		return backgroundSupervisorSummary{}, fmt.Errorf("unexpected supervise result: %#v", supervised)
	}
	restarted := supervised.Restarted[0]
	if restarted.RestartedFrom != failing.ID || restarted.RestartCount != 1 {
		return backgroundSupervisorSummary{}, fmt.Errorf("unexpected supervise result: %#v", supervised)
	}
	defer func() { _, _ = store.Stop(restarted.ID) }()
	return backgroundSupervisorSummary{
		failedStatus:   failed.Status,
		failedExitCode: *failed.ExitCode,
		restarted:      len(supervised.Restarted),
		restartCount:   restarted.RestartCount,
	}, nil
}

func watchBackgroundTaskEvents(ctx context.Context, store background.Store, taskID string) ([]background.WatchEvent, error) {
	var events []background.WatchEvent
	watchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := store.Watch(watchCtx, taskID, background.WatchOptions{Interval: 20 * time.Millisecond, MaxEvents: 2}, func(event background.WatchEvent) error {
		events = append(events, event)
		return nil
	})
	cancel()
	if err != nil {
		return nil, err
	}
	if len(events) != 2 {
		return nil, fmt.Errorf("unexpected background watch events: %#v", events)
	}
	if err := verifyHarnessChecks("background watch events", fmt.Sprintf("%#v", events),
		events[0].Type == "status",
		events[1].Type == "log",
		strings.Contains(events[1].Data, "codog-bg-ready"),
	); err != nil {
		return nil, err
	}
	return events, nil
}

func sshPrintPlanScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	var report struct {
		Kind             string   `json:"kind"`
		Status           string   `json:"status"`
		Local            bool     `json:"local"`
		Print            bool     `json:"print"`
		PromptConfigured bool     `json:"prompt_configured"`
		Command          []string `json:"command"`
		RemoteShell      string   `json:"remote_shell"`
		Message          string   `json:"message"`
	}
	output, err := decodeHarnessOutput(&report, func() (string, error) {
		return runHarnessCodog(ctx, workspace,
			"--output-format", "json",
			"ssh",
			"--local",
			"localhost",
			workspace,
			"--print=ssh harness prompt",
			"--permission-mode", "read-only",
		)
	})
	if err != nil {
		return localScenarioResult{}, err
	}
	if report.Kind != "ssh" || report.Status != "planned" || !report.Local || !report.Print || !report.PromptConfigured {
		return localScenarioResult{}, fmt.Errorf("unexpected ssh print plan: %#v", report)
	}
	command := strings.Join(report.Command, " ")
	for _, expected := range []string{"--permission-mode", "read-only", "prompt", "ssh harness prompt"} {
		if !strings.Contains(command, expected) {
			return localScenarioResult{}, fmt.Errorf("ssh print command missing %q: %s", expected, command)
		}
	}
	if strings.Contains(command, " repl") {
		return localScenarioResult{}, fmt.Errorf("ssh print command unexpectedly uses repl: %s", command)
	}
	if strings.TrimSpace(report.RemoteShell) != "" {
		return localScenarioResult{}, fmt.Errorf("local ssh print plan should not include remote shell: %q", report.RemoteShell)
	}
	return localScenarioResult{
		Output:       output,
		FinalMessage: "ssh print plan harness ok",
		RequestCount: 1,
		MessageCount: 1,
	}, nil
}

func remoteAPIListenerScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	store := session.NewWorkspaceStore(configHome, workspace)
	server := httptest.NewServer(control.Server{
		Sessions:   store,
		ConfigHome: configHome,
		Workspace:  workspace,
		AuthToken:  "secret-token",
	}.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		return localScenarioResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return localScenarioResult{}, fmt.Errorf("health returned %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp, err = http.Get(server.URL + "/sessions")
	if err != nil {
		return localScenarioResult{}, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		_ = resp.Body.Close()
		return localScenarioResult{}, fmt.Errorf("unauthenticated sessions returned %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/sessions", nil)
	if err != nil {
		return localScenarioResult{}, err
	}
	req.Header.Set("authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return localScenarioResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return localScenarioResult{}, fmt.Errorf("authenticated sessions returned %d", resp.StatusCode)
	}
	var sessions []json.RawMessage
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return localScenarioResult{}, err
	}
	if err := json.Unmarshal(body, &sessions); err != nil {
		return localScenarioResult{}, err
	}

	message := "remote api listener harness ok"
	return localScenarioResult{
		Output:       fmt.Sprintf("%s %s", message, server.URL),
		FinalMessage: message,
		RequestCount: 3,
	}, nil
}

func remoteBridgeWorkspaceScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	if err := os.MkdirAll(filepath.Join(workspace, "internal"), 0o755); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Codog\n\nhello remote bridge\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	if err := os.WriteFile(filepath.Join(workspace, "internal", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		return localScenarioResult{}, err
	}
	store := session.NewWorkspaceStore(configHome, workspace)
	server := httptest.NewServer(control.Server{
		Sessions:    store,
		ConfigHome:  configHome,
		Workspace:   workspace,
		AuthToken:   "secret-token",
		EditorToken: "editor-token",
	}.Handler())
	defer server.Close()

	editorWorkspace := filepath.ToSlash(workspace)
	client := remoteBridgeHarnessClient{baseURL: server.URL}
	if err := client.runCases([]remoteBridgeHarnessCase{
		{http.MethodGet, "/workspace/info", "", http.StatusOK, []string{`"name":"`}},
		{http.MethodGet, "/workspace/files?pattern=*.md", "", http.StatusOK, []string{`"path":"README.md"`}},
		{http.MethodGet, "/workspace/search?query=remote&glob=*.md", "", http.StatusOK, []string{`"text":"hello remote bridge"`}},
		{http.MethodPost, "/file/write", `{"path":"notes.txt","content":"hello world"}`, http.StatusOK, []string{`"bytes":11`}},
		{http.MethodPost, "/file/edit", `{"path":"notes.txt","old_string":"world","new_string":"codog"}`, http.StatusOK, []string{`"replacements":1`}},
		{http.MethodGet, "/file/read?path=notes.txt", "", http.StatusOK, []string{`"content":"hello codog"`}},
		{http.MethodPost, "/file/diff", `{"path":"README.md","old_string":"hello remote bridge","new_string":"hello codog bridge"}`, http.StatusOK, []string{`-hello remote bridge`, `+hello codog bridge`}},
		{http.MethodGet, "/file/read?path=../secret.txt", "", http.StatusBadRequest, []string{`"error"`}},
		{http.MethodPost, "/sessions/session-bridge/messages", `{"role":"user","text":"hello remote session"}`, http.StatusOK, []string{`"role":"user"`}},
		{http.MethodGet, "/sessions/session-bridge/history?limit=1", "", http.StatusOK, []string{`"text":"hello remote session"`}},
		{http.MethodPost, "/editor/identify", `{"editor":"VS Code","workspace":"` + editorWorkspace + `","token":"wrong"}`, http.StatusBadRequest, []string{"token is invalid"}},
		{http.MethodPost, "/editor/identify", `{"editor":"VS Code","version":"1.0","workspace":"` + editorWorkspace + `","token":"editor-token"}`, http.StatusOK, []string{`"editor":"VS Code"`, `"trusted":true`}},
		{http.MethodPost, "/editor/open", `{"path":"internal/main.go"}`, http.StatusOK, []string{`"path":"internal/main.go"`}},
		{http.MethodPost, "/editor/selection", `{"start_line":1,"start_column":1,"end_line":1,"end_column":8}`, http.StatusOK, []string{`"text":"package"`}},
		{http.MethodGet, "/editor/state", "", http.StatusOK, []string{`"open_file":{"path":"internal/main.go"`, `"selection":{"path":"internal/main.go"`}},
	}); err != nil {
		return localScenarioResult{}, err
	}

	report := map[string]any{
		"kind": "remote_bridge_workspace",
		"workspace": map[string]any{
			"info":   true,
			"files":  true,
			"search": true,
		},
		"file": map[string]any{
			"write":         true,
			"edit":          true,
			"read":          true,
			"diff":          true,
			"path_rejected": true,
		},
		"session": map[string]any{
			"message_appended": true,
			"history_read":     true,
		},
		"editor": map[string]any{
			"token_rejected": true,
			"identified":     true,
			"opened":         true,
			"selection":      true,
			"state":          true,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "remote bridge workspace harness ok",
		RequestCount: 16,
		MessageCount: 1,
	}, nil
}

type remoteBridgeHarnessCase struct {
	method   string
	path     string
	payload  string
	status   int
	contains []string
}

type remoteBridgeHarnessClient struct {
	baseURL string
}

func (c remoteBridgeHarnessClient) runCases(cases []remoteBridgeHarnessCase) error {
	for _, testCase := range cases {
		if err := c.runCase(testCase); err != nil {
			return err
		}
	}
	return nil
}

func (c remoteBridgeHarnessClient) runCase(testCase remoteBridgeHarnessCase) error {
	status, body, err := c.request(testCase.method, testCase.path, testCase.payload)
	if err != nil {
		return err
	}
	if status != testCase.status {
		return fmt.Errorf("%s %s returned %d: %s", testCase.method, testCase.path, status, body)
	}
	for _, expected := range testCase.contains {
		if !strings.Contains(body, expected) {
			return fmt.Errorf("%s %s response missing %s: %s", testCase.method, testCase.path, expected, body)
		}
	}
	return nil
}

func (c remoteBridgeHarnessClient) request(method, path, payload string) (int, string, error) {
	var body io.Reader
	if payload != "" {
		body = strings.NewReader(payload)
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("authorization", "Bearer secret-token")
	if payload != "" {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(data), nil
}

func mcpLifecycleScenarioRunLocal(_ context.Context, workspace string) (localScenarioResult, error) {
	seenMethods := []string{}
	seenSessionHeader := false
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer lifecycle-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		seenMethods = append(seenMethods, method)
		id := req["id"]
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "mcp-harness-session")
			writeMCPHarnessResponse(w, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{"name": "mcp-harness", "version": "1.0.0"},
			})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") == "mcp-harness-session" {
				seenSessionHeader = true
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "mcp-harness-session" {
				http.Error(w, "missing session header", http.StatusBadRequest)
				return
			}
			seenSessionHeader = true
			writeMCPHarnessResponse(w, id, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo text from the MCP lifecycle harness.",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "mcp-harness-session" {
				http.Error(w, "missing session header", http.StatusBadRequest)
				return
			}
			seenSessionHeader = true
			writeMCPHarnessResponse(w, id, map[string]any{"content": []map[string]any{{
				"type": "text",
				"text": "mcp lifecycle harness ok",
			}}})
		default:
			writeMCPHarnessError(w, id, "unsupported method: "+method)
		}
	}))
	defer mcpServer.Close()

	configHome := filepath.Join(workspace, "config-home")
	controlServer := httptest.NewServer(control.Server{
		Sessions:   session.NewWorkspaceStore(configHome, workspace),
		ConfigHome: configHome,
		Workspace:  workspace,
		MCPServers: map[string]config.MCPServerConfig{
			"harness": {
				URL:     mcpServer.URL + "/mcp?token=redacted-in-output",
				Headers: map[string]string{"Authorization": "Bearer lifecycle-token"},
			},
		},
	}.Handler())
	defer controlServer.Close()

	outputs := []string{}
	listBody, err := getControlBody(controlServer.URL + "/mcp/list?inspect=false")
	if err != nil {
		return localScenarioResult{}, err
	}
	outputs = append(outputs, listBody)
	showBody, err := getControlBody(controlServer.URL + "/mcp/show?server=harness")
	if err != nil {
		return localScenarioResult{}, err
	}
	outputs = append(outputs, showBody)
	toolsBody, err := getControlBody(controlServer.URL + "/mcp/tools?server=harness")
	if err != nil {
		return localScenarioResult{}, err
	}
	outputs = append(outputs, toolsBody)
	callBody, err := postControlBody(controlServer.URL+"/mcp/call", `{"server":"harness","tool":"echo","arguments":{"text":"hi"}}`)
	if err != nil {
		return localScenarioResult{}, err
	}
	outputs = append(outputs, callBody)

	for _, expected := range []string{`"kind":"mcp_list"`, `"kind":"mcp_show"`, `"status":"ok"`, `"kind":"mcp_tools"`, `"name":"echo"`, `"kind":"mcp_call"`, "mcp lifecycle harness ok"} {
		if !strings.Contains(strings.Join(outputs, "\n"), expected) {
			return localScenarioResult{}, fmt.Errorf("MCP lifecycle output missing %s", expected)
		}
	}
	if strings.Contains(strings.Join(outputs, "\n"), "lifecycle-token") || strings.Contains(strings.Join(outputs, "\n"), "redacted-in-output") {
		return localScenarioResult{}, fmt.Errorf("MCP lifecycle output leaked transport secrets")
	}
	for _, expectedMethod := range []string{"initialize", "notifications/initialized", "tools/list", "tools/call"} {
		if !slices.Contains(seenMethods, expectedMethod) {
			return localScenarioResult{}, fmt.Errorf("MCP server did not receive %s; methods=%v", expectedMethod, seenMethods)
		}
	}
	if !seenSessionHeader {
		return localScenarioResult{}, fmt.Errorf("MCP lifecycle did not propagate session header")
	}

	return localScenarioResult{
		Output:       strings.Join(outputs, "\n"),
		FinalMessage: "mcp lifecycle harness ok",
		ToolCalls:    1,
		ToolUses:     []string{"mcp.echo"},
		RequestCount: len(outputs),
	}, nil
}

func mcpAuthOAuthRefreshScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	oldStorage, hadStorage := os.LookupEnv("CODOG_OAUTH_STORAGE")
	if err := os.Setenv("CODOG_OAUTH_STORAGE", "file"); err != nil {
		return localScenarioResult{}, err
	}
	defer func() {
		if hadStorage {
			_ = os.Setenv("CODOG_OAUTH_STORAGE", oldStorage)
		} else {
			_ = os.Unsetenv("CODOG_OAUTH_STORAGE")
		}
	}()

	authState := &oauthRefreshHarnessServer{}
	authServer := httptest.NewServer(authState)
	defer authServer.Close()

	if _, err := oauth.SaveProviderProfile(ctx, configHome, "work", authServer.URL, "client-harness", []string{"profile"}); err != nil {
		return localScenarioResult{}, err
	}
	now := time.Now().UTC()
	if _, err := oauth.SaveToken(configHome, oauth.Token{
		AccessToken:  "old-access-token-secret",
		RefreshToken: "old-refresh-token-secret",
		ExpiresAt:    now.Add(-1 * time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}); err != nil {
		return localScenarioResult{}, err
	}

	mcpServer := httptest.NewServer(mcpAuthHarnessServer{})
	defer mcpServer.Close()

	out, err := tools.MCPAuthTool{
		Servers: map[string]config.MCPServerConfig{
			"auth": {URL: mcpServer.URL + "/mcp"},
		},
		ConfigHome:   configHome,
		OAuthProfile: "work",
	}.Execute(ctx, json.RawMessage(`{"server":"auth","action":"refresh"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	if !authState.refreshSeen {
		return localScenarioResult{}, fmt.Errorf("OAuth refresh endpoint was not called")
	}
	for _, expected := range []string{`"server": "auth"`, `"status": "ok"`, `"tool_count": 1`, `"resource_count": 1`, `"oauth_profile": "work"`, `"profile_configured": true`, `"token_present": true`, `"ready": true`, `"refreshed": true`, `"command": "codog mcp tools auth"`} {
		if !strings.Contains(out, expected) {
			return localScenarioResult{}, fmt.Errorf("MCP auth refresh output missing %s", expected)
		}
	}
	for _, secret := range []string{"old-access-token-secret", "old-refresh-token-secret", "new-access-token-secret-1234", "new-refresh-token-secret-5678"} {
		if strings.Contains(out, secret) {
			return localScenarioResult{}, fmt.Errorf("MCP auth refresh output leaked token secret")
		}
	}
	loaded, err := oauth.LoadToken(configHome)
	if err != nil {
		return localScenarioResult{}, err
	}
	if loaded.AccessToken != "new-access-token-secret-1234" || loaded.RefreshToken != "new-refresh-token-secret-5678" {
		return localScenarioResult{}, fmt.Errorf("OAuth token was not refreshed")
	}

	return localScenarioResult{
		Output:       out,
		FinalMessage: "mcp auth oauth refresh harness ok",
		ToolCalls:    1,
		ToolUses:     []string{"mcp_auth"},
		RequestCount: 1,
	}, nil
}

type oauthRefreshHarnessServer struct {
	refreshSeen bool
}

// ServeHTTP serves OAuth discovery and refresh endpoints for the MCP auth scenario.
func (s *oauthRefreshHarnessServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		writeJSONBody(w, map[string]any{
			"authorization_endpoint": "https://auth.example/authorize",
			"token_endpoint":         "http://" + r.Host + "/token",
		})
	case "/token":
		s.serveToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *oauthRefreshHarnessServer) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Form.Get("grant_type") != "refresh_token" ||
		r.Form.Get("refresh_token") != "old-refresh-token-secret" ||
		r.Form.Get("client_id") != "client-harness" {
		http.Error(w, "unexpected refresh request", http.StatusBadRequest)
		return
	}
	s.refreshSeen = true
	writeJSONBody(w, map[string]any{
		"access_token":  "new-access-token-secret-1234",
		"refresh_token": "new-refresh-token-secret-5678",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

type mcpAuthHarnessServer struct{}

// ServeHTTP serves the MCP handshake used after the scenario refreshes its token.
func (mcpAuthHarnessServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	method, _ := request["method"].(string)
	id := request["id"]
	switch method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "mcp-auth-session")
		writeMCPHarnessResponse(w, id, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
			},
			"serverInfo": map[string]any{"name": "mcp-auth-harness", "version": "1.0.0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeMCPHarnessResponse(w, id, map[string]any{"tools": []map[string]any{{
			"name":        "auth_echo",
			"description": "MCP auth harness tool.",
			"inputSchema": map[string]any{"type": "object"},
		}}})
	case "resources/list":
		writeMCPHarnessResponse(w, id, map[string]any{"resources": []map[string]any{{"uri": "codog://auth", "name": "auth"}}})
	default:
		writeMCPHarnessError(w, id, "unsupported method: "+method)
	}
}

func mcpAuthRecoveryScenarioRunLocal(ctx context.Context, workspace string) (localScenarioResult, error) {
	configHome := filepath.Join(workspace, "config-home")
	tool := tools.MCPAuthTool{
		Servers:      map[string]config.MCPServerConfig{},
		ConfigHome:   configHome,
		OAuthProfile: "work",
	}
	missingOut, err := tool.Execute(ctx, json.RawMessage(`{"server":"missing"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"server": "missing"`, `"status": "unknown"`, `"error": "server is not configured"`, `"oauth_profile": "work"`, `"profile_configured": false`, `"command": "codog mcp show missing"`, `"command": "codog mcp auth missing"`, `"command": "codog oauth provider save work ISSUER_URL CLIENT_ID [SCOPE...]"`} {
		if !strings.Contains(missingOut, expected) {
			return localScenarioResult{}, fmt.Errorf("missing-server MCP auth output missing %s: %s", expected, missingOut)
		}
	}

	unauthorizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "secret-token") || r.Header.Get("Authorization") == "Bearer secret-header" {
			http.Error(w, "unauthorized token rejected", http.StatusUnauthorized)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer unauthorizedServer.Close()
	tool.Servers = map[string]config.MCPServerConfig{
		"repo": {
			URL:     unauthorizedServer.URL + "/mcp?token=secret-token",
			Headers: map[string]string{"Authorization": "Bearer secret-header"},
		},
	}
	unauthorizedOut, err := tool.Execute(ctx, json.RawMessage(`{"server":"repo"}`))
	if err != nil {
		return localScenarioResult{}, err
	}
	for _, expected := range []string{`"server": "repo"`, `"status": "error"`, `"oauth_profile": "work"`, `"command": "codog mcp show repo"`, `"command": "codog mcp auth repo"`, `"command": "codog oauth provider save work ISSUER_URL CLIENT_ID [SCOPE...]"`} {
		if !strings.Contains(unauthorizedOut, expected) {
			return localScenarioResult{}, fmt.Errorf("unauthorized MCP auth output missing %s: %s", expected, unauthorizedOut)
		}
	}
	for _, leaked := range []string{"secret-token", "secret-header"} {
		if strings.Contains(unauthorizedOut, leaked) || strings.Contains(missingOut, leaked) {
			return localScenarioResult{}, fmt.Errorf("MCP auth recovery output leaked secret %q", leaked)
		}
	}

	report := map[string]any{
		"kind": "mcp_auth_recovery",
		"missing": map[string]any{
			"status":             "unknown",
			"profile_configured": false,
			"actions":            []string{"inspect", "retry", "oauth_provider", "oauth_login"},
		},
		"unauthorized": map[string]any{
			"status":   "error",
			"actions":  []string{"inspect", "retry", "oauth_provider", "oauth_login"},
			"redacted": true,
		},
	}
	data, err := json.Marshal(report)
	if err != nil {
		return localScenarioResult{}, err
	}
	return localScenarioResult{
		Output:       string(data),
		FinalMessage: "mcp auth recovery harness ok",
		ToolCalls:    1,
		ToolUses:     []string{"mcp_auth"},
		RequestCount: 2,
		MessageCount: 1,
	}, nil
}
