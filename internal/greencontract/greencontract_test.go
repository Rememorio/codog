package greencontract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateLevelSatisfiedBySameOrHigherLevel(t *testing.T) {
	contract, err := New(LevelPackage)
	require.NoError(t, err)

	outcome, err := contract.EvaluateLevel("package")
	require.NoError(t, err)
	require.Equal(t, OutcomeSatisfied, outcome.Outcome)
	require.True(t, outcome.Satisfied())

	outcome, err = contract.EvaluateLevel("merge-ready")
	require.NoError(t, err)
	require.Equal(t, OutcomeSatisfied, outcome.Outcome)
	require.Equal(t, LevelMergeReady, outcome.ObservedLevel)
}

func TestEvaluateLevelUnsatisfiedForLowerLevel(t *testing.T) {
	contract, err := New(LevelWorkspace)
	require.NoError(t, err)

	outcome, err := contract.EvaluateLevel(LevelPackage)
	require.NoError(t, err)

	require.Equal(t, OutcomeUnsatisfied, outcome.Outcome)
	require.Equal(t, LevelWorkspace, outcome.RequiredLevel)
	require.Equal(t, LevelPackage, outcome.ObservedLevel)
	require.False(t, outcome.Satisfied())
}

func TestMergeReadyContractRequiresEvidenceBeyondLevel(t *testing.T) {
	contract, err := MergeReady(LevelWorkspace)
	require.NoError(t, err)
	evidence := Evidence{
		ObservedLevel: LevelWorkspace,
		TestCommands:  []TestCommandProvenance{{Command: "go test ./...", ExitCode: 0}},
	}

	outcome, err := contract.EvaluateEvidence(evidence)
	require.NoError(t, err)

	require.Equal(t, OutcomeUnsatisfied, outcome.Outcome)
	require.Equal(t, []string{RequirementBaseBranchFreshness, RequirementRecoveryAttemptContext}, outcome.Missing)
	require.Empty(t, outcome.BlockingFlakes)
}

func TestMergeReadyContractAcceptsCompleteEvidence(t *testing.T) {
	contract, err := MergeReady(LevelWorkspace)
	require.NoError(t, err)
	evidence := Evidence{
		ObservedLevel:                  LevelMergeReady,
		TestCommands:                   []TestCommandProvenance{{Command: "go test ./...", ExitCode: 0}},
		BaseBranchFresh:                true,
		RecoveryAttemptContextRecorded: true,
	}

	outcome, err := contract.EvaluateEvidence(evidence)
	require.NoError(t, err)

	require.Equal(t, OutcomeSatisfied, outcome.Outcome)
	require.True(t, outcome.Satisfied())
}

func TestBlockingFlakePreventsSatisfaction(t *testing.T) {
	contract, err := MergeReady(LevelWorkspace)
	require.NoError(t, err)
	evidence := Evidence{
		ObservedLevel:                  LevelMergeReady,
		TestCommands:                   []TestCommandProvenance{{Command: "go test ./...", ExitCode: 0}},
		BaseBranchFresh:                true,
		RecoveryAttemptContextRecorded: true,
		KnownFlakes: []KnownFlake{{
			TestName:    "session_lifecycle_prefers_running_process_over_idle_shell",
			BlocksGreen: true,
		}},
	}

	outcome, err := contract.EvaluateEvidence(evidence)
	require.NoError(t, err)

	require.Equal(t, OutcomeUnsatisfied, outcome.Outcome)
	require.Empty(t, outcome.Missing)
	require.Equal(t, []KnownFlake{{
		TestName:    "session_lifecycle_prefers_running_process_over_idle_shell",
		BlocksGreen: true,
	}}, outcome.BlockingFlakes)
}

func TestNormalizeLevelAliases(t *testing.T) {
	level, err := NormalizeLevel("merge-ready")
	require.NoError(t, err)
	require.Equal(t, LevelMergeReady, level)

	_, err = NormalizeLevel("unknown")
	require.Error(t, err)
}
