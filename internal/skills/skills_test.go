package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFindAndRenderSkillInvocation(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "skills", "review"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "plain.md"), []byte("Plain skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "review", "SKILL.md"), []byte("Review skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill"), 0o644))

	all, err := Load(configHome, workspace)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, []string{"plain", "review", "team:audit"}, skillNames(all))

	skill, err := Find(configHome, workspace, "team:audit")
	require.NoError(t, err)
	require.Equal(t, "claude", skill.Source)
	rendered := RenderInvocation(skill, "check auth")
	require.Contains(t, rendered, `<skill name="team:audit"`)
	require.Contains(t, rendered, "Audit skill")
	require.Contains(t, rendered, "User request: check auth")

	_, err = Find(configHome, workspace, "missing")
	require.True(t, errors.Is(err, ErrNotFound))
}

func skillNames(all []Skill) []string {
	names := make([]string, 0, len(all))
	for _, skill := range all {
		names = append(names, skill.Name)
	}
	return names
}
