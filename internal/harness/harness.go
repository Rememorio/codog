package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tools"
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
		inlineStreamingTextScenario(),
		inlinePromptAttachmentsRoundtripScenario(),
		promptDirectoryAttachmentScenario(),
		inlineReadFileRoundtripScenario(),
		inlineWriteFileAllowedScenario(),
		inlineWriteFileDeniedScenario(),
		inlinePreToolHookUpdatesInputScenario(),
		inlineUserPromptHookAddsContextScenario(),
		inlineStopHookAddsFeedbackScenario(),
		inlinePostToolHookBlocksResultScenario(),
		inlinePostToolHookAddsFeedbackScenario(),
		inlineFileChangedHookAddsFeedbackScenario(),
		inlineMultiToolTurnRoundtripScenario(),
		inlineGrepChunkAssemblyScenario(),
		editGlobLSScenario(),
		multiEditApplyPatchScenario(),
		inlineBashStdoutRoundtripScenario(),
		bashBackgroundOutputScenario(),
		bashKillScenario(),
		powerShellStdoutScenario(),
		bashOutputTruncationScenario(),
		inlineBashPermissionPromptApprovedScenario(),
		inlineBashPermissionPromptDeniedScenario(),
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
		inlinePluginToolRoundtripScenario(),
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
		inlineAutoCompactTriggeredScenario(),
		inlineTokenCostReportingScenario(),
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
	defer func() { _ = os.RemoveAll(workspace) }()
	previous, turns, cleanup, err := prepareScenarioWorkspace(item, workspace)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return scenarioErrorReport(item.name, metadata, workspace, err)
	}
	if item.runLocal != nil {
		return runLocalScenario(ctx, item, metadata, workspace)
	}
	return runProviderScenario(ctx, item, metadata, workspace, previous, turns)
}

func prepareScenarioWorkspace(item scenario, workspace string) ([]anthropic.Message, []mockanthropic.Turn, func(), error) {
	if item.setup != nil {
		if err := item.setup(workspace); err != nil {
			return nil, nil, nil, err
		}
	}
	previous, err := loadScenarioMessages(item, workspace)
	if err != nil {
		return nil, nil, nil, err
	}
	if item.prepare == nil {
		return previous, item.turns, nil, nil
	}
	turns, cleanup, err := item.prepare(workspace)
	return previous, turns, cleanup, err
}

func loadScenarioMessages(item scenario, workspace string) ([]anthropic.Message, error) {
	if item.loadPrevious == nil {
		return item.previous, nil
	}
	return item.loadPrevious(workspace)
}

