package policyengine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultPolicyMergeRule(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:                 "lane-7",
		GreenLevel:             3,
		GreenContractSatisfied: true,
		ReviewStatus:           "approved",
		DiffScope:              "scoped",
	})
	require.Len(t, evaluation.Actions, 1)
	require.Equal(t, ActionMergeToDev, evaluation.Actions[0].Kind)
	require.Equal(t, DecisionMerge, evaluation.Events[0].Kind)
	require.Equal(t, "green-scoped-reviewed-merge", evaluation.Events[0].RuleID)
}

func TestDefaultPolicyBlocksMergeWithoutGreenContract(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:       "lane-7",
		GreenLevel:   3,
		ReviewStatus: "approved",
		DiffScope:    "scoped",
	})
	require.Empty(t, evaluation.Actions)
	require.Empty(t, evaluation.Events)
}

func TestDefaultPolicyBlocksMergeWhenBranchIsStale(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:                 "lane-7",
		GreenLevel:             3,
		GreenContractSatisfied: true,
		ReviewStatus:           "approved",
		DiffScope:              "scoped",
		BranchBehind:           1,
	})
	require.Len(t, evaluation.Actions, 1)
	require.Equal(t, ActionMergeForward, evaluation.Actions[0].Kind)
}

func TestDefaultPolicyStaleBranchMergeForward(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:              "lane-7",
		BranchStatus:        "stale",
		BranchBehind:        2,
		VerificationBlocked: true,
	})
	require.Len(t, evaluation.Actions, 1)
	require.Equal(t, ActionMergeForward, evaluation.Actions[0].Kind)
	require.Equal(t, "stale_branch", evaluation.Actions[0].RecoveryScenario)
	require.Contains(t, evaluation.Actions[0].Commands, "branch_freshness")
	require.Equal(t, DecisionMerge, evaluation.Events[0].Kind)
}

func TestDefaultPolicyStartupBlockedRecoversThenEscalates(t *testing.T) {
	recoverEval := DefaultEngine().Evaluate(LaneContext{
		LaneID:     "lane-7",
		Blocker:    "startup",
		RetryCount: 0,
		RetryLimit: 1,
	})
	require.Len(t, recoverEval.Actions, 1)
	require.Equal(t, ActionRecoverOnce, recoverEval.Actions[0].Kind)
	require.Equal(t, DecisionRecover, recoverEval.Events[0].Kind)

	escalateEval := DefaultEngine().Evaluate(LaneContext{
		LaneID:     "lane-7",
		Blocker:    "startup",
		RetryCount: 1,
		RetryLimit: 1,
	})
	require.Len(t, escalateEval.Actions, 1)
	require.Equal(t, ActionEscalate, escalateEval.Actions[0].Kind)
	require.Equal(t, DecisionEscalate, escalateEval.Events[0].Kind)
}

func TestDefaultPolicyCompletedLaneCloseout(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:    "lane-7",
		Completed: true,
	})
	require.Len(t, evaluation.Actions, 2)
	require.Equal(t, ActionCloseoutLane, evaluation.Actions[0].Kind)
	require.Equal(t, ActionCleanupSession, evaluation.Actions[1].Kind)
	require.Equal(t, DecisionCloseout, evaluation.Events[0].Kind)
	require.Equal(t, DecisionCleanup, evaluation.Events[1].Kind)
}

func TestDefaultPolicyOrdersActionsByPriority(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:                 "lane-7",
		GreenLevel:             3,
		GreenContractSatisfied: true,
		BranchBehind:           1,
		ReviewStatus:           "approved",
		DiffScope:              "scoped",
		Completed:              true,
	})
	require.Equal(t, []ActionKind{
		ActionMergeForward,
		ActionCloseoutLane,
		ActionCleanupSession,
	}, []ActionKind{
		evaluation.Actions[0].Kind,
		evaluation.Actions[1].Kind,
		evaluation.Actions[2].Kind,
	})
	require.Equal(t, 10, evaluation.Events[0].Priority)
	require.Equal(t, 40, evaluation.Events[1].Priority)
}
