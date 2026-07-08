package workspaceops

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspacePathResolutionAndHelpers(t *testing.T) {
	workspace := t.TempDir()
	service := Service{Workspace: workspace}
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "dir", "file.txt"), []byte("alpha\nbeta\n"), 0o644))

	root, rel, err := service.ResolveWorkspacePath("")
	require.NoError(t, err)
	require.Equal(t, ".", rel)
	require.Equal(t, mustEvalSymlinks(t, workspace), root)

	dir, rel, err := service.ResolveWorkspacePath("dir")
	require.NoError(t, err)
	require.Equal(t, "dir", rel)
	require.Equal(t, filepath.Join(mustEvalSymlinks(t, workspace), "dir"), dir)

	resolved, rel, err := service.Resolve("dir/file.txt", false)
	require.NoError(t, err)
	require.Equal(t, "dir/file.txt", rel)
	require.Equal(t, filepath.Join(mustEvalSymlinks(t, workspace), "dir", "file.txt"), resolved)

	rel, err = service.Rel(filepath.Join(mustEvalSymlinks(t, workspace), "dir", "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "dir/file.txt", rel)

	outside := filepath.Join(filepath.Dir(workspace), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))
	_, _, err = service.Resolve("../outside.txt", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "path escapes workspace")

	require.Equal(t, 25, BoundedLimit(0, 25, 100))
	require.Equal(t, 25, BoundedLimit(-1, 25, 100))
	require.Equal(t, 75, BoundedLimit(75, 25, 100))
	require.Equal(t, 100, BoundedLimit(150, 25, 100))

	matched, err := PatternMatch("*.txt", "dir/file.txt", "file.txt")
	require.NoError(t, err)
	require.True(t, matched)
	matched, err = PatternMatch("dir/*.txt", "dir/file.txt", "file.txt")
	require.NoError(t, err)
	require.True(t, matched)
	matched, err = PatternMatch("*.md", "dir/file.txt", "file.txt")
	require.NoError(t, err)
	require.False(t, matched)

	diff := UnifiedDiff("dir/file.txt", "old\n", "new\n")
	require.Contains(t, diff, "--- a/dir/file.txt")
	require.Contains(t, diff, "+++ b/dir/file.txt")
	require.Contains(t, diff, "-old")
	require.Contains(t, diff, "+new")
	require.Empty(t, UnifiedDiff("same.txt", "same\n", "same\n"))
}

func TestWorkspaceOperationsRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	service := Service{Workspace: workspace}

	write, err := service.Write(WriteOptions{
		Path:    "src/app.go",
		Content: "package main\n\nfunc main() {\n\tprintln(\"needle one\")\n\tprintln(\"needle two\")\n}\n",
	})
	require.NoError(t, err)
	require.Equal(t, "src/app.go", write.Path)
	require.Positive(t, write.Bytes)
	_, err = service.Write(WriteOptions{Path: ".secret", Content: "needle hidden\n"})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".git", "config"), []byte("needle ignored\n"), 0o644))

	info, err := service.Info()
	require.NoError(t, err)
	require.Equal(t, mustAbs(t, workspace), info.Path)
	require.Equal(t, filepath.Base(workspace), info.Name)

	read, err := service.Read(ReadOptions{Path: "src/app.go", Offset: 14, Limit: 4})
	require.NoError(t, err)
	require.Equal(t, "src/app.go", read.Path)
	require.Equal(t, "func", read.Content)
	require.True(t, read.Truncated)

	files, err := service.Files(FilesOptions{Pattern: "*.go"})
	require.NoError(t, err)
	require.False(t, files.Truncated)
	require.Equal(t, ".", files.Root)
	require.Equal(t, []string{"src/app.go"}, fileEntryPaths(files.Files))

	files, err = service.Files(FilesOptions{IncludeHidden: true, Pattern: "*"})
	require.NoError(t, err)
	paths := fileEntryPaths(files.Files)
	require.Contains(t, paths, ".secret")
	require.NotContains(t, paths, ".git/config")

	search, err := service.Search(SearchOptions{Query: "needle", Glob: "src/*.go", Limit: 1})
	require.NoError(t, err)
	require.True(t, search.Truncated)
	require.Len(t, search.Matches, 1)
	require.Equal(t, "src/app.go", search.Matches[0].Path)
	require.Equal(t, 4, search.Matches[0].Line)
	require.Positive(t, search.Matches[0].Column)

	search, err = service.Search(SearchOptions{Query: `needle\s+two`, Regex: true, Glob: "src/*.go"})
	require.NoError(t, err)
	require.Len(t, search.Matches, 1)
	require.Contains(t, search.Matches[0].Text, "needle two")

	_, err = service.Edit(EditOptions{Path: "src/app.go", OldString: "needle", NewString: "thread"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "replace_all")

	edit, err := service.Edit(EditOptions{Path: "src/app.go", OldString: "needle", NewString: "thread", ReplaceAll: true})
	require.NoError(t, err)
	require.Equal(t, "src/app.go", edit.Path)
	require.Equal(t, 2, edit.Replacements)

	diff, err := service.Diff(DiffOptions{Path: "src/app.go", OldString: "thread one", NewString: "fiber one"})
	require.NoError(t, err)
	require.Equal(t, "src/app.go", diff.Path)
	require.Contains(t, diff.Diff, "-\tprintln(\"thread one\")")
	require.Contains(t, diff.Diff, "+\tprintln(\"fiber one\")")
}

func TestFileOperationsEnforceSizeLimits(t *testing.T) {
	workspace := t.TempDir()
	service := Service{Workspace: workspace}
	largeContent := strings.Repeat("a", int(MaxFileBytes)+1)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(largeContent), 0o644))

	read, err := service.Read(ReadOptions{Path: "large.txt", Limit: int(MaxFileBytes) + 100})
	require.NoError(t, err)
	require.Equal(t, int(MaxFileBytes)+1, read.Bytes)
	require.True(t, read.Truncated)
	require.Len(t, read.Content, int(MaxFileBytes))

	_, err = service.Write(WriteOptions{Path: "too-large.txt", Content: largeContent})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum workspace file size")

	_, err = service.Edit(EditOptions{Path: "large.txt", OldString: "a", NewString: "b"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")

	_, err = service.Diff(DiffOptions{Path: "large.txt", OldString: "a", NewString: "b"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum editable size")
}

func fileEntryPaths(entries []FileEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	return paths
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	return abs
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}
