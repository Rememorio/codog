package verifiers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitializeGeneratesVerifierSkills(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/root\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "web"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "web", "package.json"), []byte(`{
		"scripts": {"dev": "vite --host 127.0.0.1"},
		"dependencies": {"react": "latest", "vite": "latest"}
	}`), 0o644))

	report, err := Initialize(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Len(t, report.Areas, 2)
	require.Contains(t, report.Created, ".claude/skills/verifier-cli/SKILL.md")
	require.Contains(t, report.Created, ".claude/skills/verifier-web-web/SKILL.md")

	rootSkill, err := os.ReadFile(filepath.Join(workspace, ".claude", "skills", "verifier-cli", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(rootSkill), "name: verifier-cli")
	require.Contains(t, string(rootSkill), "Path: `.`")

	webSkill, err := os.ReadFile(filepath.Join(workspace, ".claude", "skills", "verifier-web-web", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(webSkill), "mcp__playwright__*")
	require.Contains(t, string(webSkill), "Path: `web`")
}

func TestInitializeDryRunAndForce(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "requirements.txt"), []byte("fastapi\n"), 0o644))

	report, err := Initialize(Options{Workspace: workspace, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, "ok", report.Status)
	require.Contains(t, report.Created, ".claude/skills/verifier-api/SKILL.md")
	require.NoFileExists(t, filepath.Join(workspace, ".claude", "skills", "verifier-api", "SKILL.md"))

	report, err = Initialize(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Contains(t, report.Created, ".claude/skills/verifier-api/SKILL.md")

	skillPath := filepath.Join(workspace, ".claude", "skills", "verifier-api", "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("custom\n"), 0o644))
	report, err = Initialize(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Contains(t, report.Skipped, ".claude/skills/verifier-api/SKILL.md")
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(data))

	report, err = Initialize(Options{Workspace: workspace, Force: true})
	require.NoError(t, err)
	require.Contains(t, report.Updated, ".claude/skills/verifier-api/SKILL.md")
	data, err = os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "name: verifier-api")
}

func TestInitializeCodogTargetAndNoMarkers(t *testing.T) {
	workspace := t.TempDir()
	report, err := Initialize(Options{Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "skipped", report.Status)
	require.Contains(t, report.Warnings[0], "No go.mod")

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "Cargo.toml"), []byte("[package]\nname = \"cli\"\n"), 0o644))
	report, err = Initialize(Options{Workspace: workspace, Target: "codog"})
	require.NoError(t, err)
	require.Contains(t, report.Created, ".codog/skills/verifier-cli/SKILL.md")
	require.FileExists(t, filepath.Join(workspace, ".codog", "skills", "verifier-cli", "SKILL.md"))
}
