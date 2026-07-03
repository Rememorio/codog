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
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/agentruns"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/audit"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/customcommands"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/modelrouting"
	"github.com/Rememorio/codog/internal/oauth"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/policyengine"
	"github.com/Rememorio/codog/internal/providerdiag"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/sandbox"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/skills"
	prompttemplates "github.com/Rememorio/codog/internal/templates"
	"github.com/Rememorio/codog/internal/tools"
	"github.com/Rememorio/codog/internal/updater"
	"github.com/Rememorio/codog/internal/usage"
)

// ReportSchemaVersion is the stable schema identifier for mock parity reports.
const ReportSchemaVersion = "codog.mock_parity.v1"

// ManifestSchemaVersion is the stable schema identifier for mock parity manifests.
const ManifestSchemaVersion = "codog.mock_parity_manifest.v1"

// Report summarizes one deterministic mock parity harness run.
type Report struct {
	SchemaVersion string           `json:"schema_version"`
	OK            bool             `json:"ok"`
	Passed        int              `json:"passed"`
	Total         int              `json:"total"`
	ScenarioCount int              `json:"scenario_count"`
	RequestCount  int              `json:"request_count"`
	Coverage      []CategoryReport `json:"coverage"`
	Workspace     string           `json:"workspace"`
	Output        string           `json:"output"`
	Iterations    int              `json:"iterations"`
	MessageCount  int              `json:"message_count"`
	ToolCalls     int              `json:"tool_calls"`
	UsageSummary  usage.Summary    `json:"usage_summary"`
	EstimatedCost float64          `json:"estimated_cost"`
	Scenarios     []ScenarioReport `json:"scenarios"`
}

// Manifest lists the deterministic mock parity scenarios without running them.
type Manifest struct {
	SchemaVersion string             `json:"schema_version"`
	ScenarioCount int                `json:"scenario_count"`
	Categories    []ManifestCategory `json:"categories"`
	Scenarios     []ManifestScenario `json:"scenarios"`
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
	"bash_output_truncation_roundtrip",
	"bash_permission_prompt_approved",
	"bash_permission_prompt_denied",
	"sandbox_bypass_status_roundtrip",
	"policy_update_sandbox_roundtrip",
	"notebook_read_edit_roundtrip",
	"web_access_roundtrip",
	"git_workspace_roundtrip",
	"plan_todo_roundtrip",
	"lsp_static_roundtrip",
	"plugin_tool_roundtrip",
	"command_skill_template_roundtrip",
	"config_precedence_roundtrip",
	"provider_routing_roundtrip",
	"session_resume_jsonl_roundtrip",
	"resume_slash_command_roundtrip",
	"plugin_lifecycle_roundtrip",
	"background_agent_run_roundtrip",
	"remote_trigger_roundtrip",
	"remote_api_listener_roundtrip",
	"remote_bridge_workspace_roundtrip",
	"mcp_lifecycle_roundtrip",
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
		SchemaVersion: ManifestSchemaVersion,
		ScenarioCount: len(scenarios),
		Categories:    manifestCategories(scenarios),
		Scenarios:     scenarios,
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
		sandboxBypassStatusScenario(),
		policyUpdateSandboxScenario(),
		notebookReadEditScenario(),
		webAccessScenario(),
		gitWorkspaceScenario(),
		planTodoScenario(),
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
		configPrecedenceScenario(),
		providerRoutingScenario(),
		sessionResumeJSONLRoundtripScenario(),
		resumeSlashCommandScenario(),
		pluginLifecycleScenario(),
		backgroundAgentRunScenario(),
		remoteTriggerScenario(),
		remoteAPIListenerScenario(),
		remoteBridgeWorkspaceScenario(),
		mcpLifecycleScenario(),
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
	"git_workspace_roundtrip": {
		Category:    "git-workspace",
		Description: "Exercises git status, diff, log, show, blame, and stale-branch freshness checks against a local repository.",
		ParityRefs:  []string{"Git tools", "Branch freshness", "Workspace state"},
	},
	"plan_todo_roundtrip": {
		Category:    "planning",
		Description: "Enters plan mode, persists a todo list, reads it back, and exits plan mode with the final plan.",
		ParityRefs:  []string{"Plan mode", "Todo tools", "Workspace state"},
	},
	"lsp_static_roundtrip": {
		Category:    "code-intelligence",
		Description: "Queries static Go code intelligence through the LSP tool for symbols, definitions, references, hover, completions, and formatting.",
		ParityRefs:  []string{"LSP tool", "Code intelligence", "IDE bridge"},
	},
	"plugin_lifecycle_roundtrip": {
		Category:    "plugin-paths",
		Description: "Exercises plugin lifecycle metadata loading without invoking a tool.",
		ParityRefs:  []string{"Plugin lifecycle", "Plugin manifest loading"},
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

func lspStaticScenario() scenario {
	return scenario{
		name: "lsp_static_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			pkgDir := filepath.Join(workspace, "pkg")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
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

			definitionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"definition","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "definition"`, `"found": true`, `"name": "RunFast"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(definitionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp definition output missing %s", expected)
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

			return localScenarioResult{
				Output:       strings.Join([]string{symbolsOut, definitionOut, referencesOut, hoverOut, completionOut, formatOut}, "\n"),
				FinalMessage: "lsp static harness ok",
				ToolCalls:    6,
				ToolUses:     []string{"lsp", "lsp", "lsp", "lsp", "lsp", "lsp"},
				RequestCount: 6,
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
	registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})
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