func runLocalScenario(ctx context.Context, item scenario, metadata scenarioMetadata, workspace string) ScenarioReport {
	result, err := item.runLocal(ctx, workspace)
	report := ScenarioReport{
		Name:           item.name,
		Category:       metadata.Category,
		Description:    metadata.Description,
		ParityRefs:     append([]string(nil), metadata.ParityRefs...),
		Workspace:      workspace,
		Output:         result.Output,
		RequestCount:   result.RequestCount,
		MessageCount:   result.MessageCount,
		ToolCalls:      result.ToolCalls,
		ToolUses:       append([]string(nil), result.ToolUses...),
		ToolErrorCount: result.ToolErrorCount,
		FinalMessage:   result.FinalMessage,
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.OK = true
	return report
}

func runProviderScenario(ctx context.Context, item scenario, metadata scenarioMetadata, workspace string, previous []anthropic.Message, turns []mockanthropic.Turn) ScenarioReport {

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
	return verifyProviderScenario(item, workspace, result, requests, out.String(), runErr, scenarioReport)
}

func verifyProviderScenario(item scenario, workspace string, result runloop.TurnResult, requests []anthropic.Request, output string, runErr error, report ScenarioReport) ScenarioReport {
	if runErr != nil {
		report.Error = runErr.Error()
		return report
	}
	if item.verify != nil {
		if err := item.verify(workspace, result, output); err != nil {
			report.Error = err.Error()
			return report
		}
	}
	if item.verifyRequests != nil {
		if err := item.verifyRequests(requests); err != nil {
			report.Error = err.Error()
			return report
		}
	}
	report.OK = true
	return report
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

func createHarnessConfigFile(workspace string, homeName string, values map[string]any) (string, error) {
	configHome := filepath.Join(workspace, homeName)
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return "", err
	}
	if values == nil {
		values = map[string]any{}
	}
	values["config_home"] = configHome
	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	path := filepath.Join(workspace, "codog-config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func harnessContainsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}

func harnessContainsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func decodeHarnessOutput(target any, execute func() (string, error)) (string, error) {
	output, err := execute()
	if err != nil {
		return output, err
	}
	if err := json.Unmarshal([]byte(output), target); err != nil {
		return output, err
	}
	return output, nil
}

func verifyHarnessFileContainsAny(path string, expected ...string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, item := range expected {
		if strings.Contains(string(data), item) {
			return nil
		}
	}
	return fmt.Errorf("%s does not contain any expected value", path)
}

func verifyHarnessFileOmits(path string, values ...string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, value := range values {
		if strings.Contains(string(data), value) {
			return fmt.Errorf("%s still contains %s", path, value)
		}
	}
	return nil
}

func configPrecedenceScenario() scenario {
	state := &configPrecedenceState{}
	return scenario{
		name:    "config_precedence_roundtrip",
		turns:   []mockanthropic.Turn{{Text: "config precedence harness ok"}},
		prompt:  "verify config precedence",
		prepare: state.prepare,
		verify:  state.verify,
	}
}

type configPrecedenceState struct {
	model        string
	permission   string
	sessionStart []string
	mcpShared    string
}

func (s *configPrecedenceState) prepare(workspace string) ([]mockanthropic.Turn, func(), error) {
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
	if err := setupConfigPrecedenceFiles(workspace, configHome); err != nil {
		return nil, cleanup, err
	}
	if err := os.Setenv("CODOG_CONFIG_HOME", configHome); err != nil {
		return nil, cleanup, err
	}
	if err := os.Chdir(workspace); err != nil {
		return nil, cleanup, err
	}
	cfg, paths, err := config.LoadForInspection(config.FlagOverrides{})
	if err != nil {
		return nil, cleanup, err
	}
	if err := verifyConfigPrecedencePaths(configHome, paths); err != nil {
		return nil, cleanup, err
	}
	if err := verifyConfigPrecedenceValues(cfg); err != nil {
		return nil, cleanup, err
	}
	s.model = cfg.Model
	s.permission = cfg.PermissionMode
	s.sessionStart = append([]string(nil), cfg.Hooks.SessionStart...)
	s.mcpShared = cfg.MCPServers["shared"].Command
	return []mockanthropic.Turn{{Text: "config precedence harness ok"}}, cleanup, nil
}

func setupConfigPrecedenceFiles(workspace string, configHome string) error {
	if err := os.MkdirAll(configHome, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		filepath.Join(configHome, "config.json"):      `{"model":"user-model","permission_mode":"read-only","additional_dirs":["user-dir"],"hooks":{"session_start":["echo user"]},"mcp_servers":{"shared":{"command":"user-shared"},"user_only":{"command":"user-only"}}}`,
		filepath.Join(workspace, ".codog.json"):       `{"model":"project-model","permission_mode":"workspace-write","additional_dirs":["project-dir"],"hooks":{"SessionStart":[{"command":"echo project"}]},"mcp_servers":{"shared":{"command":"project-shared"},"project_only":{"command":"project-only"}}}`,
		filepath.Join(workspace, ".codog.local.json"): `{"model":"local-model","max_tokens":777,"additional_dirs":["local-dir"],"hooks":{"session_start":["echo local"]},"mcp_servers":{"shared":{"command":"local-shared"},"local_only":{"command":"local-only"}}}`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func verifyConfigPrecedencePaths(configHome string, paths []string) error {
	for _, expected := range []string{filepath.Join(configHome, "config.json"), ".codog.json", ".codog.local.json"} {
		if !slices.Contains(paths, expected) {
			return fmt.Errorf("config path %q was not loaded; paths=%v", expected, paths)
		}
	}
	return nil
}

func verifyConfigPrecedenceValues(cfg config.Config) error {
	if cfg.Model != "local-model" || cfg.PermissionMode != "workspace-write" || cfg.MaxTokens != 777 {
		return fmt.Errorf("unexpected scalar config precedence: model=%q permission=%q max_tokens=%d", cfg.Model, cfg.PermissionMode, cfg.MaxTokens)
	}
	if strings.Join(cfg.AdditionalDirs, ",") != "local-dir" {
		return fmt.Errorf("expected local additional_dirs replacement, got %v", cfg.AdditionalDirs)
	}
	if strings.Join(cfg.Hooks.SessionStart, ",") != "echo user,echo project,echo local" {
		return fmt.Errorf("unexpected hook merge order: %v", cfg.Hooks.SessionStart)
	}
	if cfg.MCPServers["shared"].Command != "local-shared" || cfg.MCPServers["user_only"].Command != "user-only" || cfg.MCPServers["project_only"].Command != "project-only" || cfg.MCPServers["local_only"].Command != "local-only" {
		return fmt.Errorf("unexpected mcp server merge: %#v", cfg.MCPServers)
	}
	return nil
}

func (s *configPrecedenceState) verify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "config precedence harness ok") {
		return fmt.Errorf("missing config precedence final response")
	}
	if err := expectToolCalls(result, 0, false); err != nil {
		return err
	}
	if s.model != "local-model" || s.permission != "workspace-write" || s.mcpShared != "local-shared" {
		return fmt.Errorf("unexpected loaded config model=%q permission=%q shared_mcp=%q", s.model, s.permission, s.mcpShared)
	}
	if strings.Join(s.sessionStart, ",") != "echo user,echo project,echo local" {
		return fmt.Errorf("unexpected loaded hook order: %v", s.sessionStart)
	}
	return nil
}

func inlineStreamingTextScenario() scenario {
	return scenario{
		name:   "streaming_text",
		turns:  []mockanthropic.Turn{{Text: "streaming harness ok"}},
		prompt: "stream text",
		verify: inlineStreamingTextScenarioVerify,
	}
}

func inlinePromptAttachmentsRoundtripScenario() scenario {
	return scenario{
		name:   "prompt_attachments_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "attachment harness ok"}},
		prompt: "describe attached image",
		userContent: []anthropic.ContentBlock{
			{Type: "text", Text: "describe attached image"},
			{Type: "image", Title: "pixel.png", Source: &anthropic.ContentSource{Type: "base64", MediaType: "image/png", Data: "aW1n"}},
		},
		verify:         inlinePromptAttachmentsRoundtripScenarioVerify,
		verifyRequests: inlinePromptAttachmentsRoundtripScenarioVerifyRequests,
	}
}

func inlineReadFileRoundtripScenario() scenario {
	return scenario{
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
		setup:  inlineReadFileRoundtripScenarioSetup,
		verify: inlineReadFileRoundtripScenarioVerify,
	}
}

func inlineWriteFileAllowedScenario() scenario {
	return scenario{
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
		verify: inlineWriteFileAllowedScenarioVerify,
	}
}

func inlineWriteFileDeniedScenario() scenario {
	return scenario{
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
		verify:   inlineWriteFileDeniedScenarioVerify,
	}
}

func inlinePreToolHookUpdatesInputScenario() scenario {
	return scenario{
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
		verify: inlinePreToolHookUpdatesInputScenarioVerify,
	}
}

func inlineUserPromptHookAddsContextScenario() scenario {
	return scenario{
		name: "user_prompt_hook_adds_context",
		hooks: config.HookConfig{
			UserPromptSubmitCommands: []config.HookCommand{{
				Command: `printf '%s' '{"systemMessage":"prompt parity note","hookSpecificOutput":{"additionalContext":"prompt parity context"}}'`,
			}},
		},
		turns:          []mockanthropic.Turn{{Text: "prompt hook harness ok"}},
		prompt:         "prompt hook context",
		verify:         inlineUserPromptHookAddsContextScenarioVerify,
		verifyRequests: inlineUserPromptHookAddsContextScenarioVerifyRequests,
	}
}

func inlineStopHookAddsFeedbackScenario() scenario {
	return scenario{
		name: "stop_hook_adds_feedback",
		hooks: config.HookConfig{
			StopCommands: []config.HookCommand{{
				Command: `printf '%s' '{"systemMessage":"stop parity note","hookSpecificOutput":{"additionalContext":"stop parity context"}}'`,
			}},
		},
		turns:  []mockanthropic.Turn{{Text: "stop hook harness ok"}},
		prompt: "stop hook feedback",
		verify: inlineStopHookAddsFeedbackScenarioVerify,
	}
}

func inlinePostToolHookBlocksResultScenario() scenario {
	return scenario{
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
		verify: inlinePostToolHookBlocksResultScenarioVerify,
	}
}

func inlinePostToolHookAddsFeedbackScenario() scenario {
	return scenario{
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
		setup:  inlinePostToolHookAddsFeedbackScenarioSetup,
		verify: inlinePostToolHookAddsFeedbackScenarioVerify,
	}
}

func inlineFileChangedHookAddsFeedbackScenario() scenario {
	return scenario{
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
		verify: inlineFileChangedHookAddsFeedbackScenarioVerify,
	}
}

func inlineMultiToolTurnRoundtripScenario() scenario {
	return scenario{
		name: "multi_tool_turn_roundtrip",
		turns: []mockanthropic.Turn{
			{ToolUses: []mockanthropic.ToolUse{
				{ID: "tool-1", Name: "read_file", Input: json.RawMessage(`{"path":"README.md"}`)},
				{ID: "tool-2", Name: "grep", Input: json.RawMessage(`{"pattern":"Needle","path":"."}`)},
			}},
			{Text: "multi tool harness ok"},
		},
		prompt: "use multiple tools",
		setup:  inlineMultiToolTurnRoundtripScenarioSetup,
		verify: inlineMultiToolTurnRoundtripScenarioVerify,
	}
}

func inlineGrepChunkAssemblyScenario() scenario {
	return scenario{
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
		setup:  inlineGrepChunkAssemblyScenarioSetup,
		verify: inlineGrepChunkAssemblyScenarioVerify,
	}
}

func inlineBashStdoutRoundtripScenario() scenario {
	return scenario{
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
		verify: inlineBashStdoutRoundtripScenarioVerify,
	}
}

func inlineBashPermissionPromptApprovedScenario() scenario {
	return scenario{
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
		verify: inlineBashPermissionPromptApprovedScenarioVerify,
	}
}

func inlineBashPermissionPromptDeniedScenario() scenario {
	return scenario{
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
		verify: inlineBashPermissionPromptDeniedScenarioVerify,
	}
}

func inlinePluginToolRoundtripScenario() scenario {
	return scenario{
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
		setup:  inlinePluginToolRoundtripScenarioSetup,
		verify: inlinePluginToolRoundtripScenarioVerify,
	}
}

func inlineAutoCompactTriggeredScenario() scenario {
	return scenario{
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
		verify:         inlineAutoCompactTriggeredScenarioVerify,
		verifyRequests: inlineAutoCompactTriggeredScenarioVerifyRequests,
	}
}

func inlineTokenCostReportingScenario() scenario {
	return scenario{
		name:   "token_cost_reporting",
		turns:  []mockanthropic.Turn{{Text: "token cost harness ok"}},
		prompt: "report token cost",
		verify: inlineTokenCostReportingScenarioVerify,
	}
}

func inlineStreamingTextScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "streaming harness ok") {
		return fmt.Errorf("missing streamed text")
	}
	if len(result.ToolCalls) != 0 {
		return fmt.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
	return nil
}

func inlinePromptAttachmentsRoundtripScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "attachment harness ok") {
		return fmt.Errorf("missing attachment response")
	}
	if len(result.Messages) == 0 || len(result.Messages[0].Content) != 2 {
		return fmt.Errorf("expected structured attachment content in first user message")
	}
	return nil
}

