package memory

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDiscoverSupportsExpandedMemoryNames(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claw"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codog"), 0o755))
	for _, name := range []string{
		"AGENTS.local.md",
		"CLAUDE.local.md",
		"CLAW.local.md",
		filepath.Join(".claw", "CLAUDE.md"),
		filepath.Join(".claw", "instructions.md"),
		filepath.Join(".codog", "instructions.md"),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(filepath.ToSlash(name)+" body"), 0o644))
	}

	files, err := discoverBetween(root, root)

	require.NoError(t, err)
	require.Len(t, files, 6)
	require.Equal(t, []string{
		"AGENTS.local.md",
		"CLAUDE.local.md",
		"CLAW.local.md",
		".claw/CLAUDE.md",
		".claw/instructions.md",
		".codog/instructions.md",
	}, memoryNames(files))
	require.Contains(t, strings.TrimSpace(files[3].Body), ".claw/CLAUDE.md")
}

func TestDiscoverMatchesMemoryNamesCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ClAuDe"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents.md"), []byte("lowercase agents"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ClAuDe", "claude.md"), []byte("mixed claude"), 0o644))

	files, err := discoverBetween(root, root)

	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, "AGENTS.md", files[0].Name)
	require.Equal(t, "lowercase agents", strings.TrimSpace(files[0].Body))
	require.Equal(t, ".claude/CLAUDE.md", files[1].Name)
	require.Equal(t, "mixed claude", strings.TrimSpace(files[1].Body))
	require.True(t, strings.EqualFold(filepath.Join(root, ".ClAuDe", "claude.md"), files[1].Path), files[1].Path)
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

func memoryNames(files []File) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}
	return names
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
	path := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("First line\nSecond line\n"), 0o644))
	modifiedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, modifiedAt, modifiedAt))

	report, err := BuildReport(root)

	require.NoError(t, err)
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, "list", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.InstructionFiles)
	require.Equal(t, "AGENTS.md", report.Files[0].Name)
	require.Equal(t, 2, report.Files[0].Lines)
	require.Equal(t, 4, report.Files[0].Words)
	require.Equal(t, "First line", report.Files[0].Preview)
	require.Equal(t, int64(len("First line\nSecond line\n")), report.Files[0].SizeBytes)
	require.Equal(t, "2026-07-01T12:00:00Z", report.Files[0].ModifiedAt)
	require.False(t, report.Files[0].Empty)
	require.GreaterOrEqual(t, report.Files[0].AgeSeconds, int64(0))

	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(data), "Second line")
}

func TestSummariesAtReportsMemoryAgeAndEmptyState(t *testing.T) {
	modifiedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	now := modifiedAt.Add(90 * time.Second)
	summaries := SummariesAt([]File{{
		Path:       "/repo/AGENTS.md",
		Name:       "AGENTS.md",
		Scope:      "/repo",
		Chars:      0,
		SizeBytes:  0,
		ModifiedAt: modifiedAt,
		Body:       " \n",
	}}, now)

	require.Len(t, summaries, 1)
	require.Equal(t, int64(90), summaries[0].AgeSeconds)
	require.True(t, summaries[0].Empty)
	require.Equal(t, 1, summaries[0].Lines)
	require.Equal(t, 0, summaries[0].Words)
	require.Equal(t, "2026-07-01T12:00:00Z", summaries[0].ModifiedAt)
}

func TestMetadataForReportsClaudeCompatibleMemoryFields(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "cmd", "tool")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	canonicalRoot := canonicalPath(root)
	canonicalWorkspace := canonicalPath(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Project instructions.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "instructions.md"), []byte("Workspace instructions.\n"), 0o644))

	files, err := discoverBetween(workspace, root)
	require.NoError(t, err)

	metadata := MetadataFor(workspace, files)

	require.Len(t, metadata, 2)
	require.Equal(t, "AGENTS.md", metadata[0].Name)
	require.Equal(t, "agents_md", metadata[0].Source)
	require.Equal(t, "ancestor", metadata[0].Origin)
	require.Equal(t, canonicalRoot, metadata[0].ScopePath)
	require.True(t, metadata[0].OutsideProject)
	require.True(t, metadata[0].Contributes)
	require.Equal(t, 1, metadata[0].Lines)
	require.Equal(t, 2, metadata[0].Words)
	require.False(t, metadata[0].Empty)
	require.NotEmpty(t, metadata[0].ModifiedAt)
	require.Equal(t, ".codog/instructions.md", metadata[1].Name)
	require.Equal(t, "codog_instructions", metadata[1].Source)
	require.Equal(t, "workspace", metadata[1].Origin)
	require.Equal(t, canonicalWorkspace, metadata[1].ScopePath)
	require.False(t, metadata[1].OutsideProject)
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

func TestSelectPreviewsDefaultAndTargetWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Project instructions\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codog", "instructions.md"), []byte("Codog instructions\n"), 0o644))
	canonicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	report, err := Select(root, "")

	require.NoError(t, err)
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, "select", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, filepath.Join(canonicalRoot, "AGENTS.md"), report.Selected)
	require.Equal(t, 2, report.OptionCount)
	require.Len(t, report.Options, 2)
	require.True(t, report.Options[0].Selected)
	require.True(t, report.Options[0].Exists)
	require.Equal(t, "agents_md", report.Options[0].Source)

	targetReport, err := Select(root, "NEW.md")

	require.NoError(t, err)
	require.Equal(t, "NEW.md", targetReport.Target)
	require.Equal(t, filepath.Join(canonicalRoot, "NEW.md"), targetReport.Selected)
	require.Equal(t, 3, targetReport.OptionCount)
	require.Equal(t, "NEW.md", targetReport.Options[2].Name)
	require.False(t, targetReport.Options[2].Exists)
	require.True(t, targetReport.Options[2].Selected)
	require.NoFileExists(t, filepath.Join(root, "NEW.md"))

	var out bytes.Buffer
	RenderSelectionReport(&out, targetReport)
	require.Contains(t, out.String(), "Memory Selection")
	require.Contains(t, out.String(), "selected=true")
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

func TestResetClearsSelectedAndAllMemoryFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Project memory\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codog", "instructions.md"), []byte("Codog memory\n"), 0o644))

	_, err := Reset(root, ResetOptions{Target: "AGENTS.md"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "confirmation required")

	report, err := Reset(root, ResetOptions{Target: "AGENTS.md", Confirm: true})
	require.NoError(t, err)
	require.Equal(t, "memory", report.Kind)
	require.Equal(t, "reset", report.Action)
	require.Equal(t, "ok", report.Status)
	require.Equal(t, 1, report.ResetCount)
	require.Equal(t, "AGENTS.md", report.Files[0].Name)
	require.Greater(t, report.Files[0].BytesRemoved, 0)
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	require.Empty(t, data)
	data, err = os.ReadFile(filepath.Join(root, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Equal(t, "Codog memory\n", string(data))

	report, err = Reset(root, ResetOptions{All: true, Confirm: true})
	require.NoError(t, err)
	require.True(t, report.All)
	require.Equal(t, 2, report.ResetCount)
	data, err = os.ReadFile(filepath.Join(root, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestRenderReportWithAndWithoutFiles(t *testing.T) {
	var out bytes.Buffer
	RenderReport(&out, Report{WorkingDirectory: "/repo", InstructionFiles: 0})
	require.Contains(t, out.String(), "Memory")
	require.Contains(t, out.String(), "No project memory files discovered")
	require.Contains(t, out.String(), ".claw/instructions.md")
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
