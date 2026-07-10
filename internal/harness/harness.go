package harness

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/bookmarks"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/contextview"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/doctor"
	"github.com/Rememorio/codog/internal/focus"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/policyengine"
	"github.com/Rememorio/codog/internal/providerdiag"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionsummary"
	"github.com/Rememorio/codog/internal/skills"
	localstatus "github.com/Rememorio/codog/internal/status"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/terminalsetup"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/tui"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
)

// ReportSchemaVersion is the stable schema identifier for mock parity reports.
const ReportSchemaVersion = reportschema.MockParityReportSchemaV1

// ManifestSchemaVersion is the stable schema identifier for mock parity manifests.
const ManifestSchemaVersion = reportschema.MockParityManifestSchemaV1

// Report summarizes one deterministic mock parity harness run.
type Report struct {
	SchemaVersion      string               `json:"schema_version"`
	OK                 bool                 `json:"ok"`
	Passed             int                  `json:"passed"`
	Total              int                  `json:"total"`
	ScenarioCount      int                  `json:"scenario_count"`
	RequestCount       int                  `json:"request_count"`
	Coverage           []CategoryReport     `json:"coverage"`
	CapabilityCoverage []CapabilityCoverage `json:"capability_coverage"`
	Workspace          string               `json:"workspace"`
	Output             string               `json:"output"`
	Iterations         int                  `json:"iterations"`
	MessageCount       int                  `json:"message_count"`
	ToolCalls          int                  `json:"tool_calls"`
	UsageSummary       usage.Summary        `json:"usage_summary"`
	EstimatedCost      float64              `json:"estimated_cost"`
	Scenarios          []ScenarioReport     `json:"scenarios"`
}

// Manifest lists the deterministic mock parity scenarios without running them.
type Manifest struct {
	SchemaVersion      string               `json:"schema_version"`
	ScenarioCount      int                  `json:"scenario_count"`
	Categories         []ManifestCategory   `json:"categories"`
	CapabilityCoverage []CapabilityCoverage `json:"capability_coverage"`
	Scenarios          []ManifestScenario   `json:"scenarios"`
}

// ManifestCategory summarizes scenarios by behavioral category.
type ManifestCategory struct {
	Category  string   `json:"category"`
	Count     int      `json:"count"`
	Scenarios []string `json:"scenarios"`
}

// ManifestScenario describes one mock parity scenario contract.
type ManifestScenario struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	ParityRefs  []string `json:"parity_refs"`
}

// CapabilityCoverage maps the harness scenarios back to the Claude-Code-style
// capability surfaces the project is trying to prove.
type CapabilityCoverage struct {
	Capability   string   `json:"capability"`
	Status       string   `json:"status"`
	RequiredRefs []string `json:"required_refs"`
	CoveredRefs  []string `json:"covered_refs"`
	Scenarios    []string `json:"scenarios"`
}

// CategoryReport summarizes mock parity results for one behavioral category.
type CategoryReport struct {
	Category  string   `json:"category"`
	OK        bool     `json:"ok"`
	Passed    int      `json:"passed"`
	Total     int      `json:"total"`
	Scenarios []string `json:"scenarios"`
}

// ScenarioReport records the outcome of one mock parity scenario.
type ScenarioReport struct {
	Name                 string        `json:"name"`
	Category             string        `json:"category"`
	Description          string        `json:"description"`
	ParityRefs           []string      `json:"parity_refs"`
	OK                   bool          `json:"ok"`
	Workspace            string        `json:"workspace"`
	Output               string        `json:"output,omitempty"`
	Iterations           int           `json:"iterations,omitempty"`
	RequestCount         int           `json:"request_count"`
	MessageCount         int           `json:"message_count,omitempty"`
	ToolCalls            int           `json:"tool_calls,omitempty"`
	ToolUses             []string      `json:"tool_uses"`
	ToolErrorCount       int           `json:"tool_error_count"`
	FinalMessage         string        `json:"final_message"`
	UsageSummary         usage.Summary `json:"usage_summary"`
	EstimatedCost        float64       `json:"estimated_cost"`
	RequestMessageCounts []int         `json:"request_message_counts,omitempty"`
	Compactions          int           `json:"compactions,omitempty"`
	Error                string        `json:"error,omitempty"`
}

type scenario struct {
	name                string
	turns               []mockanthropic.Turn
	prompt              string
	userContent         []anthropic.ContentBlock
	promptIn            string
	previous            []anthropic.Message
	autoCompactMessages int
	permission          tools.Permission
	hooks               config.HookConfig
	configHome          bool
	plugins             bool
	setup               func(string) error
	loadPrevious        func(string) ([]anthropic.Message, error)
	prepare             func(string) ([]mockanthropic.Turn, func(), error)
	verify              func(string, runloop.TurnResult, string) error
	verifyRequests      func([]anthropic.Request) error
	configureRegistry   func(*tools.Registry) error
	registryOptions     func(workspace string, configHome string) tools.RegistryOptions
	runLocal            func(context.Context, string) (localScenarioResult, error)
}

type localScenarioResult struct {
	Output         string
	FinalMessage   string
	ToolUses       []string
	ToolCalls      int
	ToolErrorCount int
	MessageCount   int
	RequestCount   int
}

var scenarioOrder = []string{
	"streaming_text",
	"prompt_attachments_roundtrip",
	"prompt_directory_attachment_roundtrip",
	"read_file_roundtrip",
	"write_file_allowed",
	"write_file_denied",
	"pre_tool_hook_updates_input",
	"user_prompt_hook_adds_context",
	"stop_hook_adds_feedback",
	"post_tool_hook_blocks_result",
	"post_tool_hook_adds_feedback",
	"file_changed_hook_adds_feedback",
	"multi_tool_turn_roundtrip",
	"grep_chunk_assembly",
	"edit_glob_ls_roundtrip",
	"multi_edit_apply_patch_roundtrip",
	"bash_stdout_roundtrip",
	"bash_background_output_roundtrip",
	"bash_kill_roundtrip",
	"powershell_stdout_roundtrip",
	"bash_output_truncation_roundtrip",
	"bash_permission_prompt_approved",
	"bash_permission_prompt_denied",
	"permission_scope_denial_roundtrip",
	"sandbox_bypass_status_roundtrip",
	"policy_update_sandbox_roundtrip",
	"policy_approval_roundtrip",
	"notebook_read_edit_roundtrip",
	"web_access_roundtrip",
	"web_access_limits_roundtrip",
	"git_workspace_roundtrip",
	"git_preserve_state_roundtrip",
	"worktree_lifecycle_roundtrip",
	"plan_todo_roundtrip",
	"todo_completion_verification_roundtrip",
	"lsp_static_roundtrip",
	"lsp_cli_metadata_roundtrip",
	"plugin_tool_roundtrip",
	"command_skill_template_roundtrip",
	"skill_activation_roundtrip",
	"onboarding_bookmarks_roundtrip",
	"memory_lifecycle_roundtrip",
	"prompt_directory_reference_roundtrip",
	"session_summary_roundtrip",
	"context_view_roundtrip",
	"theme_lifecycle_roundtrip",
	"interface_preferences_roundtrip",
	"privacy_keybindings_roundtrip",
	"browser_notifications_roundtrip",
	"model_runtime_preferences_roundtrip",
	"model_selection_roundtrip",
	"budget_lifecycle_roundtrip",
	"auth_credentials_roundtrip",
	"output_style_lifecycle_roundtrip",
	"diagnostics_status_roundtrip",
	"statusline_cli_roundtrip",
	"tui_prompt_completion_roundtrip",
	"ask_user_question_roundtrip",
	"runtime_output_tools_roundtrip",
	"repl_runtime_roundtrip",
	"config_precedence_roundtrip",
	"config_validation_status_roundtrip",
	"provider_routing_roundtrip",
	"session_resume_jsonl_roundtrip",
	"resume_slash_command_roundtrip",
	"session_export_path_safety_roundtrip",
	"plugin_lifecycle_roundtrip",
	"task_lifecycle_roundtrip",
	"task_packet_roundtrip",
	"team_cron_lifecycle_roundtrip",
	"worker_lifecycle_roundtrip",
	"recovery_lifecycle_roundtrip",
	"nudge_ack_dedupe_roundtrip",
	"provisional_status_escalation_roundtrip",
	"roadmap_pinpoint_lifecycle_roundtrip",
	"report_atomic_update_roundtrip",
	"report_backpressure_roundtrip",
	"agent_markdown_definition_roundtrip",
	"background_agent_run_roundtrip",
	"ssh_print_plan_roundtrip",
	"remote_trigger_roundtrip",
	"remote_api_listener_roundtrip",
	"remote_bridge_workspace_roundtrip",
	"mcp_lifecycle_roundtrip",
	"mcp_tool_hook_roundtrip",
	"mcp_auth_oauth_refresh_roundtrip",
	"mcp_auth_recovery_roundtrip",
	"acp_stdio_roundtrip",
	"auto_compact_triggered",
	"token_cost_reporting",
}

// ScenarioManifest returns the mock parity scenario contract without executing
// provider or tool loops.
func ScenarioManifest() Manifest {
	scenarios := make([]ManifestScenario, 0, len(scenarioOrder))
	for _, name := range scenarioOrder {
		metadata := scenarioMetadataFor(name)
		scenarios = append(scenarios, ManifestScenario{
			Name:        name,
			Category:    metadata.Category,
			Description: metadata.Description,
			ParityRefs:  append([]string(nil), metadata.ParityRefs...),
		})
	}
	return Manifest{
		SchemaVersion:      ManifestSchemaVersion,
		ScenarioCount:      len(scenarios),
		Categories:         manifestCategories(scenarios),
		CapabilityCoverage: capabilityCoverageForManifest(scenarios),
		Scenarios:          scenarios,
	}
}

// Run executes the deterministic mock parity harness against the local agent
// loop without contacting an external provider.
func Run(ctx context.Context) (Report, error) {
	scenarios := []scenario{
		{
			name:   "streaming_text",
			turns:  []mockanthropic.Turn{{Text: "streaming harness ok"}},
			prompt: "stream text",
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "streaming harness ok") {
					return fmt.Errorf("missing streamed text")
				}
				if len(result.ToolCalls) != 0 {
					return fmt.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
				}
				return nil
			},
		},
		{
			name:   "prompt_attachments_roundtrip",
			turns:  []mockanthropic.Turn{{Text: "attachment harness ok"}},
			prompt: "describe attached image",
			userContent: []anthropic.ContentBlock{
				{Type: "text", Text: "describe attached image"},
				{Type: "image", Title: "pixel.png", Source: &anthropic.ContentSource{Type: "base64", MediaType: "image/png", Data: "aW1n"}},
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "attachment harness ok") {
					return fmt.Errorf("missing attachment response")
				}
				if len(result.Messages) == 0 || len(result.Messages[0].Content) != 2 {
					return fmt.Errorf("expected structured attachment content in first user message")
				}
				return nil
			},
			verifyRequests: func(requests []anthropic.Request) error {
				if len(requests) == 0 {
					return fmt.Errorf("expected provider request")
				}
				content := requests[0].Messages[0].Content
				if len(content) != 2 || content[1].Type != "image" {
					return fmt.Errorf("expected image content block in provider request")
				}
				if content[1].Source == nil || content[1].Source.MediaType != "image/png" || content[1].Source.Data == "" {
					return fmt.Errorf("expected base64 image source in provider request")
				}
				return nil
			},
		},
		promptDirectoryAttachmentScenario(),
		{
			name: "read_file_roundtrip",
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"README.md"}`),
				}}},
				{Text: "codog harness ok"},
			},
			prompt: "read file",
			setup: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\n"), 0o644)
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "codog harness ok") {
					return fmt.Errorf("missing final read_file response")
				}
				return expectToolCalls(result, 1, false)
			},
		},
		{
			name:       "write_file_allowed",
			permission: tools.PermissionWorkspace,
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "write_file",
					Input: json.RawMessage(`{"path":"created.txt","content":"created by harness\n"}`),
				}}},
				{Text: "write harness ok"},
			},
			prompt: "write file",
			verify: func(workspace string, result runloop.TurnResult, _ string) error {
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				data, err := os.ReadFile(filepath.Join(workspace, "created.txt"))
				if err != nil {
					return err
				}
				if string(data) != "created by harness\n" {
					return fmt.Errorf("unexpected file content %q", string(data))
				}
				return nil
			},
		},
		{
			name:       "write_file_denied",
			permission: tools.PermissionReadOnly,
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "write_file",
					Input: json.RawMessage(`{"path":"denied.txt","content":"nope\n"}`),
				}}},
				{Text: "denied harness ok"},
			},
			prompt:   "deny write",
			promptIn: "n\n",
			verify: func(workspace string, result runloop.TurnResult, _ string) error {
				if err := expectToolCalls(result, 1, true); err != nil {
					return err
				}
				if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
					return fmt.Errorf("denied file exists or stat failed: %v", err)
				}
				return nil
			},
		},
		{
			name:       "pre_tool_hook_updates_input",
			permission: tools.PermissionReadOnly,
			hooks: config.HookConfig{
				PreToolUseCommands: []config.HookCommand{{
					Matcher: "write_file",
					Command: `printf '%s' '{"systemMessage":"updated","hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"hook ok","updatedInput":{"path":"hooked.txt","content":"hooked by harness\n"}}}'`,
				}},
			},
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "write_file",
					Input: json.RawMessage(`{"path":"original.txt","content":"original\n"}`),
				}}},
				{Text: "pre hook harness ok"},
			},
			prompt: "rewrite with hook",
			verify: func(workspace string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "pre hook harness ok") {
					return fmt.Errorf("missing pre-tool hook final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if result.ToolCalls[0].Input != `{"path":"hooked.txt","content":"hooked by harness\n"}` {
					return fmt.Errorf("tool input was not updated by hook: %s", result.ToolCalls[0].Input)
				}
				if _, err := os.Stat(filepath.Join(workspace, "original.txt")); !os.IsNotExist(err) {
					return fmt.Errorf("original file exists or stat failed: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(workspace, "hooked.txt"))
				if err != nil {
					return err
				}
				if string(data) != "hooked by harness\n" {
					return fmt.Errorf("unexpected hooked file content %q", string(data))
				}
				return nil
			},
		},
		{
			name: "user_prompt_hook_adds_context",
			hooks: config.HookConfig{
				UserPromptSubmitCommands: []config.HookCommand{{
					Command: `printf '%s' '{"systemMessage":"prompt parity note","hookSpecificOutput":{"additionalContext":"prompt parity context"}}'`,
				}},
			},
			turns:  []mockanthropic.Turn{{Text: "prompt hook harness ok"}},
			prompt: "prompt hook context",
			verify: func(_ string, _ runloop.TurnResult, output string) error {
				if !strings.Contains(output, "prompt hook harness ok") {
					return fmt.Errorf("missing prompt hook final response")
				}
				return nil
			},
			verifyRequests: func(requests []anthropic.Request) error {
				if len(requests) != 1 {
					return fmt.Errorf("expected 1 prompt hook request, got %d", len(requests))
				}
				if len(requests[0].Messages) != 2 {
					return fmt.Errorf("expected prompt hook feedback message, got %d messages", len(requests[0].Messages))
				}
				feedback := requests[0].Messages[1].Content[0].Text
				if !strings.Contains(feedback, "prompt parity note") ||
					!strings.Contains(feedback, "prompt parity context") {
					return fmt.Errorf("missing prompt hook feedback in request")
				}
				return nil
			},
		},
		{
			name: "stop_hook_adds_feedback",
			hooks: config.HookConfig{
				StopCommands: []config.HookCommand{{
					Command: `printf '%s' '{"systemMessage":"stop parity note","hookSpecificOutput":{"additionalContext":"stop parity context"}}'`,
				}},
			},
			turns:  []mockanthropic.Turn{{Text: "stop hook harness ok"}},
			prompt: "stop hook feedback",
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "stop hook harness ok") {
					return fmt.Errorf("missing stop hook final response")
				}
				if !slices.Equal(result.StopHookFeedback, []string{"stop parity note", "stop parity context"}) {
					return fmt.Errorf("unexpected stop hook feedback: %#v", result.StopHookFeedback)
				}
				return nil
			},
		},
		{
			name:       "post_tool_hook_blocks_result",
			permission: tools.PermissionWorkspace,
			hooks: config.HookConfig{
				PostToolUseCommands: []config.HookCommand{{
					Matcher: "write_file",
					Command: `printf '%s' '{"continue":false,"reason":"post hook blocked result"}'`,
				}},
			},
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "write_file",
					Input: json.RawMessage(`{"path":"post-hook.txt","content":"written before post hook\n"}`),
				}}},
				{Text: "post hook block harness ok"},
			},
			prompt: "write with post hook",
			verify: func(workspace string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "post hook block harness ok") {
					return fmt.Errorf("missing post-tool hook final response")
				}
				if err := expectToolCalls(result, 1, true); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "Hook feedback (error):\npost hook blocked result") {
					return fmt.Errorf("post-tool hook denial was not surfaced: %s", result.ToolCalls[0].Output)
				}
				data, err := os.ReadFile(filepath.Join(workspace, "post-hook.txt"))
				if err != nil {
					return err
				}
				if string(data) != "written before post hook\n" {
					return fmt.Errorf("unexpected post-hook file content %q", string(data))
				}
				return nil
			},
		},
		{
			name: "post_tool_hook_adds_feedback",
			hooks: config.HookConfig{
				PostToolUseCommands: []config.HookCommand{{
					Matcher: "read_file",
					Command: `printf '%s' '{"systemMessage":"post feedback"}'`,
				}},
			},
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"feedback.txt"}`),
				}}},
				{Text: "post feedback harness ok"},
			},
			prompt: "read with post feedback",
			setup: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "feedback.txt"), []byte("feedback file\n"), 0o644)
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "post feedback harness ok") {
					return fmt.Errorf("missing post feedback final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "Hook feedback:\npost feedback") {
					return fmt.Errorf("post-tool hook feedback was not surfaced: %s", result.ToolCalls[0].Output)
				}
				return nil
			},
		},
		{
			name:       "file_changed_hook_adds_feedback",
			permission: tools.PermissionWorkspace,
			hooks: config.HookConfig{
				FileChangedCommands: []config.HookCommand{{
					Matcher: "write_file",
					Command: `printf '%s' '{"systemMessage":"file changed feedback"}'`,
				}},
			},
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "write_file",
					Input: json.RawMessage(`{"path":"file-hook.txt","content":"file hook\n"}`),
				}}},
				{Text: "file changed feedback harness ok"},
			},
			prompt: "write with file changed hook",
			verify: func(workspace string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "file changed feedback harness ok") {
					return fmt.Errorf("missing file changed hook final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "Hook feedback:\nfile changed feedback") {
					return fmt.Errorf("file-changed hook feedback was not surfaced: %s", result.ToolCalls[0].Output)
				}
				data, err := os.ReadFile(filepath.Join(workspace, "file-hook.txt"))
				if err != nil {
					return err
				}
				if string(data) != "file hook\n" {
					return fmt.Errorf("unexpected file hook content %q", string(data))
				}
				return nil
			},
		},
		{
			name: "multi_tool_turn_roundtrip",
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{
					{ID: "tool-1", Name: "read_file", Input: json.RawMessage(`{"path":"README.md"}`)},
					{ID: "tool-2", Name: "grep", Input: json.RawMessage(`{"pattern":"Needle","path":"."}`)},
				}},
				{Text: "multi tool harness ok"},
			},
			prompt: "use multiple tools",
			setup: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\nNeedle\n"), 0o644)
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "multi tool harness ok") {
					return fmt.Errorf("missing multi-tool final response")
				}
				return expectToolCalls(result, 2, false)
			},
		},
		{
			name: "grep_chunk_assembly",
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:          "tool-1",
					Name:        "grep",
					InputDeltas: []string{`{"pattern":"Need`, `le","path":".","output_mode":"content"}`},
				}}},
				{Text: "grep chunk harness ok"},
			},
			prompt: "grep chunks",
			setup: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\nNeedle\n"), 0o644)
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "grep chunk harness ok") {
					return fmt.Errorf("missing grep chunk final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "Needle") {
					return fmt.Errorf("missing grep match in tool output")
				}
				return nil
			},
		},
		editGlobLSScenario(),
		multiEditApplyPatchScenario(),
		{
			name:       "bash_stdout_roundtrip",
			permission: tools.PermissionAllow,
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"printf harness-bash","timeout":1000}`),
				}}},
				{Text: "bash harness ok"},
			},
			prompt: "run bash",
			verify: func(_ string, result runloop.TurnResult, _ string) error {
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "harness-bash") {
					return fmt.Errorf("missing bash stdout in tool output")
				}
				return nil
			},
		},
		bashBackgroundOutputScenario(),
		bashKillScenario(),
		powerShellStdoutScenario(),
		bashOutputTruncationScenario(),
		{
			name:       "bash_permission_prompt_approved",
			permission: tools.PermissionWorkspace,
			promptIn:   "y\n",
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"printf approved-bash","timeout":1000}`),
				}}},
				{Text: "bash approved harness ok"},
			},
			prompt: "approve bash",
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "bash approved harness ok") {
					return fmt.Errorf("missing approved bash final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "approved-bash") {
					return fmt.Errorf("missing approved bash stdout in tool output")
				}
				return nil
			},
		},
		{
			name:       "bash_permission_prompt_denied",
			permission: tools.PermissionWorkspace,
			promptIn:   "n\n",
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "bash",
					Input: json.RawMessage(`{"command":"printf denied-bash","timeout":1000}`),
				}}},
				{Text: "bash denied harness ok"},
			},
			prompt: "deny bash",
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "bash denied harness ok") {
					return fmt.Errorf("missing denied bash final response")
				}
				if err := expectToolCalls(result, 1, true); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "permission denied") {
					return fmt.Errorf("missing permission denial in tool output")
				}
				return nil
			},
		},
		permissionScopeDenialScenario(),
		sandboxBypassStatusScenario(),
		policyUpdateSandboxScenario(),
		policyApprovalScenario(),
		notebookReadEditScenario(),
		webAccessScenario(),
		webAccessLimitsScenario(),
		gitWorkspaceScenario(),
		gitPreserveStateScenario(),
		worktreeLifecycleScenario(),
		planTodoScenario(),
		todoCompletionVerificationScenario(),
		lspStaticScenario(),
		lspCLIMetadataScenario(),
		{
			name:    "plugin_tool_roundtrip",
			plugins: true,
			turns: []mockanthropic.Turn{
				{ToolUses: []mockanthropic.ToolUse{{
					ID:    "tool-1",
					Name:  "demo_tool",
					Input: json.RawMessage(`{"message":"plugin-harness"}`),
				}}},
				{Text: "plugin harness ok"},
			},
			prompt: "run plugin",
			setup: func(workspace string) error {
				dir := filepath.Join(workspace, ".codog", "plugins", "demo")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				manifest := `{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`
				return os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644)
			},
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "plugin harness ok") {
					return fmt.Errorf("missing plugin final response")
				}
				if err := expectToolCalls(result, 1, false); err != nil {
					return err
				}
				if !strings.Contains(result.ToolCalls[0].Output, "plugin-harness") {
					return fmt.Errorf("missing plugin stdin echo in tool output")
				}
				return nil
			},
		},
		commandSkillTemplateScenario(),
		skillActivationScenario(),
		onboardingBookmarksScenario(),
		memoryLifecycleScenario(),
		promptDirectoryReferenceScenario(),
		sessionSummaryScenario(),
		contextViewScenario(),
		themeLifecycleScenario(),
		interfacePreferencesScenario(),
		privacyKeybindingsScenario(),
		browserNotificationsScenario(),
		modelRuntimePreferencesScenario(),
		modelSelectionScenario(),
		budgetLifecycleScenario(),
		authCredentialsScenario(),
		outputStyleLifecycleScenario(),
		diagnosticsStatusScenario(),
		statuslineCLIScenario(),
		tuiPromptCompletionScenario(),
		askUserQuestionScenario(),
		runtimeOutputToolsScenario(),
		replRuntimeScenario(),
		configPrecedenceScenario(),
		configValidationStatusScenario(),
		providerRoutingScenario(),
		sessionResumeJSONLRoundtripScenario(),
		resumeSlashCommandScenario(),
		sessionExportPathSafetyScenario(),
		pluginLifecycleScenario(),
		taskLifecycleScenario(),
		taskPacketRoundtripScenario(),
		teamCronLifecycleScenario(),
		workerLifecycleScenario(),
		recoveryLifecycleScenario(),
		nudgeAckDedupeScenario(),
		provisionalStatusEscalationScenario(),
		roadmapPinpointLifecycleScenario(),
		reportAtomicUpdateScenario(),
		reportBackpressureScenario(),
		agentMarkdownDefinitionScenario(),
		backgroundAgentRunScenario(),
		sshPrintPlanScenario(),
		remoteTriggerScenario(),
		remoteAPIListenerScenario(),
		remoteBridgeWorkspaceScenario(),
		mcpLifecycleScenario(),
		mcpToolHookScenario(),
		mcpAuthOAuthRefreshScenario(),
		mcpAuthRecoveryScenario(),
		acpStdioScenario(),
		{
			name: "auto_compact_triggered",
			turns: []mockanthropic.Turn{
				{Text: "compact harness ok"},
			},
			prompt:              "trigger compact",
			autoCompactMessages: 1,
			hooks: config.HookConfig{
				PreCompactCommands: []config.HookCommand{{
					Command: `printf '%s' '{"systemMessage":"compact parity pre"}'`,
				}},
				PostCompactCommands: []config.HookCommand{{
					Command: `printf '%s' '{"hookSpecificOutput":{"additionalContext":"compact parity post"}}'`,
				}},
			},
			previous: []anthropic.Message{
				anthropic.TextMessage("user", "one"),
				anthropic.TextMessage("assistant", "two"),
				anthropic.TextMessage("user", "three"),
			},
			verify: func(_ string, _ runloop.TurnResult, output string) error {
				if !strings.Contains(output, "compact harness ok") {
					return fmt.Errorf("missing compact final response")
				}
				return nil
			},
			verifyRequests: func(requests []anthropic.Request) error {
				if len(requests) != 1 {
					return fmt.Errorf("expected 1 compacted request, got %d", len(requests))
				}
				if len(requests[0].Messages) != 2 {
					return fmt.Errorf("expected compacted request to keep 2 messages, got %d", len(requests[0].Messages))
				}
				if len(requests[0].Messages[0].Content) == 0 ||
					!strings.Contains(requests[0].Messages[0].Content[0].Text, "auto-compacted") {
					return fmt.Errorf("missing auto-compaction summary message")
				}
				summary := requests[0].Messages[0].Content[0].Text
				if !strings.Contains(summary, "compact parity pre") ||
					!strings.Contains(summary, "compact parity post") {
					return fmt.Errorf("missing compaction hook feedback in summary")
				}
				return nil
			},
		},
		{
			name:   "token_cost_reporting",
			turns:  []mockanthropic.Turn{{Text: "token cost harness ok"}},
			prompt: "report token cost",
			verify: func(_ string, result runloop.TurnResult, output string) error {
				if !strings.Contains(output, "token cost harness ok") {
					return fmt.Errorf("missing token cost final response")
				}
				summary := usageSummaryForResult(result)
				if summary.Source != "actual" {
					return fmt.Errorf("expected actual token usage source, got %q", summary.Source)
				}
				if summary.TotalTokens == 0 {
					return fmt.Errorf("missing provider token counts")
				}
				if summary.EstimatedUSD <= 0 {
					return fmt.Errorf("missing estimated cost")
				}
				return nil
			},
		},
	}

	report := Report{SchemaVersion: ReportSchemaVersion, Total: len(scenarios), ScenarioCount: len(scenarios)}
	for _, item := range scenarios {
		scenarioReport := runScenario(ctx, item)
		report.Scenarios = append(report.Scenarios, scenarioReport)
		if scenarioReport.OK {
			report.Passed++
		}
		report.Workspace = scenarioReport.Workspace
		report.Output = scenarioReport.Output
		report.Iterations = scenarioReport.Iterations
		report.RequestCount += scenarioReport.RequestCount
		report.MessageCount = scenarioReport.MessageCount
		report.ToolCalls = scenarioReport.ToolCalls
		report.UsageSummary = addUsageSummary(report.UsageSummary, scenarioReport.UsageSummary)
		report.EstimatedCost = report.UsageSummary.EstimatedUSD
	}
	report.OK = report.Passed == report.Total
	report.Coverage = categoryCoverage(report.Scenarios)
	report.CapabilityCoverage = capabilityCoverageForReport(report.Scenarios)
	return report, nil
}

