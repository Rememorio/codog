package trustresolver

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectTrustPrompt(t *testing.T) {
	require.True(t, DetectTrustPrompt("Do you trust the files in this folder?\n1. Yes, proceed\n2. No"))
	require.False(t, DetectTrustPrompt("Ready for input"))
}

func TestResolverAutoTrustsAllowlistedPrompt(t *testing.T) {
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{Pattern: "/worktrees"}},
	})

	decision := resolver.Resolve("/worktrees/repo-a", "", "Do you trust the files in this folder?")

	require.Equal(t, StatusAutoTrusted, decision.Status)
	require.True(t, decision.PromptDetected)
	require.True(t, decision.Trusted)
	require.Equal(t, PolicyAutoTrust, decision.Policy)
	require.Equal(t, ResolutionAutoAllowlisted, decision.Resolution)
	require.Len(t, decision.Events, 2)
	require.Equal(t, "trust_required", decision.Events[0].Type)
	require.Equal(t, "repo-a", decision.Events[0].Repo)
	require.Equal(t, "trust_resolved", decision.Events[1].Type)
}

func TestResolverEventRepoUsesGitTopLevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo-a")
	nested := filepath.Join(repo, "pkg", "service")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	cmd := exec.Command("git", "init", repo)
	require.NoError(t, cmd.Run())
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{Pattern: parent}},
	})

	decision := resolver.Resolve(nested, "", "Do you trust the files in this folder?")

	require.Len(t, decision.Events, 2)
	require.Equal(t, "repo-a", decision.Events[0].Repo)
}

func TestResolverDenyOverridesAllowlist(t *testing.T) {
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{Pattern: "/worktrees"}},
		Denied:      []string{"/worktrees/repo-a"},
	})

	decision := resolver.Resolve("/worktrees/repo-a", "", "Trust this folder?")

	require.Equal(t, StatusDenied, decision.Status)
	require.False(t, decision.Trusted)
	require.Equal(t, PolicyDeny, decision.Policy)
	require.Equal(t, cleanPath("/worktrees/repo-a"), decision.MatchedPattern)
	require.Len(t, decision.Events, 2)
	require.Equal(t, "trust_denied", decision.Events[1].Type)
}

func TestResolverRequiresApprovalForUnknownPrompt(t *testing.T) {
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{Pattern: "/worktrees"}},
	})

	decision := resolver.Resolve("/other/repo-b", "", "Do you trust the files in this folder?")

	require.Equal(t, StatusRequiresApproval, decision.Status)
	require.False(t, decision.Trusted)
	require.Equal(t, PolicyRequireApproval, decision.Policy)
	require.Len(t, decision.Events, 1)
}

func TestResolverManualApprovalMarksTrusted(t *testing.T) {
	resolver := New(Config{})

	decision := resolver.Resolve("/other/repo-b", "", "Do you trust the files in this folder? approval granted")

	require.Equal(t, StatusRequiresApproval, decision.Status)
	require.True(t, decision.Trusted)
	require.Equal(t, ResolutionManualApproval, decision.Resolution)
	require.Len(t, decision.Events, 2)
}

func TestResolverNotRequiredStillReportsExistingTrust(t *testing.T) {
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{Pattern: "/worktrees/*"}},
	})

	decision := resolver.Resolve("/worktrees/repo-a", "", "Ready")

	require.Equal(t, StatusNotRequired, decision.Status)
	require.False(t, decision.PromptDetected)
	require.True(t, decision.Trusted)
	require.Empty(t, decision.Events)
}

func TestAllowlistEntryWithWorktreePattern(t *testing.T) {
	resolver := New(Config{
		Allowlisted: []AllowlistEntry{{
			Pattern:         "/worktrees/*",
			WorktreePattern: "*/.git",
		}},
	})

	require.True(t, resolver.Trusts("/worktrees/repo-a", "/worktrees/repo-a/.git"))
	require.False(t, resolver.Trusts("/worktrees/repo-a", "/other/path"))
	require.False(t, resolver.Trusts("/worktrees/repo-a", ""))
}

func TestPatternMatches(t *testing.T) {
	require.True(t, PatternMatches("/worktrees/*", "/worktrees"))
	require.True(t, PatternMatches("/worktrees/*", "/worktrees/repo-a/subdir"))
	require.True(t, PatternMatches("/tmp/*/repo-?", "/tmp/worktrees/repo-a"))
	require.True(t, PatternMatches("repo", "/tmp/worktrees/repo-a"))
	require.False(t, PatternMatches("/worktrees/*", "/other/repo-a"))
}

func TestPathMatchesTrustedRoot(t *testing.T) {
	root := filepath.Join("tmp", "worktrees")

	require.True(t, PathMatchesTrustedRoot(filepath.Join(root, "repo-a"), root))
	require.True(t, PathMatchesTrustedRoot(root, root))
	require.False(t, PathMatchesTrustedRoot(filepath.Join("tmp", "worktrees-other"), root))
}

func TestNewWithoutEventsSuppressesEvents(t *testing.T) {
	resolver := NewWithoutEvents(Config{
		Allowlisted: []AllowlistEntry{{Pattern: "/worktrees"}},
	})

	decision := resolver.Resolve("/worktrees/repo-a", "", "Trust this folder?")

	require.Equal(t, StatusAutoTrusted, decision.Status)
	require.Empty(t, decision.Events)
}
