package gitops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusDiffAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))

	status, err := Status(workspace)
	require.NoError(t, err)
	require.Contains(t, status, "notes.txt")

	diff, err := Diff(workspace, false)
	require.NoError(t, err)
	require.Empty(t, diff)

	changedFiles, err := DiffChangedFilesWithOptions(workspace, DiffOptions{})
	require.NoError(t, err)
	require.Empty(t, changedFiles)

	result, err := Commit(workspace, CommitOptions{All: true, Message: "add notes"})
	require.NoError(t, err)
	require.NotEmpty(t, result.Commit)
	require.Contains(t, result.Summary, "add notes")

	log, err := Log(workspace, 1)
	require.NoError(t, err)
	require.Contains(t, log, "add notes")

	changelog, err := Changelog(workspace, 1)
	require.NoError(t, err)
	require.Contains(t, changelog, "add notes")
	require.Contains(t, changelog, "notes.txt")

	root, err := Root(workspace)
	require.NoError(t, err)
	expectedRoot, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, expectedRoot, root)

	branch, err := Branch(workspace)
	require.NoError(t, err)
	require.NotEmpty(t, branch)

	head, err := Head(workspace)
	require.NoError(t, err)
	require.NotEmpty(t, head)

	blame, err := Blame(workspace, "notes.txt", 1)
	require.NoError(t, err)
	require.Contains(t, blame, "hello")
	require.Contains(t, blame, "Codog Test")

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\nagain\n"), 0o644))
	changedFiles, err = DiffChangedFilesWithOptions(workspace, DiffOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"notes.txt"}, changedFiles)

	status, err = Status(workspace)
	require.NoError(t, err)
	require.True(t, strings.Contains(status, "## main") || strings.Contains(status, "## master"))
}

func TestInspectIdentityReportsHeadAndDetachedState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add notes")

	identity, err := InspectIdentity(workspace)
	require.NoError(t, err)
	require.Len(t, identity.HeadSHA, 40)
	require.NotEmpty(t, identity.HeadShortSHA)
	require.Equal(t, "main", identity.HeadRef)
	require.False(t, identity.IsDetached)
	require.False(t, identity.IsBare)
	require.False(t, identity.IsWorktree)
	require.NotEmpty(t, identity.GitDir)

	runGit(t, workspace, "checkout", "--detach", "HEAD")
	identity, err = InspectIdentity(workspace)
	require.NoError(t, err)
	require.True(t, identity.IsDetached)
	require.Empty(t, identity.HeadRef)
	require.NotEmpty(t, identity.HeadShortSHA)
}

func TestPreserveStateForIssueFallsBackWithoutRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("base\nchange\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scratch.txt"), []byte("scratch\n"), 0o644))

	state, err := PreserveStateForIssue(workspace)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Empty(t, state.RemoteBase)
	require.Empty(t, state.RemoteBaseSHA)
	require.Empty(t, state.FormatPatch)
	require.NotEmpty(t, state.HeadSHA)
	require.Equal(t, "main", state.BranchName)
	require.Contains(t, state.Patch, "+change")
	require.Equal(t, []PreservedUntrackedFile{{Path: "scratch.txt", Content: "scratch\n"}}, state.UntrackedFiles)
}

func TestPreserveStateForIssueUsesRemoteBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, t.TempDir(), "init", "--bare", remote)
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	baseSHA, err := Run(workspace, "rev-parse", "HEAD")
	require.NoError(t, err)
	runGit(t, workspace, "remote", "add", "origin", remote)
	runGit(t, workspace, "push", "-u", "origin", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, workspace, "add", "feature.txt")
	runGit(t, workspace, "commit", "-m", "feat: preserve state")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("base\nworktree\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "scratch.txt"), []byte("scratch\n"), 0o644))

	state, err := PreserveStateForIssue(workspace)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "origin/main", state.RemoteBase)
	require.Equal(t, baseSHA, state.RemoteBaseSHA)
	require.Equal(t, "main", state.BranchName)
	require.Contains(t, state.Patch, "+feature")
	require.Contains(t, state.Patch, "+worktree")
	require.Contains(t, state.FormatPatch, "feat: preserve state")
	require.Equal(t, []PreservedUntrackedFile{{Path: "scratch.txt", Content: "scratch\n"}}, state.UntrackedFiles)
}

