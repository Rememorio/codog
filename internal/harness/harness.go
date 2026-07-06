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
	"github.com/Rememorio/codog/internal/memory"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/onboarding"
	"github.com/Rememorio/codog/internal/outputstyle"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/policyengine"
	"github.com/Rememorio/codog/internal/providerdiag"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
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
const ReportSchemaVersion = "codog.mock_parity.v1"

// ManifestSchemaVersion is the stable schema identifier for mock parity manifests.
const ManifestSchemaVersion = "codog.mock_parity_manifest.v1"

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
	"worktree_lifecycle_roundtrip",
	"plan_todo_roundtrip",
	"todo_completion_verification_roundtrip",
	"lsp_static_roundtrip",
	"plugin_tool_roundtrip",
	"command_skill_template_roundtrip",
	"onboarding_bookmarks_roundtrip",
	"memory_lifecycle_roundtrip",
	"context_view_roundtrip",
	"output_style_lifecycle_roundtrip",
	"diagnostics_status_roundtrip",
	"statusline_cli_roundtrip",
	"tui_prompt_completion_roundtrip",
	"ask_user_question_roundtrip",
	"runtime_output_tools_roundtrip",
	"repl_runtime_roundtrip",
	"config_precedence_roundtrip",
	"provider_routing_roundtrip",
	"session_resume_jsonl_roundtrip",
	"resume_slash_command_roundtrip",
	"plugin_lifecycle_roundtrip",
	"task_lifecycle_roundtrip",
	"task_packet_roundtrip",
	"team_cron_lifecycle_roundtrip",
	"worker_lifecycle_roundtrip",
	"recovery_lifecycle_roundtrip",
	"background_agent_run_roundtrip",
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
		worktreeLifecycleScenario(),
		planTodoScenario(),
		todoCompletionVerificationScenario(),
		lspStaticScenario(),
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
		onboardingBookmarksScenario(),
		memoryLifecycleScenario(),
		contextViewScenario(),
		outputStyleLifecycleScenario(),
		diagnosticsStatusScenario(),
		statuslineCLIScenario(),
		tuiPromptCompletionScenario(),
		askUserQuestionScenario(),
		runtimeOutputToolsScenario(),
		replRuntimeScenario(),
		configPrecedenceScenario(),
		providerRoutingScenario(),
		sessionResumeJSONLRoundtripScenario(),
		resumeSlashCommandScenario(),
		pluginLifecycleScenario(),
		taskLifecycleScenario(),
		taskPacketRoundtripScenario(),
		teamCronLifecycleScenario(),
		workerLifecycleScenario(),
		recoveryLifecycleScenario(),
		backgroundAgentRunScenario(),
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
	{Capability: "policy and approval control plane", RequiredRefs: []string{"Policy evaluation", "Approval tokens", "Delegation audit", "Replay denial"}},
	{Capability: "sessions, resume, and project memory", RequiredRefs: []string{"Session JSONL", "Resume", "Session context management", "Project memory"}},
	{Capability: "slash commands and custom workflows", RequiredRefs: []string{"Slash commands", "Skills", "Templates", "Project workflow surfaces"}},
	{Capability: "hooks", RequiredRefs: []string{"Hooks", "PreToolUse", "PostToolUse hooks", "UserPromptSubmit", "Stop"}},
	{Capability: "configuration and provider routing", RequiredRefs: []string{"Configuration", "Precedence rules", "Provider routing", "OpenAI-compatible APIs"}},
	{Capability: "MCP client and auth", RequiredRefs: []string{"MCP client", "MCP lifecycle", "MCP tool calls", "MCP auth", "OAuth refresh"}},
	{Capability: "token, cost, and compaction", RequiredRefs: []string{"Token usage", "Cost tracking", "Auto-compaction"}},
	{Capability: "IDE bridge and remote control", RequiredRefs: []string{"IDE bridge", "ACP/Zed", "Remote sessions", "Control API listener"}},
	{Capability: "multi-agent and background tasks", RequiredRefs: []string{"Background tasks", "Agent runs", "Lane board", "Supervisor restarts"}},
	{Capability: "structured task packets", RequiredRefs: []string{"Task packet schema", "Task packet scope resolution", "Task packet persistence"}},
	{Capability: "team and scheduled tasks", RequiredRefs: []string{"Team tools", "Team task assignment", "Cron tools", "Cron lifecycle"}},
	{Capability: "worker orchestration", RequiredRefs: []string{"Worker tools", "Worker trust recovery", "Worker prompt delivery", "Worker startup diagnostics"}},
	{Capability: "recovery recipes and ledger", RequiredRefs: []string{"Recovery recipes", "Recovery attempts", "Recovery ledger", "Escalation tracking"}},
	{Capability: "notebook and code intelligence", RequiredRefs: []string{"Notebook read", "Notebook edit", "LSP tool", "Code intelligence"}},
	{Capability: "git and worktree management", RequiredRefs: []string{"Git tools", "Branch freshness", "Worktree allocation", "Worktree cleanup"}},
	{Capability: "OAuth and account lifecycle", RequiredRefs: []string{"OAuth refresh", "Token redaction", "MCP auth"}},
	{Capability: "enterprise policy and updater", RequiredRefs: []string{"Enterprise policy", "Audit events", "Signed updater"}},
	{Capability: "plugins and marketplace", RequiredRefs: []string{"Plugin tools", "Plugin lifecycle", "Plugin manifest loading", "External plugin lifecycle"}},
	{Capability: "TUI and interactive rendering", RequiredRefs: []string{"Bubble Tea TUI", "Interactive rendering", "Output styles"}},
	{Capability: "interactive question handling", RequiredRefs: []string{"AskUserQuestion tool", "Interactive questions"}},
	{Capability: "runtime utility tools", RequiredRefs: []string{"Brief tool", "SendUserMessage tool", "StructuredOutput tool", "Sleep tool", "REPL tool"}},
	{Capability: "setup and diagnostics", RequiredRefs: []string{"Doctor", "Status diagnostics", "Terminal setup"}},
	{Capability: "context view and focus", RequiredRefs: []string{"Context view", "Focused paths", "Context signals"}},
	{Capability: "statusline rendering", RequiredRefs: []string{"Statusline", "Statusline JSON", "Statusline text"}},
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
	"context_view_roundtrip": {
		Category:    "context-management",
		Description: "Builds structured context summaries with memory, focused paths, token estimates, and text plus HTML rendering.",
		ParityRefs:  []string{"Context view", "Focused paths", "Context signals", "Session context management", "Workspace state"},
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
		Description: "Renders the Bubble Tea prompt model, completes a slash command, and captures Ctrl+S submission state without a live terminal.",
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
		ParityRefs:  []string{"Policy evaluation", "Approval tokens", "Delegation audit", "Replay denial", "Tool result roundtrip"},
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
		Description: "Queries static Go code intelligence through the LSP tool for document symbols, workspace symbols, workspace symbol resolve, definitions, declarations, type definitions, implementation lookup, highlights, folding ranges, selection ranges, monikers, linked editing ranges, document links, document colors, inlay hints, inline values, signature help, code lenses, semantic tokens, rename previews, code actions, call hierarchy, type hierarchy, references, hover, completions, completion item resolve, execute command, diagnostics, and formatting.",
		ParityRefs:  []string{"LSP tool", "Code intelligence", "IDE bridge", "Workspace symbols", "Workspace symbol resolve", "Declarations", "Type definitions", "Implementation lookup", "Document highlights", "Folding ranges", "Selection ranges", "Monikers", "Linked editing ranges", "Document links", "Document colors", "Inlay hints", "Inline values", "Signature help", "Code lenses", "Semantic tokens", "Rename previews", "Code actions", "Call hierarchy", "Type hierarchy", "Completion item resolve", "Execute command", "Diagnostics"},
	},
	"plugin_lifecycle_roundtrip": {
		Category:    "plugin-paths",
		Description: "Exercises plugin lifecycle metadata loading without invoking a tool.",
		ParityRefs:  []string{"Plugin lifecycle", "Plugin manifest loading"},
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
	"background_agent_run_roundtrip": {
		Category:    "background-agents",
		Description: "Runs, watches, heartbeats, restarts, supervises, and summarizes a background agent lane.",
		ParityRefs:  []string{"Background tasks", "Agent runs", "Lane board", "Supervisor restarts"},
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
				GitStatus:        "## main",
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
				},
				"doctor": map[string]any{
					"kind":        doctorReport.Kind,
					"status":      doctorReport.Status,
					"checks":      doctorReport.Summary.Total,
					"has_auth":    slices.Contains(doctorReport.CheckNames, "auth"),
					"has_hooks":   slices.Contains(doctorReport.CheckNames, "hooks"),
					"has_sandbox": slices.Contains(doctorReport.CheckNames, "sandbox"),
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
		runLocal: func(_ context.Context, _ string) (localScenarioResult, error) {
			multiple := tui.PreviewWithCandidates("/m", []string{"/memory list", "/model claude-test"}, 96, 24, true, false)
			for _, expected := range []string{"Codog TUI", "Ctrl+S submit", "/memory list", "/model claude-test"} {
				if !strings.Contains(multiple.View, expected) {
					return localScenarioResult{}, fmt.Errorf("TUI preview missing %s", expected)
				}
			}
			if len(multiple.Matches) != 2 {
				return localScenarioResult{}, fmt.Errorf("expected 2 TUI completion matches, got %d", len(multiple.Matches))
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
				"kind":             "tui_prompt_completion",
				"matches":          multiple.Matches,
				"submitted":        submitted.Submitted,
				"submitted_prompt": submitted.Prompt,
				"view_contains":    []string{"Codog TUI", "Ctrl+S submit"},
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
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	commandArgs := append([]string{"run", "./cmd/codog", "--cwd", workspace}, args...)
	cmd := exec.CommandContext(ctx, "go", commandArgs...)
	cmd.Dir = root
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
			permissionOut, err := registry.Execute(ctx, "testing_permission", json.RawMessage(`{
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
				ToolUses:       []string{"testing_permission", "bash", "read_file"},
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

			scope := `"scope":{"policy":"main_push_forbidden","action":"git push","repository":"owner/repo","branch":"main"}`
			grantOut, err := registry.Execute(ctx, "ApprovalTokenTool", json.RawMessage(`{
				"action": "grant",
				"token": "tok-main",
				`+scope+`,
				"approving_actor": "owner",
				"approved_executor": "release-bot",
				"max_uses": 1,
				"delegation_chain": [{"actor":"owner","session_id":"session-owner","reason":"owner approval"}]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var grant struct {
				Kind   string `json:"kind"`
				Action string `json:"action"`
				Status string `json:"status"`
				Grant  struct {
					Token            string `json:"token"`
					Status           string `json:"status"`
					ApprovingActor   string `json:"approving_actor"`
					ApprovedExecutor string `json:"approved_executor"`
					MaxUses          int    `json:"max_uses"`
				} `json:"grant"`
			}
			if err := json.Unmarshal([]byte(grantOut), &grant); err != nil {
				return localScenarioResult{}, err
			}
			if grant.Kind != "approval_token" || grant.Action != "grant" || grant.Status != "ok" || grant.Grant.Token != "tok-main" || grant.Grant.Status != "approval_granted" || grant.Grant.ApprovingActor != "owner" || grant.Grant.ApprovedExecutor != "release-bot" || grant.Grant.MaxUses != 1 {
				return localScenarioResult{}, fmt.Errorf("unexpected approval grant output: %s", grantOut)
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
					Kind               string `json:"kind"`
					Token              string `json:"token"`
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
			if verify.Status != "ok" || verify.Audit.Kind != "approval_token_audit" || verify.Audit.Token != "tok-main" || verify.Audit.Status != "approval_granted" || !verify.Audit.DelegatedExecution || len(verify.Audit.DelegationChain) != 2 || verify.Audit.DelegationChain[1].Actor != "release-bot" {
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
						Token              string `json:"token"`
						Status             string `json:"status"`
						Uses               int    `json:"uses"`
						LastAuditErrorKind string `json:"last_audit_error_kind"`
					} `json:"grants"`
				} `json:"ledger"`
			}
			if err := json.Unmarshal([]byte(listOut), &list); err != nil {
				return localScenarioResult{}, err
			}
			if list.Status != "ok" || list.Ledger.Kind != "approval_token_ledger" || len(list.Ledger.Grants) != 1 || list.Ledger.Grants[0].Token != "tok-main" || list.Ledger.Grants[0].Status != "approval_consumed" || list.Ledger.Grants[0].Uses != 1 || list.Ledger.Grants[0].LastAuditErrorKind != "approval_already_consumed" {
				return localScenarioResult{}, fmt.Errorf("unexpected approval token ledger output: %s", listOut)
			}

			report := map[string]any{
				"kind": "policy_approval",
				"policy": map[string]any{
					"stale_actions":     []string{staleEval.Actions[0].Kind, staleEval.Actions[1].Kind, staleEval.Actions[2].Kind},
					"escalation_action": escalateEval.Actions[0].Kind,
				},
				"approval": map[string]any{
					"token":                grant.Grant.Token,
					"verified":             verify.Audit.Status,
					"delegated":            verify.Audit.DelegatedExecution,
					"consumed":             consume.Audit.Status,
					"replay_error":         replay.ErrorKind,
					"ledger_status":        list.Ledger.Grants[0].Status,
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
				RequestCount: 7,
				MessageCount: 1,
				ToolCalls:    7,
				ToolUses: []string{
					"policy_evaluate",
					"policy_evaluate",
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
				Output:       strings.Join([]string{enterOut, writeOut, readOut, exitOut}, "\n"),
				FinalMessage: "plan todo harness ok",
				ToolCalls:    4,
				ToolUses:     []string{"enter_plan_mode", "todo_write", "todo_read", "exit_plan_mode"},
				RequestCount: 4,
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
			for _, expected := range []string{`"action": "code-action"`, `"source": "static"`, `"title": "Format Go file"`, `"kind": "source.format"`, `"total": 1`} {
				if !strings.Contains(codeActionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code action output missing %s", expected)
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

			toolUses := make([]string, 47)
			for i := range toolUses {
				toolUses[i] = "lsp"
			}
			return localScenarioResult{
				Output:       strings.Join([]string{symbolsOut, workspaceSymbolsOut, workspaceSymbolResolveOut, definitionOut, declarationOut, typeDefinitionOut, documentHighlightOut, foldingRangeOut, selectionRangeOut, monikerOut, linkedEditingOut, documentLinkOut, documentLinkResolveOut, documentColorOut, colorPresentationOut, inlayHintOut, inlayHintResolveOut, signatureHelpOut, codeLensOut, codeLensResolveOut, semanticTokensOut, semanticTokensRangeOut, semanticTokensDeltaOut, prepareRenameOut, renameOut, callHierarchyOut, incomingCallsOut, outgoingCallsOut, typeHierarchyOut, typeHierarchySupertypesOut, typeHierarchySubtypesOut, implementationOut, referencesOut, hoverOut, completionOut, completionResolveOut, rangeFormatOut, onTypeFormatOut, willSaveOut, codeActionOut, codeActionResolveOut, inlineValueOut, executeCommandOut, documentDiagnosticOut, workspaceDiagnosticOut, diagnosticsOut, formatOut}, "\n"),
				FinalMessage: "lsp static harness ok",
				ToolCalls:    47,
				ToolUses:     toolUses,
				RequestCount: 47,
			}, nil
		},
	}
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
			manifest := `{"id":"lifecycle","name":"lifecycle","version":"1.0.0","description":"Lifecycle harness plugin","tools":[{"name":"lifecycle_tool","command":"cat","permission":"read-only"}]}`
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
					"kind":           task.Kind,
					"agent_type":     task.AgentType,
					"session_id":     task.SessionID,
					"watch_events":   []string{events[0].Type, events[1].Type},
					"stopped":        stopped.Status,
					"restarted":      restarted.RestartedFrom == task.ID,
					"lane":           "active",
					"lane_freshness": string(taskBoard.Active[0].Freshness),
				},
				"agent_run": map[string]any{
					"agent":        run.Agent,
					"status":       status.CurrentStatus,
					"freshness":    string(status.Freshness),
					"health":       status.Health.State,
					"active_lanes": len(agentBoard.Active),
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
