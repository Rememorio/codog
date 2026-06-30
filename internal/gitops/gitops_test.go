package gitops

import (
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

	status, err = Status(workspace)
	require.NoError(t, err)
	require.True(t, strings.Contains(status, "## main") || strings.Contains(status, "## master"))
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
	require.Zero(t, freshness.Ahead)
	require.Zero(t, freshness.Behind)

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
