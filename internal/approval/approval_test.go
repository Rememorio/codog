package approval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApprovalTokenBlocksUntilGranted(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := store.Verify("missing", scope, "release-bot", now)
	requireApprovalError(t, err, "no_approval")

	pending, err := store.Grant(GrantOptions{
		Token:            "tok-pending",
		Scope:            scope,
		ApprovingActor:   "repo-owner",
		ApprovedExecutor: "release-bot",
		Status:           StatusPending,
		Now:              now,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)

	_, err = store.Verify("tok-pending", scope, "release-bot", now)
	requireApprovalError(t, err, "approval_pending")

	granted, err := store.Grant(GrantOptions{
		Token:            "tok-granted",
		Scope:            scope,
		ApprovingActor:   "repo-owner",
		ApprovedExecutor: "release-bot",
		Now:              now,
	})
	require.NoError(t, err)
	require.Equal(t, StatusGranted, granted.Status)

	audit, err := store.Verify("tok-granted", scope, "release-bot", now)
	require.NoError(t, err)
	require.Equal(t, "approval_token_audit", audit.Kind)
	require.Equal(t, "repo-owner", audit.ApprovingActor)
	require.Equal(t, "release-bot", audit.RequestingActor)
	require.Equal(t, "release-bot", audit.ExecutingActor)
	require.Equal(t, "delegated_execution", audit.ExecutionMode)
	require.True(t, audit.DelegatedExecution)
	require.Equal(t, []DelegationHop{
		{Actor: "repo-owner", Reason: "approval granted"},
		{Actor: "release-bot", Reason: "delegated execution"},
	}, audit.DelegationChain)
}

func TestApprovalTokenApprovesPendingGrantInPlace(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expiresAt := now.Add(time.Hour)

	pending, err := store.Grant(GrantOptions{
		Token:            "tok-pending",
		Scope:            scope,
		ApprovingActor:   "repo-owner",
		ApprovedExecutor: "release-bot",
		Status:           StatusPending,
		Now:              now,
	})
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)

	approved, err := store.Approve("tok-pending", GrantOptions{
		Scope:            scope,
		ApprovingActor:   "repo-owner",
		ApprovedExecutor: "release-bot",
		ExpiresAt:        &expiresAt,
		MaxUses:          2,
		DelegationChain: []DelegationHop{
			{Actor: "repo-owner", SessionID: "session-owner", Reason: "owner approval"},
			{Actor: "lead-agent", SessionID: "session-lead", Reason: "handoff"},
		},
		Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, "tok-pending", approved.Token)
	require.Equal(t, StatusGranted, approved.Status)
	require.Equal(t, 2, approved.MaxUses)
	require.Equal(t, &expiresAt, approved.ExpiresAt)
	require.Equal(t, []string{"repo-owner", "lead-agent"}, delegationActors(approved.DelegationChain))

	audit, err := store.Verify("tok-pending", scope, "release-bot", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, StatusGranted, audit.Status)
	require.Equal(t, "release-bot", audit.RequestingActor)
	require.Equal(t, "delegated_execution", audit.ExecutionMode)
	require.Equal(t, []string{"repo-owner", "lead-agent", "release-bot"}, delegationActors(audit.DelegationChain))
}

func TestApprovalTokenAuditRecordsRequesterAndExecutionMode(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "release_requires_owner", Action: "release publish", Repository: "owner/repo", Branch: "main", Commit: "abc123"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := store.Grant(GrantOptions{
		Token:            "tok-delegated",
		Scope:            scope,
		ApprovingActor:   "owner",
		RequestingActor:  "release-lead",
		ApprovedExecutor: "release-bot",
		DelegationChain: []DelegationHop{
			{Actor: "owner", SessionID: "session-owner", Reason: "owner approval"},
			{Actor: "orchestrator", SessionID: "session-orchestrator", Reason: "relay"},
		},
		Now: now,
	})
	require.NoError(t, err)

	audit, err := store.Verify("tok-delegated", scope, "release-bot", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "owner", audit.ApprovingActor)
	require.Equal(t, "release-lead", audit.RequestingActor)
	require.Equal(t, "release-bot", audit.ExecutingActor)
	require.Equal(t, "delegated_execution", audit.ExecutionMode)
	require.True(t, audit.DelegatedExecution)
	require.Equal(t, []string{"owner", "orchestrator", "release-lead", "release-bot"}, delegationActors(audit.DelegationChain))

	_, err = store.Grant(GrantOptions{
		Token:            "tok-direct",
		Scope:            scope,
		ApprovingActor:   "owner",
		RequestingActor:  "owner",
		ApprovedExecutor: "owner",
		Now:              now.Add(2 * time.Second),
	})
	require.NoError(t, err)
	direct, err := store.Verify("tok-direct", scope, "owner", now.Add(3*time.Second))
	require.NoError(t, err)
	require.Equal(t, "owner", direct.RequestingActor)
	require.Equal(t, "owner", direct.ExecutingActor)
	require.Equal(t, "direct_self_use", direct.ExecutionMode)
	require.False(t, direct.DelegatedExecution)
	require.Equal(t, []string{"owner"}, delegationActors(direct.DelegationChain))
}

func TestApprovalTokenRejectsApproveWhenNotPending(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := store.Approve("missing", GrantOptions{Scope: scope, Now: now})
	requireApprovalError(t, err, "no_approval")

	_, err = store.Grant(GrantOptions{
		Token:            "tok-granted",
		Scope:            scope,
		ApprovingActor:   "repo-owner",
		ApprovedExecutor: "release-bot",
		Now:              now,
	})
	require.NoError(t, err)

	_, err = store.Approve("tok-granted", GrantOptions{Scope: scope, Now: now.Add(time.Minute)})
	requireApprovalError(t, err, "approval_not_pending")
}

func TestApprovalTokenConsumeRejectsReplay(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "release_requires_owner", Action: "release publish", Repository: "owner/repo"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := store.Grant(GrantOptions{
		Token:            "tok-once",
		Scope:            scope,
		ApprovingActor:   "owner",
		ApprovedExecutor: "release-bot",
		Now:              now,
	})
	require.NoError(t, err)

	audit, err := store.Consume("tok-once", scope, "release-bot", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, StatusConsumed, audit.Status)
	require.Equal(t, 1, audit.Uses)

	_, err = store.Consume("tok-once", scope, "release-bot", now.Add(2*time.Second))
	requireApprovalError(t, err, "approval_already_consumed")

	ledger, err := store.List()
	require.NoError(t, err)
	require.Len(t, ledger.Grants, 1)
	require.Equal(t, StatusConsumed, ledger.Grants[0].Status)
	require.Equal(t, 1, ledger.Grants[0].Uses)
}

func TestApprovalTokenListSurfacesUsageStates(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main", Commit: "abc123"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expiredAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := store.Grant(GrantOptions{Token: "tok-pending", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", Status: StatusPending, Now: now})
	require.NoError(t, err)
	_, err = store.Grant(GrantOptions{Token: "tok-unused", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", MaxUses: 2, Now: now.Add(time.Second)})
	require.NoError(t, err)
	_, err = store.Grant(GrantOptions{Token: "tok-partial", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", MaxUses: 2, Now: now.Add(2 * time.Second)})
	require.NoError(t, err)
	_, err = store.Consume("tok-partial", scope, "bot", now.Add(3*time.Second))
	require.NoError(t, err)
	_, err = store.Grant(GrantOptions{Token: "tok-consumed", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", Now: now.Add(4 * time.Second)})
	require.NoError(t, err)
	_, err = store.Consume("tok-consumed", scope, "bot", now.Add(5*time.Second))
	require.NoError(t, err)
	_, err = store.Grant(GrantOptions{Token: "tok-expired", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", ExpiresAt: &expiredAt, Now: now.Add(6 * time.Second)})
	require.NoError(t, err)
	_, err = store.Grant(GrantOptions{Token: "tok-revoked", Scope: scope, ApprovingActor: "owner", ApprovedExecutor: "bot", Now: now.Add(7 * time.Second)})
	require.NoError(t, err)
	_, err = store.Revoke("tok-revoked", now.Add(8*time.Second))
	require.NoError(t, err)

	ledger, err := store.List()
	require.NoError(t, err)
	grants := grantsByToken(ledger.Grants)
	require.Equal(t, UsagePending, grants["tok-pending"].State)
	require.False(t, grants["tok-pending"].Usable)
	require.Equal(t, UsageUnused, grants["tok-unused"].State)
	require.True(t, grants["tok-unused"].Usable)
	require.Equal(t, 2, grants["tok-unused"].RemainingUses)
	require.Equal(t, UsagePartiallyConsumed, grants["tok-partial"].State)
	require.True(t, grants["tok-partial"].Usable)
	require.Equal(t, 1, grants["tok-partial"].RemainingUses)
	require.Equal(t, UsageConsumed, grants["tok-consumed"].State)
	require.False(t, grants["tok-consumed"].Usable)
	require.Equal(t, 0, grants["tok-consumed"].RemainingUses)
	require.Equal(t, UsageExpired, grants["tok-expired"].State)
	require.False(t, grants["tok-expired"].Usable)
	require.Equal(t, UsageRevoked, grants["tok-revoked"].State)
	require.False(t, grants["tok-revoked"].Usable)
}

func TestApprovalTokenRejectsScopeExpiryRevocationAndDelegateMismatch(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main", Commit: "abc123"}
	devScope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "dev", Commit: "abc123"}
	otherCommitScope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main", Commit: "def456"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expiresAt := now.Add(time.Minute)

	_, err := store.Grant(GrantOptions{
		Token:            "tok-expiring",
		Scope:            scope,
		ApprovingActor:   "owner",
		ApprovedExecutor: "bot",
		ExpiresAt:        &expiresAt,
		Now:              now,
	})
	require.NoError(t, err)

	_, err = store.Verify("tok-expiring", devScope, "bot", now)
	requireApprovalError(t, err, "approval_scope_mismatch")

	_, err = store.Verify("tok-expiring", otherCommitScope, "bot", now)
	requireApprovalError(t, err, "approval_scope_mismatch")

	_, err = store.Verify("tok-expiring", scope, "other-bot", now)
	requireApprovalError(t, err, "approval_unauthorized_delegate")

	_, err = store.Verify("tok-expiring", scope, "bot", expiresAt.Add(time.Second))
	requireApprovalError(t, err, "approval_expired")

	_, err = store.Grant(GrantOptions{
		Token:            "tok-revoked",
		Scope:            scope,
		ApprovingActor:   "owner",
		ApprovedExecutor: "bot",
		Now:              now,
	})
	require.NoError(t, err)
	revoked, err := store.Revoke("tok-revoked", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, StatusRevoked, revoked.Status)

	_, err = store.Verify("tok-revoked", scope, "bot", now.Add(2*time.Second))
	requireApprovalError(t, err, "approval_revoked")
}

func TestApprovalTokenPersistsLedger(t *testing.T) {
	configHome := t.TempDir()
	store := NewStore(configHome)
	scope := Scope{Policy: "deploy_requires_owner", Action: "deploy prod"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	grant, err := store.Grant(GrantOptions{
		Scope:            scope,
		ApprovingActor:   "owner",
		ApprovedExecutor: "deploy-bot",
		MaxUses:          2,
		DelegationChain: []DelegationHop{
			{Actor: "owner", SessionID: "session-owner", Reason: "owner approval"},
			{Actor: "lead-agent", SessionID: "session-lead", Reason: "handoff"},
		},
		Now: now,
	})
	require.NoError(t, err)
	require.Contains(t, grant.Token, "codog-approval-")

	reloaded := NewStore(configHome)
	audit, err := reloaded.Consume(grant.Token, scope, "deploy-bot", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, StatusGranted, audit.Status)
	require.Equal(t, 1, audit.Uses)
	require.Equal(t, []string{"owner", "lead-agent", "deploy-bot"}, delegationActors(audit.DelegationChain))

	data, err := os.ReadFile(filepath.Join(configHome, "approval-tokens.json"))
	require.NoError(t, err)
	var ledger Ledger
	require.NoError(t, json.Unmarshal(data, &ledger))
	require.Equal(t, "approval_token_ledger", ledger.Kind)
	require.Len(t, ledger.Grants, 1)
	require.Equal(t, 1, ledger.Grants[0].Uses)
	require.Equal(t, StatusGranted, ledger.Grants[0].Status)
}

func requireApprovalError(t *testing.T, err error, kind string) {
	t.Helper()
	require.Error(t, err)
	var approvalErr Error
	require.True(t, errors.As(err, &approvalErr), "expected approval.Error, got %T: %v", err, err)
	require.Equal(t, kind, approvalErr.Kind)
}

func delegationActors(chain []DelegationHop) []string {
	out := make([]string, 0, len(chain))
	for _, hop := range chain {
		out = append(out, hop.Actor)
	}
	return out
}

func grantsByToken(grants []Grant) map[string]Grant {
	out := make(map[string]Grant, len(grants))
	for _, grant := range grants {
		out[grant.Token] = grant
	}
	return out
}
