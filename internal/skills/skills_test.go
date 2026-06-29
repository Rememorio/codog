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

func TestInstallAndUninstallSkills(t *testing.T) {
	root := t.TempDir()
	sourceFile := filepath.Join(root, "review.md")
	require.NoError(t, os.WriteFile(sourceFile, []byte("Review body"), 0o644))
	sourceDir := filepath.Join(root, "audit")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte("Audit body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "notes.txt"), []byte("extra"), 0o644))
	targetRoot := filepath.Join(root, "target")

	fileReport, err := Install(sourceFile, targetRoot, "", "user")
	require.NoError(t, err)
	require.Equal(t, "review", fileReport.Name)
	require.FileExists(t, filepath.Join(targetRoot, "review.md"))

	dirReport, err := Install(sourceDir, targetRoot, "team:audit-copy", "workspace")
	require.NoError(t, err)
	require.Equal(t, "team:audit-copy", dirReport.Name)
	require.FileExists(t, filepath.Join(targetRoot, "team", "audit-copy", "SKILL.md"))
	require.FileExists(t, filepath.Join(targetRoot, "team", "audit-copy", "notes.txt"))

	uninstalled, err := Uninstall("review", []string{targetRoot})
	require.NoError(t, err)
	require.True(t, uninstalled.Removed)
	require.NoFileExists(t, filepath.Join(targetRoot, "review.md"))

	uninstalled, err = Uninstall("team:audit-copy", []string{targetRoot})
	require.NoError(t, err)
	require.True(t, uninstalled.Removed)
	require.NoDirExists(t, filepath.Join(targetRoot, "team", "audit-copy"))
}

func skillNames(all []Skill) []string {
	names := make([]string, 0, len(all))
	for _, skill := range all {
		names = append(names, skill.Name)
	}
	return names
}
