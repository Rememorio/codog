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
	require.GreaterOrEqual(t, report.Total, 14)
	require.Equal(t, "actual", report.UsageSummary.Source)
	require.Greater(t, report.UsageSummary.TotalTokens, 0)
	require.Greater(t, report.EstimatedCost, 0.0)

	readFile := findScenario(t, report, "read_file_roundtrip")
	require.Contains(t, readFile.Output, "codog harness ok")
	require.Equal(t, 2, readFile.Iterations)
	require.Equal(t, 1, readFile.ToolCalls)
	require.GreaterOrEqual(t, readFile.MessageCount, 4)

	writeDenied := findScenario(t, report, "write_file_denied")
	require.True(t, writeDenied.OK)
	require.Equal(t, 1, writeDenied.ToolCalls)

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

	pluginTool := findScenario(t, report, "plugin_tool_roundtrip")
	require.True(t, pluginTool.OK)
	require.Equal(t, 1, pluginTool.ToolCalls)
	require.Contains(t, pluginTool.Output, "plugin harness ok")

	pluginLifecycle := findScenario(t, report, "plugin_lifecycle_roundtrip")
	require.True(t, pluginLifecycle.OK)
	require.Equal(t, 0, pluginLifecycle.ToolCalls)
	require.Contains(t, pluginLifecycle.Output, "plugin lifecycle harness ok")

	remoteTrigger := findScenario(t, report, "remote_trigger_roundtrip")
	require.True(t, remoteTrigger.OK)
	require.Equal(t, 1, remoteTrigger.ToolCalls)
	require.Contains(t, remoteTrigger.Output, "remote trigger harness ok")

	autoCompact := findScenario(t, report, "auto_compact_triggered")
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
