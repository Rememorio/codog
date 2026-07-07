package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestWorkspaceStoreUsesProjectRootForSessionNamespace(t *testing.T) {
	configHome := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	subdir := filepath.Join(root, "pkg", "service")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(subdir, 0o755))
	canonicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	rootStore := NewWorkspaceStore(configHome, root)
	subdirStore := NewWorkspaceStore(configHome, subdir)
	require.Equal(t, canonicalRoot, rootStore.Workspace)
	require.Equal(t, canonicalRoot, subdirStore.Workspace)
	require.Equal(t, rootStore.Dir, subdirStore.Dir)
	require.Equal(t, filepath.Join(configHome, "sessions", WorkspaceFingerprint(canonicalRoot)), subdirStore.Dir)

	require.NoError(t, rootStore.Append("shared-project-session", anthropic.TextMessage("user", "from root")))
	opened, err := subdirStore.OpenExisting("shared-project-session")
	require.NoError(t, err)
	require.Len(t, opened.Messages, 1)
	require.Equal(t, "from root", opened.Messages[0].Content[0].Text)
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

func TestWorkspaceStoreLookupErrorDescribesCurrentNamespace(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))

	storeA := NewWorkspaceStore(configHome, workspaceA)
	storeB := NewWorkspaceStore(configHome, workspaceB)
	require.NoError(t, storeB.Append("other", anthropic.TextMessage("user", "from b")))

	_, err := storeA.LatestID()
	require.ErrorIs(t, err, ErrNoSessions)
	var lookup LookupError
	require.ErrorAs(t, err, &lookup)
	require.Equal(t, storeA.Dir, lookup.SearchDir)
	require.Equal(t, storeA.Workspace, lookup.Workspace)
	require.Equal(t, WorkspaceFingerprint(storeA.Workspace), lookup.WorkspaceFingerprint)
	require.Equal(t, 1, lookup.OtherWorkspacePartitions)
	require.Equal(t, 1, lookup.OtherWorkspaceSessions)
	require.Contains(t, err.Error(), storeA.Dir)
	require.Contains(t, err.Error(), "other workspace partition")
}

func TestWorkspaceStoreMissingSessionLookupErrorKeepsReference(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewWorkspaceStore(configHome, workspace)

	_, err := store.OpenExisting("missing-session")
	require.ErrorIs(t, err, ErrSessionNotFound)
	var lookup LookupError
	require.ErrorAs(t, err, &lookup)
	require.Equal(t, "missing-session", lookup.Reference)
	require.Equal(t, store.Dir, lookup.SearchDir)
	require.Contains(t, err.Error(), "missing-session")
	require.Contains(t, err.Error(), store.Dir)
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

func TestWorkspaceStoreRejectsExplicitSessionFromOtherWorkspace(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	storeB := NewWorkspaceStore(configHome, workspaceB)
	canonicalA, err := filepath.EvalSymlinks(workspaceA)
	require.NoError(t, err)
	canonicalB, err := filepath.EvalSymlinks(workspaceB)
	require.NoError(t, err)

	path := filepath.Join(storeB.LegacyDir, "legacy-cross.jsonl")
	writeTestSessionIdentity(t, path, "legacy-cross", SessionIdentity{
		Title:     "legacy cross",
		Workspace: canonicalA,
		Worktree:  canonicalA,
		Purpose:   "test",
	})

	_, err = storeB.OpenExisting("legacy-cross")
	require.Error(t, err)
	var mismatch WorkspaceMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, canonicalB, mismatch.Expected)
	require.Equal(t, canonicalA, mismatch.Actual)
	require.Equal(t, path, mismatch.Path)
}

func TestLatestIDSkipsEmptySessions(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("real-session", anthropic.TextMessage("user", "hello")))
	_, err := store.Create("zz-empty-session")
	require.NoError(t, err)

	latest, err := store.LatestID()
	require.NoError(t, err)
	require.Equal(t, "real-session", latest)
}

func TestSessionReferenceAliasesResolveLatestSession(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "hello")))

	for _, alias := range []string{"latest", "last", "recent"} {
		require.True(t, IsSessionReferenceAlias(alias))
		opened, err := store.OpenExisting(alias)
		require.NoError(t, err)
		require.Equal(t, "source", opened.ID)
		exists, err := store.Exists(alias)
		require.NoError(t, err)
		require.True(t, exists)
	}
}

func TestCreateRejectsSessionReferenceAliases(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, alias := range []string{"latest", "last", "recent"} {
		_, err := store.Create(alias)
		require.Error(t, err)
		require.Contains(t, err.Error(), "session alias")
	}
}

