package versioninfo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildWithMetadataRendersVersionReport(t *testing.T) {
	report := BuildWithMetadata("1.2.3", "", Metadata{
		GitSHA:    "1234567890abcdef",
		GitBranch: "main",
		GitDirty:  "false",
		BuildDate: "2026-06-29",
	})

	require.Equal(t, "version", report.Kind)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "1.2.3", report.Version)
	require.Equal(t, "1234567890ab", report.GitSHAShort)
	require.Equal(t, "main", report.GitBranch)
	require.NotEmpty(t, report.BuildTarget)
	require.Contains(t, report.HumanReadable, "Codog")
	require.Contains(t, report.HumanReadable, "Version          1.2.3")
	require.Contains(t, report.HumanReadable, "Git SHA          1234567890ab")

	var out bytes.Buffer
	RenderText(&out, report)
	require.Equal(t, report.HumanReadable, out.String())
}

func TestBuildWithMetadataWarnsWhenGitSHAUnknown(t *testing.T) {
	report := BuildWithMetadata("1.2.3", "", Metadata{})

	require.Empty(t, report.GitSHA)
	require.Contains(t, report.Hint, "Build metadata")
	require.Contains(t, report.HumanReadable, "Git SHA          unknown")
}

func TestBuildDetectsWorkspaceMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := t.TempDir()
	runGit(t, workspace, "init")
	runGit(t, workspace, "config", "user.email", "codog@example.test")
	runGit(t, workspace, "config", "user.name", "Codog Test")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("test\n"), 0o644))
	runGit(t, workspace, "add", "README.md")
	runGit(t, workspace, "commit", "-m", "initial")
	sha := strings.TrimSpace(runGitOutput(t, workspace, "rev-parse", "HEAD"))

	report := BuildWithMetadata("1.2.3", workspace, Metadata{GitSHA: sha})

	require.Equal(t, sha, report.WorkspaceGitSHA)
	require.NotNil(t, report.WorkspaceMatch)
	require.True(t, *report.WorkspaceMatch)
	require.Contains(t, report.HumanReadable, "Workspace SHA")
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, workspace, args...)
}

func runGitOutput(t *testing.T, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
	return string(data)
}
