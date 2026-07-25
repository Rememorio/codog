package projectinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitializeCreatesExpectedArtifacts(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/codog\n"), 0o644))

	report, err := Initialize(workspace)

	require.NoError(t, err)
	require.False(t, report.AlreadyInitialized)
	require.Equal(t, "ok", report.Status)
	require.Contains(t, report.Created, ".codog/")
	require.NotNil(t, report.Updated)
	require.Contains(t, report.Deferred, ".codog/sessions/")
	require.NotNil(t, report.Skipped)
	require.NotNil(t, report.Partial)
	require.Contains(t, report.Created, ".codog/instructions.md")
	require.Contains(t, report.Created, ".codog.json")
	require.Contains(t, report.Created, ".gitignore")
	require.Contains(t, report.Created, "AGENTS.md")
	require.Contains(t, report.Created, "CLAUDE.md")
	require.Equal(t, StatusDeferred, artifactByName(t, report, ".codog/sessions/").Status)
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoDirExists(t, filepath.Join(workspace, ".codog", "sessions"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.FileExists(t, filepath.Join(workspace, ".gitignore"))
	require.FileExists(t, filepath.Join(workspace, "AGENTS.md"))
	require.FileExists(t, filepath.Join(workspace, "CLAUDE.md"))

	instructions, err := os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Contains(t, string(instructions), "Languages: Go.")
	require.Contains(t, string(instructions), "go test ./...")

	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Contains(t, string(agents), ".codog/instructions.md")
	require.Contains(t, string(agents), "go test ./...")

	claude, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	require.NoError(t, err)
	require.Contains(t, string(claude), ".codog/instructions.md")
	require.Contains(t, string(claude), "go test ./...")

	configData, err := os.ReadFile(filepath.Join(workspace, ".codog.json"))
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, json.Unmarshal(configData, &config))
	require.Equal(t, "workspace-write", config["permission_mode"])

	gitignore, err := os.ReadFile(filepath.Join(workspace, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(gitignore), ".codog.local.json")
	require.Contains(t, string(gitignore), ".codog/worker-state.json")
	require.Contains(t, string(gitignore), ".codog/focus.json")
	require.Contains(t, string(gitignore), ".codog/output-style.json")
	require.Contains(t, string(gitignore), ".codog/todos.json")
	require.Contains(t, string(gitignore), ".codog/plan.json")
	require.Contains(t, string(gitignore), ".codog/undo.jsonl")
	require.Contains(t, string(gitignore), ".codog/safer-scope.json")
	require.Contains(t, string(gitignore), ".codog/additional-dirs.json")
	require.Contains(t, string(gitignore), ".codog/sessions/")
	require.Contains(t, string(gitignore), ".codog/heap/")
	require.Contains(t, string(gitignore), ".codog/share/")
	require.Contains(t, string(gitignore), ".codog/feedback/")
	require.Contains(t, string(gitignore), ".codog/autofix/")
	require.Contains(t, string(gitignore), ".codog/drafts/")
	require.Contains(t, string(gitignore), ".codog/perf/")
	require.Contains(t, string(gitignore), ".codog/recovery/")
	require.Contains(t, string(gitignore), ".codog/traces/")
	require.Contains(t, string(gitignore), ".codog/worktrees/")
	require.Contains(t, string(gitignore), ".codog/context-viz.html")
	require.Contains(t, string(gitignore), ".codog/visualizations/")
	require.Contains(t, string(gitignore), ".codog/think-back-*.html")
}

func TestInitializeIsIdempotentAndPreservesFiles(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "instructions.md"), []byte("custom instructions\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"model":"custom"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(gitignoreComment+"\n"+strings.Join(gitignoreEntries, "\n")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("custom agents\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("custom claude\n"), 0o644))

	report, err := Initialize(workspace)

	require.NoError(t, err)
	require.True(t, report.AlreadyInitialized)
	require.Empty(t, report.Created)
	require.Empty(t, report.Updated)
	require.Contains(t, report.Deferred, ".codog/sessions/")
	require.Contains(t, report.Skipped, ".codog/")
	require.Contains(t, report.Skipped, ".codog/instructions.md")
	require.Contains(t, report.Skipped, ".codog.json")
	require.Contains(t, report.Skipped, ".gitignore")
	require.Contains(t, report.Skipped, "AGENTS.md")
	require.Contains(t, report.Skipped, "CLAUDE.md")
	require.Equal(t, "already_exists", artifactByName(t, report, ".codog/").SkipReason)
	require.Equal(t, "already_exists", artifactByName(t, report, ".codog/instructions.md").SkipReason)
	require.Equal(t, "already_exists", artifactByName(t, report, ".codog.json").SkipReason)
	require.Equal(t, "already_configured", artifactByName(t, report, ".gitignore").SkipReason)
	require.Equal(t, "already_exists", artifactByName(t, report, "AGENTS.md").SkipReason)
	require.Equal(t, "already_exists", artifactByName(t, report, "CLAUDE.md").SkipReason)

	instructions, err := os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Equal(t, "custom instructions\n", string(instructions))

	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.Equal(t, "custom agents\n", string(agents))

	claude, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "custom claude\n", string(claude))
}

