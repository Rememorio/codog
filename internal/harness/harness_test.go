package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunUsesMockProvider(t *testing.T) {
	report, err := Run(context.Background())
	require.NoError(t, err)
	require.True(t, report.OK)
	require.Equal(t, report.Total, report.Passed)
	require.GreaterOrEqual(t, report.Total, 20)
	require.Equal(t, "actual", report.UsageSummary.Source)
	require.Greater(t, report.UsageSummary.TotalTokens, 0)
	require.Greater(t, report.EstimatedCost, 0.0)
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

	pluginTool := findScenario(t, report, "plugin_tool_roundtrip")
	require.True(t, pluginTool.OK)
	require.Equal(t, 1, pluginTool.ToolCalls)
	require.Contains(t, pluginTool.Output, "plugin harness ok")

	configPrecedence := findScenario(t, report, "config_precedence_roundtrip")
	require.True(t, configPrecedence.OK)
	require.Equal(t, 0, configPrecedence.ToolCalls)
	require.Contains(t, configPrecedence.Output, "config precedence harness ok")

	sessionResume := findScenario(t, report, "session_resume_jsonl_roundtrip")
	require.True(t, sessionResume.OK)
	require.Equal(t, 0, sessionResume.ToolCalls)
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

func categoryCoverageTotal(coverage []CategoryReport) int {
	total := 0
	for _, category := range coverage {
		total += category.Total
	}
	return total
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
