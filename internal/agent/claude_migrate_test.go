package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestParseClaudeImportArgs(t *testing.T) {
	req, err := parseClaudeImportArgs([]string{
		"run",
		"--source", "/tmp/claude",
		"--max-sessions=12",
		"--max-age", "7",
		"--output-format=json",
	})
	require.NoError(t, err)
	require.Equal(t, "run", req.Action)
	require.Equal(t, "/tmp/claude", req.SourceHome)
	require.Equal(t, 12, req.MaxSessions)
	require.Equal(t, 7*24*time.Hour, req.MaxAge)
	require.Equal(t, "json", req.Format)

	req, err = parseClaudeImportArgs([]string{"inspect", "--all"})
	require.NoError(t, err)
	require.Equal(t, "status", req.Action)
	require.Greater(t, req.MaxSessions, 1_000_000)

	_, err = parseClaudeImportArgs([]string{"unknown"})
	require.ErrorContains(t, err, "expected status or run")
	_, err = parseClaudeImportArgs([]string{"--max-sessions", "-1"})
	require.ErrorContains(t, err, "expected a non-negative integer")
	_, err = parseClaudeImportArgs([]string{"--max-age", "nope"})
	require.ErrorContains(t, err, "non-negative day count")
	_, err = parseClaudeImportArgs([]string{"--source"})
	require.ErrorContains(t, err, "needs an argument")
	_, err = parseClaudeImportArgs([]string{"--wat"})
	require.ErrorContains(t, err, "not defined")
}

func TestClaudeImportCommandRendersStatusAndImports(t *testing.T) {
	sourceHome := t.TempDir()
	workspace := t.TempDir()
	configHome := t.TempDir()
	projectDir := filepath.Join(sourceHome, "projects", "workspace")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	writeAgentClaudeTranscript(t, transcript, workspace)

	var out bytes.Buffer
	app := &App{
		Workspace: workspace,
		Sessions:  session.NewWorkspaceStore(configHome, workspace),
		Out:       &out,
		Err:       &out,
	}
	require.NoError(t, app.ClaudeImport([]string{"status", "--source", sourceHome, "--json"}))
	var status map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, "claude_migration", status["kind"])
	require.Equal(t, "ready", status["status"])
	require.EqualValues(t, 1, status["sessions_eligible"])
	require.NotContains(t, out.String(), "private prompt")

	out.Reset()
	require.NoError(t, app.ClaudeImport([]string{"run", "--source", sourceHome, "--output-format", "text"}))
	require.Contains(t, out.String(), "Status       imported")
	require.Contains(t, out.String(), "Imported     1")
	imported, err := app.Sessions.OpenExisting("session-1")
	require.NoError(t, err)
	require.Len(t, imported.Messages, 2)
	require.Equal(t, "private prompt", imported.Messages[0].Content[0].Text)
}

func writeAgentClaudeTranscript(t *testing.T, path, workspace string) {
	t.Helper()
	records := []map[string]any{
		{
			"type": "user", "cwd": workspace, "sessionId": "session-1", "uuid": "user-1",
			"message": map[string]any{"role": "user", "content": "private prompt"},
		},
		{
			"type": "assistant", "cwd": workspace, "sessionId": "session-1", "uuid": "assistant-1",
			"message": map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "text", "text": "private answer"}},
			},
		},
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		require.NoError(t, err)
		data = append(data, line...)
		data = append(data, '\n')
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))
}
