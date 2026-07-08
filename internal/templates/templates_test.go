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
	workspaceReview := templateByNameAndSource(all, "review", "workspace")
	require.True(t, workspaceReview.Active)
	userReview := templateByNameAndSource(all, "review", "user")
	require.False(t, userReview.Active)
	require.Equal(t, "workspace", userReview.ShadowedBy)
	require.Equal(t, workspaceReview.Path, userReview.ShadowedByPath)

	found, err := Find(configHome, workspace, "review")
	require.NoError(t, err)
	require.Equal(t, "workspace", found.Source)
	require.Contains(t, found.Body, "Workspace review")

	rendered, err := Render(found, map[string]string{"target": "auth"})
	require.NoError(t, err)
	require.Equal(t, "Workspace review auth.", rendered.Rendered)
	require.Equal(t, "auth", rendered.Vars["target"])

	sources := Sources(configHome, workspace)
	requireTemplateSource(t, sources, "workspace", filepath.Join(workspace, ".codog", "templates"), true)
	requireTemplateSource(t, sources, "user", filepath.Join(configHome, "templates"), true)
}

func TestRenderReportsMissingVariables(t *testing.T) {
	rendered, err := Render(Template{Name: "fix", Body: "Fix {{target}} for {{owner}}."}, map[string]string{"target": "tests"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "owner")
	require.Equal(t, []string{"owner"}, rendered.Unresolved)
	require.Contains(t, rendered.Rendered, "{{owner}}")
}

func templateByNameAndSource(all []Template, name string, source string) Template {
	for _, tmpl := range all {
		if tmpl.Name == name && tmpl.Source == source {
			return tmpl
		}
	}
	return Template{}
}

func requireTemplateSource(t *testing.T, roots []DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "template source root not found", "source=%s path=%s roots=%v", source, path, roots)
}