func inlinePromptAttachmentsRoundtripScenarioVerifyRequests(requests []anthropic.Request) error {
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
}

func inlineReadFileRoundtripScenarioSetup(workspace string) error {
	return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\n"), 0o644)
}

func inlineReadFileRoundtripScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "codog harness ok") {
		return fmt.Errorf("missing final read_file response")
	}
	return expectToolCalls(result, 1, false)
}

func inlineWriteFileAllowedScenarioVerify(workspace string, result runloop.TurnResult, _ string) error {
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
}

func inlineWriteFileDeniedScenarioVerify(workspace string, result runloop.TurnResult, _ string) error {
	if err := expectToolCalls(result, 1, true); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
		return fmt.Errorf("denied file exists or stat failed: %v", err)
	}
	return nil
}

func inlinePreToolHookUpdatesInputScenarioVerify(workspace string, result runloop.TurnResult, output string) error {
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
}

func inlineUserPromptHookAddsContextScenarioVerify(_ string, _ runloop.TurnResult, output string) error {
	if !strings.Contains(output, "prompt hook harness ok") {
		return fmt.Errorf("missing prompt hook final response")
	}
	return nil
}

func inlineUserPromptHookAddsContextScenarioVerifyRequests(requests []anthropic.Request) error {
	if len(requests) != 1 {
		return fmt.Errorf("expected 1 prompt hook request, got %d", len(requests))
	}
	if len(requests[0].Messages) != 2 {
		return fmt.Errorf("expected prompt hook feedback message, got %d messages", len(requests[0].Messages))
	}
	feedback := requests[0].Messages[1].Content[0].Text
	if !harnessContainsAll(feedback, "prompt parity note", "prompt parity context") {
		return fmt.Errorf("missing prompt hook feedback in request")
	}
	return nil
}

func inlineStopHookAddsFeedbackScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "stop hook harness ok") {
		return fmt.Errorf("missing stop hook final response")
	}
	if !slices.Equal(result.StopHookFeedback, []string{"stop parity note", "stop parity context"}) {
		return fmt.Errorf("unexpected stop hook feedback: %#v", result.StopHookFeedback)
	}
	return nil
}

func inlinePostToolHookBlocksResultScenarioVerify(workspace string, result runloop.TurnResult, output string) error {
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
}

func inlinePostToolHookAddsFeedbackScenarioSetup(workspace string) error {
	return os.WriteFile(filepath.Join(workspace, "feedback.txt"), []byte("feedback file\n"), 0o644)
}

func inlinePostToolHookAddsFeedbackScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}

func inlineFileChangedHookAddsFeedbackScenarioVerify(workspace string, result runloop.TurnResult, output string) error {
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
}

func inlineMultiToolTurnRoundtripScenarioSetup(workspace string) error {
	return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\nNeedle\n"), 0o644)
}

func inlineMultiToolTurnRoundtripScenarioVerify(_ string, result runloop.TurnResult, output string) error {
	if !strings.Contains(output, "multi tool harness ok") {
		return fmt.Errorf("missing multi-tool final response")
	}
	return expectToolCalls(result, 2, false)
}

