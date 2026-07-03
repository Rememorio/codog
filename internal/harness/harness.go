package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rememorio/codog/internal/acpserver"
	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/control"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/tools"
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
	"bash_stdout_roundtrip",
	"bash_output_truncation_roundtrip",
	"bash_permission_prompt_approved",
	"bash_permission_prompt_denied",
	"plugin_tool_roundtrip",
	"config_precedence_roundtrip",
	"session_resume_jsonl_roundtrip",
	"plugin_lifecycle_roundtrip",
	"remote_trigger_roundtrip",
	"remote_api_listener_roundtrip",
	"mcp_lifecycle_roundtrip",
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
		configPrecedenceScenario(),
		sessionResumeJSONLRoundtripScenario(),
		pluginLifecycleScenario(),
		remoteTriggerScenario(),
		remoteAPIListenerScenario(),
		mcpLifecycleScenario(),
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
	"session_resume_jsonl_roundtrip": {
		Category:    "session-resume",
		Description: "Loads previous JSONL session messages and sends them with the resumed prompt.",
		ParityRefs:  []string{"Session JSONL", "Resume"},
	},
	"bash_output_truncation_roundtrip": {
		Category:    "bash",
		Description: "Confirms large bash output is truncated before returning to the model.",
		ParityRefs:  []string{"Bash tool", "Output truncation"},
	},
	"plugin_lifecycle_roundtrip": {
		Category:    "plugin-paths",
		Description: "Exercises plugin lifecycle metadata loading without invoking a tool.",
		ParityRefs:  []string{"Plugin lifecycle", "Plugin manifest loading"},
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
	"mcp_lifecycle_roundtrip": {
		Category:    "mcp-lifecycle",
		Description: "Exercises HTTP MCP initialize, initialized notification, tool discovery, and tool invocation through the control API.",
		ParityRefs:  []string{"MCP client", "MCP lifecycle", "Control API MCP bridge"},
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