func TestInitializeReportsPartialDirectory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))

	report, err := Initialize(workspace)

	require.NoError(t, err)
	require.Contains(t, report.Partial, ".codog/")
	require.Contains(t, report.Created, ".codog/instructions.md")
}

func TestEnsureGitignoreEntriesUpdatesExistingFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("dist/\n"), 0o644))

	status, err := ensureGitignoreEntries(path)

	require.NoError(t, err)
	require.Equal(t, StatusUpdated, status)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "dist/")
	require.Contains(t, string(data), gitignoreComment)
	require.Contains(t, string(data), ".codog.local.json")
	require.Contains(t, string(data), ".codog/worker-state.json")
	require.Contains(t, string(data), ".codog/focus.json")
	require.Contains(t, string(data), ".codog/output-style.json")
	require.Contains(t, string(data), ".codog/todos.json")
	require.Contains(t, string(data), ".codog/plan.json")
	require.Contains(t, string(data), ".codog/undo.jsonl")
	require.Contains(t, string(data), ".codog/safer-scope.json")
	require.Contains(t, string(data), ".codog/additional-dirs.json")
	require.Contains(t, string(data), ".codog/sessions/")
	require.Contains(t, string(data), ".codog/heap/")
	require.Contains(t, string(data), ".codog/share/")
	require.Contains(t, string(data), ".codog/feedback/")
	require.Contains(t, string(data), ".codog/autofix/")
	require.Contains(t, string(data), ".codog/drafts/")
	require.Contains(t, string(data), ".codog/perf/")
	require.Contains(t, string(data), ".codog/recovery/")
	require.Contains(t, string(data), ".codog/traces/")
	require.Contains(t, string(data), ".codog/worktrees/")
	require.Contains(t, string(data), ".codog/context-viz.html")
	require.Contains(t, string(data), ".codog/think-back-*.html")
}

func TestRenderText(t *testing.T) {
	report := newReport("/repo", []Artifact{
		{Name: ".codog/", Status: StatusCreated},
		{Name: ".codog/sessions/", Status: StatusDeferred},
		{Name: ".codog.json", Status: StatusSkipped},
	})

	rendered := RenderText(report)

	require.Contains(t, rendered, "Init")
	require.Contains(t, rendered, "Project          /repo")
	require.Contains(t, rendered, ".codog/")
	require.Contains(t, rendered, "created")
	require.Contains(t, rendered, ".codog/sessions/")
	require.Contains(t, rendered, "deferred")
	require.Contains(t, rendered, NextStep)
}

func artifactByName(t *testing.T, report Report, name string) Artifact {
	t.Helper()
	for _, artifact := range report.Artifacts {
		if artifact.Name == name {
			return artifact
		}
	}
	require.Failf(t, "artifact missing", "artifact %q not found in %#v", name, report.Artifacts)
	return Artifact{}
}
