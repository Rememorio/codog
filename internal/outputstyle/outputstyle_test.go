package outputstyle

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListSetShowClearAndRenderPrompt(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "output-styles"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "output-styles"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "output-styles", "calm.md"), []byte("Use calm prose.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "output-styles", "calm.md"), []byte("Use workspace calm prose.\n"), 0o644))

	report, err := List(configHome, workspace)
	require.NoError(t, err)
	require.Equal(t, "list", report.Action)
	require.Empty(t, report.Active)
	require.NotNil(t, report.Summary)
	require.Equal(t, 5, report.Summary.Total)
	require.Equal(t, 4, report.Summary.Effective)
	require.Equal(t, 1, report.Summary.Shadowed)
	requireStyle(t, report.Styles, "concise", "builtin")
	workspaceCalm := styleByNameAndSource(report.Styles, "calm", "workspace")
	require.True(t, workspaceCalm.Effective)
	userCalm := styleByNameAndSource(report.Styles, "calm", "user")
	require.False(t, userCalm.Effective)
	require.Equal(t, "workspace", userCalm.ShadowedBy)

	report, err = Set(configHome, workspace, "calm")
	require.NoError(t, err)
	require.Equal(t, "set", report.Action)
	require.Equal(t, "calm", report.Active)
	require.NotNil(t, report.Style)
	require.Equal(t, "workspace", report.Style.Source)
	require.FileExists(t, StatePath(workspace))

	prompt := RenderPrompt(configHome, workspace)
	require.Contains(t, prompt, `<output_style name="calm" source="workspace">`)
	require.Contains(t, prompt, "Use workspace calm prose.")

	report, err = Show(configHome, workspace, "calm")
	require.NoError(t, err)
	require.Equal(t, "show", report.Action)
	require.Equal(t, "workspace", report.Style.Source)

	var out bytes.Buffer
	RenderText(&out, report)
	require.Contains(t, out.String(), "Output Style")
	require.Contains(t, out.String(), "Use workspace calm prose.")

	report, err = Clear(workspace)
	require.NoError(t, err)
	require.Equal(t, "clear", report.Action)
	require.NoFileExists(t, StatePath(workspace))
	require.Empty(t, RenderPrompt(configHome, workspace))

	report, err = Search(configHome, workspace, "workspace calm")
	require.NoError(t, err)
	require.Equal(t, "search", report.Action)
	require.Equal(t, "workspace calm", report.Query)
	require.Len(t, report.Styles, 1)
	require.Equal(t, "workspace", report.Styles[0].Source)

	report, err = Audit(configHome, workspace)
	require.NoError(t, err)
	require.Equal(t, "audit", report.Action)
	require.Equal(t, "ok", report.Status)
	require.NotNil(t, report.Summary)
	require.Equal(t, 1, report.Summary.Shadowed)
	require.NotEmpty(t, report.Sources)
	require.Equal(t, len(report.Sources), report.SourceCount)
	require.Contains(t, report.Message, "passed")

	sources := Sources(configHome, workspace)
	requireOutputStyleSource(t, sources, "workspace", filepath.Join(workspace, ".codog", "output-styles"), true)
	requireOutputStyleSource(t, sources, "user", filepath.Join(configHome, "output-styles"), true)
	requireOutputStyleSource(t, sources, "builtin", "", true)
}

func TestFindRejectsInvalidStyleName(t *testing.T) {
	_, err := Find(t.TempDir(), t.TempDir(), "../secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid output style name")
}

func requireStyle(t *testing.T, styles []StyleSummary, name string, source string) {
	t.Helper()
	require.NotEmpty(t, styleByNameAndSource(styles, name, source), "missing style %s from %s in %#v", name, source, styles)
}

func styleByNameAndSource(styles []StyleSummary, name string, source string) StyleSummary {
	for _, style := range styles {
		if style.Name == name && style.Source == source {
			return style
		}
	}
	return StyleSummary{}
}

func requireOutputStyleSource(t *testing.T, roots []DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "output style source root not found", "source=%s path=%s roots=%v", source, path, roots)
}
