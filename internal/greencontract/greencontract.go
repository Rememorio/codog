package greencontract

import (
	"fmt"
	"strings"
)

const (
	LevelTargetedTests = "targeted_tests"
	LevelPackage       = "package"
	LevelWorkspace     = "workspace"
	LevelMergeReady    = "merge_ready"

	RequirementRequiredLevel          = "required_level"
	RequirementTestCommandProvenance  = "test_command_provenance"
	RequirementBaseBranchFreshness    = "base_branch_freshness"
	RequirementRecoveryAttemptContext = "recovery_attempt_context"
	OutcomeSatisfied                  = "satisfied"
	OutcomeUnsatisfied                = "unsatisfied"
	StatusSatisfied                   = "satisfied"
	StatusUnsatisfied                 = "unsatisfied"
)

type Contract struct {
	RequiredLevel    string   `json:"required_level"`
	Requirements     []string `json:"requirements,omitempty"`
	BlockKnownFlakes bool     `json:"block_known_flakes"`
}

type Evidence struct {
	ObservedLevel                  string                  `json:"observed_level"`
	TestCommands                   []TestCommandProvenance `json:"test_commands,omitempty"`
	BaseBranchFresh                bool                    `json:"base_branch_fresh"`
	KnownFlakes                    []KnownFlake            `json:"known_flakes,omitempty"`
	RecoveryAttemptContextRecorded bool                    `json:"recovery_attempt_context_recorded"`
}

type TestCommandProvenance struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
}

func (p TestCommandProvenance) Passed() bool {
	return strings.TrimSpace(p.Command) != "" && p.ExitCode == 0
}

type KnownFlake struct {
	TestName    string `json:"test_name"`
	BlocksGreen bool   `json:"blocks_green"`
}

type Outcome struct {
	Outcome        string       `json:"outcome"`
	RequiredLevel  string       `json:"required_level"`
	ObservedLevel  string       `json:"observed_level,omitempty"`
	Missing        []string     `json:"missing,omitempty"`
	BlockingFlakes []KnownFlake `json:"blocking_flakes,omitempty"`
}

func New(requiredLevel string) (Contract, error) {
	level, err := NormalizeLevel(requiredLevel)
	if err != nil {
		return Contract{}, err
	}
	return Contract{RequiredLevel: level}, nil
}

func MergeReady(requiredLevel string) (Contract, error) {
	level, err := NormalizeLevel(requiredLevel)
	if err != nil {
		return Contract{}, err
	}
	return Contract{
		RequiredLevel: level,
		Requirements: []string{
			RequirementTestCommandProvenance,
			RequirementBaseBranchFreshness,
			RequirementRecoveryAttemptContext,
		},
		BlockKnownFlakes: true,
	}, nil
}

func (c Contract) EvaluateLevel(observedLevel string) (Outcome, error) {
	observed, err := NormalizeLevel(observedLevel)
	if err != nil {
		return Outcome{}, err
	}
	if LevelSatisfies(c.RequiredLevel, observed) {
		return Outcome{Outcome: OutcomeSatisfied, RequiredLevel: c.RequiredLevel, ObservedLevel: observed}, nil
	}
	return Outcome{Outcome: OutcomeUnsatisfied, RequiredLevel: c.RequiredLevel, ObservedLevel: observed}, nil
}

func (c Contract) EvaluateEvidence(e Evidence) (Outcome, error) {
	required, err := NormalizeLevel(c.RequiredLevel)
	if err != nil {
		return Outcome{}, err
	}
	observed, err := NormalizeLevel(e.ObservedLevel)
	if err != nil {
		return Outcome{}, err
	}
	missing := []string{}
	if !LevelSatisfies(required, observed) {
		missing = append(missing, RequirementRequiredLevel)
	}
	for _, requirement := range c.Requirements {
		switch requirement {
		case RequirementTestCommandProvenance:
			if !e.HasPassingTestCommand() {
				missing = append(missing, requirement)
			}
		case RequirementBaseBranchFreshness:
			if !e.BaseBranchFresh {
				missing = append(missing, requirement)
			}
		case RequirementRecoveryAttemptContext:
			if !e.RecoveryAttemptContextRecorded {
				missing = append(missing, requirement)
			}
		case RequirementRequiredLevel:
			if !LevelSatisfies(required, observed) && !containsString(missing, RequirementRequiredLevel) {
				missing = append(missing, RequirementRequiredLevel)
			}
		case "":
		default:
			return Outcome{}, fmt.Errorf("unknown green contract requirement %q", requirement)
		}
	}
	blockingFlakes := []KnownFlake{}
	if c.BlockKnownFlakes {
		for _, flake := range e.KnownFlakes {
			if flake.BlocksGreen {
				blockingFlakes = append(blockingFlakes, flake)
			}
		}
	}
	if len(missing) == 0 && len(blockingFlakes) == 0 {
		return Outcome{Outcome: OutcomeSatisfied, RequiredLevel: required, ObservedLevel: observed}, nil
	}
	return Outcome{
		Outcome:        OutcomeUnsatisfied,
		RequiredLevel:  required,
		ObservedLevel:  observed,
		Missing:        missing,
		BlockingFlakes: blockingFlakes,
	}, nil
}

func (e Evidence) HasPassingTestCommand() bool {
	for _, command := range e.TestCommands {
		if command.Passed() {
			return true
		}
	}
	return false
}

func (o Outcome) Satisfied() bool {
	return o.Outcome == OutcomeSatisfied
}

func StatusForOutcome(outcome Outcome) string {
	if outcome.Satisfied() {
		return StatusSatisfied
	}
	return StatusUnsatisfied
}

func NormalizeLevel(level string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(level, "-", "_")))
	switch normalized {
	case "", "targeted", "targeted_test", "targeted_tests":
		return LevelTargetedTests, nil
	case "package", "pkg":
		return LevelPackage, nil
	case "workspace", "repo", "repository":
		return LevelWorkspace, nil
	case "merge_ready", "merge", "ready":
		return LevelMergeReady, nil
	default:
		return "", fmt.Errorf("unknown green level %q", level)
	}
}

func LevelSatisfies(required string, observed string) bool {
	requiredRank := levelRank(required)
	observedRank := levelRank(observed)
	return requiredRank > 0 && observedRank >= requiredRank
}

func levelRank(level string) int {
	switch level {
	case LevelTargetedTests:
		return 1
	case LevelPackage:
		return 2
	case LevelWorkspace:
		return 3
	case LevelMergeReady:
		return 4
	default:
		return 0
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
