package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceStoreUsesCanonicalFingerprint(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewWorkspaceStore(configHome, filepath.Join(workspace, "."))
	canonical, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)

	require.Equal(t, canonical, store.Workspace)
	require.Equal(t, filepath.Join(configHome, "sessions", WorkspaceFingerprint(canonical)), store.Dir)

	msg := anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "hello"}}}
	require.NoError(t, store.Append("session-a", msg))
	require.FileExists(t, filepath.Join(store.Dir, "session-a.jsonl"))
}

func TestWorkspaceStoresIsolateSameSessionID(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))

	storeA := NewWorkspaceStore(configHome, workspaceA)
	storeB := NewWorkspaceStore(configHome, workspaceB)
	require.NotEqual(t, storeA.Dir, storeB.Dir)

	require.NoError(t, storeA.Append("shared", anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "from a"}}}))
	require.NoError(t, storeB.Append("shared", anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "from b"}}}))

	sessionA, err := storeA.Open("shared")
	require.NoError(t, err)
	require.Len(t, sessionA.Messages, 1)
	require.Equal(t, "from a", sessionA.Messages[0].Content[0].Text)

	sessionB, err := storeB.Open("shared")
	require.NoError(t, err)
	require.Len(t, sessionB.Messages, 1)
	require.Equal(t, "from b", sessionB.Messages[0].Content[0].Text)
}

func TestWorkspaceStoreReadsAndContinuesLegacyFlatSessions(t *testing.T) {
	configHome := t.TempDir()
	legacy := NewStore(configHome)
	require.NoError(t, legacy.Append("legacy-session", anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "legacy"}}}))

	store := NewWorkspaceStore(configHome, t.TempDir())
	opened, err := store.Open("legacy-session")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(configHome, "sessions", "legacy-session.jsonl"), opened.Path)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "legacy", opened.Messages[0].Content[0].Text)

	require.NoError(t, store.Append("legacy-session", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{{Type: "text", Text: "continued"}}}))
	reopened, err := store.Open("legacy-session")
	require.NoError(t, err)
	require.Len(t, reopened.Messages, 2)
	require.Equal(t, "continued", reopened.Messages[1].Content[0].Text)

	latest, err := store.LatestID()
	require.NoError(t, err)
	require.Equal(t, "legacy-session", latest)
}

func TestForkExistsAndDeleteSession(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "before fork"}}}))

	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.True(t, ok)

	forked, err := store.Fork("source", "investigation")
	require.NoError(t, err)
	require.NotEqual(t, "source", forked.ID)
	require.Len(t, forked.Messages, 1)
	require.Equal(t, "before fork", forked.Messages[0].Content[0].Text)

	data, err := os.ReadFile(forked.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"type":"fork"`)
	require.Contains(t, string(data), `"parent_session_id":"source"`)
	require.Contains(t, string(data), `"branch_name":"investigation"`)

	require.NoError(t, store.Delete(forked.ID))
	ok, err = store.Exists(forked.ID)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPromptHistoryUsesInputRecords(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "first prompt"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "message fallback should be ignored")))
	require.NoError(t, store.AppendInput("source", "second prompt\nwith detail"))

	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, 1, entries[0].Index)
	require.Equal(t, "first prompt", entries[0].Text)
	require.Equal(t, 2, entries[1].Index)
	require.Equal(t, "second prompt\nwith detail", entries[1].Text)
	require.Equal(t, "source", entries[1].SessionID)

	data, err := os.ReadFile(filepath.Join(store.Dir, "source.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"type":"input"`)
	require.Contains(t, string(data), `"input":"first prompt"`)
}

func TestPromptHistoryFallsBackToUserMessages(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "ignored")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "legacy prompt")))

	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, 1, entries[0].Index)
	require.Equal(t, "legacy prompt", entries[0].Text)
}

