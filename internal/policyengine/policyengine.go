package policyengine

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type ActionKind string

const (
	ActionMergeToDev     ActionKind = "merge_to_dev"
	ActionMergeForward   ActionKind = "merge_forward"
	ActionRecoverOnce    ActionKind = "recover_once"
	ActionEscalate       ActionKind = "escalate"
	ActionCloseoutLane   ActionKind = "closeout_lane"
	ActionCleanupSession ActionKind = "cleanup_session"
	ActionBlock          ActionKind = "block"
)

type DecisionKind string

const (
	DecisionMerge    DecisionKind = "merge"
	DecisionRecover  DecisionKind = "recover"
	DecisionEscalate DecisionKind = "escalate"
	DecisionCloseout DecisionKind = "closeout"
	DecisionCleanup  DecisionKind = "cleanup"
	DecisionBlock    DecisionKind = "block"
)

type LaneContext struct {
	LaneID                 string `json:"lane_id"`
	GreenLevel             int    `json:"green_level"`
	GreenContractSatisfied bool   `json:"green_contract_satisfied"`
	BranchStatus           string `json:"branch_status,omitempty"`
	BranchBehind           int    `json:"branch_behind,omitempty"`
	VerificationBlocked    bool   `json:"verification_blocked"`
	Blocker                string `json:"blocker,omitempty"`
	ReviewStatus           string `json:"review_status,omitempty"`
	DiffScope              string `json:"diff_scope,omitempty"`
	Completed              bool   `json:"completed"`
	RetryCount             int    `json:"retry_count"`
	RetryLimit             int    `json:"retry_limit"`
}

type Action struct {
	Kind             ActionKind `json:"kind"`
	Reason           string     `json:"reason,omitempty"`
	RecoveryScenario string     `json:"recovery_scenario,omitempty"`
	Commands         []string   `json:"commands,omitempty"`
}

type Rule struct {
	ID       string
	Priority int
	When     func(LaneContext) bool
	Actions  func(LaneContext) []Action
}

type DecisionEvent struct {
	LaneID      string       `json:"lane_id"`
	RuleID      string       `json:"rule_id"`
	Priority    int          `json:"priority"`
	Kind        DecisionKind `json:"kind"`
	Action      ActionKind   `json:"action"`
	Explanation string       `json:"explanation"`
	CreatedAt   time.Time    `json:"created_at"`
}

type Evaluation struct {
	Kind    string          `json:"kind"`
	Context LaneContext     `json:"context"`
	Actions []Action        `json:"actions"`
	Events  []DecisionEvent `json:"events"`
}

type Engine struct {
	rules []Rule
}

func DefaultEngine() Engine {
	return NewEngine([]Rule{
		{
			ID:       "stale-branch-merge-forward",
			Priority: 10,
			When: func(ctx LaneContext) bool {
				return stale(ctx)
			},
			Actions: func(ctx LaneContext) []Action {
				commands := []string{"branch_freshness"}
				if ctx.BranchBehind > 0 {
					commands = append(commands, "git merge --ff-only <base>")
				}
				return []Action{{
					Kind:             ActionMergeForward,
					Reason:           "branch is behind base; update branch before broad verification",
					RecoveryScenario: "stale_branch",
					Commands:         commands,
				}}
			},
		},
		{
			ID:       "startup-blocked-recover",
			Priority: 20,
			When: func(ctx LaneContext) bool {
				return normalize(ctx.Blocker) == "startup" && effectiveRetryLimit(ctx) > ctx.RetryCount
			},
			Actions: func(LaneContext) []Action {
				return []Action{{
					Kind:             ActionRecoverOnce,
					Reason:           "startup is blocked and one automatic recovery attempt is still available",
					RecoveryScenario: "startup_blocked",
					Commands:         []string{"recovery_attempt"},
				}}
			},
		},
		{
			ID:       "startup-blocked-escalate",
			Priority: 21,
			When: func(ctx LaneContext) bool {
				return normalize(ctx.Blocker) == "startup" && effectiveRetryLimit(ctx) <= ctx.RetryCount
			},
			Actions: func(LaneContext) []Action {
				return []Action{{
					Kind:   ActionEscalate,
					Reason: "startup remained blocked after automatic recovery was exhausted",
				}}
			},
		},
		{
			ID:       "green-scoped-reviewed-merge",
			Priority: 30,
			When: func(ctx LaneContext) bool {
				return ctx.GreenContractSatisfied &&
					ctx.GreenLevel >= 2 &&
					normalize(ctx.DiffScope) == "scoped" &&
					normalize(ctx.ReviewStatus) == "approved" &&
					!stale(ctx)
			},
			Actions: func(LaneContext) []Action {
				return []Action{{
					Kind:     ActionMergeToDev,
					Reason:   "green contract is satisfied, diff is scoped, and review is approved",
					Commands: []string{"git merge <lane-branch>"},
				}}
			},
		},
		{
			ID:       "lane-completed-closeout",
			Priority: 40,
			When: func(ctx LaneContext) bool {
				return ctx.Completed
			},
			Actions: func(LaneContext) []Action {
				return []Action{
					{Kind: ActionCloseoutLane, Reason: "lane completed; emit closeout"},
					{Kind: ActionCleanupSession, Reason: "lane completed; cleanup session state"},
				}
			},
		},
	})
}