// ValidateReport verifies that a mock parity report is internally consistent.
func ValidateReport(report Report) error {
	issues := []string{}
	if report.SchemaVersion != ReportSchemaVersion {
		issues = append(issues, fmt.Sprintf("schema_version: expected %q, got %q", ReportSchemaVersion, report.SchemaVersion))
	}
	if report.Total != report.ScenarioCount {
		issues = append(issues, fmt.Sprintf("scenario_count: expected total %d, got %d", report.Total, report.ScenarioCount))
	}
	if len(report.Scenarios) != report.ScenarioCount {
		issues = append(issues, fmt.Sprintf("scenarios: expected %d, got %d", report.ScenarioCount, len(report.Scenarios)))
	}
	passed := 0
	requests := 0
	for _, scenario := range report.Scenarios {
		if strings.TrimSpace(scenario.Name) == "" {
			issues = append(issues, "scenario name is required")
		}
		if strings.TrimSpace(scenario.Category) == "" {
			issues = append(issues, fmt.Sprintf("%s: category is required", scenario.Name))
		}
		if strings.TrimSpace(scenario.Description) == "" {
			issues = append(issues, fmt.Sprintf("%s: description is required", scenario.Name))
		}
		if len(scenario.ParityRefs) == 0 {
			issues = append(issues, fmt.Sprintf("%s: parity_refs is required", scenario.Name))
		}
		if len(scenario.ToolUses) != scenario.ToolCalls {
			issues = append(issues, fmt.Sprintf("%s: expected %d tool_uses, got %d", scenario.Name, scenario.ToolCalls, len(scenario.ToolUses)))
		}
		if scenario.ToolErrorCount > scenario.ToolCalls {
			issues = append(issues, fmt.Sprintf("%s: tool_error_count exceeds tool_calls", scenario.Name))
		}
		if scenario.OK {
			passed++
		}
		requests += scenario.RequestCount
	}
	if passed != report.Passed {
		issues = append(issues, fmt.Sprintf("passed: expected %d, got %d", passed, report.Passed))
	}
	if (passed == report.Total) != report.OK {
		issues = append(issues, fmt.Sprintf("ok: expected %t, got %t", passed == report.Total, report.OK))
	}
	if requests != report.RequestCount {
		issues = append(issues, fmt.Sprintf("request_count: expected %d, got %d", requests, report.RequestCount))
	}
	coverageTotal := 0
	for _, category := range report.Coverage {
		coverageTotal += category.Total
		if category.Passed > category.Total {
			issues = append(issues, fmt.Sprintf("%s: category passed exceeds total", category.Category))
		}
	}
	if coverageTotal != report.Total {
		issues = append(issues, fmt.Sprintf("coverage: expected %d scenarios, got %d", report.Total, coverageTotal))
	}
	if len(issues) > 0 {
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}

func runScenario(ctx context.Context, item scenario) ScenarioReport {
	metadata := scenarioMetadataFor(item.name)
	workspace, err := os.MkdirTemp("", "codog-harness-*")
	if err != nil {
		return scenarioErrorReport(item.name, metadata, "", err)
	}
	defer os.RemoveAll(workspace)
	if item.setup != nil {
		if err := item.setup(workspace); err != nil {
			return scenarioErrorReport(item.name, metadata, workspace, err)
		}
	}
	previous := item.previous
	if item.loadPrevious != nil {
		loaded, err := item.loadPrevious(workspace)
		if err != nil {
			return scenarioErrorReport(item.name, metadata, workspace, err)
		}
		previous = loaded
	}
	turns := item.turns
	if item.prepare != nil {
		preparedTurns, cleanup, err := item.prepare(workspace)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return scenarioErrorReport(item.name, metadata, workspace, err)
		}
		turns = preparedTurns
	}
	if item.runLocal != nil {
		localResult, err := item.runLocal(ctx, workspace)
		report := ScenarioReport{
			Name:           item.name,
			Category:       metadata.Category,
			Description:    metadata.Description,
			ParityRefs:     append([]string(nil), metadata.ParityRefs...),
			Workspace:      workspace,
			Output:         localResult.Output,
			RequestCount:   localResult.RequestCount,
			MessageCount:   localResult.MessageCount,
			ToolCalls:      localResult.ToolCalls,
			ToolUses:       append([]string(nil), localResult.ToolUses...),
			ToolErrorCount: localResult.ToolErrorCount,
			FinalMessage:   localResult.FinalMessage,
		}
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.OK = true
		return report
	}

	var requests []anthropic.Request
	mockServer := mockanthropic.Server{
		Turns: turns,
		OnRequest: func(raw json.RawMessage) {
			var request anthropic.Request
			if err := json.Unmarshal(raw, &request); err == nil {
				requests = append(requests, request)
			}
		},
	}
	server := httptest.NewServer(mockServer.Handler())
	defer server.Close()
	var out bytes.Buffer
	client := anthropic.New(server.URL, "mock-key", "")
	permission := item.permission
	if permission == "" {
		permission = tools.PermissionWorkspace
	}
	autoCompactMessages := item.autoCompactMessages
	if autoCompactMessages == 0 {
		autoCompactMessages = 20
	}
	configHome := ""
	if item.configHome {
		configHome = filepath.Join(workspace, "config-home")
		if err := os.MkdirAll(configHome, 0o755); err != nil {
			return scenarioErrorReport(item.name, metadata, workspace, err)
		}
	}
	registry, err := registryForScenario(workspace, configHome, item)
	if err != nil {
		return scenarioErrorReport(item.name, metadata, workspace, err)
	}
	runner := runloop.Runner{
		Config: config.Config{
			Model:               "mock",
			MaxTokens:           128,
			MaxTurns:            3,
			AutoCompactMessages: autoCompactMessages,
			Hooks:               item.hooks,
		},
		Client:    client,
		Tools:     registry,
		Prompter:  &tools.Prompter{Mode: permission, In: strings.NewReader(item.promptIn), Err: io.Discard},
		Workspace: workspace,
		Out:       &out,
	}
	var result runloop.TurnResult
	var runErr error
	if len(item.userContent) > 0 {
		result, runErr = runner.RunWithUserContent(ctx, previous, item.userContent, item.prompt)
	} else {
		result, runErr = runner.Run(ctx, previous, item.prompt)
	}
	scenarioReport := ScenarioReport{
		Name:                 item.name,
		Category:             metadata.Category,
		Description:          metadata.Description,
		ParityRefs:           append([]string(nil), metadata.ParityRefs...),
		Workspace:            workspace,
		Output:               out.String(),
		Iterations:           result.Iterations,
		RequestCount:         len(requests),
		MessageCount:         len(result.Messages),
		ToolCalls:            len(result.ToolCalls),
		ToolUses:             toolUseNames(result.ToolCalls),
		ToolErrorCount:       toolErrorCount(result.ToolCalls),
		FinalMessage:         finalAssistantMessage(result.Messages),
		UsageSummary:         usageSummaryForResult(result),
		RequestMessageCounts: requestMessageCounts(requests),
		Compactions:          compactRequestCount(requests),
	}
	scenarioReport.EstimatedCost = scenarioReport.UsageSummary.EstimatedUSD
	if runErr != nil {
		scenarioReport.Error = runErr.Error()
		return scenarioReport
	}
	if item.verify != nil {
		if err := item.verify(workspace, result, out.String()); err != nil {
			scenarioReport.Error = err.Error()
			return scenarioReport
		}
	}
	if item.verifyRequests != nil {
		if err := item.verifyRequests(requests); err != nil {
			scenarioReport.Error = err.Error()
			return scenarioReport
		}
	}
	scenarioReport.OK = true
	return scenarioReport
}

func categoryCoverage(scenarios []ScenarioReport) []CategoryReport {
	byCategory := map[string]*CategoryReport{}
	for _, scenario := range scenarios {
		category := strings.TrimSpace(scenario.Category)
		if category == "" {
			category = "uncategorized"
		}
		report := byCategory[category]
		if report == nil {
			report = &CategoryReport{Category: category, OK: true}
			byCategory[category] = report
		}
		report.Total++
		if scenario.OK {
			report.Passed++
		} else {
			report.OK = false
		}
		report.Scenarios = append(report.Scenarios, scenario.Name)
	}
	categories := make([]string, 0, len(byCategory))
	for category := range byCategory {
		categories = append(categories, category)
	}
	slices.Sort(categories)
	out := make([]CategoryReport, 0, len(categories))
	for _, category := range categories {
		report := *byCategory[category]
		slices.Sort(report.Scenarios)
		out = append(out, report)
	}
	return out
}

func manifestCategories(scenarios []ManifestScenario) []ManifestCategory {
	byCategory := map[string]*ManifestCategory{}
	for _, scenario := range scenarios {
		category := strings.TrimSpace(scenario.Category)
		if category == "" {
			category = "uncategorized"
		}
		report := byCategory[category]
		if report == nil {
			report = &ManifestCategory{Category: category}
			byCategory[category] = report
		}
		report.Count++
		report.Scenarios = append(report.Scenarios, scenario.Name)
	}
	categories := make([]string, 0, len(byCategory))
	for category := range byCategory {
		categories = append(categories, category)
	}
	slices.Sort(categories)
	out := make([]ManifestCategory, 0, len(categories))
	for _, category := range categories {
		report := *byCategory[category]
		slices.Sort(report.Scenarios)
		out = append(out, report)
	}
	return out
}

type capabilityTarget struct {
	Capability   string
	RequiredRefs []string
}

var capabilityTargets = []capabilityTarget{
	{Capability: "one-shot prompt and streaming", RequiredRefs: []string{"Anthropic streaming", "Tool result roundtrip"}},
	{Capability: "file tools", RequiredRefs: []string{"File tools", "Edit tool", "Grep chunk assembly", "Glob tool", "MultiEdit tool", "ApplyPatch tool"}},
	{Capability: "bash and shell safety", RequiredRefs: []string{"Bash tool", "BashOutput tool", "KillBash tool", "Permission prompts", "Output truncation"}},
	{Capability: "permissions and sandbox", RequiredRefs: []string{"Permission enforcement", "Workspace-write permissions", "Sandbox", "Permission safety", "Workspace scope denial"}},
	{Capability: "policy and approval control plane", RequiredRefs: []string{"Policy evaluation", "Approval tokens", "Delegation audit", "Replay denial", "Commit-scoped approval", "Execution chain traceability"}},
	{Capability: "sessions, resume, and project memory", RequiredRefs: []string{"Session JSONL", "Resume", "Session context management", "Project memory", "Session summary"}},
	{Capability: "slash commands and custom workflows", RequiredRefs: []string{"Slash commands", "Skills", "Skill activation", "Templates", "Project workflow surfaces"}},
	{Capability: "hooks", RequiredRefs: []string{"Hooks", "PreToolUse", "PostToolUse hooks", "UserPromptSubmit", "Stop"}},
	{Capability: "configuration and provider routing", RequiredRefs: []string{"Configuration", "Precedence rules", "Provider routing", "OpenAI-compatible APIs"}},
	{Capability: "MCP client and auth", RequiredRefs: []string{"MCP client", "MCP lifecycle", "MCP tool calls", "MCP auth", "OAuth refresh"}},
	{Capability: "token, cost, and compaction", RequiredRefs: []string{"Token usage", "Cost tracking", "Auto-compaction", "Compaction summary"}},
	{Capability: "IDE bridge and remote control", RequiredRefs: []string{"IDE bridge", "ACP/Zed", "Remote sessions", "Control API listener"}},
	{Capability: "multi-agent and background tasks", RequiredRefs: []string{"Background tasks", "Agent runs", "Lane board", "Supervisor restarts"}},
	{Capability: "structured task packets", RequiredRefs: []string{"Task packet schema", "Task packet scope resolution", "Task packet persistence"}},
	{Capability: "team and scheduled tasks", RequiredRefs: []string{"Team tools", "Team task assignment", "Cron tools", "Cron lifecycle"}},
	{Capability: "worker orchestration", RequiredRefs: []string{"Worker tools", "Worker trust recovery", "Worker prompt delivery", "Worker startup diagnostics"}},
	{Capability: "recovery recipes and ledger", RequiredRefs: []string{"Recovery recipes", "Recovery attempts", "Recovery ledger", "Escalation tracking"}},
	{Capability: "notebook and code intelligence", RequiredRefs: []string{"Notebook read", "Notebook edit", "LSP tool", "Code intelligence"}},
	{Capability: "git and worktree management", RequiredRefs: []string{"Git tools", "Branch freshness", "Worktree allocation", "Worktree cleanup"}},
	{Capability: "OAuth and account lifecycle", RequiredRefs: []string{"OAuth refresh", "Token redaction", "MCP auth"}},
	{Capability: "authentication and credentials", RequiredRefs: []string{"Auth status", "API key", "Auth token", "Token redaction", "Credential persistence"}},
	{Capability: "enterprise policy and updater", RequiredRefs: []string{"Enterprise policy", "Audit events", "Signed updater"}},
	{Capability: "plugins and marketplace", RequiredRefs: []string{"Plugin tools", "Plugin lifecycle", "Plugin manifest loading", "External plugin lifecycle"}},
	{Capability: "TUI and interactive rendering", RequiredRefs: []string{"Bubble Tea TUI", "Interactive rendering", "Output styles"}},
	{Capability: "interactive question handling", RequiredRefs: []string{"AskUserQuestion tool", "Interactive questions"}},
	{Capability: "runtime utility tools", RequiredRefs: []string{"Brief tool", "SendUserMessage tool", "StructuredOutput tool", "Sleep tool", "REPL tool"}},
	{Capability: "setup and diagnostics", RequiredRefs: []string{"Doctor", "Status diagnostics", "Terminal setup"}},
	{Capability: "context view and focus", RequiredRefs: []string{"Context view", "Focused paths", "Context signals", "Prompt references"}},
	{Capability: "statusline rendering", RequiredRefs: []string{"Statusline", "Statusline JSON", "Statusline text"}},
	{Capability: "appearance and preferences", RequiredRefs: []string{"Theme", "Theme persistence", "Theme reset", "Language preference", "Vim mode", "Privacy settings", "Keybindings", "Chrome integration", "Notifications", "Telemetry", "Preference persistence"}},
	{Capability: "model runtime controls", RequiredRefs: []string{"Model preference", "Model persistence", "Model routing detail", "Reasoning effort", "Fast mode", "Temperature preference", "Token budget", "Turn limit", "Preference persistence"}},
}

func capabilityCoverageForManifest(scenarios []ManifestScenario) []CapabilityCoverage {
	refsByScenario := make([]scenarioRefs, 0, len(scenarios))
	for _, scenario := range scenarios {
		refsByScenario = append(refsByScenario, scenarioRefs{
			Name:       scenario.Name,
			ParityRefs: scenario.ParityRefs,
			OK:         true,
			Ran:        true,
		})
	}
	return buildCapabilityCoverage(refsByScenario, true)
}

func capabilityCoverageForReport(scenarios []ScenarioReport) []CapabilityCoverage {
	refsByScenario := make([]scenarioRefs, 0, len(scenarios))
	for _, scenario := range scenarios {
		refsByScenario = append(refsByScenario, scenarioRefs{
			Name:       scenario.Name,
			ParityRefs: scenario.ParityRefs,
			OK:         scenario.OK,
			Ran:        true,
		})
	}
	return buildCapabilityCoverage(refsByScenario, false)
}

type scenarioRefs struct {
	Name       string
	ParityRefs []string
	OK         bool
	Ran        bool
}

func buildCapabilityCoverage(scenarios []scenarioRefs, manifestOnly bool) []CapabilityCoverage {
	out := make([]CapabilityCoverage, 0, len(capabilityTargets))
	for _, target := range capabilityTargets {
		matchedScenarios := map[string]bool{}
		coveredRefs := map[string]bool{}
		failing := false
		passing := false
		for _, scenario := range scenarios {
			matchedRefs := matchingRequiredRefs(target.RequiredRefs, scenario.ParityRefs)
			if len(matchedRefs) == 0 {
				continue
			}
			matchedScenarios[scenario.Name] = true
			for _, ref := range matchedRefs {
				coveredRefs[ref] = true
			}
			if scenario.OK {
				passing = true
			} else if scenario.Ran {
				failing = true
			}
		}
		status := "missing"
		switch {
		case manifestOnly && len(matchedScenarios) > 0:
			status = "mapped"
		case failing && passing:
			status = "partial"
		case failing:
			status = "failing"
		case passing:
			status = "passing"
		}
		out = append(out, CapabilityCoverage{
			Capability:   target.Capability,
			Status:       status,
			RequiredRefs: append([]string(nil), target.RequiredRefs...),
			CoveredRefs:  sortedKeys(coveredRefs),
			Scenarios:    sortedKeys(matchedScenarios),
		})
	}
	return out
}

func matchingRequiredRefs(required []string, actual []string) []string {
	actualSet := map[string]bool{}
	for _, ref := range actual {
		actualSet[strings.ToLower(strings.TrimSpace(ref))] = true
	}
	matches := []string{}
	for _, ref := range required {
		if actualSet[strings.ToLower(strings.TrimSpace(ref))] {
			matches = append(matches, ref)
		}
	}
	return matches
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}

type scenarioMetadata struct {
	Category    string
	Description string
	ParityRefs  []string
}

func scenarioMetadataFor(name string) scenarioMetadata {
	if metadata, ok := scenarioMetadataByName[name]; ok {
		return metadata
	}
	return scenarioMetadata{
		Category:    "runtime",
		Description: "Exercises a runtime behavior covered by the mock parity harness.",
		ParityRefs:  []string{"Mock parity harness"},
	}
}

