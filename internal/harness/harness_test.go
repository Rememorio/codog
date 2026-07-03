package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunUsesMockProvider(t *testing.T) {
	report, err := Run(context.Background())
	require.NoError(t, err)
	require.NoError(t, ValidateReport(report))
	require.Equal(t, ReportSchemaVersion, report.SchemaVersion)
	require.True(t, report.OK)
	require.Equal(t, report.Total, report.Passed)
	require.Equal(t, report.Total, report.ScenarioCount)
	require.GreaterOrEqual(t, report.Total, 20)
	require.Equal(t, "actual", report.UsageSummary.Source)
	require.Greater(t, report.UsageSummary.TotalTokens, 0)
	require.Greater(t, report.EstimatedCost, 0.0)
	require.Greater(t, report.RequestCount, 0)
	require.Equal(t, report.RequestCount, scenarioRequestCountTotal(report.Scenarios))
	require.GreaterOrEqual(t, len(report.Coverage), 8)
	require.Equal(t, report.Total, categoryCoverageTotal(report.Coverage))
	for _, scenario := range report.Scenarios {
		require.NotEmpty(t, scenario.Description, scenario.Name)
		require.NotEmpty(t, scenario.ParityRefs, scenario.Name)
		require.Len(t, scenario.ToolUses, scenario.ToolCalls, scenario.Name)
		require.LessOrEqual(t, scenario.ToolErrorCount, scenario.ToolCalls, scenario.Name)
		_, hasMetadata := scenarioMetadataByName[scenario.Name]
		require.True(t, hasMetadata, scenario.Name)
	}
	fileTools := findCategory(t, report, "file-tools")
	require.True(t, fileTools.OK)
	require.Equal(t, 3, fileTools.Total)
	require.ElementsMatch(t, []string{"grep_chunk_assembly", "read_file_roundtrip", "write_file_allowed"}, fileTools.Scenarios)
	hooks := findCategory(t, report, "hooks")
	require.Equal(t, 6, hooks.Total)
	permissions := findCategory(t, report, "permissions")
	require.Equal(t, 3, permissions.Total)

	readFile := findScenario(t, report, "read_file_roundtrip")
	require.Equal(t, "file-tools", readFile.Category)
	require.NotEmpty(t, readFile.Description)
	require.Contains(t, readFile.ParityRefs, "File tools")
	require.Equal(t, 2, readFile.RequestCount)
	require.Equal(t, []string{"read_file"}, readFile.ToolUses)
	require.Equal(t, 0, readFile.ToolErrorCount)
	require.Equal(t, "codog harness ok", readFile.FinalMessage)
	require.Contains(t, readFile.Output, "codog harness ok")
	require.Equal(t, 2, readFile.Iterations)
	require.Equal(t, 1, readFile.ToolCalls)
	require.GreaterOrEqual(t, readFile.MessageCount, 4)

	attachments := findScenario(t, report, "prompt_attachments_roundtrip")
	require.Equal(t, "attachments", attachments.Category)
	require.True(t, attachments.OK)
	require.Equal(t, 0, attachments.ToolCalls)
	require.Contains(t, attachments.Output, "attachment harness ok")

	writeDenied := findScenario(t, report, "write_file_denied")
	require.True(t, writeDenied.OK)
	require.Equal(t, 1, writeDenied.ToolCalls)
	require.Equal(t, []string{"write_file"}, writeDenied.ToolUses)
	require.Equal(t, 1, writeDenied.ToolErrorCount)

	preToolHook := findScenario(t, report, "pre_tool_hook_updates_input")
	require.True(t, preToolHook.OK)
	require.Equal(t, 1, preToolHook.ToolCalls)
	require.Contains(t, preToolHook.Output, "pre hook harness ok")

	userPromptHook := findScenario(t, report, "user_prompt_hook_adds_context")
	require.True(t, userPromptHook.OK)
	require.Equal(t, []int{2}, userPromptHook.RequestMessageCounts)
	require.Contains(t, userPromptHook.Output, "prompt hook harness ok")

	stopHook := findScenario(t, report, "stop_hook_adds_feedback")
	require.True(t, stopHook.OK)
	require.Contains(t, stopHook.Output, "stop hook harness ok")

	postToolHook := findScenario(t, report, "post_tool_hook_blocks_result")
	require.True(t, postToolHook.OK)
	require.Equal(t, 1, postToolHook.ToolCalls)
	require.Contains(t, postToolHook.Output, "post hook block harness ok")

	postToolFeedback := findScenario(t, report, "post_tool_hook_adds_feedback")
	require.True(t, postToolFeedback.OK)
	require.Equal(t, 1, postToolFeedback.ToolCalls)
	require.Contains(t, postToolFeedback.Output, "post feedback harness ok")

	fileChangedFeedback := findScenario(t, report, "file_changed_hook_adds_feedback")
	require.True(t, fileChangedFeedback.OK)
	require.Equal(t, 1, fileChangedFeedback.ToolCalls)
	require.Contains(t, fileChangedFeedback.Output, "file changed feedback harness ok")

	grepChunks := findScenario(t, report, "grep_chunk_assembly")
	require.True(t, grepChunks.OK)
	require.Equal(t, 1, grepChunks.ToolCalls)
	require.Contains(t, grepChunks.Output, "grep chunk harness ok")

	bashApproved := findScenario(t, report, "bash_permission_prompt_approved")
	require.True(t, bashApproved.OK)
	require.Equal(t, 1, bashApproved.ToolCalls)
	require.Contains(t, bashApproved.Output, "bash approved harness ok")

	bashDenied := findScenario(t, report, "bash_permission_prompt_denied")
	require.True(t, bashDenied.OK)
	require.Equal(t, 1, bashDenied.ToolCalls)
	require.Contains(t, bashDenied.Output, "bash denied harness ok")

	bashTruncation := findScenario(t, report, "bash_output_truncation_roundtrip")
	require.True(t, bashTruncation.OK)
	require.Equal(t, 1, bashTruncation.ToolCalls)
	require.Equal(t, []string{"bash"}, bashTruncation.ToolUses)
	require.Equal(t, 0, bashTruncation.ToolErrorCount)
	require.Contains(t, bashTruncation.Description, "truncated")
	require.Contains(t, bashTruncation.Output, "bash truncation harness ok")

	sandboxBypass := findScenario(t, report, "sandbox_bypass_status_roundtrip")
	require.True(t, sandboxBypass.OK)
	require.Equal(t, "sandbox", sandboxBypass.Category)
	require.Equal(t, 1, sandboxBypass.ToolCalls)
	require.Equal(t, []string{"bash"}, sandboxBypass.ToolUses)
	require.Contains(t, sandboxBypass.Output, `"dangerouslyDisableSandbox": true`)
	require.Contains(t, sandboxBypass.Output, `"resolution_status": "disabled"`)
	require.Contains(t, sandboxBypass.Output, "sandbox-bypass-ok")
	sandboxCategory := findCategory(t, report, "sandbox")
	require.True(t, sandboxCategory.OK)
	require.Equal(t, 1, sandboxCategory.Total)
	require.ElementsMatch(t, []string{"sandbox_bypass_status_roundtrip"}, sandboxCategory.Scenarios)

	notebook := findScenario(t, report, "notebook_read_edit_roundtrip")
	require.True(t, notebook.OK)
	require.Equal(t, "notebook", notebook.Category)
	require.Equal(t, 4, notebook.ToolCalls)
	require.Equal(t, []string{"notebook_read", "notebook_edit", "notebook_edit", "notebook_read"}, notebook.ToolUses)
	require.Contains(t, notebook.Output, `"kind": "notebook_read"`)
	require.Contains(t, notebook.Output, `"kind": "notebook_edit"`)
	require.Contains(t, notebook.Output, "# Renamed")
	require.Contains(t, notebook.Output, "print(2)")
	notebookCategory := findCategory(t, report, "notebook")
	require.True(t, notebookCategory.OK)
	require.Equal(t, 1, notebookCategory.Total)
	require.ElementsMatch(t, []string{"notebook_read_edit_roundtrip"}, notebookCategory.Scenarios)

	webAccess := findScenario(t, report, "web_access_roundtrip")
	require.True(t, webAccess.OK)
	require.Equal(t, "web-access", webAccess.Category)
	require.Equal(t, 2, webAccess.ToolCalls)
	require.Equal(t, []string{"web_fetch", "web_search"}, webAccess.ToolUses)
	require.Contains(t, webAccess.Output, `"title": "Codog Web Parity"`)
	require.Contains(t, webAccess.Output, `"summary": "Title: Codog Web Parity"`)
	require.Contains(t, webAccess.Output, `"tool_use_id": "web_search_1"`)
	require.Contains(t, webAccess.Output, `"url": "https://example.com/codog"`)
	require.NotContains(t, webAccess.Output, "Blocked result")
	webAccessCategory := findCategory(t, report, "web-access")
	require.True(t, webAccessCategory.OK)
	require.Equal(t, 1, webAccessCategory.Total)
	require.ElementsMatch(t, []string{"web_access_roundtrip"}, webAccessCategory.Scenarios)

	gitWorkspace := findScenario(t, report, "git_workspace_roundtrip")
	require.True(t, gitWorkspace.OK)
	require.Equal(t, "git-workspace", gitWorkspace.Category)
	require.Equal(t, 6, gitWorkspace.ToolCalls)
	require.Equal(t, []string{"git_status", "git_diff", "git_log", "git_show", "git_blame", "branch_freshness"}, gitWorkspace.ToolUses)
	require.Contains(t, gitWorkspace.Output, "notes.txt")
	require.Contains(t, gitWorkspace.Output, "+beta")
	require.Contains(t, gitWorkspace.Output, "initial notes")
	require.Contains(t, gitWorkspace.Output, `"status": "stale"`)
	require.Contains(t, gitWorkspace.Output, `"verification_blocked": true`)
	gitWorkspaceCategory := findCategory(t, report, "git-workspace")
	require.True(t, gitWorkspaceCategory.OK)
	require.Equal(t, 1, gitWorkspaceCategory.Total)
	require.ElementsMatch(t, []string{"git_workspace_roundtrip"}, gitWorkspaceCategory.Scenarios)

	planTodo := findScenario(t, report, "plan_todo_roundtrip")
	require.True(t, planTodo.OK)
	require.Equal(t, "planning", planTodo.Category)
	require.Equal(t, 4, planTodo.ToolCalls)
	require.Equal(t, []string{"enter_plan_mode", "todo_write", "todo_read", "exit_plan_mode"}, planTodo.ToolUses)
	require.Contains(t, planTodo.Output, `"action": "enter"`)
	require.Contains(t, planTodo.Output, `"status": "active"`)
	require.Contains(t, planTodo.Output, `"content": "write focused parity test"`)
	require.Contains(t, planTodo.Output, `"action": "exit"`)
	require.Contains(t, planTodo.Output, `"status": "inactive"`)
	planTodoCategory := findCategory(t, report, "planning")
	require.True(t, planTodoCategory.OK)
	require.Equal(t, 1, planTodoCategory.Total)
	require.ElementsMatch(t, []string{"plan_todo_roundtrip"}, planTodoCategory.Scenarios)

	lspStatic := findScenario(t, report, "lsp_static_roundtrip")
	require.True(t, lspStatic.OK)
	require.Equal(t, "code-intelligence", lspStatic.Category)
	require.Equal(t, 6, lspStatic.ToolCalls)
	require.Equal(t, []string{"lsp", "lsp", "lsp", "lsp", "lsp", "lsp"}, lspStatic.ToolUses)
	require.Contains(t, lspStatic.Output, `"action": "symbols"`)
	require.Contains(t, lspStatic.Output, `"name": "RunFast"`)
	require.Contains(t, lspStatic.Output, `"action": "references"`)
	require.Contains(t, lspStatic.Output, `"action": "format"`)
	lspStaticCategory := findCategory(t, report, "code-intelligence")
	require.True(t, lspStaticCategory.OK)
	require.Equal(t, 1, lspStaticCategory.Total)
	require.ElementsMatch(t, []string{"lsp_static_roundtrip"}, lspStaticCategory.Scenarios)

	pluginTool := findScenario(t, report, "plugin_tool_roundtrip")
	require.True(t, pluginTool.OK)
	require.Equal(t, 1, pluginTool.ToolCalls)
	require.Contains(t, pluginTool.Output, "plugin harness ok")

	workflow := findScenario(t, report, "command_skill_template_roundtrip")
	require.True(t, workflow.OK)
	require.Equal(t, "command-workflows", workflow.Category)
	require.Equal(t, 0, workflow.ToolCalls)
	require.Equal(t, 3, workflow.RequestCount)
	require.Equal(t, "command skill template harness ok", workflow.FinalMessage)
	require.Contains(t, workflow.Output, `"kind":"command_skill_template"`)
	require.Contains(t, workflow.Output, `"rendered":"Review src/main.go for session session-123."`)
	require.Contains(t, workflow.Output, `"matches_src":true`)
	require.Contains(t, workflow.Output, `"rendered":"Release 1.0.0 for codog."`)
	workflowCategory := findCategory(t, report, "command-workflows")
	require.True(t, workflowCategory.OK)
	require.Equal(t, 1, workflowCategory.Total)
	require.ElementsMatch(t, []string{"command_skill_template_roundtrip"}, workflowCategory.Scenarios)

	configPrecedence := findScenario(t, report, "config_precedence_roundtrip")
	require.True(t, configPrecedence.OK)
	require.Equal(t, 0, configPrecedence.ToolCalls)
	require.Contains(t, configPrecedence.Output, "config precedence harness ok")

	providerRouting := findScenario(t, report, "provider_routing_roundtrip")
	require.True(t, providerRouting.OK)
	require.Equal(t, "provider-routing", providerRouting.Category)
	require.Equal(t, 0, providerRouting.ToolCalls)
	require.Equal(t, 4, providerRouting.RequestCount)
	require.Equal(t, "provider routing harness ok", providerRouting.FinalMessage)
	require.Contains(t, providerRouting.Output, `"name": "openai-prefixed-model"`)
	require.Contains(t, providerRouting.Output, `"wire_model": "gpt-4.1-mini"`)
	require.Contains(t, providerRouting.Output, `"provider_source": "OLLAMA_HOST"`)
	require.Contains(t, providerRouting.Output, `"auth_source": "OPENAI_BASE_URL"`)
	require.Contains(t, providerRouting.Output, `"provider": "dashscope"`)
	providerRoutingCategory := findCategory(t, report, "provider-routing")
	require.True(t, providerRoutingCategory.OK)
	require.Equal(t, 1, providerRoutingCategory.Total)
	require.ElementsMatch(t, []string{"provider_routing_roundtrip"}, providerRoutingCategory.Scenarios)

	sessionResume := findScenario(t, report, "session_resume_jsonl_roundtrip")
	require.True(t, sessionResume.OK)
	require.Equal(t, 0, sessionResume.ToolCalls)
	require.Equal(t, 1, sessionResume.RequestCount)
	require.Equal(t, []int{3}, sessionResume.RequestMessageCounts)
	require.Equal(t, 4, sessionResume.MessageCount)
	require.Contains(t, sessionResume.Output, "resume harness ok")

	pluginLifecycle := findScenario(t, report, "plugin_lifecycle_roundtrip")
	require.True(t, pluginLifecycle.OK)
	require.Equal(t, 0, pluginLifecycle.ToolCalls)
	require.Contains(t, pluginLifecycle.Output, "plugin lifecycle harness ok")

	remoteTrigger := findScenario(t, report, "remote_trigger_roundtrip")
	require.True(t, remoteTrigger.OK)
	require.Equal(t, 1, remoteTrigger.ToolCalls)
	require.Contains(t, remoteTrigger.Output, "remote trigger harness ok")

	remoteAPI := findScenario(t, report, "remote_api_listener_roundtrip")
	require.True(t, remoteAPI.OK)
	require.Equal(t, "remote-control", remoteAPI.Category)
	require.Equal(t, 0, remoteAPI.ToolCalls)
	require.Equal(t, 3, remoteAPI.RequestCount)
	require.Contains(t, remoteAPI.Output, "remote api listener harness ok")
	remoteControl := findCategory(t, report, "remote-control")
	require.True(t, remoteControl.OK)
	require.Equal(t, 2, remoteControl.Total)
	require.ElementsMatch(t, []string{"remote_api_listener_roundtrip", "remote_trigger_roundtrip"}, remoteControl.Scenarios)

	mcpLifecycle := findScenario(t, report, "mcp_lifecycle_roundtrip")
	require.True(t, mcpLifecycle.OK)
	require.Equal(t, "mcp-lifecycle", mcpLifecycle.Category)
	require.Equal(t, 1, mcpLifecycle.ToolCalls)
	require.Equal(t, []string{"mcp.echo"}, mcpLifecycle.ToolUses)
	require.Equal(t, 4, mcpLifecycle.RequestCount)
	require.Contains(t, mcpLifecycle.Output, `"kind":"mcp_show"`)
	require.Contains(t, mcpLifecycle.Output, `"phase":"ready"`)
	require.Contains(t, mcpLifecycle.Output, "mcp lifecycle harness ok")
	mcpCategory := findCategory(t, report, "mcp-lifecycle")
	require.True(t, mcpCategory.OK)
	require.Equal(t, 1, mcpCategory.Total)
	require.ElementsMatch(t, []string{"mcp_lifecycle_roundtrip"}, mcpCategory.Scenarios)

	mcpAuth := findScenario(t, report, "mcp_auth_oauth_refresh_roundtrip")
	require.True(t, mcpAuth.OK)
	require.Equal(t, "mcp-auth", mcpAuth.Category)
	require.Equal(t, 1, mcpAuth.ToolCalls)
	require.Equal(t, []string{"mcp_auth"}, mcpAuth.ToolUses)
	require.Contains(t, mcpAuth.Output, `"refreshed": true`)
	require.Contains(t, mcpAuth.Output, `"oauth_profile": "work"`)
	require.NotContains(t, mcpAuth.Output, "new-access-token-secret")
	require.NotContains(t, mcpAuth.Output, "new-refresh-token-secret")
	mcpAuthCategory := findCategory(t, report, "mcp-auth")
	require.True(t, mcpAuthCategory.OK)
	require.Equal(t, 1, mcpAuthCategory.Total)
	require.ElementsMatch(t, []string{"mcp_auth_oauth_refresh_roundtrip"}, mcpAuthCategory.Scenarios)

	acp := findScenario(t, report, "acp_stdio_roundtrip")
	require.True(t, acp.OK)
	require.Equal(t, "editor-bridge", acp.Category)
	require.Equal(t, 0, acp.ToolCalls)
	require.Equal(t, 4, acp.RequestCount)
	require.Contains(t, acp.Output, `"protocolVersion"`)
	require.Contains(t, acp.Output, `"acp-harness-session"`)
	editorBridge := findCategory(t, report, "editor-bridge")
	require.True(t, editorBridge.OK)
	require.Equal(t, 1, editorBridge.Total)
	require.ElementsMatch(t, []string{"acp_stdio_roundtrip"}, editorBridge.Scenarios)

	autoCompact := findScenario(t, report, "auto_compact_triggered")
	require.Equal(t, "session-compaction", autoCompact.Category)
	require.True(t, autoCompact.OK)
	require.Equal(t, 1, autoCompact.Compactions)
	require.Equal(t, []int{2}, autoCompact.RequestMessageCounts)
	require.Contains(t, autoCompact.Output, "compact harness ok")

	tokenCost := findScenario(t, report, "token_cost_reporting")
	require.True(t, tokenCost.OK)
	require.Equal(t, "actual", tokenCost.UsageSummary.Source)
	require.Greater(t, tokenCost.UsageSummary.TotalTokens, 0)
	require.Greater(t, tokenCost.EstimatedCost, 0.0)
	require.Contains(t, tokenCost.Output, "token cost harness ok")
}

