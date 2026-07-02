package session

import (
	"errors"
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

func TestOpenExistingDoesNotCreateAndReportsDirectoryPaths(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.OpenExisting("missing-session")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.NoFileExists(t, filepath.Join(store.Dir, "missing-session.jsonl"))

	directoryPath := filepath.Join(t.TempDir(), "session-dir")
	require.NoError(t, os.MkdirAll(directoryPath, 0o755))
	_, err = store.OpenExisting(directoryPath)
	require.Error(t, err)
	var directoryErr PathIsDirectoryError
	require.True(t, errors.As(err, &directoryErr))
	require.Equal(t, directoryPath, directoryErr.Path)

	fileStore := NewStore(t.TempDir())
	require.NoError(t, fileStore.Append("external", anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "from path"}}}))
	externalPath := filepath.Join(fileStore.Dir, "external.jsonl")
	opened, err := store.OpenExisting(externalPath)
	require.NoError(t, err)
	require.Equal(t, "external", opened.ID)
	require.Equal(t, externalPath, opened.Path)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "from path", opened.Messages[0].Content[0].Text)
}

func TestCreateEmptySession(t *testing.T) {
	store := NewStore(t.TempDir())

	created, err := store.Create("")
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Empty(t, created.Messages)
	require.FileExists(t, created.Path)

	ok, err := store.Exists(created.ID)
	require.NoError(t, err)
	require.True(t, ok)
	opened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, opened.ID)
	require.Empty(t, opened.Messages)
	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, created.ID, sessions[0].ID)
	_, err = store.Create(created.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func TestOpenCreatesSessionIdentityWithTypedPlaceholders(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewWorkspaceStore(configHome, workspace)
	canonical, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)

	created, err := store.Open("")
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, created.ID, created.Identity.Title)
	require.Equal(t, canonical, created.Identity.Workspace)
	require.Equal(t, canonical, created.Identity.Worktree)
	require.Empty(t, created.Identity.Purpose)
	require.Contains(t, created.Identity.Placeholders, IdentityPlaceholder{Field: "purpose", Reason: "purpose_not_provided"})

	data, err := os.ReadFile(created.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"type":"session"`)
	require.Contains(t, string(data), `"type":"session_identity"`)
	require.NotContains(t, string(data), "unknown")

	opened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Identity, opened.Identity)
}

func TestUpdateIdentityEnrichesTypedPlaceholders(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
	created, err := store.Open("identity-session")
	require.NoError(t, err)
	require.Contains(t, created.Identity.Placeholders, IdentityPlaceholder{Field: "purpose", Reason: "purpose_not_provided"})

	identity, err := store.UpdateIdentity(created.ID, SessionIdentity{Title: "Summarize repository", Purpose: "prompt"})
	require.NoError(t, err)
	require.Equal(t, "Summarize repository", identity.Title)
	require.Equal(t, "prompt", identity.Purpose)
	require.Empty(t, identity.Placeholders)

	opened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Equal(t, identity, opened.Identity)
	data, err := os.ReadFile(opened.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"purpose":"prompt"`)
	require.NotContains(t, string(data), "unknown")
}

func TestOpenReconcilesIdentityFromInputRecord(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())

	require.NoError(t, store.AppendInput("legacy-input", "Investigate flaky test\nwith scheduler logs"))
	opened, err := store.Open("legacy-input")
	require.NoError(t, err)

	require.Equal(t, "Investigate flaky test with scheduler logs", opened.Identity.Title)
	require.Equal(t, "Investigate flaky test with scheduler logs", opened.Identity.Purpose)
	require.Empty(t, opened.Identity.Placeholders)
}

func TestOpenReconcilesIdentityFromLegacyUserMessage(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())

	require.NoError(t, store.Append("legacy-message", anthropic.TextMessage("assistant", "ignored")))
	require.NoError(t, store.Append("legacy-message", anthropic.TextMessage("user", "Summarize this repository structure")))
	opened, err := store.Open("legacy-message")
	require.NoError(t, err)

	require.Equal(t, "Summarize this repository structure", opened.Identity.Title)
	require.Equal(t, "Summarize this repository structure", opened.Identity.Purpose)
	require.Empty(t, opened.Identity.Placeholders)
}

