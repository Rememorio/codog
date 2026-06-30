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
	require.GreaterOrEqual(t, report.Total, 6)

	readFile := findScenario(t, report, "read_file_roundtrip")
	require.Contains(t, readFile.Output, "codog harness ok")
	require.Equal(t, 2, readFile.Iterations)
	require.Equal(t, 1, readFile.ToolCalls)
	require.GreaterOrEqual(t, readFile.MessageCount, 4)

	writeDenied := findScenario(t, report, "write_file_denied")
	require.True(t, writeDenied.OK)
	require.Equal(t, 1, writeDenied.ToolCalls)
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
