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
	require.Equal(t, "release-bot", audit.ExecutingActor)
	require.True(t, audit.DelegatedExecution)
	require.Equal(t, []DelegationHop{
		{Actor: "repo-owner", Reason: "approval granted"},
		{Actor: "release-bot", Reason: "delegated execution"},
	}, audit.DelegationChain)
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

func TestApprovalTokenRejectsScopeExpiryRevocationAndDelegateMismatch(t *testing.T) {
	store := NewStore(t.TempDir())
	scope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "main"}
	devScope := Scope{Policy: "main_push_forbidden", Action: "git push", Repository: "owner/repo", Branch: "dev"}
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
