package bookmarks

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreAddListGetDeleteAndClear(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	nextID := 0
	store := Store{
		ConfigHome: t.TempDir(),
		Now:        func() time.Time { return now.Add(time.Duration(nextID) * time.Minute) },
		NewID: func() (string, error) {
			nextID++
			return fmt.Sprintf("bm-test-%d", nextID), nil
		},
	}

	messageIndex := 2
	first, err := store.Add(Bookmark{Name: "start", Workspace: "/repo/a", SessionID: "session-a", MessageIndex: &messageIndex, Note: "entry point"})
	require.NoError(t, err)
	require.Equal(t, "bm-test-1", first.ID)
	require.Equal(t, "start", first.Name)
	require.Equal(t, "session-a", first.SessionID)
	require.NotNil(t, first.MessageIndex)
	require.Equal(t, 2, *first.MessageIndex)
	require.False(t, first.CreatedAt.IsZero())
	require.FileExists(t, filepath.Join(store.ConfigHome, "bookmarks.json"))

	_, err = store.Add(Bookmark{Name: "other", Workspace: "/repo/b", SessionID: "session-b"})
	require.NoError(t, err)

	listed, err := store.List(ListOptions{Workspace: "/repo/a"})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "start", listed[0].Name)

	all, err := store.List(ListOptions{Workspace: "/repo/a", All: true})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "other", all[0].Name)

	found, err := store.Get("start")
	require.NoError(t, err)
	require.Equal(t, first.ID, found.ID)

	deleted, err := store.Delete(first.ID)
	require.NoError(t, err)
	require.Equal(t, "start", deleted.Name)

	removed, err := store.Clear(ListOptions{Workspace: "/repo/b"})
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	remaining, err := store.List(ListOptions{All: true})
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestStoreRejectsInvalidBookmarks(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Add(Bookmark{Name: "   "})
	require.ErrorContains(t, err, "bookmark name is required")
	_, err = store.Get("")
	require.ErrorContains(t, err, "bookmark id or name is required")
	_, err = store.Delete("missing")
	require.ErrorContains(t, err, "bookmark not found")
}