func TestLatestIDPrefersSemanticUpdatedAtOverIDAndFileMtime(t *testing.T) {
	store := NewStore(t.TempDir())
	olderFileTime := time.Unix(100, 0).UTC()
	newerFileTime := time.Unix(200, 0).UTC()
	writeTestSessionRecord(t, store, "zz-older-session", time.Unix(20, 0).UTC(), newerFileTime)
	writeTestSessionRecord(t, store, "aa-newer-session", time.Unix(30, 0).UTC(), olderFileTime)

	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, "aa-newer-session", sessions[0].ID)

	latest, err := store.LatestID()
	require.NoError(t, err)
	require.Equal(t, "aa-newer-session", latest)
}

func TestLatestIDExcludingSkipsExcludedSession(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("older-session", anthropic.TextMessage("user", "older")))
	require.NoError(t, store.Append("zz-current-session", anthropic.TextMessage("user", "current")))

	latest, err := store.LatestIDExcluding("zz-current-session")
	require.NoError(t, err)
	require.Equal(t, "older-session", latest)
}

func TestLatestSessionFallsBackToSiblingWorkspace(t *testing.T) {
	configHome := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "repo-a")
	workspaceB := filepath.Join(t.TempDir(), "repo-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	storeA := NewWorkspaceStore(configHome, workspaceA)
	storeB := NewWorkspaceStore(configHome, workspaceB)
	_, err := storeB.CreateWithIdentity("remote-session", SessionIdentity{Purpose: "test"})
	require.NoError(t, err)
	require.NoError(t, storeB.Append("remote-session", anthropic.TextMessage("user", "from another workspace")))

	latest, err := storeA.LatestSessionExcluding("")
	require.NoError(t, err)
	require.Equal(t, "remote-session", latest.ID)
	require.Equal(t, filepath.Join(storeB.Dir, "remote-session.jsonl"), latest.Path)

	opened, err := storeA.OpenExisting("latest")
	require.NoError(t, err)
	require.Equal(t, "remote-session", opened.ID)
	require.Equal(t, latest.Path, opened.Path)
	require.Len(t, opened.Messages, 1)
}

func TestLatestAnyIDIncludesEmptySessions(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("real-session", anthropic.TextMessage("user", "hello")))
	_, err := store.Create("zz-empty-session")
	require.NoError(t, err)

	latest, err := store.LatestAnyID()
	require.NoError(t, err)
	require.Equal(t, "zz-empty-session", latest)
}

func TestLatestIDReportsAllSessionsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Create("empty-a")
	require.NoError(t, err)
	_, err = store.Create("empty-b")
	require.NoError(t, err)

	_, err = store.LatestID()
	require.ErrorIs(t, err, ErrAllSessionsEmpty)
}

func writeTestSessionRecord(t *testing.T, store *Store, id string, recordTime time.Time, fileTime time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(store.Dir, 0o755))
	path := store.pathFor(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	writeErr := writeRecord(file, Record{
		Type:      "message",
		Time:      recordTime,
		Message:   &anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: id}}},
		SessionID: id,
	})
	closeErr := file.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
	require.NoError(t, os.Chtimes(path, fileTime, fileTime))
}

func writeTestSessionIdentity(t *testing.T, path string, id string, identity SessionIdentity) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, writeRecord(file, Record{Type: "session", Time: now, SessionID: id}))
	require.NoError(t, writeRecord(file, Record{Type: "session_identity", Time: now, SessionID: id, Identity: &identity}))
	require.NoError(t, writeRecord(file, Record{
		Type:      "message",
		Time:      now,
		Message:   &anthropic.Message{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "hello"}}},
		SessionID: id,
	}))
	require.NoError(t, file.Close())
}

func TestWorkspaceStoreCleanupRemovesExpiredCurrentAndLegacySessions(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewWorkspaceStore(configHome, workspace)
	legacy := NewStore(configHome)

	require.NoError(t, store.Append("old-current", anthropic.TextMessage("user", "old current")))
	require.NoError(t, store.Append("new-current", anthropic.TextMessage("user", "new current")))
	require.NoError(t, legacy.Append("old-legacy", anthropic.TextMessage("user", "old legacy")))

	oldTime := time.Now().Add(-45 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(store.Dir, "old-current.jsonl"), oldTime, oldTime))
	require.NoError(t, os.Chtimes(filepath.Join(legacy.Dir, "old-legacy.jsonl"), oldTime, oldTime))

	cleaned, err := NewWorkspaceStoreWithCleanup(configHome, workspace, 30)
	require.NoError(t, err)
	require.False(t, cleaned.PersistenceDisabled)
	require.NoFileExists(t, filepath.Join(store.Dir, "old-current.jsonl"))
	require.NoFileExists(t, filepath.Join(legacy.Dir, "old-legacy.jsonl"))
	require.FileExists(t, filepath.Join(store.Dir, "new-current.jsonl"))
}

