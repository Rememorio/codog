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
	requireStyle(t, report.Styles, "concise", "builtin")
	requireStyle(t, report.Styles, "calm", "workspace")
	requireStyle(t, report.Styles, "calm", "user")

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
}

func TestFindRejectsInvalidStyleName(t *testing.T) {
	_, err := Find(t.TempDir(), t.TempDir(), "../secret")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid output style name")
}

func requireStyle(t *testing.T, styles []StyleSummary, name string, source string) {
	t.Helper()
	for _, style := range styles {
		if style.Name == name && style.Source == source {
			return
		}
	}
	require.Failf(t, "missing style", "expected %s from %s in %#v", name, source, styles)
}
