package visualization

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagerMaterializeAndRewrite(t *testing.T) {
	manager := testManager(t)
	require.NoError(t, os.MkdirAll(manager.SourceDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(manager.SourceDir(), "chart.html"), []byte(`<button onclick="this.textContent='ok'">Run</button>`), 0o644))

	item, err := manager.Materialize("chart.html", "Build [times]")
	require.NoError(t, err)
	require.Equal(t, "chart.html", item.File)
	require.Equal(t, "Build [times]", item.Title)
	require.Contains(t, item.URL, "file://")
	viewer, err := os.ReadFile(item.ViewerPath)
	require.NoError(t, err)
	require.True(t, viewerContainsSandbox(viewer))
	require.Contains(t, string(viewer), `sandbox="allow-scripts"`)
	require.NotContains(t, string(viewer), "allow-same-origin")
	require.Contains(t, string(viewer), "form-action &#39;none&#39;")

	input := "Result\n\n::codog-inline-vis{\"file\":\"chart.html\",\"title\":\"Build [times]\"}\n"
	output, changed := manager.RewriteMarkdown(input)
	require.True(t, changed)
	require.Contains(t, output, `[Open visualization: Build \[times\]](file://`)
	require.NotContains(t, output, "::codog-inline-vis")
	require.True(t, strings.HasSuffix(output, "\n"))
}

func TestManagerRewritePreservesCodeFencesAndRejectsInvalidDirectives(t *testing.T) {
	manager := testManager(t)
	input := "```text\n::codog-inline-vis{\"file\":\"chart.html\"}\n```\n" +
		"::codog-inline-vis{\"file\":\"../secret.html\"}\n" +
		"::codog-inline-vis{\"file\":\"chart.html\",\"extra\":true}"

	output, changed := manager.RewriteMarkdown(input)
	require.True(t, changed)
	require.Contains(t, output, "```text\n::codog-inline-vis")
	require.Contains(t, output, "Visualization unavailable: invalid file name")
	require.Contains(t, output, "Visualization unavailable: invalid directive")
}

func TestManagerRejectsUnsafeAndOversizedSources(t *testing.T) {
	manager := testManager(t)
	require.NoError(t, os.MkdirAll(manager.SourceDir(), 0o755))
	outside := filepath.Join(t.TempDir(), "outside.html")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(manager.SourceDir(), "link.html")))

	_, err := manager.Materialize("link.html", "")
	require.ErrorIs(t, err, ErrUnsafeSource)
	require.NoError(t, os.WriteFile(filepath.Join(manager.SourceDir(), "large.html"), make([]byte, maxSourceBytes+1), 0o644))
	_, err = manager.Materialize("large.html", "")
	require.ErrorIs(t, err, ErrSourceTooLarge)
}

func TestManagerRejectsSourceDirectoryOutsideWorkspace(t *testing.T) {
	manager := testManager(t)
	outside := filepath.Join(t.TempDir(), "visualizations")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(manager.Workspace, ".codog"), 0o755))
	require.NoError(t, os.Symlink(outside, manager.SourceDir()))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.html"), []byte("secret"), 0o644))

	_, err := manager.Materialize("outside.html", "")
	require.ErrorIs(t, err, ErrUnsafeSource)
}

func TestManagerListIsSortedAndSkipsUnsafeFiles(t *testing.T) {
	manager := testManager(t)
	require.NoError(t, os.MkdirAll(manager.SourceDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(manager.SourceDir(), "z.html"), []byte("z"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(manager.SourceDir(), "a.HTML"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(manager.SourceDir(), "note.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(manager.SourceDir(), "z.html"), filepath.Join(manager.SourceDir(), "link.html")))

	items, err := manager.List()
	require.NoError(t, err)
	require.Equal(t, []string{"a.HTML", "z.html"}, []string{items[0].File, items[1].File})
}

func TestManagerMissingDirectoryAndUnchangedMarkdown(t *testing.T) {
	manager := testManager(t)
	items, err := manager.List()
	require.NoError(t, err)
	require.Empty(t, items)

	output, changed := manager.RewriteMarkdown("ordinary response")
	require.False(t, changed)
	require.Equal(t, "ordinary response", output)
}

func testManager(t *testing.T) Manager {
	t.Helper()
	return Manager{Workspace: t.TempDir(), ConfigHome: t.TempDir()}
}