func TestIsNoGitRepoError(t *testing.T) {
	require.False(t, IsNoGitRepoError(nil))
	require.False(t, IsNoGitRepoError(errors.New("permission denied")))
	require.True(t, IsNoGitRepoError(errors.New("fatal: not a git repository (or any of the parent directories): .git")))
	require.True(t, IsNoGitRepoError(errors.New("warning: Not a git repository. Use --no-index to compare two paths outside a working tree")))
}

func TestStashWorkflows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	path := filepath.Join(workspace, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add notes")

	require.NoError(t, os.WriteFile(path, []byte("changed\n"), 0o644))
	output, err := StashPush(workspace, StashPushOptions{Message: "wip notes"})
	require.NoError(t, err)
	require.Contains(t, output, "Saved working directory")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(data))

	list, err := StashList(workspace)
	require.NoError(t, err)
	require.Contains(t, list, "wip notes")

	output, err = StashApply(workspace, "stash@{0}")
	require.NoError(t, err)
	require.Contains(t, output, "modified:")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "changed\n", string(data))

	runGit(t, workspace, "checkout", "--", "notes.txt")
	output, err = StashPop(workspace, "stash@{0}")
	require.NoError(t, err)
	require.Contains(t, output, "Dropped")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "changed\n", string(data))
}

func TestBranchWorkflows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add notes")

	list, err := ListBranches(workspace)
	require.NoError(t, err)
	require.NotEmpty(t, list.Current)
	require.NotEmpty(t, list.Branches)

	output, err := CreateBranch(workspace, "feature/test", "", false)
	require.NoError(t, err)
	require.Empty(t, output)
	list, err = ListBranches(workspace)
	require.NoError(t, err)
	require.Contains(t, branchNames(list.Branches), "feature/test")

	_, err = SwitchBranch(workspace, "feature/test")
	require.NoError(t, err)
	current, err := Branch(workspace)
	require.NoError(t, err)
	require.Equal(t, "feature/test", current)

	_, err = RenameBranch(workspace, "", "feature/renamed")
	require.NoError(t, err)
	current, err = Branch(workspace)
	require.NoError(t, err)
	require.Equal(t, "feature/renamed", current)

	_, err = SwitchBranch(workspace, list.Current)
	require.NoError(t, err)
	output, err = DeleteBranch(workspace, "feature/renamed", false)
	require.NoError(t, err)
	require.Contains(t, output, "Deleted branch")
}

func TestCheckBranchFreshness(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	runGit(t, workspace, "switch", "-c", "topic")

	freshness, err := CheckBranchFreshness(workspace, "topic", "main")
	require.NoError(t, err)
	require.Equal(t, "fresh", freshness.Status)
	require.True(t, freshness.Fresh)
	require.False(t, freshness.HasUpstream)
	require.Empty(t, freshness.Upstream)
	require.Zero(t, freshness.Ahead)
	require.Zero(t, freshness.Behind)
	require.False(t, freshness.VerificationBlocked)
	require.Nil(t, freshness.Event)

	runGit(t, workspace, "branch", "--set-upstream-to", "main", "topic")
	freshness, err = CheckBranchFreshness(workspace, "topic", "main")
	require.NoError(t, err)
	require.True(t, freshness.HasUpstream)
	require.Equal(t, "main", freshness.Upstream)

	runGit(t, workspace, "switch", "main")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fix.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: resolve timeout")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "docs.txt"), []byte("docs\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "docs: update notes")

	freshness, err = CheckBranchFreshness(workspace, "topic", "main")
	require.NoError(t, err)
	require.Equal(t, "stale", freshness.Status)
	require.False(t, freshness.Fresh)
	require.Zero(t, freshness.Ahead)
	require.Equal(t, 2, freshness.Behind)
	require.ElementsMatch(t, []string{"fix: resolve timeout", "docs: update notes"}, freshness.MissingFixes)
	require.True(t, freshness.VerificationBlocked)
	require.Equal(t, "stale_branch", freshness.RecoveryScenario)
	require.Equal(t, "merge_forward_before_broad_verification", freshness.SuggestedAction)
	require.Equal(t, []string{"git switch topic", "git merge --ff-only main", "go test ./..."}, freshness.SuggestedCommands)
	require.NotNil(t, freshness.Event)
	require.Equal(t, "branch.stale_against_main", freshness.Event.LaneEvent)
	require.Equal(t, "stale_branch", freshness.Event.Classification)
	require.Equal(t, "healthcheck", freshness.Event.Provenance.Source)
	require.Equal(t, "codog-git", freshness.Event.Provenance.Emitter)
	require.Equal(t, "act", freshness.Event.Binding.WatcherAction)
	require.Equal(t, 2, freshness.Event.Evidence["behind"])

	runGit(t, workspace, "switch", "topic")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "topic.txt"), []byte("topic\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: topic work")

	freshness, err = CheckBranchFreshness(workspace, "topic", "main")
	require.NoError(t, err)
	require.Equal(t, "diverged", freshness.Status)
	require.False(t, freshness.Fresh)
	require.Equal(t, 1, freshness.Ahead)
	require.Equal(t, 2, freshness.Behind)
	require.ElementsMatch(t, []string{"fix: resolve timeout", "docs: update notes"}, freshness.MissingFixes)
	require.True(t, freshness.VerificationBlocked)
	require.Equal(t, []string{"git switch topic", "git rebase main", "go test ./..."}, freshness.SuggestedCommands)
}

func TestCheckBaseCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init", "-b", "main")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: base")
	baseSHA, err := Run(workspace, "rev-parse", "HEAD")
	require.NoError(t, err)

	check, err := CheckBaseCommitForWorkspace(workspace, baseSHA)
	require.NoError(t, err)
	require.Equal(t, "matches", check.Status)
	require.True(t, check.Matches)
	require.Equal(t, "flag", check.Source.Kind)
	require.Equal(t, baseSHA, check.Expected)
	require.Equal(t, baseSHA, check.Actual)
	require.Empty(t, check.Warning)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "next.txt"), []byte("next\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: next")
	nextSHA, err := Run(workspace, "rev-parse", "HEAD")
	require.NoError(t, err)

	check, err = CheckBaseCommitForWorkspace(workspace, baseSHA)
	require.NoError(t, err)
	require.Equal(t, "diverged", check.Status)
	require.False(t, check.Matches)
	require.Equal(t, baseSHA, check.Expected)
	require.Equal(t, nextSHA, check.Actual)
	require.Contains(t, check.Warning, "stale codebase")
}

func TestResolveExpectedBasePrefersFlagThenCodogThenClaw(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog-base"), []byte("codog-base\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claw-base"), []byte("claw-base\n"), 0o644))

	source, err := ResolveExpectedBase(workspace, "flag-base")
	require.NoError(t, err)
	require.Equal(t, &BaseCommitSource{Kind: "flag", Value: "flag-base"}, source)

	source, err = ResolveExpectedBase(workspace, "")
	require.NoError(t, err)
	require.Equal(t, "codog_file", source.Kind)
	require.Equal(t, "codog-base", source.Value)
	require.Equal(t, filepath.Join(workspace, ".codog-base"), source.Path)

	require.NoError(t, os.Remove(filepath.Join(workspace, ".codog-base")))
	source, err = ResolveExpectedBase(workspace, "")
	require.NoError(t, err)
	require.Equal(t, "claw_file", source.Kind)
	require.Equal(t, "claw-base", source.Value)
	require.Equal(t, filepath.Join(workspace, ".claw-base"), source.Path)
}

func TestCheckBaseCommitNoExpectedBaseAndNotGitRepo(t *testing.T) {
	workspace := t.TempDir()

	check, err := CheckBaseCommitForWorkspace(workspace, "")
	require.NoError(t, err)
	require.Equal(t, "no_expected_base", check.Status)
	require.True(t, check.Matches)

	check = CheckBaseCommit(workspace, &BaseCommitSource{Kind: "flag", Value: "abc1234"})
	require.Equal(t, "not_git_repo", check.Status)
	require.False(t, check.Matches)
	require.Contains(t, check.Warning, "not inside a git repository")
}

func TestTagWorkflows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "add notes")

	output, err := CreateTag(workspace, "v0.1.0", "", "")
	require.NoError(t, err)
	require.Empty(t, output)
	output, err = CreateTag(workspace, "v0.2.0", "HEAD", "release v0.2.0")
	require.NoError(t, err)
	require.Empty(t, output)

	tags, err := ListTags(workspace, "v0.*", 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"v0.1.0", "v0.2.0"}, tagNames(tags))

	show, err := ShowTag(workspace, "v0.2.0")
	require.NoError(t, err)
	require.Contains(t, show, "release v0.2.0")

	output, err = DeleteTag(workspace, "v0.1.0")
	require.NoError(t, err)
	require.Contains(t, output, "Deleted tag")
}

func TestCommitRequiresStagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")

	_, err := Commit(workspace, CommitOptions{Message: "empty"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no staged changes")
}

func branchNames(branches []BranchInfo) []string {
	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		names = append(names, branch.Name)
	}
	return names
}

func tagNames(tags []TagInfo) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return names
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
