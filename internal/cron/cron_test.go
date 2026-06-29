package cron

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreCreateListDeleteCronEntries(t *testing.T) {
	store := NewStore(t.TempDir())

	entry, err := store.Create("0 9 * * 1", "review weekly status", "weekly review")
	require.NoError(t, err)
	require.NotEmpty(t, entry.ID)
	require.True(t, entry.Enabled)
	require.Equal(t, "0 9 * * 1", entry.Schedule)
	require.Equal(t, "review weekly status", entry.Prompt)

	entries, err := store.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, entry.ID, entries[0].ID)

	deleted, err := store.Delete(entry.ID)
	require.NoError(t, err)
	require.Equal(t, entry.ID, deleted.ID)
	entries, err = store.List()
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStoreRejectsInvalidCronInput(t *testing.T) {
	store := NewStore(t.TempDir())

	_, err := store.Create("", "prompt", "")
	require.Error(t, err)
	_, err = store.Create("* * * * *", "", "")
	require.Error(t, err)
	_, err = store.Delete("../bad")
	require.Error(t, err)
}
