package releasenotes

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateFromLatestTagAndRenderMarkdown(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	runGit(t, workspace, "tag", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "feat: add feature")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "bug.txt"), []byte("fix\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "fix: patch bug")

	report, err := Generate(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "release_notes", report.Kind)
	require.Equal(t, "v0.1.0", report.From)
	require.Equal(t, 2, report.Total)
	require.Equal(t, "Features", report.Sections[0].Name)
	require.Equal(t, "Fixes", report.Sections[1].Name)

	var out bytes.Buffer
	RenderMarkdown(&out, report)
	require.Contains(t, out.String(), "# Release Notes")
	require.Contains(t, out.String(), "Range: `v0.1.0..")
	require.Contains(t, out.String(), "## Features")
	require.Contains(t, out.String(), "feat: add feature")
	require.Contains(t, out.String(), "feature.txt")
}

func TestGenerateUsesLimitWhenNoTagExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "one.txt"), []byte("one\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "docs: one")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "two.txt"), []byte("two\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "test: two")

	report, err := Generate(workspace, Options{Limit: 1})
	require.NoError(t, err)
	require.Empty(t, report.From)
	require.Equal(t, 1, report.Total)
	require.Equal(t, "Tests", report.Sections[0].Name)
}

func initRepo(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	return workspace
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