func TestOpenReconcilePreservesExplicitSessionIdentity(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
	created, err := store.CreateWithIdentity("explicit-identity", SessionIdentity{
		Title:   "Release checklist",
		Purpose: "manual",
	})
	require.NoError(t, err)
	require.NoError(t, store.AppendInput(created.ID, "Rewrite the title from prompt"))

	opened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Equal(t, "Release checklist", opened.Identity.Title)
	require.Equal(t, "manual", opened.Identity.Purpose)
	require.Empty(t, opened.Identity.Placeholders)
}

func TestReplaceMessagesPreservesSessionIdentity(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
	created, err := store.Open("identity-preserve")
	require.NoError(t, err)
	identity, err := store.UpdateIdentity(created.ID, SessionIdentity{Title: "Review auth flow", Purpose: "prompt"})
	require.NoError(t, err)
	require.NoError(t, store.Append(created.ID, anthropic.TextMessage("user", "review auth")))
	opened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)

	result, err := store.ReplaceMessages(opened, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.RemovedMessages)
	reopened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Empty(t, reopened.Messages)
	require.Equal(t, identity, reopened.Identity)
}

func TestRewindToZeroPreservesSessionIdentity(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
	created, err := store.Open("identity-rewind")
	require.NoError(t, err)
	identity, err := store.UpdateIdentity(created.ID, SessionIdentity{Title: "Investigate flaky test", Purpose: "prompt"})
	require.NoError(t, err)
	require.NoError(t, store.Append(created.ID, anthropic.TextMessage("user", "investigate flaky test")))

	result, err := store.Rewind(created.ID, 1)
	require.NoError(t, err)
	require.Equal(t, 0, result.RemainingMessages)
	reopened, err := store.Open(created.ID)
	require.NoError(t, err)
	require.Empty(t, reopened.Messages)
	require.Equal(t, identity, reopened.Identity)
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

func TestRenameSessionMovesJSONLAndUpdatesRecords(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "rename prompt"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "before rename")))

	result, err := store.Rename("source", "renamed")
	require.NoError(t, err)
	require.Equal(t, "source", result.OldID)
	require.Equal(t, "renamed", result.NewID)
	require.Equal(t, 1, result.MessageCount)
	require.NoFileExists(t, filepath.Join(store.Dir, "source.jsonl"))
	require.FileExists(t, filepath.Join(store.Dir, "renamed.jsonl"))

	opened, err := store.Open("renamed")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "before rename", opened.Messages[0].Content[0].Text)
	history, err := store.PromptHistory("renamed")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "renamed", history[0].SessionID)
	data, err := os.ReadFile(result.NewPath)
	require.NoError(t, err)
	require.Contains(t, string(data), `"session_id":"renamed"`)

	_, err = store.Rename("renamed", "../bad")
	require.Error(t, err)
	_, err = store.Rename("renamed", "renamed")
	require.Error(t, err)
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

func TestReplaceMessagesPreservesRetainedUsage(t *testing.T) {
	store := NewStore(t.TempDir())
	usage := anthropic.Usage{InputTokens: 12, OutputTokens: 5, CacheReadInputTokens: 2}
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first prompt")))
	require.NoError(t, store.AppendWithUsage("source", anthropic.TextMessage("assistant", "first answer"), &usage))
	sess, err := store.Open("source")
	require.NoError(t, err)

	_, err = store.ReplaceMessages(sess, []anthropic.Message{
		anthropic.TextMessage("user", "summary"),
		sess.Messages[1],
	})

	require.NoError(t, err)
	entries, err := store.Usage("source")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, 1, entries[0].MessageIndex)
	require.Equal(t, usage.InputTokens, entries[0].Usage.InputTokens)
	require.Equal(t, usage.OutputTokens, entries[0].Usage.OutputTokens)
	require.Equal(t, usage.CacheReadInputTokens, entries[0].Usage.CacheReadInputTokens)
}