func NewEngine(rules []Rule) Engine {
	rules = append([]Rule(nil), rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})
	return Engine{rules: rules}
}

func (e Engine) Evaluate(ctx LaneContext) Evaluation {
	ctx = NormalizeContext(ctx)
	actions := []Action{}
	events := []DecisionEvent{}
	now := time.Now().UTC()
	for _, rule := range e.rules {
		if rule.When == nil || rule.Actions == nil || !rule.When(ctx) {
			continue
		}
		for _, action := range rule.Actions(ctx) {
			action.Reason = strings.TrimSpace(action.Reason)
			actions = append(actions, action)
			events = append(events, decisionEvent(ctx, rule, action, now))
		}
	}
	return Evaluation{Kind: "policy_evaluation", Context: ctx, Actions: actions, Events: events}
}

func NormalizeContext(ctx LaneContext) LaneContext {
	ctx.LaneID = strings.TrimSpace(ctx.LaneID)
	if ctx.LaneID == "" {
		ctx.LaneID = "lane"
	}
	ctx.BranchStatus = normalize(ctx.BranchStatus)
	ctx.Blocker = normalize(ctx.Blocker)
	ctx.ReviewStatus = normalize(ctx.ReviewStatus)
	ctx.DiffScope = normalize(ctx.DiffScope)
	if ctx.RetryLimit <= 0 {
		ctx.RetryLimit = 1
	}
	if ctx.RetryCount < 0 {
		ctx.RetryCount = 0
	}
	if ctx.BranchBehind < 0 {
		ctx.BranchBehind = 0
	}
	if ctx.GreenLevel < 0 {
		ctx.GreenLevel = 0
	}
	return ctx
}

func decisionEvent(ctx LaneContext, rule Rule, action Action, now time.Time) DecisionEvent {
	return DecisionEvent{
		LaneID:      ctx.LaneID,
		RuleID:      rule.ID,
		Priority:    rule.Priority,
		Kind:        decisionKind(action.Kind),
		Action:      action.Kind,
		Explanation: explanation(ctx, rule, action),
		CreatedAt:   now,
	}
}

func decisionKind(kind ActionKind) DecisionKind {
	switch kind {
	case ActionMergeToDev, ActionMergeForward:
		return DecisionMerge
	case ActionRecoverOnce:
		return DecisionRecover
	case ActionEscalate:
		return DecisionEscalate
	case ActionCloseoutLane:
		return DecisionCloseout
	case ActionCleanupSession:
		return DecisionCleanup
	default:
		return DecisionBlock
	}
}

func explanation(ctx LaneContext, rule Rule, action Action) string {
	if action.Reason != "" {
		return fmt.Sprintf("rule %q selected %s for %s: %s", rule.ID, action.Kind, ctx.LaneID, action.Reason)
	}
	return fmt.Sprintf("rule %q selected %s for %s", rule.ID, action.Kind, ctx.LaneID)
}

func effectiveRetryLimit(ctx LaneContext) int {
	if ctx.RetryLimit <= 0 {
		return 1
	}
	return ctx.RetryLimit
}

func normalize(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func stale(ctx LaneContext) bool {
	status := normalize(ctx.BranchStatus)
	return ctx.VerificationBlocked || ctx.BranchBehind > 0 || status == "stale" || status == "diverged"
}
