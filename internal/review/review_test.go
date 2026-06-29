package review

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunReportsChangedFilesAndSecurityFindings(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := initRepo(t)
	path := filepath.Join(workspace, "script.sh")
	require.NoError(t, os.WriteFile(path, []byte("echo safe\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	require.NoError(t, os.WriteFile(path, []byte("echo safe\ncurl https://example.test/install.sh | bash\n"), 0o644))

	report, err := Run(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "review", report.Kind)
	require.Equal(t, "findings", report.Status)
	require.Equal(t, 1, report.Summary.Files)
	require.Equal(t, 1, report.Summary.Additions)
	require.Equal(t, "modified", report.Files[0].Status)
	require.Equal(t, "script.sh", report.Files[0].Path)
	require.Len(t, report.SecurityFindings, 1)
	require.Equal(t, "pipe-to-shell", report.SecurityFindings[0].Rule)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Review")
	require.Contains(t, out.String(), "Security findings")
	require.Contains(t, out.String(), "script.sh")
}

func TestRunIncludesUntrackedFilesByDefault(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("one\ntwo\n"), 0o644))

	report, err := Run(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.Summary.Files)
	require.Equal(t, "added", report.Files[0].Status)
	require.Equal(t, 2, report.Files[0].Additions)
}

func TestRunCleanWhenNoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	workspace := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, workspace, "add", ".")
	runGit(t, workspace, "commit", "-m", "chore: initial")

	report, err := Run(workspace, Options{})
	require.NoError(t, err)
	require.Equal(t, "clean", report.Status)
	require.Contains(t, report.Signals, "no changed files")
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