func TestAppendInputIgnoresBlankInput(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendInput("source", "  \n\t"))

	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestPromptHistoryDisabledMarkerSuppressesUserMessageFallback(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.AppendPromptHistoryDisabled("source"))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "private prompt")))

	entries, err := store.PromptHistory("source")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestBackfillPromptHistory(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("user", "first legacy prompt")))
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("assistant", "answer")))
	require.NoError(t, store.Append("legacy", anthropic.TextMessage("user", "second legacy prompt")))
	require.NoError(t, store.AppendInput("current", "already recorded"))
	require.NoError(t, store.Append("current", anthropic.TextMessage("user", "current prompt")))
	require.NoError(t, store.AppendPromptHistoryDisabled("private"))
	require.NoError(t, store.Append("private", anthropic.TextMessage("user", "private prompt")))

	report, err := store.BackfillPromptHistory()
	require.NoError(t, err)
	require.Equal(t, "backfill_sessions", report.Kind)
	require.Equal(t, 3, report.SessionsScanned)
	require.Equal(t, 1, report.SessionsUpdated)
	require.Equal(t, 2, report.InputsAdded)
	require.Equal(t, 1, report.SkippedWithInputs)
	require.Equal(t, 1, report.SkippedDisabled)

	entries, err := store.PromptHistory("legacy")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "first legacy prompt", entries[0].Text)
	require.Equal(t, "second legacy prompt", entries[1].Text)

	report, err = store.BackfillPromptHistory()
	require.NoError(t, err)
	require.Equal(t, 0, report.SessionsUpdated)
	require.Equal(t, 0, report.InputsAdded)
	require.Equal(t, 2, report.SkippedWithInputs)
	require.Equal(t, 1, report.SkippedDisabled)
}

func TestExportMarkdownJSONJSONLAndHTML(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("export-session", anthropic.TextMessage("user", "Summarize <this> repo")))
	require.NoError(t, store.Append("export-session", anthropic.Message{Role: "assistant", Content: []anthropic.ContentBlock{
		{Type: "text", Text: "Summary <ok>"},
		{Type: "tool_use", ID: "tool-1", Name: "grep", Input: []byte(`{"pattern":"TODO"}`)},
	}}))

	markdown, sess, err := store.Export("export-session", "markdown")
	require.NoError(t, err)
	require.Equal(t, "export-session", sess.ID)
	require.Contains(t, string(markdown), "# Conversation Export")
	require.Contains(t, string(markdown), "- **Session**: `export-session`")
	require.Contains(t, string(markdown), "## 1. user")
	require.Contains(t, string(markdown), "Summarize <this> repo")
	require.Contains(t, string(markdown), "[tool_use id=tool-1 name=grep]")

	data, _, err := store.Export("export-session", "json")
	require.NoError(t, err)
	require.Contains(t, string(data), `"id": "export-session"`)
	require.Contains(t, string(data), "Summary")

	raw, _, err := store.Export("export-session", "jsonl")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"session_id":"export-session"`)

	html, _, err := store.Export("export-session", "html")
	require.NoError(t, err)
	require.Contains(t, string(html), "<!doctype html>")
	require.Contains(t, string(html), "Summarize &lt;this&gt; repo")
	require.Contains(t, string(html), "Summary &lt;ok&gt;")
	require.Contains(t, string(html), "[tool_use id=tool-1 name=grep]")

	require.Equal(t, "summarize-this-repo.md", DefaultExportFilename(sess))
	require.Equal(t, "summarize-this-repo.html", DefaultExportFilenameForFormat(sess, "html"))
}

func TestExportRequiresExistingSession(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("export-session", anthropic.TextMessage("user", "hello export")))

	data, sess, err := store.Export("missing-session", "markdown")
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.Nil(t, data)
	require.Nil(t, sess)
	_, statErr := os.Stat(filepath.Join(store.Dir, "missing-session.jsonl"))
	require.ErrorIs(t, statErr, os.ErrNotExist)

	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "export-session", sessions[0].ID)
}