func TestWorkspaceStoreCleanupZeroDisablesPersistenceAndRemovesSessions(t *testing.T) {
	configHome := t.TempDir()
	workspace := t.TempDir()
	store := NewWorkspaceStore(configHome, workspace)
	require.NoError(t, store.Append("kept-before-disable", anthropic.TextMessage("user", "private")))
	require.FileExists(t, filepath.Join(store.Dir, "kept-before-disable.jsonl"))

	disabled, err := NewWorkspaceStoreWithCleanup(configHome, workspace, 0)
	require.NoError(t, err)
	require.True(t, disabled.PersistenceDisabled)
	require.NoFileExists(t, filepath.Join(store.Dir, "kept-before-disable.jsonl"))

	sess, err := disabled.Open("")
	require.NoError(t, err)
	require.NotEmpty(t, sess.ID)
	require.Empty(t, sess.Path)
	require.NoError(t, disabled.Append(sess.ID, anthropic.TextMessage("user", "not persisted")))
	require.NoFileExists(t, filepath.Join(disabled.Dir, sess.ID+".jsonl"))
	sessions, err := disabled.List()
	require.NoError(t, err)
	require.Empty(t, sessions)
	_, err = disabled.LatestID()
	require.ErrorIs(t, err, ErrNoSessions)
}

