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
	require.NotNil(t, report.Skipped)
	require.NotNil(t, report.Partial)
	require.Contains(t, report.Created, ".codog/instructions.md")
	require.Contains(t, report.Created, ".codog.json")
	require.Contains(t, report.Created, ".gitignore")
	require.FileExists(t, filepath.Join(workspace, ".codog", "instructions.md"))
	require.FileExists(t, filepath.Join(workspace, ".codog.json"))
	require.FileExists(t, filepath.Join(workspace, ".gitignore"))

	instructions, err := os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Contains(t, string(instructions), "Languages: Go.")
	require.Contains(t, string(instructions), "go test ./...")

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
}

func TestInitializeIsIdempotentAndPreservesFiles(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".codog"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog", "instructions.md"), []byte("custom instructions\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".codog.json"), []byte(`{"model":"custom"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(gitignoreComment+"\n"+strings.Join(gitignoreEntries, "\n")+"\n"), 0o644))

	report, err := Initialize(workspace)

	require.NoError(t, err)
	require.True(t, report.AlreadyInitialized)
	require.Empty(t, report.Created)
	require.Empty(t, report.Updated)
	require.Contains(t, report.Skipped, ".codog/")
	require.Contains(t, report.Skipped, ".codog/instructions.md")
	require.Contains(t, report.Skipped, ".codog.json")
	require.Contains(t, report.Skipped, ".gitignore")

	instructions, err := os.ReadFile(filepath.Join(workspace, ".codog", "instructions.md"))
	require.NoError(t, err)
	require.Equal(t, "custom instructions\n", string(instructions))
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
}

func TestRenderText(t *testing.T) {
	report := newReport("/repo", []Artifact{
		{Name: ".codog/", Status: StatusCreated},
		{Name: ".codog.json", Status: StatusSkipped},
	})

	rendered := RenderText(report)

	require.Contains(t, rendered, "Init")
	require.Contains(t, rendered, "Project          /repo")
	require.Contains(t, rendered, ".codog/")
	require.Contains(t, rendered, "created")
	require.Contains(t, rendered, NextStep)
}