func TestValidateReportRejectsInconsistentReports(t *testing.T) {
	report, err := Run(context.Background())
	require.NoError(t, err)

	broken := report
	broken.SchemaVersion = "wrong"
	require.ErrorContains(t, ValidateReport(broken), "schema_version")

	broken = report
	broken.RequestCount++
	require.ErrorContains(t, ValidateReport(broken), "request_count")

	broken = report
	broken.Scenarios[0].Description = ""
	require.ErrorContains(t, ValidateReport(broken), "description is required")
}

func TestScenarioManifestMatchesRunScenarios(t *testing.T) {
	manifest := ScenarioManifest()
	require.Equal(t, ManifestSchemaVersion, manifest.SchemaVersion)
	require.Equal(t, len(scenarioOrder), manifest.ScenarioCount)
	require.Len(t, manifest.Scenarios, manifest.ScenarioCount)
	require.GreaterOrEqual(t, len(manifest.Categories), 8)
	require.Equal(t, manifest.ScenarioCount, manifestCategoryTotal(manifest.Categories))

	report, err := Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, scenarioNames(report.Scenarios), manifestScenarioNames(manifest.Scenarios))

	readFile := findManifestScenario(t, manifest, "read_file_roundtrip")
	require.Equal(t, "file-tools", readFile.Category)
	require.NotEmpty(t, readFile.Description)
	require.Contains(t, readFile.ParityRefs, "File tools")

	remoteAPI := findManifestScenario(t, manifest, "remote_api_listener_roundtrip")
	require.Equal(t, "remote-control", remoteAPI.Category)
	require.Contains(t, remoteAPI.ParityRefs, "Control API listener")

	acp := findManifestScenario(t, manifest, "acp_stdio_roundtrip")
	require.Equal(t, "editor-bridge", acp.Category)
	require.Contains(t, acp.ParityRefs, "JSON-RPC stdio")

	mcpLifecycle := findManifestScenario(t, manifest, "mcp_lifecycle_roundtrip")
	require.Equal(t, "mcp-lifecycle", mcpLifecycle.Category)
	require.Contains(t, mcpLifecycle.ParityRefs, "MCP lifecycle")

	mcpAuth := findManifestScenario(t, manifest, "mcp_auth_oauth_refresh_roundtrip")
	require.Equal(t, "mcp-auth", mcpAuth.Category)
	require.Contains(t, mcpAuth.ParityRefs, "OAuth refresh")

	sandboxBypass := findManifestScenario(t, manifest, "sandbox_bypass_status_roundtrip")
	require.Equal(t, "sandbox", sandboxBypass.Category)
	require.Contains(t, sandboxBypass.ParityRefs, "Sandbox")

	notebook := findManifestScenario(t, manifest, "notebook_read_edit_roundtrip")
	require.Equal(t, "notebook", notebook.Category)
	require.Contains(t, notebook.ParityRefs, "Notebook edit")

	webAccess := findManifestScenario(t, manifest, "web_access_roundtrip")
	require.Equal(t, "web-access", webAccess.Category)
	require.Contains(t, webAccess.ParityRefs, "Web search")

	gitWorkspace := findManifestScenario(t, manifest, "git_workspace_roundtrip")
	require.Equal(t, "git-workspace", gitWorkspace.Category)
	require.Contains(t, gitWorkspace.ParityRefs, "Branch freshness")

	planTodo := findManifestScenario(t, manifest, "plan_todo_roundtrip")
	require.Equal(t, "planning", planTodo.Category)
	require.Contains(t, planTodo.ParityRefs, "Plan mode")

	lspStatic := findManifestScenario(t, manifest, "lsp_static_roundtrip")
	require.Equal(t, "code-intelligence", lspStatic.Category)
	require.Contains(t, lspStatic.ParityRefs, "LSP tool")

	providerRouting := findManifestScenario(t, manifest, "provider_routing_roundtrip")
	require.Equal(t, "provider-routing", providerRouting.Category)
	require.Contains(t, providerRouting.ParityRefs, "OpenAI-compatible APIs")
	require.Contains(t, providerRouting.ParityRefs, "Ollama")

	workflow := findManifestScenario(t, manifest, "command_skill_template_roundtrip")
	require.Equal(t, "command-workflows", workflow.Category)
	require.Contains(t, workflow.ParityRefs, "Slash commands")
	require.Contains(t, workflow.ParityRefs, "Skills")
	require.Contains(t, workflow.ParityRefs, "Templates")
}