func TestAppendWithUsageStoresProviderTokenUsage(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello")))
	usage := anthropic.Usage{InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 2}
	require.NoError(t, store.AppendWithUsage("source", anthropic.TextMessage("assistant", "answer"), &usage))

	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 2)
	require.Equal(t, "answer", opened.Messages[1].Content[0].Text)

	entries, err := store.Usage("source")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, 1, entries[0].MessageIndex)
	require.Equal(t, 10, entries[0].Usage.InputTokens)
	require.Equal(t, 4, entries[0].Usage.OutputTokens)
	require.Equal(t, 2, entries[0].Usage.CacheReadInputTokens)

	data, err := os.ReadFile(filepath.Join(store.Dir, "source.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2}`)
}

func TestRewindTruncatesMessagesAndTrailingInputs(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "first prompt"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "first answer")))
	require.NoError(t, store.AppendInput("source", "second prompt"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "second prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "second answer")))

	result, err := store.Rewind("source", 2)
	require.NoError(t, err)
	require.Equal(t, "source", result.SessionID)
	require.Equal(t, 4, result.OriginalMessages)
	require.Equal(t, 2, result.RemainingMessages)
	require.Equal(t, 2, result.RemovedMessages)

	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 2)
	require.Equal(t, "first answer", opened.Messages[1].Content[0].Text)
	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "first prompt", entries[0].Text)
}

func TestWorkspaceStoreRewindsLegacySession(t *testing.T) {
	configHome := t.TempDir()
	legacy := NewStore(configHome)
	require.NoError(t, legacy.Append("legacy-session", anthropic.TextMessage("user", "legacy prompt")))
	require.NoError(t, legacy.Append("legacy-session", anthropic.TextMessage("assistant", "legacy answer")))

	store := NewWorkspaceStore(configHome, t.TempDir())
	result, err := store.Rewind("legacy-session", 1)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(configHome, "sessions", "legacy-session.jsonl"), result.Path)

	opened, err := store.Open("legacy-session")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "legacy prompt", opened.Messages[0].Content[0].Text)
}

func TestReplaceMessagesRewritesSessionMessages(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "first prompt"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "first answer")))
	sess, err := store.Open("source")
	require.NoError(t, err)

	result, err := store.ReplaceMessages(sess, []anthropic.Message{anthropic.TextMessage("user", "compacted")})

	require.NoError(t, err)
	require.Equal(t, "source", result.SessionID)
	require.Equal(t, 2, result.OriginalMessages)
	require.Equal(t, 1, result.RemainingMessages)
	require.Equal(t, 1, result.RemovedMessages)
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "compacted", opened.Messages[0].Content[0].Text)
	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "compacted", entries[0].Text)
}

func TestAppendInputIgnoresBlankInput(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "  \n\t"))

	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestExportMarkdownJSONAndJSONL(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("export-session", anthropic.TextMessage("user", "Summarize this repo")))
	require.NoError(t, store.Append("export-session", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{
		{Type: "text", Text: "Summary"},
		{Type: "tool_use", ID: "tool-1", Name: "grep", Input: []byte(`{"pattern":"TODO"}`)},
	}}))

	markdown, sess, err := store.Export("export-session", "markdown")
	require.NoError(t, err)
	require.Equal(t, "export-session", sess.ID)
	require.Contains(t, string(markdown), "# Conversation Export")
	require.Contains(t, string(markdown), "- **Session**: `export-session`")
	require.Contains(t, string(markdown), "## 1. user")
	require.Contains(t, string(markdown), "Summarize this repo")
	require.Contains(t, string(markdown), "[tool_use id=tool-1 name=grep]")

	data, _, err := store.Export("export-session", "json")
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "export-session"`)
	require.Contains(t, string(data), `"Summary"`)

	raw, _, err := store.Export("export-session", "jsonl")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"session_id":"export-session"`)

	require.Equal(t, "summarize-this-repo.md", DefaultExportFilename(sess))
}
