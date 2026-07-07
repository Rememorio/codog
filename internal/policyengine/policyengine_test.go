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

func TestDefaultPolicyReturnsBlockedHandoffForMainPush(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:          "lane-7",
		RequestedAction: "git push origin main",
		Repository:      "owner/repo",
		Branch:          "main",
		Actor:           "release-bot",
		ActorScope:      "automation",
		PolicySource:    "AGENTS.md",
	})
	require.Len(t, evaluation.Actions, 1)
	require.Equal(t, ActionBlock, evaluation.Actions[0].Kind)
	require.Equal(t, DecisionBlock, evaluation.Events[0].Kind)
	require.Equal(t, "policy-blocked-handoff", evaluation.Events[0].RuleID)
	require.NotNil(t, evaluation.BlockedHandoff)
	require.Equal(t, "policy_blocked_handoff", evaluation.BlockedHandoff.Kind)
	require.Equal(t, "blocked_by_policy", evaluation.BlockedHandoff.Status)
	require.Equal(t, "main_push_forbidden", evaluation.BlockedHandoff.Reason)
	require.Equal(t, "AGENTS.md", evaluation.BlockedHandoff.PolicySource)
	require.Equal(t, "automation", evaluation.BlockedHandoff.ActorScope)
	require.Equal(t, "release-bot", evaluation.BlockedHandoff.Actor)
	require.Equal(t, "git push origin main", evaluation.BlockedHandoff.RequestedAction)
	require.Equal(t, "owner/repo", evaluation.BlockedHandoff.Repository)
	require.Equal(t, "main", evaluation.BlockedHandoff.Branch)
	require.False(t, evaluation.BlockedHandoff.TechnicalFailure)
	require.False(t, evaluation.BlockedHandoff.ApprovalRequired)
	require.Len(t, evaluation.BlockedHandoff.Fallback, 2)
	require.Equal(t, "create_branch", evaluation.BlockedHandoff.Fallback[0].Kind)
	require.Equal(t, "open_pr", evaluation.BlockedHandoff.Fallback[1].Kind)
}

func TestDefaultPolicyReturnsOwnerApprovalFallbackForReleaseBlock(t *testing.T) {
	evaluation := DefaultEngine().Evaluate(LaneContext{
		LaneID:          "lane-release",
		RequestedAction: "release production",
		Repository:      "owner/repo",
		Branch:          "release",
		Actor:           "release-bot",
		ActorScope:      "automation",
	})
	require.NotNil(t, evaluation.BlockedHandoff)
	require.Equal(t, "release_requires_owner", evaluation.BlockedHandoff.Reason)
	require.Equal(t, "release_policy", evaluation.BlockedHandoff.PolicySource)
	require.True(t, evaluation.BlockedHandoff.ApprovalRequired)
	require.Len(t, evaluation.BlockedHandoff.Fallback, 2)
	require.Equal(t, "request_owner_approval", evaluation.BlockedHandoff.Fallback[0].Kind)
	require.True(t, evaluation.BlockedHandoff.Fallback[0].RequiresApproval)
	require.Equal(t, "verify_approval", evaluation.BlockedHandoff.Fallback[1].Kind)
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
