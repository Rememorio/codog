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
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "CLAUDE.md"), []byte("claude scoped instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("workspace instructions"), 0o644))

	files, err := discoverBetween(workspace, root)

	require.NoError(t, err)
	require.Len(t, files, 3)
	require.Equal(t, "AGENTS.md", files[0].Name)
	require.Equal(t, "root instructions", strings.TrimSpace(files[0].Body))
	require.Equal(t, ".claude/CLAUDE.md", files[1].Name)
	require.Equal(t, "claude scoped instructions", strings.TrimSpace(files[1].Body))
	require.Equal(t, "CLAUDE.md", files[2].Name)
	require.Equal(t, "workspace instructions", strings.TrimSpace(files[2].Body))
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

func TestSearchFindsRelevantMemoryLines(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use focused tests.\nKeep docs concise.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codog", "instructions.md"), []byte("Avoid broad rewrites unless asked.\n"), 0o644))

	report, err := Search(root, "focused tests", 10)

	require.NoError(t, err)
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, "search", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, "focused tests", report.Query)
	require.Equal(t, 1, report.MatchCount)
	require.Len(t, report.Matches, 1)
	require.Equal(t, "AGENTS.md", report.Matches[0].Name)
	require.Equal(t, 1, report.Matches[0].LineNumber)
	require.Equal(t, "Use focused tests.", report.Matches[0].Line)
	require.Contains(t, report.Matches[0].MatchedTerms, "focused tests")

	report, err = Search(root, "docs rewrites", 1)
	require.NoError(t, err)
	require.Equal(t, 2, report.MatchCount)
	require.Len(t, report.Matches, 1)

	_, err = Search(root, " ", 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "query is required")
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

func TestPathEnsureAndEditMemoryFile(t *testing.T) {
	root := t.TempDir()
	expectedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	pathReport, err := Path(root, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(expectedRoot, "AGENTS.md"), pathReport.Path)

	ensureReport, err := Ensure(root, ".codog/instructions.md")
	require.NoError(t, err)
	require.Equal(t, "created", ensureReport.Status)
	require.True(t, ensureReport.Created)
	_, err = os.Stat(filepath.Join(root, ".codog", "instructions.md"))
	require.NoError(t, err)

	editReport, err := Edit(root, "AGENTS.md", "", false)
	require.NoError(t, err)
	require.Equal(t, "edit", editReport.Action)
	require.True(t, editReport.Created)
	require.Contains(t, editReport.Message, "skipped")

	_, err = ResolvePath(root, "../outside.md")
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")
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
	out.Reset()

	RenderSearchReport(&out, SearchReport{
		WorkingDirectory: "/repo",
		Query:            "tests",
		MatchCount:       1,
		Matches: []SearchMatch{{
			Path:         "/repo/AGENTS.md",
			Name:         "AGENTS.md",
			LineNumber:   3,
			Line:         "Use focused tests.",
			Score:        11,
			MatchedTerms: []string{"tests"},
		}},
	})
	require.Contains(t, out.String(), "Memory Search")
	require.Contains(t, out.String(), "/repo/AGENTS.md:3")
	require.Contains(t, out.String(), "Use focused tests.")
}

func runGit(t *testing.T, workspace string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
}
