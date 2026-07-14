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
		name: "recovery_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

			recipeOut, err := registry.Execute(ctx, "RecoveryRecipeTool", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(recipeOut), &recipeReport); err != nil {
				return localScenarioResult{}, err
			}
			if recipeReport.Kind != "recovery_recipe" || recipeReport.Recipe.ID != "stale_branch" || recipeReport.Recipe.MaxAttempts != 1 || len(recipeReport.Recipe.Steps) != 2 || recipeReport.Recipe.Steps[0].Kind != "merge_forward_branch" {
				return localScenarioResult{}, fmt.Errorf("unexpected recovery recipe output: %s", recipeOut)
			}

			statusOut, err := registry.Execute(ctx, "recovery_status", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
			if err != nil {
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
			if err := json.Unmarshal([]byte(statusOut), &initialStatus); err != nil {
				return localScenarioResult{}, err
			}
			if initialStatus.Kind != "recovery_status" || initialStatus.Status.Scenario != "stale_branch" || initialStatus.Status.Attempted || initialStatus.Status.AttemptsRemaining != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected initial recovery status output: %s", statusOut)
			}

			firstAttemptOut, err := registry.Execute(ctx, "recovery_attempt", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
			if err != nil {
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
			if err := json.Unmarshal([]byte(firstAttemptOut), &firstAttempt); err != nil {
				return localScenarioResult{}, err
			}
			if firstAttempt.Kind != "recovery_attempt" || firstAttempt.Result.Kind != "recovered" || firstAttempt.Result.StepsTaken != 2 || firstAttempt.Entry.State != "succeeded" || firstAttempt.Entry.AttemptCount != 1 || len(firstAttempt.Events) == 0 || firstAttempt.Events[len(firstAttempt.Events)-1].Type != "recovery.succeeded" {
				return localScenarioResult{}, fmt.Errorf("unexpected first recovery attempt output: %s", firstAttemptOut)
			}

			secondAttemptOut, err := registry.Execute(ctx, "RecoveryAttemptTool", json.RawMessage(`{"scenario":"stale_branch"}`), nil)
			if err != nil {
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
			if err := json.Unmarshal([]byte(secondAttemptOut), &secondAttempt); err != nil {
				return localScenarioResult{}, err
			}
			if secondAttempt.Result.Kind != "escalation_required" || secondAttempt.Entry.State != "exhausted" || !strings.Contains(secondAttempt.Entry.EscalationReason, "max recovery attempts") || len(secondAttempt.Events) == 0 || secondAttempt.Events[len(secondAttempt.Events)-1].Type != "recovery.escalated" {
				return localScenarioResult{}, fmt.Errorf("unexpected second recovery attempt output: %s", secondAttemptOut)
			}

			partialAttemptOut, err := registry.Execute(ctx, "recovery_attempt", json.RawMessage(`{
				"scenario": "partial_plugin_startup",
				"failure_summary": "mcp still unhealthy",
				"failed_step_index": 1
			}`), nil)
			if err != nil {
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
			if err := json.Unmarshal([]byte(partialAttemptOut), &partialAttempt); err != nil {
				return localScenarioResult{}, err
			}
			if partialAttempt.Result.Kind != "partial_recovery" || partialAttempt.Entry.State != "failed" || partialAttempt.Entry.LastFailureSummary != "mcp still unhealthy" || len(partialAttempt.Result.Recovered) != 1 || partialAttempt.Result.Recovered[0].Kind != "restart_plugin" || len(partialAttempt.Result.Remaining) != 1 || partialAttempt.Result.Remaining[0].Kind != "retry_mcp_handshake" || len(partialAttempt.Events) == 0 || partialAttempt.Events[len(partialAttempt.Events)-1].Type != "recovery.failed" {
				return localScenarioResult{}, fmt.Errorf("unexpected partial recovery attempt output: %s", partialAttemptOut)
			}

			ledgerOut, err := registry.Execute(ctx, "RecoveryStatusTool", json.RawMessage(`{}`), nil)
			if err != nil {
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
			if err := json.Unmarshal([]byte(ledgerOut), &ledger); err != nil {
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
			if !staleStatus.Attempted || staleStatus.State != "exhausted" || staleStatus.AttemptCount != 1 || !strings.Contains(staleStatus.EscalationReason, "max recovery attempts") {
				return localScenarioResult{}, fmt.Errorf("unexpected stale branch ledger status: %#v", staleStatus)
			}
			if !partialStatus.Attempted || partialStatus.State != "failed" || partialStatus.AttemptCount != 1 || partialStatus.LastFailureSummary != "mcp still unhealthy" {
				return localScenarioResult{}, fmt.Errorf("unexpected partial plugin ledger status: %#v", partialStatus)
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
		},
	}
}

func agentMarkdownDefinitionScenario() scenario {
	return scenario{
		name: "agent_markdown_definition_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
		},
	}
}

func nudgeAckDedupeScenario() scenario {
	return scenario{
		name: "nudge_ack_dedupe_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			call := func(input string) (map[string]any, error) {
				out, err := registry.Execute(ctx, "NudgeTool", json.RawMessage(input), nil)
				if err != nil {
					return nil, err
				}
				var report map[string]any
				if err := json.Unmarshal([]byte(out), &report); err != nil {
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
		},
	}
}

func provisionalStatusEscalationScenario() scenario {
	return scenario{
		name: "provisional_status_escalation_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			call := func(input string) (map[string]any, error) {
				out, err := registry.Execute(ctx, "ProvisionalStatusTool", json.RawMessage(input), nil)
				if err != nil {
					return nil, err
				}
				var report map[string]any
				if err := json.Unmarshal([]byte(out), &report); err != nil {
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
		},
	}
}

func roadmapPinpointLifecycleScenario() scenario {
	return scenario{
		name: "roadmap_pinpoint_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			call := func(input string) (map[string]any, error) {
				out, err := registry.Execute(ctx, "RoadmapPinpointTool", json.RawMessage(input), nil)
				if err != nil {
					return nil, err
				}
				var report map[string]any
				if err := json.Unmarshal([]byte(out), &report); err != nil {
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
		},
	}
}

func reportAtomicUpdateScenario() scenario {
	return scenario{
		name: "report_atomic_update_roundtrip",
		runLocal: func(context.Context, string) (localScenarioResult, error) {
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
		},
	}
}

func reportBackpressureScenario() scenario {
	return scenario{
		name: "report_backpressure_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			call := func(tool string, input string) (map[string]any, error) {
				out, err := registry.Execute(ctx, tool, json.RawMessage(input), nil)
				if err != nil {
					return nil, err
				}
				var report map[string]any
				if err := json.Unmarshal([]byte(out), &report); err != nil {
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
			if itemID == "" || firstSchemaVersion != "codog.reporting.report.v1" || compatibilityPolicy != "codog.reporting.compatibility.v1" || len(stableCore) == 0 || firstProjectionID == "" || len(firstNew) != 1 || secondUnchanged != 1 || !secondCollapsed || !secondNoChange || secondOutcome != "no_change" || secondTrigger != "nudge-cycle-1" || lastMeaningful == "" || staleCount != 1 || len(secondNegative) != 2 || negativeStatus != "not_observed_in_checked_scope" || negativeWindow != "2026-07-07T16:01:00Z/2026-07-07T16:02:00Z" || len(secondFieldDeltas) == 0 || secondDeltaState != "cleared" || len(invalidates) != 2 || thirdPriorityState != "changed" || !projectedDowngraded || !projectedRenderingChanged || !projectedSourceChanged || !projectedLatest || projectedStale || projectedSupersedes != firstProjectionID || projectedSourceHash == "" || projectedSourceHash != canonicalHash || projectedView != "delta_brief" || projectedVerbosity != "brief" || len(omittedFamilies) == 0 || projectedPayload["field_deltas"] == nil || projectedPayload["new_items"] != nil || projectedSummary["outcome"] == nil || len(projectedTopItems) != 0 || projectedCanonical["schema_compatibility"] == nil || len(schemaFields) != 2 || firstRootKind != "hypothesis" || thirdRootKind != "observed_fact" || thirdPromotedFrom != "hypothesis" || len(thirdChanged) != 1 || snapshotBody["schema_version"] != "codog.reporting.snapshot.v1" {
				return localScenarioResult{}, fmt.Errorf("unexpected backpressure report: checks=%#v", map[string]any{
					"item_id": itemID, "first_projection_id": firstProjectionID, "projected_downgraded": projectedDowngraded,
					"rendering_changed": projectedRenderingChanged, "source_changed": projectedSourceChanged, "latest": projectedLatest,
					"stale": projectedStale, "supersedes": projectedSupersedes, "source_hash": projectedSourceHash,
					"canonical_hash": canonicalHash, "projected_view": projectedView, "projected_verbosity": projectedVerbosity,
					"omitted": len(omittedFamilies), "has_field_deltas": projectedPayload["field_deltas"] != nil,
					"has_new_items": projectedPayload["new_items"] != nil, "summary_outcome": projectedSummary["outcome"],
					"top_items": len(projectedTopItems), "schema_fields": len(schemaFields), "third_changed": len(thirdChanged),
					"snapshot_schema": snapshotBody["schema_version"],
				})
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
		},
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
		name: "background_agent_run_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
			if updatedTask.Heartbeat == nil || updatedTask.Heartbeat.Status != "working" {
				return localScenarioResult{}, fmt.Errorf("background heartbeat was not persisted")
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
			if status.CurrentStatus != "running" || status.Freshness != background.LaneFreshnessHealthy || status.Health.State != "healthy" {
				return localScenarioResult{}, fmt.Errorf("unexpected agent run status: %#v", status)
			}
			if status.Lifecycle.Terminal || status.Lifecycle.Reason != "active_status" {
				return localScenarioResult{}, fmt.Errorf("unexpected active lifecycle: %#v", status.Lifecycle)
			}
			if status.Provenance.SourceKind != "healthcheck" || status.Provenance.Emitter != "codog-harness" {
				return localScenarioResult{}, fmt.Errorf("unexpected provenance: %#v", status.Provenance)
			}
			if status.ScopeBinding.Owner != "reviewer" || status.ScopeBinding.WorkflowScope != "claw-code-dogfood" || !status.ScopeBinding.Actionable {
				return localScenarioResult{}, fmt.Errorf("unexpected scope binding: %#v", status.ScopeBinding)
			}
			agentBoard := agentruns.BuildBoard(taskStore, []agentruns.Run{run}, now, 30*time.Second)
			if len(agentBoard.Active) != 1 || agentBoard.Active[0].Run.ID != run.ID || agentBoard.Active[0].Freshness != background.LaneFreshnessHealthy {
				return localScenarioResult{}, fmt.Errorf("unexpected agent lane board: %#v", agentBoard)
			}
			taskBoard, err := taskStore.LaneBoardAt(now, 30*time.Second)
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(taskBoard.Active) != 1 || taskBoard.Active[0].TaskID != task.ID || taskBoard.Active[0].Freshness != background.LaneFreshnessHealthy {
				return localScenarioResult{}, fmt.Errorf("unexpected background lane board: %#v", taskBoard)
			}

			var events []background.WatchEvent
			watchCtx, cancelWatch := context.WithTimeout(ctx, 2*time.Second)
			err = taskStore.Watch(watchCtx, task.ID, background.WatchOptions{Interval: 20 * time.Millisecond, MaxEvents: 2}, func(event background.WatchEvent) error {
				events = append(events, event)
				return nil
			})
			cancelWatch()
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(events) != 2 || events[0].Type != "status" || events[1].Type != "log" || !strings.Contains(events[1].Data, "codog-bg-ready") {
				return localScenarioResult{}, fmt.Errorf("unexpected background watch events: %#v", events)
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
			if !stoppedStatus.Lifecycle.Terminal || stoppedStatus.Lifecycle.TerminalStateUnknown || stoppedStatus.Health.State != "finished" {
				return localScenarioResult{}, fmt.Errorf("unexpected stopped lifecycle: %#v", stoppedStatus)
			}
			if stoppedStatus.TerminalOutcome == nil || stoppedStatus.TerminalOutcome.DuplicateCount < 1 {
				return localScenarioResult{}, fmt.Errorf("missing terminal dedupe outcome: %#v", stoppedStatus.TerminalOutcome)
			}
			restarted, err := taskStore.Restart(task.ID, workspace)
			if err != nil {
				return localScenarioResult{}, err
			}
			defer func() { _, _ = taskStore.Stop(restarted.ID) }()
			if restarted.RestartedFrom != task.ID || restarted.Kind != "agent" || restarted.AgentType != "reviewer" || restarted.SessionID != sessionID {
				return localScenarioResult{}, fmt.Errorf("unexpected restarted task: %#v", restarted)
			}
			source, err := taskStore.Get(task.ID)
			if err != nil {
				return localScenarioResult{}, err
			}
			if source.RestartedBy != restarted.ID {
				return localScenarioResult{}, fmt.Errorf("source task missing restarted_by link")
			}

			failing, err := taskStore.RunWithOptions("printf codog-bg-fail; exit 7", workspace, background.RunOptions{
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
				return localScenarioResult{}, err
			}
			failed, err := waitForBackgroundTask(ctx, taskStore, failing.ID, 2*time.Second, func(task background.Task) bool {
				return task.Status == "failed" && task.ExitCode != nil && *task.ExitCode == 7
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if failed.RestartPolicy == nil || !failed.RestartPolicy.Enabled {
				return localScenarioResult{}, fmt.Errorf("failing task lost restart policy")
			}
			supervised, err := taskStore.SuperviseOnce(now.Add(time.Minute))
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(supervised.Restarted) != 1 || supervised.Restarted[0].RestartedFrom != failing.ID || supervised.Restarted[0].RestartCount != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected supervise result: %#v", supervised)
			}
			defer func() { _, _ = taskStore.Stop(supervised.Restarted[0].ID) }()

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
					"failed_status":    failed.Status,
					"failed_exit_code": *failed.ExitCode,
					"restarted":        len(supervised.Restarted),
					"restart_count":    supervised.Restarted[0].RestartCount,
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
		},
	}
}

func sshPrintPlanScenario() scenario {
	return scenario{
		name: "ssh_print_plan_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			output, err := runHarnessCodog(ctx, workspace,
				"--output-format", "json",
				"ssh",
				"--local",
				"localhost",
				workspace,
				"--print=ssh harness prompt",
				"--permission-mode", "read-only",
			)
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(output), &report); err != nil {
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
		},
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
		name: "remote_api_listener_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
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
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return localScenarioResult{}, err
			}
			var sessions []json.RawMessage
			if err := json.Unmarshal(body, &sessions); err != nil {
				return localScenarioResult{}, fmt.Errorf("sessions response was not a JSON array: %w", err)
			}

			message := "remote api listener harness ok"
			return localScenarioResult{
				Output:       fmt.Sprintf("%s %s", message, server.URL),
				FinalMessage: message,
				RequestCount: 3,
			}, nil
		},
	}
}

func remoteBridgeWorkspaceScenario() scenario {
	return scenario{
		name: "remote_bridge_workspace_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
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

			authRequest := func(method, path, payload string) (int, string, error) {
				var body io.Reader
				if payload != "" {
					body = strings.NewReader(payload)
				}
				req, err := http.NewRequest(method, server.URL+path, body)
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
			requireStatusContains := func(method, path, payload string, status int, contains ...string) (string, error) {
				gotStatus, body, err := authRequest(method, path, payload)
				if err != nil {
					return "", err
				}
				if gotStatus != status {
					return "", fmt.Errorf("%s %s returned %d: %s", method, path, gotStatus, body)
				}
				for _, expected := range contains {
					if !strings.Contains(body, expected) {
						return "", fmt.Errorf("%s %s response missing %s: %s", method, path, expected, body)
					}
				}
				return body, nil
			}

			if _, err := requireStatusContains(http.MethodGet, "/workspace/info", "", http.StatusOK, `"name":"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/workspace/files?pattern=*.md", "", http.StatusOK, `"path":"README.md"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/workspace/search?query=remote&glob=*.md", "", http.StatusOK, `"text":"hello remote bridge"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/file/write", `{"path":"notes.txt","content":"hello world"}`, http.StatusOK, `"bytes":11`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/file/edit", `{"path":"notes.txt","old_string":"world","new_string":"codog"}`, http.StatusOK, `"replacements":1`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/file/read?path=notes.txt", "", http.StatusOK, `"content":"hello codog"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/file/diff", `{"path":"README.md","old_string":"hello remote bridge","new_string":"hello codog bridge"}`, http.StatusOK, `-hello remote bridge`, `+hello codog bridge`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/file/read?path=../secret.txt", "", http.StatusBadRequest, `"error"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/sessions/session-bridge/messages", `{"role":"user","text":"hello remote session"}`, http.StatusOK, `"role":"user"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/sessions/session-bridge/history?limit=1", "", http.StatusOK, `"text":"hello remote session"`); err != nil {
				return localScenarioResult{}, err
			}
			editorWorkspace := filepath.ToSlash(workspace)
			if _, err := requireStatusContains(http.MethodPost, "/editor/identify", `{"editor":"VS Code","workspace":"`+editorWorkspace+`","token":"wrong"}`, http.StatusBadRequest, "token is invalid"); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/editor/identify", `{"editor":"VS Code","version":"1.0","workspace":"`+editorWorkspace+`","token":"editor-token"}`, http.StatusOK, `"editor":"VS Code"`, `"trusted":true`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/editor/open", `{"path":"internal/main.go"}`, http.StatusOK, `"path":"internal/main.go"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodPost, "/editor/selection", `{"start_line":1,"start_column":1,"end_line":1,"end_column":8}`, http.StatusOK, `"text":"package"`); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := requireStatusContains(http.MethodGet, "/editor/state", "", http.StatusOK, `"open_file":{"path":"internal/main.go"`, `"selection":{"path":"internal/main.go"`); err != nil {
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
		},
	}
}

func mcpLifecycleScenario() scenario {
	return scenario{
		name: "mcp_lifecycle_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
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
		},
	}
}

func mcpToolHookScenario() scenario {
	toolName := tools.NewMCPToolName("workflow", "echo")
	var serverURL string
	seenMethods := []string{}
	seenHookedArgument := false
	return scenario{
		name:       "mcp_tool_hook_roundtrip",
		permission: tools.PermissionWorkspace,
		hooks: config.HookConfig{
			PreToolUseCommands: []config.HookCommand{{
				Matcher: toolName,
				Command: `printf '%s' '{"systemMessage":"mcp pre hook","hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"mcp hook ok","updatedInput":{"text":"hooked mcp input"}}}'`,
			}},
			PostToolUseCommands: []config.HookCommand{{
				Matcher: toolName,
				Command: `printf '%s' '{"systemMessage":"mcp post hook"}'`,
			}},
		},
		prepare: func(_ string) ([]mockanthropic.Turn, func(), error) {
			mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
					w.Header().Set("Mcp-Session-Id", "mcp-tool-hook-session")
					writeMCPHarnessResponse(w, id, map[string]any{
						"protocolVersion": "2024-11-05",
						"capabilities": map[string]any{
							"tools": map[string]any{},
						},
						"serverInfo": map[string]any{"name": "mcp-tool-hook", "version": "1.0.0"},
					})
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					params, _ := req["params"].(map[string]any)
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
					seenHookedArgument = true
					writeMCPHarnessResponse(w, id, map[string]any{"content": []map[string]any{{
						"type": "text",
						"text": "mcp tool hook saw hooked mcp input",
					}}})
				default:
					writeMCPHarnessError(w, id, "unsupported method: "+method)
				}
			}))
			serverURL = mcpServer.URL + "/mcp"
			turns := []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  toolName,
					Input: json.RawMessage(`{"text":"original mcp input"}`),
				}}},
				{Text: "mcp tool hook harness ok"},
			}
			return turns, mcpServer.Close, nil
		},
		configureRegistry: func(registry *tools.Registry) error {
			if strings.TrimSpace(serverURL) == "" {
				return errors.New("MCP server URL was not prepared")
			}
			registry.Register(tools.MCPTool{
				Name:        toolName,
				ServerName:  "workflow",
				RemoteName:  "echo",
				Description: "Echo text through the MCP hook harness.",
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				Server: config.MCPServerConfig{URL: serverURL},
			})
			return nil
		},
		prompt: "call hooked MCP tool",
		verify: func(_ string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "mcp tool hook harness ok") {
				return fmt.Errorf("missing MCP tool hook final response")
			}
			if err := expectToolCalls(result, 1, false); err != nil {
				return err
			}
			if result.ToolCalls[0].Name != toolName {
				return fmt.Errorf("unexpected MCP tool name %q", result.ToolCalls[0].Name)
			}
			if result.ToolCalls[0].Input != `{"text":"hooked mcp input"}` {
				return fmt.Errorf("MCP tool input was not updated by hook: %s", result.ToolCalls[0].Input)
			}
			if !strings.Contains(result.ToolCalls[0].Output, "mcp tool hook saw hooked mcp input") ||
				!strings.Contains(result.ToolCalls[0].Output, "Hook feedback:\nmcp post hook") {
				return fmt.Errorf("MCP tool output missing result or post-hook feedback: %s", result.ToolCalls[0].Output)
			}
			for _, expectedMethod := range []string{"initialize", "notifications/initialized", "tools/call"} {
				if !slices.Contains(seenMethods, expectedMethod) {
					return fmt.Errorf("MCP hook server did not receive %s; methods=%v", expectedMethod, seenMethods)
				}
			}
			if !seenHookedArgument {
				return fmt.Errorf("MCP hook server did not receive the updated argument")
			}
			return nil
		},
	}
}

func mcpAuthOAuthRefreshScenario() scenario {
	return scenario{
		name: "mcp_auth_oauth_refresh_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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

			refreshSeen := false
			authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/oauth-authorization-server":
					writeJSONBody(w, map[string]any{
						"authorization_endpoint": "https://auth.example/authorize",
						"token_endpoint":         "http://" + r.Host + "/token",
					})
				case "/token":
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
					refreshSeen = true
					writeJSONBody(w, map[string]any{
						"access_token":  "new-access-token-secret-1234",
						"refresh_token": "new-refresh-token-secret-5678",
						"token_type":    "Bearer",
						"expires_in":    3600,
					})
				default:
					http.NotFound(w, r)
				}
			}))
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

			mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				method, _ := req["method"].(string)
				id := req["id"]
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
			}))
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
			if !refreshSeen {
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
		},
	}
}

func mcpAuthRecoveryScenario() scenario {
	return scenario{
		name: "mcp_auth_recovery_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
		},
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
