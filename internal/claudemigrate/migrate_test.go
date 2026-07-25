package claudemigrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/session"
	"github.com/stretchr/testify/require"
)

func TestRunDiscoversCompatibleAssetsAndImportsSessions(t *testing.T) {
	sourceHome := t.TempDir()
	workspace := t.TempDir()
	configHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(sourceHome, "skills", "review"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "skills", "review", "SKILL.md"), []byte("review"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".claude", "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".claude", "commands", "fix.md"), []byte("fix"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "CLAUDE.md"), []byte("rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "settings.json"), []byte(`{"hooks":{"Stop":[]}}`), 0o644))
	projectDir := filepath.Join(sourceHome, "projects", "workspace")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	sessionPath := filepath.Join(projectDir, "claude-session.jsonl")
	writeClaudeSession(t, sessionPath, workspace, "claude-session")

	store := session.NewWorkspaceStore(configHome, workspace)
	status, err := Run(Options{
		SourceHome:   sourceHome,
		Workspace:    workspace,
		SessionStore: store,
		MaxSessions:  10,
		MaxAge:       time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, "ready", status.Status)
	require.Equal(t, 1, status.SessionsEligible)
	require.Equal(t, 0, status.SessionsImported)
	require.Equal(t, 1, assetByKind(t, status.Assets, "skills").Count)
	require.Equal(t, 1, assetByKind(t, status.Assets, "commands").Count)
	require.Equal(t, 1, assetByKind(t, status.Assets, "instructions").Count)
	require.Equal(t, 1, assetByKind(t, status.Assets, "hooks").Count)

	applied, err := Run(Options{
		SourceHome:   sourceHome,
		Workspace:    workspace,
		SessionStore: store,
		MaxSessions:  10,
		MaxAge:       time.Hour,
		Apply:        true,
	})
	require.NoError(t, err)
	require.Equal(t, "imported", applied.Status)
	require.Equal(t, 1, applied.SessionsImported)
	imported, err := store.OpenExisting("claude-session")
	require.NoError(t, err)
	require.Len(t, imported.Messages, 2)
	require.Equal(t, "user", imported.Messages[0].Role)
	require.Equal(t, "review this change", imported.Messages[0].Content[0].Text)
	require.Equal(t, "assistant", imported.Messages[1].Role)
	require.Equal(t, "I will review it.", imported.Messages[1].Content[0].Text)
	require.Equal(t, "Imported from Claude Code", imported.Identity.Purpose)
	require.Equal(t, "claude-import", imported.Identity.Tag)
	history, err := store.PromptHistory("claude-session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "review this change", history[0].Text)

	repeated, err := Run(Options{
		SourceHome:   sourceHome,
		Workspace:    workspace,
		SessionStore: store,
		MaxSessions:  10,
		MaxAge:       time.Hour,
		Apply:        true,
	})
	require.NoError(t, err)
	require.Equal(t, "up_to_date", repeated.Status)
	require.Equal(t, 1, repeated.SessionsSkipped)
}

func TestRunFiltersWorkspaceAgeAndSessionLimit(t *testing.T) {
	sourceHome := t.TempDir()
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	projectDir := filepath.Join(sourceHome, "projects", "workspace")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	first := filepath.Join(projectDir, "first.jsonl")
	second := filepath.Join(projectDir, "second.jsonl")
	old := filepath.Join(projectDir, "old.jsonl")
	other := filepath.Join(projectDir, "other.jsonl")
	writeClaudeSession(t, first, workspace, "first")
	writeClaudeSession(t, second, workspace, "second")
	writeClaudeSession(t, old, workspace, "old")
	writeClaudeSession(t, other, otherWorkspace, "other")
	now := time.Now()
	require.NoError(t, os.Chtimes(first, now.Add(-2*time.Minute), now.Add(-2*time.Minute)))
	require.NoError(t, os.Chtimes(second, now.Add(-time.Minute), now.Add(-time.Minute)))
	require.NoError(t, os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))

	report, err := Run(Options{
		SourceHome:  sourceHome,
		Workspace:   workspace,
		MaxSessions: 1,
		MaxAge:      24 * time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, 1, report.SessionsEligible)
	require.Equal(t, "second", report.Sessions[0].SessionID)
}

func TestRunReportsMalformedRelevantSession(t *testing.T) {
	sourceHome := t.TempDir()
	workspace := t.TempDir()
	projectDir := filepath.Join(sourceHome, "projects", "workspace")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	path := filepath.Join(projectDir, "broken.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","cwd":`), 0o644))

	report, err := Run(Options{SourceHome: sourceHome, Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "error", report.Status)
	require.Equal(t, 1, report.SessionsFailed)
	require.Equal(t, "failed", report.Sessions[0].Status)
	require.Contains(t, report.Sessions[0].Reason, "decode transcript")
}

func TestRunHandlesMissingSourceAndValidatesOptions(t *testing.T) {
	workspace := t.TempDir()
	report, err := Run(Options{SourceHome: filepath.Join(t.TempDir(), "missing"), Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, "not_found", report.Status)
	require.Empty(t, report.Assets)

	_, err = Run(Options{SourceHome: t.TempDir(), Workspace: workspace, MaxSessions: -1})
	require.EqualError(t, err, "max sessions must be non-negative")
	_, err = Run(Options{SourceHome: t.TempDir(), Workspace: workspace, Apply: true})
	require.EqualError(t, err, "session store is required when applying a migration")
	_, err = Run(Options{SourceHome: t.TempDir(), Workspace: ""})
	require.EqualError(t, err, "workspace is required")
	_, err = Run(Options{SourceHome: t.TempDir(), Workspace: workspace, MaxAge: -time.Second})
	require.EqualError(t, err, "max age must be non-negative")

	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("data"), 0o644))
	_, err = Run(Options{SourceHome: file, Workspace: workspace})
	require.ErrorContains(t, err, "source home is not a directory")
}

func TestConvertContentPreservesToolAndMediaBlocks(t *testing.T) {
	data := json.RawMessage(`[
		{"type":"thinking","thinking":"inspect","signature":"sig"},
		{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"main.go"}},
		{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"text","text":"package main"}]},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}
	]`)
	blocks, err := convertContent(data)
	require.NoError(t, err)
	require.Len(t, blocks, 4)
	require.Equal(t, "inspect", blocks[0].Thinking)
	require.JSONEq(t, `{"file_path":"main.go"}`, string(blocks[1].Input))
	require.Equal(t, "package main", blocks[2].Content)
	require.Equal(t, "image/png", blocks[3].Source.MediaType)
}

func TestConversionRejectsMalformedDataAndSkipsUnsupportedMessages(t *testing.T) {
	_, _, err := convertMessage(json.RawMessage(`{`))
	require.ErrorContains(t, err, "decode message")
	_, ok, err := convertMessage(json.RawMessage(`{"role":"system","content":"ignored"}`))
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = convertMessage(json.RawMessage(`{"role":"assistant","content":[]}`))
	require.NoError(t, err)
	require.False(t, ok)
	_, err = convertContent(json.RawMessage(`{"unexpected":true}`))
	require.ErrorContains(t, err, "decode message content")
	require.Empty(t, contentText(nil))
	require.JSONEq(t, `{"unexpected":true}`, contentText(json.RawMessage(`{"unexpected":true}`)))
}

func TestSessionTitleUsesFallbackAndRuneLimit(t *testing.T) {
	require.Equal(t, "Imported Claude Code session empty", sessionTitle(candidate{id: "empty"}))
	title := sessionTitle(candidate{
		id: "long",
		messages: []importMessage{{
			message: anthropic.TextMessage("user", strings.Repeat("界", 80)+"\nsecond"),
		}},
	})
	require.Len(t, []rune(title), 72)
}

func TestDefaultSourceHomeHonorsEnvironment(t *testing.T) {
	source := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", source)
	resolved, err := DefaultSourceHome()
	require.NoError(t, err)
	require.Equal(t, source, resolved)
}

func TestRunReturnsAssetDecodeErrors(t *testing.T) {
	sourceHome := t.TempDir()
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "settings.json"), []byte(`{`), 0o644))
	_, err := Run(Options{SourceHome: sourceHome, Workspace: workspace})
	require.ErrorContains(t, err, "decode hooks source")

	require.NoError(t, os.WriteFile(filepath.Join(sourceHome, "settings.json"), []byte(`{"hooks":"invalid"}`), 0o644))
	_, err = Run(Options{SourceHome: sourceHome, Workspace: workspace})
	require.ErrorContains(t, err, "decode hooks in")
}

func TestParseMaxAge(t *testing.T) {
	age, err := ParseMaxAge("7")
	require.NoError(t, err)
	require.Equal(t, 7*24*time.Hour, age)
	_, err = ParseMaxAge("-1")
	require.EqualError(t, err, "max age must be a non-negative day count")
	_, err = ParseMaxAge("nope")
	require.EqualError(t, err, "max age must be a non-negative day count")
}

func writeClaudeSession(t *testing.T, path, workspace, id string) {
	t.Helper()
	records := []map[string]any{
		{
			"type": "user", "cwd": workspace, "sessionId": id, "uuid": id + "-user",
			"timestamp": "2026-07-01T00:00:00Z",
			"message":   map[string]any{"role": "user", "content": "review this change"},
		},
		{
			"type": "assistant", "cwd": workspace, "sessionId": id, "uuid": id + "-assistant",
			"timestamp": "2026-07-01T00:00:01Z",
			"message": map[string]any{
				"id": "msg-1", "role": "assistant",
				"content": []map[string]any{{"type": "text", "text": "I will review it."}},
				"usage":   map[string]any{"input_tokens": 12, "output_tokens": 5},
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

func assetByKind(t *testing.T, assets []Asset, kind string) Asset {
	t.Helper()
	for _, asset := range assets {
		if asset.Kind == kind {
			return asset
		}
	}
	t.Fatalf("asset %q not found", kind)
	return Asset{}
}
