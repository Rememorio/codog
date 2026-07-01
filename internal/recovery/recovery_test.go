package recovery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEachScenarioHasRecipe(t *testing.T) {
	for _, scenario := range AllScenarios() {
		recipe, err := RecipeFor(scenario)
		require.NoError(t, err)
		require.Equal(t, string(scenario), recipe.ID)
		require.Equal(t, scenario, recipe.Scenario)
		require.NotEmpty(t, recipe.Steps)
		require.Equal(t, 1, recipe.MaxAttempts)
	}
}

func TestRecoveryAttemptSucceedsThenEscalates(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	first, err := store.Attempt(ScenarioStaleBranch, AttemptOptions{Now: now})
	require.NoError(t, err)
	require.Equal(t, "recovery_attempt", first.Kind)
	require.Equal(t, ResultRecovered, first.Result.Kind)
	require.Equal(t, 2, first.Result.StepsTaken)
	require.Equal(t, StateSucceeded, first.Entry.State)
	require.Equal(t, 1, first.Entry.AttemptCount)
	require.Equal(t, 0, first.Entry.AttemptsRemaining)
	require.Len(t, first.Entry.CommandResults, 2)
	require.Equal(t, StepMergeForward, first.Entry.CommandResults[0].Step.Kind)
	require.Equal(t, StepCleanBuild, first.Entry.CommandResults[1].Step.Kind)
	require.Equal(t, "recovery.succeeded", first.Events[len(first.Events)-1].Type)

	second, err := store.Attempt(ScenarioStaleBranch, AttemptOptions{Now: now.Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, ResultEscalationRequired, second.Result.Kind)
	require.Equal(t, StateExhausted, second.Entry.State)
	require.Equal(t, 1, second.Entry.AttemptCount)
	require.Contains(t, second.Entry.EscalationReason, "max recovery attempts")
	require.Equal(t, "recovery.escalated", second.Events[len(second.Events)-1].Type)

	status, err := store.Status(ScenarioStaleBranch)
	require.NoError(t, err)
	require.True(t, status.Attempted)
	require.Equal(t, StateExhausted, status.State)
	require.Equal(t, 1, status.AttemptCount)
	require.Contains(t, status.EscalationReason, "max recovery attempts")
}

func TestRecoveryLedgerRecordsPartialFailure(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))
	failAt := 1

	report, err := store.Attempt(ScenarioPartialPluginStartup, AttemptOptions{
		FailureSummary:  "mcp handshake still failing",
		FailedStepIndex: &failAt,
		Now:             time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, ResultPartialRecovery, report.Result.Kind)
	require.Equal(t, StateFailed, report.Entry.State)
	require.Len(t, report.Result.Recovered, 1)
	require.Len(t, report.Result.Remaining, 1)
	require.Equal(t, StepRestartPlugin, report.Result.Recovered[0].Kind)
	require.Equal(t, StepRetryMCPHandshake, report.Result.Remaining[0].Kind)
	require.Len(t, report.Entry.CommandResults, 2)
	require.Equal(t, StateSucceeded, report.Entry.CommandResults[0].State)
	require.Equal(t, StateFailed, report.Entry.CommandResults[1].State)
	require.Equal(t, "mcp handshake still failing", report.Entry.LastFailureSummary)
	require.Equal(t, "recovery.failed", report.Events[len(report.Events)-1].Type)
}

func TestRecoveryStatusDistinguishesNotAttempted(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config"))

	status, err := store.Status(ScenarioTrustPromptUnresolved)
	require.NoError(t, err)
	require.False(t, status.Attempted)
	require.Empty(t, status.State)
	require.Equal(t, 0, status.AttemptCount)
	require.Equal(t, 1, status.RetryLimit)
	require.Equal(t, 1, status.AttemptsRemaining)
}

func TestScenarioParsingAndStartupClassificationMapping(t *testing.T) {
	scenario, err := ParseScenario("prompt-misdelivery")
	require.NoError(t, err)
	require.Equal(t, ScenarioPromptDeliveredToShell, scenario)

	scenario, ok := ScenarioFromStartupClassification("trust_required")
	require.True(t, ok)
	require.Equal(t, ScenarioTrustPromptUnresolved, scenario)

	scenario, ok = ScenarioFromStartupClassification("transport_dead")
	require.True(t, ok)
	require.Equal(t, ScenarioMCPHandshakeFailure, scenario)

	_, ok = ScenarioFromStartupClassification("unknown")
	require.False(t, ok)
}
