package templates

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFindAndRenderTemplates(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "templates"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "templates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "templates", "review.md"), []byte("Review {{target}} as {{role}}."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "templates", "review.md"), []byte("Workspace review {{.target}}."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "templates", "plan.md"), []byte("\n\nPlan {{topic}}"), 0o644))

	all, err := Load(configHome, workspace)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, "plan", all[0].Name)
	require.Equal(t, "Plan {{topic}}", all[0].Preview)

	found, err := Find(configHome, workspace, "review")
	require.NoError(t, err)
	require.Equal(t, "workspace", found.Source)
	require.Contains(t, found.Body, "Workspace review")

	rendered, err := Render(found, map[string]string{"target": "auth"})
	require.NoError(t, err)
	require.Equal(t, "Workspace review auth.", rendered.Rendered)
	require.Equal(t, "auth", rendered.Vars["target"])
}

func TestRenderReportsMissingVariables(t *testing.T) {
	rendered, err := Render(Template{Name: "fix", Body: "Fix {{target}} for {{owner}}."}, map[string]string{"target": "tests"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "owner")
	require.Equal(t, []string{"owner"}, rendered.Unresolved)
	require.Contains(t, rendered.Rendered, "{{owner}}")
}