func categoryCoverageTotal(coverage []CategoryReport) int {
	total := 0
	for _, category := range coverage {
		total += category.Total
	}
	return total
}

func scenarioRequestCountTotal(scenarios []ScenarioReport) int {
	total := 0
	for _, scenario := range scenarios {
		total += scenario.RequestCount
	}
	return total
}

func manifestCategoryTotal(categories []ManifestCategory) int {
	total := 0
	for _, category := range categories {
		total += category.Count
	}
	return total
}

func manifestScenarioNames(scenarios []ManifestScenario) []string {
	names := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		names = append(names, scenario.Name)
	}
	return names
}

func scenarioNames(scenarios []ScenarioReport) []string {
	names := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		names = append(names, scenario.Name)
	}
	return names
}

func findManifestScenario(t *testing.T, manifest Manifest, name string) ManifestScenario {
	t.Helper()
	for _, scenario := range manifest.Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("missing manifest scenario %q in %#v", name, manifest.Scenarios)
	return ManifestScenario{}
}

func findCategory(t *testing.T, report Report, category string) CategoryReport {
	t.Helper()
	for _, candidate := range report.Coverage {
		if candidate.Category == category {
			return candidate
		}
	}
	t.Fatalf("missing category %q in %#v", category, report.Coverage)
	return CategoryReport{}
}

func findScenario(t *testing.T, report Report, name string) ScenarioReport {
	t.Helper()
	for _, scenario := range report.Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("missing scenario %q in %#v", name, report.Scenarios)
	return ScenarioReport{}
}
