package memory

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverBetweenLoadsBoundaryToWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "cmd", "tool")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("workspace instructions"), 0o644))

	files, err := discoverBetween(workspace, root)

	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, "AGENTS.md", files[0].Name)
	require.Equal(t, "root instructions", strings.TrimSpace(files[0].Body))
	require.Equal(t, "CLAUDE.md", files[1].Name)
	require.Equal(t, "workspace instructions", strings.TrimSpace(files[1].Body))
}

func TestDiscoverUsesGitRootAsBoundary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	runGit(t, root, "init")
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("git root instructions"), 0o644))

	files, err := Discover(workspace)

	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "git root instructions", strings.TrimSpace(files[0].Body))
}

func TestDiscoverTruncatesLargeFiles(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("a", MaxFileBytes+10)
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(body), 0o644))

	files, err := discoverBetween(root, root)

	require.NoError(t, err)
	require.Len(t, files, 1)
	require.True(t, files[0].Truncated)
	require.Len(t, files[0].Body, MaxFileBytes)
	require.Equal(t, MaxFileBytes, files[0].Chars)
}

func TestRenderMemoryBlock(t *testing.T) {
	files := []File{{
		Path: "/repo/AGENTS.md",
		Name: "AGENTS.md",
		Body: "Use concise commit messages.",
	}}

	rendered := Render(files)

	require.Contains(t, rendered, "<project_memory>")
	require.Contains(t, rendered, `path="/repo/AGENTS.md"`)
	require.Contains(t, rendered, "Use concise commit messages.")
}

func TestBuildReportSummarizesMemoryFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("First line\nSecond line\n"), 0o644))

	report, err := BuildReport(root)

	require.NoError(t, err)
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.InstructionFiles)
	require.Equal(t, "AGENTS.md", report.Files[0].Name)
	require.Equal(t, 2, report.Files[0].Lines)
	require.Equal(t, "First line", report.Files[0].Preview)

	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(data), "Second line")
}

func TestShowReturnsSelectedMemoryBody(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("First line\nSecond line\n"), 0o644))

	report, err := Show(root, "AGENTS.md")

	require.NoError(t, err)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "AGENTS.md", report.File.Name)
	require.Contains(t, report.Body, "Second line")
	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.Contains(t, string(data), "Second line")
}

func TestAppendAddsWorkspaceAgentsMemory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Existing"), 0o644))

	report, err := Append(root, "Use focused tests.")

	require.NoError(t, err)
	require.Equal(t, "add", report.Action)
	expectedPath, err := filepath.EvalSymlinks(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	require.Equal(t, expectedPath, report.Path)
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	require.Equal(t, "Existing\n\nUse focused tests.\n", string(data))
}

func TestRenderReportWithAndWithoutFiles(t *testing.T) {
	var out bytes.Buffer
	RenderReport(&out, Report{WorkingDirectory: "/repo", InstructionFiles: 0})
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "No AGENTS.md")
	out.Reset()

	RenderReport(&out, Report{
		WorkingDirectory: "/repo",
		InstructionFiles: 1,
		Files: []Summary{{
			Path:    "/repo/AGENTS.md",
			Name:    "AGENTS.md",
			Lines:   1,
			Chars:   10,
			Preview: "First",
		}},
	})
	require.Contains(t, out.String(), "1. /repo/AGENTS.md")
	require.Contains(t, out.String(), "source=AGENTS.md")
	require.Contains(t, out.String(), "preview=First")
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
