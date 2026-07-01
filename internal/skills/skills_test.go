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
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "skills", "summarize"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "extra", "rewrite"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "plain.md"), []byte("Plain skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "review", "SKILL.md"), []byte("Review skill from ${CLAUDE_SKILL_DIR}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "skills", "team", "audit", "SKILL.md"), []byte("Audit skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "plugin.json"), []byte(`{"id":"demo","name":"demo","skills":["./extra/rewrite"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "skills", "summarize", "SKILL.md"), []byte(`---
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/*)
---
Summarize ${CLAUDE_SKILL_DIR} ${CLAUDE_PLUGIN_ROOT} ${CLAUDE_PLUGIN_DATA}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "plugins", "demo", "extra", "rewrite", "SKILL.md"), []byte("Rewrite skill"), 0o644))

	all, err := Load(configHome, workspace)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 21)
	names := skillNames(all)
	require.Contains(t, names, "batch")
	require.Contains(t, names, "verify")
	require.Contains(t, names, "verifyContent")
	require.Contains(t, names, "demo:rewrite")
	require.Contains(t, names, "demo:summarize")
	require.Contains(t, names, "plain")
	require.Contains(t, names, "review")
	require.Contains(t, names, "team:audit")

	skill, err := Find(configHome, workspace, "team:audit")
	require.NoError(t, err)
	require.Equal(t, "claude", skill.Source)
	rendered := RenderInvocation(skill, "check auth")
	require.Contains(t, rendered, `<skill name="team:audit"`)
	require.Contains(t, rendered, "Audit skill")
	require.Contains(t, rendered, "User request: check auth")

	skill, err = Find(configHome, workspace, "review")
	require.NoError(t, err)
	reviewDir := filepath.ToSlash(filepath.Join(workspace, ".codog", "skills", "review"))
	require.Equal(t, reviewDir, skill.SkillDir)
	require.Contains(t, RenderInvocation(skill, ""), "Review skill from "+reviewDir)

	skill, err = Find(configHome, workspace, "demo:summarize")
	require.NoError(t, err)
	require.Equal(t, "plugin:demo", skill.Source)
	pluginRoot := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugins", "demo"))
	pluginData := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugin-data", "demo"))
	skillDir := filepath.ToSlash(filepath.Join(workspace, ".codog", "plugins", "demo", "skills", "summarize"))
	require.Equal(t, skillDir, skill.SkillDir)
	require.Equal(t, pluginRoot, skill.PluginRoot)
	require.Equal(t, pluginData, skill.PluginData)
	require.Equal(t, []string{"Bash(" + pluginRoot + "/bin/*)"}, skill.AllowedTools)
	require.Contains(t, RenderInvocation(skill, ""), "Summarize "+skillDir+" "+pluginRoot+" "+pluginData)

	skill, err = Find(configHome, workspace, "demo:rewrite")
	require.NoError(t, err)
	require.Equal(t, "plugin:demo", skill.Source)
	require.Equal(t, "Rewrite skill", skill.Body)

	_, err = Find(configHome, workspace, "missing")
	require.True(t, errors.Is(err, ErrNotFound))
}

func TestBundledSkillsLoadAndCanBeOverridden(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "verify.md"), []byte("Project verify override."), 0o644))

	bundled := Bundled()
	require.GreaterOrEqual(t, len(bundled), 16)
	require.Contains(t, skillNames(bundled), "claudeApi")
	require.Contains(t, skillNames(bundled), "scheduleRemoteAgents")

	verify, err := Find(configHome, workspace, "verify")
	require.NoError(t, err)
	require.Equal(t, "workspace", verify.Source)
	require.Equal(t, "Project verify override.", verify.Body)

	debug, err := Find(configHome, workspace, "debug")
	require.NoError(t, err)
	require.Equal(t, "bundled", debug.Source)
	require.Equal(t, "builtin://skills/debug.md", debug.Path)
	require.Contains(t, debug.Description, "Debug failing Codog behavior")
	require.Contains(t, RenderInvocation(debug, "failing test"), "User request: failing test")
}

func TestRenderInvocationSubstitutesNamedAndIndexedArguments(t *testing.T) {
	doc := `---
description: Review a target.
arguments: [file, focus]
---
Review $file for $focus.
First: $0
Second: $ARGUMENTS[1]
All: $ARGUMENTS
Keep: $filename
`
	skill := ParseDocument("review", filepath.Join("review", "SKILL.md"), "workspace", doc)
	rendered := RenderInvocation(skill, `main.go "race condition"`)

	require.Contains(t, rendered, "Review main.go for race condition.")
	require.Contains(t, rendered, "First: main.go")
	require.Contains(t, rendered, "Second: race condition")
	require.Contains(t, rendered, `All: main.go "race condition"`)
	require.Contains(t, rendered, "Keep: $filename")
	require.Contains(t, rendered, `User request: main.go "race condition"`)
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

func TestMatchesAnyPathSupportsDirectoriesAndGlobs(t *testing.T) {
	skill := Skill{Paths: []string{"internal", "*.md", "cmd/**/*.go"}}

	require.True(t, MatchesAnyPath(skill, []string{"internal/agent/agent.go"}))
	require.True(t, MatchesAnyPath(skill, []string{"README.md"}))
	require.True(t, MatchesAnyPath(skill, []string{"cmd/codog/main.go"}))
	require.False(t, MatchesAnyPath(skill, []string{"docs/guide.txt"}))
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