func TestStoreUpdateIdentityRespectsPersistenceDisabled(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
	store.PersistenceDisabled = true

	identity, err := store.UpdateIdentity("ephemeral-session", SessionIdentity{
		Title:   "Ephemeral prompt",
		Purpose: "prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "Ephemeral prompt", identity.Title)
	require.Equal(t, "prompt", identity.Purpose)
	require.Equal(t, store.Workspace, identity.Workspace)
	require.Empty(t, identity.Placeholders)
	require.NoFileExists(t, filepath.Join(store.Dir, "ephemeral-session.jsonl"))

	_, err = store.UpdateIdentity("latest", SessionIdentity{Title: "alias"})
	require.ErrorIs(t, err, ErrNoSessions)
}

func TestStorePruneDryRunAndConfirmEmptySessions(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Create("empty-session")
	require.NoError(t, err)
	require.NoError(t, store.Append("kept-session", anthropic.TextMessage("user", "keep me")))

	dryRun, err := store.Prune(PruneOptions{EmptyOnly: true})
	require.NoError(t, err)
	require.Equal(t, "session_prune", dryRun.Kind)
	require.Equal(t, "dry_run", dryRun.Status)
	require.True(t, dryRun.DryRun)
	require.Equal(t, 2, dryRun.Scanned)
	require.Equal(t, 1, dryRun.CandidateCount)
	require.Equal(t, "empty-session", dryRun.Candidates[0].ID)
	require.Equal(t, "empty", dryRun.Candidates[0].Reason)
	require.FileExists(t, filepath.Join(store.Dir, "empty-session.jsonl"))
	require.FileExists(t, filepath.Join(store.Dir, "kept-session.jsonl"))

	confirmed, err := store.Prune(PruneOptions{EmptyOnly: true, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, "ok", confirmed.Status)
	require.False(t, confirmed.DryRun)
	require.Equal(t, 1, confirmed.DeletedCount)
	require.Equal(t, "empty-session", confirmed.Deleted[0].ID)
	require.NoFileExists(t, filepath.Join(store.Dir, "empty-session.jsonl"))
	require.FileExists(t, filepath.Join(store.Dir, "kept-session.jsonl"))
}

func TestStorePruneKeepsNewestSessions(t *testing.T) {
	store := NewStore(t.TempDir())
	writeTestSessionRecord(t, store, "older", time.Unix(10, 0).UTC(), time.Unix(10, 0).UTC())
	writeTestSessionRecord(t, store, "middle", time.Unix(20, 0).UTC(), time.Unix(20, 0).UTC())
	writeTestSessionRecord(t, store, "newest", time.Unix(30, 0).UTC(), time.Unix(30, 0).UTC())

	report, err := store.Prune(PruneOptions{Keep: 1, Confirm: true})
	require.NoError(t, err)
	require.Equal(t, 3, report.Scanned)
	require.Equal(t, 2, report.CandidateCount)
	require.Equal(t, 2, report.DeletedCount)
	require.Equal(t, "middle", report.Deleted[0].ID)
	require.Equal(t, "older", report.Deleted[1].ID)
	require.FileExists(t, filepath.Join(store.Dir, "newest.jsonl"))
	require.NoFileExists(t, filepath.Join(store.Dir, "middle.jsonl"))
	require.NoFileExists(t, filepath.Join(store.Dir, "older.jsonl"))
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

func TestOpenExistingResolvesLegacyJSONSession(t *testing.T) {
	store := NewStore(t.TempDir())
	path := filepath.Join(store.Dir, "legacy-json.json")
	writeTestSessionIdentity(t, path, "legacy-json", SessionIdentity{Title: "Legacy JSON", Purpose: "test"})

	ok, err := store.Exists("legacy-json")
	require.NoError(t, err)
	require.True(t, ok)
	opened, err := store.OpenExisting("legacy-json")
	require.NoError(t, err)
	require.Equal(t, "legacy-json", opened.ID)
	require.Equal(t, path, opened.Path)
	require.Len(t, opened.Messages, 1)

	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "legacy-json", sessions[0].ID)
	require.Equal(t, path, sessions[0].Path)
}

func TestSessionResolutionPrefersJSONLOverLegacyJSON(t *testing.T) {
	store := NewStore(t.TempDir())
	legacyPath := filepath.Join(store.Dir, "dual.json")
	primaryPath := filepath.Join(store.Dir, "dual.jsonl")
	writeTestSessionIdentity(t, legacyPath, "dual", SessionIdentity{Title: "Legacy", Purpose: "test"})
	writeTestSessionIdentity(t, primaryPath, "dual", SessionIdentity{Title: "Primary", Purpose: "test"})

	opened, err := store.OpenExisting("dual")
	require.NoError(t, err)
	require.Equal(t, primaryPath, opened.Path)
	require.Equal(t, "Primary", opened.Identity.Title)
	sessions, err := store.List()
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, primaryPath, sessions[0].Path)
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
	_, err := store.PinMessage("source", 0)
	require.NoError(t, err)

	ok, err := store.Exists("source")
	require.NoError(t, err)
	require.True(t, ok)

	forked, err := store.Fork("source", "investigation")
	require.NoError(t, err)
	require.NotEqual(t, "source", forked.ID)
	require.Len(t, forked.Messages, 1)
	require.Equal(t, "before fork", forked.Messages[0].Content[0].Text)
	require.Equal(t, "source", forked.Metadata.ParentSessionID)
	require.Equal(t, "investigation", forked.Metadata.BranchName)
	require.Equal(t, []int{0}, forked.Metadata.PinnedMessages)
	require.False(t, forked.Metadata.CreatedAt.IsZero())
	require.False(t, forked.Metadata.UpdatedAt.IsZero())

	data, err := os.ReadFile(forked.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"type":"fork"`)
	require.Contains(t, string(data), `"parent_session_id":"source"`)
	require.Contains(t, string(data), `"branch_name":"investigation"`)

	reopened, err := store.OpenExisting(forked.ID)
	require.NoError(t, err)
	require.Equal(t, "source", reopened.Metadata.ParentSessionID)
	require.Equal(t, "investigation", reopened.Metadata.BranchName)
	require.Equal(t, []int{0}, reopened.Metadata.PinnedMessages)
	require.False(t, reopened.Metadata.CreatedAt.IsZero())
	require.False(t, reopened.Metadata.UpdatedAt.IsZero())
	require.False(t, reopened.Metadata.ModifiedAt.IsZero())

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

func TestPinMessagePersistsAndUnpins(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "first")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "second")))

	result, err := store.PinMessage("source", 1)
	require.NoError(t, err)
	require.Equal(t, "pin", result.Action)
	require.Equal(t, 1, result.MessageIndex)
	require.Equal(t, []int{1}, result.PinnedMessages)
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Equal(t, []int{1}, opened.Metadata.PinnedMessages)

	result, err = store.UnpinMessage("source", 1)
	require.NoError(t, err)
	require.Equal(t, "unpin", result.Action)
	require.Empty(t, result.PinnedMessages)
	opened, err = store.Open("source")
	require.NoError(t, err)
	require.Empty(t, opened.Metadata.PinnedMessages)
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

func TestReplaceAndRewindRemapPinnedMessages(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "one")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "two")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("user", "three")))
	require.NoError(t, store.Append("source", anthropic.TextMessage("assistant", "four")))
	_, err := store.PinMessage("source", 1)
	require.NoError(t, err)
	_, err = store.PinMessage("source", 3)
	require.NoError(t, err)

	sess, err := store.Open("source")
	require.NoError(t, err)
	_, err = store.ReplaceMessages(sess, []anthropic.Message{sess.Messages[1], sess.Messages[3]})
	require.NoError(t, err)
	opened, err := store.Open("source")
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, opened.Metadata.PinnedMessages)

	_, err = store.Rewind("source", 1)
	require.NoError(t, err)
	opened, err = store.Open("source")
	require.NoError(t, err)
	require.Equal(t, []int{0}, opened.Metadata.PinnedMessages)
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
	store := NewWorkspaceStore(t.TempDir(), t.TempDir())
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
	require.Equal(t, 1, report.IdentityUpdates)
	require.Equal(t, 1, report.SkippedWithInputs)
	require.Equal(t, 1, report.SkippedDisabled)
	require.Len(t, report.BackfilledSessions, 1)
	require.True(t, report.BackfilledSessions[0].IdentityUpdated)

	entries, err := store.PromptHistory("legacy")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "first legacy prompt", entries[0].Text)
	require.Equal(t, "second legacy prompt", entries[1].Text)
	legacy, err := store.Open("legacy")
	require.NoError(t, err)
	require.Equal(t, "first legacy prompt", legacy.Identity.Title)
	require.Equal(t, "first legacy prompt", legacy.Identity.Purpose)
	require.Empty(t, legacy.Identity.Placeholders)
	data, err := os.ReadFile(legacy.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"type":"session_identity"`)
	require.Contains(t, string(data), `"purpose":"first legacy prompt"`)

	report, err = store.BackfillPromptHistory()
	require.NoError(t, err)
	require.Equal(t, 0, report.SessionsUpdated)
	require.Equal(t, 0, report.InputsAdded)
	require.Equal(t, 0, report.IdentityUpdates)
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

func TestImportJSONLAndExportedJSONSessions(t *testing.T) {
	sourceHome := t.TempDir()
	sourceWorkspace := filepath.Join(t.TempDir(), "source")
	targetHome := t.TempDir()
	targetWorkspace := filepath.Join(t.TempDir(), "target")
	source := NewWorkspaceStore(sourceHome, sourceWorkspace)
	target := NewWorkspaceStore(targetHome, targetWorkspace)
	created, err := source.CreateWithIdentity("external", SessionIdentity{
		Title:     "External session",
		Workspace: sourceWorkspace,
		Purpose:   "port this work",
	})
	require.NoError(t, err)
	require.NoError(t, source.Append(created.ID, anthropic.TextMessage("user", "import this session")))

	result, err := target.Import(created.Path, ImportOptions{ID: "ported"})
	require.NoError(t, err)
	require.Equal(t, "external", result.OriginalSessionID)
	require.Equal(t, "ported", result.SessionID)
	require.Equal(t, 1, result.MessageCount)
	require.False(t, result.Overwritten)
	require.Equal(t, canonicalWorkspace(targetWorkspace), result.Identity.Workspace)
	imported, err := target.OpenExisting("ported")
	require.NoError(t, err)
	require.Equal(t, "ported", imported.ID)
	require.Equal(t, "External session", imported.Identity.Title)
	require.Equal(t, "port this work", imported.Identity.Purpose)
	require.Equal(t, canonicalWorkspace(targetWorkspace), imported.Identity.Workspace)
	require.Equal(t, "import this session", imported.Messages[0].Content[0].Text)
	data, err := os.ReadFile(imported.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"session_id":"ported"`)
	require.NotContains(t, string(data), canonicalWorkspace(sourceWorkspace))

	_, err = target.Import(created.Path, ImportOptions{ID: "ported"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
	overwritten, err := target.Import(created.Path, ImportOptions{ID: "ported", Overwrite: true})
	require.NoError(t, err)
	require.True(t, overwritten.Overwritten)

	jsonData, _, err := source.Export("external", "json")
	require.NoError(t, err)
	jsonPath := filepath.Join(t.TempDir(), "external.json")
	require.NoError(t, os.WriteFile(jsonPath, jsonData, 0o644))
	jsonImport, err := target.Import(jsonPath, ImportOptions{ID: "from-json"})
	require.NoError(t, err)
	require.Equal(t, "external", jsonImport.OriginalSessionID)
	require.Equal(t, "from-json", jsonImport.SessionID)
	require.Equal(t, 1, jsonImport.MessageCount)
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
