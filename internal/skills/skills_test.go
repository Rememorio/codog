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
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "skills", "mismatch"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "commands", "ops"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "skills", "team", "audit"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "skills", "summarize"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog", "plugins", "demo", "extra", "rewrite"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configHome, "skills", "plain.md"), []byte("Plain skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "review", "SKILL.md"), []byte("Review skill from ${CLAUDE_SKILL_DIR}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "skills", "mismatch", "SKILL.md"), []byte(`---
name: external-review
---
Mismatch skill`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "deploy.md"), []byte("Deploy command body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "review.md"), []byte("Legacy review command body"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "commands", "ops", "rotate.md"), []byte("Rotate command body"), 0o644))
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
	require.Contains(t, names, "deploy")
	require.Contains(t, names, "mismatch")
	require.Contains(t, names, "ops:rotate")
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
	require.Equal(t, originSkillsDir, skill.Origin.ID)
	require.Equal(t, reviewDir, skill.SkillDir)
	require.Contains(t, RenderInvocation(skill, ""), "Review skill from "+reviewDir)

	skill, err = Find(configHome, workspace, "deploy")
	require.NoError(t, err)
	require.Equal(t, "workspace", skill.Source)
	require.Equal(t, "Deploy command body", skill.Body)
	require.Equal(t, originLegacyCommandsDir, skill.Origin.ID)
	require.Equal(t, legacyCommandsDetailText, skill.Origin.DetailLabel)

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

	skill, err = Find(configHome, workspace, "mismatch")
	require.NoError(t, err)
	require.True(t, skill.NameDrift)
	require.Equal(t, "external-review", skill.DisplayName)
	drifts := MetadataDrifts(all)
	require.Contains(t, drifts, MetadataDrift{
		InvocationName:  "mismatch",
		FrontmatterName: "external-review",
		Path:            filepath.Join(workspace, ".codog", "skills", "mismatch", "SKILL.md"),
		Source:          "workspace",
	})

	_, err = Find(configHome, workspace, "missing")
	require.True(t, errors.Is(err, ErrNotFound))

	sources := Sources(configHome, workspace)
	requireSource(t, sources, "bundled", "builtin://skills", true)
	requireSource(t, sources, "user", filepath.Join(configHome, "skills"), true)
	requireSource(t, sources, "workspace", filepath.Join(workspace, ".codog", "skills"), true)
	requireSource(t, sources, "claude", filepath.Join(workspace, ".claude", "skills"), true)
	commandSourceRoot := sourceByPath(sources, filepath.Join(workspace, ".codog", "commands"))
	require.Equal(t, "workspace", commandSourceRoot.Source)
	require.Equal(t, originLegacyCommandsDir, commandSourceRoot.Origin.ID)
	require.Equal(t, legacyCommandsDetailText, commandSourceRoot.Origin.DetailLabel)
	require.True(t, commandSourceRoot.Exists)
	pluginSourceRoot := sourceByPath(sources, filepath.Join(workspace, ".codog", "plugins", "demo", "skills"))
	require.Equal(t, "plugin:demo", pluginSourceRoot.Source)
	require.Equal(t, originSkillsDir, pluginSourceRoot.Origin.ID)
	require.Equal(t, "demo", pluginSourceRoot.PluginID)
	require.Equal(t, filepath.Join(workspace, ".codog", "plugins", "demo"), pluginSourceRoot.PluginRoot)
	require.True(t, pluginSourceRoot.Exists)
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
	require.True(t, verify.Active)

	all, err := Load(configHome, workspace)
	require.NoError(t, err)
	projectVerify := skillByNameAndSource(all, "verify", "workspace")
	require.True(t, projectVerify.Active)
	bundledVerify := skillByNameAndSource(all, "verify", "bundled")
	require.False(t, bundledVerify.Active)
	require.Equal(t, "workspace", bundledVerify.ShadowedBy)
	require.Equal(t, projectVerify.Path, bundledVerify.ShadowedByPath)

	debug, err := Find(configHome, workspace, "debug")
	require.NoError(t, err)
	require.Equal(t, "bundled", debug.Source)
	require.Equal(t, "builtin://skills/debug.md", debug.Path)
	require.Contains(t, debug.Description, "Debug failing Codog behavior")
	require.Contains(t, RenderInvocation(debug, "failing test"), "User request: failing test")
}

func TestCompatibilityProjectSkillRoots(t *testing.T) {
	configHome := t.TempDir()
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repo", "app")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, ".codex", "skills", "port"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agents", "skills", "agent"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claw", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".codex", "skills", "port", "SKILL.md"), []byte("Codex port skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agents", "skills", "agent", "SKILL.md"), []byte("Agent skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claw", "commands", "inspect.md"), []byte("Inspect command"), 0o644))

	codexSkill, err := Find(configHome, workspace, "port")
	require.NoError(t, err)
	require.Equal(t, "codex", codexSkill.Source)
	require.Equal(t, "Codex port skill", codexSkill.Body)
	require.Equal(t, originSkillsDir, codexSkill.Origin.ID)

	agentSkill, err := Find(configHome, workspace, "agent")
	require.NoError(t, err)
	require.Equal(t, "agents", agentSkill.Source)
	require.Equal(t, "Agent skill", agentSkill.Body)

	commandSkill, err := Find(configHome, workspace, "inspect")
	require.NoError(t, err)
	require.Equal(t, "claw", commandSkill.Source)
	require.Equal(t, "Inspect command", commandSkill.Body)
	require.Equal(t, originLegacyCommandsDir, commandSkill.Origin.ID)

	sources := Sources(configHome, workspace)
	codexSource := sourceByPath(sources, filepath.Join(parent, ".codex", "skills"))
	require.Equal(t, "codex", codexSource.Source)
	require.Equal(t, "Codex-compatible project skills", codexSource.Label)
	require.True(t, codexSource.Exists)
	clawCommandSource := sourceByPath(sources, filepath.Join(workspace, ".claw", "commands"))
	require.Equal(t, "claw", clawCommandSource.Source)
	require.Equal(t, originLegacyCommandsDir, clawCommandSource.Origin.ID)
	require.True(t, clawCommandSource.Exists)
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

func TestRenderInvocationWithSessionSubstitutesSessionID(t *testing.T) {
	skill := ParseDocument("review", filepath.Join("review", "SKILL.md"), "workspace", "Review session ${CLAUDE_SESSION_ID}.")

	require.Contains(t, RenderInvocation(skill, ""), "Review session ${CLAUDE_SESSION_ID}.")
	require.Contains(t, RenderInvocationWithSession(skill, "", "session-123"), "Review session session-123.")
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

func skillByNameAndSource(all []Skill, name string, source string) Skill {
	for _, skill := range all {
		if skill.Name == name && skill.Source == source {
			return skill
		}
	}
	return Skill{}
}

func requireSource(t *testing.T, roots []DiscoveryRoot, source string, path string, exists bool) {
	t.Helper()
	for _, root := range roots {
		if root.Source == source && root.Path == path {
			require.Equal(t, exists, root.Exists)
			require.NotEmpty(t, root.Label)
			return
		}
	}
	require.Failf(t, "source root not found", "source=%s path=%s roots=%v", source, path, roots)
}

func sourceByPath(roots []DiscoveryRoot, path string) DiscoveryRoot {
	for _, root := range roots {
		if root.Path == path {
			return root
		}
	}
	return DiscoveryRoot{}
}
