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

func TestParseSkillFrontmatterMetadata(t *testing.T) {
	doc := `---
name: Review helper
description: Review Go changes.
allowed-tools:
  - Read
  - Bash(go test:*)
argument-hint: FILE
arguments: [file, focus]
paths:
  - internal/**
  - "**"
when_to_use: When Go files change.
version: 1.2.3
model: sonnet
context: fork
agent: reviewer
effort: high
user-invocable: false
disable-model-invocation: true
---
# Review body

Use the checklist.
`
	skill := ParseDocument("review", filepath.Join("review", "SKILL.md"), "workspace", doc)

	require.Equal(t, "review", skill.Name)
	require.Equal(t, "Review helper", skill.DisplayName)
	require.Equal(t, "Review Go changes.", skill.Description)
	require.Equal(t, []string{"Read", "Bash(go test:*)"}, skill.AllowedTools)
	require.Equal(t, "FILE", skill.ArgumentHint)
	require.Equal(t, []string{"file", "focus"}, skill.Arguments)
	require.Equal(t, []string{"internal"}, skill.Paths)
	require.Equal(t, "When Go files change.", skill.WhenToUse)
	require.Equal(t, "1.2.3", skill.Version)
	require.Equal(t, "sonnet", skill.Model)
	require.Equal(t, "fork", skill.ExecutionContext)
	require.Equal(t, "reviewer", skill.Agent)
	require.Equal(t, "high", skill.Effort)
	require.False(t, skill.UserInvocable)
	require.True(t, skill.DisableModelInvocation)
	require.NotContains(t, skill.Body, "allowed-tools")

	rendered := RenderPromptBlock(skill)
	require.Contains(t, rendered, `<skill name="review"`)
	require.Contains(t, rendered, `display_name="Review helper"`)
	require.Contains(t, rendered, "Description: Review Go changes.")
	require.Contains(t, rendered, "Allowed tools: Read, Bash(go test:*)")
	require.Contains(t, rendered, "Paths: internal")
	require.Contains(t, rendered, "User invocable: false")
	require.Contains(t, rendered, "# Review body")
	require.NotContains(t, rendered, "---")
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