var scenarioMetadataByName = map[string]scenarioMetadata{
	"streaming_text": {
		Category:    "baseline",
		Description: "Validates streamed assistant text with no tool calls.",
		ParityRefs:  []string{"Mock parity harness", "Anthropic streaming"},
	},
	"prompt_attachments_roundtrip": {
		Category:    "attachments",
		Description: "Sends structured user content with an image attachment through the provider request path.",
		ParityRefs:  []string{"Prompt attachments", "Anthropic content blocks"},
	},
	"prompt_directory_attachment_roundtrip": {
		Category:    "attachments",
		Description: "Runs the real prompt CLI with a directory attachment and verifies text file aggregation plus binary skip metadata.",
		ParityRefs:  []string{"Prompt attachments", "Directory attachments", "Anthropic content blocks", "Workspace context"},
	},
	"read_file_roundtrip": {
		Category:    "file-tools",
		Description: "Executes read_file and synthesizes the final assistant response.",
		ParityRefs:  []string{"File tools", "Tool result roundtrip"},
	},
	"write_file_allowed": {
		Category:    "file-tools",
		Description: "Confirms workspace-write write_file succeeds and changes the filesystem.",
		ParityRefs:  []string{"File tools", "Workspace-write permissions"},
	},
	"write_file_denied": {
		Category:    "permissions",
		Description: "Confirms read-only mode blocks write_file with an error tool result.",
		ParityRefs:  []string{"Permission enforcement", "File tool denial"},
	},
	"pre_tool_hook_updates_input": {
		Category:    "hooks",
		Description: "Runs a PreToolUse hook that updates tool input before execution.",
		ParityRefs:  []string{"Hooks", "PreToolUse"},
	},
	"user_prompt_hook_adds_context": {
		Category:    "hooks",
		Description: "Runs a UserPromptSubmit hook that adds context before the provider request.",
		ParityRefs:  []string{"Hooks", "UserPromptSubmit"},
	},
	"stop_hook_adds_feedback": {
		Category:    "hooks",
		Description: "Runs a Stop hook and records feedback on the completed turn.",
		ParityRefs:  []string{"Hooks", "Stop"},
	},
	"post_tool_hook_blocks_result": {
		Category:    "hooks",
		Description: "Runs a PostToolUse hook that blocks a tool result with an error response.",
		ParityRefs:  []string{"Hooks", "PostToolUse blocking"},
	},
	"post_tool_hook_adds_feedback": {
		Category:    "hooks",
		Description: "Runs a PostToolUse hook that appends feedback to a successful result.",
		ParityRefs:  []string{"Hooks", "PostToolUse feedback"},
	},
	"file_changed_hook_adds_feedback": {
		Category:    "hooks",
		Description: "Runs a file-change hook after a write tool mutates workspace files.",
		ParityRefs:  []string{"Hooks", "File change feedback"},
	},
	"multi_tool_turn_roundtrip": {
		Category:    "multi-tool-turns",
		Description: "Executes multiple tool uses in one assistant turn before final synthesis.",
		ParityRefs:  []string{"Multi-tool assistant turns", "Tool result ordering"},
	},
	"grep_chunk_assembly": {
		Category:    "file-tools",
		Description: "Validates grep_search input chunk assembly and result delivery.",
		ParityRefs:  []string{"File tools", "Grep chunk assembly"},
	},
	"edit_glob_ls_roundtrip": {
		Category:    "file-tools",
		Description: "Executes edit_file, glob, and ls in one tool turn and verifies the edited workspace file.",
		ParityRefs:  []string{"File tools", "Edit tool", "Glob tool", "LS tool"},
	},
	"multi_edit_apply_patch_roundtrip": {
		Category:    "file-tools",
		Description: "Executes multi_edit followed by apply_patch and verifies the atomic file mutation chain.",
		ParityRefs:  []string{"File tools", "MultiEdit tool", "ApplyPatch tool", "Unified diff patches"},
	},
	"bash_stdout_roundtrip": {
		Category:    "bash",
		Description: "Runs bash in danger-full-access mode and returns stdout to the model.",
		ParityRefs:  []string{"Bash tool", "Shell command output"},
	},
	"bash_background_output_roundtrip": {
		Category:    "bash",
		Description: "Starts a background bash task and reads its output through BashOutput.",
		ParityRefs:  []string{"Bash tool", "BashOutput tool", "Background shell output", "Tool result roundtrip"},
	},
	"bash_kill_roundtrip": {
		Category:    "bash",
		Description: "Stops a running background bash task with KillBash and verifies stopped output status.",
		ParityRefs:  []string{"Bash tool", "BashOutput tool", "KillBash tool", "Background shell output"},
	},
	"powershell_stdout_roundtrip": {
		Category:    "powershell",
		Description: "Runs a PowerShell command through the agent tool loop and returns stdout to the model.",
		ParityRefs:  []string{"PowerShell tool", "Shell command output", "Tool result roundtrip"},
	},
	"bash_permission_prompt_approved": {
		Category:    "permissions",
		Description: "Exercises a bash escalation prompt that the user approves.",
		ParityRefs:  []string{"Permission prompts", "Bash approval"},
	},
	"bash_permission_prompt_denied": {
		Category:    "permissions",
		Description: "Exercises a bash escalation prompt that the user denies.",
		ParityRefs:  []string{"Permission prompts", "Bash denial"},
	},
	"permission_scope_denial_roundtrip": {
		Category:    "permissions",
		Description: "Verifies workspace-scope path escapes are denied with structured permission decisions and file-tool errors.",
		ParityRefs:  []string{"Permission enforcement", "Workspace scope denial", "Tool validation", "File tool denial"},
	},
	"plugin_tool_roundtrip": {
		Category:    "plugin-paths",
		Description: "Loads and executes an external plugin tool through the runtime registry.",
		ParityRefs:  []string{"Plugin tools", "External plugin lifecycle"},
	},
	"command_skill_template_roundtrip": {
		Category:    "command-workflows",
		Description: "Discovers and renders project slash commands, skills, and prompt templates without contacting a provider.",
		ParityRefs:  []string{"Slash commands", "Skills", "Templates", "Project workflow surfaces"},
	},
	"skill_activation_roundtrip": {
		Category:    "command-workflows",
		Description: "Runs the real skills CLI through enable, status, list rendering, disable, and persisted enabled-skill configuration.",
		ParityRefs:  []string{"Skills", "Skill activation", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"onboarding_bookmarks_roundtrip": {
		Category:    "command-workflows",
		Description: "Inspects workspace onboarding readiness and persists bookmark lifecycle state for resumable workflows.",
		ParityRefs:  []string{"Slash commands", "Onboarding", "Bookmarks", "Session context management", "Workspace state"},
	},
	"memory_lifecycle_roundtrip": {
		Category:    "context-management",
		Description: "Discovers, appends, searches, selects, and resets project memory instruction files.",
		ParityRefs:  []string{"Project memory", "Session context management", "Slash commands", "Workspace state"},
	},
	"prompt_directory_reference_roundtrip": {
		Category:    "context-management",
		Description: "Runs the real prompt CLI with an @directory reference and verifies recursive text context expansion plus skip metadata.",
		ParityRefs:  []string{"Prompt references", "Directory references", "Session context management", "Workspace state"},
	},
	"session_summary_roundtrip": {
		Category:    "context-management",
		Description: "Builds session summaries, previews tool activity, renders text, and preserves actionable auto-compaction summary context.",
		ParityRefs:  []string{"Session summary", "Compaction summary", "Session context management", "Token usage", "Tool result roundtrip"},
	},
	"context_view_roundtrip": {
		Category:    "context-management",
		Description: "Builds structured context summaries with memory, focused paths, token estimates, and text plus HTML rendering.",
		ParityRefs:  []string{"Context view", "Focused paths", "Context signals", "Session context management", "Workspace state"},
	},
	"theme_lifecycle_roundtrip": {
		Category:    "preferences",
		Description: "Runs the real theme CLI through local preference set, status, and reset operations.",
		ParityRefs:  []string{"Theme", "Theme persistence", "Theme reset", "Configuration"},
	},
	"interface_preferences_roundtrip": {
		Category:    "preferences",
		Description: "Runs real language and Vim preference CLI commands through set, status, text rendering, and reset operations.",
		ParityRefs:  []string{"Language preference", "Vim mode", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"privacy_keybindings_roundtrip": {
		Category:    "preferences",
		Description: "Runs privacy settings and keybindings CLI commands through persisted configuration, validation, resolution, and text rendering.",
		ParityRefs:  []string{"Privacy settings", "Prompt history", "Keybindings", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"browser_notifications_roundtrip": {
		Category:    "preferences",
		Description: "Runs Chrome, notifications, and telemetry preference CLI commands through persisted configuration, status, text rendering, and reset operations.",
		ParityRefs:  []string{"Chrome integration", "Notifications", "Telemetry", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"model_runtime_preferences_roundtrip": {
		Category:    "model-runtime",
		Description: "Runs reasoning effort, fast mode, and temperature preference CLI commands through persisted configuration, status, text rendering, and reset operations.",
		ParityRefs:  []string{"Reasoning effort", "Fast mode", "Temperature preference", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"model_selection_roundtrip": {
		Category:    "model-runtime",
		Description: "Runs the real model CLI through persisted model selection, status, model detail routing, and text rendering.",
		ParityRefs:  []string{"Model preference", "Model persistence", "Model routing detail", "Configuration", "Interactive rendering"},
	},
	"budget_lifecycle_roundtrip": {
		Category:    "model-runtime",
		Description: "Runs the real budget CLI through token and turn limit persistence, status, text rendering, and reset operations.",
		ParityRefs:  []string{"Token budget", "Turn limit", "Preference persistence", "Configuration", "Interactive rendering"},
	},
	"auth_credentials_roundtrip": {
		Category:    "auth",
		Description: "Runs API key, auth status, and setup-token CLI commands through persisted credential configuration with redacted command output.",
		ParityRefs:  []string{"Auth status", "API key", "Auth token", "Token redaction", "Credential persistence", "Configuration", "Interactive rendering"},
	},
	"output_style_lifecycle_roundtrip": {
		Category:    "interactive-ui",
		Description: "Loads, sets, injects, and clears output styles with workspace precedence over user and built-in styles.",
		ParityRefs:  []string{"Output styles", "Interactive rendering", "Workspace state"},
	},
	"diagnostics_status_roundtrip": {
		Category:    "diagnostics",
		Description: "Builds runtime status, doctor checks, and terminal setup reports from local configuration facts.",
		ParityRefs:  []string{"Doctor", "Status diagnostics", "Terminal setup", "Setup diagnostics"},
	},
	"statusline_cli_roundtrip": {
		Category:    "interactive-ui",
		Description: "Runs the real statusline CLI in JSON and text modes with configured model, permissions, and fast-mode state.",
		ParityRefs:  []string{"Statusline", "Statusline JSON", "Statusline text", "Interactive rendering"},
	},
	"tui_prompt_completion_roundtrip": {
		Category:    "interactive-ui",
		Description: "Renders the Bubble Tea prompt model, completes a slash command, and captures Enter submission state without a live terminal.",
		ParityRefs:  []string{"Bubble Tea TUI", "Interactive rendering", "Slash commands"},
	},
	"ask_user_question_roundtrip": {
		Category:    "interactive-ui",
		Description: "Asks a user question through the tool registry and resolves choices plus defaults.",
		ParityRefs:  []string{"AskUserQuestion tool", "Interactive questions", "Tool result roundtrip"},
	},
	"runtime_output_tools_roundtrip": {
		Category:    "runtime-tools",
		Description: "Runs user-facing message, structured output, and bounded sleep tools through the registry.",
		ParityRefs:  []string{"Brief tool", "SendUserMessage tool", "StructuredOutput tool", "Sleep tool"},
	},
	"repl_runtime_roundtrip": {
		Category:    "runtime-tools",
		Description: "Executes shell code through the REPL tool with workspace and configured environment.",
		ParityRefs:  []string{"REPL tool", "Runtime subprocess execution", "Tool result roundtrip"},
	},
	"auto_compact_triggered": {
		Category:    "session-compaction",
		Description: "Verifies auto-compaction fires when the message threshold is exceeded.",
		ParityRefs:  []string{"Auto-compaction", "Session context management"},
	},
	"token_cost_reporting": {
		Category:    "token-usage",
		Description: "Confirms actual usage tokens and estimated cost are reported.",
		ParityRefs:  []string{"Token usage", "Cost tracking"},
	},
	"config_precedence_roundtrip": {
		Category:    "config",
		Description: "Confirms config precedence across file, environment, and runtime overrides.",
		ParityRefs:  []string{"Configuration", "Precedence rules"},
	},
	"config_validation_status_roundtrip": {
		Category:    "config",
		Description: "Runs the real status CLI and verifies config validation is surfaced in status JSON and text.",
		ParityRefs:  []string{"Configuration", "Status diagnostics", "Config validation"},
	},
	"provider_routing_roundtrip": {
		Category:    "provider-routing",
		Description: "Confirms OpenAI-compatible, Ollama, DashScope, and custom base URL provider routing without contacting external providers.",
		ParityRefs:  []string{"Provider routing", "OpenAI-compatible APIs", "Ollama", "DashScope", "Custom base URLs"},
	},
	"session_resume_jsonl_roundtrip": {
		Category:    "session-resume",
		Description: "Loads previous JSONL session messages and sends them with the resumed prompt.",
		ParityRefs:  []string{"Session JSONL", "Resume"},
	},
	"resume_slash_command_roundtrip": {
		Category:    "session-resume",
		Description: "Runs direct and resumed /resume slash commands through the real CLI dispatcher.",
		ParityRefs:  []string{"Session JSONL", "Slash commands", "Resume"},
	},
	"session_export_path_safety_roundtrip": {
		Category:    "session-resume",
		Description: "Runs real session export paths and verifies filenames plus workspace-relative path traversal protection.",
		ParityRefs:  []string{"Session export", "Path safety", "Resume", "Workspace state"},
	},
	"bash_output_truncation_roundtrip": {
		Category:    "bash",
		Description: "Confirms large bash output is truncated before returning to the model.",
		ParityRefs:  []string{"Bash tool", "Output truncation"},
	},
	"sandbox_bypass_status_roundtrip": {
		Category:    "sandbox",
		Description: "Executes bash with Claude-compatible sandbox bypass and verifies structured sandbox status reporting.",
		ParityRefs:  []string{"Sandbox", "Bash tool", "Permission safety"},
	},
	"policy_update_sandbox_roundtrip": {
		Category:    "policy-safety",
		Description: "Evaluates enterprise policy, records an audit event, verifies a signed updater manifest, and resolves sandbox capability status.",
		ParityRefs:  []string{"Enterprise policy", "Audit events", "Signed updater", "Sandbox capability reporting"},
	},
	"policy_approval_roundtrip": {
		Category:    "policy-safety",
		Description: "Evaluates lane policy actions and exercises approval-token grant, verification, consumption, replay denial, and ledger listing.",
		ParityRefs:  []string{"Policy evaluation", "Approval tokens", "Delegation audit", "Replay denial", "Commit-scoped approval", "Execution chain traceability", "Tool result roundtrip"},
	},
	"notebook_read_edit_roundtrip": {
		Category:    "notebook",
		Description: "Reads a Jupyter notebook cell with outputs, edits notebook cells, and verifies the persisted notebook content.",
		ParityRefs:  []string{"Notebook read", "Notebook edit", "Code intelligence"},
	},
	"web_access_roundtrip": {
		Category:    "web-access",
		Description: "Fetches HTML content and searches a configured endpoint with domain filtering and source result blocks.",
		ParityRefs:  []string{"Web fetch", "Web search", "Sources block"},
	},
	"web_access_limits_roundtrip": {
		Category:    "web-access",
		Description: "Fetches bounded content and verifies filtered no-result search output.",
		ParityRefs:  []string{"Web fetch truncation", "Web search domain filters", "No-result source blocks"},
	},
	"git_workspace_roundtrip": {
		Category:    "git-workspace",
		Description: "Exercises git status, diff, log, show, blame, and stale-branch freshness checks against a local repository.",
		ParityRefs:  []string{"Git tools", "Branch freshness", "Workspace state"},
	},
	"git_preserve_state_roundtrip": {
		Category:    "git-workspace",
		Description: "Preserves issue and pull-request draft git state from a remote merge-base, including patch, committed format-patch data, and untracked files.",
		ParityRefs:  []string{"Git tools", "Issue draft", "Pull request draft", "Share", "Workspace state"},
	},
	"worktree_lifecycle_roundtrip": {
		Category:    "git-workspace",
		Description: "Allocates and removes a managed git worktree through the tool registry.",
		ParityRefs:  []string{"Git tools", "Worktree allocation", "Worktree cleanup", "Workspace state"},
	},
	"plan_todo_roundtrip": {
		Category:    "planning",
		Description: "Enters plan mode, persists a todo list, reads it back, and exits plan mode with the final plan.",
		ParityRefs:  []string{"Plan mode", "Todo tools", "Workspace state"},
	},
	"todo_completion_verification_roundtrip": {
		Category:    "planning",
		Description: "Completes a todo list, emits the verification nudge, and verifies persisted todo cleanup.",
		ParityRefs:  []string{"TodoWrite", "TodoRead", "Verification reminders"},
	},
	"lsp_static_roundtrip": {
		Category:    "code-intelligence",
		Description: "Queries static Go code intelligence through the LSP tool for document symbols, workspace symbols, workspace symbol resolve, definitions, declarations, type definitions, implementation lookup, highlights, folding ranges, selection ranges, monikers, linked editing ranges, document links, document colors, inlay hints, inline values, signature help, code lenses, semantic tokens, rename previews, code actions, organize imports, source fix-all, call hierarchy, type hierarchy, references, hover, completions, completion item resolve, execute command, diagnostics, and formatting.",
		ParityRefs:  []string{"LSP tool", "Code intelligence", "IDE bridge", "Workspace symbols", "Workspace symbol resolve", "Declarations", "Type definitions", "Implementation lookup", "Document highlights", "Folding ranges", "Selection ranges", "Monikers", "Linked editing ranges", "Document links", "Document colors", "Inlay hints", "Inline values", "Signature help", "Code lenses", "Semantic tokens", "Rename previews", "Code actions", "Organize imports", "Source fix all", "Call hierarchy", "Type hierarchy", "Completion item resolve", "Execute command", "Diagnostics"},
	},
	"lsp_cli_metadata_roundtrip": {
		Category:    "code-intelligence",
		Description: "Runs the real code-intel lsp CLI through action discovery, server candidate discovery, server listing, and text rendering.",
		ParityRefs:  []string{"LSP tool", "LSP metadata", "LSP action discovery", "Code intelligence", "Interactive rendering"},
	},
	"plugin_lifecycle_roundtrip": {
		Category:    "plugin-paths",
		Description: "Installs a plugin, executes init and shutdown lifecycle commands, and verifies enable/disable/remove state.",
		ParityRefs:  []string{"Plugin lifecycle", "Plugin manifest loading", "Plugin command execution"},
	},
	"task_lifecycle_roundtrip": {
		Category:    "background-agents",
		Description: "Creates, reads, updates, lists, and stops background tasks through the tool registry.",
		ParityRefs:  []string{"Background tasks", "Task create", "Task status", "Task output", "Task updates", "Task stop"},
	},
	"task_packet_roundtrip": {
		Category:    "task-packets",
		Description: "Creates a background task from a rich structured task packet and verifies scope resolution plus persisted packet metadata.",
		ParityRefs:  []string{"Task packet schema", "Task packet scope resolution", "Task packet persistence", "Tool result roundtrip"},
	},
	"team_cron_lifecycle_roundtrip": {
		Category:    "team-cron",
		Description: "Creates, inspects, and deletes team task groups plus scheduled cron task entries through the tool registry.",
		ParityRefs:  []string{"Team tools", "Team task assignment", "Cron tools", "Cron lifecycle", "Tool result roundtrip"},
	},
	"worker_lifecycle_roundtrip": {
		Category:    "workers",
		Description: "Creates a prompt worker, resolves trust, sends work, restarts, records completion, classifies startup timeout, and terminates it.",
		ParityRefs:  []string{"Worker tools", "Worker trust recovery", "Worker prompt delivery", "Worker startup diagnostics", "Tool result roundtrip"},
	},
	"recovery_lifecycle_roundtrip": {
		Category:    "recovery",
		Description: "Reads recovery recipes, records successful, exhausted, and partial recovery attempts, and verifies the ledger status surface.",
		ParityRefs:  []string{"Recovery recipes", "Recovery attempts", "Recovery ledger", "Escalation tracking", "Tool result roundtrip"},
	},
	"nudge_ack_dedupe_roundtrip": {
		Category:    "nudge",
		Description: "Records recurring nudge deliveries, classifies retries, acknowledges a cycle, and suppresses stale duplicates.",
		ParityRefs:  []string{"Nudge acknowledgement", "Nudge dedupe", "Recurring prompt idempotency", "Tool result roundtrip"},
	},
	"provisional_status_escalation_roundtrip": {
		Category:    "status-events",
		Description: "Deduplicates unchanged in-flight status and escalates it into a typed stale signal after its TTL policy.",
		ParityRefs:  []string{"Provisional status dedupe", "Status TTL policy", "Stale blocker signal", "Tool result roundtrip"},
	},
	"roadmap_pinpoint_lifecycle_roundtrip": {
		Category:    "roadmap",
		Description: "Files a roadmap pinpoint, preserves its stable id across updates, and drives lifecycle states to closure.",
		ParityRefs:  []string{"Stable roadmap id", "Roadmap lifecycle", "Pinpoint update", "Tool result roundtrip"},
	},
	"report_atomic_update_roundtrip": {
		Category:    "roadmap",
		Description: "Canonicalizes one logical dogfood update across misordered and partial chat transport fragments.",
		ParityRefs:  []string{"Atomic dogfood report", "Message part ordering", "Pinpoint update", "Mock parity report"},
	},
	"report_backpressure_roundtrip": {
		Category:    "roadmap",
		Description: "Generates delta-first dogfood reports, collapses unchanged backlog items, and preserves full snapshots.",
		ParityRefs:  []string{"Report backpressure", "Per-channel cursor", "Full snapshot", "Delta-first summary"},
	},
	"background_agent_run_roundtrip": {
		Category:    "background-agents",
		Description: "Runs, watches, heartbeats, restarts, supervises, and summarizes a background agent lane.",
		ParityRefs:  []string{"Background tasks", "Agent runs", "Lane board", "Supervisor restarts"},
	},
	"agent_markdown_definition_roundtrip": {
		Category:    "background-agents",
		Description: "Loads Claude Code-style Markdown agent definitions through the real agents CLI.",
		ParityRefs:  []string{"Agent definitions", "Markdown agents", "Claude Code migration", "Agent runs"},
	},
	"ssh_print_plan_roundtrip": {
		Category:    "remote-control",
		Description: "Builds a redacted SSH headless print plan and verifies the remote entrypoint uses prompt mode.",
		ParityRefs:  []string{"SSH remote sessions", "Headless print mode", "Remote sessions", "Claude Code migration"},
	},
	"remote_trigger_roundtrip": {
		Category:    "remote-control",
		Description: "Executes the remote trigger tool against a local control endpoint.",
		ParityRefs:  []string{"Remote sessions", "Control endpoint trigger"},
	},
	"remote_api_listener_roundtrip": {
		Category:    "remote-control",
		Description: "Starts the local control API handler and verifies public health plus authenticated session routes.",
		ParityRefs:  []string{"Remote sessions", "IDE bridge", "Control API listener"},
	},
	"remote_bridge_workspace_roundtrip": {
		Category:    "editor-bridge",
		Description: "Exercises authenticated remote workspace, file, session, and editor bridge operations through the control API.",
		ParityRefs:  []string{"IDE bridge", "Remote sessions", "Workspace file operations", "Editor selection"},
	},
	"mcp_lifecycle_roundtrip": {
		Category:    "mcp-lifecycle",
		Description: "Exercises HTTP MCP initialize, initialized notification, tool discovery, and tool invocation through the control API.",
		ParityRefs:  []string{"MCP client", "MCP lifecycle", "Control API MCP bridge"},
	},
	"mcp_tool_hook_roundtrip": {
		Category:    "mcp-lifecycle",
		Description: "Executes an MCP tool through the model loop with PreToolUse input updates and PostToolUse feedback.",
		ParityRefs:  []string{"MCP tool calls", "PreToolUse hooks", "PostToolUse hooks"},
	},
	"mcp_auth_oauth_refresh_roundtrip": {
		Category:    "mcp-auth",
		Description: "Exercises MCP auth diagnostics with an expired refreshable OAuth token and verifies redacted refresh output.",
		ParityRefs:  []string{"MCP auth", "OAuth refresh", "Token redaction"},
	},
	"mcp_auth_recovery_roundtrip": {
		Category:    "mcp-auth",
		Description: "Exercises MCP auth failure diagnostics, recovery actions, and secret redaction for missing and unauthorized servers.",
		ParityRefs:  []string{"MCP auth", "Recovery actions", "Error diagnostics", "Token redaction"},
	},
	"acp_stdio_roundtrip": {
		Category:    "editor-bridge",
		Description: "Runs the ACP/Zed stdio JSON-RPC handshake and verifies initialize, status, session creation, and shutdown responses.",
		ParityRefs:  []string{"ACP/Zed", "IDE bridge", "JSON-RPC stdio"},
	},
}

func scenarioErrorReport(name string, metadata scenarioMetadata, workspace string, err error) ScenarioReport {
	return ScenarioReport{
		Name:        name,
		Category:    metadata.Category,
		Description: metadata.Description,
		ParityRefs:  append([]string(nil), metadata.ParityRefs...),
		Workspace:   workspace,
		Error:       err.Error(),
	}
}

func toolUseNames(calls []runloop.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	return names
}

func toolErrorCount(calls []runloop.ToolCall) int {
	count := 0
	for _, call := range calls {
		if call.IsError {
			count++
		}
	}
	return count
}

func finalAssistantMessage(messages []anthropic.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		parts := make([]string, 0, len(messages[i].Content))
		for _, block := range messages[i].Content {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	return ""
}

func waitForBackgroundTask(ctx context.Context, store background.Store, id string, timeout time.Duration, ready func(background.Task) bool) (background.Task, error) {
	if ready == nil {
		return background.Task{}, errors.New("background task predicate is required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last background.Task
	var lastErr error
	for {
		task, err := store.Status(id)
		if err == nil {
			last = task
			if ready(task) {
				return task, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return last, fmt.Errorf("waiting for background task %s: %w", id, lastErr)
			}
			return last, fmt.Errorf("timed out waiting for background task %s", id)
		case <-ticker.C:
		}
	}
}

func waitForBackgroundLogs(ctx context.Context, store background.Store, id string, contains string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last string
	for {
		logs, err := store.Logs(id, 4096)
		if err == nil {
			last = logs
			if strings.Contains(logs, contains) {
				return logs, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("timed out waiting for background logs from %s", id)
		case <-ticker.C:
		}
	}
}

func configPrecedenceScenario() scenario {
	var loadedModel string
	var loadedPermission string
	var loadedSessionStart []string
	var loadedMCPShared string
	return scenario{
		name:   "config_precedence_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "config precedence harness ok"}},
		prompt: "verify config precedence",
		prepare: func(workspace string) ([]mockanthropic.Turn, func(), error) {
			previousCWD, err := os.Getwd()
			if err != nil {
				return nil, nil, err
			}
			previousConfigHome, hadConfigHome := os.LookupEnv("CODOG_CONFIG_HOME")
			cleanup := func() {
				_ = os.Chdir(previousCWD)
				if hadConfigHome {
					_ = os.Setenv("CODOG_CONFIG_HOME", previousConfigHome)
				} else {
					_ = os.Unsetenv("CODOG_CONFIG_HOME")
				}
			}
			configHome := filepath.Join(workspace, "config-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.Setenv("CODOG_CONFIG_HOME", configHome); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.WriteFile(filepath.Join(configHome, "config.json"), []byte(`{
				"model":"user-model",
				"permission_mode":"read-only",
				"additional_dirs":["user-dir"],
				"hooks":{"session_start":["echo user"]},
				"mcp_servers":{"shared":{"command":"user-shared"},"user_only":{"command":"user-only"}}
			}`), 0o644); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{
				"model":"project-model",
				"permission_mode":"workspace-write",
				"additional_dirs":["project-dir"],
				"hooks":{"SessionStart":[{"command":"echo project"}]},
				"mcp_servers":{"shared":{"command":"project-shared"},"project_only":{"command":"project-only"}}
			}`), 0o644); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.WriteFile(filepath.Join(workspace, ".codog.local.json"), []byte(`{
				"model":"local-model",
				"max_tokens":777,
				"additional_dirs":["local-dir"],
				"hooks":{"session_start":["echo local"]},
				"mcp_servers":{"shared":{"command":"local-shared"},"local_only":{"command":"local-only"}}
			}`), 0o644); err != nil {
				cleanup()
				return nil, nil, err
			}
			if err := os.Chdir(workspace); err != nil {
				cleanup()
				return nil, nil, err
			}
			cfg, paths, err := config.LoadForInspection(config.FlagOverrides{})
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			for _, expected := range []string{filepath.Join(configHome, "config.json"), ".codog.json", ".codog.local.json"} {
				if !slices.Contains(paths, expected) {
					cleanup()
					return nil, nil, fmt.Errorf("config path %q was not loaded; paths=%v", expected, paths)
				}
			}
			loadedModel = cfg.Model
			loadedPermission = cfg.PermissionMode
			loadedSessionStart = append([]string(nil), cfg.Hooks.SessionStart...)
			loadedMCPShared = cfg.MCPServers["shared"].Command
			if cfg.Model != "local-model" {
				cleanup()
				return nil, nil, fmt.Errorf("expected local model override, got %q", cfg.Model)
			}
			if cfg.PermissionMode != "workspace-write" {
				cleanup()
				return nil, nil, fmt.Errorf("expected project permission to survive, got %q", cfg.PermissionMode)
			}
			if cfg.MaxTokens != 777 {
				cleanup()
				return nil, nil, fmt.Errorf("expected local max_tokens override, got %d", cfg.MaxTokens)
			}
			if strings.Join(cfg.AdditionalDirs, ",") != "local-dir" {
				cleanup()
				return nil, nil, fmt.Errorf("expected local additional_dirs replacement, got %v", cfg.AdditionalDirs)
			}
			if strings.Join(cfg.Hooks.SessionStart, ",") != "echo user,echo project,echo local" {
				cleanup()
				return nil, nil, fmt.Errorf("unexpected hook merge order: %v", cfg.Hooks.SessionStart)
			}
			if cfg.MCPServers["shared"].Command != "local-shared" ||
				cfg.MCPServers["user_only"].Command != "user-only" ||
				cfg.MCPServers["project_only"].Command != "project-only" ||
				cfg.MCPServers["local_only"].Command != "local-only" {
				cleanup()
				return nil, nil, fmt.Errorf("unexpected mcp server merge: %#v", cfg.MCPServers)
			}
			return []mockanthropic.Turn{{Text: "config precedence harness ok"}}, cleanup, nil
		},
		verify: func(_ string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "config precedence harness ok") {
				return fmt.Errorf("missing config precedence final response")
			}
			if err := expectToolCalls(result, 0, false); err != nil {
				return err
			}
			if loadedModel != "local-model" || loadedPermission != "workspace-write" || loadedMCPShared != "local-shared" {
				return fmt.Errorf("unexpected loaded config model=%q permission=%q shared_mcp=%q", loadedModel, loadedPermission, loadedMCPShared)
			}
			if strings.Join(loadedSessionStart, ",") != "echo user,echo project,echo local" {
				return fmt.Errorf("unexpected loaded hook order: %v", loadedSessionStart)
			}
			return nil
		},
	}
}

func configValidationStatusScenario() scenario {
	return scenario{
		name: "config_validation_status_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configPath := filepath.Join(workspace, "config-warning.json")
			if err := os.WriteFile(configPath, []byte(`{"model":"claude-status","permission_mode":"workspace-write","modle":"typo"}`), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			var snapshot localstatus.Snapshot
			if err := json.Unmarshal([]byte(statusOut), &snapshot); err != nil {
				return localScenarioResult{}, err
			}
			if snapshot.ConfigValidation.Status != "warning" ||
				snapshot.ConfigValidation.FileCount != 1 ||
				snapshot.ConfigValidation.PresentCount != 1 ||
				snapshot.ConfigValidation.ErrorCount != 0 ||
				snapshot.ConfigValidation.WarningCount != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected config validation summary: %#v", snapshot.ConfigValidation)
			}
			if len(snapshot.ConfigValidation.Paths) != 1 || snapshot.ConfigValidation.Paths[0] != configPath {
				return localScenarioResult{}, fmt.Errorf("unexpected config validation paths: %v", snapshot.ConfigValidation.Paths)
			}
			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			expected := "Config validation status=warning files=1 present=1 errors=0 warnings=1"
			if !strings.Contains(textOut, expected) {
				return localScenarioResult{}, fmt.Errorf("missing status text config validation summary %q in %s", expected, textOut)
			}
			return localScenarioResult{
				Output:       statusOut,
				FinalMessage: "config validation status harness ok",
			}, nil
		},
	}
}

func editGlobLSScenario() scenario {
	return scenario{
		name:       "edit_glob_ls_roundtrip",
		permission: tools.PermissionWorkspace,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{
					ID:    "tool-1",
					Name:  "edit_file",
					Input: json.RawMessage(`{"path":"src/app.txt","old_string":"alpha","new_string":"beta"}`),
				},
				{
					ID:    "tool-2",
					Name:  "glob",
					Input: json.RawMessage(`{"pattern":"src/*.txt","limit":5}`),
				},
				{
					ID:    "tool-3",
					Name:  "ls",
					Input: json.RawMessage(`{"path":"src","limit":5}`),
				},
			}},
			{Text: "edit glob ls harness ok"},
		},
		prompt: "edit and inspect files",
		setup: func(workspace string) error {
			srcDir := filepath.Join(workspace, "src")
			if err := os.MkdirAll(srcDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(srcDir, "app.txt"), []byte("alpha\n"), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(srcDir, "notes.md"), []byte("notes\n"), 0o644)
		},
		verify: func(workspace string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "edit glob ls harness ok") {
				return fmt.Errorf("missing edit/glob/ls final response")
			}
			if err := expectToolCalls(result, 3, false); err != nil {
				return err
			}
			edited, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
			if err != nil {
				return err
			}
			if string(edited) != "beta\n" {
				return fmt.Errorf("edit_file did not persist edit, got %q", string(edited))
			}
			outputs := map[string]string{}
			for _, call := range result.ToolCalls {
				outputs[call.Name] = call.Output
			}
			for _, expected := range []string{`"replacements": 1`, `"oldString": "alpha"`, `"newString": "beta"`} {
				if !strings.Contains(outputs["edit_file"], expected) {
					return fmt.Errorf("edit_file output missing %s: %s", expected, outputs["edit_file"])
				}
			}
			for _, expected := range []string{`"files":`, `"filenames":`, `"numFiles": 1`, "src/app.txt"} {
				if !strings.Contains(outputs["glob"], expected) {
					return fmt.Errorf("glob output missing %s: %s", expected, outputs["glob"])
				}
			}
			for _, expected := range []string{`"kind": "ls"`, `"name": "app.txt"`, `"type": "file"`} {
				if !strings.Contains(outputs["ls"], expected) {
					return fmt.Errorf("ls output missing %s: %s", expected, outputs["ls"])
				}
			}
			return nil
		},
	}
}

func multiEditApplyPatchScenario() scenario {
	return scenario{
		name:       "multi_edit_apply_patch_roundtrip",
		permission: tools.PermissionWorkspace,
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{
					ID:    "tool-1",
					Name:  "multi_edit",
					Input: json.RawMessage(`{"path":"src/app.txt","edits":[{"old_string":"title: alpha","new_string":"title: beta"},{"old_string":"count = 1","new_string":"count = 2"}]}`),
				},
				{
					ID:    "tool-2",
					Name:  "apply_patch",
					Input: json.RawMessage(`{"patch":"--- a/src/app.txt\n+++ b/src/app.txt\n@@ -1,3 +1,4 @@\n title: beta\n count = 2\n status: draft\n+status_detail: patched"}`),
				},
			}},
			{Text: "multi edit apply patch harness ok"},
		},
		prompt: "perform multi edit and patch",
		setup: func(workspace string) error {
			srcDir := filepath.Join(workspace, "src")
			if err := os.MkdirAll(srcDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(srcDir, "app.txt"), []byte("title: alpha\ncount = 1\nstatus: draft\n"), 0o644)
		},
		verify: func(workspace string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "multi edit apply patch harness ok") {
				return fmt.Errorf("missing multi_edit/apply_patch final response")
			}
			if err := expectToolCalls(result, 2, false); err != nil {
				return err
			}
			updated, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
			if err != nil {
				return err
			}
			want := "title: beta\ncount = 2\nstatus: draft\nstatus_detail: patched\n"
			if string(updated) != want {
				return fmt.Errorf("unexpected patched file content: %q", string(updated))
			}
			outputs := map[string]string{}
			for _, call := range result.ToolCalls {
				outputs[call.Name] = call.Output
			}
			for _, expected := range []string{`"edits": 2`, `"replacements": 2`, `"undo_available": true`} {
				if !strings.Contains(outputs["multi_edit"], expected) {
					return fmt.Errorf("multi_edit output missing %s: %s", expected, outputs["multi_edit"])
				}
			}
			for _, expected := range []string{`"kind": "apply_patch"`, `"files_changed": 1`, `"operation": "update"`, `"path": "src/app.txt"`} {
				if !strings.Contains(outputs["apply_patch"], expected) {
					return fmt.Errorf("apply_patch output missing %s: %s", expected, outputs["apply_patch"])
				}
			}
			return nil
		},
	}
}

func commandSkillTemplateScenario() scenario {
	return scenario{
		name: "command_skill_template_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			commandDir := filepath.Join(workspace, ".codog", "commands")
			skillDir := filepath.Join(workspace, ".codog", "skills", "review")
			templateDir := filepath.Join(workspace, ".codog", "templates")
			for _, dir := range []string{commandDir, skillDir, templateDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return localScenarioResult{}, err
				}
			}

			commandDoc := `---
description: Review a target file.
argument-hint: TARGET
allowed-tools: read_file, grep
arguments: TARGET
---
Review $TARGET for session ${CLAUDE_SESSION_ID}.`
			if err := os.WriteFile(filepath.Join(commandDir, "review.md"), []byte(commandDoc), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			skillDoc := `---
name: review
description: Review project changes.
allowed-tools: read_file, grep
arguments: TARGET
paths: src/**, docs
---
Review skill body for $TARGET during ${CLAUDE_SESSION_ID}.`
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			templateDoc := `Release {{version}} for {{project}}.`
			if err := os.WriteFile(filepath.Join(templateDir, "release.md"), []byte(templateDoc), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			command, err := customcommands.Find(configHome, workspace, "review")
			if err != nil {
				return localScenarioResult{}, err
			}
			renderedCommand := customcommands.RenderWithSession(command, "src/main.go", "session-123")
			if renderedCommand.Source != "workspace" {
				return localScenarioResult{}, fmt.Errorf("unexpected command source %q", renderedCommand.Source)
			}
			if renderedCommand.Rendered != "Review src/main.go for session session-123." {
				return localScenarioResult{}, fmt.Errorf("unexpected rendered command: %s", renderedCommand.Rendered)
			}
			for _, expected := range []string{"read_file", "grep"} {
				if !slices.Contains(renderedCommand.AllowedTools, expected) {
					return localScenarioResult{}, fmt.Errorf("command allowed tools missing %s: %v", expected, renderedCommand.AllowedTools)
				}
			}

			skill, err := skills.Find(configHome, workspace, "review")
			if err != nil {
				return localScenarioResult{}, err
			}
			renderedSkill := skills.RenderInvocationWithSession(skill, "src/main.go", "session-123")
			for _, expected := range []string{`<skill name="review"`, "Review skill body for src/main.go during session-123.", "User request: src/main.go"} {
				if !strings.Contains(renderedSkill, expected) {
					return localScenarioResult{}, fmt.Errorf("rendered skill missing %s", expected)
				}
			}
			if !skills.MatchesAnyPath(skill, []string{"src/main.go"}) {
				return localScenarioResult{}, fmt.Errorf("skill paths did not match src/main.go")
			}
			if skills.MatchesAnyPath(skill, []string{"test/main.go"}) {
				return localScenarioResult{}, fmt.Errorf("skill paths unexpectedly matched test/main.go")
			}

			template, err := prompttemplates.Find(configHome, workspace, "release")
			if err != nil {
				return localScenarioResult{}, err
			}
			renderedTemplate, err := prompttemplates.Render(template, map[string]string{
				"project": "codog",
				"version": "1.0.0",
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if renderedTemplate.Rendered != "Release 1.0.0 for codog." {
				return localScenarioResult{}, fmt.Errorf("unexpected rendered template: %s", renderedTemplate.Rendered)
			}

			report := map[string]any{
				"kind": "command_skill_template",
				"command": map[string]any{
					"name":          renderedCommand.Name,
					"source":        renderedCommand.Source,
					"allowed_tools": renderedCommand.AllowedTools,
					"rendered":      renderedCommand.Rendered,
				},
				"skill": map[string]any{
					"name":          skill.Name,
					"source":        skill.Source,
					"allowed_tools": skill.AllowedTools,
					"paths":         skill.Paths,
					"matches_src":   skills.MatchesAnyPath(skill, []string{"src/main.go"}),
				},
				"template": map[string]any{
					"name":     renderedTemplate.Name,
					"source":   renderedTemplate.Source,
					"rendered": renderedTemplate.Rendered,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "command skill template harness ok",
				RequestCount: 3,
				MessageCount: 1,
			}, nil
		},
	}
}

func skillActivationScenario() scenario {
	return scenario{
		name: "skill_activation_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			skillDir := filepath.Join(workspace, ".codog", "skills", "review")
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			skillDoc := `---
name: review
description: Review project changes.
allowed-tools: read_file, grep
---
# Review

Review the requested change with repository context.
`
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
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

			initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			initial, err := decodeSkillActivationHarnessReport(initialOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initial.Kind != "skills" || initial.Action != "status" || initial.Status != "ok" || len(initial.EnabledSkills) != 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected initial skills status: %#v", initial)
			}
			if initial.AvailableSkillCount == 0 {
				return localScenarioResult{}, fmt.Errorf("expected at least one available skill in initial status")
			}

			enableOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "enable", "review", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			enabled, err := decodeSkillActivationHarnessReport(enableOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if enabled.Action != "enable" || !slices.Contains(enabled.EnabledSkills, "review") || !slices.Contains(enabled.Added, "review") {
				return localScenarioResult{}, fmt.Errorf("unexpected skills enable report: %#v", enabled)
			}
			if enabled.Path == "" || !strings.HasSuffix(enabled.Path, "codog-config.json") {
				return localScenarioResult{}, fmt.Errorf("unexpected skills enable path: %q", enabled.Path)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"enabled_skills":`) || !strings.Contains(string(configData), `"review"`) {
				return localScenarioResult{}, fmt.Errorf("enabled skills config did not persist review: %s", string(configData))
			}

			statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			status, err := decodeSkillActivationHarnessReport(statusOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if status.Action != "status" || status.Status != "ok" || !slices.Contains(status.EnabledSkills, "review") || !slices.Contains(status.ResolvedSkills, "review") || len(status.MissingSkills) != 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted skills status: %#v", status)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "skills", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "Skill Status") || !strings.Contains(textOut, "Enabled skills   review") || !strings.Contains(textOut, "All enabled skills resolved.") {
				return localScenarioResult{}, fmt.Errorf("skills status text missing expected values: %s", textOut)
			}

			disableOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "skills", "disable", "review", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			disabled, err := decodeSkillActivationHarnessReport(disableOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if disabled.Action != "disable" || len(disabled.EnabledSkills) != 0 || !slices.Contains(disabled.Removed, "review") {
				return localScenarioResult{}, fmt.Errorf("unexpected skills disable report: %#v", disabled)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), `"enabled_skills"`) {
				return localScenarioResult{}, fmt.Errorf("enabled skills config still present after disable: %s", string(configData))
			}

			report := map[string]any{
				"kind": "skill_activation",
				"skills": map[string]any{
					"available":        initial.AvailableSkillCount,
					"enabled":          enabled.EnabledSkills,
					"added":            enabled.Added,
					"resolved":         status.ResolvedSkills,
					"missing":          status.MissingSkills,
					"removed":          disabled.Removed,
					"final_enabled":    disabled.EnabledSkills,
					"path_persisted":   enabled.Path != "" && strings.HasSuffix(enabled.Path, "codog-config.json"),
					"text_rendered":    strings.Contains(textOut, "Enabled skills   review"),
					"config_unset":     !strings.Contains(string(configData), `"enabled_skills"`),
					"status_message":   status.Message,
					"disabled_message": disabled.Message,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "skill activation harness ok",
				RequestCount: 5,
				MessageCount: 1,
			}, nil
		},
	}
}

type skillActivationHarnessReport struct {
	Kind                string   `json:"kind"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Target              string   `json:"target,omitempty"`
	Path                string   `json:"path,omitempty"`
	EnabledSkills       []string `json:"enabled_skills"`
	Added               []string `json:"added,omitempty"`
	Removed             []string `json:"removed,omitempty"`
	Unchanged           []string `json:"unchanged,omitempty"`
	AvailableSkillCount int      `json:"available_skill_count,omitempty"`
	ResolvedSkills      []string `json:"resolved_skills,omitempty"`
	MissingSkills       []string `json:"missing_skills,omitempty"`
	Message             string   `json:"message,omitempty"`
}

func decodeSkillActivationHarnessReport(output string) (skillActivationHarnessReport, error) {
	var report skillActivationHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return report, err
	}
	return report, nil
}

func onboardingBookmarksScenario() scenario {
	return scenario{
		name: "onboarding_bookmarks_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/onboarding\n\ngo 1.25\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use focused changes.\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
				return localScenarioResult{}, err
			}

			onboardingReport, err := onboarding.Analyze(onboarding.Options{Workspace: workspace})
			if err != nil {
				return localScenarioResult{}, err
			}
			if onboardingReport.Status != "needs_setup" || !onboardingReport.HasReadme || !onboardingReport.HasTests || onboardingReport.PrimaryLanguage != "Go" {
				return localScenarioResult{}, fmt.Errorf("unexpected onboarding report: %#v", onboardingReport)
			}
			if !onboardingReport.GitRepository || !slices.Contains(onboardingReport.ReadmeFiles, "README.md") || !slices.Contains(onboardingReport.InstructionFiles, "AGENTS.md") {
				return localScenarioResult{}, fmt.Errorf("onboarding report missed repository files: %#v", onboardingReport)
			}
			if len(onboardingReport.Recommendations) == 0 {
				return localScenarioResult{}, fmt.Errorf("expected onboarding recommendation for missing codog config")
			}

			configHome := filepath.Join(workspace, "config-home")
			store := bookmarks.NewStore(configHome)
			messageIndex := 2
			created, err := store.Add(bookmarks.Bookmark{
				Name:         "review-start",
				Workspace:    workspace,
				SessionID:    "session-abc",
				MessageIndex: &messageIndex,
				PRRepo:       "Rememorio/codog",
				PRNumber:     42,
				Note:         "resume review",
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if created.ID == "" || created.Name != "review-start" || created.SessionID != "session-abc" {
				return localScenarioResult{}, fmt.Errorf("unexpected created bookmark: %#v", created)
			}
			listed, err := store.List(bookmarks.ListOptions{Workspace: workspace})
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(listed) != 1 || listed[0].ID != created.ID {
				return localScenarioResult{}, fmt.Errorf("unexpected bookmark list: %#v", listed)
			}
			shown, err := store.Get("review-start")
			if err != nil {
				return localScenarioResult{}, err
			}
			if shown.ID != created.ID || shown.PRNumber != 42 || shown.Note != "resume review" {
				return localScenarioResult{}, fmt.Errorf("unexpected shown bookmark: %#v", shown)
			}
			deleted, err := store.Delete(created.ID)
			if err != nil {
				return localScenarioResult{}, err
			}
			if deleted.ID != created.ID {
				return localScenarioResult{}, fmt.Errorf("unexpected deleted bookmark: %#v", deleted)
			}
			remaining, err := store.List(bookmarks.ListOptions{Workspace: workspace, All: true})
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(remaining) != 0 {
				return localScenarioResult{}, fmt.Errorf("expected no bookmarks after delete, got %#v", remaining)
			}

			report := map[string]any{
				"kind": "onboarding_bookmarks",
				"onboarding": map[string]any{
					"status":           onboardingReport.Status,
					"primary_language": onboardingReport.PrimaryLanguage,
					"has_readme":       onboardingReport.HasReadme,
					"has_tests":        onboardingReport.HasTests,
					"git_repository":   onboardingReport.GitRepository,
					"recommendations":  len(onboardingReport.Recommendations),
				},
				"bookmarks": map[string]any{
					"created":         created.Name,
					"listed":          len(listed),
					"shown":           shown.Name,
					"deleted":         deleted.Name,
					"remaining":       len(remaining),
					"message_index":   messageIndex,
					"pull_request":    shown.PRNumber,
					"config_home_set": configHome != "",
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "onboarding bookmarks harness ok",
				RequestCount: 2,
				MessageCount: 1,
			}, nil
		},
	}
}

func memoryLifecycleScenario() scenario {
	return scenario{
		name: "memory_lifecycle_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Prefer focused tests.\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			initial, err := memory.BuildReport(workspace)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initial.Kind != "memory" || initial.Action != "list" || initial.InstructionFiles != 1 || initial.Files[0].Name != "AGENTS.md" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial memory report: %#v", initial)
			}

			appendReport, err := memory.Append(workspace, "Remember to cite verification commands.")
			if err != nil {
				return localScenarioResult{}, err
			}
			if appendReport.Kind != "memory" || appendReport.Action != "add" || appendReport.Bytes == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected memory append report: %#v", appendReport)
			}

			search, err := memory.Search(workspace, "verification commands", 5)
			if err != nil {
				return localScenarioResult{}, err
			}
			if search.MatchCount != 1 || len(search.Matches) != 1 || search.Matches[0].Name != "AGENTS.md" {
				return localScenarioResult{}, fmt.Errorf("unexpected memory search report: %#v", search)
			}

			show, err := memory.Show(workspace, "AGENTS.md")
			if err != nil {
				return localScenarioResult{}, err
			}
			if show.File.Name != "AGENTS.md" || !strings.Contains(show.Body, "Remember to cite verification commands.") {
				return localScenarioResult{}, fmt.Errorf("unexpected memory show report: %#v", show)
			}

			selection, err := memory.Select(workspace, ".codog/instructions.md")
			if err != nil {
				return localScenarioResult{}, err
			}
			if selection.Kind != "memory" || selection.OptionCount < 2 || !strings.HasSuffix(filepath.ToSlash(selection.Selected), ".codog/instructions.md") {
				return localScenarioResult{}, fmt.Errorf("unexpected memory selection report: %#v", selection)
			}

			ensured, err := memory.Ensure(workspace, ".codog/instructions.md")
			if err != nil {
				return localScenarioResult{}, err
			}
			if ensured.Kind != "memory" || ensured.Action != "ensure" || !ensured.Created {
				return localScenarioResult{}, fmt.Errorf("unexpected memory ensure report: %#v", ensured)
			}

			reset, err := memory.Reset(workspace, memory.ResetOptions{Target: "AGENTS.md", Confirm: true})
			if err != nil {
				return localScenarioResult{}, err
			}
			if reset.Kind != "memory" || reset.ResetCount != 1 || reset.Files[0].Name != "AGENTS.md" || reset.Files[0].BytesRemoved == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected memory reset report: %#v", reset)
			}
			cleared, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(cleared) != 0 {
				return localScenarioResult{}, fmt.Errorf("expected AGENTS.md to be reset, got %q", string(cleared))
			}

			report := map[string]any{
				"kind": "memory_lifecycle",
				"list": map[string]any{
					"instruction_files": initial.InstructionFiles,
					"name":              initial.Files[0].Name,
				},
				"append": map[string]any{
					"bytes": appendReport.Bytes,
					"path":  filepath.ToSlash(appendReport.Path),
				},
				"search": map[string]any{
					"query":       search.Query,
					"match_count": search.MatchCount,
					"line":        search.Matches[0].Line,
				},
				"show": map[string]any{
					"name":     show.File.Name,
					"contains": strings.Contains(show.Body, "Remember to cite verification commands."),
				},
				"select": map[string]any{
					"option_count": selection.OptionCount,
					"selected":     filepath.ToSlash(selection.Selected),
				},
				"ensure": map[string]any{
					"created": ensured.Created,
					"path":    filepath.ToSlash(ensured.Path),
				},
				"reset": map[string]any{
					"reset_count":   reset.ResetCount,
					"bytes_removed": reset.Files[0].BytesRemoved,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "memory lifecycle harness ok",
				RequestCount: 6,
				MessageCount: 1,
			}, nil
		},
	}
}

func promptDirectoryReferenceScenario() scenario {
	return scenario{
		name: "prompt_directory_reference_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			captured := make(chan json.RawMessage, 1)
			server := httptest.NewServer(mockanthropic.Server{
				Text: "directory reference harness ok",
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
			if err := os.WriteFile(filepath.Join(workspace, "docs", "README.md"), []byte("# Reference Docs\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(docsDir, "guide.txt"), []byte("reference guide\n"), 0o644); err != nil {
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

			out, err := runHarnessCodog(ctx, workspace, "--config", configPath, "prompt", "Summarize @docs", "--output-format", "json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var promptReport struct {
				Response string `json:"response"`
			}
			if err := json.Unmarshal([]byte(out), &promptReport); err != nil {
				return localScenarioResult{}, err
			}
			if promptReport.Response != "directory reference harness ok" {
				return localScenarioResult{}, fmt.Errorf("unexpected directory reference response: %q", promptReport.Response)
			}
			var raw json.RawMessage
			select {
			case raw = <-captured:
			default:
				return localScenarioResult{}, fmt.Errorf("expected provider request for directory reference")
			}
			var body struct {
				Messages []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				return localScenarioResult{}, err
			}
			if len(body.Messages) != 1 || len(body.Messages[0].Content) == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected directory reference content: %s", string(raw))
			}
			text := body.Messages[0].Content[0].Text
			for _, expected := range []string{
				"Summarize @docs",
				"<codog_file_references>",
				`<directory path="docs" files="2"`,
				`<file path="README.md"`,
				"# Reference Docs",
				`<file path="nested/guide.txt"`,
				"reference guide",
				"<skipped>",
				"binary.bin",
			} {
				if !strings.Contains(text, expected) {
					return localScenarioResult{}, fmt.Errorf("directory reference missing %s: %s", expected, text)
				}
			}
			report := map[string]any{
				"kind": "prompt_directory_reference",
				"reference": map[string]any{
					"files":          2,
					"has_directory":  strings.Contains(text, `<directory path="docs"`),
					"has_nested":     strings.Contains(text, `nested/guide.txt`),
					"skipped_binary": strings.Contains(text, "binary.bin"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "directory reference harness ok",
				RequestCount: 1,
				MessageCount: 1,
			}, nil
		},
	}
}

func sessionSummaryScenario() scenario {
	return scenario{
		name: "session_summary_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			sessionPath := filepath.Join(workspace, ".codog", "sessions", "summary-session.jsonl")
			messages := []anthropic.Message{
				anthropic.TextMessage("user", "investigate failing tests in internal/runloop and keep the summary actionable"),
				{
					Role: "assistant",
					Content: []anthropic.ContentBlock{{
						Type:  "tool_use",
						ID:    "tool-1",
						Name:  "bash",
						Input: json.RawMessage(`{"command":"go test ./internal/runloop"}`),
					}},
				},
				anthropic.ToolResultMessage("tool-1", "package internal/runloop failed", true),
				anthropic.TextMessage("assistant", "The failure is in internal/runloop compaction handling."),
				anthropic.TextMessage("user", "also check session resume summaries before the next edit"),
			}
			report := sessionsummary.Build("summary-session", sessionPath, "claude-summary", messages)
			if report.Kind != "summary" || report.Action != "show" || report.Status != "ok" {
				return localScenarioResult{}, fmt.Errorf("unexpected session summary identity: %#v", report)
			}
			if report.MessageCount != len(messages) || report.UserMessages != 3 || report.AssistantMessages != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected session summary counts: %#v", report)
			}
			if report.ToolUses != 1 || report.ToolResults != 1 || report.ToolErrors != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected session summary tool counts: %#v", report)
			}
			if report.FirstUser == nil || !strings.Contains(report.FirstUser.Text, "investigate failing tests") {
				return localScenarioResult{}, fmt.Errorf("unexpected first user preview: %#v", report.FirstUser)
			}
			if report.LastUser == nil || !strings.Contains(report.LastUser.Text, "session resume summaries") {
				return localScenarioResult{}, fmt.Errorf("unexpected last user preview: %#v", report.LastUser)
			}
			if report.LastAssistant == nil || !strings.Contains(report.LastAssistant.Text, "compaction handling") {
				return localScenarioResult{}, fmt.Errorf("unexpected last assistant preview: %#v", report.LastAssistant)
			}
			if report.TokenEstimate.TotalTokens <= 0 {
				return localScenarioResult{}, fmt.Errorf("missing token estimate: %#v", report.TokenEstimate)
			}

			var text bytes.Buffer
			sessionsummary.RenderText(&text, report)
			textOutput := text.String()
			for _, expected := range []string{"Summary", "Session          summary-session", "Tool use         calls=1 results=1 errors=1", "session resume summaries"} {
				if !strings.Contains(textOutput, expected) {
					return localScenarioResult{}, fmt.Errorf("session summary text missing %q: %s", expected, textOutput)
				}
			}

			compaction := sessionsummary.BuildCompactionSummary(messages, 2)
			for _, expected := range []string{
				"auto-compacted",
				"- Current work: also check session resume summaries before the next edit",
				"- Last assistant response: The failure is in internal/runloop compaction handling.",
				"- Tools mentioned: bash",
				"- Tool results: 1 result message(s), 1 error result(s).",
			} {
				if !strings.Contains(compaction.Summary, expected) {
					return localScenarioResult{}, fmt.Errorf("compaction summary missing %q: %s", expected, compaction.Summary)
				}
			}
			if compaction.OriginalLines == 0 || compaction.CompressedLines == 0 || compaction.CompressedChars == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected compaction metrics: %#v", compaction)
			}

			output := map[string]any{
				"kind": "session_summary",
				"summary": map[string]any{
					"session_id":         report.SessionID,
					"message_count":      report.MessageCount,
					"user_messages":      report.UserMessages,
					"assistant_messages": report.AssistantMessages,
					"tool_uses":          report.ToolUses,
					"tool_results":       report.ToolResults,
					"tool_errors":        report.ToolErrors,
					"token_total":        report.TokenEstimate.TotalTokens,
					"text_rendered":      strings.Contains(textOutput, "Tool use         calls=1 results=1 errors=1"),
				},
				"compaction": map[string]any{
					"compressed_lines":       compaction.CompressedLines,
					"omitted_lines":          compaction.OmittedLines,
					"truncated":              compaction.Truncated,
					"has_current_work":       strings.Contains(compaction.Summary, "- Current work:"),
					"has_last_assistant":     strings.Contains(compaction.Summary, "- Last assistant response:"),
					"has_tool_summary":       strings.Contains(compaction.Summary, "- Tools mentioned: bash"),
					"has_tool_result_counts": strings.Contains(compaction.Summary, "- Tool results: 1 result message(s), 1 error result(s)."),
				},
			}
			data, err := json.Marshal(output)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "session summary harness ok",
				RequestCount: 2,
				MessageCount: 1,
			}, nil
		},
	}
}

func contextViewScenario() scenario {
	return scenario{
		name: "context_view_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			notesPath := filepath.Join(workspace, "notes.md")
			if err := os.WriteFile(notesPath, []byte("review context\nkeep tests focused\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			agentsPath := filepath.Join(workspace, "AGENTS.md")
			if err := os.WriteFile(agentsPath, []byte("Prefer focused tests.\nMention validation.\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			focusReport, err := focus.Add(workspace, []string{"notes.md"})
			if err != nil {
				return localScenarioResult{}, err
			}
			if focusReport.Kind != "focus" || focusReport.Action != "add" || focusReport.Total != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected focus report: %#v", focusReport)
			}

			statusReport := localstatus.Build(localstatus.Options{
				Version:               "dev",
				Workspace:             workspace,
				Model:                 "claude-test",
				PermissionMode:        "workspace-write",
				MaxTokens:             4096,
				MaxTurns:              8,
				AuthConfigured:        true,
				PlanActive:            true,
				PlanText:              "review context before edits",
				ToolNames:             []string{"bash", "read_file", "write_file"},
				SessionID:             "session-context",
				SessionMessages:       4,
				GitStatus:             "## main\n M notes.md\n",
				EnabledSkillCount:     2,
				SetupHookCount:        1,
				PreHookCount:          1,
				PostHookCount:         1,
				MemoryFiles:           []localstatus.MemoryFileStatus{{Path: "AGENTS.md", Name: "AGENTS.md", Scope: "workspace", Chars: 40, Lines: 2}},
				AllowedToolSource:     "default",
				AllowedToolEntries:    []string{"read_file", "write_file"},
				SandboxOS:             "darwin",
				SandboxDefault:        "seatbelt",
				SandboxAvailable:      true,
				RuntimeProvider:       "anthropic",
				RuntimeProviderSource: "config",
			})
			memoryReport := memory.Report{
				Kind:             "memory",
				Action:           "list",
				Status:           "ok",
				WorkingDirectory: workspace,
				InstructionFiles: 1,
				Files: []memory.Summary{{
					Path:      "AGENTS.md",
					Name:      "AGENTS.md",
					Scope:     "workspace",
					Lines:     2,
					Words:     5,
					Chars:     40,
					SizeBytes: 40,
					Preview:   "Prefer focused tests.",
				}},
			}
			contextReport := contextview.Build(contextview.Options{
				Status:       statusReport,
				Memory:       memoryReport,
				Focus:        focusReport,
				TokenUsage:   usage.Summary{InputTokens: 120, OutputTokens: 30, TotalTokens: 150, EstimatedUSD: 0.00042, Source: "actual"},
				SystemPrompt: "system line one\nsystem line two",
				Warnings:     []string{"context budget near threshold"},
			})
			if contextReport.Kind != "context" || contextReport.Action != "show" || contextReport.Status != "degraded" {
				return localScenarioResult{}, fmt.Errorf("unexpected context report identity: %#v", contextReport)
			}
			if contextReport.Memory.InstructionFiles != 1 || contextReport.Focus.FocusedPaths != 1 || contextReport.Prompt.Lines != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected context counts: %#v", contextReport)
			}
			for _, expected := range []string{
				"context budget near threshold",
				"git working tree has local changes",
				"plan mode is active; tool permissions are read-only",
			} {
				if !slices.Contains(contextReport.Signals, expected) {
					return localScenarioResult{}, fmt.Errorf("context report missing signal %q: %#v", expected, contextReport.Signals)
				}
			}

			var text bytes.Buffer
			contextview.RenderText(&text, contextReport)
			textOutput := text.String()
			for _, expected := range []string{"Context", "Plan             active", "Memory files     1", "Focused paths    1", "notes.md"} {
				if !strings.Contains(textOutput, expected) {
					return localScenarioResult{}, fmt.Errorf("context text missing %q: %s", expected, textOutput)
				}
			}
			htmlOutput := contextview.RenderHTML(contextReport)
			for _, expected := range []string{"<!doctype html>", "Codog Context", "Estimated tokens", "context budget near threshold"} {
				if !strings.Contains(htmlOutput, expected) {
					return localScenarioResult{}, fmt.Errorf("context html missing %q", expected)
				}
			}

			report := map[string]any{
				"kind": "context_view",
				"context": map[string]any{
					"status":            contextReport.Status,
					"workspace_name":    contextReport.Workspace.Name,
					"memory_files":      contextReport.Memory.InstructionFiles,
					"focused_paths":     contextReport.Focus.FocusedPaths,
					"prompt_lines":      contextReport.Prompt.Lines,
					"signals":           len(contextReport.Signals),
					"plan_active":       contextReport.Plan.Active,
					"token_total":       contextReport.TokenEstimate.TotalTokens,
					"text_rendered":     strings.Contains(textOutput, "Focused paths    1"),
					"html_rendered":     strings.Contains(htmlOutput, "Codog Context"),
					"git_dirty_signal":  slices.Contains(contextReport.Signals, "git working tree has local changes"),
					"plan_mode_signal":  slices.Contains(contextReport.Signals, "plan mode is active; tool permissions are read-only"),
					"warning_preserved": slices.Contains(contextReport.Signals, "context budget near threshold"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "context view harness ok",
				RequestCount: 4,
				MessageCount: 1,
			}, nil
		},
	}
}

func themeLifecycleScenario() scenario {
	return scenario{
		name: "theme_lifecycle_roundtrip",
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
			initialOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme")
			if err != nil {
				return localScenarioResult{}, err
			}
			initial, err := decodeThemeHarnessReport(initialOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initial.Kind != "theme" || initial.Action != "status" || initial.Theme != "default" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial theme report: %#v", initial)
			}
			if !slices.Contains(initial.Available, "dark") || !slices.Contains(initial.Available, "light") {
				return localScenarioResult{}, fmt.Errorf("theme list missing expected values: %#v", initial.Available)
			}

			setOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme", "set", "dark", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setReport, err := decodeThemeHarnessReport(setOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setReport.Action != "set" || setReport.Theme != "dark" || setReport.Previous != "default" {
				return localScenarioResult{}, fmt.Errorf("unexpected set theme report: %#v", setReport)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"theme": "dark"`) && !strings.Contains(string(configData), `"theme":"dark"`) {
				return localScenarioResult{}, fmt.Errorf("theme config did not persist dark theme: %s", string(configData))
			}

			statusOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusReport, err := decodeThemeHarnessReport(statusOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusReport.Action != "status" || statusReport.Theme != "dark" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted theme status: %#v", statusReport)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "theme", "list")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "Theme") || !strings.Contains(textOut, "Available") || !strings.Contains(textOut, "dark") {
				return localScenarioResult{}, fmt.Errorf("theme text output missing expected values: %s", textOut)
			}

			clearOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "theme", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearReport, err := decodeThemeHarnessReport(clearOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearReport.Action != "clear" || clearReport.Theme != "default" || clearReport.Previous != "dark" {
				return localScenarioResult{}, fmt.Errorf("unexpected clear theme report: %#v", clearReport)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), `"theme"`) {
				return localScenarioResult{}, fmt.Errorf("theme config still contains theme after clear: %s", string(configData))
			}

			report := map[string]any{
				"kind": "theme_lifecycle",
				"theme": map[string]any{
					"initial":        initial.Theme,
					"set":            setReport.Theme,
					"previous":       setReport.Previous,
					"status":         statusReport.Theme,
					"cleared":        clearReport.Theme,
					"clear_previous": clearReport.Previous,
					"path_persisted": setReport.Path != "" && strings.HasSuffix(setReport.Path, "codog-config.json"),
					"text_rendered":  strings.Contains(textOut, "Available"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "theme lifecycle harness ok",
				RequestCount: 5,
				MessageCount: 1,
			}, nil
		},
	}
}

type themeHarnessReport struct {
	Kind      string   `json:"kind"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Theme     string   `json:"theme"`
	Previous  string   `json:"previous,omitempty"`
	Path      string   `json:"path,omitempty"`
	Available []string `json:"available"`
}

func decodeThemeHarnessReport(output string) (themeHarnessReport, error) {
	var report themeHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return themeHarnessReport{}, err
	}
	return report, nil
}

func interfacePreferencesScenario() scenario {
	return scenario{
		name: "interface_preferences_roundtrip",
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

			initialLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialLanguage, err := decodeLanguageHarnessReport(initialLanguageOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialLanguage.Kind != "language" || initialLanguage.Action != "status" || initialLanguage.Configured || initialLanguage.Language != "" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial language report: %#v", initialLanguage)
			}

			setLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "use", "Japanese", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setLanguage, err := decodeLanguageHarnessReport(setLanguageOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setLanguage.Action != "set" || !setLanguage.Configured || setLanguage.Language != "Japanese" {
				return localScenarioResult{}, fmt.Errorf("unexpected set language report: %#v", setLanguage)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"language": "Japanese"`) && !strings.Contains(string(configData), `"language":"Japanese"`) {
				return localScenarioResult{}, fmt.Errorf("language config did not persist: %s", string(configData))
			}

			statusLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "view")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusLanguage, err := decodeLanguageHarnessReport(statusLanguageOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusLanguage.Action != "status" || !statusLanguage.Configured || statusLanguage.Language != "Japanese" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted language report: %#v", statusLanguage)
			}

			languageText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "language", "view")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(languageText, "Language") || !strings.Contains(languageText, "Japanese") {
				return localScenarioResult{}, fmt.Errorf("language text output missing expected values: %s", languageText)
			}

			clearLanguageOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "language", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearLanguage, err := decodeLanguageHarnessReport(clearLanguageOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearLanguage.Action != "clear" || clearLanguage.Configured || clearLanguage.Language != "" || clearLanguage.Previous != "Japanese" {
				return localScenarioResult{}, fmt.Errorf("unexpected clear language report: %#v", clearLanguage)
			}

			initialVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialVim, err := decodeVimHarnessReport(initialVimOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialVim.Kind != "vim" || initialVim.Action != "status" || initialVim.Enabled || initialVim.EditorMode != "default" {
				return localScenarioResult{}, fmt.Errorf("unexpected initial vim report: %#v", initialVim)
			}

			setVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "on", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setVim, err := decodeVimHarnessReport(setVimOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setVim.Action != "set" || !setVim.Enabled || setVim.EditorMode != "vim" || setVim.Previous != "default" {
				return localScenarioResult{}, fmt.Errorf("unexpected set vim report: %#v", setVim)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"editorMode": "vim"`) && !strings.Contains(string(configData), `"editorMode":"vim"`) {
				return localScenarioResult{}, fmt.Errorf("vim config did not persist: %s", string(configData))
			}

			statusVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusVim, err := decodeVimHarnessReport(statusVimOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusVim.Action != "status" || !statusVim.Enabled || statusVim.EditorMode != "vim" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted vim report: %#v", statusVim)
			}

			vimText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "vim", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(vimText, "Vim") || !strings.Contains(vimText, "Editor mode") || !strings.Contains(vimText, "vim") {
				return localScenarioResult{}, fmt.Errorf("vim text output missing expected values: %s", vimText)
			}

			clearVimOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "vim", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearVim, err := decodeVimHarnessReport(clearVimOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearVim.Action != "clear" || clearVim.Enabled || clearVim.EditorMode != "default" || clearVim.Previous != "vim" {
				return localScenarioResult{}, fmt.Errorf("unexpected clear vim report: %#v", clearVim)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), `"language"`) || strings.Contains(string(configData), `"editorMode"`) {
				return localScenarioResult{}, fmt.Errorf("interface preferences still persisted after clear: %s", string(configData))
			}

			report := map[string]any{
				"kind": "interface_preferences",
				"language": map[string]any{
					"initial_configured": initialLanguage.Configured,
					"set":                setLanguage.Language,
					"status":             statusLanguage.Language,
					"cleared":            clearLanguage.Language,
					"clear_previous":     clearLanguage.Previous,
					"path_persisted":     setLanguage.Path != "" && strings.HasSuffix(setLanguage.Path, "codog-config.json"),
					"text_rendered":      strings.Contains(languageText, "Japanese"),
				},
				"vim": map[string]any{
					"initial_enabled": initialVim.Enabled,
					"set":             setVim.EditorMode,
					"status":          statusVim.EditorMode,
					"cleared":         clearVim.EditorMode,
					"clear_previous":  clearVim.Previous,
					"path_persisted":  setVim.Path != "" && strings.HasSuffix(setVim.Path, "codog-config.json"),
					"text_rendered":   strings.Contains(vimText, "Editor mode"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "interface preferences harness ok",
				RequestCount: 8,
				MessageCount: 1,
			}, nil
		},
	}
}

type languageHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	Language   string `json:"language"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

func decodeLanguageHarnessReport(output string) (languageHarnessReport, error) {
	var report languageHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return languageHarnessReport{}, err
	}
	return report, nil
}

type vimHarnessReport struct {
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	EditorMode string `json:"editor_mode"`
	Previous   string `json:"previous,omitempty"`
	Path       string `json:"path,omitempty"`
}

func decodeVimHarnessReport(output string) (vimHarnessReport, error) {
	var report vimHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return vimHarnessReport{}, err
	}
	return report, nil
}

func privacyKeybindingsScenario() scenario {
	return scenario{
		name: "privacy_keybindings_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, ".codog-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{
				"config_home": configHome,
				"editorMode":  "vim",
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			initialPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialPrivacy, err := decodePrivacyHarnessReport(initialPrivacyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialPrivacy.Kind != "privacy_settings" || initialPrivacy.Action != "show" || !initialPrivacy.Settings["prompt_history_enabled"] {
				return localScenarioResult{}, fmt.Errorf("unexpected initial privacy report: %#v", initialPrivacy)
			}

			setPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "set", "prompt-history", "off", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setPrivacy, err := decodePrivacyHarnessReport(setPrivacyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setPrivacy.Action != "set" || setPrivacy.Key != "prompt_history_enabled" || setPrivacy.Value == nil || *setPrivacy.Value || setPrivacy.Settings["prompt_history_enabled"] {
				return localScenarioResult{}, fmt.Errorf("unexpected set privacy report: %#v", setPrivacy)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"prompt_history_enabled": false`) && !strings.Contains(string(configData), `"prompt_history_enabled":false`) {
				return localScenarioResult{}, fmt.Errorf("privacy config did not persist prompt-history setting: %s", string(configData))
			}

			statusPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "show")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusPrivacy, err := decodePrivacyHarnessReport(statusPrivacyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusPrivacy.Action != "show" || statusPrivacy.Settings["prompt_history_enabled"] {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted privacy report: %#v", statusPrivacy)
			}

			privacyText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "privacy-settings")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(privacyText, "Privacy Settings") || !strings.Contains(privacyText, "Prompt history") || !strings.Contains(privacyText, "disabled") {
				return localScenarioResult{}, fmt.Errorf("privacy text output missing expected values: %s", privacyText)
			}

			clearPrivacyOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "privacy-settings", "clear", "prompt-history", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearPrivacy, err := decodePrivacyHarnessReport(clearPrivacyOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearPrivacy.Action != "clear" || clearPrivacy.Key != "prompt_history_enabled" || !clearPrivacy.Settings["prompt_history_enabled"] {
				return localScenarioResult{}, fmt.Errorf("unexpected clear privacy report: %#v", clearPrivacy)
			}

			initialKeybindingsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialKeybindings, err := decodeKeybindingsHarnessReport(initialKeybindingsOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialKeybindings.Kind != "keybindings" || initialKeybindings.Action != "show" || !initialKeybindings.VimMode || initialKeybindings.KeybindingsExists {
				return localScenarioResult{}, fmt.Errorf("unexpected initial keybindings report: %#v", initialKeybindings)
			}

			pathOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "path")
			if err != nil {
				return localScenarioResult{}, err
			}
			pathReport, err := decodeKeybindingsFileHarnessReport(pathOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if pathReport.Action != "path" || pathReport.Path != filepath.Join(configHome, "keybindings.json") || pathReport.Exists {
				return localScenarioResult{}, fmt.Errorf("unexpected keybindings path report: %#v", pathReport)
			}

			initOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "init")
			if err != nil {
				return localScenarioResult{}, err
			}
			initReport, err := decodeKeybindingsFileHarnessReport(initOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initReport.Action != "init" || initReport.Status != "created" || !initReport.Created || !initReport.Exists {
				return localScenarioResult{}, fmt.Errorf("unexpected keybindings init report: %#v", initReport)
			}
			keybindingsData, err := os.ReadFile(pathReport.Path)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(keybindingsData), `"context": "repl"`) || !strings.Contains(string(keybindingsData), `"ctrl+r"`) || !strings.Contains(string(keybindingsData), `"shift+enter"`) || !strings.Contains(string(keybindingsData), `"ctrl+s"`) || !strings.Contains(string(keybindingsData), `"ctrl+g"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+e"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+k"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+c"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+u"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+s"`) || !strings.Contains(string(keybindingsData), `"ctrl+x ctrl+y"`) || !strings.Contains(string(keybindingsData), `"ctrl+x backspace"`) || !strings.Contains(string(keybindingsData), `"ctrl+_"`) || !strings.Contains(string(keybindingsData), `"ctrl+shift+-"`) || !strings.Contains(string(keybindingsData), `"ctrl+v"`) || !strings.Contains(string(keybindingsData), `"ctrl+shift+p"`) || !strings.Contains(string(keybindingsData), `"ctrl+p"`) || !strings.Contains(string(keybindingsData), `"ctrl+shift+f"`) || !strings.Contains(string(keybindingsData), `"ctrl+f"`) || !strings.Contains(string(keybindingsData), `"alt+p"`) || !strings.Contains(string(keybindingsData), `"alt+o"`) || !strings.Contains(string(keybindingsData), `"alt+t"`) || !strings.Contains(string(keybindingsData), `"shift+up"`) || !strings.Contains(string(keybindingsData), `"ctrl+o"`) || !strings.Contains(string(keybindingsData), `"ctrl+l"`) || !strings.Contains(string(keybindingsData), `"ctrl+d"`) || !strings.Contains(string(keybindingsData), `"ctrl+b"`) || !strings.Contains(string(keybindingsData), `"ctrl+t"`) || !strings.Contains(string(keybindingsData), `"ctrl+shift+t"`) || !strings.Contains(string(keybindingsData), `"up"`) {
				return localScenarioResult{}, fmt.Errorf("keybindings template missing expected entries: %s", string(keybindingsData))
			}

			editorLog := filepath.Join(workspace, "keybindings-editor.log")
			editorScript := filepath.Join(workspace, "keybindings-editor.sh")
			if err := os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.Remove(pathReport.Path); err != nil {
				return localScenarioResult{}, err
			}
			openOut, err := runHarnessCodogWithEnv(ctx, workspace, []string{"VISUAL=" + editorScript + " " + editorLog}, "--config", configPath, "--output-format", "json", "keybindings", "open")
			if err != nil {
				return localScenarioResult{}, err
			}
			openReport, err := decodeKeybindingsFileHarnessReport(openOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			openedPath, err := os.ReadFile(editorLog)
			if err != nil {
				return localScenarioResult{}, err
			}
			if openReport.Action != "open" || openReport.Status != "created_opened" || !openReport.Created || !openReport.Opened || string(openedPath) != pathReport.Path+"\n" {
				return localScenarioResult{}, fmt.Errorf("unexpected keybindings open report: %#v opened=%q", openReport, string(openedPath))
			}
			keybindingsData, err = os.ReadFile(pathReport.Path)
			if err != nil {
				return localScenarioResult{}, err
			}

			validateOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "validate")
			if err != nil {
				return localScenarioResult{}, err
			}
			validateReport, err := decodeKeybindingsValidationHarnessReport(validateOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if validateReport.Action != "validate" || !validateReport.Valid || validateReport.ContextCount != 4 || validateReport.BindingCount != 51 {
				return localScenarioResult{}, fmt.Errorf("unexpected keybindings validate report: %#v", validateReport)
			}

			resolveOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "keybindings", "resolve", "repl", "Control-R")
			if err != nil {
				return localScenarioResult{}, err
			}
			resolveReport, err := decodeKeybindingsResolveHarnessReport(resolveOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if resolveReport.Action != "resolve" || !resolveReport.Found || resolveReport.Source != "user" || resolveReport.NormalizedKey != "ctrl+r" || resolveReport.BindingAction != "reverse search prompt history" {
				return localScenarioResult{}, fmt.Errorf("unexpected keybindings resolve report: %#v", resolveReport)
			}

			keybindingsText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "keybindings")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(keybindingsText, "Keybindings") || !strings.Contains(keybindingsText, "Editor mode      vim") || !strings.Contains(keybindingsText, "User valid       true") {
				return localScenarioResult{}, fmt.Errorf("keybindings text output missing expected values: %s", keybindingsText)
			}

			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(configData), `"prompt_history_enabled"`) {
				return localScenarioResult{}, fmt.Errorf("privacy config still contains prompt_history_enabled after clear: %s", string(configData))
			}

			report := map[string]any{
				"kind": "privacy_keybindings",
				"privacy": map[string]any{
					"initial_prompt_history": initialPrivacy.Settings["prompt_history_enabled"],
					"set_prompt_history":     setPrivacy.Settings["prompt_history_enabled"],
					"status_prompt_history":  statusPrivacy.Settings["prompt_history_enabled"],
					"cleared_prompt_history": clearPrivacy.Settings["prompt_history_enabled"],
					"path_persisted":         setPrivacy.Path != "" && strings.HasSuffix(setPrivacy.Path, "codog-config.json"),
					"text_rendered":          strings.Contains(privacyText, "Prompt history"),
				},
				"keybindings": map[string]any{
					"initial_exists":        initialKeybindings.KeybindingsExists,
					"path":                  strings.HasSuffix(pathReport.Path, "keybindings.json"),
					"created":               initReport.Created,
					"opened":                openReport.Opened,
					"valid":                 validateReport.Valid,
					"contexts":              validateReport.ContextCount,
					"bindings":              validateReport.BindingCount,
					"shift_enter":           strings.Contains(string(keybindingsData), `"shift+enter"`),
					"prompt_stash_key":      strings.Contains(string(keybindingsData), `"ctrl+s"`),
					"external_editor":       strings.Contains(string(keybindingsData), `"ctrl+g"`),
					"external_editor_chord": strings.Contains(string(keybindingsData), `"ctrl+x ctrl+e"`),
					"kill_agents_chord":     strings.Contains(string(keybindingsData), `"ctrl+x ctrl+k"`),
					"compact_chord":         strings.Contains(string(keybindingsData), `"ctrl+x ctrl+c"`),
					"undo_change_chord":     strings.Contains(string(keybindingsData), `"ctrl+x ctrl+u"`),
					"export_chord":          strings.Contains(string(keybindingsData), `"ctrl+x ctrl+s"`),
					"copy_chord":            strings.Contains(string(keybindingsData), `"ctrl+x ctrl+y"`),
					"attachment_remove_key": strings.Contains(string(keybindingsData), `"ctrl+x backspace"`),
					"composer_undo_key":     strings.Contains(string(keybindingsData), `"ctrl+_"`) && strings.Contains(string(keybindingsData), `"ctrl+shift+-"`),
					"clipboard_paste_key":   strings.Contains(string(keybindingsData), `"ctrl+v"`),
					"quick_open_key":        strings.Contains(string(keybindingsData), `"ctrl+shift+p"`) && strings.Contains(string(keybindingsData), `"ctrl+p"`),
					"global_search_key":     strings.Contains(string(keybindingsData), `"ctrl+shift+f"`) && strings.Contains(string(keybindingsData), `"ctrl+f"`),
					"runtime_control_keys":  strings.Contains(string(keybindingsData), `"alt+p"`) && strings.Contains(string(keybindingsData), `"alt+o"`) && strings.Contains(string(keybindingsData), `"alt+t"`),
					"message_actions_key":   strings.Contains(string(keybindingsData), `"shift+up"`),
					"transcript_key":        strings.Contains(string(keybindingsData), `"ctrl+o"`),
					"terminal_keys":         strings.Contains(string(keybindingsData), `"ctrl+l"`) && strings.Contains(string(keybindingsData), `"ctrl+d"`),
					"background_key":        strings.Contains(string(keybindingsData), `"ctrl+b"`),
					"todo_panel_key":        strings.Contains(string(keybindingsData), `"ctrl+t"`),
					"task_board_key":        strings.Contains(string(keybindingsData), `"ctrl+shift+t"`),
					"queue_edit_key":        strings.Contains(string(keybindingsData), `"up"`),
					"resolved":              resolveReport.Found,
					"source":                resolveReport.Source,
					"text_rendered":         strings.Contains(keybindingsText, "User valid"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "privacy keybindings harness ok",
				RequestCount: 12,
				MessageCount: 1,
			}, nil
		},
	}
}

type privacyHarnessReport struct {
	Kind     string          `json:"kind"`
	Action   string          `json:"action"`
	Status   string          `json:"status"`
	Settings map[string]bool `json:"settings"`
	Key      string          `json:"key,omitempty"`
	Value    *bool           `json:"value,omitempty"`
	Path     string          `json:"path,omitempty"`
}

func decodePrivacyHarnessReport(output string) (privacyHarnessReport, error) {
	var report privacyHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return privacyHarnessReport{}, err
	}
	return report, nil
}

type keybindingsHarnessReport struct {
	Kind              string                              `json:"kind"`
	Action            string                              `json:"action"`
	Status            string                              `json:"status"`
	EditorMode        string                              `json:"editor_mode"`
	VimMode           bool                                `json:"vim_mode"`
	KeybindingsPath   string                              `json:"keybindings_path,omitempty"`
	KeybindingsExists bool                                `json:"keybindings_exists"`
	UserBindings      *keybindingsValidationHarnessReport `json:"user_bindings,omitempty"`
	Sections          []keybindingsSectionHarnessReport   `json:"sections,omitempty"`
}

type keybindingsFileHarnessReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Exists  bool   `json:"exists"`
	Opened  bool   `json:"opened,omitempty"`
}

type keybindingsValidationHarnessReport struct {
	Kind         string                            `json:"kind"`
	Action       string                            `json:"action"`
	Status       string                            `json:"status"`
	Path         string                            `json:"path"`
	Exists       bool                              `json:"exists"`
	Valid        bool                              `json:"valid"`
	ContextCount int                               `json:"context_count"`
	BindingCount int                               `json:"binding_count"`
	Errors       []string                          `json:"errors,omitempty"`
	Sections     []keybindingsSectionHarnessReport `json:"sections,omitempty"`
}

type keybindingsSectionHarnessReport struct {
	Name     string                          `json:"name"`
	Entries  []keybindingsEntryHarnessReport `json:"entries"`
	Disabled bool                            `json:"disabled,omitempty"`
}

type keybindingsEntryHarnessReport struct {
	Key           string `json:"key"`
	NormalizedKey string `json:"normalized_key,omitempty"`
	Action        string `json:"action"`
	Mode          string `json:"mode,omitempty"`
	Description   string `json:"description,omitempty"`
}

type keybindingsResolveHarnessReport struct {
	Kind          string   `json:"kind"`
	Action        string   `json:"action"`
	Status        string   `json:"status"`
	Context       string   `json:"context"`
	Key           string   `json:"key"`
	NormalizedKey string   `json:"normalized_key"`
	Found         bool     `json:"found"`
	Source        string   `json:"source,omitempty"`
	BindingAction string   `json:"binding_action,omitempty"`
	Section       string   `json:"section,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func decodeKeybindingsHarnessReport(output string) (keybindingsHarnessReport, error) {
	var report keybindingsHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsFileHarnessReport(output string) (keybindingsFileHarnessReport, error) {
	var report keybindingsFileHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsFileHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsValidationHarnessReport(output string) (keybindingsValidationHarnessReport, error) {
	var report keybindingsValidationHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsValidationHarnessReport{}, err
	}
	return report, nil
}

func decodeKeybindingsResolveHarnessReport(output string) (keybindingsResolveHarnessReport, error) {
	var report keybindingsResolveHarnessReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return keybindingsResolveHarnessReport{}, err
	}
	return report, nil
}

func browserNotificationsScenario() scenario {
	return scenario{
		name: "browser_notifications_roundtrip",
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

			initialChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialChrome, err := decodeChromeHarnessReport(initialChromeOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialChrome.Kind != "chrome" || initialChrome.Action != "status" || initialChrome.Enabled || initialChrome.Configured {
				return localScenarioResult{}, fmt.Errorf("unexpected initial chrome report: %#v", initialChrome)
			}

			setChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "on", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setChrome, err := decodeChromeHarnessReport(setChromeOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setChrome.Action != "set" || !setChrome.Enabled || !setChrome.Configured || setChrome.MCPServer == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected set chrome report: %#v", setChrome)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"chrome_default_enabled": true`) && !strings.Contains(string(configData), `"chrome_default_enabled":true`) {
				return localScenarioResult{}, fmt.Errorf("chrome config did not persist enabled state: %s", string(configData))
			}

			statusChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "status")
			if err != nil {
				return localScenarioResult{}, err
			}
			statusChrome, err := decodeChromeHarnessReport(statusChromeOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if statusChrome.Action != "status" || !statusChrome.Enabled || !statusChrome.Configured {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted chrome report: %#v", statusChrome)
			}

			chromeText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "chrome", "permissions")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(chromeText, "Chrome") || !strings.Contains(chromeText, "Permissions URL") || !strings.Contains(chromeText, "https://clau.de/chrome/permissions") {
				return localScenarioResult{}, fmt.Errorf("chrome text output missing expected values: %s", chromeText)
			}

			clearChromeOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "chrome", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearChrome, err := decodeChromeHarnessReport(clearChromeOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearChrome.Action != "clear" || clearChrome.Enabled || clearChrome.Configured || !clearChrome.Previous {
				return localScenarioResult{}, fmt.Errorf("unexpected clear chrome report: %#v", clearChrome)
			}

			initialNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialNotifications, err := decodeNotificationsHarnessReport(initialNotificationsOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialNotifications.Kind != "notifications" || initialNotifications.Action != "status" || !initialNotifications.Enabled || initialNotifications.Configured {
				return localScenarioResult{}, fmt.Errorf("unexpected initial notifications report: %#v", initialNotifications)
			}

			setNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications", "off", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setNotifications, err := decodeNotificationsHarnessReport(setNotificationsOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setNotifications.Action != "set" || setNotifications.Enabled || !setNotifications.Configured || !setNotifications.Previous {
				return localScenarioResult{}, fmt.Errorf("unexpected set notifications report: %#v", setNotifications)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"notifications_enabled": false`) && !strings.Contains(string(configData), `"notifications_enabled":false`) {
				return localScenarioResult{}, fmt.Errorf("notifications config did not persist disabled state: %s", string(configData))
			}

			notificationsText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "notifications")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(notificationsText, "Notifications") || !strings.Contains(notificationsText, "Enabled          false") {
				return localScenarioResult{}, fmt.Errorf("notifications text output missing expected values: %s", notificationsText)
			}

			clearNotificationsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "notifications", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearNotifications, err := decodeNotificationsHarnessReport(clearNotificationsOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearNotifications.Action != "clear" || !clearNotifications.Enabled || clearNotifications.Configured || clearNotifications.Previous {
				return localScenarioResult{}, fmt.Errorf("unexpected clear notifications report: %#v", clearNotifications)
			}

			initialTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry")
			if err != nil {
				return localScenarioResult{}, err
			}
			initialTelemetry, err := decodeTelemetryHarnessReport(initialTelemetryOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if initialTelemetry.Kind != "telemetry" || initialTelemetry.Action != "status" || initialTelemetry.Enabled || initialTelemetry.Configured {
				return localScenarioResult{}, fmt.Errorf("unexpected initial telemetry report: %#v", initialTelemetry)
			}

			setTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry", "on", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			setTelemetry, err := decodeTelemetryHarnessReport(setTelemetryOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if setTelemetry.Action != "set" || !setTelemetry.Enabled || !setTelemetry.Configured {
				return localScenarioResult{}, fmt.Errorf("unexpected set telemetry report: %#v", setTelemetry)
			}
			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(configData), `"telemetry_enabled": true`) && !strings.Contains(string(configData), `"telemetry_enabled":true`) {
				return localScenarioResult{}, fmt.Errorf("telemetry config did not persist enabled state: %s", string(configData))
			}

			telemetryText, err := runHarnessCodog(ctx, workspace, "--config", configPath, "telemetry")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(telemetryText, "Telemetry") || !strings.Contains(telemetryText, "Enabled          true") {
				return localScenarioResult{}, fmt.Errorf("telemetry text output missing expected values: %s", telemetryText)
			}

			clearTelemetryOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "--output-format", "json", "telemetry", "clear", "--path", configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			clearTelemetry, err := decodeTelemetryHarnessReport(clearTelemetryOut)
			if err != nil {
				return localScenarioResult{}, err
			}
			if clearTelemetry.Action != "clear" || clearTelemetry.Enabled || clearTelemetry.Configured || !clearTelemetry.Previous {
				return localScenarioResult{}, fmt.Errorf("unexpected clear telemetry report: %#v", clearTelemetry)
			}

			configData, err = os.ReadFile(configPath)
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, clearedKey := range []string{`"chrome_default_enabled"`, `"notifications_enabled"`, `"telemetry_enabled"`} {
				if strings.Contains(string(configData), clearedKey) {
					return localScenarioResult{}, fmt.Errorf("config still contains %s after clear: %s", clearedKey, string(configData))
				}
			}

			report := map[string]any{
				"kind": "browser_notifications",
				"chrome": map[string]any{
					"initial_enabled": initialChrome.Enabled,
					"set":             setChrome.Enabled,
					"status":          statusChrome.Enabled,
					"cleared":         clearChrome.Enabled,
					"mcp_server":      setChrome.MCPServer,
					"path_persisted":  setChrome.Path != "" && strings.HasSuffix(setChrome.Path, "codog-config.json"),
					"text_rendered":   strings.Contains(chromeText, "Permissions URL"),
				},
				"notifications": map[string]any{
					"initial_enabled": initialNotifications.Enabled,
					"set":             setNotifications.Enabled,
					"cleared":         clearNotifications.Enabled,
					"path_persisted":  setNotifications.Path != "" && strings.HasSuffix(setNotifications.Path, "codog-config.json"),
					"text_rendered":   strings.Contains(notificationsText, "Enabled          false"),
				},
				"telemetry": map[string]any{
					"initial_enabled": initialTelemetry.Enabled,
					"set":             setTelemetry.Enabled,
					"cleared":         clearTelemetry.Enabled,
					"path_persisted":  setTelemetry.Path != "" && strings.HasSuffix(setTelemetry.Path, "codog-config.json"),
					"text_rendered":   strings.Contains(telemetryText, "Enabled          true"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "browser notifications harness ok",
				RequestCount: 13,
				MessageCount: 1,
			}, nil
		},
	}
}

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
			queued := tui.PreviewWithQueued("", []string{"review auth flow", "write tests"}, 96, 24)
			if !strings.Contains(queued.View, "queued prompts: 2") || !strings.Contains(queued.View, "2 queued") {
				return localScenarioResult{}, fmt.Errorf("expected queued prompt preview, got %s", queued.View)
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
			if !strings.Contains(attachments.View, "attachments: 2") || !strings.Contains(attachments.View, "2 attached") || len(attachments.Attachments) != 2 {
				return localScenarioResult{}, fmt.Errorf("expected attachment preview, got %s", attachments.View)
			}
			attachmentRemoval := tui.PreviewWithAttachmentRemoval("describe", []string{"notes.txt", "pixel.png"}, 96, 24)
			if len(attachmentRemoval.Attachments) != 1 || attachmentRemoval.Attachments[0] != "notes.txt" || !strings.Contains(attachmentRemoval.View, "attachment removed") {
				return localScenarioResult{}, fmt.Errorf("expected attachment removal preview, got attachments=%#v view=%s", attachmentRemoval.Attachments, attachmentRemoval.View)
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
			thinkingToggle := tui.PreviewWithRuntimeToggle("", "alt+t", tui.RuntimeControlResult{
				Title:  "Thinking",
				Status: "thinking medium",
				Lines:  []string{"Reasoning: medium", "Previous: low"},
			}, 96, 24)
			if !strings.Contains(thinkingToggle.View, "Thinking") || !strings.Contains(thinkingToggle.View, "Reasoning: medium") {
				return localScenarioResult{}, fmt.Errorf("expected thinking toggle preview, got view=%s", thinkingToggle.View)
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
				"kind":                         "tui_prompt_completion",
				"matches":                      multiple.Matches,
				"automatic":                    automatic.Matches,
				"queued_preview":               strings.Contains(queued.View, "queued prompts: 2"),
				"stash_preview":                stash.HasStash,
				"transcript_preview":           transcript.Transcript,
				"todos_preview":                todosPreview.TodosOpen && strings.Contains(todosPreview.View, "write focused parity test"),
				"undo_preview":                 undoPreview.Value == "" && strings.Contains(undoPreview.View, "undo"),
				"attachment_preview":           strings.Contains(attachments.View, "attachments: 2"),
				"paste_preview":                strings.Contains(paste.View, "pasted 1 line"),
				"paste_image_preview":          strings.Contains(pasteImage.View, "clipboard image attached"),
				"file_ref_completion":          strings.Contains(fileRef.Value, "@internal/tui/tui.go"),
				"quick_open_preview":           strings.Contains(quickOpenPreview.View, "package quick"),
				"quick_open":                   strings.Contains(quickOpen.Value, "@internal/tui/tui.go"),
				"global_search_preview":        strings.Contains(globalSearchPreview.View, "NeedleValue"),
				"global_search":                strings.Contains(globalSearch.Value, "#L4"),
				"model_picker":                 modelPicker.ModelPicker,
				"runtime_fast_toggle":          strings.Contains(fastToggle.View, "Fast mode: on"),
				"runtime_thinking":             strings.Contains(thinkingToggle.View, "Reasoning: medium"),
				"message_actions":              messageActions.MessageMenu,
				"message_action_copy":          messageCopy.Value == "copy me",
				"message_action_target":        messageTargetCopy.Value == "first target",
				"message_action_restore":       strings.Contains(messageRestore.View, "Conversation Restored"),
				"message_action_fork":          strings.Contains(messageFork.View, "Conversation Forked"),
				"message_action_summary":       strings.Contains(messageSummarize.View, "Conversation Summarized"),
				"message_action_summary_up_to": strings.Contains(messageSummarizeUpTo.View, "Earlier Conversation Summarized"),
				"attachments":                  attachments.Attachments,
				"submitted":                    submitted.Submitted,
				"submitted_prompt":             submitted.Prompt,
				"view_contains":                []string{"Codog TUI", "Enter send"},
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
	const sessionID = "resume-jsonl"
	const prompt = "continue from stored session"
	configHome := func(workspace string) string {
		return filepath.Join(workspace, "config-home")
	}
	return scenario{
		name:   "session_resume_jsonl_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "resume harness ok"}},
		prompt: prompt,
		setup: func(workspace string) error {
			store := session.NewWorkspaceStore(configHome(workspace), workspace)
			if _, err := store.CreateWithIdentity(sessionID, session.SessionIdentity{
				Title:   "Stored resume context",
				Purpose: "prompt",
			}); err != nil {
				return err
			}
			if err := store.AppendInput(sessionID, "stored prompt"); err != nil {
				return err
			}
			if err := store.Append(sessionID, anthropic.TextMessage("user", "stored prompt")); err != nil {
				return err
			}
			return store.Append(sessionID, anthropic.TextMessage("assistant", "stored answer"))
		},
		loadPrevious: func(workspace string) ([]anthropic.Message, error) {
			store := session.NewWorkspaceStore(configHome(workspace), workspace)
			sess, err := store.OpenExisting(sessionID)
			if err != nil {
				return nil, err
			}
			if len(sess.Messages) != 2 {
				return nil, fmt.Errorf("expected 2 stored messages before resume, got %d", len(sess.Messages))
			}
			return sess.Messages, nil
		},
		verify: func(workspace string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "resume harness ok") {
				return fmt.Errorf("missing resume final response")
			}
			if len(result.Messages) != 4 {
				return fmt.Errorf("expected 4 messages after resumed turn, got %d", len(result.Messages))
			}
			store := session.NewWorkspaceStore(configHome(workspace), workspace)
			if err := store.AppendInput(sessionID, prompt); err != nil {
				return err
			}
			for _, msg := range result.Messages[2:] {
				if err := store.Append(sessionID, msg); err != nil {
					return err
				}
			}
			reopened, err := store.OpenExisting(sessionID)
			if err != nil {
				return err
			}
			if len(reopened.Messages) != 4 {
				return fmt.Errorf("expected 4 persisted messages after resume, got %d", len(reopened.Messages))
			}
			if strings.TrimSpace(reopened.Messages[0].Content[0].Text) != "stored prompt" ||
				strings.TrimSpace(reopened.Messages[1].Content[0].Text) != "stored answer" ||
				strings.TrimSpace(reopened.Messages[2].Content[0].Text) != prompt ||
				strings.TrimSpace(reopened.Messages[3].Content[0].Text) != "resume harness ok" {
				return fmt.Errorf("unexpected persisted resume messages: %#v", reopened.Messages)
			}
			if strings.TrimSpace(reopened.Identity.Workspace) == "" ||
				strings.TrimSpace(reopened.Identity.Worktree) == "" ||
				len(reopened.Identity.Placeholders) != 0 {
				return fmt.Errorf("unexpected reopened identity after resume: %#v", reopened.Identity)
			}
			return nil
		},
		verifyRequests: func(requests []anthropic.Request) error {
			if len(requests) != 1 {
				return fmt.Errorf("expected 1 resume request, got %d", len(requests))
			}
			if len(requests[0].Messages) != 3 {
				return fmt.Errorf("expected resumed request with 3 messages, got %d", len(requests[0].Messages))
			}
			if requests[0].Messages[0].Content[0].Text != "stored prompt" ||
				requests[0].Messages[1].Content[0].Text != "stored answer" ||
				requests[0].Messages[2].Content[0].Text != prompt {
				return fmt.Errorf("unexpected resumed request messages: %#v", requests[0].Messages)
			}
			return nil
		},
	}
}

func resumeSlashCommandScenario() scenario {
	return scenario{
		name: "resume_slash_command_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
		},
	}
}

func sessionExportPathSafetyScenario() scenario {
	return scenario{
		name: "session_export_path_safety_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
		},
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
	commandArgs := append([]string{"run", "./cmd/codog", "--cwd", workspace}, args...)
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
		name: "prompt_directory_attachment_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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

			out, err := runHarnessCodog(ctx, workspace, "--config", configPath, "prompt", "Describe directory attachment", "--attach", "docs", "--output-format", "json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var promptReport struct {
				Response string `json:"response"`
			}
			if err := json.Unmarshal([]byte(out), &promptReport); err != nil {
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
		},
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
		verify: func(_ string, result runloop.TurnResult, output string) error {
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
			data, err := os.ReadFile(payload.PersistedOutputPath)
			if err != nil {
				return err
			}
			var persisted struct {
				Kind            string   `json:"kind"`
				Stdout          string   `json:"stdout"`
				TruncatedFields []string `json:"truncated_fields"`
			}
			if err := json.Unmarshal(data, &persisted); err != nil {
				return err
			}
			if persisted.Kind != "bash_output" || len(persisted.Stdout) != 20000 || strings.Join(persisted.TruncatedFields, ",") != "stdout" {
				return fmt.Errorf("unexpected persisted bash output metadata: kind=%q stdout=%d fields=%v", persisted.Kind, len(persisted.Stdout), persisted.TruncatedFields)
			}
			return nil
		},
	}
}

func bashBackgroundOutputScenario() scenario {
	return scenario{
		name: "bash_background_output_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			startOut, err := registry.Execute(ctx, "Bash", json.RawMessage(`{
				"command":"printf background-harness",
				"run_in_background":true
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(startOut), &started); err != nil {
				return localScenarioResult{}, err
			}
			if !started.Background || started.BackgroundTaskID == "" || started.BackgroundedByUser || started.AssistantAutoBackgrounded {
				return localScenarioResult{}, fmt.Errorf("unexpected background bash start payload: %s", startOut)
			}
			if !started.NoOutputExpected || started.RawOutputPath != nil || started.ReturnCodeInterpretation != nil || started.SandboxPermissionsDowngraded {
				return localScenarioResult{}, fmt.Errorf("unexpected background bash contract fields: %s", startOut)
			}

			outputOut, err := registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64,
				"block":true,
				"timeout_ms":2000
			}`, started.BackgroundTaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(outputOut), &output); err != nil {
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
		},
	}
}

func bashKillScenario() scenario {
	return scenario{
		name: "bash_kill_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
			startOut, err := registry.Execute(ctx, "Bash", json.RawMessage(`{
				"command":"printf kill-ready; sleep 10",
				"run_in_background":true
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var started struct {
				BackgroundTaskID string `json:"backgroundTaskId"`
				Background       bool   `json:"background"`
			}
			if err := json.Unmarshal([]byte(startOut), &started); err != nil {
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

			beforeKillOut, err := registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64,
				"block":true,
				"timeout_ms":2000
			}`, started.BackgroundTaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var beforeKill struct {
				Stdout      string `json:"stdout"`
				Interrupted bool   `json:"interrupted"`
				TimedOut    bool   `json:"timedOut"`
			}
			if err := json.Unmarshal([]byte(beforeKillOut), &beforeKill); err != nil {
				return localScenarioResult{}, err
			}
			if beforeKill.Stdout != "kill-ready" || beforeKill.Interrupted || beforeKill.TimedOut {
				return localScenarioResult{}, fmt.Errorf("unexpected pre-kill bash output: %s", beforeKillOut)
			}

			killOut, err := registry.Execute(ctx, "KillBash", json.RawMessage(fmt.Sprintf(`{"bash_id":%q}`, started.BackgroundTaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var killed struct {
				BackgroundTaskID string `json:"backgroundTaskId"`
				Status           string `json:"status"`
				Interrupted      bool   `json:"interrupted"`
				NoOutputExpected bool   `json:"noOutputExpected"`
			}
			if err := json.Unmarshal([]byte(killOut), &killed); err != nil {
				return localScenarioResult{}, err
			}
			if killed.BackgroundTaskID != started.BackgroundTaskID || killed.Status != "stopped" || !killed.Interrupted || !killed.NoOutputExpected {
				return localScenarioResult{}, fmt.Errorf("unexpected kill bash payload: %s", killOut)
			}
			killedTask = true

			afterKillOut, err := registry.Execute(ctx, "BashOutput", json.RawMessage(fmt.Sprintf(`{
				"bash_id":%q,
				"offset":0,
				"limit":64
			}`, started.BackgroundTaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var afterKill struct {
				Stdout           string `json:"stdout"`
				Status           string `json:"status"`
				Interrupted      bool   `json:"interrupted"`
				BackgroundTaskID string `json:"backgroundTaskId"`
				RawOutputPath    string `json:"rawOutputPath"`
			}
			if err := json.Unmarshal([]byte(afterKillOut), &afterKill); err != nil {
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
		},
	}
}

func powerShellStdoutScenario() scenario {
	return scenario{
		name:       "powershell_stdout_roundtrip",
		permission: tools.PermissionAllow,
		registryOptions: func(_ string, configHome string) tools.RegistryOptions {
			return tools.RegistryOptions{
				ConfigHome: configHome,
				PowerShell: "echo",
			}
		},
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{{
				ID:    "tool-1",
				Name:  "powershell",
				Input: json.RawMessage(`{"command":"Write-Output harness-powershell","timeout":1000}`),
			}}},
			{Text: "powershell harness ok"},
		},
		prompt: "run powershell",
		verify: func(_ string, result runloop.TurnResult, output string) error {
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
		},
	}
}

func permissionScopeDenialScenario() scenario {
	return scenario{
		name: "permission_scope_denial_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			outside := filepath.Join(filepath.Dir(workspace), filepath.Base(workspace)+"-outside")
			defer os.RemoveAll(outside)
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
			permissionOut, err := registry.Execute(ctx, "permission_check", json.RawMessage(`{
				"target_tool": "BashTool",
				"input": {"command": `+strconv.Quote(command)+`}
			}`), prompter)
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(permissionOut), &permissionCheck); err != nil {
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
			if !strings.Contains(bashErr.Error(), "permission denied for tool bash by tool validation") ||
				!strings.Contains(bashErr.Error(), "path resolves outside workspace scope") {
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
		},
	}
}

func harnessShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func sandboxBypassStatusScenario() scenario {
	return scenario{
		name: "sandbox_bypass_status_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			out, err := tools.BashTool{
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
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(out), &payload); err != nil {
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
		},
	}
}

func policyUpdateSandboxScenario() scenario {
	return scenario{
		name: "policy_update_sandbox_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
		},
	}
}

func policyApprovalScenario() scenario {
	return scenario{
		name: "policy_approval_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

			staleOut, err := registry.Execute(ctx, "PolicyEvaluateTool", json.RawMessage(`{
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
			if err != nil {
				return localScenarioResult{}, err
			}
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
			if err := json.Unmarshal([]byte(staleOut), &staleEval); err != nil {
				return localScenarioResult{}, err
			}
			if staleEval.Kind != "policy_evaluation" || len(staleEval.Actions) != 3 || staleEval.Actions[0].Kind != "merge_forward" || staleEval.Actions[0].RecoveryScenario != "stale_branch" || !slices.Contains(staleEval.Actions[0].Commands, "branch_freshness") || staleEval.Actions[1].Kind != "closeout_lane" || staleEval.Actions[2].Kind != "cleanup_session" {
				return localScenarioResult{}, fmt.Errorf("unexpected stale policy evaluation: %s", staleOut)
			}
			if len(staleEval.Events) != 3 || staleEval.Events[0].RuleID != "stale-branch-merge-forward" || staleEval.Events[1].RuleID != "lane-completed-closeout" {
				return localScenarioResult{}, fmt.Errorf("unexpected stale policy events: %s", staleOut)
			}

			escalateOut, err := registry.Execute(ctx, "policy_evaluate", json.RawMessage(`{
				"lane_id": "lane-startup",
				"blocker": "startup",
				"retry_count": 1,
				"retry_limit": 1
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(escalateOut), &escalateEval); err != nil {
				return localScenarioResult{}, err
			}
			if len(escalateEval.Actions) != 1 || escalateEval.Actions[0].Kind != "escalate" || len(escalateEval.Events) != 1 || escalateEval.Events[0].Kind != "escalate" {
				return localScenarioResult{}, fmt.Errorf("unexpected escalation policy evaluation: %s", escalateOut)
			}

			blockedOut, err := registry.Execute(ctx, "policy_evaluate", json.RawMessage(`{
				"lane_id": "lane-main",
				"requested_action": "git push origin main",
				"repository": "owner/repo",
				"branch": "main",
				"actor": "release-bot",
				"actor_scope": "automation",
				"policy_source": "AGENTS.md"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(blockedOut), &blocked); err != nil {
				return localScenarioResult{}, err
			}
			if blocked.BlockedHandoff.Kind != "policy_blocked_handoff" || blocked.BlockedHandoff.Status != "blocked_by_policy" || blocked.BlockedHandoff.Reason != "main_push_forbidden" || blocked.BlockedHandoff.PolicySource != "AGENTS.md" || blocked.BlockedHandoff.ActorScope != "automation" || blocked.BlockedHandoff.TechnicalFailure || len(blocked.BlockedHandoff.Fallback) != 2 || blocked.BlockedHandoff.Fallback[0].Kind != "create_branch" || blocked.BlockedHandoff.Fallback[1].Kind != "open_pr" {
				return localScenarioResult{}, fmt.Errorf("unexpected policy-blocked handoff: %s", blockedOut)
			}

			scope := `"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main","commit":"abc123"}`
			pendingOut, err := registry.Execute(ctx, "ApprovalTokenTool", json.RawMessage(`{
				"action": "pending",
				"token": "tok-main",
				`+scope+`,
				"approving_actor": "owner",
				"requesting_actor": "release-lead",
				"approved_executor": "release-bot"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var pending struct {
				Kind   string `json:"kind"`
				Action string `json:"action"`
				Status string `json:"status"`
				Grant  struct {
					Token  string `json:"token"`
					Status string `json:"status"`
				} `json:"grant"`
			}
			if err := json.Unmarshal([]byte(pendingOut), &pending); err != nil {
				return localScenarioResult{}, err
			}
			if pending.Kind != "approval_token" || pending.Action != "pending" || pending.Status != "ok" || pending.Grant.Token != "tok-main" || pending.Grant.Status != "approval_pending" {
				return localScenarioResult{}, fmt.Errorf("unexpected approval pending output: %s", pendingOut)
			}

			pendingVerifyOut, err := registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "verify",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var pendingVerify struct {
				Status    string `json:"status"`
				ErrorKind string `json:"error_kind"`
			}
			if err := json.Unmarshal([]byte(pendingVerifyOut), &pendingVerify); err != nil {
				return localScenarioResult{}, err
			}
			if pendingVerify.Status != "denied" || pendingVerify.ErrorKind != "approval_pending" {
				return localScenarioResult{}, fmt.Errorf("unexpected pending verify output: %s", pendingVerifyOut)
			}

			grantOut, err := registry.Execute(ctx, "ApprovalTokenTool", json.RawMessage(`{
				"action": "approve",
				"token": "tok-main",
				`+scope+`,
				"approving_actor": "owner",
				"requesting_actor": "release-lead",
				"approved_executor": "release-bot",
				"max_uses": 1,
				"delegation_chain": [{"actor":"owner","session_id":"session-owner","reason":"owner approval"},{"actor":"orchestrator","session_id":"session-orchestrator","reason":"relay"}]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(grantOut), &grant); err != nil {
				return localScenarioResult{}, err
			}
			if grant.Kind != "approval_token" || grant.Action != "approve" || grant.Status != "ok" || grant.Grant.Token != "tok-main" || grant.Grant.ReplayPreventionNonce == "" || grant.Grant.Status != "approval_granted" || grant.Grant.ApprovingActor != "owner" || grant.Grant.ApprovedExecutor != "release-bot" || grant.Grant.MaxUses != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected approval approve output: %s", grantOut)
			}

			verifyOut, err := registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "verify",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(verifyOut), &verify); err != nil {
				return localScenarioResult{}, err
			}
			if verify.Status != "ok" || verify.Audit.Kind != "approval_token_audit" || verify.Audit.Token != "tok-main" || verify.Audit.ReplayPreventionNonce != grant.Grant.ReplayPreventionNonce || verify.Audit.Scope.Commit != "abc123" || verify.Audit.RequestingActor != "release-lead" || verify.Audit.ExecutingActor != "release-bot" || verify.Audit.ExecutionMode != "delegated_execution" || verify.Audit.Status != "approval_granted" || !verify.Audit.DelegatedExecution || len(verify.Audit.DelegationChain) != 4 || verify.Audit.DelegationChain[1].Actor != "orchestrator" || verify.Audit.DelegationChain[2].Actor != "release-lead" || verify.Audit.DelegationChain[3].Actor != "release-bot" {
				return localScenarioResult{}, fmt.Errorf("unexpected approval verify output: %s", verifyOut)
			}

			consumeOut, err := registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "consume",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var consume struct {
				Status string `json:"status"`
				Audit  struct {
					Status string `json:"status"`
					Uses   int    `json:"uses"`
				} `json:"audit"`
			}
			if err := json.Unmarshal([]byte(consumeOut), &consume); err != nil {
				return localScenarioResult{}, err
			}
			if consume.Status != "ok" || consume.Audit.Status != "approval_consumed" || consume.Audit.Uses != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected approval consume output: %s", consumeOut)
			}

			replayOut, err := registry.Execute(ctx, "approval_token", json.RawMessage(`{
				"action": "consume",
				"token": "tok-main",
				`+scope+`,
				"executing_actor": "release-bot"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var replay struct {
				Status    string `json:"status"`
				ErrorKind string `json:"error_kind"`
			}
			if err := json.Unmarshal([]byte(replayOut), &replay); err != nil {
				return localScenarioResult{}, err
			}
			if replay.Status != "denied" || replay.ErrorKind != "approval_already_consumed" {
				return localScenarioResult{}, fmt.Errorf("unexpected approval replay output: %s", replayOut)
			}

			listOut, err := registry.Execute(ctx, "approval_token", json.RawMessage(`{"action":"list"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
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
			if err := json.Unmarshal([]byte(listOut), &list); err != nil {
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
		},
	}
}

func notebookReadEditScenario() scenario {
	return scenario{
		name: "notebook_read_edit_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
			if !strings.Contains(replaceOut, `"kind": "notebook_edit"`) || !strings.Contains(replaceOut, `"mode": "replace"`) || !strings.Contains(replaceOut, `"cell_id": "intro"`) {
				return localScenarioResult{}, fmt.Errorf("unexpected notebook replace output: %s", replaceOut)
			}

			insertOut, err := tools.NotebookEditTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"notebook_path":"analysis.ipynb","cell_id":"intro","edit_mode":"insert","cell_type":"code","new_source":"print(2)\n"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(insertOut, `"mode": "insert"`) || !strings.Contains(insertOut, `"cell_type": "code"`) || !strings.Contains(insertOut, `"cell_count": 3`) {
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
			if !strings.Contains(finalRead, `"cell_count": 3`) || !strings.Contains(finalRead, "# Renamed") || !strings.Contains(finalRead, "print(2)") {
				return localScenarioResult{}, fmt.Errorf("final notebook read missing edited cells: %s", finalRead)
			}

			return localScenarioResult{
				Output:       strings.Join([]string{readOut, replaceOut, insertOut, finalRead}, "\n"),
				FinalMessage: "notebook read edit harness ok",
				ToolCalls:    4,
				ToolUses:     []string{"notebook_read", "notebook_edit", "notebook_edit", "notebook_read"},
				RequestCount: 4,
			}, nil
		},
	}
}

func webAccessScenario() scenario {
	return scenario{
		name: "web_access_roundtrip",
		runLocal: func(ctx context.Context, _ string) (localScenarioResult, error) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/page":
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, `<html><head><title>Codog Web Parity</title><script>ignored()</script></head><body><main><h1>Fetch Summary</h1><p>Codog fetches local HTML text for grounded answers.</p></main></body></html>`)
				case "/search":
					if got := r.URL.Query().Get("q"); got != "codog web parity" {
						http.Error(w, "unexpected query "+got, http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, `
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
		},
	}
}

func webAccessLimitsScenario() scenario {
	return scenario{
		name: "web_access_limits_roundtrip",
		runLocal: func(ctx context.Context, _ string) (localScenarioResult, error) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/large":
					w.Header().Set("Content-Type", "text/plain")
					io.WriteString(w, strings.Repeat("bounded fetch content ", 8))
				case "/search":
					if got := r.URL.Query().Get("q"); got != "codog blocked parity" {
						http.Error(w, "unexpected query "+got, http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, `
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
		},
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
		name: "git_workspace_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
			if !strings.Contains(statusOut, `"output"`) || !strings.Contains(statusOut, "notes.txt") {
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
			if !strings.Contains(blameOut, "alpha") || !strings.Contains(blameOut, "Codog Test") {
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
		},
	}
}

func gitPreserveStateScenario() scenario {
	return scenario{
		name: "git_preserve_state_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			remote := filepath.Join(workspace, "origin.git")
			if err := runHarnessGit(workspace, "init", "-q", "--bare", remote); err != nil {
				return localScenarioResult{}, err
			}
			repo := filepath.Join(workspace, "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := runHarnessGit(repo, "init", "-q", "-b", "main"); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"config", "user.email", "codog@example.test"},
				{"config", "user.name", "Codog Test"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			notesPath := filepath.Join(repo, "notes.txt")
			if err := os.WriteFile(notesPath, []byte("base\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "notes.txt"},
				{"commit", "-q", "-m", "chore: base"},
				{"remote", "add", "origin", remote},
				{"push", "-q", "-u", "origin", "main"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			baseSHA, err := gitops.Run(repo, "rev-parse", "HEAD")
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "feature.txt"},
				{"commit", "-q", "-m", "feat: preserve state"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			if err := os.WriteFile(notesPath, []byte("base\nworktree\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			state, err := gitops.PreserveStateForIssue(repo)
			if err != nil {
				return localScenarioResult{}, err
			}
			if state == nil {
				return localScenarioResult{}, errors.New("git preserve state returned nil")
			}
			if state.RemoteBase != "origin/main" || state.RemoteBaseSHA != baseSHA || state.BranchName != "main" {
				return localScenarioResult{}, fmt.Errorf("unexpected preserved git identity: %#v", state)
			}
			for _, expected := range []string{"+feature", "+worktree"} {
				if !strings.Contains(state.Patch, expected) {
					return localScenarioResult{}, fmt.Errorf("preserved patch missing %s", expected)
				}
			}
			if !strings.Contains(state.FormatPatch, "feat: preserve state") {
				return localScenarioResult{}, fmt.Errorf("format patch missing commit subject")
			}
			if len(state.UntrackedFiles) != 1 || state.UntrackedFiles[0].Path != "scratch.txt" || state.UntrackedFiles[0].Content != "scratch\n" {
				return localScenarioResult{}, fmt.Errorf("unexpected untracked preservation: %#v", state.UntrackedFiles)
			}
			output, err := json.Marshal(map[string]any{
				"kind":              "git_preserve_state",
				"remote_base":       state.RemoteBase,
				"remote_base_sha":   state.RemoteBaseSHA,
				"branch_name":       state.BranchName,
				"patch_has_feature": strings.Contains(state.Patch, "+feature"),
				"patch_has_dirty":   strings.Contains(state.Patch, "+worktree"),
				"format_patch":      strings.TrimSpace(state.FormatPatch) != "",
				"share_sidecar":     true,
				"untracked_files":   len(state.UntrackedFiles),
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(output),
				FinalMessage: "git preserve state harness ok",
				RequestCount: 1,
			}, nil
		},
	}
}

func worktreeLifecycleScenario() scenario {
	return scenario{
		name: "worktree_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
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
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("worktree parity\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "README.md"},
				{"commit", "-q", "-m", "init worktree parity"},
			} {
				if err := runHarnessGit(workspace, args...); err != nil {
					return localScenarioResult{}, err
				}
			}

			registry := tools.NewRegistry(workspace)
			enterOut, err := registry.Execute(ctx, "EnterWorktreeTool", json.RawMessage(`{"name":"reviewer"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var entered struct {
				Kind       string `json:"kind"`
				Operation  string `json:"operation"`
				Allocation struct {
					ID   string `json:"id"`
					Path string `json:"path"`
					Ref  string `json:"ref"`
				} `json:"allocation"`
			}
			if err := json.Unmarshal([]byte(enterOut), &entered); err != nil {
				return localScenarioResult{}, err
			}
			if entered.Kind != "worktree" || entered.Operation != "enter" || entered.Allocation.ID == "" || entered.Allocation.Path == "" || entered.Allocation.Ref == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected enter worktree output: %s", enterOut)
			}
			removed := false
			defer func() {
				if !removed {
					_, _ = registry.Execute(ctx, "ExitWorktreeTool", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
				}
			}()
			checkoutReadme := filepath.Join(entered.Allocation.Path, "README.md")
			data, err := os.ReadFile(checkoutReadme)
			if err != nil {
				return localScenarioResult{}, err
			}
			if string(data) != "worktree parity\n" {
				return localScenarioResult{}, fmt.Errorf("unexpected checkout README content: %q", string(data))
			}
			metadataPath := filepath.Join(workspace, ".codog", "worktrees", "metadata", entered.Allocation.ID+".json")
			if _, err := os.Stat(metadataPath); err != nil {
				return localScenarioResult{}, fmt.Errorf("missing worktree metadata: %w", err)
			}

			exitOut, err := registry.Execute(ctx, "exit_worktree", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var exited struct {
				Kind      string `json:"kind"`
				Operation string `json:"operation"`
				ID        string `json:"id"`
				Removed   bool   `json:"removed"`
			}
			if err := json.Unmarshal([]byte(exitOut), &exited); err != nil {
				return localScenarioResult{}, err
			}
			if exited.Kind != "worktree" || exited.Operation != "exit" || exited.ID != entered.Allocation.ID || !exited.Removed {
				return localScenarioResult{}, fmt.Errorf("unexpected exit worktree output: %s", exitOut)
			}
			removed = true
			if _, err := os.Stat(entered.Allocation.Path); !os.IsNotExist(err) {
				return localScenarioResult{}, fmt.Errorf("worktree path still exists or stat failed: %v", err)
			}
			if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
				return localScenarioResult{}, fmt.Errorf("worktree metadata still exists or stat failed: %v", err)
			}
			return localScenarioResult{
				Output:       strings.Join([]string{enterOut, exitOut, "worktree lifecycle harness ok"}, "\n"),
				FinalMessage: "worktree lifecycle harness ok",
				ToolCalls:    2,
				ToolUses:     []string{"enter_worktree", "exit_worktree"},
				RequestCount: 2,
			}, nil
		},
	}
}

func runHarnessGit(workspace string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func planTodoScenario() scenario {
	return scenario{
		name: "plan_todo_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			enterOut, err := tools.EnterPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"1. Inspect workspace\n2. Update tests"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "plan"`, `"action": "enter"`, `"status": "active"`, "Inspect workspace"} {
				if !strings.Contains(enterOut, expected) {
					return localScenarioResult{}, fmt.Errorf("plan enter output missing %s", expected)
				}
			}

			editorLog := filepath.Join(workspace, "plan-editor.log")
			editorScript := filepath.Join(workspace, "plan-editor.sh")
			if err := os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			openOut, err := runHarnessCodogWithEnv(ctx, workspace, []string{"VISUAL=" + editorScript + " " + editorLog}, "plan", "open", "--output-format", "json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var opened struct {
				Action string `json:"action"`
				Status string `json:"status"`
				Opened bool   `json:"opened"`
			}
			if err := json.Unmarshal([]byte(openOut), &opened); err != nil {
				return localScenarioResult{}, fmt.Errorf("plan open output was not json: %w: %s", err, openOut)
			}
			if opened.Action != "open" || opened.Status != "opened" || !opened.Opened {
				return localScenarioResult{}, fmt.Errorf("unexpected plan open output: %s", openOut)
			}
			openedPath, err := os.ReadFile(editorLog)
			if err != nil {
				return localScenarioResult{}, err
			}
			actualPlanPath, err := filepath.EvalSymlinks(strings.TrimSpace(string(openedPath)))
			if err != nil {
				return localScenarioResult{}, err
			}
			expectedPlanPath, err := filepath.EvalSymlinks(filepath.Join(workspace, ".codog", "plan.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if actualPlanPath != expectedPlanPath {
				return localScenarioResult{}, fmt.Errorf("plan editor opened unexpected path: %s", string(openedPath))
			}

			writeOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "write focused parity test",
						"activeForm": "writing focused parity test",
						"status": "in_progress",
						"priority": "high"
					},
					{
						"content": "run smoke",
						"activeForm": "running smoke",
						"status": "pending",
						"priority": "medium"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 2`, `"content": "write focused parity test"`, `"status": "in_progress"`, `"newTodos": [`} {
				if !strings.Contains(writeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo write output missing %s", expected)
				}
			}

			readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 2`, `"content": "run smoke"`} {
				if !strings.Contains(readOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
				}
			}

			exitOut, err := tools.ExitPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"Final plan: implement, test, smoke"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "plan"`, `"action": "exit"`, `"status": "inactive"`, "Final plan: implement, test, smoke"} {
				if !strings.Contains(exitOut, expected) {
					return localScenarioResult{}, fmt.Errorf("plan exit output missing %s", expected)
				}
			}

			planData, err := os.ReadFile(filepath.Join(workspace, ".codog", "plan.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(planData), `"active": false`) || !strings.Contains(string(planData), "Final plan: implement, test, smoke") {
				return localScenarioResult{}, fmt.Errorf("persisted plan state was not finalized: %s", string(planData))
			}
			todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(todoData), `"kind": "todos"`) || !strings.Contains(string(todoData), "write focused parity test") {
				return localScenarioResult{}, fmt.Errorf("persisted todo state missing active items: %s", string(todoData))
			}

			return localScenarioResult{
				Output:       strings.Join([]string{enterOut, openOut, writeOut, readOut, exitOut}, "\n"),
				FinalMessage: "plan todo harness ok",
				ToolCalls:    4,
				ToolUses:     []string{"enter_plan_mode", "todo_write", "todo_read", "exit_plan_mode"},
				RequestCount: 5,
			}, nil
		},
	}
}

func todoCompletionVerificationScenario() scenario {
	return scenario{
		name: "todo_completion_verification_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			initialOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "in_progress",
						"priority": "high"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 1`, `"content": "draft implementation"`} {
				if !strings.Contains(initialOut, expected) {
					return localScenarioResult{}, fmt.Errorf("initial todo write output missing %s", expected)
				}
			}

			completedOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "completed",
						"priority": "high"
					},
					{
						"content": "update tests",
						"activeForm": "updating tests",
						"status": "completed",
						"priority": "medium"
					},
					{
						"content": "prepare summary",
						"activeForm": "preparing summary",
						"status": "completed",
						"priority": "low"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 0`, `"oldTodos": [`, `"content": "draft implementation"`, `"verificationNudgeNeeded": true`} {
				if !strings.Contains(completedOut, expected) {
					return localScenarioResult{}, fmt.Errorf("completed todo write output missing %s", expected)
				}
			}

			readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 0`} {
				if !strings.Contains(readOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
				}
			}
			if strings.Contains(readOut, "draft implementation") || strings.Contains(readOut, "update tests") {
				return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from read output: %s", readOut)
			}

			todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(todoData), "draft implementation") || strings.Contains(string(todoData), "update tests") {
				return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from persisted state: %s", string(todoData))
			}

			return localScenarioResult{
				Output:       strings.Join([]string{initialOut, completedOut, readOut}, "\n"),
				FinalMessage: "todo completion verification harness ok",
				ToolCalls:    3,
				ToolUses:     []string{"todo_write", "todo_write", "todo_read"},
				RequestCount: 3,
			}, nil
		},
	}
}

func lspStaticScenario() scenario {
	return scenario{
		name: "lsp_static_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			pkgDir := filepath.Join(workspace, "pkg")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/harness\n\ngo 1.25\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			source := "package pkg\n\ntype Runner struct{}\n\nfunc RunFast() Runner { return Runner{} }\n\nfunc UseRunner() Runner { return RunFast() }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "runner.go"), []byte(source), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			messy := "package pkg\n\nfunc messy(){println(\"hi\")}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "messy.go"), []byte(messy), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			broken := "package pkg\n\nfunc Broken() { MissingSymbol() }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte(broken), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			foldSource := "package pkg\n\nfunc FoldOnly() {\n\tprintln(\"fold\")\n}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "fold.go"), []byte(foldSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			linkSource := "package pkg\n\n// Docs: https://example.test/docs.\nconst Link = \"https://example.test/api\"\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "links.go"), []byte(linkSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			colorSource := "package pkg\n\nconst Accent = \"#336699\"\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "colors.go"), []byte(colorSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hintSource := "package pkg\n\nfunc Build(name string, count int) int { return count }\nfunc UseBuild() { _ = Build(\"codog\", 2) }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "hints.go"), []byte(hintSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hierarchySource := "package pkg\n\ntype TypeBase struct{}\ntype TypeChild struct{ TypeBase }\ntype TypeContract interface { Build() }\nfunc (TypeChild) Build() {}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "hierarchy.go"), []byte(hierarchySource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			inlineSource := "package pkg\n\nconst InlineAnswer = 42\n\nfunc InlineValuesDemo() {\n\tlocal := \"codog\"\n\t_ = local\n}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "inline.go"), []byte(inlineSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			importsSource := "package pkg\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n\t\"bytes\"\n\t\"fmt\"\n)\n\nfunc ImportsDemo(){ fmt.Println(strings.TrimSpace(\" hi \")) }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "imports.go"), []byte(importsSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hintArgChar := strings.Index(strings.Split(hintSource, "\n")[3], `"codog"`)
			tool := tools.LSPTool{Workspace: workspace}

			symbolsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"symbols","path":"pkg/runner.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "symbols"`, `"source": "static"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`} {
				if !strings.Contains(symbolsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp symbols output missing %s", expected)
				}
			}

			workspaceSymbolsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_symbol","query":"run","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-symbol"`, `"source": "static"`, `"query": "run"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`} {
				if !strings.Contains(workspaceSymbolsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace symbols output missing %s", expected)
				}
			}

			workspaceSymbolResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_symbol_resolve","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-symbol-resolve"`, `"source": "static"`, `"found": true`, `"name": "RunFast"`, `"symbol": "RunFast"`, `"snippet": [`} {
				if !strings.Contains(workspaceSymbolResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace symbol resolve output missing %s", expected)
				}
			}

			definitionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"definition","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "definition"`, `"found": true`, `"name": "RunFast"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(definitionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp definition output missing %s", expected)
				}
			}

			declarationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"declaration","query":"Runner"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "declaration"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(declarationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp declaration output missing %s", expected)
				}
			}

			typeDefinitionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"type_definition","query":"Runner"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-definition"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(typeDefinitionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type definition output missing %s", expected)
				}
			}

			documentHighlightOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_highlight","path":"pkg/runner.go","query":"Runner","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-highlight"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"character": 5`, `"total": 3`} {
				if !strings.Contains(documentHighlightOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document highlight output missing %s", expected)
				}
			}

			foldingRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"folding_range","path":"pkg/fold.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "folding-range"`, `"source": "static"`, `"path": "pkg/fold.go"`, `"startLine": 2`, `"endLine": 4`, `"total": 1`} {
				if !strings.Contains(foldingRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp folding range output missing %s", expected)
				}
			}

			selectionRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"selection_range","path":"pkg/runner.go","line":4,"character":6,"limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "selection-range"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"kind": "Ident"`, `"character": 5`} {
				if !strings.Contains(selectionRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp selection range output missing %s", expected)
				}
			}

			monikerOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"moniker","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "moniker"`, `"source": "static"`, `"scheme": "gomod"`, `"identifier": "example.test/harness/pkg.RunFast"`, `"kind": "export"`, `"unique": "project"`} {
				if !strings.Contains(monikerOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp moniker output missing %s", expected)
				}
			}

			linkedEditingOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"linked_editing_range","path":"pkg/runner.go","query":"Runner","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "linked-editing-range"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"wordPattern": "[A-Za-z_][A-Za-z0-9_]*"`, `"total": 3`} {
				if !strings.Contains(linkedEditingOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp linked editing output missing %s", expected)
				}
			}

			documentLinkOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_link","path":"pkg/links.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-link"`, `"source": "static"`, `"path": "pkg/links.go"`, `"target": "https://example.test/docs"`, `"character": 9`, `"total": 2`} {
				if !strings.Contains(documentLinkOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document link output missing %s", expected)
				}
			}

			documentLinkResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_link_resolve","path":"pkg/links.go","line":2,"character":12}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-link-resolve"`, `"source": "static"`, `"found": true`, `"target": "https://example.test/docs"`} {
				if !strings.Contains(documentLinkResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document link resolve output missing %s", expected)
				}
			}

			documentColorOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_color","path":"pkg/colors.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-color"`, `"source": "static"`, `"path": "pkg/colors.go"`, `"text": "#336699"`, `"red": 0.2`, `"total": 1`} {
				if !strings.Contains(documentColorOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document color output missing %s", expected)
				}
			}

			colorPresentationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"color_presentation","path":"pkg/colors.go","line":2,"character":18}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "color-presentation"`, `"source": "static"`, `"found": true`, `"label": "#336699"`, `"label": "rgb(51, 102, 153)"`} {
				if !strings.Contains(colorPresentationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp color presentation output missing %s", expected)
				}
			}

			inlayHintOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"inlay_hint","path":"pkg/hints.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inlay-hint"`, `"source": "static"`, `"path": "pkg/hints.go"`, `"label": "name:"`, `"label": "count:"`, `"kind": "parameter"`, `"total": 2`} {
				if !strings.Contains(inlayHintOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inlay hint output missing %s", expected)
				}
			}

			inlayHintResolveOut, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"action":"inlay_hint_resolve","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inlay-hint-resolve"`, `"source": "static"`, `"found": true`, `"label": "name:"`, `"tooltip": "Build parameter 1"`} {
				if !strings.Contains(inlayHintResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inlay hint resolve output missing %s", expected)
				}
			}

			signatureHelpOut, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"action":"signature_help","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "signature-help"`, `"source": "static"`, `"found": true`, `"function": "Build"`, `"label": "Build(name string, count int) int"`, `"activeParameter": 0`} {
				if !strings.Contains(signatureHelpOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp signature help output missing %s", expected)
				}
			}

			codeLensOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_lens","path":"pkg/runner.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-lens"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"symbol": "Runner"`, `"command": "codog.references"`, `"total": 3`} {
				if !strings.Contains(codeLensOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code lens output missing %s", expected)
				}
			}

			codeLensResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_lens_resolve","path":"pkg/runner.go","line":2,"character":6}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-lens-resolve"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"command": "codog.references"`} {
				if !strings.Contains(codeLensResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code lens resolve output missing %s", expected)
				}
			}

			semanticTokensOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens","path":"pkg/runner.go","limit":80}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens"`, `"source": "static"`, `"legend": [`, `"text": "Runner"`, `"type": "type"`, `"text": "RunFast"`, `"type": "function"`} {
				if !strings.Contains(semanticTokensOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens output missing %s", expected)
				}
			}

			semanticTokensRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens_range","path":"pkg/runner.go","line":2,"limit":20}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens-range"`, `"source": "static"`, `"text": "Runner"`, `"line": 2`} {
				if !strings.Contains(semanticTokensRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens range output missing %s", expected)
				}
			}

			semanticTokensDeltaOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens_delta","path":"pkg/runner.go","query":"previous-result","limit":80}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens-delta"`, `"source": "static"`, `"previousResultId": "previous-result"`, `"edits": []`} {
				if !strings.Contains(semanticTokensDeltaOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens delta output missing %s", expected)
				}
			}

			prepareRenameOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_rename","path":"pkg/runner.go","line":2,"character":6}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-rename"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"placeholder": "Runner"`} {
				if !strings.Contains(prepareRenameOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare rename output missing %s", expected)
				}
			}

			renameOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"rename","query":"Runner","new_name":"RunnerRenamed","limit":20}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "rename"`, `"source": "static"`, `"query": "Runner"`, `"newName": "RunnerRenamed"`, `"file_edits": 1`, "type RunnerRenamed struct{}"} {
				if !strings.Contains(renameOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp rename output missing %s", expected)
				}
			}

			callHierarchyOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_call_hierarchy","query":"Build"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-call-hierarchy"`, `"source": "static"`, `"name": "Build"`, `"kind": "function"`, `"total": 1`} {
				if !strings.Contains(callHierarchyOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare call hierarchy output missing %s", expected)
				}
			}

			incomingCallsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"incoming_calls","query":"Build","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "call-hierarchy-incoming"`, `"source": "static"`, `"query": "Build"`, `"name": "UseBuild"`, `"name": "Build"`, `"total": 1`} {
				if !strings.Contains(incomingCallsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp incoming calls output missing %s", expected)
				}
			}

			outgoingCallsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"outgoing_calls","query":"UseBuild","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "call-hierarchy-outgoing"`, `"source": "static"`, `"query": "UseBuild"`, `"name": "Build"`, `"total": 1`} {
				if !strings.Contains(outgoingCallsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp outgoing calls output missing %s", expected)
				}
			}

			typeHierarchyOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_type_hierarchy","query":"TypeBase"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-type-hierarchy"`, `"source": "static"`, `"name": "TypeBase"`, `"kind": "struct"`, `"total": 1`} {
				if !strings.Contains(typeHierarchyOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare type hierarchy output missing %s", expected)
				}
			}

			typeHierarchySupertypesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"supertypes","query":"TypeChild","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-hierarchy-supertypes"`, `"source": "static"`, `"query": "TypeChild"`, `"name": "TypeBase"`, `"total": 1`} {
				if !strings.Contains(typeHierarchySupertypesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type hierarchy supertypes output missing %s", expected)
				}
			}

			typeHierarchySubtypesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"subtypes","query":"TypeBase","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-hierarchy-subtypes"`, `"source": "static"`, `"query": "TypeBase"`, `"name": "TypeChild"`, `"total": 1`} {
				if !strings.Contains(typeHierarchySubtypesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type hierarchy subtypes output missing %s", expected)
				}
			}

			implementationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"implementation","query":"TypeContract","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "implementation"`, `"source": "static"`, `"query": "TypeContract"`, `"name": "TypeChild"`, `"total": 1`} {
				if !strings.Contains(implementationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp implementation output missing %s", expected)
				}
			}

			referencesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"references","query":"Runner","limit":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "references"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"total": 3`} {
				if !strings.Contains(referencesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp references output missing %s", expected)
				}
			}

			hoverOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"hover","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "hover"`, `"found": true`, `"kind": "function"`, `"symbol": "RunFast"`} {
				if !strings.Contains(hoverOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp hover output missing %s", expected)
				}
			}

			completionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"completion","query":"Run","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "completion"`, `"label": "RunFast"`, `"kind": "function"`} {
				if !strings.Contains(completionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp completion output missing %s", expected)
				}
			}

			completionResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"completion_resolve","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "completion-item-resolve"`, `"source": "static"`, `"found": true`, `"label": "RunFast"`, `"kind": "function"`} {
				if !strings.Contains(completionResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp completion resolve output missing %s", expected)
				}
			}

			rangeFormatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"range_format","path":"pkg/messy.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "range-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"} {
				if !strings.Contains(rangeFormatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp range format output missing %s", expected)
				}
			}

			onTypeFormatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"on_type_format","path":"pkg/messy.go","line":2,"character":18}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "on-type-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`} {
				if !strings.Contains(onTypeFormatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp on type format output missing %s", expected)
				}
			}

			willSaveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"will_save","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "will-save"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"edits": true`} {
				if !strings.Contains(willSaveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp will save output missing %s", expected)
				}
			}

			codeActionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action","path":"pkg/messy.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action"`, `"source": "static"`, `"title": "Format Go file"`, `"kind": "source.format"`, `"title": "Fix all Go source"`, `"kind": "source.fixAll"`, `"total": 2`} {
				if !strings.Contains(codeActionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code action output missing %s", expected)
				}
			}

			organizeActionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action","path":"pkg/imports.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action"`, `"source": "static"`, `"title": "Organize Go imports"`, `"kind": "source.organizeImports"`, `"removed_imports": [`, `"bytes"`, `"duplicate_imports": [`, `"fmt"`} {
				if !strings.Contains(organizeActionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports action output missing %s", expected)
				}
			}

			codeActionResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/messy.go","query":"Format Go file"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "Format Go file"`, `"title": "Format Go file"`, "func messy()"} {
				if !strings.Contains(codeActionResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code action resolve output missing %s", expected)
				}
			}

			organizeResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.organizeImports"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.organizeImports"`, `"title": "Organize Go imports"`, `"kind": "organize_imports"`, `"removed_imports": [`} {
				if !strings.Contains(organizeResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports resolve output missing %s", expected)
				}
			}

			fixAllResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.fixAll"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.fixAll"`, `"title": "Fix all Go source"`, `"kind": "fix_all"`, `"source.organizeImports"`} {
				if !strings.Contains(fixAllResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp fix all resolve output missing %s", expected)
				}
			}

			inlineValueOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"inline_value","path":"pkg/inline.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inline-value"`, `"source": "static"`, `"name": "InlineAnswer"`, `"text": "local = \"codog\""`, `"total": 2`} {
				if !strings.Contains(inlineValueOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inline value output missing %s", expected)
				}
			}

			executeCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"format","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "format"`, `"path": "pkg/messy.go"`, "func messy()"} {
				if !strings.Contains(executeCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp execute command output missing %s", expected)
				}
			}

			organizeCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"source.organizeImports","path":"pkg/imports.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.organizeimports"`, `"path": "pkg/imports.go"`, `"organize_imports": {`, `"removed_imports": [`} {
				if !strings.Contains(organizeCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports execute command output missing %s", expected)
				}
			}

			fixAllCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"source.fixAll","path":"pkg/imports.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.fixall"`, `"path": "pkg/imports.go"`, `"fix_all": {`, `"kind": "fix_all"`, `"source.organizeImports"`} {
				if !strings.Contains(fixAllCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp fix all execute command output missing %s", expected)
				}
			}

			documentDiagnosticOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_diagnostic","path":"pkg/broken.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, `"total": 2`, "MissingSymbol"} {
				if !strings.Contains(documentDiagnosticOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document diagnostic output missing %s", expected)
				}
			}

			workspaceDiagnosticOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_diagnostic"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, "MissingSymbol"} {
				if !strings.Contains(workspaceDiagnosticOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace diagnostic output missing %s", expected)
				}
			}

			diagnosticsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"diagnostics"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "diagnostics"`, `"path": "pkg/broken.go"`, `"line": 3`, "MissingSymbol"} {
				if !strings.Contains(diagnosticsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp diagnostics output missing %s", expected)
				}
			}

			formatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"format","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "format"`, `"kind": "format"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"} {
				if !strings.Contains(formatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp format output missing %s", expected)
				}
			}
			data, err := os.ReadFile(filepath.Join(pkgDir, "messy.go"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if string(data) != messy {
				return localScenarioResult{}, fmt.Errorf("lsp format unexpectedly modified file")
			}

			toolUses := make([]string, 52)
			for i := range toolUses {
				toolUses[i] = "lsp"
			}
			return localScenarioResult{
				Output:       strings.Join([]string{symbolsOut, workspaceSymbolsOut, workspaceSymbolResolveOut, definitionOut, declarationOut, typeDefinitionOut, documentHighlightOut, foldingRangeOut, selectionRangeOut, monikerOut, linkedEditingOut, documentLinkOut, documentLinkResolveOut, documentColorOut, colorPresentationOut, inlayHintOut, inlayHintResolveOut, signatureHelpOut, codeLensOut, codeLensResolveOut, semanticTokensOut, semanticTokensRangeOut, semanticTokensDeltaOut, prepareRenameOut, renameOut, callHierarchyOut, incomingCallsOut, outgoingCallsOut, typeHierarchyOut, typeHierarchySupertypesOut, typeHierarchySubtypesOut, implementationOut, referencesOut, hoverOut, completionOut, completionResolveOut, rangeFormatOut, onTypeFormatOut, willSaveOut, codeActionOut, organizeActionOut, codeActionResolveOut, organizeResolveOut, fixAllResolveOut, inlineValueOut, executeCommandOut, organizeCommandOut, fixAllCommandOut, documentDiagnosticOut, workspaceDiagnosticOut, diagnosticsOut, formatOut}, "\n"),
				FinalMessage: "lsp static harness ok",
				ToolCalls:    52,
				ToolUses:     toolUses,
				RequestCount: 52,
			}, nil
		},
	}
}

func lspCLIMetadataScenario() scenario {
	return scenario{
		name: "lsp_cli_metadata_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
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

			actionsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "actions", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var actions lspActionsHarnessReport
			if err := json.Unmarshal([]byte(actionsOut), &actions); err != nil {
				return localScenarioResult{}, err
			}
			if actions.Kind != "lsp_actions" || actions.Action != "actions" || actions.Status != "ok" || actions.Count < 40 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp actions report: %#v", actions)
			}
			if !strings.Contains(actionsOut, `"name": "definition"`) || !strings.Contains(actionsOut, `"method": "textDocument/definition"`) || !strings.Contains(actionsOut, `"name": "references"`) {
				return localScenarioResult{}, fmt.Errorf("lsp actions output missing expected actions: %s", actionsOut)
			}

			discoverOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var discover lspDiscoverHarnessReport
			if err := json.Unmarshal([]byte(discoverOut), &discover); err != nil {
				return localScenarioResult{}, err
			}
			if discover.Kind != "lsp_discover" || discover.Action != "discover" || discover.Status != "ok" || discover.Count < 5 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp discover report: %#v", discover)
			}
			if !lspHarnessCandidateExists(discover.Candidates, "go", "gopls") || !lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer") {
				return localScenarioResult{}, fmt.Errorf("lsp discover candidates missing expected defaults: %#v", discover.Candidates)
			}

			listOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "list", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var list lspListHarnessReport
			if err := json.Unmarshal([]byte(listOut), &list); err != nil {
				return localScenarioResult{}, err
			}
			if list.Kind != "lsp_list" || list.Action != "list" || list.Status != "ok" || list.Count != 0 || len(list.Servers) != 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp list report: %#v", list)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--output-format", "text")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "LSP Discover") || !strings.Contains(textOut, "gopls") || !strings.Contains(textOut, "rust-analyzer") {
				return localScenarioResult{}, fmt.Errorf("lsp discover text output missing expected values: %s", textOut)
			}

			report := map[string]any{
				"kind": "lsp_cli_metadata",
				"lsp": map[string]any{
					"actions":       actions.Count,
					"candidates":    discover.Count,
					"servers":       list.Count,
					"has_go":        lspHarnessCandidateExists(discover.Candidates, "go", "gopls"),
					"has_rust":      lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer"),
					"text_rendered": strings.Contains(textOut, "LSP Discover"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "lsp cli metadata harness ok",
				RequestCount: 4,
				MessageCount: 1,
			}, nil
		},
	}
}

type lspActionsHarnessReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type lspDiscoverHarnessReport struct {
	Kind       string                `json:"kind"`
	Action     string                `json:"action"`
	Status     string                `json:"status"`
	Count      int                   `json:"count"`
	Candidates []lspHarnessCandidate `json:"candidates"`
}

type lspListHarnessReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Count   int               `json:"count"`
	Servers []json.RawMessage `json:"servers"`
}

type lspHarnessCandidate struct {
	Language string `json:"language"`
	Command  string `json:"command"`
}

func lspHarnessCandidateExists(candidates []lspHarnessCandidate, language string, command string) bool {
	for _, candidate := range candidates {
		if candidate.Language == language && candidate.Command == command {
			return true
		}
	}
	return false
}

func pluginLifecycleScenario() scenario {
	var installedRoot string
	var disabledRoot string
	return scenario{
		name:   "plugin_lifecycle_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "plugin lifecycle harness ok"}},
		prompt: "verify plugin lifecycle",
		setup: func(workspace string) error {
			source := filepath.Join(workspace, "plugin-source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				return err
			}
			manifest := `{"id":"lifecycle","name":"lifecycle","version":"1.0.0","description":"Lifecycle harness plugin","lifecycle":{"init":["echo init-ok > lifecycle-init.txt"],"shutdown":["echo shutdown-ok > lifecycle-shutdown.txt"]},"tools":[{"name":"lifecycle_tool","command":"cat","permission":"read-only"}]}`
			if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(source, "tool.sh"), []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
				return err
			}
			installed, err := plugins.Install(workspace, source)
			if err != nil {
				return err
			}
			installedRoot = installed.Root
			if !installed.Enabled {
				return fmt.Errorf("installed plugin is disabled")
			}
			initRun := plugins.RunLifecycle(context.Background(), installed, "init", 5*time.Second)
			if initRun.Status != "ok" {
				return fmt.Errorf("init lifecycle failed: %s", initRun.Message)
			}
			initMarker, err := os.ReadFile(filepath.Join(installed.Root, "lifecycle-init.txt"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(initMarker), "init-ok") {
				return fmt.Errorf("init lifecycle marker mismatch: %q", string(initMarker))
			}
			shutdownRun := plugins.RunLifecycle(context.Background(), installed, "shutdown", 5*time.Second)
			if shutdownRun.Status != "ok" {
				return fmt.Errorf("shutdown lifecycle failed: %s", shutdownRun.Message)
			}
			shutdownMarker, err := os.ReadFile(filepath.Join(installed.Root, "lifecycle-shutdown.txt"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(shutdownMarker), "shutdown-ok") {
				return fmt.Errorf("shutdown lifecycle marker mismatch: %q", string(shutdownMarker))
			}
			disabled, err := plugins.Disable(workspace, installed.ID)
			if err != nil {
				return err
			}
			disabledRoot = disabled.Root
			if disabled.Enabled {
				return fmt.Errorf("disabled plugin still reports enabled")
			}
			if _, err := os.Stat(filepath.Join(disabled.Root, plugins.DisabledMarker)); err != nil {
				return err
			}
			enabled, err := plugins.Enable(workspace, installed.ID)
			if err != nil {
				return err
			}
			if !enabled.Enabled {
				return fmt.Errorf("enabled plugin still reports disabled")
			}
			if _, err := os.Stat(filepath.Join(enabled.Root, plugins.DisabledMarker)); !os.IsNotExist(err) {
				return fmt.Errorf("disabled marker still present after enable: %v", err)
			}
			if err := plugins.Remove(workspace, installed.ID); err != nil {
				return err
			}
			return nil
		},
		verify: func(_ string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "plugin lifecycle harness ok") {
				return fmt.Errorf("missing plugin lifecycle final response")
			}
			if err := expectToolCalls(result, 0, false); err != nil {
				return err
			}
			for _, root := range []string{installedRoot, disabledRoot} {
				if strings.TrimSpace(root) == "" {
					return fmt.Errorf("missing lifecycle plugin root")
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					return fmt.Errorf("plugin root still exists after remove: %s", root)
				}
			}
			return nil
		},
	}
}

func taskLifecycleScenario() scenario {
	return scenario{
		name: "task_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

			createOut, err := registry.Execute(ctx, "TaskCreateTool", json.RawMessage(`{
				"command": "printf task-output",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				TaskID string          `json:"task_id"`
				Kind   string          `json:"kind"`
				Task   background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(createOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.TaskID == "" || created.Task.ID != created.TaskID || created.Kind != "parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task create output: %s", createOut)
			}

			statusOut, err := registry.Execute(ctx, "task_status", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var status struct {
				TaskID string `json:"task_id"`
				Kind   string `json:"kind"`
			}
			if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
				return localScenarioResult{}, err
			}
			if status.TaskID != created.TaskID || status.Kind != "parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task status output: %s", statusOut)
			}

			outputOut, err := registry.Execute(ctx, "TaskOutputTool", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var output struct {
				TaskID        string `json:"task_id"`
				Status        string `json:"status"`
				Stdout        string `json:"stdout"`
				HasOutput     bool   `json:"has_output"`
				RawOutputPath string `json:"rawOutputPath"`
			}
			if err := json.Unmarshal([]byte(outputOut), &output); err != nil {
				return localScenarioResult{}, err
			}
			if output.TaskID != created.TaskID || !output.HasOutput || output.Stdout != "task-output" {
				return localScenarioResult{}, fmt.Errorf("unexpected task output: %s", outputOut)
			}
			if _, err := os.Stat(output.RawOutputPath); err != nil {
				return localScenarioResult{}, fmt.Errorf("task raw output path missing: %w", err)
			}

			updateOut, err := registry.Execute(ctx, "task_update", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"message": "review logs"
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var updated struct {
				TaskID       string `json:"task_id"`
				MessageCount int    `json:"message_count"`
				LastMessage  string `json:"last_message"`
			}
			if err := json.Unmarshal([]byte(updateOut), &updated); err != nil {
				return localScenarioResult{}, err
			}
			if updated.TaskID != created.TaskID || updated.MessageCount != 1 || updated.LastMessage != "review logs" {
				return localScenarioResult{}, fmt.Errorf("unexpected task update output: %s", updateOut)
			}

			getOut, err := registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				TaskID string `json:"task_id"`
				Task   struct {
					Messages []background.TaskMessage `json:"messages"`
				} `json:"task"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.TaskID != created.TaskID || len(fetched.Task.Messages) != 1 || fetched.Task.Messages[0].Message != "review logs" {
				return localScenarioResult{}, fmt.Errorf("unexpected task get output: %s", getOut)
			}

			listOut, err := registry.Execute(ctx, "task_list", json.RawMessage(`{"session_id":"session-task","kind":"parity"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listed struct {
				Total int `json:"total"`
				Tasks []struct {
					TaskID string `json:"task_id"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
				return localScenarioResult{}, err
			}
			if listed.Total != 1 || len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != created.TaskID {
				return localScenarioResult{}, fmt.Errorf("unexpected task list output: %s", listOut)
			}

			stopCreateOut, err := registry.Execute(ctx, "task_create", json.RawMessage(`{
				"command": "printf task-stop-ready; sleep 5",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopCreated struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal([]byte(stopCreateOut), &stopCreated); err != nil {
				return localScenarioResult{}, err
			}
			if stopCreated.TaskID == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected stoppable task output: %s", stopCreateOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, stopCreated.TaskID)), nil)
			}()

			stopReadyOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, stopCreated.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(stopReadyOut, "task-stop-ready") {
				return localScenarioResult{}, fmt.Errorf("stoppable task did not produce readiness output: %s", stopReadyOut)
			}
			stopOut, err := registry.Execute(ctx, "TaskStopTool", json.RawMessage(fmt.Sprintf(`{"shell_id":%q}`, stopCreated.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopped struct {
				TaskID      string `json:"task_id"`
				Status      string `json:"status"`
				Message     string `json:"message"`
				Interrupted bool   `json:"interrupted"`
			}
			if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
				return localScenarioResult{}, err
			}
			if stopped.TaskID != stopCreated.TaskID || stopped.Status != "stopped" || stopped.Message != "Task stopped" {
				return localScenarioResult{}, fmt.Errorf("unexpected task stop output: %s", stopOut)
			}

			report := map[string]any{
				"kind": "task_lifecycle",
				"task": map[string]any{
					"id":           created.TaskID,
					"status":       output.Status,
					"stdout":       output.Stdout,
					"message":      updated.LastMessage,
					"listed_total": listed.Total,
					"raw_output":   filepath.Base(output.RawOutputPath),
				},
				"stopped": map[string]any{
					"id":          stopped.TaskID,
					"status":      stopped.Status,
					"interrupted": stopped.Interrupted,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "task lifecycle harness ok",
				RequestCount: 9,
				MessageCount: 1,
				ToolCalls:    9,
				ToolUses: []string{
					"task_create",
					"task_status",
					"task_output",
					"task_update",
					"task_get",
					"task_list",
					"task_create",
					"task_output",
					"task_stop",
				},
			}, nil
		},
	}
}

func taskPacketRoundtripScenario() scenario {
	return scenario{
		name: "task_packet_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			moduleDir := filepath.Join(workspace, "internal", "taskpacket")
			if err := os.MkdirAll(moduleDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(moduleDir, "taskpacket.go"), []byte("package taskpacket\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			shim := filepath.Join(workspace, "task-packet-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'packet:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome: configHome,
				Executable: shim,
			})

			runOut, err := registry.Execute(ctx, "RunTaskPacketTool", json.RawMessage(`{
				"objective": "Implement typed task packet parity",
				"scope": "module",
				"scope_path": "internal/taskpacket",
				"repo": "codog",
				"worktree": "reviewer",
				"branch_policy": "main only",
				"acceptance_tests": ["go test ./internal/taskpacket"],
				"acceptance_criteria": ["packet validates", "packet persists"],
				"resources": [{"kind": "module", "value": "internal/taskpacket"}],
				"model": "claude-test",
				"provider": "anthropic",
				"permission_profile": "workspace-write",
				"commit_policy": "single focused commit",
				"reporting_targets": ["leader"],
				"recovery_policy": "retry once with narrowed scope",
				"verification_plan": ["go test ./internal/taskpacket"]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				TaskID        string `json:"task_id"`
				Status        string `json:"status"`
				Description   string `json:"description"`
				Prompt        string `json:"prompt"`
				ResolvedScope struct {
					Scope        string `json:"scope"`
					Path         string `json:"path"`
					AbsolutePath string `json:"absolute_path"`
				} `json:"resolved_scope"`
				TaskPacket struct {
					Objective          string   `json:"objective"`
					Scope              string   `json:"scope"`
					ScopePath          string   `json:"scope_path"`
					Repo               string   `json:"repo"`
					Worktree           string   `json:"worktree"`
					AcceptanceCriteria []string `json:"acceptance_criteria"`
					Model              string   `json:"model"`
					Provider           string   `json:"provider"`
					PermissionProfile  string   `json:"permission_profile"`
					ReportingTargets   []string `json:"reporting_targets"`
					VerificationPlan   []string `json:"verification_plan"`
				} `json:"task_packet"`
				Task background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(runOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.TaskID == "" || created.Status != "running" || created.Description != "Implement typed task packet parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet create output: %s", runOut)
			}
			if created.Task.Kind != "task_packet" || created.Task.ID != created.TaskID || len(created.Task.TaskPacket) == 0 {
				return localScenarioResult{}, fmt.Errorf("task packet metadata was not persisted on task: %s", runOut)
			}
			if created.ResolvedScope.Scope != "module" || created.ResolvedScope.Path != "internal/taskpacket" || filepath.Clean(created.ResolvedScope.AbsolutePath) != filepath.Clean(moduleDir) {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet scope resolution: %s", runOut)
			}
			if created.TaskPacket.Objective != "Implement typed task packet parity" ||
				created.TaskPacket.Scope != "module" ||
				created.TaskPacket.Repo != "codog" ||
				created.TaskPacket.Worktree != "reviewer" ||
				created.TaskPacket.Model != "claude-test" ||
				created.TaskPacket.Provider != "anthropic" ||
				created.TaskPacket.PermissionProfile != "workspace-write" ||
				!slices.Equal(created.TaskPacket.AcceptanceCriteria, []string{"packet validates", "packet persists"}) ||
				!slices.Equal(created.TaskPacket.ReportingTargets, []string{"leader"}) ||
				!slices.Equal(created.TaskPacket.VerificationPlan, []string{"go test ./internal/taskpacket"}) {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet payload: %s", runOut)
			}
			var persisted map[string]any
			if err := json.Unmarshal(created.Task.TaskPacket, &persisted); err != nil {
				return localScenarioResult{}, err
			}
			if persisted["objective"] != "Implement typed task packet parity" || persisted["scope"] != "module" || persisted["provider"] != "anthropic" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted task packet: %#v", persisted)
			}

			outputOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 10000
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(outputOut, "packet:prompt") || !strings.Contains(outputOut, "Implement typed task packet parity") || !strings.Contains(outputOut, "Verification plan:") {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet output: %s", outputOut)
			}

			getOut, err := registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				TaskID string          `json:"task_id"`
				Task   background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.TaskID != created.TaskID || fetched.Task.Kind != "task_packet" || len(fetched.Task.TaskPacket) == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected fetched task packet task: %s", getOut)
			}

			stopOut, err := registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopped struct {
				TaskID string `json:"task_id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
				return localScenarioResult{}, err
			}
			if stopped.TaskID != created.TaskID || stopped.Status != "stopped" {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet stop output: %s", stopOut)
			}

			report := map[string]any{
				"kind": "task_packet_roundtrip",
				"task_packet": map[string]any{
					"task_id":            created.TaskID,
					"scope":              created.TaskPacket.Scope,
					"scope_path":         created.TaskPacket.ScopePath,
					"repo":               created.TaskPacket.Repo,
					"model":              created.TaskPacket.Model,
					"provider":           created.TaskPacket.Provider,
					"permission_profile": created.TaskPacket.PermissionProfile,
					"criteria":           len(created.TaskPacket.AcceptanceCriteria),
					"verification_steps": len(created.TaskPacket.VerificationPlan),
					"stopped":            stopped.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "task packet harness ok",
				RequestCount: 4,
				MessageCount: 1,
				ToolCalls:    4,
				ToolUses: []string{
					"run_task_packet",
					"task_output",
					"task_get",
					"task_stop",
				},
			}, nil
		},
	}
}

func teamCronLifecycleScenario() scenario {
	return scenario{
		name: "team_cron_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			shim := filepath.Join(workspace, "team-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'team-shim:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome: configHome,
				Executable: shim,
			})

			teamCreateOut, err := registry.Execute(ctx, "TeamCreateTool", json.RawMessage(`{
				"name": "review",
				"session_id": "session-team",
				"tasks": [
					{"description": "auth", "prompt": "check auth flow"},
					{"description": "tests", "prompt": "check test suite"}
				]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var createdTeam struct {
				ID        string   `json:"team_id"`
				Name      string   `json:"name"`
				TaskCount int      `json:"task_count"`
				TaskIDs   []string `json:"task_ids"`
				Status    string   `json:"status"`
			}
			if err := json.Unmarshal([]byte(teamCreateOut), &createdTeam); err != nil {
				return localScenarioResult{}, err
			}
			if createdTeam.ID == "" || createdTeam.Name != "review" || createdTeam.TaskCount != 2 || len(createdTeam.TaskIDs) != 2 || createdTeam.Status != "running" {
				return localScenarioResult{}, fmt.Errorf("unexpected team create output: %s", teamCreateOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			}()

			taskStore := background.NewStore(configHome)
			if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[0], "Task: auth", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[1], "check test suite", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}

			teamListOut, err := registry.Execute(ctx, "team_list", json.RawMessage(`{"status":"running"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listedTeams struct {
				Kind  string `json:"kind"`
				Total int    `json:"total"`
				Teams []struct {
					ID           string           `json:"team_id"`
					TaskStatuses []map[string]any `json:"task_statuses"`
				} `json:"teams"`
			}
			if err := json.Unmarshal([]byte(teamListOut), &listedTeams); err != nil {
				return localScenarioResult{}, err
			}
			if listedTeams.Kind != "team_list" || listedTeams.Total != 1 || len(listedTeams.Teams) != 1 || listedTeams.Teams[0].ID != createdTeam.ID || len(listedTeams.Teams[0].TaskStatuses) != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected team list output: %s", teamListOut)
			}

			teamGetOut, err := registry.Execute(ctx, "TeamGetTool", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetchedTeam struct {
				Kind      string `json:"kind"`
				ID        string `json:"team_id"`
				TaskCount int    `json:"task_count"`
				Tasks     []struct {
					Description string `json:"description"`
					Prompt      string `json:"prompt"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(teamGetOut), &fetchedTeam); err != nil {
				return localScenarioResult{}, err
			}
			if fetchedTeam.Kind != "team" || fetchedTeam.ID != createdTeam.ID || fetchedTeam.TaskCount != 2 || len(fetchedTeam.Tasks) != 2 || fetchedTeam.Tasks[0].Description != "auth" {
				return localScenarioResult{}, fmt.Errorf("unexpected team get output: %s", teamGetOut)
			}

			teamDeleteOut, err := registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var deletedTeam struct {
				ID           string   `json:"team_id"`
				Status       string   `json:"status"`
				StoppedTasks []string `json:"stopped_tasks"`
				Message      string   `json:"message"`
			}
			if err := json.Unmarshal([]byte(teamDeleteOut), &deletedTeam); err != nil {
				return localScenarioResult{}, err
			}
			if deletedTeam.ID != createdTeam.ID || deletedTeam.Status != "deleted" || deletedTeam.Message != "Team deleted" || len(deletedTeam.StoppedTasks) != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected team delete output: %s", teamDeleteOut)
			}

			cronCreateOut, err := registry.Execute(ctx, "CronCreateTool", json.RawMessage(`{
				"schedule": "0 9 * * 1",
				"prompt": "review weekly status",
				"description": "weekly review"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var createdCron struct {
				ID          string `json:"cron_id"`
				Schedule    string `json:"schedule"`
				Prompt      string `json:"prompt"`
				Description string `json:"description"`
				Enabled     bool   `json:"enabled"`
			}
			if err := json.Unmarshal([]byte(cronCreateOut), &createdCron); err != nil {
				return localScenarioResult{}, err
			}
			if createdCron.ID == "" || createdCron.Schedule != "0 9 * * 1" || createdCron.Prompt != "review weekly status" || createdCron.Description != "weekly review" || !createdCron.Enabled {
				return localScenarioResult{}, fmt.Errorf("unexpected cron create output: %s", cronCreateOut)
			}

			cronListOut, err := registry.Execute(ctx, "cron_list", json.RawMessage(`{}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listedCrons struct {
				Count int `json:"count"`
				Crons []struct {
					ID       string `json:"cron_id"`
					Schedule string `json:"schedule"`
				} `json:"crons"`
			}
			if err := json.Unmarshal([]byte(cronListOut), &listedCrons); err != nil {
				return localScenarioResult{}, err
			}
			if listedCrons.Count != 1 || len(listedCrons.Crons) != 1 || listedCrons.Crons[0].ID != createdCron.ID || listedCrons.Crons[0].Schedule != createdCron.Schedule {
				return localScenarioResult{}, fmt.Errorf("unexpected cron list output: %s", cronListOut)
			}

			cronDeleteOut, err := registry.Execute(ctx, "CronDeleteTool", json.RawMessage(fmt.Sprintf(`{"cron_id":%q}`, createdCron.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var deletedCron struct {
				ID      string `json:"cron_id"`
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(cronDeleteOut), &deletedCron); err != nil {
				return localScenarioResult{}, err
			}
			if deletedCron.ID != createdCron.ID || deletedCron.Status != "deleted" || deletedCron.Message != "Cron entry removed" {
				return localScenarioResult{}, fmt.Errorf("unexpected cron delete output: %s", cronDeleteOut)
			}

			report := map[string]any{
				"kind": "team_cron_lifecycle",
				"team": map[string]any{
					"id":            createdTeam.ID,
					"task_count":    createdTeam.TaskCount,
					"listed_total":  listedTeams.Total,
					"deleted":       deletedTeam.Status,
					"stopped_tasks": len(deletedTeam.StoppedTasks),
				},
				"cron": map[string]any{
					"id":       createdCron.ID,
					"schedule": createdCron.Schedule,
					"listed":   listedCrons.Count,
					"deleted":  deletedCron.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "team cron lifecycle harness ok",
				RequestCount: 7,
				MessageCount: 1,
				ToolCalls:    7,
				ToolUses: []string{
					"team_create",
					"team_list",
					"team_get",
					"team_delete",
					"cron_create",
					"cron_list",
					"cron_delete",
				},
			}, nil
		},
	}
}

func workerLifecycleScenario() scenario {
	return scenario{
		name: "worker_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			shim := filepath.Join(workspace, "worker-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'worker:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome:   configHome,
				Executable:   shim,
				TrustedRoots: []string{"repo-default", "shared"},
			})

			createOut, err := registry.Execute(ctx, "WorkerCreateTool", json.RawMessage(`{
				"cwd": ".",
				"trusted_roots": ["shared", "."],
				"auto_recover_prompt_misdelivery": false
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				WorkerID                     string   `json:"worker_id"`
				Status                       string   `json:"status"`
				ReadyForPrompt               bool     `json:"ready_for_prompt"`
				TrustedRoots                 []string `json:"trusted_roots"`
				AutoRecoverPromptMisdelivery bool     `json:"auto_recover_prompt_misdelivery"`
			}
			if err := json.Unmarshal([]byte(createOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.WorkerID == "" || created.Status != "ready_for_prompt" || !created.ReadyForPrompt || created.AutoRecoverPromptMisdelivery || !slices.Equal(created.TrustedRoots, []string{"repo-default", "shared", "."}) {
				return localScenarioResult{}, fmt.Errorf("unexpected worker create output: %s", createOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "worker_terminate", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			}()

			listOut, err := registry.Execute(ctx, "worker_list", json.RawMessage(`{"status":"ready_for_prompt"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listed struct {
				Kind    string `json:"kind"`
				Total   int    `json:"total"`
				Workers []struct {
					WorkerID string `json:"worker_id"`
				} `json:"workers"`
			}
			if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
				return localScenarioResult{}, err
			}
			if listed.Kind != "worker_list" || listed.Total != 1 || len(listed.Workers) != 1 || listed.Workers[0].WorkerID != created.WorkerID {
				return localScenarioResult{}, fmt.Errorf("unexpected worker list output: %s", listOut)
			}

			readyOut, err := registry.Execute(ctx, "worker_await_ready", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var ready struct {
				WorkerID       string `json:"worker_id"`
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
			}
			if err := json.Unmarshal([]byte(readyOut), &ready); err != nil {
				return localScenarioResult{}, err
			}
			if ready.WorkerID != created.WorkerID || ready.Status != "ready_for_prompt" || !ready.ReadyForPrompt {
				return localScenarioResult{}, fmt.Errorf("unexpected worker ready output: %s", readyOut)
			}

			observeOut, err := registry.Execute(ctx, "WorkerObserveTool", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"screen_text": "Do you trust this folder?"
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var observed struct {
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
			}
			if err := json.Unmarshal([]byte(observeOut), &observed); err != nil {
				return localScenarioResult{}, err
			}
			if observed.Status != "trust_prompt" || observed.ReadyForPrompt {
				return localScenarioResult{}, fmt.Errorf("unexpected worker observe output: %s", observeOut)
			}

			resolveOut, err := registry.Execute(ctx, "worker_resolve_trust", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var resolved struct {
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
				TrustResolved  bool   `json:"trust_resolved"`
			}
			if err := json.Unmarshal([]byte(resolveOut), &resolved); err != nil {
				return localScenarioResult{}, err
			}
			if resolved.Status != "ready_for_prompt" || !resolved.ReadyForPrompt || !resolved.TrustResolved {
				return localScenarioResult{}, fmt.Errorf("unexpected worker trust resolution output: %s", resolveOut)
			}

			sendOut, err := registry.Execute(ctx, "worker_send_prompt", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"prompt": "implement worker tests",
				"task_receipt": {
					"repo": "codog",
					"task_kind": "test",
					"source_surface": "tool",
					"objective_preview": "implement worker tests"
				}
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var sent struct {
				Status      string `json:"status"`
				TaskID      string `json:"task_id"`
				TaskReceipt struct {
					Repo             string `json:"repo"`
					TaskKind         string `json:"task_kind"`
					SourceSurface    string `json:"source_surface"`
					ObjectivePreview string `json:"objective_preview"`
				} `json:"task_receipt"`
			}
			if err := json.Unmarshal([]byte(sendOut), &sent); err != nil {
				return localScenarioResult{}, err
			}
			if sent.Status != "running" || sent.TaskID == "" || sent.TaskReceipt.Repo != "codog" || sent.TaskReceipt.ObjectivePreview != "implement worker tests" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker send output: %s", sendOut)
			}
			if _, err := waitForBackgroundLogs(ctx, background.NewStore(configHome), sent.TaskID, "implement worker tests", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}

			getOut, err := registry.Execute(ctx, "WorkerGetTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				WorkerID   string `json:"worker_id"`
				Status     string `json:"status"`
				TaskID     string `json:"task_id"`
				TaskStatus string `json:"task_status"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.WorkerID != created.WorkerID || fetched.Status != "running" || fetched.TaskID != sent.TaskID || fetched.TaskStatus == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker get output: %s", getOut)
			}

			restartOut, err := registry.Execute(ctx, "worker_restart", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var restarted struct {
				Status string `json:"status"`
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal([]byte(restartOut), &restarted); err != nil {
				return localScenarioResult{}, err
			}
			if restarted.Status != "running" || restarted.TaskID == "" || restarted.TaskID == sent.TaskID {
				return localScenarioResult{}, fmt.Errorf("unexpected worker restart output: %s", restartOut)
			}

			completeOut, err := registry.Execute(ctx, "worker_observe_completion", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"finish_reason": "stop",
				"tokens_output": 12
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var completed struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(completeOut), &completed); err != nil {
				return localScenarioResult{}, err
			}
			if completed.Status != "finished" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker completion output: %s", completeOut)
			}

			timeoutOut, err := registry.Execute(ctx, "worker_startup_timeout", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"last_lifecycle_state": "trust_prompt",
				"pane_command": "codog repl",
				"transport_healthy": true,
				"mcp_healthy": true,
				"elapsed_seconds": 42,
				"trust_prompt_detected": true
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var timedOut struct {
				Status            string `json:"status"`
				LastError         string `json:"last_error"`
				StartupNoEvidence struct {
					Classification string `json:"classification"`
				} `json:"startup_no_evidence"`
			}
			if err := json.Unmarshal([]byte(timeoutOut), &timedOut); err != nil {
				return localScenarioResult{}, err
			}
			if timedOut.Status != "failed" || timedOut.LastError != "startup_no_evidence: trust_required" || timedOut.StartupNoEvidence.Classification != "trust_required" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker startup timeout output: %s", timeoutOut)
			}

			terminateOut, err := registry.Execute(ctx, "WorkerTerminateTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var terminated struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(terminateOut), &terminated); err != nil {
				return localScenarioResult{}, err
			}
			if terminated.Status != "terminated" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker terminate output: %s", terminateOut)
			}

			report := map[string]any{
				"kind": "worker_lifecycle",
				"worker": map[string]any{
					"id":                created.WorkerID,
					"trusted_roots":     created.TrustedRoots,
					"trust_resolved":    resolved.TrustResolved,
					"prompt_task":       sent.TaskID,
					"restarted_task":    restarted.TaskID,
					"completion_status": completed.Status,
					"startup_failure":   timedOut.StartupNoEvidence.Classification,
					"terminal_status":   terminated.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "worker lifecycle harness ok",
				RequestCount: 11,
				MessageCount: 1,
				ToolCalls:    11,
				ToolUses: []string{
					"worker_create",
					"worker_list",
					"worker_await_ready",
					"worker_observe",
					"worker_resolve_trust",
					"worker_send_prompt",
					"worker_get",
					"worker_restart",
					"worker_observe_completion",
					"worker_startup_timeout",
					"worker_terminate",
				},
			}, nil
		},
	}
}

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
			defer resp.Body.Close()
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
				defer resp.Body.Close()
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func acpStdioScenario() scenario {
	return scenario{
		name: "acp_stdio_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
				`{"jsonrpc":"2.0","id":2,"method":"status","params":{}}`,
				`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{}}`,
				`{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}`,
				"",
			}, "\n")
			var out bytes.Buffer
			err := acpserver.Serve(ctx, strings.NewReader(input), &out, acpserver.Handlers{
				Status: func(context.Context) (any, error) {
					return map[string]any{"kind": "acp", "status": "ok", "workspace": workspace}, nil
				},
				NewSession: func(context.Context) (acpserver.SessionInfo, error) {
					return acpserver.SessionInfo{SessionID: "acp-harness-session", Workspace: workspace}, nil
				},
			}, acpserver.Options{Version: "harness", Workspace: workspace})
			if err != nil {
				return localScenarioResult{}, err
			}
			responses, err := decodeLocalJSONRPCResponses(out.String())
			if err != nil {
				return localScenarioResult{}, err
			}
			if len(responses) != 4 {
				return localScenarioResult{}, fmt.Errorf("expected 4 ACP responses, got %d", len(responses))
			}
			initialize, ok := responses[0]["result"].(map[string]any)
			if !ok {
				return localScenarioResult{}, fmt.Errorf("initialize response missing result")
			}
			serverInfo, ok := initialize["serverInfo"].(map[string]any)
			if !ok || serverInfo["version"] != "harness" {
				return localScenarioResult{}, fmt.Errorf("initialize serverInfo missing harness version: %#v", initialize["serverInfo"])
			}
			capabilities, ok := initialize["capabilities"].(map[string]any)
			if !ok || capabilities["prompt"] != true {
				return localScenarioResult{}, fmt.Errorf("initialize capabilities missing prompt support: %#v", initialize["capabilities"])
			}
			status, ok := responses[1]["result"].(map[string]any)
			if !ok || status["status"] != "ok" {
				return localScenarioResult{}, fmt.Errorf("status response was not ok: %#v", responses[1]["result"])
			}
			sessionResult, ok := responses[2]["result"].(map[string]any)
			if !ok || sessionResult["session_id"] != "acp-harness-session" {
				return localScenarioResult{}, fmt.Errorf("session/new response missing session id: %#v", responses[2]["result"])
			}
			if _, ok := responses[3]["result"]; !ok {
				return localScenarioResult{}, fmt.Errorf("shutdown response missing result: %#v", responses[3])
			}

			message := "acp stdio harness ok"
			return localScenarioResult{
				Output:       strings.TrimSpace(out.String()),
				FinalMessage: message,
				RequestCount: len(responses),
				MessageCount: len(responses),
			}, nil
		},
	}
}

func decodeLocalJSONRPCResponses(output string) ([]map[string]any, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func usageSummaryForResult(result runloop.TurnResult) usage.Summary {
	values := make([]anthropic.Usage, 0, len(result.MessageUsages))
	for _, messageUsage := range result.MessageUsages {
		values = append(values, messageUsage.Usage)
	}
	if summary, ok := usage.ActualSummary(values, "mock"); ok {
		return summary
	}
	return usage.Estimate(result.Messages, "mock")
}

func addUsageSummary(total, next usage.Summary) usage.Summary {
	total.InputTokens += next.InputTokens
	total.OutputTokens += next.OutputTokens
	total.CacheCreationInputTokens += next.CacheCreationInputTokens
	total.CacheReadInputTokens += next.CacheReadInputTokens
	total.TotalTokens += next.TotalTokens
	total.EstimatedUSD = math.Round((total.EstimatedUSD+next.EstimatedUSD)*100000) / 100000
	switch {
	case total.Source == "":
		total.Source = next.Source
	case next.Source != "" && total.Source != next.Source:
		total.Source = "mixed"
	}
	return total
}

func requestMessageCounts(requests []anthropic.Request) []int {
	if len(requests) == 0 {
		return nil
	}
	counts := make([]int, 0, len(requests))
	for _, request := range requests {
		counts = append(counts, len(request.Messages))
	}
	return counts
}

func compactRequestCount(requests []anthropic.Request) int {
	count := 0
	for _, request := range requests {
		if len(request.Messages) == 0 || len(request.Messages[0].Content) == 0 {
			continue
		}
		if strings.Contains(request.Messages[0].Content[0].Text, "auto-compacted") {
			count++
		}
	}
	return count
}

func registryForScenario(workspace string, configHome string, item scenario) (*tools.Registry, error) {
	options := tools.RegistryOptions{ConfigHome: configHome}
	if item.registryOptions != nil {
		options = item.registryOptions(workspace, configHome)
		if options.ConfigHome == "" {
			options.ConfigHome = configHome
		}
	}
	registry := tools.NewRegistryWithOptions(workspace, options)
	if item.configureRegistry != nil {
		if err := item.configureRegistry(registry); err != nil {
			return nil, err
		}
	}
	if !item.plugins {
		return registry, nil
	}
	manifests, err := plugins.Load(workspace)
	if err != nil {
		return nil, err
	}
	for _, manifest := range manifests {
		if !manifest.Enabled {
			continue
		}
		for _, tool := range manifest.Tools {
			if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Command) == "" {
				continue
			}
			if registry.Has(tool.Name) {
				return nil, fmt.Errorf("plugin tool %q conflicts with an existing tool", tool.Name)
			}
			registry.Register(tools.CommandTool{
				Name:        tool.Name,
				Description: tool.Description,
				Schema:      tool.InputSchema,
				Required:    tools.Permission(tool.Permission),
				Command:     tool.Command,
				Args:        tool.Args,
				Workspace:   manifest.Root,
			})
		}
	}
	return registry, nil
}

func expectToolCalls(result runloop.TurnResult, count int, wantError bool) error {
	if len(result.ToolCalls) != count {
		return fmt.Errorf("expected %d tool calls, got %d", count, len(result.ToolCalls))
	}
	for _, call := range result.ToolCalls {
		if call.IsError != wantError {
			return fmt.Errorf("tool %s error=%t, want %t; output=%s", call.Name, call.IsError, wantError, call.Output)
		}
	}
	return nil
}