func inlineGrepChunkAssemblyScenarioSetup(workspace string) error {
	return os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Harness\nNeedle\n"), 0o644)
}

func inlineGrepChunkAssemblyScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}

func inlineBashStdoutRoundtripScenarioVerify(_ string, result runloop.TurnResult, _ string) error {
	if err := expectToolCalls(result, 1, false); err != nil {
		return err
	}
	if !strings.Contains(result.ToolCalls[0].Output, "harness-bash") {
		return fmt.Errorf("missing bash stdout in tool output")
	}
	return nil
}

func inlineBashPermissionPromptApprovedScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}

func inlineBashPermissionPromptDeniedScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}

func inlinePluginToolRoundtripScenarioSetup(workspace string) error {
	dir := filepath.Join(workspace, ".codog", "plugins", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifest := `{"id":"demo","tools":[{"name":"demo_tool","command":"cat","permission":"read-only"}]}`
	return os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644)
}

func inlinePluginToolRoundtripScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}

func inlineAutoCompactTriggeredScenarioVerify(_ string, _ runloop.TurnResult, output string) error {
	if !strings.Contains(output, "compact harness ok") {
		return fmt.Errorf("missing compact final response")
	}
	return nil
}

func inlineAutoCompactTriggeredScenarioVerifyRequests(requests []anthropic.Request) error {
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
	if !harnessContainsAll(summary, "compact parity pre", "compact parity post") {
		return fmt.Errorf("missing compaction hook feedback in summary")
	}
	return nil
}

func inlineTokenCostReportingScenarioVerify(_ string, result runloop.TurnResult, output string) error {
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
}
