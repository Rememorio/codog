package onboarding

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnalyzeReadyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main_test.go"), []byte("package demo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Use tests.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"permission_mode":"workspace-write"}`), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0o755))

	report, err := Analyze(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "onboarding", report.Kind)
	require.Equal(t, "inspect", report.Action)
	require.Equal(t, "ready", report.Status)
	require.True(t, report.HasReadme)
	require.True(t, report.HasTests)
	require.True(t, report.GitRepository)
	require.Equal(t, "Go", report.PrimaryLanguage)
	require.False(t, report.PythonFirst)
	require.Contains(t, report.ReadmeFiles, "README.md")
	require.Contains(t, report.TestFiles, "main_test.go")
	require.Contains(t, report.InstructionFiles, "AGENTS.md")
	require.Contains(t, report.ConfigFiles, ".codog.json")
	require.Empty(t, report.Recommendations)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Onboarding")
	require.Contains(t, out.String(), "Primary language Go")

	out.Reset()
	require.NoError(t, RenderJSON(&out, report))
	require.Contains(t, out.String(), `"kind": "onboarding"`)
}

func TestAnalyzeNeedsSetup(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app.py"), []byte("print('hi')\n"), 0o644))

	report, err := Analyze(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "needs_setup", report.Status)
	require.False(t, report.HasReadme)
	require.False(t, report.HasTests)
	require.True(t, report.PythonFirst)
	require.Equal(t, "Python", report.PrimaryLanguage)
	require.Contains(t, report.Recommendations, "add a README that explains setup and verification")
	require.Contains(t, report.Recommendations, "add or document a repeatable test command")
	require.Contains(t, report.Recommendations, "run `codog init` or add AGENTS.md/.codog/instructions.md")
}
